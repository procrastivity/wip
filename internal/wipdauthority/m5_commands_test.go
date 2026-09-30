package wipdauthority

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
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	m5TestDomain = "01KZ7XHAQT1S46NYPN1PW1DX3A"
	m5TestRepo   = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	m5TestEnv    = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	m5TestGrant  = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	m5TestMatter = "01KZ7XHAQT1S46NYPN1PW1DX3E"
)

type m5CommandFixture struct {
	store        *authoritystore.Store
	root         string
	handler      http.Handler
	server       *Server
	listener     net.Listener
	serverRoots  *x509.CertPool
	clientCert   tls.Certificate
	profile      Profile
	serverCert   tls.Certificate
	config       M5LabConfig
	ownerRoot    ed25519.PublicKey
	environment  ed25519.PrivateKey
	csrDER       []byte
	grant        []byte
	caDelegation []byte
	peer         tls.ConnectionState
	calls        *atomic.Int32
	now          time.Time
}

func newM5CommandFixture(t *testing.T) *m5CommandFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), "authority")
	store, err := authoritystore.CreateEmpty(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner := m5TestKey("owner-root")
	ownerPublic := owner.Public().(ed25519.PublicKey)
	ownerID := m5TestPublicDigest(ownerPublic)
	domain := authoritystore.Domain{
		ID: m5TestDomain, OwnerPublicKey: ownerPublic, OwnerKeyID: ownerID, ActiveEpoch: 1,
	}
	setupPublic, setupPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	genesisGrant, err := authoritystore.CreateM5LabGenesisGrant(setupPrivate, domain, m5TestRepo, nonce, now, now.Add(10*time.Minute))
	clear(setupPrivate)
	clear(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BootstrapDomainWithM5LabGrant(ctx, domain, m5TestRepo, setupPublic, genesisGrant, now); err != nil {
		t.Fatal(err)
	}

	artifactKey := m5TestKey("artifact-signer")
	artifactCertificate := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "authority-artifact-key", "wipd.authority-artifact-key/1", map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": m5TestDomain, "authority_epoch": uint64(1),
		"key_generation": uint64(1), "key_id": m5TestPublicDigest(artifactKey.Public().(ed25519.PublicKey)),
		"ed25519_public_key": []byte(artifactKey.Public().(ed25519.PublicKey)),
		"not_before":         now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}, now)
	if err = store.RegisterArtifactKey(ctx, m5TestDomain, artifactCertificate, now); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureArtifactKey(ctx, m5TestDomain, artifactCertificate, now); err != nil {
		t.Fatalf("exact certified signer retry: %v", err)
	}

	caPrivate := m5TestKey("environment-ca")
	caDER := m5TestCACertificate(t, caPrivate, now)
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	delegation := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "environment-ca-delegation", "wipd.environment-ca-delegation/1", map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": m5TestDomain, "authority_epoch": uint64(1),
		"owner_key_id": ownerID, "ca_generation": uint64(1), "ca_key_id": m5TestPublicDigest(caPrivate.Public().(ed25519.PublicKey)),
		"ca_certificate_der": caDER, "not_before": caCertificate.NotBefore.UTC().Format(time.RFC3339Nano),
		"not_after": caCertificate.NotAfter.UTC().Format(time.RFC3339Nano),
	}, now)
	if err = store.InstallEnvironmentCA(ctx, m5TestDomain, delegation, now); err != nil {
		t.Fatal(err)
	}

	environmentPrivate := m5TestKey("environment-leaf")
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "Environment"}}, environmentPrivate)
	if err != nil {
		t.Fatal(err)
	}
	grant := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "enrollment-grant", "wipd.enrollment-grant/1", map[string]any{
		"schema": "wipd.enrollment-grant/1", "grant_id": m5TestGrant, "domain_id": m5TestDomain,
		"authority_epoch": uint64(1), "owner_key_id": ownerID, "scope": "environment-enroll",
		"requested_spki_digest": m5TestPublicDigest(environmentPrivate.Public().(ed25519.PublicKey)),
		"prior_environment_id":  nil, "nonce": bytes.Repeat([]byte{0x51}, 16),
		"issued_at": now.Add(-time.Minute).Format(time.RFC3339Nano), "expires_at": now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}, now)
	leafDER := m5TestLeafCertificate(t, environmentPrivate, caPrivate, caDER, m5TestDomain, m5TestEnv, ownerID, now)
	if _, err = store.IssueEnvironmentCertificate(ctx, m5TestDomain, m5TestEnv, grant, csr, [][]byte{leafDER, caDER}, now); err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{leaf, caCertificate}}
	clientCert := tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: environmentPrivate, Leaf: leaf}

	calls := &atomic.Int32{}
	registry := operation.NewRegistry()
	err = registry.Register(operation.MatterCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		calls.Add(1)
		input := request.Input.(operation.MatterCreateInput)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
			ID: m5TestMatter, Locator: input.Locator, Title: input.Title,
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	err = registry.Register(operation.StepCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		calls.Add(1)
		input := request.Input.(operation.StepCreateInput)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
			ParentID: input.ParentID, Title: input.Title,
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	tlsFixture := newAuthorityTLSFixture(t, listener, m5TestDomain, 1, ownerID)
	profile := tlsFixture.profile
	profile, err = profile.WithM5LabRepoID(m5TestRepo)
	if err != nil {
		t.Fatal(err)
	}
	config := M5LabConfig{
		Store: store, RepoID: m5TestRepo, EnrollmentGrant: grant, ExpectedCSRDER: csr,
		EnvironmentCACertificateDER: caDER,
		SignEnvironmentLeaf: func(context.Context, string, uint64, string, []byte, time.Time) ([]byte, error) {
			return nil, fmt.Errorf("unexpected enrollment in command test")
		},
		Registry: registry, ArtifactKeyCertificate: artifactCertificate,
		SignArtifact: func(_ context.Context, preimage []byte) ([]byte, error) {
			return ed25519.Sign(artifactKey, preimage), nil
		},
	}
	fixture := &m5CommandFixture{
		store: store, root: root, profile: profile, serverCert: tlsFixture.cert,
		config: config, peer: peer, calls: calls, now: now, listener: listener,
		serverRoots: tlsFixture.roots, clientCert: clientCert, ownerRoot: append(ed25519.PublicKey(nil), ownerPublic...),
		environment: append(ed25519.PrivateKey(nil), environmentPrivate...), csrDER: bytes.Clone(csr),
		grant: bytes.Clone(grant), caDelegation: bytes.Clone(delegation),
	}
	fixture.server = fixture.serverForStore(t, store)
	fixture.handler = fixture.server.http.Handler
	return fixture
}

func (fixture *m5CommandFixture) serverForStore(t *testing.T, store *authoritystore.Store) *Server {
	t.Helper()
	config := fixture.config
	config.Store = store
	server, err := NewM5LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func (fixture *m5CommandFixture) handlerForStore(t *testing.T, store *authoritystore.Store) http.Handler {
	t.Helper()
	return fixture.serverForStore(t, store).http.Handler
}

func m5TestKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func m5TestPublicDigest(public ed25519.PublicKey) string {
	spki, _ := x509.MarshalPKIXPublicKey(public)
	sum := sha256.Sum256(spki)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func m5TestOwnerArtifact(t *testing.T, owner ed25519.PrivateKey, ownerID, domain, kind, schema string, payload map[string]any, issued time.Time) []byte {
	t.Helper()
	payloadBytes, err := wipdwire.EncodeCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := map[string]any{
		"schema": "wipd.signed-artifact/1", "kind": kind, "domain_id": domain, "authority_epoch": uint64(1),
		"signer_role": "owner", "signer_key_id": ownerID, "key_generation": nil, "artifact_sequence": nil,
		"previous_artifact_digest": nil, "issued_at": issued.Format(time.RFC3339Nano), "payload_schema": schema,
		"payload_digest": digestBytes(payloadBytes), "payload": payloadBytes,
	}
	preimage, err := wipdwire.EncodeCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	unsigned["signature"] = ed25519.Sign(owner, append([]byte("wipd/signed-artifact/v1\x00"), preimage...))
	wrapper, err := wipdwire.EncodeCanonical(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func m5TestCACertificate(t *testing.T, private ed25519.PrivateKey, now time.Time) []byte {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "M5 test Environment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), BasicConstraintsValid: true,
		IsCA: true, MaxPathLen: 0, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), private)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func m5TestLeafCertificate(t *testing.T, leaf, caPrivate ed25519.PrivateKey, caDER []byte, domain, environment, ownerID string, now time.Time) []byte {
	t.Helper()
	uri := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=1&owner=%s", environment, domain, ownerID[len("sha256:"):])
	san, err := asn1.Marshal([]asn1.RawValue{{Class: asn1.ClassContextSpecific, Tag: 6, Bytes: []byte(uri)}})
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "M5 test Environment"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ExtraExtensions: []pkix.Extension{
			{Id: []int{2, 5, 29, 19}, Critical: true, Value: []byte{0x30, 0x00}},
			{Id: []int{2, 5, 29, 15}, Critical: true, Value: []byte{0x03, 0x02, 0x07, 0x80}},
			{Id: []int{2, 5, 29, 17}, Critical: true, Value: san},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, mustM5Certificate(t, caDER), leaf.Public(), caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func mustM5Certificate(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func m5Command(id string, sequence uint64, locator string) operation.Command {
	return operation.Command{
		ID: id, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1, EnvironmentID: m5TestEnv,
		EnvironmentSequence: sequence, ActedAt: "2026-09-23T12:00:00Z", CorrelationCommandID: id,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.MatterCreateInput{Title: "A title", Locator: locator},
		},
	}
}

func m5StepCommand(id string, sequence uint64, parentID, birthCommandID, title string) operation.Command {
	command := m5Command(id, sequence, "")
	command.CausationCommandID = birthCommandID
	command.CorrelationCommandID = birthCommandID
	command.Request.Operation = operation.StepCreateV1.Metadata().Operation
	command.Request.Claim = &operation.ClaimContext{ID: parentID, Epoch: "1"}
	command.Request.Input = operation.StepCreateInput{ParentID: parentID, Title: title}
	return command
}

func m5CommandHash(t *testing.T, command operation.Command) string {
	t.Helper()
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func m5Session(t *testing.T, handler http.Handler, peer tls.ConnectionState) *labConnectionSession {
	return m5SessionWithStep(t, handler, peer, false)
}

func m5SessionWithStep(t *testing.T, handler http.Handler, peer tls.ConnectionState, includeStep bool) *labConnectionSession {
	t.Helper()
	session := &labConnectionSession{}
	operations := []any{map[string]any{"name": "matter.create", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}}}
	if includeStep {
		operations = append(operations, map[string]any{"name": "step.create", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}})
	}
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations":       operations,
		"store_schemas":    []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, request := m5Request(t, session, peer, "client.hello", hello, "POST", labNegotiatePath)
	handler.ServeHTTP(record, request)
	frames := m5ResponseFrames(t, record, 2)
	if len(frames) != 2 || frames[0].Kind != "server.hello" || frames[1].Kind != "session.parameters" || !session.negotiated {
		t.Fatalf("negotiation response = %+v, session=%+v", frames, session)
	}
	selected, err := wipdwire.DecodeCanonicalMap(frames[0].Payload,
		"selected_protocol", "identity_schemas", "operations", "store_schemas", "features")
	if err != nil {
		t.Fatal(err)
	}
	ops, ok := selected["operations"].([]any)
	wantCount := 1
	if includeStep {
		wantCount = 2
	}
	if !ok || len(ops) != wantCount {
		t.Fatalf("selected operations = %#v", selected["operations"])
	}
	for i, name := range []string{"matter.create", "step.create"}[:wantCount] {
		capability, ok := ops[i].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(capability, "name", "versions", "identity_schemas") || capability["name"] != name ||
			!reflect.DeepEqual(capability["versions"], []any{uint64(1)}) || !reflect.DeepEqual(capability["identity_schemas"], []any{"wipd.command/1"}) {
			t.Fatalf("selected operation capability = %#v", ops[i])
		}
	}
	return session
}

func m5Request(t *testing.T, session *labConnectionSession, peer tls.ConnectionState, kind string, payload []byte, method, path string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	requestID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	frame, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: kind, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(frame))
	request.Header.Set("Content-Type", "application/cbor")
	request.TLS = &peer
	request = request.WithContext(context.WithValue(request.Context(), labSessionContextKey{}, session))
	return httptest.NewRecorder(), request
}

func m5CommandRequest(t *testing.T, session *labConnectionSession, peer tls.ConnectionState, kind string, payload any) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	encoded, err := wipdwire.EncodeCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return m5Request(t, session, peer, kind, encoded, http.MethodPost, labExchangePath)
}

func m5ResponseFrames(t *testing.T, response *httptest.ResponseRecorder, maxFrames int) []wipdwire.Frame {
	t.Helper()
	frames, err := wipdwire.ReadFrames(response.Body.Bytes(), maxFrames)
	if err != nil {
		t.Fatalf("decode response frames: %v; status %d; body %q", err, response.Code, response.Body.String())
	}
	return frames
}

func m5HTTPSExchange(t *testing.T, client *http.Client, profile Profile, path, kind string, payload []byte, connection *net.Conn) []wipdwire.Frame {
	t.Helper()
	requestID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	frame, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: kind, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, profile.Origin()+path, bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/cbor")
	var got net.Conn
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { got = info.Conn }}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 2 || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("%s response = status %d, protocol %s, TLS=%v", kind, response.StatusCode, response.Proto, response.TLS)
	}
	if *connection == nil {
		*connection = got
	} else if got != *connection {
		t.Fatal("negotiation and command requests did not reuse the same HTTP/2 connection")
	}
	frames, err := wipdwire.ReadFrames(body, 4)
	if err != nil {
		t.Fatalf("decode %s response: %v", kind, err)
	}
	return frames
}

func assertFindingAppendAuthorityReplay(t *testing.T, fixture *m5CommandFixture, command operation.Command, want operation.ContentSegmentOutput) {
	t.Helper()
	client, err := fixture.profile.HTTPClientWithCertificate(fixture.serverRoots, &fixture.clientCert)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	var connection net.Conn
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations": []any{map[string]any{
			"name": "finding.append", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"},
		}},
		"store_schemas": []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	negotiated := m5HTTPSExchange(t, client, fixture.profile, labNegotiatePath, "client.hello", hello, &connection)
	if len(negotiated) != 2 || negotiated[0].Kind != "server.hello" || negotiated[1].Kind != "session.parameters" {
		t.Fatalf("finding.append authenticated negotiation = %+v", negotiated)
	}
	hash := m5CommandHash(t, command)
	status, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("finding.append durable receipt before retry = %+v, %v", status, err)
	}
	receipt, err := wipdwire.DecodeCanonicalMap(status.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receipt["command_id"] != command.ID || receipt["request_hash"] != hash {
		t.Fatalf("finding.append receipt identity = %#v, %v", receipt, err)
	}
	result, ok := receipt["result"].(map[string]any)
	outputBytes, outputOK := result["output"].([]byte)
	output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "id", "subject_id", "kind", "blob_digest", "byte_length")
	if !ok || !outputOK || outputErr != nil || result["code"] != string(operation.ResultSucceeded) ||
		output["id"] != want.ID || output["subject_id"] != want.SubjectID || output["kind"] != want.Kind ||
		output["blob_digest"] != want.BlobDigest || output["byte_length"] != uint64(want.ByteLength) {
		t.Fatalf("finding.append durable output = %#v, %v", result, outputErr)
	}
	before, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshotID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshot, err := fixture.store.PinSnapshot(context.Background(), m5TestDomain, 1,
		authoritystore.EmptyPrefixAnchor(), beforeSnapshotID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	submit, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	replay := m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "command.submit", submit, &connection)
	if len(replay) != 1 || replay[0].Kind != "command.terminal" || !bytes.Equal(replay[0].Payload, status.Receipt) {
		t.Fatalf("authenticated finding.append exact retry = %+v, want exact durable receipt", replay)
	}
	conflict := command
	input, ok := conflict.Request.Input.(operation.FindingAppendInput)
	if !ok {
		t.Fatalf("finding.append command input = %T", conflict.Request.Input)
	}
	input.SubjectID = "01KZ7XHAQT1S46NYPN1PW1DX99"
	conflict.Request.Input = input
	conflictCanonical, err := conflict.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	conflictHash, err := conflict.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	conflictPayload, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: conflictCanonical, RequestHash: conflictHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	conflictFrames := m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "command.submit", conflictPayload, &connection)
	if len(conflictFrames) != 1 || conflictFrames[0].Kind != "problem" || m5ProblemCode(t, conflictFrames[0]) != "command.id-conflict" {
		t.Fatalf("authenticated finding.append same-ID/different-hash retry = %+v", conflictFrames)
	}
	after, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	afterSnapshotID, err := randomULID(time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	afterSnapshot, err := fixture.store.PinSnapshot(context.Background(), m5TestDomain, 1,
		authoritystore.EmptyPrefixAnchor(), afterSnapshotID, time.Now().UTC(), time.Minute)
	if err != nil || after != before || !reflect.DeepEqual(afterSnapshot.Delta.Events, beforeSnapshot.Delta.Events) {
		t.Fatalf("finding.append replay/conflict changed authority event bytes/order: before=%+v after=%+v events=%d/%d err=%v",
			before, after, len(beforeSnapshot.Delta.Events), len(afterSnapshot.Delta.Events), err)
	}
}

func TestM5CommandExchangeUsesNegotiatedAuthenticatedHTTP2(t *testing.T) {
	fixture := newM5CommandFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- fixture.server.Serve(ctx, fixture.listener) }()
	var client *http.Client
	t.Cleanup(func() {
		if client != nil {
			client.CloseIdleConnections()
		}
		cancel()
		select {
		case err := <-serveResult:
			if err != nil {
				t.Errorf("authority server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority server did not stop after context cancellation")
		}
	})
	client, err := fixture.profile.HTTPClientWithCertificate(fixture.serverRoots, &fixture.clientCert)
	if err != nil {
		t.Fatal(err)
	}

	var connection net.Conn
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations":       []any{map[string]any{"name": "matter.create", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}}},
		"store_schemas":    []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	negotiated := m5HTTPSExchange(t, client, fixture.profile, labNegotiatePath, "client.hello", hello, &connection)
	if len(negotiated) != 2 || negotiated[0].Kind != "server.hello" || negotiated[1].Kind != "session.parameters" {
		t.Fatalf("negotiation response = %+v", negotiated)
	}
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	submit, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: m5CommandHash(t, command),
	})
	if err != nil {
		t.Fatal(err)
	}
	submitted := m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "command.submit", submit, &connection)
	if len(submitted) != 2 || submitted[0].Kind != "submission.accepted" || submitted[1].Kind != "command.terminal" {
		t.Fatalf("authenticated submit response = %+v", submitted)
	}
	query, err := wipdwire.EncodeCanonical(wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: m5TestDomain,
		CommandID: command.ID, RequestHash: m5CommandHash(t, command),
	})
	if err != nil {
		t.Fatal(err)
	}
	queried := m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "receipt.query", query, &connection)
	if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, submitted[1].Payload) || fixture.calls.Load() != 1 {
		t.Fatalf("authenticated receipt query = %+v; calls=%d", queried, fixture.calls.Load())
	}
}

func TestM5OpenBodyFlushesAcceptedBeforeTerminalAndControlCancelOnlyStopsWait(t *testing.T) {
	for _, mode := range []string{"control.cancel", "HTTP/2 stream reset"} {
		t.Run(mode, func(t *testing.T) { testM5OpenBodyCancellation(t, mode) })
	}
}

func testM5OpenBodyCancellation(t *testing.T, mode string) {
	fixture := newM5CommandFixture(t)
	releaseDispatch := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseDispatch) }) }
	dispatchEntered := make(chan struct{})
	var enterOnce sync.Once
	registry := operation.NewRegistry()
	if err := registry.Register(operation.MatterCreateV1, func(_ context.Context, request operation.Request) operation.Result {
		fixture.calls.Add(1)
		enterOnce.Do(func() { close(dispatchEntered) })
		<-releaseDispatch
		input := request.Input.(operation.MatterCreateInput)
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
			ID: m5TestMatter, Locator: input.Locator, Title: input.Title,
		}}
	}); err != nil {
		t.Fatal(err)
	}
	fixture.config.Registry = registry
	server := fixture.serverForStore(t, fixture.store)
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(ctx, fixture.listener) }()
	client, err := fixture.profile.HTTPClientWithCertificate(fixture.serverRoots, &fixture.clientCert)
	if err != nil {
		unblock()
		cancel()
		t.Fatal(err)
	}
	var connection net.Conn
	t.Cleanup(func() {
		unblock()
		client.CloseIdleConnections()
		cancel()
		select {
		case err := <-serveResult:
			if err != nil {
				t.Errorf("authority server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority server did not stop after context cancellation")
		}
	})

	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations":       []any{map[string]any{"name": "matter.create", "versions": []any{uint64(1)}, "identity_schemas": []any{"wipd.command/1"}}},
		"store_schemas":    []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	negotiated := m5HTTPSExchange(t, client, fixture.profile, labNegotiatePath, "client.hello", hello, &connection)
	if len(negotiated) != 2 || negotiated[0].Kind != "server.hello" || negotiated[1].Kind != "session.parameters" {
		t.Fatalf("negotiation response = %+v", negotiated)
	}

	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: m5CommandHash(t, command),
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	firstFrame, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: "command.submit", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	requestReader, requestWriter := io.Pipe()
	t.Cleanup(func() { _ = requestWriter.Close() })
	request, err := http.NewRequest(http.MethodPost, fixture.profile.Origin()+labExchangePath, requestReader)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, resetRequest := context.WithCancel(request.Context())
	defer resetRequest()
	request.Header.Set("Content-Type", "application/cbor")
	var exchangeConnection net.Conn
	request = request.WithContext(httptrace.WithClientTrace(requestContext, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { exchangeConnection = info.Conn },
	}))
	responseCh := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, requestErr := client.Do(request)
		responseCh <- struct {
			response *http.Response
			err      error
		}{response: response, err: requestErr}
	}()
	if _, err = requestWriter.Write(firstFrame); err != nil {
		t.Fatalf("write first command frame: %v", err)
	}
	var response *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		response = result.response
	case <-time.After(3 * time.Second):
		t.Fatal("open-body command did not receive a response before request EOF")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 2 || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 || exchangeConnection != connection {
		t.Fatalf("open-body response status=%d protocol=%s TLS=%v reused=%v", response.StatusCode, response.Proto, response.TLS, exchangeConnection == connection)
	}
	ack, err := wipdwire.ReadFrame(response.Body)
	if err != nil || ack.Kind != "submission.accepted" || ack.Sequence != 0 || ack.RequestID != requestID {
		t.Fatalf("open-body acknowledgment = %+v, %v", ack, err)
	}
	select {
	case <-dispatchEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("accepted command did not enter the blocking operation")
	}
	terminalRead := make(chan struct {
		frame wipdwire.Frame
		err   error
	}, 1)
	go func() {
		frame, readErr := wipdwire.ReadFrame(response.Body)
		terminalRead <- struct {
			frame wipdwire.Frame
			err   error
		}{frame: frame, err: readErr}
	}()
	select {
	case result := <-terminalRead:
		t.Fatalf("terminal response arrived while operation remained blocked: %+v %v", result.frame, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	if mode == "control.cancel" {
		cancelFrame, encodeErr := wipdwire.EncodeFrame(wipdwire.Frame{
			RequestID: requestID, Sequence: 1, Kind: "control.cancel", Payload: []byte{0xa0},
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, err = requestWriter.Write(cancelFrame); err != nil {
			t.Fatalf("write control.cancel: %v", err)
		}
		_ = requestWriter.Close()
	} else {
		resetRequest()
		_ = requestWriter.CloseWithError(context.Canceled)
	}
	select {
	case result := <-terminalRead:
		if result.err == nil || (mode == "control.cancel" && !errors.Is(result.err, io.EOF)) {
			t.Fatalf("%s response stream did not stop without a terminal record: %+v %v", mode, result.frame, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not stop waiting on the response stream", mode)
	}
	unblock()
	waitM5Terminal(t, fixture.store, fixture.peer, command)
	if fixture.calls.Load() != 1 {
		t.Fatalf("cancelled accepted command handler calls=%d, want 1", fixture.calls.Load())
	}
	query := wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: m5TestDomain,
		CommandID: command.ID, RequestHash: m5CommandHash(t, command),
	}
	queryPayload, err := wipdwire.EncodeCanonical(query)
	if err != nil {
		t.Fatal(err)
	}
	queryFrames := m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "receipt.query", queryPayload, &connection)
	if len(queryFrames) != 1 || queryFrames[0].Kind != "command.terminal" {
		t.Fatalf("receipt query after %s = %+v", mode, queryFrames)
	}
}

func TestM6MatterCreateV2AuthenticatedCollisionReplayAndReopen(t *testing.T) {
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step4Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	server, err := NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatalf("create explicit M6 lab server: %v", err)
	}
	fixture.server, fixture.handler = server, server.http.Handler
	session := m6Step4Session(t, fixture.handler, fixture.peer, registry)

	first := m5Command(claimTestIDForM6(201), 1, "shared")
	first.Request.Operation = operation.MatterCreateV2.Metadata().Operation
	first.Request.Input = operation.MatterCreateInput{Title: "First Matter", Locator: "shared"}
	firstFrames := submitM6Matter(t, fixture, session, first)
	firstReceipt := m6TerminalReceipt(t, firstFrames)
	firstOutput, firstCount := m6MatterCreateOutput(t, firstReceipt)
	if firstOutput["requested_locator"] != "shared" || firstOutput["assigned_locator"] != "shared" || firstOutput["locator_repair_required"] != false || firstCount != 1 {
		t.Fatalf("first Matter result = %#v, events=%d; want un-repaired one-event birth", firstOutput, firstCount)
	}

	second := m5Command(claimTestIDForM6(202), 2, "shared")
	second.Request.Operation = operation.MatterCreateV2.Metadata().Operation
	second.Request.Input = operation.MatterCreateInput{Title: "Second Matter", Locator: "shared"}
	secondFrames := submitM6Matter(t, fixture, session, second)
	secondReceipt := m6TerminalReceipt(t, secondFrames)
	secondOutput, secondCount := m6MatterCreateOutput(t, secondReceipt)
	secondID, ok := secondOutput["id"].(string)
	assigned, assignedOK := secondOutput["assigned_locator"].(string)
	if !ok || !assignedOK || secondOutput["requested_locator"] != "shared" || secondOutput["title"] != "Second Matter" ||
		secondOutput["locator_repair_required"] != true || assigned != "shared-"+strings.ToLower(secondID[:6]) || secondCount != 2 {
		t.Fatalf("collision repair result = %#v, events=%d; want deterministic ID-suffix repair with two ordered events", secondOutput, secondCount)
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 3 {
		t.Fatalf("prefix after two M6 births = %+v, %v; want three events", anchor, err)
	}

	firstRetry := submitM6Matter(t, fixture, session, second)
	if len(firstRetry) != 1 || firstRetry[0].Kind != "command.terminal" || !bytes.Equal(firstRetry[0].Payload, secondReceipt) {
		t.Fatalf("exact M6 retry = %+v; want the byte-identical durable receipt", firstRetry)
	}
	conflict := second
	conflict.Request.Input = operation.MatterCreateInput{Title: "Changed intent", Locator: "shared"}
	conflictFrames := submitM6Matter(t, fixture, session, conflict)
	if len(conflictFrames) != 1 || conflictFrames[0].Kind != "problem" || m5ProblemCode(t, conflictFrames[0]) != "command.id-conflict" {
		t.Fatalf("same-ID different-hash M6 command = %+v", conflictFrames)
	}
	unchanged, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || unchanged != anchor {
		t.Fatalf("M6 replay/conflict changed authority prefix: %+v, %v; before %+v", unchanged, err, anchor)
	}

	if err = fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := authoritystore.OpenExisting(fixture.root)
	if err != nil {
		t.Fatalf("reopen M6 authority projection: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fixture.store = reopened
	config.Store = reopened
	server, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatalf("recreate M6 lab server after reopen: %v", err)
	}
	fixture.server, fixture.handler = server, server.http.Handler
	session = m6Step4Session(t, fixture.handler, fixture.peer, registry)
	reopenedRetry := submitM6Matter(t, fixture, session, second)
	if len(reopenedRetry) != 1 || reopenedRetry[0].Kind != "command.terminal" || !bytes.Equal(reopenedRetry[0].Payload, secondReceipt) {
		t.Fatalf("post-reopen exact retry = %+v; want original durable receipt", reopenedRetry)
	}
	finalAnchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || finalAnchor != anchor {
		t.Fatalf("reopened retry changed event bytes/order: %+v, %v; want %+v", finalAnchor, err, anchor)
	}
}

func m6Step4Session(t *testing.T, handler http.Handler, peer tls.ConnectionState, registry *operation.Registry) *labConnectionSession {
	t.Helper()
	byName := make(map[string][]any)
	for _, definition := range registry.Definitions() {
		id := definition.Metadata().Operation
		byName[id.Name] = append(byName[id.Name], uint64(id.Version))
	}
	operations := make([]any, 0, len(byName))
	for _, definition := range registry.Definitions() {
		id := definition.Metadata().Operation
		if len(byName[id.Name]) == 0 {
			continue
		}
		operations = append(operations, map[string]any{"name": id.Name, "versions": byName[id.Name], "identity_schemas": []any{"wipd.command/1"}})
		delete(byName, id.Name)
	}
	session := &labConnectionSession{}
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"}, "operations": operations,
		"store_schemas": []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, request := m5Request(t, session, peer, "client.hello", hello, http.MethodPost, labNegotiatePath)
	handler.ServeHTTP(record, request)
	frames := m5ResponseFrames(t, record, 2)
	if len(frames) != 2 || frames[0].Kind != "server.hello" || !session.negotiated || len(session.operations) != len(registry.Definitions()) {
		t.Fatalf("M6 capability negotiation = %+v; operations=%d", frames, len(session.operations))
	}
	return session
}

func submitM6Matter(t *testing.T, fixture *m5CommandFixture, session *labConnectionSession, command operation.Command) []wipdwire.Frame {
	t.Helper()
	response, frames := m5Submit(t, session, fixture, command)
	if response.Code != http.StatusOK || len(frames) == 0 {
		t.Fatalf("M6 command.submit status=%d frames=%+v", response.Code, frames)
	}
	return frames
}

func m6TerminalReceipt(t *testing.T, frames []wipdwire.Frame) []byte {
	t.Helper()
	if len(frames) != 2 || frames[0].Kind != "submission.accepted" || frames[1].Kind != "command.terminal" {
		t.Fatalf("M6 accepted terminal sequence = %+v", frames)
	}
	return bytes.Clone(frames[1].Payload)
}

func m6MatterCreateOutput(t *testing.T, receipt []byte) (map[string]any, uint64) {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || result["code"] != "result.succeeded" {
		t.Fatalf("M6 result = %#v", fields["result"])
	}
	outputBytes, ok := result["output"].([]byte)
	if !ok {
		t.Fatalf("M6 output type = %T", result["output"])
	}
	output, err := wipdwire.DecodeCanonicalMap(outputBytes, "id", "title", "requested_locator", "assigned_locator", "locator_repair_required")
	if err != nil {
		t.Fatal(err)
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	count, countOK := accepted["event_count"].(uint64)
	if !ok || !countOK {
		t.Fatalf("M6 accepted event range = %#v", fields["accepted_events"])
	}
	return output, count
}

func claimTestIDForM6(n int) string { return fmt.Sprintf("%026d", n) }

func TestM5ArtifactSignerCannotReuseCAOrEnvironmentKey(t *testing.T) {
	fixture := newM5CommandFixture(t)
	ca, err := x509.ParseCertificate(fixture.config.EnvironmentCACertificateDER)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(fixture.config.ExpectedCSRDER)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		public ed25519.PublicKey
	}{
		{name: "Environment CA", public: ca.PublicKey.(ed25519.PublicKey)},
		{name: "Environment", public: csr.PublicKey.(ed25519.PublicKey)},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := m5TestKey("owner-root")
			ownerID := fixture.profile.OwnerRootSPKI()
			keyID := m5TestPublicDigest(test.public)
			certificate := m5TestOwnerArtifact(t, owner, ownerID, m5TestDomain, "authority-artifact-key", "wipd.authority-artifact-key/1", map[string]any{
				"schema": "wipd.authority-artifact-key/1", "domain_id": m5TestDomain, "authority_epoch": uint64(1),
				"key_generation": uint64(1), "key_id": keyID, "ed25519_public_key": []byte(test.public),
				"not_before": fixture.now.Add(-time.Hour).Format(time.RFC3339Nano),
				"not_after":  fixture.now.Add(24 * time.Hour).Format(time.RFC3339Nano),
			}, fixture.now)
			config := fixture.config
			config.ArtifactKeyCertificate = certificate
			if _, err := NewM5LabServer(fixture.profile, fixture.serverCert, config); !errors.Is(err, ErrInvalidLabConfig) {
				t.Fatalf("server accepted reused %s key: %v", test.name, err)
			}
		})
	}
}

func m5Submit(t *testing.T, session *labConnectionSession, fixture *m5CommandFixture, command operation.Command) (*httptest.ResponseRecorder, []wipdwire.Frame) {
	t.Helper()
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5CommandRequest(t, session, fixture.peer, "command.submit", wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: m5CommandHash(t, command),
	})
	fixture.handler.ServeHTTP(response, request)
	return response, m5ResponseFrames(t, response, 4)
}

func m5Query(t *testing.T, session *labConnectionSession, fixture *m5CommandFixture, id, hash string) []wipdwire.Frame {
	t.Helper()
	response, request := m5CommandRequest(t, session, fixture.peer, "receipt.query", wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: m5TestDomain, CommandID: id, RequestHash: hash,
	})
	fixture.handler.ServeHTTP(response, request)
	return m5ResponseFrames(t, response, 2)
}

func m5ProblemCode(t *testing.T, frame wipdwire.Frame) string {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(frame.Payload, "code")
	if err != nil {
		t.Fatal(err)
	}
	code, ok := fields["code"].(string)
	if !ok {
		t.Fatalf("problem code has type %T", fields["code"])
	}
	return code
}

func TestM5ReadOnlyExchangesRejectBufferedTrailingFrames(t *testing.T) {
	for _, exchange := range []string{"seed.request", "receipt.query"} {
		for _, trailing := range []struct {
			name string
			kind string
			body []byte
		}{
			{name: "wrong direction", kind: "event.record", body: []byte{0xa0}},
			{name: "non-cancel client frame", kind: "command.submit", body: []byte{0xa0}},
			{name: "malformed cancel payload", kind: "control.cancel", body: []byte{0xa1, 0x61, 0x78, 0x01}},
		} {
			t.Run(exchange+"/"+trailing.name, func(t *testing.T) {
				fixture := newM5CommandFixture(t)
				session := m5Session(t, fixture.handler, fixture.peer)
				requestID, err := randomULID(time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				var firstPayload []byte
				switch exchange {
				case "seed.request":
					firstPayload, err = wipdwire.EncodeCanonical(wipdwire.SeedRequest{
						Schema: "wipd.seed-request/1", DomainID: m5TestDomain, Epoch: 1, StoreSchema: "wipd.store/1",
					})
				case "receipt.query":
					firstPayload, err = wipdwire.EncodeCanonical(wipdwire.ReceiptQuery{
						Schema: "wipd.receipt-query/1", DomainID: m5TestDomain,
						CommandID: "01KZ7XHAQT1S46NYPN1PW1DX3G", RequestHash: "sha256:" + string(bytes.Repeat([]byte{'0'}, 64)),
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				first, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: exchange, Payload: firstPayload})
				if err != nil {
					t.Fatal(err)
				}
				second, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Sequence: 1, Kind: trailing.kind, Payload: trailing.body})
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, labExchangePath, bytes.NewReader(append(first, second...)))
				request.Header.Set("Content-Type", "application/cbor")
				request.TLS = &fixture.peer
				request = request.WithContext(context.WithValue(request.Context(), labSessionContextKey{}, session))
				response := httptest.NewRecorder()
				fixture.handler.ServeHTTP(response, request)
				frames := m5ResponseFrames(t, response, 4)
				if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "protocol.out-of-order" {
					t.Fatalf("response to buffered trailing frame = %+v", frames)
				}
			})
		}
	}
}

type m5GatedResponseWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	writes  atomic.Int32
	once    sync.Once
}

func (writer *m5GatedResponseWriter) Write(data []byte) (int, error) {
	writer.writes.Add(1)
	writer.once.Do(func() {
		close(writer.entered)
		<-writer.release
	})
	return writer.ResponseRecorder.Write(data)
}

func TestM5TransferOutputStopsBetweenFramesOnControl(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      string
		wantCause error
	}{
		{name: "cancel", kind: "control.cancel", wantCause: errLabControlCancel},
		{name: "wrong direction", kind: "event.record", wantCause: errLabControlInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestID, err := randomULID(time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			bodyReader, bodyWriter := io.Pipe()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			arbiter := &labExchangeArbiter{}
			watchLabControl(ctx, bodyReader, requestID, cancel, arbiter)
			records := []labFrameRecord{
				{kind: "seed.start", payload: []byte{0xa0}},
				{kind: "event.record", payload: []byte{0xa0}},
				{kind: "blob.manifest", payload: []byte{0xa0}},
				{kind: "seed.end", payload: []byte{0xa0}},
			}
			response := httptest.NewRecorder()
			writer := &m5GatedResponseWriter{
				ResponseRecorder: response, entered: make(chan struct{}), release: make(chan struct{}),
			}
			var releaseOnce sync.Once
			releaseWriter := func() { releaseOnce.Do(func() { close(writer.release) }) }
			defer func() {
				releaseWriter()
				_ = bodyReader.Close()
				_ = bodyWriter.Close()
			}()
			writeDone := make(chan error, 1)
			go func() {
				writeDone <- writeLabFrames(writer, requestID, records, func(final bool) error {
					return arbiter.beginResponse(ctx, final)
				})
			}()
			select {
			case <-writer.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("transfer writer did not block on its first frame")
			}
			payload := []byte{0xa0}
			trailing, err := wipdwire.EncodeFrame(wipdwire.Frame{
				RequestID: requestID, Sequence: 1, Kind: test.kind, Payload: payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = bodyWriter.Write(trailing); err != nil {
				t.Fatalf("send %s control frame: %v", test.name, err)
			}
			_ = bodyWriter.Close()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatalf("%s control was not published to the exchange arbiter", test.name)
			}
			if outcome := arbiter.causeFor(ctx); !errors.Is(outcome, test.wantCause) {
				t.Fatalf("published control outcome = %v, want %v", outcome, test.wantCause)
			}
			releaseWriter()
			if err = <-writeDone; !errors.Is(err, test.wantCause) {
				t.Fatalf("transfer writer stop cause = %v, want %v", err, test.wantCause)
			}
			frames := m5ResponseFrames(t, response, 4)
			if writer.writes.Load() != 1 || len(frames) != 1 || frames[0].Kind != "seed.start" || frames[0].Sequence != 0 {
				t.Fatalf("frames after %s during first output write: writes=%d frames=%+v", test.name, writer.writes.Load(), frames)
			}
		})
	}
}

func TestM5ControlPublicationBeforeFinalSuppressesReceipt(t *testing.T) {
	requestID := "01KZ7XHAQT1S46NYPN1PW1DX3G"
	frame, err := wipdwire.EncodeFrame(wipdwire.Frame{
		RequestID: requestID, Sequence: 1, Kind: "event.record", Payload: []byte{0xa0},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	arbiter := &labExchangeArbiter{}
	watchLabControl(ctx, bytes.NewReader(frame), requestID, cancel, arbiter)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("parsed control was not published before finalization")
	}
	if cause := arbiter.causeFor(ctx); !errors.Is(cause, errLabControlInvalid) {
		t.Fatalf("published control outcome = %v, want invalid-control cause", cause)
	}
	response := httptest.NewRecorder()
	writeReadOnlyFinal(ctx, arbiter, response, requestID,
		wipdwire.Frame{RequestID: requestID, Kind: "receipt.pending", Payload: []byte{0xa0}}, "")
	frames := m5ResponseFrames(t, response, 4)
	if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "protocol.out-of-order" {
		t.Fatalf("final response after earlier control publication = %+v", frames)
	}
}

func TestM5FinalizationBeforeLatePublicationWinsBeforeWrite(t *testing.T) {
	requestID := "01KZ7XHAQT1S46NYPN1PW1DX3G"
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	arbiter := &labExchangeArbiter{}
	response := httptest.NewRecorder()
	finalStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWrite) }) }
	defer release()
	type finalResult struct {
		cause   error
		started bool
	}
	done := make(chan finalResult, 1)
	go func() {
		started, cause := arbiter.writeFinal(ctx, false, func() {
			close(finalStarted)
			<-releaseWrite
			writeLabFrame(response, wipdwire.Frame{
				RequestID: requestID, Kind: "receipt.pending", Payload: []byte{0xa0},
			})
		})
		done <- finalResult{cause: cause, started: started}
	}()
	select {
	case <-finalStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("arbiter did not commit final-response start")
	}
	if arbiter.publish(errLabControlInvalid) {
		t.Fatal("late control publication won after final-response start")
	}
	if err := arbiter.beginResponse(ctx, false); !errors.Is(err, errLabFinalAlreadyStarted) {
		t.Fatalf("post-final check returned %v, want final-started sentinel", err)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("response bytes escaped before the gated write: %q", response.Body.String())
	}
	release()
	select {
	case result := <-done:
		if result.cause != nil || !result.started {
			t.Fatalf("final write result = %+v, want committed successful start", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("final response did not complete after releasing the gated write")
	}
	frames := m5ResponseFrames(t, response, 4)
	if len(frames) != 1 || frames[0].Kind != "receipt.pending" {
		t.Fatalf("final response after losing late publication = %+v", frames)
	}
}

func TestM5ReadOnlyExchangeDoesNotWaitForIdleOpenBody(t *testing.T) {
	bodyReader, bodyWriter := io.Pipe()
	defer func() {
		_ = bodyReader.Close()
		_ = bodyWriter.Close()
	}()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	arbiter := &labExchangeArbiter{}
	watchLabControl(ctx, bodyReader, "01KZ7XHAQT1S46NYPN1PW1DX3G", cancel, arbiter)
	response := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		done <- writeReadOnlyFinal(ctx, arbiter, response, "01KZ7XHAQT1S46NYPN1PW1DX3G",
			wipdwire.Frame{RequestID: "01KZ7XHAQT1S46NYPN1PW1DX3G", Kind: "receipt.pending", Payload: []byte{0xa0}}, "")
	}()
	select {
	case started := <-done:
		if !started {
			t.Fatal("idle open body suppressed final response")
		}
	case <-time.After(time.Second):
		t.Fatal("read-only result waited for an idle open request body")
	}
}

func TestM5AuthenticatedSubmitReplayConflictAndReceiptQueries(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5Session(t, fixture.handler, fixture.peer)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	hash := m5CommandHash(t, command)
	missing := m5Query(t, session, fixture, "01KZ7XHAQT1S46NYPN1PW1DX3G", "sha256:"+string(bytes.Repeat([]byte{'0'}, 64)))
	if len(missing) != 1 || missing[0].Kind != "receipt.not-found" {
		t.Fatalf("not-found query response = %+v", missing)
	}
	var notFound wipdwire.ReceiptNotFound
	if err := wipdwire.DecodeCanonical(missing[0].Payload, &notFound, "schema", "domain_id", "command_id", "request_hash"); err != nil || notFound.Schema != "wipd.receipt-not-found/1" || notFound.CommandID != "01KZ7XHAQT1S46NYPN1PW1DX3G" {
		t.Fatalf("not-found payload = %+v, %v", notFound, err)
	}

	response, first := m5Submit(t, session, fixture, command)
	if response.Code != http.StatusOK || len(first) != 2 || first[0].Kind != "submission.accepted" || first[0].Sequence != 0 || first[1].Kind != "command.terminal" || first[1].Sequence != 1 {
		t.Fatalf("submit response status=%d frames=%+v", response.Code, first)
	}
	var accepted wipdwire.SubmissionAccepted
	if err := wipdwire.DecodeCanonical(first[0].Payload, &accepted, "schema", "domain_id", "authority_epoch", "command_id", "request_hash"); err != nil || accepted.Schema != "wipd.submission-accepted/1" || accepted.CommandID != command.ID || accepted.RequestHash != hash {
		t.Fatalf("accepted payload = %+v, %v", accepted, err)
	}
	terminal := append([]byte(nil), first[1].Payload...)
	receipt, err := wipdwire.DecodeCanonicalMap(terminal,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receipt["schema"] != "wipd.terminal-receipt/1" || receipt["command_id"] != command.ID {
		t.Fatalf("terminal receipt = %#v, %v", receipt, err)
	}
	events, ok := receipt["accepted_events"].(map[string]any)
	if !ok || events["event_count"] != uint64(1) || events["first_event_id"] != events["last_event_id"] {
		t.Fatalf("terminal accepted_events = %#v", receipt["accepted_events"])
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 1 || anchor.EventID != events["last_event_id"] || anchor.Digest == "" {
		t.Fatalf("persisted event prefix anchor = %+v, receipt range=%#v, err=%v", anchor, events, err)
	}
	if fixture.calls.Load() != 1 {
		t.Fatalf("M1 handler calls = %d, want 1", fixture.calls.Load())
	}

	_, replay := m5Submit(t, session, fixture, command)
	if len(replay) != 1 || replay[0].Kind != "command.terminal" || !bytes.Equal(replay[0].Payload, terminal) {
		t.Fatalf("same-ID/hash replay = %+v", replay)
	}
	conflict := m5Command(command.ID, 1, "different-locator")
	_, conflicting := m5Submit(t, session, fixture, conflict)
	if len(conflicting) != 1 || conflicting[0].Kind != "problem" || m5ProblemCode(t, conflicting[0]) != "command.id-conflict" {
		t.Fatalf("conflicting hash response = %+v", conflicting)
	}
	if fixture.calls.Load() != 1 {
		t.Fatalf("conflicting/replayed handler calls = %d", fixture.calls.Load())
	}
	queried := m5Query(t, session, fixture, command.ID, hash)
	if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, terminal) {
		t.Fatalf("terminal receipt query = %+v", queried)
	}

	pending := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3H", 2, "beta")
	if _, err := fixture.store.SubmitCommand(context.Background(), pending, m5CommandHash(t, pending), fixture.peer, fixture.now); err != nil {
		t.Fatal(err)
	}
	pendingFrames := m5Query(t, session, fixture, pending.ID, m5CommandHash(t, pending))
	if len(pendingFrames) != 1 || pendingFrames[0].Kind != "receipt.pending" {
		t.Fatalf("pending receipt query = %+v", pendingFrames)
	}
	var pendingAck wipdwire.SubmissionAccepted
	if err := wipdwire.DecodeCanonical(pendingFrames[0].Payload, &pendingAck, "schema", "domain_id", "authority_epoch", "command_id", "request_hash"); err != nil || pendingAck.CommandID != pending.ID {
		t.Fatalf("pending query payload = %+v, %v", pendingAck, err)
	}
	anchor, err = fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 1 || fixture.calls.Load() != 1 {
		t.Fatalf("state after replay/conflict/pending: anchor=%+v calls=%d err=%v", anchor, fixture.calls.Load(), err)
	}
}

func TestM5AuthenticatedMatterAndStepBirthReplayAcrossStoreReopen(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5SessionWithStep(t, fixture.handler, fixture.peer, true)

	matter := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "birth-matter")
	_, matterFrames := m5Submit(t, session, fixture, matter)
	if len(matterFrames) != 2 || matterFrames[0].Kind != "submission.accepted" || matterFrames[1].Kind != "command.terminal" {
		t.Fatalf("Matter birth response = %+v", matterFrames)
	}
	matterTerminal := append([]byte(nil), matterFrames[1].Payload...)
	matterReceipt, err := wipdwire.DecodeCanonicalMap(matterTerminal,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	matterEvents, ok := matterReceipt["accepted_events"].(map[string]any)
	if matterReceipt["command_id"] != matter.ID || !ok || matterEvents["event_count"] != uint64(1) || matterEvents["first_event_id"] != matterEvents["last_event_id"] {
		t.Fatalf("Matter birth receipt = %#v", matterReceipt)
	}

	step := m5StepCommand("01KZ7XHAQT1S46NYPN1PW1DX3G", 2, m5TestMatter, matter.ID, "First Step")
	_, stepFrames := m5Submit(t, session, fixture, step)
	if len(stepFrames) != 2 || stepFrames[0].Kind != "submission.accepted" || stepFrames[1].Kind != "command.terminal" {
		t.Fatalf("Step birth response = %+v", stepFrames)
	}
	stepTerminal := append([]byte(nil), stepFrames[1].Payload...)
	stepReceipt, err := wipdwire.DecodeCanonicalMap(stepTerminal,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := stepReceipt["result"].(map[string]any)
	if stepReceipt["command_id"] != step.ID || !ok || result["code"] != string(operation.ResultSucceeded) {
		t.Fatalf("Step terminal receipt = %#v", stepReceipt)
	}
	outputBytes, ok := result["output"].([]byte)
	if !ok {
		t.Fatalf("Step receipt output has type %T", result["output"])
	}
	output, err := wipdwire.DecodeCanonicalMap(outputBytes, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state")
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := output["id"].(string)
	stepEvents, ok := stepReceipt["accepted_events"].(map[string]any)
	if stepID == "" || output["parent_id"] != m5TestMatter || output["matter_id"] != m5TestMatter ||
		output["locator"] != "step-01" || output["title"] != "First Step" || output["sort_key"] != uint64(1000) || output["state"] != "planned" ||
		!ok || stepEvents["event_count"] != uint64(1) || stepEvents["first_event_id"] != stepEvents["last_event_id"] || stepEvents["first_event_id"] == matterEvents["first_event_id"] {
		t.Fatalf("Step output/range = output %#v, events %#v", output, stepReceipt["accepted_events"])
	}
	if fixture.calls.Load() != 2 {
		t.Fatalf("M1 handler calls after Matter and Step birth = %d, want 2", fixture.calls.Load())
	}

	_, replay := m5Submit(t, session, fixture, step)
	if len(replay) != 1 || replay[0].Kind != "command.terminal" || !bytes.Equal(replay[0].Payload, stepTerminal) {
		t.Fatalf("exact Step retry = %+v", replay)
	}
	conflict := m5StepCommand(step.ID, step.EnvironmentSequence, m5TestMatter, matter.ID, "Different title")
	_, conflicting := m5Submit(t, session, fixture, conflict)
	if len(conflicting) != 1 || conflicting[0].Kind != "problem" || m5ProblemCode(t, conflicting[0]) != "command.id-conflict" ||
		bytes.Contains(conflicting[0].Payload, []byte(stepID)) {
		t.Fatalf("conflicting Step retry disclosed the original outcome: %+v", conflicting)
	}
	if fixture.calls.Load() != 2 {
		t.Fatalf("replay/conflict reran handler; calls = %d", fixture.calls.Load())
	}
	for _, want := range []struct {
		id, hash string
		terminal []byte
	}{{matter.ID, m5CommandHash(t, matter), matterTerminal}, {step.ID, m5CommandHash(t, step), stepTerminal}} {
		queried := m5Query(t, session, fixture, want.id, want.hash)
		if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, want.terminal) {
			t.Fatalf("terminal query for %s = %+v", want.id, queried)
		}
	}

	oldStore := fixture.store
	if err = oldStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := authoritystore.OpenExisting(fixture.root)
	if err != nil {
		t.Fatalf("reopen authority after two births: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fixture.store = reopened
	fixture.server = fixture.serverForStore(t, reopened)
	fixture.handler = fixture.server.http.Handler
	session = m5SessionWithStep(t, fixture.handler, fixture.peer, true)
	for _, want := range []struct {
		id, hash string
		terminal []byte
	}{{matter.ID, m5CommandHash(t, matter), matterTerminal}, {step.ID, m5CommandHash(t, step), stepTerminal}} {
		queried := m5Query(t, session, fixture, want.id, want.hash)
		if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, want.terminal) {
			t.Fatalf("reopened terminal query for %s = %+v", want.id, queried)
		}
	}
	anchor, err := reopened.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 2 || anchor.EventID != stepEvents["last_event_id"] || fixture.calls.Load() != 2 {
		t.Fatalf("reopened birth state: anchor=%+v calls=%d err=%v", anchor, fixture.calls.Load(), err)
	}
	snapshotID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.PinSnapshot(context.Background(), m5TestDomain, 1, authoritystore.EmptyPrefixAnchor(), snapshotID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("pin reopened birth events: %v", err)
	}
	t.Cleanup(func() { _ = reopened.ReleaseSnapshot(context.Background(), m5TestDomain, 1, snapshotID) })
	if len(snapshot.Delta.Events) != 2 {
		t.Fatalf("reopened authority event delta has %d events", len(snapshot.Delta.Events))
	}
	wantEvents := []struct{ id, commandID, kind, subject string }{
		{matterEvents["first_event_id"].(string), matter.ID, "matter.created", m5TestMatter},
		{stepEvents["first_event_id"].(string), step.ID, "step.created", stepID},
	}
	for i, want := range wantEvents {
		eventID := snapshot.Delta.Events[i].EventID
		fields, decodeErr := wipdwire.DecodeCanonicalMap(snapshot.Delta.Events[i].Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil || eventID != want.id || fields["event_id"] != want.id || fields["command_id"] != want.commandID ||
			fields["kind"] != want.kind || fields["subject_id"] != want.subject {
			t.Fatalf("reopened event %d = %#v, err=%v", i, fields, decodeErr)
		}
	}
}

func TestM5BirthJournalReceiptBarrierLifecycleExchange(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5SessionWithStep(t, fixture.handler, fixture.peer, true)
	matter := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "birth-barrier")
	_, matterFrames := m5Submit(t, session, fixture, matter)
	if len(matterFrames) != 2 || matterFrames[1].Kind != "command.terminal" {
		t.Fatalf("Matter birth response=%+v", matterFrames)
	}
	step := m5StepCommand("01KZ7XHAQT1S46NYPN1PW1DX3G", 2, m5TestMatter, matter.ID, "Barrier step")
	_, stepFrames := m5Submit(t, session, fixture, step)
	if len(stepFrames) != 2 || stepFrames[1].Kind != "command.terminal" {
		t.Fatalf("Step birth response=%+v", stepFrames)
	}
	barrier := m5BirthJournalBarrier(t, m5TestMatter, matter.ID, m5CommandHash(t, matter), matterFrames[1].Payload,
		step.ID, m5CommandHash(t, step), stepFrames[1].Payload)

	// A terminal authority receipt is not a local-install acknowledgment. The
	// authority must refuse release until both exact receipts are ACKed.
	missingACKRaw, missingACKHash := m5BirthReleaseCommand(t, "01KZ7XHAQT1S46NYPN1PW1DX3H", 3, barrier)
	missingACK := m5Exchange(t, fixture, session, "claim.release", wipdwire.ClaimRelease{
		Schema: "wipd.claim-release/1", CanonicalCommand: missingACKRaw, RequestHash: missingACKHash, Barrier: barrier,
	})
	if len(missingACK) != 2 || missingACK[0].Kind != "submission.accepted" || missingACK[1].Kind != "command.terminal" ||
		m5ReceiptResultCode(t, missingACK[1].Payload) != operation.ResultRefused {
		t.Fatalf("release without install acknowledgments=%+v", missingACK)
	}

	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 2 {
		t.Fatalf("birth installed prefix=%+v err=%v", anchor, err)
	}
	for _, item := range []struct {
		id, hash string
		receipt  []byte
	}{{matter.ID, m5CommandHash(t, matter), matterFrames[1].Payload}, {step.ID, m5CommandHash(t, step), stepFrames[1].Payload}} {
		ack := wipdwire.BirthJournalAck{
			Schema: "wipd.birth-journal-ack/1", DomainID: m5TestDomain,
			Epoch: 1, MatterID: m5TestMatter, CommandID: item.id, RequestHash: item.hash,
			Receipt: item.receipt, Installed: m5WireAnchor(anchor),
		}
		frames := m5Exchange(t, fixture, session, "birth-journal.ack", ack)
		if len(frames) != 1 || frames[0].Kind != "birth-journal.acknowledged" {
			t.Fatalf("acknowledge installed %s receipt=%+v", item.id, frames)
		}
	}

	releaseID := "01KZ7XHAQT1S46NYPN1PW1DX3K"
	releaseRaw, releaseHash := m5BirthReleaseCommand(t, releaseID, 4, barrier)
	releasePayload := wipdwire.ClaimRelease{
		Schema: "wipd.claim-release/1", CanonicalCommand: releaseRaw, RequestHash: releaseHash, Barrier: barrier,
	}
	originalSigner := fixture.config.SignArtifact
	signerFailed := false
	fixture.config.SignArtifact = func(ctx context.Context, message []byte) ([]byte, error) {
		if !signerFailed {
			signerFailed = true
			return nil, errors.New("transient release signer failure")
		}
		return originalSigner(ctx, message)
	}
	fixture.server = fixture.serverForStore(t, fixture.store)
	fixture.handler = fixture.server.http.Handler
	firstAttempt := m5Exchange(t, fixture, session, "claim.release", releasePayload)
	if !signerFailed || len(firstAttempt) != 1 || firstAttempt[0].Kind != "submission.accepted" {
		t.Fatalf("transient signer failure did not preserve accepted/pending release: %+v", firstAttempt)
	}
	pendingQuery := m5Exchange(t, fixture, session, "receipt.query", wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: m5TestDomain, CommandID: releaseID, RequestHash: releaseHash,
	})
	if len(pendingQuery) != 1 || pendingQuery[0].Kind != "receipt.pending" {
		t.Fatalf("release signer failure fabricated terminal outcome: %+v", pendingQuery)
	}
	released := m5Exchange(t, fixture, session, "claim.release", releasePayload)
	if len(released) != 2 || released[0].Kind != "submission.accepted" || released[1].Kind != "command.terminal" ||
		m5ReceiptResultCode(t, released[1].Payload) != operation.ResultSucceeded {
		t.Fatalf("same-live-store release retry=%+v", released)
	}
	receipt, err := wipdwire.DecodeCanonicalMap(released[1].Payload,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receipt["command_id"] != releaseID {
		t.Fatalf("release receipt=%#v err=%v", receipt, err)
	}
	result := receipt["result"].(map[string]any)
	output, ok := result["output"].([]byte)
	if !ok {
		t.Fatalf("release output has type %T", result["output"])
	}
	outputFields, err := wipdwire.DecodeCanonicalMap(output, "claim_id", "claim_epoch", "dispatch_id", "barrier_digest")
	if err != nil || outputFields["claim_id"] != m5TestMatter || outputFields["claim_epoch"] != uint64(1) ||
		outputFields["dispatch_id"] != nil || outputFields["barrier_digest"] != barrier.Digest {
		t.Fatalf("dispatch-less release output=%#v err=%v", outputFields, err)
	}
	replay := m5Exchange(t, fixture, session, "claim.release", releasePayload)
	if len(replay) != 1 || replay[0].Kind != "command.terminal" || !bytes.Equal(replay[0].Payload, released[1].Payload) {
		t.Fatalf("same-ID release replay=%+v", replay)
	}
	anchor, err = fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 3 {
		t.Fatalf("released authority prefix=%+v err=%v", anchor, err)
	}
	query := wipdwire.ReceiptQuery{Schema: "wipd.receipt-query/1", DomainID: m5TestDomain, CommandID: releaseID, RequestHash: releaseHash}
	queried := m5Exchange(t, fixture, session, "receipt.query", query)
	if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, released[1].Payload) {
		t.Fatalf("release receipt query=%+v", queried)
	}

	fencedStep := m5StepCommand("01KZ7XHAQT1S46NYPN1PW1DX3J", 5, m5TestMatter, matter.ID, "after release")
	_, fenced := m5Submit(t, session, fixture, fencedStep)
	if len(fenced) != 1 || fenced[0].Kind != "problem" || m5ProblemCode(t, fenced[0]) != "auth.environment-domain-mismatch" {
		t.Fatalf("post-release Step was not authority-fenced: %+v", fenced)
	}
}

func m5BirthJournalBarrier(t *testing.T, matterID, matterCommandID, matterHash string, matterReceipt []byte,
	stepCommandID, stepHash string, stepReceipt []byte,
) wipdwire.JournalBarrier {
	t.Helper()
	readRange := func(raw []byte) *wipdwire.JournalBarrierRange {
		fields, err := wipdwire.DecodeCanonicalMap(raw,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if err != nil {
			t.Fatal(err)
		}
		accepted, ok := fields["accepted_events"].(map[string]any)
		if !ok {
			t.Fatalf("birth receipt lacks accepted range: %#v", fields["accepted_events"])
		}
		return &wipdwire.JournalBarrierRange{First: accepted["first_event_id"].(string), Last: accepted["last_event_id"].(string), Count: accepted["event_count"].(uint64)}
	}
	entries := []wipdwire.JournalBarrierEntry{
		{Position: 1, CommandID: matterCommandID, RequestHash: matterHash, ResultCode: string(operation.ResultSucceeded), Range: readRange(matterReceipt)},
		{Position: 2, CommandID: stepCommandID, RequestHash: stepHash, ResultCode: string(operation.ResultSucceeded), Range: readRange(stepReceipt)},
	}
	digest, err := wipdwire.JournalBarrierDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	return wipdwire.JournalBarrier{
		Schema: "wipd.journal-barrier/1", Journal: matterID,
		Claim: wipdwire.ClaimRef{ID: matterID, Epoch: 1}, Count: 2, Last: 2, Receipts: 2,
		Digest: digest, Sealed: true,
	}
}

func m5BirthReleaseCommand(t *testing.T, id string, sequence uint64, barrier wipdwire.JournalBarrier) ([]byte, string) {
	t.Helper()
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": id,
		"authority":   map[string]any{"domain_id": m5TestDomain, "expected_epoch": uint64(1)},
		"environment": map[string]any{"id": m5TestEnv, "sequence": sequence},
		"acted_at":    "2026-09-23T12:00:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": id,
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": m5TestRepo, "clone_id": nil, "worktree_id": nil},
		"claim":     map[string]any{"id": barrier.Claim.ID, "epoch": barrier.Claim.Epoch},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), raw...))
	return raw, "sha256:" + hex.EncodeToString(hash[:])
}

func m5WireAnchor(anchor authoritystore.PrefixAnchor) wipdwire.PrefixAnchor {
	result := wipdwire.PrefixAnchor{EventCount: anchor.EventCount, Digest: anchor.Digest}
	if anchor.EventCount != 0 {
		eventID := anchor.EventID
		result.EventID = &eventID
	}
	return result
}

func m5Exchange(t *testing.T, fixture *m5CommandFixture, session *labConnectionSession, kind string, payload any) []wipdwire.Frame {
	t.Helper()
	encoded, err := wipdwire.EncodeCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5Request(t, session, fixture.peer, kind, encoded, http.MethodPost, labExchangePath)
	fixture.handler.ServeHTTP(response, request)
	return m5ResponseFrames(t, response, 4)
}

func m5ReceiptResultCode(t *testing.T, raw []byte) operation.ResultCode {
	t.Helper()
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		t.Fatal(err)
	}
	result, ok := fields["result"].(map[string]any)
	if !ok {
		t.Fatalf("terminal receipt result=%#v", fields["result"])
	}
	code, ok := result["code"].(string)
	if !ok {
		t.Fatalf("terminal receipt code=%#v", result["code"])
	}
	return operation.ResultCode(code)
}

type m5SubmissionBarrierWriter struct {
	*httptest.ResponseRecorder
	store    *authoritystore.Store
	peer     tls.ConnectionState
	command  operation.Command
	hash     string
	checked  bool
	checkErr error
}

func (writer *m5SubmissionBarrierWriter) Write(data []byte) (int, error) {
	if !writer.checked {
		writer.checked = true
		frames, err := wipdwire.ReadFrames(data, 1)
		if err != nil || len(frames) != 1 || frames[0].Kind != "submission.accepted" {
			writer.checkErr = fmt.Errorf("first response write was not submission.accepted: frames=%+v err=%v", frames, err)
		} else {
			status, queryErr := writer.store.QueryCommand(context.Background(), m5TestDomain, writer.command.ID,
				writer.hash, 1, writer.peer, m5TestEnv, time.Now().UTC())
			if queryErr != nil || !status.Pending || len(status.Receipt) != 0 {
				writer.checkErr = fmt.Errorf("submission was not durably pending at acknowledgment: status=%+v err=%v", status, queryErr)
			}
		}
	}
	return writer.ResponseRecorder.Write(data)
}

func TestM5SubmissionAcceptedIsWrittenAfterDurableCommit(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5Session(t, fixture.handler, fixture.peer)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5CommandRequest(t, session, fixture.peer, "command.submit", wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: m5CommandHash(t, command),
	})
	writer := &m5SubmissionBarrierWriter{
		ResponseRecorder: response, store: fixture.store, peer: fixture.peer,
		command: command, hash: m5CommandHash(t, command),
	}
	fixture.handler.ServeHTTP(writer, request)
	if !writer.checked || writer.checkErr != nil {
		t.Fatalf("submission acknowledgment barrier: checked=%v err=%v", writer.checked, writer.checkErr)
	}
	frames := m5ResponseFrames(t, response, 2)
	if len(frames) != 2 || frames[0].Kind != "submission.accepted" || frames[1].Kind != "command.terminal" {
		t.Fatalf("submission response = %+v", frames)
	}
}

func TestM5PendingExactRetryRecoversAfterStoreReopen(t *testing.T) {
	fixture := newM5CommandFixture(t)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	hash := m5CommandHash(t, command)
	if _, err := fixture.store.SubmitCommand(context.Background(), command, hash, fixture.peer, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := authoritystore.OpenExisting(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	app := fixture.handlerForStore(t, reopened)
	session := m5Session(t, app, fixture.peer)
	conflict := m5Command(command.ID, 1, "not-the-retained-bytes")
	_, conflictFrames := m5SubmitWithApp(t, app, session, fixture.peer, conflict)
	if len(conflictFrames) != 1 || conflictFrames[0].Kind != "problem" || m5ProblemCode(t, conflictFrames[0]) != "command.id-conflict" {
		t.Fatalf("post-reopen conflicting attempt = %+v", conflictFrames)
	}
	_, recovered := m5SubmitWithApp(t, app, session, fixture.peer, command)
	if len(recovered) != 2 || recovered[0].Kind != "submission.accepted" || recovered[1].Kind != "command.terminal" {
		t.Fatalf("exact pending retry after reopen = %+v", recovered)
	}
	if fixture.calls.Load() != 1 {
		t.Fatalf("recovered handler calls = %d, want 1", fixture.calls.Load())
	}
	status, err := reopened.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("recovered command status = %+v, %v", status, err)
	}
}

func m5SubmitWithApp(t *testing.T, app http.Handler, session *labConnectionSession, peer tls.ConnectionState, command operation.Command) (*httptest.ResponseRecorder, []wipdwire.Frame) {
	t.Helper()
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5CommandRequest(t, session, peer, "command.submit", wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: m5CommandHash(t, command),
	})
	app.ServeHTTP(response, request)
	return response, m5ResponseFrames(t, response, 4)
}

type m5LostTerminalWriter struct {
	header http.Header
	status int
	writes int
	body   bytes.Buffer
	cancel context.CancelFunc
}

func (writer *m5LostTerminalWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (writer *m5LostTerminalWriter) WriteHeader(status int) { writer.status = status }

func (writer *m5LostTerminalWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == 1 {
		written, err := writer.body.Write(value)
		if writer.cancel != nil {
			writer.cancel()
		}
		return written, err
	}
	return len(value), nil
}

func TestM5LostTerminalResponseReplaysStoredReceipt(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5Session(t, fixture.handler, fixture.peer)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: m5CommandHash(t, command),
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: "command.submit", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, labExchangePath, bytes.NewReader(requestBytes)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/cbor")
	request.TLS = &fixture.peer
	request = request.WithContext(context.WithValue(request.Context(), labSessionContextKey{}, session))
	lost := &m5LostTerminalWriter{cancel: cancel}
	fixture.handler.ServeHTTP(lost, request)
	first, err := wipdwire.ReadFrames(lost.body.Bytes(), 2)
	if err != nil || len(first) != 1 || first[0].Kind != "submission.accepted" || requestContext.Err() != context.Canceled {
		t.Fatalf("simulated lost terminal response frames=%+v calls=%d err=%v", first, fixture.calls.Load(), err)
	}
	waitM5Terminal(t, fixture.store, fixture.peer, command)
	if fixture.calls.Load() != 1 {
		t.Fatalf("lost response execution calls=%d, want 1", fixture.calls.Load())
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := authoritystore.OpenExisting(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fixture.store = reopened
	fixture.handler = fixture.handlerForStore(t, reopened)
	session = m5Session(t, fixture.handler, fixture.peer)
	_, retry := m5Submit(t, session, fixture, command)
	if len(retry) != 1 || retry[0].Kind != "command.terminal" || fixture.calls.Load() != 1 {
		t.Fatalf("terminal reconnect retry=%+v calls=%d", retry, fixture.calls.Load())
	}
	if !bytes.Equal(retry[0].Payload, fixtureStoredReceipt(t, fixture.store, fixture.peer, command)) {
		t.Fatal("retry did not return the exact durable receipt")
	}
}

func TestM5SameLiveStoreRetryContinuesAfterTransientSignerFailure(t *testing.T) {
	fixture := newM5CommandFixture(t)
	var signerCalls atomic.Int32
	baseSigner := fixture.config.SignArtifact
	fixture.config.SignArtifact = func(ctx context.Context, preimage []byte) ([]byte, error) {
		if signerCalls.Add(1) == 1 {
			return nil, errors.New("injected transient signer failure")
		}
		return baseSigner(ctx, preimage)
	}
	fixture.handler = fixture.handlerForStore(t, fixture.store)
	session := m5Session(t, fixture.handler, fixture.peer)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	hash := m5CommandHash(t, command)

	_, first := m5Submit(t, session, fixture, command)
	if len(first) != 1 || first[0].Kind != "submission.accepted" || fixture.calls.Load() != 1 {
		t.Fatalf("first attempt after signer failure = %+v; handler calls=%d", first, fixture.calls.Load())
	}
	pending := m5Query(t, session, fixture, command.ID, hash)
	if len(pending) != 1 || pending[0].Kind != "receipt.pending" {
		t.Fatalf("receipt after transient signer failure = %+v", pending)
	}
	status, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, hash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || !status.Pending || len(status.Receipt) != 0 {
		t.Fatalf("same-store status after transient signer failure = %+v, %v", status, err)
	}

	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5CommandRequest(t, session, fixture.peer, "command.submit", wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: canonical, RequestHash: hash,
	})
	flushEntered := make(chan struct{})
	releaseFlush := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFlush) }) }
	t.Cleanup(release)
	barrier := &m5FlushBarrierWriter{ResponseRecorder: response, entered: flushEntered, release: releaseFlush}
	retryDone := make(chan struct{}, 1)
	go func() {
		fixture.handler.ServeHTTP(barrier, request)
		retryDone <- struct{}{}
	}()
	select {
	case <-flushEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("exact retry did not claim the retained completion before acknowledgment")
	}
	_, concurrentRetry := m5Submit(t, session, fixture, command)
	if len(concurrentRetry) != 1 || concurrentRetry[0].Kind != "submission.accepted" {
		t.Fatalf("concurrent exact retry created another completion owner: %+v", concurrentRetry)
	}
	if fixture.calls.Load() != 1 || signerCalls.Load() != 1 {
		t.Fatalf("concurrent retry dispatched before continuation release: handlers=%d signers=%d", fixture.calls.Load(), signerCalls.Load())
	}
	release()
	select {
	case <-retryDone:
	case <-time.After(3 * time.Second):
		t.Fatal("claimed completion did not finish after releasing the acknowledgment barrier")
	}
	retried := m5ResponseFrames(t, response, 4)
	if len(retried) != 2 || retried[0].Kind != "submission.accepted" || retried[1].Kind != "command.terminal" {
		t.Fatalf("same-live-store exact retry = %+v", retried)
	}
	if fixture.calls.Load() != 1 || signerCalls.Load() != 2 {
		t.Fatalf("retry repeated semantic execution or skipped signer retry: handler calls=%d signer calls=%d", fixture.calls.Load(), signerCalls.Load())
	}
	queried := m5Query(t, session, fixture, command.ID, hash)
	if len(queried) != 1 || queried[0].Kind != "command.terminal" || !bytes.Equal(queried[0].Payload, retried[1].Payload) {
		t.Fatalf("same-store terminal receipt query = %+v", queried)
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || anchor.EventCount != 1 {
		t.Fatalf("fold count after same-store retry = %+v, %v", anchor, err)
	}
}

type m5FlushBarrierWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
}

func (writer *m5FlushBarrierWriter) Flush() {
	close(writer.entered)
	<-writer.release
	writer.ResponseRecorder.Flush()
}

func waitM5Terminal(t *testing.T, store *authoritystore.Store, peer tls.ConnectionState, command operation.Command) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := store.QueryCommand(context.Background(), m5TestDomain, command.ID,
			m5CommandHash(t, command), 1, peer, m5TestEnv, time.Now().UTC())
		if err == nil && !status.Pending && len(status.Receipt) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("command did not reach a terminal receipt")
}

func fixtureStoredReceipt(t *testing.T, store *authoritystore.Store, peer tls.ConnectionState, command operation.Command) []byte {
	t.Helper()
	status, err := store.QueryCommand(context.Background(), m5TestDomain, command.ID, m5CommandHash(t, command), 1, peer, m5TestEnv, time.Now().UTC())
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("stored receipt lookup = %+v, %v", status, err)
	}
	return status.Receipt
}

func TestM5RecoveryAndReceiptFailuresAreNotM1Results(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5Session(t, fixture.handler, fixture.peer)
	bad := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	badBytes, err := bad.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: badBytes, RequestHash: "sha256:" + string(bytes.Repeat([]byte{'0'}, 64)),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5Request(t, session, fixture.peer, "command.submit", payload, http.MethodPost, labExchangePath)
	fixture.handler.ServeHTTP(response, request)
	frames := m5ResponseFrames(t, response, 2)
	if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "protocol.malformed-message" || fixture.calls.Load() != 0 {
		t.Fatalf("bad asserted hash response = %+v, calls=%d", frames, fixture.calls.Load())
	}
	queryFrames := m5Query(t, session, fixture, bad.ID, m5CommandHash(t, bad))
	if len(queryFrames) != 1 || queryFrames[0].Kind != "receipt.not-found" {
		t.Fatalf("bad hash did not create a submission: %+v", queryFrames)
	}
}

func TestM5UnauthenticatedCommandCannotReachM1OrSubmission(t *testing.T) {
	fixture := newM5CommandFixture(t)
	session := m5Session(t, fixture.handler, fixture.peer)
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "alpha")
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: raw, RequestHash: m5CommandHash(t, command),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, request := m5Request(t, session, tls.ConnectionState{}, "command.submit", payload, http.MethodPost, labExchangePath)
	fixture.handler.ServeHTTP(response, request)
	frames := m5ResponseFrames(t, response, 1)
	if len(frames) != 1 || frames[0].Kind != "problem" || fixture.calls.Load() != 0 {
		t.Fatalf("unauthenticated command response = %+v, M1 calls=%d", frames, fixture.calls.Load())
	}
	if _, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, m5CommandHash(t, command), 1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("unauthenticated request created a durable submission")
	}
}
