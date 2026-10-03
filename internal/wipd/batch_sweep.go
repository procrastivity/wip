package wipd

import (
	"context"
	"errors"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
)

type batchSweepAnonymousAuthority interface {
	SupportsBatchSweepAnonymous() bool
}

func (coordinator *CommandStartCoordinator) supportsBatchSweepAnonymous() bool {
	if coordinator == nil || coordinator.authority == nil {
		return false
	}
	authority, ok := coordinator.authority.(batchSweepAnonymousAuthority)
	return ok && authority.SupportsBatchSweepAnonymous()
}

func (coordinator *CommandStartCoordinator) sweepAnonymousBatch(
	preSubmissionContext, resolutionContext context.Context,
	boundary *birthReleaseBoundary,
	commandID, matterID, batchID, releaseCommandID string,
	actor operation.Actor,
) (CommandStartResult, error) {
	if coordinator == nil || coordinator.journal == nil || preSubmissionContext == nil || resolutionContext == nil ||
		boundary == nil || actor == "" || !coordinator.supportsBatchSweepAnonymous() {
		return CommandStartResult{}, errUnsupportedExtension
	}
	return coordinator.runConnectedPrepared(resolutionContext, func() (wipdjournal.Entry, error) {
		closeReference, err := coordinator.journal.ReleaseInstalledClaimCloseByID(preSubmissionContext, releaseCommandID)
		if err != nil {
			return wipdjournal.Entry{}, err
		}
		request := operation.Request{
			Operation: operation.BatchSweepAnonymousV1.Metadata().Operation,
			Actor:     actor,
			Context:   operation.Context{Repo: coordinator.journal.Identity().RepoID},
			Input: operation.BatchSweepAnonymousInput{
				MatterID: matterID, BatchID: batchID, ClaimClose: closeReference,
			},
			Blobs: []operation.BlobInput{},
		}
		entry, err := boundary.prepareCommand(preSubmissionContext, coordinator.journal,
			wipdjournal.CommandInput{ID: commandID, Request: request})
		if errors.Is(err, errBirthReleaseCancelled) {
			return wipdjournal.Entry{}, err
		}
		return entry, err
	}, func(context.Context, CommandStartSnapshot, operation.Command) error {
		return nil
	}, true)
}
