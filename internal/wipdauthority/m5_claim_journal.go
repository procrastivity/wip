package wipdauthority

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func (app *m5LabHandler) serveClaimJournalQuery(writer http.ResponseWriter, request *http.Request, body *bufio.Reader,
	frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate,
) {
	var query wipdwire.ClaimJournalQuery
	if wipdwire.DecodeCanonical(frame.Payload, &query,
		"schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id") != nil ||
		query.Schema != "wipd.claim-journal-query/1" || query.DomainID != app.profile.domainID || query.Epoch != app.profile.epoch ||
		query.EnvironmentID != environment.EnvironmentID {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	journal, err := app.store.GetCurrentClaimJournal(ctx, query.DomainID, query.Epoch, environment.EnvironmentID,
		query.ClaimID, query.ClaimEpoch, query.MatterID, query.DispatchID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug authority claim-journal ack: %v\n", err)
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, claimJournalProblem(err))
		return
	}
	payload, err := wipdwire.EncodeCanonical(wipdwire.ClaimJournalCurrent{
		Schema: "wipd.claim-journal-current/1", DomainID: journal.DomainID, Epoch: journal.AuthorityEpoch,
		EnvironmentID: journal.EnvironmentID, ClaimID: journal.ClaimID, ClaimEpoch: journal.ClaimEpoch,
		MatterID: journal.MatterID, DispatchID: journal.DispatchID, JournalID: journal.JournalID,
		Generation: journal.Generation, State: journal.State,
	})
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, "authority.unavailable")
		return
	}
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "claim-journal.current", Payload: payload}, "")
}

func (app *m5LabHandler) serveClaimJournalAck(writer http.ResponseWriter, request *http.Request, body *bufio.Reader,
	frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate,
) {
	var ack wipdwire.ClaimJournalReceiptAck
	if wipdwire.DecodeCanonical(frame.Payload, &ack,
		"schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id",
		"journal_id", "generation", "position", "terminal_receipt", "installed_prefix") != nil ||
		ack.Schema != "wipd.claim-journal-ack/1" || ack.DomainID != app.profile.domainID || ack.Epoch != app.profile.epoch ||
		ack.EnvironmentID != environment.EnvironmentID || ack.Position == 0 {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	identity, err := app.currentClaimJournalIdentity(ctx, ack.DomainID, ack.Epoch, ack.EnvironmentID,
		ack.ClaimID, ack.ClaimEpoch, ack.MatterID, ack.DispatchID, ack.JournalID, ack.Generation, "open")
	if err == nil {
		eventID := ""
		if ack.Installed.EventID != nil {
			eventID = *ack.Installed.EventID
		}
		err = app.store.AcknowledgeOwnedClaimJournalEntry(ctx, identity, ack.Position, ack.Receipt, authoritystore.PrefixAnchor{
			EventCount: ack.Installed.EventCount, EventID: eventID, Digest: ack.Installed.Digest,
		})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug authority claim-journal seal: %v\n", err)
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, claimJournalProblem(err))
		return
	}
	payload, _ := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.claim-journal-acked/1", "domain_id": ack.DomainID, "claim_id": ack.ClaimID,
		"journal_id": ack.JournalID, "generation": ack.Generation, "position": ack.Position,
	})
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "claim-journal.acked", Payload: payload}, "")
}

func (app *m5LabHandler) serveClaimJournalSeal(writer http.ResponseWriter, request *http.Request, body *bufio.Reader,
	frame wipdwire.Frame, environment authoritystore.EnvironmentCertificate,
) {
	var seal wipdwire.ClaimJournalSeal
	if wipdwire.DecodeCanonical(frame.Payload, &seal,
		"schema", "domain_id", "authority_epoch", "environment_id", "claim_id", "claim_epoch", "matter_id", "dispatch_id",
		"journal_id", "generation") != nil || seal.Schema != "wipd.claim-journal-seal/1" ||
		seal.DomainID != app.profile.domainID || seal.Epoch != app.profile.epoch || seal.EnvironmentID != environment.EnvironmentID {
		writeLabProblem(writer, frame.RequestID, "protocol.malformed-message")
		return
	}
	ctx, cancel, arbiter, ok := app.beginReadOnlyExchange(writer, request, body, frame.RequestID)
	if !ok {
		return
	}
	defer cancel(context.Canceled)
	identity, err := app.currentClaimJournalIdentity(ctx, seal.DomainID, seal.Epoch, seal.EnvironmentID,
		seal.ClaimID, seal.ClaimEpoch, seal.MatterID, seal.DispatchID, seal.JournalID, seal.Generation, "")
	var digest string
	var count uint64
	if err == nil {
		digest, count, err = app.store.SealOwnedClaimJournal(ctx, identity)
	}
	if err != nil {
		writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID, wipdwire.Frame{}, claimJournalProblem(err))
		return
	}
	payload, _ := wipdwire.EncodeCanonical(wipdwire.ClaimJournalSealed{
		Schema: "wipd.claim-journal-sealed/1", DomainID: seal.DomainID, ClaimID: seal.ClaimID,
		JournalID: seal.JournalID, Generation: seal.Generation, Count: count, Digest: digest,
	})
	writeReadOnlyFinal(ctx, arbiter, writer, frame.RequestID,
		wipdwire.Frame{RequestID: frame.RequestID, Kind: "claim-journal.sealed", Payload: payload}, "")
}

func (app *m5LabHandler) currentClaimJournalIdentity(ctx context.Context, domain string, epoch uint64, environment, claim string,
	claimEpoch uint64, matter, dispatch, journal string, generation uint64, state string,
) (authoritystore.CurrentClaimJournal, error) {
	current, err := app.store.GetCurrentClaimJournal(ctx, domain, epoch, environment, claim, claimEpoch, matter, dispatch)
	if err != nil {
		return current, err
	}
	if current.JournalID != journal || current.Generation != generation || (state != "" && current.State != state) {
		return authoritystore.CurrentClaimJournal{}, authoritystore.ErrFenced
	}
	return current, nil
}

func claimJournalProblem(err error) string {
	switch {
	case errors.Is(err, authoritystore.ErrFenced), errors.Is(err, authoritystore.ErrInvalidProof):
		return "auth.environment-domain-mismatch"
	case errors.Is(err, authoritystore.ErrPending), errors.Is(err, authoritystore.ErrPrefixMismatch):
		return "command.sequence-blocked"
	default:
		return "authority.unavailable"
	}
}
