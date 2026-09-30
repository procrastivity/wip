package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step10MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings'))) STRICT`}

var step10Schema = []schemaObject{
	{"content_segments", "table", `CREATE TABLE content_segments (
  domain_id TEXT NOT NULL,
  content_id TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('brief','workplan','body','findings')),
  blob_digest TEXT NOT NULL,
  byte_length INTEGER NOT NULL CHECK(byte_length BETWEEN 0 AND 1099511627776),
  command_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,content_id),
  UNIQUE(domain_id,command_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,blob_digest) REFERENCES blob_products(domain_id,digest),
  FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id)
) STRICT`},
	{"content_segments_immutable", "trigger", `CREATE TRIGGER content_segments_immutable BEFORE UPDATE ON content_segments BEGIN SELECT RAISE(ABORT,'immutable content segment'); END`},
	{"content_segments_no_delete", "trigger", `CREATE TRIGGER content_segments_no_delete BEFORE DELETE ON content_segments BEGIN SELECT RAISE(ABORT,'retained content segment'); END`},
	{"content_write_once", "index", `CREATE UNIQUE INDEX content_write_once ON content_segments(domain_id,subject_id,kind) WHERE kind!='findings'`},
	{"content_subject_order", "index", `CREATE INDEX content_subject_order ON content_segments(domain_id,subject_id,event_id)`},
}

func installStep10(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step10MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step10Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=9`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV8 retains a validated, exact v8 backup before adding the content
// projection. Ordinary opens never migrate existing stores.
func UpgradeV8(root string) error {
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
	if err = checkSchemaVersion(db, 8); err != nil {
		return fmt.Errorf("%w: v8 validation: %v", ErrInvalidStore, err)
	}
	if err = checkStep4State(db); err != nil {
		return fmt.Errorf("%w: v8 command state: %v", ErrInvalidStore, err)
	}
	if err = checkStep5State(db); err != nil {
		return fmt.Errorf("%w: v8 claim state: %v", ErrInvalidStore, err)
	}
	if err = checkM5GenesisState(db); err != nil {
		return fmt.Errorf("%w: v8 genesis state: %v", ErrInvalidStore, err)
	}
	if err = checkBirthJournalState(db); err != nil {
		return fmt.Errorf("%w: v8 birth journal: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v8 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v8.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v8 backup already exists; inspect before retry", ErrInvalidStore)
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
	backupErr := errors.Join(checkSchemaVersion(retained, 8), retained.Close())
	if backupErr != nil {
		return backupErr
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	file, err = os.Open(backup)
	if err != nil {
		return err
	}
	if err = errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
		return err
	}
	if err = installStep10(db); err != nil {
		return err
	}
	if err = checkSchemaVersion(db, 9); err != nil {
		return err
	}
	return checkStep4State(db)
}
