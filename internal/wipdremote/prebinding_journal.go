package wipdremote

import (
	"context"
	"errors"
	"sort"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func openAttachExchangeClient(ctx context.Context, connection wipdseed.PrebindingConnection, stateDir, repoID string) (*wipdseed.CommandExchangeClient, error) {
	definitions := operation.Step11Catalogue()
	operations := make([]operation.ID, 0, len(definitions))
	for _, definition := range definitions {
		operations = append(operations, definition.Metadata().Operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].Name < operations[j].Name || operations[i].Name == operations[j].Name && operations[i].Version < operations[j].Version
	})
	client, err := wipdseed.OpenStep11CommandExchangeClientForRepoWithCredential(ctx, connection.AuthorityProfile,
		connection.AuthorityRoots, stateDir, connection.ProfileRoot, repoID, operations)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func reconcilePrebindingJournal(ctx context.Context, profile wipdauthority.Profile, client *wipdseed.CommandExchangeClient,
	journal *wipdjournal.Journal,
) (wipdjournal.InstallSnapshot, error) {
	var empty wipdjournal.InstallSnapshot
	if ctx == nil || client == nil || journal == nil {
		return empty, wipdseed.ErrInvalidClientState
	}
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		return empty, err
	}
	state := client.State()
	if state.DomainID != profile.DomainID() || state.Epoch != profile.Epoch() || state.EnvironmentID != journal.Identity().EnvironmentID ||
		state.RepoID == "" {
		return empty, wipdseed.ErrInvalidClientState
	}
	state.Prefix = installed.Anchor
	state.EventRecords, err = journal.EventRecords(ctx)
	if err != nil || len(state.EventRecords) != int(installed.Anchor.EventCount) {
		return empty, wipdseed.ErrInvalidClientState
	}
	frames, err := client.Exchange(ctx, "pull.request", wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: profile.DomainID(), Epoch: profile.Epoch(), Installed: installed.Anchor,
	})
	if err != nil {
		return empty, err
	}
	transfer, _, err := wipdseed.VerifyPullTransfer(profile, state, installed.Anchor, frames)
	if err != nil {
		return empty, err
	}
	return journal.InstallPull(ctx, installed.Expectation(), transfer)
}

func installPrebindingAuthorityOutcome(ctx context.Context, profile wipdauthority.Profile, client *wipdseed.CommandExchangeClient,
	journal *wipdjournal.Journal, entry wipdjournal.Entry, result operation.ResultCode, receipt []byte,
) error {
	if ctx == nil || client == nil || journal == nil || len(receipt) == 0 {
		return wipdseed.ErrInvalidClientState
	}
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		return err
	}
	state := client.State()
	if state.DomainID != profile.DomainID() || state.Epoch != profile.Epoch() || state.EnvironmentID != journal.Identity().EnvironmentID ||
		state.RepoID == "" {
		return wipdseed.ErrInvalidClientState
	}
	state.Prefix = installed.Anchor
	state.EventRecords, err = journal.EventRecords(ctx)
	if err != nil || len(state.EventRecords) != int(installed.Anchor.EventCount) {
		return wipdseed.ErrInvalidClientState
	}
	frames, err := client.Exchange(ctx, "pull.request", wipdwire.PullRequest{
		Schema: "wipd.pull-request/1", DomainID: profile.DomainID(), Epoch: profile.Epoch(), Installed: installed.Anchor,
	})
	if err != nil {
		return err
	}
	transfer, _, err := wipdseed.VerifyPullTransfer(profile, state, installed.Anchor, frames)
	if err != nil {
		return err
	}
	_, err = journal.InstallAuthorityOutcome(ctx, installed.Expectation(), entry, result, receipt, transfer)
	if errors.Is(err, wipdjournal.ErrInvalidTransfer) {
		return errors.Join(err, errors.New("wipdremote: prebinding terminal did not match its exact journal attempt and authority tail"))
	}
	return err
}
