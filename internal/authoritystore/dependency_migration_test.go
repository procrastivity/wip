package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func dropNamedBatchSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for index := len(d115Schema) - 1; index >= 0; index-- {
		object := d115Schema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
	for index := len(environmentAttachSchema) - 1; index >= 0; index-- {
		object := environmentAttachSchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
	for index := len(routeIndexSchema) - 1; index >= 0; index-- {
		object := routeIndexSchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
	for index := len(namedBatchMembershipSchema) - 1; index >= 0; index-- {
		object := namedBatchMembershipSchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
	for index := len(namedBatchSchema) - 1; index >= 0; index-- {
		object := namedBatchSchema[index]
		if _, err := db.Exec(`DROP ` + object.kind + ` IF EXISTS ` + object.name); err != nil {
			t.Fatal(err)
		}
	}
}

func dropDependencySchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	dropNamedBatchSchemaForTest(t, db)
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
	if store, openErr := OpenExisting(f.root); !errors.Is(openErr, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("ordinary open migrated v18: %v", openErr)
	}
	if err = UpgradeV18(f.root); err != nil {
		t.Fatalf("explicit v18 to v19 migration: %v", err)
	}
	if store, openErr := OpenExisting(f.root); !errors.Is(openErr, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("ordinary open migrated v19: %v", openErr)
	}
	if err = UpgradeV19(f.root); err != nil {
		t.Fatalf("explicit v19 to v20 migration: %v", err)
	}
	backup19, err := connect(filepath.Join(f.root, "authority-v19.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(checkSchemaVersion(backup19, 19), backup19.Close()); err != nil {
		t.Fatal(err)
	}
	backup18, err := connect(filepath.Join(f.root, "authority-v18.backup.db"), "ro", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(checkSchemaVersion(backup18, 18), backup18.Close()); err != nil {
		t.Fatal(err)
	}
	if store, openErr := OpenExisting(f.root); !errors.Is(openErr, ErrInvalidStore) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatalf("ordinary open migrated v20: %v", openErr)
	}
	backupPath := filepath.Join(f.root, "authority-v20.backup.db")
	resumeDB, err := connect(filepath.Join(f.root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resumeDB.Exec(`VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	if err = installRouteIndex(resumeDB); err != nil {
		t.Fatalf("simulate interruption after route-index migration: %v", err)
	}
	if err = resumeDB.Close(); err != nil {
		t.Fatal(err)
	}
	interruptedDB, err := connect(filepath.Join(f.root, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSchemaVersion(interruptedDB, 21); err != nil {
		_ = interruptedDB.Close()
		t.Fatalf("validate interrupted schema 21: %v", err)
	}
	if err = installD115(interruptedDB); err != nil {
		t.Fatalf("simulate interruption after D115 migration: %v", err)
	}
	if err = interruptedDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV20(f.root); err != nil {
		t.Fatalf("resume explicit Step 11 migration from schema 22: %v", err)
	}
	backup20, err := connect(filepath.Join(f.root, "authority-v20.backup.db"), "rw", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(checkSchemaVersion(backup20, 20), backup20.Close()); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.s.Close() }()
	var version int
	if err = f.s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
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
	resumeRoot := filepath.Join(f.root, "resume-from-v21")
	if err = os.Mkdir(resumeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(resumeRoot, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	v20Bytes, err := os.ReadFile(filepath.Join(f.root, "authority-v20.backup.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(resumeRoot, "authority.db"), v20Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	resumeDB, err = connect(filepath.Join(resumeRoot, "authority.db"), "rw", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = installRouteIndex(resumeDB); err != nil {
		_ = resumeDB.Close()
		t.Fatalf("simulate interruption at schema 21: %v", err)
	}
	if err = resumeDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err = UpgradeV20(resumeRoot); err != nil {
		t.Fatalf("resume explicit migration from schema 21: %v", err)
	}
	resumed, err := OpenExisting(resumeRoot)
	if err != nil {
		t.Fatalf("open store resumed from schema 21: %v", err)
	}
	if err = resumed.Close(); err != nil {
		t.Fatal(err)
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
