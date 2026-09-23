package authoritystore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"hash"
	"io"
	"math"
	"time"

	"github.com/fxamacker/cbor/v2"
)

const (
	maxBlobLength  = uint64(1 << 40)
	maxBlobChunk   = 65536
	maxStagedBlobs = 128
	stagingTTL     = 24 * time.Hour
)

var (
	// ErrBlobLength means the declared or received length differs.
	ErrBlobLength = errors.New("blob.length-mismatch")
	// ErrBlobDigest means the full received byte hash differs.
	ErrBlobDigest = errors.New("blob.digest-mismatch")
	// ErrBlobRange means an offset or length exceeds the retained product.
	ErrBlobRange = errors.New("blob.range-invalid")
	// ErrBlobAbsent means no such verified or staged product exists.
	ErrBlobAbsent = errors.New("blob.not-found")
	// ErrBlobOffset means a chunk cannot extend or repeat the retained prefix.
	ErrBlobOffset = errors.New("transfer.resume-invalid")
)

func newSecret() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// BlobUpload states the authority's durably contiguous offset. Available
// includes verified staged bytes, but does not imply command promotion.
type BlobUpload struct {
	Offset    uint64
	Available bool
}

// StartBlob returns the exact contiguous temporary offset or verified availability.
func (s *Store) StartBlob(ctx context.Context, domain string, epoch uint64, digest string, length uint64, now time.Time) (BlobUpload, error) {
	var out BlobUpload
	if !ulid.MatchString(domain) || !validDigest(digest) || length > maxBlobLength || now.IsZero() {
		return out, ErrInvalidProof
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
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return out, err
	}
	if d.ActiveEpoch != epoch {
		return out, ErrFenced
	}
	var stored, offset uint64
	var verified int
	err = tx.QueryRowContext(ctx, `SELECT byte_length,verified_offset,verified FROM blob_products WHERE domain_id=? AND digest=?`, domain, digest).Scan(&stored, &offset, &verified)
	if err == nil {
		if stored != length {
			return out, ErrBlobLength
		}
		out = BlobUpload{offset, verified == 1}
	} else if errors.Is(err, sql.ErrNoRows) {
		var count, reserved uint64
		if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(p.byte_length),0) FROM blob_products p WHERE p.domain_id=? AND NOT EXISTS(SELECT 1 FROM blob_references r WHERE r.domain_id=p.domain_id AND r.digest=p.digest)`, domain).Scan(&count, &reserved); err != nil {
			return out, err
		}
		if count >= maxStagedBlobs || reserved > maxBlobLength-length {
			return out, ErrResourceLimit
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO blob_products(domain_id,digest,byte_length,expires_at) VALUES(?,?,?,?)`, domain, digest, length, now.Add(stagingTTL).UnixNano())
		if err != nil {
			return out, err
		}
	} else {
		return out, err
	}
	return out, tx.Commit()
}

// StageBlobChunk commits one verified contiguous temporary chunk. A retry at
// an earlier offset is accepted only when its retained bytes match exactly.
func (s *Store) StageBlobChunk(ctx context.Context, domain string, epoch uint64, digest string, offset uint64, chunk []byte) (uint64, error) {
	if !ulid.MatchString(domain) || !validDigest(digest) || len(chunk) == 0 || len(chunk) > maxBlobChunk {
		return 0, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return 0, err
	}
	if d.ActiveEpoch != epoch {
		return 0, ErrFenced
	}
	var size, head uint64
	var verified int
	err = tx.QueryRowContext(ctx, `SELECT byte_length,verified_offset,verified FROM blob_products WHERE domain_id=? AND digest=?`, domain, digest).Scan(&size, &head, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrBlobAbsent
	}
	if err != nil {
		return 0, err
	}
	if verified != 0 || offset > size || uint64(len(chunk)) > size-offset {
		return head, ErrBlobRange
	}
	if offset < head {
		var old []byte
		err = tx.QueryRowContext(ctx, `SELECT data FROM blob_chunks WHERE domain_id=? AND digest=? AND offset=?`, domain, digest, offset).Scan(&old)
		if err != nil || string(old) != string(chunk) {
			return head, ErrBlobOffset
		}
		return head, nil
	}
	if offset != head {
		return head, ErrBlobOffset
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO blob_chunks VALUES(?,?,?,?)`, domain, digest, offset, chunk); err != nil {
		return head, err
	}
	head += uint64(len(chunk))
	if _, err = tx.ExecContext(ctx, `UPDATE blob_products SET verified_offset=? WHERE domain_id=? AND digest=?`, head, domain, digest); err != nil {
		return 0, err
	}
	return head, tx.Commit()
}

// FinishBlob checks length before digest. A failed end removes only temporary
// bytes; a successful end is durable/idempotent without creating model truth.
func (s *Store) FinishBlob(ctx context.Context, domain string, epoch uint64, digest string) error {
	if !ulid.MatchString(domain) || !validDigest(digest) {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := domainOwner(ctx, tx, domain)
	if err != nil {
		return err
	}
	if d.ActiveEpoch != epoch {
		return ErrFenced
	}
	var length, offset uint64
	var verified int
	err = tx.QueryRowContext(ctx, `SELECT byte_length,verified_offset,verified FROM blob_products WHERE domain_id=? AND digest=?`, domain, digest).Scan(&length, &offset, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBlobAbsent
	}
	if err != nil {
		return err
	}
	if verified == 1 {
		return nil
	}
	var bad error
	if offset != length {
		bad = ErrBlobLength
	} else {
		h := sha256.New()
		if err = hashChunks(ctx, tx, domain, digest, length, h); err != nil {
			return err
		}
		if digestRawBytes(h.Sum(nil)) != digest {
			bad = ErrBlobDigest
		}
	}
	if bad != nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM blob_products WHERE domain_id=? AND digest=?`, domain, digest); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return bad
	}
	if _, err = tx.ExecContext(ctx, `UPDATE blob_products SET verified=1 WHERE domain_id=? AND digest=?`, domain, digest); err != nil {
		return err
	}
	return tx.Commit()
}

func hashChunks(ctx context.Context, q *sql.Tx, domain, digest string, length uint64, h hash.Hash) error {
	rows, err := q.QueryContext(ctx, `SELECT offset,data FROM blob_chunks WHERE domain_id=? AND digest=? ORDER BY offset`, domain, digest)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var at uint64
	for rows.Next() {
		var off uint64
		var b []byte
		if err = rows.Scan(&off, &b); err != nil {
			return err
		}
		if off != at || len(b) == 0 || len(b) > maxBlobChunk || uint64(len(b)) > length-at {
			return ErrInvalidStore
		}
		_, _ = h.Write(b)
		at += uint64(len(b))
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if at != length {
		return ErrInvalidStore
	}
	return nil
}

// promoteBlobsTx is reserved for a successful terminal fold in the caller's
// transaction. Step 4's only admitted operation has no blob inputs, so it
// never calls this for a nonempty set. No standalone promotion API exists.
func promoteBlobsTx(ctx context.Context, tx *sql.Tx, domain string, position uint64, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	var command []byte
	var first, last sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT s.command,r.first_position,r.last_position FROM authority_events e JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id WHERE e.domain_id=? AND e.position=? AND r.result_code='result.succeeded'`, domain, position).Scan(&command, &first, &last)
	if err != nil || !first.Valid || !last.Valid || uint64(first.Int64) > position || uint64(last.Int64) < position {
		return ErrFenced
	}
	lengths, err := commandBlobLengths(command)
	if err != nil {
		return err
	}
	if len(lengths) != len(refs) {
		return ErrFenced
	}
	for _, digest := range refs {
		length, ok := lengths[digest]
		if !ok {
			return ErrFenced
		}
		delete(lengths, digest)
		var verified int
		var stored uint64
		if err := tx.QueryRowContext(ctx, `SELECT verified,byte_length FROM blob_products WHERE domain_id=? AND digest=?`, domain, digest).Scan(&verified, &stored); err != nil || verified != 1 || stored != length {
			return ErrBlobAbsent
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_references VALUES(?,?,?)`, domain, digest, position); err != nil {
			return err
		}
	}
	if len(lengths) != 0 {
		return ErrFenced
	}
	return nil
}

func commandBlobLengths(command []byte) (map[string]uint64, error) {
	var fields map[string]cbor.RawMessage
	if err := canonicalDecode(command, &fields); err != nil {
		return nil, ErrInvalidStore
	}
	var declared []struct {
		Digest string `cbor:"digest"`
		Length uint64 `cbor:"byte_length"`
	}
	if err := artifactDecoder.Unmarshal(fields["blobs"], &declared); err != nil {
		return nil, ErrInvalidStore
	}
	lengths := make(map[string]uint64, len(declared))
	for _, blob := range declared {
		if !validDigest(blob.Digest) || blob.Length > maxBlobLength {
			return nil, ErrInvalidStore
		}
		if prior, ok := lengths[blob.Digest]; ok && prior != blob.Length {
			return nil, ErrInvalidStore
		}
		lengths[blob.Digest] = blob.Length
	}
	return lengths, nil
}

// BlobRange returns authenticated manifest-scoped bytes and their independent
// range digest. It never claims Environment hydration or a durable local pin.
func (s *Store) BlobRange(ctx context.Context, domain string, epoch uint64, snapshotID, manifestDigest, digest string, length, offset, count uint64, now time.Time) ([]byte, string, error) {
	if count > maxBlobChunk || offset > math.MaxInt64 || count > math.MaxInt64 || offset > math.MaxUint64-count {
		return nil, "", ErrBlobRange
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, "", ErrInvalidStore
	}
	var storedDomain, storedManifest string
	var storedEpoch uint64
	var expiry int64
	var expected uint64
	err := s.db.QueryRowContext(ctx, `SELECT s.domain_id,s.epoch,s.manifest_digest,s.expires_at,e.byte_length FROM snapshots s JOIN snapshot_entries e ON e.snapshot_id=s.snapshot_id JOIN domains d ON d.domain_id=s.domain_id AND d.active_epoch=s.epoch WHERE s.snapshot_id=? AND e.digest=?`, snapshotID, digest).Scan(&storedDomain, &storedEpoch, &storedManifest, &expiry, &expected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrBlobAbsent
	}
	if err != nil {
		return nil, "", err
	}
	if domain != storedDomain || epoch != storedEpoch {
		return nil, "", ErrFenced
	}
	if expiry <= now.UnixNano() {
		return nil, "", ErrSnapshotExpired
	}
	if storedManifest != manifestDigest {
		return nil, "", ErrManifestMismatch
	}
	if expected != length || offset+count > expected {
		return nil, "", ErrBlobRange
	}
	rows, err := s.db.QueryContext(ctx, `SELECT offset,data FROM blob_chunks WHERE domain_id=? AND digest=? ORDER BY offset`, domain, digest)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	result := make([]byte, 0, count)
	covered := offset
	for rows.Next() {
		var start uint64
		var b []byte
		if err = rows.Scan(&start, &b); err != nil {
			return nil, "", err
		}
		if start >= offset+count {
			break
		}
		end := start + uint64(len(b))
		if end <= offset {
			continue
		}
		lo, hi := max(start, offset), min(end, offset+count)
		if lo != covered {
			return nil, "", io.ErrUnexpectedEOF
		}
		result = append(result, b[lo-start:hi-start]...)
		covered = hi
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	if uint64(len(result)) != count {
		return nil, "", io.ErrUnexpectedEOF
	}
	return result, digestBytes(result), nil
}

// CollectExpired removes only unreferenced temporary products and expired
// read/transfer resources. Referenced bytes and durable receipt/event truth
// are never garbage-collected here.
func (s *Store) CollectExpired(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stamp := now.UnixNano()
	for _, query := range []string{
		`DELETE FROM snapshots WHERE expires_at<=?`,
		`DELETE FROM blob_products WHERE expires_at<=? AND NOT EXISTS(SELECT 1 FROM blob_references r WHERE r.domain_id=blob_products.domain_id AND r.digest=blob_products.digest)`,
	} {
		if _, err = tx.ExecContext(ctx, query, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}
