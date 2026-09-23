package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var step5MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close'))) STRICT`}

var step5Schema = []schemaObject{
	{"claim_acquire_intents", "table", `CREATE TABLE claim_acquire_intents (
  domain_id TEXT NOT NULL, command_id TEXT NOT NULL, start_count INTEGER NOT NULL CHECK(start_count >= 0),
  start_event_id TEXT, start_digest TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id),
  CHECK((start_count=0)=(start_event_id IS NULL))
) STRICT`},
	{"claim_acquire_intents_immutable", "trigger", `CREATE TRIGGER claim_acquire_intents_immutable BEFORE UPDATE ON claim_acquire_intents BEGIN SELECT RAISE(ABORT,'immutable acquire intent'); END`},
	{"claim_acquire_intents_no_delete", "trigger", `CREATE TRIGGER claim_acquire_intents_no_delete BEFORE DELETE ON claim_acquire_intents BEGIN SELECT RAISE(ABORT,'immutable acquire intent'); END`},
	{"claim_stand_down_proofs", "table", `CREATE TABLE claim_stand_down_proofs (
  domain_id TEXT NOT NULL, command_id TEXT NOT NULL, owner_nonce TEXT NOT NULL UNIQUE,
  authorization BLOB NOT NULL, verified_at TEXT NOT NULL,
  PRIMARY KEY(domain_id,command_id)
) STRICT`},
	{"claim_stand_down_proofs_immutable", "trigger", `CREATE TRIGGER claim_stand_down_proofs_immutable BEFORE UPDATE ON claim_stand_down_proofs BEGIN SELECT RAISE(ABORT,'immutable stand-down proof'); END`},
	{"claim_stand_down_proofs_no_delete", "trigger", `CREATE TRIGGER claim_stand_down_proofs_no_delete BEFORE DELETE ON claim_stand_down_proofs BEGIN SELECT RAISE(ABORT,'immutable stand-down proof'); END`},
	{"anonymous_batches", "table", `CREATE TABLE anonymous_batches (
  domain_id TEXT NOT NULL, matter_id TEXT NOT NULL, batch_id TEXT NOT NULL,
  PRIMARY KEY(domain_id,matter_id), UNIQUE(domain_id,batch_id),
  FOREIGN KEY(domain_id,matter_id) REFERENCES matters(domain_id,matter_id)
) STRICT`},
	{"anonymous_batches_immutable", "trigger", `CREATE TRIGGER anonymous_batches_immutable BEFORE UPDATE ON anonymous_batches BEGIN SELECT RAISE(ABORT,'immutable anonymous batch'); END`},
	{"anonymous_batches_no_delete", "trigger", `CREATE TRIGGER anonymous_batches_no_delete BEFORE DELETE ON anonymous_batches BEGIN SELECT RAISE(ABORT,'immutable anonymous batch'); END`},
	{"claims", "table", `CREATE TABLE claims (
  domain_id TEXT NOT NULL, claim_id TEXT NOT NULL, matter_id TEXT NOT NULL,
  claim_epoch INTEGER NOT NULL CHECK(claim_epoch > 0), authority_epoch INTEGER NOT NULL CHECK(authority_epoch > 0),
  owner_environment_id TEXT NOT NULL, worktree_id TEXT NOT NULL, batch_id TEXT NOT NULL,
  dispatch_id TEXT NOT NULL, acquire_command_id TEXT NOT NULL, close_command_id TEXT,
  PRIMARY KEY(domain_id,claim_id), UNIQUE(domain_id,matter_id,claim_epoch),
  UNIQUE(domain_id,dispatch_id), UNIQUE(domain_id,acquire_command_id),
  FOREIGN KEY(domain_id,matter_id,batch_id) REFERENCES anonymous_batches(domain_id,matter_id,batch_id),
  FOREIGN KEY(domain_id,owner_environment_id) REFERENCES environments(domain_id,environment_id),
  FOREIGN KEY(domain_id,acquire_command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,close_command_id) REFERENCES submissions(domain_id,command_id)
) STRICT`},
	{"anonymous_batch_claim_key", "index", `CREATE UNIQUE INDEX anonymous_batch_claim_key ON anonymous_batches(domain_id,matter_id,batch_id)`},
	{"claims_active_matter", "index", `CREATE UNIQUE INDEX claims_active_matter ON claims(domain_id,matter_id) WHERE close_command_id IS NULL`},
	{"claims_epoch_advance", "trigger", `CREATE TRIGGER claims_epoch_advance BEFORE INSERT ON claims WHEN NEW.claim_epoch <= COALESCE((SELECT max(claim_epoch) FROM claims WHERE domain_id=NEW.domain_id AND matter_id=NEW.matter_id),0) BEGIN SELECT RAISE(ABORT,'claim epoch must advance'); END`},
	{"claims_immutable", "trigger", `CREATE TRIGGER claims_immutable BEFORE UPDATE ON claims WHEN NEW.domain_id!=OLD.domain_id OR NEW.claim_id!=OLD.claim_id OR NEW.matter_id!=OLD.matter_id OR NEW.claim_epoch!=OLD.claim_epoch OR NEW.authority_epoch!=OLD.authority_epoch OR NEW.owner_environment_id!=OLD.owner_environment_id OR NEW.worktree_id!=OLD.worktree_id OR NEW.batch_id!=OLD.batch_id OR NEW.dispatch_id!=OLD.dispatch_id OR NEW.acquire_command_id!=OLD.acquire_command_id OR OLD.close_command_id IS NOT NULL OR NEW.close_command_id IS NULL OR NOT EXISTS(SELECT 1 FROM claim_closes c WHERE c.domain_id=OLD.domain_id AND c.claim_id=OLD.claim_id AND c.command_id=NEW.close_command_id) BEGIN SELECT RAISE(ABORT,'immutable claim'); END`},
	{"claims_no_delete", "trigger", `CREATE TRIGGER claims_no_delete BEFORE DELETE ON claims BEGIN SELECT RAISE(ABORT,'immutable claim'); END`},
	{"claim_grants", "table", `CREATE TABLE claim_grants (
  domain_id TEXT NOT NULL, acquire_command_id TEXT NOT NULL, grant_id TEXT NOT NULL, claim_id TEXT NOT NULL,
  start_count INTEGER NOT NULL CHECK(start_count >= 0), start_event_id TEXT, start_digest TEXT NOT NULL,
  end_count INTEGER NOT NULL CHECK(end_count >= start_count), end_event_id TEXT, end_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL, receipt BLOB NOT NULL, grant_wrapper BLOB NOT NULL,
  artifact_epoch INTEGER NOT NULL, artifact_generation INTEGER NOT NULL, artifact_sequence INTEGER NOT NULL,
  delta BLOB NOT NULL, manifest BLOB NOT NULL, snapshot_id TEXT,
  PRIMARY KEY(domain_id,acquire_command_id), UNIQUE(domain_id,grant_id), UNIQUE(domain_id,claim_id),
  FOREIGN KEY(domain_id,claim_id) REFERENCES claims(domain_id,claim_id),
  FOREIGN KEY(domain_id,acquire_command_id) REFERENCES terminal_receipts(domain_id,command_id),
  FOREIGN KEY(domain_id,artifact_epoch,artifact_generation,artifact_sequence) REFERENCES authority_artifacts(domain_id,epoch,generation,sequence),
  CHECK((start_count=0)=(start_event_id IS NULL)), CHECK((end_count=0)=(end_event_id IS NULL))
) STRICT`},
	{"claim_grants_immutable", "trigger", `CREATE TRIGGER claim_grants_immutable BEFORE UPDATE ON claim_grants BEGIN SELECT RAISE(ABORT,'immutable claim grant'); END`},
	{"claim_grants_no_delete", "trigger", `CREATE TRIGGER claim_grants_no_delete BEFORE DELETE ON claim_grants BEGIN SELECT RAISE(ABORT,'immutable claim grant'); END`},
	{"claim_journals", "table", `CREATE TABLE claim_journals (
  domain_id TEXT NOT NULL, journal_id TEXT NOT NULL, claim_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 0),
  state TEXT NOT NULL CHECK(state IN ('open','sealed','quarantined')),
  repair_command_id TEXT,
  PRIMARY KEY(domain_id,journal_id), UNIQUE(domain_id,claim_id,generation),
  FOREIGN KEY(domain_id,claim_id) REFERENCES claims(domain_id,claim_id),
  FOREIGN KEY(domain_id,repair_command_id) REFERENCES submissions(domain_id,command_id)
) STRICT`},
	{"claim_journals_current", "index", `CREATE UNIQUE INDEX claim_journals_current ON claim_journals(domain_id,claim_id) WHERE state IN ('open','sealed')`},
	{"claim_journals_generation", "trigger", `CREATE TRIGGER claim_journals_generation BEFORE INSERT ON claim_journals WHEN NEW.generation != COALESCE((SELECT max(generation)+1 FROM claim_journals WHERE domain_id=NEW.domain_id AND claim_id=NEW.claim_id),1) BEGIN SELECT RAISE(ABORT,'noncontiguous journal generation'); END`},
	{"claim_journals_transition", "trigger", `CREATE TRIGGER claim_journals_transition BEFORE UPDATE ON claim_journals WHEN NEW.domain_id!=OLD.domain_id OR NEW.journal_id!=OLD.journal_id OR NEW.claim_id!=OLD.claim_id OR NEW.generation!=OLD.generation OR (OLD.state='open' AND NEW.state NOT IN ('sealed','quarantined')) OR (OLD.state='sealed' AND NEW.state!='quarantined') OR OLD.state='quarantined' OR (NEW.state='quarantined' AND NEW.repair_command_id IS NULL) OR (OLD.repair_command_id IS NOT NULL AND NEW.repair_command_id IS NOT OLD.repair_command_id) BEGIN SELECT RAISE(ABORT,'immutable journal generation'); END`},
	{"claim_journals_no_delete", "trigger", `CREATE TRIGGER claim_journals_no_delete BEFORE DELETE ON claim_journals BEGIN SELECT RAISE(ABORT,'immutable journal'); END`},
	{"claim_journal_entries", "table", `CREATE TABLE claim_journal_entries (
  domain_id TEXT NOT NULL, journal_id TEXT NOT NULL, position INTEGER NOT NULL CHECK(position > 0),
  command_id TEXT NOT NULL, request_hash TEXT NOT NULL, command BLOB NOT NULL,
  environment_sequence INTEGER NOT NULL CHECK(environment_sequence > 0),
  state TEXT NOT NULL CHECK(state IN ('pending-return','unknown','terminal','quarantined')),
  receipt BLOB, installed_end_count INTEGER CHECK(installed_end_count >= 0), installed_end_digest TEXT,
  PRIMARY KEY(domain_id,journal_id,position), UNIQUE(domain_id,command_id),
  FOREIGN KEY(domain_id,journal_id) REFERENCES claim_journals(domain_id,journal_id),
  CHECK((installed_end_count IS NULL)=(installed_end_digest IS NULL)),
  CHECK(state!='terminal' OR (receipt IS NOT NULL AND installed_end_count IS NOT NULL))
) STRICT`},
	{"claim_journal_entries_contiguous", "trigger", `CREATE TRIGGER claim_journal_entries_contiguous BEFORE INSERT ON claim_journal_entries WHEN NEW.state!='pending-return' OR NOT EXISTS(SELECT 1 FROM claim_journals j WHERE j.domain_id=NEW.domain_id AND j.journal_id=NEW.journal_id AND j.state='open') OR NEW.position!=COALESCE((SELECT max(position)+1 FROM claim_journal_entries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id),1) OR (NEW.position>1 AND NEW.environment_sequence!=(SELECT environment_sequence+1 FROM claim_journal_entries WHERE domain_id=NEW.domain_id AND journal_id=NEW.journal_id AND position=NEW.position-1)) BEGIN SELECT RAISE(ABORT,'noncontiguous journal entry'); END`},
	{"claim_journal_entries_transition", "trigger", `CREATE TRIGGER claim_journal_entries_transition BEFORE UPDATE ON claim_journal_entries WHEN NEW.domain_id!=OLD.domain_id OR NEW.journal_id!=OLD.journal_id OR NEW.position!=OLD.position OR NEW.command_id!=OLD.command_id OR NEW.request_hash!=OLD.request_hash OR NEW.command!=OLD.command OR NEW.environment_sequence!=OLD.environment_sequence OR OLD.state IN ('terminal','quarantined') OR (OLD.state='pending-return' AND NEW.state NOT IN ('unknown','terminal','quarantined')) OR (OLD.state='unknown' AND NEW.state NOT IN ('terminal','quarantined')) OR (OLD.receipt IS NOT NULL AND NEW.receipt IS NOT OLD.receipt) OR (OLD.installed_end_count IS NOT NULL AND (NEW.installed_end_count IS NOT OLD.installed_end_count OR NEW.installed_end_digest IS NOT OLD.installed_end_digest)) BEGIN SELECT RAISE(ABORT,'immutable journal entry'); END`},
	{"claim_journal_entries_no_delete", "trigger", `CREATE TRIGGER claim_journal_entries_no_delete BEFORE DELETE ON claim_journal_entries BEGIN SELECT RAISE(ABORT,'immutable journal entry'); END`},
	{"claim_closes", "table", `CREATE TABLE claim_closes (
  domain_id TEXT NOT NULL, claim_id TEXT NOT NULL, command_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('release','stand-down')), barrier BLOB,
  acting_environment_id TEXT NOT NULL, reason_digest TEXT, owner_nonce TEXT UNIQUE,
  PRIMARY KEY(domain_id,claim_id), UNIQUE(domain_id,command_id),
  FOREIGN KEY(domain_id,claim_id) REFERENCES claims(domain_id,claim_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,acting_environment_id) REFERENCES environments(domain_id,environment_id),
  CHECK((kind='release' AND barrier IS NOT NULL AND reason_digest IS NULL) OR (kind='stand-down' AND barrier IS NULL AND reason_digest IS NOT NULL))
) STRICT`},
	{"claim_closes_immutable", "trigger", `CREATE TRIGGER claim_closes_immutable BEFORE UPDATE ON claim_closes BEGIN SELECT RAISE(ABORT,'immutable claim close'); END`},
	{"claim_closes_no_delete", "trigger", `CREATE TRIGGER claim_closes_no_delete BEFORE DELETE ON claim_closes BEGIN SELECT RAISE(ABORT,'immutable claim close'); END`},
}

func installStep5(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DROP TABLE schema_migrations`, step5MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close')`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return err
		}
	}
	for _, object := range step5Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if _, err = tx.Exec(`PRAGMA user_version = 5`); err != nil {
		return err
	}
	return tx.Commit()
}

// UpgradeV4 explicitly migrates an exact closed v4 store after validating and
// syncing a retained, equivalent backup. Ordinary open never migrates v4.
func UpgradeV4(root string) error {
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
	if err = checkSchemaVersion(db, 4); err != nil {
		return fmt.Errorf("%w: v4 validation: %v", ErrInvalidStore, err)
	}
	if err = checkBlobFiles(db, filepath.Join(path, "blobs")); err != nil {
		return fmt.Errorf("%w: v4 blobs: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v4.backup.db")
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
	err = errors.Join(checkSchemaVersion(b, 4), b.Close())
	if err != nil {
		return fmt.Errorf("%w: backup validation: %v", ErrInvalidStore, err)
	}
	if err = sameV4StoreContents(db, backup); err != nil {
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
	if err = installStep5(db); err != nil {
		return err
	}
	return checkSchema(db)
}

func sameV4StoreContents(db *sql.DB, backup string) error {
	u := url.URL{Scheme: "file", Path: backup}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	if _, err := db.Exec(`ATTACH DATABASE ? AS retained_v4_backup`, u.String()); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec(`DETACH DATABASE retained_v4_backup`) }()
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
		query := fmt.Sprintf(`SELECT EXISTS(SELECT * FROM main.%s EXCEPT SELECT * FROM retained_v4_backup.%s) OR EXISTS(SELECT * FROM retained_v4_backup.%s EXCEPT SELECT * FROM main.%s)`, name, name, name, name)
		if err = db.QueryRow(query).Scan(&differs); err != nil {
			return err
		}
		if differs != 0 {
			return fmt.Errorf("table %s differs", table)
		}
	}
	return nil
}

// checkStep5State uses the DB directly, not Store methods (which acquire s.mu).
// Each result set is closed before another query on the single-connection DB.
func checkStep5State(db *sql.DB) error {
	if err := checkClaimSubmissionEvidence(db); err != nil {
		return err
	}
	// Detect gaps and impossible transitions even when an external writer has
	// bypassed triggers. Aggregate queries finish before row-by-row lookups.
	for _, query := range []string{
		`SELECT count(*) FROM anonymous_batches b WHERE NOT EXISTS(SELECT 1 FROM claims c WHERE c.domain_id=b.domain_id AND c.matter_id=b.matter_id AND c.batch_id=b.batch_id)`,
		`SELECT count(*) FROM claims c WHERE NOT EXISTS(SELECT 1 FROM claim_journals j WHERE j.domain_id=c.domain_id AND j.claim_id=c.claim_id AND j.generation=1) OR NOT EXISTS(SELECT 1 FROM claim_grants g WHERE g.domain_id=c.domain_id AND g.claim_id=c.claim_id)`,
		`SELECT count(*) FROM claims c WHERE (c.close_command_id IS NULL AND EXISTS(SELECT 1 FROM claim_closes x WHERE x.domain_id=c.domain_id AND x.claim_id=c.claim_id)) OR (c.close_command_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM claim_closes x WHERE x.domain_id=c.domain_id AND x.claim_id=c.claim_id AND x.command_id=c.close_command_id)) OR EXISTS(SELECT 1 FROM claims p JOIN terminal_receipts a ON a.domain_id=p.domain_id AND a.command_id=p.acquire_command_id JOIN terminal_receipts b ON b.domain_id=c.domain_id AND b.command_id=c.acquire_command_id WHERE p.domain_id=c.domain_id AND p.matter_id=c.matter_id AND a.first_position<b.first_position AND p.claim_epoch>=c.claim_epoch)`,
		`SELECT count(*) FROM claim_journals j WHERE j.generation != (SELECT count(*) FROM claim_journals p WHERE p.domain_id=j.domain_id AND p.claim_id=j.claim_id AND p.generation<=j.generation) OR (j.state='quarantined') != (j.repair_command_id IS NOT NULL) OR (j.state!='quarantined' AND j.generation < (SELECT max(generation) FROM claim_journals p WHERE p.domain_id=j.domain_id AND p.claim_id=j.claim_id))`,
		`SELECT count(*) FROM claim_journal_entries e WHERE e.position != (SELECT count(*) FROM claim_journal_entries p WHERE p.domain_id=e.domain_id AND p.journal_id=e.journal_id AND p.position<=e.position) OR (e.position>1 AND e.environment_sequence != (SELECT p.environment_sequence+1 FROM claim_journal_entries p WHERE p.domain_id=e.domain_id AND p.journal_id=e.journal_id AND p.position=e.position-1))`,
		`SELECT count(*) FROM claim_grants g JOIN claims c ON c.domain_id=g.domain_id AND c.claim_id=g.claim_id WHERE g.acquire_command_id!=c.acquire_command_id OR g.artifact_epoch!=c.authority_epoch`,
		`SELECT count(*) FROM claim_closes x JOIN claims c ON c.domain_id=x.domain_id AND c.claim_id=x.claim_id WHERE x.command_id!=c.close_command_id OR (x.kind='release' AND x.acting_environment_id!=c.owner_environment_id) OR (x.kind='stand-down' AND x.acting_environment_id=c.owner_environment_id)`,
	} {
		var bad int
		if err := db.QueryRow(query).Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return ErrInvalidStore
		}
	}
	rows, err := db.Query(`SELECT domain_id,matter_id,batch_id FROM anonymous_batches`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var domain, matter, batch string
		if err = rows.Scan(&domain, &matter, &batch); err != nil || !ulid.MatchString(domain) || !ulid.MatchString(matter) || !ulid.MatchString(batch) {
			err = ErrInvalidStore
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	rows, err = db.Query(`SELECT c.domain_id,c.claim_id,c.matter_id,c.claim_epoch,c.authority_epoch,c.owner_environment_id,c.worktree_id,c.batch_id,c.dispatch_id,c.acquire_command_id,c.close_command_id,s.epoch,s.environment_id,s.operation_name,s.state,s.command,r.receipt FROM claims c JOIN submissions s ON s.domain_id=c.domain_id AND s.command_id=c.acquire_command_id LEFT JOIN terminal_receipts r ON r.domain_id=s.domain_id AND r.command_id=s.command_id`)
	if err != nil {
		return err
	}
	type claimCheck struct {
		domain, id, matter, owner, worktree, batch, dispatch, acquire string
		close                                                         sql.NullString
		claimEpoch, authorityEpoch, submissionEpoch                   uint64
		submissionOwner, operation, state                             string
		command, receipt                                              []byte
	}
	var claims []claimCheck
	for rows.Next() {
		var c claimCheck
		if err = rows.Scan(&c.domain, &c.id, &c.matter, &c.claimEpoch, &c.authorityEpoch, &c.owner, &c.worktree, &c.batch, &c.dispatch, &c.acquire, &c.close, &c.submissionEpoch, &c.submissionOwner, &c.operation, &c.state, &c.command, &c.receipt); err != nil {
			break
		}
		claims = append(claims, c)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, c := range claims {
		for _, id := range []string{c.domain, c.id, c.matter, c.owner, c.worktree, c.batch, c.dispatch, c.acquire} {
			if !ulid.MatchString(id) {
				return ErrInvalidStore
			}
		}
		if c.claimEpoch == 0 || c.authorityEpoch == 0 || c.authorityEpoch != c.submissionEpoch || c.owner != c.submissionOwner || c.operation != "claim.acquire" || c.state != "terminal" || c.receipt == nil {
			return ErrInvalidStore
		}
		var command struct {
			Context struct {
				Worktree string `cbor:"worktree_id"`
			} `cbor:"context"`
			Input struct {
				Matter   string `cbor:"matter_id"`
				Worktree string `cbor:"worktree_id"`
				Dispatch string `cbor:"requested_dispatch_id"`
			} `cbor:"input"`
		}
		if artifactDecoder.Unmarshal(c.command, &command) != nil || command.Context.Worktree != c.worktree || command.Input.Matter != c.matter || command.Input.Worktree != c.worktree || command.Input.Dispatch != c.dispatch {
			return ErrInvalidStore
		}
		r, e := readReceipt(c.receipt)
		if e != nil || r.Result.Code != "result.succeeded" || r.ID != c.acquire || r.Epoch != c.authorityEpoch || r.Domain != c.domain {
			return ErrInvalidStore
		}
		var output struct {
			Claim struct {
				ID    string `cbor:"id"`
				Epoch uint64 `cbor:"epoch"`
			} `cbor:"claim"`
			Matter   string `cbor:"matter_id"`
			Batch    string `cbor:"batch_id"`
			Dispatch string `cbor:"dispatch_id"`
		}
		if e = closedPayload(r.Result.Output, &output, "claim", "matter_id", "batch_id", "dispatch_id"); e != nil || output.Claim.ID != c.id || output.Claim.Epoch != c.claimEpoch || output.Matter != c.matter || output.Batch != c.batch || output.Dispatch != c.dispatch {
			return ErrInvalidStore
		}
		var grants int
		if err = db.QueryRow(`SELECT count(*) FROM claim_grants WHERE domain_id=? AND claim_id=?`, c.domain, c.id).Scan(&grants); err != nil || grants != 1 {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT g.domain_id,g.acquire_command_id,g.grant_id,g.claim_id,g.start_count,g.start_event_id,g.start_digest,g.end_count,g.end_event_id,g.end_digest,g.manifest_digest,g.receipt,g.grant_wrapper,g.artifact_epoch,g.artifact_generation,g.artifact_sequence,g.delta,g.manifest,r.receipt,a.wrapper FROM claim_grants g JOIN terminal_receipts r ON r.domain_id=g.domain_id AND r.command_id=g.acquire_command_id JOIN authority_artifacts a ON a.domain_id=g.domain_id AND a.epoch=g.artifact_epoch AND a.generation=g.artifact_generation AND a.sequence=g.artifact_sequence`)
	if err != nil {
		return err
	}
	type grantCheck struct {
		domain, acquire, id, claim, startDigest, endDigest, manifestDigest string
		startEvent, endEvent                                               sql.NullString
		start, end, epoch, generation, sequence                            uint64
		receipt, wrapper, delta, manifest, terminal, artifact              []byte
	}
	var grants []grantCheck
	for rows.Next() {
		var g grantCheck
		if err = rows.Scan(&g.domain, &g.acquire, &g.id, &g.claim, &g.start, &g.startEvent, &g.startDigest, &g.end, &g.endEvent, &g.endDigest, &g.manifestDigest, &g.receipt, &g.wrapper, &g.epoch, &g.generation, &g.sequence, &g.delta, &g.manifest, &g.terminal, &g.artifact); err != nil {
			break
		}
		grants = append(grants, g)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, g := range grants {
		if !ulid.MatchString(g.id) || !validDigest(g.startDigest) || !validDigest(g.endDigest) || !validDigest(g.manifestDigest) || !bytes.Equal(g.receipt, g.terminal) || !bytes.Equal(g.wrapper, g.artifact) || len(g.delta) == 0 || len(g.manifest) == 0 {
			return ErrInvalidStore
		}
		for _, anchor := range []struct {
			count  uint64
			event  sql.NullString
			digest string
		}{{g.start, g.startEvent, g.startDigest}, {g.end, g.endEvent, g.endDigest}} {
			if anchor.count == 0 {
				if anchor.event.Valid || anchor.digest != emptyAnchor().Digest {
					return ErrInvalidStore
				}
			} else {
				var id, digest string
				if err = db.QueryRow(`SELECT event_id,prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, g.domain, anchor.count).Scan(&id, &digest); err != nil || !anchor.event.Valid || id != anchor.event.String || digest != anchor.digest {
					return ErrInvalidStore
				}
			}
		}
		if err = checkClaimGrant(db, g.domain, g.acquire, g.id, g.claim, g.start, g.startEvent, g.startDigest, g.end, g.endEvent, g.endDigest, g.manifestDigest, g.epoch, g.generation, g.sequence, g.receipt, g.wrapper, g.delta, g.manifest); err != nil {
			return err
		}
	}
	rows, err = db.Query(`SELECT domain_id,journal_id,claim_id,repair_command_id,generation FROM claim_journals`)
	if err != nil {
		return err
	}
	type journalCheck struct {
		domain, id, claim string
		repair            sql.NullString
		generation        uint64
	}
	var journals []journalCheck
	for rows.Next() {
		var j journalCheck
		if err = rows.Scan(&j.domain, &j.id, &j.claim, &j.repair, &j.generation); err != nil {
			break
		}
		journals = append(journals, j)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, j := range journals {
		if !ulid.MatchString(j.domain) || !ulid.MatchString(j.id) || !ulid.MatchString(j.claim) {
			return ErrInvalidStore
		}
		if j.repair.Valid {
			var operation, state string
			if !ulid.MatchString(j.repair.String) || db.QueryRow(`SELECT operation_name,state FROM submissions WHERE domain_id=? AND command_id=?`, j.domain, j.repair.String).Scan(&operation, &state) != nil || operation != "claim.journal-repair" || state != "terminal" {
				return ErrInvalidStore
			}
			var command, receipt []byte
			if db.QueryRow(`SELECT s.command,t.receipt FROM submissions s JOIN terminal_receipts t USING(domain_id,command_id) WHERE s.domain_id=? AND s.command_id=?`, j.domain, j.repair.String).Scan(&command, &receipt) != nil {
				return ErrInvalidStore
			}
			var hash string
			if db.QueryRow(`SELECT request_hash FROM submissions WHERE domain_id=? AND command_id=?`, j.domain, j.repair.String).Scan(&hash) != nil {
				return ErrInvalidStore
			}
			c, parseErr := parseLifecycle(command, hash)
			r, receiptErr := readReceipt(receipt)
			var next string
			if db.QueryRow(`SELECT journal_id FROM claim_journals WHERE domain_id=? AND claim_id=? AND generation=?`, j.domain, j.claim, j.generation+1).Scan(&next) != nil || parseErr != nil || receiptErr != nil || c.repair == nil || c.claimID != j.claim || c.repair.Journal != j.id || r.Result.Code != "result.succeeded" {
				return ErrInvalidStore
			}
			var output struct {
				Claim    string `cbor:"claim_id"`
				Archived string `cbor:"archived_journal_id"`
				New      string `cbor:"new_journal_id"`
				Action   string `cbor:"action"`
			}
			if closedPayload(r.Result.Output, &output, "claim_id", "archived_journal_id", "new_journal_id", "action") != nil || output.Claim != j.claim || output.Archived != j.id || output.New != next || output.Action != c.repair.Action.Kind {
				return ErrInvalidStore
			}
		}
	}
	rows, err = db.Query(`SELECT e.domain_id,e.journal_id,e.position,e.command_id,e.request_hash,e.command,e.environment_sequence,e.state,e.receipt,e.installed_end_count,e.installed_end_digest,j.claim_id,c.claim_epoch,c.authority_epoch,c.owner_environment_id,c.worktree_id FROM claim_journal_entries e JOIN claim_journals j ON j.domain_id=e.domain_id AND j.journal_id=e.journal_id JOIN claims c ON c.domain_id=j.domain_id AND c.claim_id=j.claim_id`)
	if err != nil {
		return err
	}
	type entryCheck struct {
		domain, journal, id, hash, state, claim, owner, worktree string
		position, sequence, claimEpoch, authorityEpoch           uint64
		command, receipt                                         []byte
		endCount                                                 sql.NullInt64
		endDigest                                                sql.NullString
	}
	var entries []entryCheck
	for rows.Next() {
		var e entryCheck
		if err = rows.Scan(&e.domain, &e.journal, &e.position, &e.id, &e.hash, &e.command, &e.sequence, &e.state, &e.receipt, &e.endCount, &e.endDigest, &e.claim, &e.claimEpoch, &e.authorityEpoch, &e.owner, &e.worktree); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !ulid.MatchString(e.id) || !ulid.MatchString(e.journal) || !validDigest(e.hash) || digestBytes(append([]byte("wipd/request-hash/v1\x00"), e.command...)) != e.hash || (e.receipt != nil) != (e.endCount.Valid) || e.endCount.Valid != e.endDigest.Valid || (e.endDigest.Valid && !validDigest(e.endDigest.String)) {
			return ErrInvalidStore
		}
		var fields map[string]cbor.RawMessage
		if canonicalDecode(e.command, &fields) != nil || !exactKeys(fields, "schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id", "correlation_command_id", "operation", "context", "claim", "input", "blobs") {
			return ErrInvalidStore
		}
		var identity struct {
			Schema    string `cbor:"schema"`
			ID        string `cbor:"command_id"`
			Authority struct {
				Domain string `cbor:"domain_id"`
				Epoch  uint64 `cbor:"expected_epoch"`
			} `cbor:"authority"`
			Environment struct {
				ID       string `cbor:"id"`
				Sequence uint64 `cbor:"sequence"`
			} `cbor:"environment"`
			Context struct {
				Worktree string `cbor:"worktree_id"`
			} `cbor:"context"`
			Claim struct {
				ID    string `cbor:"id"`
				Epoch uint64 `cbor:"epoch"`
			} `cbor:"claim"`
		}
		if artifactDecoder.Unmarshal(e.command, &identity) != nil || identity.Schema != "wipd.command/1" || identity.ID != e.id || identity.Authority.Domain != e.domain || identity.Authority.Epoch != e.authorityEpoch || identity.Environment.ID != e.owner || identity.Environment.Sequence != e.sequence || identity.Context.Worktree != e.worktree || identity.Claim.ID != e.claim || identity.Claim.Epoch != e.claimEpoch {
			return ErrInvalidStore
		}
		if e.receipt != nil {
			r, x := readReceipt(e.receipt)
			if x != nil || r.Domain != e.domain || r.ID != e.id || r.Hash != e.hash || r.Epoch != e.authorityEpoch || r.Environment.ID != e.owner || r.Environment.Sequence != e.sequence {
				return ErrInvalidStore
			}
			var stored []byte
			if db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, e.domain, e.id).Scan(&stored) != nil || !bytes.Equal(stored, e.receipt) {
				return ErrInvalidStore
			}
		}
	}
	rows, err = db.Query(`SELECT domain_id,claim_id,command_id,kind,acting_environment_id,reason_digest,owner_nonce FROM claim_closes`)
	if err != nil {
		return err
	}
	type closeCheck struct {
		domain, claim, command, kind, actor string
		reason, nonce                       sql.NullString
	}
	var closes []closeCheck
	for rows.Next() {
		var c closeCheck
		if err = rows.Scan(&c.domain, &c.claim, &c.command, &c.kind, &c.actor, &c.reason, &c.nonce); err != nil {
			break
		}
		closes = append(closes, c)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, c := range closes {
		if !ulid.MatchString(c.command) || (c.reason.Valid && !validDigest(c.reason.String)) || (c.nonce.Valid && c.nonce.String == "") {
			return ErrInvalidStore
		}
		var operation, state string
		if err = db.QueryRow(`SELECT operation_name,state FROM submissions WHERE domain_id=? AND command_id=?`, c.domain, c.command).Scan(&operation, &state); err != nil || state != "terminal" || operation != "claim."+c.kind {
			return ErrInvalidStore
		}
		var command, receipt, storedBarrier []byte
		if db.QueryRow(`SELECT s.command,t.receipt,x.barrier FROM submissions s JOIN terminal_receipts t USING(domain_id,command_id) JOIN claim_closes x USING(domain_id,command_id) WHERE s.domain_id=? AND s.command_id=?`, c.domain, c.command).Scan(&command, &receipt, &storedBarrier) != nil {
			return ErrInvalidStore
		}
		var hash string
		if db.QueryRow(`SELECT request_hash FROM submissions WHERE domain_id=? AND command_id=?`, c.domain, c.command).Scan(&hash) != nil {
			return ErrInvalidStore
		}
		parsed, parseErr := parseLifecycle(command, hash)
		r, receiptErr := readReceipt(receipt)
		if parseErr != nil || receiptErr != nil || r.Result.Code != "result.succeeded" || r.ID != c.command || parsed.environment != c.actor {
			return ErrInvalidStore
		}
		if c.kind == "release" {
			if parsed.barrier == nil || !bytes.Equal(storedBarrier, mustRaw(mustRaw(command, "input"), "barrier")) || parsed.barrier.Claim.ID != c.claim {
				return ErrInvalidStore
			}
			var journalState string
			if db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=? AND claim_id=?`, c.domain, parsed.barrier.Journal, c.claim).Scan(&journalState) != nil || journalState != "sealed" {
				return ErrInvalidStore
			}
			tx, e := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if e != nil {
				return e
			}
			digest, count, e := barrierDigest(context.Background(), tx, c.domain, parsed.barrier.Journal)
			_ = tx.Rollback()
			if e != nil || digest != parsed.barrier.Digest || count != parsed.barrier.Count || count != parsed.barrier.Last || count != parsed.barrier.Receipts || !parsed.barrier.Sealed || parsed.barrier.Unresolved != 0 || parsed.barrier.Quarantined != 0 {
				return ErrInvalidStore
			}
		} else if parsed.stand == nil || !c.reason.Valid || c.reason.String != digestBytes([]byte(parsed.stand.Reason)) || !parsed.stand.Loss || !c.nonce.Valid {
			return ErrInvalidStore
		}
	}
	return nil
}

// Submission-time exchange evidence is retained even for terminal no-effect
// outcomes. Reopen must not infer it from a later caller-supplied anchor or
// from a proof whose validity interval has since elapsed.
func checkClaimSubmissionEvidence(db *sql.DB) error {
	var bad int
	for _, query := range []string{
		`SELECT count(*) FROM submissions s WHERE s.operation_name='claim.acquire' AND NOT EXISTS(SELECT 1 FROM claim_acquire_intents i WHERE i.domain_id=s.domain_id AND i.command_id=s.command_id)`,
		`SELECT count(*) FROM claim_acquire_intents i LEFT JOIN submissions s ON s.domain_id=i.domain_id AND s.command_id=i.command_id WHERE s.operation_name IS NULL OR s.operation_name!='claim.acquire'`,
		`SELECT count(*) FROM claim_grants g JOIN claim_acquire_intents i ON i.domain_id=g.domain_id AND i.command_id=g.acquire_command_id WHERE g.start_count!=i.start_count OR g.start_event_id IS NOT i.start_event_id OR g.start_digest!=i.start_digest`,
		`SELECT count(*) FROM submissions s WHERE s.operation_name='claim.stand-down' AND NOT EXISTS(SELECT 1 FROM claim_stand_down_proofs p WHERE p.domain_id=s.domain_id AND p.command_id=s.command_id)`,
		`SELECT count(*) FROM claim_stand_down_proofs p LEFT JOIN submissions s ON s.domain_id=p.domain_id AND s.command_id=p.command_id WHERE s.operation_name IS NULL OR s.operation_name!='claim.stand-down'`,
		`SELECT count(*) FROM claim_closes c JOIN claim_stand_down_proofs p ON p.domain_id=c.domain_id AND p.command_id=c.command_id WHERE c.kind='stand-down' AND c.owner_nonce!=p.owner_nonce`,
	} {
		if err := db.QueryRow(query).Scan(&bad); err != nil || bad != 0 {
			return ErrInvalidStore
		}
	}
	rows, err := db.Query(`SELECT i.domain_id,i.command_id,i.start_count,i.start_event_id,i.start_digest,s.command,s.request_hash FROM claim_acquire_intents i JOIN submissions s USING(domain_id,command_id)`)
	if err != nil {
		return err
	}
	type intent struct {
		domain, id, digest, hash string
		count                    uint64
		event                    sql.NullString
		command                  []byte
	}
	var intents []intent
	for rows.Next() {
		var i intent
		if err = rows.Scan(&i.domain, &i.id, &i.count, &i.event, &i.digest, &i.command, &i.hash); err != nil {
			break
		}
		intents = append(intents, i)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, i := range intents {
		c, e := parseLifecycle(i.command, i.hash)
		if e != nil || c.name != "claim.acquire" || c.domain != i.domain || c.id != i.id || !validDigest(i.digest) {
			return ErrInvalidStore
		}
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		a, e := anchorAt(context.Background(), tx, i.domain, i.count)
		_ = tx.Rollback()
		if e != nil || a.Digest != i.digest || i.event.Valid != (a.EventID != "") || (i.event.Valid && i.event.String != a.EventID) {
			return ErrInvalidStore
		}
	}
	rows, err = db.Query(`SELECT p.domain_id,p.command_id,p.owner_nonce,p.authorization,p.verified_at,s.command,s.request_hash FROM claim_stand_down_proofs p JOIN submissions s USING(domain_id,command_id)`)
	if err != nil {
		return err
	}
	type proof struct {
		domain, id, nonce, verified, hash string
		bytes, command                    []byte
	}
	var proofs []proof
	for rows.Next() {
		var p proof
		if err = rows.Scan(&p.domain, &p.id, &p.nonce, &p.bytes, &p.verified, &p.command, &p.hash); err != nil {
			break
		}
		proofs = append(proofs, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range proofs {
		c, e := parseLifecycle(p.command, p.hash)
		at, timeErr := utcTime(p.verified)
		if e != nil || timeErr != nil || c.name != "claim.stand-down" || c.domain != p.domain || c.id != p.id || at.Format(time.RFC3339Nano) != p.verified {
			return ErrInvalidStore
		}
		tx, e := db.Begin()
		if e != nil {
			return e
		}
		e = verifyStandDownAuthorization(context.Background(), tx, c, p.bytes, at)
		_ = tx.Rollback()
		if e != nil || c.ownerNonce != p.nonce {
			return ErrInvalidStore
		}
	}
	return nil
}

func checkClaimGrant(db *sql.DB, domain, acquire, id, claim string, start uint64, startID sql.NullString, startDigest string, end uint64, endID sql.NullString, endDigest, manifestDigest string, epoch, generation, sequence uint64, receipt, wrapper, delta, manifest []byte) error {
	var c struct {
		Matter, Batch, Dispatch, Owner string
		Epoch                          uint64
	}
	if db.QueryRow(`SELECT matter_id,batch_id,dispatch_id,owner_environment_id,claim_epoch FROM claims WHERE domain_id=? AND claim_id=? AND acquire_command_id=?`, domain, claim, acquire).Scan(&c.Matter, &c.Batch, &c.Dispatch, &c.Owner, &c.Epoch) != nil {
		return ErrInvalidStore
	}
	var submittedHash string
	if db.QueryRow(`SELECT request_hash FROM submissions WHERE domain_id=? AND command_id=?`, domain, acquire).Scan(&submittedHash) != nil {
		return ErrInvalidStore
	}
	anchor := func(count uint64, id sql.NullString, digest string) map[string]any {
		var event any
		if id.Valid {
			event = id.String
		}
		return map[string]any{"event_count": count, "high_water_event_id": event, "prefix_digest": digest}
	}
	startAnchor, endAnchor := anchor(start, startID, startDigest), anchor(end, endID, endDigest)
	var signed signedArtifact
	if closedPayload(wrapper, &signed, "schema", "kind", "domain_id", "authority_epoch", "signer_role", "signer_key_id", "key_generation", "artifact_sequence", "previous_artifact_digest", "issued_at", "payload_schema", "payload_digest", "payload", "signature") != nil || signed.Kind != "claim-grant" || signed.PayloadSchema != "wipd.claim-grant-start/1" || signed.Epoch != epoch || signed.DomainID != domain || signed.Generation == nil || *signed.Generation != generation || signed.Sequence == nil || *signed.Sequence != sequence || signed.PayloadDigest != digestBytes(signed.Payload) {
		return ErrInvalidStore
	}
	var receiptMap map[string]any
	if artifactDecoder.Unmarshal(receipt, &receiptMap) != nil {
		return ErrInvalidStore
	}
	startBytes, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.claim-grant-start/1", "grant_id": id, "acquire_command_id": acquire, "acquire_request_hash": submittedHash, "domain_id": domain, "authority_epoch": epoch, "owner_environment_id": c.Owner, "claim": map[string]any{"id": claim, "epoch": c.Epoch}, "matter_id": c.Matter, "batch_id": c.Batch, "dispatch_id": c.Dispatch, "receipt": receiptMap, "prefix": map[string]any{"start": startAnchor, "end": endAnchor}, "blob_manifest_digest": manifestDigest})
	if err != nil || !bytes.Equal(startBytes, signed.Payload) {
		return ErrInvalidStore
	}
	r, err := readReceipt(receipt)
	if err != nil || r.Result.Code != "result.succeeded" || r.Range == nil || end < start || end < r.Range.Count || endID.String < r.Range.Last {
		return ErrInvalidStore
	}
	var snapshot string
	if db.QueryRow(`SELECT snapshot_id FROM claim_grants WHERE domain_id=? AND acquire_command_id=?`, domain, acquire).Scan(&snapshot) != nil || !ulid.MatchString(snapshot) {
		return ErrInvalidStore
	}
	var snapDomain, snapDigest, snapManifest string
	var snapCount, snapEpoch uint64
	var snapID sql.NullString
	snapshotErr := db.QueryRow(`SELECT domain_id,epoch,event_count,event_id,prefix_digest,manifest_digest FROM snapshots WHERE snapshot_id=?`, snapshot).Scan(&snapDomain, &snapEpoch, &snapCount, &snapID, &snapDigest, &snapManifest)
	if snapshotErr != nil && !errors.Is(snapshotErr, sql.ErrNoRows) || snapshotErr == nil && (snapDomain != domain || snapEpoch != epoch || snapCount != end || snapID != endID || snapDigest != endDigest || snapManifest != manifestDigest) {
		return ErrInvalidStore
	}
	events := make([]map[string]any, 0, end-start)
	rows, err := db.Query(`SELECT event_id,record FROM authority_events WHERE domain_id=? AND position>? AND position<=? ORDER BY position`, domain, start, end)
	if err != nil {
		return err
	}
	for rows.Next() {
		var eventID string
		var record []byte
		if err = rows.Scan(&eventID, &record); err != nil {
			break
		}
		events = append(events, map[string]any{"event_id": eventID, "record": record})
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || uint64(len(events)) != end-start {
		return ErrInvalidStore
	}
	expectedDelta, err := artifactEncoder.Marshal(map[string]any{"start": startAnchor, "end": endAnchor, "events": events})
	if err != nil || !bytes.Equal(delta, expectedDelta) {
		return ErrInvalidStore
	}
	var retained struct {
		Schema string `cbor:"schema"`
		Domain string `cbor:"domain_id"`
		Epoch  uint64 `cbor:"authority_epoch"`
		AsOf   struct {
			Count  uint64  `cbor:"event_count"`
			ID     *string `cbor:"high_water_event_id"`
			Digest string  `cbor:"prefix_digest"`
		} `cbor:"as_of"`
		Entries []struct {
			Digest      string `cbor:"digest"`
			ByteLength  uint64 `cbor:"byte_length"`
			Requirement string `cbor:"requirement"`
		} `cbor:"entries"`
		Digest string `cbor:"manifest_digest"`
	}
	if closedPayload(manifest, &retained, "schema", "domain_id", "authority_epoch", "as_of", "entries", "manifest_digest") != nil || retained.Schema != "wipd.blob-manifest/1" || retained.Domain != domain || retained.Epoch != epoch || retained.AsOf.Count != end || retained.AsOf.Digest != endDigest || retained.Digest != manifestDigest || (retained.AsOf.ID == nil) != !endID.Valid || (retained.AsOf.ID != nil && *retained.AsOf.ID != endID.String) {
		return ErrInvalidStore
	}
	var fields map[string]cbor.RawMessage
	if canonicalDecode(mustRaw(manifest, "as_of"), &fields) != nil || !exactKeys(fields, "event_count", "high_water_event_id", "prefix_digest") {
		return ErrInvalidStore
	}
	entries := make([]BlobManifestEntry, 0, len(retained.Entries))
	for _, item := range retained.Entries {
		entries = append(entries, BlobManifestEntry{item.Digest, item.ByteLength, item.Requirement})
	}
	var references int
	if db.QueryRow(`SELECT count(*) FROM blob_references WHERE domain_id=? AND first_position<=?`, domain, end).Scan(&references) != nil || references != len(entries) {
		return ErrInvalidStore
	}
	for _, entry := range entries {
		var length, position uint64
		var verified int
		if db.QueryRow(`SELECT p.byte_length,r.first_position,p.verified FROM blob_references r JOIN blob_products p USING(domain_id,digest) WHERE r.domain_id=? AND r.digest=?`, domain, entry.Digest).Scan(&length, &position, &verified) != nil || length != entry.ByteLength || position > end || verified != 1 {
			return ErrInvalidStore
		}
	}
	if snapshotErr == nil {
		original, e := readEntries(db, snapshot)
		if e != nil || len(original) != len(entries) {
			return ErrInvalidStore
		}
		for i := range entries {
			if entries[i] != original[i] {
				return ErrInvalidStore
			}
		}
	}
	chain, err := manifestChain(entries)
	if err != nil || chain != manifestDigest {
		return ErrInvalidStore
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, map[string]any{"digest": entry.Digest, "byte_length": entry.ByteLength, "requirement": entry.Requirement})
	}
	expectedManifest, err := artifactEncoder.Marshal(map[string]any{"schema": "wipd.blob-manifest/1", "domain_id": domain, "authority_epoch": epoch, "as_of": endAnchor, "entries": items, "manifest_digest": manifestDigest})
	if err != nil || !bytes.Equal(manifest, expectedManifest) {
		return ErrInvalidStore
	}
	return nil
}
