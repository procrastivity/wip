package wipdjournal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func upgradeSchemaV10(db *sql.DB, identity Identity) error {
	var version int
	var stored Identity
	var marker string
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 10 {
		return ErrInvalidJournal
	}
	if err := db.QueryRow(`SELECT repo_id,domain_id,authority_epoch,environment_id FROM environment_state WHERE singleton=1`).
		Scan(&stored.RepoID, &stored.DomainID, &stored.AuthorityEpoch, &stored.EnvironmentID); err != nil ||
		stored.RepoID != identity.RepoID || stored.DomainID != identity.DomainID || stored.AuthorityEpoch != identity.AuthorityEpoch || stored.EnvironmentID != identity.EnvironmentID {
		return ErrInvalidIdentity
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=10`).Scan(&marker); err != nil || marker != "environment-detached-command-proof" {
		return ErrInvalidJournal
	}
	if err := checkSchemaObjects(db); err != nil {
		return err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, prefix := range []string{"birth_release", "claim_journal_release"} {
		// NULL is intentionally retained for historical returned rows. Neither
		// the current prefix nor the accepted event range proves their install end.
		for _, statement := range []string{
			`ALTER TABLE ` + prefix + `_attempts ADD COLUMN installed_event_count INTEGER CHECK(installed_event_count>=0)`,
			`ALTER TABLE ` + prefix + `_attempts ADD COLUMN installed_event_id TEXT`,
			`ALTER TABLE ` + prefix + `_attempts ADD COLUMN installed_prefix_digest TEXT CHECK(
				(installed_event_count IS NULL AND installed_event_id IS NULL AND installed_prefix_digest IS NULL) OR
				(state='returned' AND installed_event_count IS NOT NULL AND installed_prefix_digest IS NOT NULL AND length(installed_prefix_digest)=71 AND
				((installed_event_count=0 AND installed_event_id IS NULL) OR (installed_event_count>0 AND installed_event_id IS NOT NULL))))`,
			`DROP TRIGGER ` + prefix + `_state_transition`,
			`CREATE TRIGGER ` + prefix + `_state_transition BEFORE UPDATE OF state,canonical_receipt,result_code,installed_event_count,installed_event_id,installed_prefix_digest ON ` + prefix + `_attempts
				WHEN NOT (OLD.state='attempt-prepared' AND NEW.state='returned' AND NEW.installed_event_count IS NOT NULL AND NEW.installed_prefix_digest IS NOT NULL)
				BEGIN SELECT RAISE(ABORT,'invalid or immutable release installation'); END`,
		} {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	for _, statement := range []string{
		`DROP TABLE schema_migrations`,
		`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY CHECK(version=11),name TEXT NOT NULL CHECK(name='environment-release-installation-anchor')) STRICT`,
		`INSERT INTO schema_migrations VALUES(11,'environment-release-installation-anchor')`,
		`PRAGMA user_version=11`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReleaseInstalledAnchor returns the exact installation end retained for a
// successful normal claim.release, acquired or implicit-birth. It verifies the
// immutable attempt, receipt, release events and complete local prefix in one
// read transaction. ErrNotFound means no eligible evidence, including legacy
// returned rows whose installation end was never retained. It never substitutes
// the current Environment prefix for missing evidence.
func (j *Journal) ReleaseInstalledAnchor(ctx context.Context, commandID, requestHash string) (wipdwire.PrefixAnchor, error) {
	var empty wipdwire.PrefixAnchor
	if j == nil || ctx == nil {
		return empty, ErrClosed
	}
	if !identityPattern.MatchString(commandID) || !validDigest(requestHash) {
		return empty, ErrNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	anchor, _, err := releaseInstalledEvidence(tx, j.identity, commandID, requestHash)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return anchor, nil
}

// ReleaseInstalledClaimClose returns the exact successful release identity
// and its locally installed end prefix for a later anonymous Batch sweep.
// Missing or legacy installation evidence is not reconstructed from the
// current Environment head.
func (j *Journal) ReleaseInstalledClaimClose(ctx context.Context, commandID, requestHash string) (operation.ClaimCloseReference, error) {
	var empty operation.ClaimCloseReference
	if j == nil || ctx == nil {
		return empty, ErrClosed
	}
	if !identityPattern.MatchString(commandID) || !validDigest(requestHash) {
		return empty, ErrNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	_, reference, err := releaseInstalledEvidence(tx, j.identity, commandID, requestHash)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return reference, nil
}

// ReleaseInstalledClaimCloseByID resolves the immutable request hash from the
// Environment's retained release-attempt row, then applies the same exact
// receipt, event-range, and installed-prefix verification.
func (j *Journal) ReleaseInstalledClaimCloseByID(ctx context.Context, commandID string) (operation.ClaimCloseReference, error) {
	var empty operation.ClaimCloseReference
	if j == nil || ctx == nil {
		return empty, ErrClosed
	}
	if !identityPattern.MatchString(commandID) {
		return empty, ErrNotFound
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return empty, ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	var requestHash string
	var birthHash, claimHash sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT request_hash FROM birth_release_attempts WHERE command_id=?`, commandID).Scan(&birthHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT request_hash FROM claim_journal_release_attempts WHERE command_id=?`, commandID).Scan(&claimHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if birthHash.Valid == claimHash.Valid {
		if !birthHash.Valid {
			return empty, ErrNotFound
		}
		return empty, ErrInvalidJournal
	}
	if birthHash.Valid {
		requestHash = birthHash.String
	} else {
		requestHash = claimHash.String
	}
	_, reference, err := releaseInstalledEvidence(tx, j.identity, commandID, requestHash)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return reference, nil
}

func releaseInstalledEvidence(tx *sql.Tx, identity Identity, commandID, requestHash string) (wipdwire.PrefixAnchor, operation.ClaimCloseReference, error) {
	var empty wipdwire.PrefixAnchor
	var reference operation.ClaimCloseReference
	table := "birth_release_attempts"
	birth, err := readBirthReleaseAttempt(tx.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,state,canonical_receipt,result_code FROM birth_release_attempts WHERE command_id=?`, commandID))
	var eventIDs []string
	var receipt []byte
	if errors.Is(err, sql.ErrNoRows) {
		table = "claim_journal_release_attempts"
		attempt, readErr := readClaimJournalReleaseAttempt(tx.QueryRow(`SELECT command_id,environment_sequence,request_hash,canonical_bytes,claim_id,claim_epoch,matter_id,dispatch_id,journal_id,generation,barrier,state,canonical_receipt,result_code FROM claim_journal_release_attempts WHERE command_id=?`, commandID))
		if errors.Is(readErr, sql.ErrNoRows) {
			return empty, reference, ErrNotFound
		}
		if readErr != nil || validateClaimJournalReleaseCommand(identity, attempt) != nil {
			return empty, reference, ErrInvalidJournal
		}
		if attempt.RequestHash != requestHash {
			return empty, reference, ErrCommandIDConflict
		}
		if !attempt.Returned || attempt.ResultCode != operation.ResultSucceeded {
			return empty, reference, ErrNotFound
		}
		code, ids, output, receiptErr := validateClaimJournalReleaseReceipt(attempt, identity, attempt.Receipt)
		if receiptErr != nil || code != attempt.ResultCode || validateInstalledClaimJournalRelease(tx, identity, attempt, ids, output, code) != nil {
			return empty, reference, ErrInvalidJournal
		}
		eventIDs = ids
		receipt = attempt.Receipt
		reference.ClaimID = attempt.Binding.ClaimID
		reference.ClaimEpoch = attempt.Binding.ClaimEpoch
	} else {
		if err != nil || validateBirthReleaseCommand(identity, birth) != nil {
			return empty, reference, ErrInvalidJournal
		}
		if birth.RequestHash != requestHash {
			return empty, reference, ErrCommandIDConflict
		}
		if !birth.Returned || birth.ResultCode != operation.ResultSucceeded {
			return empty, reference, ErrNotFound
		}
		code, eventID, output, receiptErr := validateBirthReleaseReceipt(birth, identity, birth.Receipt)
		state, stateErr := readInstallState(tx)
		if receiptErr != nil || stateErr != nil || code != birth.ResultCode || validateInstalledBirthRelease(tx, state.anchor, identity, birth, eventID, output, code) != nil {
			return empty, reference, ErrInvalidJournal
		}
		eventIDs = []string{eventID}
		receipt = birth.Receipt
		reference.ClaimID = birth.Barrier.Claim.ID
		reference.ClaimEpoch = birth.Barrier.Claim.Epoch
	}
	reference.ReleaseCommandID = commandID
	reference.ReleaseRequestHash = requestHash
	reference.TerminalReceiptDigest = digestBytes(receipt)
	var count sql.NullInt64
	var eventID, digest sql.NullString
	if err = tx.QueryRow(`SELECT installed_event_count,installed_event_id,installed_prefix_digest FROM `+table+` WHERE command_id=? AND request_hash=?`, commandID, requestHash).
		Scan(&count, &eventID, &digest); err != nil {
		return empty, reference, err
	}
	if !count.Valid && !eventID.Valid && !digest.Valid {
		return empty, reference, ErrNotFound
	}
	if !count.Valid || count.Int64 < 0 || !digest.Valid {
		return empty, reference, ErrInvalidJournal
	}
	anchor := wipdwire.PrefixAnchor{EventCount: uint64(count.Int64), Digest: digest.String}
	if eventID.Valid {
		anchor.EventID = &eventID.String
	}
	if anchor.EventID != nil {
		eventID := *anchor.EventID
		reference.InstalledPrefixAnchor.EventID = &eventID
	}
	reference.InstalledPrefixAnchor.EventCount = anchor.EventCount
	reference.InstalledPrefixAnchor.Digest = anchor.Digest
	state, err := readInstallState(tx)
	if err != nil || anchor.EventCount > state.anchor.EventCount {
		return empty, reference, ErrInvalidJournal
	}
	if err = verifyLocalReleasePrefix(tx, identity, anchor); err != nil {
		return empty, reference, err
	}
	for _, id := range eventIDs {
		var position uint64
		if err = tx.QueryRow(`SELECT position FROM installed_events WHERE event_id=?`, id).Scan(&position); err != nil || position == 0 || position > anchor.EventCount {
			return empty, reference, ErrInvalidJournal
		}
	}
	return anchor, reference, nil
}

func verifyLocalReleasePrefix(tx *sql.Tx, identity Identity, anchor wipdwire.PrefixAnchor) error {
	if !validTransferAnchor(anchor) {
		return ErrInvalidJournal
	}
	rows, err := tx.Query(`SELECT position,event_id,record FROM installed_events WHERE position<=? ORDER BY position`, anchor.EventCount)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	chain := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	previous := ""
	var count uint64
	for rows.Next() {
		var position uint64
		var id string
		var raw []byte
		if err = rows.Scan(&position, &id, &raw); err != nil {
			return err
		}
		if position != count+1 || !transferULID.MatchString(id) || id <= previous || !validAuthorityEvent(raw, identity.DomainID, id) {
			return ErrInvalidJournal
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
		h := sha256.New()
		_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = h.Write(chain[:])
		_, _ = h.Write(length[:])
		_, _ = h.Write(raw)
		copy(chain[:], h.Sum(nil))
		count++
		previous = id
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != anchor.EventCount || "sha256:"+hex.EncodeToString(chain[:]) != anchor.Digest ||
		(count > 0 && (anchor.EventID == nil || *anchor.EventID != previous)) {
		return fmt.Errorf("%w: retained release anchor is not an exact local prefix", ErrInvalidJournal)
	}
	return nil
}
