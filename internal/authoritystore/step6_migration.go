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

var step6MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer'))) STRICT`}

var step6Schema = []schemaObject{
	{"blob_products", "table", `CREATE TABLE blob_products (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), digest TEXT NOT NULL, byte_length INTEGER NOT NULL CHECK(byte_length BETWEEN 0 AND 1099511627776),
  verified_offset INTEGER NOT NULL DEFAULT 0 CHECK(verified_offset >= 0 AND verified_offset <= byte_length),
  verified INTEGER NOT NULL DEFAULT 0 CHECK(verified IN (0,1)), expires_at INTEGER NOT NULL CHECK(expires_at > 0),
  PRIMARY KEY(domain_id,digest), CHECK(verified=0 OR verified_offset=byte_length)
) STRICT`},
	{"blob_chunks", "table", `CREATE TABLE blob_chunks (
  domain_id TEXT NOT NULL, digest TEXT NOT NULL, offset INTEGER NOT NULL CHECK(offset >= 0), chunk_hash BLOB NOT NULL CHECK(length(chunk_hash)=32), byte_length INTEGER NOT NULL CHECK(byte_length BETWEEN 1 AND 65536),
  PRIMARY KEY(domain_id,digest,offset), FOREIGN KEY(domain_id,digest) REFERENCES blob_products(domain_id,digest) ON DELETE CASCADE
) STRICT`},
	{"blob_chunks_immutable", "trigger", `CREATE TRIGGER blob_chunks_immutable BEFORE UPDATE ON blob_chunks BEGIN SELECT RAISE(ABORT,'immutable blob chunk'); END`},
	{"blob_references", "table", `CREATE TABLE blob_references (
  domain_id TEXT NOT NULL, digest TEXT NOT NULL, first_position INTEGER NOT NULL CHECK(first_position > 0),
  PRIMARY KEY(domain_id,digest), FOREIGN KEY(domain_id,digest) REFERENCES blob_products(domain_id,digest),
  FOREIGN KEY(domain_id,first_position) REFERENCES authority_events(domain_id,position)
) STRICT`},
	{"blob_references_immutable", "trigger", `CREATE TRIGGER blob_references_immutable BEFORE UPDATE ON blob_references BEGIN SELECT RAISE(ABORT,'immutable blob reference'); END`},
	{"blob_references_no_delete", "trigger", `CREATE TRIGGER blob_references_no_delete BEFORE DELETE ON blob_references BEGIN SELECT RAISE(ABORT,'immutable blob reference'); END`},
	{"snapshots", "table", `CREATE TABLE snapshots (
  snapshot_id TEXT PRIMARY KEY, domain_id TEXT NOT NULL REFERENCES domains(domain_id), epoch INTEGER NOT NULL CHECK(epoch > 0),
  event_count INTEGER NOT NULL CHECK(event_count >= 0), event_id TEXT, prefix_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL, expires_at INTEGER NOT NULL CHECK(expires_at > 0),
  CHECK((event_count=0)=(event_id IS NULL))
) STRICT`},
	{"snapshots_immutable", "trigger", `CREATE TRIGGER snapshots_immutable BEFORE UPDATE ON snapshots BEGIN SELECT RAISE(ABORT,'immutable snapshot'); END`},
	{"snapshot_entries", "table", `CREATE TABLE snapshot_entries (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(snapshot_id) ON DELETE CASCADE, digest TEXT NOT NULL, byte_length INTEGER NOT NULL,
  requirement TEXT NOT NULL CHECK(requirement IN ('lazy','pin-before-use')),
  PRIMARY KEY(snapshot_id,digest)
) STRICT`},
	{"snapshot_entries_immutable", "trigger", `CREATE TRIGGER snapshot_entries_immutable BEFORE UPDATE ON snapshot_entries BEGIN SELECT RAISE(ABORT,'immutable manifest entry'); END`},
	{"snapshot_items", "table", `CREATE TABLE snapshot_items (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(snapshot_id) ON DELETE CASCADE, ordinal INTEGER NOT NULL CHECK(ordinal >= 0),
  matter_id TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY(snapshot_id,ordinal), UNIQUE(snapshot_id,matter_id)
) STRICT`},
	{"snapshot_items_immutable", "trigger", `CREATE TRIGGER snapshot_items_immutable BEFORE UPDATE ON snapshot_items BEGIN SELECT RAISE(ABORT,'immutable snapshot item'); END`},
	{"transfers", "table", `CREATE TABLE transfers (
  transfer_id TEXT PRIMARY KEY, snapshot_id TEXT NOT NULL REFERENCES snapshots(snapshot_id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('seed','pull')), store_schema TEXT NOT NULL,
  start_count INTEGER NOT NULL CHECK(start_count >= 0), start_event_id TEXT, start_digest TEXT NOT NULL,
  next_event INTEGER NOT NULL CHECK(next_event >= 0), next_entry INTEGER NOT NULL CHECK(next_entry >= 0), expires_at INTEGER NOT NULL CHECK(expires_at > 0),
  CHECK((start_count=0)=(start_event_id IS NULL))
) STRICT`},
	{"transfer_scope_immutable", "trigger", `CREATE TRIGGER transfer_scope_immutable BEFORE UPDATE OF transfer_id,snapshot_id,kind,store_schema,start_count,start_event_id,start_digest,expires_at ON transfers BEGIN SELECT RAISE(ABORT,'immutable transfer scope'); END`},
	{"transfer_progress", "trigger", `CREATE TRIGGER transfer_progress BEFORE UPDATE OF next_event,next_entry ON transfers WHEN NEW.next_event < OLD.next_event OR NEW.next_event > OLD.next_event+1 OR NEW.next_entry < OLD.next_entry OR NEW.next_entry > OLD.next_entry+1 OR (NEW.next_event != OLD.next_event AND NEW.next_entry != OLD.next_entry) BEGIN SELECT RAISE(ABORT,'noncontiguous transfer'); END`},
	{"transfer_secret", "table", `CREATE TABLE transfer_secret (purpose TEXT PRIMARY KEY CHECK(purpose='transfer'), key BLOB NOT NULL CHECK(length(key)=32)) STRICT`},
}

func installStep6(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DROP TABLE schema_migrations`, step6MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer')`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, object := range step6Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return err
		}
	}
	key, err := newSecret()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO transfer_secret VALUES('transfer',?)`, key); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version = 4`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV3 requires an exact closed v3 store and retains a validated,
// byte-equivalent SQLite backup before the atomic migration. Ordinary open
// refuses v3, including an interrupted or incomplete upgrade.
func UpgradeV3(root string) error {
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
	if err = checkSchemaVersion(db, 3); err != nil {
		return fmt.Errorf("%w: v3 validation: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v3.backup.db")
	createdBackup := false
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
		createdBackup = true
	} else if statErr != nil {
		return statErr
	}
	if err = regularFile(backup); err != nil {
		return fmt.Errorf("%w: backup: %v", ErrInvalidStore, err)
	}
	// Only a newly created VACUUM product may be normalized to WAL. An
	// existing backup is caller evidence: verification must not rewrite it.
	mode, create := "ro", false
	if createdBackup {
		mode, create = "rw", true
	}
	b, err := connect(backup, mode, create)
	if err != nil {
		return fmt.Errorf("%w: backup open: %v", ErrInvalidStore, err)
	}
	err = errors.Join(checkSchemaVersion(b, 3), b.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	if err = sameV3StoreContents(db, backup); err != nil {
		return fmt.Errorf("%w: backup differs: %v", ErrInvalidStore, err)
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
	if err = initBlobDir(path); err != nil {
		return err
	}
	if err = installStep6(db); err != nil {
		return err
	}
	return checkSchema(db)
}

func sameV3StoreContents(db *sql.DB, backup string) error {
	u := url.URL{Scheme: "file", Path: backup}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	if _, err := db.Exec(`ATTACH DATABASE ? AS retained_v3_backup`, u.String()); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`DETACH DATABASE retained_v3_backup`) }()
	rows, err := db.Query(`SELECT name FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		tables = append(tables, name)
	}
	if err == nil {
		err = rows.Err()
	}
	if e := rows.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	for _, table := range tables {
		name := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var differs int
		query := fmt.Sprintf(`SELECT EXISTS(SELECT * FROM main.%s EXCEPT SELECT * FROM retained_v3_backup.%s) OR EXISTS(SELECT * FROM retained_v3_backup.%s EXCEPT SELECT * FROM main.%s)`, name, name, name, name)
		if err = db.QueryRow(query).Scan(&differs); err != nil {
			return err
		}
		if differs != 0 {
			return fmt.Errorf("table %s differs", table)
		}
	}
	return nil
}
