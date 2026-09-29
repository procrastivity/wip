package wipd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

var (
	// ErrCommandStartBlocked means an earlier command has unresolved or
	// ineligible local state, so pull and a later guard/write are forbidden.
	ErrCommandStartBlocked = errors.New("wipd: command start blocked by pending Environment work")
	// ErrCommandStartIdentity means a receipt or fold product is not bound to
	// the exact immutable journal entry being returned.
	ErrCommandStartIdentity = errors.New("wipd: command-start identity mismatch")

	commandStartULID        = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	commandStartStepLocator = regexp.MustCompile(`^step-[0-9]{2,}$`)
	commandStartHash        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// CommandStartSnapshot is one stable Environment view used for the entire
// command-start barrier. The implementation obtains the installed prefix,
// terminal receipt index, and overlay revision from one read transaction.
type CommandStartSnapshot struct {
	DomainID       string
	Epoch          uint64
	EnvironmentID  string
	Revision       uint64
	Anchor         wipdwire.PrefixAnchor
	ManifestDigest string
	Receipts       map[string]InstalledCommandReceipt
}

// InstalledCommandReceipt is the local terminal evidence needed to decide
// whether an older immutable journal entry is resolved or blocks dependents.
type InstalledCommandReceipt struct {
	RequestHash      string
	EnvironmentSeq   uint64
	JournalPosition  uint64
	ResultCode       operation.ResultCode
	CanonicalReceipt []byte
}

// CommandFold is one verified authority return result. Transfer carries the
// authority-verified prefix delta and complete manifest for the atomic local
// installation; the coordinator never interprets or partially installs it.
type CommandFold struct {
	DomainID         string
	Epoch            uint64
	CommandID        string
	RequestHash      string
	EnvironmentID    string
	EnvironmentSeq   uint64
	JournalPosition  uint64
	Start            wipdwire.PrefixAnchor
	End              wipdwire.PrefixAnchor
	ResultCode       operation.ResultCode
	Continue         bool
	CanonicalReceipt []byte
	Manifest         wipdwire.BlobManifest
	VerifiedTransfer wipdjournal.VerifiedTransfer
}

// CommandPull is one complete, verified authority tail pinned to one snapshot.
type CommandPull struct {
	DomainID         string
	Epoch            uint64
	Start            wipdwire.PrefixAnchor
	End              wipdwire.PrefixAnchor
	Manifest         wipdwire.BlobManifest
	VerifiedTransfer wipdjournal.VerifiedTransfer
}

// CommandStartAuthority performs the Step 6 authenticated return and pull
// exchanges. Return must preserve the journal command's ID and hash; Pull must
// return a complete verified prefix from the exact requested anchor.
type CommandStartAuthority interface {
	Return(context.Context, wipdjournal.Entry, wipdwire.PrefixAnchor) (CommandFold, error)
	Pull(context.Context, wipdwire.PrefixAnchor) (CommandPull, error)
}

// BirthReleaseAuthority acknowledges receipts only after their exact fold is
// installed locally, then submits the existing claim.release@v1 command and
// returns its terminal receipt with the complete verified authority tail.
type BirthReleaseAuthority interface {
	AcknowledgeBirthJournalEntry(context.Context, wipdwire.BirthJournalAck) error
	SubmitBirthClaimRelease(context.Context, wipdjournal.BirthReleaseCommand, wipdwire.PrefixAnchor) ([]byte, operation.ResultCode, CommandPull, error)
}

// ClaimAcquireAuthority submits one exact lifecycle identity and returns only
// a terminal refusal or a signature-verified pinned grant product.
type ClaimAcquireAuthority interface {
	AcquireClaim(context.Context, wipdjournal.ClaimAcquireAttempt, wipdwire.PrefixAnchor) (ClaimAcquireAuthorityResult, error)
}

// ClaimAcquireAuthorityResult is one terminal authority disposition. Grant is
// present only for a successful acquisition and has already been verified.
type ClaimAcquireAuthorityResult struct {
	Code    operation.ResultCode
	Receipt []byte
	Grant   *wipdjournal.VerifiedClaimGrant
}

// ClaimAcquireResult is the installed Environment product of claim.acquire.
type ClaimAcquireResult struct {
	Attempt  wipdjournal.ClaimAcquireAttempt
	Code     operation.ResultCode
	Receipt  []byte
	Grant    *wipdjournal.ClaimGrantSummary
	Snapshot CommandStartSnapshot
}

// CommandStartEnvironment owns the local atomic commit boundaries. InstallFold
// commits the exact receipt, returned prefix delta, complete manifest, return
// status, and rebuilt overlay in one transaction. InstallPull commits the
// complete tail and rebuilt overlay in one transaction. AdmitPending makes a
// new deferable command's overlay recoverable before its semantic callback.
type CommandStartEnvironment interface {
	Snapshot(context.Context) (CommandStartSnapshot, error)
	InstallFold(context.Context, CommandStartSnapshot, wipdjournal.Entry, CommandFold) (CommandStartSnapshot, error)
	InstallPull(context.Context, CommandStartSnapshot, CommandPull) (CommandStartSnapshot, error)
	InstallBirthRelease(context.Context, CommandStartSnapshot, wipdjournal.BirthReleaseCommand, []byte, CommandPull) (CommandStartSnapshot, error)
	AdmitPending(context.Context, CommandStartSnapshot, wipdjournal.Entry) (CommandStartSnapshot, error)
	commandStartJournal() *wipdjournal.Journal
}

// CommandStartCoordinator is the common D120/D128 path for a connected
// command. It persists the caller's identity before synchronization and holds
// the shared per-domain lane through return, pull, overlay installation, and
// the caller's guard/write callback.
type CommandStartCoordinator struct {
	domainID    string
	journal     *wipdjournal.Journal
	lanes       *executionLanes
	authority   CommandStartAuthority
	environment CommandStartEnvironment
}

// NewCommandStartCoordinator composes the connected command-start path with
// the Server's shared per-domain lane. It does not register or execute any
// operation handlers itself.
func (server *Server) NewCommandStartCoordinator(domainID string, journal *wipdjournal.Journal, authority CommandStartAuthority, environment CommandStartEnvironment) (*CommandStartCoordinator, error) {
	if server == nil || server.executionLanes == nil || !commandStartULID.MatchString(domainID) || journal == nil || authority == nil ||
		environment == nil || environment.commandStartJournal() != journal {
		return nil, errors.New("wipd: command-start dependencies are required")
	}
	return &CommandStartCoordinator{
		domainID: domainID, journal: journal, lanes: server.executionLanes, authority: authority, environment: environment,
	}, nil
}

// WithStableReadSnapshot acquires the same per-domain lane as connected
// commands and holds it while the caller consumes one Environment snapshot.
func (coordinator *CommandStartCoordinator) WithStableReadSnapshot(ctx context.Context, read func(context.Context, CommandStartSnapshot) error) error {
	if coordinator == nil || coordinator.lanes == nil || coordinator.environment == nil || ctx == nil || read == nil {
		return errors.New("wipd: stable-read dependencies are required")
	}
	release, acquired := coordinator.lanes.acquire(ctx, coordinator.domainID)
	if !acquired {
		return ctx.Err()
	}
	defer release()
	snapshot, err := coordinator.environment.Snapshot(ctx)
	if err != nil {
		return err
	}
	snapshot = cloneCommandStartSnapshot(snapshot)
	if snapshot.DomainID != coordinator.domainID || snapshot.Epoch == 0 || !commandStartULID.MatchString(snapshot.EnvironmentID) ||
		snapshot.Revision == 0 || snapshot.Receipts == nil || !commandStartHash.MatchString(snapshot.ManifestDigest) || !validCommandStartAnchor(snapshot.Anchor) {
		return ErrCommandStartIdentity
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return read(ctx, snapshot)
}

// CommandStartResult distinguishes an exact retry resolved by the return path
// from a newly admitted command handed to its semantic guard/write callback.
type CommandStartResult struct {
	Entry          wipdjournal.Entry
	Returned       bool
	ResultCode     operation.ResultCode
	Receipt        []byte
	SemanticResult operation.Result
}

// RunConnected admits the exact command in the Environment journal, returns
// every eligible older journal head in Environment-sequence order, installs
// the authority tail, and only then calls guardAndWrite with the stable
// post-install snapshot. The callback runs while the domain lane remains held.
// An exact retry of a locally pending command is returned without rerunning
// its semantic callback.
func (coordinator *CommandStartCoordinator) RunConnected(ctx context.Context, input wipdjournal.CommandInput, guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error) (CommandStartResult, error) {
	return coordinator.runConnected(ctx, input, guardAndWrite, false)
}

// RunConnectedTerminal uses the same journal admission, ordered return, pull,
// and per-domain lane as RunConnected, then returns and atomically installs
// the new command's terminal authority outcome before releasing that lane.
// guardAndWrite is the post-pull authority-submission callback; it must not
// write provisional semantic state locally.
func (coordinator *CommandStartCoordinator) RunConnectedTerminal(ctx context.Context, input wipdjournal.CommandInput, guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error) (CommandStartResult, error) {
	return coordinator.runConnected(ctx, input, guardAndWrite, true)
}

// RunConnectedCanonicalTerminal routes a complete M2 command identity through
// the same admission/return/pull/overlay path without reassigning its
// Environment sequence or acted time. The journal accepts it only at the
// exact next sequence for its bound Environment.
func (coordinator *CommandStartCoordinator) RunConnectedCanonicalTerminal(ctx context.Context, command operation.Command, guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error) (CommandStartResult, error) {
	return coordinator.runConnectedPrepared(ctx, func() (wipdjournal.Entry, error) {
		return coordinator.journal.PrepareCanonicalCommand(command)
	}, guardAndWrite, true)
}

func (coordinator *CommandStartCoordinator) runConnected(ctx context.Context, input wipdjournal.CommandInput, guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error, terminal bool) (CommandStartResult, error) {
	return coordinator.runConnectedPrepared(ctx, func() (wipdjournal.Entry, error) {
		return coordinator.journal.PrepareCommand(input)
	}, guardAndWrite, terminal)
}

func (coordinator *CommandStartCoordinator) runConnectedPrepared(ctx context.Context, prepare func() (wipdjournal.Entry, error), guardAndWrite func(context.Context, CommandStartSnapshot, operation.Command) error, terminal bool) (CommandStartResult, error) {
	var empty CommandStartResult
	if coordinator == nil || coordinator.journal == nil || coordinator.lanes == nil || coordinator.authority == nil || coordinator.environment == nil || prepare == nil ||
		ctx == nil || guardAndWrite == nil {
		return empty, errors.New("wipd: command-start coordinator is incomplete")
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
		return empty, fmt.Errorf("%w: a birth-claim release outcome is unresolved", ErrCommandStartBlocked)
	}
	entry, err := prepare()
	if err != nil {
		return empty, err
	}
	if entry.Command.AuthorityDomainID != coordinator.domainID {
		return empty, ErrCommandStartIdentity
	}
	if err = validateConnectedCommand(entry); err != nil {
		return empty, err
	}
	result := CommandStartResult{Entry: entry}

	installed, err := coordinator.environment.Snapshot(ctx)
	if err != nil {
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if !validCommandStartSnapshot(installed, entry.Command) {
		return empty, ErrCommandStartIdentity
	}
	entries, err := coordinator.journal.Entries()
	if err != nil {
		return empty, err
	}
	pending, err := commandStartPending(entries, installed, entry)
	if err != nil {
		return empty, err
	}
	if receipt, ok := installed.Receipts[entry.Command.ID]; ok {
		if receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq || receipt.JournalPosition != entry.JournalPosition {
			return empty, ErrCommandStartIdentity
		}
		code, receiptErr := commandReceiptCode(entry, receipt.CanonicalReceipt)
		if receiptErr != nil || code != receipt.ResultCode {
			return empty, ErrCommandStartIdentity
		}
		if len(pending) != 0 {
			return empty, ErrCommandStartIdentity
		}
		result.Returned = true
		result.ResultCode = code
		result.Receipt = append([]byte(nil), receipt.CanonicalReceipt...)
		result.SemanticResult, err = commandReceiptResult(entry, result.Receipt)
		if err != nil {
			return empty, ErrCommandStartIdentity
		}
		if code != operation.ResultSucceeded {
			return result, nil
		}
	}
	for index, pendingEntry := range pending {
		if err = coordinator.validateReturnEligibility(pendingEntry, installed); err != nil {
			return empty, err
		}
		fold, foldErr := coordinator.authority.Return(ctx, pendingEntry, installed.Anchor)
		if foldErr != nil {
			return empty, foldErr
		}
		if err = validateCommandFold(pendingEntry, installed.Anchor, fold); err != nil {
			return empty, err
		}
		if fold.Continue && (fold.ResultCode != operation.ResultSucceeded || index+1 == len(pending)) {
			return empty, ErrCommandStartIdentity
		}
		previousRevision := installed.Revision
		previousSnapshot := installed
		installed, err = coordinator.environment.InstallFold(ctx, installed, pendingEntry, fold)
		if err != nil {
			return empty, err
		}
		installed = cloneCommandStartSnapshot(installed)
		if installed.Revision <= previousRevision || !validCommandStartSnapshot(installed, entry.Command) ||
			!sameCommandStartAnchor(installed.Anchor, fold.End) || !commandStartReceiptsPreserved(previousSnapshot.Receipts, installed.Receipts) {
			return empty, ErrCommandStartIdentity
		}
		installedReceipt, ok := installed.Receipts[pendingEntry.Command.ID]
		if !ok || installedReceipt.RequestHash != pendingEntry.RequestHash || installedReceipt.EnvironmentSeq != pendingEntry.EnvironmentSeq ||
			installedReceipt.JournalPosition != pendingEntry.JournalPosition || installedReceipt.ResultCode != fold.ResultCode ||
			!bytes.Equal(installedReceipt.CanonicalReceipt, fold.CanonicalReceipt) || installed.ManifestDigest != fold.Manifest.Digest {
			return empty, ErrCommandStartIdentity
		}
		if pendingEntry.Command.ID == entry.Command.ID {
			result.Returned = true
			result.ResultCode = fold.ResultCode
			result.Receipt = append([]byte(nil), fold.CanonicalReceipt...)
			result.SemanticResult, err = commandReceiptResult(entry, result.Receipt)
			if err != nil {
				return empty, ErrCommandStartIdentity
			}
		}
		if fold.ResultCode != operation.ResultSucceeded {
			if pendingEntry.Command.ID == entry.Command.ID {
				return result, nil
			}
			return empty, fmt.Errorf("%w: returned command %s completed with %s", ErrCommandStartBlocked,
				pendingEntry.Command.ID, fold.ResultCode)
		}
		if index+1 < len(pending) && !fold.Continue {
			return empty, fmt.Errorf("%w: authority stopped before the next pending head", ErrCommandStartBlocked)
		}
	}

	pull, err := coordinator.authority.Pull(ctx, installed.Anchor)
	if err != nil {
		return empty, err
	}
	if pull.DomainID != entry.Command.AuthorityDomainID || pull.Epoch != entry.Command.ExpectedAuthorityEpoch ||
		!sameCommandStartAnchor(pull.Start, installed.Anchor) || !validCommandStartAnchor(pull.End) || !pull.VerifiedTransfer.Valid() ||
		pull.VerifiedTransfer.DomainID() != pull.DomainID || pull.VerifiedTransfer.Epoch() != pull.Epoch ||
		!sameCommandStartAnchor(pull.VerifiedTransfer.Start(), pull.Start) || !sameCommandStartAnchor(pull.VerifiedTransfer.End(), pull.End) ||
		pull.Manifest.Schema != "wipd.blob-manifest/1" || pull.Manifest.DomainID != pull.DomainID || pull.Manifest.Epoch != pull.Epoch ||
		!sameCommandStartAnchor(pull.Manifest.AsOf, pull.End) || !commandStartHash.MatchString(pull.Manifest.Digest) ||
		pull.VerifiedTransfer.Manifest().Digest != pull.Manifest.Digest ||
		pull.End.EventCount < pull.Start.EventCount || pull.End.EventCount == pull.Start.EventCount && !sameCommandStartAnchor(pull.End, pull.Start) {
		return empty, ErrCommandStartIdentity
	}
	previousSnapshot := installed
	installed, err = coordinator.environment.InstallPull(ctx, installed, pull)
	if err != nil {
		return empty, err
	}
	installed = cloneCommandStartSnapshot(installed)
	if installed.Revision <= previousSnapshot.Revision || !validCommandStartSnapshot(installed, entry.Command) ||
		!sameCommandStartAnchor(installed.Anchor, pull.End) || installed.ManifestDigest != pull.Manifest.Digest ||
		!commandStartReceiptsPreserved(previousSnapshot.Receipts, installed.Receipts) {
		return empty, ErrCommandStartIdentity
	}

	if result.Returned {
		return result, nil
	}
	if !entry.Created && entry.State != wipdjournal.StatePreAdmission {
		return empty, fmt.Errorf("%w: exact retry %s did not resolve through its installed receipt", ErrCommandStartBlocked, entry.Command.ID)
	}
	if entry.State == wipdjournal.StatePreAdmission {
		if err = coordinator.validateReturnEligibility(entry, installed); err != nil {
			return empty, err
		}
		previousRevision := installed.Revision
		previousSnapshot := installed
		previousAnchor := installed.Anchor
		previousManifest := installed.ManifestDigest
		installed, err = coordinator.environment.AdmitPending(ctx, installed, entry)
		if err != nil {
			return empty, err
		}
		installed = cloneCommandStartSnapshot(installed)
		if installed.Revision <= previousRevision || !validCommandStartSnapshot(installed, entry.Command) ||
			!sameCommandStartAnchor(installed.Anchor, previousAnchor) || installed.ManifestDigest != previousManifest ||
			!commandStartReceiptsPreserved(previousSnapshot.Receipts, installed.Receipts) {
			return empty, ErrCommandStartIdentity
		}
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	if err = guardAndWrite(ctx, installed, entry.Command); err != nil {
		return empty, err
	}
	if terminal {
		fold, foldErr := coordinator.authority.Return(ctx, entry, installed.Anchor)
		if foldErr != nil {
			return empty, foldErr
		}
		if err = validateCommandFold(entry, installed.Anchor, fold); err != nil {
			return empty, err
		}
		previousSnapshot := installed
		installed, err = coordinator.environment.InstallFold(ctx, installed, entry, fold)
		if err != nil {
			return empty, err
		}
		installed = cloneCommandStartSnapshot(installed)
		if installed.Revision <= previousSnapshot.Revision || !validCommandStartSnapshot(installed, entry.Command) ||
			!sameCommandStartAnchor(installed.Anchor, fold.End) || !commandStartReceiptsPreserved(previousSnapshot.Receipts, installed.Receipts) ||
			installed.ManifestDigest != fold.Manifest.Digest {
			return empty, ErrCommandStartIdentity
		}
		receipt, ok := installed.Receipts[entry.Command.ID]
		if !ok || receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq ||
			receipt.JournalPosition != entry.JournalPosition || receipt.ResultCode != fold.ResultCode ||
			!bytes.Equal(receipt.CanonicalReceipt, fold.CanonicalReceipt) {
			return empty, ErrCommandStartIdentity
		}
		result.Returned = true
		result.ResultCode = fold.ResultCode
		result.Receipt = append([]byte(nil), fold.CanonicalReceipt...)
		result.SemanticResult, err = commandReceiptResult(entry, result.Receipt)
		if err != nil {
			return empty, ErrCommandStartIdentity
		}
	}
	return result, nil
}

func asCommandStartString(value any) string {
	text, _ := value.(string)
	return text
}

func commandStartPending(entries []wipdjournal.Entry, installed CommandStartSnapshot, current wipdjournal.Entry) ([]wipdjournal.Entry, error) {
	pending := make([]wipdjournal.Entry, 0)
	for _, entry := range entries {
		if entry.EnvironmentSeq > current.EnvironmentSeq || entry.EnvironmentSeq == current.EnvironmentSeq && entry.Command.ID != current.Command.ID {
			continue
		}
		receipt, resolved := installed.Receipts[entry.Command.ID]
		if resolved {
			if receipt.RequestHash != entry.RequestHash || receipt.EnvironmentSeq != entry.EnvironmentSeq || receipt.JournalPosition != entry.JournalPosition {
				return nil, ErrCommandStartIdentity
			}
			resultCode, receiptErr := commandReceiptCode(entry, receipt.CanonicalReceipt)
			if receiptErr != nil || resultCode != receipt.ResultCode {
				return nil, ErrCommandStartIdentity
			}
			if resultCode != operation.ResultSucceeded {
				if entry.Command.ID == current.Command.ID && entry.EnvironmentSeq == current.EnvironmentSeq {
					continue
				}
				return nil, fmt.Errorf("%w: prior command %s has terminal %s receipt", ErrCommandStartBlocked, entry.Command.ID, receipt.ResultCode)
			}
			continue
		}
		if entry.Command.ID == current.Command.ID && entry.State == wipdjournal.StatePreAdmission {
			continue
		}
		if entry.State == wipdjournal.StateAttemptPrepared {
			return nil, fmt.Errorf("%w: prior authority attempt %s is unresolved", ErrCommandStartBlocked, entry.Command.ID)
		}
		if entry.State == wipdjournal.StatePreAdmission {
			return nil, fmt.Errorf("%w: prior command %s has not committed recoverable overlay admission", ErrCommandStartBlocked, entry.Command.ID)
		}
		if entry.State != wipdjournal.StatePendingReturn {
			return nil, ErrCommandStartBlocked
		}
		pending = append(pending, entry)
	}
	return pending, nil
}

func (coordinator *CommandStartCoordinator) validateReturnEligibility(entry wipdjournal.Entry, installed CommandStartSnapshot) error {
	if err := validateConnectedCommand(entry); err != nil {
		return err
	}
	if entry.Command.Request.Operation == operation.StepCreateV1.Metadata().Operation {
		return coordinator.validateImplicitBirthDependency(entry, installed)
	}
	if causation := entry.Command.CausationCommandID; causation != "" && causation != entry.Command.ID {
		receipt, ok := installed.Receipts[causation]
		if !ok || receipt.ResultCode != operation.ResultSucceeded {
			return fmt.Errorf("%w: causation %s is not terminal-success", ErrCommandStartBlocked, causation)
		}
	}
	return nil
}

func (coordinator *CommandStartCoordinator) validateImplicitBirthDependency(entry wipdjournal.Entry, installed CommandStartSnapshot) error {
	input := entry.Command.Request.Input.(operation.StepCreateInput)
	birthID := entry.Command.CausationCommandID
	birth, err := coordinator.journal.Get(birthID)
	if err != nil || birth.State != wipdjournal.StateReturned || birth.Command.Request.Operation != operation.MatterCreateV1.Metadata().Operation ||
		birth.Command.CausationCommandID != "" || birth.Command.CorrelationCommandID != birthID ||
		birth.Command.EnvironmentID != entry.Command.EnvironmentID || birth.Command.Request.Context.Repo != entry.Command.Request.Context.Repo ||
		birth.EnvironmentSeq >= entry.EnvironmentSeq {
		return fmt.Errorf("%w: Step parent birth command is not an earlier returned Matter in this Environment and Repo", ErrCommandStartBlocked)
	}
	receipt, ok := installed.Receipts[birthID]
	if !ok || receipt.RequestHash != birth.RequestHash || receipt.EnvironmentSeq != birth.EnvironmentSeq ||
		receipt.JournalPosition != birth.JournalPosition || receipt.ResultCode != operation.ResultSucceeded {
		return fmt.Errorf("%w: Step parent lacks its installed terminal-success receipt", ErrCommandStartBlocked)
	}
	code, err := commandReceiptCode(birth, receipt.CanonicalReceipt)
	if err != nil || code != operation.ResultSucceeded {
		return fmt.Errorf("%w: Step parent birth receipt is invalid or unsuccessful", ErrCommandStartBlocked)
	}
	fields, err := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return ErrCommandStartIdentity
	}
	result, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(result, "code", "output", "problem_code") {
		return ErrCommandStartIdentity
	}
	outputBytes, ok := result["output"].([]byte)
	if !ok {
		return ErrCommandStartIdentity
	}
	output, err := wipdwire.DecodeCanonicalMap(outputBytes, "id", "locator", "title")
	if err != nil || output["id"] != input.ParentID {
		return fmt.Errorf("%w: Step claim does not name the Matter established by its causation receipt", ErrCommandStartBlocked)
	}
	return nil
}

func validateConnectedCommand(entry wipdjournal.Entry) error {
	if entry.Delivery != operation.DeliveryProvisional && entry.Delivery != operation.DeliveryEnvironment {
		return fmt.Errorf("%w: delivery %s is not eligible for connected command start", ErrCommandStartBlocked, entry.Delivery)
	}
	var metadata *operation.Metadata
	for _, definition := range operation.Catalogue() {
		candidate := definition.Metadata()
		if candidate.Operation == entry.Command.Request.Operation {
			metadata = &candidate
			break
		}
	}
	if metadata == nil || metadata.Delivery != entry.Delivery {
		return fmt.Errorf("%w: operation metadata does not establish eligibility for %s", ErrCommandStartBlocked, entry.Command.ID)
	}
	switch metadata.Claim {
	case operation.ClaimNone:
		if entry.Command.Request.Claim != nil {
			return fmt.Errorf("%w: claim supplied to an unclaimed operation", ErrCommandStartBlocked)
		}
	case operation.ClaimImplicitBirth:
		input, ok := entry.Command.Request.Input.(operation.StepCreateInput)
		claim := entry.Command.Request.Claim
		if !ok || entry.Command.Request.Operation != operation.StepCreateV1.Metadata().Operation || claim == nil ||
			claim.ID != input.ParentID || claim.Epoch != "1" || entry.Command.CausationCommandID == "" ||
			entry.Command.CorrelationCommandID != entry.Command.CausationCommandID || entry.Command.CausationCommandID == entry.Command.ID {
			return fmt.Errorf("%w: Step birth lacks its exact provisional Matter claim", ErrCommandStartBlocked)
		}
	default:
		return fmt.Errorf("%w: claim lifecycle is not part of the provisional birth subset", ErrCommandStartBlocked)
	}
	return nil
}

func validateCommandFold(entry wipdjournal.Entry, start wipdwire.PrefixAnchor, fold CommandFold) error {
	if fold.DomainID != entry.Command.AuthorityDomainID || fold.Epoch != entry.Command.ExpectedAuthorityEpoch ||
		fold.CommandID != entry.Command.ID || fold.RequestHash != entry.RequestHash || fold.EnvironmentID != entry.Command.EnvironmentID ||
		fold.EnvironmentSeq != entry.EnvironmentSeq || fold.JournalPosition != entry.JournalPosition ||
		!sameCommandStartAnchor(fold.Start, start) || !validCommandStartAnchor(fold.End) ||
		fold.End.EventCount < fold.Start.EventCount || len(fold.CanonicalReceipt) == 0 || !fold.VerifiedTransfer.Valid() ||
		fold.VerifiedTransfer.DomainID() != fold.DomainID || fold.VerifiedTransfer.Epoch() != fold.Epoch ||
		!sameCommandStartAnchor(fold.VerifiedTransfer.Start(), fold.Start) || !sameCommandStartAnchor(fold.VerifiedTransfer.End(), fold.End) ||
		fold.Manifest.Schema != "wipd.blob-manifest/1" || fold.Manifest.DomainID != fold.DomainID || fold.Manifest.Epoch != fold.Epoch ||
		!sameCommandStartAnchor(fold.Manifest.AsOf, fold.End) || !commandStartHash.MatchString(fold.Manifest.Digest) ||
		fold.VerifiedTransfer.Manifest().Digest != fold.Manifest.Digest {
		return ErrCommandStartIdentity
	}
	if fold.End.EventCount == fold.Start.EventCount && !sameCommandStartAnchor(fold.End, fold.Start) {
		return ErrCommandStartIdentity
	}
	return nil
}

func commandReceiptCode(entry wipdjournal.Entry, raw []byte) (operation.ResultCode, error) {
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil || fields["schema"] != "wipd.terminal-receipt/1" || fields["domain_id"] != entry.Command.AuthorityDomainID ||
		fields["authority_epoch"] != entry.Command.ExpectedAuthorityEpoch || fields["identity_schema"] != "wipd.command/1" ||
		fields["command_id"] != entry.Command.ID || fields["request_hash"] != entry.RequestHash {
		return "", ErrCommandStartIdentity
	}
	operationFields, ok := fields["operation"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(operationFields, "name", "version") ||
		operationFields["name"] != entry.Command.Request.Operation.Name || operationFields["version"] != uint64(entry.Command.Request.Operation.Version) {
		return "", ErrCommandStartIdentity
	}
	environmentFields, ok := fields["environment"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(environmentFields, "id", "sequence") ||
		environmentFields["id"] != entry.Command.EnvironmentID || environmentFields["sequence"] != entry.EnvironmentSeq {
		return "", ErrCommandStartIdentity
	}
	resultFields, ok := fields["result"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(resultFields, "code", "output", "problem_code") {
		return "", ErrCommandStartIdentity
	}
	codeText, ok := resultFields["code"].(string)
	if !ok {
		return "", ErrCommandStartIdentity
	}
	code := operation.ResultCode(codeText)
	if code != operation.ResultSucceeded && code != operation.ResultRejected && code != operation.ResultRefused && code != operation.ResultFailed {
		return "", ErrCommandStartIdentity
	}
	acceptedValue := fields["accepted_events"]
	accepted, hasRange := acceptedValue.(map[string]any)
	if acceptedValue != nil && !hasRange {
		return "", ErrCommandStartIdentity
	}
	if code == operation.ResultSucceeded {
		if resultFields["problem_code"] != nil {
			return "", ErrCommandStartIdentity
		}
		outputBytes, outputOK := resultFields["output"].([]byte)
		if !outputOK {
			return "", ErrCommandStartIdentity
		}
		switch entry.Command.Request.Operation {
		case operation.MatterCreateV1.Metadata().Operation:
			output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "id", "locator", "title")
			if outputErr != nil || !commandStartULID.MatchString(asCommandStartString(output["id"])) {
				return "", ErrCommandStartIdentity
			}
		case operation.StepCreateV1.Metadata().Operation:
			output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state")
			input, inputOK := entry.Command.Request.Input.(operation.StepCreateInput)
			sortKey, sortOK := output["sort_key"].(uint64)
			if outputErr != nil || !inputOK || !sortOK || sortKey == 0 || sortKey > math.MaxInt64 || !commandStartULID.MatchString(asCommandStartString(output["id"])) ||
				output["parent_id"] != input.ParentID || output["matter_id"] != input.ParentID || output["title"] != input.Title || output["state"] != "planned" ||
				!commandStartStepLocator.MatchString(asCommandStartString(output["locator"])) {
				return "", ErrCommandStartIdentity
			}
		default:
			return "", ErrCommandStartIdentity
		}
		metadata, found := operationMetadata(entry.Command.Request.Operation)
		if !found || metadata.Delivery != entry.Delivery {
			return "", ErrCommandStartIdentity
		}
		if len(metadata.Writes) > 0 && !hasRange {
			return "", ErrCommandStartIdentity
		}
		if hasRange {
			if !wipdwire.ExactMapKeys(accepted, "first_event_id", "last_event_id", "event_count") {
				return "", ErrCommandStartIdentity
			}
			first, firstOK := accepted["first_event_id"].(string)
			last, lastOK := accepted["last_event_id"].(string)
			count, countOK := accepted["event_count"].(uint64)
			if !firstOK || !lastOK || !countOK || count == 0 || !commandStartULID.MatchString(first) || !commandStartULID.MatchString(last) || first > last {
				return "", ErrCommandStartIdentity
			}
		}
	} else if acceptedValue != nil || resultFields["output"] != nil {
		return "", ErrCommandStartIdentity
	} else if _, ok = resultFields["problem_code"].(string); !ok {
		return "", ErrCommandStartIdentity
	}
	return code, nil
}

func commandReceiptResult(entry wipdjournal.Entry, raw []byte) (operation.Result, error) {
	code, err := commandReceiptCode(entry, raw)
	if err != nil {
		return operation.Result{}, err
	}
	fields, err := wipdwire.DecodeCanonicalMap(raw,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return operation.Result{}, ErrCommandStartIdentity
	}
	resultFields := fields["result"].(map[string]any)
	if code != operation.ResultSucceeded {
		problemCode, ok := resultFields["problem_code"].(string)
		if !ok || problemCode == "" {
			return operation.Result{}, ErrCommandStartIdentity
		}
		result := operation.Result{Code: code, Problem: &operation.Problem{Code: operation.ProblemCode(problemCode), Message: problemCode}}
		definition, found := operationDefinition(entry.Command.Request.Operation)
		if !found || definition.ValidateResult(result) != nil {
			return operation.Result{}, ErrCommandStartIdentity
		}
		return result, nil
	}
	outputBytes, ok := resultFields["output"].([]byte)
	if !ok {
		return operation.Result{}, ErrCommandStartIdentity
	}
	var output map[string]any
	var typed operation.Output
	switch entry.Command.Request.Operation {
	case operation.MatterCreateV1.Metadata().Operation:
		output, err = wipdwire.DecodeCanonicalMap(outputBytes, "id", "locator", "title")
		if err != nil {
			return operation.Result{}, ErrCommandStartIdentity
		}
		typed = operation.MatterCreateOutput{
			ID: asCommandStartString(output["id"]), Locator: asCommandStartString(output["locator"]), Title: asCommandStartString(output["title"]),
		}
	case operation.StepCreateV1.Metadata().Operation:
		output, err = wipdwire.DecodeCanonicalMap(outputBytes, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state")
		if err != nil {
			return operation.Result{}, ErrCommandStartIdentity
		}
		sortKey, ok := output["sort_key"].(uint64)
		if !ok || sortKey > math.MaxInt64 {
			return operation.Result{}, ErrCommandStartIdentity
		}
		typed = operation.StepCreateOutput{
			ID: asCommandStartString(output["id"]), ParentID: asCommandStartString(output["parent_id"]),
			MatterID: asCommandStartString(output["matter_id"]), Locator: asCommandStartString(output["locator"]),
			Title: asCommandStartString(output["title"]), SortKey: int64(sortKey), State: asCommandStartString(output["state"]),
		}
	default:
		return operation.Result{}, ErrCommandStartIdentity
	}
	result := operation.Result{Code: code, Output: typed}
	definition, found := operationDefinition(entry.Command.Request.Operation)
	if !found || definition.ValidateResult(result) != nil {
		return operation.Result{}, ErrCommandStartIdentity
	}
	return result, nil
}

func operationMetadata(id operation.ID) (operation.Metadata, bool) {
	for _, definition := range operation.Catalogue() {
		metadata := definition.Metadata()
		if metadata.Operation == id {
			return metadata, true
		}
	}
	return operation.Metadata{}, false
}

func validCommandStartSnapshot(snapshot CommandStartSnapshot, command operation.Command) bool {
	return snapshot.DomainID == command.AuthorityDomainID && snapshot.Epoch == command.ExpectedAuthorityEpoch &&
		snapshot.EnvironmentID == command.EnvironmentID && snapshot.Revision > 0 && snapshot.Receipts != nil &&
		commandStartHash.MatchString(snapshot.ManifestDigest) && validCommandStartAnchor(snapshot.Anchor)
}

func validCommandStartAnchor(anchor wipdwire.PrefixAnchor) bool {
	if !commandStartHash.MatchString(anchor.Digest) {
		return false
	}
	if anchor.EventCount == 0 {
		empty := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
		return anchor.EventID == nil && anchor.Digest == "sha256:"+hex.EncodeToString(empty[:])
	}
	return anchor.EventID != nil && commandStartULID.MatchString(*anchor.EventID)
}

func sameCommandStartAnchor(left, right wipdwire.PrefixAnchor) bool {
	if left.EventCount != right.EventCount || left.Digest != right.Digest || (left.EventID == nil) != (right.EventID == nil) {
		return false
	}
	return left.EventID == nil || *left.EventID == *right.EventID
}

func commandStartReceiptsPreserved(before, after map[string]InstalledCommandReceipt) bool {
	for id, expected := range before {
		actual, ok := after[id]
		if !ok || actual.RequestHash != expected.RequestHash || actual.EnvironmentSeq != expected.EnvironmentSeq ||
			actual.JournalPosition != expected.JournalPosition || actual.ResultCode != expected.ResultCode ||
			!bytes.Equal(actual.CanonicalReceipt, expected.CanonicalReceipt) {
			return false
		}
	}
	return true
}

func cloneCommandStartSnapshot(snapshot CommandStartSnapshot) CommandStartSnapshot {
	copyOf := snapshot
	if snapshot.Anchor.EventID != nil {
		eventID := *snapshot.Anchor.EventID
		copyOf.Anchor.EventID = &eventID
	}
	if snapshot.Receipts != nil {
		copyOf.Receipts = make(map[string]InstalledCommandReceipt, len(snapshot.Receipts))
		for id, receipt := range snapshot.Receipts {
			receipt.CanonicalReceipt = bytes.Clone(receipt.CanonicalReceipt)
			copyOf.Receipts[id] = receipt
		}
	}
	return copyOf
}
