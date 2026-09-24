package authoritystore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"time"
)

// ErrResumeInvalid means a token was forged, rescoped, or rewound too far.
var ErrResumeInvalid = errors.New("transfer.resume-invalid")

// Transfer contains one immutable seed/pull product. Each NextTransfer call
// returns one whole verified-boundary record or manifest entry; a transfer
// never exposes a sparse prefix or an entry from a different snapshot.
type Transfer struct {
	ID, SnapshotID, Kind, StoreSchema string
	Snapshot                          PinnedSnapshot
	EventCount, EventByteLength       uint64
	Token                             string
}

type transferClaims struct {
	Schema      string  `cbor:"schema"`
	Kind        string  `cbor:"kind"`
	ID          string  `cbor:"transfer_id"`
	Domain      string  `cbor:"domain_id"`
	Epoch       uint64  `cbor:"authority_epoch"`
	SnapshotID  string  `cbor:"snapshot_id"`
	StartCount  uint64  `cbor:"start_count"`
	StartID     *string `cbor:"start_event_id"`
	StartDigest string  `cbor:"start_digest"`
	EndCount    uint64  `cbor:"as_of_event_count"`
	EndID       *string `cbor:"as_of_event_id"`
	EndDigest   string  `cbor:"as_of_prefix_digest"`
	Manifest    string  `cbor:"manifest_digest"`
	NextEvent   uint64  `cbor:"next_event"`
	NextEntry   uint64  `cbor:"next_entry"`
	StoreSchema string  `cbor:"store_schema"`
	Expires     string  `cbor:"expires_at"`
}

func signTransfer(claims transferClaims, key []byte) (string, error) {
	raw, err := artifactEncoder.Marshal(claims)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(raw)
	return "tt1." + base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

func verifyTransfer(token string, key []byte) (transferClaims, error) {
	var claims transferClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "tt1" || len(token) > 4096 {
		return claims, ErrResumeInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, ErrResumeInvalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, ErrResumeInvalid
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(raw)
	if !hmac.Equal(mac, h.Sum(nil)) || canonicalDecode(raw, &claims) != nil || claims.Schema != "wipd.transfer-token/1" {
		return claims, ErrResumeInvalid
	}
	return claims, nil
}

func transferToken(ctx context.Context, tx *sql.Tx, claims transferClaims) (string, error) {
	var secret []byte
	if err := tx.QueryRowContext(ctx, `SELECT key FROM transfer_secret WHERE purpose='transfer'`).Scan(&secret); err != nil {
		return "", err
	}
	return signTransfer(claims, secret)
}

// StartTransfer pins one complete authority product and durable progress in
// one commit. The caller supplies the exact installed anchor for pull; seed
// always begins at genesis. StoreSchema is the negotiated wipd.store/1 base.
func (s *Store) StartTransfer(ctx context.Context, domain string, epoch uint64, kind, storeSchema string, start PrefixAnchor, snapshotID, transferID string, now time.Time) (Transfer, error) {
	var out Transfer
	if kind != "seed" && kind != "pull" || storeSchema != "wipd.store/1" || !ulid.MatchString(transferID) {
		return out, ErrInvalidProof
	}
	if kind == "seed" && start != emptyAnchor() {
		return out, ErrPrefixMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkWriteAdmission(ctx, tx, domain, epoch); err != nil {
		return out, err
	}
	snapshot, err := pinSnapshotTx(ctx, tx, domain, epoch, start, snapshotID, now, 15*time.Minute, nil)
	if err != nil {
		return out, err
	}
	for _, e := range snapshot.Delta.Events {
		if uint64(len(e.Record)) > math.MaxUint64-out.EventByteLength {
			return Transfer{}, ErrResourceLimit
		}
		out.EventByteLength += uint64(len(e.Record))
	}
	out.EventCount = uint64(len(snapshot.Delta.Events))
	if out.EventByteLength > maxBlobLength {
		return Transfer{}, ErrResourceLimit
	}
	var startID any
	if start.EventID != "" {
		startID = start.EventID
	}
	expires := snapshot.ExpiresAt.Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO transfers VALUES(?,?,?,?,?,?,?,?,?,?)`, transferID, snapshotID, kind, storeSchema, start.EventCount, startID, start.Digest, 0, 0, snapshot.ExpiresAt.UnixNano()); err != nil {
		return Transfer{}, writeError(err)
	}
	out.ID, out.SnapshotID, out.Kind, out.StoreSchema, out.Snapshot = transferID, snapshotID, kind, storeSchema, snapshot
	claims := transferClaims{Schema: "wipd.transfer-token/1", Kind: kind, ID: transferID, Domain: domain, Epoch: epoch, SnapshotID: snapshotID, StartCount: start.EventCount, StartDigest: start.Digest, EndCount: snapshot.Delta.End.EventCount, EndDigest: snapshot.Delta.End.Digest, Manifest: snapshot.Manifest.Digest, StoreSchema: storeSchema, Expires: expires}
	if start.EventID != "" {
		claims.StartID = &start.EventID
	}
	if snapshot.Delta.End.EventID != "" {
		claims.EndID = &snapshot.Delta.End.EventID
	}
	out.Token, err = transferToken(ctx, tx, claims)
	if err != nil {
		return Transfer{}, err
	}
	return out, tx.Commit()
}

// TransferBoundary is one record/entry verified boundary or the final closure.
type TransferBoundary struct {
	Event          *PrefixRecord
	Entry          *BlobManifestEntry
	Anchor         PrefixAnchor // cumulative anchor after an event, or final anchor for manifest/end
	Complete       bool
	ManifestDigest string
}

func transferState(ctx context.Context, tx *sql.Tx, token string, now time.Time) (transferClaims, uint64, uint64, error) {
	if now.IsZero() {
		return transferClaims{}, 0, 0, ErrInvalidProof
	}
	var key []byte
	if err := tx.QueryRowContext(ctx, `SELECT key FROM transfer_secret WHERE purpose='transfer'`).Scan(&key); err != nil {
		return transferClaims{}, 0, 0, err
	}
	c, err := verifyTransfer(token, key)
	if err != nil {
		return c, 0, 0, err
	}
	var stored transferClaims
	var eventID sql.NullString
	var startID sql.NullString
	var event, entry uint64
	var expiresAt int64
	var activeEpoch uint64
	err = tx.QueryRowContext(ctx, `SELECT t.kind,t.transfer_id,s.domain_id,s.epoch,t.snapshot_id,t.start_count,t.start_event_id,t.start_digest,s.event_count,s.event_id,s.prefix_digest,s.manifest_digest,t.next_event,t.next_entry,t.store_schema,t.expires_at,d.active_epoch FROM transfers t JOIN snapshots s USING(snapshot_id) JOIN domains d ON d.domain_id=s.domain_id WHERE t.transfer_id=?`, c.ID).
		Scan(&stored.Kind, &stored.ID, &stored.Domain, &stored.Epoch, &stored.SnapshotID, &stored.StartCount, &startID, &stored.StartDigest, &stored.EndCount, &eventID, &stored.EndDigest, &stored.Manifest, &event, &entry, &stored.StoreSchema, &expiresAt, &activeEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return c, 0, 0, ErrResumeInvalid
	}
	if err != nil {
		return c, 0, 0, err
	}
	if stored.Epoch != activeEpoch {
		return c, 0, 0, ErrFenced
	}
	stored.Schema = "wipd.transfer-token/1"
	if startID.Valid {
		stored.StartID = &startID.String
	}
	if eventID.Valid {
		stored.EndID = &eventID.String
	}
	stored.Expires = time.Unix(0, expiresAt).UTC().Format(time.RFC3339Nano)
	if now.UnixNano() >= expiresAt {
		return c, 0, 0, ErrSnapshotExpired
	}
	if c.Schema != stored.Schema || c.Kind != stored.Kind || c.ID != stored.ID || c.Domain != stored.Domain || c.Epoch != stored.Epoch || c.SnapshotID != stored.SnapshotID || c.StartCount != stored.StartCount || c.StartDigest != stored.StartDigest || c.EndCount != stored.EndCount || c.EndDigest != stored.EndDigest || c.Manifest != stored.Manifest || c.StoreSchema != stored.StoreSchema || c.Expires != stored.Expires || (c.StartID == nil) != (stored.StartID == nil) || (c.StartID != nil && *c.StartID != *stored.StartID) || (c.EndID == nil) != (stored.EndID == nil) || (c.EndID != nil && *c.EndID != *stored.EndID) {
		return c, 0, 0, ErrResumeInvalid
	}
	if c.NextEvent > event || c.NextEntry > entry || event-c.NextEvent+entry-c.NextEntry > 1 {
		return c, 0, 0, ErrResumeInvalid
	}
	if c.NextEntry > 0 && c.NextEvent != c.EndCount-c.StartCount {
		return c, 0, 0, ErrResumeInvalid
	}
	return c, event, entry, nil
}

// NextTransfer reads at most one complete event or sorted manifest entry. A
// just-acknowledged stale token repeats that last boundary once; older tokens
// cannot rewind the retained progress. It holds the lane only for this read.
func (s *Store) NextTransfer(ctx context.Context, token string, maxRecordBytes uint64, now time.Time) (TransferBoundary, error) {
	var out TransferBoundary
	if maxRecordBytes == 0 || maxRecordBytes > maxBlobLength {
		return out, ErrResourceLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return out, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	c, _, _, err := transferState(ctx, tx, token, now)
	if err != nil {
		return out, err
	}
	out.ManifestDigest = c.Manifest
	if c.NextEvent < c.EndCount-c.StartCount {
		var record PrefixRecord
		var digest string
		err = tx.QueryRowContext(ctx, `SELECT event_id,record,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, c.Domain, c.StartCount+c.NextEvent+1).Scan(&record.EventID, &record.Record, &digest)
		if err != nil {
			return out, ErrInvalidStore
		}
		if uint64(len(record.Record)) > maxRecordBytes {
			return out, ErrResourceLimit
		}
		out.Event = &record
		out.Anchor = PrefixAnchor{c.StartCount + c.NextEvent + 1, record.EventID, digest}
	} else {
		out.Anchor = PrefixAnchor{c.EndCount, "", c.EndDigest}
		if c.EndID != nil {
			out.Anchor.EventID = *c.EndID
		}
		var digest, requirement string
		var length uint64
		err = tx.QueryRowContext(ctx, `SELECT digest,byte_length,requirement FROM snapshot_entries WHERE snapshot_id=? ORDER BY digest LIMIT 1 OFFSET ?`, c.SnapshotID, c.NextEntry).Scan(&digest, &length, &requirement)
		if errors.Is(err, sql.ErrNoRows) {
			out.Complete = true
		} else if err != nil {
			return out, err
		} else {
			out.Entry = &BlobManifestEntry{digest, length, requirement}
		}
	}
	return out, tx.Commit()
}

// AcknowledgeTransfer commits only one verified boundary, returning a token
// for the new exact ordinal. verified must equal the sent event's cumulative
// anchor or the pinned manifest entry digest. A duplicate acknowledgment
// returns the same current token and never skips an event/entry.
func (s *Store) AcknowledgeTransfer(ctx context.Context, token string, verified string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	c, event, entry, err := transferState(ctx, tx, token, now)
	if err != nil {
		return "", err
	}
	if c.NextEvent < c.EndCount-c.StartCount {
		var digest string
		if err = tx.QueryRowContext(ctx, `SELECT prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, c.Domain, c.StartCount+c.NextEvent+1).Scan(&digest); err != nil {
			return "", err
		}
		if verified != digest {
			return "", ErrPrefixMismatch
		}
		if c.NextEvent == event {
			event++
		}
	} else {
		var digest string
		err = tx.QueryRowContext(ctx, `SELECT digest FROM snapshot_entries WHERE snapshot_id=? ORDER BY digest LIMIT 1 OFFSET ?`, c.SnapshotID, c.NextEntry).Scan(&digest)
		if errors.Is(err, sql.ErrNoRows) {
			if verified != c.Manifest {
				return "", ErrManifestMismatch
			}
		} else if err != nil {
			return "", err
		} else {
			if verified != digest {
				return "", ErrManifestMismatch
			}
			if c.NextEntry == entry {
				entry++
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE transfers SET next_event=?,next_entry=? WHERE transfer_id=?`, event, entry, c.ID); err != nil {
		return "", err
	}
	c.NextEvent, c.NextEntry = event, entry
	next, err := transferToken(ctx, tx, c)
	if err != nil {
		return "", err
	}
	return next, tx.Commit()
}
