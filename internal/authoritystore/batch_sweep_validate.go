package authoritystore

import (
	"bytes"
	"context"
	"database/sql"

	"github.com/procrastivity/wip/internal/operation"
)

// checkBatchSweepTerminal re-evaluates the immutable pre-effect boundary,
// including eventless terminals. Neither today's prefix nor earlier receipt
// ranges can supply that boundary: private-only repairs have no public receipt.
func checkBatchSweepTerminal(db *sql.DB, submission storedSubmission, receipt receiptRecord, receiptRaw []byte,
	first, last sql.NullInt64,
) error {
	command, err := operation.DecodeCanonicalCommand(submission.command)
	if err != nil || command.Request.Operation != operation.BatchSweepAnonymousV1.Metadata().Operation ||
		operation.BatchSweepAnonymousV1.ValidateRequest(command.Request) != nil {
		return ErrInvalidStore
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var anchor PrefixAnchor
	var hash, digest, code string
	var eventID, storedOutcome, problem sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT request_hash,terminal_receipt_digest,result_code,outcome,problem_code,event_count,event_id,prefix_digest
		FROM batch_sweep_boundaries WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).
		Scan(&hash, &digest, &code, &storedOutcome, &problem, &anchor.EventCount, &eventID, &anchor.Digest); err != nil ||
		hash != submission.hash || digest != digestBytes(receiptRaw) || code != receipt.Result.Code || eventID.Valid != (anchor.EventCount > 0) {
		return ErrInvalidStore
	}
	anchor.EventID = eventID.String
	if batchSweepVerifyPrefix(ctx, tx, submission.domain, anchor) != nil {
		return ErrInvalidStore
	}
	// A genuine prefix can still be too late for this terminal. Its
	// Environment cannot have observed its own or a later submission's
	// effects; other Environments' sequences do not impose that ordering.
	var futureEffects bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM authority_events e JOIN submissions s USING(domain_id,command_id)
		WHERE e.domain_id=? AND e.position<=? AND s.environment_id=? AND s.environment_sequence>=?
	)`, submission.domain, anchor.EventCount, submission.env, submission.seq).Scan(&futureEffects); err != nil {
		return err
	}
	if futureEffects {
		return ErrInvalidStore
	}
	position := anchor.EventCount
	want, err := batchSweepResultAt(ctx, tx, command, position)
	if err != nil || receipt.Result.Code != string(want.Code) {
		return ErrInvalidStore
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&count); err != nil {
		return err
	}
	if want.Problem != nil {
		if storedOutcome.Valid || !problem.Valid || problem.String != string(want.Problem.Code) ||
			receipt.Result.Problem == nil || *receipt.Result.Problem != problem.String || receipt.Result.Output != nil ||
			receipt.Range != nil || first.Valid || last.Valid || count != 0 {
			return ErrInvalidStore
		}
		return nil
	}
	outcome := want.Output.(operation.BatchSweepAnonymousOutput).Outcome
	output, err := artifactEncoder.Marshal(map[string]any{"outcome": string(outcome)})
	if err != nil || !storedOutcome.Valid || storedOutcome.String != string(outcome) || problem.Valid ||
		!bytes.Equal(receipt.Result.Output, output) || receipt.Result.Problem != nil {
		return ErrInvalidStore
	}
	if outcome == operation.BatchSweepAnonymousAlreadySwept {
		if receipt.Range != nil || first.Valid || last.Valid || count != 0 {
			return ErrInvalidStore
		}
		return nil
	}
	if !first.Valid || !last.Valid || first.Int64 != int64(position+1) || last.Int64 != first.Int64 || count != 1 ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != receipt.Range.Last {
		return ErrInvalidStore
	}
	var id string
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT event_id,record FROM authority_events WHERE domain_id=? AND position=? AND command_id=?`,
		submission.domain, position+1, submission.id).Scan(&id, &raw); err != nil || id != receipt.Range.First {
		return ErrInvalidStore
	}
	return validateBatchSweepEvent(command, submission.hash, position+1, id, raw)
}

func validateBatchSweepEvent(command operation.Command, hash string, position uint64, id string, raw []byte) error {
	event, err := parseStep12Event(raw, command.AuthorityDomainID, position, id, command.ID)
	input, ok := command.Request.Input.(operation.BatchSweepAnonymousInput)
	if err != nil || !ok || event.hash != hash || event.environment != command.EnvironmentID ||
		event.sequence != command.EnvironmentSequence || event.repo != command.Request.Context.Repo || event.acted != command.ActedAt ||
		event.kind != "batch.swept" || event.subject != input.BatchID || len(event.payload) != 0 {
		return ErrInvalidStore
	}
	return nil
}
