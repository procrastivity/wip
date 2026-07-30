package readsurface

// Step-03: `wip status`'s founding-question content, composed over the
// tier-scoped stub `tiers/tier-verbs` step-08 already built. This package
// does not touch `tiers.Status` or its types at all — it reads that Step's
// output as settled input (this Matter's own seed card, verbatim) and adds
// the durable answer to all three founding questions on top, scoped to the
// same Repo(s) the tier scoping already resolved. `status` takes no cursor
// dependency: it may *mark* the current clone's cursor node for orientation
// (the CLI layer does that, since only it knows the current Clone/Worktree),
// but the content computed here is identical from any clone (MODEL §1).

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// RepoContent is one Repo's durable answer to the founding questions: what
// is in progress, what is finished (sealed vs. locally complete), and what
// is next to start (the unblocked frontier, plus what's blocking everything
// else). It emits nothing — a pure read (MODEL §1).
type RepoContent struct {
	InProgress []store.Node
	Finished   []Finished
	Ready      []store.Node
	Blocked    []Blocked
}

// Content computes one Repo's RepoContent, scoped by filtering the
// store-wide predicates (InProgress, Done, Frontier — all tier-free facts
// over rows, D66) down to this Repo. `status` calls this once per Repo it is
// about to render — the single Repo inside a known Clone, or every Repo on
// the host outside one (tiers Brief, "Read scope").
func Content(ctx context.Context, v store.View, repo string) (RepoContent, error) {
	inProgress, err := v.InProgress(ctx)
	if err != nil {
		return RepoContent{}, err
	}
	finished, err := FinishedNodes(ctx, v)
	if err != nil {
		return RepoContent{}, err
	}
	ready, blocked, err := Frontier(ctx, v)
	if err != nil {
		return RepoContent{}, err
	}

	out := RepoContent{}
	for _, n := range inProgress {
		if n.Repo == repo {
			out.InProgress = append(out.InProgress, n)
		}
	}
	for _, f := range finished {
		if f.Node.Repo == repo {
			out.Finished = append(out.Finished, f)
		}
	}
	for _, n := range ready {
		if n.Repo == repo {
			out.Ready = append(out.Ready, n)
		}
	}
	// Same grain as next's candidate list (CollapseReady): a ready Matter
	// stands for its own Planned interior in "next to start".
	out.Ready, err = CollapseReady(ctx, v, out.Ready)
	if err != nil {
		return RepoContent{}, err
	}
	for _, b := range blocked {
		if b.Node.Repo == repo {
			out.Blocked = append(out.Blocked, b)
		}
	}
	return out, nil
}
