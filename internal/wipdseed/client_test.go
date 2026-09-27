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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	testDomainID = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	testRepoID   = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	testGrantID  = "01KZ7XHAQT1S46NYPN1PW1DX3D"
)

var testEncoder, _ = cbor.CoreDetEncOptions().EncMode()

func TestEnrollAndSeedInstallsOnlyVerifiedEmptyShadow(t *testing.T) {
	fixture := newClientFixture(t)
	identity := fixture.identity
	var err error
	directory := t.TempDir()
	if err = SavePending(directory, identity); err != nil {
		t.Fatal(err)
	}
	grant := fixture.grant
	state, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.Schema != "wipd.m5-client-state/1" || state.RepoID != testRepoID || state.DomainID != testDomainID || state.Epoch != 1 ||
		state.EnvironmentID == "" || state.OwnerKeyID != fixture.ownerKeyID || state.SPKIDigest == "" || len(state.CertificateDER) != 2 ||
		state.Prefix.EventCount != 0 || state.Prefix.EventID != nil || state.Prefix.Digest != emptyPrefixDigest() ||
		state.ManifestDigest != emptyManifestDigest() || state.Projections == nil || len(state.Projections) != 0 {
		t.Fatalf("installed shadow does not match verified empty seed: %+v", state)
	}
	if _, err = os.Stat(filepath.Join(directory, pendingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending identity remains after successful install: %v", err)
	}
	path := filepath.Join(directory, stateName)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("installed state mode = %v, %v; want 0600", info, err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted ClientState
	if err = json.Unmarshal(encoded, &persisted); err != nil || persisted.EnvironmentID != state.EnvironmentID || !bytes.Equal(persisted.PrivateKeyPKCS8, identity.PrivateKeyPKCS8) {
		t.Fatalf("persisted state differs from installed verified identity: %+v, %v", persisted, err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, directory); !errors.Is(err, ErrStateExists) {
		t.Fatalf("second install = %v, want create-only refusal", err)
	}
	recoveryDirectory := t.TempDir()
	if err = SavePending(recoveryDirectory, identity); err != nil {
		t.Fatal(err)
	}
	recovered, err := EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, grant, recoveryDirectory)
	if err != nil || recovered.EnvironmentID != state.EnvironmentID || !bytes.Equal(recovered.CertificateDER[0], state.CertificateDER[0]) {
		t.Fatalf("same-grant response-loss recovery = %+v, %v; want the original issued identity/certificate", recovered, err)
	}
	if calls := fixture.signerCalls.Load(); calls != 1 {
		t.Fatalf("external CA signer was called %d times after an exact retry, want once", calls)
	}

	wrongKey, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, wrongKey, grant, t.TempDir()); err == nil {
		t.Fatal("reusing a consumed grant for a different CSR was accepted")
	}
	wrongRepoConfig := wipdauthority.M5LabConfig{
		Store: fixture.store, RepoID: "01KZ7XHAQT1S46NYPN1PW1DX3E", EnrollmentGrant: fixture.grant,
		ExpectedCSRDER: fixture.identity.CSRDER, EnvironmentCACertificateDER: fixture.caDER,
		SignEnvironmentLeaf: func(context.Context, string, uint64, string, []byte, time.Time) ([]byte, error) {
			return nil, errors.New("unexpected signer call")
		},
	}
	if _, err = wipdauthority.NewM5LabServer(fixture.profile, tlsCertificate(t, fixture.serverCertDER, fixture.serverPrivate), wrongRepoConfig); !errors.Is(err, wipdauthority.ErrRepoMembershipMismatch) {
		t.Fatalf("wrong Repo server binding = %v, want membership refusal", err)
	}
}

func TestEnrollAndSeedWrongDomainOrGrantLeavesNoClientState(t *testing.T) {
	fixture := newClientFixture(t)
	identity := fixture.identity
	var err error
	wrongDomain, err := wipdauthority.NewProfile(fixture.profile.Origin(), "01KZ7XHAQT1S46NYPN1PW1DX3E", 1, fixture.authorityPin, fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), wrongDomain, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, identity, fixture.grant, directory); err == nil {
		t.Fatal("wrong authority domain was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-domain attempt installed state: %v", err)
	}
	wrongOwner, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directory = t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, wrongOwner, fixture.delegation, testRepoID, identity, fixture.grant, directory); err == nil {
		t.Fatal("delegation signed by a different owner root was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-owner attempt installed state: %v", err)
	}

	wrongKey, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	badGrant := fixture.enrollmentGrant(identity.CSRDER)
	directory = t.TempDir()
	if _, err = EnrollAndSeed(context.Background(), fixture.profile, fixture.roots, fixture.ownerRoot, fixture.delegation, testRepoID, wrongKey, badGrant, directory); err == nil {
		t.Fatal("grant bound to a different CSR was accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-grant attempt installed state: %v", err)
	}
}

func TestExpiredEnrollmentGrantRefusesBeforeExternalCASigning(t *testing.T) {
	fixture := newClientFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	expiredGrant := fixture.enrollmentGrantAt(fixture.identity.CSRDER, now.Add(-20*time.Minute))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER := testAuthorityCertificate(t, serverPrivate, testDomainID, fixture.ownerKeyID, now)
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverSPKI, err := x509.MarshalPKIXPublicKey(serverPublic)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := wipdauthority.NewProfile(fmt.Sprintf("https://localhost:%d", port), testDomainID, 1, testDigest(serverSPKI), fixture.ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	var signerCalls atomic.Int32
	server, err := wipdauthority.NewM5LabServer(profile, tlsCertificate(t, serverDER, serverPrivate), wipdauthority.M5LabConfig{
		Store: fixture.store, RepoID: testRepoID, EnrollmentGrant: expiredGrant,
		ExpectedCSRDER: fixture.identity.CSRDER, EnvironmentCACertificateDER: fixture.caDER,
		SignEnvironmentLeaf: func(_ context.Context, domain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			signerCalls.Add(1)
			return testEnvironmentLeaf(t, fixture.caKey, fixture.caDER, csrDER, domain, environment, fixture.ownerKeyID, epoch, at)
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
		case <-serverDone:
		case <-time.After(6 * time.Second):
			t.Error("expired-grant authority did not stop")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(serverCert)
	directory := t.TempDir()
	if err = SavePending(directory, fixture.identity); err != nil {
		t.Fatal(err)
	}
	if _, err = EnrollAndSeed(context.Background(), profile, roots, fixture.ownerRoot, fixture.delegation, testRepoID, fixture.identity, expiredGrant, directory); err == nil {
		t.Fatal("expired enrollment grant was accepted")
	}
	if calls := signerCalls.Load(); calls != 0 {
		t.Fatalf("restricted CA signer was called %d times for an expired grant, want zero", calls)
	}
	if _, err = os.Stat(filepath.Join(directory, stateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired-grant attempt installed client state: %v", err)
	}
}

func TestVerifyEmptySeedRejectsTruncatedReorderedOrMismatchedProduct(t *testing.T) {
	fixture := newClientFixture(t)
	frames := fixture.seedFrames(t)
	for _, test := range []struct {
		name   string
		mutate func([]wipdwire.Frame) []wipdwire.Frame
	}{
		{name: "truncated", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame { return frames[:2] }},
		{name: "reordered", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			return []wipdwire.Frame{frames[1], frames[0], frames[2]}
		}},
		{name: "different request", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			frames[2].RequestID = "00000000000000000000000000"
			return frames
		}},
		{name: "incomplete", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			frames[2].Payload = mustEncode(t, wipdwire.SeedEnd{Schema: "wipd.seed-end/1", TransferID: "01KZ7XHAQT1S46NYPN1PW1DX3D", VerifiedPrefix: wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}, ManifestDigest: emptyManifestDigest(), Complete: false})
			return frames
		}},
		{name: "unknown prefix key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[0].Payload, "schema", "transfer_id", "domain_id", "authority_epoch", "store_schema", "snapshot_id", "prefix", "event_count", "event_byte_length", "blob_manifest_digest")
			if err != nil {
				t.Fatal(err)
			}
			prefix := fields["prefix"].(map[string]any)
			prefix["unexpected"] = true
			frames[0].Payload = mustEncode(t, fields)
			return frames
		}},
		{name: "unknown prefix-anchor key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[2].Payload, "schema", "transfer_id", "verified_prefix", "verified_blob_manifest_digest", "complete")
			if err != nil {
				t.Fatal(err)
			}
			anchor := fields["verified_prefix"].(map[string]any)
			anchor["unexpected"] = true
			frames[2].Payload = mustEncode(t, fields)
			return frames
		}},
		{name: "unknown manifest-anchor key", mutate: func(frames []wipdwire.Frame) []wipdwire.Frame {
			fields, err := wipdwire.DecodeCanonicalMap(frames[1].Payload, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest")
			if err != nil {
				t.Fatal(err)
			}
			anchor := fields["as_of"].(map[string]any)
			anchor["unexpected"] = true
			frames[1].Payload = mustEncode(t, fields)
			return frames
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneFrames(frames)
			if _, err := verifyEmptySeed(test.mutate(candidate), fixture.profile, testRepoID, "01KZ7XHAQT1S46NYPN1PW1DX3E", "sha256:"+strings.Repeat("a", 64)); !errors.Is(err, ErrInvalidClientState) {
				t.Fatalf("mutated seed = %v, want invalid-state refusal", err)
			}
		})
	}
}

func TestSavePendingIsCreateOnlyAndRejectsPermissiveFiles(t *testing.T) {
	directory := t.TempDir()
	identity, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err = SavePending(directory, identity); err != nil {
		t.Fatal(err)
	}
	if err = SavePending(directory, identity); !errors.Is(err, os.ErrExist) {
		t.Fatalf("pending overwrite = %v, want already-exists", err)
	}
	if err = os.Chmod(filepath.Join(directory, pendingName), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPending(directory); !errors.Is(err, ErrInvalidClientState) {
		t.Fatalf("permissive pending file = %v, want invalid-state refusal", err)
	}
}

type clientFixture struct {
	profile        wipdauthority.Profile
	roots          *x509.CertPool
	owner          ed25519.PrivateKey
	ownerRoot      ed25519.PublicKey
	ownerKeyID     string
	authorityPin   string
	caKey          ed25519.PrivateKey
	caDER          []byte
	store          *authoritystore.Store
	listener       net.Listener
	server         *wipdauthority.Server
	serverCertDER  []byte
	serverPrivate  ed25519.PrivateKey
	serverStop     context.CancelFunc
	serverComplete chan error
	signerCalls    *atomic.Int32
	identity       PreparedIdentity
	grant          []byte
	delegation     []byte
}

func newClientFixture(t *testing.T) *clientFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ownerPublic, owner, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerKeyID := testSPKIID(t, ownerPublic)
	domain := authoritystore.Domain{ID: testDomainID, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerKeyID, ActiveEpoch: 1}
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	genesisNonce := bytes.Repeat([]byte{0x41}, 16)
	genesisGrant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, testRepoID, genesisNonce, now, now.Add(time.Minute))
	clear(setupPrivate)
	if err != nil {
		t.Fatal(err)
	}
	store, err := authoritystore.CreateEmpty(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.BootstrapDomainWithM5LabGrant(context.Background(), domain, testRepoID, setupPublic, genesisGrant, now); err != nil {
		t.Fatal(err)
	}

	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caDER := testCACertificate(t, caPrivate, now)
	delegation := signOwnerArtifact(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domain.ID, ownerKeyID, 1, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domain.ID, "authority_epoch": uint64(1), "owner_key_id": ownerKeyID,
		"ca_generation": uint64(1), "ca_key_id": testSPKIID(t, caPublic), "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err = store.InstallEnvironmentCA(context.Background(), domain.ID, delegation, now); err != nil {
		t.Fatal(err)
	}
	identity, err := PrepareIdentity()
	if err != nil {
		t.Fatal(err)
	}
	grantSource := &clientFixture{owner: owner, ownerKeyID: ownerKeyID}
	enrollmentGrant := grantSource.enrollmentGrant(identity.CSRDER)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER := testAuthorityCertificate(t, serverPrivate, domain.ID, ownerKeyID, now)
	serverCert, err := x509.ParseCertificate(serverDER)
	if err != nil {
		t.Fatal(err)
	}
	serverSPKI, err := x509.MarshalPKIXPublicKey(serverPublic)
	if err != nil {
		t.Fatal(err)
	}
	serverPin := testDigest(serverSPKI)
	profile, err := wipdauthority.NewProfile(fmt.Sprintf("https://localhost:%d", port), domain.ID, 1, serverPin, ownerKeyID)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyPair := tlsCertificate(t, serverDER, serverPrivate)
	signerCalls := &atomic.Int32{}
	server, err := wipdauthority.NewM5LabServer(profile, serverKeyPair, wipdauthority.M5LabConfig{
		Store: store, RepoID: testRepoID, EnrollmentGrant: enrollmentGrant,
		ExpectedCSRDER: identity.CSRDER, EnvironmentCACertificateDER: caDER,
		SignEnvironmentLeaf: func(_ context.Context, gotDomain string, epoch uint64, environment string, csrDER []byte, at time.Time) ([]byte, error) {
			signerCalls.Add(1)
			return testEnvironmentLeaf(t, caPrivate, caDER, csrDER, gotDomain, environment, ownerKeyID, epoch, at)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, serverStop := context.WithCancel(context.Background())
	serverComplete := make(chan error, 1)
	go func() { serverComplete <- server.Serve(serverCtx, listener) }()
	t.Cleanup(func() {
		serverStop()
		select {
		case <-serverComplete:
		case <-time.After(6 * time.Second):
			t.Error("authority server did not stop after cancellation")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(serverCert)
	return &clientFixture{
		profile: profile, roots: roots, owner: owner, ownerKeyID: ownerKeyID,
		authorityPin: serverPin, caKey: caPrivate, caDER: caDER, store: store,
		identity: identity, grant: enrollmentGrant,
		ownerRoot: ownerPublic, delegation: delegation,
		listener: listener, server: server, serverCertDER: serverDER, serverPrivate: serverPrivate,
		serverStop: serverStop, serverComplete: serverComplete, signerCalls: signerCalls,
	}
}

func (fixture *clientFixture) enrollmentGrant(csrDER []byte) []byte {
	return fixture.enrollmentGrantAt(csrDER, time.Now().UTC().Truncate(time.Second))
}

func (fixture *clientFixture) enrollmentGrantAt(csrDER []byte, now time.Time) []byte {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		panic(err)
	}
	ownerKeyID := fixture.ownerKeyID
	spkiDigest := testSPKIID(nil, csr.PublicKey)
	return signOwnerArtifact(nil, fixture.owner, "enrollment-grant", "wipd.enrollment-grant/1", testDomainID, ownerKeyID, 1, map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": testGrantID, "domain_id": testDomainID,
		"authority_epoch": uint64(1), "owner_key_id": ownerKeyID, "scope": "environment-enroll",
		"requested_spki_digest": spkiDigest, "prior_environment_id": nil, "nonce": bytes.Repeat([]byte{0x62}, 16),
		"issued_at": now.Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	})
}

func (fixture *clientFixture) seedFrames(t *testing.T) []wipdwire.Frame {
	t.Helper()
	requestID := "01KZ7XHAQT1S46NYPN1PW1DX3D"
	empty := wipdwire.PrefixAnchor{Digest: emptyPrefixDigest()}
	manifestDigest := emptyManifestDigest()
	start := wipdwire.SeedStart{
		Schema: "wipd.seed-start/1", TransferID: "01KZ7XHAQT1S46NYPN1PW1DX3E", DomainID: testDomainID, Epoch: 1,
		StoreSchema: "wipd.store/1", SnapshotID: "01KZ7XHAQT1S46NYPN1PW1DX3F", Prefix: struct {
			Start wipdwire.PrefixAnchor `cbor:"start"`
			End   wipdwire.PrefixAnchor `cbor:"end"`
		}{Start: empty, End: empty}, ManifestDigest: manifestDigest,
	}
	manifest := wipdwire.BlobManifest{Schema: "wipd.blob-manifest/1", DomainID: testDomainID, Epoch: 1, AsOf: empty, Entries: []wipdwire.BlobManifestEntry{}, Digest: manifestDigest}
	end := wipdwire.SeedEnd{Schema: "wipd.seed-end/1", TransferID: start.TransferID, VerifiedPrefix: empty, ManifestDigest: manifestDigest, Complete: true}
	return []wipdwire.Frame{
		{RequestID: requestID, Sequence: 0, Kind: "seed.start", Payload: mustEncode(t, start)},
		{RequestID: requestID, Sequence: 1, Kind: "blob.manifest", Payload: mustEncode(t, manifest)},
		{RequestID: requestID, Sequence: 2, Kind: "seed.end", Payload: mustEncode(t, end)},
	}
}

func testCACertificate(t *testing.T, private ed25519.PrivateKey, now time.Time) []byte {
	t.Helper()
	public := private.Public().(ed25519.PublicKey)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "M5 delegated Environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testAuthorityCertificate(t *testing.T, private ed25519.PrivateKey, domain, owner string, now time.Time) []byte {
	t.Helper()
	uri, err := url.Parse(fmt.Sprintf("wipd://authority/%s?epoch=1&owner=%s", domain, strings.TrimPrefix(owner, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	san, err := asn1.Marshal([]asn1.RawValue{
		{Class: asn1.ClassContextSpecific, Tag: 2, Bytes: []byte("localhost")},
		{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri.String())},
	})
	if err != nil {
		t.Fatal(err)
	}
	public := private.Public().(ed25519.PublicKey)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(20), Subject: pkix.Name{CommonName: "M5 lab authority"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, MaxPathLen: 0, MaxPathLenZero: true,
		KeyUsage:        x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testEnvironmentLeaf(t *testing.T, caPrivate ed25519.PrivateKey, caDER, csrDER []byte, domain, environment, owner string, epoch uint64, now time.Time) ([]byte, error) {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errors.New("invalid CSR")
	}
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", environment, domain, epoch, strings.TrimPrefix(owner, "sha256:"))
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(30), Subject: pkix.Name{CommonName: "M5 Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: asn1.ObjectIdentifier{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	return x509.CreateCertificate(rand.Reader, template, ca, csr.PublicKey, caPrivate)
}

func signOwnerArtifact(t *testing.T, owner ed25519.PrivateKey, kind, payloadSchema, domain, ownerID string, epoch uint64, payload map[string]any) []byte {
	if t != nil {
		t.Helper()
	}
	payloadBytes, err := testEncoder.Marshal(payload)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	unsigned := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": kind, "domain_id": domain, "authority_epoch": epoch,
		"signer_role": "owner", "signer_key_id": ownerID, "key_generation": nil, "artifact_sequence": nil,
		"previous_artifact_digest": nil, "issued_at": time.Now().UTC().Format(time.RFC3339Nano),
		"payload_schema": payloadSchema, "payload_digest": testDigest(payloadBytes), "payload": payloadBytes,
	}
	preimage, err := testEncoder.Marshal(unsigned)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	unsigned["signature"] = ed25519.Sign(owner, append([]byte("wipd/signed-artifact/v1\x00"), preimage...))
	encoded, err := testEncoder.Marshal(unsigned)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return encoded
}

func testSPKIID(t *testing.T, public any) string {
	if t != nil {
		t.Helper()
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return testDigest(der)
}

func testDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func tlsCertificate(t *testing.T, der []byte, key ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func mustEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := wipdwire.EncodeCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func cloneFrames(frames []wipdwire.Frame) []wipdwire.Frame {
	result := make([]wipdwire.Frame, len(frames))
	for i, frame := range frames {
		result[i] = frame
		result[i].Payload = bytes.Clone(frame.Payload)
	}
	return result
}
