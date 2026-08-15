package readsurface

// Step-02: the durable unblocked frontier — the durable half of "next to
// start" (MODEL §1) — plus the locally-complete/sealed predicate it and
// step-03's "finished" bucket both need (D13, D55, D62). This is the shared
// logic `status` (step-03) and `next` (step-04) both consume, factored once
// here so the two verbs never diverge on what "unblocked" means, the way
// `tiers` step-06 factored its addressing resolver once for every verb after
// it.

import (
	"context"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// LocallyComplete reports whether a node is Done and every gate declared at
// its own scale is closed against it (D13, D55, D62) — "satisfied at locally
// complete, not sealed" (D29) is this predicate. A Canceled, Paused or still
// In Progress node is never locally complete; an empty declaration set at a
// node's own scale completes at Done (D62) — true for every Step and Stage
// in this dogfood, which declares its one gate at Matter scale only
// (HANDOFF §1.2).
func LocallyComplete(ctx context.Context, v store.View, n store.Node) (bool, error) {
	if n.Lifecycle != store.Done {
		return false, nil
	}
	return gatesClosedAt(ctx, v, n.Repo, n.Kind, n.ID)
}

// Sealed reports whether a node is Done with every gate at its own scale
// *and* every enclosing scale closed against its enclosing node (D55, D62) —
// archivable, not merely handoff-ready. At Matter scale the two completions
// coincide (D13): a Matter has no enclosing scale, so Sealed and
// LocallyComplete agree on every Matter. Below Matter scale they can
// genuinely differ under this dogfood's single declared gate: a Done Step
// is locally complete the moment it's Done (its own scale declares nothing),
// but not sealed until its Matter's `reviewed-local` gate is also closed.
func Sealed(ctx context.Context, v store.View, n store.Node) (bool, error) {
	ok, err := LocallyComplete(ctx, v, n)
	if err != nil || !ok {
		return false, err
	}
	cur := n
	for cur.Parent != "" {
		parent, err := v.Node(ctx, cur.Parent)
		if err != nil {
			return false, err
		}
		satisfied, err := gatesClosedAt(ctx, v, parent.Repo, parent.Kind, parent.ID)
		if err != nil {
			return false, err
		}
		if !satisfied {
			return false, nil
		}
		cur = parent
	}
	return true, nil
}

// gatesClosedAt reports whether every gate a Repo declares at one scale is
// closed against one node at that scale — the one question both
// LocallyComplete (asked of the node itself) and Sealed (asked of each
// ancestor in turn) reduce to.
func gatesClosedAt(ctx context.Context, v store.View, repo string, scale store.Scale, node string) (bool, error) {
	declared, err := v.GateDeclarations(ctx, repo)
	if err != nil {
		return false, err
	}
	var names []string
	for _, d := range declared {
		if d.Scale == scale {
			names = append(names, d.Gate)
		}
	}
	if len(names) == 0 {
		return true, nil // D62: an empty declaration set completes at Done.
	}
	for _, name := range names {
		satisfied, err := v.GateSatisfied(ctx, repo, node, name)
		if err != nil {
			return false, err
		}
		if !satisfied {
			return false, nil
		}
	}
	return true, nil
}

// Blocked is one Planned node still waiting, and what it's waiting for.
type Blocked struct {
	Node     store.Node
	Blockers []store.Node
}

// Frontier computes MODEL §1's durable unblocked half of "next to start":
// every Planned node whose every in-force `blocked-by` edge (D63 — BlockedBy
// already reads only those) points at a node that is at least locally
// complete (D29, D64). A Canceled or Paused blocker still blocks; a
// tombstoned one does not, because BlockedBy never returns an edge to one
// (D64). Ready is returned in creation order (D51: presentation-only);
// Blocked pairs each still-waiting node with the specific blockers holding
// it, so `status`'s and `next`'s "nothing unblocked" output can name them
// rather than just say "blocked". Both are store-wide (D66) — the caller
// (status/next) narrows to its own read scope by filtering on Node.Repo.
func Frontier(ctx context.Context, v store.View) (ready []store.Node, blocked []Blocked, err error) {
	planned, err := v.Planned(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range planned {
		edges, err := v.BlockedBy(ctx, n.ID)
		if err != nil {
			return nil, nil, err
		}
		var unmet []store.Node
		for _, e := range edges {
			blocker, err := v.Node(ctx, e.Blocker)
			if err != nil {
				return nil, nil, err
			}
			ok, err := LocallyComplete(ctx, v, blocker)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				unmet = append(unmet, blocker)
			}
		}
		if len(unmet) == 0 {
			ready = append(ready, n)
		} else {
			blocked = append(blocked, Blocked{Node: n, Blockers: unmet})
		}
	}
	return ready, blocked, nil
}

// CollapseReady narrows a ready list to its outermost nodes: any ready node
// with an ancestor also in the list is dropped, so a ready Planned Matter
// appears alone rather than beside its own Planned interior. This is drafted
// vocabulary output 4's grain — Matters and one Stage, never a Matter's own
// steps beside it — and it is presentation grain only: the dropped nodes are
// still ready (starting one auto-starts its ancestors), and a workable
// frontier inside a *blocked* ancestor still surfaces, because a blocked
// ancestor is not in the ready list.
func CollapseReady(ctx context.Context, v store.View, ready []store.Node) ([]store.Node, error) {
	inReady := make(map[string]bool, len(ready))
	for _, n := range ready {
		inReady[n.ID] = true
	}
	var out []store.Node
	for _, n := range ready {
		drop := false
		for cur := n; cur.Parent != ""; {
			parent, err := v.Node(ctx, cur.Parent)
			if err != nil {
				return nil, err
			}
			if inReady[parent.ID] {
				drop = true
				break
			}
			cur = parent
		}
		if !drop {
			out = append(out, n)
		}
	}
	return out, nil
}

// CollapseFinished narrows a finished list to the sealed frontier, symmetric
// to CollapseReady: any Sealed entry with an ancestor (walked via
// Node.Parent) that is also Sealed and in the list is dropped, so a sealed
// Matter absorbs its own sealed subtree and appears alone. A partly sealed
// Stage stays expanded automatically — its unsealed children are never
// dropped, since the map only marks Sealed entries and an unsealed child
// never matches. Canceled nodes never appear here at all (status renders
// only InProgress/Done/Planned, MODEL §2.3), so no collapsing policy is
// needed for them. Preserve input order (D51: presentation-only). exempt
// names node IDs never dropped regardless — the cursor node and its
// ancestors, so collapsing never erases the one orientation mark `status`
// draws (a cursor on a sealed node is already dangling per D67, but dangling
// is shown, not hidden); nil means no exemptions.
func CollapseFinished(ctx context.Context, v store.View, finished []Finished, exempt map[string]bool) ([]Finished, error) {
	sealedByID := make(map[string]bool, len(finished))
	for _, f := range finished {
		sealedByID[f.Node.ID] = f.Sealed
	}
	var out []Finished
	for _, f := range finished {
		drop := false
		if f.Sealed && !exempt[f.Node.ID] {
			for cur := f.Node; cur.Parent != ""; {
				parent, err := v.Node(ctx, cur.Parent)
				if err != nil {
					return nil, err
				}
				if sealedByID[parent.ID] {
					drop = true
					break
				}
				cur = parent
			}
		}
		if !drop {
			out = append(out, f)
		}
	}
	return out, nil
}

// SealedAt is the sealed-time proxy: there is no `*.sealed` event because
// sealed is the predicate above, not a state, so "when" is the max of (a)
// the node's own scale-correct `*.finished` event time and (b) the latest
// ClosedAt among its closed gates. Either alone can be the later one —
// finish-then-gate-close and gate-close-then-finish are both legal orders
// (D62's order-independence). The bool result is false only when neither
// exists, which should not happen for a node this package already reports
// Sealed, but the caller decides what to do with that.
func SealedAt(ctx context.Context, v store.View, n store.Node) (time.Time, bool, error) {
	var finishedType string
	switch n.Kind {
	case store.ScaleMatter:
		finishedType = store.TypeMatterFinished
	case store.ScaleStage:
		finishedType = store.TypeStageFinished
	case store.ScaleStep:
		finishedType = store.TypeStepFinished
	}
	at, ok, err := v.LastEventAt(ctx, n.ID, []string{finishedType})
	if err != nil {
		return time.Time{}, false, err
	}
	gates, err := v.ClosedGates(ctx, n.ID)
	if err != nil {
		return time.Time{}, false, err
	}
	for _, g := range gates {
		if !ok || g.ClosedAt.After(at) {
			at, ok = g.ClosedAt, true
		}
	}
	return at, ok, nil
}

// Finished is one Done node (MODEL §1's "finished" third), marked sealed vs.
// merely locally complete (D13) — archivable vs. handoff-ready — vocabulary's
// "sealed", never "closed". A Done node that is neither is described, never
// named (MODEL §2.3): its own gate is open, and it is awaiting that gate.
type Finished struct {
	Node            store.Node
	LocallyComplete bool
	Sealed          bool
}

// FinishedNodes computes MODEL §1's "finished" third: every Done node,
// store-wide (D66), marked with both completion predicates so a caller can
// tell "sealed" from "locally complete, gate still open at some enclosing
// scale" from "Done, own gate still open" — the three distinct things a Done
// node can be under D13/D55, collapsed to one binary nowhere in this
// package.
func FinishedNodes(ctx context.Context, v store.View) ([]Finished, error) {
	nodes, err := v.Done(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Finished, 0, len(nodes))
	for _, n := range nodes {
		lc, err := LocallyComplete(ctx, v, n)
		if err != nil {
			return nil, err
		}
		var sl bool
		if lc {
			sl, err = Sealed(ctx, v, n)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, Finished{Node: n, LocallyComplete: lc, Sealed: sl})
	}
	return out, nil
}
