package eventsourced

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"
)

// LatestVersion is the highest numbered migration this spike knows about.
// v1 is the scenario as pinned in docs/store-fork/scenario.md; v2 is the
// migration exercise (nodes gain an optional label).
const LatestVersion = 2

// migration is one numbered, all-or-nothing schema unit. Keeping them as
// separate entries in one ordered slice is the whole point of the exercise:
// the diff between v1 and v2 is meant to be legible as a diff.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations is the ordered register. Never edit a shipped entry; append.
var migrations = []migration{
	// ---------------------------------------------------------------------
	// v1 — the event log is the source of truth; `nodes` is a projection of
	// it and carries no fact that the log does not already carry.
	// ---------------------------------------------------------------------
	{
		version: 1,
		name:    "event-log-and-node-projection",
		stmts: []string{
			// The log. `id` is the event's own ULID and doubles as the
			// total-order key — there is no sequence column (D44, D51).
			// The envelope. Actor, causation and correlation belong to v1 and
			// not to a later migration on purpose: MODEL §10 says later phases
			// add event *types* but never change these columns, so an envelope
			// field arriving as an increment would be exactly the retrofit the
			// contract exists to prevent.
			//
			// causation and correlation are NOT NULL and self-referential for
			// an origin event (the a:a:a form) rather than nullable: "this
			// event began its own chain" and "nobody filled this in" should not
			// be the same value.
			`CREATE TABLE events (
				id          TEXT PRIMARY KEY,
				type        TEXT NOT NULL,
				occurred_at TEXT NOT NULL,
				actor       TEXT NOT NULL,
				causation   TEXT NOT NULL REFERENCES events(id),
				correlation TEXT NOT NULL REFERENCES events(id),
				repo        TEXT,
				clone       TEXT,
				worktree    TEXT,
				subject     TEXT NOT NULL,
				payload     TEXT NOT NULL
			) WITHOUT ROWID`,

			// Chains are walked in both directions: "what did this event
			// cause" and "everything in this command".
			`CREATE INDEX events_causation ON events(causation)`,
			`CREATE INDEX events_correlation ON events(correlation)`,

			// Append-only, enforced by the substrate rather than by Go
			// discipline: no code path in or out of this package can rewrite
			// or drop an event.
			`CREATE TRIGGER events_no_update BEFORE UPDATE ON events
			 BEGIN SELECT RAISE(ABORT, 'events are append-only'); END`,
			`CREATE TRIGGER events_no_delete BEFORE DELETE ON events
			 BEGIN SELECT RAISE(ABORT, 'events are append-only'); END`,

			// The projection. Derived, disposable, rebuildable — see
			// Store.Rebuild. `birth_event` is the ULID of the *.created event
			// that produced the row, so creation order is a column and needs
			// no separate sort key.
			`CREATE TABLE nodes (
				id          TEXT PRIMARY KEY,
				kind        TEXT NOT NULL,
				parent      TEXT REFERENCES nodes(id),
				locator     TEXT NOT NULL,
				title       TEXT NOT NULL,
				lifecycle   TEXT NOT NULL,
				birth_event TEXT NOT NULL
			) WITHOUT ROWID`,
			`CREATE INDEX nodes_lifecycle ON nodes(lifecycle, birth_event)`,
			`CREATE INDEX nodes_parent ON nodes(parent)`,
		},
	},

	// ---------------------------------------------------------------------
	// v2 — the migration exercise. Nodes gain an optional `label`, set after
	// birth by a new verb (Store.Label) emitting a new event type
	// (matter.labeled / step.labeled) and surfaced by a new read path
	// (Store.InProgressLabeled). See label.go for the whole increment.
	//
	// What had to be true about existing data: nothing. Rows born under v1
	// answer with the column default. No backfill, no rewrite, no touching
	// the log.
	// ---------------------------------------------------------------------
	{
		version: 2,
		name:    "node-label",
		stmts: []string{
			`ALTER TABLE nodes ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
		},
	},
}

// migrate brings the database up to `target`, applying each pending migration
// in its own transaction, and returns the version actually reached.
//
// PLAN 1.2's posture is versioned migrations + backup-before-migrate: if the
// database already carries data under an older version, the file is copied
// aside before anything is applied.
func migrate(ctx context.Context, db *sql.DB, path string, target int) (int, error) {
	if target < 1 || target > LatestVersion {
		return 0, fmt.Errorf("eventsourced: target version %d out of range 1..%d", target, LatestVersion)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return 0, fmt.Errorf("eventsourced: migration bookkeeping: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return 0, fmt.Errorf("eventsourced: read schema version: %w", err)
	}
	if current > target {
		return current, fmt.Errorf(
			"eventsourced: database is at schema v%d, refusing to open at v%d (no downgrades)", current, target)
	}

	pending := make([]migration, 0, len(migrations))
	for _, m := range migrations {
		if m.version > current && m.version <= target {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return current, nil
	}
	// Backup-before-migrate, but only when there is something to lose: a
	// database being created from nothing has no prior state worth a copy.
	if current > 0 {
		if err := backup(ctx, db, path, current); err != nil {
			return current, err
		}
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return current, err
		}
		current = m.version
	}
	return current, nil
}

// applyMigration runs one numbered unit atomically.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventsourced: begin migration v%d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("eventsourced: migration v%d (%s) statement %d: %w", m.version, m.name, i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("eventsourced: record migration v%d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventsourced: commit migration v%d: %w", m.version, err)
	}
	return nil
}

// backupPath is where backup() puts the pre-migration copy.
func backupPath(path string, fromVersion int) string {
	return fmt.Sprintf("%s.v%d.bak", path, fromVersion)
}

// backup checkpoints the WAL and copies the database file aside.
//
// Spike-grade: a real implementation would use SQLite's backup API or
// VACUUM INTO so the copy is consistent under concurrent writers. This runs
// at open time before any of this process's writers exist, and wip is
// single-user single-host by refusal (D34), so a checkpoint plus a file copy
// is honest enough to demonstrate the posture.
func backup(ctx context.Context, db *sql.DB, path string, fromVersion int) error {
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("eventsourced: checkpoint before backup: %w", err)
	}
	src, err := os.Open(path) //nolint:gosec // spike; path is the caller's own database
	if err != nil {
		return fmt.Errorf("eventsourced: open database for backup: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(backupPath(path, fromVersion)) //nolint:gosec // spike
	if err != nil {
		return fmt.Errorf("eventsourced: create backup: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("eventsourced: write backup: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("eventsourced: close backup: %w", err)
	}
	return nil
}
