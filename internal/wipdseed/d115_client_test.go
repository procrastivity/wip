package wipdseed

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/d115"
	"golang.org/x/net/http2"
)

func TestD115CommitLostResponseUnsentRetryRestartAndExactRecovery(t *testing.T) {
	storeRoot := filepath.Join(t.TempDir(), "authority")
	store, err := authoritystore.CreateEmpty(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC().Truncate(time.Second)
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
	grantBytes := testClientD115Grant(t, trust.RealmID, now, operatorID, operatorPrivate, ownerPublic)
	grantArtifact := base64.RawURLEncoding.EncodeToString(grantBytes)

	certificate, certificateDER, pin := testD115ProvisioningCertificate(t, trust.RealmID)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + listener.Addr().String()
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/wipd/v1/create-domain" || request.Header.Get("Content-Type") != "application/cbor" {
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			http.Error(writer, "read failure", http.StatusBadRequest)
			return
		}
		count := requestCount.Add(1)
		receipt, submitErr := store.SubmitCreateDomain(request.Context(), body)
		clear(body)
		if submitErr != nil {
			http.Error(writer, submitErr.Error(), http.StatusForbidden)
			return
		}
		if count == 1 {
			panic(http.ErrAbortHandler) // authority committed; client receives no response.
		}
		writer.Header().Set("Content-Type", "application/cbor")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(receipt)
	})
	httpServer := &http.Server{Handler: handler, TLSConfig: &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"},
	}, ErrorLog: log.New(io.Discard, "", 0)}
	if err = http2.ConfigureServer(httpServer, &http2.Server{}); err != nil {
		t.Fatal(err)
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- httpServer.Serve(tls.NewListener(listener, httpServer.TLSConfig)) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		select {
		case <-serverErr:
		case <-time.After(time.Second):
		}
	})

	testRoot := t.TempDir()
	if err = os.Chmod(testRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	profileParent := filepath.Join(testRoot, "private-parent")
	if err = os.Mkdir(profileParent, 0o700); err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(profileParent, "profile")
	if err = os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "isolated-test",
		Origin: origin, ProvisioningRealmID: trust.RealmID, AuthoritySPKIPin: pin, AuthorityCertificateDER: certificateDER,
	}
	if err = SaveD115ProvisioningConfig(profileRoot, config); err != nil {
		t.Fatalf("SaveD115ProvisioningConfig(): %v", err)
	}
	var signerCalls atomic.Int32
	signer := testD115OwnerSigner{keyID: ownerID, private: ownerPrivate, count: &signerCalls}
	binding := "sha256:" + strings.Repeat("a", 64)
	bundleBytes, err := store.ExportD115OperatorTrustBundle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareD115CreateLocal(profileRoot, grantArtifact, binding)
	if err != nil || prepared.State != D115PendingSignature {
		t.Fatalf("prepare=%+v err=%v", prepared, err)
	}
	preparedAgain, err := PrepareD115CreateLocal(profileRoot, grantArtifact, binding)
	if err != nil || preparedAgain.AttemptID != prepared.AttemptID || string(preparedAgain.SigningRequest) != string(prepared.SigningRequest) {
		t.Fatalf("repeated prepare changed intent: first=%+v again=%+v err=%v", prepared, preparedAgain, err)
	}
	if _, err = PrepareD115CreateLocal(profileRoot, grantArtifact, "sha256:"+strings.Repeat("b", 64)); !errors.Is(err, ErrStateExists) {
		t.Fatalf("cross-checkout same-grant prepare=%v, want refusal", err)
	}
	if restarted, reopenErr := D115CreateLocalStatus(profileRoot, prepared.AttemptID); reopenErr != nil || restarted.State != D115PendingSignature {
		t.Fatalf("pre-sign restart=%+v err=%v", restarted, reopenErr)
	}
	proofBytes, err := OwnerSignD115Request(context.Background(), prepared.SigningRequest, signer, bundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	proofValue, err := d115.DecodeOwnerProofArtifact(proofBytes)
	if err != nil {
		t.Fatal(err)
	}
	proofValue.OwnerProof[0] ^= 0xff
	invalidProof, err := d115.EncodeOwnerProofArtifact(proofValue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImportD115OwnerProof(profileRoot, prepared.AttemptID, invalidProof); err == nil {
		t.Fatal("mismatched owner proof accepted")
	}
	if state, statusErr := D115CreateLocalStatus(profileRoot, prepared.AttemptID); statusErr != nil || state.State != D115PendingSignature {
		t.Fatalf("invalid proof changed durable state: %+v err=%v", state, statusErr)
	}
	if _, err = OwnerSignD115Request(context.Background(), prepared.SigningRequest, nil, bundleBytes); err == nil || err.Error() != "signing.owner-provider-unavailable" {
		t.Fatalf("nil production provider error=%v", err)
	}
	imported, err := ImportD115OwnerProof(profileRoot, prepared.AttemptID, proofBytes)
	if err != nil || imported.State != D115Prepared {
		t.Fatalf("import=%+v err=%v", imported, err)
	}
	if _, err = SubmitPreparedD115CreateLocal(context.Background(), profileRoot, binding, prepared.AttemptID); !errors.Is(err, ErrD115OutcomeUnknown) {
		t.Fatalf("lost response = %v, want ErrD115OutcomeUnknown", err)
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ledger.loadAll()
	if err != nil || len(attempts) != 1 || attempts[0].Disposition != D115OutcomeUnknown || len(attempts[0].RequestBytes) == 0 || len(attempts[0].Exchanges) != 2 ||
		attempts[0].Exchanges[0].Disposition != string(D115PossiblySubmitted) || attempts[0].Exchanges[1].Disposition != string(D115OutcomeUnknown) {
		t.Fatalf("after lost response attempts=%+v err=%v", attempts, err)
	}
	commandID, requestHash := attempts[0].CommandID, attempts[0].RequestHash
	requestBytes := append([]byte(nil), attempts[0].RequestBytes...)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = SubmitPreparedD115CreateLocal(cancelled, profileRoot, binding, commandID); !errors.Is(err, ErrD115OutcomeUnknown) {
		t.Fatalf("cancelled exact retry = %v, want cumulative outcome-unknown", err)
	}
	if err = ledger.withFile(func(file *d115LedgerFile) error {
		if len(file.Attempts) != 1 || file.Attempts[0].Disposition != D115OutcomeUnknown || len(file.Attempts[0].Exchanges) != 3 ||
			file.Attempts[0].Exchanges[2].Disposition != string(D115DefinitelyUnsent) || file.Attempts[0].CommandID != commandID ||
			file.Attempts[0].RequestHash != requestHash || string(file.Attempts[0].RequestBytes) != string(requestBytes) {
			t.Fatalf("cancelled retry downgraded or changed command attempt: %+v", file.Attempts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reopen a fresh ledger handle as after a daemon restart, then recover with
	// the original canonical request bytes and no second signing operation.
	ledger = nil
	reopenedLedger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := reopenedLedger.loadAll()
	if err != nil || len(reopened) != 1 || reopened[0].Disposition != D115OutcomeUnknown || string(reopened[0].RequestBytes) != string(requestBytes) {
		t.Fatalf("restart ledger = %+v, %v", reopened, err)
	}
	receipts, err := RecoverD115StartupAttempts(context.Background(), profileRoot)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("RecoverD115StartupAttempts() = %d receipts, %v", len(receipts), err)
	}
	receipt := receipts[0]
	if receipt.CommandID != commandID || receipt.RequestHash != requestHash || receipt.RealmID != trust.RealmID || requestCount.Load() != 2 || signerCalls.Load() != 1 {
		t.Fatalf("recovered receipt=%+v requests=%d", receipt, requestCount.Load())
	}
	replayed, err := SubmitPreparedD115CreateLocal(context.Background(), profileRoot, binding, commandID)
	if err != nil || replayed.DomainID != receipt.DomainID || replayed.RepoID != receipt.RepoID ||
		replayed.CommandID != receipt.CommandID || replayed.RequestHash != receipt.RequestHash || requestCount.Load() != 2 {
		t.Fatalf("repeated submit did not join installed outcome: receipt=%+v replay=%+v err=%v requests=%d", receipt, replayed, err, requestCount.Load())
	}
	if err = reopenedLedger.withFile(func(file *d115LedgerFile) error {
		attempt := file.Attempts[0]
		if attempt.Disposition != D115Installed || len(attempt.RequestBytes) != 0 || len(attempt.Exchanges) != 5 ||
			attempt.Exchanges[0].Disposition != string(D115PossiblySubmitted) ||
			attempt.Exchanges[1].Disposition != string(D115OutcomeUnknown) ||
			attempt.Exchanges[2].Disposition != string(D115DefinitelyUnsent) ||
			attempt.Exchanges[3].Disposition != string(D115PossiblySubmitted) ||
			attempt.Exchanges[4].Disposition != string(D115ReceiptDurable) {
			t.Fatalf("final attempt disposition/order = %+v", attempt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	domain, err := store.LookupDomain(context.Background(), receipt.DomainID)
	if err != nil || domain.OwnerKeyID != ownerID || domain.ActiveEpoch != 1 {
		t.Fatalf("recovered domain = %+v, %v", domain, err)
	}
	if boundDomain, bindErr := store.RepoDomain(context.Background(), receipt.RepoID); bindErr != nil || boundDomain != receipt.DomainID {
		t.Fatalf("recovered Repo binding domain=%q err=%v", boundDomain, bindErr)
	}
	installed, err := os.ReadFile(filepath.Join(profileRoot, "d115-attempts", "receipts", commandID+".cbor"))
	if err != nil {
		t.Fatal(err)
	}
	installedReceipt, err := d115.DecodeReceipt(installed)
	if err != nil || installedReceipt.DomainID != receipt.DomainID || installedReceipt.RepoID != receipt.RepoID {
		t.Fatalf("installed receipt = %+v, %v", installedReceipt, err)
	}
	attachReceipt, err := LoadInstalledD115ReceiptForAttach(profileRoot, commandID, binding)
	if err != nil || attachReceipt.AttemptID != commandID || attachReceipt.CheckoutBinding != binding ||
		attachReceipt.AuthoritySPKI != pin || !bytes.Equal(attachReceipt.Bytes, installed) ||
		attachReceipt.Receipt.DomainID != receipt.DomainID || attachReceipt.Receipt.RepoID != receipt.RepoID {
		t.Fatalf("D115 attach receipt evidence = %+v, %v", attachReceipt, err)
	}
	if _, err = LoadInstalledD115ReceiptForAttach(profileRoot, commandID, "sha256:"+strings.Repeat("c", 64)); !errors.Is(err, ErrD115LocalRefused) {
		t.Fatalf("receipt was available to a different checkout binding: %v", err)
	}
	attachReceipts, err := ListInstalledD115ReceiptsForAttach(profileRoot, binding)
	if err != nil || len(attachReceipts) != 1 || attachReceipts[0].AttemptID != commandID {
		t.Fatalf("installed D115 attach receipt candidates = %+v, %v", attachReceipts, err)
	}

	// A distinct grant in the same profile gets an independent receipt slot;
	// status and repeated submit must continue to resolve both durable results.
	secondNow := time.Now().UTC().Truncate(time.Second)
	secondGrantID, err := d115.NewULID(secondNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	secondGrant := base64.RawURLEncoding.EncodeToString(testClientD115GrantWithID(t, trust.RealmID, secondNow, secondGrantID, operatorID, operatorPrivate, ownerPublic))
	secondPrepared, err := PrepareD115CreateLocal(profileRoot, secondGrant, binding)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest, err := d115.DecodeOwnerSigningRequest(secondPrepared.SigningRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondProof, err := d115.EncodeOwnerProofArtifact(d115.OwnerProofArtifact{
		Schema: d115.OwnerProofSchema, RealmID: secondRequest.RealmID, CommandID: secondRequest.CommandID,
		RequestHash: secondRequest.RequestHash, GrantDigest: secondRequest.GrantDigest,
		OwnerKeyID: secondRequest.OwnerKeyID, OwnerProof: ed25519.Sign(ownerPrivate, secondRequest.Preimage),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImportD115OwnerProof(profileRoot, secondPrepared.AttemptID, secondProof); err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := SubmitPreparedD115CreateLocal(context.Background(), profileRoot, binding, secondPrepared.AttemptID)
	if err != nil || secondReceipt.DomainID == receipt.DomainID || secondReceipt.RepoID == receipt.RepoID || requestCount.Load() != 3 {
		t.Fatalf("second grant receipt=%+v err=%v requests=%d", secondReceipt, err, requestCount.Load())
	}
	secondStatus, err := D115CreateLocalStatus(profileRoot, secondPrepared.AttemptID)
	if err != nil || secondStatus.State != D115Installed || len(secondStatus.Receipt) == 0 {
		t.Fatalf("second receipt status=%+v err=%v", secondStatus, err)
	}
	secondReplay, err := SubmitPreparedD115CreateLocal(context.Background(), profileRoot, binding, secondPrepared.AttemptID)
	if err != nil || secondReplay.DomainID != secondReceipt.DomainID || secondReplay.RepoID != secondReceipt.RepoID || requestCount.Load() != 3 {
		t.Fatalf("second repeated submit=%+v err=%v requests=%d", secondReplay, err, requestCount.Load())
	}
	secondInstalled, err := os.ReadFile(filepath.Join(profileRoot, "d115-attempts", "receipts", secondPrepared.AttemptID+".cbor"))
	if err != nil || !bytes.Equal(secondInstalled, secondStatus.Receipt) {
		t.Fatalf("second durable receipt file mismatch: err=%v", err)
	}
}

func TestExpiredD115ProfileRemainsLoadableOfflineButNotForLiveUse(t *testing.T) {
	const realmID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	certificate, certificateDER, pin := testD115ProvisioningCertificate(t, realmID)
	parsed, err := x509.ParseCertificate(certificateDER)
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
	expiredDER, err := x509.CreateCertificate(rand.Reader, &expired, &expired, parsed.PublicKey, certificate.PrivateKey)
	if err != nil {
		t.Fatalf("create expired D115 fixture certificate: %v", err)
	}
	config := D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "expired-offline-test",
		Origin: "https://127.0.0.1:443", ProvisioningRealmID: realmID,
		AuthoritySPKIPin: pin, AuthorityCertificateDER: expiredDER,
	}
	root := t.TempDir()
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(root, "profile")
	if err = os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(profileRoot, d115ProfileName), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, loadErr := LoadD115ProvisioningConfig(profileRoot); loadErr != nil || !bytes.Equal(loaded.AuthorityCertificateDER, expiredDER) {
		t.Fatalf("offline load of pinned expired D115 profile = %+v, %v", loaded, loadErr)
	}
	if err = validateD115ProvisioningConfigAt(config, time.Time{}); err != nil {
		t.Fatalf("offline structural validation rejected expired profile: %v", err)
	}
	if err = validateD115ProvisioningConfigAt(config, time.Now().UTC()); err == nil {
		t.Fatal("live D115 profile validation accepted an expired authority certificate")
	}
}

func TestD115ProvisioningProfileAndLedgerRequirePrivateNonSymlinkFiles(t *testing.T) {
	realmID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	_, certificateDER, pin := testD115ProvisioningCertificate(t, realmID)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(root, "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	config := D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "private-test",
		Origin: "https://127.0.0.1:443", ProvisioningRealmID: realmID, AuthoritySPKIPin: pin, AuthorityCertificateDER: certificateDER,
	}
	if err := SaveD115ProvisioningConfig(profileRoot, config); err != nil {
		t.Fatal(err)
	}
	profileInfo, err := os.Stat(filepath.Join(profileRoot, d115ProfileName))
	if err != nil || profileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("provisioning profile mode=%v err=%v; want 0600", profileInfo, err)
	}
	if _, err = LoadD115ProvisioningConfig(profileRoot); err != nil {
		t.Fatalf("load private provisioning profile: %v", err)
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	ledgerInfo, err := os.Stat(filepath.Join(ledger.directory, d115LedgerName))
	if err != nil || ledgerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("attempt ledger mode=%v err=%v; want 0600", ledgerInfo, err)
	}
	lockInfo, err := os.Stat(filepath.Join(ledger.directory, d115LockName))
	if err != nil || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("attempt lock mode=%v err=%v; want 0600", lockInfo, err)
	}
	if err = os.Chmod(filepath.Join(ledger.directory, d115LedgerName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.loadAll(); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("world-readable ledger accepted: %v", err)
	}

	configPath := filepath.Join(profileRoot, d115ProfileName)
	if err = os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "config-target")
	if err = os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, configPath); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadD115ProvisioningConfig(profileRoot); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("symlinked provisioning profile accepted: %v", err)
	}

	ledgerRoot := filepath.Join(root, "symlink-ledger")
	if err = os.Mkdir(ledgerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerDirectory := filepath.Join(ledgerRoot, "d115-attempts")
	targetDirectory := filepath.Join(root, "ledger-target")
	if err = os.Mkdir(targetDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(targetDirectory, ledgerDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err = openD115Ledger(ledgerRoot); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("symlinked ledger directory accepted: %v", err)
	}

	ledgerRoot = filepath.Join(root, "symlink-files")
	if err = os.Mkdir(ledgerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerDirectory = filepath.Join(ledgerRoot, "d115-attempts")
	if err = os.Mkdir(ledgerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(ledgerDirectory, d115LockName)); err != nil {
		t.Fatal(err)
	}
	if _, err = openD115Ledger(ledgerRoot); err == nil {
		t.Fatal("symlinked attempt lock accepted")
	}
}

func TestD115LedgerDirectoryLinkIsSyncedAndFailureIsRetried(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected parent directory fsync failure")
	var syncedPath string
	if _, err := openD115LedgerWithDirectorySync(root, func(path string) error {
		syncedPath = path
		return syncFailure
	}); !errors.Is(err, syncFailure) || syncedPath != root {
		t.Fatalf("ledger creation sync path=%q error=%v; want parent sync failure", syncedPath, err)
	}
	ledger, err := openD115Ledger(root)
	if err != nil {
		t.Fatalf("retry after parent sync failure: %v", err)
	}
	if _, err = os.Stat(ledger.directory); err != nil {
		t.Fatalf("durable ledger directory missing after retry: %v", err)
	}
}

func TestD115LedgerReopensAfterCrashOrphanedTemp(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger, err := openD115Ledger(root)
	if err != nil {
		t.Fatal(err)
	}
	attempt := D115Attempt{
		RealmID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", GrantDigest: "sha256:" + strings.Repeat("a", 64),
		CheckoutBinding: "sha256:" + strings.Repeat("b", 64), CommandID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		RequestHash: "sha256:" + strings.Repeat("c", 64), OwnerKeyID: "sha256:" + strings.Repeat("d", 64),
		GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Disposition: D115PendingSignature,
		SigningRequest: []byte("committed signing request"), Exchanges: []D115Exchange{},
	}
	if _, err = ledger.getOrCreate(attempt); err != nil {
		t.Fatal(err)
	}
	committed, err := ledger.findByCommandID(attempt.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	tempPath := filepath.Join(ledger.directory, ".attempt-ledger.tmp")
	if err = os.WriteFile(tempPath, []byte(`{"truncated":`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := openD115Ledger(root)
	if err != nil {
		t.Fatalf("reopen with crash-orphaned temp: %v", err)
	}
	if _, err = os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned temp remains after safe cleanup: %v", err)
	}
	got, err := reopened.findByCommandID(attempt.CommandID)
	if err != nil || got.Disposition != committed.Disposition || !bytes.Equal(got.SigningRequest, committed.SigningRequest) ||
		got.CommandID != committed.CommandID || got.RequestHash != committed.RequestHash {
		t.Fatalf("committed state changed during temp recovery: before=%+v after=%+v err=%v", committed, got, err)
	}
}

func TestD115PrepareConcurrencyCancellationAndExpiryWhilePending(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(root, "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
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
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedOrigin := "https://" + closedListener.Addr().String()
	if err = closedListener.Close(); err != nil {
		t.Fatal(err)
	}
	realm, err := d115.NewULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, certificateDER, pin := testD115ProvisioningCertificate(t, realm)
	if err = SaveD115ProvisioningConfig(profileRoot, D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "lifecycle-test", Origin: closedOrigin,
		ProvisioningRealmID: realm, AuthoritySPKIPin: pin, AuthorityCertificateDER: certificateDER,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	binding := "sha256:" + strings.Repeat("c", 64)
	grant := base64.RawURLEncoding.EncodeToString(testClientD115Grant(t, realm, now, operatorID, operatorPrivate, ownerPublic))
	const callers = 12
	results := make(chan D115LocalState, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			state, prepareErr := PrepareD115CreateLocal(profileRoot, grant, binding)
			if prepareErr != nil {
				errorsFound <- prepareErr
				return
			}
			results <- state
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for prepareErr := range errorsFound {
		t.Fatalf("concurrent prepare: %v", prepareErr)
	}
	var first D115LocalState
	for state := range results {
		if first.AttemptID == "" {
			first = state
			continue
		}
		if state.AttemptID != first.AttemptID || !bytes.Equal(state.SigningRequest, first.SigningRequest) || state.State != D115PendingSignature {
			t.Fatalf("concurrent prepare did not converge: first=%+v next=%+v", first, state)
		}
	}
	cancelled, err := CancelD115CreateLocal(profileRoot, first.AttemptID)
	if err != nil || cancelled.State != D115Cancelled {
		t.Fatalf("cancel pending=%+v err=%v", cancelled, err)
	}
	if repeated, repeatErr := PrepareD115CreateLocal(profileRoot, grant, binding); repeatErr != nil || repeated.AttemptID != first.AttemptID || repeated.State != D115Cancelled {
		t.Fatalf("prepare resurrected cancelled intent: %+v err=%v", repeated, repeatErr)
	}

	// Race a prepared submit against cancellation. If cancellation wins, the
	// serialized submit must observe cancelled state and never append a send
	// marker; if submit wins, cancellation must refuse the possible send.
	raceGrant := base64.RawURLEncoding.EncodeToString(testClientD115Grant(t, realm, time.Now().UTC().Truncate(time.Second), operatorID, operatorPrivate, ownerPublic))
	raceAttempt, err := PrepareD115CreateLocal(profileRoot, raceGrant, binding)
	if err != nil {
		t.Fatal(err)
	}
	raceRequest, err := d115.DecodeOwnerSigningRequest(raceAttempt.SigningRequest)
	if err != nil {
		t.Fatal(err)
	}
	raceProof, err := d115.EncodeOwnerProofArtifact(d115.OwnerProofArtifact{
		Schema: d115.OwnerProofSchema, RealmID: raceRequest.RealmID, CommandID: raceRequest.CommandID,
		RequestHash: raceRequest.RequestHash, GrantDigest: raceRequest.GrantDigest,
		OwnerKeyID: raceRequest.OwnerKeyID, OwnerProof: ed25519.Sign(ownerPrivate, raceRequest.Preimage),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImportD115OwnerProof(profileRoot, raceAttempt.AttemptID, raceProof); err != nil {
		t.Fatal(err)
	}
	startRace := make(chan struct{})
	type raceResult struct{ err error }
	cancelResult, submitResult := make(chan raceResult, 1), make(chan raceResult, 1)
	go func() {
		<-startRace
		_, raceErr := CancelD115CreateLocal(profileRoot, raceAttempt.AttemptID)
		cancelResult <- raceResult{err: raceErr}
	}()
	go func() {
		<-startRace
		_, raceErr := SubmitPreparedD115CreateLocal(context.Background(), profileRoot, binding, raceAttempt.AttemptID)
		submitResult <- raceResult{err: raceErr}
	}()
	close(startRace)
	cancelledResult, submittedResult := <-cancelResult, <-submitResult
	raceState, err := D115CreateLocalStatus(profileRoot, raceAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	raceLedger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	raceStored, err := raceLedger.findByCommandID(raceAttempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	possibleSend := hasPossiblySubmitted(raceStored.Exchanges)
	if cancelledResult.err == nil {
		if possibleSend || raceState.State != D115Cancelled || submittedResult.err == nil {
			t.Fatalf("successful cancel raced a send: state=%+v possibleSend=%t submitErr=%v", raceState, possibleSend, submittedResult.err)
		}
	} else if !possibleSend || raceState.State == D115Cancelled || submittedResult.err == nil {
		t.Fatalf("submit/cancel race had no safe winner: state=%+v possibleSend=%t cancelErr=%v submitErr=%v", raceState, possibleSend, cancelledResult.err, submittedResult.err)
	}

	// Expiry during owner action is a local refusal and cannot persist proof or
	// submit bytes, even when the detached owner proof itself is valid.
	issued := time.Now().UTC().Truncate(time.Second)
	expires := time.Now().UTC().Add(500 * time.Millisecond)
	expiring := base64.RawURLEncoding.EncodeToString(testClientD115GrantExpiresAt(t, realm, issued, expires, operatorID, operatorPrivate, ownerPublic))
	pending, err := PrepareD115CreateLocal(profileRoot, expiring, binding)
	if err != nil || pending.State != D115PendingSignature {
		t.Fatalf("prepare short-lived grant=%+v err=%v", pending, err)
	}
	if delay := time.Until(expires); delay > 0 {
		time.Sleep(delay + 20*time.Millisecond)
	}
	pinned, err := d115.DecodeOwnerSigningRequest(pending.SigningRequest)
	if err != nil {
		t.Fatal(err)
	}
	proofBytes, err := d115.EncodeOwnerProofArtifact(d115.OwnerProofArtifact{
		Schema: d115.OwnerProofSchema, RealmID: pinned.RealmID, CommandID: pinned.CommandID,
		RequestHash: pinned.RequestHash, GrantDigest: pinned.GrantDigest, OwnerKeyID: ownerID,
		OwnerProof: ed25519.Sign(ownerPrivate, pinned.Preimage),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ImportD115OwnerProof(profileRoot, pending.AttemptID, proofBytes); !errors.Is(err, ErrD115LocalRefused) {
		t.Fatalf("expired pending proof import=%v; want local refusal", err)
	}
	if expired, statusErr := D115CreateLocalStatus(profileRoot, pending.AttemptID); statusErr != nil || expired.State != D115Refused {
		t.Fatalf("expired attempt state=%+v err=%v", expired, statusErr)
	}
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.withFile(func(file *d115LedgerFile) error {
		for _, attempt := range file.Attempts {
			if attempt.CommandID == pending.AttemptID && (len(attempt.ProofArtifact) != 0 || len(attempt.RequestBytes) != 0) {
				t.Fatal("expired pending attempt retained proof or submit bytes")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestD115RestartPromotesUnresolvedRecoverySendMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger, err := openD115Ledger(root)
	if err != nil {
		t.Fatal(err)
	}
	attempt := D115Attempt{
		RealmID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", GrantDigest: "sha256:" + strings.Repeat("a", 64),
		CheckoutBinding: "sha256:" + strings.Repeat("b", 64), CommandID: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
		RequestHash: "sha256:" + strings.Repeat("c", 64), OwnerKeyID: "sha256:" + strings.Repeat("d", 64),
		GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", RequestBytes: []byte("pinned request"),
		Disposition: D115Prepared, Exchanges: []D115Exchange{},
	}
	if _, err = ledger.getOrCreate(attempt); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.update(attempt.RealmID, attempt.GrantDigest, func(current *D115Attempt) error {
		appendD115Exchange(current, D115PossiblySubmitted, "send-marker")
		current.Disposition = D115PossiblySubmitted
		appendD115Exchange(current, D115OutcomeUnknown, "transport.outcome-unknown")
		current.Disposition = D115OutcomeUnknown
		appendD115Exchange(current, D115PossiblySubmitted, "recovery-send-marker")
		current.Disposition = D115OutcomeUnknown
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := openD115Ledger(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := restarted.findByCommandID(attempt.CommandID)
	if err != nil || state.Disposition != D115OutcomeUnknown || len(state.Exchanges) != 3 ||
		state.Exchanges[0].Disposition != string(D115PossiblySubmitted) ||
		state.Exchanges[1].Disposition != string(D115OutcomeUnknown) ||
		state.Exchanges[2].Disposition != string(D115PossiblySubmitted) {
		t.Fatalf("restart state=%+v err=%v; want both send markers intact and cumulative outcome-unknown", state, err)
	}
}

func TestD115StartupRecoveryContinuesAfterAttemptFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	profileRoot := filepath.Join(root, "profile")
	if err := os.Mkdir(profileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	realmID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	_, certificateDER, pin := testD115ProvisioningCertificate(t, realmID)
	if err := SaveD115ProvisioningConfig(profileRoot, D115ProvisioningConfig{
		Schema: "wipd.d115-provisioning-profile/1", Name: "recovery-test", Origin: "https://127.0.0.1:1",
		ProvisioningRealmID: realmID, AuthoritySPKIPin: pin, AuthorityCertificateDER: certificateDER,
	}); err != nil {
		t.Fatal(err)
	}
	ownerPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerID, _ := d115.KeyID(ownerPublic)
	ledger, err := openD115Ledger(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	commandOne := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	commandTwo := "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	validReceipt, err := d115.EncodeReceipt(d115.Receipt{
		Schema: d115.ReceiptSchema, RealmID: realmID, CommandID: commandTwo,
		RequestHash: "sha256:" + strings.Repeat("b", 64), Operation: d115.Operation{Name: "repo.create-local", Version: 1},
		ResultCode: "result.succeeded", DomainID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", RepoID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ",
		InitialEpoch: 1, OwnerKeyID: ownerID, GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FB0", CommittedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []D115Attempt{
		{RealmID: realmID, GrantDigest: "sha256:" + strings.Repeat("0", 64), CheckoutBinding: "sha256:" + strings.Repeat("c", 64), CommandID: commandOne, Disposition: D115ReceiptDurable, ReceiptBytes: []byte("invalid receipt"), Exchanges: []D115Exchange{}},
		{RealmID: realmID, GrantDigest: "sha256:" + strings.Repeat("1", 64), CheckoutBinding: "sha256:" + strings.Repeat("c", 64), CommandID: commandTwo, RequestHash: "sha256:" + strings.Repeat("b", 64), OwnerKeyID: ownerID, GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FB0", Disposition: D115ReceiptDurable, ReceiptBytes: validReceipt, Exchanges: []D115Exchange{}},
	} {
		if _, err = ledger.getOrCreate(attempt); err != nil {
			t.Fatal(err)
		}
	}
	receipts, err := RecoverD115StartupAttempts(context.Background(), profileRoot)
	if !errors.Is(err, ErrInvalidClientState) || len(receipts) != 1 || receipts[0].CommandID != commandTwo {
		t.Fatalf("startup recovery receipts=%+v err=%v; want later receipt plus first-attempt error", receipts, err)
	}
	state, err := D115CreateLocalStatus(profileRoot, commandTwo)
	if err != nil || state.State != D115Installed || !bytes.Equal(state.Receipt, validReceipt) {
		t.Fatalf("later attempt was not installed: state=%+v err=%v", state, err)
	}
}

func TestD115RefusalResponsePreservesCumulativeUnknown(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger, err := openD115Ledger(root)
	if err != nil {
		t.Fatal(err)
	}
	base := D115Attempt{
		RealmID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", CheckoutBinding: "sha256:" + strings.Repeat("c", 64),
		OwnerKeyID: "sha256:" + strings.Repeat("d", 64), GrantID: "01ARZ3NDEKTSV4RRFFQ69G5FAX",
		Disposition: D115PossiblySubmitted, Exchanges: []D115Exchange{{Number: 1, Disposition: string(D115PossiblySubmitted), ResultCode: "send-marker"}},
	}
	first := base
	first.GrantDigest = "sha256:" + strings.Repeat("0", 64)
	first.CommandID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	first.RequestHash = "sha256:" + strings.Repeat("e", 64)
	if _, err = ledger.getOrCreate(first); err != nil {
		t.Fatal(err)
	}
	if err = recordD115Response(ledger, first, "auth.create-domain-refused"); !errors.Is(err, ErrD115OutcomeUnknown) {
		t.Fatalf("first-send refusal returned %v; want cumulative outcome-unknown", err)
	}
	second := base
	second.GrantDigest = "sha256:" + strings.Repeat("1", 64)
	second.CommandID = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	second.RequestHash = "sha256:" + strings.Repeat("f", 64)
	second.Disposition = D115OutcomeUnknown
	second.Exchanges = append(append([]D115Exchange(nil), base.Exchanges...), D115Exchange{Number: 2, Disposition: string(D115OutcomeUnknown), ResultCode: "transport.outcome-unknown"})
	if _, err = ledger.getOrCreate(second); err != nil {
		t.Fatal(err)
	}
	if err = recordD115Response(ledger, second, "auth.create-domain-refused"); !errors.Is(err, ErrD115OutcomeUnknown) {
		t.Fatalf("refusal after earlier unknown returned %v; want cumulative outcome-unknown", err)
	}
	restarted, err := openD115Ledger(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := restarted.findByCommandID(second.CommandID)
	if err != nil || state.Disposition != D115OutcomeUnknown || len(state.Exchanges) != 3 || state.Exchanges[2].Disposition != "response" {
		t.Fatalf("refusal restart state=%+v err=%v; want unresolved command with retained response evidence", state, err)
	}
}

type testD115OwnerSigner struct {
	keyID   string
	private ed25519.PrivateKey
	count   *atomic.Int32
}

func (signer testD115OwnerSigner) OwnerRootKeyID() string { return signer.keyID }

func (signer testD115OwnerSigner) SignD115OwnerProof(_ context.Context, grant []byte, claims d115.Claims, preimage []byte) ([]byte, error) {
	if _, decodedClaims, err := d115.DecodeGrant(grant); err != nil || decodedClaims.GrantID != claims.GrantID || claims.OwnerKeyID != signer.keyID {
		return nil, errors.New("test signer rejected grant")
	}
	signer.count.Add(1)
	return ed25519.Sign(signer.private, preimage), nil
}

func testClientD115Grant(t *testing.T, realm string, now time.Time, operatorID string, operatorPrivate ed25519.PrivateKey, ownerPublic ed25519.PublicKey) []byte {
	t.Helper()
	return testClientD115GrantExpiresAt(t, realm, now, now.Add(9*time.Minute), operatorID, operatorPrivate, ownerPublic)
}

func testClientD115GrantExpiresAt(t *testing.T, realm string, now, expires time.Time, operatorID string, operatorPrivate ed25519.PrivateKey, ownerPublic ed25519.PublicKey) []byte {
	t.Helper()
	grantID, err := d115.NewULID(now)
	if err != nil {
		t.Fatal(err)
	}
	return testClientD115GrantWithIDAndExpiry(t, realm, now, expires, grantID, operatorID, operatorPrivate, ownerPublic)
}

func testClientD115GrantWithID(t *testing.T, realm string, now time.Time, grantID, operatorID string, operatorPrivate ed25519.PrivateKey, ownerPublic ed25519.PublicKey) []byte {
	t.Helper()
	return testClientD115GrantWithIDAndExpiry(t, realm, now, now.Add(9*time.Minute), grantID, operatorID, operatorPrivate, ownerPublic)
}

func testClientD115GrantWithIDAndExpiry(t *testing.T, realm string, now, expires time.Time, grantID, operatorID string, operatorPrivate ed25519.PrivateKey, ownerPublic ed25519.PublicKey) []byte {
	t.Helper()
	ownerID, err := d115.KeyID(ownerPublic)
	if err != nil {
		t.Fatal(err)
	}
	claimsBytes, err := d115.EncodeClaims(d115.Claims{
		Schema: d115.ClaimsSchema, GrantID: grantID, Audience: realm,
		Scope: d115.CreateDomainScope, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID,
		IssuedAt: now.Add(-time.Second).Format(time.RFC3339Nano), NotBefore: now.Add(-time.Second).Format(time.RFC3339Nano),
		ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
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
	encoded, err := d115.EncodeGrant(grant)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testD115ProvisioningCertificate(t *testing.T, realm string) (tls.Certificate, []byte, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri := []byte("wipd://provisioning-realm/" + realm)
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: 2, Tag: 7, Bytes: net.ParseIP("127.0.0.1").To4()},
		{Class: 2, Tag: 6, Bytes: uri},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "D115 test authority"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: []int{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(pemCertificate(der), pemPrivateKey(privateKey))
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

func pemCertificate(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pemPrivateKey(key ed25519.PrivateKey) []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
