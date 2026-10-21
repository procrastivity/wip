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
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/reporoute"
	localstore "github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("make test temp directory private: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat private test temp directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("test temp directory mode = %04o, want owner-only", info.Mode().Perm())
	}
	return dir
}

func shortPrivateTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wip-")
	if err != nil {
		t.Fatalf("create short private test directory: %v", err)
	}
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("make short test directory private: %v", err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			t.Errorf("remove short private test directory: %v", removeErr)
		}
	})
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("short test directory mode=%v err=%v; want private directory 0700", info, err)
	}
	if socketPath := filepath.Join(dir, "profile", "wipd.sock"); len(socketPath) >= 108 {
		t.Fatalf("short temp profile socket path length=%d exceeds Linux sockaddr_un limit", len(socketPath))
	}
	return dir
}

func TestSaveConfigExactRetryAndConflict(t *testing.T) {
	profileRoot := filepath.Join(privateTempDir(t), "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := validRuntimeTestConfig(t)
	if err := SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("save connected authority profile: %v", err)
	}
	if err := SaveConfig(profileRoot, config); err != nil {
		t.Fatalf("exact profile retry: %v", err)
	}
	loaded, err := LoadConfig(profileRoot)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("loaded profile = %+v, %v; want %+v", loaded, err, config)
	}
	info, err := os.Stat(filepath.Join(profileRoot, configName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("profile file mode = %v, %v; want 0600", info, err)
	}

	conflicting := config
	conflicting.RepoID = "01KZ7XHAQT1S46NYPN1PW1DX3E"
	if err = SaveConfig(profileRoot, conflicting); err == nil {
		t.Fatal("conflicting profile replaced the pinned connected-authority configuration")
	}
	loaded, err = LoadConfig(profileRoot)
	if err != nil || !reflect.DeepEqual(loaded, config) {
		t.Fatalf("conflict changed saved profile = %+v, %v", loaded, err)
	}

	legacyRoot := filepath.Join(privateTempDir(t), "legacy-profile")
	legacy := config
	legacy.Schema = "wipd.connected-authority-profile/1"
	legacy.OwnerRootPublicKey = nil
	legacy.ArtifactKeyCertificate = nil
	if err = SaveConfig(legacyRoot, legacy); err != nil {
		t.Fatalf("save legacy connected profile: %v", err)
	}
	loadedLegacy, err := LoadConfig(legacyRoot)
	if err != nil || !reflect.DeepEqual(loadedLegacy, legacy) {
		t.Fatalf("reopen legacy connected profile = %+v, %v; want %+v", loadedLegacy, err, legacy)
	}
	if (&Runtime{config: loadedLegacy}).SupportsClaimAcquisition() {
		t.Fatal("legacy connected profile enabled acquisition without grant-verification trust")
	}
}

func TestD115StartupUsesOwnerMediatedAuthenticatedLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("named wipd launcher authenticated IPC is Linux-only")
	}
	if action := os.Getenv("WIPD_D115_TEST_CLIENT_ACTION"); action != "" {
		runD115StartupClientHelper(t, action)
		return
	}
	if os.Getenv("WIPD_ATTACH_TEST_CLIENT_ACTION") != "" {
		runAttachStartupClientHelper(t)
		return
	}
	testRoot := shortPrivateTempDir(t)
	profileRoot := filepath.Join(testRoot, "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(testRoot, "authority")
	store, err := authoritystore.CreateEmpty(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	operatorPublic, operatorPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	operatorID, _ := d115.KeyID(operatorPublic)
	ownerID, _ := d115.KeyID(ownerPublic)
	now := time.Now().UTC().Truncate(time.Second)
	trust, err := store.D115Trust(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trust, err = store.UpdateD115Trust(context.Background(), trust.Generation, true, &authoritystore.D115OperatorKey{
		KeyID: operatorID, PublicKey: operatorPublic, NotBefore: now.Add(-time.Minute).Format(time.RFC3339Nano),
		NotAfter: now.Add(time.Hour).Format(time.RFC3339Nano), State: "active",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + listener.Addr().String()
	realmID := trust.RealmID
	certificate, certificateDER, pin := runtimeD115TestCertificate(t, realmID)
	authorityServer, err := wipdauthority.NewM6D115ProvisioningLabServer(origin, certificate, store)
	if err != nil {
		t.Fatal(err)
	}
	authorityCtx, stopAuthority := context.WithCancel(context.Background())
	authorityDone := make(chan error, 1)
	go func() { authorityDone <- authorityServer.Serve(authorityCtx, listener) }()
	t.Cleanup(func() {
		stopAuthority()
		if serveErr := <-authorityDone; serveErr != nil {
			t.Errorf("D115 authority server shutdown: %v", serveErr)
		}
	})
	config := wipdseed.D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "startup-test",
		Origin: origin, ProvisioningRealmID: realmID, AuthoritySPKIPin: pin, AuthorityCertificateDER: certificateDER,
	}
	if err = wipdseed.SaveD115ProvisioningConfig(profileRoot, config); err != nil {
		t.Fatal(err)
	}
	peerCheckout := filepath.Join(testRoot, "peer-checkout")
	if err = os.Mkdir(peerCheckout, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, initErr := exec.Command("git", "init", peerCheckout).CombinedOutput(); initErr != nil {
		t.Fatalf("initialize disposable authenticated-peer checkout: %v\n%s", initErr, output)
	}
	if _, runtime, newErr := NewServer(profileRoot); newErr != nil || runtime != nil {
		t.Fatalf("production NewServer must configure D115 without a signer: runtime=%v err=%v", runtime, newErr)
	}
	if _, _, err = NewServerWithD115OwnerSigner(profileRoot, &runtimeD115OwnerSigner{keyID: ownerID, private: ownerPrivate}); err == nil {
		t.Fatal("daemon runtime accepted an owner signer")
	}
	grantID, err := d115.NewULID(now)
	if err != nil {
		t.Fatal(err)
	}
	claimsBytes, err := d115.EncodeClaims(d115.Claims{
		Schema: d115.ClaimsSchema, GrantID: grantID, Audience: realmID,
		Scope: d115.CreateDomainScope, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID,
		IssuedAt: now.Add(-time.Second).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Second).Format(time.RFC3339Nano),
		ExpiresAt: now.Add(8 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := d115.Grant{Schema: d115.GrantSchema, Claims: claimsBytes, SignerKeyID: operatorID, Signature: make([]byte, ed25519.SignatureSize)}
	preimage, err := d115.GrantSigningPreimage(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Signature = ed25519.Sign(operatorPrivate, preimage)
	grantBytes, err := d115.EncodeGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, runtime, newErr := NewServer(profileRoot); newErr != nil || runtime != nil {
		t.Fatalf("NewServer() = runtime %v, err %v", runtime, newErr)
	}
	binary := filepath.Join(t.TempDir(), "wipd")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/wipd")
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build named wipd launcher: %v\n%s", buildErr, output)
	}
	var process *exec.Cmd
	var processDone chan error
	var processOutput bytes.Buffer
	startProcess := func() {
		t.Helper()
		processOutput.Reset()
		process = exec.Command(binary, "--profile-root", profileRoot)
		process.Stdout, process.Stderr = &processOutput, &processOutput
		if startErr := process.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		processDone = make(chan error, 1)
		go func(command *exec.Cmd, done chan<- error) { done <- command.Wait() }(process, processDone)
	}
	stopProcess := func() {
		t.Helper()
		if process == nil {
			return
		}
		if process.Process != nil {
			_ = process.Process.Signal(os.Interrupt)
		}
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			_ = process.Process.Kill()
			<-processDone
		}
		process = nil
	}
	startProcess()
	t.Cleanup(stopProcess)
	var client *wipd.Client
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		client, err = wipd.Connect(context.Background(), profileRoot)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect to named wipd launcher: %v", err)
	}
	_ = client.Close()
	preparedResult := runD115StartupClient(t, peerCheckout, testRoot, profileRoot, "prepare", base64.RawURLEncoding.EncodeToString(grantBytes), "", nil)
	prepared := preparedResult.Attempt
	if prepared.State != string(wipdseed.D115PendingSignature) {
		t.Fatalf("authenticated prepare = %+v", prepared)
	}
	pinned, requestErr := d115.DecodeOwnerSigningRequest(prepared.SigningRequest)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	proof, requestErr := d115.EncodeOwnerProofArtifact(d115.OwnerProofArtifact{
		Schema: d115.OwnerProofSchema, RealmID: pinned.RealmID, CommandID: pinned.CommandID, RequestHash: pinned.RequestHash,
		GrantDigest: pinned.GrantDigest, OwnerKeyID: pinned.OwnerKeyID, OwnerProof: ed25519.Sign(ownerPrivate, pinned.Preimage),
	})
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	importedResult := runD115StartupClient(t, peerCheckout, testRoot, profileRoot, "import", "", prepared.AttemptID, proof)
	imported := importedResult.Attempt
	if imported.State != string(wipdseed.D115Prepared) {
		t.Fatalf("proof import = %+v", imported)
	}
	submitResult := runD115StartupClient(t, peerCheckout, testRoot, profileRoot, "submit", "", prepared.AttemptID, nil)
	if submitResult.Receipt == nil {
		t.Fatal("authenticated submit helper returned no receipt")
	}
	receipt := *submitResult.Receipt
	if receipt.RealmID != realmID || receipt.OwnerKeyID != ownerID {
		t.Fatalf("authenticated create-local receipt=%+v", receipt)
	}
	domain, err := store.LookupDomain(context.Background(), receipt.DomainID)
	if err != nil || domain.OwnerKeyID != ownerID || domain.ActiveEpoch != 1 {
		t.Fatalf("authority-minted default domain = %+v, %v", domain, err)
	}
	if boundDomain, bindErr := store.RepoDomain(context.Background(), receipt.RepoID); bindErr != nil || boundDomain != receipt.DomainID {
		t.Fatalf("authority-minted initial Repo membership domain=%q err=%v", boundDomain, bindErr)
	}
	if output, remoteErr := exec.Command("git", "-C", peerCheckout, "remote", "add", "origin", "https://github.com/acme/widget.git").CombinedOutput(); remoteErr != nil {
		t.Fatalf("configure authenticated peer origin: %v\n%s", remoteErr, output)
	}
	commonDir, err := exec.Command("git", "-C", peerCheckout, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := exec.Command("git", "-C", peerCheckout, "rev-parse", "--path-format=absolute", "--git-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(commonDir)) != strings.TrimSpace(string(gitDir)) {
		t.Fatalf("public attach fixture is not the main checkout: common dir=%q git dir=%q", commonDir, gitDir)
	}
	bindingDigest := sha256.Sum256([]byte("wipd/d115-checkout-binding/v1\x00" + strings.TrimSpace(string(commonDir)) + "\x00" + strings.TrimSpace(string(gitDir))))
	checkoutBinding := "sha256:" + hex.EncodeToString(bindingDigest[:])
	installedD115, err := wipdseed.LoadInstalledD115ReceiptForAttach(profileRoot, prepared.AttemptID, checkoutBinding)
	if err != nil || installedD115.Receipt.DomainID != receipt.DomainID || installedD115.Receipt.RepoID != receipt.RepoID {
		t.Fatalf("exact D115 attempt/receipt is not selectable for attach: receipt=%+v err=%v", installedD115, err)
	}
	// The attached projection must be created from the exact D115 Repo identity;
	// no local Repo row is seeded before the authenticated IPC flow.
	legacyDatabasePath := filepath.Join(shortPrivateTempDir(t), "legacy-wip.db")
	t.Setenv("WIP_DB_PATH", legacyDatabasePath)
	databasePath := filepath.Join(profileRoot, "local-registry.db")
	localStore, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if repos, readErr := localStore.Repos(context.Background()); readErr != nil || len(repos) != 0 {
		t.Fatalf("pre-attach Repo projection is not empty: repos=%+v err=%v", repos, readErr)
	}
	if err = localStore.Close(); err != nil {
		t.Fatal(err)
	}
	stopProcess()
	terminalCommitted := make(chan operation.Command, 1)
	connectionProfile, grantArtifact, rotateCanonicalCredential, stopM6 := startD115AttachAuthority(t, store, profileRoot, receipt, ownerPublic, ownerPrivate, terminalCommitted)
	t.Cleanup(stopM6)
	if err = SavePrebindingConfig(profileRoot, PrebindingConfig{
		Schema:   "wipd.route-resolver-profile/1",
		Entries:  []reporoute.Entry{{Key: "github.com/acme/widget", DomainID: receipt.DomainID, ConnectionProfile: "primary"}},
		Profiles: []PrebindingProfile{connectionProfile},
	}); err != nil {
		t.Fatalf("save D54 route resolver profile: %v", err)
	}
	connection, err := buildPrebindingConnection(profileRoot, connectionProfile)
	if err != nil {
		t.Fatalf("build connected D54 profile: %v", err)
	}
	clientStateDirectory := connection.ProfileRoot
	if _, err = wipdseed.SeedRepoFromEnvironmentCredential(context.Background(), connection.AuthorityProfile,
		connection.AuthorityRoots, connection.OwnerRoot, connection.CADElegation, receipt.RepoID,
		connection.ProfileRoot, clientStateDirectory); err != nil {
		t.Fatalf("seed connected M2 client state: %v", err)
	}
	startProcess()
	if err = waitForWipd(profileRoot); err != nil {
		t.Fatalf("start prebinding-only wipd: %v; output: %s", err, processOutput.String())
	}
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(peerCheckout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })
	firstResult, firstErr := attachViaPublicIPCHelper(t, profileRoot, grantArtifact, 10*time.Second)
	if firstErr == nil || firstResult.Attempt.Receipt != nil {
		t.Fatalf("injected terminal response loss returned success: result=%+v err=%v", firstResult, firstErr)
	}
	select {
	case command := <-terminalCommitted:
		if command.Request.Operation != operation.EnvironmentAttachOnceV1.Metadata().Operation {
			t.Fatalf("fault hook ran for unexpected operation %s", command.Request.Operation.Name)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("attach command did not reach durable terminal commit before the client deadline: first result=%+v err=%v", firstResult, firstErr)
	}
	if binding, _, routeErr := store.LookupRepoRouteSnapshotForDomain(context.Background(), "github.com/acme/widget", receipt.DomainID, 1); !errors.Is(routeErr, authoritystore.ErrRouteUnbound) {
		t.Fatalf("AttachOnce mutated or conflicted with the route index: binding=%+v err=%v", binding, routeErr)
	}
	initialCredential, initialCertificate, err := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot,
		connection.AuthorityProfile, connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
	if err != nil {
		t.Fatalf("load pre-rotation canonical credential: %v", err)
	}
	initialSPKI := initialCredential.SPKIDigest
	clear(initialCredential.PrivateKeyPKCS8)
	clear(initialCertificate.PrivateKey.(ed25519.PrivateKey))
	rotateCanonicalCredential()
	rotatedCredential, rotatedCertificate, err := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot,
		connection.AuthorityProfile, connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
	if err != nil || rotatedCredential.SPKIDigest == initialSPKI {
		t.Fatalf("canonical Environment credential did not rotate: initial=%s rotated=%s err=%v", initialSPKI, rotatedCredential.SPKIDigest, err)
	}
	environmentID := rotatedCredential.EnvironmentID
	clear(rotatedCredential.PrivateKeyPKCS8)
	clear(rotatedCertificate.PrivateKey.(ed25519.PrivateKey))
	expireD115ProvisioningConfig(t, profileRoot, certificate)
	stopProcess()
	startProcess()
	if err = waitForWipd(profileRoot); err != nil {
		t.Fatalf("restart wipd for exact attach recovery: %v", err)
	}
	attachedResult, attachErr := attachViaPublicIPCHelper(t, profileRoot, grantArtifact, 30*time.Second)
	attached := attachedResult.Attempt
	if attachErr != nil || attached.State != string(wipdseed.EnvironmentAttachInstalled) || len(attached.Receipt) == 0 || len(attached.SignedReceipt) == 0 {
		t.Fatalf("exact attach recovery = %+v, %v; daemon output: %s", attachedResult, attachErr, processOutput.String())
	}
	postAttachSeed, seedErr := wipdseed.SeedRepoFromEnvironmentCredential(context.Background(), connection.AuthorityProfile,
		connection.AuthorityRoots, connection.OwnerRoot, connection.CADElegation, receipt.RepoID,
		connection.ProfileRoot, filepath.Join(profileRoot, "post-attach-seed"))
	if seedErr != nil || postAttachSeed.RepoID != receipt.RepoID || postAttachSeed.EnvironmentID != environmentID {
		t.Fatalf("seed fresh Repo client from attached event history: state=%+v err=%v", postAttachSeed, seedErr)
	}
	attachedEvents := 0
	for _, event := range postAttachSeed.EventRecords {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(event.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatalf("decode seeded authority event %s: %v", event.EventID, decodeErr)
		}
		if fields["kind"] == "environment.checkout-attached/1" {
			attachedEvents++
		}
	}
	if attachedEvents != 1 {
		t.Fatalf("fresh Repo seed contains %d attach events; want exactly one", attachedEvents)
	}
	localStore, err = localstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = localStore.Close() }()
	repo, err := localStore.Repo(context.Background(), receipt.RepoID)
	if err != nil || repo.ID != receipt.RepoID || repo.RemoteURL != "" || repo.IdentityRemote != "" {
		t.Fatalf("D115 Repo projection = %+v, %v", repo, err)
	}
	if _, statErr := os.Lstat(legacyDatabasePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("attach projection touched the default WIP database override: stat error=%v", statErr)
	}
	clone, cloneFound, err := localStore.CloneByCommonDir(context.Background(), filepath.Join(peerCheckout, ".git"))
	if err != nil || !cloneFound || clone.ID == "" || clone.Repo != receipt.RepoID {
		t.Fatalf("receipt-driven Clone projection = %+v found=%t err=%v", clone, cloneFound, err)
	}
	worktree, worktreeFound, err := localStore.WorktreeByName(context.Background(), clone.ID, "")
	if err != nil || !worktreeFound || worktree.ID == "" {
		t.Fatalf("receipt-driven Worktree projection = %+v found=%t err=%v", worktree, worktreeFound, err)
	}
	outputFields, err := attachedReceiptOutput(attached.Receipt)
	if err != nil || outputFields["repo_id"] != receipt.RepoID || outputFields["clone_id"] != clone.ID || outputFields["worktree_id"] != worktree.ID {
		t.Fatalf("local projection IDs differ from signed attach output: output=%v clone=%+v worktree=%+v err=%v", outputFields, clone, worktree, err)
	}
	terminalFields, err := wipdwire.DecodeCanonicalMap(attached.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatalf("decode retained attach receipt identity: %v", err)
	}
	attachCommandID, ok := terminalFields["command_id"].(string)
	if !ok {
		t.Fatal("retained attach receipt omitted command ID")
	}
	authorityDB, err := sql.Open("sqlite", filepath.Join(storeRoot, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authorityDB.Close() }()
	var retainedReceipt, retainedSigned []byte
	if err = authorityDB.QueryRow(`SELECT receipt,wrapper FROM terminal_receipts WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&retainedReceipt, &retainedSigned); err != nil ||
		!bytes.Equal(retainedReceipt, attached.Receipt) || !bytes.Equal(retainedSigned, attached.SignedReceipt) {
		t.Fatalf("exact retry did not return the one retained portable terminal: receipt_equal=%t signed_equal=%t err=%v", bytes.Equal(retainedReceipt, attached.Receipt), bytes.Equal(retainedSigned, attached.SignedReceipt), err)
	}
	var consumptionCount, attachEventCount int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM environment_attach_grant_consumptions WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&consumptionCount); err != nil || consumptionCount != 1 {
		t.Fatalf("attach grant consumption count=%d err=%v; want exactly one", consumptionCount, err)
	}
	if err = authorityDB.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&attachEventCount); err != nil || attachEventCount != 1 {
		t.Fatalf("attach authority event count=%d err=%v; want exactly one", attachEventCount, err)
	}
	if err = localStore.Close(); err != nil {
		t.Fatal(err)
	}
	var registeredCheckoutCount int
	environmentFields, environmentOK := terminalFields["environment"].(map[string]any)
	environmentID, environmentIDOK := environmentFields["id"].(string)
	if !environmentOK || !environmentIDOK {
		t.Fatalf("attach terminal environment identity is malformed: %#v", terminalFields["environment"])
	}
	var attachedWorktreeName string
	if err = authorityDB.QueryRow(`SELECT count(*),coalesce(max(worktree_name),'') FROM environment_checkout_attachments
		WHERE domain_id=? AND environment_id=? AND repo_id=? AND clone_id=? AND worktree_id=? AND normalized_repo_key=?`,
		receipt.DomainID, environmentID, receipt.RepoID, clone.ID, worktree.ID, "github.com/acme/widget").Scan(&registeredCheckoutCount, &attachedWorktreeName); err != nil || registeredCheckoutCount != 1 || attachedWorktreeName != "" {
		t.Fatalf("D115 main-checkout attachment count=%d worktree name=%q err=%v; want one row with empty name", registeredCheckoutCount, attachedWorktreeName, err)
	}
	if code := submitStep11RouteCommand(t, profileRoot, receipt, clone.ID, worktree.ID, 2,
		operation.RepoAdoptRemoteV1, "github.com/acme/widget"); code != operation.ResultSucceeded {
		t.Fatalf("prebinding repo.adopt-remote result = %s", code)
	}
	if err = SaveConfig(profileRoot, Config{
		Schema: "wipd.connected-authority-profile/2", CommandCatalogue: "m6-step11",
		Origin: connection.AuthorityProfile.Origin(), DomainID: connection.AuthorityProfile.DomainID(),
		Epoch: connection.AuthorityProfile.Epoch(), RepoID: receipt.RepoID,
		OwnerRootSPKI:           connection.AuthorityProfile.OwnerRootSPKI(),
		AuthoritySPKIPin:        connection.AuthorityProfile.AuthoritySPKI(),
		AuthorityCertificateDER: bytes.Clone(connectionProfile.AuthorityCertificateDER),
		OwnerRootPublicKey:      bytes.Clone(connectionProfile.OwnerRootPublicKey),
		ArtifactKeyCertificate:  bytes.Clone(connection.ArtifactKeyCertificate),
		ClientStateDirectory:    clientStateDirectory,
	}); err != nil {
		t.Fatalf("save connected Step11 authority profile: %v", err)
	}
	stopProcess()
	startProcess()
	if err = waitForWipd(profileRoot); err != nil {
		t.Fatalf("restart daemon before next connected route command: %v", err)
	}
	if code := submitStep11RouteCommand(t, profileRoot, receipt, clone.ID, worktree.ID, 3,
		operation.RepoRouteBindV1, "github.com/acme/widget"); code != operation.ResultSucceeded {
		t.Fatalf("authenticated registered-checkout repo.route-bind result = %s", code)
	}
	if binding, err := store.LookupRepoRoute(context.Background(), "github.com/acme/widget"); err != nil || binding.RepoID != receipt.RepoID || binding.DomainID != receipt.DomainID {
		t.Fatalf("authenticated route-bind/adoption binding = %+v err=%v", binding, err)
	}
	routeClient, err := wipd.ConnectStep11Routes(context.Background(), profileRoot)
	if err != nil {
		t.Fatalf("connect for public remembered route resolution: %v", err)
	}
	resolvedRoute, resolveErr := routeClient.ResolveRoute(context.Background())
	_ = routeClient.Close()
	if resolveErr != nil || resolvedRoute.RepoID == nil || *resolvedRoute.RepoID != receipt.RepoID ||
		resolvedRoute.DomainID != receipt.DomainID || resolvedRoute.Epoch != 1 {
		t.Fatalf("public remembered route resolution = %+v err=%v", resolvedRoute, resolveErr)
	}
	if code := submitStep11RouteCommand(t, profileRoot, receipt, clone.ID, worktree.ID, 4,
		operation.RepoAdoptRemoteV1, "github.com/acme/other"); code != operation.ResultRefused {
		t.Fatalf("non-null Repo adoption control result = %s; want refused", code)
	}
	if binding, err := store.LookupRepoRoute(context.Background(), "github.com/acme/widget"); err != nil || binding.RepoID != receipt.RepoID {
		t.Fatalf("refused conflicting adoption changed the route binding: %+v err=%v", binding, err)
	}
	var submissionsBeforeMismatchedIDs int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND environment_id=?`,
		receipt.DomainID, environmentID).Scan(&submissionsBeforeMismatchedIDs); err != nil {
		t.Fatal(err)
	}
	if code := submitStep11RouteCommand(t, profileRoot, receipt, receipt.RepoID, worktree.ID, 5,
		operation.RepoRouteBindV1, "github.com/acme/widget"); code != operation.ResultRefused {
		t.Fatalf("route-bind with a CloneID not registered for the live checkout = %s; want refused", code)
	}
	var submissionsAfterMismatchedIDs int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND environment_id=?`,
		receipt.DomainID, environmentID).Scan(&submissionsAfterMismatchedIDs); err != nil || submissionsAfterMismatchedIDs != submissionsBeforeMismatchedIDs {
		t.Fatalf("mismatched local route IDs reached M2: before=%d after=%d err=%v", submissionsBeforeMismatchedIDs, submissionsAfterMismatchedIDs, err)
	}
	var submissionsBefore int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&submissionsBefore); err != nil || submissionsBefore != 1 {
		t.Fatalf("attach submission count before D115 evidence removal=%d err=%v; want one", submissionsBefore, err)
	}
	if output, remoteErr := exec.Command("git", "-C", peerCheckout, "remote", "set-url", "origin", "https://github.com/acme/changed.git").CombinedOutput(); remoteErr != nil {
		t.Fatalf("change checkout remote for exact replay refusal: %v\n%s", remoteErr, output)
	}
	if _, changedRemoteErr := attachViaPublicIPCHelper(t, profileRoot, grantArtifact, 15*time.Second); changedRemoteErr == nil {
		t.Fatal("attach replay accepted a changed live normalized remote")
	}
	if output, remoteErr := exec.Command("git", "-C", peerCheckout, "remote", "set-url", "origin", "https://github.com/acme/widget.git").CombinedOutput(); remoteErr != nil {
		t.Fatalf("restore checkout remote after replay refusal: %v\n%s", remoteErr, output)
	}
	var afterChangedRemote int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&afterChangedRemote); err != nil || afterChangedRemote != submissionsBefore {
		t.Fatalf("changed-remote replay resubmitted M2 command: before=%d after=%d err=%v", submissionsBefore, afterChangedRemote, err)
	}
	if err = os.Remove(filepath.Join(profileRoot, "d115-attempts", "receipts", prepared.AttemptID+".cbor")); err != nil {
		t.Fatalf("remove installed D115 receipt for refusal control: %v", err)
	}
	_, replayErr := attachViaPublicIPCHelper(t, profileRoot, grantArtifact, 15*time.Second)
	if replayErr == nil {
		t.Fatalf("attach replay without its pinned D115 evidence = %v; want refusal", replayErr)
	}
	var submissionsAfter int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, receipt.DomainID, attachCommandID).Scan(&submissionsAfter); err != nil || submissionsAfter != submissionsBefore {
		t.Fatalf("D115-evidence refusal resubmitted M2 command: before=%d after=%d err=%v", submissionsBefore, submissionsAfter, err)
	}
}

func attachedReceiptOutput(receipt []byte) (map[string]any, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return nil, err
	}
	result, ok := fields["result"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid terminal result map")
	}
	output, ok := result["output"].([]byte)
	if !ok {
		return nil, errors.New("invalid terminal output bytes")
	}
	return wipdwire.DecodeCanonicalMap(output, "repo_id", "domain_id", "authority_epoch", "environment_id", "clone_id", "worktree_id")
}

func submitStep11RouteCommand(t *testing.T, profileRoot string, receipt d115.Receipt, cloneID, worktreeID string, sequence uint64,
	definition operation.Definition, normalizedKey string,
) operation.ResultCode {
	t.Helper()
	resolver, err := loadPrebindingResolver(profileRoot)
	if err != nil || resolver == nil || len(resolver.Connections) != 1 {
		t.Fatalf("load D54 connection for route command: resolver=%v err=%v", resolver, err)
	}
	connection := resolver.Connections["primary"]
	if connection.AuthorityProfile.DomainID() != receipt.DomainID {
		t.Fatalf("route command profile domain = %s; want %s", connection.AuthorityProfile.DomainID(), receipt.DomainID)
	}
	credential, certificate, err := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot, connection.AuthorityProfile,
		connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
	if err != nil {
		t.Fatalf("load Environment identity for route command: %v", err)
	}
	environmentID := credential.EnvironmentID
	clear(credential.PrivateKeyPKCS8)
	clear(certificate.PrivateKey.(ed25519.PrivateKey))
	commandID, err := d115.NewULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request := operation.Request{
		Operation: definition.Metadata().Operation, Actor: "human",
		Context: operation.Context{Repo: receipt.RepoID, Clone: cloneID, Worktree: worktreeID},
	}
	switch definition.Metadata().Operation {
	case operation.RepoRouteBindV1.Metadata().Operation:
		request.Input = operation.RepoRouteBindInput{NormalizedKey: normalizedKey}
	case operation.RepoAdoptRemoteV1.Metadata().Operation:
		request.Input = operation.RepoAdoptRemoteInput{NormalizedKey: normalizedKey}
	default:
		t.Fatalf("unsupported route command %s", definition.Metadata().Operation.Name)
	}
	now := time.Now().UTC().Truncate(time.Second)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: receipt.DomainID, ExpectedAuthorityEpoch: 1,
		EnvironmentID: environmentID, EnvironmentSequence: sequence,
		ActedAt: now.Format(time.RFC3339Nano), CorrelationCommandID: commandID, Request: request,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := wipd.ConnectStep11Routes(ctx, profileRoot)
	if err != nil {
		t.Fatalf("connect authenticated local Step11 IPC: %v", err)
	}
	defer func() { _ = client.Close() }()
	result, err := client.ExecuteCommand(ctx, command)
	if err != nil {
		t.Fatalf("submit public authenticated %s: %v", definition.Metadata().Operation.Name, err)
	}
	return result.Code
}

func startD115AttachAuthority(t *testing.T, authorityStore *authoritystore.Store, profileRoot string, d115Receipt d115.Receipt,
	ownerPublic ed25519.PublicKey, ownerPrivate ed25519.PrivateKey, terminalCommitted chan<- operation.Command,
) (PrebindingProfile, string, func(), func()) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ownerID, err := d115.KeyID(ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	origin := "https://localhost:" + strconv.Itoa(port)
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER := openRuntimeAuthorityCertificate(t, serverPrivate, d115Receipt.DomainID, ownerID, 1, now)
	serverLeaf, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverPin := openRuntimeSPKI(t, serverPublic)
	profile, err := wipdauthority.NewProfile(origin, d115Receipt.DomainID, 1, serverPin, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = profile.WithM5LabRepoID(d115Receipt.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate := tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverPrivate, Leaf: serverLeaf}
	artifactPublic, artifactPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := openRuntimeSPKI(t, artifactPublic)
	artifactCertificate := openRuntimeOwnerArtifactForDomain(t, ownerPrivate, ownerID, d115Receipt.DomainID, 1,
		"authority-artifact-key", "wipd.authority-artifact-key/1", map[string]any{
			"schema": "wipd.authority-artifact-key/1", "domain_id": d115Receipt.DomainID, "authority_epoch": uint64(1),
			"key_generation": uint64(1), "key_id": artifactID, "ed25519_public_key": []byte(artifactPublic),
			"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		}, now)
	if err = authorityStore.RegisterArtifactKey(context.Background(), d115Receipt.DomainID, artifactCertificate, now); err != nil {
		t.Fatal(err)
	}
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caDER := openRuntimeCACertificate(t, caPrivate, now)
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caDelegation := openRuntimeOwnerArtifactForDomain(t, ownerPrivate, ownerID, d115Receipt.DomainID, 1,
		"environment-ca-delegation", "wipd.environment-ca-delegation/1", map[string]any{
			"schema": "wipd.environment-ca-delegation/1", "domain_id": d115Receipt.DomainID, "authority_epoch": uint64(1),
			"owner_key_id": ownerID, "ca_generation": uint64(1), "ca_key_id": openRuntimeSPKI(t, caPublic),
			"ca_certificate_der": caDER, "not_before": caCertificate.NotBefore.UTC().Format(time.RFC3339Nano),
			"not_after": caCertificate.NotAfter.UTC().Format(time.RFC3339Nano),
		}, now)
	if err = authorityStore.InstallEnvironmentCA(context.Background(), d115Receipt.DomainID, caDelegation, now); err != nil {
		t.Fatal(err)
	}
	identity, err := wipdseed.PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	credentialRoot := filepath.Join(profileRoot, "attach-credentials")
	if err = os.Mkdir(credentialRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = wipdseed.SavePending(credentialRoot, identity); err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(identity.CSRDER)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentID, err := d115.NewULID(now)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentGrant := openRuntimeOwnerArtifactForDomain(t, ownerPrivate, ownerID, d115Receipt.DomainID, 1,
		"enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
			"schema": "wipd.enrollment-grant/1", "grant_id": enrollmentID, "domain_id": d115Receipt.DomainID,
			"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-enroll",
			"requested_spki_digest": openRuntimeSPKI(t, csr.PublicKey), "prior_environment_id": nil,
			"nonce": bytes.Repeat([]byte{0x37}, 16), "issued_at": now.Add(-time.Second).Format(time.RFC3339Nano),
			"expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
		}, now)
	registry, err := wipdauthority.NewM6Step11Registry()
	if err != nil {
		t.Fatal(err)
	}
	var dropOnce sync.Once
	server, err := wipdauthority.NewM6D115LabServer(profile, serverCertificate, wipdauthority.M5LabConfig{
		Store: authorityStore, RepoID: d115Receipt.RepoID, EnrollmentGrant: enrollmentGrant,
		ExpectedCSRDER: identity.CSRDER, EnvironmentCACertificateDER: caDER, Registry: registry, NamedBatchRead: true,
		ArtifactKeyCertificate: artifactCertificate,
		SignArtifact: func(_ context.Context, preimage []byte) ([]byte, error) {
			return ed25519.Sign(artifactPrivate, preimage), nil
		},
		SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			return openRuntimeLeafCertificate(t, caPrivate, caDER, csrDER, domain, environment, ownerID, epoch, at)
		},
		AfterCommandTerminalCommit: func(ctx context.Context, command operation.Command) {
			if command.Request.Operation == operation.EnvironmentAttachOnceV1.Metadata().Operation {
				dropOnce.Do(func() {
					terminalCommitted <- command
					<-ctx.Done()
				})
			}
		},
	}, mustEncodeD115Receipt(t, d115Receipt))
	if err != nil {
		t.Fatal(err)
	}
	serveContext, stop := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveContext, listener) }()
	roots := x509.NewCertPool()
	roots.AddCert(serverLeaf)
	clientProfile, err := wipdauthority.NewProfile(origin, d115Receipt.DomainID, 1, serverPin, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wipdseed.EnrollEnvironment(context.Background(), clientProfile, roots, ownerPublic, caDelegation,
		identity, enrollmentGrant, credentialRoot); err != nil {
		stop()
		_ = listener.Close()
		t.Fatalf("enroll generated attach test Environment: %v", err)
	}
	credential, _, err := wipdseed.LoadEnvironmentCredential(credentialRoot, clientProfile, ownerPublic, caDelegation, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(credential.PrivateKeyPKCS8)
	grantID, err := d115.NewULID(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	grantBytes := openRuntimeOwnerArtifactForDomain(t, ownerPrivate, ownerID, d115Receipt.DomainID, 1,
		"environment-attach-grant", "wipd.environment-attach-grant/1", map[string]any{
			"schema": "wipd.environment-attach-grant/1", "grant_id": grantID, "nonce": bytes.Repeat([]byte{0x38}, 16),
			"domain_id": d115Receipt.DomainID, "authority_epoch": uint64(1), "owner_key_id": ownerID,
			"scope": "environment.attach-once@v1", "audience": map[string]any{
				"schema": "wipd.attach-audience/1", "kind": "authority-spki", "authority_spki_digest": serverPin,
			}, "repo_id": d115Receipt.RepoID,
			"normalized_repo_key": "github.com/acme/widget", "environment_id": credential.EnvironmentID,
			"environment_spki_digest": credential.SPKIDigest, "issued_at": now.Format(time.RFC3339Nano),
			"expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
		}, now)
	if claims, verifyErr := authoritystore.VerifyEnvironmentAttachGrantArtifact(base64.RawURLEncoding.EncodeToString(grantBytes),
		ownerPublic, d115Receipt.DomainID, 1, ownerID, time.Now().UTC()); verifyErr != nil || claims.AudienceSPKI != serverPin {
		t.Fatalf("generated attach grant did not verify against its pinned D54 audience: claims=%+v err=%v", claims, verifyErr)
	}
	rotateCanonicalCredential := func() {
		t.Helper()
		rotatedIdentity, prepareErr := wipdseed.PrepareIdentity()
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		defer clear(rotatedIdentity.PrivateKeyPKCS8)
		csr, parseErr := x509.ParseCertificateRequest(rotatedIdentity.CSRDER)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		rotatedAt := time.Now().UTC().Truncate(time.Second)
		rotationID, idErr := d115.NewULID(rotatedAt)
		if idErr != nil {
			t.Fatal(idErr)
		}
		rotationGrant := openRuntimeOwnerArtifactForDomain(t, ownerPrivate, ownerID, d115Receipt.DomainID, 1,
			"enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
				"schema": "wipd.enrollment-grant/1", "grant_id": rotationID, "domain_id": d115Receipt.DomainID,
				"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-rotate",
				"requested_spki_digest": openRuntimeSPKI(t, csr.PublicKey), "prior_environment_id": credential.EnvironmentID,
				"nonce": bytes.Repeat([]byte{0x39}, 16), "issued_at": rotatedAt.Add(-time.Second).Format(time.RFC3339Nano),
				"expires_at": rotatedAt.Add(5 * time.Minute).Format(time.RFC3339Nano),
			}, rotatedAt)
		leafDER, leafErr := openRuntimeLeafCertificate(t, caPrivate, caDER, rotatedIdentity.CSRDER,
			d115Receipt.DomainID, credential.EnvironmentID, ownerID, 1, rotatedAt)
		if leafErr != nil {
			t.Fatalf("create rotated Environment certificate: %v", leafErr)
		}
		issued, issueErr := authorityStore.IssueEnvironmentCertificate(context.Background(), d115Receipt.DomainID,
			credential.EnvironmentID, rotationGrant, rotatedIdentity.CSRDER, [][]byte{leafDER, caDER}, rotatedAt)
		if issueErr != nil {
			t.Fatalf("issue rotated canonical Environment credential: %v", issueErr)
		}
		rotated := wipdseed.EnvironmentCredential{
			Schema: "wipd.environment-credential/1", DomainID: d115Receipt.DomainID, Epoch: 1,
			EnvironmentID: credential.EnvironmentID, OwnerKeyID: ownerID, SPKIDigest: issued.SPKIDigest,
			PrivateKeyPKCS8: bytes.Clone(rotatedIdentity.PrivateKeyPKCS8), CertificateDER: issued.Chain,
		}
		encoded, marshalErr := json.Marshal(rotated)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		credentialPath := filepath.Join(credentialRoot, "environment-credential.json")
		temporaryPath := credentialPath + ".rotation"
		if writeErr := os.WriteFile(temporaryPath, encoded, 0o600); writeErr != nil {
			t.Fatalf("write rotated canonical Environment credential: %v", writeErr)
		}
		if renameErr := os.Rename(temporaryPath, credentialPath); renameErr != nil {
			_ = os.Remove(temporaryPath)
			t.Fatalf("install rotated canonical Environment credential: %v", renameErr)
		}
		directory, openErr := os.Open(credentialRoot)
		if openErr != nil {
			t.Fatal(openErr)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			t.Fatalf("sync rotated credential directory: %v", errors.Join(syncErr, closeErr))
		}
		clear(rotated.PrivateKeyPKCS8)
	}
	return PrebindingProfile{
			Name: "primary", Origin: origin, DomainID: d115Receipt.DomainID, Epoch: 1, RepoID: d115Receipt.RepoID,
			AuthoritySPKIPin: serverPin, OwnerRootSPKI: ownerID, AuthorityCertificateDER: serverDER,
			ArtifactKeyCertificate: artifactCertificate, OwnerRootPublicKey: bytes.Clone(ownerPublic),
			EnvironmentCADElegation: caDelegation, CredentialDirectory: "attach-credentials",
		}, base64.RawURLEncoding.EncodeToString(grantBytes), rotateCanonicalCredential, func() {
			stop()
			select {
			case <-serveDone:
			case <-time.After(5 * time.Second):
				t.Error("M6 D115 authority server did not stop")
			}
			_ = listener.Close()
		}
}

func mustEncodeD115Receipt(t *testing.T, receipt d115.Receipt) []byte {
	t.Helper()
	encoded, err := d115.EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func expireD115ProvisioningConfig(t *testing.T, profileRoot string, certificate tls.Certificate) {
	t.Helper()
	config, err := wipdseed.LoadD115ProvisioningConfig(profileRoot)
	if err != nil {
		t.Fatalf("load D115 profile before expiry control: %v", err)
	}
	parsed, err := x509.ParseCertificate(config.AuthorityCertificateDER)
	if err != nil {
		t.Fatal(err)
	}
	expired := *parsed
	expired.NotBefore = time.Now().UTC().Add(-2 * time.Hour)
	expired.NotAfter = time.Now().UTC().Add(-time.Hour)
	for _, extension := range parsed.Extensions {
		if extension.Id.Equal([]int{2, 5, 29, 17}) {
			expired.ExtraExtensions = []pkix.Extension{{Id: extension.Id, Critical: extension.Critical, Value: bytes.Clone(extension.Value)}}
			break
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &expired, &expired, parsed.PublicKey, certificate.PrivateKey)
	if err != nil {
		t.Fatalf("create expired test D115 certificate: %v", err)
	}
	config.AuthorityCertificateDER = der
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profileRoot, "d115-provisioning-profile.json")
	temporary := path + ".expiry"
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		t.Fatalf("write expired D115 profile: %v", err)
	}
	if err = os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		t.Fatalf("install expired D115 profile: %v", err)
	}
	if _, err = wipdseed.LoadD115ProvisioningConfig(profileRoot); err != nil {
		t.Fatalf("offline D115 profile load rejected durable receipt recovery: %v", err)
	}
}

type d115StartupClientResult struct {
	Attempt wipd.D115Attempt `json:"attempt"`
	Receipt *d115.Receipt    `json:"receipt,omitempty"`
}

func runD115StartupClient(t *testing.T, peerCheckout, testRoot, profileRoot, action, grant, commandID string, proof []byte) d115StartupClientResult {
	t.Helper()
	resultPath := filepath.Join(testRoot, "client-result-"+action+".json")
	_ = os.Remove(resultPath)
	command := exec.Command(os.Args[0], "-test.run=^TestD115StartupUsesOwnerMediatedAuthenticatedLifecycle$")
	command.Dir = peerCheckout
	command.Env = withoutD115StartupHelperEnv(os.Environ())
	command.Env = append(command.Env,
		"WIPD_D115_TEST_CLIENT_ACTION="+action,
		"WIPD_D115_TEST_CLIENT_PROFILE="+profileRoot,
		"WIPD_D115_TEST_CLIENT_RESULT="+resultPath,
		"WIPD_D115_TEST_CLIENT_GRANT="+grant,
		"WIPD_D115_TEST_CLIENT_COMMAND="+commandID,
		"WIPD_D115_TEST_CLIENT_PROOF="+base64.RawURLEncoding.EncodeToString(proof),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated %s helper failed: %v\n%s", action, err, output)
	}
	resultBytes, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read authenticated %s helper result: %v\n%s", action, err, output)
	}
	var result d115StartupClientResult
	if err = json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatalf("decode authenticated %s helper result: %v", action, err)
	}
	return result
}

func runD115StartupClientHelper(t *testing.T, action string) {
	t.Helper()
	client, err := wipd.Connect(context.Background(), os.Getenv("WIPD_D115_TEST_CLIENT_PROFILE"))
	if err != nil {
		t.Fatalf("connect authenticated helper client: %v", err)
	}
	defer func() { _ = client.Close() }()
	var result d115StartupClientResult
	switch action {
	case "prepare":
		result.Attempt, err = client.D115Prepare(context.Background(), os.Getenv("WIPD_D115_TEST_CLIENT_GRANT"))
	case "import":
		proof, decodeErr := base64.RawURLEncoding.DecodeString(os.Getenv("WIPD_D115_TEST_CLIENT_PROOF"))
		if decodeErr != nil {
			t.Fatalf("decode helper proof: %v", decodeErr)
		}
		result.Attempt, err = client.D115ImportProof(context.Background(), os.Getenv("WIPD_D115_TEST_CLIENT_COMMAND"), proof)
	case "submit":
		var receipt d115.Receipt
		receipt, err = client.D115Submit(context.Background(), os.Getenv("WIPD_D115_TEST_CLIENT_COMMAND"))
		result.Receipt = &receipt
	default:
		t.Fatalf("unknown D115 helper action %q", action)
	}
	if err != nil {
		t.Fatalf("authenticated %s request failed: %v", action, err)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode authenticated %s helper result: %v", action, err)
	}
	if err = os.WriteFile(os.Getenv("WIPD_D115_TEST_CLIENT_RESULT"), resultBytes, 0o600); err != nil {
		t.Fatalf("write authenticated %s helper result: %v", action, err)
	}
}

type attachStartupClientResult struct {
	Attempt    wipd.AttachLocalAttempt      `json:"attempt"`
	Route      operation.RouteResolveOutput `json:"route"`
	RouteError string                       `json:"route_error,omitempty"`
	Error      string                       `json:"error,omitempty"`
}

func attachViaPublicIPCHelper(t *testing.T, profileRoot, grant string, timeout time.Duration) (attachStartupClientResult, error) {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "attach-client-result.json")
	command := exec.Command(os.Args[0], "-test.run=^TestD115StartupUsesOwnerMediatedAuthenticatedLifecycle$")
	command.Dir, _ = os.Getwd()
	command.Env = withoutD115StartupHelperEnv(os.Environ())
	command.Env = append(command.Env,
		"WIPD_ATTACH_TEST_CLIENT_ACTION=attach",
		"WIPD_ATTACH_TEST_CLIENT_PROFILE="+profileRoot,
		"WIPD_ATTACH_TEST_CLIENT_RESULT="+resultPath,
		"WIPD_ATTACH_TEST_CLIENT_GRANT="+grant,
		"WIPD_ATTACH_TEST_CLIENT_TIMEOUT="+timeout.String(),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("authenticated AttachOnce helper process failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read authenticated AttachOnce helper result: %v", err)
	}
	var result attachStartupClientResult
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode authenticated AttachOnce helper result: %v", err)
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}

func runAttachStartupClientHelper(t *testing.T) {
	t.Helper()
	client, err := wipd.ConnectAttachLifecycle(context.Background(), os.Getenv("WIPD_ATTACH_TEST_CLIENT_PROFILE"))
	if err != nil {
		t.Fatalf("connect authenticated attach helper: %v", err)
	}
	defer func() { _ = client.Close() }()
	timeout, err := time.ParseDuration(os.Getenv("WIPD_ATTACH_TEST_CLIENT_TIMEOUT"))
	if err != nil || timeout <= 0 {
		t.Fatalf("invalid AttachOnce helper timeout: %v", err)
	}
	routeCtx, cancelRoute := context.WithTimeout(context.Background(), 5*time.Second)
	route, routeErr := client.ResolveRoute(routeCtx)
	cancelRoute()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	attempt, attachErr := client.AttachOnce(ctx, os.Getenv("WIPD_ATTACH_TEST_CLIENT_GRANT"))
	result := attachStartupClientResult{Attempt: attempt, Route: route}
	if routeErr != nil {
		result.RouteError = routeErr.Error()
	}
	if attachErr != nil {
		result.Error = attachErr.Error()
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode authenticated AttachOnce result: %v", err)
	}
	if err = os.WriteFile(os.Getenv("WIPD_ATTACH_TEST_CLIENT_RESULT"), data, 0o600); err != nil {
		t.Fatalf("write authenticated AttachOnce result: %v", err)
	}
}

func waitForWipd(profileRoot string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		client, err := wipd.ConnectAttachLifecycle(context.Background(), profileRoot)
		if err == nil {
			return client.Close()
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("wipd did not become ready with Step 11 IPC")
}

func withoutD115StartupHelperEnv(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, "WIPD_D115_TEST_CLIENT_") {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

type runtimeD115OwnerSigner struct {
	keyID   string
	private ed25519.PrivateKey
}

func (signer *runtimeD115OwnerSigner) OwnerRootKeyID() string { return signer.keyID }

func (signer *runtimeD115OwnerSigner) SignD115OwnerProof(_ context.Context, _ []byte, _ d115.Claims, preimage []byte) ([]byte, error) {
	return ed25519.Sign(signer.private, preimage), nil
}

func runtimeD115TestCertificate(t *testing.T, realmID string) (tls.Certificate, []byte, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: 2, Tag: 7, Bytes: net.ParseIP("127.0.0.1").To4()},
		{Class: 2, Tag: 6, Bytes: []byte("wipd://provisioning-realm/" + realmID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "isolated D115 authority"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: []int{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), func() []byte {
		privateDER, marshalErr := x509.MarshalPKCS8PrivateKey(privateKey)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	}())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	return certificate, der, "sha256:" + hex.EncodeToString(digest[:])
}

func validRuntimeTestConfig(t *testing.T) Config {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "connected authority test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), private)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(spki)
	return Config{
		Schema: "wipd.connected-authority-profile/2", Origin: "https://authority.example:8443",
		DomainID: "01KZ7XHAQT1S46NYPN1PW1DX3A", Epoch: 1, RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3B",
		OwnerRootSPKI: "sha256:" + hex.EncodeToString(pin[:]), AuthoritySPKIPin: "sha256:" + hex.EncodeToString(pin[:]),
		AuthorityCertificateDER: certificateDER, OwnerRootPublicKey: private.Public().(ed25519.PublicKey),
		ArtifactKeyCertificate: []byte{1}, ClientStateDirectory: filepath.Join(t.TempDir(), "client-state"),
	}
}

func TestConnectedCommandCatalogueIsExplicitAndClosed(t *testing.T) {
	m5, err := registryForConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if hasCommand(m5, operation.MatterStartV1.Metadata().Operation) {
		t.Fatal("default M5 connected profile unexpectedly advertises M6 lifecycle commands")
	}
	m6, err := registryForConfig(Config{CommandCatalogue: "m6-step5"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasCommand(m6, operation.MatterStartV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted matter.start")
	}
	if !hasCommand(m6, operation.StepCancelV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted step.cancel")
	}
	if hasCommand(m6, operation.BatchSweepAnonymousV1.Metadata().Operation) {
		t.Fatal("M6 Step 5 profile unexpectedly enabled the Step 7 sweep")
	}
	if !hasCommand(m6, operation.ContentWriteOnceV1.Metadata().Operation) ||
		!hasCommand(m6, operation.FindingAppendV1.Metadata().Operation) {
		t.Fatal("explicit M6 connected profile omitted content.write-once or finding.append")
	}
	step7, err := registryForConfig(Config{CommandCatalogue: "m6-step7"})
	if err != nil || !hasCommand(step7, operation.BatchSweepAnonymousV1.Metadata().Operation) {
		t.Fatalf("explicit M6 Step 7 catalogue omitted batch sweep: registry=%v err=%v", step7, err)
	}
	if hasCommand(m5, operation.BatchSweepAnonymousV1.Metadata().Operation) {
		t.Fatal("default M5 connected profile unexpectedly enabled the Step 7 sweep")
	}
	for _, definition := range operation.GateCatalogue() {
		id := definition.Metadata().Operation
		if hasCommand(m5, id) || hasCommand(m6, id) || !hasCommand(step7, id) {
			t.Fatalf("ordinary gate %s is not isolated to explicit m6-step7", id)
		}
	}
	config := validRuntimeTestConfig(t)
	config.CommandCatalogue = "m6-step4"
	if err = validateConfig(config); err == nil {
		t.Fatal("accepted an unrecognized connected command catalogue")
	}
	config.CommandCatalogue = "m6-step7"
	if err = validateConfig(config); err != nil {
		t.Fatalf("rejected explicit Step 7 profile: %v", err)
	}
	step8, err := registryForConfig(Config{CommandCatalogue: "m6-step8"})
	if err != nil {
		t.Fatal(err)
	}
	config.CommandCatalogue = "m6-step8"
	if err = validateConfig(config); err != nil {
		t.Fatalf("rejected explicit Step 8 profile: %v", err)
	}
	if len(step8.Definitions()) != len(step7.Definitions())+5 {
		t.Fatal("Step 8 widened beyond its five operations")
	}
	for _, definition := range operation.Step8Catalogue() {
		id := definition.Metadata().Operation
		if hasCommand(m5, id) || hasCommand(m6, id) || hasCommand(step7, id) || !hasCommand(step8, id) {
			t.Fatalf("%s is not isolated to explicit m6-step8", id)
		}
	}
	step9a, err := registryForConfig(Config{CommandCatalogue: "m6-step9a"})
	if err != nil {
		t.Fatal(err)
	}
	step9b, err := registryForConfig(Config{CommandCatalogue: "m6-step9b"})
	if err != nil {
		t.Fatal(err)
	}
	config.CommandCatalogue = "m6-step9b"
	if err = validateConfig(config); err != nil {
		t.Fatalf("rejected explicit Step 9-B profile: %v", err)
	}
	if len(step9b.Definitions()) != len(step9a.Definitions())+len(operation.NamedBatchMembershipCatalogue()) {
		t.Fatal("Step 9-B widened beyond its named Batch membership and dismissal operations")
	}
	for _, definition := range operation.NamedBatchMembershipCatalogue() {
		id := definition.Metadata().Operation
		if hasCommand(m5, id) || hasCommand(m6, id) || hasCommand(step7, id) || hasCommand(step8, id) || hasCommand(step9a, id) || !hasCommand(step9b, id) {
			t.Fatalf("%s is not isolated to explicit m6-step9b", id)
		}
	}
	step10, err := registryForConfig(Config{CommandCatalogue: "m6-step10"})
	if err != nil {
		t.Fatal(err)
	}
	config.CommandCatalogue = "m6-step10"
	if err = validateConfig(config); err != nil {
		t.Fatalf("rejected explicit Step 10 profile: %v", err)
	}
	if len(step10.Definitions()) != len(step9b.Definitions())+len(operation.Step10Catalogue()) {
		t.Fatal("Step 10 widened beyond its isolated scheduler and lifecycle capabilities")
	}
	for _, definition := range operation.Step10Catalogue() {
		id := definition.Metadata().Operation
		if hasCommand(m5, id) || hasCommand(m6, id) || hasCommand(step7, id) || hasCommand(step8, id) ||
			hasCommand(step9a, id) || hasCommand(step9b, id) || !hasCommand(step10, id) {
			t.Fatalf("%s is not isolated to explicit m6-step10", id)
		}
	}
	step11, err := registryForConfig(Config{CommandCatalogue: "m6-step11"})
	if err != nil {
		t.Fatal(err)
	}
	config.CommandCatalogue = "m6-step11"
	if err = validateConfig(config); err != nil {
		t.Fatalf("rejected explicit Step 11 profile: %v", err)
	}
	if len(step11.Definitions()) != len(step10.Definitions())+len(operation.Step11Catalogue()) {
		t.Fatal("Step 11 registry does not add the complete isolated routing/adoption capability set")
	}
	for _, definition := range operation.Step11Catalogue() {
		id := definition.Metadata().Operation
		if hasCommand(step10, id) || !hasCommand(step11, id) {
			t.Fatalf("%s is not isolated to explicit m6-step11", id)
		}
	}
}

func TestPrebindingRouteProfileStartsWithoutRepoRuntimeOrJournal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated checkout discovery is Linux-only for this Step 11 slice")
	}
	profileRoot, err := os.MkdirTemp("/tmp", "wip-r-")
	if err != nil {
		t.Fatalf("create short prebinding fixture root under /tmp: %v", err)
	}
	if err = os.Chmod(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(profileRoot)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("prebinding fixture root mode = %v, %v; want owner-only", info, err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(profileRoot); err != nil {
			t.Errorf("remove short prebinding fixture root: %v", err)
		}
	})
	_, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerDER, err := x509.MarshalPKIXPublicKey(ownerPrivate.Public())
	if err != nil {
		t.Fatal(err)
	}
	ownerDigest := sha256.Sum256(ownerDER)
	_, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	authorityTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "route authority"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	authorityDER, err := x509.CreateCertificate(rand.Reader, authorityTemplate, authorityTemplate, authorityPrivate.Public(), authorityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	authoritySPKI, err := x509.MarshalPKIXPublicKey(authorityPrivate.Public())
	if err != nil {
		t.Fatal(err)
	}
	authorityDigest := sha256.Sum256(authoritySPKI)
	domainID := "01KZ7XHAQT1S46NYPN1PW1DX3A"
	config := PrebindingConfig{
		Schema:  "wipd.route-resolver-profile/1",
		Entries: []reporoute.Entry{{Key: "**", DomainID: domainID, ConnectionProfile: "primary"}},
		Profiles: []PrebindingProfile{{
			Name: "primary", Origin: "https://authority.example:8443", DomainID: domainID, Epoch: 1, RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3C",
			AuthoritySPKIPin:        "sha256:" + hex.EncodeToString(authorityDigest[:]),
			OwnerRootSPKI:           "sha256:" + hex.EncodeToString(ownerDigest[:]),
			AuthorityCertificateDER: authorityDER, OwnerRootPublicKey: ownerPrivate.Public().(ed25519.PublicKey),
			EnvironmentCADElegation: []byte("trusted delegation"), CredentialDirectory: "environment-primary",
		}},
	}
	if err = SavePrebindingConfig(profileRoot, config); err != nil {
		t.Fatalf("save D54 route profile: %v", err)
	}
	if err = SavePrebindingConfig(profileRoot, config); err != nil {
		t.Fatalf("exact route profile retry: %v", err)
	}
	conflicting := config
	conflicting.Entries = []reporoute.Entry{{Key: "github.com/acme/**", DomainID: domainID, ConnectionProfile: "primary"}}
	if err = SavePrebindingConfig(profileRoot, conflicting); err == nil {
		t.Fatal("conflicting D54 route profile replaced immutable provisioning")
	}
	server, runtime, err := NewServer(profileRoot)
	if err != nil || server == nil || runtime != nil {
		t.Fatalf("prebinding NewServer() = server:%v runtime:%v err:%v", server != nil, runtime != nil, err)
	}
	if err = server.ConfigureRouteResolve(wipd.RouteResolverFunc(func(context.Context, *wipd.PeerProcess) (operation.RouteResolveOutput, error) {
		return operation.RouteResolveOutput{}, nil
	})); err == nil {
		t.Fatal("prebinding startup did not install its configured route resolver")
	}
	daemon, err := wipd.Start(profileRoot)
	if err != nil {
		t.Fatalf("start local checkout-discovery daemon: %v", err)
	}
	serveCtx, stopServing := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serveCtx, daemon) }()
	t.Cleanup(func() {
		stopServing()
		select {
		case serveErr := <-serveDone:
			if serveErr != nil {
				t.Errorf("prebinding local daemon shutdown: %v", serveErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("prebinding local daemon did not stop")
		}
	})
	client, err := wipd.ConnectRouteResolve(context.Background(), profileRoot)
	if err != nil {
		t.Fatalf("connect using read-only prebinding capability: %v", err)
	}
	if _, err = client.ResolveRoute(context.Background()); err == nil {
		t.Fatal("selected D54 checkout resolved without its canonical Environment credential")
	} else {
		var exchangeErr *wipd.ExchangeError
		if !errors.As(err, &exchangeErr) || exchangeErr.Code != "query.route-unresolved" {
			t.Fatalf("selected D54 checkout failed outside the expected missing-credential boundary: %v", err)
		}
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	attachClient, err := wipd.ConnectAttachLifecycle(context.Background(), profileRoot)
	if err != nil {
		t.Fatalf("connect prebinding attach lifecycle without Repo-bound command execution: %v", err)
	}
	if _, err = attachClient.ResolveRoute(context.Background()); err == nil {
		t.Fatal("attach-capable route client resolved without its canonical Environment credential")
	} else {
		var exchangeErr *wipd.ExchangeError
		if !errors.As(err, &exchangeErr) || exchangeErr.Code != "query.route-unresolved" {
			t.Fatalf("attach-capable prebinding client failed outside missing-credential boundary: %v", err)
		}
	}
	if err = attachClient.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"client-state.json", "environment-journal", "environment-journal.lock"} {
		if _, statErr := os.Lstat(filepath.Join(profileRoot, path)); !os.IsNotExist(statErr) {
			t.Fatalf("prebinding startup created Repo-bound state %q: %v", path, statErr)
		}
	}
	info, err = os.Stat(filepath.Join(profileRoot, routeConfigName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("route profile mode = %v, %v; want 0600", info, err)
	}
}

func hasCommand(registry *operation.Registry, command operation.ID) bool {
	for _, definition := range registry.Definitions() {
		if definition.Metadata().Operation == command {
			return true
		}
	}
	return false
}
