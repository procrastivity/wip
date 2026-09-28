package authoritystore

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func appendBirthJournalStep(ctx context.Context, tx *sql.Tx, command operation.Command, requestHash string) error {
	input := command.Request.Input.(operation.StepCreateInput)
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM birth_journals WHERE domain_id=? AND matter_id=?`,
		command.AuthorityDomainID, input.ParentID).Scan(&state); err != nil || state != "open" {
		return ErrFenced
	}
	var position uint64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(position),0)+1 FROM birth_journal_entries WHERE domain_id=? AND matter_id=?`,
		command.AuthorityDomainID, input.ParentID).Scan(&position); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO birth_journal_entries(domain_id,matter_id,position,command_id,request_hash,environment_sequence,state)
		VALUES(?,?,?,?,?,?,'pending')`, command.AuthorityDomainID, input.ParentID, position, command.ID, requestHash, command.EnvironmentSequence)
	return writeError(err)
}

func validateBirthReleaseOwner(ctx context.Context, tx *sql.Tx, c *lifecycleCommand) (claimState, error) {
	state, err := loadImplicitBirthClaim(ctx, tx, c)
	if state.authority != c.epoch || state.epoch != c.claimEpoch || state.owner != c.environment || state.repo != c.repo ||
		state.state != "open" || state.closed.Valid {
		return state, ErrFenced
	}
	return state, err
}

func loadImplicitBirthClaim(ctx context.Context, tx *sql.Tx, c *lifecycleCommand) (claimState, error) {
	var state claimState
	err := tx.QueryRowContext(ctx, `SELECT matter_id,repo_id,owner_environment_id,claim_epoch,release_command_id,matter_id,state
		FROM birth_journals WHERE domain_id=? AND matter_id=?`, c.domain, c.claimID).Scan(
		&state.matter, &state.repo, &state.owner, &state.epoch, &state.closed, &state.journal, &state.state)
	if err != nil {
		return state, err
	}
	state.implicit = true
	state.authority = c.epoch
	state.dispatch = ""
	state.worktree = ""
	return state, nil
}

func validateImplicitBirthClaim(ctx context.Context, tx *sql.Tx, command operation.Command) error {
	input, ok := command.Request.Input.(operation.StepCreateInput)
	if !ok || command.Request.Claim == nil || command.Request.Claim.ID != input.ParentID || command.Request.Claim.Epoch != "1" {
		return ErrFenced
	}
	var ownerEnvironment, repo, birthCommand, journalState string
	var epoch uint64
	err := tx.QueryRowContext(ctx, `SELECT c.owner_environment_id,c.repo_id,c.birth_command_id,c.claim_epoch,j.state
		FROM implicit_birth_claims c JOIN birth_journals j USING(domain_id,matter_id) WHERE c.domain_id=? AND c.matter_id=?`,
		command.AuthorityDomainID, input.ParentID).Scan(&ownerEnvironment, &repo, &birthCommand, &epoch, &journalState)
	if err != nil || journalState != "open" || ownerEnvironment != command.EnvironmentID || repo != command.Request.Context.Repo || epoch != 1 ||
		command.CausationCommandID != birthCommand || command.CorrelationCommandID != birthCommand {
		return ErrFenced
	}
	var birthSequence uint64
	var birthState string
	if err = tx.QueryRowContext(ctx, `SELECT environment_sequence,state FROM submissions WHERE domain_id=? AND command_id=? AND environment_id=?`,
		command.AuthorityDomainID, birthCommand, command.EnvironmentID).Scan(&birthSequence, &birthState); err != nil ||
		birthState != "terminal" || birthSequence >= command.EnvironmentSequence {
		return ErrFenced
	}
	return nil
}

// AcknowledgeBirthJournalEntry records the owner's authenticated assertion
// that the exact successful receipt and its accepted event range are already
// in the installed authority prefix. It never infers terminality from local
// counters or command status alone.
func (s *Store) AcknowledgeBirthJournalEntry(ctx context.Context, ack wipdwire.BirthJournalAck, peer tls.ConnectionState, environment string, at time.Time) error {
	if ack.Schema != "wipd.birth-journal-ack/1" || !ulid.MatchString(ack.DomainID) || !ulid.MatchString(ack.MatterID) ||
		!ulid.MatchString(ack.CommandID) || !validDigest(ack.RequestHash) || len(ack.Receipt) == 0 ||
		!ulid.MatchString(environment) || at.IsZero() || !validBirthAnchor(ack.Installed) {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("authoritystore: closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	domain, err := domainOwner(ctx, tx, ack.DomainID)
	if err != nil {
		return err
	}
	if domain.ActiveEpoch != ack.Epoch {
		return ErrFenced
	}
	if _, err = verifyPeer(ctx, tx, ack.DomainID, environment, ack.Epoch, peer, at); err != nil {
		return err
	}
	var owner, repo, journalState, entryState, storedHash string
	var installedEndCount sql.NullInt64
	var installedEndDigest sql.NullString
	var position, envSequence uint64
	var submittedState string
	if err = tx.QueryRowContext(ctx, `SELECT j.owner_environment_id,j.repo_id,j.state,e.position,e.request_hash,e.environment_sequence,e.state,e.installed_end_count,e.installed_end_digest,s.state
		FROM birth_journals j JOIN birth_journal_entries e USING(domain_id,matter_id)
		JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		WHERE j.domain_id=? AND j.matter_id=? AND e.command_id=?`, ack.DomainID, ack.MatterID, ack.CommandID).
		Scan(&owner, &repo, &journalState, &position, &storedHash, &envSequence, &entryState,
			&installedEndCount, &installedEndDigest, &submittedState); err != nil {
		return err
	}
	if owner != environment || journalState != "open" && journalState != "released" || storedHash != ack.RequestHash ||
		(entryState != "pending" && entryState != "returned") || submittedState != "terminal" {
		return ErrFenced
	}
	if entryState == "returned" && (!installedEndCount.Valid || installedEndCount.Int64 < 0 || !installedEndDigest.Valid || !validDigest(installedEndDigest.String)) {
		return ErrInvalidProof
	}
	var earlierUnreturned uint64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM birth_journal_entries WHERE domain_id=? AND matter_id=? AND position<? AND state!='returned'`,
		ack.DomainID, ack.MatterID, position).Scan(&earlierUnreturned); err != nil {
		return err
	}
	if earlierUnreturned != 0 {
		return ErrPending
	}
	var retained []byte
	if err = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, ack.DomainID, ack.CommandID).Scan(&retained); err != nil {
		return err
	}
	receipt, err := readReceipt(retained)
	if err != nil || !bytes.Equal(retained, ack.Receipt) || receipt.ID != ack.CommandID || receipt.Hash != ack.RequestHash ||
		receipt.Domain != ack.DomainID || receipt.Epoch != ack.Epoch || receipt.Environment.ID != environment ||
		receipt.Environment.Sequence != envSequence || receipt.Result.Code != "result.succeeded" || receipt.Range == nil {
		return ErrInvalidProof
	}
	_, last, err := verifyBirthReceiptRange(ctx, tx, ack.DomainID, ack.CommandID, ack.RequestHash,
		receipt.Range.First, receipt.Range.Last, receipt.Range.Count)
	if err != nil || uint64(last) > ack.Installed.EventCount {
		return ErrPrefixMismatch
	}
	installed, err := anchorAt(ctx, tx, ack.DomainID, ack.Installed.EventCount)
	if err != nil || !equalAnchor(installed, birthAnchor(ack.Installed)) {
		return ErrPrefixMismatch
	}
	if entryState == "returned" {
		return tx.Commit()
	}
	if journalState != "open" {
		return ErrFenced
	}
	result, err := tx.ExecContext(ctx, `UPDATE birth_journal_entries SET state='returned',installed_end_count=?,installed_end_digest=?
		WHERE domain_id=? AND matter_id=? AND position=? AND command_id=? AND request_hash=? AND state='pending'`,
		ack.Installed.EventCount, ack.Installed.Digest, ack.DomainID, ack.MatterID, position, ack.CommandID, ack.RequestHash)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrFenced
	}
	_ = repo
	return tx.Commit()
}

func verifyBirthReceiptRange(ctx context.Context, tx *sql.Tx, domain, commandID, requestHash, firstID, lastID string, count uint64) (int64, int64, error) {
	if count == 0 || !ulid.MatchString(firstID) || !ulid.MatchString(lastID) || firstID > lastID {
		return 0, 0, ErrInvalidProof
	}
	var first, last int64
	if err := tx.QueryRowContext(ctx, `SELECT position FROM authority_events WHERE domain_id=? AND event_id=? AND command_id=?`, domain, firstID, commandID).Scan(&first); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT position FROM authority_events WHERE domain_id=? AND event_id=? AND command_id=?`, domain, lastID, commandID).Scan(&last); err != nil {
		return 0, 0, err
	}
	if first <= 0 || last < first || uint64(last-first+1) != count {
		return 0, 0, ErrInvalidProof
	}
	rows, err := tx.QueryContext(ctx, `SELECT position,event_id,record FROM authority_events WHERE domain_id=? AND command_id=? AND position BETWEEN ? AND ? ORDER BY position`, domain, commandID, first, last)
	if err != nil {
		return 0, 0, err
	}
	seen := int64(0)
	for rows.Next() {
		var position int64
		var eventID string
		var raw []byte
		if err = rows.Scan(&position, &eventID, &raw); err != nil {
			break
		}
		var event struct {
			Schema  string `cbor:"schema"`
			ID      string `cbor:"event_id"`
			Domain  string `cbor:"domain_id"`
			Command string `cbor:"command_id"`
			Hash    string `cbor:"request_hash"`
			Repo    string `cbor:"repo_id"`
		}
		if position != first+seen || closedPayload(raw, &event, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload") != nil ||
			event.Schema != "wipd.event/1" || event.ID != eventID || event.Domain != domain || event.Command != commandID || event.Hash != requestHash || event.Repo == "" {
			err = ErrInvalidProof
			break
		}
		seen++
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil || seen != last-first+1 {
		return 0, 0, ErrInvalidProof
	}
	return first, last, nil
}

func validBirthAnchor(anchor wipdwire.PrefixAnchor) bool {
	if !validDigest(anchor.Digest) {
		return false
	}
	if anchor.EventCount == 0 {
		return anchor.EventID == nil && anchor.Digest == EmptyPrefixAnchor().Digest
	}
	return anchor.EventID != nil && ulid.MatchString(*anchor.EventID)
}

func birthAnchor(anchor wipdwire.PrefixAnchor) PrefixAnchor {
	value := PrefixAnchor{EventCount: anchor.EventCount, Digest: anchor.Digest}
	if anchor.EventID != nil {
		value.EventID = *anchor.EventID
	}
	return value
}

func birthBarrierStatus(ctx context.Context, tx *sql.Tx, domain, matter string) (string, uint64, uint64, uint64, uint64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.position,e.command_id,e.request_hash,e.state,s.state
		FROM birth_journal_entries e JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		WHERE e.domain_id=? AND e.matter_id=? ORDER BY e.position`, domain, matter)
	if err != nil {
		return "", 0, 0, 0, 0, err
	}
	type retainedEntry struct {
		position          uint64
		id, hash          string
		state, submission string
	}
	var entries []retainedEntry
	for rows.Next() {
		var entry retainedEntry
		if err = rows.Scan(&entry.position, &entry.id, &entry.hash, &entry.state, &entry.submission); err != nil {
			break
		}
		entries = append(entries, entry)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return "", 0, 0, 0, 0, err
	}
	var receiptCount, unresolved, quarantined uint64
	digestEntries := make([]wipdwire.JournalBarrierEntry, 0, len(entries))
	for index, entry := range entries {
		if entry.position != uint64(index+1) {
			return "", 0, 0, 0, 0, ErrInvalidStore
		}
		if entry.submission != "terminal" {
			unresolved++
			continue
		}
		var raw []byte
		if err = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, entry.id).Scan(&raw); err != nil {
			return "", 0, 0, 0, 0, err
		}
		receipt, readErr := readReceipt(raw)
		if readErr != nil || receipt.ID != entry.id || receipt.Hash != entry.hash || receipt.Domain != domain ||
			receipt.Result.Code != "result.succeeded" && receipt.Result.Code != "result.rejected" &&
				receipt.Result.Code != "result.refused" && receipt.Result.Code != "result.failed" {
			return "", 0, 0, 0, 0, ErrInvalidStore
		}
		var accepted *wipdwire.JournalBarrierRange
		if receipt.Range != nil {
			first, last, verifyErr := verifyBirthReceiptRange(ctx, tx, domain, entry.id, entry.hash,
				receipt.Range.First, receipt.Range.Last, receipt.Range.Count)
			if verifyErr != nil {
				return "", 0, 0, 0, 0, verifyErr
			}
			_ = first
			_ = last
			accepted = &wipdwire.JournalBarrierRange{First: receipt.Range.First, Last: receipt.Range.Last, Count: receipt.Range.Count}
		}
		receiptCount++
		digestEntries = append(digestEntries, wipdwire.JournalBarrierEntry{
			Position: receiptCount, CommandID: entry.id, RequestHash: entry.hash,
			ResultCode: receipt.Result.Code, Range: accepted,
		})
		if receipt.Result.Code != "result.succeeded" || entry.state == "quarantined" {
			quarantined++
		} else if entry.state != "returned" {
			unresolved++
		} else if receipt.Range == nil {
			return "", 0, 0, 0, 0, ErrInvalidStore
		}
	}
	if receiptCount != uint64(len(entries)) {
		return "", uint64(len(entries)), receiptCount, unresolved, quarantined, nil
	}
	digest, err := wipdwire.JournalBarrierDigest(digestEntries)
	return digest, uint64(len(entries)), receiptCount, unresolved, quarantined, err
}
