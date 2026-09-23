package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var step4MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts'))) STRICT`}

var step4Schema = []schemaObject{
	{"submissions", "table", `CREATE TABLE submissions (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), command_id TEXT NOT NULL, request_hash TEXT NOT NULL,
  command BLOB NOT NULL, epoch INTEGER NOT NULL, environment_id TEXT NOT NULL, environment_sequence INTEGER NOT NULL CHECK(environment_sequence > 0),
  operation_name TEXT NOT NULL, operation_version INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('submitted','terminal')),
  PRIMARY KEY(domain_id,command_id), UNIQUE(domain_id,environment_id,environment_sequence),
  FOREIGN KEY(domain_id,environment_id) REFERENCES environments(domain_id,environment_id)
) STRICT`},
	{"submissions_immutable", "trigger", `CREATE TRIGGER submissions_immutable BEFORE UPDATE ON submissions WHEN NEW.domain_id!=OLD.domain_id OR NEW.command_id!=OLD.command_id OR NEW.request_hash!=OLD.request_hash OR NEW.command!=OLD.command OR NEW.epoch!=OLD.epoch OR NEW.environment_id!=OLD.environment_id OR NEW.environment_sequence!=OLD.environment_sequence OR NEW.operation_name!=OLD.operation_name OR NEW.operation_version!=OLD.operation_version OR OLD.state!='submitted' OR NEW.state!='terminal' OR NOT EXISTS(SELECT 1 FROM terminal_receipts r WHERE r.domain_id=OLD.domain_id AND r.command_id=OLD.command_id) BEGIN SELECT RAISE(ABORT,'immutable submission'); END`},
	{"submissions_no_delete", "trigger", `CREATE TRIGGER submissions_no_delete BEFORE DELETE ON submissions BEGIN SELECT RAISE(ABORT,'immutable submission'); END`},
	{"terminal_receipts", "table", `CREATE TABLE terminal_receipts (
  domain_id TEXT NOT NULL, command_id TEXT NOT NULL, receipt BLOB NOT NULL, wrapper BLOB NOT NULL,
  artifact_epoch INTEGER NOT NULL, artifact_generation INTEGER NOT NULL, artifact_sequence INTEGER NOT NULL,
  first_position INTEGER, last_position INTEGER, result_code TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id), UNIQUE(domain_id,artifact_epoch,artifact_generation,artifact_sequence),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,artifact_epoch,artifact_generation,artifact_sequence) REFERENCES authority_artifacts(domain_id,epoch,generation,sequence),
  CHECK((first_position IS NULL) = (last_position IS NULL))
) STRICT`},
	{"terminal_receipts_immutable", "trigger", `CREATE TRIGGER terminal_receipts_immutable BEFORE UPDATE ON terminal_receipts BEGIN SELECT RAISE(ABORT,'immutable receipt'); END`},
	{"terminal_receipts_no_delete", "trigger", `CREATE TRIGGER terminal_receipts_no_delete BEFORE DELETE ON terminal_receipts BEGIN SELECT RAISE(ABORT,'immutable receipt'); END`},
	{"authority_events", "table", `CREATE TABLE authority_events (
  domain_id TEXT NOT NULL, position INTEGER NOT NULL CHECK(position > 0), event_id TEXT NOT NULL,
  command_id TEXT NOT NULL, record BLOB NOT NULL, prefix_digest TEXT NOT NULL,
  PRIMARY KEY(domain_id,position), UNIQUE(domain_id,event_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id)
) STRICT`},
	{"authority_events_immutable", "trigger", `CREATE TRIGGER authority_events_immutable BEFORE UPDATE ON authority_events BEGIN SELECT RAISE(ABORT,'immutable event'); END`},
	{"authority_events_no_delete", "trigger", `CREATE TRIGGER authority_events_no_delete BEFORE DELETE ON authority_events BEGIN SELECT RAISE(ABORT,'immutable event'); END`},
	{"matters", "table", `CREATE TABLE matters (
  domain_id TEXT NOT NULL, matter_id TEXT NOT NULL, repo_id TEXT NOT NULL, locator TEXT NOT NULL, title TEXT NOT NULL, birth_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,matter_id), UNIQUE(domain_id,repo_id,locator),
  FOREIGN KEY(domain_id) REFERENCES domains(domain_id), FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id)
) STRICT`},
	{"matters_no_update", "trigger", `CREATE TRIGGER matters_no_update BEFORE UPDATE ON matters BEGIN SELECT RAISE(ABORT,'Step 4 projection immutable'); END`},
	{"matters_no_delete", "trigger", `CREATE TRIGGER matters_no_delete BEFORE DELETE ON matters BEGIN SELECT RAISE(ABORT,'Step 4 projection immutable'); END`},
	{"environment_sequence_terminal", "trigger", `CREATE TRIGGER environment_sequence_terminal BEFORE UPDATE OF sequence_head ON environments
WHEN NEW.sequence_head != OLD.sequence_head + 1 OR NOT EXISTS(
 SELECT 1 FROM submissions s JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id
 WHERE s.domain_id=NEW.domain_id AND s.environment_id=NEW.environment_id AND s.environment_sequence=NEW.sequence_head AND s.state='terminal')
BEGIN SELECT RAISE(ABORT,'sequence requires terminal acknowledgment'); END`},
}

func installStep4(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DROP TRIGGER environment_sequence_no_advance`, `DROP TABLE schema_migrations`, step4MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts')`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, object := range step4Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version = 3`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV2 explicitly upgrades a closed, exact v2 store after retaining an
// independently verified, fsynced backup. Ordinary open never migrates.
func UpgradeV2(root string) error {
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
	file := filepath.Join(path, "authority.db")
	if err = regularFile(file); err != nil {
		return err
	}
	db, err := connect(file, "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 2); err != nil {
		return fmt.Errorf("%w: v2 validation: %v", ErrInvalidStore, err)
	}
	var artifacts int
	if err = db.QueryRow(`SELECT count(*) FROM authority_artifacts`).Scan(&artifacts); err != nil || artifacts != 0 {
		return fmt.Errorf("%w: v2 contains products without Step 4 terminal owners: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v2.backup.db")
	if _, statErr := os.Lstat(backup); errors.Is(statErr, os.ErrNotExist) {
		f, e := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		if _, err = db.Exec(`VACUUM INTO ?`, backup); err != nil {
			return err
		}
	} else if statErr != nil {
		return statErr
	}
	if err = regularFile(backup); err != nil {
		return fmt.Errorf("%w: backup: %v", ErrInvalidStore, err)
	}
	b, err := connect(backup, "rw", true)
	if err != nil {
		return err
	}
	err = errors.Join(checkSchemaVersion(b, 2), b.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	if err = sameV2StoreContents(db, backup); err != nil {
		return fmt.Errorf("%w: backup does not match source: %v", ErrInvalidStore, err)
	}
	f, err := os.Open(backup)
	if err != nil {
		return err
	}
	if err = errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
		return err
	}
	if err = installStep4(db); err != nil {
		return err
	}
	return checkSchema(db)
}

// sameV2StoreContents proves that an existing schema-valid backup is the
// retained snapshot of this source, rather than an unrelated v2 store.
func sameV2StoreContents(db *sql.DB, backup string) error {
	u := url.URL{Scheme: "file", Path: backup}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	if _, err := db.Exec(`ATTACH DATABASE ? AS retained_v2_backup`, u.String()); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`DETACH DATABASE retained_v2_backup`) }()
	rows, err := db.Query(`SELECT name FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			break
		}
		tables = append(tables, table)
	}
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, table := range tables {
		name := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var differs int
		query := fmt.Sprintf(`SELECT EXISTS(SELECT * FROM main.%s EXCEPT SELECT * FROM retained_v2_backup.%s) OR EXISTS(SELECT * FROM retained_v2_backup.%s EXCEPT SELECT * FROM main.%s)`, name, name, name, name)
		if err = db.QueryRow(query).Scan(&differs); err != nil {
			return err
		}
		if differs != 0 {
			return fmt.Errorf("table %s differs", table)
		}
	}
	return nil
}
