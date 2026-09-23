package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func checkV3Root(root string) error {
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return checkSchemaVersion(db, 3)
}

func completedMatter(t *testing.T, s *Store, peerTime time.Time, seq uint64, id, event, locator string, peer fixturePeer) {
	t.Helper()
	c := matterCommand(id, seq, locator)
	h := hashCommand(t, c)
	status, err := s.SubmitCommand(context.Background(), c, h, peer.state, peerTime)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit %d: %+v %v", seq, status, err)
	}
	if _, err = s.CompleteCommand(context.Background(), status.Owner, success(id, locator), id, event, peerTime, signWith(peer.key)); err != nil {
		t.Fatalf("complete %d: %v", seq, err)
	}
}

type fixturePeer struct {
	state tls.ConnectionState
	key   ed25519.PrivateKey
}

func TestStep6SnapshotDeltaRaceRollbackAndReopen(t *testing.T) {
	s, root, state, key, now := commandFixture(t)
	peer := fixturePeer{state, key}
	ctx := context.Background()
	completedMatter(t, s, now, 1, domainB, repoB, "bravo", peer)
	completedMatter(t, s, now, 2, repoC, repoC, "alpha", peer) // different immutable item tie/order
	var first, second []byte
	if err := s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND position=1`, domainA).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND position=2`, domainA).Scan(&second); err != nil {
		t.Fatal(err)
	}
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	for _, record := range [][]byte{first, second} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record)))
		input := append([]byte("wipd/event-prefix-step/v1\x00"), chain[:]...)
		input = append(append(input, length[:]...), record...)
		chain = sha256.Sum256(input)
	}
	// The transaction-scoped Step 5 hook cannot independently commit a pin.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := pinSnapshotTx(ctx, tx, domainA, 7, emptyAnchor(), domainB, now, 5*time.Minute, nil)
	if err != nil || pin.Delta.End.EventCount != 2 || pin.Delta.End.Digest != "sha256:"+hex.EncodeToString(chain[:]) || len(pin.Delta.Events) != 2 || !bytes.Equal(pin.Delta.Events[1].Record, second) {
		t.Fatalf("pin: %+v %v", pin.Delta, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM snapshots WHERE snapshot_id=?`, domainB).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial pin: %d %v", count, err)
	}
	if _, err = s.PinSnapshot(ctx, domainA, 7, PrefixAnchor{EventCount: 1, EventID: repoB, Digest: emptyAnchor().Digest}, domainB, now, 5*time.Minute); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("accepted wrong anchor: %v", err)
	}
	pin, err = s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), domainB, now, 5*time.Minute)
	if err != nil || len(pin.Items) != 2 || pin.Items[0].ID != repoC || pin.Items[1].ID != domainB || pin.Manifest.AsOf != pin.Delta.End {
		t.Fatalf("pinned view: %+v %v", pin, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); completedMatter(t, s, now, 3, grantA, grantA, "charlie", peer) }()
	wg.Wait()
	page, complete, err := s.SnapshotPage(ctx, domainA, 7, pin.ID, 0, 1, now)
	if err != nil || complete || len(page) != 1 || page[0].ID != repoC {
		t.Fatalf("page 1: %+v %v %v", page, complete, err)
	}
	page, complete, err = s.SnapshotPage(ctx, domainA, 7, pin.ID, 1, 1, now)
	if err != nil || !complete || len(page) != 1 || page[0].ID != domainB {
		t.Fatalf("page 2: %+v %v %v", page, complete, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen pinned view: %v", err)
	}
	defer func() { _ = s.Close() }()
	page, complete, err = s.SnapshotPage(ctx, domainA, 7, pin.ID, 0, 100, now)
	if err != nil || !complete || len(page) != 2 || page[0].ID != repoC {
		t.Fatalf("reopened page: %+v %v %v", page, complete, err)
	}
	if _, _, err = s.SnapshotPage(ctx, domainA, 7, pin.ID, 0, 1, pin.ExpiresAt); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("expired snapshot: %v", err)
	}
	if err = s.CollectExpired(ctx, pin.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SnapshotPage(ctx, domainA, 7, pin.ID, 0, 1, now); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("GC snapshot: %v", err)
	}
}

func TestStep6ConcurrentUploadAndPinVsTerminal(t *testing.T) {
	s, root, state, key, now := commandFixture(t)
	ctx := context.Background()
	peer := fixturePeer{state, key}
	content := []byte("contiguous repeated bytes")
	digest := digestBytes(content)
	if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now); err != nil {
		t.Fatal(err)
	}
	const writers = 16
	start := make(chan struct{})
	out := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			<-start
			offset, err := s.StageBlobChunk(ctx, domainA, 7, digest, 0, content)
			if err == nil && offset != uint64(len(content)) {
				err = fmt.Errorf("offset %d", offset)
			}
			out <- err
		}()
	}
	close(start)
	for i := 0; i < writers; i++ {
		if err := <-out; err != nil {
			t.Fatalf("competing same-byte chunk: %v", err)
		}
	}
	if err := s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", peer)
	command := matterCommand(repoC, 2, "beta")
	status, err := s.SubmitCommand(ctx, command, hashCommand(t, command), state, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("pending fold: %v", err)
	}
	gate := make(chan struct{})
	type result struct {
		pin PinnedSnapshot
		err error
	}
	pins := make(chan result, 1)
	folds := make(chan error, 1)
	go func() {
		<-gate
		pin, e := s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), grantA, now, time.Minute)
		pins <- result{pin, e}
	}()
	go func() {
		<-gate
		_, e := s.CompleteCommand(ctx, status.Owner, success(repoC, "beta"), repoC, repoC, now, signWith(key))
		folds <- e
	}()
	close(gate)
	p := <-pins
	if p.err != nil {
		t.Fatal(p.err)
	}
	if err := <-folds; err != nil {
		t.Fatal(err)
	}
	if p.pin.Delta.End.EventCount != 1 && p.pin.Delta.End.EventCount != 2 {
		t.Fatalf("torn as-of: %+v", p.pin.Delta)
	}
	if uint64(len(p.pin.Delta.Events)) != p.pin.Delta.End.EventCount || uint64(len(p.pin.Items)) != p.pin.Delta.End.EventCount {
		t.Fatal("partial snapshot during terminal commit")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("race reopen: %v", err)
	}
	defer func() { _ = s.Close() }()
	items, complete, err := s.SnapshotPage(ctx, domainA, 7, p.pin.ID, 0, 100, now)
	if err != nil || !complete || uint64(len(items)) != p.pin.Delta.End.EventCount {
		t.Fatalf("race changed pinned view: %+v %v", items, err)
	}
}

func TestStep6SealedBlobVectorsAgainstSQLite(t *testing.T) {
	data, err := os.ReadFile("../../docs/wipd/read-transfer-snapshot-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		RawBlob struct {
			Content string `json:"content_hex"`
			Digest  string `json:"digest"`
			Length  uint64 `json:"byte_length"`
		} `json:"blob"`
		Manifest struct {
			Entries []struct {
				Digest      string `json:"digest"`
				ByteLength  uint64 `json:"byte_length"`
				Requirement string `json:"requirement"`
			} `json:"entries"`
			Digest string `json:"manifest_digest"`
		} `json:"manifest"`
	}
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	content, err := hex.DecodeString(vector.RawBlob.Content)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(content)) != vector.RawBlob.Length || digestBytes(content) != vector.RawBlob.Digest {
		t.Fatal("fixture mismatch")
	}
	entries := make([]BlobManifestEntry, 0, len(vector.Manifest.Entries))
	for _, e := range vector.Manifest.Entries {
		entries = append(entries, BlobManifestEntry{e.Digest, e.ByteLength, e.Requirement})
	}
	manifest, err := manifestChain(entries)
	if err != nil || manifest != vector.Manifest.Digest {
		t.Fatalf("sealed manifest: %s %v", manifest, err)
	}
	s, root := fresh(t)
	d, _ := identity(domainA, 7)
	if err = s.BootstrapDomain(context.Background(), d, repoA); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	upload, err := s.StartBlob(ctx, domainA, 7, vector.RawBlob.Digest, vector.RawBlob.Length, now)
	if err != nil || upload.Offset != 0 || upload.Available {
		t.Fatalf("start: %+v %v", upload, err)
	}
	if _, err = s.StageBlobChunk(ctx, domainA, 7, vector.RawBlob.Digest, 5, content[5:]); !errors.Is(err, ErrBlobOffset) {
		t.Fatalf("gap: %v", err)
	}
	if offset, e := s.StageBlobChunk(ctx, domainA, 7, vector.RawBlob.Digest, 0, content[:5]); e != nil || offset != 5 {
		t.Fatalf("first chunk: %d %v", offset, e)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("partial reopen: %v", err)
	}
	defer func() { _ = s.Close() }()
	upload, err = s.StartBlob(ctx, domainA, 7, vector.RawBlob.Digest, vector.RawBlob.Length, now)
	if err != nil || upload.Offset != 5 {
		t.Fatalf("resume: %+v %v", upload, err)
	}
	if offset, e := s.StageBlobChunk(ctx, domainA, 7, vector.RawBlob.Digest, 0, content[:5]); e != nil || offset != 5 {
		t.Fatalf("duplicate chunk: %d %v", offset, e)
	}
	if _, e := s.StageBlobChunk(ctx, domainA, 7, vector.RawBlob.Digest, 0, []byte("wrong")); !errors.Is(e, ErrBlobOffset) {
		t.Fatalf("mismatched replay: %v", e)
	}
	if offset, e := s.StageBlobChunk(ctx, domainA, 7, vector.RawBlob.Digest, 5, content[5:]); e != nil || offset != 14 {
		t.Fatalf("resume chunk: %d %v", offset, e)
	}
	if err = s.FinishBlob(ctx, domainA, 7, vector.RawBlob.Digest); err != nil {
		t.Fatal(err)
	}
	upload, err = s.StartBlob(ctx, domainA, 7, vector.RawBlob.Digest, 14, now)
	if err != nil || !upload.Available || upload.Offset != 14 {
		t.Fatalf("idempotent staged: %+v %v", upload, err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, vector.RawBlob.Digest, 13, now); !errors.Is(err, ErrBlobLength) {
		t.Fatalf("wrong known length: %v", err)
	}
	var refs int
	if err = s.db.QueryRow(`SELECT count(*) FROM blob_references`).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("staging promoted: %d %v", refs, err)
	}
	bad := digest('a')
	if _, err = s.StartBlob(ctx, domainA, 7, bad, 14, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StageBlobChunk(ctx, domainA, 7, bad, 0, content); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishBlob(ctx, domainA, 7, bad); !errors.Is(err, ErrBlobDigest) {
		t.Fatalf("wrong digest: %v", err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, digest('b'), 14, now); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishBlob(ctx, domainA, 7, digest('b')); !errors.Is(err, ErrBlobLength) {
		t.Fatalf("wrong length: %v", err)
	}
	if err = s.CollectExpired(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM blob_products`).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("temporary retention: %d %v", refs, err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, digest('c'), maxBlobLength, now); err != nil {
		t.Fatalf("absolute blob limit: %v", err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, digest('d'), 1, now); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("unbounded staging: %v", err)
	}
	if err = s.CollectExpired(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, digest('d'), 1, now); err != nil {
		t.Fatalf("quota not released: %v", err)
	}
}

func TestStep6SealedPrefixChainVectors(t *testing.T) {
	data, err := os.ReadFile("../../docs/wipd/read-transfer-snapshot-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Events []struct {
			Bytes  string `json:"bytes_hex"`
			Length uint64 `json:"byte_length"`
			Digest string `json:"prefix_digest"`
		} `json:"events"`
		Anchors struct {
			Empty struct {
				Digest string `json:"prefix_digest"`
			} `json:"empty"`
			Three struct {
				Digest string `json:"prefix_digest"`
			} `json:"three"`
		} `json:"anchors"`
		Seed struct {
			Count  uint64 `json:"event_count"`
			Length uint64 `json:"event_byte_length"`
		} `json:"seed"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	if emptyAnchor().Digest != v.Anchors.Empty.Digest {
		t.Fatal("empty anchor differs from sealed vector")
	}
	var total uint64
	for _, e := range v.Events {
		record, err := hex.DecodeString(e.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if uint64(len(record)) != e.Length {
			t.Fatal("event length differs")
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(record)))
		input := append([]byte("wipd/event-prefix-step/v1\x00"), chain[:]...)
		input = append(append(input, size[:]...), record...)
		chain = sha256.Sum256(input)
		if "sha256:"+hex.EncodeToString(chain[:]) != e.Digest {
			t.Fatal("cumulative prefix differs from sealed vector")
		}
		total += e.Length
	}
	if uint64(len(v.Events)) != v.Seed.Count || total != v.Seed.Length || "sha256:"+hex.EncodeToString(chain[:]) != v.Anchors.Three.Digest {
		t.Fatal("seed closure differs from sealed vector")
	}
}

func TestStep6PinnedResourceQuota(t *testing.T) {
	s, _ := fresh(t)
	ctx := context.Background()
	d, _ := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxLiveSnapshots; i++ {
		id := domainA[:24] + fmt.Sprintf("%02X", i)
		if _, err := s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), id, now, time.Minute); err != nil {
			t.Fatalf("pin %d: %v", i, err)
		}
	}
	if _, err := s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), grantA, now, time.Minute); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("snapshot quota: %v", err)
	}
	if err := s.CollectExpired(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), grantA, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatalf("quota recovery: %v", err)
	}
}

func TestStep6ManifestRangesAndPromotionBoundary(t *testing.T) {
	data, err := os.ReadFile("../../docs/wipd/read-transfer-snapshot-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Blob struct {
			Content string `json:"content_hex"`
			Digest  string `json:"digest"`
		} `json:"blob"`
		Manifest struct {
			Digest  string `json:"manifest_digest"`
			Entries []struct {
				Digest      string `json:"digest"`
				ByteLength  uint64 `json:"byte_length"`
				Requirement string `json:"requirement"`
			} `json:"entries"`
		} `json:"manifest"`
		Hydration struct {
			Ranges []struct {
				Offset uint64 `json:"offset"`
				Length uint64 `json:"length"`
				Bytes  string `json:"bytes_hex"`
				Digest string `json:"range_digest"`
			} `json:"ranges"`
		} `json:"hydration"`
	}
	if err = json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	content, err := hex.DecodeString(v.Blob.Content)
	if err != nil {
		t.Fatal(err)
	}
	s, root, state, key, now := commandFixture(t)
	ctx := context.Background()
	peer := fixturePeer{state, key}
	for _, e := range v.Manifest.Entries {
		var b []byte
		if e.Digest == v.Blob.Digest {
			b = content
		}
		if _, err = s.StartBlob(ctx, domainA, 7, e.Digest, e.ByteLength, now); err != nil {
			t.Fatal(err)
		}
		if len(b) > 0 {
			if _, err = s.StageBlobChunk(ctx, domainA, 7, e.Digest, 0, b); err != nil {
				t.Fatal(err)
			}
		}
		if err = s.FinishBlob(ctx, domainA, 7, e.Digest); err != nil {
			t.Fatal(err)
		}
	}
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", peer)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = promoteBlobsTx(ctx, tx, domainA, 1, []string{v.Blob.Digest}); !errors.Is(err, ErrFenced) {
		t.Fatalf("undeclared Step 4 blob promoted: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM blob_references`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback promoted: %d %v", n, err)
	}
	// Synthetic test-only future blob-bearing effect: the only current
	// production operation declares no blobs, so no such reference can be
	// committed by the real terminal path. This fixture exercises the real
	// manifest/range APIs and reopen's refusal of a forged reference.
	for _, e := range v.Manifest.Entries {
		if _, err = s.db.Exec(`INSERT INTO blob_references VALUES(?,?,1)`, domainA, e.Digest); err != nil {
			t.Fatal(err)
		}
	}
	pin, err := func() (PinnedSnapshot, error) {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return PinnedSnapshot{}, e
		}
		defer func() { _ = tx.Rollback() }()
		p, e := pinSnapshotTx(ctx, tx, domainA, 7, emptyAnchor(), grantA, now, 5*time.Minute, []string{v.Manifest.Entries[1].Digest})
		if e != nil {
			return p, e
		}
		return p, tx.Commit()
	}()
	if err != nil || pin.Manifest.Digest != v.Manifest.Digest || pin.Manifest.AsOf != pin.Delta.End || len(pin.Manifest.Entries) != 2 || pin.Manifest.Entries[0].Requirement != "lazy" || pin.Manifest.Entries[1].Requirement != "pin-before-use" {
		t.Fatalf("snapshot-bound manifest: %+v %v", pin.Manifest, err)
	}
	for _, r := range v.Hydration.Ranges {
		got, digest, e := s.BlobRange(ctx, domainA, 7, pin.ID, pin.Manifest.Digest, v.Blob.Digest, 14, r.Offset, r.Length, now)
		want, _ := hex.DecodeString(r.Bytes)
		if e != nil || !bytes.Equal(got, want) || digest != r.Digest {
			t.Fatalf("range %d: %x %s %v", r.Offset, got, digest, e)
		}
	}
	if _, _, err = s.BlobRange(ctx, domainA, 7, pin.ID, pin.Manifest.Digest, v.Blob.Digest, 14, 14, 1, now); !errors.Is(err, ErrBlobRange) {
		t.Fatalf("out-of-range: %v", err)
	}
	if _, _, err = s.BlobRange(ctx, domainB, 7, pin.ID, pin.Manifest.Digest, v.Blob.Digest, 14, 0, 5, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong domain: %v", err)
	}
	if _, _, err = s.BlobRange(ctx, domainA, 7, pin.ID, digest('a'), v.Blob.Digest, 14, 0, 5, now); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("wrong manifest: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("forged undeclared reference survived reopen: %v", err)
	}
}

func TestStep6TransferPinnedResume(t *testing.T) {
	s, root, state, key, now := commandFixture(t)
	peer := fixturePeer{state, key}
	ctx := context.Background()
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", peer)
	completedMatter(t, s, now, 2, repoC, repoC, "beta", peer)
	seed, err := s.StartTransfer(ctx, domainA, 7, "seed", "wipd.store/1", emptyAnchor(), grantA, domainB, now)
	if err != nil || seed.EventCount != 2 || seed.EventByteLength == 0 || seed.Snapshot.Manifest.AsOf != seed.Snapshot.Delta.End {
		t.Fatalf("seed: %+v %v", seed, err)
	}
	first, err := s.NextTransfer(ctx, seed.Token, 1<<20, now)
	if err != nil || first.Event == nil || first.Event.EventID != repoB {
		t.Fatalf("first: %+v %v", first, err)
	}
	if _, err = s.AcknowledgeTransfer(ctx, seed.Token, digest('a'), now); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("bad ack: %v", err)
	}
	token, err := s.AcknowledgeTransfer(ctx, seed.Token, first.Anchor.Digest, now)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.NextTransfer(ctx, seed.Token, 1<<20, now)
	if err != nil || duplicate.Event == nil || duplicate.Event.EventID != first.Event.EventID {
		t.Fatalf("last boundary repeat: %+v %v", duplicate, err)
	}
	again, err := s.AcknowledgeTransfer(ctx, seed.Token, first.Anchor.Digest, now)
	if err != nil || again != token {
		t.Fatalf("idempotent ack: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen transfer: %v", err)
	}
	defer func() { _ = s.Close() }()
	second, err := s.NextTransfer(ctx, token, 1<<20, now)
	if err != nil || second.Event == nil || second.Event.EventID != repoC {
		t.Fatalf("second: %+v %v", second, err)
	}
	if _, err = s.NextTransfer(ctx, token, 1, now); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("bounded record: %v", err)
	}
	completedMatter(t, s, now, 3, grantA, grantA, "gamma", peer)
	token, err = s.AcknowledgeTransfer(ctx, token, second.Anchor.Digest, now)
	if err != nil {
		t.Fatal(err)
	}
	end, err := s.NextTransfer(ctx, token, 1<<20, now)
	if err != nil || !end.Complete || end.Anchor != seed.Snapshot.Delta.End || end.ManifestDigest != seed.Snapshot.Manifest.Digest {
		t.Fatalf("end: %+v %v", end, err)
	}
	if _, err = s.NextTransfer(ctx, token+"x", 1<<20, now); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("tampered token: %v", err)
	}
	if _, err = s.NextTransfer(ctx, seed.Token, 1<<20, now); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("old token rewind: %v", err)
	}
	if _, err = s.NextTransfer(ctx, token, 1<<20, seed.Snapshot.ExpiresAt); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("expired transfer: %v", err)
	}
	pull, err := s.StartTransfer(ctx, domainA, 7, "pull", "wipd.store/1", seed.Snapshot.Delta.End, repoA, domainA, now)
	if err != nil || pull.EventCount != 1 || pull.Snapshot.Delta.Events[0].EventID != grantA {
		t.Fatalf("pull: %+v %v", pull, err)
	}
	if _, err = s.StartTransfer(ctx, domainA, 7, "pull", "wipd.store/1", PrefixAnchor{2, repoC, digest('f')}, repoB, repoB, now); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("divergent pull: %v", err)
	}
	if err = s.ReleaseSnapshot(ctx, domainB, 7, pull.SnapshotID); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("cross-domain cancellation: %v", err)
	}
	if err = s.ReleaseSnapshot(ctx, domainA, 7, pull.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextTransfer(ctx, pull.Token, 1<<20, now); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("released transfer resumed: %v", err)
	}
}

func TestStep6V3MigrationBackupRefusalAndNoMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "authority")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "authority.db")
	db, err := connect(file, "rwc", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, install := range []func(*sql.DB) error{installBaseline, installStep3, installStep4} {
		if err = install(db); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open migrated: %v", err)
	}
	backup := filepath.Join(root, "authority-v3.backup.db")
	if err = os.WriteFile(backup, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV3(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted incomplete backup: %v", err)
	}
	after, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("modified supplied backup: %v", err)
	}
	if err = checkV3Root(root); err != nil {
		t.Fatalf("failed upgrade changed source: %v", err)
	}
	if err = os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	// A complete v3 backup of a *different* source is evidence too. The
	// migration must compare content read-only, not normalize or replace it.
	db, err = connect(file, "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(backup, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := connect(backup, "rw", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	before, err = os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	db, err = connect(file, "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := identity(domainA, 7)
	if _, err = db.Exec(`INSERT INTO domains(domain_id,owner_public_key,owner_key_id,initial_epoch,active_epoch) VALUES(?,?,?,?,?)`, d.ID, []byte(d.OwnerPublicKey), d.OwnerKeyID, 7, 7); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO repo_memberships VALUES(?,?)`, repoA, domainA); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = checkV3Root(root); err != nil {
		t.Fatalf("v3 source invalid: %v", err)
	}
	if err = UpgradeV3(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted mismatched backup: %v", err)
	}
	after, err = os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rewrote valid mismatched backup: %v", err)
	}
	if err = checkV3Root(root); err != nil {
		t.Fatalf("mismatch altered source: %v", err)
	}
	if err = os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV3(root); err != nil {
		t.Fatal(err)
	}
	s, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err = UpgradeV3(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("repeated upgrade: %v", err)
	}
}
