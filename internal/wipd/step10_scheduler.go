package wipd

import (
	"context"
	"errors"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/wipdjournal"
)

// ErrSchedulerRunHeld is returned before Environment admission when another
// coordinator owns the host-local Run lock.
var ErrSchedulerRunHeld = errors.New("wipd: scheduler Run lock is held")

func (coordinator *CommandStartCoordinator) retainSchedulerRun(command operation.Command) (string, bool, error) {
	runID, ok := schedulerRunID(coordinator.journal, command)
	if !ok {
		return "", false, nil
	}
	coordinator.runLockMu.Lock()
	defer coordinator.runLockMu.Unlock()
	if lock := coordinator.runLocks[runID]; lock != nil {
		return runID, false, nil
	}
	lock, err := runlock.Acquire(runID)
	if errors.Is(err, runlock.ErrHeld) {
		return "", false, fmt.Errorf("%w: %s", ErrSchedulerRunHeld, runID)
	}
	if err != nil {
		return "", false, err
	}
	coordinator.runLocks[runID] = lock
	return runID, true, nil
}

func (coordinator *CommandStartCoordinator) releaseSchedulerRun(runID string) {
	if coordinator == nil || runID == "" {
		return
	}
	coordinator.runLockMu.Lock()
	lock := coordinator.runLocks[runID]
	delete(coordinator.runLocks, runID)
	coordinator.runLockMu.Unlock()
	if lock != nil {
		_ = lock.Release()
	}
}

func schedulerRunID(journal *wipdjournal.Journal, command operation.Command) (string, bool) {
	if journal == nil {
		return "", false
	}
	switch input := command.Request.Input.(type) {
	case operation.SchedulerHookInput:
		if operation.Step10Operation(command.Request.Operation) {
			return input.RunID, true
		}
	case operation.RoleLifecycleInput:
		if operation.Step10RoleOperation(command.Request.Operation) {
			return input.RunID, true
		}
	case operation.RunResumeInput:
		if operation.Step10RunResumeOperation(command.Request.Operation) {
			return input.RunID, true
		}
	case operation.RunFinishInput:
		if operation.Step10RunFinishOperation(command.Request.Operation) {
			return input.RunID, true
		}
	}
	if command.CausationCommandID == "" {
		return "", false
	}
	causation, err := journal.Get(command.CausationCommandID)
	if err != nil {
		return "", false
	}
	input, ok := causation.Command.Request.Input.(operation.SchedulerHookInput)
	if !ok || !operation.Step10Operation(causation.Command.Request.Operation) {
		return "", false
	}
	return input.RunID, true
}

func (coordinator *CommandStartCoordinator) continueSchedulerHook(ctx context.Context, entry wipdjournal.Entry, result operation.Result) error {
	if coordinator == nil || ctx == nil || !resultIsSuccess(result) {
		return ErrCommandStartIdentity
	}
	input, inputOK := entry.Command.Request.Input.(operation.SchedulerHookInput)
	output, outputOK := result.Output.(operation.SchedulerHookOutput)
	if !inputOK || !outputOK || entry.Command.Request.Operation != operation.SchedulerOrchestrateV1.Metadata().Operation ||
		output.Kind != input.Kind || output.Version != input.Version {
		return ErrCommandStartIdentity
	}
	coordinator.schedulerContinue.Lock()
	defer coordinator.schedulerContinue.Unlock()
	switch input.Kind {
	case "plan":
		return coordinator.continuePlan(ctx, entry.Command, output)
	case "work":
		return coordinator.continueWork(ctx, entry.Command, output)
	case "ask":
		return nil
	default:
		return ErrCommandStartIdentity
	}
}

// recoverSchedulerRun resumes only original hook identities already retained
// by this Environment. It runs before Role closure or scheduler.resume can
// advance the frontier, so an installed-but-unconsumed result cannot be
// skipped in favor of fresh Dispatch work.
func (coordinator *CommandStartCoordinator) recoverSchedulerRun(ctx context.Context, runID string) error {
	if coordinator == nil || coordinator.journal == nil || ctx == nil || runID == "" {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	hooks := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Command.Request.Operation != operation.SchedulerOrchestrateV1.Metadata().Operation {
			continue
		}
		input, ok := entry.Command.Request.Input.(operation.SchedulerHookInput)
		if ok && input.RunID == runID {
			hooks[entry.Command.ID] = struct{}{}
		}
	}
	for _, entry := range entries {
		input, hook := entry.Command.Request.Input.(operation.SchedulerHookInput)
		shouldRecover := hook && input.RunID == runID && entry.Command.Request.Operation == operation.SchedulerOrchestrateV1.Metadata().Operation
		if role, ok := entry.Command.Request.Input.(operation.RoleLifecycleInput); ok && role.RunID == runID &&
			operation.Step10RoleOperation(entry.Command.Request.Operation) {
			shouldRecover = true
		}
		if resume, ok := entry.Command.Request.Input.(operation.RunResumeInput); ok && resume.RunID == runID &&
			operation.Step10RunResumeOperation(entry.Command.Request.Operation) {
			shouldRecover = true
		}
		if finish, ok := entry.Command.Request.Input.(operation.RunFinishInput); ok && finish.RunID == runID &&
			operation.Step10RunFinishOperation(entry.Command.Request.Operation) {
			shouldRecover = true
		}
		if _, causedByHook := hooks[entry.Command.CausationCommandID]; causedByHook && entry.Command.CausationCommandID != "" {
			switch entry.Command.Request.Operation {
			case operation.StepCreateV2.Metadata().Operation, operation.MatterFinishV1.Metadata().Operation,
				operation.StageFinishV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
				operation.StepStartV1.Metadata().Operation, operation.GateCloseV1.Metadata().Operation:
				shouldRecover = true
			}
		}
		if !shouldRecover {
			continue
		}
		result, recoverErr := coordinator.runConnectedCanonicalTerminal(ctx, entry.Command, emptyConnectedGuard)
		if recoverErr != nil {
			return fmt.Errorf("wipd: recover Step 10 command %s before Run frontier: %w", entry.Command.ID, recoverErr)
		}
		if entry.Command.Request.Operation == operation.SchedulerOrchestrateV1.Metadata().Operation &&
			result.Returned && result.ResultCode == operation.ResultSucceeded {
			if err = coordinator.continueSchedulerHook(ctx, result.Entry, result.SemanticResult); err != nil {
				return fmt.Errorf("wipd: consume recovered scheduler hook %s before Run frontier: %w", entry.Command.ID, err)
			}
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) continuePlan(ctx context.Context, plan operation.Command, output operation.SchedulerHookOutput) error {
	input, ok := plan.Request.Input.(operation.SchedulerHookInput)
	if !ok || input.Kind != "plan" {
		return ErrCommandStartIdentity
	}
	if output.Outcome == "failed" {
		if len(output.Steps) != 0 {
			return ErrCommandStartIdentity
		}
		return nil
	}
	if output.Outcome != "planned" {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	births := make([]wipdjournal.Entry, 0, len(output.Steps))
	for _, candidate := range entries {
		if candidate.Command.CausationCommandID != plan.ID || candidate.Command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			continue
		}
		births = append(births, candidate)
	}
	if len(births) > len(output.Steps) {
		return ErrCommandStartIdentity
	}
	for index, step := range output.Steps {
		var result CommandStartResult
		if index < len(births) {
			birth := births[index]
			birthInput, valid := birth.Command.Request.Input.(operation.StepCreateInput)
			if !valid || birth.Command.CorrelationCommandID != plan.ID || birth.Command.Request.Actor != "role:scheduler" ||
				birth.Command.Request.Context != plan.Request.Context || birth.Command.Request.Claim == nil || plan.Request.Claim == nil ||
				*birth.Command.Request.Claim != *plan.Request.Claim || birthInput.ParentID != input.MatterID || birthInput.Title != step.Title {
				return ErrCommandStartIdentity
			}
			result, err = coordinator.runConnectedCanonicalTerminal(ctx, birth.Command, emptyConnectedGuard)
		} else {
			id, idErr := newFrameRequestID()
			if idErr != nil {
				return idErr
			}
			request := operation.Request{
				Operation: operation.StepCreateV2.Metadata().Operation,
				Actor:     "role:scheduler",
				Context:   plan.Request.Context,
				Claim:     &operation.ClaimContext{ID: plan.Request.Claim.ID, Epoch: plan.Request.Claim.Epoch},
				Input:     operation.StepCreateInput{ParentID: input.MatterID, Title: step.Title},
			}
			result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
				ID: id, CausationCommandID: plan.ID, CorrelationCommandID: plan.ID, Request: request,
			}, emptyConnectedGuard)
		}
		if err != nil {
			return fmt.Errorf("wipd: recover Plan birth %d for %s: %w", index, plan.ID, err)
		}
		created, valid := result.SemanticResult.Output.(operation.StepCreateOutput)
		if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
			!valid || created.ParentID != input.MatterID || created.MatterID != input.MatterID || created.Title != step.Title || created.State != "planned" {
			return ErrCommandStartIdentity
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) continueWork(ctx context.Context, work operation.Command, output operation.SchedulerHookOutput) error {
	input, ok := work.Request.Input.(operation.SchedulerHookInput)
	if !ok || input.Kind != "work" {
		return ErrCommandStartIdentity
	}
	if input.Role == "verifier" {
		if output.Outcome != "verified" {
			// Negative verification is a durable parked result. The next
			// explicit owning-Environment Resume decides whether a correction
			// pass is ready; it must not close the gate or finish the Matter.
			return nil
		}
		return coordinator.closeVerifiedSchedulerGate(ctx, work, input)
	}
	if output.Outcome != "completed" {
		return nil
	}
	finishOperation, finishInput := schedulerWorkFinish(input)
	if finishOperation.Name == "" || finishInput == nil {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var prior *wipdjournal.Entry
	for index := range entries {
		candidate := &entries[index]
		if candidate.Command.CausationCommandID == work.ID && candidate.Command.Request.Operation == finishOperation {
			if prior != nil {
				return ErrCommandStartIdentity
			}
			prior = candidate
		}
	}
	var result CommandStartResult
	if prior != nil {
		if prior.Command.CorrelationCommandID != work.ID || prior.Command.Request.Actor != "system:scheduler" ||
			prior.Command.Request.Context != work.Request.Context || prior.Command.Request.Claim == nil || work.Request.Claim == nil ||
			*prior.Command.Request.Claim != *work.Request.Claim || !schedulerWorkFinishInputMatches(prior.Command.Request.Input, finishOperation, finishInput) {
			return ErrCommandStartIdentity
		}
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return idErr
		}
		request := operation.Request{
			Operation: finishOperation,
			Actor:     "system:scheduler",
			Context:   work.Request.Context,
			Claim:     &operation.ClaimContext{ID: work.Request.Claim.ID, Epoch: work.Request.Claim.Epoch},
			Input:     finishInput,
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
			ID: id, CausationCommandID: work.ID, CorrelationCommandID: work.ID, Request: request,
		}, emptyConnectedGuard)
	}
	if err != nil {
		return err
	}
	// A lifecycle refusal is durable no-effect evidence: the scheduler must
	// leave the Work bracket open for readiness recomputation rather than
	// treating the useful computation as a finished Matter.
	if result.ResultCode != operation.ResultSucceeded {
		return nil
	}
	if !result.Returned || result.SemanticResult.Code != operation.ResultSucceeded || !schedulerWorkFinishOutputMatches(result.SemanticResult.Output, finishOperation, input.TargetID) {
		return ErrCommandStartIdentity
	}
	return nil
}

func (coordinator *CommandStartCoordinator) closeVerifiedSchedulerGate(ctx context.Context, work operation.Command,
	input operation.SchedulerHookInput,
) error {
	if coordinator == nil || ctx == nil || work.Request.Claim == nil || input.Role != "verifier" ||
		input.TargetKind != "matter" || input.TargetID != input.MatterID || input.Revision != operation.SchedulerStep10VerifierRevision {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var prior *wipdjournal.Entry
	for index := range entries {
		candidate := &entries[index]
		if candidate.Command.CausationCommandID != work.ID || candidate.Command.Request.Operation != operation.GateCloseV1.Metadata().Operation {
			continue
		}
		if prior != nil {
			return ErrCommandStartIdentity
		}
		gate, gateOK := candidate.Command.Request.Input.(operation.GateCloseInput)
		if !gateOK || gate.Gate != "verified" || gate.NodeID != input.MatterID ||
			candidate.Command.CorrelationCommandID != work.ID || candidate.Command.Request.Actor != "role:verifier" ||
			candidate.Command.Request.Context != work.Request.Context || candidate.Command.Request.Claim == nil ||
			*candidate.Command.Request.Claim != *work.Request.Claim {
			return ErrCommandStartIdentity
		}
		prior = candidate
	}
	var result CommandStartResult
	if prior != nil {
		result, err = coordinator.runConnectedCanonicalTerminal(ctx, prior.Command, emptyConnectedGuard)
	} else {
		id, idErr := newFrameRequestID()
		if idErr != nil {
			return idErr
		}
		result, err = coordinator.RunConnectedTerminal(ctx, wipdjournal.CommandInput{
			ID: id, CausationCommandID: work.ID, CorrelationCommandID: work.ID,
			Request: operation.Request{
				Operation: operation.GateCloseV1.Metadata().Operation,
				Actor:     "role:verifier",
				Context:   work.Request.Context,
				Claim:     &operation.ClaimContext{ID: work.Request.Claim.ID, Epoch: work.Request.Claim.Epoch},
				Input:     operation.GateCloseInput{Gate: "verified", NodeID: input.MatterID},
			},
		}, emptyConnectedGuard)
	}
	if err != nil {
		return fmt.Errorf("wipd: close verified gate after exact positive Verifier %s: %w", work.ID, err)
	}
	closed, ok := result.SemanticResult.Output.(operation.GateCloseOutput)
	if !result.Returned || result.ResultCode != operation.ResultSucceeded || result.SemanticResult.Code != operation.ResultSucceeded ||
		!ok || closed.Gate != "verified" || closed.NodeID != input.MatterID || closed.Scale != "matter" {
		problem := ""
		if result.SemanticResult.Problem != nil {
			problem = string(result.SemanticResult.Problem.Code)
		}
		return fmt.Errorf("%w: verified gate close was not an exact successful Matter result (returned=%t receipt=%q semantic=%q problem=%q output=%T)",
			ErrCommandStartIdentity, result.Returned, result.ResultCode, result.SemanticResult.Code, problem, result.SemanticResult.Output)
	}
	return nil
}

func schedulerWorkFinish(input operation.SchedulerHookInput) (operation.ID, operation.Input) {
	switch input.TargetKind {
	case "matter":
		return operation.MatterFinishV1.Metadata().Operation, operation.MatterFinishInput{MatterID: input.TargetID}
	case "stage":
		return operation.StageFinishV1.Metadata().Operation, operation.NodeLifecycleInput{NodeID: input.TargetID}
	case "step":
		return operation.StepFinishV1.Metadata().Operation, operation.StepLifecycleInput{StepID: input.TargetID}
	default:
		return operation.ID{}, nil
	}
}

func schedulerWorkFinishInputMatches(input operation.Input, operationID operation.ID, expected operation.Input) bool {
	switch operationID {
	case operation.MatterFinishV1.Metadata().Operation:
		actual, ok := input.(operation.MatterFinishInput)
		want, wantOK := expected.(operation.MatterFinishInput)
		return ok && wantOK && actual == want
	case operation.StageFinishV1.Metadata().Operation:
		actual, ok := input.(operation.NodeLifecycleInput)
		want, wantOK := expected.(operation.NodeLifecycleInput)
		return ok && wantOK && actual == want
	case operation.StepFinishV1.Metadata().Operation:
		actual, ok := input.(operation.StepLifecycleInput)
		want, wantOK := expected.(operation.StepLifecycleInput)
		return ok && wantOK && actual == want
	default:
		return false
	}
}

func schedulerWorkFinishOutputMatches(output operation.Output, operationID operation.ID, targetID string) bool {
	switch operationID {
	case operation.MatterFinishV1.Metadata().Operation:
		finished, ok := output.(operation.MatterFinishOutput)
		return ok && finished.MatterID == targetID && finished.State == "done"
	case operation.StageFinishV1.Metadata().Operation:
		finished, ok := output.(operation.NodeLifecycleOutput)
		return ok && finished.NodeID == targetID && finished.State == "done"
	case operation.StepFinishV1.Metadata().Operation:
		finished, ok := output.(operation.StepLifecycleOutput)
		return ok && finished.StepID == targetID && finished.State == "done"
	default:
		return false
	}
}

func resultIsSuccess(result operation.Result) bool {
	return result.Code == operation.ResultSucceeded && result.Problem == nil
}

func emptyConnectedGuard(context.Context, CommandStartSnapshot, operation.Command) error { return nil }
