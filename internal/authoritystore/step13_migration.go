package authoritystore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var step13MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier') OR (version = 9 AND name = 'step-10-content-and-findings') OR (version = 10 AND name = 'step-16-claim-journal-sequence-order') OR (version = 11 AND name = 'step-4-stage-step-operations') OR (version = 12 AND name = 'step-7-authority-gate-config-projections'))) STRICT`}

var step13Schema = []schemaObject{
	{"m6_repo_memberships_domain", "index", `CREATE UNIQUE INDEX m6_repo_memberships_domain ON repo_memberships(domain_id,repo_id)`},
	{"m6_repo_config", "table", `CREATE TABLE m6_repo_config (
  domain_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  config_key TEXT NOT NULL CHECK(length(config_key)>0),
  value TEXT NOT NULL,
  last_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,repo_id,config_key),
  FOREIGN KEY(domain_id,repo_id) REFERENCES repo_memberships(domain_id,repo_id),
  FOREIGN KEY(domain_id,last_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"m6_gate_declarations", "table", `CREATE TABLE m6_gate_declarations (
  domain_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  gate TEXT NOT NULL CHECK(length(gate)>0),
  scale TEXT NOT NULL CHECK(scale IN ('matter','stage','step')),
  declaration_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,repo_id,gate),
  UNIQUE(domain_id,repo_id,gate,declaration_event_id),
  FOREIGN KEY(domain_id,repo_id) REFERENCES repo_memberships(domain_id,repo_id),
  FOREIGN KEY(domain_id,declaration_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"m6_gate_states", "table", `CREATE TABLE m6_gate_states (
  domain_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  gate TEXT NOT NULL,
  scale TEXT NOT NULL CHECK(scale IN ('matter','stage','step')),
  state TEXT NOT NULL CHECK(state IN ('closed','dismissed','exempt')),
  reason TEXT,
  source_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,node_id,gate),
  FOREIGN KEY(domain_id,node_id) REFERENCES m6_nodes(domain_id,node_id),
  FOREIGN KEY(domain_id,repo_id,gate) REFERENCES m6_gate_declarations(domain_id,repo_id,gate),
  FOREIGN KEY(domain_id,source_event_id) REFERENCES authority_events(domain_id,event_id),
  CHECK((state='dismissed' AND reason IS NOT NULL AND length(trim(reason))>0) OR
        (state!='dismissed' AND reason IS NULL))
) STRICT`},
	{"m6_tracker_references", "table", `CREATE TABLE m6_tracker_references (
  domain_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  ref TEXT NOT NULL CHECK(length(ref)>0),
  removed_event_id TEXT,
  birth_event_id TEXT NOT NULL,
  last_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,matter_id,ref),
  FOREIGN KEY(domain_id,matter_id) REFERENCES m6_nodes(domain_id,node_id),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,last_event_id) REFERENCES authority_events(domain_id,event_id),
  FOREIGN KEY(domain_id,removed_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"m6_tracker_references_active", "index", `CREATE INDEX m6_tracker_references_active ON m6_tracker_references(domain_id,ref,matter_id) WHERE removed_event_id IS NULL`},
	{"m6_tracker_references_immutable_birth", "trigger", `CREATE TRIGGER m6_tracker_references_immutable_birth BEFORE UPDATE OF domain_id,matter_id,ref,birth_event_id ON m6_tracker_references WHEN NEW.domain_id!=OLD.domain_id OR NEW.matter_id!=OLD.matter_id OR NEW.ref!=OLD.ref OR NEW.birth_event_id!=OLD.birth_event_id BEGIN SELECT RAISE(ABORT,'immutable tracker-reference birth'); END`},
	{"m6_tracker_references_advance", "trigger", `CREATE TRIGGER m6_tracker_references_advance BEFORE UPDATE OF last_event_id ON m6_tracker_references WHEN (SELECT position FROM authority_events WHERE domain_id=NEW.domain_id AND event_id=NEW.last_event_id)<=(SELECT position FROM authority_events WHERE domain_id=OLD.domain_id AND event_id=OLD.last_event_id) BEGIN SELECT RAISE(ABORT,'tracker reference must advance'); END`},
	{"m6_tracker_aggregates", "table", `CREATE TABLE m6_tracker_aggregates (
  domain_id TEXT NOT NULL,
  ref TEXT NOT NULL,
  disposition TEXT CHECK(disposition IN ('active','completed','canceled')),
  member_count INTEGER NOT NULL CHECK(member_count>0),
  PRIMARY KEY(domain_id,ref),
  FOREIGN KEY(domain_id) REFERENCES domains(domain_id)
) STRICT`},
	{"m6_tracker_candidates", "table", `CREATE TABLE m6_tracker_candidates (
  domain_id TEXT NOT NULL,
  candidate_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('state','comment')),
  subject_id TEXT NOT NULL,
  ref TEXT NOT NULL CHECK(length(ref)>0),
  idempotency_key TEXT NOT NULL,
  payload TEXT NOT NULL CHECK(json_valid(payload) AND json_type(payload)='object'),
  birth_event_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,candidate_id),
  UNIQUE(domain_id,idempotency_key),
  FOREIGN KEY(domain_id) REFERENCES domains(domain_id),
  FOREIGN KEY(domain_id,repo_id) REFERENCES repo_memberships(domain_id,repo_id),
  FOREIGN KEY(domain_id,subject_id) REFERENCES m6_nodes(domain_id,node_id),
  FOREIGN KEY(domain_id,birth_event_id) REFERENCES authority_events(domain_id,event_id)
) STRICT`},
	{"m6_tracker_candidates_queue", "index", `CREATE INDEX m6_tracker_candidates_queue ON m6_tracker_candidates(domain_id,repo_id,birth_event_id,candidate_id)`},
}

func installStep13(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step13MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier'),(9,'step-10-content-and-findings'),(10,'step-16-claim-journal-sequence-order'),(11,'step-4-stage-step-operations'),(12,'step-7-authority-gate-config-projections')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step13Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version=12`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV11 retains a validated v11 backup before installing the durable
// event-derived gate, Repo config, shared-reference, aggregate, and candidate
// projections. Ordinary opens never migrate.
func UpgradeV11(root string) error {
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
	if err = checkSchemaVersion(db, 11); err != nil {
		return fmt.Errorf("%w: v11 validation: %v", ErrInvalidStore, err)
	}
	for _, check := range []struct {
		name string
		fn   func(*sql.DB) error
	}{
		{"authority state", checkStep4State}, {"snapshot state", checkStep6State},
		{"claim state", checkStep5State}, {"genesis state", checkM5GenesisState},
		{"birth journal", checkBirthJournalState}, {"M6 node projection", checkStep12State},
	} {
		if err = check.fn(db); err != nil {
			return fmt.Errorf("%w: v11 %s: %v", ErrInvalidStore, check.name, err)
		}
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v11 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v11.backup.db")
	if _, statErr := os.Lstat(backup); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return statErr
		}
		return fmt.Errorf("%w: v11 backup already exists; inspect before retry", ErrInvalidStore)
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
	if err = errors.Join(checkSchemaVersion(retained, 11), retained.Close()); err != nil {
		return err
	}
	if err = sameStoreContents(db, backup); err != nil {
		return err
	}
	if err = syncFileAndDirectory(backup, path); err != nil {
		return err
	}
	if err = installStep13(db); err != nil {
		return err
	}
	if err = rebuildStep13Projection(db); err != nil {
		return err
	}
	if err = installStep14(db); err != nil {
		return err
	}
	if err = checkSchema(db); err != nil {
		return err
	}
	if err = checkStep13State(db); err != nil {
		return err
	}
	return checkStep14State(db)
}
