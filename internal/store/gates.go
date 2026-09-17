package store

import (
	"context"
	"fmt"
	"time"
)

// GateRequirementState describes how one effective requirement is satisfied.
type GateRequirementState string

const (
	// GateRequirementOpen is an unsatisfied effective gate requirement.
	GateRequirementOpen GateRequirementState = "open"
	// GateRequirementClosed is a gate requirement satisfied by a close event.
	GateRequirementClosed GateRequirementState = "closed"
	// GateRequirementExempt is a gate requirement satisfied by a prospective exemption.
	GateRequirementExempt GateRequirementState = "exempt"
)

// GateRelationship identifies whether a requirement belongs to the target or
// to one of its enclosing nodes.
type GateRelationship string

const (
	// GateOwn identifies a requirement declared at the target node's scale.
	GateOwn GateRelationship = "own"
	// GateEnclosing identifies a requirement declared at an ancestor's scale.
	GateEnclosing GateRelationship = "enclosing"
)

// GateRequirement is one repository declaration applied to a live node.
type GateRequirement struct {
	Gate         string
	Scale        Scale
	Owner        Actor
	Subject      Node
	Relationship GateRelationship
	State        GateRequirementState
	ClosedBy     Actor
	ClosedAt     *time.Time
}

// CompletionOverlay describes lifecycle and gate decisions that have not yet
// projected. It changes completion predicates but not requirement metadata.
type CompletionOverlay struct {
	FinishingNode string
	ClosingNode   string
	ClosingGate   string
}

// NodeCompletion is the canonical completion result for one node.
type NodeCompletion struct {
	LocallyComplete bool
	Sealed          bool
	Requirements    []GateRequirement
	Pending         []GateRequirement
}

type closedGateMetadata struct {
	scale    Scale
	actor    Actor
	closedAt time.Time
}

// EffectiveGateRequirements returns the target's own requirements followed by
// each ancestor's requirements, with declarations sorted by GateDeclarations.
func (v View) EffectiveGateRequirements(ctx context.Context, target Node) ([]GateRequirement, error) {
	declarations, err := v.GateDeclarations(ctx, target.Repo)
	if err != nil {
		return nil, err
	}

	requirements := make([]GateRequirement, 0)
	for current := target; ; {
		closed, err := v.closedGateMetadata(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		for _, declaration := range declarations {
			if declaration.Scale != current.Kind {
				continue
			}
			requirement := GateRequirement{
				Gate:         declaration.Gate,
				Scale:        declaration.Scale,
				Owner:        ActorHuman,
				Subject:      current,
				Relationship: GateEnclosing,
				State:        GateRequirementOpen,
			}
			if current.ID == target.ID {
				requirement.Relationship = GateOwn
			}
			if owner, owned := GateOwner(declaration.Gate); owned {
				requirement.Owner = owner.Actor()
			}
			if metadata, ok := closed[declaration.Gate]; ok {
				requirement.State = GateRequirementClosed
				requirement.ClosedBy = metadata.actor
				closedAt := metadata.closedAt
				requirement.ClosedAt = &closedAt
			} else {
				exempt, err := v.gateExemptOnNode(ctx, target.Repo, current.ID, declaration.Gate)
				if err != nil {
					return nil, err
				}
				if exempt {
					requirement.State = GateRequirementExempt
				}
			}
			requirements = append(requirements, requirement)
		}
		if current.Parent == "" {
			break
		}
		current, err = v.Node(ctx, current.Parent)
		if err != nil {
			return nil, err
		}
	}
	return requirements, nil
}

// NodeCompletion evaluates completion against committed state.
func (v View) NodeCompletion(ctx context.Context, target Node) (NodeCompletion, error) {
	return v.NodeCompletionWithOverlay(ctx, target, CompletionOverlay{})
}

// NodeCompletionWithOverlay evaluates completion against committed state plus
// one prospective finish and one prospective gate close.
func (v View) NodeCompletionWithOverlay(ctx context.Context, target Node, overlay CompletionOverlay) (NodeCompletion, error) {
	requirements, err := v.EffectiveGateRequirements(ctx, target)
	if err != nil {
		return NodeCompletion{}, err
	}
	done := target.Lifecycle == Done || overlay.FinishingNode == target.ID
	ownComplete := done
	sealed := done
	pending := make([]GateRequirement, 0)
	for _, requirement := range requirements {
		satisfied := requirement.State != GateRequirementOpen
		if overlay.ClosingNode != "" && overlay.ClosingGate != "" &&
			requirement.Subject.ID == overlay.ClosingNode && requirement.Gate == overlay.ClosingGate {
			satisfied = true
		}
		if requirement.Relationship == GateOwn && !satisfied {
			ownComplete = false
		}
		if !satisfied {
			sealed = false
			if done {
				pending = append(pending, requirement)
			}
		}
	}
	if !ownComplete {
		sealed = false
	}
	return NodeCompletion{
		LocallyComplete: ownComplete,
		Sealed:          sealed,
		Requirements:    requirements,
		Pending:         pending,
	}, nil
}

func (v View) closedGateMetadata(ctx context.Context, node string) (map[string]closedGateMetadata, error) {
	rows, err := v.q.QueryContext(ctx, `SELECT s.gate, s.scale, e.actor, e.occurred_at
		FROM gate_state s JOIN events e ON e.id = s.last_event WHERE s.node = ?`, node)
	if err != nil {
		return nil, fmt.Errorf("store: read closed gate metadata of %s: %w", node, err)
	}
	defer func() { _ = rows.Close() }()
	closed := make(map[string]closedGateMetadata)
	for rows.Next() {
		var gate string
		var metadata closedGateMetadata
		var actor, at string
		if err := rows.Scan(&gate, &metadata.scale, &actor, &at); err != nil {
			return nil, fmt.Errorf("store: read closed gate metadata of %s: %w", node, err)
		}
		metadata.actor = Actor(actor)
		metadata.closedAt, err = time.Parse(timestampLayout, at)
		if err != nil {
			return nil, fmt.Errorf("store: gate %s on %s carries an unreadable timestamp %q: %w", gate, node, at, err)
		}
		metadata.closedAt = metadata.closedAt.UTC()
		closed[gate] = metadata
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read closed gate metadata of %s: %w", node, err)
	}
	return closed, nil
}

func (v View) gateExemptOnNode(ctx context.Context, repo, node, gate string) (bool, error) {
	if v.schemaVersion < 8 {
		return false, nil
	}
	var exempt bool
	if err := v.q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM gate_exemptions WHERE repo=? AND node=? AND gate=?)`,
		repo, node, gate).Scan(&exempt); err != nil {
		return false, fmt.Errorf("store: read gate exemption %s on %s: %w", gate, node, err)
	}
	return exempt, nil
}
