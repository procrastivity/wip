package wipd

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// BirthClaimReleaseResult is the locally installed terminal authority outcome
// for one existing claim.release@v1 identity.
type BirthClaimReleaseResult struct {
	Attempt  wipdjournal.BirthReleaseCommand
	Code     operation.ResultCode
	Receipt  []byte
	Snapshot CommandStartSnapshot
}

// ReleaseBirthClaim returns the complete eligible Environment prefix, installs
// each exact terminal fold and resulting tail, acknowledges the installed
// birth-journal receipts in order, and only then submits the durable release
// command. The shared domain lane excludes connected writes throughout.
func (coordinator *CommandStartCoordinator) ReleaseBirthClaim(ctx context.Context, matterID, commandID string, actor operation.Actor) (BirthClaimReleaseResult, error) {
	var empty BirthClaimReleaseResult
	if coordinator == nil || coordinator.journal == nil || coordinator.lanes == nil || coordinator.authority == nil ||
		coordinator.environment == nil || ctx == nil || actor == "" {
		return empty, errors.New("wipd: birth-release dependencies are required")
	}
	release, acquired := coordinator.lanes.acquire(ctx, coordinator.domainID)
	if !acquired {
		return empty, ctx.Err()
	}
	defer release()
	authority, ok := coordinator.authority.(BirthReleaseAuthority)
	if !ok {
		return empty, errors.New("wipd: authority does not support birth-journal release")
	}

	attempt, attemptErr := coordinator.journal.BirthReleaseAttempt(commandID)
	if attemptErr != nil && !errors.Is(attemptErr, wipdjournal.ErrNotFound) {
		return empty, attemptErr
	}
	if attemptErr == nil && attempt.Returned {
		if attempt.Barrier.Journal != matterID || birthReleaseActor(attempt.CanonicalBytes) != string(actor) {
			return empty, ErrCommandStartIdentity
		}
		return BirthClaimReleaseResult{
			Attempt: attempt, Code: attempt.ResultCode,
			Receipt: bytes.Clone(attempt.Receipt),
		}, nil
	}
	if attemptErr != nil {
		if err := coordinator.returnEligiblePrefix(ctx); err != nil {
			return empty, err
		}
	}

	barrier, journalReceipts, err := coordinator.journal.BirthJournal(matterID)
	if err != nil {
		return empty, err
	}
	if attemptErr == nil && !sameBirthBarrier(attempt.Barrier, barrier) {
		return empty, ErrCommandStartIdentity
	}
	installed, err := coordinator.environment.Snapshot(ctx)
	if err != nil {
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.DomainID != coordinator.domainID || installed.Epoch == 0 || installed.EnvironmentID == "" ||
		installed.Revision == 0 || installed.Receipts == nil || !validCommandStartAnchor(installed.Anchor) {
		return empty, ErrCommandStartIdentity
	}
	if attemptErr != nil {
		for _, item := range journalReceipts {
			ackAnchor := installed.Anchor
			if installed.Anchor.EventID != nil {
				eventID := *installed.Anchor.EventID
				ackAnchor.EventID = &eventID
			}
			ack := wipdwire.BirthJournalAck{
				Schema: "wipd.birth-journal-ack/1", DomainID: installed.DomainID, Epoch: installed.Epoch,
				MatterID: matterID, CommandID: item.Entry.Command.ID, RequestHash: item.Entry.RequestHash,
				Receipt: bytes.Clone(item.Receipt.CanonicalReceipt), Installed: ackAnchor,
			}
			if err = authority.AcknowledgeBirthJournalEntry(ctx, ack); err != nil {
				return empty, err
			}
		}
	}
	if attemptErr != nil {
		attempt, err = coordinator.journal.PrepareBirthRelease(commandID, barrier, string(actor))
		if err != nil {
			return empty, err
		}
	}
	if !sameBirthBarrier(attempt.Barrier, barrier) {
		return empty, ErrCommandStartIdentity
	}
	receipt, code, tail, err := authority.SubmitBirthClaimRelease(ctx, attempt, installed.Anchor)
	if err != nil {
		return empty, err
	}
	if code != operation.ResultSucceeded && code != operation.ResultRejected && code != operation.ResultRefused && code != operation.ResultFailed {
		return empty, ErrCommandStartIdentity
	}
	if err = validateBirthReleaseTail(tail, installed, coordinator.domainID); err != nil {
		return empty, err
	}
	previous := installed
	installed, err = coordinator.environment.InstallBirthRelease(ctx, previous, attempt, receipt, tail)
	if err != nil {
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.Revision <= previous.Revision || !sameCommandStartAnchor(installed.Anchor, tail.End) ||
		installed.ManifestDigest != tail.Manifest.Digest || !commandStartReceiptsPreserved(previous.Receipts, installed.Receipts) {
		return empty, ErrCommandStartIdentity
	}
	attempt, err = coordinator.journal.BirthReleaseAttempt(commandID)
	if err != nil || !attempt.Returned || !bytes.Equal(attempt.Receipt, receipt) || attempt.ResultCode != code {
		return empty, ErrCommandStartIdentity
	}
	return BirthClaimReleaseResult{Attempt: attempt, Code: code, Receipt: bytes.Clone(receipt), Snapshot: installed}, nil
}

func (coordinator *CommandStartCoordinator) returnEligiblePrefix(ctx context.Context) error {
	installed, err := coordinator.environment.Snapshot(ctx)
	if err != nil {
		return err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.DomainID != coordinator.domainID || installed.Epoch == 0 || installed.EnvironmentID == "" ||
		installed.Revision == 0 || installed.Receipts == nil || !validCommandStartAnchor(installed.Anchor) {
		return ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return err
	}
	pending := make([]wipdjournal.Entry, 0)
	for _, entry := range entries {
		if receipt, ok := installed.Receipts[entry.Command.ID]; ok {
			if receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
				receipt.JournalPosition != entry.JournalPosition {
				return ErrCommandStartIdentity
			}
			code, receiptErr := commandReceiptCode(entry, receipt.CanonicalReceipt)
			if receiptErr != nil || code != receipt.ResultCode {
				return ErrCommandStartIdentity
			}
			if code != operation.ResultSucceeded {
				return fmt.Errorf("%w: prior command %s is terminal %s", ErrCommandStartBlocked, entry.Command.ID, code)
			}
			continue
		}
		switch entry.State {
		case wipdjournal.StatePendingReturn:
			pending = append(pending, entry)
		case wipdjournal.StatePreAdmission, wipdjournal.StateAttemptPrepared:
			return fmt.Errorf("%w: prior command %s is unresolved or not admitted", ErrCommandStartBlocked, entry.Command.ID)
		default:
			return fmt.Errorf("%w: command %s has no installed terminal receipt", ErrCommandStartBlocked, entry.Command.ID)
		}
	}
	for index, entry := range pending {
		if err = coordinator.validateReturnEligibility(entry, installed); err != nil {
			return err
		}
		fold, foldErr := coordinator.authority.Return(ctx, entry, installed.Anchor)
		if foldErr != nil {
			return foldErr
		}
		if err = validateCommandFold(entry, installed.Anchor, fold); err != nil {
			return err
		}
		if fold.Continue && (fold.ResultCode != operation.ResultSucceeded || index+1 == len(pending)) {
			return ErrCommandStartIdentity
		}
		previous := installed
		installed, err = coordinator.environment.InstallFold(ctx, installed, entry, fold)
		if err != nil {
			return err
		}
		installed = cloneCommandStartSnapshot(installed)
		if installed.Revision <= previous.Revision || !sameCommandStartAnchor(installed.Anchor, fold.End) ||
			installed.ManifestDigest != fold.Manifest.Digest || !commandStartReceiptsPreserved(previous.Receipts, installed.Receipts) {
			return ErrCommandStartIdentity
		}
		receipt, ok := installed.Receipts[entry.Command.ID]
		if !ok || receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
			receipt.JournalPosition != entry.JournalPosition || receipt.ResultCode != fold.ResultCode ||
			!bytes.Equal(receipt.CanonicalReceipt, fold.CanonicalReceipt) {
			return ErrCommandStartIdentity
		}
		if fold.ResultCode != operation.ResultSucceeded {
			return fmt.Errorf("%w: authority quarantined birth-journal prefix command %s with %s", ErrCommandStartBlocked, entry.Command.ID, fold.ResultCode)
		}
		if index+1 < len(pending) && !fold.Continue {
			return fmt.Errorf("%w: authority stopped before birth-journal suffix command %s", ErrCommandStartBlocked, pending[index+1].Command.ID)
		}
	}
	pull, err := coordinator.authority.Pull(ctx, installed.Anchor)
	if err != nil {
		return err
	}
	if err = validateBirthReleaseTail(pull, installed, coordinator.domainID); err != nil {
		return err
	}
	previous := installed
	installed, err = coordinator.environment.InstallPull(ctx, installed, pull)
	if err != nil {
		return err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.Revision <= previous.Revision || !sameCommandStartAnchor(installed.Anchor, pull.End) ||
		installed.ManifestDigest != pull.Manifest.Digest || !commandStartReceiptsPreserved(previous.Receipts, installed.Receipts) {
		return ErrCommandStartIdentity
	}
	return nil
}

func validateBirthReleaseTail(tail CommandPull, installed CommandStartSnapshot, domainID string) error {
	if tail.DomainID != domainID || tail.Epoch != installed.Epoch || !sameCommandStartAnchor(tail.Start, installed.Anchor) ||
		!validCommandStartAnchor(tail.End) || !tail.VerifiedTransfer.Valid() || tail.VerifiedTransfer.DomainID() != tail.DomainID ||
		tail.VerifiedTransfer.Epoch() != tail.Epoch || !sameCommandStartAnchor(tail.VerifiedTransfer.Start(), tail.Start) ||
		!sameCommandStartAnchor(tail.VerifiedTransfer.End(), tail.End) || tail.Manifest.Schema != "wipd.blob-manifest/1" ||
		tail.Manifest.DomainID != tail.DomainID || tail.Manifest.Epoch != tail.Epoch || !sameCommandStartAnchor(tail.Manifest.AsOf, tail.End) ||
		!commandStartHash.MatchString(tail.Manifest.Digest) || tail.VerifiedTransfer.Manifest().Digest != tail.Manifest.Digest ||
		tail.End.EventCount < tail.Start.EventCount {
		return ErrCommandStartIdentity
	}
	return nil
}

func birthReleaseActor(canonical []byte) string {
	fields, err := wipdwire.DecodeCanonicalMap(canonical,
		"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs")
	if err != nil {
		return ""
	}
	actor, _ := fields["actor"].(string)
	return actor
}

func sameBirthBarrier(left, right wipdwire.JournalBarrier) bool {
	leftBytes, leftErr := wipdwire.EncodeCanonical(left)
	rightBytes, rightErr := wipdwire.EncodeCanonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}
