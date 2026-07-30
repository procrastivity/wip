package store

import (
	"context"
	"fmt"
	"sort"
)

// The static cycle check lives here, and only here.
//
// Dependencies are native and cycles are detected statically (D28, D29): an edge
// that would close a cycle is refused *before* it is persisted, so a cycle is
// never recorded as an event. Two callers, one function:
//
//   - `write-surface`'s `wip depend add <node> --blocked-by <node>` calls
//     WouldCycle as a precondition, and refuses without emitting
//     `dependency.added`;
//   - `guards`/`doctor`'s cycle check calls Cycles as a whole-store audit.
//
// Both are expressed over one definition — reachability across the live
// (non-tombstoned) edge set — so there is exactly one answer to "what is a
// cycle" and neither caller can drift from the other.

// waitsFor is the live edge set as an adjacency map: waitsFor[x] is everything x
// is waiting for.
type waitsFor map[string][]string

// liveEdgeGraph loads every live edge. A tombstoned edge is not part of the
// graph: removal means removed, and an edge whose only cycle ran through a
// removed edge is legal again.
//
// It loads the whole set rather than walking the database per hop. wip is a
// personal store (D34) whose edge count is bounded by a plan a person wrote; the
// clarity of one graph, one definition and two callers is worth more here than
// a recursive query that would have to be written twice.
func (v View) liveEdgeGraph(ctx context.Context) (waitsFor, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT blocked, blocker FROM edges WHERE tombstone_event IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: read the live edge set: %w", err)
	}
	defer func() { _ = rows.Close() }()

	graph := waitsFor{}
	for rows.Next() {
		var blocked, blocker string
		if err := rows.Scan(&blocked, &blocker); err != nil {
			return nil, fmt.Errorf("store: read the live edge set: %w", err)
		}
		graph[blocked] = append(graph[blocked], blocker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the live edge set: %w", err)
	}
	return graph, nil
}

// WouldCycle reports whether adding "blocked is blocked by blocker" would close a
// cycle in the live edge set.
//
// It answers the question before anything is written, which is the whole point:
// the caller refuses, and no event is emitted. A self-edge is the degenerate
// cycle and is refused too.
func (v View) WouldCycle(ctx context.Context, blocked, blocker string) (bool, error) {
	if blocked == blocker {
		return true, nil
	}
	graph, err := v.liveEdgeGraph(ctx)
	if err != nil {
		return false, err
	}
	// The new edge makes blocked wait for blocker. That closes a cycle exactly
	// when blocker already waits, transitively, for blocked.
	return reaches(graph, blocker, blocked), nil
}

// reaches reports whether from waits, transitively, for to.
func reaches(graph waitsFor, from, to string) bool {
	seen := map[string]bool{from: true}
	stack := []string{from}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, next := range graph[cur] {
			if next == to {
				return true
			}
			if !seen[next] {
				seen[next] = true
				stack = append(stack, next)
			}
		}
	}
	return false
}

// Cycles returns every cycle in the live edge set, each as the sequence of node
// identities around it in waits-for order — the last node waits for the first, so
// the first is where the loop closes.
//
// This is the store-wide audit `doctor` runs. It shares liveEdgeGraph and the
// same notion of an edge with WouldCycle, so a cycle the audit finds is exactly a
// cycle the add-time check would have refused — which matters, because a cycle
// present in the store is by definition one that got there some other way (an
// edge added before the check existed, a hand-edited database, a bug).
//
// Every distinct loop is reported, and each of them exactly once. That is what the
// shape below is for, and it is why this is not one depth-first sweep recording
// back edges: such a sweep finds at least one loop per tangle but not all of them,
// because two loops can share the edge that closes them and only the one on the
// current path is seen. An audit that reports one loop out of two sends its reader
// round again after every repair. So each loop is enumerated from its own smallest
// node, over a walk that never steps below that node: a loop has exactly one
// smallest node, so it is found exactly once — and it comes back rotated to start
// there, which is the canonical form that makes the same loop reached from two
// entry points one answer rather than two. See step-06's report.
//
// The work is bounded by the number of loops, which in a personal store (D34)
// whose edges are a plan a person wrote is a handful — and is nothing at all in the
// case that runs every day, since a store with no cycles never leaves the first
// hop of any walk.
func (v View) Cycles(ctx context.Context) ([][]string, error) {
	graph, err := v.liveEdgeGraph(ctx)
	if err != nil {
		return nil, err
	}

	// Deterministic iteration at both levels — the entry points here and the hops
	// below — so two runs of doctor over one store report the same cycles in the
	// same order. Only nodes that wait for something can be on a loop, so the keys
	// of the graph are every entry point there is.
	starts := make([]string, 0, len(graph))
	for node := range graph {
		starts = append(starts, node)
	}
	sort.Strings(starts)

	var (
		found  [][]string
		path   []string
		onPath map[string]bool
	)
	// walk extends path one hop at a time, looking for the way back to start.
	var walk func(start, node string)
	walk = func(start, node string) {
		next := append([]string(nil), graph[node]...)
		sort.Strings(next)
		for _, target := range next {
			switch {
			case target == start:
				// The path closed: report it as it stands, starting at start.
				found = append(found, append([]string(nil), path...))
			case target < start || onPath[target]:
				// Below start: every loop through it has a smaller node than start,
				// so it belongs to that node's pass and not to this one. Already on
				// the path: going round twice is not one loop.
				continue
			default:
				onPath[target] = true
				path = append(path, target)
				walk(start, target)
				path = path[:len(path)-1]
				onPath[target] = false
			}
		}
	}
	for _, start := range starts {
		path = []string{start}
		onPath = map[string]bool{start: true}
		walk(start, start)
	}
	return found, nil
}
