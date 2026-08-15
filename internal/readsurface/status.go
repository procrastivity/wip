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
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// recentSealedWindow is a display policy, not state: archival is a predicate
// over sealed (MODEL §2.3/§9), and this window only decides how long a
// sealed Matter stays in the default `status` listing before it is
// considered old news. It is a hard-coded constant on purpose (D51-adjacent:
// presentation, no new config surface).
const recentSealedWindow = 14 * 24 * time.Hour

// ContentOptions governs Content's presentation choices. All restores the
// full, uncollapsed, unfiltered listing. Now is the "as of" instant recency
// is measured against; the zero value means time.Now(), and a caller (tests)
// may inject a fixed instant instead. Cursor is the current clone's cursor
// node, if any — it and its ancestors are exempt from collapsing and
// recency-hiding, so the one orientation mark `status` draws never
// disappears into a collapsed row.
type ContentOptions struct {
	All    bool
	Now    time.Time
	Cursor string
}

// RepoContent is one Repo's durable answer to the founding questions: what
// is in progress, what is finished (sealed vs. locally complete), and what
// is next to start (the unblocked frontier, plus what's blocking everything
// else). It emits nothing — a pure read (MODEL §1).
type RepoContent struct {
	InProgress []store.Node
	Finished   []Finished
	Ready      []store.Node
	Blocked    []Blocked
	// HiddenSealedMatters counts sealed Matters older than recentSealedWindow
	// that the default view hid from Finished. Always 0 under ContentOptions.All.
	HiddenSealedMatters int
}

// cursorExemptions names the cursor node and its live ancestors — the rows
// collapsing and recency-hiding must leave alone so the cursor mark (and the
// Matter row standing above it) stays visible. A cursor the store no longer
// resolves (D67's "removed" case) yields no exemptions rather than an error:
// orientation is best-effort, never a status failure.
func cursorExemptions(ctx context.Context, v store.View, cursor string) (map[string]bool, error) {
	if cursor == "" {
		return nil, nil
	}
	n, err := v.Node(ctx, cursor)
	if err != nil {
		return nil, nil
	}
	exempt := map[string]bool{n.ID: true}
	for n.Parent != "" {
		n, err = v.Node(ctx, n.Parent)
		if err != nil {
			return nil, err
		}
		exempt[n.ID] = true
	}
	return exempt, nil
}

// Content computes one Repo's RepoContent, scoped by filtering the
// store-wide predicates (InProgress, Done, Frontier — all tier-free facts
// over rows, D66) down to this Repo. `status` calls this once per Repo it is
// about to render — the single Repo inside a known Clone, or every Repo on
// the host outside one (tiers Brief, "Read scope").
//
// Unless opts.All, the finished section is collapsed to its sealed frontier
// (CollapseFinished) and then further thinned by hiding sealed Matters older
// than recentSealedWindow — old sealed context nobody asked to see again.
// Non-Matter rows and unsealed rows are never hidden by recency: a sealed
// Stage inside an open Matter is current context and always shows.
func Content(ctx context.Context, v store.View, repo string, opts ContentOptions) (RepoContent, error) {
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
	if !opts.All {
		exempt, err := cursorExemptions(ctx, v, opts.Cursor)
		if err != nil {
			return RepoContent{}, err
		}
		out.Finished, err = CollapseFinished(ctx, v, out.Finished, exempt)
		if err != nil {
			return RepoContent{}, err
		}
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		cutoff := now.Add(-recentSealedWindow)
		var kept []Finished
		for _, f := range out.Finished {
			if f.Node.Kind == store.ScaleMatter && f.Sealed && !exempt[f.Node.ID] {
				at, ok, err := SealedAt(ctx, v, f.Node)
				if err != nil {
					return RepoContent{}, err
				}
				if ok && at.Before(cutoff) {
					out.HiddenSealedMatters++
					continue
				}
			}
			kept = append(kept, f)
		}
		out.Finished = kept
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
