package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Prose lives in the store.
//
// D36 makes the store the source of truth for all content including prose, and
// MODEL §3.1 is explicit that one SQLite database holds everything — so prose
// bytes are in the store by default, not in a column on the entity row and not
// at a path the store merely points at. Every markdown rendering is a projection
// of this, never parsed back (D36, D40).
//
// The one accommodation is size. Above SpillThreshold a content row spills its
// bytes to a sidecar file the store owns and keeps the reference — PLAN 1.2's
// "the store owns the reference either way," for the case it names: a 40 MB CI
// log that would otherwise bloat the database. Spill is transparent to readers
// and is an escape hatch, not a second storage model.

// SpillThreshold is the byte length above which content spills to a sidecar
// file. One mebibyte: comfortably above any Brief, Workplan or findings list a
// person or an agent writes, and comfortably below the ingested-log case.
const SpillThreshold = 1 << 20

// ContentDraft prepares a content write: it mints the content row's identity,
// hashes the bytes, decides whether they spill, and returns the Draft the caller
// hands back from its decide function.
//
// Which event type it is follows from the kind and is not the caller's choice:
// create-once kinds (Brief, Workplan, body) are `content.created`, and findings
// accumulate through `content.appended` (the `schema` Brief §B).
//
// A spilled blob is written before the transaction commits, so a rolled-back
// command can leave an unreferenced file behind. That is deliberate — the
// alternative is a file the store has already promised to have — and it is why
// the reaping of unreferenced blobs belongs with `clean`'s crash orphans.
func (t *Tx) ContentDraft(node string, kind ContentKind, data []byte) (Draft, error) {
	switch kind {
	case KindBrief, KindWorkplan, KindBody, KindFindings:
	default:
		return Draft{}, fmt.Errorf("store: %q is not a content kind", kind)
	}

	sum := sha256.Sum256(data)
	written := ContentWritten{
		Kind:    kind,
		Content: t.NewID(),
		ByteLen: int64(len(data)),
		SHA256:  hex.EncodeToString(sum[:]),
	}
	eventType := TypeContentCreated
	if kind.AppendOnlyKind() {
		eventType = TypeContentAppended
	}

	if len(data) > SpillThreshold {
		if err := t.store.writeBlob(written.Content, data); err != nil {
			return Draft{}, err
		}
		written.BlobRef = written.Content
	} else {
		// A nil slice and an empty one are the same content object — zero bytes —
		// and the payload has to say so with a value rather than an absence, because
		// an absent `bytes` is how a spilled payload says its bytes are elsewhere.
		if data == nil {
			data = []byte{}
		}
		written.Bytes = data
	}

	return Draft{Type: eventType, Subject: node, Payload: written}, nil
}

// writeBlob puts spilled bytes in the store's sidecar directory.
func (s *Store) writeBlob(ref string, data []byte) error {
	if err := os.MkdirAll(s.blobDir, 0o700); err != nil {
		return fmt.Errorf("store: preparing the blob directory %s: %w", s.blobDir, err)
	}
	path := filepath.Join(s.blobDir, ref)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("store: spilling content to %s: %w", path, err)
	}
	return nil
}

// insertContent projects content.created and content.appended. Both are an
// insert: a create-once kind is one segment, enforced by a unique index rather
// than by the verb remembering, and an append kind accumulates segments ordered
// by their own identities.
func insertContent(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p ContentWritten
	if err := decode(ev, &p); err != nil {
		return err
	}
	if len(p.Content) != IDLen {
		return fmt.Errorf("store: %s names content %q, which is not an identity", ev.Type, p.Content)
	}
	if (p.Bytes == nil) == (p.BlobRef == "") {
		return fmt.Errorf("store: %s must carry either its bytes or a blob reference, not both and not neither", ev.Type)
	}
	if p.Kind.AppendOnlyKind() != (ev.Type == TypeContentAppended) {
		return fmt.Errorf("store: kind %q does not belong on %s", p.Kind, ev.Type)
	}
	// A row the store cannot read back is a row it should not have written. For
	// in-store bytes the recorded length and digest are checkable right here, and
	// checking them leaves SegmentBytes' verification saying something about the
	// sidecar file — the one fact the log does not carry — rather than papering
	// over a payload that contradicted itself.
	if p.Bytes != nil {
		if int64(len(p.Bytes)) != p.ByteLen {
			return fmt.Errorf("store: %s carries %d bytes and records %d", ev.Type, len(p.Bytes), p.ByteLen)
		}
		sum := sha256.Sum256(p.Bytes)
		if got := hex.EncodeToString(sum[:]); got != p.SHA256 {
			return fmt.Errorf("store: %s carries bytes hashing to %s and records %s", ev.Type, got, p.SHA256)
		}
	}

	var bytesArg any
	if p.Bytes != nil {
		bytesArg = p.Bytes
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO content (id, node, kind, bytes, blob_ref, byte_len, sha256, birth_event, last_event)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Content, ev.Subject, string(p.Kind), bytesArg, nullable(p.BlobRef),
		p.ByteLen, p.SHA256, ev.ID, ev.ID)
	if err != nil {
		return contentRefusal(ctx, tx, ev, p, err)
	}
	return nil
}

// contentRefusal attributes a refused content insert to the guard that fired.
//
// The one refusal an ordinary caller can provoke is a second create-once segment,
// and SQLite reports it as `UNIQUE constraint failed: content.node, content.kind`
// — the columns of an index whose *name* and partial predicate carry the whole
// meaning, and which a reader of the error has no way to identify. Every other
// refusal in this store says which rule it is; this one now does too, and the
// index is still what enforces it.
func contentRefusal(ctx context.Context, tx *sql.Tx, ev Event, p ContentWritten, cause error) error {
	if !p.Kind.AppendOnlyKind() {
		var existing string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM content WHERE node = ? AND kind = ? AND tombstone_event IS NULL`,
			ev.Subject, string(p.Kind)).Scan(&existing); err == nil {
			return fmt.Errorf(
				"store: project %s: content_create_once: %s already has live %s content (%s), which is written once and never rewritten: %w",
				ev.Type, ev.Subject, p.Kind, existing, cause)
		}
	}
	return fmt.Errorf("store: project %s: %w", ev.Type, cause)
}

// ContentSegment is one stored content object.
type ContentSegment struct {
	ID      string
	Node    string
	Kind    ContentKind
	ByteLen int64
	SHA256  string
	// Spilled reports that the bytes live in a sidecar file rather than in the
	// database. Readers do not need to care: Bytes resolves either.
	Spilled bool
}

// ContentSegments lists a node's live content of one kind, in write order.
func (v View) ContentSegments(ctx context.Context, node string, kind ContentKind) ([]ContentSegment, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT id, node, kind, byte_len, sha256, blob_ref IS NOT NULL
		 FROM   content
		 WHERE  node = ? AND kind = ? AND tombstone_event IS NULL
		 ORDER  BY id`, node, string(kind))
	if err != nil {
		return nil, fmt.Errorf("store: read %s content of %s: %w", kind, node, err)
	}
	defer func() { _ = rows.Close() }()

	var out []ContentSegment
	for rows.Next() {
		var seg ContentSegment
		if err := rows.Scan(&seg.ID, &seg.Node, &seg.Kind, &seg.ByteLen, &seg.SHA256, &seg.Spilled); err != nil {
			return nil, fmt.Errorf("store: read %s content of %s: %w", kind, node, err)
		}
		out = append(out, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read %s content of %s: %w", kind, node, err)
	}
	return out, nil
}

// SegmentBytes resolves one content segment's bytes, from the database or from
// its sidecar file, and verifies them against the length and digest the store
// recorded.
//
// The verification is what makes spill honest: the bytes of an in-store row
// cannot go missing, but a sidecar file can, and a reader must be told that
// rather than handed a short answer.
func (s *Store) SegmentBytes(ctx context.Context, id string) ([]byte, error) {
	var (
		data    []byte
		blobRef sql.NullString
		byteLen int64
		digest  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT bytes, blob_ref, byte_len, sha256 FROM content WHERE id = ?`, id).
		Scan(&data, &blobRef, &byteLen, &digest)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("store: no content %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read content %s: %w", id, err)
	}

	if blobRef.Valid {
		path := filepath.Join(s.blobDir, blobRef.String)
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("store: content %s spilled to %s and the store cannot read it back: %w", id, path, err)
		}
	}
	if int64(len(data)) != byteLen {
		return nil, fmt.Errorf("store: content %s is %d bytes; the store recorded %d", id, len(data), byteLen)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != digest {
		return nil, fmt.Errorf("store: content %s hashes to %s; the store recorded %s", id, got, digest)
	}
	return data, nil
}

// Content resolves a node's whole content of one kind: the single segment of a
// create-once kind, or every append concatenated in write order.
func (s *Store) Content(ctx context.Context, node string, kind ContentKind) ([]byte, error) {
	segments, err := s.ContentSegments(ctx, node, kind)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, seg := range segments {
		data, err := s.SegmentBytes(ctx, seg.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return out, nil
}
