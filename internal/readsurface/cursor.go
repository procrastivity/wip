package readsurface

// Step-01: the cursor read path. The cursor is a state row keyed at Clone +
// Worktree whose moves are logged like any write (PLAN 1.7) — so reading it
// is one keyed lookup, never a replay. Whether that row is a maintained
// projection or the primary table is schema's call under the winning
// store-fork shape (D46, D61); this package reads whatever store.View
// delivers and does not decide store shape itself.

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
)

// Current is the Clone + Worktree a command is running from, resolved once
// so every verb in this package that needs it (the cursor lookup, `next
// --set`'s write, and any node this Matter's verbs touch) shares one
// resolution and one refusal.
type Current struct {
	Repo     store.Repo
	Clone    store.Clone
	Worktree store.Worktree
}

// Env is the tier context Current names — what a Commit's Request.Env needs
// for an execution event like cursor.moved (D56).
func (c Current) Env() store.Env {
	return store.Env{Repo: c.Repo.ID, Clone: c.Clone.ID, Worktree: c.Worktree.ID}
}

// ResolveCurrent resolves the current Clone and Worktree from dir, the way
// every non-status verb in this package needs to: a hard failure, never a
// guess, when either is unknown to wip (MODEL §11) — the exception `status`
// carves out belongs to `tiers`'s Brief alone and is not inherited here.
//
// It reuses tiers.ResolveCurrentClone rather than re-deriving common-dir
// resolution, exactly as `tiers/tier-verbs` step-06's addressing resolver is
// meant to be imported rather than reimplemented — the same convention this
// Matter's own workplan draws for `chassis`'s --json/-v.
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
	// clone.GitCommonDir is already this location's common-dir — it is what
	// CloneByCommonDir just matched — so there is no need to shell out to git
	// a second time to learn it, the way tiers/status.go's own caller does.
	wtName, err := tiers.CurrentWorktreeName(ctx, dir, clone.GitCommonDir)
	if err != nil {
		return Current{}, err
	}
	wt, found, err := s.WorktreeByName(ctx, clone.ID, wtName)
	if err != nil {
		return Current{}, err
	}
	if !found {
		// A known Clone whose current location has never itself been
		// init'd (`tiers` Brief: a second `wip init`, run inside a linked
		// worktree of an already-known Clone, is what creates its Worktree
		// row). The remedy is `wip init` in this worktree, which births only
		// the Worktree row — so the refusal names the worktree and the clone
		// rather than calling the clone unknown (read-surface decisions, "wip next
		// --set needs a resolved Worktree", amended 2026-08-28).
		return Current{}, tiers.UnknownWorktree(wtName, clone.Label)
	}
	return Current{Repo: repo, Clone: clone, Worktree: wt}, nil
}

// Cursor reads the cursor for one Clone + Worktree as a point lookup. The
// "no cursor set for this clone" case (vocabulary output 4) is a first-class
// result — the second return is false, not an error — because the cursor is
// attention, and attention is allowed to be nowhere (D38).
func Cursor(ctx context.Context, v store.View, cur Current) (node string, set bool, err error) {
	return v.Cursor(ctx, cur.Clone.ID, cur.Worktree.ID)
}
