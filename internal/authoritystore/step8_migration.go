package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step8MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection'))) STRICT`}

var step8Schema = []schemaObject{
	{"implicit_birth_claims", "table", `CREATE TABLE implicit_birth_claims (
  domain_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  claim_epoch INTEGER NOT NULL CHECK(claim_epoch=1),
  owner_environment_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  birth_command_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,matter_id),
  UNIQUE(domain_id,birth_command_id),
  FOREIGN KEY(domain_id,matter_id) REFERENCES matters(domain_id,matter_id),
  FOREIGN KEY(domain_id,birth_command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,owner_environment_id) REFERENCES environments(domain_id,environment_id),
  FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id)
) STRICT`},
	{"implicit_birth_claims_immutable", "trigger", `CREATE TRIGGER implicit_birth_claims_immutable BEFORE UPDATE ON implicit_birth_claims BEGIN SELECT RAISE(ABORT,'immutable implicit birth claim'); END`},
	{"implicit_birth_claims_no_delete", "trigger", `CREATE TRIGGER implicit_birth_claims_no_delete BEFORE DELETE ON implicit_birth_claims BEGIN SELECT RAISE(ABORT,'immutable implicit birth claim'); END`},
	{"steps", "table", `CREATE TABLE steps (
  domain_id TEXT NOT NULL,
  step_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  parent_id TEXT NOT NULL,
  locator TEXT NOT NULL,
  title TEXT NOT NULL,
  sort_key INTEGER NOT NULL,
  state TEXT NOT NULL CHECK(state='planned'),
  birth_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,step_id),
  UNIQUE(domain_id,matter_id,locator),
  UNIQUE(domain_id,parent_id,sort_key),
  CHECK(parent_id=matter_id),
  FOREIGN KEY(domain_id,matter_id) REFERENCES matters(domain_id,matter_id),
  FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"steps_immutable", "trigger", `CREATE TRIGGER steps_immutable BEFORE UPDATE ON steps BEGIN SELECT RAISE(ABORT,'immutable Step 8 projection'); END`},
	{"steps_no_delete", "trigger", `CREATE TRIGGER steps_no_delete BEFORE DELETE ON steps BEGIN SELECT RAISE(ABORT,'immutable Step 8 projection'); END`},
}

func installStep8(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step8MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step8Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO implicit_birth_claims(domain_id,matter_id,claim_epoch,owner_environment_id,repo_id,birth_command_id)
		SELECT m.domain_id,m.matter_id,1,s.environment_id,m.repo_id,e.command_id
		FROM matters m JOIN authority_events e ON e.domain_id=m.domain_id AND e.event_id=m.birth_event_id
		JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id AND s.state='terminal'`); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version = 7`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV6 retains a validated, byte-equivalent backup before adding the
// Step projection. Ordinary opens never migrate an existing store.
func UpgradeV6(root string) error {
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
	if err = checkSchemaVersion(db, 6); err != nil {
		return fmt.Errorf("%w: v6 validation: %v", ErrInvalidStore, err)
	}
	if err = checkStep4State(db); err != nil {
		return fmt.Errorf("%w: v6 command state: %v", ErrInvalidStore, err)
	}
	if err = checkStep5State(db); err != nil {
		return fmt.Errorf("%w: v6 claim state: %v", ErrInvalidStore, err)
	}
	if err = checkM5GenesisState(db); err != nil {
		return fmt.Errorf("%w: v6 genesis state: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v6 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v6.backup.db")
	created := false
	if _, statErr := os.Lstat(backup); errors.Is(statErr, os.ErrNotExist) {
		file, createErr := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return createErr
		}
		if createErr = file.Close(); createErr != nil {
			return createErr
		}
		if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
			return err
		}
		created = true
	} else if statErr != nil {
		return statErr
	}
	if err = regularFile(backup); err != nil {
		return err
	}
	mode, create := "ro", false
	if created {
		mode, create = "rw", true
	}
	retained, err := connect(backup, mode, create)
	if err != nil {
		return err
	}
	err = errors.Join(checkSchemaVersion(retained, 6), retained.Close())
	if err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	file, err := os.Open(backup)
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
	if err = installStep8(db); err != nil {
		return err
	}
	if err = checkSchemaVersion(db, 7); err != nil {
		return err
	}
	if err = checkStep4State(db); err != nil {
		return err
	}
	if err = checkStep5State(db); err != nil {
		return err
	}
	if err = checkM5GenesisState(db); err != nil {
		return err
	}
	return checkBlobFiles(db, filepath.Join(path, "blobs"))
}
