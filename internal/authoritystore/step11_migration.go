package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step11MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order'))) STRICT`}

var step11Schema = []schemaObject{
	{"claim_journal_entries_contiguous", "trigger", `CREATE TRIGGER claim_journal_entries_contiguous BEFORE INSERT ON claim_journal_entries WHEN NEW.state!='pending-return' OR NOT EXISTS(SELECT 1 FROM claim_journals j WHERE j.domain_id=NEW.domain_id AND j.journal_id=NEW.journal_id AND j.state='open') OR NEW.position!=COALESCE((SELECT max(position)+1 FROM claim_journal_entries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id),1) OR (NEW.position>1 AND NEW.environment_sequence<=(SELECT environment_sequence FROM claim_journal_entries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id AND position=NEW.position-1)) BEGIN SELECT RAISE(ABORT,'noncontiguous journal entry'); END`},
}

func installStep11(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER claim_journal_entries_contiguous`, step11Schema[0].sql,
		`DROP TABLE schema_migrations`, step11MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order')`,
		`PRAGMA user_version=10`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV9 retains a byte-equivalent backup before allowing authority-class
// Environment commands to interleave between claim-local receipt positions.
// Journal positions remain contiguous; only Environment sequence gaps become
// valid, and entries must still be strictly ordered.
func UpgradeV9(root string) error {
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
	if err = checkSchemaVersion(db, 9); err != nil {
		return fmt.Errorf("%w: v9 validation: %v", ErrInvalidStore, err)
	}
	if err = checkStep4State(db); err != nil {
		return fmt.Errorf("%w: v9 authority state: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v9 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v9.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v9 backup already exists; inspect before retry", ErrInvalidStore)
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
	backupErr := errors.Join(checkSchemaVersion(retained, 9), retained.Close())
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
	if err = installStep11(db); err != nil {
		return err
	}
	if err = checkSchemaVersion(db, 10); err != nil {
		return err
	}
	return checkStep4State(db)
}
