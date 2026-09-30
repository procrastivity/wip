package authoritystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/operation"
)

var step9MigrationMarker = schemaObject{"schema_migrations", "table", `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY CHECK (version IN (1, 2, 3, 4, 5, 6, 7, 8)), name TEXT NOT NULL CHECK ((version = 1 AND name = 'baseline') OR (version = 2 AND name = 'environment-and-artifacts') OR (version = 3 AND name = 'submissions-and-receipts') OR (version = 4 AND name = 'prefix-snapshot-blob-transfer') OR (version = 5 AND name = 'claims-grants-journals-close') OR (version = 6 AND name = 'm5-lab-genesis-grant-consumption') OR (version = 7 AND name = 'step-8-provisional-birth-projection') OR (version = 8 AND name = 'step-9-birth-journal-receipt-barrier'))) STRICT`}

var step9Schema = []schemaObject{
	{"birth_journals", "table", `CREATE TABLE birth_journals (
  domain_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  claim_epoch INTEGER NOT NULL CHECK(claim_epoch=1),
  owner_environment_id TEXT NOT NULL,
  repo_id TEXT NOT NULL,
  birth_command_id TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('open','released')),
  release_command_id TEXT,
  barrier_digest TEXT,
  PRIMARY KEY(domain_id,matter_id),
  UNIQUE(domain_id,birth_command_id),
  CHECK((state='open' AND release_command_id IS NULL AND barrier_digest IS NULL) OR
        (state='released' AND release_command_id IS NOT NULL AND length(barrier_digest)=71)),
  FOREIGN KEY(domain_id,matter_id) REFERENCES implicit_birth_claims(domain_id,matter_id),
  FOREIGN KEY(domain_id,birth_command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,release_command_id) REFERENCES submissions(domain_id,command_id),
  FOREIGN KEY(domain_id,owner_environment_id) REFERENCES environments(domain_id,environment_id),
  FOREIGN KEY(repo_id) REFERENCES repo_memberships(repo_id)
) STRICT`},
	{"birth_journal_identity_immutable", "trigger", `CREATE TRIGGER birth_journal_identity_immutable BEFORE UPDATE OF domain_id,matter_id,claim_epoch,owner_environment_id,repo_id,birth_command_id ON birth_journals BEGIN SELECT RAISE(ABORT,'immutable birth journal identity'); END`},
	{"birth_journal_state_transition", "trigger", `CREATE TRIGGER birth_journal_state_transition BEFORE UPDATE OF state ON birth_journals WHEN NOT (OLD.state='open' AND NEW.state='released') BEGIN SELECT RAISE(ABORT,'invalid birth journal transition'); END`},
	{"birth_journal_no_delete", "trigger", `CREATE TRIGGER birth_journal_no_delete BEFORE DELETE ON birth_journals BEGIN SELECT RAISE(ABORT,'retained birth journal'); END`},
	{"birth_journal_entries", "table", `CREATE TABLE birth_journal_entries (
  domain_id TEXT NOT NULL,
  matter_id TEXT NOT NULL,
  position INTEGER NOT NULL CHECK(position>0),
  command_id TEXT NOT NULL,
  request_hash TEXT NOT NULL CHECK(length(request_hash)=71),
  environment_sequence INTEGER NOT NULL CHECK(environment_sequence>0),
  state TEXT NOT NULL CHECK(state IN ('pending','returned','quarantined')),
  installed_end_count INTEGER,
  installed_end_digest TEXT,
  PRIMARY KEY(domain_id,matter_id,position),
  UNIQUE(domain_id,command_id),
  CHECK((state='returned' AND installed_end_count IS NOT NULL AND installed_end_count>=0 AND length(installed_end_digest)=71) OR
        (state!='returned' AND installed_end_count IS NULL AND installed_end_digest IS NULL)),
  FOREIGN KEY(domain_id,matter_id) REFERENCES birth_journals(domain_id,matter_id),
  FOREIGN KEY(domain_id,command_id) REFERENCES submissions(domain_id,command_id)
) STRICT`},
	{"birth_journal_entry_identity_immutable", "trigger", `CREATE TRIGGER birth_journal_entry_identity_immutable BEFORE UPDATE OF domain_id,matter_id,position,command_id,request_hash,environment_sequence ON birth_journal_entries BEGIN SELECT RAISE(ABORT,'immutable birth journal entry'); END`},
	{"birth_journal_entry_transition", "trigger", `CREATE TRIGGER birth_journal_entry_transition BEFORE UPDATE OF state ON birth_journal_entries WHEN NOT (OLD.state='pending' AND NEW.state IN ('returned','quarantined')) BEGIN SELECT RAISE(ABORT,'invalid birth journal entry transition'); END`},
	{"birth_journal_entry_no_delete", "trigger", `CREATE TRIGGER birth_journal_entry_no_delete BEFORE DELETE ON birth_journal_entries BEGIN SELECT RAISE(ABORT,'retained birth journal entry'); END`},
	{"birth_journal_command_order", "index", `CREATE INDEX birth_journal_command_order ON birth_journal_entries(domain_id,command_id)`},
}

func installStep9(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`DROP TABLE schema_migrations`, step9MigrationMarker.sql,
		`INSERT INTO schema_migrations VALUES (1,'baseline'),(2,'environment-and-artifacts'),(3,'submissions-and-receipts'),(4,'prefix-snapshot-blob-transfer'),(5,'claims-grants-journals-close'),(6,'m5-lab-genesis-grant-consumption'),(7,'step-8-provisional-birth-projection'),(8,'step-9-birth-journal-receipt-barrier')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, object := range step9Schema {
		if _, err = tx.Exec(object.sql); err != nil {
			return fmt.Errorf("%s: %w", object.name, err)
		}
	}
	if err = populateBirthJournals(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=8`); err != nil {
		return err
	}
	return tx.Commit()
}

func populateBirthJournals(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT domain_id,matter_id,claim_epoch,owner_environment_id,repo_id,birth_command_id FROM implicit_birth_claims ORDER BY domain_id,matter_id`)
	if err != nil {
		return err
	}
	type birth struct {
		domain, matter, owner, repo, command string
		epoch                                uint64
	}
	var births []birth
	for rows.Next() {
		var value birth
		if err = rows.Scan(&value.domain, &value.matter, &value.epoch, &value.owner, &value.repo, &value.command); err != nil {
			break
		}
		births = append(births, value)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, value := range births {
		if _, err = tx.Exec(`INSERT INTO birth_journals(domain_id,matter_id,claim_epoch,owner_environment_id,repo_id,birth_command_id,state)
			VALUES(?,?,?,?,?,?,'open')`, value.domain, value.matter, value.epoch, value.owner, value.repo, value.command); err != nil {
			return err
		}
		var hash string
		var sequence uint64
		if err = tx.QueryRow(`SELECT request_hash,environment_sequence FROM submissions WHERE domain_id=? AND command_id=?`, value.domain, value.command).Scan(&hash, &sequence); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO birth_journal_entries(domain_id,matter_id,position,command_id,request_hash,environment_sequence,state) VALUES(?,?,1,?,?,?,'pending')`,
			value.domain, value.matter, value.command, hash, sequence); err != nil {
			return err
		}
		stepRows, queryErr := tx.Query(`SELECT command_id,request_hash,command,environment_sequence,state FROM submissions WHERE domain_id=? AND operation_name='step.create' ORDER BY environment_sequence`, value.domain)
		if queryErr != nil {
			return queryErr
		}
		position := uint64(1)
		for stepRows.Next() {
			var id, requestHash, state string
			var canonical []byte
			var envSequence uint64
			if queryErr = stepRows.Scan(&id, &requestHash, &canonical, &envSequence, &state); queryErr != nil {
				break
			}
			command, decodeErr := operation.DecodeCanonicalCommand(canonical)
			if decodeErr != nil {
				queryErr = ErrInvalidStore
				break
			}
			if command.Request.Claim == nil || command.Request.Claim.ID != value.matter {
				continue
			}
			position++
			entryState := "pending"
			if state == "terminal" {
				var receipt []byte
				var code string
				if queryErr = tx.QueryRow(`SELECT receipt,result_code FROM terminal_receipts WHERE domain_id=? AND command_id=?`, value.domain, id).Scan(&receipt, &code); queryErr != nil {
					break
				}
				if _, readErr := readReceipt(receipt); readErr != nil {
					queryErr = ErrInvalidStore
					break
				}
				if code != "result.succeeded" {
					entryState = "quarantined"
				}
			}
			if _, queryErr = tx.Exec(`INSERT INTO birth_journal_entries(domain_id,matter_id,position,command_id,request_hash,environment_sequence,state) VALUES(?,?,?,?,?,?,?)`,
				value.domain, value.matter, position, id, requestHash, envSequence, entryState); queryErr != nil {
				break
			}
		}
		if queryErr == nil {
			queryErr = stepRows.Err()
		}
		_ = stepRows.Close()
		if queryErr != nil {
			return queryErr
		}
	}
	return nil
}

// UpgradeV7 retains the exact pre-upgrade authority store before installing
// the authority-owned provisional-birth journal and release fence.
func UpgradeV7(root string) error {
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
	if err = checkSchemaVersion(db, 7); err != nil {
		return fmt.Errorf("%w: v7 validation: %v", ErrInvalidStore, err)
	}
	if err = checkStep4State(db); err != nil {
		return fmt.Errorf("%w: v7 command state: %v", ErrInvalidStore, err)
	}
	if err = checkStep5State(db); err != nil {
		return fmt.Errorf("%w: v7 claim state: %v", ErrInvalidStore, err)
	}
	if err = checkM5GenesisState(db); err != nil {
		return fmt.Errorf("%w: v7 genesis state: %v", ErrInvalidStore, err)
	}
	backup := filepath.Join(path, "authority-v7.backup.db")
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
	} else {
		return fmt.Errorf("%w: v7 backup already exists; inspect before retry", ErrInvalidStore)
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
	err = errors.Join(checkSchemaVersion(retained, 7), retained.Close())
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
	if err = installStep9(db); err != nil {
		return err
	}
	if err = checkSchemaVersion(db, 8); err != nil {
		return err
	}
	if err = checkBirthJournalState(db); err != nil {
		return err
	}
	return nil
}

func checkBirthJournalState(db *sql.DB) error {
	var missing int
	if err := db.QueryRow(`SELECT count(*) FROM implicit_birth_claims c LEFT JOIN birth_journals j USING(domain_id,matter_id) WHERE j.matter_id IS NULL`).Scan(&missing); err != nil || missing != 0 {
		return ErrInvalidStore
	}
	journalRows, err := db.Query(`SELECT domain_id,matter_id,claim_epoch,owner_environment_id,repo_id,birth_command_id,state,release_command_id,barrier_digest
		FROM birth_journals ORDER BY domain_id,matter_id`)
	if err != nil {
		return err
	}
	type journal struct {
		domain, matter, owner, repo, birth, state string
		epoch                                     uint64
		releaseID, barrier                        sql.NullString
	}
	var journals []journal
	for journalRows.Next() {
		var value journal
		if err = journalRows.Scan(&value.domain, &value.matter, &value.epoch, &value.owner, &value.repo, &value.birth, &value.state, &value.releaseID, &value.barrier); err != nil {
			break
		}
		journals = append(journals, value)
	}
	if err == nil {
		err = journalRows.Err()
	}
	_ = journalRows.Close()
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT domain_id,matter_id,position,command_id,request_hash,environment_sequence,state,installed_end_count,installed_end_digest FROM birth_journal_entries ORDER BY domain_id,matter_id,position`)
	if err != nil {
		return err
	}
	type entry struct {
		domain, matter, id, hash, state string
		position, sequence              uint64
		endCount                        sql.NullInt64
		endDigest                       sql.NullString
	}
	var entries []entry
	for rows.Next() {
		var value entry
		if err = rows.Scan(&value.domain, &value.matter, &value.position, &value.id, &value.hash, &value.sequence, &value.state, &value.endCount, &value.endDigest); err != nil {
			break
		}
		entries = append(entries, value)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	positions := map[string]uint64{}
	for _, value := range entries {
		key := ownerKey(value.domain, value.matter)
		positions[key]++
		var owner, repo string
		var birth string
		if value.position != positions[key] || !ulid.MatchString(value.id) || !validDigest(value.hash) ||
			db.QueryRow(`SELECT owner_environment_id,repo_id,birth_command_id FROM birth_journals WHERE domain_id=? AND matter_id=?`, value.domain, value.matter).Scan(&owner, &repo, &birth) != nil {
			return ErrInvalidStore
		}
		var commandBytes []byte
		var env string
		var sequence uint64
		var submissionState string
		if db.QueryRow(`SELECT command,environment_id,environment_sequence,state FROM submissions WHERE domain_id=? AND command_id=?`, value.domain, value.id).Scan(&commandBytes, &env, &sequence, &submissionState) != nil ||
			env != owner || sequence != value.sequence {
			return ErrInvalidStore
		}
		command, decodeErr := operation.DecodeCanonicalCommand(commandBytes)
		if decodeErr != nil || command.Request.Context.Repo != repo || (value.position == 1 && value.id != birth) ||
			(value.position > 1 && (command.Request.Operation != operation.StepCreateV1.Metadata().Operation || command.Request.Claim == nil || command.Request.Claim.ID != value.matter)) {
			return ErrInvalidStore
		}
		if value.state == "returned" {
			if submissionState != "terminal" || !value.endCount.Valid || !value.endDigest.Valid || !validDigest(value.endDigest.String) {
				return ErrInvalidStore
			}
			tx, txErr := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if txErr != nil {
				return txErr
			}
			anchor, anchorErr := anchorAt(context.Background(), tx, value.domain, uint64(value.endCount.Int64))
			_ = tx.Rollback()
			if anchorErr != nil || anchor.Digest != value.endDigest.String {
				return ErrInvalidStore
			}
		} else if value.endCount.Valid || value.endDigest.Valid || value.state != "pending" && value.state != "quarantined" {
			return ErrInvalidStore
		}
	}
	for _, value := range journals {
		var claimOwner, claimRepo, claimBirth string
		var claimEpoch uint64
		if value.epoch != 1 || !ulid.MatchString(value.matter) ||
			db.QueryRow(`SELECT owner_environment_id,repo_id,birth_command_id,claim_epoch FROM implicit_birth_claims WHERE domain_id=? AND matter_id=?`, value.domain, value.matter).
				Scan(&claimOwner, &claimRepo, &claimBirth, &claimEpoch) != nil || claimOwner != value.owner || claimRepo != value.repo || claimBirth != value.birth || claimEpoch != 1 {
			return ErrInvalidStore
		}
		if value.state == "open" {
			if value.releaseID.Valid || value.barrier.Valid {
				return ErrInvalidStore
			}
			continue
		}
		if value.state != "released" || !value.releaseID.Valid || !ulid.MatchString(value.releaseID.String) || !value.barrier.Valid || !validDigest(value.barrier.String) {
			return ErrInvalidStore
		}
		var command []byte
		var releaseHash string
		if db.QueryRow(`SELECT s.command,s.request_hash FROM birth_journals j JOIN submissions s ON s.domain_id=j.domain_id AND s.command_id=j.release_command_id
			WHERE j.domain_id=? AND j.matter_id=?`, value.domain, value.matter).Scan(&command, &releaseHash) != nil {
			return ErrInvalidStore
		}
		release, parseErr := parseLifecycle(command, releaseHash)
		if parseErr != nil || release.id != value.releaseID.String || release.domain != value.domain || release.claimID != value.matter ||
			release.claimEpoch != 1 || release.barrier == nil || release.barrier.Digest != value.barrier.String {
			return ErrInvalidStore
		}
		tx, txErr := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if txErr != nil {
			return txErr
		}
		digest, count, receipts, unresolved, quarantined, barrierErr := birthBarrierStatus(context.Background(), tx, value.domain, value.matter)
		_ = tx.Rollback()
		if barrierErr != nil || digest != value.barrier.String || count == 0 || receipts != count || unresolved != 0 || quarantined != 0 ||
			release.barrier.Count != count || release.barrier.Last != count || release.barrier.Receipts != count || !release.barrier.Sealed ||
			release.barrier.Unresolved != 0 || release.barrier.Quarantined != 0 {
			return ErrInvalidStore
		}
		var receiptRaw []byte
		if db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, value.domain, value.releaseID.String).Scan(&receiptRaw) != nil {
			return ErrInvalidStore
		}
		receipt, receiptErr := readReceipt(receiptRaw)
		if receiptErr != nil || receipt.Result.Code != "result.succeeded" || receipt.Range == nil || receipt.Range.Count != 1 {
			return ErrInvalidStore
		}
	}
	var orphan int
	if err = db.QueryRow(`SELECT count(*) FROM submissions s JOIN implicit_birth_claims c ON c.domain_id=s.domain_id AND c.owner_environment_id=s.environment_id WHERE s.operation_name='step.create' AND s.operation_version=1 AND NOT EXISTS(SELECT 1 FROM birth_journal_entries e WHERE e.domain_id=s.domain_id AND e.command_id=s.command_id)`).Scan(&orphan); err != nil || orphan != 0 {
		return ErrInvalidStore
	}
	return nil
}
