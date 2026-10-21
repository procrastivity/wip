package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// M6PrebindingLabConfig configures the disposable Repo-independent D117
// enrollment and route.resolve read boundary. It is never used by production
// daemon startup.
type M6PrebindingLabConfig struct {
	Store                       *authoritystore.Store
	EnrollmentGrant             []byte
	ExpectedCSRDER              []byte
	EnvironmentCACertificateDER []byte
	SignEnvironmentLeaf         EnvironmentLeafSigner
	AfterEnrollmentCommit       func(context.Context)
}

// NewM6PrebindingLabServer exposes only D117 enrollment and capability-
// negotiated route.resolve over a domain-scoped Environment certificate. It
// does not require or open a Repo membership at bootstrap.
func NewM6PrebindingLabServer(profile Profile, certificate tls.Certificate, config M6PrebindingLabConfig) (*Server, error) {
	if config.Store == nil || len(config.EnrollmentGrant) == 0 || len(config.EnrollmentGrant) > 4096 ||
		len(config.ExpectedCSRDER) == 0 || len(config.ExpectedCSRDER) > 16_384 ||
		len(config.EnvironmentCACertificateDER) == 0 || config.SignEnvironmentLeaf == nil || profile.M5LabRepoID() != "" {
		return nil, ErrInvalidLabConfig
	}
	csr, err := x509.ParseCertificateRequest(config.ExpectedCSRDER)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(config.ExpectedCSRDER, csr.Raw) {
		return nil, ErrInvalidLabConfig
	}
	ca, err := x509.ParseCertificate(config.EnvironmentCACertificateDER)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || !ca.MaxPathLenZero || ca.MaxPathLen != 0 || ca.KeyUsage != x509.KeyUsageCertSign {
		return nil, ErrInvalidLabConfig
	}
	domain, err := config.Store.LookupDomain(context.Background(), profile.domainID)
	if err != nil || domain.ActiveEpoch != profile.epoch || domain.OwnerKeyID != profile.OwnerRootSPKI() {
		return nil, ErrAuthorityBindingMismatch
	}
	app := &m5LabHandler{
		profile: profile, store: config.Store, grant: bytes.Clone(config.EnrollmentGrant),
		csrDER: bytes.Clone(config.ExpectedCSRDER), caDER: bytes.Clone(config.EnvironmentCACertificateDER),
		signLeaf: config.SignEnvironmentLeaf, afterEnrollmentCommit: config.AfterEnrollmentCommit,
		operations: []operation.Definition{operation.RouteResolveV1}, m6: true, prebinding: true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", serveHealth)
	mux.HandleFunc(labEnrollPath, app.serveEnroll)
	mux.HandleFunc(labNegotiatePath, app.serveNegotiate)
	mux.HandleFunc(labExchangePath, app.serveExchange)
	return newServer(profile, certificate, mux, true)
}

func (app *m5LabHandler) servePrebindingRouteQuery(writer http.ResponseWriter, request *http.Request, frame wipdwire.Frame, session *labConnectionSession) {
	session.mu.Lock()
	_, selected := session.operations[operation.RouteResolveV1.Metadata().Operation]
	session.mu.Unlock()
	if !selected || frame.Kind != "query.request" {
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-kind")
		return
	}
	var envelope wipdwire.QueryRequest
	if wipdwire.DecodeCanonical(frame.Payload, &envelope, "schema", "query", "query_payload", "deadline") != nil ||
		envelope.Schema != "wipd.query-request/1" || envelope.Query.Name != "route.resolve" || envelope.Query.Version != 1 || envelope.Deadline != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	var query wipdwire.ReadQuery
	if wipdwire.DecodeCanonical(envelope.QueryPayload, &query,
		"schema", "domain_id", "expected_epoch", "source", "filter", "overlay_policy", "page_size", "page_token") != nil ||
		query.Schema != "wipd.read-query/1" || query.DomainID != app.profile.domainID || query.ExpectedEpoch != app.profile.epoch ||
		query.Source != "authority" || query.OverlayPolicy != "folded-only" || query.PageSize != 1 || query.PageToken != nil ||
		!wipdwire.ExactMapKeys(query.Filter, "normalized_key") {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	normalizedKey, ok := query.Filter["normalized_key"].(string)
	if !ok {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	binding, anchor, lookupErr := app.store.LookupRepoRouteSnapshotForDomain(request.Context(), normalizedKey, app.profile.domainID, app.profile.epoch)
	if lookupErr != nil && !errors.Is(lookupErr, authoritystore.ErrRouteUnbound) {
		code := "query.route-unresolved"
		if !errors.Is(lookupErr, authoritystore.ErrRouteDomainMismatch) {
			code = "authority.unavailable"
		}
		writeLabProblem(writer, frame.RequestID, code)
		return
	}
	bound := lookupErr == nil
	filterBytes, err := wipdwire.EncodeCanonical(query.Filter)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	filterDigest := sha256.Sum256(filterBytes)
	snapshotID, err := randomULID(time.Now().UTC())
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
		return
	}
	var highWater *string
	if anchor.EventID != "" {
		eventID := anchor.EventID
		highWater = &eventID
	}
	wireAnchor := wipdwire.PrefixAnchor{EventCount: anchor.EventCount, EventID: highWater, Digest: anchor.Digest}
	response := wipdwire.NamedBatchReadResponse{
		Schema: "wipd.read-response/1", Query: wipdwire.NamedBatchReadIdentity{Name: "route.resolve", Version: 1},
		FilterHash: "sha256:" + hex.EncodeToString(filterDigest[:]),
		Snapshot: wipdwire.NamedBatchReadSnapshot{
			ID: snapshotID, DomainID: app.profile.domainID, Epoch: app.profile.epoch, AsOf: wireAnchor,
			OverlayPolicy: "folded-only", ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		},
		Provenance: wipdwire.NamedBatchReadProvenance{
			Source: "authority", AuthorityReachability: "reachable", AuthorityAsOf: &wireAnchor,
			PendingJournalCount: 0, ProvisionalItemCount: 0, QuarantinedItemCount: 0, HistoryState: "current",
		},
		Items: []wipdwire.NamedBatchReadItem{}, Complete: true,
	}
	if bound {
		value, encodeErr := wipdwire.EncodeCanonical(map[string]any{
			"repo_id": binding.RepoID, "domain_id": binding.DomainID, "authority_epoch": binding.ActiveEpoch,
		})
		if encodeErr != nil {
			writeLabProblem(writer, frame.RequestID, "authority.unavailable")
			return
		}
		response.Items = []wipdwire.NamedBatchReadItem{{Value: value, State: "folded"}}
	}
	payload, err := wipdwire.EncodeCanonical(response)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "authority.unavailable")
		return
	}
	writeLabFrame(writer, wipdwire.Frame{RequestID: frame.RequestID, Sequence: 0, Kind: "response.end", Payload: payload})
}
