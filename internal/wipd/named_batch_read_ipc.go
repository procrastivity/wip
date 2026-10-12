package wipd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	namedBatchReadFrameKind     = "batch.read"
	namedBatchReadRequestSchema = "wipd.local-batch-read/1"
)

// ReadNamedBatch sends one authenticated authority-only batch.read@v1 query.
func (c *Client) ReadNamedBatch(ctx context.Context, batchID string, pageSize uint16, pageToken *string) (wipdwire.NamedBatchReadResponse, error) {
	var empty wipdwire.NamedBatchReadResponse
	if c == nil || c.httpClient == nil || ctx == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !c.step9c || !containsString(c.hello.features, wipdwire.NamedBatchReadFeature) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	request := wipdwire.LocalNamedBatchReadRequest{
		Schema: namedBatchReadRequestSchema,
		Query: wipdwire.NamedBatchReadQuery{
			Name: "batch.read", Version: 1,
			Filter: wipdwire.NamedBatchReadFilter{BatchID: batchID},
		},
		PageSize: pageSize, PageToken: pageToken,
	}
	if !commandStartULID.MatchString(batchID) || pageSize == 0 || pageSize > 1000 || pageToken != nil && len(*pageToken) > 16_384 {
		return empty, &ExchangeError{Code: "protocol.malformed-message"}
	}
	payload, err := wipdwire.EncodeCanonical(request)
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: namedBatchReadFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(errors.New("unexpected named-Batch read response status"))
	}
	frame, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody))
	if err != nil || frame.requestID != requestID || frame.sequence != 0 {
		return empty, uncertainExchange(errWrongCorrelation)
	}
	if frame.kind == "problem" {
		code, decodeErr := problemCodeFromPayload(frame.payload)
		if decodeErr != nil {
			return empty, uncertainExchange(decodeErr)
		}
		if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
			return empty, &ExchangeError{Code: code, Err: err}
		}
		return empty, &ExchangeError{Code: code}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	var result wipdwire.NamedBatchReadResponse
	if err = decodeNamedBatchReadResponse(frame.payload, request.Query, pageSize, pageToken != nil, time.Now().UTC(), &result); err != nil {
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func (s *Server) serveNamedBatchRead(writer http.ResponseWriter, request *http.Request, hello serverHello, parameters sessionParameters, frame frameRecord) {
	reader := s.namedBatchReadPath()
	if reader == nil || !containsString(hello.features, wipdwire.NamedBatchReadFeature) {
		s.writeProblem(writer, frame.requestID, 0, "protocol.unsupported-extension", uint32(parameters.maxFrameBody))
		return
	}
	query, err := decodeLocalNamedBatchReadRequest(frame.payload)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, errMalformedMessage.Error(), uint32(parameters.maxFrameBody))
		return
	}
	select {
	case s.exchangeSlots <- struct{}{}:
		defer func() { <-s.exchangeSlots }()
	default:
		s.writeProblem(writer, frame.requestID, 0, "transport.overloaded", uint32(parameters.maxFrameBody))
		return
	}
	result, err := reader.ReadNamedBatch(request.Context(), query)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, namedBatchReadProblem(err), uint32(parameters.maxFrameBody))
		return
	}
	if err = validateNamedBatchReadResponse(result, query.Query, query.PageSize, query.PageToken != nil, time.Now().UTC()); err != nil {
		s.writeProblem(writer, frame.requestID, 0, "authority.unavailable", uint32(parameters.maxFrameBody))
		return
	}
	payload, err := wipdwire.EncodeCanonical(result)
	if err != nil {
		abortHTTP2Stream()
	}
	wire, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload}, uint32(parameters.maxFrameBody))
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, "protocol.frame-too-large", uint32(parameters.maxFrameBody))
		return
	}
	_, _ = writer.Write(wire)
}

func namedBatchReadProblem(err error) string {
	switch err.Error() {
	case "batch.named-not-found", "query.page-token-scope", "query.snapshot-expired", "query.invalid-page-token", "query.invalid-request":
		return err.Error()
	case "protocol.unsupported-extension":
		return err.Error()
	default:
		return "authority.unavailable"
	}
}

func decodeLocalNamedBatchReadRequest(payload []byte) (wipdwire.LocalNamedBatchReadRequest, error) {
	var request wipdwire.LocalNamedBatchReadRequest
	fields, err := wipdwire.DecodeCanonicalMap(payload, "schema", "query", "page_size", "page_token")
	query, queryOK := fields["query"].(map[string]any)
	if err != nil || fields["schema"] != namedBatchReadRequestSchema || !queryOK || !wipdwire.ExactMapKeys(query, "name", "version", "filter") {
		return request, errMalformedMessage
	}
	filter, filterOK := query["filter"].(map[string]any)
	pageSize, sizeOK := fields["page_size"].(uint64)
	tokenValid := fields["page_token"] == nil
	if token, ok := fields["page_token"].(string); ok && len(token) <= 16_384 {
		tokenValid = true
	}
	if !filterOK || !wipdwire.ExactMapKeys(filter, "batch_id") || query["name"] != "batch.read" || query["version"] != uint64(1) ||
		!sizeOK || pageSize == 0 || pageSize > 1000 || !tokenValid {
		return request, errMalformedMessage
	}
	if err = wipdwire.DecodeCanonical(payload, &request, "schema", "query", "page_size", "page_token"); err != nil ||
		request.Schema != namedBatchReadRequestSchema || request.Query.Name != "batch.read" || request.Query.Version != 1 ||
		!commandStartULID.MatchString(request.Query.Filter.BatchID) {
		return wipdwire.LocalNamedBatchReadRequest{}, errMalformedMessage
	}
	return request, nil
}

func validateNamedBatchReadResponse(response wipdwire.NamedBatchReadResponse, query wipdwire.NamedBatchReadQuery, pageSize uint16, continuation bool, now time.Time) error {
	if response.Schema != "wipd.read-response/1" || response.Query.Name != query.Name || response.Query.Version != query.Version ||
		!requestHashPattern.MatchString(response.FilterHash) ||
		!commandStartULID.MatchString(response.Snapshot.ID) || !commandStartULID.MatchString(response.Snapshot.DomainID) || response.Snapshot.Epoch == 0 ||
		response.Snapshot.OverlayPolicy != "folded-only" || !validNamedBatchReadAnchor(response.Snapshot.AsOf) ||
		response.Provenance.Source != "authority" || response.Provenance.AuthorityReachability != "reachable" ||
		response.Provenance.AuthorityAsOf == nil || !sameNamedBatchReadAnchor(*response.Provenance.AuthorityAsOf, response.Snapshot.AsOf) ||
		response.Provenance.LocalBaseAsOf != nil || response.Provenance.PendingJournalCount != 0 ||
		response.Provenance.ProvisionalItemCount != 0 || response.Provenance.QuarantinedItemCount != 0 ||
		response.Provenance.HistoryState != "current" || len(response.Items) == 0 || len(response.Items) > 1000 ||
		!continuation && len(response.Items) > int(pageSize) ||
		response.Complete != (response.NextPageToken == nil) {
		return errMalformedMessage
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.Snapshot.ExpiresAt)
	if err != nil || expiresAt.UTC().Format(time.RFC3339Nano) != response.Snapshot.ExpiresAt || !expiresAt.After(now) {
		return errMalformedMessage
	}
	if response.NextPageToken != nil && *response.NextPageToken == "" {
		return errMalformedMessage
	}
	previousMatterID := ""
	headerSeen := continuation
	for index, item := range response.Items {
		if len(item.Value) == 0 || item.State != "folded" || item.EnvironmentSequence != nil {
			return errMalformedMessage
		}
		if header, decodeErr := wipdwire.DecodeCanonicalMap(item.Value, "kind", "batch_id", "name", "birth_event_id", "dismissed_event_id"); decodeErr == nil {
			batchID, batchOK := header["batch_id"].(string)
			name, nameOK := header["name"].(string)
			birth, birthOK := header["birth_event_id"].(string)
			dismissed, dismissedOK := header["dismissed_event_id"]
			dismissedEventID, dismissedIsString := dismissed.(string)
			if continuation || index != 0 || headerSeen || header["kind"] != "batch" || !batchOK || batchID != query.Filter.BatchID ||
				!nameOK || name == "" || strings.TrimSpace(name) != name || !birthOK || !commandStartULID.MatchString(birth) || !dismissedOK ||
				(dismissed != nil && (!dismissedIsString || dismissedEventID == "" || !commandStartULID.MatchString(dismissedEventID))) {
				return errMalformedMessage
			}
			headerSeen = true
			continue
		}
		membership, decodeErr := wipdwire.DecodeCanonicalMap(item.Value, "kind", "batch_id", "matter_id", "repo_id", "joined_event_id")
		batchID, batchOK := membership["batch_id"].(string)
		matterID, matterOK := membership["matter_id"].(string)
		repoID, repoOK := membership["repo_id"].(string)
		joinedEventID, eventOK := membership["joined_event_id"].(string)
		if decodeErr != nil || membership["kind"] != "membership" || !headerSeen || !batchOK || batchID != query.Filter.BatchID ||
			!matterOK || !commandStartULID.MatchString(matterID) || !repoOK || !commandStartULID.MatchString(repoID) ||
			!eventOK || !commandStartULID.MatchString(joinedEventID) || previousMatterID != "" && matterID <= previousMatterID {
			return errMalformedMessage
		}
		previousMatterID = matterID
	}
	if !headerSeen {
		return errMalformedMessage
	}
	return nil
}

func decodeNamedBatchReadResponse(payload []byte, query wipdwire.NamedBatchReadQuery, pageSize uint16, continuation bool, now time.Time, response *wipdwire.NamedBatchReadResponse) error {
	if wipdwire.DecodeNamedBatchReadResponse(payload, response) != nil {
		return errMalformedMessage
	}
	return validateNamedBatchReadResponse(*response, query, pageSize, continuation, now)
}

func validNamedBatchReadAnchor(anchor wipdwire.PrefixAnchor) bool {
	if !requestHashPattern.MatchString(anchor.Digest) {
		return false
	}
	if anchor.EventCount == 0 {
		digest := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
		return anchor.EventID == nil && anchor.Digest == "sha256:"+hex.EncodeToString(digest[:])
	}
	return anchor.EventID != nil && commandStartULID.MatchString(*anchor.EventID)
}

func sameNamedBatchReadAnchor(left, right wipdwire.PrefixAnchor) bool {
	if left.EventCount != right.EventCount || left.Digest != right.Digest || (left.EventID == nil) != (right.EventID == nil) {
		return false
	}
	return left.EventID == nil || *left.EventID == *right.EventID
}
