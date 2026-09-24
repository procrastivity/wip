package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func continuityFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, now time.Time, binding ContinuityBinding, nonce byte) []byte {
	t.Helper()
	subject := encodeTest(t, map[string]any{
		"schema":                      "wipd.activation-intent-subject/1",
		"source_authority_spki":       binding.SourceAuthoritySPKI,
		"destination_origin":          binding.DestinationOrigin,
		"destination_authority_spki":  binding.DestinationAuthoritySPKI,
		"destination_artifact_key_id": binding.DestinationArtifactKeyID,
		"next_epoch":                  d.ActiveEpoch + 1,
	})
	next := d.ActiveEpoch + 1
	return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", d.ID, d.OwnerKeyID, d.ActiveEpoch, map[string]any{
		"schema": "wipd.owner-attestation/1", "action": "activation-intent", "domain_id": d.ID,
		"current_epoch": d.ActiveEpoch, "next_epoch": next, "subject_schema": "wipd.activation-intent-subject/1",
		"subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
		"issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": false,
	})
}

func createV5Fixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "v5")
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
	for _, install := range []func(*sql.DB) error{installBaseline, installStep3, installStep4, installStep6, installStep5} {
		if err = install(db); err != nil {
			t.Fatal(err)
		}
	}
	if err = initBlobDir(root); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStep7V5UpgradeIsExplicitAndBackupValidated(t *testing.T) {
	root := createV5Fixture(t)
	if _, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("ordinary open migrated v5: %v", err)
	}
	if err := UpgradeV5(root); err != nil {
		t.Fatalf("valid v5 upgrade: %v", err)
	}
	backup, err := connect(filepath.Join(root, "authority-v5.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(backup, 5); err != nil {
		t.Fatalf("retained v5 backup: %v", err)
	}
	_ = backup.Close()
	current, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("upgraded reopen: %v", err)
	}
	_ = current.Close()
	bad := createV5Fixture(t)
	badBackup := filepath.Join(bad, "authority-v5.backup.db")
	if err = os.WriteFile(badBackup, []byte("not a verified backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(badBackup)
	if err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV5(bad); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("accepted invalid v5 backup: %v", err)
	}
	after, err := os.ReadFile(badBackup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("modified refused backup: %v", err)
	}
	db, err := connect(filepath.Join(bad, "authority.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(db, 5); err != nil {
		t.Fatalf("backup refusal mutated source: %v", err)
	}
	_ = db.Close()
}

func handoffFinalFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, binding ContinuityBinding, intent []byte, products ContinuityProducts, now time.Time, nonce byte) []byte {
	t.Helper()
	subject := encodeTest(t, map[string]any{
		"schema": "wipd.planned-handoff-subject/1", "activation_intent_digest": artifactProductDigest(intent),
		"source_authority_spki": binding.SourceAuthoritySPKI, "destination_origin": binding.DestinationOrigin,
		"destination_authority_spki": binding.DestinationAuthoritySPKI, "destination_artifact_key_id": binding.DestinationArtifactKeyID,
		"bundle_artifact_digest": products.BundleDigest, "relinquishment_artifact_digest": products.RelinquishmentDigest,
		"source_artifact_chain_head": products.RelinquishmentDigest,
	})
	next := d.ActiveEpoch + 1
	return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", d.ID, d.OwnerKeyID, d.ActiveEpoch, map[string]any{
		"schema": "wipd.owner-attestation/1", "action": "planned-handoff", "domain_id": d.ID,
		"current_epoch": d.ActiveEpoch, "next_epoch": next, "subject_schema": "wipd.planned-handoff-subject/1",
		"subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
		"issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": false,
	})
}

func destinationKeyFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, private ed25519.PrivateKey, epoch uint64, now time.Time) ([]byte, string) {
	t.Helper()
	id, err := spkiID(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	return signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", d.ID, d.OwnerKeyID, epoch, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": d.ID, "authority_epoch": epoch,
		"key_generation": uint64(1), "key_id": id, "ed25519_public_key": []byte(private.Public().(ed25519.PublicKey)),
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	}), id
}

func TestStep7QuiesceReplayReopenAndPlannedActivation(t *testing.T) {
	s, root, peer, sourceKey, now := commandFixture(t)
	ctx := context.Background()
	d, owner := identity(domainA, 7)
	destinationKey := key("step7-destination-artifact")
	cert, keyID := destinationKeyFixture(t, owner, d, destinationKey, 8, now)
	binding := ContinuityBinding{SourceAuthoritySPKI: digest('1'), DestinationOrigin: "https://destination.example", DestinationAuthoritySPKI: digest('2'), DestinationArtifactKeyID: keyID}
	priorCommand := matterCommand(domainB, 1, "before-handoff")
	priorHash := hashCommand(t, priorCommand)
	priorStatus, err := s.SubmitCommand(ctx, priorCommand, priorHash, peer, now)
	if err != nil || priorStatus.Owner == nil {
		t.Fatalf("prior submission: %+v %v", priorStatus, err)
	}
	priorTerminal, err := s.CompleteCommand(ctx, priorStatus.Owner, success(repoC, "before-handoff"), repoC, grantA, now, signWith(sourceKey))
	if err != nil || priorTerminal.Pending {
		t.Fatalf("prior terminal: %+v %v", priorTerminal, err)
	}
	intent := continuityFixture(t, owner, d, now, binding, 11)
	if _, err = s.ActivateVerifiedBundle(ctx, domainA, nil, cert, binding, now, signWith(destinationKey)); !errors.Is(err, ErrFenced) {
		t.Fatalf("activated from owner intent alone: %v", err)
	}
	products, err := s.QuiesceAndRelinquish(ctx, domainA, intent, binding, now, signWith(sourceKey))
	if err != nil || len(products.BundleManifest) == 0 || len(products.Relinquishment) == 0 || products.Prefix.EventCount != 1 {
		t.Fatalf("quiesce: %+v %v", products, err)
	}
	var bundleWrapper signedArtifact
	if err = artifactDecoder.Unmarshal(products.BundleManifest, &bundleWrapper); err != nil {
		t.Fatal(err)
	}
	var bundleFields map[string]cbor.RawMessage
	if err = canonicalDecode(bundleWrapper.Payload, &bundleFields); err != nil {
		t.Fatal(err)
	}
	var entries []cbor.RawMessage
	if err = artifactDecoder.Unmarshal(bundleFields["entries"], &entries); err != nil || len(entries) == 0 {
		t.Fatalf("bundle entries: %d %v", len(entries), err)
	}
	for _, raw := range entries {
		var fields map[string]cbor.RawMessage
		var entry bundleEntry
		if canonicalDecode(raw, &fields) != nil || !exactKeys(fields, "kind", "logical_name", "byte_length", "digest", "record_count") || artifactDecoder.Unmarshal(raw, &entry) != nil || bundleTableKinds[entry.LogicalName] != entry.Kind {
			t.Fatalf("bundle entry does not match conformance schema: %x", raw)
		}
	}
	malformed := bundlePayload{Schema: "wipd.bundle-manifest/1", Domain: domainA, Epoch: 7, StoreSchema: "wipd.store/1", BlobManifestDigest: digest('a'), Entries: []bundleEntry{{LogicalName: "domains", ByteLength: 1, Digest: digest('b')}}}
	malformed.Prefix.Count, malformed.Prefix.Digest = 0, emptyAnchor().Digest
	malformedBytes, err := artifactEncoder.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	if artifactDecoder.Unmarshal(malformedBytes, &malformed) != nil || validateBundlePayload(malformedBytes, &malformed) == nil {
		t.Fatal("accepted a bundle entry without a valid kind")
	}
	if kind := func() string {
		var a signedArtifact
		_ = artifactDecoder.Unmarshal(products.BundleManifest, &a)
		return a.Kind
	}(); kind != "bundle-manifest" {
		t.Fatalf("bundle kind %q", kind)
	}
	if err = checkStep7State(s.db); err != nil {
		t.Fatalf("quiesce reopen state: %v", err)
	}
	if err = checkStep7Closure(s.db, filepath.Dir(s.blobs)); err != nil {
		t.Fatalf("quiesce closure: %v", err)
	}
	replayedCommand, err := s.SubmitCommand(ctx, priorCommand, priorHash, peer, now)
	if err != nil || !bytes.Equal(replayedCommand.Receipt, priorTerminal.Receipt) || replayedCommand.Pending {
		t.Fatalf("safe exact command replay: %+v %v", replayedCommand, err)
	}
	replay, err := s.QuiesceAndRelinquish(ctx, domainA, intent, binding, now, signWith(sourceKey))
	if err != nil || !bytes.Equal(replay.BundleManifest, products.BundleManifest) || !bytes.Equal(replay.Relinquishment, products.Relinquishment) {
		t.Fatalf("quiesce replay changed evidence: %+v %v", replay, err)
	}
	wrongBinding := binding
	wrongBinding.DestinationOrigin = "https://wrong.example"
	if _, err = s.QuiesceAndRelinquish(ctx, domainA, intent, wrongBinding, now, signWith(sourceKey)); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong replay scope: %v", err)
	}
	if _, err = s.StartBlob(ctx, domainA, 7, digest('a'), 1, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("blob write after relinquishment: %v", err)
	}
	c := matterCommand(repoC, 2, "after-handoff")
	if _, err = s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("write after relinquishment: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen relinquished source: %v", err)
	}
	sameNonceFinal := handoffFinalFixture(t, owner, d, binding, intent, products, now, 11)
	if _, err = s.ActivateVerifiedBundle(ctx, domainA, sameNonceFinal, cert, binding, now, signWith(destinationKey)); !errors.Is(err, ErrFenced) {
		t.Fatalf("reused activation-intent nonce for final attestation: %v", err)
	}
	wrongFinalBinding := binding
	wrongFinalBinding.SourceAuthoritySPKI = digest('f')
	wrongFinal := handoffFinalFixture(t, owner, d, wrongFinalBinding, intent, products, now, 12)
	if _, err = s.ActivateVerifiedBundle(ctx, domainA, wrongFinal, cert, binding, now, signWith(destinationKey)); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("accepted mismatched final scope: %v", err)
	}
	final := handoffFinalFixture(t, owner, d, binding, intent, products, now, 12)
	activation, err := s.ActivateVerifiedBundle(ctx, domainA, final, cert, binding, now, signWith(destinationKey))
	if err != nil || activation.Epoch != 8 || activation.Prefix.EventCount != 1 {
		t.Fatalf("activation: %+v %v", activation, err)
	}
	got, err := s.LookupDomain(ctx, domainA)
	if err != nil || got.ActiveEpoch != 8 {
		t.Fatalf("epoch promotion: %+v %v", got, err)
	}
	retry, err := s.ActivateVerifiedBundle(ctx, domainA, final, cert, binding, now, signWith(destinationKey))
	if err != nil || !bytes.Equal(retry.Artifact, activation.Artifact) {
		t.Fatalf("activation replay: %+v %v", retry, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("activated reopen: %v", err)
	}
	d2, owner := identity(domainA, 8)
	caPrivate2 := key("step7-epoch-eight-ca")
	caDER2 := caFixture(t, caPrivate2, now)
	caKeyID2, _ := spkiID(caPrivate2.Public())
	delegation2 := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainA, d2.OwnerKeyID, 8, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainA, "authority_epoch": uint64(8), "owner_key_id": d2.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caKeyID2, "ca_certificate_der": caDER2,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err = reopened.InstallEnvironmentCA(ctx, domainA, delegation2, now); err != nil {
		t.Fatalf("epoch-eight CA delegation: %v", err)
	}
	leafPrivate2 := key("step7-epoch-eight-environment")
	csr2 := csrFixture(t, leafPrivate2, "Epoch Eight Environment")
	leaf2 := leafFixture(t, leafPrivate2, caPrivate2, caDER2, domainA, envB, d2.OwnerKeyID, 8, now, 201)
	grant2 := grantFixture(t, owner, d2, "environment-enroll", claimTestID(203), envB, leafPrivate2, 2)
	issued2, err := reopened.IssueEnvironmentCertificate(ctx, domainA, envB, grant2, csr2, [][]byte{leaf2, caDER2}, now)
	if err != nil {
		t.Fatalf("epoch-eight environment enrollment: %v", err)
	}
	peer2 := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, issued2.Chain[0]), mustCert(t, issued2.Chain[1])}}
	epochEightCommand := matterCommand(domainB, 1, "epoch-eight-activity")
	epochEightCommand.ID = claimTestID(300)
	epochEightCommand.CorrelationCommandID = epochEightCommand.ID
	epochEightCommand.EnvironmentID = envB
	epochEightCommand.ExpectedAuthorityEpoch = 8
	epochEightHash := hashCommand(t, epochEightCommand)
	epochEightStatus, err := reopened.SubmitCommand(ctx, epochEightCommand, epochEightHash, peer2, now)
	if err != nil || epochEightStatus.Owner == nil {
		t.Fatalf("epoch-eight submission: %+v %v", epochEightStatus, err)
	}
	epochEightTerminal, err := reopened.CompleteCommand(ctx, epochEightStatus.Owner, success(repoB, "epoch-eight-activity"), repoB, "70000000000000000000000000", now, signWith(destinationKey))
	if err != nil || epochEightTerminal.Pending {
		t.Fatalf("epoch-eight terminal: %+v %v", epochEightTerminal, err)
	}
	destinationKey2 := key("step7-second-destination-artifact")
	cert2, keyID2 := destinationKeyFixture(t, owner, d2, destinationKey2, 9, now)
	binding2 := ContinuityBinding{SourceAuthoritySPKI: digest('5'), DestinationOrigin: "https://destination-two.example", DestinationAuthoritySPKI: digest('6'), DestinationArtifactKeyID: keyID2}
	intent2 := continuityFixture(t, owner, d2, now, binding2, 13)
	products2, err := reopened.QuiesceAndRelinquish(ctx, domainA, intent2, binding2, now, signWith(destinationKey))
	if err != nil || products2.Prefix.EventCount != 2 {
		t.Fatalf("second-epoch quiesce: %+v %v", products2, err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen second-epoch quiescence: %v", err)
	}
	final2 := handoffFinalFixture(t, owner, d2, binding2, intent2, products2, now, 14)
	activation2, err := reopened.ActivateVerifiedBundle(ctx, domainA, final2, cert2, binding2, now, signWith(destinationKey2))
	if err != nil || activation2.Epoch != 9 || activation2.Prefix.EventCount != 2 {
		t.Fatalf("second epoch promotion: %+v %v", activation2, err)
	}
	if _, err = reopened.ActivateVerifiedBundle(ctx, domainA, final, cert, binding, now, signWith(destinationKey)); !errors.Is(err, ErrFenced) {
		t.Fatalf("replayed stale first promotion after second activation: %v", err)
	}
	var promotions int
	if err = reopened.db.QueryRow(`SELECT count(*) FROM epoch_promotions WHERE domain_id=?`, domainA).Scan(&promotions); err != nil || promotions != 2 {
		t.Fatalf("retained promotion history count=%d err=%v", promotions, err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	finalReopen, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen after two promotions: %v", err)
	}
	if err = finalReopen.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStep7QuiesceRejectsPendingSubmissionAndIncompleteBlob(t *testing.T) {
	s, _, peer, _, now := commandFixture(t)
	ctx := context.Background()
	d, owner := identity(domainA, 7)
	destinationKey := key("step7-blocked-destination")
	_, keyID := destinationKeyFixture(t, owner, d, destinationKey, 8, now)
	binding := ContinuityBinding{digest('3'), "https://destination.example", digest('4'), keyID}
	intent := continuityFixture(t, owner, d, now, binding, 13)
	c := matterCommand(domainB, 1, "pending")
	if _, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuiesceAndRelinquish(ctx, domainA, intent, binding, now, signWith(key("step4-artifact"))); !errors.Is(err, ErrFenced) {
		t.Fatalf("quiesced pending command: %v", err)
	}
	if _, err := s.StartBlob(ctx, domainA, 7, digest('a'), 4, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QuiesceAndRelinquish(ctx, domainA, intent, binding, now, signWith(key("step4-artifact"))); !errors.Is(err, ErrFenced) {
		t.Fatalf("quiesced incomplete blob: %v", err)
	}
}

func TestStep7QuiesceRejectsActiveClaimAndCorruptBlobBytes(t *testing.T) {
	t.Run("active claim", func(t *testing.T) {
		f := newClaimTestFixture(t)
		anchor := f.anchor(t)
		allocation := claimTestAllocation(91, anchor, 111, 112, 113)
		_, _, _, _ = f.acquire(t, 92, 2, anchor, allocation)
		d, owner := identity(domainA, 7)
		dest := key("step7-claim-destination")
		_, keyID := destinationKeyFixture(t, owner, d, dest, 8, f.now)
		binding := ContinuityBinding{digest('b'), "https://destination.example", digest('c'), keyID}
		intent := continuityFixture(t, owner, d, f.now, binding, 44)
		if _, err := f.s.QuiesceAndRelinquish(context.Background(), domainA, intent, binding, f.now, signWith(f.key)); !errors.Is(err, ErrFenced) {
			t.Fatalf("quiesced active claim: %v", err)
		}
	})
	t.Run("corrupt verified bytes", func(t *testing.T) {
		s, _, _, authorityKey, now := commandFixture(t)
		defer func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
		d, owner := identity(domainA, 7)
		dest := key("step7-corrupt-destination")
		_, keyID := destinationKeyFixture(t, owner, d, dest, 8, now)
		binding := ContinuityBinding{digest('d'), "https://destination.example", digest('e'), keyID}
		intent := continuityFixture(t, owner, d, now, binding, 45)
		content := []byte("verified but altered after fsync")
		blobDigest := digestBytes(content)
		if _, err := s.StartBlob(context.Background(), domainA, 7, blobDigest, uint64(len(content)), now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StageBlobChunk(context.Background(), domainA, 7, blobDigest, 0, content); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishBlob(context.Background(), domainA, 7, blobDigest); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(s.blobs, productName(blobDigest))
		changed := bytes.Clone(content)
		changed[0] ^= 0xff
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := s.QuiesceAndRelinquish(context.Background(), domainA, intent, binding, now, signWith(authorityKey)); !errors.Is(err, ErrInvalidStore) {
			t.Fatalf("accepted changed blob bytes: %v", err)
		}
	})
}

func TestStep7DisasterRestoreDoesNotNeedRelinquishmentAndFencesOldRecovery(t *testing.T) {
	f := newClaimTestFixture(t)
	s, root, peer, sourceKey, now := f.s, f.root, f.peer, f.key, f.now
	ctx := context.Background()
	d, owner := identity(domainA, 7)
	claimStart := f.anchor(t)
	claimAllocation := claimTestAllocation(91, claimStart, 111, 112, 113)
	_, oldGrantHash, _, _ := f.acquire(t, 92, 2, claimStart, claimAllocation)
	c := matterCommand(domainB, 3, "unknown-at-crash")
	hash := hashCommand(t, c)
	status, err := s.SubmitCommand(ctx, c, hash, peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("pending old command: %+v %v", status, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	prefix, blobDigest, entries, err := bundleClosure(ctx, tx, root, domainA, 7)
	if err != nil {
		t.Fatal(err)
	}
	head, err := currentArtifactHead(ctx, tx, domainA, 7)
	if err != nil {
		t.Fatal(err)
	}
	var eventID *string
	if prefix.EventID != "" {
		eventID = &prefix.EventID
	}
	payload := bundlePayload{Schema: "wipd.bundle-manifest/1", Domain: domainA, Epoch: 7, StoreSchema: "wipd.store/1", BlobManifestDigest: blobDigest, ArtifactHead: head, Entries: entries}
	payload.Prefix.Count, payload.Prefix.EventID, payload.Prefix.Digest = prefix.EventCount, eventID, prefix.Digest
	bundlePayloadBytes, err := artifactEncoder.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := appendSignedArtifactTx(ctx, tx, domainA, 7, "bundle-manifest", "wipd.bundle-manifest/1", bundlePayloadBytes, now, signWith(sourceKey))
	if err != nil {
		t.Fatal(err)
	}
	if err = addContinuityProduct(ctx, tx, domainA, "bundle-manifest", appended.Wrapper, appended.Digest); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	read, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	unresolved, err := unresolvedSubmissionDigest(ctx, read, domainA)
	_ = read.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveredHead, err := currentArtifactHead(ctx, current, domainA, 7)
	_ = current.Rollback()
	if err != nil || recoveredHead == nil {
		t.Fatalf("recovered chain head: %v", err)
	}
	destinationKey := key("step7-disaster-destination")
	keyWrapper, keyID := destinationKeyFixture(t, owner, d, destinationKey, 8, now)
	binding := ContinuityBinding{SourceAuthoritySPKI: digest('a'), DestinationOrigin: "https://restore.example", DestinationAuthoritySPKI: digest('b'), DestinationArtifactKeyID: keyID}
	subject := encodeTest(t, map[string]any{"schema": "wipd.disaster-restore-subject/1", "dead_source_authority_spki": binding.SourceAuthoritySPKI, "destination_origin": binding.DestinationOrigin, "destination_authority_spki": binding.DestinationAuthoritySPKI, "destination_artifact_key_id": keyID, "bundle_or_proof_digest": appended.Digest, "recovered_prefix": prefixFixture(prefix), "recovered_artifact_chain_head": *recoveredHead, "unresolved_old_commands_digest": unresolved})
	restore := signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", domainA, d.OwnerKeyID, 7, map[string]any{"schema": "wipd.owner-attestation/1", "action": "disaster-restore", "domain_id": domainA, "current_epoch": uint64(7), "next_epoch": uint64(8), "subject_schema": "wipd.disaster-restore-subject/1", "subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{31}, 16), "issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": true})
	activation, err := s.ActivateDisasterRestore(ctx, domainA, appended.Wrapper, restore, keyWrapper, binding, now, signWith(destinationKey))
	if err != nil || activation.Epoch != 8 {
		t.Fatalf("disaster activation: %+v %v", activation, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("restore reopen: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err = reopened.RecoverCommand(ctx, c, hash); !errors.Is(err, ErrFenced) {
		t.Fatalf("recovered old-epoch command: %v", err)
	}
	if _, _, err = reopened.QueryClaimGrant(ctx, domainA, claimTestID(92), oldGrantHash, 7, peer, envA, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("served old-epoch claim grant after restore: %v", err)
	}
	var batch string
	if err = reopened.db.QueryRow(`SELECT batch_id FROM claims WHERE domain_id=? AND claim_id=?`, domainA, claimAllocation.ClaimID).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	newEpochCommand := matterCommand(claimTestID(200), 1, "next-epoch-acquire")
	newEpochCommand.ExpectedAuthorityEpoch = 8
	newEpochCommand.EnvironmentSequence = 4
	newEpochBytes, err := newEpochCommand.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	newEpochHash := hashCommand(t, newEpochCommand)
	tx, err = reopened.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, domainA, newEpochCommand.ID, newEpochHash, newEpochBytes, 8, envA, 4, "claim.acquire", 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("new-epoch submission fixture: %v", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO claims(domain_id,claim_id,matter_id,claim_epoch,authority_epoch,owner_environment_id,worktree_id,batch_id,dispatch_id,acquire_command_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, domainA, claimTestID(201), f.matter, 2, 8, envA, f.worktree, batch, claimTestID(202), newEpochCommand.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("old active claim blocked a next-epoch claim: %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func prefixFixture(anchor PrefixAnchor) map[string]any {
	var id any
	if anchor.EventID != "" {
		id = anchor.EventID
	}
	return map[string]any{"event_count": anchor.EventCount, "high_water_event_id": id, "prefix_digest": anchor.Digest}
}

func migrationAuthorizationFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, now time.Time, nonce byte) []byte {
	t.Helper()
	subject := encodeTest(t, map[string]any{"schema": "wipd.migration-authorization-subject/1", "migration_id": domainB, "source_store_digest": digest('5'), "coupling_audit_digest": digest('6'), "destination_origin": "https://legacy.example", "destination_domain_id": d.ID, "destination_epoch": d.ActiveEpoch, "cutover_at": now.Format(time.RFC3339Nano)})
	return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", d.ID, d.OwnerKeyID, d.ActiveEpoch, map[string]any{"schema": "wipd.owner-attestation/1", "action": "migration-authorize", "domain_id": d.ID, "current_epoch": d.ActiveEpoch, "next_epoch": nil, "subject_schema": "wipd.migration-authorization-subject/1", "subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16), "issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": false})
}

func migrationSealFixture(t *testing.T, owner ed25519.PrivateKey, d Domain, authorization, proof []byte, now time.Time, nonce byte) []byte {
	t.Helper()
	subject := encodeTest(t, map[string]any{"schema": "wipd.migration-seal-subject/1", "migration_id": domainB, "authorization_artifact_digest": artifactProductDigest(authorization), "proof_artifact_digest": artifactProductDigest(proof), "destination_domain_id": d.ID, "destination_epoch": d.ActiveEpoch})
	return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", d.ID, d.OwnerKeyID, d.ActiveEpoch, map[string]any{"schema": "wipd.owner-attestation/1", "action": "migration-seal", "domain_id": d.ID, "current_epoch": d.ActiveEpoch, "next_epoch": nil, "subject_schema": "wipd.migration-seal-subject/1", "subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16), "issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": false})
}

func migrationProofFixture(t *testing.T, d Domain, artifactKey ed25519.PrivateKey, artifactKeyID string, now time.Time, closure string, head *string) []byte {
	t.Helper()
	anchor := emptyAnchor()
	group := map[string]any{"correlation_origin": domainB, "environment_id": envA, "environment_sequence": uint64(1), "acted_at": now.Format(time.RFC3339Nano), "request_hash": digest('7'), "event_range": map[string]any{"first_event_id": domainB, "last_event_id": domainB, "event_count": uint64(1)}}
	payload := map[string]any{"schema": "wipd.migration-proof/1", "migration_id": domainB, "source_store_schema": "legacy.store/1", "source_store_digest": digest('5'), "source_backup_digest": digest('8'), "source_high_water": prefixFixture(anchor), "coupling_audit_digest": digest('6'), "components": []any{map[string]any{"component_id": repoA, "repo_ids": []string{repoA}, "facts_digest": digest('9')}}, "selected_domain_unions": []any{[]string{repoA}}, "groups": []any{group}, "clone_bindings": []any{}, "synthetic_binding_command_id": nil, "synthetic_binding_request_hash": nil, "synthetic_binding_event_range": nil, "destination_domain_id": d.ID, "destination_epoch": d.ActiveEpoch, "destination_prefix": prefixFixture(anchor), "blob_closure_digest": closure, "artifact_chain_head": head, "rollback_fence": "refuse-after-first-destination-submission"}
	encoded := encodeTest(t, payload)
	fields := map[string]any{"schema": "wipd.signed-artifact/1", "kind": "migration-proof", "domain_id": d.ID, "authority_epoch": d.ActiveEpoch, "signer_role": "authority", "signer_key_id": artifactKeyID, "key_generation": uint64(1), "artifact_sequence": uint64(1), "previous_artifact_digest": nil, "issued_at": now.Format(time.RFC3339Nano), "payload_schema": "wipd.migration-proof/1", "payload_digest": digestBytes(encoded), "payload": encoded}
	fields["signature"] = ed25519.Sign(artifactKey, append([]byte("wipd/signed-artifact/v1\x00"), encodeTest(t, fields)...))
	return encodeTest(t, fields)
}

func TestStep7MigrationAuthorizationProofSealAndSubmissionFence(t *testing.T) {
	s, root, peer, artifactKey, now := commandFixture(t)
	ctx := context.Background()
	d, owner := identity(domainA, 7)
	artifactID, _ := spkiID(artifactKey.Public())
	authorization := migrationAuthorizationFixture(t, owner, d, now, 21)
	if err := s.AuthorizeMigration(ctx, domainA, authorization, now); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	c := matterCommand(domainB, 1, "pre-seal")
	if _, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("write before seal: %v", err)
	}
	closure, _ := manifestChain(nil)
	wrongClosure := migrationProofFixture(t, d, artifactKey, artifactID, now, digest('f'), nil)
	if err := s.RecordMigrationProof(ctx, domainA, wrongClosure, now); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("accepted caller closure unrelated to DB: %v", err)
	}
	proof := migrationProofFixture(t, d, artifactKey, artifactID, now, closure, nil)
	wrong := bytes.Clone(proof)
	wrong[len(wrong)-1] ^= 1
	if err := s.RecordMigrationProof(ctx, domainA, wrong, now); err == nil {
		t.Fatal("accepted tampered proof")
	}
	if err := s.RecordMigrationProof(ctx, domainA, proof, now); err != nil {
		t.Fatalf("record proof: %v", err)
	}
	if err := s.RecordMigrationProof(ctx, domainA, proof, now); err != nil {
		t.Fatalf("proof retry: %v", err)
	}
	if _, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("write before distinct seal: %v", err)
	}
	sameNonceSeal := migrationSealFixture(t, owner, d, authorization, proof, now, 21)
	if err := s.AcceptMigrationSeal(ctx, domainA, sameNonceSeal, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("reused owner nonce: %v", err)
	}
	wrongSealSubject := encodeTest(t, map[string]any{"schema": "wipd.migration-seal-subject/1", "migration_id": domainB, "authorization_artifact_digest": artifactProductDigest(authorization), "proof_artifact_digest": digest('f'), "destination_domain_id": domainA, "destination_epoch": uint64(7)})
	wrongSeal := signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", domainA, d.OwnerKeyID, 7, map[string]any{"schema": "wipd.owner-attestation/1", "action": "migration-seal", "domain_id": domainA, "current_epoch": uint64(7), "next_epoch": nil, "subject_schema": "wipd.migration-seal-subject/1", "subject_digest": digestBytes(wrongSealSubject), "subject": wrongSealSubject, "nonce": bytes.Repeat([]byte{23}, 16), "issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano), "loss_accepted": false})
	if err := s.AcceptMigrationSeal(ctx, domainA, wrongSeal, now); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("accepted seal for a different proof: %v", err)
	}
	seal := migrationSealFixture(t, owner, d, authorization, proof, now, 22)
	if err := s.AcceptMigrationSeal(ctx, domainA, seal, now); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := s.CheckMigrationRollback(ctx, domainA); err != nil {
		t.Fatalf("rollback fenced before first post-migration submission: %v", err)
	}
	wrongRepo := matterCommand(repoC, 1, "refused-before-submission")
	wrongRepo.Request.Context.Repo = repoB
	if _, err := s.SubmitCommand(ctx, wrongRepo, hashCommand(t, wrongRepo), peer, now); !errors.Is(err, ErrFenced) {
		t.Fatalf("bad Repo admitted: %v", err)
	}
	var fenced int
	if err := s.db.QueryRow(`SELECT rollback_fenced FROM authority_continuity WHERE domain_id=?`, domainA).Scan(&fenced); err != nil || fenced != 0 {
		t.Fatalf("validation refusal fenced rollback: %d %v", fenced, err)
	}
	c = matterCommand(repoC, 1, "first-post-seal")
	status, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("first post-seal submission: %+v %v", status, err)
	}
	if err = s.db.QueryRow(`SELECT rollback_fenced FROM authority_continuity WHERE domain_id=?`, domainA).Scan(&fenced); err != nil || fenced != 1 {
		t.Fatalf("submission did not atomically fence: %d %v", fenced, err)
	}
	var fenceCommand, fenceHash string
	var fenceEpoch uint64
	if err = s.db.QueryRow(`SELECT rollback_command_id,rollback_request_hash,rollback_epoch FROM authority_continuity WHERE domain_id=?`, domainA).Scan(&fenceCommand, &fenceHash, &fenceEpoch); err != nil || fenceCommand != c.ID || fenceHash != hashCommand(t, c) || fenceEpoch != c.ExpectedAuthorityEpoch {
		t.Fatalf("wrong first-submission fence identity: %s %s %d %v", fenceCommand, fenceHash, fenceEpoch, err)
	}
	if err = s.CheckMigrationRollback(ctx, domainA); !errors.Is(err, ErrMigrationRollbackForbidden) || err.Error() != "migration.rollback-forbidden" {
		t.Fatalf("post-submission rollback result: %v", err)
	}
	replay, err := s.SubmitCommand(ctx, c, hashCommand(t, c), peer, now)
	if err != nil || !replay.Pending {
		t.Fatalf("same ID/hash replay after fence: %+v %v", replay, err)
	}
	failed := operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemUnknownClone, Message: "not eligible"}}
	if _, err = s.CompleteCommand(ctx, status.Owner, failed, "", "", now, signWith(artifactKey)); err != nil {
		t.Fatalf("terminal after fence: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("migration reopen: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err = reopened.db.QueryRow(`SELECT rollback_fenced FROM authority_continuity WHERE domain_id=?`, domainA).Scan(&fenced); err != nil || fenced != 1 {
		t.Fatalf("reopen fence: %d %v", fenced, err)
	}
	if err = reopened.CheckMigrationRollback(ctx, domainA); !errors.Is(err, ErrMigrationRollbackForbidden) {
		t.Fatalf("reopen lost rollback prohibition: %v", err)
	}
}
