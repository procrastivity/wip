package authoritystore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/procrastivity/wip/internal/operation"
)

// ErrStep10OpenBracket reports a claim release blocked by unresolved scheduler work.
var ErrStep10OpenBracket = errors.New("authoritystore: Step 10 claim has an open scheduler bracket")

func step10ClaimHasUnresolvedWorkTx(ctx context.Context, tx *sql.Tx, domain, claimID string) (bool, error) {
	roles, err := step10RolesAtTx(ctx, tx, domain, "")
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if role.claim == claimID {
			return true, nil
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT command FROM submissions
		WHERE domain_id=? AND operation_name='scheduler.orchestrate' AND state='submitted'`, domain)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		command, decodeErr := operation.DecodeCanonicalCommand(raw)
		if decodeErr != nil {
			return false, ErrInvalidStore
		}
		input, ok := command.Request.Input.(operation.SchedulerHookInput)
		if ok && input.Kind != "ask" && command.Request.Claim != nil && command.Request.Claim.ID == claimID {
			return true, nil
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	incompletePlan, err := step10PlanBirthsIncompleteTx(ctx, tx, domain, claimID, "")
	if err != nil || incompletePlan {
		return incompletePlan, err
	}
	incompleteWork, err := step10WorkConsumptionIncompleteTx(ctx, tx, domain, claimID, "")
	return incompleteWork, err
}

func step10PlanBirthsIncompleteTx(ctx context.Context, tx *sql.Tx, domain, claimID, roleID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command_id,command,state FROM submissions
		WHERE domain_id=? AND operation_name='scheduler.orchestrate' AND operation_version=1 ORDER BY environment_sequence`, domain)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, submissionState string
		var raw []byte
		if err = rows.Scan(&id, &raw, &submissionState); err != nil {
			return false, err
		}
		plan, decodeErr := operation.DecodeCanonicalCommand(raw)
		if decodeErr != nil {
			return false, ErrInvalidStore
		}
		input, ok := plan.Request.Input.(operation.SchedulerHookInput)
		if !ok || input.Kind != "plan" || plan.Request.Claim == nil || plan.Request.Claim.ID != claimID || roleID != "" && input.RoleID != roleID {
			continue
		}
		if submissionState != "terminal" {
			return true, nil
		}
		var receiptBytes []byte
		if err = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, id).Scan(&receiptBytes); err != nil {
			return true, nil
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		if receiptErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil {
			continue
		}
		var output operation.SchedulerHookOutput
		if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil || output.Kind != "plan" ||
			operation.SchedulerOrchestrateV1.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
			return false, ErrInvalidStore
		}
		if output.Outcome != "planned" || len(output.Steps) == 0 {
			continue
		}
		complete, err := step10PlanBirthsCompleteTx(ctx, tx, domain, plan, input, output)
		if err != nil || !complete {
			return !complete, err
		}
	}
	return false, rows.Err()
}

func step10PlanBirthsCompleteTx(ctx context.Context, tx *sql.Tx, domain string, plan operation.Command,
	input operation.SchedulerHookInput, output operation.SchedulerHookOutput,
) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command_id,command,state FROM submissions
		WHERE domain_id=? AND environment_id=? AND operation_name='step.create' AND operation_version=2 ORDER BY environment_sequence`,
		domain, plan.EnvironmentID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var id, state string
		var raw []byte
		if err = rows.Scan(&id, &raw, &state); err != nil {
			return false, err
		}
		birth, decodeErr := operation.DecodeCanonicalCommand(raw)
		if decodeErr != nil {
			return false, ErrInvalidStore
		}
		if birth.CausationCommandID != plan.ID {
			continue
		}
		if count >= len(output.Steps) {
			return false, ErrInvalidStore
		}
		step, ok := birth.Request.Input.(operation.StepCreateInput)
		if !ok || birth.CorrelationCommandID != plan.ID || birth.Request.Actor != "role:scheduler" ||
			birth.EnvironmentID != plan.EnvironmentID || birth.Request.Context != plan.Request.Context ||
			birth.Request.Claim == nil || plan.Request.Claim == nil || birth.Request.Claim.ID != plan.Request.Claim.ID ||
			birth.Request.Claim.Epoch != plan.Request.Claim.Epoch || step.ParentID != input.MatterID ||
			step.Title != output.Steps[count].Title {
			return false, ErrInvalidStore
		}
		if state != "terminal" {
			return false, nil
		}
		var receiptBytes []byte
		if err = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, id).Scan(&receiptBytes); err != nil {
			return false, nil
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		var result map[string]any
		decodeErr = nil
		if receiptErr == nil {
			decodeErr = artifactDecoder.Unmarshal(receipt.Result.Output, &result)
		}
		parentID, parentOK := result["parent_id"].(string)
		title, titleOK := result["title"].(string)
		birthState, stateOK := result["state"].(string)
		if receiptErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil ||
			decodeErr != nil || !parentOK || parentID != input.MatterID || !titleOK || title != step.Title || !stateOK || birthState != "planned" {
			return false, ErrInvalidStore
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	return count == len(output.Steps), nil
}

func step10WorkConsumptionIncompleteTx(ctx context.Context, tx *sql.Tx, domain, claimID, roleID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command_id,command,state FROM submissions
		WHERE domain_id=? AND operation_name='scheduler.orchestrate' AND operation_version=1 ORDER BY environment_sequence`, domain)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, state string
		var raw []byte
		if err = rows.Scan(&id, &raw, &state); err != nil {
			return false, err
		}
		command, decodeErr := operation.DecodeCanonicalCommand(raw)
		if decodeErr != nil {
			return false, ErrInvalidStore
		}
		input, ok := command.Request.Input.(operation.SchedulerHookInput)
		if !ok || input.Kind != "work" || command.Request.Claim == nil || command.Request.Claim.ID != claimID || roleID != "" && input.RoleID != roleID {
			continue
		}
		if state != "terminal" {
			return true, nil
		}
		var receiptBytes []byte
		if err = tx.QueryRowContext(ctx, `SELECT receipt FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domain, id).Scan(&receiptBytes); err != nil {
			return true, nil
		}
		receipt, receiptErr := readReceipt(receiptBytes)
		if receiptErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil {
			continue
		}
		var output operation.SchedulerHookOutput
		if artifactDecoder.Unmarshal(receipt.Result.Output, &output) != nil || output.Kind != "work" ||
			operation.SchedulerOrchestrateV1.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: output}) != nil {
			return false, ErrInvalidStore
		}
		switch output.Outcome {
		case "verified":
			satisfied, err := gateSatisfiedTx(ctx, tx, domain, input.MatterID, "verified")
			if err != nil || !satisfied {
				return !satisfied, err
			}
		case "completed":
			var kind string
			if err = tx.QueryRowContext(ctx, `SELECT kind FROM m6_nodes WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`,
				domain, input.TargetID).Scan(&kind); err != nil {
				return true, nil
			}
			lifecycle, lifecycleErr := lifecycleStateTx(ctx, tx, domain, input.TargetID, kind)
			if lifecycleErr != nil || lifecycle != "done" {
				return true, lifecycleErr
			}
		}
	}
	return false, rows.Err()
}
