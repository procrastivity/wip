package wipdjournal

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestBatchSweepNoEventReceiptInstallationIsExact(t *testing.T) {
	for _, mutation := range []string{"valid", "swept", "unknown-outcome", "output-type", "extra-output", "problem", "other-operation", "wrong-hash", "wrong-environment", "event-range"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "journal")
			journal := openInstallTestJournal(t, root)
			defer func() { _ = journal.Close() }()
			id := testCommandPrefix + "75"
			digest := hydrationDigest([]byte("normal release"))
			entry, err := journal.PrepareCommand(CommandInput{ID: id, Request: operation.Request{
				Operation: operation.BatchSweepAnonymousV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID},
				Input: operation.BatchSweepAnonymousInput{
					MatterID: testCommandPrefix + "71", BatchID: testCommandPrefix + "72",
					ClaimClose: operation.ClaimCloseReference{
						ClaimID: testCommandPrefix + "73", ClaimEpoch: 2, ReleaseCommandID: testCommandPrefix + "74",
						ReleaseRequestHash: digest, TerminalReceiptDigest: digest,
						InstalledPrefixAnchor: operation.ClaimClosePrefix{EventCount: 1, EventID: &id, Digest: digest},
					},
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			output := map[string]any{"outcome": "already-swept"}
			switch mutation {
			case "swept", "unknown-outcome":
				output["outcome"] = mutation
			case "output-type":
				output["outcome"] = true
			case "extra-output":
				output["batch_id"] = testCommandPrefix + "72"
			}
			encodedOutput, _ := wipdwire.EncodeCanonical(output)
			result := map[string]any{"code": "result.succeeded", "output": encodedOutput, "problem_code": nil}
			environment := map[string]any{"id": testEnvironmentID, "sequence": entry.EnvironmentSeq}
			fields := map[string]any{
				"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": uint64(7), "identity_schema": "wipd.command/1",
				"command_id": id, "request_hash": entry.RequestHash,
				"operation":   map[string]any{"name": "batch.sweep-anonymous", "version": uint64(1)},
				"environment": environment, "result": result, "accepted_events": nil,
			}
			switch mutation {
			case "problem":
				result["problem_code"] = "refusal.batch-sweep-not-eligible"
			case "other-operation":
				fields["operation"] = map[string]any{"name": "matter.finish", "version": uint64(1)}
			case "wrong-hash":
				fields["request_hash"] = digest
			case "wrong-environment":
				environment["sequence"] = entry.EnvironmentSeq + 1
			case "event-range":
				fields["accepted_events"] = map[string]any{"first_event_id": id, "last_event_id": id, "event_count": uint64(1)}
			}
			raw, _ := wipdwire.EncodeCanonical(fields)
			before, err := journal.InstallSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			installed, err := journal.InstallAuthorityOutcome(ctx, before.Expectation(), entry, operation.ResultSucceeded, raw, emptyInstallTestTransfer(t, before.Anchor))
			if mutation != "valid" {
				if !errors.Is(err, ErrInvalidTransfer) {
					t.Fatalf("malformed no-event receipt installed: %v", err)
				}
				if after, err := journal.InstallSnapshot(ctx); err != nil || !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected receipt partially installed: %+v %v", after, err)
				}
			} else if err != nil || !sameTransferAnchor(installed.Anchor, before.Anchor) ||
				!bytes.Equal(installed.Receipts[id].CanonicalReceipt, raw) || installed.Revision <= before.Revision {
				t.Fatalf("no-event installation: %+v %v", installed, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = openInstallTestJournal(t, root)
			reopened, err := journal.InstallSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "valid" {
				if !reflect.DeepEqual(reopened, installed) {
					t.Fatalf("no-event terminal did not survive reopen: %+v", reopened)
				}
			} else if !reflect.DeepEqual(reopened, before) {
				t.Fatalf("rejected terminal survived reopen: %+v", reopened)
			}
		})
	}
}
