package wipdjournal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestInstallFoldRejectsPhantomReceiptRangeAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	entry := prepareAndAdmitInstallTestCommand(t, journal, testCommandPrefix+"81")
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transfer := successfulInstallTestTransfer(t, installed.Anchor, entry)
	phantomID := testCommandPrefix + "82"
	receipt := installTestReceipt(t, entry, operation.ResultSucceeded, []string{phantomID})
	if _, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("phantom accepted event range error = %v; want ErrInvalidTransfer", err)
	}
	if got, err := journal.Get(entry.Command.ID); err != nil || got.State != StatePendingReturn {
		t.Fatalf("journal disposition after rejected range = %+v, %v", got, err)
	}
	if installed, err = journal.InstallSnapshot(context.Background()); err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 0 {
		t.Fatalf("phantom range changed installed state: %+v, %v", installed, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	if installed, err = journal.InstallSnapshot(context.Background()); err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 0 {
		t.Fatalf("phantom range survived reopen: %+v, %v", installed, err)
	}
}

func TestInstallFoldReceiptRangeSelectsExactEventsFromMixedDelta(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	entry := prepareAndAdmitInstallTestCommand(t, journal, testCommandPrefix+"86")
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unrelatedID := testCommandPrefix + "89"
	acceptedID := testCommandPrefix + "90"
	records := []wipdwire.EventRecord{
		{EventID: unrelatedID, Record: installTestEventRecord(t, unrelatedID, testCommandPrefix+"87", "sha256:"+hex.EncodeToString(make([]byte, sha256.Size)), 17)},
		{EventID: acceptedID, Record: installTestEventRecord(t, acceptedID, entry.Command.ID, entry.RequestHash, entry.EnvironmentSeq)},
	}
	transfer := verifiedInstallTestTransfer(t, installed.Anchor, records)
	receipt := installTestReceipt(t, entry, operation.ResultSucceeded, []string{acceptedID})
	installed, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultSucceeded, receipt, transfer)
	if err != nil {
		t.Fatalf("InstallFold mixed delta: %v", err)
	}
	if installed.Anchor.EventCount != 2 || len(installed.Receipts) != 1 {
		t.Fatalf("installed mixed delta = %+v; want both events and one receipt", installed)
	}
	var eventCount, firstPosition, lastPosition int64
	var firstID, lastID string
	if err = journal.db.QueryRow(`SELECT event_count,first_position,last_position,first_event_id,last_event_id FROM installed_receipts WHERE command_id=?`, entry.Command.ID).
		Scan(&eventCount, &firstPosition, &lastPosition, &firstID, &lastID); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || firstPosition != 2 || lastPosition != 2 || firstID != acceptedID || lastID != acceptedID {
		t.Fatalf("persisted accepted range = count %d, positions %d..%d, IDs %s..%s; want only accepted event at position 2", eventCount, firstPosition, lastPosition, firstID, lastID)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	if installed, err = journal.InstallSnapshot(context.Background()); err != nil || installed.Anchor.EventCount != 2 || len(installed.Receipts) != 1 {
		t.Fatalf("mixed delta/range did not validate across reopen: %+v, %v", installed, err)
	}
}

func TestInstallFoldAcceptsReceiptRangeAlreadyInInstalledLineage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	entry := prepareAndAdmitInstallTestCommand(t, journal, testCommandPrefix+"91")
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acceptedID := testCommandPrefix + "92"
	priorTail := verifiedInstallTestTransfer(t, installed.Anchor, []wipdwire.EventRecord{{
		EventID: acceptedID, Record: installTestEventRecord(t, acceptedID, entry.Command.ID, entry.RequestHash, entry.EnvironmentSeq),
	}})
	installed, err = journal.InstallPull(context.Background(), installed.Expectation(), priorTail)
	if err != nil {
		t.Fatalf("InstallPull of accepted lineage: %v", err)
	}
	entry, err = journal.Get(entry.Command.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt := installTestReceipt(t, entry, operation.ResultSucceeded, []string{acceptedID})
	emptyFold := emptyInstallTestTransfer(t, installed.Anchor)
	installed, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultSucceeded, receipt, emptyFold)
	if err != nil {
		t.Fatalf("InstallFold receipt replay from installed lineage: %v", err)
	}
	if installed.Anchor.EventCount != 1 || len(installed.Receipts) != 1 || installed.Receipts[entry.Command.ID].ResultCode != operation.ResultSucceeded {
		t.Fatalf("replayed receipt duplicated or lost installed event: %+v", installed)
	}
	var eventCount, firstPosition, lastPosition int64
	if err = journal.db.QueryRow(`SELECT event_count,first_position,last_position FROM installed_receipts WHERE command_id=?`, entry.Command.ID).
		Scan(&eventCount, &firstPosition, &lastPosition); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || firstPosition != 1 || lastPosition != 1 {
		t.Fatalf("installed-lineage range = count %d positions %d..%d; want existing position 1 only", eventCount, firstPosition, lastPosition)
	}
}

func TestInstallFoldCommitAndOverlayAreAtomicAcrossFailureAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	entry := prepareAndAdmitInstallTestCommand(t, journal, testCommandPrefix+"83")
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transfer := successfulInstallTestTransfer(t, installed.Anchor, entry)
	receipt := installTestReceipt(t, entry, operation.ResultSucceeded, transfer.EventIDs())
	if _, err = journal.db.Exec(`CREATE TRIGGER fail_overlay_rebuild BEFORE INSERT ON environment_overlay BEGIN SELECT RAISE(ABORT,'injected overlay rebuild failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); err == nil {
		t.Fatal("InstallFold unexpectedly committed despite injected overlay write failure")
	}
	if _, err = journal.db.Exec(`DROP TRIGGER fail_overlay_rebuild`); err != nil {
		t.Fatal(err)
	}
	if got, err := journal.Get(entry.Command.ID); err != nil || got.State != StatePendingReturn {
		t.Fatalf("failed fold changed command disposition: %+v, %v", got, err)
	}
	if snapshot, err := journal.InstallSnapshot(context.Background()); err != nil || snapshot.Anchor.EventCount != 0 || len(snapshot.Receipts) != 0 {
		t.Fatalf("failed fold leaked prefix or receipt: %+v, %v", snapshot, err)
	}
	items, err := journal.Overlay(context.Background())
	if err != nil || len(items) != 1 || items[0].Kind != "provisional" || items[0].ID != entry.Command.ID {
		t.Fatalf("failed fold overlay = %+v, %v; want original provisional row", items, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	entry, err = journal.Get(entry.Command.ID)
	if err != nil || entry.State != StatePendingReturn {
		t.Fatalf("command after failure/reopen = %+v, %v", entry, err)
	}
	installed, err = journal.InstallSnapshot(context.Background())
	if err != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 0 {
		t.Fatalf("partial fold persisted across reopen: %+v, %v", installed, err)
	}
	if _, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultSucceeded, receipt, transfer); err != nil {
		t.Fatal(err)
	}
	installed, err = journal.InstallSnapshot(context.Background())
	if err != nil || installed.Anchor.EventCount != 1 || len(installed.Receipts) != 1 {
		t.Fatalf("successful fold state = %+v, %v", installed, err)
	}
	got, err := journal.Get(entry.Command.ID)
	if err != nil || got.State != StateReturned {
		t.Fatalf("successful fold disposition = %+v, %v; want returned", got, err)
	}
	items, err = journal.Overlay(context.Background())
	if err != nil || len(items) != 1 || items[0].Kind != "folded" || items[0].ID != transfer.EventIDs()[0] {
		t.Fatalf("rebuilt overlay = %+v, %v; want folded source only", items, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	installed, err = journal.InstallSnapshot(context.Background())
	if err != nil || installed.Anchor.EventCount != 1 || len(installed.Receipts) != 1 || installed.Receipts[entry.Command.ID].RequestHash != entry.RequestHash {
		t.Fatalf("committed fold did not recover exactly after reopen: %+v, %v", installed, err)
	}
}

func TestAdmissionMarkerAndRecoverableOverlayCommitTogether(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	entry, err := journal.PrepareCommand(commandInput(testCommandPrefix+"84", "atomic-admit", "Atomic admission"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.db.Exec(`CREATE TRIGGER fail_overlay_admission BEFORE INSERT ON environment_overlay BEGIN SELECT RAISE(ABORT,'injected admission overlay failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AdmitPending(context.Background(), installed.Expectation(), entry.Command.ID); err == nil {
		t.Fatal("AdmitPending unexpectedly committed with overlay failure")
	}
	if _, err = journal.db.Exec(`DROP TRIGGER fail_overlay_admission`); err != nil {
		t.Fatal(err)
	}
	entry, err = journal.Get(entry.Command.ID)
	if err != nil || entry.State != StatePreAdmission {
		t.Fatalf("failed admission changed disposition: %+v, %v", entry, err)
	}
	if pending, err := journal.JournaledCommands(); err != nil || len(pending) != 0 {
		t.Fatalf("failed admission became return-eligible: %+v, %v", pending, err)
	}
	if overlay, err := journal.Overlay(context.Background()); err != nil || len(overlay) != 0 {
		t.Fatalf("failed admission exposed overlay: %+v, %v", overlay, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	entry, err = journal.Get(entry.Command.ID)
	if err != nil || entry.State != StatePreAdmission {
		t.Fatalf("pre-admission state after reopen = %+v, %v", entry, err)
	}
	installed, err = journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AdmitPending(context.Background(), installed.Expectation(), entry.Command.ID); err != nil {
		t.Fatal(err)
	}
	entry, err = journal.Get(entry.Command.ID)
	if err != nil || entry.State != StatePendingReturn {
		t.Fatalf("successful recovered admission = %+v, %v", entry, err)
	}
	if overlay, err := journal.Overlay(context.Background()); err != nil || len(overlay) != 1 || overlay[0].Kind != "provisional" {
		t.Fatalf("committed admission overlay = %+v, %v", overlay, err)
	}
}

func TestSchemaV1JournalUpgradeKeepsRowsPreAdmission(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal := openInstallTestJournal(t, root)
	input := commandInput(testCommandPrefix+"85", "migration", "Schema upgrade")
	original, err := journal.PrepareCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = downgradeInstallTestDBToV1(journal.db); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openInstallTestJournal(t, root)
	t.Cleanup(func() { _ = journal.Close() })
	upgraded, err := journal.Get(original.Command.ID)
	if err != nil || upgraded.State != StatePreAdmission || upgraded.RequestHash != original.RequestHash ||
		!bytes.Equal(upgraded.CanonicalBytes, original.CanonicalBytes) {
		t.Fatalf("upgraded command = %+v, %v; want immutable identity mapped to non-eligible pre-admission", upgraded, err)
	}
	if pending, err := journal.JournaledCommands(); err != nil || len(pending) != 0 {
		t.Fatalf("upgraded legacy row became return-eligible: %+v, %v", pending, err)
	}
}

func openInstallTestJournal(t *testing.T, root string) *Journal {
	t.Helper()
	journal, err := Open(root, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func prepareAndAdmitInstallTestCommand(t *testing.T, journal *Journal, id string) Entry {
	t.Helper()
	_, err := journal.PrepareCommand(commandInput(id, "install:"+id, "Install test"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AdmitPending(context.Background(), installed.Expectation(), id); err != nil {
		t.Fatal(err)
	}
	entry, err := journal.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func emptyInstallTestTransfer(t *testing.T, start wipdwire.PrefixAnchor) VerifiedTransfer {
	t.Helper()
	manifest := emptyTransferManifest(testDomainID, testIdentity.AuthorityEpoch, start)
	transfer, err := VerifyTransfer(testDomainID, testIdentity.AuthorityEpoch, start, start, nil, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}

func successfulInstallTestTransfer(t *testing.T, start wipdwire.PrefixAnchor, entry Entry) VerifiedTransfer {
	t.Helper()
	eventID := testCommandPrefix + "90"
	record := installTestEventRecord(t, eventID, entry.Command.ID, entry.RequestHash, entry.EnvironmentSeq)
	return verifiedInstallTestTransfer(t, start, []wipdwire.EventRecord{{EventID: eventID, Record: record}})
}

func installTestEventRecord(t *testing.T, eventID, commandID, requestHash string, sequence uint64) []byte {
	t.Helper()
	record, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": testDomainID,
		"command_id": commandID, "request_hash": requestHash,
		"environment": map[string]any{"id": testEnvironmentID, "sequence": sequence},
		"acted_at":    "2026-09-28T00:00:00Z", "occurred_at": "2026-09-28T00:00:01Z",
		"kind": "matter.created", "subject_id": eventID, "repo_id": testRepoID,
		"payload": map[string]any{"id": eventID, "locator": "install:" + eventID, "title": "Installed matter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func verifiedInstallTestTransfer(t *testing.T, start wipdwire.PrefixAnchor, records []wipdwire.EventRecord) VerifiedTransfer {
	t.Helper()
	var length [8]byte
	previous, err := hex.DecodeString(start.Digest[len("sha256:"):])
	if err != nil {
		t.Fatal(err)
	}
	chain := previous
	for _, event := range records {
		binary.BigEndian.PutUint64(length[:], uint64(len(event.Record)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain)
		_, _ = h.Write(length[:])
		_, _ = h.Write(event.Record)
		chain = h.Sum(nil)
	}
	var end wipdwire.PrefixAnchor
	if len(records) == 0 {
		end = cloneTransferAnchor(start)
	} else {
		endID := records[len(records)-1].EventID
		end = wipdwire.PrefixAnchor{EventCount: start.EventCount + uint64(len(records)), EventID: &endID, Digest: "sha256:" + hex.EncodeToString(chain)}
	}
	manifest := emptyTransferManifest(testDomainID, testIdentity.AuthorityEpoch, end)
	transfer, err := VerifyTransfer(testDomainID, testIdentity.AuthorityEpoch, start, end, records, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return transfer
}

func installTestReceipt(t *testing.T, entry Entry, result operation.ResultCode, eventIDs []string) []byte {
	t.Helper()
	var accepted, output, problem any
	if result == operation.ResultSucceeded {
		accepted = map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[len(eventIDs)-1], "event_count": uint64(len(eventIDs))}
		output = []byte{0xa0}
	} else {
		problem = "operation.invalid-request"
	}
	encoded, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID,
		"authority_epoch": entry.Command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation":       map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(entry.Command.Request.Operation.Version)},
		"environment":     map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
		"result":          map[string]any{"code": string(result), "output": output, "problem_code": problem},
		"accepted_events": accepted,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func downgradeInstallTestDBToV1(db *sql.DB) error {
	for _, statement := range []string{
		`DROP TRIGGER authority_command_outcome_immutable`, `DROP TRIGGER authority_command_outcome_no_delete`, `DROP TABLE authority_command_outcomes`,
		`DROP TRIGGER birth_release_after_pending_claim_acquire`, `DROP TRIGGER command_claim_acquire_id_conflict`,
		`DROP TRIGGER command_after_pending_claim_acquire`, `DROP TRIGGER claim_acquire_command_id_conflict`,
		`DROP TRIGGER claim_acquire_before_insert`, `DROP TRIGGER claim_acquire_state_transition`,
		`DROP TRIGGER claim_acquire_no_delete`, `DROP TRIGGER claim_acquire_identity_immutable`,
		`DROP TABLE claim_acquire_attempts`,
		`DROP TRIGGER hydration_grant_requires_installed`, `DROP TRIGGER installed_claim_grant_no_update`,
		`DROP TRIGGER installed_claim_grant_no_delete`, `DROP TABLE installed_claim_grants`,
		`DROP TRIGGER hydration_pin_no_update`, `DROP TRIGGER hydration_pin_no_delete`, `DROP TABLE hydration_pins`,
		`DROP INDEX hydration_grant_claim_unique`, `DROP TRIGGER hydration_grant_no_delete`,
		`DROP TRIGGER hydration_grant_state_transition`, `DROP TRIGGER hydration_grant_identity_immutable`, `DROP TABLE hydration_grants`,
		`DROP TRIGGER command_birth_release_id_conflict`, `DROP TRIGGER command_after_pending_birth_release`,
		`DROP TRIGGER birth_release_command_id_conflict`, `DROP TRIGGER birth_release_before_insert`,
		`DROP TRIGGER birth_release_state_transition`, `DROP TRIGGER birth_release_no_delete`,
		`DROP TRIGGER birth_release_identity_immutable`, `DROP TABLE birth_release_attempts`,
		`DROP TRIGGER command_state_transition`, `DROP INDEX commands_pending_order`,
		`DROP TRIGGER command_identity_immutable`, `DROP TRIGGER command_no_delete`,
		`DROP INDEX environment_overlay_order`, `DROP TABLE environment_overlay`,
		`DROP TRIGGER installed_receipt_no_update`, `DROP TRIGGER installed_receipt_no_delete`, `DROP TABLE installed_receipts`,
		`DROP TRIGGER installed_event_no_update`, `DROP TRIGGER installed_event_no_delete`, `DROP TABLE installed_events`,
		`DROP TRIGGER environment_install_revision`, `DROP TABLE environment_install`,
		`ALTER TABLE commands RENAME TO commands_v2`,
		`CREATE TABLE commands(command_id TEXT PRIMARY KEY,environment_sequence INTEGER NOT NULL UNIQUE CHECK(environment_sequence>0),journal_position INTEGER UNIQUE CHECK(journal_position IS NULL OR journal_position>0),request_hash TEXT NOT NULL CHECK(length(request_hash)=71),canonical_bytes BLOB NOT NULL,delivery TEXT NOT NULL CHECK(delivery IN ('authority','claim','provisional','capture','environment')),state TEXT NOT NULL CHECK(state IN ('attempt-prepared','journaled')),CHECK((delivery='authority' AND journal_position IS NULL AND state='attempt-prepared') OR (delivery!='authority' AND journal_position IS NOT NULL AND state='journaled'))) STRICT, WITHOUT ROWID`,
		`INSERT INTO commands SELECT command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery,CASE WHEN state='pre-admission' THEN 'journaled' ELSE state END FROM commands_v2`,
		`DROP TABLE commands_v2`,
		`CREATE INDEX commands_pending_order ON commands(state,journal_position)`,
		`CREATE TRIGGER command_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,journal_position,request_hash,canonical_bytes,delivery ON commands BEGIN SELECT RAISE(ABORT,'immutable command identity'); END`,
		`CREATE TRIGGER command_no_delete BEFORE DELETE ON commands BEGIN SELECT RAISE(ABORT,'immutable command journal'); END`,
		`ALTER TABLE schema_migrations RENAME TO schema_migrations_v2`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=1),name TEXT NOT NULL CHECK(name='durable-client-command-journal')) STRICT`,
		`INSERT INTO schema_migrations VALUES(1,'durable-client-command-journal')`,
		`DROP TABLE schema_migrations_v2`,
		`PRAGMA user_version=1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("downgrade to v1 (%s): %w", statement, err)
		}
	}
	return nil
}
