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

var namedBatchMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(
	strings.Replace(dependencyMigrationMarker.sql, "16, 17, 18)", "16, 17, 18, 19)", 1),
	"name = 'step-8-dependency-graph'))", "name = 'step-8-dependency-graph') OR (version = 19 AND name = 'step-9a-named-batch-projection'))", 1)}

var namedBatchSchema = []schemaObject{
	{"m6_named_batches", "table", `CREATE TABLE m6_named_batches (
  domain_id TEXT NOT NULL,
  batch_id TEXT NOT NULL,
  name TEXT NOT NULL CHECK(length(name)>0),
  birth_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,batch_id),
  UNIQUE(domain_id,name),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT, WITHOUT ROWID`},
	{"m6_named_batches_identity_immutable", "trigger", `CREATE TRIGGER m6_named_batches_identity_immutable BEFORE UPDATE ON m6_named_batches BEGIN SELECT RAISE(ABORT,'immutable named Batch identity'); END`},
	{"m6_named_batches_no_delete", "trigger", `CREATE TRIGGER m6_named_batches_no_delete BEFORE DELETE ON m6_named_batches BEGIN SELECT RAISE(ABORT,'retained named Batch identity'); END`},
}

func installNamedBatches(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
		return fmt.Errorf("%w: named Batch projection requires schema 18", ErrInvalidStore)
	}
	for _, object := range namedBatchSchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if err = rebuildNamedBatchesTx(context.Background(), tx); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, namedBatchMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(19,'step-9a-named-batch-projection')`, `PRAGMA user_version=19`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV18 retains an exact validated v18 backup before installing the
// additive named-Batch projection. Ordinary opens never migrate.
func UpgradeV18(root string) error {
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
	if err = checkSchemaVersion(db, 18); err != nil {
		return fmt.Errorf("%w: v18 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v18 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v18.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v18 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 18), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installNamedBatches(db); err != nil {
		return err
	}
	if err = checkSchemaVersion(db, 19); err != nil {
		return err
	}
	return checkBlobFiles(db, filepath.Join(path, "blobs"))
}
