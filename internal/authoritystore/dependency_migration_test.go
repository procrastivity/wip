package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func dropDependencySchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for index := len(dependencySchema) - 1; index >= 0; index-- {
		object := dependencySchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
}

func dependencyDowngradeV17(t *testing.T, db *sql.DB) {
	t.Helper()
	dropDependencySchemaForTest(t, db)
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, batchSweepMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations WHERE version<=17`, `DROP TABLE old_schema_migrations`, `PRAGMA user_version=17`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDependencyExplicitV17MigrationRetainsHistory(t *testing.T) {
	f := newClaimTestFixture(t)
	before := f.anchor(t)
	var original []byte
	if err := f.s.db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE command_id=?`, claimTestID(10)).Scan(&original); err != nil {
		t.Fatal(err)
	}
	dependencyDowngradeV17(t, f.s.db)
	if err := checkSchemaVersion(f.s.db, 17); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("ordinary open migrated v17: %v", err)
	}
	if err := UpgradeV17(f.root); err != nil {
		t.Fatal(err)
	}
	backup, err := connect(filepath.Join(f.root, "authority-v17.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(checkSchemaVersion(backup, 17), backup.Close()); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.s.Close() }()
	var version int
	if err = f.s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
		t.Fatalf("version=%d: %v", version, err)
	}
	var retained []byte
	if err = f.s.db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE command_id=?`, claimTestID(10)).Scan(&retained); err != nil || !bytes.Equal(retained, original) {
		t.Fatalf("migration changed receipt: %v", err)
	}
	if got, err := readDependencyProjection(f.s.db); err != nil || len(got) != 0 || f.anchor(t) != before {
		t.Fatalf("migration invented graph/history: %+v %v", got, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV17(f.root); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("repeat upgrade = %v", err)
	}
}

func TestDependencyMigrationFailureRollsBackSchemaAndProjection(t *testing.T) {
	f, other := newDependencyHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	// No dependency history was admitted by v17. Construct malformed retained
	// input to exercise a replay failure before the migration can commit.
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hash := hashCommand(t, matterCommand(claimTestID(11), 2, "beta"))
	_, err = appendCommandEvent(context.Background(), tx, eventIdentity{domainA, claimTestID(11), hash, envA, 2, "2026-09-23T11:59:00Z", repoA}, f.now,
		claimTestID(102), "dependency.removed", f.matter, map[string]any{"edge": claimTestID(90), "blocker": other})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dependencyDowngradeV17(t, f.s.db)
	if err = installDependencies(f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("invalid graph migrated: %v", err)
	}
	var tables, version int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='m6_dependencies'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("failed install left schema: %d %v", tables, err)
	}
	if err = f.s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 17 {
		t.Fatalf("failed install advanced version: %d %v", version, err)
	}
}
