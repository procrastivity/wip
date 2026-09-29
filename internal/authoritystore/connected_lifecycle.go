package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func connectedLifecycleOperation(id operation.ID) bool {
	switch id {
	case operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
		operation.MatterFinishV1.Metadata().Operation:
		return true
	default:
		return false
	}
}

func (s *Store) submitConnectedLifecycle(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool,
) (CommandStatus, error) {
	var empty CommandStatus
	c, err := parseLifecycle(canonical, hash)
	if err != nil || c.name != command.Request.Operation.Name || c.domain != command.AuthorityDomainID ||
		c.epoch != command.ExpectedAuthorityEpoch || c.environment != command.EnvironmentID || c.sequence != command.EnvironmentSequence ||
		c.id != command.ID || c.repo != command.Request.Context.Repo || c.claimID == "" || c.claimEpoch == 0 {
		return empty, ErrInvalidProof
	}
	definition, ok := connectedLifecycleDefinition(command.Request.Operation)
	if !ok || definition.ValidateRequest(command.Request) != nil {
		return empty, ErrInvalidProof
	}
	metadata := definition.Metadata()
	if metadata.Delivery != operation.DeliveryClaim && metadata.Delivery != operation.DeliveryAuthority {
		return empty, ErrInvalidProof
	}
	c.lifecycle = c
	identity := c.commandIdentity
	identity.encoded = append([]byte(nil), canonical...)
	identity.m1 = &command
	identity.lifecycle = c
	before := func(tx *sql.Tx) error {
		if c.name == "step.start" || c.name == "step.finish" {
			var repo string
			if err := tx.QueryRowContext(ctx, `SELECT matter_id,repo_id FROM steps WHERE domain_id=? AND step_id=?`, c.domain, c.stepID).Scan(&c.matter, &repo); err != nil || repo != c.repo {
				return ErrFenced
			}
		}
		var matterRepo string
		if err := tx.QueryRowContext(ctx, `SELECT repo_id FROM matters WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&matterRepo); err != nil || matterRepo != c.repo {
			return ErrFenced
		}
		claim, err := loadClaim(ctx, tx, c, c.claimID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFenced
		}
		if err != nil {
			return err
		}
		if claim.closed.Valid || claim.authority != c.epoch || claim.epoch != c.claimEpoch ||
			claim.owner != c.environment || claim.repo != c.repo || claim.worktree != c.worktree || claim.matter != c.matter {
			return ErrFenced
		}
		return nil
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
	return s.submitIdentity(ctx, identity, peer, at, before, beforeCommit, nil)
}

func connectedLifecycleDefinition(id operation.ID) (operation.Definition, bool) {
	switch id {
	case operation.StepStartV1.Metadata().Operation:
		return operation.StepStartV1, true
	case operation.StepFinishV1.Metadata().Operation:
		return operation.StepFinishV1, true
	case operation.MatterFinishV1.Metadata().Operation:
		return operation.MatterFinishV1, true
	default:
		return operation.Definition{}, false
	}
}

// CompleteConnectedLifecycle atomically applies a validated connected Step or Matter finish.
func (s *Store) CompleteConnectedLifecycle(ctx context.Context, owner *Execution, eventIDs []string, occurred time.Time, sign Signer) (CommandStatus, error) {
	var empty CommandStatus
	if owner == nil || owner.store != s || owner.lifecycle == nil || !connectedLifecycleOperation(operation.ID{
		Name: owner.lifecycle.name, Version: uint16(owner.lifecycle.version),
	}) || sign == nil || occurred.IsZero() || len(eventIDs) != 2 {
		return empty, ErrInvalidProof
	}
	c := owner.lifecycle
	for index, id := range eventIDs {
		if !ulid.MatchString(id) || index > 0 && eventIDs[index-1] >= id {
			return empty, ErrInvalidProof
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := ownerKey(c.domain, c.id)
	if s.db == nil || !s.owners[key] || s.executions[key] != owner {
		return empty, ErrNotOwner
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	head, err := checkLifecycleOwner(ctx, tx, c, owner.hash)
	if err != nil {
		return empty, err
	}
	claim, claimErr := loadClaim(ctx, tx, c, c.claimID)
	if errors.Is(claimErr, sql.ErrNoRows) {
		claimErr = ErrFenced
	}
	if claimErr != nil && !errors.Is(claimErr, ErrFenced) {
		return empty, claimErr
	}
	problem := ""
	if errors.Is(claimErr, ErrFenced) || claim.closed.Valid || claim.authority != c.epoch || claim.epoch != c.claimEpoch ||
		claim.owner != c.environment || claim.repo != c.repo || claim.worktree != c.worktree || claim.matter != c.matter {
		problem = "refusal.claim-fenced"
	}
	matterState, stepState := "", ""
	if problem == "" {
		matterState, err = lifecycleStateTx(ctx, tx, c.domain, c.matter, "matter")
		if err != nil {
			return empty, err
		}
		if c.stepID != "" {
			stepState, err = lifecycleStateTx(ctx, tx, c.domain, c.stepID, "step")
			if err != nil {
				return empty, err
			}
		}
		switch c.name {
		case "step.start":
			if stepState != "planned" {
				problem = "refusal.invalid-transition"
			}
		case "step.finish":
			if stepState != "in-progress" {
				problem = "refusal.invalid-transition"
			}
		case "matter.finish":
			if matterState != "in-progress" {
				problem = "refusal.invalid-transition"
			}
		}
	}
	if problem != "" {
		result := operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemCode(problem), Message: problem}}
		return s.finishCommandTx(ctx, tx, c.commandIdentity, head, string(result.Code), nil, problem, nil, nil, nil, occurred, sign, nil)
	}
	identity := eventIdentity{c.domain, c.id, c.hash, c.environment, c.sequence, c.actedAt, c.repo}
	var first, last any
	var count uint64
	appendEvent := func(id, kind, subject string, payload map[string]any) error {
		position, appendErr := appendCommandEvent(ctx, tx, identity, occurred, id, kind, subject, payload)
		if appendErr != nil {
			return appendErr
		}
		if count == 0 {
			first = position
		}
		last = position
		count++
		return nil
	}
	var output operation.Output
	switch c.name {
	case "step.start":
		if matterState == "planned" {
			if err = appendEvent(eventIDs[0], "matter.started", c.matter, map[string]any{"from": "planned", "to": "in-progress", "cascade": true}); err != nil {
				return empty, err
			}
			if err = appendEvent(eventIDs[1], "step.started", c.stepID, map[string]any{"from": "planned", "to": "in-progress"}); err != nil {
				return empty, err
			}
		} else {
			if err = appendEvent(eventIDs[0], "step.started", c.stepID, map[string]any{"from": "planned", "to": "in-progress"}); err != nil {
				return empty, err
			}
		}
		output = operation.StepLifecycleOutput{StepID: c.stepID, MatterID: c.matter, State: "in-progress"}
	case "step.finish":
		if err = appendEvent(eventIDs[0], "step.finished", c.stepID, map[string]any{"from": "in-progress", "to": "done"}); err != nil {
			return empty, err
		}
		output = operation.StepLifecycleOutput{StepID: c.stepID, MatterID: c.matter, State: "done"}
	case "matter.finish":
		var batch string
		batchErr := tx.QueryRowContext(ctx, `SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, c.domain, c.matter).Scan(&batch)
		if batchErr != nil && !errors.Is(batchErr, sql.ErrNoRows) {
			return empty, batchErr
		}
		becameSealed, sealErr := matterSubtreeCompleteTx(ctx, tx, c.domain, c.matter)
		if sealErr != nil {
			return empty, sealErr
		}
		if err = appendEvent(eventIDs[0], "matter.finished", c.matter, map[string]any{"from": "in-progress", "to": "done"}); err != nil {
			return empty, err
		}
		if becameSealed && !errors.Is(batchErr, sql.ErrNoRows) {
			if err = appendEvent(eventIDs[1], "batch.swept", batch, map[string]any{}); err != nil {
				return empty, err
			}
		}
		output = operation.MatterFinishOutput{MatterID: c.matter, State: "done", BecameSealed: becameSealed}
	}
	definition, ok := connectedLifecycleDefinition(operation.ID{Name: c.name, Version: 1})
	result := operation.Result{Code: operation.ResultSucceeded, Output: output}
	if !ok || definition.ValidateResult(result) != nil {
		return empty, ErrInvalidProof
	}
	var encoded []byte
	switch value := output.(type) {
	case operation.StepLifecycleOutput:
		encoded, err = artifactEncoder.Marshal(map[string]any{
			"step_id": value.StepID, "matter_id": value.MatterID, "state": value.State,
		})
	case operation.MatterFinishOutput:
		encoded, err = artifactEncoder.Marshal(map[string]any{
			"matter_id": value.MatterID, "state": value.State, "became_sealed": value.BecameSealed,
		})
	default:
		return empty, ErrInvalidProof
	}
	if err != nil {
		return empty, err
	}
	if count == 0 {
		return empty, ErrInvalidProof
	}
	rangeValue := map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[count-1], "event_count": count}
	return s.finishCommandTx(ctx, tx, c.commandIdentity, head, string(result.Code), encoded, nil, rangeValue, first, last, occurred, sign, nil)
}

// matterSubtreeCompleteTx evaluates the M5 seal predicate from the Matter's
// authority-folded child Steps. Gate declarations are not modeled in this
// authority schema; every Step that is modeled must be Done before completion
// crosses the seal boundary.
func matterSubtreeCompleteTx(ctx context.Context, tx *sql.Tx, domain, matter string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT step_id FROM steps WHERE domain_id=? AND matter_id=? ORDER BY step_id`, domain, matter)
	if err != nil {
		return false, err
	}
	var steps []string
	for rows.Next() {
		var step string
		if err = rows.Scan(&step); err != nil {
			_ = rows.Close()
			return false, err
		}
		steps = append(steps, step)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	for _, step := range steps {
		state, stateErr := lifecycleStateTx(ctx, tx, domain, step, "step")
		if stateErr != nil {
			return false, stateErr
		}
		if state != "done" {
			return false, nil
		}
	}
	return true, nil
}

func lifecycleStateTx(ctx context.Context, tx *sql.Tx, domain, subject, scale string) (string, error) {
	state := "planned"
	rows, err := tx.QueryContext(ctx, `SELECT record FROM authority_events WHERE domain_id=? ORDER BY position`, domain)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var record []byte
		if err = rows.Scan(&record); err != nil {
			return "", err
		}
		var event struct {
			Kind    string         `cbor:"kind"`
			Subject string         `cbor:"subject_id"`
			Payload map[string]any `cbor:"payload"`
		}
		if artifactDecoder.Unmarshal(record, &event) != nil {
			return "", ErrInvalidStore
		}
		if event.Subject != subject {
			continue
		}
		if scale == "matter" && (event.Kind == "matter.started" || event.Kind == "matter.finished") ||
			scale == "step" && (event.Kind == "step.started" || event.Kind == "step.finished") {
			to, ok := event.Payload["to"].(string)
			if !ok {
				return "", ErrInvalidStore
			}
			state = to
		}
	}
	return state, rows.Err()
}
