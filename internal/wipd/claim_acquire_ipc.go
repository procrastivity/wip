package wipd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	claimAcquireRequestSchema = "wipd.local-claim-acquire/1"
	claimAcquireResultSchema  = "wipd.local-claim-acquire-result/1"
	claimAcquireFrameKind     = "claim.acquire"
)

// ClaimAcquireResponse reports the exact terminal receipt and installed grant
// descriptor returned by the configured authenticated daemon path.
type ClaimAcquireResponse struct {
	CommandID string
	Code      operation.ResultCode
	Receipt   []byte
	Grant     *wipdjournal.ClaimGrantSummary
	Hydration wipdjournal.ClaimHydration
	Installed wipdwire.PrefixAnchor
}

func (s *Server) supportsClaimAcquire() bool {
	coordinator := s.connectedCommandStart()
	if coordinator == nil {
		return false
	}
	_, ok := coordinator.authority.(ClaimAcquireAuthority)
	return ok
}

func (s *Server) serveClaimAcquire(writer http.ResponseWriter, request *http.Request, hello serverHello, parameters sessionParameters, frame frameRecord) {
	if !containsString(hello.features, claimAcquireFeature) || !s.supportsClaimAcquire() {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
		return
	}
	commandID, matterID, cloneID, worktreeID, dispatchID, actor, err := decodeLocalClaimAcquire(frame.payload)
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
	result, err := s.connectedCommandStart().AcquireClaim(request.Context(), commandID, matterID, cloneID, worktreeID, dispatchID, actor)
	if err != nil {
		code := "authority.unavailable"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "transport.outcome-unknown"
		} else if errors.Is(err, ErrCommandStartBlocked) || errors.Is(err, wipdjournal.ErrInvalidTransfer) {
			code = "command.sequence-blocked"
		} else if errors.Is(err, wipdjournal.ErrCommandIDConflict) {
			code = "command.id-conflict"
		}
		s.writeProblem(writer, frame.requestID, 0, code, uint32(parameters.maxFrameBody))
		return
	}
	var grantValue any
	var hydrationValue any
	if result.Grant != nil {
		grantValue = map[string]any{
			"grant_id":     result.Grant.GrantID,
			"request_hash": result.Grant.RequestHash,
			"claim":        map[string]any{"id": result.Grant.ClaimID, "epoch": result.Grant.ClaimEpoch},
			"matter_id":    result.Grant.MatterID, "batch_id": result.Grant.BatchID, "dispatch_id": result.Grant.DispatchID,
			"as_of": result.Grant.AsOf, "manifest_digest": result.Grant.Manifest,
		}
		hydration, hydrationErr := s.connectedCommandStart().journal.BeginClaimHydration(request.Context(), result.Grant.GrantID)
		if hydrationErr != nil {
			s.writeProblem(writer, frame.requestID, 0, "authority.unavailable", uint32(parameters.maxFrameBody))
			return
		}
		hydrationValue = map[string]any{
			"grant_id": hydration.GrantID, "claim_id": hydration.ClaimID, "claim_epoch": hydration.ClaimEpoch,
			"as_of": hydration.AsOf, "manifest_digest": hydration.ManifestDigest,
			"required_entry_count":        hydration.RequiredEntryCount,
			"verified_pinned_entry_count": hydration.VerifiedPinnedEntryCount, "state": hydration.State,
		}
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": claimAcquireResultSchema, "command_id": commandID, "result_code": string(result.Code),
		"terminal_receipt": result.Receipt, "grant": grantValue, "hydration": hydrationValue,
		"installed_prefix": result.Snapshot.Anchor,
	})
	if err != nil {
		abortHTTP2Stream()
	}
	wire, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload}, uint32(parameters.maxFrameBody))
	if err != nil {
		abortHTTP2Stream()
	}
	_, _ = writer.Write(wire)
}

func decodeLocalClaimAcquire(payload []byte) (string, string, string, string, string, operation.Actor, error) {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "command_id", "matter_id", "clone_id", "worktree_id", "dispatch_id", "actor")
	if err != nil || fields["schema"] != claimAcquireRequestSchema {
		return "", "", "", "", "", "", errMalformedMessage
	}
	commandID, commandOK := fields["command_id"].(string)
	matterID, matterOK := fields["matter_id"].(string)
	cloneID, cloneOK := fields["clone_id"].(string)
	worktreeID, worktreeOK := fields["worktree_id"].(string)
	dispatchID, dispatchOK := fields["dispatch_id"].(string)
	actor, actorOK := fields["actor"].(string)
	if !commandOK || !commandStartULID.MatchString(commandID) || !matterOK || !commandStartULID.MatchString(matterID) ||
		!cloneOK || !commandStartULID.MatchString(cloneID) || !worktreeOK || !commandStartULID.MatchString(worktreeID) ||
		!dispatchOK || !commandStartULID.MatchString(dispatchID) || !actorOK || actor == "" {
		return "", "", "", "", "", "", errMalformedMessage
	}
	return commandID, matterID, cloneID, worktreeID, dispatchID, operation.Actor(actor), nil
}

// AcquireClaim runs the configured common connected coordinator. A repeated
// command ID resolves the same durable authority receipt/grant; changed intent
// is rejected before another authority submission.
func (c *Client) AcquireClaim(ctx context.Context, commandID, matterID, cloneID, worktreeID, dispatchID string, actor operation.Actor) (ClaimAcquireResponse, error) {
	var empty ClaimAcquireResponse
	if c == nil || c.httpClient == nil || ctx == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !containsString(c.hello.features, claimAcquireFeature) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": claimAcquireRequestSchema, "command_id": commandID, "matter_id": matterID,
		"clone_id": cloneID, "worktree_id": worktreeID, "dispatch_id": dispatchID, "actor": string(actor),
	})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: claimAcquireFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != 200 {
		return empty, uncertainExchange(errors.New("unexpected local claim-acquire response status"))
	}
	frame, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, uncertainExchange(err)
	}
	if frame.requestID != requestID || frame.sequence != 0 {
		return empty, uncertainExchange(errWrongCorrelation)
	}
	if frame.kind == "problem" {
		code, decodeErr := problemCodeFromPayload(frame.payload)
		if decodeErr != nil {
			return empty, uncertainExchange(decodeErr)
		}
		return empty, &ExchangeError{Code: code}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	result, err := decodeLocalClaimAcquireResult(frame.payload, commandID)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	if result.Grant != nil && (result.Grant.MatterID != matterID || result.Grant.DispatchID != dispatchID ||
		!sameCommandStartAnchor(result.Installed, result.Grant.AsOf)) {
		return empty, uncertainExchange(errWrongCorrelation)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func decodeLocalClaimAcquireResult(payload []byte, commandID string) (ClaimAcquireResponse, error) {
	var response ClaimAcquireResponse
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "command_id", "result_code", "terminal_receipt", "grant", "hydration", "installed_prefix")
	if err != nil || fields["schema"] != claimAcquireResultSchema || fields["command_id"] != commandID {
		return response, errMalformedMessage
	}
	code, ok := fields["result_code"].(string)
	receipt, receiptOK := fields["terminal_receipt"].([]byte)
	if !ok || (code != string(operation.ResultSucceeded) && code != string(operation.ResultRejected) && code != string(operation.ResultRefused) && code != string(operation.ResultFailed)) || !receiptOK || len(receipt) == 0 {
		return response, errMalformedMessage
	}
	installed, anchorOK := decodeClaimAcquireAnchor(fields["installed_prefix"])
	if !anchorOK {
		return response, errMalformedMessage
	}
	response.CommandID, response.Code, response.Receipt, response.Installed = commandID, operation.ResultCode(code), bytes.Clone(receipt), installed
	if code != string(operation.ResultSucceeded) {
		if fields["grant"] != nil || fields["hydration"] != nil {
			return ClaimAcquireResponse{}, errMalformedMessage
		}
		return response, nil
	}
	grantFields, ok := fields["grant"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(grantFields, "grant_id", "request_hash", "claim", "matter_id", "batch_id", "dispatch_id", "as_of", "manifest_digest") {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	grantID, grantOK := grantFields["grant_id"].(string)
	matterID, matterOK := grantFields["matter_id"].(string)
	batchID, batchOK := grantFields["batch_id"].(string)
	dispatchID, dispatchOK := grantFields["dispatch_id"].(string)
	manifest, manifestOK := grantFields["manifest_digest"].(string)
	claimFields, claimOK := grantFields["claim"].(map[string]any)
	if !grantOK || !matterOK || !batchOK || !dispatchOK || !manifestOK || !claimOK || !wipdwire.ExactMapKeys(claimFields, "id", "epoch") {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	claimID, claimIDOK := claimFields["id"].(string)
	claimEpoch, claimEpochOK := claimFields["epoch"].(uint64)
	grantAnchor, anchorOK := decodeClaimAcquireAnchor(grantFields["as_of"])
	if !claimIDOK || !commandStartULID.MatchString(claimID) || !claimEpochOK || claimEpoch == 0 || !anchorOK ||
		!grantOK || !commandStartULID.MatchString(grantID) || !matterOK || !commandStartULID.MatchString(matterID) ||
		!batchOK || !commandStartULID.MatchString(batchID) || !dispatchOK || !commandStartULID.MatchString(dispatchID) ||
		!manifestOK || !commandStartHash.MatchString(manifest) {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	hydrationFields, ok := fields["hydration"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(hydrationFields, "grant_id", "claim_id", "claim_epoch", "as_of", "manifest_digest", "required_entry_count", "verified_pinned_entry_count", "state") ||
		hydrationFields["grant_id"] != grantID || hydrationFields["claim_id"] != claimID || hydrationFields["claim_epoch"] != claimEpoch ||
		hydrationFields["manifest_digest"] != manifest || hydrationFields["state"] != "offline-ready" && hydrationFields["state"] != "hydrating" {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	hydrationAnchor, hydrationAnchorOK := decodeClaimAcquireAnchor(hydrationFields["as_of"])
	requiredCount, requiredOK := hydrationFields["required_entry_count"].(uint64)
	pinnedCount, pinnedOK := hydrationFields["verified_pinned_entry_count"].(uint64)
	if !hydrationAnchorOK || !sameCommandStartAnchor(grantAnchor, hydrationAnchor) || !requiredOK || !pinnedOK || pinnedCount > requiredCount ||
		(hydrationFields["state"] == "offline-ready") != (pinnedCount == requiredCount) {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	requestHash, requestHashOK := grantFields["request_hash"].(string)
	if !requestHashOK || !commandStartHash.MatchString(requestHash) {
		return ClaimAcquireResponse{}, errMalformedMessage
	}
	response.Grant = &wipdjournal.ClaimGrantSummary{
		GrantID: grantID, CommandID: commandID, RequestHash: requestHash,
		ClaimID: claimID, ClaimEpoch: claimEpoch, MatterID: matterID, BatchID: batchID,
		DispatchID: dispatchID, AsOf: grantAnchor, Manifest: manifest,
	}
	response.Hydration = wipdjournal.ClaimHydration{
		GrantID: grantID, ClaimID: claimID, ClaimEpoch: claimEpoch, AsOf: hydrationAnchor,
		ManifestDigest: manifest, RequiredEntryCount: requiredCount, VerifiedPinnedEntryCount: pinnedCount,
		State: hydrationFields["state"].(string),
	}
	return response, nil
}

func decodeClaimAcquireAnchor(value any) (wipdwire.PrefixAnchor, bool) {
	fields, ok := value.(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(fields, "event_count", "high_water_event_id", "prefix_digest") {
		return wipdwire.PrefixAnchor{}, false
	}
	count, countOK := fields["event_count"].(uint64)
	digest, digestOK := fields["prefix_digest"].(string)
	if !countOK || !digestOK || !commandStartHash.MatchString(digest) {
		return wipdwire.PrefixAnchor{}, false
	}
	var eventID *string
	if fields["high_water_event_id"] != nil {
		id, idOK := fields["high_water_event_id"].(string)
		if !idOK || !commandStartULID.MatchString(id) {
			return wipdwire.PrefixAnchor{}, false
		}
		eventID = &id
	}
	if (count == 0) != (eventID == nil) {
		return wipdwire.PrefixAnchor{}, false
	}
	return wipdwire.PrefixAnchor{EventCount: count, EventID: eventID, Digest: digest}, true
}
