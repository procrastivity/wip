package wipdjournal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
			entry := prepareBatchSweepReceiptTestCommand(t, journal)
			id := entry.Command.ID
			digest := hydrationDigest([]byte("normal release"))
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

func TestBatchSweepTerminalReceiptOutcomeMatchesEventRange(t *testing.T) {
	journal := openInstallTestJournal(t, filepath.Join(t.TempDir(), "journal"))
	defer journal.Close()
	entry := prepareBatchSweepReceiptTestCommand(t, journal)
	for _, test := range []struct {
		outcome string
		count   int
		valid   bool
	}{
		{"swept", 1, true},
		{"already-swept", 0, true},
		{"already-swept", 1, false},
		{"swept", 0, false},
		{"swept", 2, false},
		{"unknown", 1, false},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.outcome, test.count), func(t *testing.T) {
			ids := []string{testCommandPrefix + "90", testCommandPrefix + "91"}[:test.count]
			receipt := batchSweepReceiptTestBytes(t, entry, test.outcome, ids)
			err := validateTerminalReceipt(entry, operation.ResultSucceeded, receipt, ids)
			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrInvalidTransfer) {
				t.Fatalf("outcome=%s count=%d valid=%t: %v", test.outcome, test.count, test.valid, err)
			}
		})
	}
}

func TestBatchSweepReopenRejectsAlreadySweptWithGenuineEventRange(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	defer func() { _ = journal.Close() }()
	entry := prepareBatchSweepReceiptTestCommand(t, journal)
	eventID := testCommandPrefix + "90"
	input := entry.Command.Request.Input.(operation.BatchSweepAnonymousInput)
	record, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"environment": map[string]any{"id": testEnvironmentID, "sequence": entry.EnvironmentSeq},
		"acted_at":    entry.Command.ActedAt, "occurred_at": entry.Command.ActedAt,
		"kind": "batch.swept", "subject_id": input.BatchID, "repo_id": testRepoID, "payload": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transfer := verifiedInstallTestTransfer(t, before.Anchor, []wipdwire.EventRecord{{EventID: eventID, Record: record}})
	receipt := batchSweepReceiptTestBytes(t, entry, "swept", []string{eventID})
	if _, err := journal.InstallAuthorityOutcome(ctx, before.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil || installed.Anchor.EventCount != 1 || !bytes.Equal(installed.Receipts[entry.Command.ID].CanonicalReceipt, receipt) {
		t.Fatalf("genuine swept receipt did not survive reopen: %+v %v", installed, err)
	}
	// Substitute only the typed outcome, retaining every identity field and
	// the genuine installed event range. Restore the immutability trigger so
	// reopen cannot reject merely because a schema object is missing.
	corrupt := batchSweepReceiptTestBytes(t, entry, "already-swept", []string{eventID})
	var trigger string
	if err := journal.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='authority_command_outcome_immutable'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.db.Exec(`UPDATE authority_command_outcomes SET canonical_receipt=? WHERE command_id=?`, corrupt, entry.Command.ID); err == nil {
		t.Fatal("immutable receipt allowed substitution without trigger bypass")
	}
	if _, err := journal.db.Exec(`DROP TRIGGER authority_command_outcome_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.db.Exec(`UPDATE authority_command_outcomes SET canonical_receipt=? WHERE command_id=?`, corrupt, entry.Command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	tx, err := journal.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, _, _, err := receiptEventRange(ctx, tx, installed.Anchor, emptyInstallTestTransfer(t, installed.Anchor), entry, operation.ResultSucceeded, corrupt)
	_ = tx.Rollback()
	if err != nil || !reflect.DeepEqual(ids, []string{eventID}) {
		t.Fatalf("substituted receipt lost its genuine event range: %v %v", ids, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, testIdentity)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("reopen accepted already-swept with an event: %v", err)
	}
}

func prepareBatchSweepReceiptTestCommand(t *testing.T, journal *Journal) Entry {
	t.Helper()
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
	return entry
}

func batchSweepReceiptTestBytes(t *testing.T, entry Entry, outcome string, ids []string) []byte {
	t.Helper()
	output, err := wipdwire.EncodeCanonical(map[string]any{"outcome": outcome})
	if err != nil {
		t.Fatal(err)
	}
	var accepted any
	if len(ids) > 0 {
		accepted = map[string]any{"first_event_id": ids[0], "last_event_id": ids[len(ids)-1], "event_count": uint64(len(ids))}
	}
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID,
		"authority_epoch": entry.Command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation":   map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(entry.Command.Request.Operation.Version)},
		"environment": map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
		"result":      map[string]any{"code": string(operation.ResultSucceeded), "output": output, "problem_code": nil}, "accepted_events": accepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
