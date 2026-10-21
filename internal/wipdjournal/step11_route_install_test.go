package wipdjournal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestRouteMutationEventlessOutcomeInstallsAndSurvivesReopen(t *testing.T) {
	tests := []struct {
		name      string
		operation operation.Definition
		input     operation.Input
		context   operation.Context
	}{
		{
			name: "route-bind", operation: operation.RepoRouteBindV1,
			input:   operation.RepoRouteBindInput{NormalizedKey: "github.com/acme/widget"},
			context: operation.Context{Repo: testRepoID, Clone: testCommandPrefix + "61", Worktree: testCommandPrefix + "62"},
		},
		{
			name: "adopt-remote", operation: operation.RepoAdoptRemoteV1,
			input:   operation.RepoAdoptRemoteInput{NormalizedKey: "github.com/acme/widget"},
			context: operation.Context{Repo: testRepoID, Clone: testCommandPrefix + "61", Worktree: testCommandPrefix + "62"},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(privateTempDir(t), "journal")
			journal := openInstallTestJournal(t, root)
			command := operation.Command{
				ID: testCommandPrefix + string(rune('6'+index)) + "3", CorrelationCommandID: testCommandPrefix + string(rune('6'+index)) + "3",
				AuthorityDomainID: testDomainID, ExpectedAuthorityEpoch: testIdentity.AuthorityEpoch,
				EnvironmentID: testEnvironmentID, EnvironmentSequence: 1,
				ActedAt: "2026-10-20T00:00:00Z",
				Request: operation.Request{Operation: test.operation.Metadata().Operation, Actor: "human", Context: test.context, Input: test.input},
			}
			entry, err := journal.PrepareCanonicalCommand(command)
			if err != nil {
				t.Fatal(err)
			}
			installed, err := journal.InstallSnapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			receipt := routeMutationReceipt(t, entry, testCommandPrefix+"99")
			if _, err = journal.InstallAuthorityOutcome(context.Background(), installed.Expectation(), entry,
				operation.ResultSucceeded, receipt, emptyInstallTestTransfer(t, installed.Anchor)); err == nil {
				t.Fatal("eventless route receipt with wrong Repo output was accepted")
			}
			receipt = routeMutationReceipt(t, entry, entry.Command.Request.Context.Repo)
			installed, err = journal.InstallAuthorityOutcome(context.Background(), installed.Expectation(), entry,
				operation.ResultSucceeded, receipt, emptyInstallTestTransfer(t, installed.Anchor))
			if err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 1 {
				t.Fatalf("eventless route outcome installation = %+v, %v", installed, err)
			}
			if err = journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = openInstallTestJournal(t, root)
			t.Cleanup(func() { _ = journal.Close() })
			installed, err = journal.InstallSnapshot(context.Background())
			if err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 1 ||
				installed.Receipts[entry.Command.ID].ResultCode != operation.ResultSucceeded {
				t.Fatalf("eventless route receipt after reopen = %+v, %v", installed, err)
			}
		})
	}
}

func TestInstallPullAcceptsAttachedCheckoutEvent(t *testing.T) {
	root := filepath.Join(privateTempDir(t), "journal")
	journal := openInstallTestJournal(t, root)
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eventID := testCommandPrefix + "70"
	payload := map[string]any{
		"repo_id": testIdentity.RepoID, "environment_id": testIdentity.EnvironmentID,
		"normalized_repo_key": "github.com/acme/widget", "clone_id": testCommandPrefix + "51", "worktree_id": testCommandPrefix + "52",
		"common_dir_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"git_dir_digest":    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"worktree_name":     "widget", "grant_id": testCommandPrefix + "53",
		"grant_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	record := m6LifecycleEventRecord(t, "environment.checkout-attached/1", testIdentity.RepoID, payload)
	transfer := verifiedInstallTestTransfer(t, installed.Anchor, []wipdwire.EventRecord{{EventID: eventID, Record: record}})
	installed, err = journal.InstallPull(context.Background(), installed.Expectation(), transfer)
	if err != nil || installed.Anchor.EventCount != 1 || installed.Anchor.EventID == nil || *installed.Anchor.EventID != eventID {
		t.Fatalf("InstallPull of attach event = %+v, %v", installed, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	installed, err = journal.InstallSnapshot(context.Background())
	if err != nil || installed.Anchor.EventCount != 1 || installed.Anchor.EventID == nil || *installed.Anchor.EventID != eventID {
		t.Fatalf("attached event after pull journal reopen = %+v, %v", installed, err)
	}
}

func routeMutationReceipt(t *testing.T, entry Entry, repoID string) []byte {
	t.Helper()
	output, err := wipdwire.EncodeCanonical(map[string]any{
		"repo_id": repoID, "domain_id": entry.Command.AuthorityDomainID, "epoch": entry.Command.ExpectedAuthorityEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID,
		"authority_epoch": entry.Command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation":       map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(entry.Command.Request.Operation.Version)},
		"environment":     map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
		"result":          map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil},
		"accepted_events": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
