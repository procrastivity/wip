package wipdauthority

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	labEnrollPath       = "/wipd/v1/enroll"
	labExchangePath     = "/wipd/v1/exchange"
	maxLabRequestBytes  = 65_536
	maxLabResponseBytes = 65_536
)

var ulidAlphabet = []byte("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

// EnvironmentLeafSigner is the restricted per-run signer. The caller must
// keep its CA private key outside SQLite and return only the certificate DER.
type EnvironmentLeafSigner func(context.Context, string, uint64, string, []byte, time.Time) ([]byte, error)

// M5LabConfig binds the lab-only enrollment and initial-seed endpoints to the
// persisted authority store and one exact initial Repo membership.
type M5LabConfig struct {
	Store                       *authoritystore.Store
	RepoID                      string
	EnrollmentGrant             []byte
	ExpectedCSRDER              []byte
	EnvironmentCACertificateDER []byte
	SignEnvironmentLeaf         EnvironmentLeafSigner
}

type m5LabHandler struct {
	profile  Profile
	store    *authoritystore.Store
	repoID   string
	grant    []byte
	csrDER   []byte
	caDER    []byte
	signLeaf EnvironmentLeafSigner
}

// NewM5LabServer exposes the existing M2 enrollment and seed/exchange records
// for one exact Repo in the disposable M5 lab. NewServer remains health-only.
func NewM5LabServer(profile Profile, certificate tls.Certificate, config M5LabConfig) (*Server, error) {
	if config.Store == nil || !ulidPattern.MatchString(config.RepoID) || len(config.EnrollmentGrant) == 0 || len(config.EnrollmentGrant) > 4096 ||
		len(config.ExpectedCSRDER) == 0 || len(config.ExpectedCSRDER) > 16_384 || len(config.EnvironmentCACertificateDER) == 0 || config.SignEnvironmentLeaf == nil {
		return nil, ErrInvalidLabConfig
	}
	csr, err := x509.ParseCertificateRequest(config.ExpectedCSRDER)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(config.ExpectedCSRDER, csr.Raw) {
		return nil, ErrInvalidLabConfig
	}
	domain, err := config.Store.LookupDomain(context.Background(), profile.domainID)
	if err != nil || domain.ActiveEpoch != profile.epoch || domain.OwnerKeyID != profile.OwnerRootSPKI() {
		return nil, ErrAuthorityBindingMismatch
	}
	repoDomain, err := config.Store.RepoDomain(context.Background(), config.RepoID)
	if err != nil || repoDomain != profile.domainID {
		return nil, ErrRepoMembershipMismatch
	}
	ca, err := x509.ParseCertificate(config.EnvironmentCACertificateDER)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 {
		return nil, ErrInvalidLabConfig
	}
	app := &m5LabHandler{
		profile: profile, store: config.Store, repoID: config.RepoID,
		grant: bytes.Clone(config.EnrollmentGrant), csrDER: bytes.Clone(config.ExpectedCSRDER),
		caDER: bytes.Clone(config.EnvironmentCACertificateDER), signLeaf: config.SignEnvironmentLeaf,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", serveHealth)
	mux.HandleFunc(labEnrollPath, app.serveEnroll)
	mux.HandleFunc(labExchangePath, app.serveExchange)
	return newServer(profile, certificate, mux, true)
}

var ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

var (
	labEncoder, _ = cbor.CoreDetEncOptions().EncMode()
	labDecoder, _ = (cbor.DecOptions{
		DupMapKey:        cbor.DupMapKeyEnforcedAPF,
		IndefLength:      cbor.IndefLengthForbidden,
		TagsMd:           cbor.TagsForbidden,
		MaxNestedLevels:  16,
		MaxArrayElements: 4096,
		MaxMapPairs:      1024,
	}).DecMode()
)

type enrollmentGrantIdentity struct {
	Schema string `cbor:"schema"`
	ID     string `cbor:"grant_id"`
}

type signedArtifactIdentity struct {
	Payload []byte `cbor:"payload"`
}

func (app *m5LabHandler) serveEnroll(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != labEnrollPath || request.Method != http.MethodPost || request.URL.RawQuery != "" || request.URL.ForceQuery || !allowedLabHTTP(request) {
		http.NotFound(writer, request)
		return
	}
	if !hasContentType(request, "application/cbor") {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	frame, err := readSingleFrame(request.Body)
	if err != nil || frame.Sequence != 0 || frame.Kind != "enrollment.request" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	var payload wipdwire.EnrollmentRequest
	if err = wipdwire.DecodeCanonical(frame.Payload, &payload, "schema", "credential", "csr_der", "prior_environment_id"); err != nil ||
		payload.Schema != "wipd.enrollment-request/1" || len(payload.Credential) == 0 || len(payload.Credential) > 4096 ||
		len(payload.CSRDER) == 0 || len(payload.CSRDER) > 16_384 || payload.PriorID != nil {
		writeLabProblem(writer, frame.RequestID, "auth.grant-invalid")
		return
	}
	if err = app.verifyRoute(request.Context()); err != nil {
		writeLabProblem(writer, frame.RequestID, "auth.authority-binding-mismatch")
		return
	}
	if !bytes.Equal(payload.Credential, app.grant) || !bytes.Equal(payload.CSRDER, app.csrDER) {
		writeLabProblem(writer, frame.RequestID, "auth.grant-invalid")
		return
	}
	csr, err := x509.ParseCertificateRequest(payload.CSRDER)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(payload.CSRDER, csr.Raw) {
		writeLabProblem(writer, frame.RequestID, "auth.grant-invalid")
		return
	}
	environmentID, err := stableEnvironmentID(app.profile.domainID, payload.Credential)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "auth.grant-invalid")
		return
	}
	issued, alreadyIssued, err := app.store.ValidateEnvironmentEnrollment(request.Context(), app.profile.domainID, environmentID, payload.Credential, payload.CSRDER, time.Now().UTC())
	if err != nil {
		code := "auth.grant-invalid"
		if errors.Is(err, authoritystore.ErrFenced) || errors.Is(err, authoritystore.ErrExists) {
			code = "auth.grant-consumed"
		}
		writeLabProblem(writer, frame.RequestID, code)
		return
	}
	if !alreadyIssued {
		leaf, err := app.signLeaf(request.Context(), app.profile.domainID, app.profile.epoch, environmentID, payload.CSRDER, time.Now().UTC())
		if err != nil || len(leaf) == 0 || len(leaf) > 16_384 {
			writeLabProblem(writer, frame.RequestID, "auth.grant-invalid")
			return
		}
		issued, err = app.store.IssueEnvironmentCertificate(request.Context(), app.profile.domainID, environmentID, payload.Credential, payload.CSRDER, [][]byte{leaf, app.caDER}, time.Now().UTC())
		if err != nil {
			code := "auth.grant-invalid"
			if errors.Is(err, authoritystore.ErrFenced) || errors.Is(err, authoritystore.ErrExists) {
				code = "auth.grant-consumed"
			}
			writeLabProblem(writer, frame.RequestID, code)
			return
		}
	}
	response := wipdwire.EnrollmentIssued{
		Schema: "wipd.enrollment-issued/1", DomainID: issued.DomainID, Epoch: issued.Epoch,
		EnvironmentID: issued.EnvironmentID, OwnerKeyID: app.profile.OwnerRootSPKI(),
		SPKIDigest: issued.SPKIDigest, CertificateChain: issued.Chain,
	}
	body, err := wipdwire.EncodeCanonical(response)
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 0, Kind: "enrollment.issued", Payload: body})
}

func (app *m5LabHandler) serveExchange(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != labExchangePath || request.Method != http.MethodPost || request.URL.RawQuery != "" || request.URL.ForceQuery || !allowedLabHTTP(request) {
		http.NotFound(writer, request)
		return
	}
	if !hasContentType(request, "application/cbor") {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if request.TLS == nil || !request.TLS.HandshakeComplete || len(request.TLS.PeerCertificates) != 2 {
		writeLabProblem(writer, "00000000000000000000000000", "auth.environment-certificate-required")
		return
	}
	peer := request.TLS.PeerCertificates[0]
	if len(peer.URIs) != 1 || peer.URIs[0] == nil {
		writeLabProblem(writer, "00000000000000000000000000", "auth.environment-domain-mismatch")
		return
	}
	// VerifyEnvironmentPeer validates the exact SAN, chain, key possession,
	// revocation state, domain, and epoch from the completed TLS connection.
	certificate, err := app.store.VerifyEnvironmentPeer(request.Context(), app.profile.domainID, environmentIDFromURI(peer.URIs[0].String()), app.profile.epoch, *request.TLS, time.Now().UTC())
	if err != nil || certificate.DomainID != app.profile.domainID {
		writeLabProblem(writer, "00000000000000000000000000", "auth.environment-domain-mismatch")
		return
	}
	if err = app.verifyRoute(request.Context()); err != nil {
		writeLabProblem(writer, "00000000000000000000000000", "auth.authority-binding-mismatch")
		return
	}
	frame, err := readSingleFrame(request.Body)
	if err != nil || frame.Sequence != 0 || frame.Kind != "seed.request" {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	var seedRequest wipdwire.SeedRequest
	if err = wipdwire.DecodeCanonical(frame.Payload, &seedRequest, "schema", "domain_id", "expected_epoch", "store_schema", "resume_token"); err != nil ||
		seedRequest.Schema != "wipd.seed-request/1" || seedRequest.DomainID != app.profile.domainID ||
		seedRequest.Epoch != app.profile.epoch || seedRequest.StoreSchema != "wipd.store/1" || seedRequest.ResumeToken != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	current, err := app.store.CurrentPrefixAnchor(request.Context(), app.profile.domainID)
	if err != nil || current.EventCount != 0 || current.EventID != "" || current.Digest != emptyPrefixDigest() {
		writeLabProblem(writer, frame.RequestID, "transfer.prefix-mismatch")
		return
	}
	product, err := app.initialSeed(request.Context(), time.Now().UTC())
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "transfer.incomplete")
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	for sequence, record := range product {
		wire, encodeErr := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: frame.RequestID, Sequence: uint64(sequence), Kind: record.kind, Payload: record.payload})
		if encodeErr != nil || len(wire) > maxLabResponseBytes {
			http.Error(writer, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err = writer.Write(wire); err != nil {
			return
		}
	}
}

type labFrameRecord struct {
	kind    string
	payload []byte
}

func (app *m5LabHandler) initialSeed(ctx context.Context, now time.Time) ([]labFrameRecord, error) {
	empty := authoritystore.PrefixAnchor{Digest: emptyPrefixDigest()}
	transferID, err := randomULID(now)
	if err != nil {
		return nil, err
	}
	snapshotID, err := randomULID(now)
	if err != nil {
		return nil, err
	}
	transfer, err := app.store.StartTransfer(ctx, app.profile.domainID, app.profile.epoch, "seed", "wipd.store/1", empty, snapshotID, transferID, now)
	if err != nil || transfer.EventCount != 0 || transfer.EventByteLength != 0 || len(transfer.Snapshot.Manifest.Entries) != 0 || len(transfer.Snapshot.Items) != 0 {
		return nil, errors.New("fresh Step 4 seed must be empty")
	}
	boundary, err := app.store.NextTransfer(ctx, transfer.Token, wipdwire.FrameLimit, now)
	if err != nil || !boundary.Complete || boundary.ManifestDigest != transfer.Snapshot.Manifest.Digest {
		return nil, errors.New("empty seed transfer did not reach complete manifest boundary")
	}
	start := wipdwire.SeedStart{
		Schema: "wipd.seed-start/1", TransferID: transfer.ID, DomainID: app.profile.domainID,
		Epoch: app.profile.epoch, StoreSchema: "wipd.store/1", SnapshotID: transfer.SnapshotID,
		EventCount: 0, EventByteLength: 0, ManifestDigest: transfer.Snapshot.Manifest.Digest,
	}
	start.Prefix.Start = wireAnchor(transfer.Snapshot.Delta.Start)
	start.Prefix.End = wireAnchor(transfer.Snapshot.Delta.End)
	manifest := wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: transfer.Snapshot.Manifest.DomainID,
		Epoch: transfer.Snapshot.Manifest.Epoch, AsOf: wireAnchor(transfer.Snapshot.Manifest.AsOf),
		Entries: []wipdwire.BlobManifestEntry{}, Digest: transfer.Snapshot.Manifest.Digest,
	}
	end := wipdwire.SeedEnd{
		Schema: "wipd.seed-end/1", TransferID: transfer.ID,
		VerifiedPrefix: wireAnchor(transfer.Snapshot.Delta.End),
		ManifestDigest: transfer.Snapshot.Manifest.Digest, Complete: true,
	}
	values := []struct {
		kind  string
		value any
	}{
		{"seed.start", start},
		{"blob.manifest", manifest},
		{"seed.end", end},
	}
	product := make([]labFrameRecord, 0, len(values))
	for _, item := range values {
		payload, encodeErr := wipdwire.EncodeCanonical(item.value)
		if encodeErr != nil {
			return nil, encodeErr
		}
		product = append(product, labFrameRecord{kind: item.kind, payload: payload})
	}
	return product, nil
}

func (app *m5LabHandler) verifyRoute(ctx context.Context) error {
	domain, err := app.store.LookupDomain(ctx, app.profile.domainID)
	if err != nil || domain.ActiveEpoch != app.profile.epoch || domain.OwnerKeyID != app.profile.OwnerRootSPKI() {
		return ErrAuthorityBindingMismatch
	}
	repoDomain, err := app.store.RepoDomain(ctx, app.repoID)
	if err != nil || repoDomain != app.profile.domainID {
		return ErrRepoMembershipMismatch
	}
	return nil
}

func readSingleFrame(reader io.Reader) (wipdwire.Frame, error) {
	limited := io.LimitReader(reader, maxLabRequestBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) == 0 || len(data) > maxLabRequestBytes {
		return wipdwire.Frame{}, wipdwire.ErrInvalidRecord
	}
	frames, err := wipdwire.ReadFrames(data, 1)
	if err != nil || len(frames) != 1 {
		return wipdwire.Frame{}, wipdwire.ErrInvalidRecord
	}
	return frames[0], nil
}

func allowedLabHTTP(request *http.Request) bool {
	for name := range request.Header {
		if hasHeader(http.Header{name: nil}, "Authorization", "Cookie", "Content-Encoding", "Accept-Encoding") {
			return false
		}
	}
	return true
}

func hasContentType(request *http.Request, expected string) bool {
	return request.Header.Get("Content-Type") == expected
}

func writeLabFrame(writer http.ResponseWriter, frame wipdwire.Frame) {
	wire, err := wipdwire.EncodeFrame(frame)
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(wire)
}

func writeLabProblem(writer http.ResponseWriter, requestID, code string) {
	if !ulidPattern.MatchString(requestID) {
		requestID = "00000000000000000000000000"
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{"code": code})
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: requestID, Sequence: 0, Kind: "problem", Payload: payload})
}

func wireAnchor(anchor authoritystore.PrefixAnchor) wipdwire.PrefixAnchor {
	var eventID *string
	if anchor.EventID != "" {
		eventID = &anchor.EventID
	}
	return wipdwire.PrefixAnchor{EventCount: anchor.EventCount, EventID: eventID, Digest: anchor.Digest}
}

func emptyPrefixDigest() string {
	sum := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randomULID(now time.Time) (string, error) {
	var id [16]byte
	millis := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		id[i] = byte(millis)
		millis >>= 8
	}
	if _, err := rand.Read(id[6:]); err != nil {
		return "", err
	}
	return encodeULID(id), nil
}

func encodeULID(id [16]byte) string {
	value := new(big.Int).SetBytes(id[:])
	base := big.NewInt(32)
	var output [26]byte
	for i := len(output) - 1; i >= 0; i-- {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(value, base, remainder)
		output[i] = ulidAlphabet[remainder.Int64()]
		value = quotient
	}
	return string(output[:])
}

func stableEnvironmentID(domain string, credential []byte) (string, error) {
	var wrapper map[string]any
	if err := labDecoder.Unmarshal(credential, &wrapper); err != nil || wrapper == nil || len(wrapper) != 14 {
		return "", ErrInvalidGrantIdentity
	}
	for _, key := range []string{"schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature"} {
		if _, ok := wrapper[key]; !ok {
			return "", ErrInvalidGrantIdentity
		}
	}
	canonical, err := labEncoder.Marshal(wrapper)
	if err != nil || !bytes.Equal(canonical, credential) {
		return "", ErrInvalidGrantIdentity
	}
	var identity signedArtifactIdentity
	if err = labDecoder.Unmarshal(credential, &identity); err != nil {
		return "", ErrInvalidGrantIdentity
	}
	var grant enrollmentGrantIdentity
	if err = labDecoder.Unmarshal(identity.Payload, &grant); err != nil || grant.Schema != "wipd.enrollment-grant/1" || !ulidPattern.MatchString(grant.ID) {
		return "", ErrInvalidGrantIdentity
	}
	var payload map[string]any
	if err = labDecoder.Unmarshal(identity.Payload, &payload); err != nil || payload == nil || len(payload) != 11 {
		return "", ErrInvalidGrantIdentity
	}
	canonical, err = labEncoder.Marshal(payload)
	if err != nil || !bytes.Equal(canonical, identity.Payload) {
		return "", ErrInvalidGrantIdentity
	}
	hash := sha256.Sum256(append(append([]byte("wipd/environment-id/v1\x00"), []byte(domain)...), []byte(grant.ID)...))
	var id [16]byte
	copy(id[:], hash[:16])
	id[0] &= 0x3f // ULID's 130-bit representation has two leading zero bits.
	return encodeULID(id), nil
}

func environmentIDFromURI(uri string) string {
	const prefix = "wipd://environment/"
	if !bytes.HasPrefix([]byte(uri), []byte(prefix)) {
		return ""
	}
	rest := uri[len(prefix):]
	for i, char := range rest {
		if char == '?' {
			return rest[:i]
		}
	}
	return ""
}

var (
	// ErrInvalidLabConfig reports an invalid bounded M5 Compose lab service configuration.
	ErrInvalidLabConfig = errors.New("wipdauthority: invalid M5 lab server configuration")
	// ErrRepoMembershipMismatch reports that the exact Repo is not bound to the configured domain.
	ErrRepoMembershipMismatch = errors.New("wipdauthority: Repo is not a member of the configured domain")
	// ErrInvalidGrantIdentity reports a malformed or mismatched enrollment grant identity.
	ErrInvalidGrantIdentity = errors.New("wipdauthority: invalid enrollment grant identity")
)
