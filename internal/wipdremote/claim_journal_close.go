package wipdremote

import (
	"bytes"
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// CurrentClaimJournal queries and verifies the authority-assigned current
// generation for an installed acquired-claim grant.
func (runtime *Runtime) CurrentClaimJournal(ctx context.Context, grant wipdjournal.ClaimGrantSummary) (wipdjournal.ClaimJournalBinding, error) {
	var empty wipdjournal.ClaimJournalBinding
	if runtime == nil || !runtime.SupportsClaimJournalClose() || runtime.client == nil || ctx == nil ||
		grant.ClaimID == "" || grant.ClaimEpoch == 0 || grant.MatterID == "" || grant.DispatchID == "" {
		return empty, wipdseed.ErrInvalidClientState
	}
	query := wipdwire.ClaimJournalQuery{
		Schema: "wipd.claim-journal-query/1", DomainID: runtime.state.DomainID, Epoch: runtime.state.Epoch,
		EnvironmentID: runtime.state.EnvironmentID, ClaimID: grant.ClaimID, ClaimEpoch: grant.ClaimEpoch,
		MatterID: grant.MatterID, DispatchID: grant.DispatchID,
	}
	frames, err := runtime.client.Exchange(ctx, "claim-journal.query", query)
	if err != nil {
		return empty, err
	}
	if len(frames) != 1 || frames[0].Kind != "claim-journal.current" {
		return empty, wipdseed.ErrInvalidClientState
	}
	var current wipdwire.ClaimJournalCurrent
	if wipdwire.DecodeCanonical(frames[0].Payload,
		&current, "schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id", "journal_id", "generation", "state") != nil ||
		current.Schema != "wipd.claim-journal-current/1" || current.DomainID != runtime.state.DomainID || current.Epoch != runtime.state.Epoch ||
		current.EnvironmentID != runtime.state.EnvironmentID || current.ClaimID != grant.ClaimID || current.ClaimEpoch != grant.ClaimEpoch ||
		current.MatterID != grant.MatterID || current.DispatchID != grant.DispatchID || current.JournalID == "" || current.Generation == 0 ||
		(current.State != "open" && current.State != "sealed") {
		return empty, wipdseed.ErrInvalidClientState
	}
	return wipdjournal.ClaimJournalBinding{
		ClaimID: current.ClaimID, ClaimEpoch: current.ClaimEpoch, MatterID: current.MatterID,
		DispatchID: current.DispatchID, JournalID: current.JournalID, Generation: current.Generation, State: current.State,
	}, nil
}

// AcknowledgeClaimJournalEntry sends one owner-bound ordered receipt ACK after
// the command's terminal fold has been installed locally.
func (runtime *Runtime) AcknowledgeClaimJournalEntry(ctx context.Context, binding wipdjournal.ClaimJournalBinding,
	ack wipdwire.ClaimJournalReceiptAck,
) error {
	if runtime == nil || !runtime.SupportsClaimJournalClose() || runtime.client == nil || ctx == nil ||
		binding.State != "open" || ack.Schema != "wipd.claim-journal-ack/1" || ack.DomainID != runtime.state.DomainID ||
		ack.Epoch != runtime.state.Epoch || ack.EnvironmentID != runtime.state.EnvironmentID || ack.ClaimID != binding.ClaimID ||
		ack.ClaimEpoch != binding.ClaimEpoch || ack.MatterID != binding.MatterID || ack.DispatchID != binding.DispatchID ||
		ack.JournalID != binding.JournalID || ack.Generation != binding.Generation || ack.Position == 0 || len(ack.Receipt) == 0 {
		return wipdseed.ErrInvalidClientState
	}
	frames, err := runtime.client.Exchange(ctx, "claim-journal.ack", ack)
	if err != nil {
		return err
	}
	if len(frames) != 1 || frames[0].Kind != "claim-journal.acked" {
		return wipdseed.ErrInvalidClientState
	}
	fields, err := wipdwire.DecodeCanonicalMap(frames[0].Payload, "schema", "domain_id", "claim_id", "journal_id", "generation", "position")
	if err != nil || fields["schema"] != "wipd.claim-journal-acked/1" || fields["domain_id"] != ack.DomainID ||
		fields["claim_id"] != ack.ClaimID || fields["journal_id"] != ack.JournalID || fields["generation"] != ack.Generation ||
		fields["position"] != ack.Position {
		return wipdseed.ErrInvalidClientState
	}
	return nil
}

// SealClaimJournal asks the authority to freeze the exact acknowledged current
// generation and verifies the returned barrier identity.
func (runtime *Runtime) SealClaimJournal(ctx context.Context, binding wipdjournal.ClaimJournalBinding) (wipdwire.ClaimJournalSealed, error) {
	var empty wipdwire.ClaimJournalSealed
	if runtime == nil || !runtime.SupportsClaimJournalClose() || runtime.client == nil || ctx == nil ||
		(binding.State != "open" && binding.State != "sealed") {
		return empty, wipdseed.ErrInvalidClientState
	}
	seal := wipdwire.ClaimJournalSeal{
		Schema: "wipd.claim-journal-seal/1", DomainID: runtime.state.DomainID, Epoch: runtime.state.Epoch,
		EnvironmentID: runtime.state.EnvironmentID, ClaimID: binding.ClaimID, ClaimEpoch: binding.ClaimEpoch,
		MatterID: binding.MatterID, DispatchID: binding.DispatchID, JournalID: binding.JournalID, Generation: binding.Generation,
	}
	frames, err := runtime.client.Exchange(ctx, "claim-journal.seal", seal)
	if err != nil {
		return empty, err
	}
	if len(frames) != 1 || frames[0].Kind != "claim-journal.sealed" {
		return empty, wipdseed.ErrInvalidClientState
	}
	var sealed wipdwire.ClaimJournalSealed
	if wipdwire.DecodeCanonical(frames[0].Payload, &sealed,
		"schema", "domain_id", "claim_id", "journal_id", "generation", "entry_count", "entries_digest") != nil ||
		sealed.Schema != "wipd.claim-journal-sealed/1" || sealed.DomainID != seal.DomainID || sealed.ClaimID != binding.ClaimID ||
		sealed.JournalID != binding.JournalID || sealed.Generation != binding.Generation || sealed.Digest == "" {
		return empty, wipdseed.ErrInvalidClientState
	}
	return sealed, nil
}

// SubmitClaimJournalRelease submits the unchanged claim.release command only
// after the acquired journal's exact generation has been sealed.
func (runtime *Runtime) SubmitClaimJournalRelease(ctx context.Context, attempt wipdjournal.ClaimJournalReleaseCommand,
	installed wipdwire.PrefixAnchor,
) ([]byte, operation.ResultCode, wipd.CommandPull, error) {
	var empty wipd.CommandPull
	if runtime == nil || !runtime.SupportsClaimJournalClose() || runtime.client == nil || ctx == nil || attempt.ID == "" ||
		attempt.Binding.State != "sealed" || !attempt.Barrier.Sealed || attempt.Barrier.Journal != attempt.Binding.JournalID ||
		attempt.Barrier.Claim.ID != attempt.Binding.ClaimID || attempt.Barrier.Claim.Epoch != attempt.Binding.ClaimEpoch {
		return nil, "", empty, wipdseed.ErrInvalidClientState
	}
	payload := wipdwire.ClaimRelease{
		Schema: "wipd.claim-release/1", CanonicalCommand: bytes.Clone(attempt.CanonicalBytes),
		RequestHash: attempt.RequestHash, Barrier: attempt.Barrier,
	}
	frames, err := runtime.client.Exchange(ctx, "claim.release", payload)
	if err != nil {
		return nil, "", empty, err
	}
	legacy := wipdjournal.BirthReleaseCommand{
		ID: attempt.ID, RequestHash: attempt.RequestHash, EnvironmentSeq: attempt.EnvironmentSeq,
		CanonicalBytes: bytes.Clone(attempt.CanonicalBytes), Barrier: attempt.Barrier,
	}
	receipt, err := terminalFromBirthRelease(ctx, runtime.client, runtime.state.DomainID, runtime.state.Epoch,
		runtime.state.EnvironmentID, legacy, payload, frames)
	if err != nil {
		return nil, "", empty, err
	}
	code, err := validateBirthReleaseTerminal(receipt, runtime.state.DomainID, runtime.state.Epoch, runtime.state.EnvironmentID, legacy)
	if err != nil {
		return nil, "", empty, err
	}
	tail, err := runtime.pull(ctx, installed)
	if err != nil {
		return nil, "", empty, fmt.Errorf("claim.release authority tail: %w", err)
	}
	return receipt, code, tail, nil
}

var _ wipd.ClaimJournalCloseAuthority = (*Runtime)(nil)
