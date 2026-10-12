package wipdauthority

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveNamedBatchRead(writer http.ResponseWriter, request *http.Request, body *bufio.Reader, frame wipdwire.Frame) {
	query, err := decodeAuthorityNamedBatchReadRequest(frame.Payload)
	if err != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	if query.DomainID != app.profile.domainID || query.Epoch != app.profile.epoch {
		writeLabProblem(writer, frame.RequestID, "authority.epoch-fenced")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	pageToken := ""
	if query.PageToken != nil {
		pageToken = *query.PageToken
	}
	page, err := app.store.ReadNamedBatch(ctx, query.DomainID, query.Epoch, query.Query.Filter.BatchID,
		query.PageSize, pageToken, time.Now().UTC())
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, namedBatchAuthorityProblem(err))
		return
	}
	response := namedBatchReadResponse(query, page)
	payload, err := wipdwire.EncodeCanonical(response)
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
		return
	}
	encoded, err := wipdwire.EncodeFrame(wipdwire.Frame{RequestID: frame.RequestID, Kind: "batch.read.page", Payload: payload})
	if err != nil || len(encoded) > wipdwire.FrameLimit+4 {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "query.resource-limit")
		return
	}
	emitted := writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "batch.read.page", Payload: payload}, "")
	if !emitted {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), time.Second)
		defer releaseCancel()
		_ = app.store.ReleaseSnapshot(releaseCtx, query.DomainID, query.Epoch, page.SnapshotID)
	}
}

func decodeAuthorityNamedBatchReadRequest(payload []byte) (wipdwire.AuthorityNamedBatchReadRequest, error) {
	var request wipdwire.AuthorityNamedBatchReadRequest
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "domain_id", "expected_epoch", "query", "source", "overlay_policy", "page_size", "page_token")
	query, queryOK := fields["query"].(map[string]any)
	if err != nil || fields["schema"] != "wipd.authority-batch-read/1" || !queryOK || !wipdwire.ExactMapKeys(query, "name", "version", "filter") {
		return request, errors.New("protocol.malformed-message")
	}
	filter, filterOK := query["filter"].(map[string]any)
	pageSize, sizeOK := fields["page_size"].(uint64)
	tokenValid := fields["page_token"] == nil
	if token, ok := fields["page_token"].(string); ok && len(token) <= 16_384 {
		tokenValid = true
	}
	if !filterOK || !wipdwire.ExactMapKeys(filter, "batch_id") || query["name"] != "batch.read" || query["version"] != uint64(1) ||
		fields["source"] != "authority" || fields["overlay_policy"] != "folded-only" || !sizeOK || pageSize == 0 || pageSize > 1000 || !tokenValid {
		return request, errors.New("protocol.malformed-message")
	}
	if err = wipdwire.DecodeCanonical(payload, &request,
		"schema", "domain_id", "expected_epoch", "query", "source", "overlay_policy", "page_size", "page_token"); err != nil ||
		request.Schema != "wipd.authority-batch-read/1" || request.Query.Name != "batch.read" || request.Query.Version != 1 ||
		request.Source != "authority" || request.OverlayPolicy != "folded-only" || request.Epoch == 0 ||
		!ulidPattern.MatchString(request.DomainID) || !ulidPattern.MatchString(request.Query.Filter.BatchID) {
		return wipdwire.AuthorityNamedBatchReadRequest{}, errors.New("protocol.malformed-message")
	}
	return request, nil
}

func namedBatchReadResponse(request wipdwire.AuthorityNamedBatchReadRequest, page authoritystore.NamedBatchPage) wipdwire.NamedBatchReadResponse {
	anchor := wireAnchor(page.AsOf)
	provenanceAnchor := anchor
	items := make([]wipdwire.NamedBatchReadItem, len(page.Items))
	for index, item := range page.Items {
		items[index] = wipdwire.NamedBatchReadItem{Value: item.Value, State: "folded"}
	}
	var nextPageToken *string
	if page.NextPageToken != "" {
		nextPageToken = &page.NextPageToken
	}
	return wipdwire.NamedBatchReadResponse{
		Schema: "wipd.read-response/1",
		Query:  wipdwire.NamedBatchReadIdentity{Name: request.Query.Name, Version: request.Query.Version}, FilterHash: page.FilterHash,
		Snapshot: wipdwire.NamedBatchReadSnapshot{
			ID: page.SnapshotID, DomainID: page.DomainID, Epoch: page.Epoch, AsOf: anchor,
			OverlayPolicy: "folded-only", ExpiresAt: page.ExpiresAt.UTC().Format(time.RFC3339Nano),
		},
		Provenance: wipdwire.NamedBatchReadProvenance{
			Source: "authority", AuthorityReachability: "reachable", AuthorityAsOf: &provenanceAnchor,
			PendingJournalCount: 0, ProvisionalItemCount: 0, QuarantinedItemCount: 0, HistoryState: page.HistoryState,
		},
		Items: items, NextPageToken: nextPageToken, Complete: page.Complete,
	}
}

func namedBatchAuthorityProblem(err error) string {
	switch {
	case errors.Is(err, authoritystore.ErrNamedBatchNotFound):
		return "batch.named-not-found"
	case errors.Is(err, authoritystore.ErrPageTokenScope):
		return "query.page-token-scope"
	case errors.Is(err, authoritystore.ErrInvalidPageToken):
		return "query.invalid-page-token"
	case errors.Is(err, authoritystore.ErrSnapshotExpired):
		return "query.snapshot-expired"
	case errors.Is(err, authoritystore.ErrFenced):
		return "authority.epoch-fenced"
	case errors.Is(err, authoritystore.ErrResourceLimit):
		return "query.resource-limit"
	default:
		return "authority.unavailable"
	}
}
