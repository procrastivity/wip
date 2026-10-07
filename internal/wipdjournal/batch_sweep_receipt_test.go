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
			root := filepath.Join(privateTempDir(t), "journal")
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
	journal := openInstallTestJournal(t, filepath.Join(privateTempDir(t), "journal"))
	defer func() { _ = journal.Close() }()
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
	root := filepath.Join(privateTempDir(t), "journal")
	journal := openInstallTestJournal(t, root)
	defer func() { _ = journal.Close() }()
	entry := prepareBatchSweepReceiptTestCommand(t, journal)
	event := batchSweepReceiptTestEvent(t, entry, "valid")
	eventID := event.EventID
	before, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transfer := verifiedInstallTestTransfer(t, before.Anchor, []wipdwire.EventRecord{event})
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

func TestBatchSweepInstallRejectsSubstitutedEvent(t *testing.T) {
	for _, mutation := range []string{"wrong-kind", "wrong-batch", "wrong-repo"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(privateTempDir(t), "journal")
			journal := openInstallTestJournal(t, root)
			defer func() { _ = journal.Close() }()
			entry := prepareBatchSweepReceiptTestCommand(t, journal)
			before, err := journal.InstallSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			event := batchSweepReceiptTestEvent(t, entry, mutation)
			transfer := verifiedInstallTestTransfer(t, before.Anchor, []wipdwire.EventRecord{event})
			receipt := batchSweepReceiptTestBytes(t, entry, "swept", []string{event.EventID})
			if _, err := journal.InstallAuthorityOutcome(ctx, before.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); !errors.Is(err, ErrInvalidTransfer) {
				t.Fatalf("substituted %s installed: %v", mutation, err)
			}
			if after, err := journal.InstallSnapshot(ctx); err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected event partially installed: %+v %v", after, err)
			}
			if records, err := journal.EventRecords(ctx); err != nil || len(records) != 0 {
				t.Fatalf("rejected event retained: %+v %v", records, err)
			}
			if overlay, err := journal.Overlay(ctx); err != nil || len(overlay) != 0 {
				t.Fatalf("rejected event changed overlay: %+v %v", overlay, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = openInstallTestJournal(t, root)
			if reopened, err := journal.InstallSnapshot(ctx); err != nil || !reflect.DeepEqual(reopened, before) {
				t.Fatalf("rejected event survived reopen: %+v %v", reopened, err)
			}
		})
	}
}

func TestBatchSweepReopenRejectsSubstitutedEvent(t *testing.T) {
	for _, mutation := range []string{"wrong-kind", "wrong-batch", "wrong-repo"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(privateTempDir(t), "journal")
			journal := openInstallTestJournal(t, root)
			defer func() { _ = journal.Close() }()
			entry := prepareBatchSweepReceiptTestCommand(t, journal)
			before, err := journal.InstallSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			event := batchSweepReceiptTestEvent(t, entry, "valid")
			transfer := verifiedInstallTestTransfer(t, before.Anchor, []wipdwire.EventRecord{event})
			receipt := batchSweepReceiptTestBytes(t, entry, "swept", []string{event.EventID})
			if _, err := journal.InstallAuthorityOutcome(ctx, before.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = openInstallTestJournal(t, root)
			corrupt := batchSweepReceiptTestEvent(t, entry, mutation)
			corruptTransfer := verifiedInstallTestTransfer(t, before.Anchor, []wipdwire.EventRecord{corrupt})
			manifest, err := encodeManifest(corruptTransfer.Manifest())
			if err != nil {
				t.Fatal(err)
			}
			var trigger string
			if err := journal.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='installed_event_no_update'`).Scan(&trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.db.Exec(`UPDATE installed_events SET record=? WHERE event_id=?`, corrupt.Record, event.EventID); err == nil {
				t.Fatal("immutable event allowed substitution without trigger bypass")
			}
			// Preserve the exact receipt/range and command identity, recompute a
			// valid prefix/manifest/overlay, and restore the original trigger.
			// Reopen must reject the semantic substitution, not broken hashing,
			// a stale overlay, or a missing schema object.
			tx, err := journal.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.Exec(`DROP TRIGGER installed_event_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE installed_events SET record=? WHERE event_id=?`, corrupt.Record, event.EventID); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			state, err := readInstallState(tx)
			if err != nil {
				t.Fatal(err)
			}
			if err := updateInstallState(ctx, tx, state, corruptTransfer.End(), corruptTransfer.Manifest().Digest, manifest); err != nil {
				t.Fatal(err)
			}
			if err := rebuildOverlay(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			installed, err := journal.InstallSnapshot(ctx)
			if err != nil || !sameTransferAnchor(installed.Anchor, corruptTransfer.End()) || !bytes.Equal(installed.Receipts[entry.Command.ID].CanonicalReceipt, receipt) {
				t.Fatalf("corruption changed receipt or failed to retain the recomputed prefix: %+v %v", installed, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(root, testIdentity)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrInvalidJournal) {
				t.Fatalf("reopen accepted substituted %s: %v", mutation, err)
			}
		})
	}
}

func batchSweepReceiptTestEvent(t *testing.T, entry Entry, mutation string) wipdwire.EventRecord {
	t.Helper()
	eventID := testCommandPrefix + "90"
	input := entry.Command.Request.Input.(operation.BatchSweepAnonymousInput)
	fields := map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"environment": map[string]any{"id": testEnvironmentID, "sequence": entry.EnvironmentSeq},
		"acted_at":    entry.Command.ActedAt, "occurred_at": entry.Command.ActedAt,
		"kind": "batch.swept", "subject_id": input.BatchID, "repo_id": entry.Command.Request.Context.Repo, "payload": map[string]any{},
	}
	switch mutation {
	case "wrong-kind":
		fields["kind"] = "matter.finished"
		fields["payload"] = map[string]any{"from": "in-progress", "to": "done"}
	case "wrong-batch":
		fields["subject_id"] = testCommandPrefix + "76"
	case "wrong-repo":
		fields["repo_id"] = testCommandPrefix + "77"
	}
	raw, err := wipdwire.EncodeCanonical(fields)
	if err != nil || !validAuthorityEvent(raw, testDomainID, eventID) {
		t.Fatalf("%s fixture is not a structurally valid event: %v", mutation, err)
	}
	return wipdwire.EventRecord{EventID: eventID, Record: raw}
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
