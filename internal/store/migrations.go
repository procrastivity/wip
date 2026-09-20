package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"time"
)

// The store is the only durable artifact wip has, so migrations are
// load-bearing from before first ship (PLAN 1.2). The posture:
//
//   - **versioned, forward-only, numbered, embedded in the binary.** The
//     database records the versions it has applied; at store-open the binary
//     applies every migration newer than that. Downgrades are refused.
//   - **backup before migrate.** Before applying anything to a database that
//     already carries data, the file is copied to a timestamped sidecar, so a
//     failed or regretted migration is recoverable. Cheap insurance for the
//     one artifact that cannot be regenerated.
//   - **a v1 baseline exists** even though v1 has nothing to migrate from, so
//     v2 is an increment and never a retrofit.

// migration is one numbered, all-or-nothing schema unit. Never edit a shipped
// entry; append.
type migration struct {
	version   int
	name      string
	stmts     []string
	preflight func(context.Context, *sql.Tx) error
	// dataMigrate runs after stmts, inside the same transaction, for the rare
	// migration whose data half is not expressible as SQL — v13's
	// synthetic-history backfill (schema_v13.go) is the first and, as of P1,
	// the only one. It sees a *migrationIDs rather than a *Store because no
	// Store exists yet at migration time (Store.NewID needs the floor
	// loadFloor establishes after migrate returns).
	dataMigrate func(context.Context, *sql.Tx, *migrationIDs) error
}

// register is the ordered migration register, embedded in the binary. Its last
// entry is the version a fresh store is created at.
var register = []migration{
	{version: 1, name: "baseline", stmts: v1Statements()},
	{version: 2, name: "run-substrate", stmts: v2Statements(), preflight: rejectLegacyRuns},
	{version: 3, name: "batch-lifecycle", stmts: v3Statements(), preflight: rejectLegacyBatches},
	{version: 4, name: "roles", stmts: v4Statements()},
	{version: 5, name: "tracker-substrate", stmts: v5Statements()},
	{version: 6, name: "tracker-item-created", stmts: v6Statements()},
	{version: 7, name: "outbox-lifecycle", stmts: v7Statements()},
	{version: 8, name: "prospective-gate-declarations", stmts: v8Statements()},
	{version: 9, name: "tracker-state-observed", stmts: v9Statements()},
	{version: 10, name: "backlog-decline-reason", stmts: v10Statements()},
	{version: 11, name: "emergency-gate-dismissal", stmts: v11Statements()},
	{version: 12, name: "backlog-entry-findings", stmts: v12Statements()},
	{version: 13, name: "evented-config", stmts: v13Statements(), dataMigrate: v13SyntheticHistory},
}

// latestVersion is the highest migration this binary carries.
func latestVersion(reg []migration) int {
	if len(reg) == 0 {
		return 0
	}
	return reg[len(reg)-1].version
}

// projectionVersion is the version of the *derivation* — the rules in
// project.go that turn the log into the projection tables. It is deliberately
// distinct from the schema version, because the two change for different
// reasons: a schema change alters the tables, a projection change alters what
// the same log means. Bumping it makes every existing store rebuild its
// projection at the next open, which is exactly the affordance the event-sourced
// shape buys and the tables shape cannot offer.
//
// It deliberately did not move when v13 made config, gate declarations and gate
// exemptions projections: a bump refolds every existing store at its next open,
// and until the migration that gives those rows synthetic history there is no
// event behind the ones a store already carries. The bump belongs with that
// migration — this one. v13's dataMigrate (schema_v13.go) mints the events
// first, in the same transaction as the schema statements; this bump is what
// makes them take effect, because nothing here folds them into config,
// gate_declarations or gate_exemptions directly (those tables already hold the
// rows being explained). The very next ensureProjectionVersion call, still
// inside the same Open, sees an outdated projection, clears all three and
// refolds the whole log — the synthetic events included — which is where
// "reproduces state identical to the pre-migration rows" actually happens
// (see the migration test).
const projectionVersion = 11

// projectionVersionKey is where the store records the projection version it was
// last folded under.
const projectionVersionKey = "projection_version"

// migrate brings the database at path up to the register's latest version and
// returns the version reached. clock feeds a dataMigrate step's *migrationIDs
// (see appendMigrationEvent's doc) — production always passes time.Now via
// openAt; OpenWithClock's test seam reaches it the same way it reaches the
// live id source.
func migrate(ctx context.Context, db *sql.DB, path string, reg []migration, target int, clock func() time.Time) (int, error) {
	if target < 1 || target > latestVersion(reg) {
		return 0, fmt.Errorf("store: target schema version %d out of range 1..%d", target, latestVersion(reg))
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return 0, fmt.Errorf("store: migration bookkeeping: %w", err)
	}

	current, err := schemaVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	if current > target {
		return current, fmt.Errorf(
			"store: database is at schema v%d and this wip understands v%d; forward-only migrations mean there is no downgrade path",
			current, target)
	}

	var pending []migration
	for _, m := range reg {
		if m.version > current && m.version <= target {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return current, nil
	}

	// A v1-to-v3 open must reject an unconvertible anonymous Batch before v2
	// can commit. This read-only preflight spans the pending migration chain;
	// v3's own preflight remains the guard for a v2-to-v3 open.
	if target >= 3 && current >= 1 && current < 3 {
		check, err := db.BeginTx(ctx, nil)
		if err != nil {
			return current, fmt.Errorf("store: migration v3 preflight: %w", err)
		}
		err = rejectLegacyBatches(ctx, check)
		_ = check.Rollback()
		if err != nil {
			return current, fmt.Errorf("store: migration v3 (batch-lifecycle) preflight: %w", err)
		}
	}

	// Backup before migrate, but only when there is something to lose: a
	// database being created from nothing has no prior state worth a copy.
	if current > 0 {
		if _, err := backup(ctx, db, path, current); err != nil {
			return current, err
		}
	}

	for _, m := range pending {
		if err := applyMigration(ctx, db, m, clock); err != nil {
			return current, err
		}
		current = m.version
	}
	return current, nil
}

// schemaVersion reads the highest migration the database has applied. The
// database recording its own version is what lets any binary decide, without
// configuration, whether it has work to do at open.
func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

// applyMigration runs one numbered unit atomically: either the whole version
// lands or none of it does. dataMigrate shares that all-or-nothing property
// with stmts by running inside the same transaction: a row it cannot honestly
// turn into an event aborts the whole migration rather than committing a
// truncated log (schema_v2.go's rejectLegacyRuns/rejectLegacyBatches
// precedent — refuse, never drop).
func applyMigration(ctx context.Context, db *sql.DB, m migration, clock func() time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration v%d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if m.preflight != nil {
		if err := m.preflight(ctx, tx); err != nil {
			return fmt.Errorf("store: migration v%d (%s) preflight: %w", m.version, m.name, err)
		}
	}
	for i, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: migration v%d (%s) statement %d: %w", m.version, m.name, i+1, err)
		}
	}
	if m.dataMigrate != nil {
		// After stmts, not before: a type this migration's own taxonomy seed
		// just added (seedTaxonomy in stmts) has to already be an event_types
		// row before any event naming it can be appended (events.type's FK).
		ids, err := newMigrationIDs(ctx, tx, clock)
		if err != nil {
			return fmt.Errorf("store: migration v%d (%s) data backfill: %w", m.version, m.name, err)
		}
		if err := m.dataMigrate(ctx, tx, ids); err != nil {
			return fmt.Errorf("store: migration v%d (%s) data backfill: %w", m.version, m.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(timestampLayout)); err != nil {
		return fmt.Errorf("store: record migration v%d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration v%d: %w", m.version, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Minting events before a Store exists
// ---------------------------------------------------------------------------

// migrationIDs mints monotonic event identities for a migration's data
// backfill — Store.NewID's own floor-and-retry discipline (store.go), repeated
// here because no Store exists yet to own it: a Store is not constructed until
// migrate returns, and NewID's floor comes from loadFloor, which runs after
// that.
type migrationIDs struct {
	src   *idSource
	floor string
}

// newMigrationIDs seeds a migrationIDs from the log's own high-water mark
// inside the migration's transaction, so a synthetic event can never sort
// below the history already on disk (events_monotonic, D44, D51).
func newMigrationIDs(ctx context.Context, tx *sql.Tx, clock func() time.Time) (*migrationIDs, error) {
	var floor sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&floor); err != nil {
		return nil, fmt.Errorf("store: read the identity high-water mark for migration: %w", err)
	}
	ids := &migrationIDs{src: newIDSourceWithClock(clock)}
	if floor.Valid {
		ids.floor = floor.String
	}
	return ids, nil
}

// next mints the next identity, strictly above everything minted so far in
// this migration or already on disk.
func (m *migrationIDs) next() string {
	for {
		id := m.src.next()
		if id > m.floor {
			m.floor = id
			return id
		}
		// Store.NewID's own comment applies verbatim: the floor's timestamp
		// cannot be ahead of now by more than clock skew, so one tick clears it.
		time.Sleep(time.Millisecond)
	}
}

// appendMigrationEvent stamps and appends one synthetic, self-originating
// event on a migration's behalf: its own id is its causation and correlation,
// because a migration reconstructing history did not chain off any command the
// log records (contrast Store.stamp, which a live command's Commit uses and
// which chains against the rest of that command's drafts).
//
// Store.Commit remains the only append path a live command reaches — this one
// exists solely because a migration runs before any Store does, so nothing
// else in this package can append on its behalf. It skips stamp's dimension
// lookup because every caller today is a durable, Repo-only v13 type; a future
// dataMigrate that needs the general rule should route through
// requiredDimensions instead of assuming this shape.
func appendMigrationEvent(ctx context.Context, tx *sql.Tx, ids *migrationIDs, actor Actor, typ, repo, subject string, payload any) (Event, error) {
	if len(subject) != IDLen {
		return Event{}, fmt.Errorf("store: migration event %s names subject %q, which is not an identity", typ, subject)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("store: marshal migration payload for %s: %w", typ, err)
	}
	id := ids.next()
	occurredAt, err := timeOfID(id)
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		ID:         id,
		Type:       typ,
		OccurredAt: occurredAt,
		Actor:      actor,
		Repo:       repo,
		Subject:    subject,
		Payload:    raw,
	}
	ev.Causation, ev.Correlation = ev.ID, ev.ID
	if err := appendEvent(ctx, tx, ev); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// clock is time.Now, indirected the way paths.go indirects os.Hostname: the
// sidecar's name is part of the contract, and a test that had to race the wall
// clock to say anything about two backups landing in the same instant would be a
// test that sometimes lied.
var clock = time.Now

// backupPath is where backup() puts the pre-migration copy: the store's own
// path plus the version it is leaving and the moment it left, so a directory
// listing reads as a history rather than as a pile of `.bak` files.
func backupPath(path string, fromVersion int, at time.Time) string {
	return fmt.Sprintf("%s.bak-%d-%s", path, fromVersion, at.UTC().Format(time.RFC3339))
}

// backupCollisions bounds the disambiguation in backup(). The name carries a
// timestamp to the second, so reaching this many collisions would mean a
// pathological number of opens inside one second, each with a migration pending.
const backupCollisions = 64

// backup checkpoints the WAL and copies the database aside, returning the path
// written.
//
// A plain copy is safe here for a reason rather than by luck: it runs at open
// time, before this process has any writer, and wip is single-user single-host
// by refusal (D34) with one connection per store. VACUUM INTO would be the
// answer if either of those stopped being true.
func backup(ctx context.Context, db *sql.DB, path string, fromVersion int) (string, error) {
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("store: checkpoint before backup: %w", err)
	}

	src, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store: open database for backup: %w", err)
	}
	defer func() { _ = src.Close() }()

	// O_EXCL always, and a name that steps aside when it is taken. Two rules
	// pulling against each other, both load-bearing: a backup that overwrote the
	// one already there would destroy exactly what it exists to preserve, and an
	// open that refused because the name was taken would make "wip cannot open
	// your store until you delete your only backup" a thing this framework says.
	// The collision is not hypothetical — it is the retry after a migration that
	// failed a moment ago, which is the open that most needs its own copy.
	base := backupPath(path, fromVersion, clock())
	dst := base
	var out *os.File
	for n := 2; ; n++ {
		out, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || n > backupCollisions {
			return "", fmt.Errorf("store: create backup %s: %w", dst, err)
		}
		dst = fmt.Sprintf("%s-%d", base, n)
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return "", fmt.Errorf("store: write backup %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("store: close backup %s: %w", dst, err)
	}
	return dst, nil
}

// ensureProjectionVersion folds the log again if the projection this database
// carries was built by an older set of rules than this binary's.
//
// This is the affordance D61 bought: the projection holds no fact the log does
// not, so a change to the derivation is a rebuild rather than a data migration
// with a backfill over live primary data.
func (s *Store) ensureProjectionVersion(ctx context.Context) error {
	recorded, present, err := s.meta(ctx, projectionVersionKey)
	if err != nil {
		return err
	}
	if !present {
		// A store being born: its projection is current by definition.
		return s.setMeta(ctx, projectionVersionKey, fmt.Sprint(projectionVersion))
	}
	// strconv.Atoi and not a scan: a scan accepts a leading number and discards
	// whatever follows it, so a garbled `1x` would read as 1 and the store would
	// decide it had nothing to refold. Silently skipping a rebuild the store needs
	// is the one wrong answer this branch can give.
	have, err := strconv.Atoi(recorded)
	if err != nil {
		return fmt.Errorf("store: %s records an unreadable projection version %q", projectionVersionKey, recorded)
	}
	switch {
	case have == projectionVersion:
		return nil
	case have > projectionVersion:
		return fmt.Errorf(
			"store: this store's projection was built by a newer wip (v%d; this one folds v%d)", have, projectionVersion)
	default:
		if err := s.Rebuild(ctx); err != nil {
			return fmt.Errorf("store: rebuilding the projection from v%d to v%d: %w", have, projectionVersion, err)
		}
		return s.setMeta(ctx, projectionVersionKey, fmt.Sprint(projectionVersion))
	}
}

// meta reads one store_meta value.
func (s *Store) meta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read %s: %w", key, err)
	}
	return v, true, nil
}

// setMeta writes one store_meta value.
func (s *Store) setMeta(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO store_meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("store: write %s: %w", key, err)
	}
	return nil
}
