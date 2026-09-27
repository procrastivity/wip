package wipdauthority

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

const testDomainID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

const testOtherDomainID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"

const testOwnerDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type authorityTLSFixture struct {
	profile Profile
	cert    tls.Certificate
	roots   *x509.CertPool
}

func newAuthorityTLSFixture(t *testing.T, listener net.Listener, domain string, epoch uint64, owner string) authorityTLSFixture {
	t.Helper()

	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return newAuthorityTLSFixtureWithHosts(t, listener, host, host, domain, epoch, owner)
}

func newAuthorityTLSFixtureWithHosts(t *testing.T, listener net.Listener, originHost, certificateHost, domain string, epoch uint64, owner string) authorityTLSFixture {
	t.Helper()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	origin := "https://" + net.JoinHostPort(originHost, port)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&leafKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pinDigest := sha256.Sum256(spki)
	pin := "sha256:" + hex.EncodeToString(pinDigest[:])
	ownerHex := strings.TrimPrefix(owner, "sha256:")
	uri := fmt.Sprintf("wipd://authority/%s?epoch=%d&owner=%s", domain, epoch, ownerHex)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test authority root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	san, err := authoritySAN(certificateHost, uri)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test authority"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(30 * time.Minute),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		ExtraExtensions: []pkix.Extension{{
			Id:       subjectAlternativeNameOID,
			Critical: true,
			Value:    san,
		}},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	profile, err := NewProfile(origin, domain, epoch, pin, owner)
	if err != nil {
		t.Fatal(err)
	}
	return authorityTLSFixture{
		profile: profile,
		cert: tls.Certificate{
			Certificate: [][]byte{leafDER, caDER},
			PrivateKey:  leafKey,
			Leaf:        leaf,
		},
		roots: roots,
	}
}

func authoritySAN(host, uri string) ([]byte, error) {
	name := asn1.RawValue{Class: 2, Tag: 6, Bytes: []byte(uri)}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			ip = ipv4
		}
		name = asn1.RawValue{Class: 2, Tag: 7, Bytes: ip}
		return asn1.Marshal([]asn1.RawValue{name, {Class: 2, Tag: 6, Bytes: []byte(uri)}})
	}
	return asn1.Marshal([]asn1.RawValue{{Class: 2, Tag: 2, Bytes: []byte(host)}, name})
}

func startCountingHTTP2Authority(t *testing.T, listener net.Listener, certificate tls.Certificate) *atomic.Int32 {
	t.Helper()
	var requestCount atomic.Int32
	redirectTarget := "https://" + listener.Addr().String() + healthPath
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.NegotiatedProtocol != "h2" {
				return ErrHTTP2Required
			}
			return nil
		},
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if request.URL.Path == "/redirect" {
			writer.Header().Set("Location", redirectTarget)
			writer.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})}
	if err := http2.ConfigureServer(server, &http2.Server{}); err != nil {
		t.Fatal(err)
	}
	server.TLSConfig = tlsConfig
	server.TLSConfig.NextProtos = []string{"h2"}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("test HTTPS server: %v", err)
		}
	})
	return &requestCount
}

func TestNewProfileRequiresExplicitM2Values(t *testing.T) {
	validPin := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name, origin, domain, pin, owner string
		epoch                            uint64
	}{
		{name: "missing explicit port", origin: "https://authority.example", domain: testDomainID, epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "path", origin: "https://authority.example:443/", domain: testDomainID, epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "query", origin: "https://authority.example:443?x=1", domain: testDomainID, epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "empty fragment marker", origin: "https://authority.example:443#", domain: testDomainID, epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "user info", origin: "https://user@authority.example:443", domain: testDomainID, epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "invalid domain", origin: "https://authority.example:443", domain: "01M4FIXTURE0000000000000001", epoch: 1, pin: validPin, owner: testOwnerDigest},
		{name: "zero epoch", origin: "https://authority.example:443", domain: testDomainID, epoch: 0, pin: validPin, owner: testOwnerDigest},
		{name: "noncanonical pin", origin: "https://authority.example:443", domain: testDomainID, epoch: 1, pin: "SHA256:" + strings.Repeat("A", 64), owner: testOwnerDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewProfile(test.origin, test.domain, test.epoch, test.pin, test.owner); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("NewProfile() error = %v, want ErrInvalidProfile", err)
			}
		})
	}
}

func TestPinnedHTTPClientRejectsWrongPinAndAuthorityBindingBeforeHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 7, testOwnerDigest)
	requests := startCountingHTTP2Authority(t, listener, fixture.cert)

	goodClient, err := fixture.profile.HTTPClient(fixture.roots)
	if err != nil {
		t.Fatal(err)
	}
	defer goodClient.CloseIdleConnections()
	response, err := goodClient.Get(fixture.profile.HealthURL())
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 {
		_ = response.Body.Close()
		t.Fatalf("negotiated protocol = %s TLS=%v; want HTTP/2 over TLS 1.3", response.Proto, response.TLS)
	}
	_ = response.Body.Close()
	if got := requests.Load(); got != 1 {
		t.Fatalf("accepted request count = %d, want 1", got)
	}

	wrongPin, err := NewProfile(fixture.profile.Origin(), testDomainID, 7,
		"sha256:"+strings.Repeat("b", 64), testOwnerDigest)
	if err != nil {
		t.Fatal(err)
	}
	wrongEpoch, err := NewProfile(fixture.profile.Origin(), testDomainID, 8,
		profilePin(t, fixture.cert), testOwnerDigest)
	if err != nil {
		t.Fatal(err)
	}
	wrongOwner, err := NewProfile(fixture.profile.Origin(), testDomainID, 7,
		profilePin(t, fixture.cert), "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	wrongDomain, err := NewProfile(fixture.profile.Origin(), testOtherDomainID, 7,
		profilePin(t, fixture.cert), testOwnerDigest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		profile Profile
		wantErr error
	}{
		{name: "wrong pin", profile: wrongPin, wantErr: ErrAuthorityPinMismatch},
		{name: "wrong domain binding", profile: wrongDomain, wantErr: ErrAuthorityBindingMismatch},
		{name: "wrong epoch binding", profile: wrongEpoch, wantErr: ErrAuthorityBindingMismatch},
		{name: "wrong owner binding", profile: wrongOwner, wantErr: ErrAuthorityBindingMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := test.profile.HTTPClient(fixture.roots)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			response, err := client.Get(test.profile.HealthURL())
			if response != nil {
				_ = response.Body.Close()
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("HTTP request error = %v, want %v", err, test.wantErr)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("server handler ran after rejected TLS identity: requests = %d, want 1", got)
			}
		})
	}
	untrustedClient, err := fixture.profile.HTTPClient(x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	defer untrustedClient.CloseIdleConnections()
	if response, err := untrustedClient.Get(fixture.profile.HealthURL()); err == nil {
		_ = response.Body.Close()
		t.Fatal("untrusted certificate chain was accepted")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server handler ran after chain validation failed: requests = %d, want 1", got)
	}
}

func TestPinnedHTTPClientRetainsHostnameVerification(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAuthorityTLSFixtureWithHosts(t, listener, "127.0.0.1", "wrong.example", testDomainID, 7, testOwnerDigest)
	requests := startCountingHTTP2Authority(t, listener, fixture.cert)
	client, err := fixture.profile.HTTPClient(fixture.roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if response, err := client.Get("https://127.0.0.1:" + port + healthPath); err == nil {
		_ = response.Body.Close()
		t.Fatal("certificate with a mismatched hostname SAN was accepted")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server handler ran after hostname validation failed: requests = %d, want 0", got)
	}
}

func TestPinnedHTTPClientRejectsAlternateOriginHeadersAndRedirects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 7, testOwnerDigest)
	requests := startCountingHTTP2Authority(t, listener, fixture.cert)
	client, err := fixture.profile.HTTPClient(fixture.roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	request, err := http.NewRequest(http.MethodGet, "https://elsewhere.example/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("alternate-origin request error = %v, want ErrOriginMismatch", err)
	}
	request, err = http.NewRequest(http.MethodGet, fixture.profile.HealthURL()+"?domain=private", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); !errors.Is(err, ErrOriginMismatch) {
		t.Fatalf("query request error = %v, want ErrOriginMismatch", err)
	}
	request, err = http.NewRequest(http.MethodGet, fixture.profile.HealthURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header["authorization"] = []string{""}
	if _, err := client.Do(request); !errors.Is(err, ErrForbiddenHTTPField) {
		t.Fatalf("authorization request error = %v, want ErrForbiddenHTTPField", err)
	}

	response, err := client.Get(strings.TrimSuffix(fixture.profile.HealthURL(), healthPath) + "/redirect")
	if !errors.Is(err, ErrRedirectForbidden) {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("redirect request error = %v, want ErrRedirectForbidden", err)
	}
	if response != nil {
		_ = response.Body.Close()
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("only the redirect endpoint should reach the handler, got %d requests", got)
	}
}

func profilePin(t *testing.T, certificate tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestServerStopsAfterContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 1, testOwnerDigest)
	server, err := NewServer(fixture.profile, fixture.cert)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(ctx, listener) }()
	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Serve after cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}
