package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var m5GenesisMigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption'))) STRICT`}

var m5GenesisSchema = []schemaObject{
	{"m5_lab_genesis_grant_consumptions", "table", `CREATE TABLE m5_lab_genesis_grant_consumptions (
  nonce BLOB PRIMARY KEY NOT NULL CHECK(length(nonce) = 16),
  grant_digest TEXT NOT NULL UNIQUE CHECK(length(grant_digest) = 71),
  setup_signer_spki_digest TEXT NOT NULL CHECK(length(setup_signer_spki_digest) = 71),
  domain_id TEXT NOT NULL UNIQUE REFERENCES domains(domain_id),
  repo_id TEXT NOT NULL UNIQUE REFERENCES repo_memberships(repo_id),
  owner_root_spki_digest TEXT NOT NULL CHECK(length(owner_root_spki_digest) = 71),
  epoch INTEGER NOT NULL CHECK(epoch = 1),
  consumed_at TEXT NOT NULL
) STRICT`},
	{"m5_lab_genesis_grant_consumptions_immutable", "trigger", `CREATE TRIGGER m5_lab_genesis_grant_consumptions_immutable BEFORE UPDATE ON m5_lab_genesis_grant_consumptions BEGIN SELECT RAISE(ABORT,'immutable M5 lab genesis grant consumption'); END`},
	{"m5_lab_genesis_grant_consumptions_no_delete", "trigger", `CREATE TRIGGER m5_lab_genesis_grant_consumptions_no_delete BEFORE DELETE ON m5_lab_genesis_grant_consumptions BEGIN SELECT RAISE(ABORT,'immutable M5 lab genesis grant consumption'); END`},
}

func installM5Genesis(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, m5GenesisMigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range m5GenesisSchema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version = 6`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV5 installs the M5 test-lab genesis-grant consumption table only
// after retaining and validating an equivalent v5 backup. Ordinary opens do
// not upgrade an authority store.
func UpgradeV5(root string) error {
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
	if err = checkSchemaVersion(db, 5); err != nil {
		return fmt.Errorf("%w: v5 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v5 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v5.backup.db")
	created := false
	if _, statErr := os.Lstat(backup); errors.Is(statErr, os.ErrNotExist) {
		f, createErr := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return createErr
		}
		if createErr = f.Close(); createErr != nil {
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
		return fmt.Errorf("%w: backup: %v", ErrInvalidStore, err)
	}
	mode, create := "ro", false
	if created {
		mode, create = "rw", true
	}
	retained, err := connect(backup, mode, create)
	if err != nil {
		return fmt.Errorf("%w: backup open: %v", ErrInvalidStore, err)
	}
	err = errors.Join(checkSchemaVersion(retained, 5), retained.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	if err = sameStoreContents(db, backup); err != nil {
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
	if err = installM5Genesis(db); err != nil {
		return err
	}
	return checkSchemaVersion(db, 6)
}

func checkM5GenesisState(db *sql.DB) error {
	query := `SELECT nonce,grant_digest,setup_signer_spki_digest,domain_id,repo_id,owner_root_spki_digest,epoch,consumed_at
FROM m5_lab_genesis_grant_consumptions ORDER BY domain_id`
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	type consumption struct {
		nonce                                          []byte
		grantDigest, signerDigest, domain, repo, owner string
		epoch                                          int64
		consumedAt                                     string
	}
	var records []consumption
	for rows.Next() {
		var record consumption
		if err = rows.Scan(&record.nonce, &record.grantDigest, &record.signerDigest, &record.domain, &record.repo, &record.owner, &record.epoch, &record.consumedAt); err != nil {
			break
		}
		records = append(records, record)
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
	for _, record := range records {
		if len(record.nonce) != 16 || !validDigest(record.grantDigest) || !validDigest(record.signerDigest) || record.signerDigest == record.owner ||
			!ulid.MatchString(record.domain) || !ulid.MatchString(record.repo) || !validDigest(record.owner) || record.epoch != 1 {
			return ErrInvalidStore
		}
		if _, err = utcTime(record.consumedAt); err != nil {
			return ErrInvalidStore
		}
		var initial int64
		var owner, repoDomain string
		if err = db.QueryRow(`SELECT d.initial_epoch,d.owner_key_id,r.domain_id FROM domains d JOIN repo_memberships r ON r.repo_id=? WHERE d.domain_id=?`, record.repo, record.domain).Scan(&initial, &owner, &repoDomain); err != nil || initial != 1 || owner != record.owner || repoDomain != record.domain {
			return ErrInvalidStore
		}
	}
	return nil
}
