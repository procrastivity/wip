package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func dropBatchSweepSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for index := len(batchSweepSchema) - 1; index >= 0; index-- {
		object := batchSweepSchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBatchSweepExplicitV16Migration(t *testing.T) {
	// Historical inline sweep intent is preserved, not backfilled into a new
	// submission or observed-boundary row.
	x := newSweepFixture(t, false, true, false)
	before := x.f.anchor(t)
	var original []byte
	if err := x.f.s.db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE command_id=?`, claimTestID(13)).Scan(&original); err != nil {
		t.Fatal(err)
	}
	dropBatchSweepSchemaForTest(t, x.f.s.db)
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, step17MigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations WHERE version<=16`, `DROP TABLE old_schema_migrations`,
		`PRAGMA user_version=16`,
	} {
		if _, err := x.f.s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkSchemaVersion(x.f.s.db, 16); err != nil {
		t.Fatal(err)
	}
	if err := x.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExisting(x.f.root); !errors.Is(err, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("ordinary open migrated v16: %v", err)
	}
	if err := UpgradeV16(x.f.root); err != nil {
		t.Fatal(err)
	}
	backup, err := connect(filepath.Join(x.f.root, "authority-v16.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(checkSchemaVersion(backup, 16), backup.Close()); err != nil {
		t.Fatal(err)
	}
	x.f.s, err = OpenExisting(x.f.root)
	if err != nil {
		t.Fatal(err)
	}
	var version, boundaries int
	var retained []byte
	if err = x.f.s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("migration version %d: %v", version, err)
	}
	if err = x.f.s.db.QueryRow(`SELECT count(*) FROM batch_sweep_boundaries`).Scan(&boundaries); err != nil || boundaries != 0 || x.f.anchor(t) != before {
		t.Fatalf("migration invented boundaries/effects: %d, %v", boundaries, err)
	}
	if err = x.f.s.db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE command_id=?`, claimTestID(13)).Scan(&retained); err != nil || !bytes.Equal(retained, original) {
		t.Fatalf("migration changed legacy receipt: %v", err)
	}
	command := x.command(90, x.acquired)
	status, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, "", signWith(x.f.key))
	if err != nil {
		t.Fatal(err)
	}
	sweepTestResult(t, status, "already-swept", "")
	if err = x.f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV16(x.f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("repeated migration = %v", err)
	}
}

func TestBatchSweepMigrationDoesNotBackfillAndRollsBack(t *testing.T) {
	x := newSweepFixture(t, false, false, false)
	command := x.command(90, x.acquired)
	if _, err := x.f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), x.f.peer, x.f.now, claimTestID(120), signWith(x.f.key)); err != nil {
		t.Fatal(err)
	}
	dropBatchSweepSchemaForTest(t, x.f.s.db)
	if err := installBatchSweep(x.f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("unwitnessed candidate history promoted: %v", err)
	}
	var tables int
	if err := x.f.s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='batch_sweep_boundaries'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("failed migration left a table: %d, %v", tables, err)
	}
}

func TestBatchSweepRetainsBoundaryAfterPrivateOnlyRepair(t *testing.T) {
	fixture := prepareRepairTerminalFixture(t, "success-missing-snapshot", 0xa1)
	f := fixture.f
	admitRepairTerminalFixture(t, fixture)
	before := f.anchor(t)
	terminal, err := f.s.completeGateExemptionRepair(context.Background(), fixture.canonical, fixture.hash, fixture.proof, []byte(claimTestID(120)), f.now)
	if err != nil || terminal.EventID != claimTestID(120) {
		t.Fatalf("private repair = %+v, %v", terminal, err)
	}
	var public int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE command_id=?`, fixture.command.ID).Scan(&public); err != nil || public != 0 {
		t.Fatalf("private repair promoted: %d, %v", public, err)
	}
	after := f.anchor(t)
	if after.EventCount != before.EventCount+1 {
		t.Fatal("private repair did not advance history")
	}
	command := step12Command(f, 90, fixture.command.EnvironmentSequence+1, operation.BatchSweepAnonymousV1,
		operation.BatchSweepAnonymousInput{MatterID: f.matter, BatchID: fixture.claim.BatchID, ClaimClose: operation.ClaimCloseReference{
			ClaimID: fixture.claim.ClaimID, ClaimEpoch: 1, ReleaseCommandID: claimTestID(999),
			ReleaseRequestHash: digestBytes([]byte("missing release")), TerminalReceiptDigest: digestBytes([]byte("missing receipt")),
			InstalledPrefixAnchor: operation.ClaimClosePrefix{EventCount: after.EventCount, EventID: &after.EventID, Digest: after.Digest},
		}}, "")
	status, err := f.s.sweepAnonymousBatch(context.Background(), command, hashCommand(t, command), f.peer, f.now, "", signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	sweepTestResult(t, status, "", operation.ProblemBatchSweepClaimClose)
	var retained PrefixAnchor
	if err = f.s.db.QueryRow(`SELECT event_count,event_id,prefix_digest FROM batch_sweep_boundaries WHERE command_id=?`, command.ID).
		Scan(&retained.EventCount, &retained.EventID, &retained.Digest); err != nil || retained != after {
		t.Fatalf("eventless boundary = %+v, %v; want %+v", retained, err, after)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("private-only repair plus refusal reopen: %v", err)
	}
}
