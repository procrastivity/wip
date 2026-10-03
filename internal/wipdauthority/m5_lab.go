package wipdauthority

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
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

// M5LabConfig binds the lab-only enrollment, transfer, and optional command
// endpoints to the persisted authority store and one exact initial Repo. A
// non-nil Registry enables the negotiated M5 operations; it requires the
// offline owner-certified authority-artifact key plus its restricted signer.
type M5LabConfig struct {
	Store                       *authoritystore.Store
	RepoID                      string
	EnrollmentGrant             []byte
	ExpectedCSRDER              []byte
	EnvironmentCACertificateDER []byte
	SignEnvironmentLeaf         EnvironmentLeafSigner
	Registry                    *operation.Registry
	ArtifactKeyCertificate      []byte
	SignArtifact                authoritystore.Signer
}

type m5LabHandler struct {
	profile    Profile
	store      *authoritystore.Store
	repoID     string
	grant      []byte
	csrDER     []byte
	caDER      []byte
	signLeaf   EnvironmentLeafSigner
	registry   *operation.Registry
	operations []operation.Definition
	sign       authoritystore.Signer
	m6         bool
}

// NewM5LabServer exposes the existing M2 enrollment and negotiated exchange
// records for one exact Repo in the disposable M5 lab. NewServer remains
// health-only.
func NewM5LabServer(profile Profile, certificate tls.Certificate, config M5LabConfig) (*Server, error) {
	return newLabServer(profile, certificate, config, false)
}

// NewM6LabServer creates the explicit M6 acceptance path. It preserves the
// closed M5 compatibility set and additionally requires the complete Step 4
// and Step 5 catalogues. The Step 7 sweep is admitted only when its explicit
// candidate definition is present in the supplied M6 registry.
func NewM6LabServer(profile Profile, certificate tls.Certificate, config M5LabConfig) (*Server, error) {
	return newLabServer(profile, certificate, config, true)
}

func newLabServer(profile Profile, certificate tls.Certificate, config M5LabConfig, m6 bool) (*Server, error) {
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
	genesisRepoID, err := config.Store.M5LabGenesisRepoID(context.Background(), profile.domainID)
	if err != nil || genesisRepoID != config.RepoID || profile.M5LabRepoID() != genesisRepoID {
		return nil, ErrRepoBindingMismatch
	}
	ca, err := x509.ParseCertificate(config.EnvironmentCACertificateDER)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 {
		return nil, ErrInvalidLabConfig
	}
	var operations []operation.Definition
	if m6 && config.Registry == nil {
		return nil, ErrInvalidLabConfig
	}
	if config.Registry != nil {
		operations = config.Registry.Definitions()
		maxOperations := 7
		if m6 {
			maxOperations += len(operation.Step4Catalogue()) + len(operation.Step5Catalogue())
		}
		step7SweepRegistered := false
		for _, definition := range operations {
			if definition.Metadata().Operation == operation.BatchSweepAnonymousV1.Metadata().Operation {
				step7SweepRegistered = true
			}
		}
		step7OperationCount := 0
		if m6 && step7SweepRegistered {
			step7OperationCount = 1
		}
		if len(operations) == 0 || len(operations) > maxOperations+step7OperationCount ||
			m6 && len(operations) != maxOperations+step7OperationCount ||
			len(config.ArtifactKeyCertificate) == 0 || len(config.ArtifactKeyCertificate) > 1<<20 || config.SignArtifact == nil {
			return nil, ErrInvalidLabConfig
		}
		matterRegistered := false
		step4Registered := make(map[operation.ID]bool)
		step5Registered := make(map[operation.ID]bool)
		registered := make(map[operation.ID]bool, len(operations))
		for _, definition := range operations {
			id := definition.Metadata().Operation
			registered[id] = true
			if m6 && operation.Step4Operation(id) {
				step4Registered[id] = true
				continue
			}
			if m6 && operation.Step5Operation(id) {
				step5Registered[id] = true
				continue
			}
			switch id {
			case operation.MatterCreateV1.Metadata().Operation:
				matterRegistered = true
			case operation.StepCreateV1.Metadata().Operation,
				operation.StepStartV1.Metadata().Operation,
				operation.StepFinishV1.Metadata().Operation,
				operation.MatterFinishV1.Metadata().Operation,
				operation.ContentWriteOnceV1.Metadata().Operation,
				operation.FindingAppendV1.Metadata().Operation:
			case operation.BatchSweepAnonymousV1.Metadata().Operation:
				if !m6 {
					return nil, ErrInvalidLabConfig
				}
			default:
				return nil, ErrInvalidLabConfig
			}
		}
		if !matterRegistered {
			return nil, ErrInvalidLabConfig
		}
		if m6 {
			for _, definition := range []operation.Definition{
				operation.MatterCreateV1, operation.StepCreateV1, operation.StepStartV1,
				operation.StepFinishV1, operation.MatterFinishV1, operation.ContentWriteOnceV1, operation.FindingAppendV1,
			} {
				if !registered[definition.Metadata().Operation] {
					return nil, ErrInvalidLabConfig
				}
			}
			for _, definition := range operation.Step4Catalogue() {
				if !step4Registered[definition.Metadata().Operation] {
					return nil, ErrInvalidLabConfig
				}
			}
			for _, definition := range operation.Step5Catalogue() {
				if !step5Registered[definition.Metadata().Operation] {
					return nil, ErrInvalidLabConfig
				}
			}
		}
		if len(certificate.Certificate) == 0 {
			return nil, ErrInvalidLabConfig
		}
		tlsLeaf, parseErr := x509.ParseCertificate(certificate.Certificate[0])
		artifactPublic, keyErr := artifactCertificatePublicKey(config.ArtifactKeyCertificate)
		if parseErr != nil || keyErr != nil {
			return nil, ErrInvalidLabConfig
		}
		artifactSPKI, artifactErr := x509.MarshalPKIXPublicKey(artifactPublic)
		if artifactErr != nil {
			return nil, ErrInvalidLabConfig
		}
		for _, channelKey := range []any{tlsLeaf.PublicKey, ca.PublicKey, csr.PublicKey} {
			channelSPKI, marshalErr := x509.MarshalPKIXPublicKey(channelKey)
			if marshalErr != nil || bytes.Equal(channelSPKI, artifactSPKI) {
				return nil, ErrInvalidLabConfig
			}
		}
		if err = config.Store.EnsureArtifactKey(context.Background(), profile.domainID, config.ArtifactKeyCertificate, time.Now().UTC()); err != nil {
			return nil, err
		}
	} else if len(config.ArtifactKeyCertificate) != 0 || config.SignArtifact != nil {
		return nil, ErrInvalidLabConfig
	}
	if m6 {
		// The detached operation has no generic Registry.Dispatch handler. Its
		// capability is selected only with v2 and its durable private seam owns
		// proof admission, factual guards, terminal witnesses, and receipts.
		operations = append(operations, operation.GateExemptionRepairV1)
	}
	app := &m5LabHandler{
		profile: profile, store: config.Store, repoID: config.RepoID,
		grant: bytes.Clone(config.EnrollmentGrant), csrDER: bytes.Clone(config.ExpectedCSRDER),
		caDER: bytes.Clone(config.EnvironmentCACertificateDER), signLeaf: config.SignEnvironmentLeaf, m6: m6,
		registry: config.Registry, operations: operations, sign: config.SignArtifact,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", serveHealth)
	mux.HandleFunc(labEnrollPath, app.serveEnroll)
	mux.HandleFunc(labNegotiatePath, app.serveNegotiate)
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
	defer func() { _ = request.Body.Close() }()
	body := bufio.NewReader(request.Body)
	environment, err := app.authenticateEnvironment(request)
	if err != nil {
		writeLabProblem(writer, "00000000000000000000000000", "auth.environment-domain-mismatch")
		return
	}
	frame, err := wipdwire.ReadFrame(body)
	if err != nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	session := connectionSession(request)
	if session == nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	session.mu.Lock()
	negotiated := session.negotiated && !session.failed && !session.negotiating
	if !negotiated {
		session.failed = true
	}
	session.mu.Unlock()
	if !negotiated {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	if frame.Sequence != 0 {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	kind := ""
	var start authoritystore.PrefixAnchor
	switch frame.Kind {
	case "seed.request":
		var seed wipdwire.SeedRequest
		if err = wipdwire.DecodeCanonical(frame.Payload, &seed, "schema", "domain_id", "expected_epoch", "store_schema", "resume_token"); err != nil ||
			seed.Schema != "wipd.seed-request/1" || seed.DomainID != app.profile.domainID || seed.Epoch != app.profile.epoch ||
			seed.StoreSchema != "wipd.store/1" || seed.ResumeToken != nil {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		kind = "seed"
		start = authoritystore.EmptyPrefixAnchor()
	case "pull.request":
		fields, decodeErr := wipdwire.DecodeCanonicalMap(frame.Payload, "schema", "domain_id", "expected_epoch", "installed", "resume_token")
		installed, installedOK := fields["installed"].(map[string]any)
		var pull wipdwire.PullRequest
		if decodeErr != nil || !installedOK || !wipdwire.ExactMapKeys(installed, "event_count", "high_water_event_id", "prefix_digest") ||
			wipdwire.DecodeCanonical(frame.Payload, &pull, "schema", "domain_id", "expected_epoch", "installed", "resume_token") != nil ||
			pull.Schema != "wipd.pull-request/1" || pull.DomainID != app.profile.domainID || pull.Epoch != app.profile.epoch || pull.ResumeToken != nil {
			writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
			return
		}
		kind = "pull"
		start = authorityAnchor(pull.Installed)
	case "command.submit":
		if app.registry == nil {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveCommandSubmit(writer, request, body, frame, environment)
		return
	case "receipt.query":
		if app.registry == nil {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveReceiptQuery(writer, request, body, frame, environment)
		return
	case "birth-journal.ack":
		if app.registry == nil {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveBirthJournalAck(writer, request, body, frame, environment)
		return
	case "claim-journal.query":
		app.serveClaimJournalQuery(writer, request, body, frame, environment)
		return
	case "claim-journal.ack":
		app.serveClaimJournalAck(writer, request, body, frame, environment)
		return
	case "claim-journal.seal":
		app.serveClaimJournalSeal(writer, request, body, frame, environment)
		return
	case "claim.release":
		if app.registry == nil || app.sign == nil {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveBirthClaimRelease(writer, request, body, frame, environment)
		return
	case "claim.acquire":
		if app.registry == nil || app.sign == nil || app.operations == nil {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveClaimAcquire(writer, request, body, frame, environment)
		return
	case "blob.upload-start", "blob.upload-chunk", "blob.upload-finish":
		if app.registry == nil || !contentUploadNegotiated(request) {
			writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
			return
		}
		app.serveBlobUpload(writer, request, body, frame)
		return
	default:
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
		return
	}
	app.serveTransferExchange(writer, request, body, frame, kind, start)
}

func (app *m5LabHandler) serveNegotiate(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != labNegotiatePath || request.Method != http.MethodPost || request.URL.RawQuery != "" || request.URL.ForceQuery || !allowedLabHTTP(request) {
		http.NotFound(writer, request)
		return
	}
	if !hasContentType(request, "application/cbor") {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if _, err := app.authenticateEnvironment(request); err != nil {
		writeLabProblem(writer, "00000000000000000000000000", "auth.environment-domain-mismatch")
		return
	}
	session := connectionSession(request)
	if session == nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	session.mu.Lock()
	if session.negotiating || session.negotiated || session.failed {
		session.failed = true
		session.mu.Unlock()
		writeLabProblem(writer, "00000000000000000000000000", "protocol.out-of-order")
		return
	}
	session.negotiating = true
	session.mu.Unlock()
	succeeded := false
	defer func() {
		session.mu.Lock()
		session.negotiating = false
		if !succeeded {
			session.failed = true
		}
		session.mu.Unlock()
	}()
	frame, err := readSingleFrame(request.Body)
	if err != nil {
		http.Error(writer, "bad request", http.StatusBadRequest)
		return
	}
	if frame.Sequence != 0 || frame.Kind != "client.hello" {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	hello, parameters, operations, problem := negotiateLab(frame.Payload, app.operations)
	if problem != "" {
		writeLabProblem(writer, frame.RequestID, problem)
		return
	}
	selection, err := wipdwire.DecodeCanonicalMap(hello,
		"selected_protocol", "identity_schemas", "operations", "store_schemas", "features")
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
		return
	}
	selectedFeatures, _ := sortedStrings(selection["features"])
	first, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: frame.RequestID, Kind: "server.hello", Payload: hello})
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	second, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: frame.RequestID, Sequence: 1, Kind: "session.parameters", Payload: parameters})
	if err != nil || len(first)+len(second) > maxLabResponseBytes {
		http.Error(writer, "internal error", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Cache-Control", "no-store")
	if _, err = writer.Write(first); err != nil {
		return
	}
	if _, err = writer.Write(second); err != nil {
		return
	}
	session.mu.Lock()
	session.operations = operations
	session.commandSubmitV2 = containsString(selectedFeatures, wipdwire.CommandSubmitV2Feature)
	session.negotiated = true
	succeeded = true
	session.mu.Unlock()
}

func (app *m5LabHandler) authenticateEnvironment(request *http.Request) (authoritystore.EnvironmentCertificate, error) {
	if request.TLS == nil || !request.TLS.HandshakeComplete || len(request.TLS.PeerCertificates) != 2 {
		return authoritystore.EnvironmentCertificate{}, errors.New("missing Environment certificate")
	}
	peer := request.TLS.PeerCertificates[0]
	if len(peer.URIs) != 1 || peer.URIs[0] == nil {
		return authoritystore.EnvironmentCertificate{}, errors.New("invalid Environment identity")
	}
	certificate, err := app.store.VerifyEnvironmentPeer(request.Context(), app.profile.domainID, environmentIDFromURI(peer.URIs[0].String()), app.profile.epoch, *request.TLS, time.Now().UTC())
	if err != nil || certificate.DomainID != app.profile.domainID {
		return authoritystore.EnvironmentCertificate{}, errors.New("untrusted Environment certificate")
	}
	if err = app.verifyRoute(request.Context()); err != nil {
		return authoritystore.EnvironmentCertificate{}, err
	}
	return certificate, nil
}

type labFrameRecord struct {
	kind    string
	payload []byte
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
