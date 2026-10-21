package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestValidateRouteResolveResponseAcceptsAuthorityHitAndFreshMiss(t *testing.T) {
	pin := "sha256:" + hex.EncodeToString(make([]byte, 32))
	profile, err := wipdauthority.NewProfile("https://authority.example:443", "01ARZ3NDEKTSV4RRFFQ69G5FAV", 7, pin, pin)
	if err != nil {
		t.Fatal(err)
	}
	filter := map[string]any{"normalized_key": "github.com/acme/widget"}
	filterBytes, err := wipdwire.EncodeCanonical(filter)
	if err != nil {
		t.Fatal(err)
	}
	filterDigest := sha256.Sum256(filterBytes)
	now := time.Now().UTC()
	anchor := wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}
	response := wipdwire.NamedBatchReadResponse{
		Schema: "wipd.read-response/1", Query: wipdwire.NamedBatchReadIdentity{Name: "route.resolve", Version: 1},
		FilterHash: "sha256:" + hex.EncodeToString(filterDigest[:]),
		Snapshot: wipdwire.NamedBatchReadSnapshot{
			ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", DomainID: profile.DomainID(), Epoch: profile.Epoch(),
			AsOf: anchor, OverlayPolicy: "folded-only", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		},
		Provenance: wipdwire.NamedBatchReadProvenance{
			Source: "authority", AuthorityReachability: "reachable", AuthorityAsOf: &anchor,
			HistoryState: "current",
		},
		Items: []wipdwire.NamedBatchReadItem{}, Complete: true,
	}
	if err = validateRouteResolveResponse(response, profile, filter); err != nil {
		t.Fatalf("fresh-miss response rejected: %v", err)
	}
	response.Items = []wipdwire.NamedBatchReadItem{{
		Value: mustEncodeRouteBinding(t, "01ARZ3NDEKTSV4RRFFQ69G5FAX", profile.DomainID(), profile.Epoch()), State: "folded",
	}}
	if err = validateRouteResolveResponse(response, profile, filter); err != nil {
		t.Fatalf("authority hit response rejected: %v", err)
	}
	response.Provenance.AuthorityReachability = "unavailable"
	if err = validateRouteResolveResponse(response, profile, filter); err == nil {
		t.Fatal("unavailable authority response accepted")
	}
}

func TestM6PrebindingRouteQueryUsesD117EnvironmentMTLSAndHasNoMissEffects(t *testing.T) {
	fixture := newClientFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	profile, err := wipdauthority.NewProfile("https://localhost:"+strconv.Itoa(port), testDomainID, 1,
		fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(privateTempDir(t), "environment")
	issuerCalls := &atomic.Int32{}
	forceInstallConflict := &atomic.Bool{}
	forceInstallConflict.Store(true)
	loseEnrollmentResponse := &atomic.Bool{}
	loseEnrollmentResponse.Store(true)
	commitReached := make(chan struct{}, 1)
	server, err := wipdauthority.NewM6PrebindingLabServer(profile,
		tlsCertificate(t, fixture.serverCertDER, fixture.serverPrivate), wipdauthority.M6PrebindingLabConfig{
			Store: fixture.store, EnrollmentGrant: fixture.grant, ExpectedCSRDER: fixture.identity.CSRDER,
			EnvironmentCACertificateDER: fixture.caDER,
			SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csr []byte, at time.Time) ([]byte, error) {
				issuerCalls.Add(1)
				if forceInstallConflict.CompareAndSwap(true, false) {
					if err := os.Mkdir(filepath.Join(profileRoot, environmentCredentialName), 0o700); err != nil {
						return nil, err
					}
				}
				return testEnvironmentLeaf(t, fixture.caKey, fixture.caDER, csr, domain, environment, fixture.ownerKeyID, epoch, at)
			},
			AfterEnrollmentCommit: func(ctx context.Context) {
				if loseEnrollmentResponse.CompareAndSwap(true, false) {
					commitReached <- struct{}{}
					<-ctx.Done()
				}
			},
		})
	if err != nil {
		t.Fatalf("construct Repo-independent M6 authority: %v", err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case serveErr := <-serverDone:
			if serveErr != nil {
				t.Errorf("prebinding authority shutdown: %v", serveErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("prebinding authority did not stop")
		}
	})

	if err = SavePending(profileRoot, fixture.identity); err != nil {
		t.Fatal(err)
	}
	enrollmentCtx, cancelEnrollment := context.WithCancel(context.Background())
	firstEnrollment := make(chan error, 1)
	go func() {
		_, enrollErr := EnrollEnvironment(enrollmentCtx, profile, fixture.roots,
			fixture.ownerRoot, fixture.delegation, fixture.identity, fixture.grant, profileRoot)
		firstEnrollment <- enrollErr
	}()
	select {
	case <-commitReached:
	case <-time.After(5 * time.Second):
		cancelEnrollment()
		t.Fatal("authority did not commit enrollment before the response-loss injection")
	}
	cancelEnrollment()
	if err = <-firstEnrollment; err == nil {
		t.Fatal("enrollment unexpectedly succeeded after its committed response was lost")
	}
	if issuerCalls.Load() != 1 {
		t.Fatalf("initial enrollment issuer calls = %d, want 1", issuerCalls.Load())
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, pendingName)); statErr != nil {
		t.Fatalf("pending Environment key was lost after local install failure: %v", statErr)
	}
	restartedPending, err := LoadPending(profileRoot)
	if err != nil {
		t.Fatalf("reopen the same pending identity after lost response: %v", err)
	}
	if !bytes.Equal(restartedPending.CSRDER, fixture.identity.CSRDER) || !bytes.Equal(restartedPending.PrivateKeyPKCS8, fixture.identity.PrivateKeyPKCS8) {
		t.Fatal("reopened pending identity differs from the original CSR/key")
	}
	if _, err = EnrollEnvironment(context.Background(), profile, fixture.roots,
		fixture.ownerRoot, fixture.delegation, restartedPending, fixture.grant, profileRoot); err == nil {
		t.Fatal("retry unexpectedly installed over the injected local path conflict")
	}
	clear(restartedPending.PrivateKeyPKCS8)
	if issuerCalls.Load() != 1 {
		t.Fatalf("same-pending-key retry after lost response reissued certificate; issuer calls=%d", issuerCalls.Load())
	}
	if err = os.Remove(filepath.Join(profileRoot, environmentCredentialName)); err != nil {
		t.Fatalf("remove injected local install conflict: %v", err)
	}
	recoveredPending, err := LoadPending(profileRoot)
	if err != nil {
		t.Fatalf("load durable pending Environment identity for restart recovery: %v", err)
	}
	credential, err := EnrollEnvironment(context.Background(), profile, fixture.roots,
		fixture.ownerRoot, fixture.delegation, recoveredPending, fixture.grant, profileRoot)
	clear(recoveredPending.PrivateKeyPKCS8)
	if err != nil {
		t.Fatalf("recover consumed D117 enrollment after local install failure: %v", err)
	}
	committed, alreadyIssued, err := fixture.store.ValidateEnvironmentEnrollment(context.Background(), testDomainID,
		credential.EnvironmentID, fixture.grant, fixture.identity.CSRDER, time.Now().UTC())
	if err != nil || !alreadyIssued {
		t.Fatalf("verify authority commit recovered from lost response: alreadyIssued=%v err=%v", alreadyIssued, err)
	}
	if credential.EnvironmentID != committed.EnvironmentID || !equalDERChain(credential.CertificateDER, committed.Chain) {
		t.Fatal("recovery after lost response did not return the exact authority-committed Environment certificate")
	}
	if issuerCalls.Load() != 1 {
		t.Fatalf("recovery after local install failure reissued certificate; issuer calls=%d", issuerCalls.Load())
	}
	const concurrentRecoveries = 8
	var recoveries sync.WaitGroup
	identities := make(chan string, concurrentRecoveries)
	errs := make(chan error, concurrentRecoveries)
	for range concurrentRecoveries {
		recoveries.Add(1)
		go func() {
			defer recoveries.Done()
			got, recoverErr := EnrollEnvironment(context.Background(), profile, fixture.roots,
				fixture.ownerRoot, fixture.delegation, fixture.identity, fixture.grant, profileRoot)
			if recoverErr != nil {
				errs <- recoverErr
				return
			}
			identities <- got.EnvironmentID
			clear(got.PrivateKeyPKCS8)
		}()
	}
	recoveries.Wait()
	close(identities)
	close(errs)
	for recoverErr := range errs {
		t.Errorf("concurrent canonical Environment recovery: %v", recoverErr)
	}
	for environmentID := range identities {
		if environmentID != credential.EnvironmentID {
			t.Errorf("concurrent recovery Environment ID = %q, want canonical %q", environmentID, credential.EnvironmentID)
		}
	}
	if issuerCalls.Load() != 1 {
		t.Fatalf("recovery reissued Environment certificate %d times; want no additional issuer calls", issuerCalls.Load()-1)
	}
	conflictingIdentity, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(conflictingIdentity.PrivateKeyPKCS8)
	if err = SavePending(profileRoot, conflictingIdentity); err != nil {
		t.Fatalf("save conflicting pending identity: %v", err)
	}
	if _, err = EnrollEnvironment(context.Background(), profile, fixture.roots,
		fixture.ownerRoot, fixture.delegation, fixture.identity, fixture.grant, profileRoot); !errors.Is(err, ErrStateExists) {
		t.Fatalf("conflicting pending identity error = %v, want ErrStateExists", err)
	}
	if issuerCalls.Load() != 1 {
		t.Fatalf("conflicting pending identity caused %d issuer calls, want no reissuance", issuerCalls.Load()-1)
	}
	pending, pendingErr := LoadPending(profileRoot)
	if pendingErr != nil {
		t.Fatalf("conflicting pending identity was discarded: %v", pendingErr)
	}
	if !bytes.Equal(pending.PrivateKeyPKCS8, conflictingIdentity.PrivateKeyPKCS8) || !bytes.Equal(pending.CSRDER, conflictingIdentity.CSRDER) {
		t.Fatal("asymmetric pending identity refusal changed the competing private key or CSR")
	}
	clear(pending.PrivateKeyPKCS8)
	if err = os.Remove(filepath.Join(profileRoot, pendingName)); err != nil {
		t.Fatalf("remove test conflicting pending identity: %v", err)
	}
	clear(credential.PrivateKeyPKCS8)
	credential, clientCertificate, err := LoadEnvironmentCredential(profileRoot, profile, fixture.ownerRoot, fixture.delegation, time.Now().UTC())
	if err != nil {
		t.Fatalf("load canonical prebinding Environment credential: %v", err)
	}
	defer clear(credential.PrivateKeyPKCS8)
	defer clear(clientCertificate.PrivateKey.(ed25519.PrivateKey))
	credentialPath := filepath.Join(profileRoot, environmentCredentialName)
	credentialBytesBeforeReplacement, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyReplacement := credential
	wrongKeyReplacement.PrivateKeyPKCS8 = bytes.Clone(conflictingIdentity.PrivateKeyPKCS8)
	err = installEnvironmentCredential(profileRoot, wrongKeyReplacement)
	clear(wrongKeyReplacement.PrivateKeyPKCS8)
	if !errors.Is(err, ErrStateExists) {
		t.Fatalf("wrong-key canonical credential replacement = %v, want ErrStateExists", err)
	}
	wrongLeaf, err := testEnvironmentLeaf(t, fixture.caKey, fixture.caDER, fixture.identity.CSRDER,
		testDomainID, credential.EnvironmentID, fixture.ownerKeyID, 1, time.Now().UTC().Add(-2*time.Minute))
	if err != nil {
		t.Fatalf("create non-authoritative replacement certificate fixture: %v", err)
	}
	wrongGenerationReplacement := credential
	wrongGenerationReplacement.CertificateDER = clone2D(credential.CertificateDER)
	wrongGenerationReplacement.CertificateDER[0] = wrongLeaf
	if err = installEnvironmentCredential(profileRoot, wrongGenerationReplacement); !errors.Is(err, ErrStateExists) {
		t.Fatalf("wrong-certificate-generation replacement = %v, want ErrStateExists", err)
	}
	credentialBytesAfterReplacement, err := os.ReadFile(credentialPath)
	if err != nil || !bytes.Equal(credentialBytesBeforeReplacement, credentialBytesAfterReplacement) {
		t.Fatalf("credential replacement refusal changed the canonical record: read err=%v", err)
	}
	client, err := profile.HTTPClientWithCertificate(fixture.roots, &clientCertificate)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	key := "github.com/acme/fresh-miss"
	before, beforeAnchor, err := fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, testDomainID, 1)
	if !errors.Is(err, authoritystore.ErrRouteUnbound) || before.RepoID != "" {
		t.Fatalf("initial fresh route = %+v, %v", before, err)
	}
	wrongGenerationCertificate := clientCertificate
	wrongGenerationCertificate.Certificate = clone2D(credential.CertificateDER)
	wrongGenerationCertificate.Certificate[0] = wrongLeaf
	wrongGenerationClient, err := profile.HTTPClientWithCertificate(fixture.roots, &wrongGenerationCertificate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queryRouteResolve(context.Background(), wrongGenerationClient, profile, key); err == nil || !strings.Contains(err.Error(), "auth.environment") {
		t.Fatalf("unregistered certificate generation lookup error = %v, want authority Environment rejection", err)
	}
	wrongGenerationClient.CloseIdleConnections()
	after, afterAnchor, err := fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, testDomainID, 1)
	if !errors.Is(err, authoritystore.ErrRouteUnbound) || after.RepoID != "" || afterAnchor != beforeAnchor {
		t.Fatalf("unregistered certificate generation changed route state: binding=%+v anchor=%+v before=%+v err=%v", after, afterAnchor, beforeAnchor, err)
	}
	result, err := queryRouteResolve(context.Background(), client, profile, key)
	if err != nil || result.RepoID != nil || result.DomainID != testDomainID || result.Epoch != 1 {
		t.Fatalf("authenticated prebinding miss = %+v, %v", result, err)
	}
	after, afterAnchor, err = fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, testDomainID, 1)
	if !errors.Is(err, authoritystore.ErrRouteUnbound) || after.RepoID != "" || afterAnchor != beforeAnchor {
		t.Fatalf("fresh prebinding lookup changed authority state: binding=%+v anchor=%+v before=%+v err=%v", after, afterAnchor, beforeAnchor, err)
	}
	for _, name := range []string{stateName, "environment-journal"} {
		if _, statErr := os.Stat(filepath.Join(profileRoot, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("prebinding enrollment/query created Repo-bound state %q: %v", name, statErr)
		}
	}

	wrongOwner := sha256.Sum256([]byte("wrong-owner-pin"))
	wrongOwnerProfile, err := wipdauthority.NewProfile(profile.Origin(), testDomainID, 1,
		fixture.authorityPin, "sha256:"+hex.EncodeToString(wrongOwner[:]))
	if err != nil {
		t.Fatal(err)
	}
	wrongDomainProfile, err := wipdauthority.NewProfile(profile.Origin(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1,
		fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	wrongEpochProfile, err := wipdauthority.NewProfile(profile.Origin(), testDomainID, 2,
		fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]wipdauthority.Profile{
		"owner":  wrongOwnerProfile,
		"domain": wrongDomainProfile,
		"epoch":  wrongEpochProfile,
	} {
		if _, _, loadErr := LoadEnvironmentCredential(profileRoot, candidate, fixture.ownerRoot, fixture.delegation, time.Now().UTC()); !errors.Is(loadErr, ErrInvalidClientState) {
			t.Errorf("wrong %s profile loaded the canonical credential: %v", name, loadErr)
		}
	}
	if issuerCalls.Load() != 1 {
		t.Fatalf("wrong owner/domain/epoch checks re-entered enrollment; issuer calls=%d", issuerCalls.Load())
	}
	unchangedCredential, _, err := LoadEnvironmentCredential(profileRoot, profile, fixture.ownerRoot, fixture.delegation, time.Now().UTC())
	if err != nil {
		t.Fatalf("canonical credential failed after mismatch refusals: %v", err)
	}
	if unchangedCredential.EnvironmentID != credential.EnvironmentID || !equalDERChain(unchangedCredential.CertificateDER, credential.CertificateDER) {
		t.Fatal("wrong owner/domain/epoch refusal replaced the canonical identity or certificate")
	}
	clear(unchangedCredential.PrivateKeyPKCS8)
	if _, err = queryRouteResolve(context.Background(), client, wrongEpochProfile, key); err == nil {
		t.Fatal("prebinding route query with the wrong authority epoch succeeded")
	}
	after, afterAnchor, err = fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, testDomainID, 1)
	if !errors.Is(err, authoritystore.ErrRouteUnbound) || after.RepoID != "" || afterAnchor != beforeAnchor {
		t.Fatalf("rejected epoch query changed authority state: binding=%+v anchor=%+v before=%+v err=%v", after, afterAnchor, beforeAnchor, err)
	}

	// The canonical profile credential is also the identity reused by later
	// Repo-bound initialization; it must be rejected remotely if revoked before
	// the Step 11 command client can be opened (and before a journal is created).
	seededState, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot,
		fixture.delegation, testRepoID, fixture.identity, fixture.grant, profileRoot)
	if err != nil {
		t.Fatalf("initialize the Repo-bound state with the canonical Environment identity: %v", err)
	}
	defer clear(seededState.PrivateKeyPKCS8)
	repoWrongEpoch, err := wipdauthority.NewProfile(fixture.profile.Origin(), testDomainID, 2,
		fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	repoWrongEpoch, err = repoWrongEpoch.WithM5LabRepoID(testRepoID)
	if err != nil {
		t.Fatal(err)
	}
	stateBeforeStaleEpoch, err := os.ReadFile(filepath.Join(profileRoot, stateName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStep11CommandExchangeClient(context.Background(), repoWrongEpoch, fixture.roots,
		profileRoot, []operation.ID{operation.MatterCreateV1.Metadata().Operation}); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("Repo-bound startup with stale profile epoch = %v, want ErrInvalidClientState", err)
	}
	stateAfterStaleEpoch, err := os.ReadFile(filepath.Join(profileRoot, stateName))
	if err != nil || !bytes.Equal(stateBeforeStaleEpoch, stateAfterStaleEpoch) {
		t.Fatalf("stale-epoch startup changed Repo-bound state: read err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, "environment-journal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale-epoch startup created the Environment journal: %v", statErr)
	}
	journalIdentity := wipdjournal.Identity{
		RepoID: seededState.RepoID, DomainID: seededState.DomainID, AuthorityEpoch: seededState.Epoch,
		EnvironmentID: seededState.EnvironmentID, OwnerRootSPKI: seededState.OwnerKeyID,
	}
	journalRoot := filepath.Join(privateTempDir(t), "identity-bound-journal")
	journal, err := wipdjournal.Open(journalRoot, journalIdentity)
	if err != nil {
		t.Fatalf("create Repo-bound journal from the installed canonical identity: %v", err)
	}
	if journal.Identity() != journalIdentity {
		t.Fatalf("new journal identity = %+v, want %+v", journal.Identity(), journalIdentity)
	}
	firstEntry, err := journal.PrepareCommand(wipdjournal.CommandInput{
		ID: "00000000000000000000000001",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: seededState.RepoID}, Input: operation.MatterCreateInput{Title: "first identity-bound entry"},
		},
	})
	if err != nil {
		t.Fatalf("prepare first identity-bound journal entry: %v", err)
	}
	secondEntry, err := journal.PrepareCommand(wipdjournal.CommandInput{
		ID: "00000000000000000000000002",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: seededState.RepoID}, Input: operation.MatterCreateInput{Title: "second identity-bound entry"},
		},
	})
	if err != nil {
		t.Fatalf("prepare second identity-bound journal entry: %v", err)
	}
	if firstEntry.EnvironmentSeq >= secondEntry.EnvironmentSeq || firstEntry.JournalPosition >= secondEntry.JournalPosition {
		t.Fatalf("journal entries are not ordered: first=%+v second=%+v", firstEntry, secondEntry)
	}
	if err = journal.Close(); err != nil {
		t.Fatalf("close initial identity-bound journal: %v", err)
	}
	databasePath := filepath.Join(journalRoot, "command-journal.sqlite")
	databaseBeforeRefusal, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	reopenedJournal, err := wipdjournal.Open(journalRoot, journalIdentity)
	if err != nil {
		t.Fatalf("reopen journal with the same authoritative Environment identity: %v", err)
	}
	if reopenedJournal.Identity() != journalIdentity {
		t.Fatalf("reopened journal identity = %+v, want %+v", reopenedJournal.Identity(), journalIdentity)
	}
	reopenedEntries, err := reopenedJournal.Entries()
	if err != nil || len(reopenedEntries) != 2 || reopenedEntries[0].Command.ID != firstEntry.Command.ID ||
		reopenedEntries[1].Command.ID != secondEntry.Command.ID ||
		reopenedEntries[0].EnvironmentSeq != firstEntry.EnvironmentSeq ||
		reopenedEntries[1].EnvironmentSeq != secondEntry.EnvironmentSeq ||
		reopenedEntries[0].JournalPosition != firstEntry.JournalPosition ||
		reopenedEntries[1].JournalPosition != secondEntry.JournalPosition {
		t.Fatalf("same-identity journal reopen lost entry order: entries=%+v err=%v", reopenedEntries, err)
	}
	if err = reopenedJournal.Close(); err != nil {
		t.Fatalf("close reopened identity-bound journal: %v", err)
	}
	for name, mismatched := range map[string]wipdjournal.Identity{
		"epoch": func() wipdjournal.Identity {
			candidate := journalIdentity
			candidate.AuthorityEpoch++
			return candidate
		}(),
		"environment": func() wipdjournal.Identity {
			candidate := journalIdentity
			candidate.EnvironmentID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
			return candidate
		}(),
	} {
		if _, openErr := wipdjournal.Open(journalRoot, mismatched); !errors.Is(openErr, wipdjournal.ErrInvalidIdentity) {
			t.Errorf("journal reopen with mismatched %s = %v, want ErrInvalidIdentity", name, openErr)
		}
	}
	databaseAfterRefusal, err := os.ReadFile(databasePath)
	if err != nil || !bytes.Equal(databaseBeforeRefusal, databaseAfterRefusal) {
		t.Fatalf("mismatched journal reopen changed durable database: read err=%v", err)
	}
	stateBeforeRevocation, err := os.ReadFile(filepath.Join(profileRoot, stateName))
	if err != nil {
		t.Fatal(err)
	}
	issued, alreadyIssued, err := fixture.store.ValidateEnvironmentEnrollment(context.Background(), testDomainID,
		credential.EnvironmentID, fixture.grant, fixture.identity.CSRDER, time.Now().UTC())
	if err != nil || !alreadyIssued {
		t.Fatalf("read committed Environment certificate for revocation test: alreadyIssued=%v err=%v", alreadyIssued, err)
	}
	if err = fixture.store.RevokeEnvironmentCertificate(context.Background(), testDomainID,
		credential.EnvironmentID, 1, issued.Generation, time.Now().UTC()); err != nil {
		t.Fatalf("revoke canonical Environment certificate: %v", err)
	}
	if _, err = queryRouteResolve(context.Background(), client, profile, key); err == nil {
		t.Fatal("prebinding route query succeeded with the authoritative Environment certificate revoked")
	}
	if _, err = loadInstalledClientStateMode(profileRoot, fixture.profile, true); err != nil {
		t.Fatalf("local Repo-bound identity failed before revocation request: %v", err)
	}
	if _, err = OpenStep11CommandExchangeClient(context.Background(), fixture.profile, fixture.roots,
		profileRoot, []operation.ID{operation.MatterCreateV1.Metadata().Operation}); err == nil || !strings.Contains(err.Error(), "auth.environment") {
		t.Fatalf("Repo-bound startup revocation error = %v, want authority Environment rejection", err)
	}
	stateAfterRevocation, err := os.ReadFile(filepath.Join(profileRoot, stateName))
	if err != nil || !bytes.Equal(stateBeforeRevocation, stateAfterRevocation) {
		t.Fatalf("revoked startup changed Repo-bound state: read err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, "environment-journal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("revoked Repo-bound startup opened/created the Environment journal: %v", statErr)
	}
	after, afterAnchor, err = fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, testDomainID, 1)
	if !errors.Is(err, authoritystore.ErrRouteUnbound) || after.RepoID != "" || afterAnchor != beforeAnchor {
		t.Fatalf("revoked query changed route state: binding=%+v anchor=%+v before=%+v err=%v", after, afterAnchor, beforeAnchor, err)
	}
}

func TestM6ConcurrentFirstUseEnrollmentConvergesOnOneCredential(t *testing.T) {
	fixture := newClientFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	profile, err := wipdauthority.NewProfile("https://localhost:"+strconv.Itoa(port), testDomainID, 1,
		fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(privateTempDir(t), "first-use-profile")
	issuerCalls := &atomic.Int32{}
	server, err := wipdauthority.NewM6PrebindingLabServer(profile,
		tlsCertificate(t, fixture.serverCertDER, fixture.serverPrivate), wipdauthority.M6PrebindingLabConfig{
			Store: fixture.store, EnrollmentGrant: fixture.grant, ExpectedCSRDER: fixture.identity.CSRDER,
			EnvironmentCACertificateDER: fixture.caDER,
			SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csr []byte, at time.Time) ([]byte, error) {
				issuerCalls.Add(1)
				return testEnvironmentLeaf(t, fixture.caKey, fixture.caDER, csr, domain, environment, fixture.ownerKeyID, epoch, at)
			},
		})
	if err != nil {
		t.Fatalf("construct Repo-independent M6 authority: %v", err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case serveErr := <-serverDone:
			if serveErr != nil {
				t.Errorf("prebinding authority shutdown: %v", serveErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("prebinding authority did not stop")
		}
	})
	if err = SavePending(profileRoot, fixture.identity); err != nil {
		t.Fatal(err)
	}

	type enrollmentResult struct {
		credential EnvironmentCredential
		err        error
	}
	const contenders = 8
	start := make(chan struct{})
	results := make(chan enrollmentResult, contenders)
	var workers sync.WaitGroup
	for range contenders {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			credential, enrollErr := EnrollEnvironment(context.Background(), profile, fixture.roots,
				fixture.ownerRoot, fixture.delegation, fixture.identity, fixture.grant, profileRoot)
			results <- enrollmentResult{credential: credential, err: enrollErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var canonical EnvironmentCredential
	for result := range results {
		if result.err != nil {
			t.Errorf("concurrent first-use enrollment: %v", result.err)
			clear(result.credential.PrivateKeyPKCS8)
			continue
		}
		if canonical.EnvironmentID == "" {
			canonical = result.credential
			continue
		}
		if result.credential.EnvironmentID != canonical.EnvironmentID || result.credential.SPKIDigest != canonical.SPKIDigest ||
			!equalDERChain(result.credential.CertificateDER, canonical.CertificateDER) ||
			!bytes.Equal(result.credential.PrivateKeyPKCS8, canonical.PrivateKeyPKCS8) {
			t.Error("concurrent first-use callers received different Environment identity material")
		}
		clear(result.credential.PrivateKeyPKCS8)
	}
	if canonical.EnvironmentID == "" {
		t.Fatal("no concurrent first-use enrollment installed a canonical credential")
	}
	defer clear(canonical.PrivateKeyPKCS8)
	if issuerCalls.Load() != 1 {
		t.Fatalf("concurrent first-use signer calls = %d, want exactly one", issuerCalls.Load())
	}
	installed, _, err := LoadEnvironmentCredential(profileRoot, profile, fixture.ownerRoot, fixture.delegation, time.Now().UTC())
	if err != nil {
		t.Fatalf("load credential installed by concurrent first-use attempts: %v", err)
	}
	if installed.EnvironmentID != canonical.EnvironmentID || installed.SPKIDigest != canonical.SPKIDigest ||
		!equalDERChain(installed.CertificateDER, canonical.CertificateDER) ||
		!bytes.Equal(installed.PrivateKeyPKCS8, canonical.PrivateKeyPKCS8) {
		t.Fatal("persisted canonical credential differs from concurrent first-use results")
	}
	clear(installed.PrivateKeyPKCS8)
	if _, err = LoadPending(profileRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful first use retained or changed pending identity: %v", err)
	}
	issued, alreadyIssued, err := fixture.store.ValidateEnvironmentEnrollment(context.Background(), testDomainID,
		canonical.EnvironmentID, fixture.grant, fixture.identity.CSRDER, time.Now().UTC())
	if err != nil || !alreadyIssued || issued.EnvironmentID != canonical.EnvironmentID ||
		!equalDERChain(issued.Chain, canonical.CertificateDER) {
		t.Fatalf("concurrent first use did not consume the one-use grant exactly once: issued=%+v already=%v err=%v", issued, alreadyIssued, err)
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, stateName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("first-use enrollment created Repo-bound state: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(profileRoot, "environment-journal")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("first-use enrollment created an Environment journal: %v", statErr)
	}

	competing, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(competing.PrivateKeyPKCS8)
	if err = SavePending(profileRoot, competing); err != nil {
		t.Fatalf("save asymmetric competing pending identity: %v", err)
	}
	if _, err = EnrollEnvironment(context.Background(), profile, fixture.roots,
		fixture.ownerRoot, fixture.delegation, fixture.identity, fixture.grant, profileRoot); !errors.Is(err, ErrStateExists) {
		t.Fatalf("canonical identity with a competing pending identity = %v, want ErrStateExists", err)
	}
	preservedPending, err := LoadPending(profileRoot)
	if err != nil {
		t.Fatalf("load competing pending identity after refusal: %v", err)
	}
	if !bytes.Equal(preservedPending.PrivateKeyPKCS8, competing.PrivateKeyPKCS8) || !bytes.Equal(preservedPending.CSRDER, competing.CSRDER) {
		t.Fatal("asymmetric refusal changed the competing pending private key or CSR")
	}
	clear(preservedPending.PrivateKeyPKCS8)
	if issuerCalls.Load() != 1 {
		t.Fatalf("asymmetric pending refusal re-entered enrollment; signer calls=%d", issuerCalls.Load())
	}
}

func TestStep11CommandClientRejectsRepoStateWithoutCanonicalEnvironmentCredential(t *testing.T) {
	fixture := newClientFixture(t)
	directory := filepath.Join(privateTempDir(t), "repo-state")
	if _, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot,
		fixture.delegation, testRepoID, fixture.identity, fixture.grant, directory); err != nil {
		t.Fatalf("install legacy Repo-bound fixture state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, environmentCredentialName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture unexpectedly has a canonical Environment credential: %v", err)
	}
	if _, err := OpenStep11CommandExchangeClient(context.Background(), fixture.profile, fixture.roots,
		directory, []operation.ID{operation.RouteResolveV1.Metadata().Operation}); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("Step 11 client opened without the canonical Environment credential: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "environment-journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal was created despite missing canonical credential: %v", err)
	}
}

func mustEncodeRouteBinding(t *testing.T, repoID, domainID string, epoch uint64) []byte {
	t.Helper()
	encoded, err := wipdwire.EncodeCanonical(map[string]any{"repo_id": repoID, "domain_id": domainID, "authority_epoch": epoch})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
