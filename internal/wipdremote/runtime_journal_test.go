package wipdremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	openRuntimeDomain        = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	openRuntimeRepo          = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	openRuntimeGrant         = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	openRuntimeEnvironmentID = "01ARZ3NDEKTSV4RRFFQ69G5FAT"
)

func TestStep11AttachReservationRecoversJournalCommitBeforeLedgerUpdate(t *testing.T) {
	profileRoot := filepath.Join(privateTempDir(t), "attach-profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger, err := wipdseed.OpenEnvironmentAttachLedger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	grantBytes, receiptBytes := []byte("grant"), []byte("d115 receipt")
	proof, err := wipdwire.EncodeCanonical(operation.AttachCheckoutProof{
		Schema: "wipd.attach-checkout-proof/1", NormalizedRepoKey: "github.com/acme/widget",
		CommonDirDigest: "sha256:" + strings.Repeat("1", 64), GitDirDigest: "sha256:" + strings.Repeat("2", 64),
		PeerSnapshotDigest: "sha256:" + strings.Repeat("3", 64), RouteSnapshotDigest: "sha256:" + strings.Repeat("4", 64),
		EnvironmentID: openRuntimeEnvironmentID, EnvironmentSPKIDigest: "sha256:" + strings.Repeat("5", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	grantHash, receiptHash := sha256.Sum256(grantBytes), sha256.Sum256(receiptBytes)
	commandID := "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	reservation := wipdseed.EnvironmentAttachAttempt{
		EnvironmentID: openRuntimeEnvironmentID, GrantBytes: grantBytes,
		GrantDigest: "sha256:" + hex.EncodeToString(grantHash[:]), D115ReceiptBytes: receiptBytes,
		D115ReceiptDigest: "sha256:" + hex.EncodeToString(receiptHash[:]), D115AttemptID: "01ARZ3NDEKTSV4RRFFQ69G5FAY",
		CommandID: commandID, AuthorityDomainID: openRuntimeDomain, AuthorityEpoch: 1,
		EnvironmentSPKI: "sha256:" + strings.Repeat("5", 64), Route: "github.com/acme/widget",
		CheckoutProof: proof, Disposition: wipdseed.EnvironmentAttachPrepared,
	}
	reserved, err := ledger.GetOrCreate(reservation)
	if err != nil || len(reserved.RequestBytes) != 0 {
		t.Fatalf("reserve intent before journal preparation = %+v, %v", reserved, err)
	}
	grant := base64.RawURLEncoding.EncodeToString(grantBytes)
	request := operation.Request{
		Operation: operation.EnvironmentAttachOnceV1.Metadata().Operation, Actor: "human",
		Context: operation.Context{Repo: openRuntimeRepo},
		Input:   operation.EnvironmentAttachOnceInput{GrantArtifact: grant, CheckoutProof: proof},
	}
	journal, err := openStep11Journal(profileRoot, openRuntimeRepo, openRuntimeDomain, 1, openRuntimeEnvironmentID, "")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: commandID, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash after the command journal commit but before the attach
	// ledger records the prepared canonical request.
	journal, err = openStep11Journal(profileRoot, openRuntimeRepo, openRuntimeDomain, 1, openRuntimeEnvironmentID, "")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := journal.PrepareCommand(wipdjournal.CommandInput{ID: commandID, Request: request})
	if err != nil || !bytes.Equal(recovered.CanonicalBytes, entry.CanonicalBytes) || recovered.EnvironmentSeq != entry.EnvironmentSeq {
		t.Fatalf("same-ID journal recovery = %+v, %v; want exact original command", recovered, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	connectedState := wipdseed.ClientState{
		RepoID: openRuntimeRepo, DomainID: openRuntimeDomain, Epoch: 1,
		EnvironmentID: openRuntimeEnvironmentID,
	}
	connectedJournal, err := openRuntimeJournal(profileRoot, Config{CommandCatalogue: "m6-step11"}, connectedState)
	if err != nil {
		t.Fatalf("open connected Step 11 journal after prebinding command: %v", err)
	}
	connectedEntry, err := connectedJournal.PrepareCommand(wipdjournal.CommandInput{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FA3",
		Request: operation.Request{
			Operation: operation.RepoRouteBindV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: openRuntimeRepo, Clone: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Worktree: "01ARZ3NDEKTSV4RRFFQ69G5FAB"},
			Input:   operation.RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"},
		},
	})
	if err != nil || connectedEntry.EnvironmentSeq != entry.EnvironmentSeq+1 {
		t.Fatalf("connected mode did not continue the prebinding Environment sequence: entry=%+v err=%v", connectedEntry, err)
	}
	if err = connectedJournal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = openStep11Journal(profileRoot, openRuntimeRepo, openRuntimeDomain, 1, openRuntimeEnvironmentID, "")
	if err != nil {
		t.Fatal(err)
	}
	prebindingEntry, err := journal.PrepareCommand(wipdjournal.CommandInput{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FA4",
		Request: operation.Request{
			Operation: operation.RepoAdoptRemoteV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: openRuntimeRepo, Clone: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Worktree: "01ARZ3NDEKTSV4RRFFQ69G5FAB"},
			Input:   operation.RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/widget"},
		},
	})
	if err != nil || prebindingEntry.EnvironmentSeq != connectedEntry.EnvironmentSeq+1 {
		t.Fatalf("prebinding mode did not continue the connected Environment sequence: entry=%+v err=%v", prebindingEntry, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := ledger.StorePreparedCommand(reserved.EnvironmentID, reserved.GrantDigest,
		recovered.CanonicalBytes, recovered.RequestHash)
	if err != nil || !bytes.Equal(stored.RequestBytes, entry.CanonicalBytes) || stored.RequestHash != entry.RequestHash {
		t.Fatalf("recovered reservation update = %+v, %v", stored, err)
	}
}

func TestOpenRuntimeCreatesAndReopensIdentityBoundJournalBeforeEpochFences(t *testing.T) {
	fixture := newOpenRuntimeJournalFixture(t)
	profileRoot := filepath.Join(privateTempDir(t), "profile")
	stateDirectory := filepath.Join(privateTempDir(t), "client-state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := fixture.identity
	var err error
	defer clear(identity.PrivateKeyPKCS8)
	if err = wipdseed.SavePending(stateDirectory, identity); err != nil {
		t.Fatal(err)
	}
	credential, err := wipdseed.EnrollEnvironment(context.Background(), fixture.profile, fixture.roots,
		fixture.ownerPublic, fixture.caDelegation, identity, fixture.enrollmentGrant, stateDirectory)
	if err != nil {
		t.Fatalf("install canonical D117 Environment credential: %v", err)
	}
	seeded, err := wipdseed.EnrollAndSeed(context.Background(), fixture.profile, fixture.roots,
		fixture.ownerPublic, fixture.caDelegation, openRuntimeRepo, identity, fixture.enrollmentGrant, stateDirectory)
	if err != nil {
		t.Fatalf("initialize Repo-bound state from the same canonical credential: %v", err)
	}
	defer clear(seeded.PrivateKeyPKCS8)
	if seeded.EnvironmentID != credential.EnvironmentID || seeded.Epoch != credential.Epoch || seeded.DomainID != credential.DomainID {
		t.Fatalf("Repo-bound initialization changed the canonical Environment identity: credential=%+v state=%+v", credential, seeded)
	}

	config := fixture.runtimeConfig(stateDirectory)
	if err = SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("save pinned connected profile: %v", err)
	}
	statePath := filepath.Join(stateDirectory, "client-state.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(profileRoot, config)
	if err != nil {
		t.Fatalf("open current-epoch D117 runtime: %v", err)
	}
	localRegistry, err := registryForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	wantOperations := make(map[operation.ID]bool)
	for _, definition := range localRegistry.Definitions() {
		wantOperations[definition.Metadata().Operation] = true
	}
	wantOperations[operation.GateExemptionRepairV1.Metadata().Operation] = true
	allDefinitions := append(operation.Catalogue(), operation.Step4Catalogue()...)
	allDefinitions = append(allDefinitions, operation.Step5Catalogue()...)
	allDefinitions = append(allDefinitions, operation.Step8Catalogue()...)
	allDefinitions = append(allDefinitions, operation.Step10Catalogue()...)
	allDefinitions = append(allDefinitions, operation.Step11Catalogue()...)
	for _, definition := range allDefinitions {
		id := definition.Metadata().Operation
		if got, want := runtime.client.SupportsOperation(id), wantOperations[id]; got != want {
			t.Errorf("negotiated operation %s = %v, want %v", id, got, want)
		}
	}
	journalIdentity := wipdjournal.Identity{
		RepoID: seeded.RepoID, DomainID: seeded.DomainID, AuthorityEpoch: seeded.Epoch,
		EnvironmentID: seeded.EnvironmentID, OwnerRootSPKI: seeded.OwnerKeyID,
	}
	if got := runtime.journal.Identity(); got != journalIdentity {
		t.Fatalf("new OpenRuntime journal identity = %+v, want %+v", got, journalIdentity)
	}
	first, err := runtime.journal.PrepareCommand(wipdjournal.CommandInput{
		ID: "00000000000000000000000001",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: seeded.RepoID}, Input: operation.MatterCreateInput{Title: "first OpenRuntime entry"},
		},
	})
	if err != nil {
		t.Fatalf("prepare first Environment journal entry: %v", err)
	}
	second, err := runtime.journal.PrepareCommand(wipdjournal.CommandInput{
		ID: "00000000000000000000000002",
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: seeded.RepoID}, Input: operation.MatterCreateInput{Title: "second OpenRuntime entry"},
		},
	})
	if err != nil {
		t.Fatalf("prepare second Environment journal entry: %v", err)
	}
	if first.EnvironmentSeq >= second.EnvironmentSeq || first.JournalPosition >= second.JournalPosition {
		t.Fatalf("journal entries are not ordered: first=%+v second=%+v", first, second)
	}
	journalPath := filepath.Join(profileRoot, "environment-journal", "command-journal.sqlite")
	if err = runtime.Close(); err != nil {
		t.Fatalf("close current-epoch runtime: %v", err)
	}
	databaseBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	stateAfterClose, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfterClose) {
		t.Fatalf("same-identity runtime close changed installed state: %v", err)
	}

	reopened, err := OpenRuntime(profileRoot, config)
	if err != nil {
		t.Fatalf("reopen journal with same canonical D117 identity: %v", err)
	}
	entries, err := reopened.journal.Entries()
	if err != nil || len(entries) != 2 || entries[0].Command.ID != first.Command.ID || entries[1].Command.ID != second.Command.ID ||
		entries[0].EnvironmentSeq != first.EnvironmentSeq || entries[1].EnvironmentSeq != second.EnvironmentSeq ||
		entries[0].JournalPosition != first.JournalPosition || entries[1].JournalPosition != second.JournalPosition {
		t.Fatalf("same-identity OpenRuntime reopen changed journal order: entries=%+v err=%v", entries, err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatalf("close reopened runtime: %v", err)
	}
	databaseAfter, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(databaseBefore, databaseAfter) {
		t.Fatalf("same-identity reopen unexpectedly changed journal bytes: %v", err)
	}
	if fixture.leafIssuances.Load() != 1 {
		t.Fatalf("OpenRuntime restart re-enrolled the Environment certificate: issuer calls=%d", fixture.leafIssuances.Load())
	}

	for name, wrong := range map[string]Config{
		"domain": func() Config { c := config; c.DomainID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"; return c }(),
		"owner":  fixture.wrongOwnerConfig(config),
		"epoch":  fixture.staleEpochConfig(config),
	} {
		refusalRoot := filepath.Join(privateTempDir(t), "refused-"+name)
		stateCopy := filepath.Join(refusalRoot, "client-state")
		copyPrivateDirectory(t, stateDirectory, stateCopy)
		wrong.ClientStateDirectory = stateCopy
		before, readErr := os.ReadFile(filepath.Join(stateCopy, "client-state.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, openErr := OpenRuntime(refusalRoot, wrong); openErr == nil || name == "epoch" && !errors.Is(openErr, wipdseed.ErrInvalidClientState) {
			t.Errorf("OpenRuntime refusal for wrong %s identity = %v", name, openErr)
		}
		assertNoEnvironmentJournal(t, refusalRoot)
		after, readErr := os.ReadFile(filepath.Join(stateCopy, "client-state.json"))
		if readErr != nil || !bytes.Equal(before, after) {
			t.Errorf("wrong %s identity changed client state: %v", name, readErr)
		}
	}
	if fixture.leafIssuances.Load() != 1 {
		t.Fatalf("wrong-identity starts triggered enrollment: issuer calls=%d", fixture.leafIssuances.Load())
	}

	// The authority's certificate revocation fence must reject startup before
	// OpenRuntime creates a profile-local Environment journal.
	revokedRoot := filepath.Join(privateTempDir(t), "revoked")
	revokedState := filepath.Join(revokedRoot, "client-state")
	copyPrivateDirectory(t, stateDirectory, revokedState)
	revokedConfig := config
	revokedConfig.ClientStateDirectory = revokedState
	certificate, _, err := fixture.store.ValidateEnvironmentEnrollment(context.Background(), openRuntimeDomain,
		credential.EnvironmentID, fixture.enrollmentGrant, identity.CSRDER, time.Now().UTC())
	if err != nil {
		t.Fatalf("read authoritative Environment certificate for revocation: %v", err)
	}
	if err = fixture.store.RevokeEnvironmentCertificate(context.Background(), openRuntimeDomain,
		credential.EnvironmentID, 1, certificate.Generation, time.Now().UTC()); err != nil {
		t.Fatalf("revoke canonical Environment certificate: %v", err)
	}
	stateBeforeRevoke, err := os.ReadFile(filepath.Join(revokedState, "client-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenRuntime(revokedRoot, revokedConfig); err == nil || !strings.Contains(err.Error(), "auth.environment") {
		t.Fatalf("revoked OpenRuntime error = %v, want authoritative auth.environment rejection", err)
	}
	assertNoEnvironmentJournal(t, revokedRoot)
	stateAfterRevoke, err := os.ReadFile(filepath.Join(revokedState, "client-state.json"))
	if err != nil || !bytes.Equal(stateBeforeRevoke, stateAfterRevoke) {
		t.Fatalf("revoked startup changed installed client state: %v", err)
	}
	if fixture.leafIssuances.Load() != 1 {
		t.Fatalf("revoked startup attempted new enrollment: issuer calls=%d", fixture.leafIssuances.Load())
	}
}

func TestOpenRuntimeRefusesReopenAfterCurrentAuthorityEpochAdvances(t *testing.T) {
	fixture := newOpenRuntimeJournalFixture(t)
	profileRoot := filepath.Join(privateTempDir(t), "profile")
	stateDirectory := filepath.Join(privateTempDir(t), "client-state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := fixture.identity
	defer clear(identity.PrivateKeyPKCS8)
	if err := wipdseed.SavePending(stateDirectory, identity); err != nil {
		t.Fatalf("save pending epoch-1 Environment identity: %v", err)
	}
	credential, err := wipdseed.EnrollEnvironment(context.Background(), fixture.profile, fixture.roots,
		fixture.ownerPublic, fixture.caDelegation, identity, fixture.enrollmentGrant, stateDirectory)
	if err != nil {
		t.Fatalf("install epoch-1 Environment credential: %v", err)
	}
	seeded, err := wipdseed.EnrollAndSeed(context.Background(), fixture.profile, fixture.roots,
		fixture.ownerPublic, fixture.caDelegation, openRuntimeRepo, identity, fixture.enrollmentGrant, stateDirectory)
	if err != nil {
		t.Fatalf("enroll and seed epoch-1 Environment: %v", err)
	}
	defer clear(seeded.PrivateKeyPKCS8)
	if credential.EnvironmentID != seeded.EnvironmentID || credential.DomainID != seeded.DomainID || credential.Epoch != seeded.Epoch {
		t.Fatalf("seeded client state changed installed D117 identity: credential=%+v state=%+v", credential, seeded)
	}
	config := fixture.runtimeConfig(stateDirectory)
	if err = SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("save epoch-1 connected profile: %v", err)
	}
	runtime, err := OpenRuntime(profileRoot, config)
	if err != nil {
		t.Fatalf("open epoch-1 runtime: %v", err)
	}
	if got, want := runtime.journal.Identity().AuthorityEpoch, uint64(1); got != want {
		t.Fatalf("initial journal authority epoch = %d, want %d", got, want)
	}
	if err = runtime.Close(); err != nil {
		t.Fatalf("close epoch-1 runtime: %v", err)
	}

	journalPath := filepath.Join(profileRoot, "environment-journal", "command-journal.sqlite")
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDirectory, "client-state.json")
	stateBefore, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", filepath.Join(fixture.authorityRoot, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE domains SET active_epoch=2 WHERE domain_id=?`, openRuntimeDomain); err != nil {
		_ = database.Close()
		t.Fatalf("advance isolated authority current epoch: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close isolated epoch-transition connection: %v", err)
	}
	domain, err := fixture.store.LookupDomain(context.Background(), openRuntimeDomain)
	if err != nil || domain.ActiveEpoch != 2 {
		t.Fatalf("authority current epoch after transition = %+v, %v", domain, err)
	}

	if _, err = OpenRuntime(profileRoot, config); err == nil {
		t.Fatal("reopen accepted an epoch-1 Environment after the authority advanced to epoch 2")
	}
	journalAfter, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(journalBefore, journalAfter) {
		t.Fatalf("stale-epoch reopen changed the existing Environment journal: %v", err)
	}
	stateAfter, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatalf("stale-epoch reopen changed installed client state: %v", err)
	}
}

func assertNoEnvironmentJournal(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, "environment-journal")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refused OpenRuntime created an Environment journal: %v", err)
	}
}

func copyPrivateDirectory(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			t.Fatalf("unexpected non-regular private fixture entry %q", entry.Name())
		}
		data, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
}

type openRuntimeJournalFixture struct {
	store           *authoritystore.Store
	authorityRoot   string
	profile         wipdauthority.Profile
	roots           *x509.CertPool
	ownerPublic     ed25519.PublicKey
	ownerID         string
	caDelegation    []byte
	enrollmentGrant []byte
	artifactCert    []byte
	artifactSigner  ed25519.PrivateKey
	caPrivate       ed25519.PrivateKey
	caDER           []byte
	identity        wipdseed.PreparedIdentity
	serverDER       []byte
	serverPrivate   ed25519.PrivateKey
	leafIssuances   *atomic.Int32
}

func (fixture *openRuntimeJournalFixture) runtimeConfig(stateDirectory string) Config {
	return Config{
		Schema: "wipd.connected-authority-profile/2", CommandCatalogue: "m6-step11",
		Origin: fixture.profile.Origin(), DomainID: openRuntimeDomain, Epoch: 1, RepoID: openRuntimeRepo,
		OwnerRootSPKI: fixture.ownerID, AuthoritySPKIPin: openRuntimeSPKI(nil, mustParseCertificate(fixture.serverDER).PublicKey),
		AuthorityCertificateDER: bytes.Clone(fixture.serverDER), OwnerRootPublicKey: bytes.Clone(fixture.ownerPublic),
		ArtifactKeyCertificate: bytes.Clone(fixture.artifactCert), ClientStateDirectory: stateDirectory,
	}
}

func (fixture *openRuntimeJournalFixture) wrongOwnerConfig(config Config) Config {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	config.OwnerRootPublicKey = bytes.Clone(public)
	config.OwnerRootSPKI = openRuntimeSPKI(nil, public)
	return config
}

func (fixture *openRuntimeJournalFixture) staleEpochConfig(config Config) Config {
	config.Epoch = 2
	config.AuthorityCertificateDER = openRuntimeAuthorityCertificate(nil, fixture.serverPrivate, openRuntimeDomain, fixture.ownerID, 2, time.Now().UTC())
	config.AuthoritySPKIPin = openRuntimeSPKI(nil, mustParseCertificate(config.AuthorityCertificateDER).PublicKey)
	return config
}

func newOpenRuntimeJournalFixture(t *testing.T) *openRuntimeJournalFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(ownerPrivate) })
	ownerID := openRuntimeSPKI(t, ownerPublic)
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate,
		authoritystore.Domain{ID: openRuntimeDomain, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID, ActiveEpoch: 1},
		openRuntimeRepo, bytes.Repeat([]byte{0x31}, 16), now, now.Add(time.Minute))
	clear(setupPrivate)
	if err != nil {
		t.Fatal(err)
	}
	authorityRoot := filepath.Join(privateTempDir(t), "authority")
	store, err := authoritystore.CreateEmpty(authorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	domain := authoritystore.Domain{ID: openRuntimeDomain, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID, ActiveEpoch: 1}
	if err = store.BootstrapDomainWithM5LabGrant(context.Background(), domain, openRuntimeRepo, setupPublic, genesis, now); err != nil {
		t.Fatal(err)
	}

	artifactSignerPublic, artifactSigner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(artifactSigner) })
	artifactCert := openRuntimeOwnerArtifact(t, ownerPrivate, ownerID, "authority-artifact-key", "wipd.authority-artifact-key/1", map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": openRuntimeDomain, "authority_epoch": uint64(1),
		"key_generation": uint64(1), "key_id": openRuntimeSPKI(t, artifactSignerPublic),
		"ed25519_public_key": []byte(artifactSignerPublic),
		"not_before":         now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}, now)
	if err = store.RegisterArtifactKey(context.Background(), openRuntimeDomain, artifactCert, now); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureArtifactKey(context.Background(), openRuntimeDomain, artifactCert, now); err != nil {
		t.Fatal(err)
	}

	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(caPrivate) })
	caDER := openRuntimeCACertificate(t, caPrivate, now)
	caDelegation := openRuntimeOwnerArtifact(t, ownerPrivate, ownerID, "environment-ca-delegation", "wipd.environment-ca-delegation/1", map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": openRuntimeDomain, "authority_epoch": uint64(1),
		"owner_key_id": ownerID, "ca_generation": uint64(1), "ca_key_id": openRuntimeSPKI(t, caPublic),
		"ca_certificate_der": caDER, "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano),
		"not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}, now)
	if err = store.InstallEnvironmentCA(context.Background(), openRuntimeDomain, caDelegation, now); err != nil {
		t.Fatal(err)
	}

	identity, err := wipdseed.PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(identity.PrivateKeyPKCS8) })
	csr, err := x509.ParseCertificateRequest(identity.CSRDER)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentGrant := openRuntimeOwnerArtifact(t, ownerPrivate, ownerID, "enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": openRuntimeGrant, "domain_id": openRuntimeDomain,
		"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-enroll",
		"requested_spki_digest": openRuntimeSPKI(t, csr.PublicKey), "prior_environment_id": nil,
		"nonce": bytes.Repeat([]byte{0x43}, 16), "issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano),
		"expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}, now)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	_, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(serverPrivate) })
	serverDER := openRuntimeAuthorityCertificate(t, serverPrivate, openRuntimeDomain, ownerID, 1, now)
	serverCertificate, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverPin := openRuntimeSPKI(t, serverCertificate.PublicKey)
	profile, err := wipdauthority.NewProfile("https://localhost:"+strconv.Itoa(port), openRuntimeDomain, 1, serverPin, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = profile.WithM5LabRepoID(openRuntimeRepo)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := wipdauthority.NewM6Step11Registry()
	if err != nil {
		t.Fatal(err)
	}
	issuances := &atomic.Int32{}
	server, err := wipdauthority.NewM6LabServer(profile, tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverPrivate}, wipdauthority.M5LabConfig{
		Store: store, RepoID: openRuntimeRepo, EnrollmentGrant: enrollmentGrant, ExpectedCSRDER: identity.CSRDER,
		EnvironmentCACertificateDER: caDER, Registry: registry, ArtifactKeyCertificate: artifactCert, NamedBatchRead: true,
		SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			issuances.Add(1)
			return openRuntimeLeafCertificate(t, caPrivate, caDER, csrDER, domain, environment, ownerID, epoch, at)
		},
		SignArtifact: func(_ context.Context, preimage []byte) ([]byte, error) {
			return ed25519.Sign(artifactSigner, preimage), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case serveErr := <-serverDone:
			if serveErr != nil {
				t.Errorf("OpenRuntime authority shutdown: %v", serveErr)
			}
		case <-time.After(5 * time.Second):
			t.Error("OpenRuntime authority did not stop")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(serverCertificate)
	return &openRuntimeJournalFixture{
		store: store, authorityRoot: authorityRoot, profile: profile, roots: roots, ownerPublic: ownerPublic, ownerID: ownerID,
		caDelegation: caDelegation, enrollmentGrant: enrollmentGrant, artifactCert: artifactCert,
		artifactSigner: artifactSigner, caPrivate: caPrivate, caDER: caDER, identity: identity,
		serverDER: serverDER, serverPrivate: serverPrivate, leafIssuances: issuances,
	}
}

func openRuntimeOwnerArtifact(t *testing.T, owner ed25519.PrivateKey, ownerID, kind, schema string, payload map[string]any, now time.Time) []byte {
	return openRuntimeOwnerArtifactForDomain(t, owner, ownerID, openRuntimeDomain, 1, kind, schema, payload, now)
}

func openRuntimeOwnerArtifactForDomain(t *testing.T, owner ed25519.PrivateKey, ownerID, domainID string, epoch uint64, kind, schema string, payload map[string]any, now time.Time) []byte {
	t.Helper()
	payloadBytes, err := wipdwire.EncodeCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": kind, "domain_id": domainID,
		"authority_epoch": epoch, "signer_role": "owner", "signer_key_id": ownerID,
		"key_generation": nil, "artifact_sequence": nil, "previous_artifact_digest": nil,
		"issued_at": now.Format(time.RFC3339Nano), "payload_schema": schema,
		"payload_digest": openRuntimeDigest(payloadBytes), "payload": payloadBytes,
	}
	preimage, err := wipdwire.EncodeCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	unsigned["signature"] = ed25519.Sign(owner, append([]byte("wipd/signed-artifact/v1\x00"), preimage...))
	encoded, err := wipdwire.EncodeCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func openRuntimeSPKI(t *testing.T, key any) string {
	if t != nil {
		t.Helper()
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func openRuntimeDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func openRuntimeCACertificate(t *testing.T, key ed25519.PrivateKey, now time.Time) []byte {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "D117 test Environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), BasicConstraintsValid: true,
		IsCA: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func openRuntimeAuthorityCertificate(t *testing.T, key ed25519.PrivateKey, domain, owner string, epoch uint64, now time.Time) []byte {
	if t != nil {
		t.Helper()
	}
	ownerHex := strings.TrimPrefix(owner, "sha256:")
	uri := fmt.Sprintf("wipd://authority/%s?epoch=%d&owner=%s", domain, epoch, ownerHex)
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte("localhost")},
		{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)},
	})
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(int64(epoch)), Subject: pkix.Name{CommonName: "D117 test authority"},
		DNSNames: []string{"localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: []int{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return der
}

func openRuntimeLeafCertificate(t *testing.T, caPrivate ed25519.PrivateKey, caDER, csrDER []byte, domain, environment, owner string, epoch uint64, now time.Time) ([]byte, error) {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", environment, domain, epoch, strings.TrimPrefix(owner, "sha256:"))
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "D117 test Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: []int{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: []int{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: []int{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	return x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, caPrivate)
}

func mustParseCertificate(der []byte) *x509.Certificate {
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return certificate
}
