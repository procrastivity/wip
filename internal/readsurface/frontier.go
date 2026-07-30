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
	closed, err := v.ClosedGates(ctx, node)
	if err != nil {
		return false, err
	}
	isClosed := make(map[string]bool, len(closed))
	for _, c := range closed {
		isClosed[c.Gate] = true
	}
	for _, name := range names {
		if !isClosed[name] {
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
