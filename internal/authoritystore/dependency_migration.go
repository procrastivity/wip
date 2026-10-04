package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var dependencyMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(strings.Replace(
	batchSweepMigrationMarker.sql, "16, 17)", "16, 17, 18)", 1),
	"name = 'step-7-batch-sweep-boundaries'))", "name = 'step-7-batch-sweep-boundaries') OR (version = 18 AND name = 'step-8-dependency-graph'))", 1)}

// m6_dependencies.repo_id is the immutable add-command context Repo. The
// domain owns the graph; neither endpoint nor a removal must share that Repo.
var dependencySchema = []schemaObject{
	{"m6_dependencies", "table", `CREATE TABLE m6_dependencies (
  domain_id TEXT NOT NULL,
  edge_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  blocked_id TEXT NOT NULL,
  blocker_id TEXT NOT NULL,
  birth_event_id TEXT NOT NULL,
  last_event_id TEXT NOT NULL,
  tombstone_event_id TEXT,
  PRIMARY KEY(domain_id,edge_id),
  CHECK(blocked_id!=blocker_id),
  CHECK((tombstone_event_id IS NULL AND last_event_id=birth_event_id) OR (tombstone_event_id IS NOT NULL AND tombstone_event_id=last_event_id)),
  FOREIGN KEY(domain_id,repo_id) REFERENCES repo_memberships(domain_id,repo_id),
  FOREIGN KEY(domain_id,blocked_id) REFERENCES m6_nodes(domain_id,node_id),
  FOREIGN KEY(domain_id,blocker_id) REFERENCES m6_nodes(domain_id,node_id),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,last_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,tombstone_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"m6_dependencies_active_pair", "index", `CREATE UNIQUE INDEX m6_dependencies_active_pair ON m6_dependencies(domain_id,blocked_id,blocker_id) WHERE tombstone_event_id IS NULL`},
	{"m6_dependencies_identity_immutable", "trigger", `CREATE TRIGGER m6_dependencies_identity_immutable BEFORE UPDATE OF domain_id,edge_id,repo_id,blocked_id,blocker_id,birth_event_id ON m6_dependencies BEGIN SELECT RAISE(ABORT,'immutable dependency edge identity'); END`},
}

func installDependencies(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, object := range dependencySchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if err = dependencyProjectionTx(context.Background(), tx); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, dependencyMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(18,'step-8-dependency-graph')`, `PRAGMA user_version=18`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV17 retains a validated, exact v17 backup before installing and
// rebuilding the dependency projection. Ordinary opens never migrate.
func UpgradeV17(root string) error {
	path, err := cleanRoot(root)
	if err != nil {
		return err
	}
	if err = checkRoot(path); err != nil {
		return err
	}
	lock, err := acquire(path)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	db, err := connect(filepath.Join(path, "authority.db"), "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 17); err != nil {
		return fmt.Errorf("%w: v17 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v17 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v17.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v17 backup already exists; inspect before retry", ErrInvalidStore)
	}
	file, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
		return err
	}
	if err = regularFile(backup); err != nil {
		return err
	}
	retained, err := connect(backup, "rw", true)
	if err != nil {
		return err
	}
	if err = errors.Join(checkSchemaVersion(retained, 17), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installDependencies(db); err != nil {
		return err
	}
	return checkSchema(db)
}
