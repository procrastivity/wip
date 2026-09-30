package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step12MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations'))) STRICT`}

var step12Schema = []schemaObject{
	{"m6_nodes", "table", `CREATE TABLE m6_nodes (
  domain_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('matter','stage','step')),
  repo_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  parent_id TEXT,
  locator TEXT NOT NULL,
  title TEXT NOT NULL,
  sort_key INTEGER NOT NULL,
  birth_event_id TEXT NOT NULL,
  last_event_id TEXT NOT NULL,
  tombstone_event_id TEXT,
  repair_required INTEGER NOT NULL CHECK(repair_required IN (0,1)),
  requested_locator TEXT,
  PRIMARY KEY(domain_id,node_id),
  CHECK((kind='matter' AND node_id=matter_id AND parent_id IS NULL AND sort_key=0) OR
        (kind!='matter' AND parent_id IS NOT NULL AND sort_key>0)),
  CHECK((repair_required=0 AND requested_locator IS NULL) OR (repair_required=1 AND kind='matter' AND requested_locator IS NOT NULL)),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,last_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,tombstone_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id)
) STRICT`},
	{"m6_matter_locator", "index", `CREATE UNIQUE INDEX m6_matter_locator ON m6_nodes(domain_id,repo_id,locator) WHERE kind='matter'`},
	{"m6_stage_locator", "index", `CREATE UNIQUE INDEX m6_stage_locator ON m6_nodes(domain_id,matter_id,locator) WHERE kind='stage'`},
	{"m6_step_locator", "index", `CREATE UNIQUE INDEX m6_step_locator ON m6_nodes(domain_id,matter_id,locator) WHERE kind='step'`},
	{"m6_live_sibling_order", "index", `CREATE UNIQUE INDEX m6_live_sibling_order ON m6_nodes(domain_id,parent_id,kind,sort_key) WHERE kind!='matter' AND tombstone_event_id IS NULL`},
	{"m6_nodes_identity_immutable", "trigger", `CREATE TRIGGER m6_nodes_identity_immutable BEFORE UPDATE OF domain_id,node_id,kind,repo_id,matter_id,parent_id,birth_event_id ON m6_nodes BEGIN SELECT RAISE(ABORT,'immutable M6 node identity'); END`},
	{"m6_nodes_no_delete", "trigger", `CREATE TRIGGER m6_nodes_no_delete BEFORE DELETE ON m6_nodes BEGIN SELECT RAISE(ABORT,'retained M6 node'); END`},
	{"matters_identity_immutable", "trigger", `CREATE TRIGGER matters_identity_immutable BEFORE UPDATE ON matters WHEN NEW.domain_id!=OLD.domain_id OR NEW.matter_id!=OLD.matter_id OR NEW.repo_id!=OLD.repo_id OR NEW.title!=OLD.title OR NEW.birth_event_id!=OLD.birth_event_id BEGIN SELECT RAISE(ABORT,'immutable Matter identity'); END`},
}

func installStep12(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TRIGGER matters_no_update`,
		`DROP TABLE schema_migrations`, step12MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step12Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,tombstone_event_id,repair_required,requested_locator)
		SELECT domain_id,matter_id,'matter',repo_id,matter_id,NULL,locator,title,0,birth_event_id,birth_event_id,NULL,0,NULL FROM matters`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO m6_nodes(domain_id,node_id,kind,repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,last_event_id,tombstone_event_id,repair_required,requested_locator)
		SELECT domain_id,step_id,'step',repo_id,matter_id,parent_id,locator,title,sort_key,birth_event_id,birth_event_id,NULL,0,NULL FROM steps`); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=11`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV10 retains a validated byte-equivalent backup before installing the
// event-backed M6 current-node projection. Ordinary opens never migrate.
func UpgradeV10(root string) error {
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
	if err = checkSchemaVersion(db, 10); err != nil {
		return fmt.Errorf("%w: v10 validation: %v", ErrInvalidStore, err)
	}
	if err = checkStep4State(db); err != nil {
		return fmt.Errorf("%w: v10 authority state: %v", ErrInvalidStore, err)
	}
	if err = checkStep5State(db); err != nil {
		return fmt.Errorf("%w: v10 claim state: %v", ErrInvalidStore, err)
	}
	if err = checkM5GenesisState(db); err != nil {
		return fmt.Errorf("%w: v10 genesis state: %v", ErrInvalidStore, err)
	}
	if err = checkBirthJournalState(db); err != nil {
		return fmt.Errorf("%w: v10 birth journal: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v10 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v10.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v10 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 10), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep12(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	return checkStep4State(db)
}

func syncFileAndDirectory(filePath, directoryPath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	if err = errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
