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

var step7MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'continuity-and-migration-proof'))) STRICT`}

var step7Schema = []schemaObject{
	{"authority_continuity", "table", `CREATE TABLE authority_continuity (
  domain_id TEXT PRIMARY KEY REFERENCES domains(domain_id) ON DELETE RESTRICT,
  admission_closed INTEGER NOT NULL DEFAULT 0 CHECK(admission_closed IN (0,1)),
  closed_epoch INTEGER,
  rollback_fenced INTEGER NOT NULL DEFAULT 0 CHECK(rollback_fenced IN (0,1)),
  rollback_command_id TEXT,
  rollback_request_hash TEXT,
  rollback_epoch INTEGER,
  FOREIGN KEY(domain_id,rollback_command_id) REFERENCES submissions(domain_id,command_id),
  CHECK((admission_closed=0 AND closed_epoch IS NULL) OR (admission_closed=1 AND closed_epoch>0)),
  CHECK((rollback_fenced=0 AND rollback_command_id IS NULL AND rollback_request_hash IS NULL AND rollback_epoch IS NULL) OR (rollback_fenced=1 AND rollback_command_id IS NOT NULL AND rollback_request_hash LIKE 'sha256:%' AND rollback_epoch>0))
) STRICT`},
	{"continuity_state_monotone", "trigger", `CREATE TRIGGER continuity_state_monotone BEFORE UPDATE ON authority_continuity
WHEN NEW.domain_id!=OLD.domain_id OR NEW.admission_closed<OLD.admission_closed OR (OLD.closed_epoch IS NOT NULL AND (NEW.closed_epoch IS NULL OR NEW.closed_epoch<OLD.closed_epoch)) OR (NEW.closed_epoch IS NOT OLD.closed_epoch AND NEW.closed_epoch!=(SELECT active_epoch FROM domains WHERE domain_id=NEW.domain_id)) OR NEW.rollback_fenced<OLD.rollback_fenced OR (OLD.rollback_fenced=1 AND (NEW.rollback_command_id IS NOT OLD.rollback_command_id OR NEW.rollback_request_hash IS NOT OLD.rollback_request_hash OR NEW.rollback_epoch IS NOT OLD.rollback_epoch)) OR (NEW.rollback_fenced=1 AND (NOT EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=OLD.domain_id AND p.kind='migration-seal') OR NOT EXISTS(SELECT 1 FROM submissions s WHERE s.domain_id=NEW.domain_id AND s.command_id=NEW.rollback_command_id AND s.request_hash=NEW.rollback_request_hash AND s.epoch=NEW.rollback_epoch)))
BEGIN SELECT RAISE(ABORT,'continuity state is monotone'); END`},
	{"continuity_state_no_delete", "trigger", `CREATE TRIGGER continuity_state_no_delete BEFORE DELETE ON authority_continuity BEGIN SELECT RAISE(ABORT,'immutable continuity state'); END`},
	{"continuity_state_on_domain", "trigger", `CREATE TRIGGER continuity_state_on_domain AFTER INSERT ON domains BEGIN INSERT INTO authority_continuity(domain_id) VALUES(NEW.domain_id); END`},
	{"continuity_products", "table", `CREATE TABLE continuity_products (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT,
  kind TEXT NOT NULL CHECK(kind IN ('activation-intent','bundle-manifest','authority-relinquishment','owner-attestation','authority-activation','migration-authorization','migration-proof','migration-seal')),
  authority_epoch INTEGER NOT NULL CHECK(authority_epoch>0),
  artifact BLOB NOT NULL, digest TEXT NOT NULL,
  PRIMARY KEY(domain_id,kind,authority_epoch)
) STRICT`},
	{"continuity_products_immutable", "trigger", `CREATE TRIGGER continuity_products_immutable BEFORE UPDATE ON continuity_products BEGIN SELECT RAISE(ABORT,'immutable continuity product'); END`},
	{"continuity_products_no_delete", "trigger", `CREATE TRIGGER continuity_products_no_delete BEFORE DELETE ON continuity_products BEGIN SELECT RAISE(ABORT,'immutable continuity product'); END`},
	{"continuity_single_migration", "index", `CREATE UNIQUE INDEX continuity_single_migration ON continuity_products(domain_id,kind) WHERE kind IN ('migration-authorization','migration-proof','migration-seal')`},
	{"owner_nonce_uses", "table", `CREATE TABLE owner_nonce_uses (
  domain_id TEXT NOT NULL REFERENCES domains(domain_id) ON DELETE RESTRICT,
  nonce TEXT NOT NULL CHECK(length(nonce)=32 AND nonce NOT GLOB '*[^0-9a-f]*'),
  action TEXT NOT NULL CHECK(action IN ('claim-stand-down','activation-intent','planned-handoff','disaster-restore','migration-authorize','migration-seal')),
  artifact_digest TEXT NOT NULL, consumed_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,nonce)
) STRICT`},
	{"owner_nonce_uses_immutable", "trigger", `CREATE TRIGGER owner_nonce_uses_immutable BEFORE UPDATE ON owner_nonce_uses BEGIN SELECT RAISE(ABORT,'immutable owner nonce use'); END`},
	{"owner_nonce_uses_no_delete", "trigger", `CREATE TRIGGER owner_nonce_uses_no_delete BEFORE DELETE ON owner_nonce_uses BEGIN SELECT RAISE(ABORT,'immutable owner nonce use'); END`},
	{"continuity_submission_admission", "trigger", `CREATE TRIGGER continuity_submission_admission BEFORE INSERT ON submissions
WHEN EXISTS(SELECT 1 FROM authority_continuity c JOIN domains d USING(domain_id) WHERE c.domain_id=NEW.domain_id AND ((c.admission_closed=1 AND c.closed_epoch=d.active_epoch) OR (EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=NEW.domain_id AND p.kind='migration-authorization' AND p.authority_epoch=d.active_epoch) AND NOT EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=NEW.domain_id AND p.kind='migration-seal' AND p.authority_epoch=d.active_epoch))))
BEGIN SELECT RAISE(ABORT,'authority admission closed'); END`},
	{"continuity_submission_rollback_fence", "trigger", `CREATE TRIGGER continuity_submission_rollback_fence AFTER INSERT ON submissions
WHEN EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=NEW.domain_id AND p.kind='migration-seal') AND EXISTS(SELECT 1 FROM authority_continuity c WHERE c.domain_id=NEW.domain_id AND c.rollback_fenced=0)
BEGIN UPDATE authority_continuity SET rollback_fenced=1,rollback_command_id=NEW.command_id,rollback_request_hash=NEW.request_hash,rollback_epoch=NEW.epoch WHERE domain_id=NEW.domain_id; END`},
	continuityWriteTrigger("continuity_membership_admission", "repo_memberships", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_artifact_key_admission", "artifact_keys", "INSERT", writeBlockDomain("NEW.domain_id", "NEW.epoch=d.active_epoch")),
	continuityWriteTrigger("continuity_artifact_key_fence_admission", "artifact_keys", "UPDATE OF fence", writeBlockDomain("NEW.domain_id", "NEW.epoch=d.active_epoch")),
	continuityWriteTrigger("continuity_ca_admission", "environment_cas", "INSERT", writeBlockDomain("NEW.domain_id", "NEW.epoch=d.active_epoch")),
	continuityWriteTrigger("continuity_ca_fence_admission", "environment_cas", "UPDATE OF fence", writeBlockDomain("NEW.domain_id", "NEW.epoch=d.active_epoch")),
	continuityWriteTrigger("continuity_environment_admission", "environments", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_environment_generation_admission", "environments", "UPDATE OF generation", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_certificate_admission", "environment_certificates", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_certificate_revoke_admission", "environment_certificates", "UPDATE OF revoked_at", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_enrollment_admission", "enrollment_consumptions", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_renewal_admission", "environment_renewals", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_product_insert_admission", "blob_products", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_product_update_admission", "blob_products", "UPDATE", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_product_delete_admission", "blob_products", "DELETE", writeBlockDomain("OLD.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_chunk_admission", "blob_chunks", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_chunk_delete_admission", "blob_chunks", "DELETE", writeBlockDomain("OLD.domain_id", "1")),
	continuityWriteTrigger("continuity_blob_reference_admission", "blob_references", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_snapshot_admission", "snapshots", "INSERT", writeBlockDomain("NEW.domain_id", "NEW.epoch=d.active_epoch")),
	continuityWriteTrigger("continuity_transfer_admission", "transfers", "INSERT", writeBlockSnapshot("NEW.snapshot_id")),
	continuityWriteTrigger("continuity_transfer_progress_admission", "transfers", "UPDATE OF next_event,next_entry", writeBlockSnapshot("NEW.snapshot_id")),
	continuityWriteTrigger("continuity_snapshot_entry_admission", "snapshot_entries", "INSERT", writeBlockSnapshotEntry("NEW.snapshot_id")),
	continuityWriteTrigger("continuity_snapshot_item_admission", "snapshot_items", "INSERT", writeBlockSnapshotEntry("NEW.snapshot_id")),
	continuityWriteTrigger("continuity_claim_admission", "claims", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_transition_admission", "claims", "UPDATE OF close_command_id", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_grant_admission", "claim_grants", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_journal_admission", "claim_journals", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_journal_update_admission", "claim_journals", "UPDATE", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_journal_entry_admission", "claim_journal_entries", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_journal_entry_update_admission", "claim_journal_entries", "UPDATE", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_close_admission", "claim_closes", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_claim_intent_admission", "claim_acquire_intents", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	continuityWriteTrigger("continuity_standdown_proof_admission", "claim_stand_down_proofs", "INSERT", writeBlockDomain("NEW.domain_id", "1")),
	{"claims_active_matter", "index", `CREATE UNIQUE INDEX claims_active_matter ON claims(domain_id,matter_id,authority_epoch) WHERE close_command_id IS NULL`},
}

const writeBlockSQL = `EXISTS(SELECT 1 FROM authority_continuity c JOIN domains d USING(domain_id) WHERE c.domain_id=%s AND ((c.admission_closed=1 AND c.closed_epoch=d.active_epoch AND (%s)) OR (EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=%s AND p.kind='migration-authorization' AND p.authority_epoch=d.active_epoch) AND NOT EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=%s AND p.kind='migration-seal' AND p.authority_epoch=d.active_epoch))))`

func writeBlockDomain(domainExpr, closedScope string) string {
	return fmt.Sprintf(writeBlockSQL, domainExpr, closedScope, domainExpr, domainExpr)
}

func writeBlockSnapshot(snapshotExpr string) string {
	return `EXISTS(SELECT 1 FROM snapshots s JOIN authority_continuity c USING(domain_id) JOIN domains d USING(domain_id) WHERE s.snapshot_id=` + snapshotExpr + ` AND ((c.admission_closed=1 AND c.closed_epoch=d.active_epoch AND s.epoch=d.active_epoch) OR (EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=s.domain_id AND p.kind='migration-authorization' AND p.authority_epoch=d.active_epoch) AND NOT EXISTS(SELECT 1 FROM continuity_products p WHERE p.domain_id=s.domain_id AND p.kind='migration-seal' AND p.authority_epoch=d.active_epoch))))`
}

func writeBlockSnapshotEntry(snapshotExpr string) string { return writeBlockSnapshot(snapshotExpr) }

func continuityWriteTrigger(name, table, event, predicate string) schemaObject {
	return schemaObject{name, "trigger", fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'authority admission closed'); END`, name, event, table, predicate)}
}

func installStep7(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DROP TABLE schema_migrations`, step7MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'continuity-and-migration-proof')`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, object := range step7Schema {
		if object.name == "claims_active_matter" {
			if _, err = tx.Exec(`DROP INDEX claims_active_matter`); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO authority_continuity(domain_id) SELECT domain_id FROM domains`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT c.domain_id,c.owner_nonce,p.authorization,p.verified_at FROM claim_closes c JOIN claim_stand_down_proofs p USING(domain_id,command_id) WHERE c.owner_nonce IS NOT NULL`)
	if err != nil {
		return err
	}
	type nonceUse struct {
		domain, nonce, verified string
		proof                   []byte
	}
	var uses []nonceUse
	for rows.Next() {
		var use nonceUse
		if err = rows.Scan(&use.domain, &use.nonce, &use.proof, &use.verified); err != nil {
			break
		}
		uses = append(uses, use)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, use := range uses {
		if _, err = tx.Exec(`INSERT INTO owner_nonce_uses(domain_id,nonce,action,artifact_digest,consumed_at) VALUES(?,?,'claim-stand-down',?,?)`, use.domain, use.nonce, digestBytes(use.proof), use.verified); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=6`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV5 is the sole explicit v5-to-v6 path. It validates and fsyncs an
// equivalent retained backup before applying the schema transaction; open
// never upgrades implicitly.
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
	db, err := connect(filepath.Join(path, "authority.db"), "rw", false)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = checkSchemaVersion(db, 5); err != nil {
		return fmt.Errorf("%w: v5 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v5 blob validation: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v5.backup.db")
	created := false
	if _, e := os.Lstat(backup); errors.Is(e, os.ErrNotExist) {
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
		created = true
	} else if e != nil {
		return e
	}
	if err = regularFile(backup); err != nil {
		return fmt.Errorf("%w: backup: %v", ErrInvalidStore, err)
	}
	mode, create := "ro", false
	if created {
		mode, create = "rw", true
	}
	b, err := connect(backup, mode, create)
	if err != nil {
		return fmt.Errorf("%w: backup open: %v", ErrInvalidStore, err)
	}
	err = errors.Join(checkSchemaVersion(b, 5), b.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	if err = sameV5StoreContents(db, backup); err != nil {
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
	if err = installStep7(db); err != nil {
		return err
	}
	return checkSchema(db)
}

func sameV5StoreContents(db *sql.DB, backup string) error {
	u := url.URL{Scheme: "file", Path: backup}
	if _, err := db.Exec(`ATTACH DATABASE ? AS retained_v5_backup`, u.String()); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`DETACH DATABASE retained_v5_backup`) }()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
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
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, table := range tables {
		if strings.ContainsAny(table, `";`) {
			return ErrInvalidStore
		}
		var differs int
		q := fmt.Sprintf(`SELECT EXISTS(SELECT * FROM main."%s" EXCEPT SELECT * FROM retained_v5_backup."%s") OR EXISTS(SELECT * FROM retained_v5_backup."%s" EXCEPT SELECT * FROM main."%s")`, table, table, table, table)
		if err = db.QueryRow(q).Scan(&differs); err != nil || differs != 0 {
			return ErrInvalidStore
		}
	}
	return nil
}
