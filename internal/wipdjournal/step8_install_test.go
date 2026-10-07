package wipdjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	step8A    = "00000000000000000000000041"
	step8B    = "00000000000000000000000042"
	step8Edge = "00000000000000000000000060"
)

func step8InstallEvent(t *testing.T, number int, kind, subject, repo string, p map[string]any, entry *Entry) wipdwire.EventRecord {
	t.Helper()
	id := fmt.Sprintf("%026d", number)
	f := map[string]any{
		"schema": "wipd.event/1", "event_id": id, "domain_id": testDomainID, "command_id": fmt.Sprintf("%026d", number+200), "request_hash": hydrationDigest([]byte(id)),
		"environment": map[string]any{"id": testEnvironmentID, "sequence": uint64(number)}, "acted_at": "2026-10-02T00:00:00Z", "occurred_at": "2026-10-02T00:00:00Z", "kind": kind, "subject_id": subject, "repo_id": repo, "payload": p,
	}
	if entry != nil {
		f["command_id"], f["request_hash"], f["acted_at"] = entry.Command.ID, entry.RequestHash, entry.Command.ActedAt
		f["environment"] = map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq}
	}
	raw, err := wipdwire.EncodeCanonical(f)
	if err != nil {
		t.Fatal(err)
	}
	return wipdwire.EventRecord{EventID: id, Record: raw}
}

func step8InstallFixture(t *testing.T, journal *Journal, d operation.Definition) (Entry, []wipdwire.EventRecord, wipdwire.EventRecord, map[string]any) {
	t.Helper()
	records := []wipdwire.EventRecord{
		step8InstallEvent(t, 100, "matter.created", step8A, testRepoID, map[string]any{"id": step8A, "locator": "a", "title": "A"}, nil),
		step8InstallEvent(t, 101, "matter.created", step8B, testRepoID, map[string]any{"id": step8B, "locator": "b", "title": "B"}, nil),
	}
	var input operation.Input
	kind, subject := "", step8A
	p, output := map[string]any{}, map[string]any{}
	switch d.Metadata().Operation {
	case operation.DependencyAddV1.Metadata().Operation:
		input = operation.DependencyAddInput{BlockedID: step8A, BlockerID: step8B}
		kind = "dependency.added"
		p = map[string]any{"edge": step8Edge, "blocker": step8B}
		output = map[string]any{"edge": step8Edge, "blocked_id": step8A, "blocker_id": step8B}
	case operation.DependencyRemoveV1.Metadata().Operation:
		input = operation.DependencyRemoveInput{BlockedID: step8A, BlockerID: step8B}
		kind = "dependency.removed"
		p = map[string]any{"edge": step8Edge, "blocker": step8B}
		output = map[string]any{"blocked_id": step8A, "blocker_id": step8B}
		records = append(records, step8InstallEvent(t, 102, "dependency.added", step8A, testRepoID, p, nil))
	case operation.ReferenceBindV1.Metadata().Operation:
		input = operation.ReferenceBindInput{MatterID: step8A, Reference: "NEW"}
		kind = "reference.added"
		p = map[string]any{"ref": "NEW", "tracker_push_level": "off"}
		output = map[string]any{"matter_id": step8A, "reference": "NEW"}
	case operation.ReferenceUnbindV1.Metadata().Operation:
		input = operation.ReferenceUnbindInput{MatterID: step8A, Reference: "OLD"}
		kind = "reference.removed"
		p = map[string]any{"ref": "OLD", "tracker_push_level": "off"}
		output = map[string]any{"matter_id": step8A, "reference": "OLD"}
		records = append(records, step8InstallEvent(t, 102, "reference.bound", step8A, testRepoID, map[string]any{"ref": "OLD"}, nil))
	case operation.ReferenceRebindV1.Metadata().Operation:
		input = operation.ReferenceRebindInput{MatterID: step8A, From: "OLD", To: "NEW"}
		kind = "reference.rebound"
		p = map[string]any{"from": "OLD", "to": "NEW", "tracker_push_level": "off"}
		output = map[string]any{"matter_id": step8A, "reference": "NEW", "previous_reference": "OLD"}
		records = append(records, step8InstallEvent(t, 102, "reference.bound", step8A, testRepoID, map[string]any{"ref": "OLD"}, nil))
	}
	entry, err := journal.PrepareCommand(CommandInput{ID: fmt.Sprintf("%026d", 400), Request: operation.Request{Operation: d.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: testRepoID}, Input: input}})
	if err != nil {
		t.Fatal(err)
	}
	return entry, records, step8InstallEvent(t, 110, kind, subject, testRepoID, p, &entry), output
}

func step8InstallReceipt(t *testing.T, entry Entry, eventID string, output map[string]any) map[string]any {
	t.Helper()
	raw, err := wipdwire.EncodeCanonical(output)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": testDomainID, "authority_epoch": uint64(7), "identity_schema": "wipd.command/1", "command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation": map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(1)}, "environment": map[string]any{"id": testEnvironmentID, "sequence": entry.EnvironmentSeq},
		"result": map[string]any{"code": "result.succeeded", "output": raw, "problem_code": nil}, "accepted_events": map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)},
	}
}

func TestStep8TerminalBindingAndAtomicRollback(t *testing.T) {
	for _, d := range []operation.Definition{operation.DependencyAddV1, operation.DependencyRemoveV1, operation.ReferenceBindV1, operation.ReferenceUnbindV1, operation.ReferenceRebindV1} {
		for _, mutation := range []string{"valid", "kind", "subject", "Repo", "event-hash", "event-sequence", "acted-at", "input", "snapshot", "output", "output-extra", "edge-output", "operation", "range", "no-range", "receipt-hash", "receipt-environment", "effectful-refusal"} {
			t.Run(d.Metadata().Operation.Name+"/"+mutation, func(t *testing.T) {
				ctx := context.Background()
				root := filepath.Join(privateTempDir(t), "journal")
				journal := openInstallTestJournal(t, root)
				defer func() { _ = journal.Close() }()
				entry, records, event, output := step8InstallFixture(t, journal, d)
				base, err := journal.InstallPull(ctx, (mustStep8Snapshot(t, journal)).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
				if err != nil {
					t.Fatal(err)
				}
				beforeOverlay, err := journal.Overlay(ctx)
				if err != nil {
					t.Fatal(err)
				}
				fields, _ := wipdwire.DecodeCanonicalMap(event.Record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
				p := fields["payload"].(map[string]any)
				switch mutation {
				case "kind":
					fields["kind"] = "matter.paused"
					fields["payload"] = map[string]any{"from": "in-progress", "to": "paused"}
				case "subject":
					fields["subject_id"] = "00000000000000000000000043"
				case "Repo":
					fields["repo_id"] = step8B
				case "event-hash":
					fields["request_hash"] = hydrationDigest([]byte("forged"))
				case "event-sequence":
					fields["environment"].(map[string]any)["sequence"] = entry.EnvironmentSeq + 1
				case "acted-at":
					fields["acted_at"] = "2026-10-01T00:00:00Z"
				case "snapshot":
					if _, ok := p["tracker_push_level"]; ok {
						p["tracker_push_level"] = "boundary"
					} else {
						output["extra"] = true
					}
				case "input":
					if _, ok := p["blocker"]; ok {
						p["blocker"] = "00000000000000000000000043"
					} else if _, ok := p["ref"]; ok {
						p["ref"] = "SUBSTITUTED"
					} else {
						p["to"] = "SUBSTITUTED"
					}
				case "output":
					if _, ok := output["blocker_id"]; ok {
						output["blocker_id"] = step8A
					} else {
						output["reference"] = "SUBSTITUTED"
					}
				case "output-extra":
					output["extra"] = true
				case "edge-output":
					if _, ok := output["edge"]; ok {
						output["edge"] = step8B
					} else {
						output["extra"] = true
					}
				}
				event.Record, _ = wipdwire.EncodeCanonical(fields)
				r := step8InstallReceipt(t, entry, event.EventID, output)
				code := operation.ResultSucceeded
				switch mutation {
				case "operation":
					r["operation"].(map[string]any)["name"] = "matter.start"
				case "range":
					r["accepted_events"].(map[string]any)["event_count"] = uint64(2)
				case "no-range":
					r["accepted_events"] = nil
				case "receipt-hash":
					r["request_hash"] = hydrationDigest([]byte("forged"))
				case "receipt-environment":
					r["environment"].(map[string]any)["id"] = step8B
				case "effectful-refusal":
					code = operation.ResultRefused
					r["result"] = map[string]any{"code": string(code), "output": nil, "problem_code": "refusal.reference-missing"}
					r["accepted_events"] = nil
				}
				receipt, _ := wipdwire.EncodeCanonical(r)
				// Structural verifier recomputes the prefix, so semantic checks
				// must reject even a correctly hashed substituted tail.
				transfer := verifiedInstallTestTransfer(t, base.Anchor, []wipdwire.EventRecord{event})
				installed, err := journal.InstallAuthorityOutcome(ctx, base.Expectation(), entry, code, receipt, transfer)
				if mutation == "valid" {
					if err != nil || installed.Step8Projection == nil || !bytes.Equal(installed.Receipts[entry.Command.ID].CanonicalReceipt, receipt) {
						t.Fatalf("valid install: %v", err)
					}
					base = installed
				} else {
					if !errors.Is(err, ErrInvalidTransfer) {
						t.Fatalf("substitution accepted or wrong error: %v", err)
					}
					if after := mustStep8Snapshot(t, journal); !reflect.DeepEqual(after, base) {
						t.Fatal("failed terminal left partial prefix/receipt/projection/revision")
					}
					if after, err := journal.Overlay(ctx); err != nil || !reflect.DeepEqual(after, beforeOverlay) {
						t.Fatal("failed terminal left partial overlay")
					}
				}
				if err := journal.Close(); err != nil {
					t.Fatal(err)
				}
				journal = openInstallTestJournal(t, root)
				if !reflect.DeepEqual(mustStep8Snapshot(t, journal), base) {
					t.Fatal("reopen changed installed state")
				}
			})
		}
	}
}

func mustStep8Snapshot(t *testing.T, j *Journal) InstallSnapshot {
	t.Helper()
	s, err := j.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStep8ReopenRejectsForgedEventWithRecomputedPrefixAndOverlay(t *testing.T) {
	for _, mutation := range []string{"kind", "Repo", "reference", "edge-output", "receipt-output"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(privateTempDir(t), "journal")
			j := openInstallTestJournal(t, root)
			defer func() { _ = j.Close() }()
			d := operation.ReferenceBindV1
			if mutation == "edge-output" {
				d = operation.DependencyAddV1
			}
			entry, records, event, output := step8InstallFixture(t, j, d)
			base, err := j.InstallPull(ctx, mustStep8Snapshot(t, j).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
			if err != nil {
				t.Fatal(err)
			}
			r, _ := wipdwire.EncodeCanonical(step8InstallReceipt(t, entry, event.EventID, output))
			installed, err := j.InstallAuthorityOutcome(ctx, base.Expectation(), entry, operation.ResultSucceeded, r, verifiedInstallTestTransfer(t, base.Anchor, []wipdwire.EventRecord{event}))
			if err != nil {
				t.Fatal(err)
			}
			if mutation == "receipt-output" {
				output["reference"] = "SUBSTITUTED"
				forgedReceipt, _ := wipdwire.EncodeCanonical(step8InstallReceipt(t, entry, event.EventID, output))
				var trigger string
				if err := j.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='authority_command_outcome_immutable'`).Scan(&trigger); err != nil {
					t.Fatal(err)
				}
				if _, err := j.db.Exec(`DROP TRIGGER authority_command_outcome_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := j.db.Exec(`UPDATE authority_command_outcomes SET canonical_receipt=? WHERE command_id=?`, forgedReceipt, entry.Command.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := j.db.Exec(trigger); err != nil {
					t.Fatal(err)
				}
			}
			f, _ := wipdwire.DecodeCanonicalMap(event.Record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			switch mutation {
			case "kind":
				f["kind"] = "reference.bound"
				f["payload"] = map[string]any{"ref": "NEW"}
			case "Repo":
				f["repo_id"] = step8B
			case "reference":
				f["payload"].(map[string]any)["ref"] = "SUBSTITUTED"
			case "edge-output":
				f["payload"].(map[string]any)["edge"] = step8B
			}
			event.Record, _ = wipdwire.EncodeCanonical(f)
			forged := verifiedInstallTestTransfer(t, base.Anchor, []wipdwire.EventRecord{event})
			var trigger string
			if err := j.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='installed_event_no_update'`).Scan(&trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(`DROP TRIGGER installed_event_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(`UPDATE installed_events SET record=? WHERE event_id=?`, event.Record, event.EventID); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(`UPDATE environment_install SET revision=?,prefix_digest=? WHERE singleton=1`, installed.Revision+1, forged.End().Digest); err != nil {
				t.Fatal(err)
			}
			if _, err := j.db.Exec(`UPDATE environment_overlay SET source_bytes=? WHERE item_id=?`, event.Record, event.EventID); err != nil {
				t.Fatal(err)
			}
			manifest := forged.Manifest()
			raw, _ := wipdwire.EncodeCanonical(manifest)
			if _, err := j.db.Exec(`UPDATE environment_install SET revision=revision+1,manifest=? WHERE singleton=1`, raw); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(root, testIdentity); !errors.Is(err, ErrInvalidJournal) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("forged event survived reopen: %v", err)
			}
		})
	}
}

func TestStep8JournalSnapshotHasNoMutableCacheAlias(t *testing.T) {
	j := openInstallTestJournal(t, filepath.Join(privateTempDir(t), "journal"))
	defer func() { _ = j.Close() }()
	_, records, _, _ := step8InstallFixture(t, j, operation.ReferenceUnbindV1)
	s, err := j.InstallPull(context.Background(), mustStep8Snapshot(t, j).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(s.Step8Projection)
	s.Step8Projection.References[0].Reference = "tampered caller"
	got, _ := json.Marshal(mustStep8Snapshot(t, j).Step8Projection)
	if !bytes.Equal(got, want) {
		t.Fatal("caller mutated journal projection")
	}
}

func TestStep8TerminalUsesAlreadyPulledWholeCommandLineage(t *testing.T) {
	for _, mode := range []string{"success", "refusal hides effect", "success hides second effect"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(privateTempDir(t), "journal")
			j := openInstallTestJournal(t, root)
			defer func() { _ = j.Close() }()
			entry, records, event, output := step8InstallFixture(t, j, operation.ReferenceBindV1)
			records = append(records, event)
			if mode == "success hides second effect" {
				records = append(records, step8InstallEvent(t, 111, "reference.added", step8A, testRepoID, map[string]any{"ref": "SECOND", "tracker_push_level": "off"}, &entry))
			}
			base, err := j.InstallPull(ctx, mustStep8Snapshot(t, j).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
			if err != nil {
				t.Fatal(err)
			}
			r := step8InstallReceipt(t, entry, event.EventID, output)
			code := operation.ResultSucceeded
			if mode == "refusal hides effect" {
				code = operation.ResultRefused
				r["result"] = map[string]any{"code": string(code), "output": nil, "problem_code": "refusal.reference-exists"}
				r["accepted_events"] = nil
			}
			raw, _ := wipdwire.EncodeCanonical(r)
			installed, err := j.InstallAuthorityOutcome(ctx, base.Expectation(), entry, code, raw, emptyInstallTestTransfer(t, base.Anchor))
			if mode == "success" {
				if err != nil || !sameTransferAnchor(installed.Anchor, base.Anchor) || !bytes.Equal(installed.Receipts[entry.Command.ID].CanonicalReceipt, raw) {
					t.Fatalf("already-pulled success: %v", err)
				}
				base = installed
			} else if !errors.Is(err, ErrInvalidTransfer) || !reflect.DeepEqual(mustStep8Snapshot(t, j), base) {
				t.Fatalf("unreceipted pulled effect accepted/partially installed: %v", err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j = openInstallTestJournal(t, root)
			if !reflect.DeepEqual(mustStep8Snapshot(t, j), base) {
				t.Fatal("lineage check changed state on reopen")
			}
		})
	}
}
