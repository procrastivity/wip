package wipdauthority

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerHealthIsStatelessAndOnlyExposesProcessReadiness(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 12, testOwnerDigest)
	server, err := NewServer(fixture.profile, fixture.cert)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(ctx, listener) }()
	client, err := fixture.profile.HTTPClient(fixture.roots)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case err := <-serveResult:
			if err != nil {
				t.Errorf("server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop after context cancellation")
		}
	})

	response, err := client.Get(fixture.profile.HealthURL())
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close health response: read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("health response = %d %q, want empty 204", response.StatusCode, body)
	}
	if response.ProtoMajor != 2 || response.TLS == nil || response.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("health protocol = %s TLS=%v, want HTTP/2 over TLS 1.3", response.Proto, response.TLS)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("health Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
	for name := range response.Header {
		lower := strings.ToLower(name)
		for _, forbidden := range []string{"owner", "environment", "grant", "claim", "receipt", "high-water", "high_water"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("health disclosed forbidden response header %q", name)
			}
		}
	}

	for _, path := range []string{"/wipd/v1/enroll", "/wipd/v1/renew", "/wipd/v1/negotiate", "/wipd/v1/exchange", "/"} {
		response, err := client.Get(fixture.profile.Origin() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, response.StatusCode)
		}
	}

	request, err := http.NewRequest(http.MethodPost, fixture.profile.HealthURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("POST health response = %d Allow=%q, want 405 and GET", response.StatusCode, response.Header.Get("Allow"))
	}
}

func TestNewServerRejectsCertificateNotBoundToExplicitProfile(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 12, testOwnerDigest)
	wrongProfile, err := NewProfile(fixture.profile.Origin(), testDomainID, 13,
		profilePin(t, fixture.cert), testOwnerDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(wrongProfile, fixture.cert); !errors.Is(err, ErrAuthorityBindingMismatch) {
		t.Fatalf("NewServer() error = %v, want ErrAuthorityBindingMismatch", err)
	}
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongCertificate := fixture.cert
	wrongCertificate.PrivateKey = wrongKey
	if _, err := NewServer(fixture.profile, wrongCertificate); !errors.Is(err, ErrAuthorityBindingMismatch) {
		t.Fatalf("NewServer() with mismatched private key error = %v, want ErrAuthorityBindingMismatch", err)
	}
}

func TestServerRejectsTLSWithoutHTTP2ALPN(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	fixture := newAuthorityTLSFixture(t, listener, testDomainID, 12, testOwnerDigest)
	server, err := NewServer(fixture.profile, fixture.cert)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveResult:
			if err != nil {
				t.Errorf("server stopped with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop after context cancellation")
		}
	})

	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
		RootCAs:    fixture.roots,
		ServerName: fixture.profile.host,
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
	})
	if err == nil {
		_ = conn.Close()
		t.Fatal("TLS handshake succeeded without h2 ALPN")
	}
}
