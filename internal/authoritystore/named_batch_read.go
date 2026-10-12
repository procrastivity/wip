package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNamedBatchNotFound intentionally covers anonymous, unknown, and
// out-of-domain Batch IDs so callers cannot distinguish those cases.
var ErrNamedBatchNotFound = errors.New("batch.named-not-found")

// NamedBatchPage is one authority-only page from a pinned batch.read@v1 view.
type NamedBatchPage struct {
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

// ReadNamedBatch executes batch.read@v1. A null continuation acquires one
// authority snapshot; later pages recover that exact snapshot exclusively
// from the authenticated continuation token.
func (s *Store) ReadNamedBatch(ctx context.Context, domain string, epoch uint64, batchID string, pageSize uint16, token string, now time.Time) (NamedBatchPage, error) {
	if !ulid.MatchString(domain) || !ulid.MatchString(batchID) || epoch == 0 || now.IsZero() {
		return NamedBatchPage{}, ErrInvalidProof
	}
	snapshotID, err := s.namedBatchReadSnapshotID(ctx, domain, epoch, batchID, token)
	if err != nil {
		return NamedBatchPage{}, err
	}
	if token == "" {
		id, idErr := makeULID(now)
		if idErr != nil {
			return NamedBatchPage{}, idErr
		}
		snapshotID = id
		if _, err = s.PinSnapshot(ctx, domain, epoch, emptyAnchor(), snapshotID, now, 5*time.Minute); err != nil {
			return NamedBatchPage{}, err
		}
	}
	return s.ReadNamedBatchPage(ctx, domain, epoch, snapshotID, batchID, pageSize, token, now)
}

func (s *Store) namedBatchReadSnapshotID(ctx context.Context, domain string, epoch uint64, batchID, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", ErrInvalidStore
	}
	secret, err := transferSecret(ctx, s.db)
	if err != nil {
		return "", err
	}
	claims, err := verifyPageToken(token, derivePageTokenKey(secret))
	if err != nil {
		return "", ErrInvalidPageToken
	}
	filterBytes, err := artifactEncoder.Marshal(map[string]any{"batch_id": batchID})
	if err != nil {
		return "", err
	}
	filterHash := digestBytes(append([]byte("wipd/query-filter/v1\x00"), filterBytes...))
	if claims.QueryName != "batch.read" || claims.QueryVersion != 1 || claims.DomainID != domain || claims.Epoch != epoch || claims.FilterHash != filterHash {
		return "", ErrPageTokenScope
	}
	return claims.SnapshotID, nil
}

type namedBatchReadHeader struct {
	Kind             string  `cbor:"kind"`
	BatchID          string  `cbor:"batch_id"`
	Name             string  `cbor:"name"`
	BirthEventID     string  `cbor:"birth_event_id"`
	DismissedEventID *string `cbor:"dismissed_event_id"`
}

type namedBatchReadMembership struct {
	Kind          string `cbor:"kind"`
	BatchID       string `cbor:"batch_id"`
	MatterID      string `cbor:"matter_id"`
	RepoID        string `cbor:"repo_id"`
	JoinedEventID string `cbor:"joined_event_id"`
}

type namedBatchReadFold struct {
	name, birth, dismissed, lastEventID string
	matterRepo                          map[string]string
	active                              map[string]string
	anonymous                           bool
}

// ReadNamedBatchPage returns one page of a closed, named-Batch snapshot.
// Items are reconstructed from the immutable authority event prefix pinned by
// snapshotID, never from mutable projection rows.
func (s *Store) ReadNamedBatchPage(ctx context.Context, domain string, epoch uint64, snapshotID, batchID string, pageSize uint16, token string, now time.Time) (NamedBatchPage, error) {
	var out NamedBatchPage
	if !ulid.MatchString(domain) || !ulid.MatchString(snapshotID) || !ulid.MatchString(batchID) || epoch == 0 || now.IsZero() {
		return out, ErrInvalidProof
	}
	if pageSize == 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		return out, ErrInvalidProof
	}
	filterBytes, err := artifactEncoder.Marshal(map[string]any{"batch_id": batchID})
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
	var claims pageTokenClaims
	if token != "" {
		claims, err = verifyPageTokenRequestScope(ctx, tx, token, domain, epoch, snapshotID, filterHash)
		if err != nil {
			return out, err
		}
		if claims.QueryName != "batch.read" || claims.QueryVersion != 1 {
			return out, ErrPageTokenScope
		}
	}
	var storedDomain, manifestDigest, prefixDigest string
	var storedEpoch, eventCount uint64
	var eventID sql.NullString
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
	retained, err := anchorAt(ctx, tx, domain, eventCount)
	if err != nil || !equalAnchor(anchor, retained) {
		return out, ErrPrefixMismatch
	}
	var offset uint64
	if token != "" {
		expected := pageTokenClaims{
			Schema: "wipd.page-token/1", Issuer: "authority", DomainID: domain, Epoch: epoch,
			QueryName: "batch.read", QueryVersion: 1, FilterHash: filterHash, SnapshotID: snapshotID,
			AsOfCount: anchor.EventCount, AsOfEventID: nullableEventID(anchor.EventID), AsOfDigest: anchor.Digest, Overlay: "folded-only",
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
		if canonicalDecode(claims.Cursor, &offset) != nil || offset == 0 {
			return out, ErrInvalidPageToken
		}
	}
	fold, err := foldNamedBatchReadPrefix(ctx, tx, domain, eventCount, batchID)
	if err != nil {
		return out, err
	}
	if fold.name == "" {
		return out, ErrNamedBatchNotFound
	}
	if fold.lastEventID != anchor.EventID {
		return out, ErrPrefixMismatch
	}
	items := make([]SnapshotItem, 0, len(fold.active)+1)
	var dismissedEventID *string
	if fold.dismissed != "" {
		dismissedEventID = &fold.dismissed
	}
	value, err := artifactEncoder.Marshal(namedBatchReadHeader{
		Kind: "batch", BatchID: batchID, Name: fold.name,
		BirthEventID: fold.birth, DismissedEventID: dismissedEventID,
	})
	if err != nil {
		return out, err
	}
	items = append(items, SnapshotItem{ID: batchID, Value: value})
	members := make([]string, 0, len(fold.active))
	for matterID := range fold.active {
		members = append(members, matterID)
	}
	sort.Strings(members)
	for _, matterID := range members {
		value, err = artifactEncoder.Marshal(namedBatchReadMembership{
			Kind: "membership", BatchID: batchID, MatterID: matterID,
			RepoID: fold.matterRepo[matterID], JoinedEventID: fold.active[matterID],
		})
		if err != nil {
			return out, err
		}
		items = append(items, SnapshotItem{ID: matterID, Value: value})
	}
	if offset > uint64(len(items)) {
		return out, ErrInvalidPageToken
	}
	start := int(offset)
	end := start + int(pageSize)
	if end > len(items) {
		end = len(items)
	}
	pageItems := append([]SnapshotItem(nil), items[start:end]...)
	complete := end == len(items)
	var nextToken string
	if !complete {
		cursor, encodeErr := artifactEncoder.Marshal(uint64(end))
		if encodeErr != nil {
			return out, encodeErr
		}
		pageKeySecret, queryErr := transferSecret(ctx, tx)
		if queryErr != nil {
			return out, queryErr
		}
		expires := time.Unix(0, expiry).UTC().Format(time.RFC3339Nano)
		if token != "" {
			expires = claims.ExpiresAt
		}
		pageKey := derivePageTokenKey(pageKeySecret)
		nextToken, err = signPageToken(pageTokenClaims{
			Schema: "wipd.page-token/1", Issuer: "authority", DomainID: domain, Epoch: epoch,
			QueryName: "batch.read", QueryVersion: 1, FilterHash: filterHash, SnapshotID: snapshotID, AsOfCount: anchor.EventCount,
			AsOfEventID: nullableEventID(anchor.EventID), AsOfDigest: anchor.Digest, Overlay: "folded-only", PageSize: uint64(pageSize),
			Cursor: cursor, ExpiresAt: expires,
		}, pageKey)
		if err != nil {
			return out, err
		}
	}
	out = NamedBatchPage{
		SnapshotID: snapshotID, DomainID: domain, Epoch: epoch, AsOf: anchor, ManifestDigest: manifestDigest,
		FilterHash: filterHash, ExpiresAt: time.Unix(0, expiry).UTC(), Items: pageItems, NextPageToken: nextToken, Complete: complete,
		Source: "authority", Reachability: "reachable", HistoryState: "current",
	}
	if err = tx.Commit(); err != nil {
		return NamedBatchPage{}, err
	}
	return out, nil
}

func transferSecret(ctx context.Context, tx namedBatchQueryer) ([]byte, error) {
	var secret []byte
	err := tx.QueryRowContext(ctx, `SELECT key FROM transfer_secret WHERE purpose='transfer'`).Scan(&secret)
	return secret, err
}

func foldNamedBatchReadPrefix(ctx context.Context, tx *sql.Tx, domain string, count uint64, batchID string) (namedBatchReadFold, error) {
	fold := namedBatchReadFold{matterRepo: make(map[string]string), active: make(map[string]string)}
	rows, err := tx.QueryContext(ctx, `SELECT position,event_id,command_id,record FROM authority_events WHERE domain_id=? AND position<=? ORDER BY position`, domain, count)
	if err != nil {
		return fold, err
	}
	defer func() { _ = rows.Close() }()
	var seen uint64
	for rows.Next() {
		var event step12Event
		var raw []byte
		if err = rows.Scan(&event.position, &event.id, &event.command, &raw); err != nil {
			return fold, err
		}
		event, err = parseStep12Event(raw, domain, event.position, event.id, event.command)
		if err != nil {
			return fold, ErrInvalidStore
		}
		seen++
		if event.position != seen {
			return fold, ErrPrefixMismatch
		}
		fold.lastEventID = event.id
		if event.kind == "matter.created" && event.repo != "" {
			fold.matterRepo[event.subject] = event.repo
		}
		if event.subject != batchID {
			continue
		}
		if event.kind == "batch.created" && event.repo != "" {
			fold.anonymous = true
			continue
		}
		if fold.anonymous {
			continue
		}
		switch event.kind {
		case "batch.created":
			if fold.name != "" || !exactKeys(event.payload, "name") || artifactDecoder.Unmarshal(event.payload["name"], &fold.name) != nil ||
				fold.name == "" || !ulid.MatchString(event.id) {
				return fold, ErrInvalidStore
			}
			fold.birth = event.id
		case "batch.joined":
			var matterID string
			if event.repo != "" || !exactKeys(event.payload, "matter_id") || artifactDecoder.Unmarshal(event.payload["matter_id"], &matterID) != nil ||
				!ulid.MatchString(matterID) || fold.name == "" || fold.active[matterID] != "" {
				return fold, ErrInvalidStore
			}
			fold.active[matterID] = event.id
		case "batch.left":
			var matterID string
			if event.repo != "" || !exactKeys(event.payload, "matter_id") || artifactDecoder.Unmarshal(event.payload["matter_id"], &matterID) != nil || fold.active[matterID] == "" {
				return fold, ErrInvalidStore
			}
			delete(fold.active, matterID)
		case "batch.dismissed":
			if event.repo != "" || !exactKeys(event.payload) || fold.dismissed != "" || fold.name == "" {
				return fold, ErrInvalidStore
			}
			fold.dismissed = event.id
		}
	}
	if err = rows.Err(); err != nil {
		return fold, err
	}
	if seen != count {
		return fold, fmt.Errorf("%w: named Batch snapshot prefix length mismatch", ErrInvalidStore)
	}
	if fold.name != "" {
		for matterID := range fold.active {
			if !ulid.MatchString(fold.matterRepo[matterID]) {
				return fold, ErrInvalidStore
			}
		}
	}
	return fold, nil
}
