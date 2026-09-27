// Package wipdauthority implements explicit M2 authority profiles and the
// pinned HTTPS process-readiness boundary without opening authority state.
package wipdauthority

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
)

var (
	// ErrInvalidProfile means required explicit profile values are malformed.
	ErrInvalidProfile = errors.New("wipdauthority: invalid explicit profile")
	// ErrAuthorityPinMismatch means the peer certificate SPKI differs from the configured pin.
	ErrAuthorityPinMismatch = errors.New("wipdauthority: authority SPKI pin mismatch")
	// ErrAuthorityBindingMismatch means the authority certificate does not match the configured identity.
	ErrAuthorityBindingMismatch = errors.New("wipdauthority: authority certificate binding mismatch")
	// ErrHTTP2Required means TLS did not negotiate HTTP/2 via ALPN.
	ErrHTTP2Required = errors.New("wipdauthority: HTTP/2 ALPN is required")
	// ErrOriginMismatch means a request targets anything other than the explicit HTTPS origin.
	ErrOriginMismatch = errors.New("wipdauthority: request origin differs from explicit profile")
	// ErrRedirectForbidden means an HTTP redirect response was received.
	ErrRedirectForbidden = errors.New("wipdauthority: redirects are forbidden")
	// ErrForbiddenHTTPField means a request contains an M2-forbidden HTTP field.
	ErrForbiddenHTTPField = errors.New("wipdauthority: forbidden HTTP field")

	canonicalDomainIDPattern     = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	canonicalSPKIDigestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	subjectAlternativeNameOID    = []int{2, 5, 29, 17}
	serverAuthenticationExtended = x509.ExtKeyUsageServerAuth
)

// Profile is the explicit M2 remote authority profile. It is a typed runtime
// value; M2 does not define a profile-file serialization or a wire encoding.
type Profile struct {
	origin        string
	host          string
	address       string
	domainID      string
	epoch         uint64
	authoritySPKI [sha256.Size]byte
	ownerRootSPKI [sha256.Size]byte
}

// NewProfile requires every M2 trust value. Pins use the repository's
// canonical sha256:<lowercase-hex> digest spelling; no trust-on-first-use or
// default origin is provided.
func NewProfile(origin, domainID string, epoch uint64, authoritySPKIPin, ownerRootSPKI string) (Profile, error) {
	var profile Profile
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(origin, "#") {
		return profile, fmt.Errorf("%w: origin must be an HTTPS origin with no path, query, fragment, or user info", ErrInvalidProfile)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if host == "" || portErr != nil || portNumber == 0 || strconv.FormatUint(portNumber, 10) != port {
		return profile, fmt.Errorf("%w: origin requires an exact host and canonical nonzero port", ErrInvalidProfile)
	}
	if !canonicalDomainIDPattern.MatchString(domainID) || epoch == 0 {
		return profile, fmt.Errorf("%w: domain ID must be a canonical ULID and authority epoch must be positive", ErrInvalidProfile)
	}
	pin, err := parseSPKIDigest(authoritySPKIPin)
	if err != nil {
		return profile, fmt.Errorf("%w: authority SPKI pin: %v", ErrInvalidProfile, err)
	}
	owner, err := parseSPKIDigest(ownerRootSPKI)
	if err != nil {
		return profile, fmt.Errorf("%w: owner-root SPKI digest: %v", ErrInvalidProfile, err)
	}
	return Profile{
		origin:        origin,
		host:          host,
		address:       parsed.Host,
		domainID:      domainID,
		epoch:         epoch,
		authoritySPKI: pin,
		ownerRootSPKI: owner,
	}, nil
}

// Origin returns the exact configured HTTPS origin.
func (profile Profile) Origin() string { return profile.origin }

// HealthURL returns the stateless process-readiness endpoint for this origin.
func (profile Profile) HealthURL() string { return profile.origin + "/healthz" }

// HTTPClient creates a client restricted to this exact HTTPS origin. It
// preserves Go's normal certificate-chain, validity, EKU, and hostname
// verification, then additionally enforces TLS 1.3, h2, the exact configured
// SPKI pin, and the canonical authority binding URI SAN. It does not follow
// redirects or permit alternate origins, credentials, queries, or compression.
func (profile Profile) HTTPClient(roots *x509.CertPool) (*http.Client, error) {
	if err := profile.validate(); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		ServerName: profile.host,
		RootCAs:    roots,
		NextProtos: []string{"h2"},
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.NegotiatedProtocol != "h2" {
				return ErrHTTP2Required
			}
			if len(state.PeerCertificates) == 0 {
				return ErrAuthorityBindingMismatch
			}
			return verifyAuthorityCertificate(profile, state.PeerCertificates[0], time.Now())
		},
	}
	transport := &http2.Transport{
		TLSClientConfig:    tlsConfig,
		DisableCompression: true,
	}
	return &http.Client{
		Transport: profileTransport{profile: profile, transport: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirectForbidden
		},
	}, nil
}

type profileTransport struct {
	profile   Profile
	transport *http2.Transport
}

func (transport profileTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" ||
		request.URL.Host != transport.profile.address || request.URL.User != nil ||
		request.URL.Opaque != "" || request.URL.RawQuery != "" || request.URL.ForceQuery ||
		request.URL.Fragment != "" || request.Host != "" && request.Host != transport.profile.address {
		return nil, ErrOriginMismatch
	}
	for name := range request.Header {
		if !strings.EqualFold(name, "authorization") && !strings.EqualFold(name, "cookie") &&
			!strings.EqualFold(name, "content-encoding") && !strings.EqualFold(name, "accept-encoding") {
			continue
		}
		return nil, fmt.Errorf("%w: %s", ErrForbiddenHTTPField, name)
	}
	return transport.transport.RoundTrip(request)
}

func (transport profileTransport) CloseIdleConnections() {
	transport.transport.CloseIdleConnections()
}

func (profile Profile) validate() error {
	if profile.origin == "" || profile.host == "" || profile.address == "" || profile.domainID == "" || profile.epoch == 0 {
		return ErrInvalidProfile
	}
	return nil
}

func parseSPKIDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if !canonicalSPKIDigestPattern.MatchString(value) {
		return digest, errors.New("must be sha256:<64 lowercase hexadecimal characters>")
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || len(decoded) != len(digest) {
		return digest, errors.New("must encode exactly one SHA-256 digest")
	}
	copy(digest[:], decoded)
	return digest, nil
}

func verifyAuthorityCertificate(profile Profile, certificate *x509.Certificate, now time.Time) error {
	if certificate == nil || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) ||
		certificate.VerifyHostname(profile.host) != nil || !hasServerAuthenticationEKU(certificate) {
		return ErrAuthorityBindingMismatch
	}
	gotPin := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	if subtle.ConstantTimeCompare(gotPin[:], profile.authoritySPKI[:]) != 1 {
		return ErrAuthorityPinMismatch
	}
	if !criticalSubjectAlternativeName(certificate) || len(certificate.URIs) != 1 {
		return ErrAuthorityBindingMismatch
	}
	wantURI := fmt.Sprintf("wipd://authority/%s?epoch=%d&owner=%s", profile.domainID, profile.epoch, hex.EncodeToString(profile.ownerRootSPKI[:]))
	if certificate.URIs[0] == nil || certificate.URIs[0].String() != wantURI {
		return ErrAuthorityBindingMismatch
	}
	return nil
}

func hasServerAuthenticationEKU(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == serverAuthenticationExtended {
			return true
		}
	}
	return false
}

func criticalSubjectAlternativeName(certificate *x509.Certificate) bool {
	count := 0
	critical := false
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(subjectAlternativeNameOID) {
			count++
			critical = extension.Critical
		}
	}
	return count == 1 && critical
}
