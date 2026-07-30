package store

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Tests for step-06: the static cycle check, which had never executed either.
//
// Cycles are refused statically and never recorded as events (CONTRACT §B, D28,
// D29). Two callers built in other Matters call this one function —
// `write-surface`'s `wip depend add … --blocked-by …` as an add-time precondition,
// and `guards`/`doctor`'s cycle check as a store-wide audit — so what has to hold
// is not only that each answer is right but that they are the *same* answer:
//
//   - WouldCycle reports whether a candidate edge would close a loop, before
//     anything is persisted, over the live edge set only;
//   - Cycles reports every loop in the store, each once, in the same order twice;
//   - an edge the audit finds in a loop is an edge the check would have refused,
//     which is the reason both live in this file over one liveEdgeGraph.
//
// The loops the audit is asked about have to be inserted around the API, because
// the add-time check makes them unreachable through the write path. That is not a
// gap in the test: it is what the audit is for. A cycle in a store is always one
// that arrived some other way — a hand-edited database, or the store's own bug.

// ---------------------------------------------------------------------------
// The graph a case describes
// ---------------------------------------------------------------------------

// depGraph turns a case's picture into nodes and edges. Names are locators, which
// is all a locator is — a mutable, human-facing name nothing in the log references
// (MODEL §10) — so a case can be written as `{"c", "a"}` and read back the same
// way when it fails.
type depGraph struct {
	h     *harness
	ids   map[string]string
	names map[string]string
}

// newDepGraph makes one Step per name under one Matter, and one per name in
// `elsewhere` under a second Matter, so a case can put a hop across a Matter
// boundary (D28, D29).
func newDepGraph(h *harness, nodes, elsewhere []string) *depGraph {
	h.t.Helper()
	g := &depGraph{h: h, ids: map[string]string{}, names: map[string]string{}}
	here := h.matter("here", "The Matter under test")
	for _, name := range nodes {
		g.bind(name, h.step(here, name, "Step "+name))
	}
	if len(elsewhere) > 0 {
		there := h.matter("there", "Another Matter entirely")
		for _, name := range elsewhere {
			g.bind(name, h.step(there, name, "Step "+name+", in the other Matter"))
		}
	}
	return g
}

func (g *depGraph) bind(name, id string) {
	g.ids[name] = id
	g.names[id] = name
}

// id resolves a name to the identity the store knows it by.
func (g *depGraph) id(name string) string {
	g.h.t.Helper()
	id, ok := g.ids[name]
	if !ok {
		g.h.t.Fatalf("the case names %q, which is not one of its nodes", name)
	}
	return id
}

// name is the reverse, for reading an answer back out.
func (g *depGraph) name(id string) string {
	if name, ok := g.names[id]; ok {
		return name
	}
	return id
}

// add adds a live edge through the write path: pair[0] waits for pair[1].
func (g *depGraph) add(pair [2]string) string {
	g.h.t.Helper()
	return g.h.depend(g.id(pair[0]), g.id(pair[1]))
}

// addThenRemove adds an edge and removes it, leaving a tombstone — an edge that was
// there and is not, which is where a cycle stops being one.
func (g *depGraph) addThenRemove(pair [2]string) {
	g.h.t.Helper()
	g.h.undepend(g.id(pair[0]), g.add(pair), g.id(pair[1]))
}

// ---------------------------------------------------------------------------
// Reading the audit back
// ---------------------------------------------------------------------------

// cyclePictures renders what the audit found as `a->b->c->a`, by locator and with
// the loop closed, so an assertion about a loop is legible.
func (h *harness) cyclePictures() []string {
	h.t.Helper()
	found, err := h.Cycles(h.ctx)
	if err != nil {
		h.t.Fatalf("audit the store for cycles: %v", err)
	}
	out := make([]string, 0, len(found))
	for _, cycle := range found {
		if len(cycle) == 0 {
			h.t.Fatal("the audit reported an empty cycle")
		}
		names := make([]string, 0, len(cycle)+1)
		for _, node := range cycle {
			names = append(names, h.locatorOf(node))
		}
		// The first node is where the loop closes, so saying it twice is the
		// picture and not a repetition.
		names = append(names, names[0])
		out = append(out, strings.Join(names, "->"))
	}
	return out
}

// wantCycles asserts the whole audit result, in order, as pictures — and asserts
// determinism while it is there, because Cycles sorts for exactly that reason and
// "it sorts" is not the same claim as "two runs agree".
func (h *harness) wantCycles(what string, want ...string) {
	h.t.Helper()
	got := h.cyclePictures()
	wanted := make([]string, 0, len(want))
	wanted = append(wanted, want...)
	if !reflect.DeepEqual(got, wanted) {
		h.t.Errorf("%s: the audit reports %v, want %v", what, got, wanted)
	}
	if again := h.cyclePictures(); !reflect.DeepEqual(got, again) {
		h.t.Errorf("%s: two audits of one store report %v and then %v", what, got, again)
	}
}

// wantCanonicalCycles asserts every reported loop starts at its own smallest
// identity. That is the canonicalisation behind "the same loop found from two entry
// points is reported once": a loop has one smallest node, so a rotation to it is a
// name the loop has no matter where the walk went in.
func (h *harness) wantCanonicalCycles(what string) {
	h.t.Helper()
	found, err := h.Cycles(h.ctx)
	if err != nil {
		h.t.Fatalf("%s: audit the store for cycles: %v", what, err)
	}
	for _, cycle := range found {
		for _, node := range cycle {
			if node < cycle[0] {
				h.t.Errorf("%s: the loop %v starts at %s, which is not its smallest identity",
					what, h.locators(cycle), h.locatorOf(cycle[0]))
				break
			}
		}
	}
}

// locators renders a run of identities by locator.
func (h *harness) locators(ids []string) []string {
	h.t.Helper()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, h.locatorOf(id))
	}
	return out
}

// wantCheckAgreesWithAudit holds the two callers to one answer: every edge of every
// loop the audit reports is an edge the add-time check would have refused. They
// share liveEdgeGraph so that this is true by construction — and it is asserted
// because "by construction" is precisely the claim being made.
func (h *harness) wantCheckAgreesWithAudit(what string) {
	h.t.Helper()
	found, err := h.Cycles(h.ctx)
	if err != nil {
		h.t.Fatalf("%s: audit the store for cycles: %v", what, err)
	}
	if len(found) == 0 {
		h.t.Errorf("%s: the audit reports no loop, so there is nothing for the two to agree about", what)
	}
	for _, cycle := range found {
		for i, blocked := range cycle {
			blocker := cycle[(i+1)%len(cycle)]
			would, err := h.WouldCycle(h.ctx, blocked, blocker)
			if err != nil {
				h.t.Fatalf("%s: ask the check about an edge of %v: %v", what, h.locators(cycle), err)
			}
			if !would {
				h.t.Errorf("%s: the audit reports the loop %v, but the check would have allowed %s to wait for %s",
					what, h.locators(cycle), h.locatorOf(blocked), h.locatorOf(blocker))
			}
		}
	}
}

// wantCycleThrough asserts some reported loop runs along one particular edge: in
// waits-for order the blocker is the hop after the blocked node.
func (h *harness) wantCycleThrough(what, blocked, blocker string) {
	h.t.Helper()
	found, err := h.Cycles(h.ctx)
	if err != nil {
		h.t.Fatalf("%s: audit the store for cycles: %v", what, err)
	}
	for _, cycle := range found {
		for i, node := range cycle {
			if node == blocked && cycle[(i+1)%len(cycle)] == blocker {
				return
			}
		}
	}
	h.t.Errorf("%s: no reported loop runs from %s to %s; the audit reports %v",
		what, h.locatorOf(blocked), h.locatorOf(blocker), h.cyclePictures())
}

// ---------------------------------------------------------------------------
// 1. The check itself, case by case
// ---------------------------------------------------------------------------

// TestWouldCycleRefusesExactlyTheEdgesThatCloseALoop is step-06's table.
//
// Each case describes a live edge set, sometimes an edge that was removed from it,
// and one candidate edge — and then asks the question three ways over the same
// store: what the check answers, what the add-time verb does with that answer, and
// whether the audit agrees once the edge is in the store by other means.
func TestWouldCycleRefusesExactlyTheEdgesThatCloseALoop(t *testing.T) {
	for _, c := range []struct {
		name string
		// nodes become Steps in one Matter; elsewhere, Steps in another.
		nodes, elsewhere []string
		// edges and removed are `{blocked, blocker}` pairs: blocked waits for
		// blocker. The removed ones are added and then taken out again, so what
		// they leave behind is a tombstone.
		edges, removed [][2]string
		// The candidate edge the check is asked about.
		blocked, blocker string
		want             bool
		why              string
	}{
		{
			name:    "a two-node loop is refused",
			nodes:   []string{"a", "b"},
			edges:   [][2]string{{"a", "b"}},
			blocked: "b", blocker: "a",
			want: true,
			why:  "a already waits for b, so b waiting for a closes the pair",
		},
		{
			name:    "a multi-hop loop is refused",
			nodes:   []string{"a", "b", "c", "d", "e"},
			edges:   [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "e"}},
			blocked: "e", blocker: "a",
			want: true,
			why:  "a reaches e in four hops, so e waiting for a closes a five-node loop",
		},
		{
			name:    "a diamond is accepted",
			nodes:   []string{"a", "b", "c", "d"},
			edges:   [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}},
			blocked: "a", blocker: "d",
			want: false,
			why:  "b and c share the ancestor d, and waiting on a shared ancestor is not a loop",
		},
		{
			name:    "a self-edge is refused",
			nodes:   []string{"a"},
			blocked: "a", blocker: "a",
			want: true,
			why:  "a node waiting for itself is the degenerate loop",
		},
		{
			name:    "an edge whose only loop runs through a tombstoned edge is accepted",
			nodes:   []string{"a", "b", "c"},
			edges:   [][2]string{{"a", "b"}},
			removed: [][2]string{{"b", "c"}},
			blocked: "c", blocker: "a",
			want: false,
			why:  "b waited for c until that edge was removed, and a tombstoned edge is not live",
		},
		{
			name:      "a loop reachable only across a Matter boundary is refused",
			nodes:     []string{"a", "b"},
			elsewhere: []string{"c"},
			edges:     [][2]string{{"a", "b"}, {"b", "c"}},
			blocked:   "c", blocker: "a",
			want: true,
			why:  "the hop from b to c leaves the Matter, and D28/D29 make it as real as any other hop",
		},
		{
			name:    "an edge that only restates a path already there is accepted",
			nodes:   []string{"a", "b", "c"},
			edges:   [][2]string{{"a", "b"}, {"b", "c"}},
			blocked: "a", blocker: "c",
			want: false,
			why:  "a already waits for c transitively, and saying so directly adds no loop",
		},
		{
			name:    "an edge joining two separate chains is accepted",
			nodes:   []string{"a", "b", "c", "d"},
			edges:   [][2]string{{"a", "b"}, {"c", "d"}},
			blocked: "b", blocker: "c",
			want: false,
			why:  "the two chains become one acyclic run",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			g := newDepGraph(h, c.nodes, c.elsewhere)
			for _, pair := range c.edges {
				g.add(pair)
			}
			for _, pair := range c.removed {
				g.addThenRemove(pair)
			}
			blocked, blocker := g.id(c.blocked), g.id(c.blocker)

			// The check runs over the live edge set and over nothing else: the
			// tombstoned edges above are in the table and out of the graph.
			got, err := h.WouldCycle(h.ctx, blocked, blocker)
			if err != nil {
				t.Fatalf("ask whether %s may wait for %s: %v", c.blocked, c.blocker, err)
			}
			if got != c.want {
				t.Errorf("%s waiting for %s: WouldCycle is %v, want %v — %s",
					c.blocked, c.blocker, got, c.want, c.why)
			}

			live := len(h.rowsOf("edges", "tombstone_event IS NULL"))
			if c.want {
				// The refusal happens *before* anything is persisted (CONTRACT §B):
				// the verb turns the command down inside its decide function, so
				// there is no `dependency.added` and no edge row to find afterwards.
				refusalMentions(t, "adding an edge that closes a loop",
					h.dependError(blocked, blocker), "would close a cycle")
				h.wantRowCount("a refused edge", "edges",
					"blocked = ? AND blocker = ?", []any{blocked, blocker}, 0)
				h.wantRowCount("a refused edge", "events",
					"type = ? AND subject = ? AND json_extract(payload, '$.blocker') = ?",
					[]any{TypeDependencyAdded, blocked, blocker}, 0)
				h.wantRowCount("a refused edge", "edges", "tombstone_event IS NULL", nil, live)
			} else {
				edge := h.depend(blocked, blocker)
				h.wantRowCount("an accepted edge", "edges",
					"id = ? AND tombstone_event IS NULL", []any{edge}, 1)
			}

			// And now the same question of the audit, which has to answer it the
			// same way over the same graph.
			switch {
			case c.want && blocked == blocker:
				// A self-edge cannot be persisted at all, so no audit will ever see
				// one: the CHECK is the floor under this row of the table.
				event := h.newEvent(blocked)
				rawRefusedBy(h, "a self-edge inserted around the API",
					"CHECK constraint failed: blocked <> blocker",
					rawEdgeSQL, h.NewID(), blocked, blocker, event, event)
				h.wantCycles("a store the check kept a self-edge out of")
			case c.want:
				h.rawEdge(blocked, blocker)
				h.wantCycleThrough("the loop the check refused", blocked, blocker)
				h.wantCanonicalCycles("the loop the check refused")
				h.wantCheckAgreesWithAudit("the loop the check refused")
			default:
				h.wantCycles("a store the check accepted an edge into")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. The audit `guards`/`doctor` will call
// ---------------------------------------------------------------------------

// TestTheAuditReportsNothingInAStoreWithNoLoops is the case that runs every day.
// A diamond, a hop across a Matter boundary and an edge that was removed are all
// perfectly ordinary; none of them is a loop, and the audit says so.
func TestTheAuditReportsNothingInAStoreWithNoLoops(t *testing.T) {
	h := newHarness(t)
	g := newDepGraph(h, []string{"a", "b", "c", "d"}, []string{"e"})
	for _, pair := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}, {"d", "e"}} {
		g.add(pair)
	}
	g.addThenRemove([2]string{"b", "c"})

	h.wantCycles("a store with no loops in it")
}

// TestTheAuditReportsALoopOnceAndTheSameWayTwice is what canonicalisation is for.
//
// A three-node loop can be entered at any of its three nodes. The audit reports it
// once, rotated to its smallest identity, and reports it identically on a second
// run — which is the property `doctor` needs, because a report that reshuffles
// between runs cannot be diffed or acted on.
func TestTheAuditReportsALoopOnceAndTheSameWayTwice(t *testing.T) {
	h := newHarness(t)
	g := newDepGraph(h, []string{"a", "b", "c"}, nil)
	g.add([2]string{"a", "b"})
	g.add([2]string{"b", "c"})

	// The closing edge goes in around the API, because the add-time check refuses
	// it — the audit's whole subject is the store that got one anyway.
	h.rawEdge(g.id("c"), g.id("a"))

	h.wantCycles("a three-node loop", "a->b->c->a")
	h.wantCanonicalCycles("a three-node loop")
	h.wantCheckAgreesWithAudit("a three-node loop")
}

// TestTheAuditFindsEveryLoopAndNotOnlyTheFirst is the case the audit used to get
// wrong.
//
// Two loops can share the edge that closes them, sit inside one another, or share a
// node. A single depth-first sweep that records back edges finds one loop out of
// each such pair and marks the rest of the tangle done — so `doctor` would report
// one loop, the reader would repair it, and the next run would report the next one.
// See step-06's report.
func TestTheAuditFindsEveryLoopAndNotOnlyTheFirst(t *testing.T) {
	for _, c := range []struct {
		name  string
		nodes []string
		// edges go in through the write path; closing edges close loops and so can
		// only go in around it.
		edges, closing [][2]string
		want           []string
	}{
		{
			name:    "two loops closed by one edge",
			nodes:   []string{"a", "b", "c", "d"},
			edges:   [][2]string{{"a", "b"}, {"a", "d"}, {"b", "c"}, {"d", "c"}},
			closing: [][2]string{{"c", "a"}},
			want:    []string{"a->b->c->a", "a->d->c->a"},
		},
		{
			name:    "one loop inside another",
			nodes:   []string{"a", "b", "c", "d"},
			edges:   [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}},
			closing: [][2]string{{"c", "a"}, {"d", "a"}},
			want:    []string{"a->b->c->a", "a->b->c->d->a"},
		},
		{
			name:    "two loops sharing a node",
			nodes:   []string{"a", "b", "c"},
			edges:   [][2]string{{"a", "b"}, {"b", "c"}},
			closing: [][2]string{{"b", "a"}, {"c", "b"}},
			want:    []string{"a->b->a", "b->c->b"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			g := newDepGraph(h, c.nodes, nil)
			for _, pair := range c.edges {
				g.add(pair)
			}
			for _, pair := range c.closing {
				// Every one of these is an edge the check would have refused, which
				// is asserted rather than assumed — it is the same agreement the
				// audit is held to below, taken from the other end.
				would, err := h.WouldCycle(h.ctx, g.id(pair[0]), g.id(pair[1]))
				if err != nil {
					t.Fatalf("ask whether %s may wait for %s: %v", pair[0], pair[1], err)
				}
				if !would {
					t.Errorf("the check would have allowed %s to wait for %s, which closes a loop", pair[0], pair[1])
				}
				h.rawEdge(g.id(pair[0]), g.id(pair[1]))
			}

			h.wantCycles("loops in one tangle", c.want...)
			h.wantCanonicalCycles("loops in one tangle")
			h.wantCheckAgreesWithAudit("loops in one tangle")
		})
	}
}

// TestTheAuditReadsOnlyTheLiveEdgeSet is the audit's half of "a tombstoned edge is
// not part of the graph".
//
// It is also the repair path: the way to get rid of a loop the audit found is a
// `dependency.removed` for one of its edges, and what that leaves behind is a
// tombstoned row and not a shorter table (D44).
func TestTheAuditReadsOnlyTheLiveEdgeSet(t *testing.T) {
	h := newHarness(t)
	g := newDepGraph(h, []string{"a", "b", "c"}, nil)
	g.add([2]string{"a", "b"})
	middle := g.add([2]string{"b", "c"})
	h.rawEdge(g.id("c"), g.id("a"))

	h.wantCycles("a loop in the live edge set", "a->b->c->a")

	// The repair is a `dependency.removed` for one of the loop's edges, and the
	// loop goes with it.
	h.undepend(g.id("b"), middle, g.id("c"))
	h.wantCycles("the same store with one of the loop's edges removed")

	// The row is still there, tombstoned: the audit is reading the live edge set
	// and not an emptier table.
	h.wantRowCount("a retired loop", "edges", "", nil, 3)
	h.wantRowCount("a retired loop", "edges", "id = ? AND tombstone_event IS NOT NULL", []any{middle}, 1)

	// The check says the same thing from its own side: the edge that used to close
	// the loop no longer closes one, because the hop it closed over is not live.
	would, err := h.WouldCycle(h.ctx, g.id("c"), g.id("a"))
	if err != nil {
		t.Fatalf("ask whether c may wait for a: %v", err)
	}
	if would {
		t.Error("c waiting for a still reads as a loop, though the hop it closed over was removed")
	}
}

// ---------------------------------------------------------------------------
// 3. The two functions against an independent enumeration
// ---------------------------------------------------------------------------

// TestTheCheckAndTheAuditAgreeWithAnIndependentEnumeration is the confidence the
// named cases cannot buy.
//
// The cases above pin the shapes a reader can picture. What they cannot pin is the
// class of mistake the audit actually had: a search that finds *a* loop in a tangle
// and stops. So both functions are also checked against a deliberately naive
// oracle written for this test alone — every simple path that returns where it
// started, and a transitive closure computed by relaxation rather than by a walk —
// over a few hundred small random graphs, with fixed seeds so a failure is
// reproducible and this is a test rather than a weather report.
//
// Each trial's edges are retired by tombstoning them, so every trial after the
// first also runs against a table full of dead edges the oracle knows nothing
// about — which is the live-edge-set claim again, for free.
func TestTheCheckAndTheAuditAgreeWithAnIndependentEnumeration(t *testing.T) {
	const trials = 120

	names := []string{"a", "b", "c", "d", "e"}
	h := newHarness(t)
	g := newDepGraph(h, names, nil)

	for trial := range trials {
		want := randomWaitsFor(trial, names)

		// Around the API: better than half of these graphs have a loop in them,
		// and the add-time check exists so that the write path cannot make one.
		birth := h.newEvent(g.id(names[0]))
		var inserted []string
		for _, blocked := range names {
			for _, blocker := range want[blocked] {
				id, err := h.rawEdgeInsert(birth, g.id(blocked), g.id(blocker))
				if err != nil {
					t.Fatalf("trial %d: insert %s waiting for %s: %v", trial, blocked, blocker, err)
				}
				inserted = append(inserted, id)
			}
		}

		// The audit finds exactly the loops the oracle finds.
		found, err := h.Cycles(h.ctx)
		if err != nil {
			t.Fatalf("trial %d: audit the store for cycles: %v", trial, err)
		}
		got := make([]string, 0, len(found))
		for _, cycle := range found {
			named := make([]string, 0, len(cycle))
			for _, node := range cycle {
				named = append(named, g.name(node))
			}
			got = append(got, loopKey(named))
		}
		sort.Strings(got)
		if oracle := everyLoop(want, names); !reflect.DeepEqual(got, oracle) {
			t.Errorf("trial %d over %v: the audit reports %v, want %v", trial, want, got, oracle)
		}
		// No loop twice, whatever the order.
		for i := 1; i < len(got); i++ {
			if got[i] == got[i-1] {
				t.Errorf("trial %d over %v: the audit reports the loop %s twice", trial, want, got[i])
			}
		}

		// And the check answers the reachability question the oracle answers, for
		// every ordered pair — the pairs an edge already joins included.
		reaches := closureOf(want, names)
		for _, blocked := range names {
			for _, blocker := range names {
				would, err := h.WouldCycle(h.ctx, g.id(blocked), g.id(blocker))
				if err != nil {
					t.Fatalf("trial %d: ask whether %s may wait for %s: %v", trial, blocked, blocker, err)
				}
				if wanted := blocked == blocker || reaches[blocker][blocked]; would != wanted {
					t.Errorf("trial %d over %v: %s waiting for %s reads as a loop = %v, want %v",
						trial, want, blocked, blocker, would, wanted)
				}
			}
		}

		// Retire the trial's edges. A tombstoned edge is out of the graph, so the
		// next trial starts from an empty live set.
		death := h.newEvent(g.id(names[0]))
		for _, id := range inserted {
			if err := h.rawExec(
				`UPDATE edges SET tombstone_event = ?, last_event = ? WHERE id = ?`, death, death, id); err != nil {
				t.Fatalf("trial %d: retire an edge: %v", trial, err)
			}
		}
		h.wantRowCount("a retired trial", "edges", "tombstone_event IS NULL", nil, 0)
	}
}

// randomWaitsFor builds one trial's graph as a `blocked -> blockers` map. The
// generator is a two-line xorshift rather than math/rand so the trials are fixed
// for good: seed n is the same graph in every run of this test, forever.
func randomWaitsFor(seed int, names []string) map[string][]string {
	state := uint64(seed)*2654435761 + 12345
	next := func() uint64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	graph := map[string][]string{}
	for _, blocked := range names {
		for _, blocker := range names {
			// No self-edges: the substrate refuses those outright (step-05), so a
			// trial cannot contain one.
			if blocked != blocker && next()%4 == 0 {
				graph[blocked] = append(graph[blocked], blocker)
			}
		}
	}
	return graph
}

// everyLoop is the oracle: every simple path that comes back to where it started,
// from every node, deduplicated by rotation. It is the obvious, wasteful way to
// answer the question, which is exactly why it is worth comparing against.
func everyLoop(graph map[string][]string, names []string) []string {
	keys := map[string]bool{}
	for _, start := range names {
		path := []string{start}
		onPath := map[string]bool{start: true}
		var walk func(node string)
		walk = func(node string) {
			for _, target := range graph[node] {
				switch {
				case target == start:
					keys[loopKey(path)] = true
				case onPath[target]:
					continue
				default:
					onPath[target] = true
					path = append(path, target)
					walk(target)
					path = path[:len(path)-1]
					onPath[target] = false
				}
			}
		}
		walk(start)
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// loopKey names a loop independently of where it was entered: rotate it to its
// smallest node. Two walks that found the same loop from different nodes produce
// the same key, which is what makes "reported once" checkable.
func loopKey(loop []string) string {
	smallest := 0
	for i, node := range loop {
		if node < loop[smallest] {
			smallest = i
		}
	}
	rotated := make([]string, 0, len(loop))
	for i := range loop {
		rotated = append(rotated, loop[(smallest+i)%len(loop)])
	}
	return strings.Join(rotated, "->")
}

// closureOf is the second oracle: transitive reachability by relaxation, so
// nothing here walks the graph the way the code under test does. closureOf[x][y]
// is "x waits, eventually, for y".
func closureOf(graph map[string][]string, names []string) map[string]map[string]bool {
	reaches := map[string]map[string]bool{}
	for _, from := range names {
		reaches[from] = map[string]bool{}
		for _, to := range graph[from] {
			reaches[from][to] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, from := range names {
			for _, via := range names {
				if !reaches[from][via] {
					continue
				}
				for _, to := range names {
					if reaches[via][to] && !reaches[from][to] {
						reaches[from][to] = true
						changed = true
					}
				}
			}
		}
	}
	return reaches
}
