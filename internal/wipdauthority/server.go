package wipdauthority

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

const healthPath = "/healthz"

// ErrInvalidListener means a server, context, or listener is required.
var ErrInvalidListener = errors.New("wipdauthority: server and listener are required")

// Server exposes only the stateless M2 process-readiness endpoint. It has no
// authority-store dependency and does not create or bootstrap domain state.
type Server struct {
	profile Profile
	http    *http.Server
}

// NewServer binds the listener identity to an explicit profile and its
// authority certificate. Application endpoints remain unavailable at this
// step; health reports only that this process is serving requests.
func NewServer(profile Profile, certificate tls.Certificate) (*Server, error) {
	return newServer(profile, certificate, http.HandlerFunc(serveHealth), false)
}

func newServer(profile Profile, certificate tls.Certificate, handler http.Handler, requestClientCertificate bool) (*Server, error) {
	if err := profile.validate(); err != nil {
		return nil, err
	}
	if len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return nil, ErrAuthorityBindingMismatch
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.Join(ErrAuthorityBindingMismatch, err)
	}
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, ErrAuthorityBindingMismatch
	}
	publicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicKey, leaf.RawSubjectPublicKeyInfo) {
		return nil, ErrAuthorityBindingMismatch
	}
	if err := verifyAuthorityCertificate(profile, leaf, time.Now()); err != nil {
		return nil, err
	}
	certificate.Leaf = leaf

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2"},
		ClientAuth:   tls.NoClientCert,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			for _, protocol := range hello.SupportedProtos {
				if protocol == "h2" {
					return nil, nil
				}
			}
			return nil, ErrHTTP2Required
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.NegotiatedProtocol != "h2" {
				return ErrHTTP2Required
			}
			return nil
		},
	}
	if requestClientCertificate {
		tlsConfig.ClientAuth = tls.RequestClientCert
	}
	httpServer := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if err := http2.ConfigureServer(httpServer, &http2.Server{IdleTimeout: 60 * time.Second}); err != nil {
		return nil, err
	}
	// ConfigureServer also enables HTTP/1.1 for general-purpose servers. This
	// boundary is h2-only, so keep ALPN exact; VerifyConnection rejects peers
	// that do not negotiate h2 before net/http can parse their request.
	httpServer.TLSConfig.NextProtos = []string{"h2"}
	return &Server{profile: profile, http: httpServer}, nil
}

// ListenAndServe binds TCP only to the exact host and port in the explicit
// HTTPS origin. It does not discover a default listener address.
func (server *Server) ListenAndServe(ctx context.Context) error {
	if server == nil || server.http == nil {
		return ErrInvalidListener
	}
	listener, err := net.Listen("tcp", server.profile.address)
	if err != nil {
		return err
	}
	return server.Serve(ctx, listener)
}

// Serve serves on a caller-owned listener with TLS 1.3 and h2 only. Canceling
// ctx stops accepting new requests and drains active health responses.
func (server *Server) Serve(ctx context.Context, listener net.Listener) error {
	if server == nil || server.http == nil || listener == nil || ctx == nil {
		return ErrInvalidListener
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.http.Serve(tls.NewListener(listener, server.http.TLSConfig))
	}()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.http.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.http.Close()
		}
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

func serveHealth(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != healthPath {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery || request.ContentLength != 0 ||
		len(request.TransferEncoding) != 0 || hasHeader(request.Header, "Authorization", "Cookie", "Content-Encoding", "Accept-Encoding") {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func hasHeader(header http.Header, names ...string) bool {
	for name := range header {
		for _, expected := range names {
			if strings.EqualFold(name, expected) {
				return true
			}
		}
	}
	return false
}
