package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestV1AnonymousBatchRefusesBeforeAnyMigrationDDL(t *testing.T) {
	h := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), baselineRegister(), 1)
	matter := h.matter("legacy-anon", "Legacy anonymous Matter")
	batch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{}}, nil
	})
	h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: matter}})
	beforeSchema := schemaShape(t, h.Store)
	beforeLedger := h.rowsOf("schema_migrations", "")
	beforeEvents := h.rowsOf("events", "")
	beforeProjection := h.snapshotProjection()
	beforeBatches := h.rowsOf("batches", "")
	if _, err := h.reopen(shipped(), latestVersion(shipped())); err == nil {
		t.Fatal("legacy anonymous Batch migration succeeded")
	} else {
		refusalMentions(t, "legacy anonymous Batch migration", err, "no Matter association")
	}
	back, err := h.reopen(baselineRegister(), 1)
	if err != nil {
		t.Fatal(err)
	}
	wantSameSchema(t, "schema after refused anonymous migration", schemaShape(t, back.Store), beforeSchema)
	wantSameRows(t, "migration ledger after refusal", beforeLedger, back.rowsOf("schema_migrations", ""))
	wantSameRows(t, "event log after refusal", beforeEvents, back.rowsOf("events", ""))
	wantSameRows(t, "Batch projection after refusal", beforeBatches, back.rowsOf("batches", ""))
	back.wantSameProjection("all projections after refusal", beforeProjection, back.snapshotProjection())
}

func TestV2ToV3PreservesLogAndMatchesDirectSchema(t *testing.T) {
	h := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), shipped(), 2)
	matter := h.matter("v2-to-v3", "V2 to V3 Matter")
	batch := h.newBatch("v2-to-v3-batch")
	run := startRunForTest(h, batch, "run-01", matter)
	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: CloseCompleted}})
	h.commit(Draft{Type: TypeRunFinished, Subject: run, Payload: RunFinished{}})
	log := h.rowsOf("events", "")

	migrated, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("v2-to-v3 reopen: %v", err)
	}
	wantSameRows(t, "event log after v2-to-v3", log, migrated.rowsOf("events", ""))

	direct := newHarnessAt(t, filepath.Join(t.TempDir(), "direct-v3.db"), shipped(), latestVersion(shipped()))
	wantSameSchema(t, "v2-to-v3 schema against direct v3", schemaShape(t, migrated.Store), schemaShape(t, direct.Store))
	gotRun, err := migrated.Run(migrated.ctx, run)
	if err != nil || gotRun.CloseReason != CloseCompleted || gotRun.ClosedAt == nil {
		t.Fatalf("migrated terminal Run = %#v, err=%v", gotRun, err)
	}
}

func TestV2NamedBatchRunAndDispatchMigrateToV3(t *testing.T) {
	h := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), shipped(), 2)
	matter := h.matter("migrated", "Migrated Matter")
	batch := h.newBatch("migrated-batch")
	run := startRunForTest(h, batch, "run-01", matter)
	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	migrated, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := migrated.Batch(migrated.ctx, batch)
	if err != nil || b.State != "live" || b.Name != "migrated-batch" {
		t.Fatalf("migrated Batch = %#v, err=%v", b, err)
	}
	gotRun, err := migrated.Run(migrated.ctx, run)
	if err != nil || !gotRun.Open || gotRun.Locator != "run-01" {
		t.Fatalf("migrated Run = %#v, err=%v", gotRun, err)
	}
	gotDispatch, err := migrated.Dispatch(migrated.ctx, dispatch)
	if err != nil || !gotDispatch.Open || gotDispatch.Run != run || gotDispatch.Matter != matter {
		t.Fatalf("migrated Dispatch = %#v, err=%v", gotDispatch, err)
	}
}
