package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestPrebindingRouteResolveFreshMissIsRepoIndependentAndReadOnly(t *testing.T) {
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step11Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	server, err := NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatalf("construct M6 command and route-query server: %v", err)
	}

	key := "github.com/acme/unbound"
	before, beforeAnchor, err := fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, m5TestDomain, 1)
	if err != authoritystore.ErrRouteUnbound || before.RepoID != "" || beforeAnchor.Digest == "" {
		t.Fatalf("initial route snapshot = %+v, %+v, %v", before, beforeAnchor, err)
	}
	session := &labConnectionSession{
		negotiated: true, commandSubmitV2: true,
		operations: map[operation.ID]struct{}{
			operation.RouteResolveV1.Metadata().Operation: {},
			operation.MatterCreateV1.Metadata().Operation: {},
		},
	}
	filter := map[string]any{"normalized_key": key}
	filterBytes, err := wipdwire.EncodeCanonical(filter)
	if err != nil {
		t.Fatal(err)
	}
	queryPayload, err := wipdwire.EncodeCanonical(wipdwire.ReadQuery{
		Schema: "wipd.read-query/1", DomainID: m5TestDomain, ExpectedEpoch: 1,
		Source: "authority", Filter: filter, OverlayPolicy: "folded-only", PageSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestPayload, err := wipdwire.EncodeCanonical(wipdwire.QueryRequest{
		Schema: "wipd.query-request/1", Query: wipdwire.QueryOperation{Name: "route.resolve", Version: 1},
		QueryPayload: queryPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := randomULID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := wipdwire.EncodeFrame(wipdwire.Frame{
		RequestID: requestID, Kind: "query.request", Payload: requestPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, labExchangePath, bytes.NewReader(requestBytes))
	request.Header.Set("Content-Type", "application/cbor")
	request.TLS = &fixture.peer
	request = request.WithContext(context.WithValue(request.Context(), labSessionContextKey{}, session))
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	frames, err := wipdwire.ReadFrames(response.Body.Bytes(), 2)
	if err != nil || len(frames) != 1 || frames[0].Kind != "response.end" || frames[0].RequestID != requestID {
		t.Fatalf("route query response = %+v, err=%v, status=%d body=%q", frames, err, response.Code, response.Body.String())
	}
	var readResponse wipdwire.NamedBatchReadResponse
	if err = wipdwire.DecodeNamedBatchReadResponse(frames[0].Payload, &readResponse); err != nil {
		t.Fatal(err)
	}
	if len(readResponse.Items) != 0 || !readResponse.Complete || readResponse.FilterHash != "sha256:"+hex.EncodeToString(sha256Sum(filterBytes)) {
		t.Fatalf("fresh route miss response = %+v", readResponse)
	}
	after, afterAnchor, err := fixture.store.LookupRepoRouteSnapshotForDomain(context.Background(), key, m5TestDomain, 1)
	if err != authoritystore.ErrRouteUnbound || after.RepoID != "" || afterAnchor != beforeAnchor {
		t.Fatalf("route miss changed durable state: binding=%+v anchor=%+v before=%+v err=%v", after, afterAnchor, beforeAnchor, err)
	}
	command := m5Command("01KZ7XHAQT1S46NYPN1PW1DX3F", 1, "step11-same-server-command")
	response, frames = m5SubmitWithApp(t, server.http.Handler, session, fixture.peer, command)
	if response.Code != http.StatusOK || len(frames) != 2 || frames[0].Kind != "submission.accepted" || frames[1].Kind != "command.terminal" {
		t.Fatalf("M2 command.submit on same M6 route server returned status=%d frames=%+v", response.Code, frames)
	}
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
