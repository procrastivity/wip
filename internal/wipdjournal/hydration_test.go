package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestClaimHydrationRequiresExactVerifiedPinnedClosureAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()

	firstBytes := []byte("amber-required-blob")
	secondBytes := []byte("navy-required-content-is-longer")
	lazyBytes := []byte("not-needed-yet")
	entries := []wipdwire.BlobManifestEntry{
		{Digest: hydrationDigest(lazyBytes), ByteLength: uint64(len(lazyBytes)), Requirement: "lazy"},
		{Digest: hydrationDigest(firstBytes), ByteLength: uint64(len(firstBytes)), Requirement: "pin-before-use"},
		{Digest: hydrationDigest(secondBytes), ByteLength: uint64(len(secondBytes)), Requirement: "pin-before-use"},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Digest < entries[j].Digest })
	anchor := emptyTransferAnchor()
	manifest := hydrationManifest(testIdentity, anchor, entries)
	transfer, err := VerifyTransfer(testDomainID, testIdentity.AuthorityEpoch, anchor, anchor, nil, manifest)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallPull(ctx, mustInstallSnapshot(t, journal).Expectation(), transfer)
	if err != nil || installed.ManifestDigest != manifest.Digest {
		t.Fatalf("install complete as-of manifest: %+v, %v", installed, err)
	}

	grantID, claimID := testCommandPrefix+"96", testCommandPrefix+"97"
	status, err := journal.BeginClaimHydration(ctx, grantID, claimID, 1, manifest)
	if err != nil || status.State != "hydrating" || status.RequiredEntryCount != 2 || status.VerifiedPinnedEntryCount != 0 {
		t.Fatalf("initial claim hydration: %+v, %v", status, err)
	}
	claim := &operation.ClaimContext{ID: claimID, Epoch: "1"}
	if err = claimHydrationReady(journal.db, claim, journal.blobsDir); !errors.Is(err, ErrClaimNotReady) {
		t.Fatalf("missing required blobs allowed claim use: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = journal.HydrateClaimBlob(canceled, grantID, hydrationDigest(firstBytes), bytes.NewReader(firstBytes), int64(len(firstBytes))); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled hydration continued: %v", err)
	}
	if _, err = journal.HydrateClaimBlob(ctx, grantID, hydrationDigest(firstBytes), bytes.NewReader(firstBytes[:len(firstBytes)-1]), int64(len(firstBytes))); !errors.Is(err, ErrBlobLength) {
		t.Fatalf("truncated blob bytes accepted: %v", err)
	}
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "hydrating" || status.VerifiedPinnedEntryCount != 0 {
		t.Fatalf("truncated bytes created a durable pin: %+v, %v", status, err)
	}
	if _, err = journal.HydrateClaimBlob(ctx, grantID, hydrationDigest(firstBytes),
		bytes.NewReader(bytes.Repeat([]byte{'x'}, len(firstBytes))), int64(len(firstBytes))); !errors.Is(err, ErrBlobDigest) {
		t.Fatalf("same-length tampered bytes accepted: %v", err)
	}
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "hydrating" || status.VerifiedPinnedEntryCount != 0 {
		t.Fatalf("tampered bytes created a durable pin: %+v, %v", status, err)
	}
	if _, err = journal.HydrateClaimBlob(ctx, grantID, hydrationDigest(firstBytes), bytes.NewReader(firstBytes), int64(len(firstBytes))); err != nil {
		t.Fatal(err)
	}
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "hydrating" || status.VerifiedPinnedEntryCount != 1 {
		t.Fatalf("one of two asymmetric required entries marked ready: %+v, %v", status, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = Open(root, testIdentity)
	if err != nil {
		t.Fatalf("reopen partial hydration: %v", err)
	}
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "hydrating" || status.VerifiedPinnedEntryCount != 1 {
		t.Fatalf("partial pin did not recover exactly: %+v, %v", status, err)
	}
	if _, err = journal.HydrateClaimBlob(ctx, grantID, hydrationDigest(secondBytes), bytes.NewReader(secondBytes), int64(len(secondBytes))); err != nil {
		t.Fatal(err)
	}
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "offline-ready" || status.RequiredEntryCount != 2 || status.VerifiedPinnedEntryCount != 2 {
		t.Fatalf("complete verified closure not ready: %+v, %v", status, err)
	}
	if err = claimHydrationReady(journal.db, claim, journal.blobsDir); err != nil {
		t.Fatalf("ready claim rejected before use: %v", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = Open(root, testIdentity)
	if err != nil {
		t.Fatalf("reopen ready hydration: %v", err)
	}
	defer func() { _ = journal.Close() }()
	status, err = journal.ClaimHydrationStatus(ctx, grantID)
	if err != nil || status.State != "offline-ready" || status.VerifiedPinnedEntryCount != 2 {
		t.Fatalf("ready closure did not survive restart: %+v, %v", status, err)
	}
	if err = claimHydrationReady(journal.db, claim, journal.blobsDir); err != nil {
		t.Fatalf("recovered ready claim rejected: %v", err)
	}
	blobPath := filepath.Join(journal.blobsDir, strings.TrimPrefix(hydrationDigest(firstBytes), digestPrefix))
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(blobPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(blobPath, bytes.Repeat([]byte{'z'}, len(firstBytes)), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root, testIdentity); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("reopen accepted altered bytes behind a durable pin: %v", err)
	}
}

func mustInstallSnapshot(t *testing.T, journal *Journal) InstallSnapshot {
	t.Helper()
	snapshot, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func hydrationDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hydrationManifest(identity Identity, anchor wipdwire.PrefixAnchor, entries []wipdwire.BlobManifestEntry) wipdwire.BlobManifest {
	chain := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	for _, entry := range entries {
		digest, _ := hex.DecodeString(entry.Digest[len("sha256:"):])
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], entry.ByteLength)
		requirement := byte(0)
		if entry.Requirement == "pin-before-use" {
			requirement = 1
		}
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/blob-manifest-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(digest)
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte{requirement})
		copy(chain[:], h.Sum(nil))
	}
	return wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: identity.DomainID, Epoch: identity.AuthorityEpoch,
		AsOf: cloneTransferAnchor(anchor), Entries: append([]wipdwire.BlobManifestEntry(nil), entries...),
		Digest: "sha256:" + hex.EncodeToString(chain[:]),
	}
}

func TestEmptyRequiredHydrationIsDurablyReadyOnlyForItsInstalledManifest(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	anchor := emptyTransferAnchor()
	manifest := emptyTransferManifest(testDomainID, testIdentity.AuthorityEpoch, anchor)
	status, err := journal.BeginClaimHydration(ctx, testCommandPrefix+"98", testCommandPrefix+"99", 3, manifest)
	if err != nil || status.State != "offline-ready" || status.RequiredEntryCount != 0 {
		t.Fatalf("empty closure readiness: %+v, %v", status, err)
	}
	wrong := manifest
	wrong.Digest = hydrationDigest([]byte("not the installed manifest"))
	if _, err = journal.BeginClaimHydration(ctx, testCommandPrefix+"90", testCommandPrefix+"91", 3, wrong); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("uninstalled manifest was bound to grant: %v", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
}
