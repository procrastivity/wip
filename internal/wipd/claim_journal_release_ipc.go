package wipd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	claimJournalCloseRequestSchema = "wipd.local-claim-journal-close/1"
	claimJournalCloseResultSchema  = "wipd.local-claim-journal-close-result/1"
	claimJournalCloseFrameKind     = "claim.journal.close"
)

// ClaimJournalCloseResponse reports the exact installed terminal receipt for
// one acquired claim's claim.release@v1 command.
type ClaimJournalCloseResponse struct {
	CommandID string
	Code      operation.ResultCode
	Receipt   []byte
}

func (s *Server) supportsClaimJournalClose() bool {
	coordinator := s.connectedCommandStart()
	if coordinator == nil {
		return false
	}
	authority, ok := coordinator.authority.(ClaimJournalCloseAuthority)
	return ok && authority.SupportsClaimJournalClose()
}

func (s *Server) serveClaimJournalClose(writer http.ResponseWriter, request *http.Request,
	hello serverHello, parameters sessionParameters, frame frameRecord,
) {
	if !containsString(hello.features, claimJournalCloseFeature) || !s.supportsClaimJournalClose() {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
		return
	}
	commandID, claimID, claimEpoch, matterID, dispatchID, actor, err := decodeClaimJournalCloseRequest(frame.payload)
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
	result, err := s.connectedCommandStart().ReleaseClaimJournal(request.Context(), commandID, claimID, claimEpoch, matterID, dispatchID, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug acquired-claim close: %v\n", err)
		code := "transport.outcome-unknown"
		switch {
		case errors.Is(err, errBirthReleaseCancelled):
			code = "transport.cancelled-before-submission"
		case errors.Is(err, ErrCommandStartBlocked), errors.Is(err, wipdjournal.ErrClaimJournalIncomplete),
			errors.Is(err, wipdjournal.ErrClaimNotReady):
			code = "claim.release-barrier-incomplete"
		case errors.Is(err, wipdjournal.ErrCommandIDConflict):
			code = "command.id-conflict"
		case errors.Is(err, wipdjournal.ErrInvalidCommand):
			code = "protocol.malformed-message"
		}
		s.writeProblem(writer, frame.requestID, 0, code, uint32(parameters.maxFrameBody))
		return
	}
	if result.Attempt.ID != commandID || result.Attempt.Binding.ClaimID != claimID || result.Attempt.Binding.ClaimEpoch != claimEpoch ||
		result.Attempt.Binding.MatterID != matterID || result.Attempt.Binding.DispatchID != dispatchID ||
		result.Code != result.Attempt.ResultCode || len(result.Receipt) == 0 {
		s.writeProblem(writer, frame.requestID, 0, "transport.outcome-unknown", uint32(parameters.maxFrameBody))
		return
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": claimJournalCloseResultSchema, "command_id": commandID,
		"result_code": string(result.Code), "terminal_receipt": result.Receipt,
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

func decodeClaimJournalCloseRequest(payload []byte) (string, string, uint64, string, string, operation.Actor, error) {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "command_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id", "actor")
	if err != nil || fields["schema"] != claimJournalCloseRequestSchema {
		return "", "", 0, "", "", "", errMalformedMessage
	}
	commandID, commandOK := fields["command_id"].(string)
	claimID, claimOK := fields["claim_id"].(string)
	claimEpoch, epochOK := fields["claim_epoch"].(uint64)
	matterID, matterOK := fields["matter_id"].(string)
	dispatchID, dispatchOK := fields["dispatch_id"].(string)
	actor, actorOK := fields["actor"].(string)
	if !commandOK || !commandStartULID.MatchString(commandID) || !claimOK || !commandStartULID.MatchString(claimID) ||
		!epochOK || claimEpoch == 0 || !matterOK || !commandStartULID.MatchString(matterID) ||
		!dispatchOK || !commandStartULID.MatchString(dispatchID) || !actorOK || actor == "" {
		return "", "", 0, "", "", "", errMalformedMessage
	}
	return commandID, claimID, claimEpoch, matterID, dispatchID, operation.Actor(actor), nil
}

// ReleaseClaimJournal requests an acquired-claim close. Callers must reuse the
// same command ID after transport uncertainty to resolve the retained outcome.
func (c *Client) ReleaseClaimJournal(ctx context.Context, commandID, claimID string, claimEpoch uint64,
	matterID, dispatchID string, actor operation.Actor,
) (ClaimJournalCloseResponse, error) {
	var empty ClaimJournalCloseResponse
	if c == nil || c.httpClient == nil || ctx == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !containsString(c.hello.features, claimJournalCloseFeature) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": claimJournalCloseRequestSchema, "command_id": commandID, "claim_id": claimID,
		"claim_epoch": claimEpoch, "matter_id": matterID, "dispatch_id": dispatchID, "actor": string(actor),
	})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: claimJournalCloseFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(errors.New("unexpected local claim-journal-close response status"))
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
		return empty, &ExchangeError{Code: code, Uncertain: code == "transport.outcome-unknown"}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	result, err := decodeClaimJournalCloseResult(frame.payload, commandID)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func decodeClaimJournalCloseResult(payload []byte, commandID string) (ClaimJournalCloseResponse, error) {
	fields, err := wipdwire.DecodeCanonicalMap(payload, "schema", "command_id", "result_code", "terminal_receipt")
	if err != nil || fields["schema"] != claimJournalCloseResultSchema || fields["command_id"] != commandID {
		return ClaimJournalCloseResponse{}, errMalformedMessage
	}
	code, codeOK := fields["result_code"].(string)
	receipt, receiptOK := fields["terminal_receipt"].([]byte)
	if !codeOK || !validResultCode(operation.ResultCode(code)) || !receiptOK || len(receipt) == 0 {
		return ClaimJournalCloseResponse{}, errMalformedMessage
	}
	return ClaimJournalCloseResponse{CommandID: commandID, Code: operation.ResultCode(code), Receipt: append([]byte(nil), receipt...)}, nil
}
