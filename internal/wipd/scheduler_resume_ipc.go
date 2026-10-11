package wipd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	schedulerResumeFrameKind     = "scheduler.resume.owned"
	schedulerResumeRequestSchema = "wipd.local-scheduler-resume/1"
	schedulerResumeResultSchema  = "wipd.local-scheduler-resume-result/1"
)

func (s *Server) supportsSchedulerResume(hello serverHello) bool {
	coordinator := s.connectedCommandStart()
	if coordinator == nil || !operationCapabilityContains(hello.operations, operation.SchedulerResumeV1.Metadata().Operation, identitySchemaV1) ||
		!operationCapabilityContains(hello.operations, operation.RoleSpawnV1.Metadata().Operation, identitySchemaV1) ||
		!operationCapabilityContains(hello.operations, operation.RoleCloseV1.Metadata().Operation, identitySchemaV1) {
		return false
	}
	if _, ok := coordinator.authority.(ClaimAcquireAuthority); !ok {
		return false
	}
	closer, ok := coordinator.authority.(ClaimJournalCloseAuthority)
	if !ok || !closer.SupportsClaimJournalClose() {
		return false
	}
	_, ok = coordinator.environment.(ClaimJournalReleaseEnvironment)
	return ok
}

func (s *Server) serveSchedulerResume(writer http.ResponseWriter, request *http.Request, hello serverHello,
	parameters sessionParameters, frame frameRecord,
) {
	if !s.supportsSchedulerResume(hello) {
		s.writeProblem(writer, frame.requestID, 0, "protocol.unsupported-extension", uint32(parameters.maxFrameBody))
		return
	}
	fields, err := wipdwire.DecodeCanonicalMap(frame.payload, "schema", "run_id")
	runID, runOK := fields["run_id"].(string)
	if err != nil || fields["schema"] != schedulerResumeRequestSchema || !runOK || !commandStartULID.MatchString(runID) {
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
	result, err := s.connectedCommandStart().ResumeOwnedRun(request.Context(), runID)
	if err != nil {
		fmt.Printf("Step 10 Resume internal error: %v\n", err)
		code := "authority.unavailable"
		switch {
		case errors.Is(err, ErrSchedulerRunHeld):
			code = "run.lock-held"
		case errors.Is(err, ErrCommandStartBlocked), errors.Is(err, ErrCommandStartIdentity),
			errors.Is(err, wipdjournal.ErrClaimNotReady), errors.Is(err, wipdjournal.ErrClaimJournalIncomplete):
			code = "command.sequence-blocked"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "transport.outcome-unknown"
		}
		s.writeProblem(writer, frame.requestID, 0, code, uint32(parameters.maxFrameBody))
		return
	}
	payload, err := encodeSchedulerResumeResult(result)
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, "authority.unavailable", uint32(parameters.maxFrameBody))
		return
	}
	wire, err := encodeFrame(frameRecord{requestID: frame.requestID, sequence: 0, kind: "response.end", payload: payload}, uint32(parameters.maxFrameBody))
	if err != nil {
		s.writeProblem(writer, frame.requestID, 0, "protocol.frame-too-large", uint32(parameters.maxFrameBody))
		return
	}
	_, _ = writer.Write(wire)
}

func encodeSchedulerResumeResult(result SchedulerResumeResponse) ([]byte, error) {
	var claim any
	if result.Claim.ClaimID != "" {
		claim = map[string]any{
			"grant_id": result.Claim.GrantID, "command_id": result.Claim.CommandID, "request_hash": result.Claim.RequestHash,
			"claim_id": result.Claim.ClaimID, "claim_epoch": result.Claim.ClaimEpoch,
			"matter_id": result.Claim.MatterID, "batch_id": result.Claim.BatchID, "dispatch_id": result.Claim.DispatchID,
			"as_of": result.Claim.AsOf, "manifest": result.Claim.Manifest,
		}
	}
	return wipdwire.EncodeCanonical(map[string]any{
		"schema": schedulerResumeResultSchema, "run_id": result.RunID, "matter_id": result.MatterID,
		"claim": claim, "role_id": result.RoleID, "role_command_id": result.RoleCommandID,
		"role_name": result.RoleName, "target_id": result.TargetID, "target_kind": result.TargetKind,
		"work_causation_id": result.WorkCausationID, "finished": result.Finished, "blocked": result.Blocked,
	})
}

// ResumeSchedulerRun is the private Step 10 owner-Environment recovery path.
// It uses the existing isolated Step 10 profile and is not an ordinary CLI
// or newly advertised control-plane operation.
func (c *Client) ResumeSchedulerRun(ctx context.Context, runID string) (SchedulerResumeResponse, error) {
	var empty SchedulerResumeResponse
	if c == nil || c.httpClient == nil || ctx == nil || !c.step10 || !commandStartULID.MatchString(runID) ||
		!operationCapabilityContains(c.hello.operations, operation.SchedulerResumeV1.Metadata().Operation, identitySchemaV1) {
		return empty, &ExchangeError{Code: "protocol.unsupported-extension"}
	}
	payload, err := wipdwire.EncodeCanonical(map[string]any{"schema": schedulerResumeRequestSchema, "run_id": runID})
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.malformed-message", Err: err}
	}
	requestID, err := newFrameRequestID()
	if err != nil {
		return empty, &ExchangeError{Code: "transport.unavailable", Err: err}
	}
	wire, err := encodeFrame(frameRecord{requestID: requestID, sequence: 0, kind: schedulerResumeFrameKind, payload: payload}, uint32(c.parameters.maxFrameBody))
	if err != nil {
		return empty, &ExchangeError{Code: "protocol.frame-too-large", Err: err}
	}
	response, err := c.post(ctx, exchangePath, wire)
	if err != nil {
		return empty, uncertainExchange(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK {
		return empty, uncertainExchange(errors.New("unexpected owner-Environment Resume response status"))
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
	result, err := decodeSchedulerResumeResult(frame.payload, runID)
	if err != nil {
		fmt.Printf("Step 10 Resume response decode error: %v payload=%s\n", err, frame.payload)
		return empty, uncertainExchange(err)
	}
	if _, err = readFrame(response.Body, uint32(c.parameters.maxFrameBody)); !errors.Is(err, io.EOF) {
		return empty, uncertainExchange(errOutOfOrder)
	}
	return result, nil
}

func decodeSchedulerResumeResult(payload []byte, runID string) (SchedulerResumeResponse, error) {
	var result SchedulerResumeResponse
	fields, err := wipdwire.DecodeCanonicalMap(payload,
		"schema", "run_id", "matter_id", "claim", "role_id", "role_command_id", "role_name", "target_id",
		"target_kind", "work_causation_id", "finished", "blocked")
	if err != nil || fields["schema"] != schedulerResumeResultSchema || fields["run_id"] != runID {
		return result, errMalformedMessage
	}
	result.RunID = runID
	result.MatterID, _ = fields["matter_id"].(string)
	result.RoleID, _ = fields["role_id"].(string)
	result.RoleCommandID, _ = fields["role_command_id"].(string)
	result.RoleName, _ = fields["role_name"].(string)
	result.TargetID, _ = fields["target_id"].(string)
	result.TargetKind, _ = fields["target_kind"].(string)
	result.WorkCausationID, _ = fields["work_causation_id"].(string)
	result.Blocked, _ = fields["blocked"].(string)
	var boolOK bool
	result.Finished, boolOK = fields["finished"].(bool)
	if !boolOK {
		return SchedulerResumeResponse{}, errMalformedMessage
	}
	if fields["claim"] != nil {
		claim, ok := fields["claim"].(map[string]any)
		if !ok || !wipdwire.ExactMapKeys(claim, "grant_id", "command_id", "request_hash", "claim_id", "claim_epoch", "matter_id", "batch_id", "dispatch_id", "as_of", "manifest") {
			return SchedulerResumeResponse{}, errMalformedMessage
		}
		grantID, grantOK := claim["grant_id"].(string)
		commandID, commandOK := claim["command_id"].(string)
		requestHash, hashOK := claim["request_hash"].(string)
		claimID, claimOK := claim["claim_id"].(string)
		epoch, epochOK := claim["claim_epoch"].(uint64)
		matterID, matterOK := claim["matter_id"].(string)
		batchID, batchOK := claim["batch_id"].(string)
		dispatchID, dispatchOK := claim["dispatch_id"].(string)
		manifest, manifestOK := claim["manifest"].(string)
		anchor, anchorOK := decodeClaimAcquireAnchor(claim["as_of"])
		if !grantOK || !commandStartULID.MatchString(grantID) || !commandOK || !commandStartULID.MatchString(commandID) ||
			!hashOK || !requestHashPattern.MatchString(requestHash) || !claimOK || !commandStartULID.MatchString(claimID) ||
			!epochOK || epoch == 0 || !matterOK || matterID != result.MatterID || !batchOK || !commandStartULID.MatchString(batchID) ||
			!dispatchOK || !commandStartULID.MatchString(dispatchID) || !manifestOK || !requestHashPattern.MatchString(manifest) || !anchorOK {
			return SchedulerResumeResponse{}, errMalformedMessage
		}
		result.Claim = wipdjournal.ClaimGrantSummary{
			GrantID: grantID, CommandID: commandID, RequestHash: requestHash,
			ClaimID: claimID, ClaimEpoch: epoch, MatterID: matterID, BatchID: batchID,
			DispatchID: dispatchID, AsOf: anchor, Manifest: manifest,
		}
	}
	if result.Blocked != "" {
		if result.Finished || result.TargetID != "" && (!commandStartULID.MatchString(result.TargetID) ||
			(result.TargetKind != "matter" && result.TargetKind != "step" && result.TargetKind != "stage")) ||
			result.TargetID == "" && (result.TargetKind != "" || result.WorkCausationID != "") ||
			result.WorkCausationID != "" && !commandStartULID.MatchString(result.WorkCausationID) {
			return SchedulerResumeResponse{}, errMalformedMessage
		}
		switch result.Blocked {
		case "policy-required":
			if !commandStartULID.MatchString(result.MatterID) || result.Claim.ClaimID != "" ||
				result.RoleID != "" || result.RoleCommandID != "" || result.RoleName != "" ||
				result.TargetID != "" || result.TargetKind != "" || result.WorkCausationID != "" {
				return SchedulerResumeResponse{}, errMalformedMessage
			}
		case "policy-halt":
			if result.MatterID == "" {
				if result.Claim.ClaimID != "" || result.RoleID != "" || result.RoleCommandID != "" || result.RoleName != "" ||
					result.TargetID != "" || result.TargetKind != "" || result.WorkCausationID != "" {
					return SchedulerResumeResponse{}, errMalformedMessage
				}
				break
			}
			if !commandStartULID.MatchString(result.MatterID) || result.Claim.ClaimID == "" ||
				result.Claim.MatterID != result.MatterID || !commandStartULID.MatchString(result.RoleID) ||
				!commandStartULID.MatchString(result.RoleCommandID) ||
				(result.RoleName != "orchestrator" && result.RoleName != "researcher" && result.RoleName != "builder" && result.RoleName != "verifier") {
				return SchedulerResumeResponse{}, errMalformedMessage
			}
		default:
			if result.MatterID != "" || result.Claim.ClaimID != "" || result.RoleID != "" || result.RoleCommandID != "" ||
				result.RoleName != "" || result.TargetID != "" || result.TargetKind != "" || result.WorkCausationID != "" {
				return SchedulerResumeResponse{}, errMalformedMessage
			}
		}
		return result, nil
	}
	if result.Finished {
		if result.MatterID != "" || fields["claim"] != nil || result.RoleID != "" || result.Blocked != "" {
			return SchedulerResumeResponse{}, errMalformedMessage
		}
		return result, nil
	}
	if result.MatterID == "" || !commandStartULID.MatchString(result.MatterID) || result.Blocked != "" ||
		result.Claim.ClaimID == "" || !commandStartULID.MatchString(result.RoleID) ||
		!commandStartULID.MatchString(result.RoleCommandID) ||
		(result.RoleName != "orchestrator" && result.RoleName != "researcher" && result.RoleName != "builder" && result.RoleName != "verifier") ||
		!commandStartULID.MatchString(result.TargetID) || (result.TargetKind != "matter" && result.TargetKind != "step" && result.TargetKind != "stage") ||
		result.WorkCausationID != "" && !commandStartULID.MatchString(result.WorkCausationID) {
		return SchedulerResumeResponse{}, errMalformedMessage
	}
	return result, nil
}
