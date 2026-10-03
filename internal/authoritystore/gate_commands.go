package authoritystore

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func gateDefinition(id operation.ID) (operation.Definition, bool) {
	for _, definition := range operation.GateCatalogue() {
		if definition.Metadata().Operation == id {
			return definition, true
		}
	}
	return operation.Definition{}, false
}

func (s *Store) submitGateCommand(ctx context.Context, command operation.Command, canonical []byte, hash string,
	peer tls.ConnectionState, at, deadline time.Time, checkContext bool, definition operation.Definition,
) (CommandStatus, error) {
	var empty CommandStatus
	if definition.ValidateRequest(command.Request) != nil || command.Request.Context.Repo == "" ||
		command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" || command.Request.Claim == nil {
		return empty, ErrInvalidProof
	}
	if command.Request.Actor != "human" && !strings.HasPrefix(string(command.Request.Actor), "role:") {
		return empty, ErrInvalidProof
	}
	before := func(tx *sql.Tx) error {
		return validateGateClaimTx(ctx, tx, command)
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
		id: command.ID, name: definition.Metadata().Operation.Name, version: uint64(definition.Metadata().Operation.Version),
		repo: command.Request.Context.Repo, encoded: canonical, hash: hash, m1: &command,
	}, peer, at, before, beforeCommit, func(tx *sql.Tx) error {
		return appendConnectedClaimJournalEntry(ctx, tx, command, canonical, hash)
	})
}

// validateGateClaimTx mirrors the exact Step 4 identity fences but permits a
// live Matter in Done state. Gate close/dismiss can intentionally follow
// Matter finish while its current claim journal remains open.
func validateGateClaimTx(ctx context.Context, tx *sql.Tx, command operation.Command) error {
	claim := command.Request.Claim
	if claim == nil || !ulid.MatchString(claim.ID) {
		return ErrFenced
	}
	epoch, err := strconv.ParseUint(claim.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != claim.Epoch {
		return ErrFenced
	}
	var matter, owner, repo, worktree, journalState string
	var storedEpoch, authorityEpoch, activeEpoch, generation uint64
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.matter_id,c.owner_environment_id,c.worktree_id,c.claim_epoch,c.authority_epoch,c.close_command_id,
		m.repo_id,j.state,j.generation,d.active_epoch
		FROM claims c JOIN matters m ON m.domain_id=c.domain_id AND m.matter_id=c.matter_id
		JOIN claim_journals j ON j.domain_id=c.domain_id AND j.claim_id=c.claim_id
		JOIN domains d ON d.domain_id=c.domain_id
		WHERE c.domain_id=? AND c.claim_id=? AND j.generation=(SELECT max(current.generation) FROM claim_journals current WHERE current.domain_id=c.domain_id AND current.claim_id=c.claim_id)`,
		command.AuthorityDomainID, claim.ID).Scan(&matter, &owner, &worktree, &storedEpoch, &authorityEpoch, &closed, &repo, &journalState, &generation, &activeEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	if err != nil {
		return err
	}
	if closed.Valid || journalState != "open" || generation == 0 || storedEpoch != epoch ||
		authorityEpoch != command.ExpectedAuthorityEpoch || activeEpoch != command.ExpectedAuthorityEpoch ||
		owner != command.EnvironmentID || repo != command.Request.Context.Repo || worktree != command.Request.Context.Worktree {
		return ErrFenced
	}
	var liveMatter string
	if err = tx.QueryRowContext(ctx, `SELECT node_id FROM m6_nodes WHERE domain_id=? AND node_id=? AND kind='matter' AND repo_id=? AND tombstone_event_id IS NULL`,
		command.AuthorityDomainID, matter, repo).Scan(&liveMatter); err != nil || liveMatter != matter {
		return ErrFenced
	}
	state, err := lifecycleStateTx(ctx, tx, command.AuthorityDomainID, matter, "matter")
	if err != nil {
		return err
	}
	if state != "planned" && state != "in-progress" && state != "done" {
		return ErrFenced
	}
	return nil
}

func appendNonemptyEventIDs(first string, rest []string) []string {
	if first == "" {
		return append([]string(nil), rest...)
	}
	return append([]string{first}, rest...)
}

func completeGateTx(ctx context.Context, tx *sql.Tx, command operation.Command, identity eventIdentity,
	result operation.Result, eventIDs []string, occurred time.Time,
) (step4Fold, error) {
	fold := step4Fold{result: result}
	refuse := func(code, message string) (step4Fold, error) {
		fold.result = operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemCode(code), Message: message}}
		fold.subject, fold.eventIDs = "", nil
		return fold, nil
	}
	if err := validateGateClaimTx(ctx, tx, command); err != nil {
		if errors.Is(err, ErrFenced) {
			return refuse("refusal.claim-fenced", "the Matter claim or current journal generation is no longer active")
		}
		return fold, err
	}
	var kind string
	var node string
	var gate, scale, eventKind, reason string
	var err error
	var payload map[string]any
	var output operation.Output
	switch input := command.Request.Input.(type) {
	case operation.GateDeclareInput:
		gate, scale = input.Gate, input.Scale
		if command.Request.Actor != "human" {
			return refuse("refusal.gate-owner", "gate declarations are Repo configuration and require the human actor")
		}
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT scale FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=?`, identity.domain, identity.repo, gate).Scan(&existing)
		if err == nil {
			if existing == scale {
				// A runtime caller may allocate one candidate ID before the
				// transaction determines this declaration is already present.
				// The no-op fold discards it; it never enters history or receipts.
				if len(eventIDs) > 1 {
					return fold, ErrInvalidProof
				}
				output = operation.GateDeclareOutput{Gate: gate, Scale: scale}
				return finishGateNoop(fold, output)
			}
			return refuse("refusal.gate-scale-change", fmt.Sprintf("%s already binds to %s scale; declare a new gate name instead of changing it to %s", gate, existing, scale))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fold, err
		}
		declarations, err := gateDeclarationsTx(ctx, tx, identity.domain, identity.repo)
		if err != nil {
			return fold, err
		}
		if other, violated := gateOrderConflict(declarations, gate, scale); violated {
			return refuse("refusal.gate-order-violation", fmt.Sprintf("%s at %s scale would violate gate-order monotonicity against %s at %s scale", gate, scale, other.gate, other.scale))
		}
		exempt, err := sealedNodesAtScaleTx(ctx, tx, identity.domain, identity.repo, scale)
		if err != nil {
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		node, eventKind = identity.repo, "gate.declared"
		payload = map[string]any{"gate": gate, "scale": scale}
		if len(exempt) > 0 {
			payload["exempt"] = exempt
		}
		output = operation.GateDeclareOutput{Gate: gate, Scale: scale}
	case operation.GateCloseInput:
		gate, node = input.Gate, input.NodeID
		scale, kind, err = gateSubjectTx(ctx, tx, identity.domain, identity.repo, gate, node, command.Request.Claim.ID)
		if errors.Is(err, errGateRefused) {
			return refuse("refusal.gate-subject", "the gate subject or Repo declaration does not match")
		}
		if err != nil {
			return fold, err
		}
		if err = authorizeGateActor(string(command.Request.Actor), gate, false); err != nil {
			return refuse("refusal.gate-owner", err.Error())
		}
		if satisfied, err := gateSatisfiedTx(ctx, tx, identity.domain, node, gate); err != nil {
			return fold, err
		} else if satisfied {
			return refuse("refusal.gate-already-satisfied", fmt.Sprintf("%s is already satisfied on %s", gate, node))
		}
		level, err := gatePushLevelTx(ctx, tx, identity.domain, identity.repo)
		if err != nil {
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		eventKind = "gate.closed"
		payload = map[string]any{"gate": gate, "scale": scale, "tracker_push_level": level}
		output = operation.GateCloseOutput{Gate: gate, NodeID: node, Scale: scale}
	case operation.GateDismissInput:
		gate, node, reason = input.Gate, input.NodeID, input.Reason
		scale, kind, err = gateSubjectTx(ctx, tx, identity.domain, identity.repo, gate, node, command.Request.Claim.ID)
		if errors.Is(err, errGateRefused) {
			return refuse("refusal.gate-subject", "the gate subject or Repo declaration does not match")
		}
		if err != nil {
			return fold, err
		}
		if err = authorizeGateActor(string(command.Request.Actor), gate, true); err != nil {
			return refuse("refusal.gate-owner", err.Error())
		}
		if lifecycle, err := lifecycleStateTx(ctx, tx, identity.domain, node, kind); err != nil {
			return fold, err
		} else if lifecycle != "done" {
			return refuse("refusal.gate-dismissal-lifecycle", fmt.Sprintf("%s is %s; only Done nodes can receive a gate dismissal", node, lifecycle))
		}
		if satisfied, err := gateSatisfiedTx(ctx, tx, identity.domain, node, gate); err != nil {
			return fold, err
		} else if satisfied {
			return refuse("refusal.gate-already-satisfied", fmt.Sprintf("%s is already satisfied on %s", gate, node))
		}
		level, err := gatePushLevelTx(ctx, tx, identity.domain, identity.repo)
		if err != nil {
			return fold, err
		}
		if len(eventIDs) != 1 {
			return fold, ErrInvalidProof
		}
		eventKind = "gate.dismissed"
		payload = map[string]any{"gate": gate, "scale": scale, "reason": reason, "tracker_push_level": level}
		output = operation.GateDismissOutput{Gate: gate, NodeID: node, Scale: scale}
	default:
		return fold, ErrInvalidProof
	}
	if eventKind == "" || !ulid.MatchString(eventIDs[0]) {
		return fold, ErrInvalidProof
	}
	position, err := appendCommandEvent(ctx, tx, identity, occurred, eventIDs[0], eventKind, node, payload)
	if err != nil {
		return fold, err
	}
	if err = step13ProjectionTx(ctx, tx); err != nil {
		return fold, err
	}
	definition, ok := gateDefinition(command.Request.Operation)
	fold.result = operation.Result{Code: operation.ResultSucceeded, Output: output}
	if !ok || definition.ValidateResult(fold.result) != nil {
		return fold, ErrInvalidProof
	}
	encoded, err := gateOutputBytes(output)
	if err != nil {
		return fold, err
	}
	fold.subject, fold.eventIDs = node, eventIDs
	fold.first, fold.last = position, position
	fold.rangeValue = map[string]any{"first_event_id": eventIDs[0], "last_event_id": eventIDs[0], "event_count": uint64(1)}
	fold.output = encoded
	return fold, nil
}

func finishGateNoop(fold step4Fold, output operation.Output) (step4Fold, error) {
	fold.result = operation.Result{Code: operation.ResultSucceeded, Output: output}
	encoded, err := gateOutputBytes(output)
	if err != nil {
		return fold, err
	}
	fold.first, fold.last, fold.rangeValue, fold.output = nil, nil, nil, encoded
	fold.subject, fold.eventIDs = "", nil
	return fold, nil
}

func gateOutputBytes(output operation.Output) ([]byte, error) {
	switch value := output.(type) {
	case operation.GateDeclareOutput:
		return artifactEncoder.Marshal(map[string]any{"gate": value.Gate, "scale": value.Scale})
	case operation.GateCloseOutput:
		return artifactEncoder.Marshal(map[string]any{"gate": value.Gate, "node_id": value.NodeID, "scale": value.Scale})
	case operation.GateDismissOutput:
		return artifactEncoder.Marshal(map[string]any{"gate": value.Gate, "node_id": value.NodeID, "scale": value.Scale})
	default:
		return nil, ErrInvalidProof
	}
}

var errGateRefused = errors.New("gate guard refused")

func gateSubjectTx(ctx context.Context, tx *sql.Tx, domain, repo, gate, node, claimID string) (string, string, error) {
	var declaredScale, kind, nodeRepo, matter string
	if err := tx.QueryRowContext(ctx, `SELECT scale FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=?`, domain, repo, gate).Scan(&declaredScale); errors.Is(err, sql.ErrNoRows) {
		return "", "", errGateRefused
	} else if err != nil {
		return "", "", err
	}
	var tombstone sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT kind,repo_id,matter_id,tombstone_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, domain, node).Scan(&kind, &nodeRepo, &matter, &tombstone); errors.Is(err, sql.ErrNoRows) || tombstone.Valid || nodeRepo != repo {
		return "", "", errGateRefused
	} else if err != nil {
		return "", "", err
	}
	if kind != declaredScale {
		return "", "", errGateRefused
	}
	var claimedMatter string
	if err := tx.QueryRowContext(ctx, `SELECT matter_id FROM claims WHERE domain_id=? AND claim_id=?`, domain, claimID).Scan(&claimedMatter); err != nil || claimedMatter != matter {
		return "", "", errGateRefused
	}
	return declaredScale, kind, nil
}

func gateSatisfiedTx(ctx context.Context, tx *sql.Tx, domain, node, gate string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?)`, domain, node, gate).Scan(&exists)
	return exists, err
}

func gatePushLevelTx(ctx context.Context, tx *sql.Tx, domain, repo string) (string, error) {
	var level string
	err := tx.QueryRowContext(ctx, `SELECT value FROM m6_repo_config WHERE domain_id=? AND repo_id=? AND config_key='tracker.push-level'`, domain, repo).Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		var backend string
		err = tx.QueryRowContext(ctx, `SELECT value FROM m6_repo_config WHERE domain_id=? AND repo_id=? AND config_key='tracker.backend'`, domain, repo).Scan(&backend)
		if errors.Is(err, sql.ErrNoRows) || backend == "" && err == nil {
			return "off", nil
		}
		if err != nil {
			return "", err
		}
		return "boundary", nil
	}
	if err != nil {
		return "", err
	}
	if level != "off" && level != "boundary" && level != "narrated" {
		return "", ErrInvalidStore
	}
	return level, nil
}

type gateDeclarationRow struct{ gate, scale string }

func gateDeclarationsTx(ctx context.Context, tx *sql.Tx, domain, repo string) ([]gateDeclarationRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT gate,scale FROM m6_gate_declarations WHERE domain_id=? AND repo_id=?`, domain, repo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var declarations []gateDeclarationRow
	for rows.Next() {
		var row gateDeclarationRow
		if err = rows.Scan(&row.gate, &row.scale); err != nil {
			return nil, err
		}
		declarations = append(declarations, row)
	}
	return declarations, rows.Err()
}

func gateOrderConflict(declarations []gateDeclarationRow, gate, scale string) (gateDeclarationRow, bool) {
	order := map[string]int{"verified": 0, "reviewed-local": 1, "reviewed": 2, "ci-green": 3}
	rank := map[string]int{"step": 0, "stage": 1, "matter": 2}
	newOrder, known := order[gate]
	if !known {
		return gateDeclarationRow{}, false
	}
	for _, prior := range declarations {
		priorOrder, priorKnown := order[prior.gate]
		if !priorKnown {
			continue
		}
		if priorOrder < newOrder && rank[prior.scale] > rank[scale] || priorOrder > newOrder && rank[scale] > rank[prior.scale] {
			return prior, true
		}
	}
	return gateDeclarationRow{}, false
}

func authorizeGateActor(actor, gate string, dismissal bool) error {
	owner := map[string]string{"verified": "verifier", "reviewed": "warden", "ci-green": "warden"}[gate]
	if actor == "human" {
		if !dismissal && owner != "" {
			return fmt.Errorf("%s is closed by its owning role %s", gate, owner)
		}
		return nil
	}
	if owner == "" || actor != "role:"+owner {
		return fmt.Errorf("%s can only be closed or dismissed by its owning role; arbitrary roles cannot act on it", gate)
	}
	// The accepted authority projection currently has no role-spawn projection;
	// fail closed rather than treating actor text as proof of an open spawn.
	return fmt.Errorf("%s requires an active %s role spawn, which this authority projection does not currently represent", gate, owner)
}

func sealedNodesAtScaleTx(ctx context.Context, tx *sql.Tx, domain, repo, scale string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,kind,matter_id FROM m6_nodes WHERE domain_id=? AND repo_id=? AND kind=? AND tombstone_event_id IS NULL ORDER BY node_id`, domain, repo, scale)
	if err != nil {
		return nil, err
	}
	type item struct{ id, kind, matter string }
	var nodes []item
	for rows.Next() {
		var node item
		if err = rows.Scan(&node.id, &node.kind, &node.matter); err != nil {
			_ = rows.Close()
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	declarations, err := gateDeclarationsTx(ctx, tx, domain, repo)
	if err != nil {
		return nil, err
	}
	var sealed []string
	for _, node := range nodes {
		state, err := lifecycleStateTx(ctx, tx, domain, node.id, node.kind)
		if err != nil || state != "done" {
			if err != nil {
				return nil, err
			}
			continue
		}
		ancestors := []item{node}
		current := node.id
		for {
			var parent string
			err = tx.QueryRowContext(ctx, `SELECT coalesce(parent_id,'') FROM m6_nodes WHERE domain_id=? AND node_id=?`, domain, current).Scan(&parent)
			if err != nil || parent == "" {
				if err != nil {
					return nil, err
				}
				break
			}
			var parentNode item
			if err = tx.QueryRowContext(ctx, `SELECT node_id,kind,matter_id FROM m6_nodes WHERE domain_id=? AND node_id=? AND tombstone_event_id IS NULL`, domain, parent).Scan(&parentNode.id, &parentNode.kind, &parentNode.matter); err != nil {
				return nil, ErrInvalidStore
			}
			ancestors = append(ancestors, parentNode)
			current = parent
		}
		complete := true
		for _, ancestor := range ancestors {
			for _, declaration := range declarations {
				if declaration.scale != ancestor.kind {
					continue
				}
				var satisfied bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?)`, domain, ancestor.id, declaration.gate).Scan(&satisfied); err != nil {
					return nil, err
				}
				if !satisfied {
					complete = false
					break
				}
			}
			if !complete {
				break
			}
		}
		if complete {
			sealed = append(sealed, node.id)
		}
	}
	return sealed, nil
}
