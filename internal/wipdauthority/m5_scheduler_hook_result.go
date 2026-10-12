package wipdauthority

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveSchedulerHookResult(writer http.ResponseWriter, request *http.Request, body *bufio.Reader,
	frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate,
) {
	var submitted wipdwire.SchedulerHookResult
	if wipdwire.DecodeCanonical(frame.Payload, &submitted,
		"schema", "domain_id", "authority_epoch", "environment_id", "command_id", "request_hash", "canonical_command", "result") != nil ||
		submitted.Schema != "wipd.scheduler-hook-result/1" || submitted.DomainID != app.profile.domainID ||
		submitted.Epoch != app.profile.epoch || submitted.EnvironmentID != environment.EnvironmentID ||
		!ulidPattern.MatchString(submitted.CommandID) || !canonicalSPKIDigestPattern.MatchString(submitted.RequestHash) ||
		len(submitted.CanonicalCommand) == 0 || len(submitted.CanonicalCommand) > 1<<20 || len(submitted.Result) == 0 || len(submitted.Result) > 1<<20 {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	session := connectionSession(request)
	if session == nil {
		writeLabProblem(writer, frame.RequestID, "protocol.out-of-order")
		return
	}
	session.mu.Lock()
	_, selected := session.operations[operation.SchedulerOrchestrateV1.Metadata().Operation]
	session.mu.Unlock()
	if !app.step10 || !selected {
		writeLabProblem(writer, frame.RequestID, "protocol.unsupported-extension")
		return
	}
	command, err := operation.DecodeCanonicalCommand(submitted.CanonicalCommand)
	if err != nil || command.ID != submitted.CommandID || command.AuthorityDomainID != submitted.DomainID ||
		command.ExpectedAuthorityEpoch != submitted.Epoch || command.EnvironmentID != submitted.EnvironmentID ||
		!operation.Step10Operation(command.Request.Operation) {
		writeLabProblem(writer, frame.RequestID, "auth.authority-binding-mismatch")
		return
	}
	hash, err := command.RequestHash()
	if err != nil || hash != submitted.RequestHash {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	var output operation.SchedulerHookOutput
	if wipdwire.DecodeCanonical(submitted.Result, &output,
		"kind", "version", "input_digest", "result_digest", "outcome", "revision", "evidence_refs", "problem_code", "steps", "handling") != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: output}
	if operation.SchedulerOrchestrateV1.ValidateResult(result) != nil {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	status, err := app.store.QueryCommand(ctx, submitted.DomainID, submitted.CommandID, submitted.RequestHash,
		submitted.Epoch, *request.TLS, submitted.EnvironmentID, time.Now().UTC())
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, receiptQueryProblem(err))
		return
	}
	if !status.Pending {
		if !schedulerHookReceiptMatches(status.Receipt, submitted, output) {
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "command.id-conflict")
			return
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
			wipdwire.Frame{RequestID: frame.RequestID, Kind: "command.terminal", Payload: status.Receipt}, "")
		return
	}
	input := command.Request.Input.(operation.SchedulerHookInput)
	owner, completion, claimed, err := app.store.ClaimPendingCommandCompletion(command, submitted.RequestHash)
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, submissionProblem(err))
		return
	}
	if claimed {
		if completion.SubjectID != input.MatterID || !sameSchedulerHookResult(completion.Result, result) {
			_ = app.store.ReleaseClaimedCommandCompletion(owner)
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "command.id-conflict")
			return
		}
	} else {
		owner, err = app.store.RecoverCommand(ctx, command, submitted.RequestHash)
		if err != nil {
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, submissionProblem(err))
			return
		}
		now := time.Now().UTC()
		eventID, idErr := randomULID(now)
		if idErr != nil {
			_ = app.store.ReleasePendingCommandExecution(owner)
			writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
			return
		}
		completion = authoritystore.CommandCompletion{Result: result, SubjectID: input.MatterID, EventID: eventID, Occurred: now}
	}
	status, err = app.store.CompleteCommand(context.Background(), owner, completion.Result, completion.SubjectID,
		completion.EventID, completion.Occurred, app.sign, completion.AdditionalEventIDs...)
	if errors.Is(err, authoritystore.ErrNotOwner) {
		status, err = app.store.QueryCommand(ctx, submitted.DomainID, submitted.CommandID, submitted.RequestHash,
			submitted.Epoch, *request.TLS, submitted.EnvironmentID, time.Now().UTC())
		if err == nil && status.Pending {
			err = authoritystore.ErrPending
		}
	}
	if err != nil || status.Pending || len(status.Receipt) == 0 || !schedulerHookReceiptMatches(status.Receipt, submitted, output) {
		code := "authority.unavailable"
		if errors.Is(err, authoritystore.ErrPending) {
			code = "command.pending"
		} else if errors.Is(err, authoritystore.ErrConflict) {
			code = "command.id-conflict"
		} else if errors.Is(err, authoritystore.ErrFenced) || errors.Is(err, authoritystore.ErrInvalidProof) {
			code = "auth.environment-domain-mismatch"
		}
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, code)
		return
	}
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "command.terminal", Payload: status.Receipt}, "")
}

func sameSchedulerHookResult(left, right operation.Result) bool {
	if left.Code != right.Code || left.Problem != nil || right.Problem != nil {
		return false
	}
	leftBytes, leftErr := wipdwire.EncodeCanonical(left.Output)
	rightBytes, rightErr := wipdwire.EncodeCanonical(right.Output)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func schedulerHookReceiptMatches(receipt []byte, submitted wipdwire.SchedulerHookResult, output operation.SchedulerHookOutput) bool {
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != submitted.DomainID ||
		fields["authority_epoch"] != submitted.Epoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != submitted.CommandID || fields["request_hash"] != submitted.RequestHash {
		return false
	}
	op, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(op, "name", "version") || op["name"] != "scheduler.orchestrate" || op["version"] != uint64(1) {
		return false
	}
	env, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(env, "id", "sequence") || env["id"] != submitted.EnvironmentID {
		return false
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") ||
		result["code"] != string(operation.ResultSucceeded) || result["problem_code"] != nil {
		return false
	}
	want, err := wipdwire.EncodeCanonical(output)
	actual, ok := result["output"].([]byte)
	if err != nil || !ok || !bytes.Equal(want, actual) {
		return false
	}
	accepted, ok := fields["accepted_events"].(map[string]any)
	return ok && wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") &&
		accepted["event_count"] == uint64(1) && accepted["first_event_id"] == accepted["last_event_id"]
}
