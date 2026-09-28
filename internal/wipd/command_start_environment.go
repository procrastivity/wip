package wipd

import (
	"context"
	"errors"

	"github.com/procrastivity/wip/internal/wipdjournal"
)

// JournalCommandStartEnvironment is the durable Environment transaction
// boundary used by the connected command-start coordinator. Prefix records,
// receipts, journal disposition, and rebuilt overlay are stored by one
// Environment-local SQLite journal.
type JournalCommandStartEnvironment struct {
	journal *wipdjournal.Journal
}

// NewJournalCommandStartEnvironment binds the concrete command-start store to
// one already-open Environment journal.
func NewJournalCommandStartEnvironment(journal *wipdjournal.Journal) (*JournalCommandStartEnvironment, error) {
	if journal == nil {
		return nil, errors.New("wipd: command-start journal is required")
	}
	return &JournalCommandStartEnvironment{journal: journal}, nil
}

// Snapshot returns one consistent installed-prefix and receipt view.
func (environment *JournalCommandStartEnvironment) Snapshot(ctx context.Context) (CommandStartSnapshot, error) {
	if environment == nil || environment.journal == nil {
		return CommandStartSnapshot{}, errors.New("wipd: command-start journal is required")
	}
	installed, err := environment.journal.InstallSnapshot(ctx)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	return commandStartSnapshot(installed), nil
}

// InstallFold atomically commits a verified fold through the Environment journal.
func (environment *JournalCommandStartEnvironment) InstallFold(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry, fold CommandFold) (CommandStartSnapshot, error) {
	if err := environment.checkExpected(expected); err != nil {
		return CommandStartSnapshot{}, err
	}
	installed, err := environment.journal.InstallFold(ctx, installExpectation(expected), entry, fold.ResultCode, fold.CanonicalReceipt, fold.VerifiedTransfer)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	return commandStartSnapshot(installed), nil
}

// InstallPull atomically commits a verified authority tail through the Environment journal.
func (environment *JournalCommandStartEnvironment) InstallPull(ctx context.Context, expected CommandStartSnapshot, pull CommandPull) (CommandStartSnapshot, error) {
	if err := environment.checkExpected(expected); err != nil {
		return CommandStartSnapshot{}, err
	}
	installed, err := environment.journal.InstallPull(ctx, installExpectation(expected), pull.VerifiedTransfer)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	return commandStartSnapshot(installed), nil
}

// InstallBirthRelease atomically installs the lifecycle receipt, verified
// authority tail, and rebuilt overlay through the Environment journal.
func (environment *JournalCommandStartEnvironment) InstallBirthRelease(ctx context.Context, expected CommandStartSnapshot, attempt wipdjournal.BirthReleaseCommand, receipt []byte, tail CommandPull) (CommandStartSnapshot, error) {
	if err := environment.checkExpected(expected); err != nil {
		return CommandStartSnapshot{}, err
	}
	installed, err := environment.journal.InstallBirthRelease(ctx, installExpectation(expected), attempt, receipt, tail.VerifiedTransfer)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	return commandStartSnapshot(installed), nil
}

// AdmitPending commits the recoverable provisional overlay with return eligibility.
func (environment *JournalCommandStartEnvironment) AdmitPending(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry) (CommandStartSnapshot, error) {
	if err := environment.checkExpected(expected); err != nil {
		return CommandStartSnapshot{}, err
	}
	installed, err := environment.journal.AdmitPending(ctx, installExpectation(expected), entry.Command.ID)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	return commandStartSnapshot(installed), nil
}

func (environment *JournalCommandStartEnvironment) checkExpected(expected CommandStartSnapshot) error {
	if environment == nil || environment.journal == nil || expected.DomainID != environment.journal.Identity().DomainID ||
		expected.Epoch != environment.journal.Identity().AuthorityEpoch || expected.EnvironmentID != environment.journal.Identity().EnvironmentID {
		return ErrCommandStartIdentity
	}
	return nil
}

func (environment *JournalCommandStartEnvironment) commandStartJournal() *wipdjournal.Journal {
	if environment == nil {
		return nil
	}
	return environment.journal
}

func installExpectation(snapshot CommandStartSnapshot) wipdjournal.InstallExpectation {
	return wipdjournal.InstallExpectation{Revision: snapshot.Revision, Anchor: snapshot.Anchor, ManifestDigest: snapshot.ManifestDigest}
}

func commandStartSnapshot(installed wipdjournal.InstallSnapshot) CommandStartSnapshot {
	receipts := make(map[string]InstalledCommandReceipt, len(installed.Receipts))
	for id, receipt := range installed.Receipts {
		receipts[id] = InstalledCommandReceipt{
			RequestHash: receipt.RequestHash, EnvironmentSeq: receipt.EnvironmentSeq,
			JournalPosition: receipt.JournalPosition, ResultCode: receipt.ResultCode,
			CanonicalReceipt: append([]byte(nil), receipt.CanonicalReceipt...),
		}
	}
	return CommandStartSnapshot{
		DomainID: installed.Identity.DomainID, Epoch: installed.Identity.AuthorityEpoch,
		EnvironmentID: installed.Identity.EnvironmentID, Revision: installed.Revision,
		Anchor: installed.Anchor, ManifestDigest: installed.ManifestDigest, Receipts: receipts,
	}
}

var _ CommandStartEnvironment = (*JournalCommandStartEnvironment)(nil)
