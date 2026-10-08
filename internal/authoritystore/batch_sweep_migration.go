package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var batchSweepMigrationMarker = schemaObject{"schema_migrations", "table", strings.Replace(strings.Replace(
	step17MigrationMarker.sql, "15, 16)", "15, 16, 17)", 1),
	"name = 'step-7-terminal-and-journal-state-boundaries'))", "name = 'step-7-terminal-and-journal-state-boundaries') OR (version = 17 AND name = 'step-7-batch-sweep-boundaries'))", 1)}

var batchSweepSchema = []schemaObject{
	{"batch_sweep_boundaries", "table", `CREATE TABLE batch_sweep_boundaries (
  domain_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  terminal_receipt_digest TEXT NOT NULL,
  result_code TEXT NOT NULL CHECK(result_code IN ('result.succeeded','result.refused')),
  outcome TEXT,
  problem_code TEXT,
  event_count INTEGER NOT NULL CHECK(event_count >= 0),
  event_id TEXT,
  prefix_digest TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES terminal_receipts(domain_id,command_id),
  CHECK((event_count=0 AND event_id IS NULL) OR (event_count>0 AND event_id IS NOT NULL)),
  CHECK((result_code='result.succeeded' AND outcome IN ('swept','already-swept') AND outcome IS NOT NULL AND problem_code IS NULL) OR
    (result_code='result.refused' AND outcome IS NULL AND problem_code IS NOT NULL AND problem_code IN (
      'refusal.batch-sweep-target-missing','refusal.batch-sweep-claim-close','refusal.batch-sweep-not-eligible','refusal.batch-sweep-unsupported-state')))
) STRICT`},
	{"batch_sweep_boundaries_insert", "trigger", `CREATE TRIGGER batch_sweep_boundaries_insert BEFORE INSERT ON batch_sweep_boundaries
WHEN NOT EXISTS(
  SELECT 1 FROM submissions s JOIN terminal_receipts r USING(domain_id,command_id)
  WHERE s.domain_id=NEW.domain_id AND s.command_id=NEW.command_id AND s.request_hash=NEW.request_hash
    AND s.operation_name='batch.sweep-anonymous' AND s.operation_version=1 AND s.state='terminal'
    AND r.result_code=NEW.result_code
    AND ((NEW.outcome='swept' AND r.first_position=NEW.event_count+1 AND r.last_position=r.first_position) OR
      ((NEW.outcome='already-swept' OR NEW.result_code='result.refused') AND r.first_position IS NULL AND r.last_position IS NULL))
) OR (NEW.event_count>0 AND NOT EXISTS(
  SELECT 1 FROM authority_events e WHERE e.domain_id=NEW.domain_id AND e.position=NEW.event_count
    AND e.event_id=NEW.event_id AND e.prefix_digest=NEW.prefix_digest
))
BEGIN SELECT RAISE(ABORT,'invalid batch sweep terminal boundary'); END`},
	{"batch_sweep_boundaries_immutable", "trigger", `CREATE TRIGGER batch_sweep_boundaries_immutable BEFORE UPDATE ON batch_sweep_boundaries BEGIN SELECT RAISE(ABORT,'immutable batch sweep boundary'); END`},
	{"batch_sweep_boundaries_no_delete", "trigger", `CREATE TRIGGER batch_sweep_boundaries_no_delete BEFORE DELETE ON batch_sweep_boundaries BEGIN SELECT RAISE(ABORT,'immutable batch sweep boundary'); END`},
}

func installBatchSweep(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var candidates int
	if err = tx.QueryRow(`SELECT count(*) FROM submissions WHERE operation_name='batch.sweep-anonymous'`).Scan(&candidates); err != nil {
		return err
	}
	if candidates != 0 {
		// This candidate never had public or private sweep submissions. Do not
		// invent observed boundaries or promote any previously retained rows.
		return fmt.Errorf("%w: sweep submissions predate their boundary store", ErrInvalidStore)
	}
	for _, object := range batchSweepSchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE schema_migrations RENAME TO old_schema_migrations`, batchSweepMigrationMarker.sql,
		`INSERT INTO schema_migrations SELECT * FROM old_schema_migrations`, `DROP TABLE old_schema_migrations`,
		`INSERT INTO schema_migrations VALUES(17,'step-7-batch-sweep-boundaries')`, `PRAGMA user_version=17`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeV16 explicitly validates and retains an exact v16 backup before
// installing the empty sweep boundary store. Ordinary open never migrates.
func UpgradeV16(root string) error {
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
	if err = checkSchemaVersion(db, 16); err != nil {
		return fmt.Errorf("%w: v16 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v16 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v16.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v16 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 16), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installBatchSweep(db); err != nil {
		return err
	}
	if err = installDependencies(db); err != nil {
		return err
	}
	if err = installNamedBatches(db); err != nil {
		return err
	}
	return checkSchema(db)
}
