// Package wipdseed implements the Step 4 client enrollment, verified seed,
// and bounded prefix-pull install for the M5 authority lab.
package wipdseed

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	stateName                         = "client-state.json"
	pendingName                       = "pending-enrollment.json"
	maxEnvironmentCertificateLifetime = 24 * time.Hour
)

var (
	// ErrInvalidClientState reports malformed, untrusted, or mismatched client identity data.
	ErrInvalidClientState = errors.New("wipdseed: invalid client state")
	// ErrStateExists means a different Environment identity is already installed.
	ErrStateExists    = errors.New("wipdseed: a different client identity is already installed")
	clientULIDPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// PreparedIdentity holds an Environment private key and its matching CSR
// until a verified enrollment and seed can be installed.
type PreparedIdentity struct {
	PrivateKeyPKCS8 []byte `json:"private_key_pkcs8"`
	CSRDER          []byte `json:"csr_der"`
}

// ClientState is the locally installed identity and verified shadow anchor.
type ClientState struct {
	Schema          string                       `json:"schema"`
	RepoID          string                       `json:"repo_id"`
	DomainID        string                       `json:"domain_id"`
	Epoch           uint64                       `json:"authority_epoch"`
	EnvironmentID   string                       `json:"environment_id"`
	OwnerKeyID      string                       `json:"owner_key_id"`
	SPKIDigest      string                       `json:"spki_digest"`
	PrivateKeyPKCS8 []byte                       `json:"private_key_pkcs8"`
	CertificateDER  [][]byte                     `json:"certificate_chain_der"`
	Prefix          wipdwire.PrefixAnchor        `json:"prefix"`
	EventRecords    []wipdwire.EventRecord       `json:"event_records"`
	ManifestDigest  string                       `json:"manifest_digest"`
	ManifestEntries []wipdwire.BlobManifestEntry `json:"manifest_entries"`
	Projections     []json.RawMessage            `json:"projections"`
	StepProjections []json.RawMessage            `json:"step_projections"`
}

// PrepareIdentity creates a private Environment key and CSR. The caller keeps
// this as pending material; it is not an installed Environment identity.
func PrepareIdentity() (PreparedIdentity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return PreparedIdentity{}, err
	}
	defer clear(privateKey)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return PreparedIdentity{}, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "wipd Environment"}}, privateKey)
	if err != nil {
		clear(der)
		return PreparedIdentity{}, err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		clear(der)
		return PreparedIdentity{}, ErrInvalidClientState
	}
	return PreparedIdentity{PrivateKeyPKCS8: der, CSRDER: csrDER}, nil
}

// SavePending writes the CSR and private key as one private pending file with
// create-only semantics. It never overwrites an installed identity.
func SavePending(directory string, identity PreparedIdentity) error {
	if len(identity.PrivateKeyPKCS8) == 0 || len(identity.CSRDER) == 0 {
		return ErrInvalidClientState
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	defer clear(data)
	path := filepath.Join(directory, pendingName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(err, closeErr)
	}
	return syncDirectory(directory)
}

// LoadPending reads a private pending identity without following symlinks.
func LoadPending(directory string) (PreparedIdentity, error) {
	path := filepath.Join(directory, pendingName)
	info, err := os.Lstat(path)
	if err != nil {
		return PreparedIdentity{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 32<<10 {
		return PreparedIdentity{}, ErrInvalidClientState
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PreparedIdentity{}, err
	}
	defer clear(data)
	var identity PreparedIdentity
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&identity); err != nil || decoder.Decode(new(any)) != io.EOF || len(identity.PrivateKeyPKCS8) == 0 || len(identity.CSRDER) == 0 {
		clear(identity.PrivateKeyPKCS8)
		return PreparedIdentity{}, ErrInvalidClientState
	}
	return identity, nil
}

// EnrollAndSeed authenticates the authority before enrollment, obtains the
// certificate for the pending CSR, proves its key possession with mTLS, and
// installs only after the complete empty-prefix seed has verified.
func EnrollAndSeed(ctx context.Context, profile wipdauthority.Profile, roots *x509.CertPool, ownerRoot ed25519.PublicKey, caDelegation []byte, repoID string, identity PreparedIdentity, grant []byte, directory string) (ClientState, error) {
	var empty ClientState
	if ctx == nil || !clientULIDPattern.MatchString(repoID) || len(grant) == 0 || len(grant) > 4096 {
		return empty, ErrInvalidClientState
	}
	if profile.M5LabRepoID() != repoID {
		return empty, wipdauthority.ErrRepoBindingMismatch
	}
	trustedEnvironmentCA, err := wipdauthority.VerifyEnvironmentCADelegation(profile, ownerRoot, caDelegation, time.Now().UTC())
	if err != nil {
		return empty, ErrInvalidClientState
	}
	privateKey, csr, spkiDigest, err := validatePrepared(identity)
	if err != nil {
		return empty, err
	}
	defer clear(privateKey)
	if info, statErr := os.Lstat(filepath.Join(directory, stateName)); statErr == nil && info.Mode().IsRegular() {
		return empty, ErrStateExists
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return empty, statErr
	}
	enrollmentClient, err := profile.HTTPClient(roots)
	if err != nil {
		return empty, err
	}
	defer enrollmentClient.CloseIdleConnections()
	request := wipdwire.EnrollmentRequest{Schema: "wipd.enrollment-request/1", Credential: bytes.Clone(grant), CSRDER: bytes.Clone(identity.CSRDER)}
	defer clear(request.Credential)
	requestFrame, requestID, err := encodeRequestFrame("enrollment.request", request)
	if err != nil {
		return empty, err
	}
	defer clear(requestFrame)
	responseFrame, err := postSingleFrame(ctx, enrollmentClient, profile.Origin()+"/wipd/v1/enroll", requestFrame, requestID)
	if err != nil {
		return empty, err
	}
	if responseFrame.Kind == "problem" {
		return empty, decodeProblem(responseFrame.Payload)
	}
	if responseFrame.Kind != "enrollment.issued" || responseFrame.Sequence != 0 {
		return empty, ErrInvalidClientState
	}
	var issued wipdwire.EnrollmentIssued
	if err = wipdwire.DecodeCanonical(responseFrame.Payload, &issued, "schema", "domain_id", "authority_epoch", "environment_id", "owner_key_id", "spki_digest", "certificate_chain_der"); err != nil {
		return empty, err
	}
	if issued.Schema != "wipd.enrollment-issued/1" || issued.DomainID != profile.DomainID() || issued.Epoch != profile.Epoch() ||
		!clientULIDPattern.MatchString(issued.EnvironmentID) ||
		issued.OwnerKeyID != profile.OwnerRootSPKI() || issued.SPKIDigest != spkiDigest || len(issued.CertificateChain) != 2 {
		return empty, ErrInvalidClientState
	}
	clientCertificate, err := verifyIssuedCertificate(issued, trustedEnvironmentCA, csr, privateKey, time.Now())
	if err != nil {
		return empty, err
	}
	seedClient, err := profile.HTTPClientWithCertificate(roots, &clientCertificate)
	if err != nil {
		return empty, err
	}
	defer seedClient.CloseIdleConnections()
	limits, err := negotiateRemote(ctx, seedClient, profile.Origin())
	if err != nil {
		return empty, err
	}
	seedRequest := wipdwire.SeedRequest{
		Schema: "wipd.seed-request/1", DomainID: profile.DomainID(), Epoch: profile.Epoch(),
		StoreSchema: "wipd.store/1",
	}
	seedFrame, seedRequestID, err := encodeRequestFrame("seed.request", seedRequest)
	if err != nil {
		return empty, err
	}
	if !withinSessionFrame(seedFrame, limits) {
		return empty, ErrInvalidClientState
	}
	seedFrames, err := postFramesWithinSession(ctx, seedClient, profile.Origin()+"/wipd/v1/exchange", seedFrame, seedRequestID, maxClientTransferEvents+3, limits)
	if err != nil {
		return empty, err
	}
	seed, err := verifyTransferFrames(seedFrames, "seed", profile, repoID, issued.EnvironmentID, issued.SPKIDigest, emptyWireAnchor(), nil)
	if err != nil {
		return empty, err
	}
	seed.PrivateKeyPKCS8 = bytes.Clone(identity.PrivateKeyPKCS8)
	seed.CertificateDER = clone2D(issued.CertificateChain)
	if err = installState(directory, seed); err != nil {
		clear(seed.PrivateKeyPKCS8)
		return empty, err
	}
	if err = os.Remove(filepath.Join(directory, pendingName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		clear(seed.PrivateKeyPKCS8)
		return seed, err
	}
	if err = syncDirectory(directory); err != nil {
		clear(seed.PrivateKeyPKCS8)
		return seed, err
	}
	return seed, nil
}

func validatePrepared(identity PreparedIdentity) (ed25519.PrivateKey, *x509.CertificateRequest, string, error) {
	key, err := x509.ParsePKCS8PrivateKey(identity.PrivateKeyPKCS8)
	if err != nil {
		return nil, nil, "", ErrInvalidClientState
	}
	signer, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, "", ErrInvalidClientState
	}
	csr, err := x509.ParseCertificateRequest(identity.CSRDER)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(identity.CSRDER, csr.Raw) {
		clear(signer)
		return nil, nil, "", ErrInvalidClientState
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicDER, csr.RawSubjectPublicKeyInfo) {
		clear(signer)
		return nil, nil, "", ErrInvalidClientState
	}
	sum := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	return signer, csr, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func verifyIssuedCertificate(issued wipdwire.EnrollmentIssued, trustedEnvironmentCA []byte, csr *x509.CertificateRequest, privateKey crypto.Signer, now time.Time) (tls.Certificate, error) {
	var result tls.Certificate
	if len(issued.CertificateChain) != 2 {
		return result, ErrInvalidClientState
	}
	leaf, err := x509.ParseCertificate(issued.CertificateChain[0])
	if err != nil {
		return result, ErrInvalidClientState
	}
	if !bytes.Equal(issued.CertificateChain[1], trustedEnvironmentCA) {
		return result, ErrInvalidClientState
	}
	ca, err := x509.ParseCertificate(issued.CertificateChain[1])
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 || ca.KeyUsage != x509.KeyUsageCertSign || ca.CheckSignatureFrom(ca) != nil {
		return result, ErrInvalidClientState
	}
	if !bytes.Equal(leaf.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) || !bytes.Equal(leaf.RawSubjectPublicKeyInfo, mustSPKI(privateKey.Public())) ||
		leaf.IsCA || !leaf.BasicConstraintsValid || !hasCriticalCertificateExtension(leaf, []int{2, 5, 29, 19}) ||
		leaf.KeyUsage != x509.KeyUsageDigitalSignature || !hasCriticalCertificateExtension(leaf, []int{2, 5, 29, 15}) ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth ||
		len(leaf.UnknownExtKeyUsage) != 0 || len(leaf.DNSNames) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.IPAddresses) != 0 ||
		len(leaf.URIs) != 1 || leaf.URIs[0] == nil || leaf.NotBefore.Before(ca.NotBefore) || leaf.NotAfter.After(ca.NotAfter) ||
		!leaf.NotBefore.Before(leaf.NotAfter) || leaf.NotAfter.Sub(leaf.NotBefore) > maxEnvironmentCertificateLifetime || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return result, ErrInvalidClientState
	}
	wantURI := fmt.Sprintf("wipd://environment/%s?domain=%s&epoch=%d&owner=%s", issued.EnvironmentID, issued.DomainID, issued.Epoch, strings.TrimPrefix(issued.OwnerKeyID, "sha256:"))
	if leaf.URIs[0].String() != wantURI || leaf.CheckSignatureFrom(ca) != nil || !hasCriticalSAN(leaf) {
		return result, ErrInvalidClientState
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return result, ErrInvalidClientState
	}
	return tls.Certificate{Certificate: clone2D(issued.CertificateChain), PrivateKey: privateKey, Leaf: leaf}, nil
}

func hasCriticalSAN(certificate *x509.Certificate) bool {
	count := 0
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal([]int{2, 5, 29, 17}) {
			if !extension.Critical {
				return false
			}
			count++
		}
	}
	return count == 1
}

func hasCriticalCertificateExtension(certificate *x509.Certificate, oid []int) bool {
	for _, extension := range certificate.Extensions {
		if extension.Id.Equal(oid) {
			return extension.Critical
		}
	}
	return false
}

func mustSPKI(public any) []byte {
	der, _ := x509.MarshalPKIXPublicKey(public)
	return der
}

type nestedMapSchema struct {
	path []string
	keys []string
}

func decodeClosedRecord(payload []byte, destination any, keys ...string) error {
	fields, err := wipdwire.DecodeCanonicalMap(payload, keys...)
	if err != nil {
		return err
	}
	var schemas []nestedMapSchema
	switch destination.(type) {
	case *wipdwire.SeedStart:
		schemas = []nestedMapSchema{
			{path: []string{"prefix"}, keys: []string{"start", "end"}},
			{path: []string{"prefix", "start"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}},
			{path: []string{"prefix", "end"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}},
		}
	case *wipdwire.PullStart:
		schemas = []nestedMapSchema{
			{path: []string{"prefix"}, keys: []string{"start", "end"}},
			{path: []string{"prefix", "start"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}},
			{path: []string{"prefix", "end"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}},
		}
	case *wipdwire.BlobManifest:
		schemas = []nestedMapSchema{{path: []string{"as_of"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}}}
	case *wipdwire.SeedEnd:
		schemas = []nestedMapSchema{{path: []string{"verified_prefix"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}}}
	case *wipdwire.PullEnd:
		schemas = []nestedMapSchema{{path: []string{"verified_prefix"}, keys: []string{"event_count", "high_water_event_id", "prefix_digest"}}}
	default:
		return ErrInvalidClientState
	}
	for _, schema := range schemas {
		value := any(fields)
		for _, part := range schema.path {
			parent, ok := value.(map[string]any)
			if !ok {
				return ErrInvalidClientState
			}
			value, ok = parent[part]
			if !ok {
				return ErrInvalidClientState
			}
		}
		nested, ok := value.(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(nested, schema.keys...) {
			return ErrInvalidClientState
		}
	}
	return wipdwire.DecodeCanonical(payload, destination, keys...)
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func postSingleFrame(ctx context.Context, client *http.Client, endpoint string, requestFrame []byte, requestID string) (wipdwire.Frame, error) {
	frames, err := postFrames(ctx, client, endpoint, requestFrame, requestID, 1)
	if err != nil {
		return wipdwire.Frame{}, err
	}
	return frames[0], nil
}

func postFrames(ctx context.Context, client *http.Client, endpoint string, requestFrame []byte, requestID string, maxFrames int) ([]wipdwire.Frame, error) {
	return postFramesBounded(ctx, client, endpoint, requestFrame, requestID, maxFrames,
		maxFrames*(wipdwire.FrameLimit+4), wipdwire.FrameLimit)
}

func postFramesWithinSession(ctx context.Context, client *http.Client, endpoint string, requestFrame []byte, requestID string, maxFrames int, limits sessionLimits) ([]wipdwire.Frame, error) {
	if !withinSessionFrame(requestFrame, limits) {
		return nil, ErrInvalidClientState
	}
	return postFramesBounded(ctx, client, endpoint, requestFrame, requestID, maxFrames, limits.streamBytes, limits.frameBody)
}

func postFramesBounded(ctx context.Context, client *http.Client, endpoint string, requestFrame []byte, requestID string, maxFrames, maxBytes, maxFrameBody int) ([]wipdwire.Frame, error) {
	if maxFrames <= 0 || maxBytes <= 0 || maxFrameBody <= 0 {
		return nil, ErrInvalidClientState
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestFrame))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/cbor")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil || len(data) > maxBytes || response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "application/cbor" || response.Header.Get("Content-Encoding") != "" {
		return nil, ErrInvalidClientState
	}
	frames, err := wipdwire.ReadFrames(data, maxFrames)
	if err != nil || len(frames) == 0 {
		return nil, ErrInvalidClientState
	}
	for _, frame := range frames {
		if frame.RequestID != requestID {
			return nil, ErrInvalidClientState
		}
		wire, encodeErr := wipdwire.EncodeFrame(frame)
		if encodeErr != nil || len(wire)-4 > maxFrameBody {
			return nil, ErrInvalidClientState
		}
	}
	return frames, nil
}

func encodeRequestFrame(kind string, value any) ([]byte, string, error) {
	requestID, err := newRequestID()
	if err != nil {
		return nil, "", err
	}
	payload, err := wipdwire.EncodeCanonical(value)
	if err != nil {
		return nil, "", err
	}
	frame, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: requestID, Kind: kind, Payload: payload})
	return frame, requestID, err
}

func newRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	// Use the same canonical 26-character Crockford Base32 ID shape as ULID.
	raw[0] &= 0x3f
	return encodeULID(raw), nil
}

func encodeULID(raw [16]byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	value := new(big.Int).SetBytes(raw[:])
	base := big.NewInt(32)
	var result [26]byte
	for i := len(result) - 1; i >= 0; i-- {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(value, base, remainder)
		result[i] = alphabet[remainder.Int64()]
		value = quotient
	}
	return string(result[:])
}

func decodeProblem(payload []byte) error {
	var problem struct {
		Code string `cbor:"code"`
	}
	if wipdwire.DecodeCanonical(payload, &problem, "code") != nil || problem.Code == "" {
		return ErrInvalidClientState
	}
	return errors.New("authority rejected client exchange: " + problem.Code)
}

func emptyPrefixDigest() string {
	sum := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func emptyManifestDigest() string {
	sum := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func installState(directory string, state ClientState) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(data)
	target := filepath.Join(directory, stateName)
	temp, err := os.CreateTemp(directory, ".client-state-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(tempName, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			installed, readErr := os.ReadFile(target)
			if readErr == nil && bytes.Equal(installed, data) {
				return nil
			}
			return ErrStateExists
		}
		return err
	}
	if err = os.Remove(tempName); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func clone2D(input [][]byte) [][]byte {
	result := make([][]byte, len(input))
	for i := range input {
		result[i] = bytes.Clone(input[i])
	}
	return result
}
