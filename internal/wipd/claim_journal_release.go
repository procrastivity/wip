package wipd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// ClaimJournalReleaseResult is returned after the acquired release receipt and
// verified authority tail have been installed atomically in the Environment.
type ClaimJournalReleaseResult struct {
	Attempt  wipdjournal.ClaimJournalReleaseCommand
	Code     operation.ResultCode
	Receipt  []byte
	Snapshot CommandStartSnapshot
}

// ReleaseClaimJournal closes an acquired claim through the common domain lane.
// Authority-owned lookup, ordered receipt ACK, seal, and release all bind to
// the exact acquired grant and its authority-assigned journal generation.
func (coordinator *CommandStartCoordinator) ReleaseClaimJournal(ctx context.Context, commandID, claimID string,
	claimEpoch uint64, matterID, dispatchID string, actor operation.Actor,
) (ClaimJournalReleaseResult, error) {
	if ctx == nil {
		return ClaimJournalReleaseResult{}, errors.New("wipd: claim-journal release context is required")
	}
	preSubmissionContext, boundary, stop := newBirthReleaseBoundary(ctx, ctx)
	defer stop()
	return coordinator.releaseClaimJournal(preSubmissionContext, context.WithoutCancel(ctx), boundary,
		commandID, claimID, claimEpoch, matterID, dispatchID, actor)
}

func (coordinator *CommandStartCoordinator) releaseClaimJournal(ctx, resolutionContext context.Context, boundary *birthReleaseBoundary,
	commandID, claimID string, claimEpoch uint64, matterID, dispatchID string, actor operation.Actor,
) (ClaimJournalReleaseResult, error) {
	var empty ClaimJournalReleaseResult
	if coordinator == nil || coordinator.journal == nil || coordinator.lanes == nil || coordinator.authority == nil ||
		coordinator.environment == nil || ctx == nil || resolutionContext == nil || boundary == nil || actor == "" ||
		!commandStartULID.MatchString(commandID) || !commandStartULID.MatchString(claimID) || claimEpoch == 0 ||
		!commandStartULID.MatchString(matterID) || !commandStartULID.MatchString(dispatchID) {
		return empty, errors.New("wipd: acquired-claim release dependencies and identity are required")
	}
	authority, ok := coordinator.authority.(ClaimJournalCloseAuthority)
	if !ok || !authority.SupportsClaimJournalClose() {
		return empty, errors.New("wipd: authority does not support acquired claim-journal close")
	}
	environment, ok := coordinator.environment.(ClaimJournalReleaseEnvironment)
	if !ok {
		return empty, errors.New("wipd: Environment does not support acquired claim-journal close")
	}

	attempt, attemptErr := coordinator.journal.ClaimJournalReleaseAttempt(commandID)
	if attemptErr != nil && !errors.Is(attemptErr, wipdjournal.ErrNotFound) {
		return empty, attemptErr
	}
	operationContext := ctx
	submitted := false
	if attemptErr == nil {
		if attempt.Binding.ClaimID != claimID || attempt.Binding.ClaimEpoch != claimEpoch || attempt.Binding.MatterID != matterID ||
			attempt.Binding.DispatchID != dispatchID || claimJournalReleaseActor(attempt) != string(actor) {
			return empty, ErrCommandStartIdentity
		}
		if attempt.Returned {
			return ClaimJournalReleaseResult{Attempt: attempt, Code: attempt.ResultCode, Receipt: bytes.Clone(attempt.Receipt)}, nil
		}
		boundary.markSubmitted()
		submitted = true
		operationContext = resolutionContext
	}

	release, acquired := coordinator.lanes.acquire(operationContext, coordinator.domainID)
	if !acquired {
		if !submitted {
			return empty, errBirthReleaseCancelled
		}
		return empty, operationContext.Err()
	}
	defer release()

	attempt, attemptErr = coordinator.journal.ClaimJournalReleaseAttempt(commandID)
	if attemptErr != nil && !errors.Is(attemptErr, wipdjournal.ErrNotFound) {
		return empty, attemptErr
	}
	if attemptErr == nil {
		if attempt.Binding.ClaimID != claimID || attempt.Binding.ClaimEpoch != claimEpoch || attempt.Binding.MatterID != matterID ||
			attempt.Binding.DispatchID != dispatchID || claimJournalReleaseActor(attempt) != string(actor) {
			return empty, ErrCommandStartIdentity
		}
		if attempt.Returned {
			return ClaimJournalReleaseResult{Attempt: attempt, Code: attempt.ResultCode, Receipt: bytes.Clone(attempt.Receipt)}, nil
		}
		boundary.markSubmitted()
		submitted = true
		operationContext = resolutionContext
	} else {
		submitted = false
		operationContext = ctx
		if err := boundary.checkBeforeSubmission(operationContext); err != nil {
			return empty, err
		}
	}

	grant, acquireAttempt, err := coordinator.journal.InstalledClaimGrantForClaim(claimID)
	if err != nil {
		return empty, err
	}
	if grant.ClaimEpoch != claimEpoch || grant.MatterID != matterID || grant.DispatchID != dispatchID ||
		acquireAttempt.MatterID != matterID || acquireAttempt.DispatchID != dispatchID || acquireAttempt.WorktreeID == "" || acquireAttempt.CloneID == "" {
		return empty, ErrCommandStartIdentity
	}
	claim := &operation.ClaimContext{ID: grant.ClaimID, Epoch: strconv.FormatUint(grant.ClaimEpoch, 10)}
	if err = coordinator.journal.ValidateInstalledClaimContext(operationContext, claim, grant.MatterID, acquireAttempt.WorktreeID); err != nil {
		return empty, err
	}
	hydration, err := coordinator.journal.ClaimHydrationStatus(operationContext, grant.GrantID)
	if err != nil || hydration.State != "offline-ready" || hydration.ClaimID != grant.ClaimID || hydration.ClaimEpoch != grant.ClaimEpoch ||
		hydration.AsOf.EventCount != grant.AsOf.EventCount || hydration.AsOf.Digest != grant.AsOf.Digest || hydration.ManifestDigest != grant.Manifest {
		if err != nil {
			return empty, err
		}
		return empty, wipdjournal.ErrClaimNotReady
	}

	var binding wipdjournal.ClaimJournalBinding
	var barrier wipdwire.JournalBarrier
	var journalReceipts []wipdjournal.ClaimJournalReceipt
	var installed CommandStartSnapshot
	if attemptErr == nil {
		// A prepared release may be replayed after the authority committed it
		// but before its receipt and tail were installed. Do not query a claim
		// that may already be closed; recover only the retained exact identity.
		binding, err = coordinator.journal.InstalledClaimJournalBinding(claimID)
		if err != nil || binding.State != "sealed" || binding.JournalID != attempt.Binding.JournalID ||
			binding.Generation != attempt.Binding.Generation || binding.ClaimEpoch != attempt.Binding.ClaimEpoch ||
			binding.MatterID != attempt.Binding.MatterID || binding.DispatchID != attempt.Binding.DispatchID {
			return empty, ErrCommandStartIdentity
		}
		barrier, journalReceipts, err = coordinator.journal.ClaimJournal(binding)
		if err != nil || !sameClaimJournalBarrier(barrier, attempt.Barrier) ||
			!claimJournalReleaseContextMatches(attempt, acquireAttempt, actor) {
			return empty, ErrCommandStartIdentity
		}
		boundary.markSubmitted()
		operationContext = resolutionContext
		installed, err = coordinator.environment.Snapshot(operationContext)
	} else {
		pendingClose, pendingErr := coordinator.journal.HasPendingClaimJournalRelease()
		if pendingErr != nil || pendingClose {
			return empty, fmt.Errorf("%w: another acquired-claim release outcome is unresolved", ErrCommandStartBlocked)
		}
		pendingBirth, pendingErr := coordinator.journal.HasPendingBirthRelease()
		if pendingErr != nil || pendingBirth {
			return empty, fmt.Errorf("%w: a birth-claim release outcome is unresolved", ErrCommandStartBlocked)
		}
		pendingAcquire, pendingErr := coordinator.journal.HasPendingClaimAcquire()
		if pendingErr != nil || pendingAcquire {
			return empty, fmt.Errorf("%w: a claim acquisition outcome is unresolved", ErrCommandStartBlocked)
		}
		if err = coordinator.returnEligiblePrefix(operationContext, boundary); err != nil {
			return empty, birthReleasePreSubmissionError(operationContext, boundary, err)
		}
		current, lookupErr := authority.CurrentClaimJournal(operationContext, grant)
		if lookupErr != nil {
			return empty, birthReleasePreSubmissionError(operationContext, boundary, fmt.Errorf("claim journal current generation lookup: %w", lookupErr))
		}
		binding, err = coordinator.journal.PinClaimJournalIdentity(operationContext, current)
		if err != nil {
			return empty, ErrCommandStartIdentity
		}
		barrier, journalReceipts, err = coordinator.journal.ClaimJournal(binding)
		if err != nil {
			return empty, err
		}
		installed, err = coordinator.environment.Snapshot(operationContext)
	}
	if err != nil {
		if !submitted {
			return empty, birthReleasePreSubmissionError(operationContext, boundary, err)
		}
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if !validClaimJournalSnapshot(installed, coordinator.domainID) {
		return empty, ErrCommandStartIdentity
	}
	if attemptErr != nil {
		if err = boundary.checkBeforeSubmission(operationContext); err != nil {
			return empty, err
		}
		current, lookupErr := authority.CurrentClaimJournal(operationContext, grant)
		if lookupErr != nil {
			return empty, birthReleasePreSubmissionError(operationContext, boundary, fmt.Errorf("claim journal current generation recheck: %w", lookupErr))
		}
		pinned, pinErr := coordinator.journal.PinClaimJournalIdentity(operationContext, current)
		if pinErr != nil || pinned != binding {
			return empty, ErrCommandStartIdentity
		}
		if binding.State == "open" {
			ackAnchor := installed.Anchor
			if ackAnchor.EventID != nil {
				eventID := *ackAnchor.EventID
				ackAnchor.EventID = &eventID
			}
			for _, item := range journalReceipts {
				if err = boundary.checkBeforeSubmission(operationContext); err != nil {
					return empty, err
				}
				ack := wipdwire.ClaimJournalReceiptAck{
					Schema: "wipd.claim-journal-ack/1", DomainID: installed.DomainID, Epoch: installed.Epoch,
					EnvironmentID: installed.EnvironmentID, ClaimID: binding.ClaimID, ClaimEpoch: binding.ClaimEpoch,
					MatterID: binding.MatterID, DispatchID: binding.DispatchID, JournalID: binding.JournalID,
					Generation: binding.Generation, Position: item.Position,
					Receipt: bytes.Clone(item.Receipt.CanonicalReceipt), Installed: ackAnchor,
				}
				if err = authority.AcknowledgeClaimJournalEntry(operationContext, binding, ack); err != nil {
					return empty, birthReleasePreSubmissionError(operationContext, boundary, fmt.Errorf("claim journal receipt acknowledgment: %w", err))
				}
			}
		}
		if err = boundary.checkBeforeSubmission(operationContext); err != nil {
			return empty, err
		}
		sealed, sealErr := authority.SealClaimJournal(operationContext, binding)
		if sealErr != nil {
			return empty, birthReleasePreSubmissionError(operationContext, boundary, fmt.Errorf("claim journal seal: %w", sealErr))
		}
		if sealed.DomainID != installed.DomainID || sealed.ClaimID != binding.ClaimID || sealed.JournalID != binding.JournalID ||
			sealed.Generation != binding.Generation || sealed.Count != barrier.Count || sealed.Digest != barrier.Digest {
			return empty, ErrCommandStartIdentity
		}
		binding.State = "sealed"
		binding, err = coordinator.journal.PinClaimJournalIdentity(operationContext, binding)
		if err != nil || binding.State != "sealed" {
			return empty, ErrCommandStartIdentity
		}
		barrier.Sealed = true
		attempt, err = boundary.prepareClaimJournalRelease(operationContext, coordinator.journal, commandID, binding, barrier,
			acquireAttempt.CloneID, acquireAttempt.WorktreeID, string(actor))
		if err != nil {
			return empty, err
		}
		boundary.markSubmitted()
		operationContext = resolutionContext
	} else if !sameClaimJournalBarrier(barrier, attempt.Barrier) {
		return empty, ErrCommandStartIdentity
	}

	if !sameClaimJournalBarrier(barrier, attempt.Barrier) || !claimJournalReleaseContextMatches(attempt, acquireAttempt, actor) {
		return empty, ErrCommandStartIdentity
	}
	receipt, code, tail, err := authority.SubmitClaimJournalRelease(operationContext, attempt, installed.Anchor)
	if err != nil {
		return empty, fmt.Errorf("submit acquired-claim release: %w", err)
	}
	if !validResultCode(code) || len(receipt) == 0 || validateBirthReleaseTail(tail, installed, coordinator.domainID) != nil {
		return empty, ErrCommandStartIdentity
	}
	previous := installed
	installed, err = environment.InstallClaimJournalRelease(operationContext, previous, attempt, receipt, tail)
	if err != nil {
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.Revision <= previous.Revision || !sameCommandStartAnchor(installed.Anchor, tail.End) ||
		installed.ManifestDigest != tail.Manifest.Digest || !commandStartReceiptsPreserved(previous.Receipts, installed.Receipts) {
		return empty, ErrCommandStartIdentity
	}
	attempt, err = coordinator.journal.ClaimJournalReleaseAttempt(commandID)
	if err != nil || !attempt.Returned || !bytes.Equal(attempt.Receipt, receipt) || attempt.ResultCode != code {
		return empty, ErrCommandStartIdentity
	}
	return ClaimJournalReleaseResult{Attempt: attempt, Code: code, Receipt: bytes.Clone(receipt), Snapshot: installed}, nil
}

func validClaimJournalSnapshot(snapshot CommandStartSnapshot, domain string) bool {
	return snapshot.DomainID == domain && snapshot.Epoch > 0 && commandStartULID.MatchString(snapshot.EnvironmentID) &&
		snapshot.Revision > 0 && snapshot.Receipts != nil && commandStartHash.MatchString(snapshot.ManifestDigest) && validCommandStartAnchor(snapshot.Anchor)
}

func sameClaimJournalBarrier(left, right wipdwire.JournalBarrier) bool {
	leftBytes, leftErr := wipdwire.EncodeCanonical(left)
	rightBytes, rightErr := wipdwire.EncodeCanonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func claimJournalReleaseActor(attempt wipdjournal.ClaimJournalReleaseCommand) string {
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return ""
	}
	actor, _ := fields["actor"].(string)
	return actor
}

func claimJournalReleaseContextMatches(attempt wipdjournal.ClaimJournalReleaseCommand, acquired wipdjournal.ClaimAcquireAttempt, actor operation.Actor) bool {
	fields, err := wipdwire.DecodeCanonicalMap(attempt.CanonicalBytes,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil || fields["actor"] != string(actor) {
		return false
	}
	contextFields, ok := fields["context"].(map[string]any)
	return ok && contextFields["clone_id"] == acquired.CloneID && contextFields["worktree_id"] == acquired.WorktreeID
}

func validResultCode(code operation.ResultCode) bool {
	return code == operation.ResultSucceeded || code == operation.ResultRejected || code == operation.ResultRefused || code == operation.ResultFailed
}

// acknowledgeLatestClaimJournalHeads recovers an installed receipt whose ACK
// may have been lost with the previous process. Authority admission permits
// only one unacknowledged head per acquired journal, so ACKing the latest
// installed head for each claim is sufficient and remains idempotent.
func (coordinator *CommandStartCoordinator) acknowledgeLatestClaimJournalHeads(ctx context.Context, current wipdjournal.Entry,
	installed CommandStartSnapshot,
) error {
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	latest := make(map[string]wipdjournal.Entry)
	for _, entry := range entries {
		if entry.EnvironmentSeq > current.EnvironmentSeq || entry.Delivery != operation.DeliveryClaim {
			continue
		}
		claim := entry.Command.Request.Claim
		if claim == nil || claim.ID == "" {
			return ErrCommandStartIdentity
		}
		receipt, ok := installed.Receipts[entry.Command.ID]
		if !ok {
			if entry.Command.ID == current.Command.ID && entry.State == wipdjournal.StatePreAdmission {
				continue
			}
			return fmt.Errorf("%w: claim command %s has no installed receipt", ErrCommandStartBlocked, entry.Command.ID)
		}
		if receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
			receipt.JournalPosition != entry.JournalPosition {
			return ErrCommandStartIdentity
		}
		code, receiptErr := commandReceiptCode(entry, receipt.CanonicalReceipt)
		if receiptErr != nil || code != receipt.ResultCode {
			return ErrCommandStartIdentity
		}
		if prior, exists := latest[claim.ID]; exists && prior.Command.Request.Claim.Epoch != claim.Epoch {
			return ErrCommandStartIdentity
		}
		latest[claim.ID] = entry
	}
	if len(latest) == 0 {
		return nil
	}
	for _, entry := range entries {
		claim := entry.Command.Request.Claim
		if entry.EnvironmentSeq > current.EnvironmentSeq || entry.Delivery != operation.DeliveryClaim || claim == nil {
			continue
		}
		head, ok := latest[claim.ID]
		if !ok || head.Command.ID != entry.Command.ID {
			continue
		}
		if err = coordinator.acknowledgeClaimJournalReceipt(ctx, entry, installed); err != nil {
			return err
		}
	}
	return nil
}

// acknowledgeClaimJournalReceipt advances one exact acquired-journal head
// only after its command receipt and the covering authority tail are installed.
func (coordinator *CommandStartCoordinator) acknowledgeClaimJournalReceipt(ctx context.Context, entry wipdjournal.Entry,
	installed CommandStartSnapshot,
) error {
	if coordinator == nil || ctx == nil || entry.Delivery != operation.DeliveryClaim || !validClaimJournalSnapshot(installed, coordinator.domainID) {
		return ErrCommandStartIdentity
	}
	claim := entry.Command.Request.Claim
	if claim == nil {
		return ErrCommandStartIdentity
	}
	receipt, ok := installed.Receipts[entry.Command.ID]
	if !ok || receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
		receipt.JournalPosition != entry.JournalPosition {
		return ErrCommandStartIdentity
	}
	code, err := commandReceiptCode(entry, receipt.CanonicalReceipt)
	if err != nil || code != receipt.ResultCode {
		return ErrCommandStartIdentity
	}
	claimEpoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || claimEpoch == 0 || entry.Command.Request.Context.Repo == "" {
		return ErrCommandStartIdentity
	}
	grant, acquisition, err := coordinator.journal.InstalledClaimGrantForClaim(claim.ID)
	if err != nil || grant.ClaimEpoch != claimEpoch || grant.ClaimID != claim.ID ||
		acquisition.WorktreeID != entry.Command.Request.Context.Worktree ||
		acquisition.CloneID != entry.Command.Request.Context.Clone {
		return ErrCommandStartIdentity
	}
	authority, ok := coordinator.authority.(ClaimJournalCloseAuthority)
	if !ok || !authority.SupportsClaimJournalClose() {
		return errors.New("wipd: authority does not support acquired claim-journal acknowledgment")
	}
	current, err := authority.CurrentClaimJournal(ctx, grant)
	if err != nil || current.State != "open" {
		return ErrCommandStartIdentity
	}
	binding, err := coordinator.journal.PinClaimJournalIdentity(ctx, current)
	if err != nil || binding != current {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	var position uint64
	found := false
	for _, member := range entries {
		memberClaim := member.Command.Request.Claim
		if member.Delivery != operation.DeliveryClaim || memberClaim == nil || memberClaim.ID != claim.ID || member.EnvironmentSeq > entry.EnvironmentSeq {
			continue
		}
		if memberClaim.Epoch != claim.Epoch {
			return ErrCommandStartIdentity
		}
		position++
		if member.Command.ID == entry.Command.ID {
			found = true
		}
	}
	if !found || position == 0 {
		return ErrCommandStartIdentity
	}
	anchor := installed.Anchor
	if anchor.EventID != nil {
		eventID := *anchor.EventID
		anchor.EventID = &eventID
	}
	ack := wipdwire.ClaimJournalReceiptAck{
		Schema: "wipd.claim-journal-ack/1", DomainID: installed.DomainID, Epoch: installed.Epoch,
		EnvironmentID: installed.EnvironmentID, ClaimID: binding.ClaimID, ClaimEpoch: binding.ClaimEpoch,
		MatterID: binding.MatterID, DispatchID: binding.DispatchID, JournalID: binding.JournalID,
		Generation: binding.Generation, Position: position, Receipt: bytes.Clone(receipt.CanonicalReceipt), Installed: anchor,
	}
	return authority.AcknowledgeClaimJournalEntry(ctx, binding, ack)
}
