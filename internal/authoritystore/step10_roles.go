package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func step10RoleDefinition(id operation.ID) (operation.Definition, bool) {
	switch id {
	case operation.RoleSpawnV1.Metadata().Operation:
		return operation.RoleSpawnV1, true
	case operation.RoleCloseV1.Metadata().Operation:
		return operation.RoleCloseV1, true
	default:
		return operation.Definition{}, false
	}
}

func (s *Store) submitStep10RunFinish(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	if operation.RunFinishV1.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return CommandStatus{}, ErrInvalidProof
	}
	input := command.Request.Input.(operation.RunFinishInput)
	before := func(tx *sql.Tx) error { return validateStep10RunFinishTx(ctx, tx, command, input) }
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

func (s *Store) submitStep10Role(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	definition, ok := step10RoleDefinition(command.Request.Operation)
	if !ok || definition.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return CommandStatus{}, ErrInvalidProof
	}
	before := func(tx *sql.Tx) error {
		return validateStep10RoleCommandTx(ctx, tx, command)
	}
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
	}, peer, at, before, beforeCommit, func(tx *sql.Tx) error {
		return appendConnectedClaimJournalEntry(ctx, tx, command, canonical, hash)
	})
}

type step10RunState struct {
	batch, locator, owner, clone, worktree string
	matters                                []string
	active                                 bool
}

func validateStep10RoleCommandTx(ctx context.Context, tx *sql.Tx, command operation.Command) error {
	input, ok := command.Request.Input.(operation.RoleLifecycleInput)
	if !ok || !operation.Step10RoleOperation(command.Request.Operation) {
		return ErrInvalidProof
	}
	if err := validateStep4ClaimTx(ctx, tx, command); err != nil {
		return err
	}
	var matter, dispatch, batch string
	if err := tx.QueryRowContext(ctx, `SELECT c.matter_id,c.dispatch_id,c.batch_id FROM claims c
		WHERE c.domain_id=? AND c.claim_id=? AND c.close_command_id IS NULL`,
		command.AuthorityDomainID, command.Request.Claim.ID).Scan(&matter, &dispatch, &batch); err != nil ||
		matter != input.MatterID || dispatch != input.DispatchID {
		return ErrFenced
	}
	run, found, err := step10RunStateTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	if found {
		if !run.active || run.batch != batch || run.locator != input.RunLocator || run.owner != command.EnvironmentID ||
			run.clone != command.Request.Context.Clone || run.worktree != command.Request.Context.Worktree ||
			!step10Contains(run.matters, input.MatterID) {
			return ErrFenced
		}
		batchMatters, err := step10BatchMattersTx(ctx, tx, command.AuthorityDomainID, batch)
		if err != nil || !equalStrings(batchMatters, run.matters) {
			return ErrFenced
		}
	} else if command.Request.Operation != operation.RoleSpawnV1.Metadata().Operation {
		return ErrFenced
	}
	if input.Role == "verifier" {
		var declared int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate='verified'`,
			command.AuthorityDomainID, command.Request.Context.Repo).Scan(&declared); err != nil || declared != 1 {
			return ErrFenced
		}
	}
	active, err := step10RolesAtTx(ctx, tx, command.AuthorityDomainID, "")
	if err != nil {
		return err
	}
	if command.Request.Operation == operation.RoleSpawnV1.Metadata().Operation {
		if _, exists := active[input.RoleID]; exists {
			return ErrConflict
		}
		for _, role := range active {
			if role.dispatch == input.DispatchID && role.name == input.Role {
				return ErrConflict
			}
		}
		return nil
	}
	role, exists := active[input.RoleID]
	if !exists || role.name != input.Role || role.dispatch != input.DispatchID || role.matter != input.MatterID ||
		role.claim != command.Request.Claim.ID || role.claimEpoch != command.Request.Claim.Epoch {
		return ErrFenced
	}
	if command.Request.Operation == operation.RoleCloseV1.Metadata().Operation {
		if incomplete, err := step10PlanBirthsIncompleteTx(ctx, tx, command.AuthorityDomainID, command.Request.Claim.ID, input.RoleID); err != nil || incomplete {
			return ErrPending
		}
		if incomplete, err := step10WorkConsumptionIncompleteTx(ctx, tx, command.AuthorityDomainID,
			command.Request.Claim.ID, input.RoleID); err != nil || incomplete {
			return ErrPending
		}
	}
	return nil
}

func validateStep10RunFinishTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.RunFinishInput) error {
	run, found, err := step10RunStateTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	if !found || !run.active || run.owner != command.EnvironmentID || run.clone != command.Request.Context.Clone ||
		run.worktree != command.Request.Context.Worktree || run.batch == "" || len(run.matters) == 0 {
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
	for _, matter := range run.matters {
		state, stateErr := lifecycleStateTx(ctx, tx, command.AuthorityDomainID, matter, "matter")
		if stateErr != nil {
			return stateErr
		}
		if state != "done" {
			return ErrFenced
		}
	}
	return nil
}

func completeStep10RunFinishTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	result operation.Result, subjectID, eventID string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{result: result, subject: subjectID}
	input, ok := command.Request.Input.(operation.RunFinishInput)
	output, outputOK := result.Output.(operation.RunFinishOutput)
	if !ok || !outputOK || result.Code != operation.ResultSucceeded || result.Problem != nil ||
		operation.RunFinishV1.ValidateResult(result) != nil || input.RunID != subjectID || output.RunID != input.RunID || output.State != "finished" ||
		!ulid.MatchString(eventID) || !occurred.After(time.Time{}) {
		return fold, ErrInvalidProof
	}
	if err := validateStep10RunFinishTx(ctx, tx, command, input); err != nil {
		return fold, err
	}
	position, err := appendCommandEvent(ctx, tx, identity, occurred, eventID, "run.finished", input.RunID, map[string]any{})
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

func validateStep10RunFinishHistoryEvent(db *sql.DB, event step12Event, submission storedSubmission, command operation.Command) error {
	input, ok := command.Request.Input.(operation.RunFinishInput)
	if !ok || command.Request.Operation != operation.RunFinishV1.Metadata().Operation ||
		operation.RunFinishV1.ValidateRequest(command.Request) != nil || command.Request.Claim != nil ||
		event.domain != submission.domain || event.command != submission.id || event.hash != submission.hash ||
		event.environment != submission.env || event.sequence != submission.seq || event.repo != command.Request.Context.Repo ||
		event.acted != command.ActedAt || event.kind != "run.finished" || event.subject != input.RunID || len(event.payload) != 0 {
		return ErrInvalidStore
	}
	var owner string
	rows, err := db.Query(`SELECT e.position,e.event_id,e.command_id,e.record,s.environment_id FROM authority_events e
		JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id WHERE e.domain_id=? ORDER BY e.position`, submission.domain)
	if err != nil {
		return err
	}
	for rows.Next() {
		var position uint64
		var eventID, commandID, environment string
		var raw []byte
		if err = rows.Scan(&position, &eventID, &commandID, &raw, &environment); err != nil {
			_ = rows.Close()
			return err
		}
		started, parseErr := parseStep12Event(raw, submission.domain, position, eventID, commandID)
		if parseErr != nil {
			_ = rows.Close()
			return ErrInvalidStore
		}
		if started.kind == "run.started" && started.subject == input.RunID {
			if owner != "" {
				_ = rows.Close()
				return ErrInvalidStore
			}
			owner = environment
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil || owner == "" || owner != submission.env {
		return ErrInvalidStore
	}
	var receiptBytes []byte
	if err = db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&receiptBytes); err != nil {
		return ErrInvalidStore
	}
	receipt, err := readReceipt(receiptBytes)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil || receipt.Range == nil ||
		receipt.Range.Count != 1 || receipt.Range.First != event.id || receipt.Range.Last != event.id {
		return ErrInvalidStore
	}
	var output operation.RunFinishOutput
	if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil ||
		operation.RunFinishV1.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil ||
		output.RunID != input.RunID || output.State != "finished" {
		return ErrInvalidStore
	}
	return nil
}

type step10RoleState struct {
	runID, name, matter, dispatch, claim, claimEpoch string
}

func step10RunStateTx(ctx context.Context, tx *sql.Tx, domain, runID string) (step10RunState, bool, error) {
	var state step10RunState
	rows, err := tx.QueryContext(ctx, `SELECT e.position,e.event_id,e.command_id,e.record,s.command,s.environment_id
		FROM authority_events e JOIN submissions s ON s.domain_id=e.domain_id AND s.command_id=e.command_id
		WHERE e.domain_id=? AND ((e.record IS NOT NULL)) ORDER BY e.position`, domain)
	if err != nil {
		return state, false, err
	}
	defer func() { _ = rows.Close() }()
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
			return state, false, parseErr
		}
		if event.subject != runID || event.kind != "run.started" && event.kind != "run.finished" && event.kind != "run.stood-down" {
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
			if !ok || input.RunID != runID || input.RunLocator != locator || input.MatterID == "" {
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
		}
	}
	if err = rows.Err(); err != nil {
		return state, false, err
	}
	return state, found, nil
}

func step10RolesAtTx(ctx context.Context, tx *sql.Tx, domain, runID string) (map[string]step10RoleState, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.position,e.event_id,e.command_id,e.record FROM authority_events e
		WHERE e.domain_id=? ORDER BY e.position`, domain)
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
			return nil, parseErr
		}
		if event.kind != "role.spawned" && event.kind != "role.closed" {
			continue
		}
		var roleRun string
		if artifactDecoder.Unmarshal(event.payload["run_id"], &roleRun) != nil {
			return nil, ErrInvalidStore
		}
		if runID != "" && roleRun != runID {
			continue
		}
		if event.kind == "role.spawned" {
			var value step10RoleState
			if !exactKeys(event.payload, "run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name") ||
				artifactDecoder.Unmarshal(event.payload["run_id"], &value.runID) != nil ||
				artifactDecoder.Unmarshal(event.payload["matter_id"], &value.matter) != nil ||
				artifactDecoder.Unmarshal(event.payload["dispatch_id"], &value.dispatch) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_id"], &value.claim) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_epoch"], &value.claimEpoch) != nil ||
				artifactDecoder.Unmarshal(event.payload["name"], &value.name) != nil || !ulid.MatchString(event.subject) {
				return nil, ErrInvalidStore
			}
			if _, exists := active[event.subject]; exists {
				return nil, ErrInvalidStore
			}
			active[event.subject] = value
		} else {
			value, exists := active[event.subject]
			var matter, dispatch, claim, claimEpoch, name string
			if !exists || value.runID != roleRun || !exactKeys(event.payload, "run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name", "reason") ||
				artifactDecoder.Unmarshal(event.payload["matter_id"], &matter) != nil ||
				artifactDecoder.Unmarshal(event.payload["dispatch_id"], &dispatch) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_id"], &claim) != nil ||
				artifactDecoder.Unmarshal(event.payload["claim_epoch"], &claimEpoch) != nil ||
				artifactDecoder.Unmarshal(event.payload["name"], &name) != nil || value.matter != matter ||
				value.dispatch != dispatch || value.claim != claim || value.claimEpoch != claimEpoch || value.name != name {
				return nil, ErrInvalidStore
			}
			delete(active, event.subject)
		}
	}
	return active, rows.Err()
}

func completeStep10RoleTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	result operation.Result, subjectID, eventID string, additionalEventIDs []string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{result: result, subject: subjectID}
	input, ok := command.Request.Input.(operation.RoleLifecycleInput)
	definition, defined := step10RoleDefinition(command.Request.Operation)
	if !ok || !defined || definition.ValidateResult(result) != nil || result.Code != operation.ResultSucceeded || result.Problem != nil {
		return fold, ErrInvalidProof
	}
	output, ok := result.Output.(operation.RoleLifecycleOutput)
	state := "open"
	if command.Request.Operation == operation.RoleCloseV1.Metadata().Operation {
		state = "closed"
	}
	if !ok || output.RunID != input.RunID || output.MatterID != input.MatterID || output.DispatchID != input.DispatchID ||
		output.RoleID != input.RoleID || output.Role != input.Role || output.State != state || subjectID != input.RoleID {
		return fold, ErrInvalidProof
	}
	if err := validateStep10RoleCommandTx(ctx, tx, command); err != nil {
		return fold, err
	}
	if !ulid.MatchString(eventID) || len(additionalEventIDs) > 1 || len(additionalEventIDs) == 1 &&
		(!ulid.MatchString(additionalEventIDs[0]) || additionalEventIDs[0] <= eventID) {
		return fold, ErrInvalidProof
	}
	appendRoleEvent := func(id, kind string, payload map[string]any) (uint64, error) {
		return appendCommandEvent(ctx, tx, identity, occurred, id, kind, subjectID, payload)
	}
	var first, last uint64
	eventCount := uint64(1)
	if command.Request.Operation == operation.RoleSpawnV1.Metadata().Operation {
		_, found, err := step10RunStateTx(ctx, tx, command.AuthorityDomainID, input.RunID)
		if err != nil {
			return fold, err
		}
		if !found {
			if len(additionalEventIDs) != 1 {
				return fold, ErrInvalidProof
			}
			var batch string
			if err = tx.QueryRowContext(ctx, `SELECT batch_id FROM claims WHERE domain_id=? AND claim_id=? AND close_command_id IS NULL`,
				command.AuthorityDomainID, command.Request.Claim.ID).Scan(&batch); err != nil {
				return fold, ErrFenced
			}
			rows, queryErr := tx.QueryContext(ctx, `SELECT matter_id FROM anonymous_batches WHERE domain_id=? AND batch_id=? ORDER BY matter_id`,
				command.AuthorityDomainID, batch)
			if queryErr != nil {
				return fold, queryErr
			}
			var matters []string
			for rows.Next() {
				var matter string
				if queryErr = rows.Scan(&matter); queryErr != nil {
					break
				}
				matters = append(matters, matter)
			}
			queryErr = errors.Join(queryErr, rows.Err(), rows.Close())
			if queryErr != nil || !step10Contains(matters, input.MatterID) || len(matters) == 0 {
				return fold, ErrFenced
			}
			first, err = appendCommandEvent(ctx, tx, identity, occurred, eventID, "run.started", input.RunID,
				map[string]any{"batch": batch, "locator": input.RunLocator, "matters": matters})
			if err != nil {
				return fold, err
			}
			roleEventID := additionalEventIDs[0]
			last, err = appendRoleEvent(roleEventID, "role.spawned", map[string]any{
				"run_id": input.RunID, "matter_id": input.MatterID, "dispatch_id": input.DispatchID,
				"claim_id": command.Request.Claim.ID, "claim_epoch": command.Request.Claim.Epoch, "name": input.Role,
			})
			if err != nil {
				return fold, err
			}
			eventCount = 2
		} else {
			first, err = appendRoleEvent(eventID, "role.spawned", map[string]any{
				"run_id": input.RunID, "matter_id": input.MatterID, "dispatch_id": input.DispatchID,
				"claim_id": command.Request.Claim.ID, "claim_epoch": command.Request.Claim.Epoch, "name": input.Role,
			})
			if err != nil {
				return fold, err
			}
			last = first
		}
	} else {
		if len(additionalEventIDs) != 0 {
			return fold, ErrInvalidProof
		}
		var err error
		first, err = appendRoleEvent(eventID, "role.closed", map[string]any{
			"run_id": input.RunID, "matter_id": input.MatterID, "dispatch_id": input.DispatchID,
			"claim_id": command.Request.Claim.ID, "claim_epoch": command.Request.Claim.Epoch,
			"name": input.Role, "reason": input.Reason,
		})
		if err != nil {
			return fold, err
		}
		last = first
	}
	rawOutput, err := artifactEncoder.Marshal(output)
	if err != nil {
		return fold, err
	}
	fold.first, fold.last, fold.output = first, last, rawOutput
	fold.rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": eventID, "event_count": eventCount}
	if eventCount == 2 {
		fold.rangeValue = map[string]any{"first_event_id": eventID, "last_event_id": additionalEventIDs[0], "event_count": eventCount}
	}
	fold.eventIDs = []string{eventID}
	if eventCount == 2 {
		fold.eventIDs = append(fold.eventIDs, additionalEventIDs[0])
	}
	fold.subject = subjectID
	return fold, nil
}

func activeRoleForGateTx(ctx context.Context, tx *sql.Tx, command operation.Command, roleName string) (bool, error) {
	claim := command.Request.Claim
	if claim == nil {
		return false, nil
	}
	var dispatch, matter string
	if err := tx.QueryRowContext(ctx, `SELECT dispatch_id,matter_id FROM claims WHERE domain_id=? AND claim_id=? AND close_command_id IS NULL`,
		command.AuthorityDomainID, claim.ID).Scan(&dispatch, &matter); err != nil {
		return false, nil
	}
	var epoch uint64
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil {
		return false, nil
	}
	roles, err := step10RolesAtTx(ctx, tx, command.AuthorityDomainID, "")
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if role.name == roleName && role.matter == matter && role.dispatch == dispatch && role.claim == claim.ID && role.claimEpoch == strconv.FormatUint(epoch, 10) {
			run, found, runErr := step10RunStateTx(ctx, tx, command.AuthorityDomainID, role.runID)
			if runErr != nil {
				return false, runErr
			}
			if found && run.active && run.owner == command.EnvironmentID && run.clone == command.Request.Context.Clone &&
				run.worktree == command.Request.Context.Worktree && step10Contains(run.matters, matter) {
				return true, nil
			}
		}
	}
	return false, nil
}

func validateRunOwnerForSchedulerHookTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.SchedulerHookInput) error {
	run, found, err := step10RunStateTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil || !found || !run.active || run.owner != command.EnvironmentID || run.clone != command.Request.Context.Clone ||
		run.worktree != command.Request.Context.Worktree || !step10Contains(run.matters, input.MatterID) {
		return ErrFenced
	}
	return nil
}

func validateSchedulerHookContextTx(ctx context.Context, tx *sql.Tx, command operation.Command, input operation.SchedulerHookInput) error {
	if err := validateRunOwnerForSchedulerHookTx(ctx, tx, command, input); err != nil {
		return err
	}
	if input.Kind == "ask" {
		return nil
	}
	if command.Request.Claim == nil {
		return ErrFenced
	}
	var matter, dispatch string
	if err := tx.QueryRowContext(ctx, `SELECT matter_id,dispatch_id FROM claims WHERE domain_id=? AND claim_id=? AND close_command_id IS NULL`,
		command.AuthorityDomainID, command.Request.Claim.ID).Scan(&matter, &dispatch); err != nil || matter != input.MatterID || dispatch != input.DispatchID {
		return ErrFenced
	}
	epoch, err := strconv.ParseUint(command.Request.Claim.Epoch, 10, 64)
	if err != nil {
		return ErrFenced
	}
	roles, err := step10RolesAtTx(ctx, tx, command.AuthorityDomainID, input.RunID)
	if err != nil {
		return err
	}
	role, exists := roles[input.RoleID]
	if !exists || role.name != input.Role || role.matter != input.MatterID || role.dispatch != input.DispatchID ||
		role.claim != command.Request.Claim.ID || role.claimEpoch != strconv.FormatUint(epoch, 10) {
		return ErrFenced
	}
	return nil
}

func step10Contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func step10BatchMattersTx(ctx context.Context, tx *sql.Tx, domain, batch string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT matter_id FROM anonymous_batches WHERE domain_id=? AND batch_id=? ORDER BY matter_id`, domain, batch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var matters []string
	for rows.Next() {
		var matter string
		if err = rows.Scan(&matter); err != nil {
			return nil, err
		}
		matters = append(matters, matter)
	}
	return matters, rows.Err()
}

func validateStep10RoleHistoryEvent(db *sql.DB, event step12Event, submission storedSubmission, command operation.Command) error {
	input, ok := command.Request.Input.(operation.RoleLifecycleInput)
	definition, defined := step10RoleDefinition(command.Request.Operation)
	if !ok || !defined || definition.ValidateRequest(command.Request) != nil || event.domain != submission.domain ||
		event.command != submission.id || event.hash != submission.hash || event.environment != submission.env ||
		event.sequence != submission.seq || event.repo != command.Request.Context.Repo || command.Request.Claim == nil {
		return ErrInvalidStore
	}
	var claimMatter, dispatch, batch, owner string
	var epoch uint64
	if err := db.QueryRow(`SELECT matter_id,dispatch_id,batch_id,owner_environment_id,claim_epoch FROM claims
		WHERE domain_id=? AND claim_id=?`, submission.domain, command.Request.Claim.ID).
		Scan(&claimMatter, &dispatch, &batch, &owner, &epoch); err != nil || claimMatter != input.MatterID ||
		dispatch != input.DispatchID || owner != submission.env || strconv.FormatUint(epoch, 10) != command.Request.Claim.Epoch {
		return ErrInvalidStore
	}
	var receiptBytes []byte
	if err := db.QueryRow(`SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, submission.domain, submission.id).Scan(&receiptBytes); err != nil {
		return ErrInvalidStore
	}
	receipt, err := readReceipt(receiptBytes)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil {
		return ErrInvalidStore
	}
	var output operation.RoleLifecycleOutput
	if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil || definition.ValidateResult(
		operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil || output.RunID != input.RunID ||
		output.MatterID != input.MatterID || output.DispatchID != input.DispatchID || output.RoleID != input.RoleID || output.Role != input.Role {
		return ErrInvalidStore
	}
	var rows *sql.Rows
	rows, err = db.Query(`SELECT position,event_id,record FROM authority_events WHERE domain_id=? AND command_id=? ORDER BY position`,
		submission.domain, submission.id)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type storedRoleEvent struct {
		position uint64
		id       string
		event    step12Event
	}
	var events []storedRoleEvent
	for rows.Next() {
		var position uint64
		var id string
		var raw []byte
		if err = rows.Scan(&position, &id, &raw); err != nil {
			return err
		}
		parsed, parseErr := parseStep12Event(raw, submission.domain, position, id, submission.id)
		if parseErr != nil {
			return ErrInvalidStore
		}
		events = append(events, storedRoleEvent{position: position, id: id, event: parsed})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if receipt.Range == nil || uint64(len(events)) != receipt.Range.Count || len(events) == 0 ||
		events[0].id != receipt.Range.First || events[len(events)-1].id != receipt.Range.Last {
		return ErrInvalidStore
	}
	eventFound := false
	for _, stored := range events {
		if stored.id == event.id && stored.position == event.position && stored.event.kind == event.kind && stored.event.subject == event.subject {
			eventFound = true
			break
		}
	}
	if !eventFound {
		return ErrInvalidStore
	}
	if command.Request.Operation == operation.RoleSpawnV1.Metadata().Operation {
		output.State = "open"
		if len(events) == 2 {
			started := events[0].event
			if started.kind != "run.started" || started.subject != input.RunID ||
				!exactKeys(started.payload, "batch", "locator", "matters") {
				return ErrInvalidStore
			}
			var gotBatch, locator string
			var gotMatters []string
			if artifactDecoder.Unmarshal(started.payload["batch"], &gotBatch) != nil || gotBatch != batch ||
				artifactDecoder.Unmarshal(started.payload["locator"], &locator) != nil || locator != input.RunLocator ||
				artifactDecoder.Unmarshal(started.payload["matters"], &gotMatters) != nil {
				return ErrInvalidStore
			}
			batchMatters, queryErr := db.Query(`SELECT matter_id FROM anonymous_batches WHERE domain_id=? AND batch_id=? ORDER BY matter_id`,
				submission.domain, batch)
			if queryErr != nil {
				return queryErr
			}
			var expectedMatters []string
			for batchMatters.Next() {
				var matter string
				if queryErr = batchMatters.Scan(&matter); queryErr != nil {
					break
				}
				expectedMatters = append(expectedMatters, matter)
			}
			queryErr = errors.Join(queryErr, batchMatters.Err(), batchMatters.Close())
			if queryErr != nil || !equalStrings(gotMatters, expectedMatters) || !step10Contains(gotMatters, input.MatterID) {
				return ErrInvalidStore
			}
		} else if len(events) != 1 {
			return ErrInvalidStore
		}
		roleEvent := events[len(events)-1].event
		if roleEvent.kind != "role.spawned" || roleEvent.subject != input.RoleID ||
			validateStep10RoleEventPayload(roleEvent, input, command.Request.Claim, "") != nil {
			return ErrInvalidStore
		}
	} else {
		output.State = "closed"
		if len(events) != 1 || events[0].event.kind != "role.closed" || events[0].event.subject != input.RoleID ||
			validateStep10RoleEventPayload(events[0].event, input, command.Request.Claim, input.Reason) != nil {
			return ErrInvalidStore
		}
	}
	if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil || definition.ValidateResult(
		operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
		return ErrInvalidStore
	}
	return nil
}

func validateStep10RoleEventPayload(event step12Event, input operation.RoleLifecycleInput, claim *operation.ClaimContext, reason string) error {
	wantKeys := []string{"run_id", "matter_id", "dispatch_id", "claim_id", "claim_epoch", "name"}
	if reason != "" {
		wantKeys = append(wantKeys, "reason")
	}
	if !exactKeys(event.payload, wantKeys...) {
		return ErrInvalidStore
	}
	var runID, matter, dispatch, claimID, claimEpoch, name string
	if artifactDecoder.Unmarshal(event.payload["run_id"], &runID) != nil ||
		artifactDecoder.Unmarshal(event.payload["matter_id"], &matter) != nil ||
		artifactDecoder.Unmarshal(event.payload["dispatch_id"], &dispatch) != nil ||
		artifactDecoder.Unmarshal(event.payload["claim_id"], &claimID) != nil ||
		artifactDecoder.Unmarshal(event.payload["claim_epoch"], &claimEpoch) != nil ||
		artifactDecoder.Unmarshal(event.payload["name"], &name) != nil || runID != input.RunID ||
		matter != input.MatterID || dispatch != input.DispatchID || claim == nil || claimID != claim.ID ||
		claimEpoch != claim.Epoch || name != input.Role {
		return ErrInvalidStore
	}
	if reason != "" {
		var gotReason string
		if artifactDecoder.Unmarshal(event.payload["reason"], &gotReason) != nil || gotReason != reason {
			return ErrInvalidStore
		}
	}
	return nil
}
