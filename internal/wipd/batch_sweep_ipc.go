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
	batchSweepFrameKind     = "batch.sweep.anonymous"
	batchSweepRequestSchema = "wipd.local-batch-sweep-anonymous/1"
	batchSweepResultSchema  = "wipd.local-batch-sweep-anonymous-result/1"
)

// BatchSweepAnonymousResponse is returned only after the exact authority
// terminal receipt and verified tail have been installed in the Environment.
type BatchSweepAnonymousResponse struct {
	CommandID   string
	RequestHash string
	Result      operation.Result
	Receipt     []byte
}

func (s *Server) supportsBatchSweepAnonymous(hello serverHello) bool {
	return operationCapabilityContains(hello.operations,
		operation.BatchSweepAnonymousV1.Metadata().Operation, identitySchemaV1) &&
		s.connectedCommandStart().supportsBatchSweepAnonymous()
}

func (s *Server) serveBatchSweepAnonymous(writer http.ResponseWriter, request *http.Request, state *connectionSession,
	hello serverHello, parameters sessionParameters, frame frameRecord,
) {
	if !s.supportsBatchSweepAnonymous(hello) {
		s.writeProblem(writer, frame.requestID, 0, errUnsupportedExtension.Error(), uint32(parameters.maxFrameBody))
		return
	}
	commandID, matterID, batchID, releaseCommandID, actor, err := decodeBatchSweepRequest(frame.payload)
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
	preSubmissionContext, boundary, stop := newBirthReleaseBoundary(request.Context(), state.ctx)
	defer stop()
	result, err := s.connectedCommandStart().sweepAnonymousBatch(
		preSubmissionContext, state.ctx, boundary, commandID, matterID, batchID,
		releaseCommandID, actor,
	)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, batchSweepProblem(err), uint32(parameters.maxFrameBody))
		return
	}
	if !result.Returned || result.Entry.Command.ID != commandID || result.Entry.RequestHash == "" ||
		len(result.Receipt) == 0 || result.ResultCode != result.SemanticResult.Code {
		s.writeProblem(writer, frame.requestID, 0, "transport.outcome-unknown", uint32(parameters.maxFrameBody))
		return
	}
	encodedResult, err := encodeOperationResultPayload(operation.BatchSweepAnonymousV1.Metadata().Operation, result.SemanticResult)
	if err != nil {
		abortHTTP2Stream()
	}
	payload, err := encodePayload(map[string]any{
		"schema": batchSweepResultSchema, "command_id": commandID, "request_hash": result.Entry.RequestHash,
		"result_payload": encodedResult, "terminal_receipt": result.Receipt,
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

func decodeBatchSweepRequest(payload []byte) (string, string, string, string, operation.Actor, error) {
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "command_id", "matter_id", "batch_id", "release_command_id", "actor")
	if err != nil || fields["schema"] != batchSweepRequestSchema {
		return "", "", "", "", "", errMalformedMessage
	}
	commandID, commandOK := fields["command_id"].(string)
	matterID, matterOK := fields["matter_id"].(string)
	batchID, batchOK := fields["batch_id"].(string)
	releaseID, releaseOK := fields["release_command_id"].(string)
	actor, actorOK := fields["actor"].(string)
	if !commandOK || !commandStartULID.MatchString(commandID) || !matterOK || !commandStartULID.MatchString(matterID) ||
		!batchOK || !commandStartULID.MatchString(batchID) || !releaseOK || !commandStartULID.MatchString(releaseID) ||
		!actorOK || actor == "" {
		return "", "", "", "", "", errMalformedMessage
	}
	return commandID, matterID, batchID, releaseID, operation.Actor(actor), nil
}

func batchSweepProblem(err error) string {
	switch {
	case errors.Is(err, errBirthReleaseCancelled):
		return "transport.cancelled-before-submission"
	case errors.Is(err, wipdjournal.ErrCommandIDConflict):
		return "command.id-conflict"
	case errors.Is(err, errUnsupportedExtension):
		return "protocol.unsupported-extension"
	case errors.Is(err, wipdjournal.ErrNotFound), errors.Is(err, ErrCommandStartIdentity):
		return "batch.sweep-claim-close-unavailable"
	case errors.Is(err, wipdjournal.ErrInvalidCommand):
		return "protocol.malformed-message"
	case errors.Is(err, ErrCommandStartBlocked):
		return "command.sequence-blocked"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrOutcomeUnknown):
		return "transport.outcome-unknown"
	default:
		return "authority.unavailable"
	}
}

// SweepAnonymousBatch submits one post-close sweep through the M6-only local
// capability. The caller reuses commandID after any uncertain response.
func (c *Client) SweepAnonymousBatch(ctx context.Context, commandID, matterID, batchID,
	releaseCommandID string, actor operation.Actor,
) (BatchSweepAnonymousResponse, error) {
	var empty BatchSweepAnonymousResponse
	if c == nil || c.httpClient == nil || ctx == nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: ErrUnavailable}
	}
	if !operationCapabilityContains(c.hello.operations, operation.BatchSweepAnonymousV1.Metadata().Operation, identitySchemaV1) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	if !commandStartULID.MatchString(commandID) || !commandStartULID.MatchString(matterID) ||
		!commandStartULID.MatchString(batchID) || !commandStartULID.MatchString(releaseCommandID) || actor == "" {
		return empty, &ExchangeError{Code: "protocol.malformed-message"}
	}
	payload, err := encodePayload(map[string]any{
		"schema": batchSweepRequestSchema, "command_id": commandID, "matter_id": matterID, "batch_id": batchID,
		"release_command_id": releaseCommandID, "actor": string(actor),
	})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: batchSweepFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(errors.New("unexpected local Batch sweep response status"))
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
		return empty, &ExchangeError{Code: code, Uncertain: code == "transport.outcome-unknown"}
	}
	if frame.kind != "response.end" {
		return empty, uncertainExchange(errUnsupportedKind)
	}
	result, err := decodeBatchSweepResponse(frame.payload, commandID)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func decodeBatchSweepResponse(payload []byte, commandID string) (BatchSweepAnonymousResponse, error) {
	var empty BatchSweepAnonymousResponse
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "command_id", "request_hash", "result_payload", "terminal_receipt")
	if err != nil || fields["schema"] != batchSweepResultSchema || fields["command_id"] != commandID {
		return empty, errMalformedMessage
	}
	hash, hashOK := fields["request_hash"].(string)
	resultPayload, resultOK := fields["result_payload"].([]byte)
	receipt, receiptOK := fields["terminal_receipt"].([]byte)
	if !hashOK || !commandStartHash.MatchString(hash) || !resultOK || !receiptOK || len(receipt) == 0 {
		return empty, errMalformedMessage
	}
	result, err := decodeOperationResultPayload(operation.BatchSweepAnonymousV1.Metadata().Operation, resultPayload)
	if err != nil {
		return empty, errMalformedMessage
	}
	receiptFields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || receiptFields["schema"] != "wipd.terminal-receipt/1" || receiptFields["identity_schema"] != identitySchemaV1 ||
		receiptFields["command_id"] != commandID || receiptFields["request_hash"] != hash {
		return empty, errMalformedMessage
	}
	operationFields, ok := receiptFields["operation"].(map[string]any)
	if !ok || !exactFields(operationFields, "name", "version") ||
		operationFields["name"] != operation.BatchSweepAnonymousV1.Metadata().Operation.Name ||
		operationFields["version"] != uint64(operation.BatchSweepAnonymousV1.Metadata().Operation.Version) {
		return empty, errMalformedMessage
	}
	receiptResult, ok := receiptFields["result"].(map[string]any)
	if !ok || !exactFields(receiptResult, "code", "output", "problem_code") || receiptResult["code"] != string(result.Code) {
		return empty, errMalformedMessage
	}
	resultFields, err := wipdwire.DecodeCanonicalMap(resultPayload, "code", "output", "problem_code")
	if err != nil || resultFields["code"] != receiptResult["code"] || resultFields["problem_code"] != receiptResult["problem_code"] {
		return empty, errMalformedMessage
	}
	if result.Code == operation.ResultSucceeded {
		payloadOutput, payloadOK := resultFields["output"].([]byte)
		receiptOutput, receiptOK := receiptResult["output"].([]byte)
		if !payloadOK || !receiptOK || !bytes.Equal(payloadOutput, receiptOutput) {
			return empty, errMalformedMessage
		}
	} else if resultFields["output"] != nil || receiptResult["output"] != nil {
		return empty, errMalformedMessage
	}
	return BatchSweepAnonymousResponse{
		CommandID: commandID, RequestHash: hash, Result: result, Receipt: append([]byte(nil), receipt...),
	}, nil
}
