package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var namedBatchMembershipMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(
	strings.Replace(dependencyMigrationMarker.sql, "16, 17, 18)", "16, 17, 18, 19, 20)", 1),
	"name = 'step-8-dependency-graph'))", "name = 'step-8-dependency-graph') OR (version = 19 AND name = 'step-9a-named-batch-projection') OR (version = 20 AND name = 'step-9b-named-batch-membership'))", 1)}

var namedBatchMembershipSchema = []schemaObject{
	{"m6_named_batches", "table", `CREATE TABLE m6_named_batches (
  domain_id TEXT NOT NULL,
  batch_id TEXT NOT NULL,
  name TEXT NOT NULL CHECK(length(name)>0),
  birth_event_id TEXT NOT NULL,
  dismissed_event_id TEXT,
  PRIMARY KEY(domain_id,batch_id),
  UNIQUE(domain_id,name),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,dismissed_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT, WITHOUT ROWID`},
	{"m6_named_batches_identity_immutable", "trigger", `CREATE TRIGGER m6_named_batches_identity_immutable BEFORE UPDATE OF domain_id,batch_id,name,birth_event_id ON m6_named_batches BEGIN SELECT RAISE(ABORT,'immutable named Batch identity'); END`},
	{"m6_named_batches_no_delete", "trigger", `CREATE TRIGGER m6_named_batches_no_delete BEFORE DELETE ON m6_named_batches BEGIN SELECT RAISE(ABORT,'retained named Batch identity'); END`},
	{"m6_named_batches_dismiss_once", "trigger", `CREATE TRIGGER m6_named_batches_dismiss_once BEFORE UPDATE OF dismissed_event_id ON m6_named_batches WHEN OLD.dismissed_event_id IS NOT NULL OR NEW.dismissed_event_id IS NULL BEGIN SELECT RAISE(ABORT,'named Batch dismissal is terminal'); END`},
	{"m6_named_batch_memberships", "table", `CREATE TABLE m6_named_batch_memberships (
  domain_id TEXT NOT NULL,
  batch_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  joined_event_id TEXT NOT NULL,
  left_event_id TEXT,
  PRIMARY KEY(domain_id,batch_id,matter_id,joined_event_id),
  FOREIGN KEY(domain_id,batch_id) REFERENCES m6_named_batches(domain_id,batch_id),
  FOREIGN KEY(domain_id,matter_id) REFERENCES matters(domain_id,matter_id),
  FOREIGN KEY(domain_id,joined_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,left_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT, WITHOUT ROWID`},
	{"m6_named_batch_memberships_active_pair", "index", `CREATE UNIQUE INDEX m6_named_batch_memberships_active_pair ON m6_named_batch_memberships(domain_id,batch_id,matter_id) WHERE left_event_id IS NULL`},
	{"m6_named_batch_memberships_identity_immutable", "trigger", `CREATE TRIGGER m6_named_batch_memberships_identity_immutable BEFORE UPDATE OF domain_id,batch_id,matter_id,joined_event_id ON m6_named_batch_memberships BEGIN SELECT RAISE(ABORT,'immutable named Batch membership identity'); END`},
	{"m6_named_batch_memberships_leave_once", "trigger", `CREATE TRIGGER m6_named_batch_memberships_leave_once BEFORE UPDATE OF left_event_id ON m6_named_batch_memberships WHEN OLD.left_event_id IS NOT NULL OR NEW.left_event_id IS NULL BEGIN SELECT RAISE(ABORT,'named Batch membership leave is terminal'); END`},
	{"m6_named_batch_memberships_no_delete", "trigger", `CREATE TRIGGER m6_named_batch_memberships_no_delete BEFORE DELETE ON m6_named_batch_memberships BEGIN SELECT RAISE(ABORT,'retained named Batch membership history'); END`},
}

func installNamedBatchMembership(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 19 {
		return fmt.Errorf("%w: named Batch membership requires schema 19", ErrInvalidStore)
	}
	for _, statement := range []string{
		`DROP TRIGGER m6_named_batches_identity_immutable`, `DROP TRIGGER m6_named_batches_no_delete`,
		`ALTER TABLE m6_named_batches RENAME TO m6_named_batches_v19`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range namedBatchMembershipSchema[:4] {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO m6_named_batches(domain_id,batch_id,name,birth_event_id,dismissed_event_id)
		SELECT domain_id,batch_id,name,birth_event_id,NULL FROM m6_named_batches_v19`); err != nil {
		return err
	}
	if _, err = tx.Exec(`DROP TABLE m6_named_batches_v19`); err != nil {
		return err
	}
	for _, object := range namedBatchMembershipSchema[4:] {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, namedBatchMembershipMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(20,'step-9b-named-batch-membership')`, `PRAGMA user_version=20`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV19 retains a validated, exact v19 backup before installing the
// named-Batch membership projection. Ordinary opens never migrate.
func UpgradeV19(root string) error {
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
	if err = checkSchemaVersion(db, 19); err != nil {
		return fmt.Errorf("%w: v19 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v19 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v19.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v19 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 19), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installNamedBatchMembership(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	if err = checkNamedBatchMembershipState(db); err != nil {
		return err
	}
	return checkBlobFiles(db, filepath.Join(path, "blobs"))
}
