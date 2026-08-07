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
	if other, violates := guards.WouldViolate(declared, gate, scale); violates {
		return wiperr.New("refusal.gate-order-violation",
			guards.ViolationMessage(store.GateDeclaration{Gate: gate, Scale: scale}, other))
	}

	return s.DeclareGate(ctx, repo, gate, scale)
}

// CloseGate closes a declared gate against a node. The recorded scale is
// always the subject's own kind (D62) — never a second opinion from the
// caller — and closing a gate the subject's Repo never declared is refused.
func CloseGate(ctx context.Context, s *store.Store, actor store.Actor, repo, gate, locator string) (store.Node, error) {
	return closeGateEnv(ctx, s, actor, store.Env{Repo: repo}, gate, locator)
}

// CloseGateWithEnv is CloseGate with the caller's full Env, so a final Matter
// gate can seal and sweep with correct event dimensions.
func CloseGateWithEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, gate, locator string) (store.Node, error) {
	return closeGateEnv(ctx, s, actor, env, gate, locator)
}

func closeGateEnv(ctx context.Context, s *store.Store, actor store.Actor, env store.Env, gate, locator string) (store.Node, error) {
	repo := env.Repo
	n, err := ResolveNode(ctx, s.View, repo, locator)
	if err != nil {
		return store.Node{}, err
	}
	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		return store.Node{}, err
	}
	found := false
	for _, d := range declared {
		if d.Gate == gate {
			found = true
			break
		}
	}
	if !found {
		return store.Node{}, wiperr.New("validation.gate-not-declared", fmt.Sprintf("%q is not a gate this repo declares", gate))
	}

	req := store.Request{Actor: actor, Env: env}
	if _, err := s.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Node(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		drafts := []store.Draft{{
			Type:    store.TypeGateClosed,
			Subject: fresh.ID,
			Payload: store.GateClosed{Gate: gate, Scale: fresh.Kind},
		}}
		if fresh.Kind == store.ScaleMatter {
			if sweep, found, err := sealSweepDraft(ctx, tx, fresh.ID, false, gate); err != nil {
				return nil, err
			} else if found {
				sweep.Cause = 0
				drafts = append(drafts, sweep)
			}
		}
		return drafts, nil
	}); err != nil {
		return store.Node{}, err
	}
	return n, nil
}
