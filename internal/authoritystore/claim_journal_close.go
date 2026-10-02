package authoritystore

import (
	"bytes"
	"context"
	"database/sql"
)

// CurrentClaimJournal is the authority-owned current generation identity for
// an authenticated claim holder. It is never inferred from the claim ID.
type CurrentClaimJournal struct {
	DomainID, EnvironmentID, RepoID, WorktreeID, ClaimID, MatterID, DispatchID, JournalID string
	AuthorityEpoch, ClaimEpoch, Generation                                                uint64
	State                                                                                 string
}

// GetCurrentClaimJournal returns only the open/sealed generation after
// checking the authenticated holder and all related claim identity fields.
func (s *Store) GetCurrentClaimJournal(ctx context.Context, domain string, authorityEpoch uint64,
	environment, claimID string, claimEpoch uint64, matterID, dispatchID string,
) (CurrentClaimJournal, error) {
	var result CurrentClaimJournal
	if ctx == nil || domain == "" || environment == "" || claimID == "" || matterID == "" || dispatchID == "" ||
		authorityEpoch == 0 || claimEpoch == 0 {
		return result, ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return result, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	var owner, state string
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.owner_environment_id,c.matter_id,m.repo_id,c.worktree_id,c.dispatch_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.journal_id,j.state,j.generation
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id JOIN claim_journals j USING(domain_id,claim_id) JOIN domains d ON d.domain_id=c.domain_id
		WHERE c.domain_id=? AND c.claim_id=? AND j.state IN ('open','sealed') AND c.authority_epoch=d.active_epoch`, domain, claimID).
		Scan(&owner, &result.MatterID, &result.RepoID, &result.WorktreeID, &result.DispatchID, &result.ClaimEpoch, &result.AuthorityEpoch, &closed, &result.JournalID, &state, &result.Generation)
	if err != nil {
		return result, err
	}
	if owner != environment || result.MatterID != matterID || result.DispatchID != dispatchID ||
		result.ClaimEpoch != claimEpoch || result.AuthorityEpoch != authorityEpoch || closed.Valid {
		return CurrentClaimJournal{}, ErrFenced
	}
	result.DomainID, result.EnvironmentID, result.ClaimID, result.State = domain, environment, claimID, state
	return result, tx.Commit()
}

// AcknowledgeOwnedClaimJournalEntry atomically fences the receipt ACK against
// the authenticated owner and active journal generation.
func (s *Store) AcknowledgeOwnedClaimJournalEntry(ctx context.Context, identity CurrentClaimJournal,
	position uint64, receipt []byte, end PrefixAnchor,
) error {
	r, err := readReceipt(receipt)
	if err != nil || position == 0 || identity.State != "open" {
		return ErrInvalidProof
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owner, matter, repo, worktree, dispatch, state string
	var claimEpoch, authorityEpoch, generation uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.owner_environment_id,c.matter_id,m.repo_id,c.worktree_id,c.dispatch_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.state,j.generation
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id JOIN claim_journals j USING(domain_id,claim_id) JOIN domains d ON d.domain_id=c.domain_id
		WHERE c.domain_id=? AND c.claim_id=? AND j.journal_id=? AND j.state='open' AND c.authority_epoch=d.active_epoch`,
		identity.DomainID, identity.ClaimID, identity.JournalID).
		Scan(&owner, &matter, &repo, &worktree, &dispatch, &claimEpoch, &authorityEpoch, &closed, &state, &generation)
	if err != nil || owner != identity.EnvironmentID || matter != identity.MatterID || dispatch != identity.DispatchID ||
		claimEpoch != identity.ClaimEpoch || authorityEpoch != identity.AuthorityEpoch || generation != identity.Generation || closed.Valid {
		return ErrFenced
	}
	var id, hash, entryState string
	var sequence, receiptEpoch, lastPosition, installedCount uint64
	var installedDigest sql.NullString
	var retained, command []byte
	err = tx.QueryRowContext(ctx, `SELECT e.command_id,e.request_hash,e.state,e.environment_sequence,e.command,t.receipt,t.artifact_epoch,COALESCE(t.last_position,0),COALESCE(e.installed_end_count,0),e.installed_end_digest
		FROM claim_journal_entries e JOIN terminal_receipts t ON t.domain_id=e.domain_id AND t.command_id=e.command_id
		WHERE e.domain_id=? AND e.journal_id=? AND e.position=?`, identity.DomainID, identity.JournalID, position).
		Scan(&id, &hash, &entryState, &sequence, &command, &retained, &receiptEpoch, &lastPosition, &installedCount, &installedDigest)
	if err != nil {
		return err
	}
	parsed, parseErr := parseJournalCommand(command, hash)
	if (entryState != "pending-return" && entryState != "unknown" && entryState != "terminal") || parseErr != nil || !bytes.Equal(retained, receipt) ||
		r.Domain != identity.DomainID || r.ID != id || r.Hash != hash || r.Environment.ID != identity.EnvironmentID ||
		r.Environment.Sequence != sequence || r.Epoch != identity.AuthorityEpoch || parsed.Claim != identity.ClaimID ||
		parsed.ClaimEpoch != identity.ClaimEpoch || parsed.Repo != repo || parsed.Worktree != worktree ||
		(r.Result.Code != "result.succeeded" && r.Result.Code != "result.rejected" && r.Result.Code != "result.refused" && r.Result.Code != "result.failed") {
		return ErrInvalidProof
	}
	if position > 1 {
		var prior string
		if err = tx.QueryRowContext(ctx, `SELECT state FROM claim_journal_entries WHERE domain_id=? AND journal_id=? AND position=?`, identity.DomainID, identity.JournalID, position-1).Scan(&prior); err != nil || prior != "terminal" {
			return ErrPending
		}
	}
	anchor, err := anchorAt(ctx, tx, identity.DomainID, end.EventCount)
	if err != nil || !equalAnchor(anchor, end) || end.EventCount < lastPosition {
		return ErrPrefixMismatch
	}
	if entryState == "terminal" {
		if !installedDigest.Valid || installedCount < lastPosition {
			return ErrInvalidProof
		}
		installed, anchorErr := anchorAt(ctx, tx, identity.DomainID, installedCount)
		if anchorErr != nil || installed.Digest != installedDigest.String || end.EventCount < installedCount {
			return ErrPrefixMismatch
		}
		return tx.Commit()
	}
	entryState = "terminal"
	if r.Result.Code != "result.succeeded" {
		entryState = "quarantined"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE claim_journal_entries SET state=?,receipt=?,installed_end_count=?,installed_end_digest=? WHERE domain_id=? AND journal_id=? AND position=?`, entryState, receipt, end.EventCount, end.Digest, identity.DomainID, identity.JournalID, position); err != nil {
		return err
	}
	return tx.Commit()
}

// SealOwnedClaimJournal seals only the exact current generation for its owner.
func (s *Store) SealOwnedClaimJournal(ctx context.Context, identity CurrentClaimJournal) (string, uint64, error) {
	if identity.State != "open" && identity.State != "sealed" {
		return "", 0, ErrFenced
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", 0, ErrInvalidStore
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var owner, matter, dispatch, state string
	var claimEpoch, authorityEpoch, generation uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.owner_environment_id,c.matter_id,c.dispatch_id,c.claim_epoch,c.authority_epoch,c.close_command_id,j.state,j.generation
		FROM claims c JOIN claim_journals j USING(domain_id,claim_id) JOIN domains d ON d.domain_id=c.domain_id
		WHERE c.domain_id=? AND c.claim_id=? AND j.journal_id=? AND j.state IN ('open','sealed') AND c.authority_epoch=d.active_epoch`,
		identity.DomainID, identity.ClaimID, identity.JournalID).
		Scan(&owner, &matter, &dispatch, &claimEpoch, &authorityEpoch, &closed, &state, &generation)
	if err != nil || owner != identity.EnvironmentID || matter != identity.MatterID || dispatch != identity.DispatchID ||
		claimEpoch != identity.ClaimEpoch || authorityEpoch != identity.AuthorityEpoch || generation != identity.Generation || closed.Valid {
		return "", 0, ErrFenced
	}
	digest, count, err := barrierDigest(ctx, tx, identity.DomainID, identity.JournalID)
	if err != nil {
		return "", 0, err
	}
	if state == "open" {
		if _, err = tx.ExecContext(ctx, `UPDATE claim_journals SET state='sealed' WHERE domain_id=? AND journal_id=? AND state='open'`, identity.DomainID, identity.JournalID); err != nil {
			return "", 0, err
		}
		if err = recordClaimJournalStateBoundary(ctx, tx, identity.DomainID, identity.ClaimID,
			identity.JournalID, identity.Generation, "sealed"); err != nil {
			return "", 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	return digest, count, nil
}
