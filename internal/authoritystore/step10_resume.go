package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func (s *Store) submitStep10RunResume(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	if operation.SchedulerResumeV1.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return CommandStatus{}, ErrInvalidProof
	}
	input := command.Request.Input.(operation.RunResumeInput)
	before := func(tx *sql.Tx) error { return validateStep10RunResumeTx(ctx, tx, command, input) }
	var beforeCommit func() error
	if checkContext {
		beforeCommit = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return context.DeadlineExceeded
			}
			return nil
		}
	}
	return s.submitIdentity(ctx, commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch,
		environment: command.EnvironmentID, sequence: command.EnvironmentSequence,
		id: command.ID, name: command.Request.Operation.Name, version: uint64(command.Request.Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, before, beforeCommit, nil)
}

func validateStep10RunResumeTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.RunResumeInput) error {
	run, found, err := step10RunStateTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	if !found || !run.active || run.locator != input.RunLocator || run.owner != command.EnvironmentID ||
		run.clone != command.Request.Context.Clone || run.worktree != command.Request.Context.Worktree || run.batch == "" {
		return ErrFenced
	}
	roles, err := step10RolesAtTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	if len(roles) != 0 {
		return ErrFenced
	}
	var openClaims int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE domain_id=? AND batch_id=? AND close_command_id IS NULL`,
		command.AuthorityDomainID, run.batch).Scan(&openClaims); err != nil {
		return err
	}
	if openClaims != 0 {
		return ErrFenced
	}
	return nil
}

func completeStep10RunResumeTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	result operation.Result, subjectID, eventID string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{result: result, subject: subjectID}
	input, inputOK := command.Request.Input.(operation.RunResumeInput)
	output, outputOK := result.Output.(operation.RunResumeOutput)
	if !inputOK || !outputOK || result.Code != operation.ResultSucceeded || result.Problem != nil ||
		operation.SchedulerResumeV1.ValidateResult(result) != nil || input.RunID != subjectID ||
		output.RunID != input.RunID || output.State != "resumed" || !ulid.MatchString(eventID) || !occurred.After(time.Time{}) {
		return fold, ErrInvalidProof
	}
	if err := validateStep10RunResumeTx(ctx, tx, command, input); err != nil {
		return fold, err
	}
	position, err := appendCommandEvent(ctx, tx, identity, occurred, eventID, "run.resumed", input.RunID, map[string]any{})
	if err != nil {
		return fold, err
	}
	encoded, err := artifactEncoder.Marshal(output)
	if err != nil {
		return fold, err
	}
	fold.first, fold.last, fold.output = position, position, encoded
	fold.rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": uint64(1)}
	fold.eventIDs = []string{eventID}
	return fold, nil
}

func validateStep10RunResumeHistoryEvent(db *sql.DB, event step12Event, submission storedSubmission, command operation.Command) error {
	input, ok := command.Request.Input.(operation.RunResumeInput)
	if !ok || command.Request.Operation != operation.SchedulerResumeV1.Metadata().Operation ||
		operation.SchedulerResumeV1.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
		event.domain != submission.domain || event.command != submission.id || event.hash != submission.hash ||
		event.environment != submission.env || event.sequence != submission.seq || event.repo != command.Request.Context.Repo ||
		event.acted != command.ActedAt || event.kind != "run.resumed" || event.subject != input.RunID || len(event.payload) != 0 {
		return ErrInvalidStore
	}
	state, found, err := step10RunStateBeforeEvent(db, submission.domain, input.RunID, event.position)
	if err != nil || !found || !state.active || state.locator != input.RunLocator || state.owner != submission.env ||
		state.clone != command.Request.Context.Clone || state.worktree != command.Request.Context.Worktree {
		return ErrInvalidStore
	}
	activeRoles, err := step10RolesAtPosition(db, submission.domain, input.RunID, event.position)
	if err != nil || len(activeRoles) != 0 {
		return ErrInvalidStore
	}
	var openClaims int
	err = db.QueryRow(`SELECT count(*) FROM claims c
		JOIN terminal_receipts acquired ON acquired.domain_id=c.domain_id AND acquired.command_id=c.acquire_command_id
		LEFT JOIN terminal_receipts closed ON closed.domain_id=c.domain_id AND closed.command_id=c.close_command_id
		WHERE c.domain_id=? AND c.batch_id=? AND acquired.first_position<? AND
			(c.close_command_id IS NULL OR closed.first_position IS NULL OR closed.first_position>=?)`,
		submission.domain, state.batch, event.position, event.position).Scan(&openClaims)
	if err != nil || openClaims != 0 {
		return ErrInvalidStore
	}
	var receiptBytes []byte
	if err = db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&receiptBytes); err != nil {
		return ErrInvalidStore
	}
	receipt, err := readReceipt(receiptBytes)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != event.id || receipt.Range.Last != event.id {
		return ErrInvalidStore
	}
	var output operation.RunResumeOutput
	if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil ||
		operation.SchedulerResumeV1.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil ||
		output.RunID != input.RunID || output.State != "resumed" {
		return ErrInvalidStore
	}
	return nil
}

func step10RunStateBeforeEvent(db *sql.DB, domain, runID string, before uint64) (step10RunState, bool, error) {
	rows, err := db.Query(`SELECT e.position,e.event_id,e.command_id,e.record,s.command,s.environment_id
		FROM authority_events e JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		WHERE e.domain_id=? AND e.position<? ORDER BY e.position`, domain, before)
	if err != nil {
		return step10RunState{}, false, err
	}
	defer func() { _ = rows.Close() }()
	var state step10RunState
	found := false
	for rows.Next() {
		var position uint64
		var eventID, commandID, environment string
		var raw, canonical []byte
		if err = rows.Scan(&position, &eventID, &commandID, &raw, &canonical, &environment); err != nil {
			return state, false, err
		}
		event, parseErr := parseStep12Event(raw, domain, position, eventID, commandID)
		if parseErr != nil {
			return state, false, ErrInvalidStore
		}
		if event.subject != runID {
			continue
		}
		switch event.kind {
		case "run.started":
			if found || !exactKeys(event.payload, "batch", "locator", "matters") {
				return state, false, ErrInvalidStore
			}
			var batch, locator string
			var matters []string
			if artifactDecoder.Unmarshal(event.payload["batch"], &batch) != nil || artifactDecoder.Unmarshal(event.payload["locator"], &locator) != nil ||
				artifactDecoder.Unmarshal(event.payload["matters"], &matters) != nil || !ulid.MatchString(batch) || locator == "" || len(matters) == 0 {
				return state, false, ErrInvalidStore
			}
			started, decodeErr := operation.DecodeCanonicalCommand(canonical)
			if decodeErr != nil || started.ID != commandID || started.EnvironmentID != environment {
				return state, false, ErrInvalidStore
			}
			input, ok := started.Request.Input.(operation.RoleLifecycleInput)
			if !ok || input.RunID != runID || input.RunLocator != locator {
				return state, false, ErrInvalidStore
			}
			state = step10RunState{
				batch: batch, locator: locator, owner: environment,
				clone: started.Request.Context.Clone, worktree: started.Request.Context.Worktree,
				matters: matters, active: true,
			}
			found = true
		case "run.finished", "run.stood-down":
			if !found || !state.active || len(event.payload) != 0 {
				return state, false, ErrInvalidStore
			}
			state.active = false
		case "run.resumed":
			if !found || !state.active || len(event.payload) != 0 {
				return state, false, ErrInvalidStore
			}
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return state, false, err
	}
	return state, found, nil
}

func step10RolesAtPosition(db *sql.DB, domain, runID string, before uint64) (map[string]step10RoleState, error) {
	rows, err := db.Query(`SELECT e.position,e.event_id,e.command_id,e.record FROM authority_events e
		WHERE e.domain_id=? AND e.position<? ORDER BY e.position`, domain, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	active := make(map[string]step10RoleState)
	for rows.Next() {
		var position uint64
		var eventID, commandID string
		var raw []byte
		if err = rows.Scan(&position, &eventID, &commandID, &raw); err != nil {
			return nil, err
		}
		event, parseErr := parseStep12Event(raw, domain, position, eventID, commandID)
		if parseErr != nil {
			return nil, ErrInvalidStore
		}
		if event.kind != "role.spawned" && event.kind != "role.closed" {
			continue
		}
		var roleRun string
		if artifactDecoder.Unmarshal(event.payload["run_id"], &roleRun) != nil {
			return nil, ErrInvalidStore
		}
		if roleRun != runID {
			continue
		}
		if event.kind == "role.spawned" {
			var role step10RoleState
			if !exactKeys(event.payload, "run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name") ||
				artifactDecoder.Unmarshal(event.payload["matter_id"], &role.matter) != nil ||
				artifactDecoder.Unmarshal(event.payload["dispatch_id"], &role.dispatch) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_id"], &role.claim) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_epoch"], &role.claimEpoch) != nil ||
				artifactDecoder.Unmarshal(event.payload["name"], &role.name) != nil {
				return nil, ErrInvalidStore
			}
			role.runID = roleRun
			if _, exists := active[event.subject]; exists {
				return nil, ErrInvalidStore
			}
			active[event.subject] = role
		} else {
			role, exists := active[event.subject]
			var matter, dispatch, claim, claimEpoch, name string
			if !exists || !exactKeys(event.payload, "run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name", "reason") ||
				artifactDecoder.Unmarshal(event.payload["matter_id"], &matter) != nil ||
				artifactDecoder.Unmarshal(event.payload["dispatch_id"], &dispatch) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_id"], &claim) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_epoch"], &claimEpoch) != nil ||
				artifactDecoder.Unmarshal(event.payload["name"], &name) != nil || role.matter != matter || role.dispatch != dispatch ||
				role.claim != claim || role.claimEpoch != claimEpoch || role.name != name {
				return nil, ErrInvalidStore
			}
			delete(active, event.subject)
		}
	}
	return active, rows.Err()
}
