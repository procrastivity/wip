package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func TestM5LabGenesisGrantPinsIssuerAndBindsBootstrapIdentity(t *testing.T) {
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-setup-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("0123456789abcdef"), now, now.Add(10*time.Minute))

	if err := VerifyM5LabGenesisGrant(grant, pinned, domain, repoA, now.Add(9*time.Minute)); err != nil {
		t.Fatalf("valid grant before expiry: %v", err)
	}
	wrongSigner := key("m5-lab-wrong-setup-signer").Public().(ed25519.PublicKey)
	if err := VerifyM5LabGenesisGrant(grant, wrongSigner, domain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("grant verified under unpinned signer: %v", err)
	}
	if err := VerifyM5LabGenesisGrant(grant, pinned, domain, repoB, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("grant accepted a different initial Repo: %v", err)
	}
	otherOwner, _ := identity(domainB, 1)
	otherOwner.ID = domain.ID
	if err := VerifyM5LabGenesisGrant(grant, pinned, otherOwner, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("grant accepted a different owner root: %v", err)
	}
	otherDomain := domain
	otherDomain.ID = domainB
	if err := VerifyM5LabGenesisGrant(grant, pinned, otherDomain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("grant accepted a different domain: %v", err)
	}
	wrongEpoch, _ := identity(domainA, 2)
	if err := VerifyM5LabGenesisGrant(grant, pinned, wrongEpoch, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("genesis grant accepted epoch %d", wrongEpoch.ActiveEpoch)
	}
}

func TestM5LabGenesisGrantRejectsWrongScopeExpiryAndExcessLifetime(t *testing.T) {
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-expiry-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	nonce := []byte("0123456789abcdef")

	wrongScope := signM5LabGrantForTest(t, setupSigner, m5LabGenesisGrantPayload{
		Scope: "environment-enroll", DomainID: domain.ID, RepoID: repoA, Epoch: 1,
		OwnerRootSPKIDigest: domain.OwnerKeyID, Nonce: nonce,
		IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	})
	if err := VerifyM5LabGenesisGrant(wrongScope, pinned, domain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("accepted non-create-domain scope: %v", err)
	}

	valid := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", nonce, now, now.Add(10*time.Minute))
	for _, at := range []time.Time{now.Add(-time.Nanosecond), now.Add(10 * time.Minute)} {
		if err := VerifyM5LabGenesisGrant(valid, pinned, domain, repoA, at); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
			t.Errorf("grant valid at boundary %s: %v", at.Format(time.RFC3339Nano), err)
		}
	}

	tooLong := signM5LabGrantForTest(t, setupSigner, m5LabGenesisGrantPayload{
		Scope: "create-domain", DomainID: domain.ID, RepoID: repoA, Epoch: 1,
		OwnerRootSPKIDigest: domain.OwnerKeyID, Nonce: []byte("fedcba9876543210"),
		IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(10*time.Minute + time.Nanosecond).Format(time.RFC3339Nano),
	})
	if err := VerifyM5LabGenesisGrant(tooLong, pinned, domain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("accepted grant lifetime over ten minutes: %v", err)
	}
	if _, err := CreateM5LabGenesisGrant(setupSigner, domain, repoA, nonce, now, now.Add(M5LabGenesisGrantMaxLifetime+time.Nanosecond)); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("issuer created grant exceeding maximum lifetime: %v", err)
	}
}

func TestM5LabGenesisGrantRejectsUnknownPayloadAndNoncanonicalEnvelope(t *testing.T) {
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-schema-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	valid := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("schema-test-0001"), now, now.Add(time.Minute))

	var envelope m5LabGenesisGrantEnvelope
	if err := artifactDecoder.Unmarshal(valid, &envelope); err != nil {
		t.Fatal(err)
	}
	unknownPayload, err := artifactEncoder.Marshal(map[string]any{
		"scope": "create-domain", "domain_id": domain.ID, "initial_repo_id": repoA,
		"epoch": uint64(1), "owner_root_spki_digest": domain.OwnerKeyID,
		"nonce": []byte("schema-test-0001"), "issued_at": now.Format(time.RFC3339Nano),
		"expires_at": now.Add(time.Minute).Format(time.RFC3339Nano), "unexpected": "field",
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownSignature := ed25519.Sign(setupSigner, append([]byte(m5LabGenesisGrantPreimage), unknownPayload...))
	unknownGrant, err := artifactEncoder.Marshal(m5LabGenesisGrantEnvelope{Payload: unknownPayload, Signature: unknownSignature})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyM5LabGenesisGrant(unknownGrant, pinned, domain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("accepted signed unknown payload field: %v", err)
	}

	signatureKey, _ := artifactEncoder.Marshal("signature")
	signatureValue, _ := artifactEncoder.Marshal(envelope.Signature)
	payloadKey, _ := artifactEncoder.Marshal("payload")
	payloadValue, _ := artifactEncoder.Marshal(envelope.Payload)
	noncanonical := []byte{0xa2}
	for _, part := range [][]byte{signatureKey, signatureValue, payloadKey, payloadValue} {
		noncanonical = append(noncanonical, part...)
	}
	if err := VerifyM5LabGenesisGrant(noncanonical, pinned, domain, repoA, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
		t.Fatalf("accepted noncanonical grant envelope: %v", err)
	}
}

func TestM5LabGenesisGrantRefusalsDoNotMutateAuthorityState(t *testing.T) {
	store, _ := fresh(t)
	domain, _ := identity(domainA, 1)
	otherDomain, _ := identity(domainB, 1)
	setupSigner := key("m5-lab-no-effect-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	wrongPin := key("m5-lab-no-effect-wrong-pin").Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	nonce := []byte("no-effect-000001")
	valid := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", nonce, now, now.Add(time.Minute))
	wrongScope := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "environment-enroll", []byte("wrong-scope-0001"), now, now.Add(time.Minute))
	expired := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("expired-grant001"), now.Add(-time.Minute), now)

	for _, attempt := range []struct {
		name   string
		domain Domain
		repoID string
		pin    ed25519.PublicKey
		grant  []byte
	}{
		{name: "wrong issuer pin", domain: domain, repoID: repoA, pin: wrongPin, grant: valid},
		{name: "wrong scope", domain: domain, repoID: repoA, pin: pinned, grant: wrongScope},
		{name: "expired", domain: domain, repoID: repoA, pin: pinned, grant: expired},
		{name: "wrong initial Repo binding", domain: domain, repoID: repoB, pin: pinned, grant: valid},
		{name: "wrong domain binding", domain: otherDomain, repoID: repoB, pin: pinned, grant: valid},
	} {
		if err := store.BootstrapDomainWithM5LabGrant(context.Background(), attempt.domain, attempt.repoID, attempt.pin, attempt.grant, now); !errors.Is(err, ErrM5LabGenesisGrantInvalid) {
			t.Errorf("%s = %v, want grant refusal", attempt.name, err)
		}
		for query, want := range map[string]int{
			`SELECT count(*) FROM domains`:                           0,
			`SELECT count(*) FROM repo_memberships`:                  0,
			`SELECT count(*) FROM m5_lab_genesis_grant_consumptions`: 0,
		} {
			var got int
			if err := store.db.QueryRow(query).Scan(&got); err != nil || got != want {
				t.Errorf("%s left query %q at %d, %v; want %d", attempt.name, query, got, err, want)
			}
		}
	}
}

func TestM5LabGenesisGrantBootstrapPersistsIdentityHighWaterAndReplayFence(t *testing.T) {
	store, root := fresh(t)
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-replay-signer")
	pinned := append(ed25519.PublicKey(nil), setupSigner.Public().(ed25519.PublicKey)...)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("fedcba9876543210"), now, now.Add(10*time.Minute))

	if err := store.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.LookupDomain(context.Background(), domain.ID)
	if err != nil || got.ID != domain.ID || got.ActiveEpoch != 1 || got.OwnerKeyID != domain.OwnerKeyID {
		t.Fatalf("persisted domain identity = %+v, %v", got, err)
	}
	if repoDomain, err := store.RepoDomain(context.Background(), repoA); err != nil || repoDomain != domain.ID {
		t.Fatalf("persisted Repo binding = %q, %v", repoDomain, err)
	}
	anchor, err := store.CurrentPrefixAnchor(context.Background(), domain.ID)
	wantEmpty := emptyAnchor()
	if err != nil || anchor.EventCount != 0 || anchor.EventID != "" || anchor.Digest != wantEmpty.Digest {
		t.Fatalf("initial high-water = %+v, %v; want empty prefix %+v", anchor, err, wantEmpty)
	}
	var uses int
	if err := store.db.QueryRow(`SELECT count(*) FROM m5_lab_genesis_grant_consumptions`).Scan(&uses); err != nil || uses != 1 {
		t.Fatalf("grant consumption rows = %d, %v; want 1", uses, err)
	}

	if err := store.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now); !errors.Is(err, ErrM5LabGenesisGrantConsumed) {
		t.Fatalf("replayed grant = %v, want consumed", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if err := reopened.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now); !errors.Is(err, ErrM5LabGenesisGrantConsumed) {
		t.Fatalf("replay after reopen = %v, want consumed", err)
	}
	secondDomain, _ := identity(domainB, 1)
	secondGrant := issueM5LabGrantForTest(t, setupSigner, secondDomain, repoB, "create-domain", []byte("fedcba9876543210"), now, now.Add(10*time.Minute))
	if err := reopened.BootstrapDomainWithM5LabGrant(context.Background(), secondDomain, repoB, pinned, secondGrant, now); !errors.Is(err, ErrM5LabGenesisGrantConsumed) {
		t.Fatalf("second signed grant reusing consumed nonce = %v, want consumed", err)
	}
	var domains, memberships int
	for query, count := range map[string]*int{
		`SELECT count(*) FROM domains`:                           &domains,
		`SELECT count(*) FROM repo_memberships`:                  &memberships,
		`SELECT count(*) FROM m5_lab_genesis_grant_consumptions`: &uses,
	} {
		if err := reopened.db.QueryRow(query).Scan(count); err != nil {
			t.Fatal(err)
		}
	}
	if domains != 1 || memberships != 1 || uses != 1 {
		t.Fatalf("replay changed authority rows: domains=%d memberships=%d uses=%d", domains, memberships, uses)
	}
	if _, err := reopened.LookupDomain(context.Background(), domain.ID); err != nil {
		t.Fatalf("consumed replay damaged domain: %v", err)
	}
}

func TestM5LabBootstrapPreservesOfflineOwnerRootForEnrollmentGrant(t *testing.T) {
	store, _ := fresh(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	owner := key("m5-lab-retained-offline-owner-root")
	ownerPublic := owner.Public().(ed25519.PublicKey)
	ownerKeyID, err := spkiID(ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	domain := Domain{ID: domainA, OwnerPublicKey: append(ed25519.PublicKey(nil), ownerPublic...), OwnerKeyID: ownerKeyID, ActiveEpoch: 1}
	setupSigner := key("m5-lab-owner-continuity-setup")
	genesisGrant := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("owner-link-00001"), now, now.Add(time.Minute))
	if err := store.BootstrapDomainWithM5LabGrant(ctx, domain, repoA, setupSigner.Public().(ed25519.PublicKey), genesisGrant, now); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.LookupDomain(ctx, domain.ID)
	if err != nil || !bytes.Equal(persisted.OwnerPublicKey, ownerPublic) || persisted.OwnerKeyID != ownerKeyID {
		t.Fatalf("bootstrapped owner root = %+v, %v", persisted, err)
	}

	caPrivate := key("m5-lab-owner-continuity-ca")
	caDER := caFixture(t, caPrivate, now)
	caKeyID, err := spkiID(caPrivate.Public())
	if err != nil {
		t.Fatal(err)
	}
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domain.ID, ownerKeyID, 1, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domain.ID, "authority_epoch": uint64(1), "owner_key_id": ownerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caKeyID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := store.InstallEnvironmentCA(ctx, domain.ID, delegation, now); err != nil {
		t.Fatalf("owner-signed CA delegation: %v", err)
	}
	leafPrivate := key("m5-lab-owner-continuity-environment")
	csr := csrFixture(t, leafPrivate, "retained owner enrollment")
	leaf := leafFixture(t, leafPrivate, caPrivate, caDER, domain.ID, envA, ownerKeyID, 1, now, 121)
	enrollmentGrant := grantFixture(t, owner, domain, "environment-enroll", grantA, envA, leafPrivate, 0x41)
	issued, err := store.IssueEnvironmentCertificate(ctx, domain.ID, envA, enrollmentGrant, csr, [][]byte{leaf, caDER}, now)
	if err != nil || issued.DomainID != domain.ID || issued.Epoch != 1 || issued.Generation != 1 || !bytes.Equal(issued.Chain[0], leaf) {
		t.Fatalf("same offline owner root could not authorize enrollment: %+v, %v", issued, err)
	}
}

func TestM5LabGenesisGrantConsumptionAndBootstrapRollbackTogether(t *testing.T) {
	store, _ := fresh(t)
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-atomic-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("0011223344556677"), now, now.Add(time.Minute))
	if _, err := store.db.Exec(`CREATE TRIGGER fail_m5_lab_grant_insert BEFORE INSERT ON m5_lab_genesis_grant_consumptions BEGIN SELECT RAISE(ABORT,'injected grant persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now); err == nil {
		t.Fatal("bootstrap committed after grant-consumption write failed")
	}
	if _, err := store.LookupDomain(context.Background(), domain.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed transaction left domain: %v", err)
	}
	if _, err := store.RepoDomain(context.Background(), repoA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed transaction left Repo membership: %v", err)
	}
	var uses int
	if err := store.db.QueryRow(`SELECT count(*) FROM m5_lab_genesis_grant_consumptions`).Scan(&uses); err != nil || uses != 0 {
		t.Fatalf("failed transaction left %d grant uses: %v", uses, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_m5_lab_grant_insert`); err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now); err != nil {
		t.Fatalf("grant was consumed despite rollback: %v", err)
	}
}

func TestM5LabGenesisGrantConcurrentReplayHasOneWinner(t *testing.T) {
	store, _ := fresh(t)
	domain, _ := identity(domainA, 1)
	setupSigner := key("m5-lab-concurrency-signer")
	pinned := setupSigner.Public().(ed25519.PublicKey)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	grant := issueM5LabGrantForTest(t, setupSigner, domain, repoA, "create-domain", []byte("8899aabbccddeeff"), now, now.Add(time.Minute))
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- store.BootstrapDomainWithM5LabGrant(context.Background(), domain, repoA, pinned, grant, now)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if (first == nil && !errors.Is(second, ErrM5LabGenesisGrantConsumed)) || (second == nil && !errors.Is(first, ErrM5LabGenesisGrantConsumed)) {
		t.Fatalf("concurrent grant outcomes = %v and %v, want one success and one consumed", first, second)
	}
	var domains, memberships, uses int
	for query, count := range map[string]*int{
		`SELECT count(*) FROM domains`:                           &domains,
		`SELECT count(*) FROM repo_memberships`:                  &memberships,
		`SELECT count(*) FROM m5_lab_genesis_grant_consumptions`: &uses,
	} {
		if err := store.db.QueryRow(query).Scan(count); err != nil {
			t.Fatal(err)
		}
	}
	if domains != 1 || memberships != 1 || uses != 1 {
		t.Fatalf("concurrent bootstrap rows: domains=%d memberships=%d uses=%d", domains, memberships, uses)
	}
}

func issueM5LabGrantForTest(t *testing.T, signer ed25519.PrivateKey, domain Domain, repoID, scope string, nonce []byte, issued, expires time.Time) []byte {
	t.Helper()
	return signM5LabGrantForTest(t, signer, m5LabGenesisGrantPayload{
		Scope: scope, DomainID: domain.ID, RepoID: repoID, Epoch: 1,
		OwnerRootSPKIDigest: domain.OwnerKeyID, Nonce: nonce,
		IssuedAt: issued.UTC().Format(time.RFC3339Nano), ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
	})
}

func signM5LabGrantForTest(t *testing.T, signer ed25519.PrivateKey, payload m5LabGenesisGrantPayload) []byte {
	t.Helper()
	payloadBytes := encodeTest(t, payload)
	signature := ed25519.Sign(signer, append([]byte(m5LabGenesisGrantPreimage), payloadBytes...))
	return encodeTest(t, m5LabGenesisGrantEnvelope{Payload: payloadBytes, Signature: signature})
}
