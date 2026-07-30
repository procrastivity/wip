// Package readsurface implements PLAN 1.7's read verbs: `wip status`'s
// founding-question content, `wip next` (with `--set`), and `wip session`.
// MODEL §1's founding question is what is in progress, what is finished,
// what is next to start — plus the temporal tense Session adds, what was
// done recently. Every answer here comes from the store directly, never
// `.wip/`'s regenerated projection (MODEL §3.1, D36).
//
// This package owns no storage of its own; it reads and writes exclusively
// through internal/store's data-access layer (the `schema` Brief) and, for
// the current Clone/Worktree a command runs from, through internal/tiers —
// exactly the contract `tiers/tier-verbs` step-08's `wip status` stub
// already confirms. It deliberately does not import internal/writesurface:
// this Matter's one edge is `read-surface ← schema, tiers/tier-verbs`, and
// its own addressing needs (`wip next --set <locator>`) are small enough to
// own here rather than depend on the write verbs for.
package readsurface

import "github.com/procrastivity/wip/internal/wiperr"

// unknownClone is vocabulary's ratified unknown-clone refusal
// (workplans/vocabulary.md step-10), reused verbatim — the same message
// `tiers`'s own errors.go raises, for the same reason: every verb in this
// package that needs a resolved current Clone (and, since the cursor keys at
// Clone *and* Worktree, D38, a resolved current Worktree too) raises this
// when it has none, except `status`, which inherits `tiers`'s own
// host-wide carve-out rather than this package's own.
func unknownClone() error {
	return wiperr.New("refusal.unknown-clone",
		"refused — this clone is unknown to wip; run `wip init` here first")
}
