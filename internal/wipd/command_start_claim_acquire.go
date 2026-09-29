package wipd

import (
	"bytes"
	"context"
	"errors"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
)

// AcquireClaim synchronizes and installs the authority prefix, durably retains
// one exact claim.acquire identity, then installs its verified grant and full
// as-of tail while holding the shared domain lane. An unresolved attempt is
// retried only with its original command bytes and installed anchor.
func (coordinator *CommandStartCoordinator) AcquireClaim(ctx context.Context, commandID, matterID, cloneID, worktreeID, dispatchID string, actor operation.Actor) (ClaimAcquireResult, error) {
	var empty ClaimAcquireResult
	if coordinator == nil || coordinator.journal == nil || coordinator.lanes == nil || coordinator.authority == nil ||
		coordinator.environment == nil || ctx == nil || actor == "" {
		return empty, errors.New("wipd: claim-acquire dependencies are required")
	}
	authority, ok := coordinator.authority.(ClaimAcquireAuthority)
	if !ok {
		return empty, errors.New("wipd: authority does not support claim acquisition")
	}
	release, acquired := coordinator.lanes.acquire(ctx, coordinator.domainID)
	if !acquired {
		return empty, ctx.Err()
	}
	defer release()
	pendingRelease, err := coordinator.journal.HasPendingBirthRelease()
	if err != nil {
		return empty, err
	}
	if pendingRelease {
		return empty, ErrCommandStartBlocked
	}

	attempt, attemptErr := coordinator.journal.ClaimAcquireAttempt(commandID)
	if attemptErr != nil && !errors.Is(attemptErr, wipdjournal.ErrNotFound) {
		return empty, attemptErr
	}
	otherAttemptPending, pendingErr := coordinator.journal.HasUnresolvedClaimAcquire(commandID)
	if pendingErr != nil {
		return empty, pendingErr
	}
	if otherAttemptPending {
		return empty, ErrCommandStartBlocked
	}
	if errors.Is(attemptErr, wipdjournal.ErrNotFound) {
		before, snapshotErr := coordinator.environment.Snapshot(ctx)
		if snapshotErr != nil {
			return empty, snapshotErr
		}
		identity := coordinator.journal.Identity()
		barrierCommand := operation.Command{
			AuthorityDomainID: identity.DomainID, ExpectedAuthorityEpoch: identity.AuthorityEpoch, EnvironmentID: identity.EnvironmentID,
		}
		if !validCommandStartSnapshot(before, barrierCommand) {
			return empty, ErrCommandStartIdentity
		}
		entries, entriesErr := coordinator.journal.Entries()
		if entriesErr != nil {
			return empty, entriesErr
		}
		barrierEntry := wipdjournal.Entry{Command: barrierCommand, EnvironmentSeq: ^uint64(0)}
		pending, pendingErr := commandStartPending(entries, before, barrierEntry)
		if pendingErr != nil {
			return empty, pendingErr
		}
		installed, stopped, returnErr := coordinator.returnPending(ctx, barrierCommand, wipdjournal.Entry{}, before, pending, nil)
		if returnErr != nil {
			return empty, returnErr
		}
		if stopped {
			return empty, ErrCommandStartBlocked
		}
		pull, pullErr := coordinator.authority.Pull(ctx, installed.Anchor)
		if pullErr != nil {
			return empty, pullErr
		}
		if err = validateCommandPull(coordinator.domainID, before.Epoch, installed.Anchor, pull); err != nil {
			return empty, err
		}
		previousSnapshot := installed
		installed, installErr := coordinator.environment.InstallPull(ctx, installed, pull)
		if installErr != nil {
			return empty, installErr
		}
		installed = cloneCommandStartSnapshot(installed)
		if installed.Revision <= previousSnapshot.Revision || !validCommandStartSnapshot(installed, barrierCommand) ||
			!sameCommandStartAnchor(installed.Anchor, pull.End) || installed.ManifestDigest != pull.Manifest.Digest ||
			!commandStartReceiptsPreserved(previousSnapshot.Receipts, installed.Receipts) {
			return empty, ErrCommandStartIdentity
		}
		if attempt, err = coordinator.journal.PrepareClaimAcquire(commandID, matterID, cloneID, worktreeID, dispatchID, string(actor), installed.Anchor); err != nil {
			return empty, err
		}
	} else if attempt.MatterID != matterID || attempt.CloneID != cloneID || attempt.WorktreeID != worktreeID ||
		attempt.DispatchID != dispatchID || attempt.Actor != string(actor) {
		return empty, wipdjournal.ErrCommandIDConflict
	}
	snapshot, err := coordinator.environment.Snapshot(ctx)
	if err != nil {
		return empty, err
	}
	if snapshot.DomainID != coordinator.domainID || snapshot.Anchor.EventCount < attempt.Installed.EventCount ||
		(snapshot.Anchor.EventCount == attempt.Installed.EventCount && !sameCommandStartAnchor(snapshot.Anchor, attempt.Installed)) {
		return empty, ErrCommandStartIdentity
	}
	if attempt.Returned {
		result := ClaimAcquireResult{Attempt: attempt, Code: attempt.ResultCode, Receipt: bytes.Clone(attempt.Receipt), Snapshot: snapshot}
		if attempt.ResultCode == operation.ResultSucceeded {
			grant, grantErr := coordinator.journal.InstalledClaimGrantByCommand(commandID)
			if grantErr != nil || grant.GrantID != attempt.GrantID || grant.RequestHash != attempt.RequestHash {
				return empty, ErrCommandStartIdentity
			}
			result.Grant = &grant
		}
		return result, nil
	}
	if !sameCommandStartAnchor(snapshot.Anchor, attempt.Installed) {
		return empty, ErrCommandStartBlocked
	}
	authorityResult, err := authority.AcquireClaim(ctx, attempt, attempt.Installed)
	if err != nil {
		return empty, err
	}
	if authorityResult.Code != operation.ResultSucceeded {
		if authorityResult.Grant != nil || len(authorityResult.Receipt) == 0 {
			return empty, ErrCommandStartIdentity
		}
		if err = coordinator.journal.InstallClaimAcquireRefusal(commandID, authorityResult.Receipt, authorityResult.Code); err != nil {
			return empty, err
		}
		resolved, lookupErr := coordinator.journal.ClaimAcquireAttempt(commandID)
		if lookupErr != nil {
			return empty, lookupErr
		}
		return ClaimAcquireResult{Attempt: resolved, Code: resolved.ResultCode, Receipt: bytes.Clone(resolved.Receipt), Snapshot: snapshot}, nil
	}
	if authorityResult.Grant == nil {
		return empty, ErrCommandStartIdentity
	}
	installed, err := coordinator.journal.InstallClaimAcquireGrant(ctx, snapshotInstallExpectation(snapshot), commandID, *authorityResult.Grant)
	if err != nil {
		return empty, err
	}
	resolved, err := coordinator.journal.ClaimAcquireAttempt(commandID)
	if err != nil || !resolved.Returned || resolved.ResultCode != operation.ResultSucceeded || len(resolved.Receipt) == 0 {
		return empty, ErrCommandStartIdentity
	}
	grant, err := coordinator.journal.InstalledClaimGrantByCommand(commandID)
	if err != nil || grant.GrantID != resolved.GrantID || grant.RequestHash != resolved.RequestHash ||
		!sameCommandStartAnchor(grant.AsOf, installed.Anchor) {
		return empty, ErrCommandStartIdentity
	}
	snapshot, err = coordinator.environment.Snapshot(ctx)
	if err != nil {
		return empty, err
	}
	return ClaimAcquireResult{Attempt: resolved, Code: resolved.ResultCode, Receipt: bytes.Clone(resolved.Receipt), Grant: &grant, Snapshot: snapshot}, nil
}

func snapshotInstallExpectation(snapshot CommandStartSnapshot) wipdjournal.InstallExpectation {
	return wipdjournal.InstallExpectation{Revision: snapshot.Revision, Anchor: snapshot.Anchor, ManifestDigest: snapshot.ManifestDigest}
}
