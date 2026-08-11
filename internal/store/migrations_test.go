package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for the migration framework (step-09): versioned, forward-only,
// numbered migrations embedded in the binary, a database that records the
// versions it has applied, and a backup taken before anything is applied to a
// store that has something to lose.
//
// Two things make this file different from every other one here.
//
// The first is that it tests the *open path* and not the write path. Nothing
// below drives a verb; what is asserted is what happens to a database between
// one process closing it and the next one opening it, which is the only place a
// migration exists at all.
//
// The second is the oracle, and it is the whole point. "The migration applied"
// is trivially true of a framework that ran a migration twice, ran two of them
// out of order, re-ran the baseline underneath, skipped one and recorded it
// anyway, or applied everything to a store whose data it quietly dropped on the
// way — every one of those returns no error. So the assertions are comparative:
// the schema a store reaches by migrating is compared against the schema of a
// store built at that version directly, and everything the store held before is
// compared, column for column, against what it holds after.

// ---------------------------------------------------------------------------
// The synthetic register
// ---------------------------------------------------------------------------

// The migrations below exist only inside this file. `openAt` takes an injectable
// register precisely so a v2 can exist for the length of a test, and shipping a
// real v2 to make the framework testable would put a migration in the product to
// serve a test — which is the retrofit the v1 baseline exists to prevent.
var (
	// syntheticV3 is schema-only: a new table and an index on an existing one.
	// Product v2 owns the slot immediately after the frozen baseline; the
	// framework fixtures therefore start at v3.
	syntheticV3 = migration{version: 7, name: "annotations", stmts: []string{
		`CREATE TABLE annotations (
			id   TEXT NOT NULL PRIMARY KEY,
			node TEXT NOT NULL REFERENCES nodes(id),
			note TEXT NOT NULL
		) STRICT, WITHOUT ROWID`,
		`CREATE INDEX nodes_title ON nodes(title)`,
	}}

	// syntheticV4 alters a populated table, which is the increment a schema-only
	// one cannot stand in for: every node row already in the store has to come
	// through it, and come out answering the same questions.
	syntheticV4 = migration{version: 8, name: "node-review-note", stmts: []string{
		`ALTER TABLE nodes ADD COLUMN review_note TEXT`,
	}}

	// brokenV3 lands its first statement and then fails. That shape is the only
	// one that says anything about all-or-nothing: a migration failing on its
	// first statement would leave nothing behind whether or not there were a
	// transaction around it.
	brokenV3 = migration{version: 7, name: "half-applied", stmts: []string{
		`CREATE TABLE half_applied (id TEXT NOT NULL PRIMARY KEY) STRICT, WITHOUT ROWID`,
		`ALTER TABLE no_such_table ADD COLUMN nothing TEXT`,
	}}

	// brokenV4 is the same failure one version later, so the *granularity* of
	// all-or-nothing can be pinned: it is per migration, not per open.
	brokenV4 = migration{version: 8, name: "no-such-table", stmts: []string{
		`ALTER TABLE no_such_table ADD COLUMN nothing TEXT`,
	}}
)

// shipped is the binary's own register, copied — so appending a synthetic
// version to a test's register can never reach the real one through a shared
// backing array.
func shipped() []migration { return append([]migration{}, register...) }

// baselineRegister is the frozen v1 fixture used when a test must model a
// pre-v2 store. It is deliberately separate from shipped: current product code
// must never pretend that v1 is the shipped register.
func baselineRegister() []migration { return append([]migration{}, register[:1]...) }

// through is the shipped register plus synthetic versions after product v2.
func through(extra ...migration) []migration { return append(shipped(), extra...) }

// ---------------------------------------------------------------------------
// Reading a store from outside
// ---------------------------------------------------------------------------

// backupsIn lists the migration sidecars beside a store, in name order — which
// is also chronological order, because the name carries the moment.
func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wip.db.bak-") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// backupNameShape is the sidecar name the Brief fixes: the store's own file, the
// version it is leaving, and the moment it left.
var backupNameShape = regexp.MustCompile(`^wip\.db\.bak-(\d+)-(.+)$`)

// wantBackupName holds a sidecar to that shape.
//
// The name is most of the affordance. A person recovering from a migration they
// regret reads a directory listing, and "which version was this, and when" has to
// be in it — otherwise the backup is a file they have to guess about, which is
// not insurance.
func wantBackupName(t *testing.T, name string, fromVersion int, notBefore time.Time) {
	t.Helper()
	m := backupNameShape.FindStringSubmatch(name)
	if m == nil {
		t.Errorf("the sidecar is named %q, want wip.db.bak-<from-version>-<rfc3339>", name)
		return
	}
	if got, want := m[1], strconv.Itoa(fromVersion); got != want {
		t.Errorf("the sidecar %q says it was taken leaving v%s, want v%s", name, got, want)
	}
	if !strings.HasSuffix(m[2], "Z") {
		t.Errorf("the sidecar %q is not stamped in UTC", name)
	}
	at, err := time.Parse(time.RFC3339, m[2])
	if err != nil {
		t.Errorf("the sidecar %q carries %q, which is not RFC3339: %v", name, m[2], err)
		return
	}
	if at.Before(notBefore.Add(-time.Minute)) || at.After(time.Now().Add(time.Minute)) {
		t.Errorf("the sidecar %q is stamped %s, which is nowhere near when it was taken", name, at)
	}
}

// schemaShape is the whole schema as the database reports it about itself: every
// table, index, trigger and view with the SQL that created it, and the columns
// each table actually ended up with.
//
// This is the oracle "it applied, didn't it" is not. Comparing a store that
// migrated to v2 against one built at v2 directly is a claim no bookkeeping
// mistake survives — a skipped migration, a doubled one, a pair applied out of
// order, a re-run baseline — because none of them leave the same schema.
func schemaShape(t *testing.T, s *Store) []string {
	t.Helper()
	ctx := context.Background()

	var shape, tables []string
	rows, err := s.db.QueryContext(ctx,
		`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	for rows.Next() {
		var kind, name, table, ddl string
		if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
			_ = rows.Close()
			t.Fatalf("read the schema: %v", err)
		}
		shape = append(shape, fmt.Sprintf("%s %s on %s: %s", kind, name, table, strings.Join(strings.Fields(ddl), " ")))
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}

	// The column list separately, because ALTER TABLE ADD COLUMN is the one
	// change a later migration is most likely to make and the DDL text is not the
	// most readable way to notice it went missing.
	for _, table := range tables {
		cols, err := s.db.QueryContext(ctx,
			`SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk
			   FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatalf("read the columns of %s: %v", table, err)
		}
		for cols.Next() {
			var name, kind, dflt string
			var notNull, pk int
			if err := cols.Scan(&name, &kind, &notNull, &dflt, &pk); err != nil {
				_ = cols.Close()
				t.Fatalf("read the columns of %s: %v", table, err)
			}
			shape = append(shape, fmt.Sprintf("column %s.%s %s notnull=%d default=%q pk=%d",
				table, name, kind, notNull, dflt, pk))
		}
		err = cols.Err()
		_ = cols.Close()
		if err != nil {
			t.Fatalf("read the columns of %s: %v", table, err)
		}
	}
	sort.Strings(shape)
	return shape
}

// wantSameSchema asserts two schemas are the same object for object.
func wantSameSchema(t *testing.T, what string, got, want []string) {
	t.Helper()
	index := func(lines []string) map[string]bool {
		m := make(map[string]bool, len(lines))
		for _, l := range lines {
			m[l] = true
		}
		return m
	}
	inGot, inWant := index(got), index(want)
	for _, l := range want {
		if !inGot[l] {
			t.Errorf("%s: the schema is missing %s", what, l)
		}
	}
	for _, l := range got {
		if !inWant[l] {
			t.Errorf("%s: the schema carries %s, which the one it is compared against does not", what, l)
		}
	}
}

// wantSameRows asserts a table's rows came through unchanged, over every column
// the earlier read carried.
//
// It compares the database's own rendering of each value, so NULL, the empty
// string and the four characters "NULL" stay three different answers — which
// matters for the log, where three of eleven columns are nullable by rule.
func wantSameRows(t *testing.T, what string, before, after []map[string]string) {
	t.Helper()
	if len(after) != len(before) {
		t.Errorf("%s: %d rows, was %d", what, len(after), len(before))
		return
	}
	for i := range before {
		for col, was := range before[i] {
			got, present := after[i][col]
			switch {
			case !present:
				t.Errorf("%s: row %d has no column %s any more", what, i, col)
			case got != was:
				t.Errorf("%s: row %d column %s is %s, was %s", what, i, col, got, was)
			}
		}
	}
}

// restore puts a backup back over the database it was taken from — the recovery
// a person with a store they regret migrating actually performs.
//
// The write-ahead log goes with it: it belongs to the file being replaced and
// not to the one arriving.
func restore(t *testing.T, from, to string) {
	t.Helper()
	for _, stale := range []string{to + "-wal", to + "-shm"} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			t.Fatalf("clear %s: %v", stale, err)
		}
	}
	src, err := os.Open(from)
	if err != nil {
		t.Fatalf("open the backup: %v", err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Create(to)
	if err != nil {
		t.Fatalf("open the store for restore: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatalf("restore the backup: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("restore the backup: %v", err)
	}
}

// setMetaAround writes one store_meta value with the store closed.
//
// An open that is refused is refused before there is a Store handle to write
// through, so the only way to say what such a refusal left behind is to put the
// store back into a state it will open from — from outside.
func setMetaAround(t *testing.T, path, key, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open %s around the API: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(
		`INSERT INTO store_meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		t.Fatalf("write %s around the API: %v", key, err)
	}
}

// wantMigrationLedger asserts the database's own record of what it has applied:
// one row per version, in order, each naming the migration that produced it and
// stamped when it did.
func wantMigrationLedger(h *harness, what string, want []migration) {
	h.t.Helper()
	rows, err := h.db.QueryContext(h.ctx,
		`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		h.t.Fatalf("%s: read schema_migrations: %v", what, err)
	}
	defer func() { _ = rows.Close() }()

	var got []migration
	var stamps []string
	for rows.Next() {
		var m migration
		var appliedAt string
		if err := rows.Scan(&m.version, &m.name, &appliedAt); err != nil {
			h.t.Fatalf("%s: read schema_migrations: %v", what, err)
		}
		got = append(got, m)
		stamps = append(stamps, appliedAt)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("%s: read schema_migrations: %v", what, err)
	}

	if len(got) != len(want) {
		h.t.Errorf("%s: the store records %d migrations, want %d", what, len(got), len(want))
		return
	}
	for i := range want {
		if got[i].version != want[i].version || got[i].name != want[i].name {
			h.t.Errorf("%s: migration %d is v%d (%s), want v%d (%s)",
				what, i, got[i].version, got[i].name, want[i].version, want[i].name)
		}
	}
	// Fixed-width UTC timestamps, so lexical order is chronological order: a
	// ledger whose stamps went backwards would be one written out of order.
	for i := 1; i < len(stamps); i++ {
		if stamps[i] < stamps[i-1] {
			h.t.Errorf("%s: v%d was stamped %s, before v%d's %s",
				what, got[i].version, stamps[i], got[i-1].version, stamps[i-1])
		}
	}
}

// wantSameProjectionThroughMigration compares the v1 columns that existed
// before the migration. v2 adds nullable Dispatch columns and new Run tables;
// those additions are checked by schemaShape and must not look like data loss.
func wantSameProjectionThroughMigration(t *testing.T, what string, before, after projectionSnapshot) {
	t.Helper()
	for table, rows := range before {
		got := after[table]
		if len(got) != len(rows) {
			t.Errorf("%s: %s has %d rows, was %d", what, table, len(got), len(rows))
			continue
		}
		for i, row := range rows {
			for column, want := range row {
				if got[i][column] != want {
					t.Errorf("%s: %s row %d column %s is %s, was %s", what, table, i, column, got[i][column], want)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 1. A synthetic v1 -> v2, end to end
// ---------------------------------------------------------------------------

// TestASyntheticMigrationBacksUpAppliesAndKeepsEverything is the Step's central
// assertion, and it is four claims rather than one: a backup was written and
// named the way the Brief fixes it, the migration was applied and recorded, the
// data that was there is still there and still says the same thing, and the log —
// the source of truth under D61 — was not touched at all.
//
// The last claim is the one a migration framework is most likely to break and
// least likely to be caught breaking, because a schema change that silently
// rewrote the log would still leave a store that opens.
func TestASyntheticMigrationBacksUpAppliesAndKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	if got := h.SchemaVersion(); got != 1 {
		t.Fatalf("the store was built at v%d, want v1", got)
	}

	before := h.snapshotProjection()
	h.wantProjectionIsPopulated(before)
	log := h.rowsOf("events", "")
	if len(log) < 60 {
		t.Fatalf("the fixture wrote %d events; a migration test wants a store worth migrating", len(log))
	}
	logColumns := h.columnsOf("events")

	// A store that was created rather than migrated has nothing to lose, so it
	// takes no copy. This is also the control for the sidecar assertion below.
	if got := backupsIn(t, dir); len(got) != 0 {
		t.Fatalf("creating a store wrote %v; a backup is for a migration", got)
	}

	at := time.Now()
	migrated, err := h.reopen(through(syntheticV3), 7)
	if err != nil {
		t.Fatalf("reopen under v2: %v", err)
	}

	// --- the migration was applied, and the store records it -----------------
	if got := migrated.SchemaVersion(); got != 7 {
		t.Errorf("the reopened store reports v%d, want v7", got)
	}
	wantMigrationLedger(migrated, "after v1 -> v7", through(syntheticV3))
	if got := len(migrated.rowsOf("sqlite_master", "name = 'annotations'")); got != 1 {
		t.Errorf("the v2 table is not in the schema after a migration to v2")
	}

	// --- a backup was written, and named as the Brief fixes it ---------------
	sidecars := backupsIn(t, dir)
	if len(sidecars) != 1 {
		t.Fatalf("migrating v1 -> v2 left %v, want exactly one sidecar", sidecars)
	}
	wantBackupName(t, sidecars[0], 1, at)

	// --- everything that was in the store is still in it ---------------------
	wantSameProjectionThroughMigration(t, "after migrating v1 -> v2", before, migrated.snapshotProjection())

	// --- and the log is untouched, byte for byte, column for column ----------
	if got := migrated.columnsOf("events"); !reflect.DeepEqual(got, logColumns) {
		t.Errorf("the envelope is %v after a migration, was %v", got, logColumns)
	}
	wantSameRows(t, "the log after a migration", log, migrated.rowsOf("events", ""))

	// --- the oracle ----------------------------------------------------------
	// A store that got to v2 by migrating is indistinguishable from one built at
	// v2 directly. Every assertion above is satisfied by a framework that also
	// did something else; this one is not.
	direct := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), through(syntheticV3), 7)
	wantSameSchema(t, "a store migrated v1 -> v2 against one built at v2",
		schemaShape(t, migrated.Store), schemaShape(t, direct.Store))
}

// ---------------------------------------------------------------------------
// 2. An open with nothing to migrate
// ---------------------------------------------------------------------------

// TestAnOpenWithNothingToMigrateTouchesNothing is the other half of the seal
// condition's "backup-before-migrate", and it is asserted by absence: no
// sidecar, and — the stronger claim — no migration re-applied.
//
// "Nothing broke" is not the assertion. A framework that re-ran every migration
// at every open would leave a store that still works and a `schema_migrations`
// row whose `applied_at` moved, so that is what is checked.
func TestAnOpenWithNothingToMigrateTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")
	latest := latestVersion(shipped())

	h := newHarnessAt(t, path, shipped(), latest)
	richHistory(h)
	before := h.snapshotProjection()
	log := h.rowsOf("events", "")
	ledger := h.rowsOf("schema_migrations", "")

	again, err := h.reopen(shipped(), latest)
	if err != nil {
		t.Fatalf("reopen a store that is already current: %v", err)
	}

	if got := backupsIn(t, dir); len(got) != 0 {
		t.Errorf("an open with nothing to migrate wrote %v; a backup is for a migration, not for an open", got)
	}
	if got := again.SchemaVersion(); got != latest {
		t.Errorf("the reopened store reports v%d, want v%d", got, latest)
	}
	wantSameRows(t, "schema_migrations after a no-op open", ledger, again.rowsOf("schema_migrations", ""))
	again.wantSameProjection("after a no-op open", before, again.snapshotProjection())
	wantSameRows(t, "the log after a no-op open", log, again.rowsOf("events", ""))

	// The target pins the register, not the current product binary. Exercise
	// that boundary against a separate store that is genuinely still at v1.
	pinnedDir := t.TempDir()
	pinned := newHarnessAt(t, filepath.Join(pinnedDir, "wip.db"), baselineRegister(), 1)
	if got := pinned.SchemaVersion(); got != 1 {
		t.Errorf("an open targeting v1 reached v%d", got)
	}
	if got := backupsIn(t, pinnedDir); len(got) != 0 {
		t.Errorf("an open targeting v1 wrote %v", got)
	}
}

// ---------------------------------------------------------------------------
// 3. The downgrade refusal
// ---------------------------------------------------------------------------

// TestAStoreFromANewerBinaryIsRefusedAndNotDowngraded is what forward-only means
// when it is a person's actual situation: two binaries, one store, the older one
// second.
//
// The refusal has to be the whole of it. A downgrade path would have to guess
// what to do with data written under rules this binary does not have, and the
// store is the one artifact that cannot be regenerated — so nothing is dropped,
// nothing is copied aside, and the open simply does not happen.
func TestAStoreFromANewerBinaryIsRefusedAndNotDowngraded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, through(syntheticV3), 7)
	richHistory(h)
	before := h.snapshotProjection()
	log := h.rowsOf("events", "")
	shape := schemaShape(t, h.Store)

	_, err := h.reopen(shipped(), latestVersion(shipped()))
	refusalMentions(t, "a v2 store opened by a binary that understands v1", err, "no downgrade path")

	// Open is exactly that binary: the shipped register, its own latest version.
	_, err = Open(path)
	refusalMentions(t, "Open on a store built by a newer wip", err, "no downgrade path")

	// A refused open changed nothing — including taking no backup, which would be
	// the tempting thing to do and would mean two refused opens leave two copies of
	// a store nobody migrated.
	if got := backupsIn(t, dir); len(got) != 0 {
		t.Errorf("a refused downgrade wrote %v", got)
	}

	sound, err := h.reopen(through(syntheticV3), 7)
	if err != nil {
		t.Fatalf("reopen under v2 after two refused opens: %v", err)
	}
	if got := sound.SchemaVersion(); got != 7 {
		t.Errorf("the store is at v%d after two refused opens, want v7", got)
	}
	wantSameSchema(t, "after two refused downgrades", schemaShape(t, sound.Store), shape)
	sound.wantSameProjection("after two refused downgrades", before, sound.snapshotProjection())
	wantSameRows(t, "the log after two refused downgrades", log, sound.rowsOf("events", ""))
}

// ---------------------------------------------------------------------------
// 4. All-or-nothing
// ---------------------------------------------------------------------------

// TestAMigrationThatFailsPartwayLeavesNothingBehind is where "it applied, didn't
// it" lies to you most directly.
//
// The failing v2 lands a table and then fails, so a framework with no transaction
// around a migration would leave a store that still opens, still reports v1, and
// carries half a schema nobody can see. The assertion is therefore not that the
// open was refused — it is that the store afterwards is object-for-object,
// row-for-row the store from before, with the backup beside it to prove the
// framework thought so too.
func TestAMigrationThatFailsPartwayLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	before := h.snapshotProjection()
	log := h.rowsOf("events", "")
	at := time.Now()
	_, err := h.reopen(through(brokenV3), 7)
	refusalMentions(t, "a synthetic migration whose second statement names no table", err, "migration v7")

	// The backup was taken before anything was attempted, which is the only order
	// in which it is worth anything.
	sidecars := backupsIn(t, dir)
	if len(sidecars) != 1 {
		t.Fatalf("a failed migration left %v, want exactly one sidecar", sidecars)
	}
	wantBackupName(t, sidecars[0], 1, at)

	after, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen at v4 after a failed v5: %v", err)
	}
	if got := after.SchemaVersion(); got != latestVersion(shipped()) {
		t.Errorf("the store reports v%d after a failed v7, want v%d", got, latestVersion(shipped()))
	}
	// The first statement of the failed v3 left with its transaction, while the
	// earlier product v2 remains applied as its own migration unit.
	if got := len(after.rowsOf("sqlite_master", "name = 'half_applied'")); got != 0 {
		t.Errorf("the table the failed migration's first statement created is still in the schema")
	}
	directV2 := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), shipped(), latestVersion(shipped()))
	wantSameSchema(t, "after a v7 that failed partway", schemaShape(t, after.Store), schemaShape(t, directV2.Store))
	wantMigrationLedger(after, "after a v7 that failed", shipped())
	wantSameRows(t, "the log after a v3 that failed", log, after.rowsOf("events", ""))
	wantSameProjectionThroughMigration(t, "after a v3 that failed", before, after.snapshotProjection())
}

// TestAllOrNothingIsPerMigrationAndNotPerOpen states the granularity, because the
// two are easy to conflate and only one of them is true.
//
// One open may apply several versions. If a later one fails, the earlier ones
// stay applied and stay recorded — which is correct, because each is a numbered
// unit that either landed or did not, and re-running an applied migration is
// exactly what forward-only migrations must never do.
func TestAllOrNothingIsPerMigrationAndNotPerOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")
	reg := through(syntheticV3, brokenV4)

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	before := h.snapshotProjection()

	_, err := h.reopen(reg, 8)
	refusalMentions(t, "a v8 that names no table", err, "migration v8")

	// v5 landed and stayed; v6 did not.
	stopped, err := h.reopen(reg, 7)
	if err != nil {
		t.Fatalf("reopen at v5 after a failed v6: %v", err)
	}
	if got := stopped.SchemaVersion(); got != 7 {
		t.Errorf("the store is at v%d after v7 succeeded and v8 failed, want v7", got)
	}
	wantMigrationLedger(stopped, "after v7 succeeded and v8 failed", through(syntheticV3))
	if got := len(stopped.rowsOf("sqlite_master", "name = 'annotations'")); got != 1 {
		t.Errorf("v2 was rolled back by v3's failure; each numbered unit stands alone")
	}
	wantSameProjectionThroughMigration(t, "after v3 succeeded and v4 failed", before, stopped.snapshotProjection())

	// Exactly one backup, for the one open that had migrations to run. The
	// refused open that follows it takes none, because it has nothing pending it
	// has not already copied.
	if got := backupsIn(t, dir); len(got) != 1 {
		t.Errorf("two opens with pending migrations left %v, want one sidecar per open that migrated", got)
	}
}

// TestTheBackupAFailedMigrationLeftRestoresTheStore is the claim the sidecar is
// actually for, and the one every other assertion here stops short of: the file
// is not merely present and plausibly named, it is the pre-migration store and
// putting it back gets that store back.
//
// A backup taken after the migration, taken from the wrong file, taken while a
// write-ahead log held half the content, or truncated on the way out would
// satisfy "a sidecar exists" and fail here.
func TestTheBackupAFailedMigrationLeftRestoresTheStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	before := h.snapshotProjection()
	h.wantProjectionIsPopulated(before)
	log := h.rowsOf("events", "")
	shape := schemaShape(t, h.Store)

	_, err := h.reopen(through(brokenV3), 7)
	refusalMentions(t, "a synthetic migration that fails partway", err, "migration v7")

	sidecars := backupsIn(t, dir)
	if len(sidecars) != 1 {
		t.Fatalf("a failed migration left %v, want exactly one sidecar", sidecars)
	}
	restore(t, filepath.Join(dir, sidecars[0]), path)

	restored, err := h.reopen(baselineRegister(), 1)
	if err != nil {
		t.Fatalf("open the restored backup: %v", err)
	}
	if got := restored.SchemaVersion(); got != 1 {
		t.Errorf("the restored store is at v%d, want the v1 it was copied at", got)
	}
	wantSameSchema(t, "the restored backup", schemaShape(t, restored.Store), shape)
	wantSameRows(t, "the log in the restored backup", log, restored.rowsOf("events", ""))
	restored.wantSameProjection("the restored backup", before, restored.snapshotProjection())
}

// TestABackupNeverOverwritesTheOneAlreadyThere is the collision, and it is not a
// contrived one: the open that collides is the retry after a migration that
// failed a moment ago, which is the one open that most needs its own copy and the
// one most likely to land in the same second as the copy already on disk.
//
// Two things have to hold at once, and they pull against each other. The sidecar
// already there is never overwritten — it is the only copy of the pre-migration
// state, and losing it to a retry would defeat the whole affordance. And the
// store still opens, because "wip cannot open your store until you delete your
// only backup" is the worst sentence this framework could produce.
//
// The clock is pinned so the collision is the subject rather than a race the test
// has to win.
func TestABackupNeverOverwritesTheOneAlreadyThere(t *testing.T) {
	pinned := time.Date(2026, 7, 29, 21, 5, 0, 0, time.UTC)
	was := clock
	clock = func() time.Time { return pinned }
	t.Cleanup(func() { clock = was })

	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	log := h.rowsOf("events", "")

	_, err := h.reopen(through(brokenV3), 7)
	refusalMentions(t, "a synthetic migration that fails partway", err, "migration v7")

	first := backupsIn(t, dir)
	if len(first) != 1 {
		t.Fatalf("a failed migration left %v, want exactly one sidecar", first)
	}
	wantBackupName(t, first[0], 1, pinned)
	kept, err := os.ReadFile(filepath.Join(dir, first[0]))
	if err != nil {
		t.Fatalf("read the sidecar: %v", err)
	}

	// The retry: a binary whose v2 works, run against the same store in the same
	// instant.
	fixed, err := h.reopen(through(syntheticV3), 7)
	if err != nil {
		t.Fatalf("a store whose migration was fixed did not open: %v", err)
	}
	if got := fixed.SchemaVersion(); got != 7 {
		t.Errorf("the retried migration reached v%d, want v7", got)
	}

	after := backupsIn(t, dir)
	if len(after) != 2 {
		t.Errorf("the retry left %v, want its own sidecar beside the first", after)
	}
	again, err := os.ReadFile(filepath.Join(dir, first[0]))
	if err != nil {
		t.Fatalf("re-read the first sidecar: %v", err)
	}
	if !bytes.Equal(again, kept) {
		t.Errorf("the sidecar %s was overwritten by a later migration", first[0])
	}
	wantSameRows(t, "the log after a failed migration and the retry", log, fixed.rowsOf("events", ""))
}

// ---------------------------------------------------------------------------
// 5. The projection version, which is not the schema version
// ---------------------------------------------------------------------------

// TestAProjectionBuiltUnderOlderRulesIsRefoldedAtOpen is the affordance D61
// bought, and the reason the two versions are separate numbers.
//
// A schema change alters the tables; a projection change alters what the same log
// *means*. The second needs no migration at all — the projection holds no fact
// the log does not, so the answer is to fold it again. What has to hold is that
// the refold really happens, and the control is what makes that assertion worth
// something: the same store, damaged the same way, reopened at the *current*
// projection version, comes back still damaged.
func TestAProjectionBuiltUnderOlderRulesIsRefoldedAtOpen(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Dir(h.Path())
	running := richHistory(h)

	sound := h.snapshotProjection()
	h.wantProjectionIsPopulated(sound)

	// Damage the projection the one way the guards deliberately cannot catch:
	// nothing checks that the event a row names is an event *about* that row.
	newer := h.commit(renderDraft(running))[0]
	log := h.rowsOf("events", "")
	if err := h.rawExec(
		`UPDATE nodes SET locator = 'corrupted', title = 'corrupted', last_event = ? WHERE id = ?`,
		newer.ID, running); err != nil {
		t.Fatalf("corrupt the projection: %v", err)
	}
	damaged := h.snapshotProjection()
	if diffs := h.diffProjection(sound, damaged); len(diffs) != 3 {
		t.Fatalf("the corruption produced %v, want three differences", diffs)
	}

	// The control: an open at the current projection version folds nothing.
	unchanged, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	unchanged.wantSameProjection("after an open at the current projection version",
		damaged, unchanged.snapshotProjection())

	// Now the store says its projection was folded under older rules.
	if err := unchanged.setMeta(unchanged.ctx, projectionVersionKey, "0"); err != nil {
		t.Fatalf("record an older projection version: %v", err)
	}
	refolded, err := unchanged.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen after a projection-version bump: %v", err)
	}
	refolded.wantSameProjection("after a projection-version bump", sound, refolded.snapshotProjection())

	// It records the version it now carries, so the next open has nothing to do.
	recorded, present, err := refolded.meta(refolded.ctx, projectionVersionKey)
	if err != nil {
		t.Fatalf("read the projection version: %v", err)
	}
	if want := fmt.Sprint(projectionVersion); !present || recorded != want {
		t.Errorf("the store records projection version %q (present %v), want %q", recorded, present, want)
	}

	// A refold is a fold and not a migration: the log is untouched and no backup
	// is taken, because nothing about the schema changed.
	wantSameRows(t, "the log after a refold", log, refolded.rowsOf("events", ""))
	if got := backupsIn(t, dir); len(got) != 0 {
		t.Errorf("a projection refold wrote %v; the sidecar belongs to schema migrations", got)
	}
	if got := refolded.SchemaVersion(); got != latestVersion(shipped()) {
		t.Errorf("a projection refold moved the schema version to v%d", got)
	}
}

// TestAProjectionFromANewerWipIsRefused is the downgrade refusal's counterpart on
// the other version. A binary that folds v1 of the derivation cannot make sense
// of a projection built under v2 of it, and re-folding would silently replace the
// newer store's answers with this binary's older ones.
func TestAProjectionFromANewerWipIsRefused(t *testing.T) {
	h := newHarness(t)
	richHistory(h)
	before := h.snapshotProjection()

	if err := h.setMeta(h.ctx, projectionVersionKey, fmt.Sprint(projectionVersion+1)); err != nil {
		t.Fatalf("record a newer projection version: %v", err)
	}
	path := h.Path()
	_, err := h.reopen(shipped(), latestVersion(shipped()))
	refusalMentions(t, "a projection folded by a newer wip", err, "newer wip")

	// The refusal left the projection alone rather than folding it down. Saying so
	// takes a write with the store closed, because the refusal happens before
	// there is a Store handle to write through.
	setMetaAround(t, path, projectionVersionKey, fmt.Sprint(projectionVersion))
	back, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen after putting the projection version back: %v", err)
	}
	back.wantSameProjection("after a refused open", before, back.snapshotProjection())
}

// TestAnUnreadableProjectionVersionIsRefused holds the parse, because the failure
// it prevents is silent. A value this binary cannot read is a store it cannot
// make a decision about, and the decision it would otherwise default to —
// "current, nothing to do" — is the one that skips a rebuild the store needs.
func TestAnUnreadableProjectionVersionIsRefused(t *testing.T) {
	for _, recorded := range []string{"one", "", "1x", " ", "1.5"} {
		t.Run(fmt.Sprintf("%q", recorded), func(t *testing.T) {
			h := newHarness(t)
			h.matter("unreadable", "A store with a garbled projection version")
			if err := h.setMeta(h.ctx, projectionVersionKey, recorded); err != nil {
				t.Fatalf("record %q: %v", recorded, err)
			}
			_, err := h.reopen(shipped(), latestVersion(shipped()))
			refusalMentions(t, fmt.Sprintf("a projection version recorded as %q", recorded),
				err, "unreadable projection version")
		})
	}
}

// ---------------------------------------------------------------------------
// 6. Two migrations in one open
// ---------------------------------------------------------------------------

// TestTwoMigrationsInOneOpenApplyInOrderUnderOneBackup is the case nobody asked
// for, and it exercises three things at once that a single v1 -> v2 cannot.
//
// Ordering: v3 alters a table v2 has already touched, so a register applied out
// of order would fail rather than merely differ. The backup: it is taken once per
// open, named for the version the *store* was at and not for each migration, so
// two versions in one open leave one sidecar stamped v1. And a populated table
// coming through an ALTER, which is the increment a schema-only migration cannot
// stand in for.
//
// The oracle is three-way. A store that went v1 -> v3 in one open, a store built
// at v3 directly, and a store that went v1 -> v2 -> v3 across two opens all have
// to end up with the same schema — because "forward-only, numbered, applied in
// order" means exactly that the route does not matter.
func TestTwoMigrationsInOneOpenApplyInOrderUnderOneBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")
	reg := through(syntheticV3, syntheticV4)

	h := newHarnessAt(t, path, baselineRegister(), 1)
	richHistory(h)
	nodeColumnsBefore := h.columnsOf("nodes")
	nodesBefore := h.rowsOf("nodes", "")
	log := h.rowsOf("events", "")
	if len(nodesBefore) == 0 {
		t.Fatal("the fixture wrote no nodes; an ALTER over a populated table wants rows")
	}

	at := time.Now()
	migrated, err := h.reopen(reg, 8)
	if err != nil {
		t.Fatalf("reopen under v3: %v", err)
	}
	if got := migrated.SchemaVersion(); got != 8 {
		t.Errorf("one open applied up to v%d, want v8", got)
	}
	wantMigrationLedger(migrated, "after v1 -> v8 in one open", through(syntheticV3, syntheticV4))

	// One backup for one open, named for the version the store was leaving.
	sidecars := backupsIn(t, dir)
	if len(sidecars) != 1 {
		t.Fatalf("two migrations in one open left %v, want one sidecar", sidecars)
	}
	wantBackupName(t, sidecars[0], 1, at)

	// Every node row came through the ALTER saying exactly what it said before,
	// with the new column null on all of them: an added column is a column nobody
	// has filled in, never a value invented for rows that predate it.
	nodesAfter := migrated.rowsOf("nodes", "")
	wantSameRows(t, "nodes through an ALTER TABLE", nodesBefore, nodesAfter)
	if got := migrated.columnsOf("nodes"); !reflect.DeepEqual(got, append(append([]string{}, nodeColumnsBefore...), "review_note")) {
		t.Errorf("nodes has columns %v after v3, want the v1 set plus review_note", got)
	}
	for i, row := range nodesAfter {
		if row["review_note"] != "NULL" {
			t.Errorf("node row %d has review_note = %s, want NULL", i, row["review_note"])
		}
	}
	wantSameRows(t, "the log after two migrations", log, migrated.rowsOf("events", ""))

	// --- the three-way oracle ------------------------------------------------
	direct := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), reg, 8)
	wantSameSchema(t, "v1 -> v3 in one open against a store built at v3",
		schemaShape(t, migrated.Store), schemaShape(t, direct.Store))

	stepwise := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), baselineRegister(), 1)
	atTwo, err := stepwise.reopen(reg, 3)
	if err != nil {
		t.Fatalf("reopen under v2: %v", err)
	}
	atThree, err := atTwo.reopen(reg, 8)
	if err != nil {
		t.Fatalf("reopen under v3: %v", err)
	}
	wantSameSchema(t, "v1 -> v3 in one open against v1 -> v2 -> v3 in two",
		schemaShape(t, migrated.Store), schemaShape(t, atThree.Store))
	// And the store that took two opens took two backups, one per open that had
	// something pending — the second stamped v3, after the first open applied
	// product v2 and synthetic v3 together.
	stepwiseSidecars := backupsIn(t, filepath.Dir(atThree.Path()))
	if len(stepwiseSidecars) != 2 {
		t.Fatalf("two opens that each migrated left %v, want two sidecars", stepwiseSidecars)
	}
	wantBackupName(t, stepwiseSidecars[0], 1, at)
	wantBackupName(t, stepwiseSidecars[1], 3, at)
}

func TestV2RefusesEveryLegacyRunRowWithoutChangingV1(t *testing.T) {
	for _, state := range []string{"queued", "open", "finished"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "wip.db")
			h := newHarnessAt(t, path, baselineRegister(), 1)
			batch := h.newBatch("legacy-" + state)
			event := h.birthEventOf(batch)
			run := h.NewID()
			if err := h.rawExec(`INSERT INTO runs (id, clone, batch, state, birth_event, last_event) VALUES (?, ?, ?, ?, ?, ?)`,
				run, h.Clone, batch, state, event.ID, event.ID); err != nil {
				t.Fatalf("insert legacy Run: %v", err)
			}
			beforeSchema := schemaShape(t, h.Store)
			beforeRuns := h.rowsOf("runs", "")
			beforeLog := h.rowsOf("events", "")

			_, err := h.reopen(through(syntheticV3), 3)
			refusalMentions(t, "legacy Run migration", err, "incompatible Run row "+run)

			reopened, err := h.reopen(baselineRegister(), 1)
			if err != nil {
				t.Fatalf("reopen unchanged v1 store: %v", err)
			}
			if got := reopened.SchemaVersion(); got != 1 {
				t.Errorf("schema version after refusal = %d, want 1", got)
			}
			wantSameSchema(t, "schema after refused migration", schemaShape(t, reopened.Store), beforeSchema)
			wantSameRows(t, "legacy Run row after refused migration", beforeRuns, reopened.rowsOf("runs", ""))
			wantSameRows(t, "event log after refused migration", beforeLog, reopened.rowsOf("events", ""))
			if got := len(reopened.rowsOf("schema_migrations", "version = 2")); got != 0 {
				t.Errorf("refused migration was recorded")
			}
		})
	}
}
