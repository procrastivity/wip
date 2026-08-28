// Package render implements PLAN 1.4: the render pipeline that projects
// store content onto `.wip/generated/`, the `.wip/` layout and its
// `.git/info/exclude` management (D41), dispatch-id minting and
// `.wip/work/<dispatch-id>/` namespacing, sweep-on-close plus `wip clean` for
// crash orphans, and the two decisions PLAN 1.4 leaves open (eager-vs-on-demand
// coverage, missing-file recovery) — see workplans/render-scratch.md's "Open
// calls resolved here" for the resolutions this package encodes.
//
// The governing test is MODEL §3.1's own: if deleting `.wip/` between
// dispatches loses anything, it is in the wrong place (D33 as amended).
// Nothing in this package is a source of truth; it reads and writes
// exclusively through internal/store's data-access layer, exactly as `tiers`
// and `write-surface` do.
package render

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Current is the Repo/Clone/Worktree a dispatch/render command runs against,
// plus the worktree's own filesystem root — where `.wip/` lives for this
// checkout. Resolved once so every verb in this package shares one
// resolution and one refusal, exactly as readsurface.Current does for its
// own package (render-scratch owns no read-surface dependency, so this is a
// deliberate, small duplication of that convention rather than an import of
// it — the same convention tiers/errors.go and readsurface.go each keep their
// own unknownClone() rather than share one).
//
// The unknown-*worktree* refusal is the exception: it needs the clone label
// and worktree name, so tiers.UnknownWorktree is shared rather than copied.
type Current struct {
	Repo     store.Repo
	Clone    store.Clone
	Worktree store.Worktree
	// Root is the absolute path this worktree checks out to — `git
	// rev-parse --show-toplevel` — which is where `.wip/generated/` and
	// `.wip/work/` live for this location. It is not a tier key (D37: paths
	// are mutable attributes, never keys) and is resolved fresh every call.
	Root string
}

// Env is the tier context Current names — what a Commit's Request.Env needs
// for the execution events this package emits (render.performed,
// dispatch.opened, dispatch.closed — D56).
func (c Current) Env() store.Env {
	return store.Env{Repo: c.Repo.ID, Clone: c.Clone.ID, Worktree: c.Worktree.ID}
}

// ResolveCurrent resolves the current Clone, Worktree and worktree root from
// dir. Every verb in this package needs a resolved current location — there
// is no host-wide carve-out here the way `status` has one (tiers Brief): a
// dispatch is against a worktree, and rendering with no known worktree is
// exactly the unknown-clone hard failure MODEL §11 requires (never a guess).
func ResolveCurrent(ctx context.Context, s *store.Store, actor store.Actor, dir string) (Current, error) {
	clone, found, err := tiers.ResolveCurrentClone(ctx, s, actor, dir)
	if err != nil {
		return Current{}, err
	}
	if !found {
		return Current{}, unknownClone()
	}
	repo, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		return Current{}, err
	}
	wtName, err := tiers.CurrentWorktreeName(ctx, dir, clone.GitCommonDir)
	if err != nil {
		return Current{}, err
	}
	wt, found, err := s.WorktreeByName(ctx, clone.ID, wtName)
	if err != nil {
		return Current{}, err
	}
	if !found {
		// Known Clone, un-init'd worktree: the same second gap
		// readsurface.ResolveCurrent names, refused with the same shared
		// tiers helper so both surfaces say the same thing.
		return Current{}, tiers.UnknownWorktree(wtName, clone.Label)
	}
	root, err := WorktreeRoot(ctx, dir)
	if err != nil {
		return Current{}, err
	}
	return Current{Repo: repo, Clone: clone, Worktree: wt, Root: root}, nil
}

// unknownClone is vocabulary's ratified unknown-clone refusal
// (workplans/vocabulary.md step-10), reused verbatim — the same message
// `tiers`'s and `readsurface`'s own errors.go raise, for the same reason.
func unknownClone() error {
	return wiperr.New("refusal.unknown-clone",
		"refused — this clone is unknown to wip; run `wip init` here first (a write; if you cannot run it, ask the user)")
}
