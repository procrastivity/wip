package writesurface

// Stage gates-and-dependencies, step-01/step-02: gate declaration (config,
// no taxonomy event — the one documented seam) and gate close (`gate.closed`,
// the only gate event in P1).
//
// Gate-order monotonicity (D12) is a `doctor` check owned by `guards`,
// called here at declare time as an add-time precondition — the mirror of
// how `depend add` calls `schema`'s cycle check (guards.md step-03: "two
// callers, one function… ownership reversed"). `guards`'s own audit calls
// the same `guards.Violations` over every declared binding.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// DeclareGate declares a gate binding at a scale, as project configuration
// (D4): statically knowable, never runtime-conditional. It writes config and
// emits no domain event — the taxonomy's only gate event is `gate.closed`.
func DeclareGate(ctx context.Context, s *store.Store, repo, gate string, scale store.Scale) error {
	switch scale {
	case store.ScaleMatter, store.ScaleStage, store.ScaleStep:
	default:
		return wiperr.New("validation.invalid-scale", fmt.Sprintf("%q is not matter, stage or step", scale))
	}
	if gate == "" {
		return wiperr.New("validation.missing-gate-name", "a gate declaration needs a name")
	}

	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		return err
	}
	for _, existing := range declared {
		if existing.Gate == gate && existing.Scale != scale {
			return wiperr.New("refusal.gate-scale-change", fmt.Sprintf(
				"%s already binds to %s scale; declare a new gate name instead of changing it to %s",
				gate, existing.Scale, scale))
		}
	}
	if other, violates := guards.WouldViolate(declared, gate, scale); violates {
		return wiperr.New("refusal.gate-order-violation",
			guards.ViolationMessage(store.GateDeclaration{Gate: gate, Scale: scale}, other))
	}

	return s.DeclareGate(ctx, repo, gate, scale)
}

// RepairGateExemption restores one prospective exemption that a declaration
// made before schema v8 could not snapshot. It accepts only a Done node at the
// gate's bound scale whose other own and enclosing gates are already satisfied.
// The repair is configuration and emits no event or false gate close.
func RepairGateExemption(ctx context.Context, s *store.Store, repo, gate, locator string) (store.Node, error) {
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	if n.Repo != repo {
		return store.Node{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no node %s in this repo", locator))
	}

	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		return store.Node{}, err
	}
	var bound store.Scale
	for _, d := range declared {
		if d.Gate == gate {
			bound = d.Scale
			break
		}
	}
	if bound == "" {
		return store.Node{}, wiperr.New("validation.gate-not-declared", fmt.Sprintf("%q is not a gate this repo declares", gate))
	}
	if n.Kind != bound {
		return store.Node{}, wiperr.New("refusal.gate-repair-scale", fmt.Sprintf(
			"%s binds to %s scale and cannot exempt %s %s", gate, bound, n.Kind, locator))
	}
	if n.Lifecycle != store.Done {
		return store.Node{}, wiperr.New("refusal.gate-repair-lifecycle", fmt.Sprintf(
			"%s is %s; only Done nodes can receive a repaired gate exemption", locator, n.Lifecycle))
	}

	exempt, err := s.GateExempt(ctx, repo, n.ID, gate)
	if err != nil {
		return store.Node{}, err
	}
	if exempt {
		return n, nil
	}
	satisfied, err := s.GateSatisfied(ctx, repo, n.ID, gate)
	if err != nil {
		return store.Node{}, err
	}
	if satisfied {
		return store.Node{}, wiperr.New("refusal.gate-already-satisfied",
			fmt.Sprintf("%s is already satisfied on %s", gate, locator))
	}

	cur := n
	for {
		for _, d := range declared {
			if d.Scale != cur.Kind || (cur.ID == n.ID && d.Gate == gate) {
				continue
			}
			ok, err := s.GateSatisfied(ctx, repo, cur.ID, d.Gate)
			if err != nil {
				return store.Node{}, err
			}
			if !ok {
				return store.Node{}, wiperr.New("refusal.gate-repair-prerequisite", fmt.Sprintf(
					"%s was not sealed before %s became operative: %s is open on %s",
					locator, gate, d.Gate, cur.Locator))
			}
		}
		if cur.Parent == "" {
			break
		}
		cur, err = s.Node(ctx, cur.Parent)
		if err != nil {
			return store.Node{}, err
		}
	}

	if err := s.RepairGateExemption(ctx, repo, gate, n.ID); err != nil {
		return store.Node{}, err
	}
	return n, nil
}

// CloseGate closes a declared gate against a node. The recorded scale is
// always the subject's own kind (D62) — never a second opinion from the
// caller — and closing a gate the subject's Repo never declared is refused.
func CloseGate(ctx context.Context, s *store.Store, actor store.Actor, repo, gate, locator string) (store.Node, error) {
	result, err := closeGateEnv(ctx, s, actor, store.Env{Repo: repo}, gate, locator)
	return result.Node, err
}

// CloseGateWithEnv is CloseGate with the caller's full Env, so a final Matter
// gate can seal and sweep with correct event dimensions.
func CloseGateWithEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, gate, locator string) (store.Node, error) {
	result, err := CloseGateWithEnvResult(ctx, s, actor, env, gate, locator)
	return result.Node, err
}

// CloseGateWithEnvResult is CloseGateWithEnv with an atomic indication that
// this gate close crossed a Matter's seal boundary.
func CloseGateWithEnvResult(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, gate, locator string) (SealTransition, error) {
	return closeGateEnv(ctx, s, actor, env, gate, locator)
}

func closeGateEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, gate, locator string) (SealTransition, error) {
	repo := env.Repo
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return SealTransition{}, err
	}
	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		return SealTransition{}, err
	}
	var bound store.Scale
	for _, d := range declared {
		if d.Gate == gate {
			bound = d.Scale
			break
		}
	}
	if bound == "" {
		return SealTransition{}, wiperr.New("validation.gate-not-declared", fmt.Sprintf("%q is not a gate this repo declares", gate))
	}
	if n.Kind != bound {
		return SealTransition{}, wiperr.New("refusal.gate-scale", fmt.Sprintf(
			"%s binds to %s scale and cannot close on %s %s", gate, bound, n.Kind, locator))
	}
	satisfied, err := s.GateSatisfied(ctx, repo, n.ID, gate)
	if err != nil {
		return SealTransition{}, err
	}
	if satisfied {
		return SealTransition{}, wiperr.New("refusal.gate-already-satisfied",
			fmt.Sprintf("%s is already satisfied on %s", gate, locator))
	}

	// Gate config drives role activation (D14), and ownership is the other
	// side of the same list: a role-owned gate closes only under its owning
	// role's actor — which the write path in turn verifies against an open
	// spawn — and a human-owned gate closes only under the human. This is
	// what turns MODEL §2.3's "closed by" column from discipline into
	// structure.
	if owner, owned := store.GateOwner(gate); owned {
		if actor != owner.Actor() {
			return SealTransition{}, wiperr.New("refusal.gate-owner",
				fmt.Sprintf("%s is closed by its owning role %s (D14); spawn it and run under --as-role %s", gate, owner, owner))
		}
	} else if actor != store.ActorHuman {
		return SealTransition{}, wiperr.New("refusal.gate-owner",
			fmt.Sprintf("%s is human-owned; a role or system actor cannot close it", gate))
	}

	req := store.Request{Actor: actor, Env: env}
	becameSealed := false
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		level, err := tx.EffectiveTrackerPushLevel(ctx, repo)
		if err != nil {
			return nil, err
		}
		fresh, err := tx.Node(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		freshDeclarations, err := tx.GateDeclarations(ctx, repo)
		if err != nil {
			return nil, err
		}
		var freshBound store.Scale
		for _, declaration := range freshDeclarations {
			if declaration.Gate == gate {
				freshBound = declaration.Scale
				break
			}
		}
		if freshBound == "" {
			return nil, wiperr.New("validation.gate-not-declared", fmt.Sprintf("%q is not a gate this repo declares", gate))
		}
		if fresh.Kind != freshBound {
			return nil, wiperr.New("refusal.gate-scale", fmt.Sprintf(
				"%s binds to %s scale and cannot close on %s %s", gate, freshBound, fresh.Kind, locator))
		}
		drafts := []store.Draft{{
			Type:    store.TypeGateClosed,
			Subject: fresh.ID,
			Payload: store.GateClosed{Gate: gate, Scale: fresh.Kind, TrackerPushLevel: level},
		}}
		if fresh.Kind == store.ScaleMatter {
			wasSealed, err := matterSealedProspectively(ctx, tx, fresh.ID, false, "")
			if err != nil {
				return nil, err
			}
			willBeSealed, err := matterSealedProspectively(ctx, tx, fresh.ID, false, gate)
			if err != nil {
				return nil, err
			}
			becameSealed = !wasSealed && willBeSealed
			if !becameSealed {
				return drafts, nil
			}
			if sweep, found, err := sealSweepDraft(ctx, tx, fresh.ID); err != nil {
				return nil, err
			} else if found {
				sweep.Cause = 0
				drafts = append(drafts, sweep)
			}
		}
		return drafts, nil
	}); err != nil {
		return SealTransition{}, err
	}
	fresh, err := s.Node(ctx, n.ID)
	if err != nil {
		return SealTransition{}, err
	}
	return SealTransition{Node: fresh, BecameSealed: becameSealed}, nil
}
