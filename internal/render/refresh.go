package render

import (
	"context"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Precondition is a check run before any render-path write reaches disk —
// step-09's hook. Refresh, Render, and Exit all run it, unconditionally,
// before EnsureLayout or any generated/scratch write. This Matter's only
// obligation is that the call site exists and is unconditionally reached; the
// check itself (is `.wip/` tracked by git in this repo?) is `guards`'s to
// supply (`guards ← tiers, render-scratch`, HANDOFF §5) — guards wires its
// own precondition in here once it lands, rather than opening a second hook.
type Precondition func(ctx context.Context, cur Current) error

// NoPrecondition is the default: always passes. The verb layer
// (internal/verbs/refresh) passes this until `guards` supplies its own.
func NoPrecondition(context.Context, Current) error { return nil }

// Result is what a render pass produced — `wip refresh`'s success payload.
type Result struct {
	DispatchID string
	ScratchDir string
	Opened     bool
	Superseded string
	Rendered   []string
}

// Refresh implements the eager path: dispatch-open-or-reuse (step-02), then
// step-05's eager coverage — every not-sealed Matter in this Repo — each
// rendered per step-03's depth policy, then exactly one render.performed
// event (step-03) attributed to the dispatch.
func Refresh(ctx context.Context, s *store.Store, cur Current, actor store.Actor, precondition Precondition) (Result, error) {
	if err := precondition(ctx, cur); err != nil {
		return Result{}, err
	}
	if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
		return Result{}, err
	}

	dispatch, opened, superseded, err := OpenOrReuse(ctx, s, cur, actor, time.Now(), DefaultStaleBound)
	if err != nil {
		return Result{}, err
	}

	scope, err := EagerScope(ctx, s, cur.Repo.ID)
	if err != nil {
		return Result{}, err
	}
	rendered := make([]string, 0, len(scope))
	for _, matter := range scope {
		if err := renderMatterTree(ctx, s, cur.Root, matter); err != nil {
			return Result{}, err
		}
		rendered = append(rendered, matter.Locator)
	}

	if err := recordRenderPerformed(ctx, s, cur, actor, dispatch.ID, ""); err != nil {
		return Result{}, err
	}

	return Result{
		DispatchID: dispatch.ID,
		ScratchDir: ScratchDir(cur.Root, dispatch.ID),
		Opened:     opened,
		Superseded: superseded,
		Rendered:   rendered,
	}, nil
}

// Render implements the on-demand path: `wip refresh <locator>` names one
// node explicitly — the only way a sealed Matter renders (step-05). Render
// granularity is always the owning Matter's whole subtree (step-03's depth
// policy is per-Matter), regardless of which node within it locator names.
func Render(ctx context.Context, s *store.Store, cur Current, actor store.Actor, locator string, precondition Precondition) (Result, error) {
	if err := precondition(ctx, cur); err != nil {
		return Result{}, err
	}
	if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
		return Result{}, err
	}

	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		return Result{}, err
	}
	matter, err := s.Node(ctx, node.Matter)
	if err != nil {
		return Result{}, err
	}

	dispatch, opened, superseded, err := OpenOrReuse(ctx, s, cur, actor, time.Now(), DefaultStaleBound)
	if err != nil {
		return Result{}, err
	}

	if err := renderMatterTree(ctx, s, cur.Root, matter); err != nil {
		return Result{}, err
	}

	if err := recordRenderPerformed(ctx, s, cur, actor, dispatch.ID, locator); err != nil {
		return Result{}, err
	}

	return Result{
		DispatchID: dispatch.ID,
		ScratchDir: ScratchDir(cur.Root, dispatch.ID),
		Opened:     opened,
		Superseded: superseded,
		Rendered:   []string{matter.Locator},
	}, nil
}

// Exit writes one Matter's generated tree after it sealed, without opening
// a dispatch and without emitting render.performed. Eager coverage will
// skip this Matter from here on, so this is the last snapshot a subsequent
// bare `wip refresh` would have taken. Seal is not a render pass of a
// dispatch (D59); minting one here would be a side effect of finish or
// gate close.
func Exit(ctx context.Context, s *store.Store, cur Current, node store.Node, precondition Precondition) error {
	if err := precondition(ctx, cur); err != nil {
		return err
	}
	if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
		return err
	}
	matter := node
	if node.Kind != store.ScaleMatter {
		var err error
		matter, err = s.Node(ctx, node.Matter)
		if err != nil {
			return err
		}
	}
	return renderMatterTree(ctx, s, cur.Root, matter)
}

// recordRenderPerformed emits render.performed (step-03): exactly one per
// render pass, subject to the dispatch it belongs to — render is part of the
// dispatch's own bracket (D59), not a fact about any one rendered node.
// Nothing is projected from it (project.go's own no-op for this type): the
// render target is a projection of the store and never a source (D36, D40),
// so this event exists to be read as history, never folded into state.
func recordRenderPerformed(ctx context.Context, s *store.Store, cur Current, actor store.Actor, dispatchID, target string) error {
	req := store.Request{Actor: actor, Env: cur.Env()}
	_, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeRenderPerformed,
			Subject: dispatchID,
			Payload: store.RenderPerformed{Target: target},
		}}, nil
	})
	return err
}
