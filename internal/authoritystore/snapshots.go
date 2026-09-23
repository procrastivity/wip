package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

const (
	maxSnapshotLife  = 15 * time.Minute
	maxLiveSnapshots = 128
)

var (
	// ErrPrefixMismatch means the supplied anchor is not in retained lineage.
	ErrPrefixMismatch = errors.New("transfer.prefix-mismatch")
	// ErrManifestMismatch means an entry or manifest digest disagrees.
	ErrManifestMismatch = errors.New("transfer.manifest-mismatch")
	// ErrSnapshotExpired means a pinned resource has expired or been released.
	ErrSnapshotExpired = errors.New("query.snapshot-expired")
	// ErrResourceLimit means a pin or record exceeds the local bounded quota.
	ErrResourceLimit = errors.New("authoritystore: snapshot resource limit")
)

// PrefixAnchor identifies an exact cumulative authority event prefix.
type PrefixAnchor struct {
	EventCount uint64
	EventID    string // empty represents protocol null
	Digest     string
}

// PrefixRecord contains the exact retained event bytes and their identity.
type PrefixRecord struct {
	EventID string
	Record  []byte
}

// PrefixDelta contains every event after Start through End in fold order.
type PrefixDelta struct {
	Start, End PrefixAnchor
	Events     []PrefixRecord
}

// BlobManifestEntry names one verified authority blob and its use requirement.
type BlobManifestEntry struct {
	Digest      string
	ByteLength  uint64
	Requirement string
}

// BlobManifest binds complete sorted entries to one domain, epoch, and anchor.
type BlobManifest struct {
	DomainID string
	Epoch    uint64
	AsOf     PrefixAnchor
	Entries  []BlobManifestEntry
	Digest   string
}

// SnapshotItem contains a folded item value pinned at snapshot acquisition.
type SnapshotItem struct {
	ID    string
	Value []byte
}

// PinnedSnapshot is an immutable read and transfer product with bounded lifetime.
type PinnedSnapshot struct {
	ID        string
	DomainID  string
	Epoch     uint64
	ExpiresAt time.Time
	Delta     PrefixDelta
	Manifest  BlobManifest
	Items     []SnapshotItem // immutable authority folded matter.list@v1 view, sorted locator then ID
}

func emptyAnchor() PrefixAnchor {
	return PrefixAnchor{Digest: digestBytes([]byte("wipd/event-prefix/v1\x00"))}
}

func anchorAt(ctx context.Context, tx *sql.Tx, domain string, count uint64) (PrefixAnchor, error) {
	if count == 0 {
		return emptyAnchor(), nil
	}
	var a PrefixAnchor
	err := tx.QueryRowContext(ctx, `SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, domain, count).Scan(&a.EventCount, &a.EventID, &a.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrPrefixMismatch
	}
	return a, err
}

func currentAnchor(ctx context.Context, tx *sql.Tx, domain string) (PrefixAnchor, error) {
	var a PrefixAnchor
	err := tx.QueryRowContext(ctx, `SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? ORDER BY position DESC LIMIT 1`, domain).Scan(&a.EventCount, &a.EventID, &a.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return emptyAnchor(), nil
	}
	return a, err
}

func equalAnchor(a, b PrefixAnchor) bool { return a == b }

func manifestChain(entries []BlobManifestEntry) (string, error) {
	hash := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	var prior []byte
	for _, e := range entries {
		raw, err := digestRaw(e.Digest)
		if err != nil || e.ByteLength > maxBlobLength || (len(prior) != 0 && bytes.Compare(prior, raw) >= 0) {
			return "", ErrManifestMismatch
		}
		prior = raw
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], e.ByteLength)
		var requirement byte
		switch e.Requirement {
		case "lazy":
		case "pin-before-use":
			requirement = 1
		default:
			return "", ErrManifestMismatch
		}
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/blob-manifest-step/v1\x00"))
		_, _ = h.Write(hash[:])
		_, _ = h.Write(raw)
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte{requirement})
		copy(hash[:], h.Sum(nil))
	}
	return digestRawBytes(hash[:]), nil
}

// pinSnapshotTx is the Step 5 integration boundary. The caller already owns
// its lane and SQL transaction. It must validate the acquisition receipt and
// claim-required digest set in that transaction, then bind its grant and
// artifact-chain advance to the returned ID, anchors, and manifest digest.
// This method does not start, commit, or roll back a nested transaction.
func pinSnapshotTx(ctx context.Context, tx *sql.Tx, domain string, epoch uint64, start PrefixAnchor, id string, now time.Time, lifetime time.Duration, required []string) (PinnedSnapshot, error) {
	var out PinnedSnapshot
	if !ulid.MatchString(domain) || !ulid.MatchString(id) || epoch == 0 || now.IsZero() || lifetime <= 0 || lifetime > maxSnapshotLife {
		return out, ErrInvalidProof
	}
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != epoch {
		return out, ErrFenced
	}
	end, err := currentAnchor(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if start.EventCount > end.EventCount || start.EventCount > math.MaxInt64 {
		return out, ErrPrefixMismatch
	}
	retained, err := anchorAt(ctx, tx, domain, start.EventCount)
	if err != nil || !equalAnchor(start, retained) {
		return out, ErrPrefixMismatch
	}
	var live int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM snapshots WHERE domain_id=? AND expires_at>?`, domain, now.UnixNano()).Scan(&live); err != nil {
		return out, err
	}
	if live >= maxLiveSnapshots {
		return out, ErrResourceLimit
	}
	out = PinnedSnapshot{ID: id, DomainID: domain, Epoch: epoch, ExpiresAt: now.Add(lifetime).UTC(), Delta: PrefixDelta{Start: start, End: end}}
	rows, err := tx.QueryContext(ctx, `SELECT event_id,record FROM authority_events WHERE domain_id=? AND position>? AND position<=? ORDER BY position`, domain, start.EventCount, end.EventCount)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	for rows.Next() {
		var event PrefixRecord
		if err = rows.Scan(&event.EventID, &event.Record); err != nil {
			break
		}
		out.Delta.Events = append(out.Delta.Events, event)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return PinnedSnapshot{}, err
	}
	if uint64(len(out.Delta.Events)) != end.EventCount-start.EventCount {
		return PinnedSnapshot{}, ErrInvalidStore
	}
	needed := make(map[string]bool, len(required))
	for _, digest := range required {
		if !validDigest(digest) || needed[digest] {
			return PinnedSnapshot{}, ErrManifestMismatch
		}
		needed[digest] = true
	}
	rows, err = tx.QueryContext(ctx, `SELECT r.digest,p.byte_length FROM blob_references r JOIN blob_products p USING(domain_id,digest) WHERE r.domain_id=? AND r.first_position<=? AND p.verified=1 ORDER BY r.digest`, domain, end.EventCount)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	out.Manifest = BlobManifest{DomainID: domain, Epoch: epoch, AsOf: end}
	for rows.Next() {
		var entry BlobManifestEntry
		if err = rows.Scan(&entry.Digest, &entry.ByteLength); err != nil {
			break
		}
		entry.Requirement = "lazy"
		if needed[entry.Digest] {
			entry.Requirement = "pin-before-use"
			delete(needed, entry.Digest)
		}
		out.Manifest.Entries = append(out.Manifest.Entries, entry)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return PinnedSnapshot{}, err
	}
	if len(needed) != 0 {
		return PinnedSnapshot{}, ErrManifestMismatch
	}
	// SQL's collation on canonical lowercase ASCII hex matches raw digest sort.
	out.Manifest.Digest, err = manifestChain(out.Manifest.Entries)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT m.matter_id,m.repo_id,m.locator,m.title,m.birth_event_id FROM matters m JOIN authority_events e ON e.domain_id=m.domain_id AND e.event_id=m.birth_event_id WHERE m.domain_id=? AND e.position<=? ORDER BY m.locator,m.matter_id`, domain, end.EventCount)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	for rows.Next() {
		var item SnapshotItem
		var repo, locator, title, birth string
		if err = rows.Scan(&item.ID, &repo, &locator, &title, &birth); err != nil {
			break
		}
		item.Value, err = artifactEncoder.Marshal(map[string]any{"id": item.ID, "repo_id": repo, "locator": locator, "title": title, "birth_event_id": birth})
		if err != nil {
			break
		}
		out.Items = append(out.Items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return PinnedSnapshot{}, err
	}
	var eventID any
	if end.EventID != "" {
		eventID = end.EventID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO snapshots VALUES(?,?,?,?,?,?,?,?)`, id, domain, epoch, end.EventCount, eventID, end.Digest, out.Manifest.Digest, out.ExpiresAt.UnixNano()); err != nil {
		return PinnedSnapshot{}, writeError(err)
	}
	for _, entry := range out.Manifest.Entries {
		if _, err = tx.ExecContext(ctx, `INSERT INTO snapshot_entries VALUES(?,?,?,?)`, id, entry.Digest, entry.ByteLength, entry.Requirement); err != nil {
			return PinnedSnapshot{}, err
		}
	}
	for ordinal, item := range out.Items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO snapshot_items VALUES(?,?,?,?)`, id, ordinal, item.ID, item.Value); err != nil {
			return PinnedSnapshot{}, err
		}
	}
	return out, nil
}

// PinSnapshot captures the complete current authority prefix and manifest.
func (s *Store) PinSnapshot(ctx context.Context, domain string, epoch uint64, start PrefixAnchor, id string, now time.Time, lifetime time.Duration) (PinnedSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return PinnedSnapshot{}, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := pinSnapshotTx(ctx, tx, domain, epoch, start, id, now, lifetime, nil)
	if err != nil {
		return PinnedSnapshot{}, err
	}
	return out, tx.Commit()
}

// SnapshotPage reads only pinned bytes, not current projection rows. The
// caller's continuation boundary is an ordinal scoped to this snapshot; a
// future query adapter must wrap it in the sealed page-token claims/MAC.
func (s *Store) SnapshotPage(ctx context.Context, domain string, epoch uint64, id string, offset, size uint64, now time.Time) ([]SnapshotItem, bool, error) {
	if now.IsZero() {
		return nil, false, ErrInvalidProof
	}
	if size == 0 {
		size = 100
	}
	if size > 1000 || offset > math.MaxInt64 || size > math.MaxInt64-offset {
		return nil, false, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false, ErrInvalidStore
	}
	var expiry int64
	if err := s.db.QueryRowContext(ctx, `SELECT s.expires_at FROM snapshots s JOIN domains d ON d.domain_id=s.domain_id AND d.active_epoch=s.epoch WHERE s.snapshot_id=? AND s.domain_id=? AND s.epoch=?`, id, domain, epoch).Scan(&expiry); err != nil {
		return nil, false, ErrSnapshotExpired
	}
	if expiry <= now.UnixNano() {
		return nil, false, ErrSnapshotExpired
	}
	rows, err := s.db.QueryContext(ctx, `SELECT matter_id,value FROM snapshot_items WHERE snapshot_id=? AND ordinal>=? ORDER BY ordinal LIMIT ?`, id, offset, size+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	var items []SnapshotItem
	for rows.Next() {
		var item SnapshotItem
		if err = rows.Scan(&item.ID, &item.Value); err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	complete := uint64(len(items)) <= size
	if !complete {
		items = items[:size]
	}
	return items, complete, nil
}

// ReleaseSnapshot cancels a scoped read or transfer without changing authority truth.
func (s *Store) ReleaseSnapshot(ctx context.Context, domain string, epoch uint64, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE snapshot_id=? AND domain_id=? AND epoch=? AND EXISTS(SELECT 1 FROM domains d WHERE d.domain_id=? AND d.active_epoch=?)`, id, domain, epoch, domain, epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSnapshotExpired
	}
	return nil
}

func checkStep6State(db *sql.DB) error {
	var key []byte
	if err := db.QueryRow(`SELECT key FROM transfer_secret WHERE purpose='transfer'`).Scan(&key); err != nil || len(key) != 32 {
		return ErrInvalidStore
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM transfer_secret`).Scan(&n); err != nil || n != 1 {
		return ErrInvalidStore
	}
	rows, err := db.Query(`SELECT domain_id,digest,first_position FROM blob_references`)
	if err != nil {
		return err
	}
	type reference struct {
		domain, digest string
		position       uint64
	}
	var refs []reference
	for rows.Next() {
		var r reference
		if err = rows.Scan(&r.domain, &r.digest, &r.position); err != nil {
			break
		}
		refs = append(refs, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, r := range refs {
		var verified int
		var length uint64
		var command []byte
		var first, last sql.NullInt64
		if err = db.QueryRow(`SELECT p.verified,p.byte_length,s.command,r.first_position,r.last_position FROM blob_products p JOIN authority_events e ON e.domain_id=p.domain_id AND e.position=? JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id AND r.result_code='result.succeeded' WHERE p.domain_id=? AND p.digest=?`, r.position, r.domain, r.digest).Scan(&verified, &length, &command, &first, &last); err != nil || verified != 1 || !first.Valid || !last.Valid || uint64(first.Int64) > r.position || uint64(last.Int64) < r.position {
			return ErrInvalidStore
		}
		declared, e := commandBlobLengths(command)
		declaredLength, ok := declared[r.digest]
		if e != nil || !ok || declaredLength != length {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT snapshot_id,domain_id,epoch,event_count,event_id,prefix_digest,manifest_digest,expires_at FROM snapshots`)
	if err != nil {
		return err
	}
	type snap struct {
		id, domain, digest, manifest string
		expiry                       int64
		epoch, count                 uint64
		event                        sql.NullString
	}
	var snaps []snap
	for rows.Next() {
		var p snap
		if err = rows.Scan(&p.id, &p.domain, &p.epoch, &p.count, &p.event, &p.digest, &p.manifest, &p.expiry); err != nil {
			break
		}
		snaps = append(snaps, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range snaps {
		if !ulid.MatchString(p.id) || !ulid.MatchString(p.domain) || p.epoch == 0 {
			return ErrInvalidStore
		}
		var active uint64
		if err = db.QueryRow(`SELECT active_epoch FROM domains WHERE domain_id=?`, p.domain).Scan(&active); err != nil || p.epoch > active {
			return ErrInvalidStore
		}
		if p.expiry <= 0 {
			return ErrInvalidStore
		}
		var anchor PrefixAnchor
		if p.count == 0 {
			anchor = emptyAnchor()
		} else {
			err = db.QueryRow(`SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, p.domain, p.count).Scan(&anchor.EventCount, &anchor.EventID, &anchor.Digest)
			if err != nil {
				return ErrInvalidStore
			}
		}
		if anchor.Digest != p.digest || anchor.EventID != p.event.String {
			return ErrInvalidStore
		}
		entries, err := readEntries(db, p.id)
		if err != nil {
			return err
		}
		digest, err := manifestChain(entries)
		if err != nil || digest != p.manifest {
			return ErrInvalidStore
		}
		for _, entry := range entries {
			var length uint64
			var position uint64
			var verified int
			if err = db.QueryRow(`SELECT p.byte_length,r.first_position,p.verified FROM blob_references r JOIN blob_products p USING(domain_id,digest) WHERE r.domain_id=? AND r.digest=?`, p.domain, entry.Digest).Scan(&length, &position, &verified); err != nil || length != entry.ByteLength || position > p.count || verified != 1 {
				return ErrInvalidStore
			}
		}
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM blob_references WHERE domain_id=? AND first_position<=?`, p.domain, p.count).Scan(&count); err != nil || count != len(entries) {
			return ErrInvalidStore
		}
		items, err := readItems(db, p.id)
		if err != nil {
			return err
		}
		projected, err := db.Query(`SELECT m.matter_id,m.repo_id,m.locator,m.title,m.birth_event_id FROM matters m JOIN authority_events e ON e.domain_id=m.domain_id AND e.event_id=m.birth_event_id WHERE m.domain_id=? AND e.position<=? ORDER BY m.locator,m.matter_id`, p.domain, p.count)
		if err != nil {
			return err
		}
		ordinal := 0
		for projected.Next() {
			var id, repo, locator, title, birth string
			if err = projected.Scan(&id, &repo, &locator, &title, &birth); err != nil {
				break
			}
			var value []byte
			value, err = artifactEncoder.Marshal(map[string]any{"id": id, "repo_id": repo, "locator": locator, "title": title, "birth_event_id": birth})
			if err != nil {
				break
			}
			if ordinal >= len(items) || items[ordinal].ID != id || !bytes.Equal(items[ordinal].Value, value) {
				err = ErrInvalidStore
				break
			}
			ordinal++
		}
		if err == nil {
			err = projected.Err()
		}
		_ = projected.Close()
		if err != nil || ordinal != len(items) {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT t.transfer_id,t.start_count,t.start_event_id,t.start_digest,t.next_event,t.next_entry,s.snapshot_id,s.domain_id,s.event_count,s.expires_at,t.expires_at FROM transfers t JOIN snapshots s USING(snapshot_id)`)
	if err != nil {
		return err
	}
	type transferCheck struct {
		id, snapshot, domain, digest   string
		start, event, entry, end       uint64
		snapshotExpiry, transferExpiry int64
		eventID                        sql.NullString
	}
	var transfers []transferCheck
	for rows.Next() {
		var t transferCheck
		if err = rows.Scan(&t.id, &t.start, &t.eventID, &t.digest, &t.event, &t.entry, &t.snapshot, &t.domain, &t.end, &t.snapshotExpiry, &t.transferExpiry); err != nil {
			break
		}
		transfers = append(transfers, t)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, t := range transfers {
		var entryCount uint64
		if err = db.QueryRow(`SELECT count(*) FROM snapshot_entries WHERE snapshot_id=?`, t.snapshot).Scan(&entryCount); err != nil {
			return err
		}
		if !ulid.MatchString(t.id) || t.start > t.end || t.event > t.end-t.start || t.entry > entryCount || (t.entry > 0 && t.event != t.end-t.start) || t.transferExpiry > t.snapshotExpiry {
			return ErrInvalidStore
		}
		var anchor PrefixAnchor
		if t.start == 0 {
			anchor = emptyAnchor()
		} else {
			err = db.QueryRow(`SELECT position,event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, t.domain, t.start).Scan(&anchor.EventCount, &anchor.EventID, &anchor.Digest)
			if err != nil {
				return ErrInvalidStore
			}
		}
		if anchor.EventID != t.eventID.String || anchor.Digest != t.digest {
			return ErrInvalidStore
		}
	}
	return nil
}

func readEntries(db *sql.DB, id string) ([]BlobManifestEntry, error) {
	rows, err := db.Query(`SELECT digest,byte_length,requirement FROM snapshot_entries WHERE snapshot_id=? ORDER BY digest`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []BlobManifestEntry
	for rows.Next() {
		var e BlobManifestEntry
		if err = rows.Scan(&e.Digest, &e.ByteLength, &e.Requirement); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func readItems(db *sql.DB, id string) ([]SnapshotItem, error) {
	rows, err := db.Query(`SELECT matter_id,value FROM snapshot_items WHERE snapshot_id=? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []SnapshotItem
	for rows.Next() {
		var e SnapshotItem
		if err = rows.Scan(&e.ID, &e.Value); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}

func (a PrefixAnchor) String() string {
	return fmt.Sprintf("%d/%s/%s", a.EventCount, a.EventID, a.Digest)
}
