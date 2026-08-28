package tiers

import (
	"fmt"

	"github.com/procrastivity/wip/internal/wiperr"
)

// unknownClone is vocabulary's ratified unknown-clone refusal
// (workplans/vocabulary.md step-10), reused verbatim: "running against an
// unknown clone without `init` (hard failure, never a guess)" — MODEL §11.
// Every verb in this Matter that needs a resolved current Clone raises this
// when it has none, except `status`, which the Brief's "Read scope" section
// carves out into a host-wide fallback instead.
//
// chassis's wiperr.Error carries one Message rendered identically in human
// and --json mode (its Brief's envelope has no second field for optional
// guidance lines), so this uses vocabulary's --json message body — the
// denser of its two drafted forms — for both.
func unknownClone() error {
	return wiperr.New("refusal.unknown-clone",
		"refused — this clone is unknown to wip; run `wip init` here first (a write; if you cannot run it, ask the user)")
}

// UnknownWorktree is the refusal for the second gap the read-surface decisions
// record: the Clone resolved (its common-dir is known), but the location the
// verb ran from — a linked worktree, or in a degenerate store the main
// worktree itself — has no Worktree row, because `wip init` was never run
// there (tiers Brief, worked example 2: that second init births only the
// Worktree row). It names both tiers so the remedy is unambiguous; a bare
// "this clone is unknown" would contradict what `wip doctor` says one
// directory over. worktreeName is git's own name for the linked worktree, or
// empty for the main worktree.
func UnknownWorktree(worktreeName, cloneLabel string) error {
	where := fmt.Sprintf("worktree %q", worktreeName)
	if worktreeName == "" {
		where = "the main worktree"
	}
	return wiperr.New("refusal.unknown-worktree",
		fmt.Sprintf("refused — %s of clone %q is unknown to wip; run `wip init` here to attach it", where, cloneLabel))
}
