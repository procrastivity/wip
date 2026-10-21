package wipdauthority

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/d115"
	"golang.org/x/net/http2"
)

// D115CreateDomainPath is the isolated pre-domain provisioning submit endpoint.
const D115CreateDomainPath = "/wipd/v1/create-domain"

// NewM6D115ProvisioningLabServer exposes only the isolated pre-domain create
// endpoint. Its TLS SAN and SPKI bind the endpoint to the store's immutable
// provisioning realm; ordinary M2 and D117 paths are not mounted here.
func NewM6D115ProvisioningLabServer(origin string, certificate tls.Certificate, store *authoritystore.Store) (*Server, error) {
	if store == nil || len(certificate.Certificate) == 0 || certificate.PrivateKey == nil {
		return nil, ErrInvalidLabConfig
	}
	trust, err := store.D115Trust(context.Background())
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || !bytes.Equal(leaf.Raw, certificate.Certificate[0]) {
		return nil, ErrAuthorityBindingMismatch
	}
	key, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, ErrAuthorityBindingMismatch
	}
	publicKey, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(publicKey, leaf.RawSubjectPublicKeyInfo) {
		return nil, ErrAuthorityBindingMismatch
	}
	digest := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	pin := "sha256:" + hex.EncodeToString(digest[:])
	profile, err := NewProvisioningProfile(origin, trust.RealmID, pin)
	if err != nil || verifyProvisioningCertificate(profile, leaf, time.Now()) != nil {
		return nil, ErrAuthorityBindingMismatch
	}
	certificate.Leaf = leaf
	mux := http.NewServeMux()
	mux.HandleFunc(D115CreateDomainPath, func(writer http.ResponseWriter, request *http.Request) {
		serveD115CreateDomain(writer, request, store)
	})
	mux.HandleFunc("/healthz", serveHealth)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"h2"}, ClientAuth: tls.NoClientCert,
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
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	if err = http2.ConfigureServer(httpServer, &http2.Server{IdleTimeout: 60 * time.Second}); err != nil {
		return nil, err
	}
	httpServer.TLSConfig.NextProtos = []string{"h2"}
	return &Server{listenAddress: profile.address, http: httpServer}, nil
}

func serveD115CreateDomain(writer http.ResponseWriter, request *http.Request, store *authoritystore.Store) {
	if request.URL.Path != D115CreateDomainPath || request.Method != http.MethodPost || request.URL.RawQuery != "" || request.URL.ForceQuery ||
		request.ContentLength < 1 || request.ContentLength > 8192 || len(request.TransferEncoding) != 0 ||
		request.Header.Get("Content-Type") != "application/cbor" || request.Header.Get("Content-Encoding") != "" ||
		request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 8192))
	if err != nil || int64(len(body)) != request.ContentLength {
		http.Error(writer, "protocol.malformed-message", http.StatusBadRequest)
		return
	}
	receipt, err := store.SubmitCreateDomain(request.Context(), body)
	clear(body)
	if err != nil {
		status, code := d115HTTPProblem(err)
		http.Error(writer, code, status)
		return
	}
	if _, err = d115.DecodeReceipt(receipt); err != nil {
		http.Error(writer, "authority.unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(receipt)
}

func d115HTTPProblem(err error) (int, string) {
	switch {
	case errors.Is(err, authoritystore.ErrD115Conflict):
		return http.StatusConflict, "command.id-conflict"
	case errors.Is(err, authoritystore.ErrD115GrantConsumed):
		return http.StatusConflict, "auth.create-domain-grant-consumed"
	case errors.Is(err, authoritystore.ErrD115Refused):
		return http.StatusForbidden, "auth.create-domain-refused"
	default:
		return http.StatusServiceUnavailable, "authority.unavailable"
	}
}
