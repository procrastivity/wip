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
	birthReleaseRequestSchema = "wipd.local-birth-claim-release/1"
	birthReleaseResultSchema  = "wipd.local-birth-claim-release-result/1"
	claimReleaseFrameKind     = "claim.release"
)

// BirthClaimReleaseResponse is returned only after the coordinator has
// installed the exact terminal receipt and verified authority tail locally.
// A transport failure after dispatch remains outcome-unknown; callers retry
// with the same Matter and command IDs.
type BirthClaimReleaseResponse struct {
	CommandID string
	Code      operation.ResultCode
	Receipt   []byte
}

func (s *Server) supportsBirthClaimRelease() bool {
	coordinator := s.connectedCommandStart()
	if coordinator == nil {
		return false
	}
	_, ok := coordinator.authority.(BirthReleaseAuthority)
	return ok
}

func (s *Server) serveBirthClaimRelease(
	writer http.ResponseWriter,
	request *http.Request,
	state *connectionSession,
	hello serverHello,
	parameters sessionParameters,
	frame frameRecord,
) {
	if !containsString(hello.features, birthReleaseFeature) || !s.supportsBirthClaimRelease() {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
		return
	}
	matterID, commandID, actor, err := decodeBirthClaimReleaseRequest(frame.payload)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, errMalformedMessage.Error(), uint32(parameters.maxFrameBody))
		return
	}
	select {
	case s.exchangeSlots <- struct{}{}:
	default:
		s.writeProblem(writer, frame.requestID, 0, "transport.overloaded", uint32(parameters.maxFrameBody))
		return
	}
	slotOwnedByDispatch := false
	defer func() {
		if !slotOwnedByDispatch {
			<-s.exchangeSlots
		}
	}()

	preSubmissionContext, gate, stopGate := newBirthReleaseBoundary(request.Context(), state.ctx)
	defer stopGate()
	dispatchDone := make(chan struct{})
	dispatchOutcomes := make(chan birthReleaseDispatchOutcome, 1)
	coordinator := s.connectedCommandStart()
	go func() {
		defer close(dispatchDone)
		result, releaseErr := coordinator.releaseBirthClaim(preSubmissionContext, state.ctx, gate, matterID, commandID, actor)
		if releaseErr != nil {
			switch {
			case errors.Is(releaseErr, ErrBirthReleaseCancelled):
				dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "transport.cancelled-before-submission"}
			case errors.Is(releaseErr, ErrCommandStartBlocked), errors.Is(releaseErr, wipdjournal.ErrBirthBarrierIncomplete):
				dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "claim.release-barrier-incomplete"}
			case errors.Is(releaseErr, wipdjournal.ErrCommandIDConflict):
				dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "command.id-conflict"}
			case errors.Is(releaseErr, context.Canceled), errors.Is(releaseErr, context.DeadlineExceeded):
				dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "transport.outcome-unknown"}
			default:
				// The authority may have committed before a later pull/install
				// failure. Only an exact-ID retry can safely resolve this state.
				dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "transport.outcome-unknown"}
			}
			return
		}
		if result.Attempt.ID != commandID || result.Attempt.Barrier.Journal != matterID ||
			result.Code != result.Attempt.ResultCode || len(result.Receipt) == 0 ||
			!bytes.Equal(result.Receipt, result.Attempt.Receipt) {
			dispatchOutcomes <- birthReleaseDispatchOutcome{problemCode: "transport.outcome-unknown"}
			return
		}
		dispatchOutcomes <- birthReleaseDispatchOutcome{result: result}
	}()
	transferSlotToDispatch := func() {
		slotOwnedByDispatch = true
		go func() {
			<-dispatchDone
			<-s.exchangeSlots
		}()
	}
	nextFrame := make(chan frameReadResult, 1)
	go func() {
		next, readErr := readFrame(request.Body, uint32(parameters.maxFrameBody))
		nextFrame <- frameReadResult{frame: next, err: readErr}
	}()

	for {
		select {
		case incoming := <-nextFrame:
			if errors.Is(incoming.err, io.EOF) {
				nextFrame = nil
				continue
			}
			if incoming.err != nil {
				gate.cancelBeforeSubmission()
				transferSlotToDispatch()
				abortHTTP2Stream()
			}
			if validateCancelFrame(frame, incoming.frame) != nil {
				gate.cancelBeforeSubmission()
				transferSlotToDispatch()
				abortHTTP2Stream()
			}
			if gate.cancelBeforeSubmission() {
				transferSlotToDispatch()
				s.writeProblem(writer, frame.requestID, 0, "transport.cancelled-before-submission", uint32(parameters.maxFrameBody))
				return
			}
			transferSlotToDispatch()
			abortHTTP2Stream()
		case <-request.Context().Done():
			gate.cancelBeforeSubmission()
			transferSlotToDispatch()
			return
		case outcome := <-dispatchOutcomes:
			if outcome.problemCode != "" {
				s.writeProblem(writer, frame.requestID, 0, outcome.problemCode, uint32(parameters.maxFrameBody))
				return
			}
			select {
			case incoming := <-nextFrame:
				if !errors.Is(incoming.err, io.EOF) {
					transferSlotToDispatch()
					abortHTTP2Stream()
				}
				nextFrame = nil
			default:
			}
			payload, encodeErr := encodePayload(map[string]any{
				"schema":           birthReleaseResultSchema,
				"command_id":       commandID,
				"result_code":      string(outcome.result.Code),
				"terminal_receipt": outcome.result.Receipt,
			})
			if encodeErr != nil {
				abortHTTP2Stream()
			}
			wire, encodeErr := encodeFrame(frameRecord{
				requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload,
			}, uint32(parameters.maxFrameBody))
			if encodeErr != nil {
				abortHTTP2Stream()
			}
			_, _ = writer.Write(wire)
			return
		}
	}
}

type birthReleaseDispatchOutcome struct {
	result      BirthClaimReleaseResult
	problemCode string
}

func decodeBirthClaimReleaseRequest(payload []byte) (matterID, commandID string, actor operation.Actor, err error) {
	value, err := decodePayload(payload)
	if err != nil {
		return "", "", "", err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "schema", "matter_id", "command_id", "actor") ||
		fields["schema"] != birthReleaseRequestSchema {
		return "", "", "", errMalformedMessage
	}
	matterID, matterOK := fields["matter_id"].(string)
	commandID, commandOK := fields["command_id"].(string)
	actorText, actorOK := fields["actor"].(string)
	if !matterOK || !commandOK || !actorOK || !commandStartULID.MatchString(matterID) ||
		!commandStartULID.MatchString(commandID) || actorText == "" {
		return "", "", "", errMalformedMessage
	}
	return matterID, commandID, operation.Actor(actorText), nil
}

// ReleaseBirthClaim runs the configured command-start coordinator. The local
// peer must have negotiated the birth-release feature; a default or fixture
// daemon cannot acquire an authority-backed release route accidentally.
func (c *Client) ReleaseBirthClaim(ctx context.Context, matterID, commandID string, actor operation.Actor) (BirthClaimReleaseResponse, error) {
	var empty BirthClaimReleaseResponse
	if c == nil || c.httpClient == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !containsString(c.hello.features, birthReleaseFeature) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	if !commandStartULID.MatchString(matterID) || !commandStartULID.MatchString(commandID) ||
		actor == "" {
		return empty, &ExchangeError{Code: "protocol.malformed-message"}
	}
	payload, err := encodePayload(map[string]any{
		"schema": birthReleaseRequestSchema, "matter_id": matterID,
		"command_id": commandID, "actor": string(actor),
	})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: claimReleaseFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(errors.New("unexpected local release response status"))
	}
	frame, err := readFrame(response.Body, uint32(c.parameters.maxFrameBody))
	if err != nil || frame.requestID != requestID || frame.sequence != 0 {
		return empty, uncertainExchange(errWrongCorrelation)
	}
	if frame.kind == "problem" {
		code, parseErr := problemCodeFromPayload(frame.payload)
		if parseErr != nil {
			return empty, uncertainExchange(parseErr)
		}
		if _, readErr := readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(readErr, io.EOF) {
			return empty, &ExchangeError{Code: code, Err: errOutOfOrder}
		}
		return empty, &ExchangeError{Code: code, Uncertain: code == "transport.outcome-unknown"}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	result, err := decodeBirthClaimReleaseResponse(frame.payload, matterID, commandID)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func decodeBirthClaimReleaseResponse(payload []byte, matterID, commandID string) (BirthClaimReleaseResponse, error) {
	var empty BirthClaimReleaseResponse
	value, err := decodePayload(payload)
	if err != nil {
		return empty, err
	}
	fields, ok := value.(map[string]any)
	if !ok || !exactFields(fields, "schema", "command_id", "result_code", "terminal_receipt") ||
		fields["schema"] != birthReleaseResultSchema || fields["command_id"] != commandID {
		return empty, errMalformedMessage
	}
	codeText, codeOK := fields["result_code"].(string)
	receipt, receiptOK := fields["terminal_receipt"].([]byte)
	code := operation.ResultCode(codeText)
	if !codeOK || !receiptOK || len(receipt) == 0 ||
		(code != operation.ResultSucceeded && code != operation.ResultRejected && code != operation.ResultRefused && code != operation.ResultFailed) {
		return empty, errMalformedMessage
	}
	receiptFields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receiptFields["schema"] != "wipd.terminal-receipt/1" || receiptFields["command_id"] != commandID ||
		receiptFields["identity_schema"] != identitySchemaV1 || !requestHashPattern.MatchString(ipcString(receiptFields["request_hash"])) ||
		!commandStartULID.MatchString(ipcString(receiptFields["domain_id"])) {
		return empty, errMalformedMessage
	}
	if epoch, ok := receiptFields["authority_epoch"].(uint64); !ok || epoch == 0 {
		return empty, errMalformedMessage
	}
	operationFields, ok := receiptFields["operation"].(map[string]any)
	if !ok || !exactFields(operationFields, "name", "version") || operationFields["name"] != "claim.release" || operationFields["version"] != uint64(1) {
		return empty, errMalformedMessage
	}
	environment, ok := receiptFields["environment"].(map[string]any)
	if !ok || !exactFields(environment, "id", "sequence") || !commandStartULID.MatchString(ipcString(environment["id"])) {
		return empty, errMalformedMessage
	}
	if sequence, ok := environment["sequence"].(uint64); !ok || sequence == 0 {
		return empty, errMalformedMessage
	}
	result, ok := receiptFields["result"].(map[string]any)
	if !ok || !exactFields(result, "code", "output", "problem_code") || result["code"] != codeText {
		return empty, errMalformedMessage
	}
	if code == operation.ResultSucceeded {
		if result["problem_code"] != nil || receiptFields["accepted_events"] == nil {
			return empty, errMalformedMessage
		}
		output, ok := result["output"].([]byte)
		if !ok {
			return empty, errMalformedMessage
		}
		outputFields, outputErr := wipdwire.DecodeCanonicalMap(output, "claim_id", "claim_epoch", "dispatch_id", "barrier_digest")
		if outputErr != nil || outputFields["claim_id"] != matterID || outputFields["claim_epoch"] != uint64(1) || outputFields["dispatch_id"] != nil ||
			!requestHashPattern.MatchString(ipcString(outputFields["barrier_digest"])) {
			return empty, errMalformedMessage
		}
		events, ok := receiptFields["accepted_events"].(map[string]any)
		if !ok || !exactFields(events, "first_event_id", "last_event_id", "event_count") ||
			!commandStartULID.MatchString(ipcString(events["first_event_id"])) || events["first_event_id"] != events["last_event_id"] || events["event_count"] != uint64(1) {
			return empty, errMalformedMessage
		}
	} else {
		problem, ok := result["problem_code"].(string)
		if result["output"] != nil || !ok || problem == "" || receiptFields["accepted_events"] != nil {
			return empty, errMalformedMessage
		}
	}
	return BirthClaimReleaseResponse{CommandID: commandID, Code: code, Receipt: append([]byte(nil), receipt...)}, nil
}

func ipcString(value any) string {
	text, _ := value.(string)
	return text
}
