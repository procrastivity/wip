package wipd

import (
	"context"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// CommandSubmitV2Authority reports the capability selected on the authority
// session, not merely a locally implemented encoder. Daemons advertise v2
// only when their connected return path independently negotiated it.
type CommandSubmitV2Authority interface {
	SupportsCommandSubmitV2() bool
}

func (s *Server) supportsCommandSubmitV2() bool {
	coordinator := s.connectedCommandStart()
	return coordinator != nil && coordinator.supportsCommandSubmitV2()
}

func (coordinator *CommandStartCoordinator) supportsCommandSubmitV2() bool {
	authority, ok := coordinator.authority.(CommandSubmitV2Authority)
	return ok && authority.SupportsCommandSubmitV2()
}

func decodeNegotiatedCommandSubmit(payload []byte, localV2, authorityV2 bool) (operation.Command, *time.Time, wipdwire.CommandSubmitV2, error) {
	submit, version2, err := wipdwire.DecodeCommandSubmit(payload)
	if err != nil {
		return operation.Command{}, nil, submit, errMalformedMessage
	}
	if version2 && (!localV2 || !authorityV2) {
		return operation.Command{}, nil, submit, errUnsupportedExtension
	}
	// Reuse the closed v1 command/hash/deadline validation, stripping only the
	// transport envelope. Detached authorization never enters command bytes.
	canonicalEnvelope, err := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
		Schema: "wipd.command-submit/1", CanonicalCommand: submit.CanonicalCommand,
		RequestHash: submit.RequestHash, Deadline: submit.Deadline,
	})
	if err != nil {
		return operation.Command{}, nil, submit, errMalformedMessage
	}
	command, deadline, err := decodeCommandSubmit(canonicalEnvelope)
	if err != nil {
		return command, deadline, submit, err
	}
	if command.Request.Operation.Name == "gate.exemption.repair" && !version2 ||
		command.Request.Operation.Name != "gate.exemption.repair" && submit.DetachedProof != nil {
		return operation.Command{}, nil, submit, errUnsupportedExtension
	}
	return command, deadline, submit, nil
}

// RunConnectedCanonicalSubmission retains v2 transport bytes before ordered
// return. Both capabilities must still hold before any local durable write.
func (coordinator *CommandStartCoordinator) RunConnectedCanonicalSubmission(ctx context.Context, command operation.Command, submit wipdwire.CommandSubmitV2,
	guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error,
) (CommandStartResult, error) {
	if submit.Schema == "wipd.command-submit/1" {
		if command.Request.Operation.Name == "gate.exemption.repair" {
			return CommandStartResult{}, errUnsupportedExtension
		}
		return coordinator.RunConnectedCanonicalTerminal(ctx, command, guardAndWrite)
	}
	if submit.Schema != wipdwire.CommandSubmitV2Feature || !coordinator.supportsCommandSubmitV2() {
		return CommandStartResult{}, errUnsupportedExtension
	}
	runID, acquired, err := coordinator.retainSchedulerRun(command)
	if err != nil {
		return CommandStartResult{}, err
	}
	if command.Request.Operation == operation.RoleCloseV1.Metadata().Operation || operation.Step10RunResumeOperation(command.Request.Operation) ||
		operation.Step10RunFinishOperation(command.Request.Operation) {
		if err = coordinator.recoverSchedulerRun(ctx, runID); err != nil {
			if acquired && !errors.Is(err, ErrOutcomeUnknown) {
				coordinator.releaseSchedulerRun(runID)
			}
			return CommandStartResult{}, err
		}
	}
	result, err := coordinator.runConnectedPrepared(ctx, func() (wipdjournal.Entry, error) {
		if err := coordinator.validateSchedulerHookPrefix(ctx, command); err != nil {
			return wipdjournal.Entry{}, err
		}
		if err := coordinator.validateCanonicalClaimReadiness(ctx, command); err != nil {
			return wipdjournal.Entry{}, err
		}
		return coordinator.journal.PrepareCanonicalSubmission(command, submit.Schema, submit.DetachedProof)
	}, guardAndWrite, true)
	if err != nil {
		if acquired && !errors.Is(err, ErrOutcomeUnknown) {
			coordinator.releaseSchedulerRun(runID)
		}
		return result, err
	}
	if result.Returned && result.ResultCode == operation.ResultSucceeded {
		if command.Request.Operation == operation.SchedulerOrchestrateV1.Metadata().Operation {
			if err = coordinator.continueSchedulerHook(ctx, result.Entry, result.SemanticResult); err != nil {
				return CommandStartResult{}, err
			}
		}
		if operation.Step10RunFinishOperation(command.Request.Operation) {
			coordinator.releaseSchedulerRun(runID)
		}
	}
	return result, nil
}
