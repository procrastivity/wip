package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step3MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts'))) STRICT`}

var step3Schema = []schemaObject{
	{"artifact_keys", "table", `CREATE TABLE artifact_keys (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), epoch INTEGER NOT NULL CHECK(epoch > 0), generation INTEGER NOT NULL CHECK(generation > 0),
  key_id TEXT NOT NULL, public_key BLOB NOT NULL CHECK(length(public_key) = 32), certificate BLOB NOT NULL,
  not_before TEXT NOT NULL, not_after TEXT NOT NULL, fence BLOB, final_sequence INTEGER, final_digest TEXT,
  PRIMARY KEY(domain_id, epoch, generation), UNIQUE(domain_id, epoch, key_id),
  CHECK((fence IS NULL AND final_sequence IS NULL AND final_digest IS NULL) OR
        (fence IS NOT NULL AND final_sequence >= 0 AND (final_sequence = 0) = (final_digest IS NULL)))
) STRICT`},
	{"artifact_keys_identity_immutable", "trigger", `CREATE TRIGGER artifact_keys_identity_immutable BEFORE UPDATE OF domain_id, epoch, generation, key_id, public_key, certificate, not_before, not_after ON artifact_keys BEGIN SELECT RAISE(ABORT, 'immutable artifact key'); END`},
	{"artifact_keys_fence_once", "trigger", `CREATE TRIGGER artifact_keys_fence_once BEFORE UPDATE OF fence ON artifact_keys WHEN OLD.fence IS NOT NULL OR NEW.fence IS NULL BEGIN SELECT RAISE(ABORT, 'immutable artifact fence'); END`},
	{"authority_artifacts", "table", `CREATE TABLE authority_artifacts (
  domain_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation INTEGER NOT NULL, sequence INTEGER NOT NULL CHECK(sequence > 0),
  digest TEXT NOT NULL, predecessor TEXT, wrapper BLOB NOT NULL,
  PRIMARY KEY(domain_id, epoch, generation, sequence), UNIQUE(domain_id, epoch, digest),
  FOREIGN KEY(domain_id, epoch, generation) REFERENCES artifact_keys(domain_id, epoch, generation),
  CHECK((sequence = 1) = (predecessor IS NULL))
) STRICT`},
	{"authority_artifacts_contiguous", "trigger", `CREATE TRIGGER authority_artifacts_contiguous BEFORE INSERT ON authority_artifacts
WHEN (SELECT fence FROM artifact_keys WHERE domain_id=NEW.domain_id AND epoch=NEW.epoch AND generation=NEW.generation) IS NOT NULL
 OR NEW.sequence != 1 + coalesce((SELECT max(sequence) FROM authority_artifacts WHERE domain_id=NEW.domain_id AND epoch=NEW.epoch AND generation=NEW.generation),0)
 OR coalesce(NEW.predecessor,'') != coalesce((SELECT digest FROM authority_artifacts WHERE domain_id=NEW.domain_id AND epoch=NEW.epoch AND generation=NEW.generation ORDER BY sequence DESC LIMIT 1),'')
BEGIN SELECT RAISE(ABORT, 'fenced or noncontiguous artifact'); END`},
	{"authority_artifacts_immutable", "trigger", `CREATE TRIGGER authority_artifacts_immutable BEFORE UPDATE ON authority_artifacts BEGIN SELECT RAISE(ABORT, 'immutable artifact'); END`},
	{"authority_artifacts_no_delete", "trigger", `CREATE TRIGGER authority_artifacts_no_delete BEFORE DELETE ON authority_artifacts BEGIN SELECT RAISE(ABORT, 'immutable artifact'); END`},
	{"environment_cas", "table", `CREATE TABLE environment_cas (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), epoch INTEGER NOT NULL CHECK(epoch > 0), generation INTEGER NOT NULL CHECK(generation > 0),
  key_id TEXT NOT NULL, certificate BLOB NOT NULL, delegation BLOB NOT NULL, not_before TEXT NOT NULL, not_after TEXT NOT NULL,
  fence BLOB, PRIMARY KEY(domain_id, epoch, generation), UNIQUE(domain_id, epoch, key_id)
) STRICT`},
	{"environment_ca_identity_immutable", "trigger", `CREATE TRIGGER environment_ca_identity_immutable BEFORE UPDATE OF domain_id, epoch, generation, key_id, certificate, delegation, not_before, not_after ON environment_cas BEGIN SELECT RAISE(ABORT, 'immutable CA delegation'); END`},
	{"environment_ca_fence_once", "trigger", `CREATE TRIGGER environment_ca_fence_once BEFORE UPDATE OF fence ON environment_cas WHEN OLD.fence IS NOT NULL OR NEW.fence IS NULL BEGIN SELECT RAISE(ABORT, 'immutable CA fence'); END`},
	{"environments", "table", `CREATE TABLE environments (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), environment_id TEXT NOT NULL,
  epoch INTEGER NOT NULL CHECK(epoch > 0), generation INTEGER NOT NULL CHECK(generation > 0),
  sequence_head INTEGER NOT NULL DEFAULT 0 CHECK(sequence_head >= 0),
  PRIMARY KEY(domain_id, environment_id)
) STRICT`},
	{"environment_identity_immutable", "trigger", `CREATE TRIGGER environment_identity_immutable BEFORE UPDATE OF domain_id, environment_id, epoch ON environments BEGIN SELECT RAISE(ABORT, 'immutable Environment identity'); END`},
	{"environment_generation_contiguous", "trigger", `CREATE TRIGGER environment_generation_contiguous BEFORE UPDATE OF generation ON environments WHEN NEW.generation != OLD.generation + 1 BEGIN SELECT RAISE(ABORT, 'noncontiguous certificate generation'); END`},
	{"environment_sequence_no_advance", "trigger", `CREATE TRIGGER environment_sequence_no_advance BEFORE UPDATE OF sequence_head ON environments BEGIN SELECT RAISE(ABORT, 'sequence requires owning submission or return acknowledgment'); END`},
	{"environment_no_delete", "trigger", `CREATE TRIGGER environment_no_delete BEFORE DELETE ON environments BEGIN SELECT RAISE(ABORT, 'immutable Environment'); END`},
	{"environment_certificates", "table", `CREATE TABLE environment_certificates (
  domain_id TEXT NOT NULL, environment_id TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation > 0),
  ca_epoch INTEGER NOT NULL, ca_generation INTEGER NOT NULL, serial TEXT NOT NULL, spki_digest TEXT NOT NULL,
  certificate BLOB NOT NULL, ca_certificate BLOB NOT NULL, not_before TEXT NOT NULL, not_after TEXT NOT NULL,
  revoked_at TEXT, PRIMARY KEY(domain_id, environment_id, generation), UNIQUE(domain_id, ca_epoch, ca_generation, serial),
  FOREIGN KEY(domain_id, environment_id) REFERENCES environments(domain_id, environment_id),
  FOREIGN KEY(domain_id, ca_epoch, ca_generation) REFERENCES environment_cas(domain_id, epoch, generation)
) STRICT`},
	{"environment_cert_identity_immutable", "trigger", `CREATE TRIGGER environment_cert_identity_immutable BEFORE UPDATE OF domain_id, environment_id, generation, ca_epoch, ca_generation, serial, spki_digest, certificate, ca_certificate, not_before, not_after ON environment_certificates BEGIN SELECT RAISE(ABORT, 'immutable certificate'); END`},
	{"environment_cert_revocation_once", "trigger", `CREATE TRIGGER environment_cert_revocation_once BEFORE UPDATE OF revoked_at ON environment_certificates WHEN OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL BEGIN SELECT RAISE(ABORT, 'immutable certificate revocation'); END`},
	{"environment_cert_no_delete", "trigger", `CREATE TRIGGER environment_cert_no_delete BEFORE DELETE ON environment_certificates BEGIN SELECT RAISE(ABORT, 'immutable certificate'); END`},
	{"enrollment_consumptions", "table", `CREATE TABLE enrollment_consumptions (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id), grant_id TEXT NOT NULL, nonce BLOB NOT NULL CHECK(length(nonce) = 16),
  grant_digest TEXT NOT NULL, scope TEXT NOT NULL, csr_digest TEXT NOT NULL, environment_id TEXT NOT NULL, generation INTEGER NOT NULL,
  PRIMARY KEY(domain_id, grant_id), UNIQUE(domain_id, nonce),
  FOREIGN KEY(domain_id, environment_id, generation) REFERENCES environment_certificates(domain_id, environment_id, generation)
) STRICT`},
	{"enrollment_consumptions_immutable", "trigger", `CREATE TRIGGER enrollment_consumptions_immutable BEFORE UPDATE ON enrollment_consumptions BEGIN SELECT RAISE(ABORT, 'immutable consumption'); END`},
	{"enrollment_consumptions_no_delete", "trigger", `CREATE TRIGGER enrollment_consumptions_no_delete BEFORE DELETE ON enrollment_consumptions BEGIN SELECT RAISE(ABORT, 'immutable consumption'); END`},
	{"environment_renewals", "table", `CREATE TABLE environment_renewals (
  domain_id TEXT NOT NULL, environment_id TEXT NOT NULL, predecessor TEXT NOT NULL, csr_digest TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 1),
  PRIMARY KEY(domain_id, environment_id, predecessor, csr_digest),
  UNIQUE(domain_id, environment_id, predecessor),
  FOREIGN KEY(domain_id, environment_id, generation) REFERENCES environment_certificates(domain_id, environment_id, generation)
) STRICT`},
	{"environment_renewals_immutable", "trigger", `CREATE TRIGGER environment_renewals_immutable BEFORE UPDATE ON environment_renewals BEGIN SELECT RAISE(ABORT, 'immutable renewal'); END`},
	{"environment_renewals_no_delete", "trigger", `CREATE TRIGGER environment_renewals_no_delete BEFORE DELETE ON environment_renewals BEGIN SELECT RAISE(ABORT, 'immutable renewal'); END`},
}

func installStep3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(step3MigrationMarker.sql); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES (1, 'baseline'), (2, 'environment-and-artifacts')`); err != nil {
		return err
	}
	for _, object := range step3Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV1 is the only v1-to-v2 path. It retains a fully validated v1 backup
// before the atomic SQL migration. A failed/interrupted migration remains v1
// (and may be retried); a committed migration opens only as v2. An incomplete
// backup is never silently overwritten.
func UpgradeV1(root string) error {
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
		return fmt.Errorf("%w: database: %v", ErrInvalidStore, err)
	}
	db, err := connect(file, "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 1); err != nil {
		return fmt.Errorf("%w: v1 validation: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v1.backup.db")
	if _, statErr := os.Lstat(backup); errors.Is(statErr, os.ErrNotExist) {
		// An empty destination is permitted by VACUUM INTO. Create it
		// privately first so a crash cannot expose a world-readable backup
		// between SQLite creation and a later chmod.
		f, createErr := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return createErr
		}
		if err = f.Close(); err != nil {
			return err
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
	// VACUUM INTO produces a rollback-journal snapshot even when its source
	// uses WAL. Make the backup independently reopenable as an exact v1 root.
	b, err := connect(backup, "rw", true)
	if err != nil {
		return err
	}
	err = checkSchemaVersion(b, 1)
	err = errors.Join(err, b.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	f, err := os.Open(backup)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return err
	}
	if err = installStep3(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return fmt.Errorf("%w: upgraded schema: %v", ErrInvalidStore, err)
	}
	return nil
}
