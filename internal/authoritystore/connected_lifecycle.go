package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func connectedLifecycleOperation(id operation.ID) bool {
	switch id {
	case operation.StepStartV1.Metadata().Operation, operation.StepFinishV1.Metadata().Operation,
		operation.MatterFinishV1.Metadata().Operation, operation.MatterStartV1.Metadata().Operation,
		operation.StageStartV1.Metadata().Operation, operation.MatterPauseV1.Metadata().Operation,
		operation.StagePauseV1.Metadata().Operation, operation.StepPauseV1.Metadata().Operation,
		operation.MatterResumeV1.Metadata().Operation, operation.StageResumeV1.Metadata().Operation,
		operation.StepResumeV1.Metadata().Operation, operation.MatterCancelV1.Metadata().Operation,
		operation.StageCancelV1.Metadata().Operation, operation.StepCancelV1.Metadata().Operation,
		operation.StageFinishV1.Metadata().Operation:
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
		if err := resolveConnectedLifecycleMatter(ctx, tx, c); err != nil {
			return err
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
	var afterInsert func(*sql.Tx) error
	if metadata.Delivery == operation.DeliveryClaim {
		afterInsert = func(tx *sql.Tx) error {
			return appendConnectedClaimJournalEntry(ctx, tx, command, canonical, hash)
		}
	}
	return s.submitIdentity(ctx, identity, peer, at, before, beforeCommit, afterInsert)
}

func appendConnectedClaimJournalEntry(ctx context.Context, tx *sql.Tx, command operation.Command, canonical []byte, hash string) error {
	parsed, err := parseJournalCommand(canonical, hash)
	if err != nil || command.Request.Claim == nil || parsed.ID != command.ID || parsed.Domain != command.AuthorityDomainID ||
		parsed.Epoch != command.ExpectedAuthorityEpoch || parsed.Environment != command.EnvironmentID ||
		parsed.Sequence != command.EnvironmentSequence || parsed.Repo != command.Request.Context.Repo ||
		parsed.Worktree != command.Request.Context.Worktree || parsed.Claim != command.Request.Claim.ID {
		return ErrInvalidProof
	}
	claimEpoch, err := strconv.ParseUint(command.Request.Claim.Epoch, 10, 64)
	if err != nil || strconv.FormatUint(claimEpoch, 10) != command.Request.Claim.Epoch || claimEpoch != parsed.ClaimEpoch {
		return ErrInvalidProof
	}
	var journalID, journalState, owner, worktree, repo string
	var authorityEpoch, storedClaimEpoch uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT j.journal_id,j.state,c.owner_environment_id,c.worktree_id,m.repo_id,c.authority_epoch,c.claim_epoch,c.close_command_id
		FROM claim_journals j JOIN claims c USING(domain_id,claim_id) JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		JOIN domains d ON d.domain_id=c.domain_id
		WHERE j.domain_id=? AND j.claim_id=? AND j.state IN ('open','sealed') AND c.authority_epoch=d.active_epoch`, parsed.Domain, parsed.Claim).
		Scan(&journalID, &journalState, &owner, &worktree, &repo, &authorityEpoch, &storedClaimEpoch, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if journalState != "open" || closed.Valid || owner != parsed.Environment || worktree != parsed.Worktree ||
		repo != parsed.Repo || authorityEpoch != parsed.Epoch || storedClaimEpoch != parsed.ClaimEpoch {
		return ErrFenced
	}
	var position uint64
	if err = tx.QueryRowContext(ctx, `SELECT count(*)+1 FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, parsed.Domain, journalID).Scan(&position); err != nil {
		return err
	}
	return appendClaimJournalEntryTx(ctx, tx, journalID, position, parsed, canonical)
}

func resolveConnectedLifecycleMatter(ctx context.Context, tx *sql.Tx, command *lifecycleCommand) error {
	if command == nil {
		return ErrInvalidProof
	}
	name := command.name
	if !connectedLifecycleOperation(operation.ID{Name: name, Version: 1}) {
		return ErrInvalidProof
	}
	target := command.nodeID
	if command.stepID != "" {
		target = command.stepID
	}
	if target == "" {
		target = command.matter
	}
	var kind, matter, repo string
	var tombstone sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT kind,matter_id,repo_id,tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, command.domain, target).
		Scan(&kind, &matter, &repo, &tombstone); err != nil || tombstone.Valid || repo != command.repo ||
		(strings.HasPrefix(name, "matter.") && kind != "matter") || (strings.HasPrefix(name, "stage.") && kind != "stage") ||
		(strings.HasPrefix(name, "step.") && kind != "step") || (command.matter != "" && command.matter != matter) {
		return ErrFenced
	}
	command.matter = matter
	command.nodeID = target
	return nil
}

func connectedLifecycleDefinition(id operation.ID) (operation.Definition, bool) {
	switch id {
	case operation.StepStartV1.Metadata().Operation:
		return operation.StepStartV1, true
	case operation.StepFinishV1.Metadata().Operation:
		return operation.StepFinishV1, true
	case operation.MatterFinishV1.Metadata().Operation:
		return operation.MatterFinishV1, true
	case operation.MatterStartV1.Metadata().Operation:
		return operation.MatterStartV1, true
	case operation.StageStartV1.Metadata().Operation:
		return operation.StageStartV1, true
	case operation.MatterPauseV1.Metadata().Operation:
		return operation.MatterPauseV1, true
	case operation.StagePauseV1.Metadata().Operation:
		return operation.StagePauseV1, true
	case operation.StepPauseV1.Metadata().Operation:
		return operation.StepPauseV1, true
	case operation.MatterResumeV1.Metadata().Operation:
		return operation.MatterResumeV1, true
	case operation.StageResumeV1.Metadata().Operation:
		return operation.StageResumeV1, true
	case operation.StepResumeV1.Metadata().Operation:
		return operation.StepResumeV1, true
	case operation.MatterCancelV1.Metadata().Operation:
		return operation.MatterCancelV1, true
	case operation.StageCancelV1.Metadata().Operation:
		return operation.StageCancelV1, true
	case operation.StepCancelV1.Metadata().Operation:
		return operation.StepCancelV1, true
	case operation.StageFinishV1.Metadata().Operation:
		return operation.StageFinishV1, true
	default:
		return operation.Definition{}, false
	}
}

// CompleteConnectedLifecycle atomically applies a validated connected Step or Matter finish.
func (s *Store) CompleteConnectedLifecycle(ctx context.Context, owner *Execution, eventIDs []string, occurred time.Time, sign Signer) (CommandStatus, error) {
	var empty CommandStatus
	if owner == nil || owner.store != s || owner.lifecycle == nil || !connectedLifecycleOperation(operation.ID{
		Name: owner.lifecycle.name, Version: uint16(owner.lifecycle.version),
	}) || sign == nil || occurred.IsZero() || len(eventIDs) == 0 {
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
	if err = resolveConnectedLifecycleMatter(ctx, tx, c); err != nil {
		return empty, err
	}
	claim, claimErr := loadClaim(ctx, tx, c, c.claimID)
	if errors.Is(claimErr, sql.ErrNoRows) {
		claimErr = ErrFenced
	}
	if claimErr != nil && !errors.Is(claimErr, ErrFenced) {
		return empty, claimErr
	}
	return s.completeM6LifecycleTx(ctx, tx, c, head, claim, claimErr, eventIDs, occurred, sign)
}

func (s *Store) completeM6LifecycleTx(ctx context.Context, tx *sql.Tx, c *lifecycleCommand, head uint64,
	claim claimState, claimErr error, eventIDs []string, occurred time.Time, sign Signer,
) (CommandStatus, error) {
	var empty CommandStatus
	problem := ""
	if errors.Is(claimErr, ErrFenced) || claim.closed.Valid || claim.authority != c.epoch || claim.epoch != c.claimEpoch ||
		claim.owner != c.environment || claim.repo != c.repo || claim.worktree != c.worktree || claim.matter != c.matter {
		problem = "refusal.claim-fenced"
	}
	target := c.nodeID
	if c.stepID != "" {
		target = c.stepID
	}
	if target == "" {
		target = c.matter
	}
	scale, _, ok := strings.Cut(c.name, ".")
	if !ok {
		return empty, ErrInvalidProof
	}
	verb := strings.TrimPrefix(c.name, scale+".")
	from := map[string]string{"start": "planned", "finish": "in-progress", "pause": "in-progress", "cancel": "in-progress", "resume": "paused"}[verb]
	to := map[string]string{"start": "in-progress", "finish": "done", "pause": "paused", "cancel": "canceled", "resume": "in-progress"}[verb]
	state := ""
	if problem == "" {
		var err error
		state, err = lifecycleStateTx(ctx, tx, c.domain, target, scale)
		if err != nil {
			return empty, err
		}
		if state != from {
			problem = "refusal.invalid-transition"
		}
	}
	if problem != "" {
		result := operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemCode(problem), Message: problem}}
		return s.finishCommandTx(ctx, tx, c.commandIdentity, head, string(result.Code), nil, problem, nil, nil, nil, occurred, sign, nil)
	}
	identity := eventIdentity{c.domain, c.id, c.hash, c.environment, c.sequence, c.actedAt, c.repo}
	var first, last any
	var lastPosition uint64
	used := 0
	appendEvent := func(kind, subject string, payload map[string]any) error {
		if used >= len(eventIDs) {
			return ErrInvalidProof
		}
		id := eventIDs[used]
		position, err := appendCommandEvent(ctx, tx, identity, occurred, id, kind, subject, payload)
		if err != nil {
			return err
		}
		if kind != "batch.swept" {
			result, updateErr := tx.ExecContext(ctx, `UPDATE m6_nodes SET last_event_id=? WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, id, c.domain, subject)
			if updateErr != nil {
				return updateErr
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return ErrFenced
			}
		}
		if used == 0 {
			first = position
		}
		last = position
		lastPosition = position
		used++
		return nil
	}
	var output operation.Output
	if verb == "start" {
		type ancestor struct{ id, kind string }
		var planned []ancestor
		current := target
		for {
			var kind string
			var parent sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT kind,parent_id FROM m6_nodes WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, c.domain, current).Scan(&kind, &parent); err != nil {
				return empty, ErrFenced
			}
			if current != target {
				ancestorState, err := lifecycleStateTx(ctx, tx, c.domain, current, kind)
				if err != nil {
					return empty, err
				}
				if ancestorState == "planned" {
					planned = append(planned, ancestor{current, kind})
				}
			}
			if !parent.Valid {
				break
			}
			current = parent.String
		}
		for left, right := 0, len(planned)-1; left < right; left, right = left+1, right-1 {
			planned[left], planned[right] = planned[right], planned[left]
		}
		planned = append(planned, ancestor{target, scale})
		var cause string
		for _, node := range planned {
			payload := map[string]any{"from": "planned", "to": "in-progress"}
			if node.id != target {
				payload["cascade"] = true
			}
			if cause != "" {
				payload["cause_event_id"] = cause
			}
			if err := appendEvent(node.kind+".started", node.id, payload); err != nil {
				return empty, err
			}
			cause = eventIDs[used-1]
		}
		switch scale {
		case "step":
			output = operation.StepLifecycleOutput{StepID: target, MatterID: c.matter, State: to}
		case "stage":
			output = operation.NodeLifecycleOutput{NodeID: target, MatterID: c.matter, State: to}
		case "matter":
			output = operation.MatterLifecycleOutput{MatterID: c.matter, State: to}
		}
	} else if c.name == "matter.finish" {
		if err := appendEvent("matter.finished", c.matter, map[string]any{"from": "in-progress", "to": "done"}); err != nil {
			return empty, err
		}
		if err := step13ProjectionTx(ctx, tx); err != nil {
			return empty, err
		}
		nodes, nodesErr := step13NodesTx(ctx, tx)
		if nodesErr != nil {
			return empty, nodesErr
		}
		sealed, sealErr := step13ProjectedSealed(ctx, tx, nodes, c.domain, c.matter, lastPosition)
		if sealErr != nil {
			return empty, sealErr
		}
		output = operation.MatterFinishOutput{MatterID: c.matter, State: "done", BecameSealed: sealed}
	} else {
		kind := scale + "." + map[string]string{"finish": "finished", "pause": "paused", "resume": "resumed", "cancel": "canceled"}[verb]
		payload := map[string]any{"from": state, "to": to}
		if verb == "cancel" && c.reason != "" {
			payload["reason"] = c.reason
		}
		if err := appendEvent(kind, target, payload); err != nil {
			return empty, err
		}
		switch scale {
		case "step":
			output = operation.StepLifecycleOutput{StepID: target, MatterID: c.matter, State: to}
		case "stage":
			output = operation.NodeLifecycleOutput{NodeID: target, MatterID: c.matter, State: to}
		case "matter":
			output = operation.MatterLifecycleOutput{MatterID: c.matter, State: to}
		}
	}
	if c.name != "matter.finish" {
		if err := step13ProjectionTx(ctx, tx); err != nil {
			return empty, err
		}
	}
	definition, ok := connectedLifecycleDefinition(operation.ID{Name: c.name, Version: 1})
	result := operation.Result{Code: operation.ResultSucceeded, Output: output}
	if !ok || definition.ValidateResult(result) != nil || used == 0 {
		return empty, ErrInvalidProof
	}
	encoded, err := lifecycleOutputBytes(output)
	if err != nil {
		return empty, err
	}
	rangeValue := map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[used-1], "event_count": uint64(used)}
	return s.finishCommandTx(ctx, tx, c.commandIdentity, head, string(result.Code), encoded, nil, rangeValue, first, last, occurred, sign, nil)
}

func lifecycleOutputBytes(output operation.Output) ([]byte, error) {
	switch value := output.(type) {
	case operation.StepLifecycleOutput:
		return artifactEncoder.Marshal(map[string]any{"step_id": value.StepID, "matter_id": value.MatterID, "state": value.State})
	case operation.NodeLifecycleOutput:
		return artifactEncoder.Marshal(map[string]any{"node_id": value.NodeID, "matter_id": value.MatterID, "state": value.State})
	case operation.MatterLifecycleOutput:
		return artifactEncoder.Marshal(map[string]any{"matter_id": value.MatterID, "state": value.State})
	case operation.MatterFinishOutput:
		return artifactEncoder.Marshal(map[string]any{"matter_id": value.MatterID, "state": value.State, "became_sealed": value.BecameSealed})
	default:
		return nil, ErrInvalidProof
	}
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
		if scale == "matter" && lifecycleEventKind(scale, event.Kind) ||
			scale == "stage" && lifecycleEventKind(scale, event.Kind) ||
			scale == "step" && lifecycleEventKind(scale, event.Kind) {
			to, ok := event.Payload["to"].(string)
			if !ok {
				return "", ErrInvalidStore
			}
			state = to
		}
	}
	return state, rows.Err()
}

func lifecycleEventKind(scale, kind string) bool {
	for _, verb := range []string{"started", "finished", "paused", "resumed", "canceled"} {
		if kind == scale+"."+verb {
			return true
		}
	}
	return false
}
