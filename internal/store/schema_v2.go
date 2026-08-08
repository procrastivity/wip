package store

import (
	"context"
	"database/sql"
	"fmt"
)

func rejectLegacyRuns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, state FROM runs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return err
		}
		return fmt.Errorf("incompatible Run row %s has legacy state %q; schema v2 cannot infer its locator or lifecycle", id, state)
	}
	return rows.Err()
}

// rejectLegacyBatches guards the v3 Batch rebuild. An owner-less anonymous
// Batch is legal P1 history — the old refresh path minted one per dispatch,
// behavior run-substrate later removed — and while it stayed memberless and
// run-less the v3 copy converts it to a closed (swept) legacy row. One that
// gained members or runs has an owner the migration cannot infer, and refuses.
// (Amended by `roles` from a blanket refusal, by decision: the blanket made
// every real P1 store structurally unable to migrate past v2.)
func rejectLegacyBatches(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT b.id FROM batches b WHERE b.name IS NULL AND (
		EXISTS (SELECT 1 FROM batch_members m WHERE m.batch = b.id)
		OR EXISTS (SELECT 1 FROM runs r WHERE r.batch = b.id)) ORDER BY b.id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		return fmt.Errorf("incompatible anonymous Batch row %s has no Matter association; migration cannot infer its owner", id)
	}
	return rows.Err()
}

// v2 adds the ratified Run projection and the host-wide Matter claim carried
// by a P2 Dispatch. The v1 statements remain frozen.
func v2Statements() []string {
	return []string{
		seedTaxonomy(V2Taxonomy),
		`DROP TRIGGER runs_born_by_event`, `DROP TRIGGER runs_immutable_birth`, `DROP TRIGGER runs_advance`, `DROP TRIGGER runs_no_delete`,
		`DROP TRIGGER dispatches_born_by_event`, `DROP TRIGGER dispatches_immutable_birth`, `DROP TRIGGER dispatches_advance`, `DROP TRIGGER dispatches_no_delete`,
		`CREATE TABLE runs_v2 (
			id TEXT NOT NULL PRIMARY KEY, clone TEXT NOT NULL REFERENCES clones(id), batch TEXT NOT NULL REFERENCES batches(id),
			locator TEXT NOT NULL, state TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','stood-down','reaped')),
			started_at TEXT NOT NULL, closed_at TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id), last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26), CHECK ((state='closed') = (close_reason IS NOT NULL)),
			CHECK ((state='closed') = (closed_at IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE UNIQUE INDEX runs_batch_locator ON runs_v2(batch, locator)`,
		`DROP TABLE runs`, `ALTER TABLE runs_v2 RENAME TO runs`,
		`CREATE TABLE dispatches_v2 (
			id TEXT NOT NULL PRIMARY KEY, clone TEXT NOT NULL REFERENCES clones(id), worktree TEXT NOT NULL REFERENCES worktrees(id),
			run TEXT REFERENCES runs(id), matter TEXT REFERENCES nodes(id), state TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','superseded','reaped')),
			opened_at TEXT NOT NULL, closed_at TEXT, birth_event TEXT NOT NULL REFERENCES events(id), last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26), CHECK ((state='closed') = (close_reason IS NOT NULL)), CHECK ((state='closed') = (closed_at IS NOT NULL)),
			CHECK ((run IS NULL) = (matter IS NULL))
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO dispatches_v2 (id,clone,worktree,state,close_reason,opened_at,closed_at,birth_event,last_event) SELECT id,clone,worktree,state,close_reason,opened_at,closed_at,birth_event,last_event FROM dispatches`,
		`DROP TABLE dispatches`, `ALTER TABLE dispatches_v2 RENAME TO dispatches`,
		`CREATE INDEX dispatches_open ON dispatches(worktree, state)`,
		`CREATE UNIQUE INDEX dispatches_open_matter ON dispatches(matter) WHERE state='open' AND matter IS NOT NULL`,
		`CREATE TABLE run_matters (
			run TEXT NOT NULL REFERENCES runs(id), matter TEXT NOT NULL REFERENCES nodes(id), ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
			PRIMARY KEY (run,matter), UNIQUE (run,ordinal)
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER runs_born_by_event BEFORE INSERT ON runs WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'runs: a row is born by exactly one event'); END`,
		`CREATE TRIGGER runs_immutable_birth BEFORE UPDATE ON runs WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'runs: a row birth is immutable'); END`,
		`CREATE TRIGGER runs_advance BEFORE UPDATE ON runs WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'runs: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER runs_no_delete BEFORE DELETE ON runs WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'runs is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER dispatches_born_by_event BEFORE INSERT ON dispatches WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'dispatches: a row is born by exactly one event'); END`,
		`CREATE TRIGGER dispatches_immutable_birth BEFORE UPDATE ON dispatches WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'dispatches: a row birth is immutable'); END`,
		`CREATE TRIGGER dispatches_advance BEFORE UPDATE ON dispatches WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'dispatches: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER dispatches_no_delete BEFORE DELETE ON dispatches WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'dispatches is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER run_matters_no_delete BEFORE DELETE ON run_matters WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'run_matters is a projection: only a rebuild may clear it'); END`,
	}
}
