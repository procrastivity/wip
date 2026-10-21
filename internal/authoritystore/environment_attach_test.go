package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestEnvironmentAttachGrantRevocationIsOwnerSignedDurableAndConflictSafe(t *testing.T) {
	store, _ := fresh(t)
	defer func() { _ = store.Close() }()
	domain, owner := identity(domainA, 7)
	if err := store.BootstrapDomain(context.Background(), domain, repoA); err != nil {
		t.Fatal(err)
	}
	grantDigest := digestBytes([]byte("exact signed grant bytes"))
	makeArtifact := func(epoch uint64, digest string) []byte {
		t.Helper()
		return signedTest(t, owner, "environment-attach-grant-revocation", "wipd.environment-attach-grant-revocation/1",
			domainA, domain.OwnerKeyID, epoch, map[string]any{
				"schema": "wipd.environment-attach-grant-revocation/1", "domain_id": domainA,
				"authority_epoch": epoch, "owner_key_id": domain.OwnerKeyID,
				"grant_id": grantA, "grant_digest": digest,
			})
	}
	artifact := makeArtifact(7, grantDigest)
	if err := store.RevokeEnvironmentAttachGrant(artifact); err != nil {
		t.Fatalf("valid owner revocation rejected: %v", err)
	}
	if err := store.RevokeEnvironmentAttachGrant(artifact); err != nil {
		t.Fatalf("exact revocation replay was not idempotent: %v", err)
	}
	if err := store.RevokeEnvironmentAttachGrant(makeArtifact(7, digestBytes([]byte("different grant")))); !errors.Is(err, ErrConflict) {
		t.Fatalf("same grant ID with another exact digest = %v, want conflict", err)
	}
	if err := store.RevokeEnvironmentAttachGrant(makeArtifact(6, grantDigest)); !errors.Is(err, ErrFenced) {
		t.Fatalf("stale-epoch revocation = %v, want fenced", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT count(*) FROM environment_attach_grant_revocations WHERE domain_id=? AND grant_id=? AND grant_digest=?`, domainA, grantA, grantDigest).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("durable revocation rows=%d err=%v", rows, err)
	}
	if err := checkSchema(store.db); err != nil {
		t.Fatalf("schema/integrity check after revocation: %v", err)
	}
}

func TestAttachCompletionRefusesRepoMismatchAndChangedServingSPKI(t *testing.T) {
	makeSubmission := func(t *testing.T, contextRepo string) (*Store, tls.ConnectionState, ed25519.PrivateKey,
		operation.Command, string, string, time.Time,
	) {
		t.Helper()
		store, _, peer, artifactKey, now := commandFixture(t)
		domain, owner := identity(domainA, 7)
		if contextRepo == repoB {
			if err := store.AttachRepo(context.Background(), domainA, repoB); err != nil {
				t.Fatal(err)
			}
		}
		servingSPKI := digestBytes([]byte("isolated authority TLS SPKI"))
		environmentSPKI := digestBytes(peer.PeerCertificates[0].RawSubjectPublicKeyInfo)
		proof := operation.AttachCheckoutProof{
			Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
			CommonDirDigest: digestBytes([]byte("common")), GitDirDigest: digestBytes([]byte("git")),
			PeerSnapshotDigest: digestBytes([]byte("peer")), RouteSnapshotDigest: digestBytes([]byte("route")),
			EnvironmentID: envA, EnvironmentSPKIDigest: environmentSPKI,
		}
		proofBytes, err := artifactEncoder.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		grantID := claimTestID(920)
		grantNow := now.Add(-time.Minute)
		payload := map[string]any{
			"schema": "wipd.environment-attach-grant/1", "grant_id": grantID, "nonce": []byte("0123456789abcdef"),
			"domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
			"scope": "environment.attach-once@v1", "audience": map[string]any{
				"schema": "wipd.attach-audience/1", "kind": "authority-spki", "authority_spki_digest": servingSPKI,
			},
			"repo_id": repoA, "normalized_repo_key": proof.NormalizedRepoKey,
			"environment_id": envA, "environment_spki_digest": environmentSPKI,
			"issued_at": grantNow.Format(time.RFC3339Nano), "expires_at": grantNow.Add(5 * time.Minute).Format(time.RFC3339Nano),
		}
		grant := base64.RawURLEncoding.EncodeToString(signedTest(t, owner, "environment-attach-grant",
			"wipd.environment-attach-grant/1", domainA, domain.OwnerKeyID, 7, payload))
		commandID := claimTestID(921)
		command := operation.Command{
			ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA,
			EnvironmentSequence: 1, ActedAt: now.Add(-time.Second).Format(time.RFC3339Nano), CorrelationCommandID: commandID,
			Request: operation.Request{
				Operation: operation.EnvironmentAttachOnceV1.Metadata().Operation, Actor: "human",
				Context: operation.Context{Repo: contextRepo},
				Input:   operation.EnvironmentAttachOnceInput{GrantArtifact: grant, CheckoutProof: proofBytes},
			},
		}
		return store, peer, artifactKey, command, grant, servingSPKI, now
	}

	t.Run("grant Repo must equal command Context Repo", func(t *testing.T) {
		store, peer, artifactKey, command, _, servingSPKI, now := makeSubmission(t, repoB)
		defer func() { _ = store.Close() }()
		hash := hashCommand(t, command)
		submitted, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash, peer,
			servingSPKI, now, time.Now().UTC().Add(time.Minute))
		if err != nil || submitted.Owner == nil {
			t.Fatalf("submit mismatched Repo attach = %+v, %v", submitted, err)
		}
		terminal, err := store.CompleteCommandForServingSPKI(context.Background(), submitted.Owner,
			operation.Result{Code: operation.ResultSucceeded}, "", "", now, signWith(artifactKey), servingSPKI)
		if err != nil || terminal.Pending || len(terminal.Receipt) == 0 {
			t.Fatalf("mismatched Repo refusal terminal = %+v, %v", terminal, err)
		}
		fields, err := wipdwire.DecodeCanonicalMap(terminal.Receipt,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		result, _ := fields["result"].(map[string]any)
		if err != nil || result["code"] != string(operation.ResultRefused) {
			t.Fatalf("grant/context Repo disagreement terminal result=%v err=%v", result, err)
		}
		var effects int
		if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM environment_attach_grant_consumptions WHERE command_id=?) +
			(SELECT count(*) FROM authority_events WHERE command_id=?)`, command.ID, command.ID).Scan(&effects); err != nil || effects != 0 {
			t.Fatalf("grant/context Repo disagreement effects=%d err=%v; want no consumption or event", effects, err)
		}
	})

	t.Run("pending completion must retain current serving SPKI", func(t *testing.T) {
		store, peer, artifactKey, command, _, servingSPKI, now := makeSubmission(t, repoA)
		defer func() { _ = store.Close() }()
		hash := hashCommand(t, command)
		submitted, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash, peer,
			servingSPKI, now, time.Now().UTC().Add(time.Minute))
		if err != nil || submitted.Owner == nil {
			t.Fatalf("submit changed-SPKI attach = %+v, %v", submitted, err)
		}
		terminal, err := store.CompleteCommandForServingSPKI(context.Background(), submitted.Owner,
			operation.Result{Code: operation.ResultSucceeded}, "", "", now, signWith(artifactKey), digestBytes([]byte("rotated authority SPKI")))
		if err != nil || terminal.Pending || len(terminal.Receipt) == 0 {
			t.Fatalf("changed-SPKI refusal terminal = %+v, %v", terminal, err)
		}
		fields, err := wipdwire.DecodeCanonicalMap(terminal.Receipt,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		result, _ := fields["result"].(map[string]any)
		if err != nil || result["code"] != string(operation.ResultRefused) {
			t.Fatalf("changed-SPKI terminal result=%v err=%v", result, err)
		}
		var effects int
		if err = store.db.QueryRow(`SELECT (SELECT count(*) FROM environment_attach_grant_consumptions WHERE command_id=?) +
			(SELECT count(*) FROM authority_events WHERE command_id=?)`, command.ID, command.ID).Scan(&effects); err != nil || effects != 0 {
			t.Fatalf("changed-SPKI attach effects=%d err=%v; want no consumption or event", effects, err)
		}
	})
}

func TestEnvironmentAttachGrantChecksDirectOwnerScopeAudienceRouteAndEnvironment(t *testing.T) {
	store, _, _, _, now := commandFixture(t)
	defer func() { _ = store.Close() }()
	domain, owner := identity(domainA, 7)
	serverSPKI := digestBytes([]byte("isolated authority TLS SPKI"))
	environmentSPKI := digestBytes([]byte("authenticated environment SPKI"))
	proof := operation.AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
		CommonDirDigest: digestBytes([]byte("common")), GitDirDigest: digestBytes([]byte("git")),
		PeerSnapshotDigest: digestBytes([]byte("peer")), RouteSnapshotDigest: digestBytes([]byte("route")),
		EnvironmentID: envA, EnvironmentSPKIDigest: environmentSPKI,
	}
	makeGrant := func(audience, route, environment string, expires string) string {
		t.Helper()
		payload := map[string]any{
			"schema": "wipd.environment-attach-grant/1", "grant_id": grantA, "nonce": []byte("0123456789abcdef"),
			"domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
			"scope": "environment.attach-once@v1", "audience": map[string]any{
				"schema": "wipd.attach-audience/1", "kind": "authority-spki", "authority_spki_digest": audience,
			},
			"repo_id": repoA, "normalized_repo_key": route, "environment_id": environment,
			"environment_spki_digest": environmentSPKI,
			"issued_at":               now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": expires,
		}
		return base64.RawURLEncoding.EncodeToString(signedTest(t, owner, "environment-attach-grant", "wipd.environment-attach-grant/1", domainA, domain.OwnerKeyID, 7, payload))
	}
	valid := makeGrant(serverSPKI, proof.NormalizedRepoKey, envA, now.Add(5*time.Minute).Format(time.RFC3339Nano))
	claims, err := VerifyEnvironmentAttachGrantArtifact(valid, owner.Public().(ed25519.PublicKey), domainA, 7, domain.OwnerKeyID, now)
	if err != nil || claims.GrantID != grantA || claims.RepoID != repoA || claims.NormalizedKey != proof.NormalizedRepoKey ||
		claims.EnvironmentID != envA || claims.EnvironmentSPKI != environmentSPKI || claims.AudienceSPKI != serverSPKI {
		t.Fatalf("locally verified attach grant claims = %+v, %v", claims, err)
	}
	if _, err = VerifyEnvironmentAttachGrantArtifact(valid, owner.Public().(ed25519.PublicKey), domainA, 7, domain.OwnerKeyID, now.Add(6*time.Minute)); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("expired first-use grant = %v, want invalid proof", err)
	}
	replayClaims, err := VerifyEnvironmentAttachGrantArtifactForReplay(valid, owner.Public().(ed25519.PublicKey), domainA, 7, domain.OwnerKeyID)
	if err != nil || replayClaims.GrantDigest != claims.GrantDigest || replayClaims.RepoID != claims.RepoID {
		t.Fatalf("exact retained grant replay verification = %+v, %v", replayClaims, err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	active, err := domainOwner(context.Background(), tx, domainA)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := validateEnvironmentAttachGrantTx(context.Background(), tx, valid, proof, active, serverSPKI, now)
	if err != nil || grant.ID != grantA || grant.RepoID != repoA || grant.Digest == "" {
		t.Fatalf("valid owner-root grant = %+v, %v", grant, err)
	}
	_ = tx.Rollback()
	for _, test := range []struct {
		name  string
		grant string
	}{
		{"wrong audience key", makeGrant(digestBytes([]byte("other endpoint")), proof.NormalizedRepoKey, envA, now.Add(5*time.Minute).Format(time.RFC3339Nano))},
		{"wrong normalized route", makeGrant(serverSPKI, "github.com/acme/other", envA, now.Add(5*time.Minute).Format(time.RFC3339Nano))},
		{"wrong environment", makeGrant(serverSPKI, proof.NormalizedRepoKey, envB, now.Add(5*time.Minute).Format(time.RFC3339Nano))},
		{"expired", makeGrant(serverSPKI, proof.NormalizedRepoKey, envA, now.Format(time.RFC3339Nano))},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, beginErr := store.db.Begin()
			if beginErr != nil {
				t.Fatal(beginErr)
			}
			defer func() { _ = tx.Rollback() }()
			active, lookupErr := domainOwner(context.Background(), tx, domainA)
			if lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if _, validationErr := validateEnvironmentAttachGrantTx(context.Background(), tx, test.grant, proof, active, serverSPKI, now); !errors.Is(validationErr, ErrInvalidProof) {
				t.Fatalf("invalid grant = %v, want invalid proof", validationErr)
			}
		})
	}
}

func TestEnvironmentAttachGrantRevocationRejectsWrongOwnerRoot(t *testing.T) {
	store, _ := fresh(t)
	defer func() { _ = store.Close() }()
	domain, _ := identity(domainA, 7)
	if err := store.BootstrapDomain(context.Background(), domain, repoA); err != nil {
		t.Fatal(err)
	}
	_, foreignOwner := identity(domainB, 7)
	artifact := signedTest(t, foreignOwner, "environment-attach-grant-revocation", "wipd.environment-attach-grant-revocation/1",
		domainA, domain.OwnerKeyID, 7, map[string]any{
			"schema": "wipd.environment-attach-grant-revocation/1", "domain_id": domainA,
			"authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
			"grant_id": grantA, "grant_digest": digestBytes([]byte("grant")),
		})
	if err := store.RevokeEnvironmentAttachGrant(artifact); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("foreign owner root revocation = %v, want invalid proof", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT count(*) FROM environment_attach_grant_revocations`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("invalid revocation left rows=%d err=%v", rows, err)
	}
}

func TestEnvironmentAttachM2TerminalConsumesGrantAndReplaysExactReceipt(t *testing.T) {
	store, root, peer, artifactKey, now := commandFixture(t)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	domain, owner := identity(domainA, 7)
	grantNow := time.Now().UTC()
	serverSPKI := digestBytes([]byte("isolated authority TLS SPKI"))
	environmentSPKI := digestBytes(peer.PeerCertificates[0].RawSubjectPublicKeyInfo)
	proof := operation.AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
		CommonDirDigest: digestBytes([]byte("common")), GitDirDigest: digestBytes([]byte("git")), WorktreeName: "main",
		PeerSnapshotDigest: digestBytes([]byte("peer")), RouteSnapshotDigest: digestBytes([]byte("route")),
		EnvironmentID: envA, EnvironmentSPKIDigest: environmentSPKI,
	}
	proofBytes, err := artifactEncoder.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	grantPayload := map[string]any{
		"schema": "wipd.environment-attach-grant/1", "grant_id": grantA, "nonce": []byte("0123456789abcdef"),
		"domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
		"scope": "environment.attach-once@v1", "audience": map[string]any{
			"schema": "wipd.attach-audience/1", "kind": "authority-spki", "authority_spki_digest": serverSPKI,
		},
		"repo_id": repoA, "normalized_repo_key": proof.NormalizedRepoKey,
		"environment_id": envA, "environment_spki_digest": environmentSPKI,
		"issued_at": grantNow.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": grantNow.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}
	grant := base64.RawURLEncoding.EncodeToString(signedTest(t, owner, "environment-attach-grant", "wipd.environment-attach-grant/1", domainA, domain.OwnerKeyID, 7, grantPayload))
	command := operation.Command{
		ID: domainB, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: 1,
		ActedAt: now.Add(-time.Second).Format(time.RFC3339Nano), CorrelationCommandID: domainB,
		Request: operation.Request{
			Operation: operation.EnvironmentAttachOnceV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA}, Input: operation.EnvironmentAttachOnceInput{GrantArtifact: grant, CheckoutProof: proofBytes},
		},
	}
	hash := hashCommand(t, command)
	submitted, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash, peer, serverSPKI, now, time.Now().UTC().Add(time.Minute))
	if err != nil || submitted.Owner == nil || !submitted.Pending {
		t.Fatalf("submit attach = %+v, %v", submitted, err)
	}
	terminal, err := store.CompleteCommandForServingSPKI(context.Background(), submitted.Owner, operation.Result{Code: operation.ResultSucceeded}, "", "", now, signWith(artifactKey), serverSPKI)
	if err != nil || terminal.Pending || len(terminal.Receipt) == 0 || len(terminal.SignedReceipt) == 0 {
		t.Fatalf("complete attach = %+v, %v", terminal, err)
	}
	var consumed, events, routes int
	if err = store.db.QueryRow(`SELECT count(*) FROM environment_attach_grant_consumptions WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("consumption count=%d err=%v", consumed, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("attach event count=%d err=%v", events, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM repo_route_index WHERE repo_id=?`, repoA).Scan(&routes); err != nil || routes != 0 {
		t.Fatalf("attach unexpectedly bound route: count=%d err=%v", routes, err)
	}
	replayed, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash, peer, serverSPKI, now.Add(time.Minute), time.Now().UTC().Add(2*time.Minute))
	if err != nil || replayed.Pending || !bytes.Equal(replayed.Receipt, terminal.Receipt) || !bytes.Equal(replayed.SignedReceipt, terminal.SignedReceipt) {
		t.Fatalf("exact M2 replay did not retain receipt: %+v, %v", replayed, err)
	}
	var cloneID, worktreeID string
	if err = store.db.QueryRow(`SELECT clone_id,worktree_id FROM environment_checkout_attachments WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&cloneID, &worktreeID); err != nil {
		t.Fatal(err)
	}
	routeCommand := operation.Command{
		ID: claimTestID(30), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: 2,
		ActedAt: now.Add(time.Second).Format(time.RFC3339Nano), CorrelationCommandID: claimTestID(30),
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:   operation.RepoRouteBindInput{NormalizedKey: proof.NormalizedRepoKey},
		},
	}
	routeHash := hashCommand(t, routeCommand)
	routeSubmit, err := store.SubmitCommandWithDeadline(context.Background(), routeCommand, routeHash, peer, now, time.Now().UTC().Add(time.Minute))
	if err != nil || routeSubmit.Owner == nil {
		t.Fatalf("registered-checkout route bind admission = %+v, %v", routeSubmit, err)
	}
	routeTerminal, err := store.CompleteCommand(context.Background(), routeSubmit.Owner, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.RepoRouteBindOutput{RepoID: repoA, DomainID: domainA, Epoch: 7},
	}, "", "", now.Add(time.Second), signWith(artifactKey))
	if err != nil || routeTerminal.Pending {
		t.Fatalf("registered-checkout route bind completion = %+v, %v", routeTerminal, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM repo_route_index WHERE normalized_key=? AND repo_id=?`, proof.NormalizedRepoKey, repoA).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("route-bind projection count=%d err=%v", routes, err)
	}
	adoptCommand := operation.Command{
		ID: claimTestID(31), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: 3,
		ActedAt: now.Add(2 * time.Second).Format(time.RFC3339Nano), CorrelationCommandID: claimTestID(31),
		Request: operation.Request{
			Operation: operation.RepoAdoptRemoteV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:   operation.RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/another"},
		},
	}
	adoptHash := hashCommand(t, adoptCommand)
	adoptSubmit, err := store.SubmitCommandWithDeadline(context.Background(), adoptCommand, adoptHash, peer, now, time.Now().UTC().Add(time.Minute))
	if !errors.Is(err, ErrFenced) || adoptSubmit.Owner != nil {
		t.Fatalf("unregistered adoption key admission = %+v, %v; want refusal before command submission", adoptSubmit, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM repo_route_index WHERE repo_id=?`, repoA).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("conflicting adoption partially changed route index: count=%d err=%v", routes, err)
	}
	reuse := command
	reuse.ID = claimTestID(32)
	reuse.CorrelationCommandID = reuse.ID
	reuse.EnvironmentSequence = 3
	reuseHash := hashCommand(t, reuse)
	reuseSubmit, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), reuse, reuseHash, peer, serverSPKI, now, time.Now().UTC().Add(time.Minute))
	if err != nil || reuseSubmit.Owner == nil {
		t.Fatalf("consumed-grant replay admission = %+v, %v", reuseSubmit, err)
	}
	reuseTerminal, err := store.CompleteCommand(context.Background(), reuseSubmit.Owner, operation.Result{Code: operation.ResultSucceeded}, "", "", now.Add(3*time.Second), signWith(artifactKey))
	if err != nil || reuseTerminal.Pending || len(reuseTerminal.Receipt) == 0 {
		t.Fatalf("consumed-grant terminal refusal = %+v, %v", reuseTerminal, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM environment_attach_grant_consumptions WHERE domain_id=?`, domainA).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("grant reuse changed consumption count=%d err=%v", consumed, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&events); err != nil || events != 1 {
		t.Fatalf("grant reuse appended effect event count=%d err=%v", events, err)
	}
	rotatedKey := key("attach-rotated-environment")
	rotationCSR := csrFixture(t, rotatedKey, "attach-rotated")
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, _, caDER, caErr := currentCA(context.Background(), tx, domainA, 7, now)
	_ = tx.Rollback()
	if caErr != nil {
		t.Fatal(caErr)
	}
	rotationLeaf := leafFixture(t, rotatedKey, key("step4-ca"), caDER, domainA, envA, domain.OwnerKeyID, 7, now, 201)
	rotationGrant := grantFixture(t, owner, domain, "environment-rotate", claimTestID(35), envA, rotatedKey, 3)
	rotated, err := store.IssueEnvironmentCertificate(context.Background(), domainA, envA, rotationGrant,
		rotationCSR, [][]byte{rotationLeaf, caDER}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("rotate active Environment credential: %v", err)
	}
	rotatedPeer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, rotated.Chain[0]), mustCert(t, rotated.Chain[1])}}
	replayedAfterRotation, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash,
		rotatedPeer, serverSPKI, now.Add(3*time.Minute), time.Now().UTC().Add(4*time.Minute))
	if err != nil || replayedAfterRotation.Pending || !bytes.Equal(replayedAfterRotation.Receipt, terminal.Receipt) ||
		!bytes.Equal(replayedAfterRotation.SignedReceipt, terminal.SignedReceipt) {
		t.Fatalf("terminal replay after active Environment key rotation = %+v, %v", replayedAfterRotation, err)
	}
	pendingCommand := operation.Command{
		ID: claimTestID(36), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: 4,
		ActedAt: now.Add(4 * time.Minute).Format(time.RFC3339Nano), CorrelationCommandID: claimTestID(36),
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: cloneID, Worktree: worktreeID},
			Input:   operation.RepoRouteBindInput{NormalizedKey: proof.NormalizedRepoKey},
		},
	}
	pendingHash := hashCommand(t, pendingCommand)
	pending, err := store.SubmitCommand(context.Background(), pendingCommand, pendingHash, rotatedPeer, now.Add(4*time.Minute))
	if err != nil || !pending.Pending || pending.Owner == nil {
		t.Fatalf("submit pending Step 11 route command = %+v, %v", pending, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen authority store with a pending Step 11 route command and prior attach/bind/refusal terminals: %v", err)
	}
	status, err := store.QueryCommand(context.Background(), domainA, pendingCommand.ID, pendingHash, 7,
		rotatedPeer, envA, now.Add(5*time.Minute))
	if err != nil || !status.Pending || len(status.Receipt) != 0 {
		t.Fatalf("pending Step 11 route command after reopen = %+v, %v", status, err)
	}
}

func TestOpenRejectsDivergentEnvironmentAttachProjection(t *testing.T) {
	immutableTriggerSQL := environmentAttachmentTriggerSQL(t, "environment_checkout_attachment_immutable")
	tests := []struct {
		name   string
		column string
		value  string
	}{
		{name: "environment", column: "environment_id", value: envB},
		{name: "normalized key", column: "normalized_repo_key", value: "github.com/acme/other"},
		{name: "common directory digest", column: "common_dir_digest", value: digestBytes([]byte("other common"))},
		{name: "git directory digest", column: "git_dir_digest", value: digestBytes([]byte("other git"))},
		{name: "worktree name", column: "worktree_name", value: "other-worktree"},
		{name: "event association", column: "event_id", value: claimTestID(999)},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, root, peer, artifactKey, now := commandFixture(t)
			if test.column == "event_id" {
				if err := store.AttachRepo(context.Background(), domainA, repoB); err != nil {
					t.Fatalf("attach second Repo for distinct valid event: %v", err)
				}
			}
			commandID := claimTestID(940 + index*3)
			_, _ = registerTestCheckout(t, store, peer, artifactKey, now, repoA, claimTestID(941+index*3), "github.com/acme/widget", commandID)
			corruptValue := test.value
			if test.column == "event_id" {
				otherCommandID := claimTestID(960 + index*3)
				_, _ = registerTestCheckoutAtSequence(t, store, peer, artifactKey, now, repoB,
					claimTestID(961+index*3), "github.com/acme/other", otherCommandID, 2, "main")
				if queryErr := store.db.QueryRow(`SELECT event_id FROM authority_events WHERE command_id=?`, otherCommandID).Scan(&corruptValue); queryErr != nil {
					t.Fatalf("read distinct valid event for association corruption: %v", queryErr)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`DROP TRIGGER environment_checkout_attachment_immutable`); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE environment_checkout_attachments SET `+test.column+`=? WHERE command_id=?`, corruptValue, commandID); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if _, err = db.Exec(immutableTriggerSQL); err != nil {
				_ = db.Close()
				t.Fatalf("restore attachment immutable trigger: %v", err)
			}
			checkEnvironmentAttachSchemaObjects(t, db)
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := OpenExisting(root); !errors.Is(err, ErrInvalidStore) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("reopened divergent %s attachment projection: %v", test.name, err)
			}
		})
	}
}

func TestOpenExistingAcceptsMainCheckoutWithEmptyWorktreeName(t *testing.T) {
	store, root, peer, artifactKey, now := commandFixture(t)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	cloneID, worktreeID := registerTestCheckout(t, store, peer, artifactKey, now,
		repoA, claimTestID(980), "github.com/acme/widget", claimTestID(981), "")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	reopened, err := OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen successful main-checkout attach with empty worktree name: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var name, storedClone, storedWorktree string
	if err = reopened.db.QueryRow(`SELECT worktree_name,clone_id,worktree_id FROM environment_checkout_attachments WHERE repo_id=?`, repoA).Scan(&name, &storedClone, &storedWorktree); err != nil {
		t.Fatal(err)
	}
	if name != "" || storedClone != cloneID || storedWorktree != worktreeID {
		t.Fatalf("reopened main-checkout projection=(%q,%q,%q), want empty name and exact IDs", name, storedClone, storedWorktree)
	}
}

func TestOpenRejectsConsumedAttachWithoutProjection(t *testing.T) {
	noDeleteTriggerSQL := environmentAttachmentTriggerSQL(t, "environment_checkout_attachment_no_delete")
	store, root, peer, artifactKey, now := commandFixture(t)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	_, _ = registerTestCheckout(t, store, peer, artifactKey, now,
		repoA, claimTestID(982), "github.com/acme/widget", claimTestID(983), "main")
	var receipts, events, consumptions int
	if err := store.db.QueryRow(`SELECT
		(SELECT count(*) FROM terminal_receipts WHERE command_id=?) + 0,
		(SELECT count(*) FROM authority_events WHERE command_id=?),
		(SELECT count(*) FROM environment_attach_grant_consumptions WHERE command_id=?)`,
		claimTestID(983), claimTestID(983), claimTestID(983)).Scan(&receipts, &events, &consumptions); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || events != 1 || consumptions != 1 {
		t.Fatalf("fixture evidence before projection loss: receipts=%d events=%d consumptions=%d", receipts, events, consumptions)
	}
	var resultCode string
	var firstPosition, lastPosition int64
	if err := store.db.QueryRow(`SELECT result_code,first_position,last_position FROM terminal_receipts WHERE command_id=?`, claimTestID(983)).Scan(&resultCode, &firstPosition, &lastPosition); err != nil {
		t.Fatal(err)
	}
	if resultCode != string(operation.ResultSucceeded) || firstPosition <= 0 || firstPosition != lastPosition {
		t.Fatalf("fixture terminal receipt result=%q range=(%d,%d); want successful one-event terminal", resultCode, firstPosition, lastPosition)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	db, err := connect(filepath.Join(root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TRIGGER environment_checkout_attachment_no_delete`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM environment_checkout_attachments WHERE command_id=?`, claimTestID(983)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(noDeleteTriggerSQL); err != nil {
		_ = db.Close()
		t.Fatalf("restore attachment no-delete trigger: %v", err)
	}
	checkEnvironmentAttachSchemaObjects(t, db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenExisting(root); !errors.Is(openErr, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("reopen store with receipt/event/consumption but no attachment projection: %v", openErr)
	}
}

func environmentAttachmentTriggerSQL(t *testing.T, name string) string {
	t.Helper()
	for _, object := range environmentAttachSchema {
		if object.name == name {
			return object.sql
		}
	}
	t.Fatalf("attachment trigger %q not found in schema", name)
	return ""
}

func checkEnvironmentAttachSchemaObjects(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, want := range environmentAttachSchema {
		var kind, definition string
		if err := db.QueryRow(`SELECT type,sql FROM sqlite_master WHERE name=?`, want.name).Scan(&kind, &definition); err != nil {
			t.Fatalf("read environment attach schema object %s: %v", want.name, err)
		}
		if kind != want.kind || normalizeSchemaSQL(definition) != normalizeSchemaSQL(want.sql) {
			t.Fatalf("environment attach schema object %s is not restored exactly", want.name)
		}
	}
}

func registerTestCheckout(t *testing.T, store *Store, peer tls.ConnectionState, artifactKey ed25519.PrivateKey, now time.Time,
	repoID, grantID, normalizedKey, commandID string, worktreeNames ...string,
) (string, string) {
	return registerTestCheckoutAtSequence(t, store, peer, artifactKey, now, repoID, grantID, normalizedKey, commandID, 1, worktreeNames...)
}

func registerTestCheckoutAtSequence(t *testing.T, store *Store, peer tls.ConnectionState, artifactKey ed25519.PrivateKey, now time.Time,
	repoID, grantID, normalizedKey, commandID string, sequence uint64, worktreeNames ...string,
) (string, string) {
	t.Helper()
	worktreeName := "main"
	if len(worktreeNames) > 1 {
		t.Fatal("registerTestCheckout accepts at most one worktree name")
	}
	if len(worktreeNames) == 1 {
		worktreeName = worktreeNames[0]
	}
	domain, owner := identity(domainA, 7)
	serverSPKI := digestBytes([]byte("isolated authority TLS SPKI"))
	environmentSPKI := digestBytes(peer.PeerCertificates[0].RawSubjectPublicKeyInfo)
	proof := operation.AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: normalizedKey,
		CommonDirDigest: digestBytes([]byte("common/" + commandID)), GitDirDigest: digestBytes([]byte("git/" + commandID)), WorktreeName: worktreeName,
		PeerSnapshotDigest: digestBytes([]byte("peer")), RouteSnapshotDigest: digestBytes([]byte("route")),
		EnvironmentID: envA, EnvironmentSPKIDigest: environmentSPKI,
	}
	proofBytes, err := artifactEncoder.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	grantNow := time.Now().UTC()
	grantPayload := map[string]any{
		"schema": "wipd.environment-attach-grant/1", "grant_id": grantID, "nonce": []byte("0123456789abcdef"),
		"domain_id": domainA, "authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
		"scope": "environment.attach-once@v1", "audience": map[string]any{
			"schema": "wipd.attach-audience/1", "kind": "authority-spki", "authority_spki_digest": serverSPKI,
		},
		"repo_id": repoID, "normalized_repo_key": proof.NormalizedRepoKey,
		"environment_id": envA, "environment_spki_digest": environmentSPKI,
		"issued_at": grantNow.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": grantNow.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}
	grant := base64.RawURLEncoding.EncodeToString(signedTest(t, owner, "environment-attach-grant", "wipd.environment-attach-grant/1", domainA, domain.OwnerKeyID, 7, grantPayload))
	command := operation.Command{
		ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7, EnvironmentID: envA, EnvironmentSequence: sequence,
		ActedAt: now.Add(-time.Second).Format(time.RFC3339Nano), CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.EnvironmentAttachOnceV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoID}, Input: operation.EnvironmentAttachOnceInput{GrantArtifact: grant, CheckoutProof: proofBytes},
		},
	}
	hash := hashCommand(t, command)
	submitted, err := store.SubmitEnvironmentAttachCommandWithDeadline(context.Background(), command, hash, peer, serverSPKI, now, time.Now().UTC().Add(time.Minute))
	if err != nil || submitted.Owner == nil {
		t.Fatalf("test attach submission: %+v, %v", submitted, err)
	}
	terminal, err := store.CompleteCommandForServingSPKI(context.Background(), submitted.Owner, operation.Result{Code: operation.ResultSucceeded}, "", "", now, signWith(artifactKey), serverSPKI)
	if err != nil || terminal.Pending {
		t.Fatalf("test attach terminal: %+v, %v", terminal, err)
	}
	receipt, receiptErr := readReceipt(terminal.Receipt)
	if receiptErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) {
		problem := ""
		if receipt.Result.Problem != nil {
			problem = *receipt.Result.Problem
		}
		t.Fatalf("test attach terminal result=%q problem=%q err=%v; want success", receipt.Result.Code, problem, receiptErr)
	}
	var cloneID, worktreeID string
	if err = store.db.QueryRow(`SELECT clone_id,worktree_id FROM environment_checkout_attachments WHERE domain_id=? AND command_id=?`, domainA, commandID).Scan(&cloneID, &worktreeID); err != nil {
		t.Fatal(err)
	}
	return cloneID, worktreeID
}
