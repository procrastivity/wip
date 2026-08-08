// Package scheduler is the Orchestrator loop's engine (MODEL §6): the one
// dispatch path over a Run's frozen Matter set. This file is the Ready
// engine — one readiness derivation (D64) over in-force edges (D63), applied
// at every plan-node scale within a Matter and across the Batch (G3), with
// engagement and slot accounting against the one Run-wide cap (G4).
//
// Everything here is a read. Engagement, claims, skips, and events belong to
// the loop (step-02); `next`'s frontier display consumes the same derivation
// (step-04) so a read can never disagree with what the scheduler would see.
package scheduler

import (
	"context"
	"sort"

	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
)

// Frontier is one Run's parallel frontier at one moment: the full Ready set,
// what is engaged against the cap, and how many slots remain. Presentation
// order never selects work — Ready is in identity (creation) order, and the
// scheduler's own selection reads the same order, never a sibling sort key
// (G1, D51).
type Frontier struct {
	Run store.Run
	// Members is the Run's frozen dispatch set (F3), in frozen order.
	Members []store.Node
	// Ready is every workable node the Run may engage now: Planned, its own
	// in-force edges satisfied at locally complete (D64), every ancestor's
	// edges likewise satisfied, and no live children of its own — the grain
	// the worked traces call the frontier. The cap does not reduce this set;
	// it only limits engagement (G1).
	Ready []store.Node
	// Engaged is every childless In Progress node in the frozen set — the
	// work currently counting against the cap (G4). A grouping In Progress
	// is attention, not work: only nodes with nothing live beneath them
	// occupy slots.
	Engaged []store.Node
	// Cap is the Run-wide concurrency cap; 1 is plain sequential (D31).
	Cap int
	// Slots is max(0, Cap - len(Engaged)).
	Slots int
}

// Derive computes one Run's Frontier under one cap.
//
// The predicate is D64 at every scale, exactly once: a Planned node is Ready
// when every in-force blocked-by edge on it — and on each of its ancestors —
// is satisfied at locally complete. The ancestor clause is what makes the
// rule scale-uniform for *dispatch*: engaging a Step inside a Matter whose
// own edge is unmet would order work by something other than the graph. (The
// read surface deliberately shows such interiors — a human may look inside a
// blocked Matter; the scheduler may not reach into one.)
func Derive(ctx context.Context, v store.View, run store.Run, runCap int) (Frontier, error) {
	f := Frontier{Run: run, Cap: runCap}
	memberIDs, err := v.RunMatters(ctx, run.ID)
	if err != nil {
		return Frontier{}, err
	}
	for _, id := range memberIDs {
		matter, err := v.Node(ctx, id)
		if err != nil {
			return Frontier{}, err
		}
		f.Members = append(f.Members, matter)

		nodes, err := v.MatterNodes(ctx, id)
		if err != nil {
			return Frontier{}, err
		}
		live := make(map[string]store.Node, len(nodes))
		hasLiveChild := make(map[string]bool)
		for _, n := range nodes {
			live[n.ID] = n
			if n.Parent != "" {
				hasLiveChild[n.Parent] = true
			}
		}

		satisfied := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			ok, err := edgesSatisfied(ctx, v, n.ID)
			if err != nil {
				return Frontier{}, err
			}
			satisfied[n.ID] = ok
		}
		// chainClear: this node's edges and every ancestor's, inside the
		// Matter. (A Step's cross-Matter edges are already banned at plan
		// time — HANDOFF §1.4 — but the derivation does not rely on that:
		// edgesSatisfied reads whatever edges are in force.)
		var chainClear func(n store.Node) bool
		chainClear = func(n store.Node) bool {
			if !satisfied[n.ID] {
				return false
			}
			if n.Parent == "" {
				return true
			}
			parent, ok := live[n.Parent]
			if !ok {
				return false
			}
			return chainClear(parent)
		}

		for _, n := range nodes {
			if hasLiveChild[n.ID] {
				continue // a grouping is attention, not work
			}
			switch n.Lifecycle {
			case store.InProgress:
				f.Engaged = append(f.Engaged, n)
			case store.Planned:
				if chainClear(n) && ancestorsEngageable(n, live) {
					f.Ready = append(f.Ready, n)
				}
			}
		}
	}
	// Identity order, not store order: MatterNodes returns presentation
	// order (sort_key first), and a selection taking a prefix of that would
	// let a reorder choose work — exactly what G1 forbids. Identity is
	// creation order and no one curates it.
	sort.Slice(f.Ready, func(i, j int) bool { return f.Ready[i].ID < f.Ready[j].ID })
	sort.Slice(f.Engaged, func(i, j int) bool { return f.Engaged[i].ID < f.Engaged[j].ID })
	f.Slots = f.Cap - len(f.Engaged)
	if f.Slots < 0 {
		f.Slots = 0
	}
	return f, nil
}

// ancestorsEngageable refuses a workable node under a terminal grouping: a
// Planned Step below a Done, Canceled, or Paused ancestor is not the Run's
// to start — Paused is a person's explicit set-down, and Canceled/Done
// groupings take no new work.
func ancestorsEngageable(n store.Node, live map[string]store.Node) bool {
	for cur := n; cur.Parent != ""; {
		parent, ok := live[cur.Parent]
		if !ok {
			return false
		}
		if parent.Lifecycle != store.Planned && parent.Lifecycle != store.InProgress {
			return false
		}
		cur = parent
	}
	return true
}

// edgesSatisfied is D64's clause over one node: every in-force blocked-by
// edge (D63 — BlockedBy reads only those) satisfied at locally complete
// (D29). Only Done satisfies; a Canceled or Paused blocker still blocks.
func edgesSatisfied(ctx context.Context, v store.View, node string) (bool, error) {
	edges, err := v.BlockedBy(ctx, node)
	if err != nil {
		return false, err
	}
	for _, e := range edges {
		blocker, err := v.Node(ctx, e.Blocker)
		if err != nil {
			return false, err
		}
		ok, err := readsurface.LocallyComplete(ctx, v, blocker)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// Select picks up to slots nodes from ready, in identity order — a stable
// arbitrary order that is not a sibling sort key (G1: selection must not
// read sort order; D51: sort keys are presentation-only). Ready is already
// in identity (creation) order; Select simply takes the prefix of an order
// nobody curates.
func Select(ready []store.Node, slots int) []store.Node {
	if slots <= 0 {
		return nil
	}
	if len(ready) <= slots {
		return ready
	}
	return ready[:slots]
}
