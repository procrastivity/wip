package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBlobFilesCrashBoundariesAndCollection(t *testing.T) {
	s, root := fresh(t)
	ctx := context.Background()
	d, _ := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	content := bytes.Repeat([]byte("different bytes across pages;"), 4000)
	digest := digestBytes(content)
	if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now); err != nil {
		t.Fatal(err)
	}
	first := content[:maxBlobChunk]
	sum, err := durableChunk(s.blobs, domainA, digest, 0, first) // crash before progress commit
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(s.blobs, chunkName(domainA, digest, 0, sum))
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("orphan before progress must not be exposed: %v", err)
	}
	upload, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now)
	if err != nil || upload.Offset != 0 || upload.Available {
		t.Fatalf("uncommitted chunk became authority progress: %+v %v", upload, err)
	}
	if err = s.CollectExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan chunk not collected: %v", err)
	}
	if off, err := s.StageBlobChunk(ctx, domainA, 7, digest, 0, first); err != nil || off != uint64(len(first)) {
		t.Fatalf("committed chunk: %d %v", off, err)
	}
	var dbBytes int
	if err = s.db.QueryRow(`SELECT length(chunk_hash) FROM blob_chunks WHERE domain_id=? AND digest=?`, domainA, digest).Scan(&dbBytes); err != nil || dbBytes != sha256.Size {
		t.Fatalf("SQLite contains payload instead of hash: %d %v", dbBytes, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("committed staged evidence not reopenable: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err = s.CollectExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(firstPath); err != nil {
		t.Fatalf("GC removed unresolved staged evidence: %v", err)
	}
	if off, err := s.StageBlobChunk(ctx, domainA, 7, digest, uint64(len(first)), content[len(first):]); err != nil || off != uint64(len(content)) {
		t.Fatalf("resume: %d %v", off, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = durableProduct(ctx, tx, s.blobs, domainA, digest, uint64(len(content))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	productPath := filepath.Join(s.blobs, productName(digest))
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("unreferenced durable product prevented reopen: %v", err)
	}
	upload, err = s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now)
	if err != nil || upload.Offset != uint64(len(content)) || upload.Available {
		t.Fatalf("uncommitted product exposed as verified: %+v %v", upload, err)
	}
	if err = s.CollectExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(productPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan verified file not collected: %v", err)
	}
	if err = s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	if err = s.CollectExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(productPath); err != nil {
		t.Fatalf("GC removed verified unexpired product: %v", err)
	}
	if _, err = os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-verification staging not collected: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("verified product did not reopen: %v", err)
	}
	upload, err = s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now)
	if err != nil || !upload.Available {
		t.Fatalf("verified state lost: %+v %v", upload, err)
	}
	entry := BlobManifestEntry{Digest: digest, ByteLength: uint64(len(content)), Requirement: "lazy"}
	manifest, err := manifestChain([]BlobManifestEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic manifest entry tests a range crossing the original 64 KiB
	// chunk boundary; Step 4 currently has no command that can adopt blobs.
	if _, err = s.db.Exec(`INSERT INTO snapshots VALUES(?,?,?,?,?,?,?,?)`, domainB, domainA, 7, 0, nil, emptyAnchor().Digest, manifest, now.Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO snapshot_entries VALUES(?,?,?,?)`, domainB, digest, len(content), "lazy"); err != nil {
		t.Fatal(err)
	}
	rangeBytes, rangeDigest, err := s.BlobRange(ctx, domainA, 7, domainB, manifest, digest, uint64(len(content)), maxBlobChunk-7, 23, now)
	want := content[maxBlobChunk-7 : maxBlobChunk+16]
	if err != nil || !bytes.Equal(rangeBytes, want) || rangeDigest != digestBytes(want) {
		t.Fatalf("cross-boundary range: %x %s %v", rangeBytes, rangeDigest, err)
	}
	if err = s.CollectExpired(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(productPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired unreferenced product not collected: %v", err)
	}
}

func TestCollectExpiredRejectsZeroClockWithoutCollection(t *testing.T) {
	s, _ := fresh(t)
	ctx := context.Background()
	d, _ := identity(domainA, 7)
	if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	data := []byte("uncommitted staged bytes")
	digest := digestBytes(data)
	if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(data)), now); err != nil {
		t.Fatal(err)
	}
	sum, err := durableChunk(s.blobs, domainA, digest, 0, data) // crash before chunk row commits
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.blobs, chunkName(domainA, digest, 0, sum))
	wantExpiry := now.Add(stagingTTL).UnixNano()
	if err := s.CollectExpired(ctx, time.Time{}); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("zero collection clock: %v", err)
	}
	var expiry int64
	var offset uint64
	if err := s.db.QueryRow(`SELECT expires_at,verified_offset FROM blob_products WHERE domain_id=? AND digest=?`, domainA, digest).Scan(&expiry, &offset); err != nil || expiry != wantExpiry || offset != 0 {
		t.Fatalf("zero clock changed staged SQL state: expiry=%d offset=%d err=%v", expiry, offset, err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("zero clock changed orphan bytes: %q %v", got, err)
	}
	if err := s.CollectExpired(ctx, now.Add(stagingTTL)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM blob_products WHERE domain_id=? AND digest=?`, domainA, digest).Scan(&count); err != nil || count != 0 {
		t.Fatalf("valid-clock expiry did not collect staged row: %d %v", count, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valid-clock expiry did not collect orphan: %v", err)
	}
}

func TestBlobReopenRejectsCorruptNamedBytes(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "staged", true: "verified"}[verified], func(t *testing.T) {
			s, root := fresh(t)
			ctx := context.Background()
			d, _ := identity(domainA, 7)
			if err := s.BootstrapDomain(ctx, d, repoA); err != nil {
				t.Fatal(err)
			}
			data := []byte("private verified byte range")
			digest := digestBytes(data)
			if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(data)), time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StageBlobChunk(ctx, domainA, 7, digest, 0, data); err != nil {
				t.Fatal(err)
			}
			name := ""
			if verified {
				if err := s.FinishBlob(ctx, domainA, 7, digest); err != nil {
					t.Fatal(err)
				}
				name = productName(digest)
			} else {
				h := sha256.Sum256(data)
				name = chunkName(domainA, digest, 0, h[:])
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "blobs", name)
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(data)), 0o400); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("reopened corrupt %s bytes: %v", name, err)
			}
		})
	}
}

func TestBlobCollectionPreservesReferencedAndPinnedProducts(t *testing.T) {
	s, _, state, key, now := commandFixture(t)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", fixturePeer{state, key})
	var files []string
	for i, content := range [][]byte{[]byte("referenced"), []byte("pinned without command adoption")} {
		digest := digestBytes(content)
		if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StageBlobChunk(ctx, domainA, 7, digest, 0, content); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishBlob(ctx, domainA, 7, digest); err != nil {
			t.Fatal(err)
		}
		files = append(files, filepath.Join(s.blobs, productName(digest)))
		if i == 0 {
			// Synthetic future blob-bearing command; FK establishes the durable
			// first position. This fixture deliberately does not test reopen.
			if _, err := s.db.Exec(`INSERT INTO blob_references VALUES(?,?,1)`, domainA, digest); err != nil {
				t.Fatal(err)
			}
		} else {
			entry := BlobManifestEntry{Digest: digest, ByteLength: uint64(len(content)), Requirement: "lazy"}
			manifest, err := manifestChain([]BlobManifestEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO snapshots VALUES(?,?,?,?,?,?,?,?)`, grantA, domainA, 7, 0, nil, emptyAnchor().Digest, manifest, now.Add(26*time.Hour).UnixNano()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO snapshot_entries VALUES(?,?,?,?)`, grantA, digest, len(content), "lazy"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.CollectExpired(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("GC deleted live referenced/pinned bytes %s: %v", path, err)
		}
	}
	if err := s.CollectExpired(ctx, now.Add(27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("GC deleted referenced bytes: %v", err)
	}
	if _, err := os.Stat(files[1]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired unreferenced pin not collected: %v", err)
	}
}

func TestTransferEpochAndContinuationClocks(t *testing.T) {
	s, root, state, key, now := commandFixture(t)
	ctx := context.Background()
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", fixturePeer{state, key})
	transfer, err := s.StartTransfer(ctx, domainA, 7, "seed", "wipd.store/1", emptyAnchor(), grantA, repoC, now)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Time{}
	if _, _, err = s.SnapshotPage(ctx, domainA, 7, transfer.SnapshotID, 0, 1, zero); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("zero snapshot clock: %v", err)
	}
	if _, _, err = s.BlobRange(ctx, domainA, 7, transfer.SnapshotID, transfer.Snapshot.Manifest.Digest, digest('a'), 1, 0, 1, zero); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("zero blob clock: %v", err)
	}
	if _, err = s.NextTransfer(ctx, transfer.Token, 1<<20, zero); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("zero next clock: %v", err)
	}
	if _, err = s.AcknowledgeTransfer(ctx, transfer.Token, digest('a'), zero); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("zero ack clock: %v", err)
	}
	if _, err = s.NextTransfer(ctx, transfer.Token, 1<<20, now); err != nil {
		t.Fatalf("zero clock changed transfer progress: %v", err)
	}
	if _, err = s.db.Exec(`INSERT INTO epoch_promotions VALUES(?,?,?,?,?)`, domainA, 7, 8, digest('a'), digest('b')); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE domains SET active_epoch=8 WHERE domain_id=?`, domainA); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NextTransfer(ctx, transfer.Token, 1<<20, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("old epoch next: %v", err)
	}
	if _, err = s.AcknowledgeTransfer(ctx, transfer.Token, transfer.Snapshot.Delta.End.Digest, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("old epoch ack: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenExisting(root); !errors.Is(openErr, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("accepted SQL-only promotion without activation artifact: %v", openErr)
	}
}

func TestBlobPromotionRepeatDigestKeepsFirstAnchor(t *testing.T) {
	s, _, state, key, now := commandFixture(t)
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	peer := fixturePeer{state, key}
	completedMatter(t, s, now, 1, domainB, repoB, "alpha", peer)
	completedMatter(t, s, now, 2, repoC, repoC, "beta", peer)
	content := []byte("identical bytes used twice")
	digest := digestBytes(content)
	if _, err := s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageBlobChunk(ctx, domainA, 7, digest, 0, content); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	// Synthetic future blob-bearing commands: current matter.create@v1 has no
	// blob inputs. Roll back the fixture so Step 4's real command truth is intact.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TRIGGER submissions_immutable`); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{domainB, repoC} {
		entries := make([]map[string]any, i+1)
		for j := range entries {
			entries[j] = map[string]any{"digest": digest, "byte_length": uint64(len(content))}
		}
		command, err := artifactEncoder.Marshal(map[string]any{"blobs": entries})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE submissions SET command=? WHERE domain_id=? AND command_id=?`, command, domainA, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = promoteBlobsTx(ctx, tx, domainA, 1, []string{digest}); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	if err = promoteBlobsTx(ctx, tx, domainA, 2, []string{digest, digest}); err != nil {
		t.Fatalf("repeat digest input and second command: %v", err)
	}
	var first, n int
	if err = tx.QueryRow(`SELECT first_position FROM blob_references WHERE domain_id=? AND digest=?`, domainA, digest).Scan(&first); err != nil || first != 1 {
		t.Fatalf("first-reference anchor moved: %d %v", first, err)
	}
	if err = tx.QueryRow(`SELECT count(*) FROM blob_references WHERE domain_id=?`, domainA).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate reference row: %d %v", n, err)
	}
	if err = promoteBlobsTx(ctx, tx, domainA, 2, []string{digestBytes([]byte("other"))}); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong digest set accepted: %v", err)
	}
}
