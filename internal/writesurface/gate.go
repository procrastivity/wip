package writesurface

// Stage gates-and-dependencies, step-01/step-02: gate declaration (config,
// no taxonomy event — the one documented seam) and gate close (`gate.closed`,
// the only gate event in P1).
//
// Gate-order monotonicity (D12) is a `doctor` check owned by `guards`,
// called at declare time per the workplan — but `guards` does not exist yet
// (guards ← tiers, render-scratch). DeclareGate therefore does not call it:
// there is nothing to call. This is a documented gap, not a silent omission
// — see docs/write-surface/decisions.md — and `guards`, when built, is the
// Matter that wires the precondition in here.

import (
	"context"
	"fmt"

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
	return s.DeclareGate(ctx, repo, gate, scale)
}

// CloseGate closes a declared gate against a node. The recorded scale is
// always the subject's own kind (D62) — never a second opinion from the
// caller — and closing a gate the subject's Repo never declared is refused.
func CloseGate(ctx context.Context, s *store.Store, actor store.Actor, repo, gate, locator string) (store.Node, error) {
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

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeGateClosed,
			Subject: n.ID,
			Payload: store.GateClosed{Gate: gate, Scale: n.Kind},
		}}, nil
	}); err != nil {
		return store.Node{}, err
	}
	return n, nil
}
