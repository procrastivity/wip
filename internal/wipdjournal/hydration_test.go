package wipdjournal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestClaimHydrationRequiresExactVerifiedPinnedClosureAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")

	firstBytes := []byte("amber-required-blob")
	secondBytes := []byte("navy-required-content-is-longer")
	lazyBytes := []byte("not-needed-yet")
	entries := []wipdwire.BlobManifestEntry{
		{Digest: hydrationDigest(lazyBytes), ByteLength: uint64(len(lazyBytes)), Requirement: "lazy"},
		{Digest: hydrationDigest(firstBytes), ByteLength: uint64(len(firstBytes)), Requirement: "pin-before-use"},
		{Digest: hydrationDigest(secondBytes), ByteLength: uint64(len(secondBytes)), Requirement: "pin-before-use"},
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Digest < entries[j].Digest })
	fixture := makeHydrationGrantFixture(t, entries, 10)
	journal, err := Open(root, fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if _, err = journal.BeginClaimHydration(ctx, fixture.grantID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uninstalled acquisition grant became ready: %v", err)
	}
	installed, err := journal.InstallClaimGrant(ctx, mustInstallSnapshot(t, journal).Expectation(), fixture.grant)
	if err != nil || installed.ManifestDigest != fixture.manifest.Digest || installed.Anchor.EventCount != 3 {
		t.Fatalf("atomically install verified acquisition grant: %+v, %v", installed, err)
	}
	replayed, err := journal.InstallClaimGrant(ctx, installed.Expectation(), fixture.grant)
	if err != nil || replayed.Revision != installed.Revision || !sameTransferAnchor(replayed.Anchor, installed.Anchor) {
		t.Fatalf("exact grant installation replay changed the installed state: %+v, %v", replayed, err)
	}

	grantID, claimID := fixture.grantID, fixture.claimID
	status, err := journal.BeginClaimHydration(ctx, grantID)
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

	journal, err = Open(root, fixture.identity)
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

	journal, err = Open(root, fixture.identity)
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
	if _, err = Open(root, fixture.identity); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("reopen accepted altered bytes behind a durable pin: %v", err)
	}
}

func TestGenericPullManifestCannotCreateClaimHydrationReadiness(t *testing.T) {
	ctx := context.Background()
	fixture := makeHydrationGrantFixture(t, nil, 30)
	journal, err := Open(filepath.Join(t.TempDir(), "journal"), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()

	anchor := emptyTransferAnchor()
	manifest := emptyTransferManifest(fixture.identity.DomainID, fixture.identity.AuthorityEpoch, anchor)
	transfer, err := VerifyTransfer(fixture.identity.DomainID, fixture.identity.AuthorityEpoch, anchor, anchor, nil, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.InstallPull(ctx, mustInstallSnapshot(t, journal).Expectation(), transfer); err != nil {
		t.Fatalf("install generic complete pull: %v", err)
	}
	if _, err = journal.BeginClaimHydration(ctx, fixture.grantID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("generic installed manifest created fabricated grant readiness: %v", err)
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
	fixture := makeHydrationGrantFixture(t, nil, 40)
	journal, err := Open(root, fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BeginClaimHydration(ctx, fixture.grantID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty closure on an invented grant became ready: %v", err)
	}
	installed, err := journal.InstallClaimGrant(ctx, mustInstallSnapshot(t, journal).Expectation(), fixture.grant)
	if err != nil || installed.ManifestDigest != fixture.manifest.Digest || installed.Anchor.EventCount != 3 {
		t.Fatalf("install exact empty-closure acquisition: %+v, %v", installed, err)
	}
	status, err := journal.BeginClaimHydration(ctx, fixture.grantID)
	if err != nil || status.State != "offline-ready" || status.RequiredEntryCount != 0 {
		t.Fatalf("verified empty closure readiness: %+v, %v", status, err)
	}
	other := makeHydrationGrantFixture(t, nil, 60)
	if _, err = journal.BeginClaimHydration(ctx, other.grantID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another grant's claim was accepted: %v", err)
	}
	if _, err = journal.InstallClaimGrant(ctx, installed.Expectation(), other.grant); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("grant signed for another owner/profile was installed: %v", err)
	}
	wrongClaim := fixture.grant
	wrongClaim.claimID = testCommandPrefix + "99"
	if _, err = journal.InstallClaimGrant(ctx, installed.Expectation(), wrongClaim); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("verified receipt was rebound to another claim: %v", err)
	}
	wrongManifest := fixture.grant
	wrongManifest.manifest = emptyTransferManifest(testDomainID, testIdentity.AuthorityEpoch, emptyTransferAnchor())
	if _, err = journal.InstallClaimGrant(ctx, installed.Expectation(), wrongManifest); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("verified grant was rebound to another manifest: %v", err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = Open(root, fixture.identity)
	if err != nil {
		t.Fatalf("reopen installed empty-closure grant: %v", err)
	}
	defer func() { _ = journal.Close() }()
	status, err = journal.BeginClaimHydration(ctx, fixture.grantID)
	if err != nil || status.State != "offline-ready" || status.ClaimID != fixture.claimID || status.ClaimEpoch != 1 {
		t.Fatalf("exact verified grant did not recover: %+v, %v", status, err)
	}
	if err = claimHydrationReady(journal.db, &operation.ClaimContext{ID: fixture.claimID, Epoch: "1"}, journal.blobsDir); err != nil {
		t.Fatalf("reopened installed claim did not remain ready: %v", err)
	}
}

type hydrationGrantFixture struct {
	identity        Identity
	trust           ClaimGrantTrust
	artifactPrivate ed25519.PrivateKey
	grantID         string
	claimID         string
	manifest        wipdwire.BlobManifest
	grant           VerifiedClaimGrant
}

func makeHydrationGrantFixture(t *testing.T, entries []wipdwire.BlobManifestEntry, base int) hydrationGrantFixture {
	t.Helper()
	makeID := func(offset int) string { return testCommandPrefix + fmt.Sprintf("%02d", base+offset) }
	ownerSeed := sha256.Sum256([]byte(fmt.Sprintf("hydration owner %d", base)))
	ownerPrivate := ed25519.NewKeyFromSeed(ownerSeed[:])
	ownerPublic := ownerPrivate.Public().(ed25519.PublicKey)
	ownerDER, err := x509.MarshalPKIXPublicKey(ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := hydrationDigest(ownerDER)
	identity := testIdentity
	identity.OwnerRootSPKI = ownerID
	artifactSeed := sha256.Sum256([]byte(fmt.Sprintf("hydration artifact %d", base)))
	artifactPrivate := ed25519.NewKeyFromSeed(artifactSeed[:])
	artifactPublic := artifactPrivate.Public().(ed25519.PublicKey)
	artifactDER, err := x509.MarshalPKIXPublicKey(artifactPublic)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := hydrationDigest(artifactDER)
	const notBefore = "2020-01-01T00:00:00Z"
	const notAfter = "2035-01-01T00:00:00Z"
	const issued = "2025-01-02T03:04:05Z"
	trustTime, _ := time.Parse(time.RFC3339Nano, issued)
	certificatePayload, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": identity.DomainID, "authority_epoch": identity.AuthorityEpoch,
		"key_generation": uint64(1), "key_id": artifactID, "ed25519_public_key": []byte(artifactPublic),
		"not_before": notBefore, "not_after": notAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	certificate := signHydrationArtifact(t, map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": "authority-artifact-key", "domain_id": identity.DomainID,
		"authority_epoch": identity.AuthorityEpoch, "signer_role": "owner", "signer_key_id": ownerID,
		"key_generation": nil, "artifact_sequence": nil, "previous_artifact_digest": nil, "issued_at": issued,
		"payload_schema": "wipd.authority-artifact-key/1", "payload_digest": hydrationDigest(certificatePayload), "payload": certificatePayload,
	}, ownerPrivate)
	claimID, matterID, batchID, dispatchID, commandID := makeID(6), makeID(7), makeID(8), makeID(9), makeID(4)
	grantID := makeID(5)
	requestHash := hydrationDigest([]byte("asymmetric claim acquire request"))
	worktreeID := makeID(10)
	const eventTime = issued
	eventIDs := []string{makeID(1), makeID(2), makeID(3)}
	eventSpecs := []struct {
		kind, subject string
		payload       map[string]any
	}{
		{"batch.anonymous-created", batchID, map[string]any{"batch_id": batchID, "matter_id": matterID}},
		{"claim.acquired", claimID, map[string]any{
			"claim_id": claimID, "claim_epoch": uint64(1), "matter_id": matterID, "batch_id": batchID,
			"dispatch_id": dispatchID, "owner_environment_id": identity.EnvironmentID, "worktree_id": worktreeID,
		}},
		{"dispatch.opened", dispatchID, map[string]any{
			"dispatch_id": dispatchID, "matter_id": matterID, "batch_id": batchID, "claim_id": claimID, "worktree_id": worktreeID,
		}},
	}
	records := make([]wipdwire.EventRecord, 0, len(eventSpecs))
	for index, spec := range eventSpecs {
		record, encodeErr := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventIDs[index], "domain_id": identity.DomainID,
			"command_id": commandID, "request_hash": requestHash,
			"environment": map[string]any{"id": identity.EnvironmentID, "sequence": uint64(1)},
			"acted_at":    eventTime, "occurred_at": eventTime, "kind": spec.kind, "subject_id": spec.subject,
			"repo_id": identity.RepoID, "payload": spec.payload,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		records = append(records, wipdwire.EventRecord{EventID: eventIDs[index], Record: record})
	}
	end := hydrationEventAnchor(records)
	manifest := hydrationManifest(identity, end, entries)
	transfer, err := VerifyTransfer(identity.DomainID, identity.AuthorityEpoch, emptyTransferAnchor(), end, records, manifest)
	if err != nil {
		t.Fatalf("verify fixture acquisition prefix: %v", err)
	}
	output, err := wipdwire.EncodeCanonical(map[string]any{
		"claim": map[string]any{"id": claimID, "epoch": uint64(1)}, "matter_id": matterID, "batch_id": batchID, "dispatch_id": dispatchID,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": identity.DomainID, "authority_epoch": identity.AuthorityEpoch,
		"identity_schema": "wipd.command/1", "command_id": commandID, "request_hash": requestHash,
		"operation":       map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"environment":     map[string]any{"id": identity.EnvironmentID, "sequence": uint64(1)},
		"result":          map[string]any{"code": "result.succeeded", "output": output, "problem_code": nil},
		"accepted_events": map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[2], "event_count": uint64(3)},
	}
	start, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.claim-grant-start/1", "grant_id": grantID, "acquire_command_id": commandID,
		"acquire_request_hash": requestHash, "domain_id": identity.DomainID, "authority_epoch": identity.AuthorityEpoch,
		"owner_environment_id": identity.EnvironmentID, "claim": map[string]any{"id": claimID, "epoch": uint64(1)},
		"matter_id": matterID, "batch_id": batchID, "dispatch_id": dispatchID, "receipt": receipt,
		"prefix":               map[string]any{"start": hydrationAnchorMap(emptyTransferAnchor()), "end": hydrationAnchorMap(end)},
		"blob_manifest_digest": manifest.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantEnd, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.claim-grant-end/1", "grant_id": grantID, "verified_prefix": hydrationAnchorMap(end),
		"verified_blob_manifest_digest": manifest.Digest, "complete": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := signHydrationArtifact(t, map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": "claim-grant", "domain_id": identity.DomainID,
		"authority_epoch": identity.AuthorityEpoch, "signer_role": "authority", "signer_key_id": artifactID,
		"key_generation": uint64(1), "artifact_sequence": uint64(1), "previous_artifact_digest": nil, "issued_at": issued,
		"payload_schema": "wipd.claim-grant-start/1", "payload_digest": hydrationDigest(start), "payload": start,
	}, artifactPrivate)
	trust := ClaimGrantTrust{
		OwnerRootPublicKey: ownerPublic, OwnerRootSPKI: ownerID, VerifiedAt: trustTime,
	}
	evidence := ClaimGrantEvidence{
		ArtifactKeyCertificate: certificate, Wrapper: wrapper, Start: start, End: grantEnd, Transfer: transfer,
	}
	grant, err := VerifyClaimGrant(identity, trust, evidence)
	if err != nil {
		t.Fatalf("verify fixture signed claim grant: %v", err)
	}
	return hydrationGrantFixture{
		identity: identity, trust: trust, artifactPrivate: artifactPrivate,
		grantID: grantID, claimID: claimID, manifest: manifest, grant: grant,
	}
}

func hydrationGrantEvidenceForRepo(t *testing.T, fixture hydrationGrantFixture, repoID string) ClaimGrantEvidence {
	t.Helper()
	records := fixture.grant.transfer.Records()
	for index := range records {
		fields, err := wipdwire.DecodeCanonicalMap(records[index].Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if err != nil {
			t.Fatal(err)
		}
		fields["repo_id"] = repoID
		records[index].Record, err = wipdwire.EncodeCanonical(fields)
		if err != nil {
			t.Fatal(err)
		}
	}
	end := hydrationEventAnchor(records)
	manifest := hydrationManifest(fixture.identity, end, fixture.grant.manifest.Entries)
	transfer, err := VerifyTransfer(fixture.identity.DomainID, fixture.identity.AuthorityEpoch,
		fixture.grant.start, end, records, manifest)
	if err != nil {
		t.Fatalf("verify signed wrong-Repo transfer: %v", err)
	}
	startFields, err := wipdwire.DecodeCanonicalMap(fixture.grant.startBytes,
		"schema", "grant_id", "acquire_command_id", "acquire_request_hash", "domain_id", "authority_epoch", "owner_environment_id",
		"claim", "matter_id", "batch_id", "dispatch_id", "receipt", "prefix", "blob_manifest_digest")
	if err != nil {
		t.Fatal(err)
	}
	prefix, ok := startFields["prefix"].(map[string]any)
	if !ok {
		t.Fatalf("grant prefix has type %T", startFields["prefix"])
	}
	prefix["end"] = hydrationAnchorMap(end)
	start, err := wipdwire.EncodeCanonical(startFields)
	if err != nil {
		t.Fatal(err)
	}
	wrapperFields, err := wipdwire.DecodeCanonicalMap(fixture.grant.wrapper,
		"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence",
		"previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature")
	if err != nil {
		t.Fatal(err)
	}
	delete(wrapperFields, "signature")
	wrapperFields["payload_digest"] = hydrationDigest(start)
	wrapperFields["payload"] = start
	wrapper := signHydrationArtifact(t, wrapperFields, fixture.artifactPrivate)
	endBytes, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.claim-grant-end/1", "grant_id": fixture.grantID, "verified_prefix": hydrationAnchorMap(end),
		"verified_blob_manifest_digest": manifest.Digest, "complete": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ClaimGrantEvidence{
		ArtifactKeyCertificate: fixture.grant.artifactKeyCertificate,
		Wrapper:                wrapper, Start: start, End: endBytes, Transfer: transfer,
	}
}

func TestClaimGrantRepoIsBoundToJournalIdentity(t *testing.T) {
	fixture := makeHydrationGrantFixture(t, nil, 50)
	const otherRepoID = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	evidence := hydrationGrantEvidenceForRepo(t, fixture, otherRepoID)
	if _, err := VerifyClaimGrant(fixture.identity, fixture.trust, evidence); !errors.Is(err, errInvalidClaimGrant) {
		t.Fatalf("validly signed grant for Repo B verified for Repo A: %v", err)
	}
	otherRepoIdentity := fixture.identity
	otherRepoIdentity.RepoID = otherRepoID
	verifiedForOtherRepo, err := VerifyClaimGrant(otherRepoIdentity, fixture.trust, evidence)
	if err != nil {
		t.Fatalf("validly signed Repo B grant did not verify for Repo B: %v", err)
	}
	journal, err := Open(filepath.Join(t.TempDir(), "repo-a-journal"), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if _, err = journal.InstallClaimGrant(context.Background(), mustInstallSnapshot(t, journal).Expectation(), verifiedForOtherRepo); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("Repo B grant installed into Repo A journal: %v", err)
	}
}

func signHydrationArtifact(t *testing.T, fields map[string]any, private ed25519.PrivateKey) []byte {
	t.Helper()
	unsigned, err := wipdwire.EncodeCanonical(fields)
	if err != nil {
		t.Fatal(err)
	}
	fields["signature"] = ed25519.Sign(private, append([]byte("wipd/signed-artifact/v1\x00"), unsigned...))
	signed, err := wipdwire.EncodeCanonical(fields)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func hydrationEventAnchor(records []wipdwire.EventRecord) wipdwire.PrefixAnchor {
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	for _, record := range records {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record.Record)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(record.Record)
		copy(chain[:], h.Sum(nil))
	}
	eventID := records[len(records)-1].EventID
	return wipdwire.PrefixAnchor{EventCount: uint64(len(records)), EventID: &eventID, Digest: "sha256:" + hex.EncodeToString(chain[:])}
}

func hydrationAnchorMap(anchor wipdwire.PrefixAnchor) map[string]any {
	var eventID any
	if anchor.EventID != nil {
		eventID = *anchor.EventID
	}
	return map[string]any{"event_count": anchor.EventCount, "high_water_event_id": eventID, "prefix_digest": anchor.Digest}
}
