package wipdjournal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ClaimJournalBinding is the locally pinned authority-assigned active journal
// generation for one acquired claim.
type ClaimJournalBinding struct {
	ClaimID, MatterID, DispatchID, JournalID string
	ClaimEpoch, Generation                   uint64
	State                                    string
}

func createClaimJournalCloseSchema(executor sqlExecutor) error {
	for _, statement := range []string{
		`CREATE TABLE installed_claim_journals(
			claim_id TEXT PRIMARY KEY,
			claim_epoch INTEGER NOT NULL CHECK(claim_epoch>0),
			matter_id TEXT NOT NULL,
			dispatch_id TEXT NOT NULL,
			journal_id TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK(generation>0),
			state TEXT NOT NULL CHECK(state IN ('open','sealed','released'))
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER claim_journal_binding_immutable BEFORE UPDATE ON installed_claim_journals
			WHEN NOT (
				(OLD.state='open' AND NEW.state='sealed' AND NEW.generation=OLD.generation AND NEW.journal_id=OLD.journal_id AND NEW.claim_id=OLD.claim_id AND NEW.claim_epoch=OLD.claim_epoch AND NEW.matter_id=OLD.matter_id AND NEW.dispatch_id=OLD.dispatch_id) OR
				(OLD.state='sealed' AND NEW.state='released' AND NEW.generation=OLD.generation AND NEW.journal_id=OLD.journal_id AND NEW.claim_id=OLD.claim_id AND NEW.claim_epoch=OLD.claim_epoch AND NEW.matter_id=OLD.matter_id AND NEW.dispatch_id=OLD.dispatch_id)
			)
			BEGIN SELECT RAISE(ABORT,'immutable claim journal binding'); END`,
		`CREATE TRIGGER claim_journal_binding_no_delete BEFORE DELETE ON installed_claim_journals
			BEGIN SELECT RAISE(ABORT,'retained claim journal binding'); END`,
		`CREATE TABLE claim_journal_release_attempts(
			command_id TEXT PRIMARY KEY,
			environment_sequence INTEGER NOT NULL UNIQUE CHECK(environment_sequence>0),
			request_hash TEXT NOT NULL CHECK(length(request_hash)=71),
			canonical_bytes BLOB NOT NULL,
			claim_id TEXT NOT NULL,
			claim_epoch INTEGER NOT NULL CHECK(claim_epoch>0),
			matter_id TEXT NOT NULL,
			dispatch_id TEXT NOT NULL,
			journal_id TEXT NOT NULL,
			generation INTEGER NOT NULL CHECK(generation>0),
			barrier BLOB NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('attempt-prepared','returned')),
			canonical_receipt BLOB,
			result_code TEXT CHECK(result_code IS NULL OR result_code IN ('result.succeeded','result.rejected','result.refused','result.failed')),
			CHECK((state='attempt-prepared' AND canonical_receipt IS NULL AND result_code IS NULL) OR
				(state='returned' AND canonical_receipt IS NOT NULL AND result_code IS NOT NULL))
		) STRICT, WITHOUT ROWID`,
		`CREATE TRIGGER claim_journal_release_identity_immutable BEFORE UPDATE OF command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier ON claim_journal_release_attempts
			BEGIN SELECT RAISE(ABORT,'immutable claim journal release identity'); END`,
		`CREATE TRIGGER claim_journal_release_no_delete BEFORE DELETE ON claim_journal_release_attempts
			BEGIN SELECT RAISE(ABORT,'retained claim journal release identity'); END`,
		`CREATE TRIGGER claim_journal_release_state_transition BEFORE UPDATE OF state ON claim_journal_release_attempts
			WHEN NOT (OLD.state='attempt-prepared' AND NEW.state='returned')
			BEGIN SELECT RAISE(ABORT,'invalid claim journal release transition'); END`,
		`CREATE TRIGGER claim_journal_release_outcome_immutable BEFORE UPDATE OF canonical_receipt,result_code ON claim_journal_release_attempts
			WHEN OLD.state='returned'
			BEGIN SELECT RAISE(ABORT,'immutable claim journal release outcome'); END`,
		`CREATE TRIGGER claim_journal_release_before_insert BEFORE INSERT ON claim_journal_release_attempts
			WHEN EXISTS(SELECT 1 FROM commands c LEFT JOIN authority_command_outcomes o USING(command_id) WHERE c.state!='returned' AND (c.delivery!='authority' OR o.command_id IS NULL)) OR
				EXISTS(SELECT 1 FROM birth_release_attempts WHERE state='attempt-prepared') OR
				EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='attempt-prepared') OR
				NOT EXISTS(SELECT 1 FROM installed_claim_journals j WHERE j.claim_id=NEW.claim_id AND j.claim_epoch=NEW.claim_epoch AND j.matter_id=NEW.matter_id AND j.dispatch_id=NEW.dispatch_id AND j.journal_id=NEW.journal_id AND j.generation=NEW.generation AND j.state='sealed')
			BEGIN SELECT RAISE(ABORT,'claim journal release is unresolved or unsealed'); END`,
		`CREATE TRIGGER command_after_pending_claim_journal_release BEFORE INSERT ON commands
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'claim journal release outcome is unresolved'); END`,
		`CREATE TRIGGER command_after_quarantined_claim_journal_release BEFORE INSERT ON commands
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='returned' AND result_code!='result.succeeded')
			BEGIN SELECT RAISE(ABORT,'claim journal release quarantined Environment'); END`,
		`CREATE TRIGGER command_claim_journal_release_id_conflict BEFORE INSERT ON commands
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE command_id=NEW.command_id)
			BEGIN SELECT RAISE(ABORT,'command ID conflicts with claim journal release'); END`,
		`CREATE TRIGGER birth_release_after_pending_claim_journal_release BEFORE INSERT ON birth_release_attempts
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'claim journal release outcome is unresolved'); END`,
		`CREATE TRIGGER birth_release_after_quarantined_claim_journal_release BEFORE INSERT ON birth_release_attempts
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='returned' AND result_code!='result.succeeded')
			BEGIN SELECT RAISE(ABORT,'claim journal release quarantined Environment'); END`,
		`CREATE TRIGGER claim_acquire_after_pending_claim_journal_release BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='attempt-prepared')
			BEGIN SELECT RAISE(ABORT,'claim journal release outcome is unresolved'); END`,
		`CREATE TRIGGER claim_acquire_after_quarantined_claim_journal_release BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE state='returned' AND result_code!='result.succeeded')
			BEGIN SELECT RAISE(ABORT,'claim journal release quarantined Environment'); END`,
		`CREATE TRIGGER claim_acquire_claim_journal_release_id_conflict BEFORE INSERT ON claim_acquire_attempts
			WHEN EXISTS(SELECT 1 FROM claim_journal_release_attempts WHERE command_id=NEW.command_id)
			BEGIN SELECT RAISE(ABORT,'command ID conflicts with claim journal release'); END`,
	} {
		if _, err := executor.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func upgradeSchemaV7(db *sql.DB, identity Identity) error {
	var version int
	var repoID, domainID, environmentID, marker string
	var epoch int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 7 {
		return ErrInvalidJournal
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).Scan(&repoID, &domainID, &epoch, &environmentID); err != nil ||
		repoID != identity.RepoID || domainID != identity.DomainID || epoch != int64(identity.AuthorityEpoch) || environmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=7`).Scan(&marker); err != nil || marker != "environment-authority-command-outcomes" {
		return ErrInvalidJournal
	}
	if err := checkSchemaObjectsV7(db); err != nil {
		return ErrInvalidJournal
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = createClaimJournalCloseSchema(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`DROP TABLE schema_migrations`); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=8),name TEXT NOT NULL CHECK(name='environment-claim-journal-close')) STRICT`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES(8,'environment-claim-journal-close')`); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version=8`); err != nil {
		return err
	}
	return tx.Commit()
}

// PinClaimJournalIdentity retains the first authority-verified current
// generation. A lookup cannot silently switch it; generation advancement is
// fail-closed until a verified repair path is available.
func (j *Journal) PinClaimJournalIdentity(ctx context.Context, binding ClaimJournalBinding) (ClaimJournalBinding, error) {
	var empty ClaimJournalBinding
	if j == nil || ctx == nil || !identityPattern.MatchString(binding.ClaimID) || !identityPattern.MatchString(binding.MatterID) ||
		!identityPattern.MatchString(binding.DispatchID) || !identityPattern.MatchString(binding.JournalID) ||
		binding.ClaimEpoch == 0 || binding.Generation == 0 || binding.State != "open" && binding.State != "sealed" {
		return empty, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	var grantID string
	if err = tx.QueryRowContext(ctx, `SELECT grant_id FROM installed_claim_grants WHERE claim_id=? AND claim_epoch=? AND matter_id=? AND dispatch_id=?`,
		binding.ClaimID, binding.ClaimEpoch, binding.MatterID, binding.DispatchID).Scan(&grantID); err != nil || grantID == "" {
		return empty, fmt.Errorf("%w: claim journal lookup does not match an installed acquired grant", ErrInvalidJournal)
	}
	var stored ClaimJournalBinding
	err = tx.QueryRowContext(ctx, `SELECT claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,state FROM installed_claim_journals WHERE claim_id=?`, binding.ClaimID).
		Scan(&stored.ClaimID, &stored.ClaimEpoch, &stored.MatterID, &stored.DispatchID, &stored.JournalID, &stored.Generation, &stored.State)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO installed_claim_journals VALUES(?,?,?,?,?,?,?)`,
			binding.ClaimID, binding.ClaimEpoch, binding.MatterID, binding.DispatchID, binding.JournalID, binding.Generation, binding.State); err != nil {
			return empty, err
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return binding, nil
	}
	if err != nil {
		return empty, err
	}
	if stored.ClaimEpoch != binding.ClaimEpoch || stored.MatterID != binding.MatterID || stored.DispatchID != binding.DispatchID ||
		stored.JournalID != binding.JournalID || stored.Generation != binding.Generation ||
		stored.State != binding.State && (stored.State != "open" || binding.State != "sealed") {
		return empty, fmt.Errorf("%w: current claim journal generation changed", ErrInvalidJournal)
	}
	if stored.State == "open" && binding.State == "sealed" {
		if _, err = tx.ExecContext(ctx, `UPDATE installed_claim_journals SET state='sealed' WHERE claim_id=? AND state='open'`, binding.ClaimID); err != nil {
			return empty, err
		}
		stored.State = "sealed"
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return stored, nil
}

// InstalledClaimJournalBinding returns the durable current-generation pin.
func (j *Journal) InstalledClaimJournalBinding(claimID string) (ClaimJournalBinding, error) {
	var binding ClaimJournalBinding
	if j == nil || !identityPattern.MatchString(claimID) {
		return binding, ErrInvalidCommand
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return binding, ErrClosed
	}
	err := j.db.QueryRow(`SELECT claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,state FROM installed_claim_journals WHERE claim_id=?`, claimID).
		Scan(&binding.ClaimID, &binding.ClaimEpoch, &binding.MatterID, &binding.DispatchID, &binding.JournalID, &binding.Generation, &binding.State)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimJournalBinding{}, ErrNotFound
	}
	return binding, err
}
