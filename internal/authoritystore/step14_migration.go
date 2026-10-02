package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step14MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations') OR (version = 12 AND name = 'step-7-authority-gate-config-projections') OR (version = 13 AND name = 'step-7-detached-gate-repair-admissions'))) STRICT`}

var step14Schema = []schemaObject{
	{"gate_exemption_repair_nonces", "table", `CREATE TABLE gate_exemption_repair_nonces (
		domain_id TEXT NOT NULL,
		nonce BLOB NOT NULL CHECK(length(nonce)=16),
		command_id TEXT NOT NULL,
		PRIMARY KEY(domain_id,nonce),
		UNIQUE(domain_id,command_id),
		FOREIGN KEY(domain_id) REFERENCES domains(domain_id)
	) STRICT`},
	{"gate_exemption_repair_nonces_immutable", "trigger", `CREATE TRIGGER gate_exemption_repair_nonces_immutable BEFORE UPDATE ON gate_exemption_repair_nonces BEGIN SELECT RAISE(ABORT,'immutable repair nonce'); END`},
	{"gate_exemption_repair_nonces_no_delete", "trigger", `CREATE TRIGGER gate_exemption_repair_nonces_no_delete BEFORE DELETE ON gate_exemption_repair_nonces BEGIN SELECT RAISE(ABORT,'immutable repair nonce'); END`},
	{"gate_exemption_repair_admissions", "table", `CREATE TABLE gate_exemption_repair_admissions (
  domain_id TEXT NOT NULL,
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  command BLOB NOT NULL CHECK(length(command)>0 AND length(command)<=1048576),
  authority_epoch INTEGER NOT NULL CHECK(authority_epoch>0),
  environment_id TEXT NOT NULL,
  environment_sequence INTEGER NOT NULL CHECK(environment_sequence>0),
  repo_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  gate TEXT NOT NULL CHECK(length(gate)>0),
  declaration_event_count INTEGER NOT NULL CHECK(declaration_event_count>0),
  declaration_event_id TEXT NOT NULL,
  declaration_prefix_digest TEXT NOT NULL,
  nonce BLOB NOT NULL CHECK(length(nonce)=16),
  proof BLOB NOT NULL CHECK(length(proof)>0 AND length(proof)<=1048576),
  verified_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id),
  UNIQUE(domain_id,environment_id,environment_sequence),
  FOREIGN KEY(domain_id) REFERENCES domains(domain_id),
  FOREIGN KEY(domain_id,environment_id) REFERENCES environments(domain_id,environment_id),
  FOREIGN KEY(domain_id,repo_id) REFERENCES repo_memberships(domain_id,repo_id),
  FOREIGN KEY(domain_id,nonce) REFERENCES gate_exemption_repair_nonces(domain_id,nonce),
  FOREIGN KEY(domain_id,command_id) REFERENCES gate_exemption_repair_nonces(domain_id,command_id),
  FOREIGN KEY(domain_id,declaration_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"gate_exemption_repair_admissions_immutable", "trigger", `CREATE TRIGGER gate_exemption_repair_admissions_immutable BEFORE UPDATE ON gate_exemption_repair_admissions BEGIN SELECT RAISE(ABORT,'immutable repair admission'); END`},
	{"gate_exemption_repair_admissions_no_delete", "trigger", `CREATE TRIGGER gate_exemption_repair_admissions_no_delete BEFORE DELETE ON gate_exemption_repair_admissions BEGIN SELECT RAISE(ABORT,'immutable repair admission'); END`},
	{"gate_exemption_repair_admissions_no_submission_collision", "trigger", `CREATE TRIGGER gate_exemption_repair_admissions_no_submission_collision BEFORE INSERT ON gate_exemption_repair_admissions WHEN EXISTS(SELECT 1 FROM submissions s WHERE s.domain_id=NEW.domain_id AND (s.command_id=NEW.command_id OR (s.environment_id=NEW.environment_id AND s.environment_sequence=NEW.environment_sequence))) BEGIN SELECT RAISE(ABORT,'repair admission conflicts with submission'); END`},
	{"submissions_no_repair_admission_collision", "trigger", `CREATE TRIGGER submissions_no_repair_admission_collision BEFORE INSERT ON submissions WHEN EXISTS(SELECT 1 FROM gate_exemption_repair_admissions a WHERE a.domain_id=NEW.domain_id AND (a.command_id=NEW.command_id OR (a.environment_id=NEW.environment_id AND a.environment_sequence=NEW.environment_sequence))) BEGIN SELECT RAISE(ABORT,'submission conflicts with repair admission'); END`},
}

func installStep14(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step14MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections'),(13,'step-7-detached-gate-repair-admissions')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step14Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=13`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV12 retains a validated v12 backup before installing the internal
// detached gate-repair admission and nonce-reservation tables. Ordinary opens
// never migrate, and the repair operation remains outside the catalogue.
func UpgradeV12(root string) error {
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
	if err = checkSchemaVersion(db, 12); err != nil {
		return fmt.Errorf("%w: v12 validation: %v", ErrInvalidStore, err)
	}
	for _, check := range []struct {
		name string
		fn   func(*sql.DB) error
	}{
		{"authority state", checkStep4State}, {"snapshot state", checkStep6State},
		{"claim state", checkStep5State}, {"genesis state", checkM5GenesisState},
		{"birth journal", checkBirthJournalState}, {"M6 node projection", checkStep12State},
		{"M6 gate/config projection", checkStep13State},
	} {
		if err = check.fn(db); err != nil {
			return fmt.Errorf("%w: v12 %s: %v", ErrInvalidStore, check.name, err)
		}
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v12 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v12.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v12 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 12), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep14(db); err != nil {
		return err
	}
	if err = installStep15(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	if err = checkStep14State(db); err != nil {
		return err
	}
	return checkStep15State(db)
}
