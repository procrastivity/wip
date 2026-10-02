package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step15MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations') OR (version = 12 AND name = 'step-7-authority-gate-config-projections') OR (version = 13 AND name = 'step-7-detached-gate-repair-admissions') OR (version = 14 AND name = 'step-7-private-gate-repair-terminals'))) STRICT`}

var step15Schema = []schemaObject{
	{"gate_exemption_repair_terminals", "table", `CREATE TABLE gate_exemption_repair_terminals (
  domain_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  result_code TEXT NOT NULL CHECK(result_code IN ('result.succeeded','result.refused')),
  refusal_code TEXT,
  refusal_message TEXT,
  observed_position INTEGER NOT NULL CHECK(observed_position>0),
  observed_event_id TEXT NOT NULL,
  observed_prefix_digest TEXT NOT NULL,
  event_id TEXT,
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id),
  UNIQUE(domain_id,event_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES gate_exemption_repair_admissions(domain_id,command_id),
  CHECK((result_code='result.succeeded' AND refusal_code IS NULL AND refusal_message IS NULL) OR
        (result_code='result.refused' AND refusal_code LIKE 'refusal.%' AND length(refusal_message)>0 AND event_id IS NULL))
) STRICT`},
	{"gate_exemption_repair_terminals_immutable", "trigger", `CREATE TRIGGER gate_exemption_repair_terminals_immutable BEFORE UPDATE ON gate_exemption_repair_terminals BEGIN SELECT RAISE(ABORT,'immutable repair terminal'); END`},
	{"gate_exemption_repair_terminals_no_delete", "trigger", `CREATE TRIGGER gate_exemption_repair_terminals_no_delete BEFORE DELETE ON gate_exemption_repair_terminals BEGIN SELECT RAISE(ABORT,'immutable repair terminal'); END`},
	{"submissions_no_repair_admission_collision", "trigger", `CREATE TRIGGER submissions_no_repair_admission_collision BEFORE INSERT ON submissions
WHEN EXISTS(SELECT 1 FROM gate_exemption_repair_admissions a WHERE a.domain_id=NEW.domain_id AND
  (a.command_id=NEW.command_id OR (a.environment_id=NEW.environment_id AND a.environment_sequence=NEW.environment_sequence)))
AND NOT EXISTS(
  SELECT 1 FROM gate_exemption_repair_admissions a JOIN gate_exemption_repair_terminals t
    ON t.domain_id=a.domain_id AND t.command_id=a.command_id
  WHERE a.domain_id=NEW.domain_id AND a.command_id=NEW.command_id AND a.request_hash=NEW.request_hash AND a.command=NEW.command
    AND a.authority_epoch=NEW.epoch AND a.environment_id=NEW.environment_id AND a.environment_sequence=NEW.environment_sequence
    AND NEW.operation_name='gate.exemption.repair' AND NEW.operation_version=1 AND NEW.state='terminal'
)
BEGIN SELECT RAISE(ABORT,'submission conflicts with repair admission'); END`},
	{"environment_sequence_terminal", "trigger", `CREATE TRIGGER environment_sequence_terminal BEFORE UPDATE OF sequence_head ON environments
WHEN NEW.sequence_head != OLD.sequence_head + 1 OR (
  NOT EXISTS(
    SELECT 1 FROM submissions s JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id
    WHERE s.domain_id=NEW.domain_id AND s.environment_id=NEW.environment_id AND s.environment_sequence=NEW.sequence_head AND s.state='terminal'
  ) AND NOT EXISTS(
    SELECT 1 FROM submissions s
    JOIN gate_exemption_repair_admissions a ON a.domain_id=s.domain_id AND a.command_id=s.command_id
    JOIN gate_exemption_repair_terminals t ON t.domain_id=s.domain_id AND t.command_id=s.command_id
    WHERE s.domain_id=NEW.domain_id AND s.environment_id=NEW.environment_id AND s.environment_sequence=NEW.sequence_head
      AND s.state='terminal' AND s.operation_name='gate.exemption.repair' AND s.operation_version=1
      AND s.command=a.command AND s.request_hash=a.request_hash AND s.epoch=a.authority_epoch
      AND s.environment_id=a.environment_id AND s.environment_sequence=a.environment_sequence
  )
)
BEGIN SELECT RAISE(ABORT,'sequence requires terminal acknowledgment'); END`},
}

func installStep15(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER submissions_no_repair_admission_collision`,
		`DROP TRIGGER environment_sequence_terminal`,
		`DROP TABLE schema_migrations`, step15MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections'),(13,'step-7-detached-gate-repair-admissions'),(14,'step-7-private-gate-repair-terminals')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("install private repair terminal schema: %w", err)
		}
	}
	for _, object := range step15Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=14`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV13 installs the private terminal-outcome store while retaining an
// exact v13 backup. It adds no operation registration or receipt path.
func UpgradeV13(root string) error {
	return upgradeV13(root)
}

func upgradeV13(root string) error {
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
	if err = checkSchemaVersion(db, 13); err != nil {
		return fmt.Errorf("%w: v13 validation: %v", ErrInvalidStore, err)
	}
	for _, check := range []struct {
		name string
		fn   func(*sql.DB) error
	}{{"authority", checkStep4State}, {"gate/config projection", checkStep13State}, {"repair admission", checkStep14State}} {
		if err = check.fn(db); err != nil {
			return fmt.Errorf("%w: v13 %s: %v", ErrInvalidStore, check.name, err)
		}
	}
	backup := filepath.Join(path, "authority-v13.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v13 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 13), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep15(db); err != nil {
		return err
	}
	if err = installStep16(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	return checkStep14State(db)
}
