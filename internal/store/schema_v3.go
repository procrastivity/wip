package store

// v3 makes Batch ownership and terminal lifecycle explicit. v1 and v2 are
// shipped migrations and remain frozen; this migration rebuilds the dependent
// execution projections so their foreign keys point at the new Batch table.
func v3Statements() []string {
	return []string{
		seedTaxonomy(V3Taxonomy),

		// The old triggers belong to tables that are replaced below. Drop them
		// before the table names are reused.
		`DROP TRIGGER batches_born_by_event`,
		`DROP TRIGGER batches_immutable_birth`,
		`DROP TRIGGER batches_advance`,
		`DROP TRIGGER batches_no_delete`,
		`DROP TRIGGER batch_members_advance`,
		`DROP TRIGGER batch_members_no_delete`,
		`DROP TRIGGER runs_born_by_event`,
		`DROP TRIGGER runs_immutable_birth`,
		`DROP TRIGGER runs_advance`,
		`DROP TRIGGER runs_no_delete`,
		`DROP TRIGGER dispatches_born_by_event`,
		`DROP TRIGGER dispatches_immutable_birth`,
		`DROP TRIGGER dispatches_advance`,
		`DROP TRIGGER dispatches_no_delete`,
		`DROP TRIGGER run_matters_no_delete`,

		// Rename the old dependency roots first. SQLite updates the old child
		// foreign keys to those temporary names, which lets the new tables be
		// created with their final references while the old rows remain intact.
		`DROP INDEX batches_name`,
		`DROP INDEX runs_batch_locator`,
		`DROP INDEX dispatches_open`,
		`DROP INDEX dispatches_open_matter`,
		`ALTER TABLE batches RENAME TO batches_old`,
		`CREATE TABLE batches (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT,
			matter TEXT REFERENCES nodes(id),
			state TEXT NOT NULL CHECK (state IN ('live','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('dismissed','swept')),
			closed_at TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26),
			-- An anonymous Batch carries its Matter — except the legacy P1
			-- shape: an owner-less anonymous row survives only closed and
			-- swept, which is how a pre-run-model refresh artifact reads
			-- after the fact (amended by roles, see rejectLegacyBatches).
			CHECK ((name IS NULL) = (matter IS NOT NULL)
			       OR (name IS NULL AND matter IS NULL AND state='closed' AND close_reason='swept')),
			CHECK (name IS NULL OR length(name) > 0),
			CHECK ((state='closed') = (close_reason IS NOT NULL)),
			CHECK ((state='closed') = (closed_at IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		// Named Batches copy over live; owner-less anonymous ones — legal P1
		// history the preflight admitted precisely because they are memberless
		// and run-less — close as swept at their own birth moment.
		`INSERT INTO batches (id,name,matter,state,close_reason,closed_at,birth_event,last_event)
		 SELECT b.id, b.name, NULL,
		        CASE WHEN b.name IS NULL THEN 'closed' ELSE 'live' END,
		        CASE WHEN b.name IS NULL THEN 'swept' END,
		        CASE WHEN b.name IS NULL THEN (SELECT e.occurred_at FROM events e WHERE e.id = b.birth_event) END,
		        b.birth_event, b.last_event
		 FROM batches_old b`,
		`CREATE UNIQUE INDEX batches_name ON batches(name) WHERE name IS NOT NULL`,
		`CREATE UNIQUE INDEX batches_anonymous_matter ON batches(matter) WHERE matter IS NOT NULL`,

		`ALTER TABLE batch_members RENAME TO batch_members_old`,
		`ALTER TABLE runs RENAME TO runs_old`,
		`ALTER TABLE run_matters RENAME TO run_matters_old`,
		`ALTER TABLE dispatches RENAME TO dispatches_old`,

		`CREATE TABLE batch_members (
			batch TEXT NOT NULL REFERENCES batches(id),
			matter TEXT NOT NULL REFERENCES nodes(id),
			left_event TEXT REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			PRIMARY KEY (batch,matter)
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO batch_members (batch,matter,left_event,last_event)
		 SELECT batch,matter,left_event,last_event FROM batch_members_old`,

		`CREATE TABLE runs (
			id TEXT NOT NULL PRIMARY KEY,
			clone TEXT NOT NULL REFERENCES clones(id),
			batch TEXT NOT NULL REFERENCES batches(id),
			locator TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','stood-down','reaped')),
			started_at TEXT NOT NULL,
			closed_at TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26),
			CHECK ((state='closed') = (close_reason IS NOT NULL)),
			CHECK ((state='closed') = (closed_at IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO runs (id,clone,batch,locator,state,close_reason,started_at,closed_at,birth_event,last_event)
		 SELECT id,clone,batch,locator,state,close_reason,started_at,closed_at,birth_event,last_event FROM runs_old`,
		`CREATE UNIQUE INDEX runs_batch_locator ON runs(batch,locator)`,

		`CREATE TABLE run_matters (
			run TEXT NOT NULL REFERENCES runs(id),
			matter TEXT NOT NULL REFERENCES nodes(id),
			ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
			PRIMARY KEY (run,matter), UNIQUE (run,ordinal)
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO run_matters (run,matter,ordinal)
		 SELECT run,matter,ordinal FROM run_matters_old`,

		`CREATE TABLE dispatches (
			id TEXT NOT NULL PRIMARY KEY,
			clone TEXT NOT NULL REFERENCES clones(id),
			worktree TEXT NOT NULL REFERENCES worktrees(id),
			run TEXT REFERENCES runs(id),
			matter TEXT REFERENCES nodes(id),
			state TEXT NOT NULL CHECK (state IN ('open','closed')),
			close_reason TEXT CHECK (close_reason IS NULL OR close_reason IN ('completed','superseded','reaped')),
			opened_at TEXT NOT NULL,
			closed_at TEXT,
			birth_event TEXT NOT NULL REFERENCES events(id),
			last_event TEXT NOT NULL REFERENCES events(id),
			CHECK (length(id)=26),
			CHECK ((state='closed') = (close_reason IS NOT NULL)),
			CHECK ((state='closed') = (closed_at IS NOT NULL)),
			CHECK ((run IS NULL) = (matter IS NULL))
		) STRICT, WITHOUT ROWID`,
		`INSERT INTO dispatches (id,clone,worktree,run,matter,state,close_reason,opened_at,closed_at,birth_event,last_event)
		 SELECT id,clone,worktree,run,matter,state,close_reason,opened_at,closed_at,birth_event,last_event FROM dispatches_old`,
		`CREATE INDEX dispatches_open ON dispatches(worktree,state)`,
		`CREATE UNIQUE INDEX dispatches_open_matter ON dispatches(matter) WHERE state='open' AND matter IS NOT NULL`,

		`DROP TABLE dispatches_old`,
		`DROP TABLE run_matters_old`,
		`DROP TABLE runs_old`,
		`DROP TABLE batch_members_old`,
		`DROP TABLE batches_old`,

		// Reinstall projection guards for the replacement tables.
		`CREATE TRIGGER batches_born_by_event BEFORE INSERT ON batches WHEN NEW.birth_event <> NEW.last_event BEGIN SELECT RAISE(ABORT, 'batches: a row is born by exactly one event'); END`,
		`CREATE TRIGGER batches_immutable_birth BEFORE UPDATE ON batches WHEN NEW.birth_event <> OLD.birth_event BEGIN SELECT RAISE(ABORT, 'batches: a row birth is immutable'); END`,
		`CREATE TRIGGER batches_advance BEFORE UPDATE ON batches WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'batches: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER batches_no_delete BEFORE DELETE ON batches WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'batches is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
		`CREATE TRIGGER batch_members_advance BEFORE UPDATE ON batch_members WHEN NEW.last_event <= OLD.last_event BEGIN SELECT RAISE(ABORT, 'batch_members: a row may only advance to a newer event'); END`,
		`CREATE TRIGGER batch_members_no_delete BEFORE DELETE ON batch_members WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding') BEGIN SELECT RAISE(ABORT, 'batch_members is a projection: rows are tombstoned, not deleted (D44); only a rebuild may clear it'); END`,
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
