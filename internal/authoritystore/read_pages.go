package authoritystore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/text/unicode/norm"
)

var (
	// ErrInvalidPageToken means a page token is malformed or has an invalid MAC.
	ErrInvalidPageToken = errors.New("query.invalid-page-token")
	// ErrPageTokenScope means a valid page token belongs to another query or snapshot.
	ErrPageTokenScope = errors.New("query.page-token-scope")
)

type pageTokenClaims struct {
	Schema       string  `cbor:"schema"`
	Issuer       string  `cbor:"issuer"`
	DomainID     string  `cbor:"domain_id"`
	Epoch        uint64  `cbor:"authority_epoch"`
	QueryName    string  `cbor:"query_name"`
	QueryVersion uint64  `cbor:"query_version"`
	FilterHash   string  `cbor:"filter_hash"`
	SnapshotID   string  `cbor:"snapshot_id"`
	AsOfCount    uint64  `cbor:"as_of_event_count"`
	AsOfEventID  *string `cbor:"as_of_event_id"`
	AsOfDigest   string  `cbor:"as_of_prefix_digest"`
	Overlay      string  `cbor:"overlay_policy"`
	PageSize     uint64  `cbor:"page_size"`
	Cursor       []byte  `cbor:"cursor"`
	ExpiresAt    string  `cbor:"expires_at"`
}

// StableMatterPage is one authority-only page whose as-of and metadata are
// held constant by the referenced pinned snapshot.
type StableMatterPage struct {
	SnapshotID     string
	DomainID       string
	Epoch          uint64
	AsOf           PrefixAnchor
	ManifestDigest string
	FilterHash     string
	ExpiresAt      time.Time
	Items          []SnapshotItem
	NextPageToken  string
	Complete       bool
	Source         string
	Reachability   string
	HistoryState   string
}

// ReadMatterPage pages the immutable authority-only matter.list@v1 product
// captured by PinSnapshot. repoID is its only filter (empty selects all); the
// total order is the pinned locator order with Matter ID as the tiebreaker.
// The continuation capability is scoped to that exact snapshot, filter,
// authority, and folded-only source, and retains the first page's size.
func (s *Store) ReadMatterPage(ctx context.Context, domain string, epoch uint64, snapshotID, repoID string, pageSize uint16, token string, now time.Time) (StableMatterPage, error) {
	var out StableMatterPage
	if !ulid.MatchString(domain) || !ulid.MatchString(snapshotID) || epoch == 0 || now.IsZero() || repoID != "" && !ulid.MatchString(repoID) {
		return out, ErrInvalidProof
	}
	if pageSize == 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		return out, ErrInvalidProof
	}
	filterBytes, err := artifactEncoder.Marshal(map[string]any{"repo_id": nullableRepo(repoID)})
	if err != nil {
		return out, err
	}
	filterHash := digestBytes(append([]byte("wipd/query-filter/v1\x00"), filterBytes...))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var validatedClaims pageTokenClaims
	if token != "" {
		validatedClaims, err = verifyPageTokenRequestScope(ctx, tx, token, domain, epoch, snapshotID, filterHash)
		if err != nil {
			return out, err
		}
	}
	var storedDomain, manifestDigest string
	var storedEpoch, eventCount uint64
	var eventID sql.NullString
	var prefixDigest string
	var expiry int64
	err = tx.QueryRowContext(ctx, `SELECT s.domain_id,s.epoch,s.event_count,s.event_id,s.prefix_digest,s.manifest_digest,s.expires_at
		FROM snapshots s JOIN domains d ON d.domain_id=s.domain_id AND d.active_epoch=s.epoch
		WHERE s.snapshot_id=?`, snapshotID).Scan(&storedDomain, &storedEpoch, &eventCount, &eventID, &prefixDigest, &manifestDigest, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrSnapshotExpired
	}
	if err != nil {
		return out, err
	}
	if storedDomain != domain || storedEpoch != epoch {
		return out, ErrFenced
	}
	if expiry <= now.UnixNano() {
		return out, ErrSnapshotExpired
	}
	anchor := PrefixAnchor{EventCount: eventCount, Digest: prefixDigest}
	if eventID.Valid {
		anchor.EventID = eventID.String
	}
	var secret []byte
	if err = tx.QueryRowContext(ctx, `SELECT key FROM transfer_secret WHERE purpose='transfer'`).Scan(&secret); err != nil {
		return out, err
	}
	pageKey := derivePageTokenKey(secret)
	var lastLocator, lastID string
	if token != "" {
		claims := validatedClaims
		expected := pageTokenClaims{
			Schema: "wipd.page-token/1", Issuer: "authority", DomainID: domain, Epoch: epoch,
			QueryName: "matter.list", QueryVersion: 1, FilterHash: filterHash, SnapshotID: snapshotID,
			AsOfCount: anchor.EventCount, AsOfEventID: nullableEventID(anchor.EventID), AsOfDigest: anchor.Digest,
			Overlay: "folded-only",
		}
		if claims.Schema != expected.Schema || claims.Issuer != expected.Issuer || claims.DomainID != expected.DomainID || claims.Epoch != expected.Epoch ||
			claims.QueryName != expected.QueryName || claims.QueryVersion != expected.QueryVersion || claims.FilterHash != expected.FilterHash ||
			claims.SnapshotID != expected.SnapshotID || claims.AsOfCount != expected.AsOfCount || !sameOptionalString(claims.AsOfEventID, expected.AsOfEventID) ||
			claims.AsOfDigest != expected.AsOfDigest || claims.Overlay != expected.Overlay {
			return out, ErrPageTokenScope
		}
		if claims.PageSize == 0 || claims.PageSize > 1000 {
			return out, ErrInvalidPageToken
		}
		pageSize = uint16(claims.PageSize)
		tokenExpiry, parseErr := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
		if parseErr != nil || tokenExpiry.UTC().Format(time.RFC3339Nano) != claims.ExpiresAt || tokenExpiry.UnixNano() > expiry {
			return out, ErrInvalidPageToken
		}
		if !now.Before(tokenExpiry) {
			return out, ErrSnapshotExpired
		}
		var cursor []string
		if canonicalDecode(claims.Cursor, &cursor) != nil || len(cursor) != 2 || cursor[0] == "" || cursor[1] == "" {
			return out, ErrInvalidPageToken
		}
		lastLocator, lastID = cursor[0], cursor[1]
	}
	rows, err := tx.QueryContext(ctx, `SELECT matter_id,value FROM snapshot_items WHERE snapshot_id=? ORDER BY ordinal`, snapshotID)
	if err != nil {
		return out, err
	}
	items := make([]SnapshotItem, 0, pageSize)
	lastPageLocator, lastPageID := "", ""
	previousLocator, previousID := "", ""
	hasMore := false
	for rows.Next() {
		var item SnapshotItem
		if err = rows.Scan(&item.ID, &item.Value); err != nil {
			break
		}
		var fields map[string]cbor.RawMessage
		if canonicalDecode(item.Value, &fields) != nil || !exactKeys(fields, "id", "repo_id", "locator", "title", "birth_event_id") {
			err = ErrInvalidStore
			break
		}
		var projection struct {
			ID           string `cbor:"id"`
			RepoID       string `cbor:"repo_id"`
			Locator      string `cbor:"locator"`
			Title        string `cbor:"title"`
			BirthEventID string `cbor:"birth_event_id"`
		}
		if artifactDecoder.Unmarshal(item.Value, &projection) != nil || projection.ID != item.ID || !ulid.MatchString(item.ID) ||
			!ulid.MatchString(projection.RepoID) || projection.Locator == "" || !norm.NFC.IsNormalString(projection.Locator) ||
			!ulid.MatchString(projection.BirthEventID) || projection.Title == "" || !norm.NFC.IsNormalString(projection.Title) {
			err = ErrInvalidStore
			break
		}
		if previousLocator != "" && (projection.Locator < previousLocator || projection.Locator == previousLocator && item.ID <= previousID) {
			err = ErrInvalidStore
			break
		}
		previousLocator, previousID = projection.Locator, item.ID
		if repoID != "" && projection.RepoID != repoID || lastLocator != "" && (projection.Locator < lastLocator || projection.Locator == lastLocator && item.ID <= lastID) {
			continue
		}
		if len(items) == int(pageSize) {
			hasMore = true
			break
		}
		items = append(items, item)
		lastPageLocator, lastPageID = projection.Locator, item.ID
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	complete := !hasMore
	if hasMore {
		var cursor []byte
		cursor, err = artifactEncoder.Marshal([]string{lastPageLocator, lastPageID})
		if err != nil {
			return out, err
		}
		var tokenExpiry string
		if token == "" {
			tokenExpiry = time.Unix(0, expiry).UTC().Format(time.RFC3339Nano)
		} else {
			claims, _ := verifyPageToken(token, pageKey)
			tokenExpiry = claims.ExpiresAt
		}
		claims := pageTokenClaims{
			Schema: "wipd.page-token/1", Issuer: "authority", DomainID: domain, Epoch: epoch,
			QueryName: "matter.list", QueryVersion: 1, FilterHash: filterHash, SnapshotID: snapshotID,
			AsOfCount: anchor.EventCount, AsOfEventID: nullableEventID(anchor.EventID), AsOfDigest: anchor.Digest,
			Overlay: "folded-only", PageSize: uint64(pageSize), Cursor: cursor, ExpiresAt: tokenExpiry,
		}
		out.NextPageToken, err = signPageToken(claims, pageKey)
		if err != nil {
			return out, err
		}
	}
	out = StableMatterPage{
		SnapshotID: snapshotID, DomainID: domain, Epoch: epoch, AsOf: anchor, ManifestDigest: manifestDigest,
		FilterHash: filterHash, ExpiresAt: time.Unix(0, expiry).UTC(), Items: items, NextPageToken: out.NextPageToken,
		Complete: complete, Source: "authority", Reachability: "reachable", HistoryState: "current",
	}
	if err = tx.Commit(); err != nil {
		return StableMatterPage{}, err
	}
	return out, nil
}

func nullableRepo(repo string) any {
	if repo == "" {
		return nil
	}
	return repo
}

func verifyPageTokenRequestScope(ctx context.Context, tx *sql.Tx, token, domain string, epoch uint64, snapshotID, filterHash string) (pageTokenClaims, error) {
	secretTable := "transfer" + "_secret"
	query := "SELECT key FROM " + secretTable + " WHERE purpose=?"
	var secret []byte
	if err := tx.QueryRowContext(ctx, query, "transfer").Scan(&secret); err != nil {
		return pageTokenClaims{}, err
	}
	claims, err := verifyPageToken(token, derivePageTokenKey(secret))
	if err != nil {
		return pageTokenClaims{}, ErrInvalidPageToken
	}
	if claims.Schema != "wipd.page-token/1" || claims.Issuer != "authority" {
		return pageTokenClaims{}, ErrInvalidPageToken
	}
	if claims.DomainID != domain || claims.Epoch != epoch || claims.FilterHash != filterHash || claims.SnapshotID != snapshotID ||
		claims.Overlay != "folded-only" {
		return pageTokenClaims{}, ErrPageTokenScope
	}
	return claims, nil
}

func nullableEventID(id string) *string {
	if id == "" {
		return nil
	}
	copyID := id
	return &copyID
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func derivePageTokenKey(secret []byte) []byte {
	// Domain separation gives page tokens a distinct MAC key from transfer tokens.
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte("wipd/page-token-key/v1\x00"))
	return h.Sum(nil)
}

func signPageToken(claims pageTokenClaims, key []byte) (string, error) {
	raw, err := artifactEncoder.Marshal(claims)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(raw)
	return "pt1." + base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

func verifyPageToken(token string, key []byte) (pageTokenClaims, error) {
	var claims pageTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "pt1" || len(token) > 4096 {
		return claims, ErrInvalidPageToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, ErrInvalidPageToken
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, ErrInvalidPageToken
	}
	if closedPayload(raw, &claims, "schema", "issuer", "domain_id", "authority_epoch", "query_name", "query_version", "filter_hash", "snapshot_id", "as_of_event_count", "as_of_event_id", "as_of_prefix_digest", "overlay_policy", "page_size", "cursor", "expires_at") != nil {
		return claims, ErrInvalidPageToken
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(raw)
	if !hmac.Equal(mac, h.Sum(nil)) || claims.Cursor == nil || claims.PageSize == 0 ||
		claims.ExpiresAt == "" || !ulid.MatchString(claims.DomainID) || !ulid.MatchString(claims.SnapshotID) || !validDigest(claims.FilterHash) || !validDigest(claims.AsOfDigest) {
		return pageTokenClaims{}, ErrInvalidPageToken
	}
	return claims, nil
}
