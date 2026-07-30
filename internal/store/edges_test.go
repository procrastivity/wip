package store

import (
	"context"
	"reflect"
	"testing"
)

// Tests for step-05: the `blocked-by` edge table, and the tombstone mechanism it
// shares with removed nodes. Neither had ever executed before this Step.
//
// What has to hold:
//
//   - an edge is a row with its own identity — one row per directed
//     `blocker -> blocked` pair, one insert per `dependency.added` and one
//     tombstone per `dependency.removed` (D44) — and never an array on a node,
//     which is what makes each of those verbs touch exactly one row;
//   - any node may block any node, within a Matter and across Matters (MODEL §9,
//     D28, D29);
//   - removal is soft at every scale: the row stays, its identity is never
//     reissued, and every prior event that referenced it stays a valid identity
//     reference (MODEL §10 identity-not-locator);
//   - the mechanism is *one* mechanism. The same helper tombstones a removed node
//     and a removed edge, so there is one answer to "what does removal leave
//     behind" rather than one per entity;
//   - Canceled is not a tombstone, and the two are orthogonal in both orders (the
//     `schema` Brief, "Tombstones and Canceled").

// ---------------------------------------------------------------------------
// 1. One row per edge, tombstoned and never deleted
// ---------------------------------------------------------------------------

// TestAnAddedEdgeIsOneRowAndARemovedEdgeIsATombstone is the shape claim itself.
// Edges are rows in a dedicated table, so `dependency.added` is one insert and
// `dependency.removed` is one tombstone — and the row a removal leaves behind is
// still there afterwards, which is the whole content of "never hard-deleted"
// (D44).
func TestAnAddedEdgeIsOneRowAndARemovedEdgeIsATombstone(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("edges", "A Matter whose order means something")
	first := h.step(matter, "step-01", "The Step that has to go first")
	second := h.step(matter, "step-02", "The Step that waits for it")

	edge := h.depend(second, first)
	added := h.lastEventOf(second)

	// One row — and its own identity: not the blocked node's, not the blocker's,
	// not its event's. An edge is an entity (MODEL §9).
	h.wantRowCount("one dependency.added", "edges", "", nil, 1)
	for what, other := range map[string]string{
		"the blocked node": second,
		"the blocker":      first,
		"its own event":    added.ID,
	} {
		if edge == other {
			t.Errorf("the edge's identity is %s's", what)
		}
	}
	h.wantRow("an added edge", "edges", "id = ?", []any{edge}, map[string]any{
		"id":              edge,
		"blocked":         second,
		"blocker":         first,
		"birth_event":     added.ID,
		"last_event":      added.ID,
		"tombstone_event": nil,
	})

	// The event names the edge, which is what lets a later removal name it by
	// identity instead of searching for a pair.
	var p DependencyChange
	if err := decode(added, &p); err != nil {
		t.Fatalf("decode %s: %v", added.Type, err)
	}
	if p.Edge != edge || p.Blocker != first {
		t.Errorf("dependency.added carries edge %s blocked by %s, want %s and %s", p.Edge, p.Blocker, edge, first)
	}

	// Both read paths see it.
	h.wantBlockers("an added edge", second, "step-01")
	h.wantLiveEdges("an added edge", Edge{ID: edge, Blocked: second, Blocker: first})

	// --- and now the removal ------------------------------------------------
	h.undepend(second, edge, first)
	removed := h.lastEventOf(second)

	// Still exactly one row: the removal tombstoned it and deleted nothing.
	h.wantRowCount("a removed edge", "edges", "", nil, 1)
	h.wantRow("a removed edge", "edges", "id = ?", []any{edge}, map[string]any{
		"id":      edge,
		"blocked": second,
		"blocker": first,
		// The row was born once and by one event, and removal did not re-birth it.
		"birth_event":     added.ID,
		"last_event":      removed.ID,
		"tombstone_event": removed.ID,
	})

	// Neither read path sees a tombstoned edge.
	h.wantBlockers("a removed edge", second)
	h.wantLiveEdges("a removed edge")

	// The edge's history hangs off the blocked node, and removal did not shorten
	// it: both events are still readable, in order.
	var types []string
	for _, ev := range h.eventsOf(second) {
		types = append(types, ev.Type)
	}
	want := []string{TypeStepCreated, TypeDependencyAdded, TypeDependencyRemoved}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("the blocked Step's history is %v, want %v", types, want)
	}
}

// TestTheLiveEdgeIndexRefusesADuplicateAndPermitsAReAdd is why `edges_live` is
// partial rather than plain.
//
// One directed pair may be live once, so a second `dependency.added` for a pair
// already there is a duplicate and the substrate says so — even though the second
// edge carries its own identity, because the index is over the pair. But a
// *removed* edge is in nobody's way: the pair can be added again, and what arrives
// is a new row with a new identity while the old one stays tombstoned. Re-adding
// is never resurrection, because an identity is never reissued (D44).
func TestTheLiveEdgeIndexRefusesADuplicateAndPermitsAReAdd(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("edges", "Edges")
	first := h.step(matter, "step-01", "The Step that has to go first")
	second := h.step(matter, "step-02", "The Step that waits for it")

	edge := h.depend(second, first)
	added := h.lastEventOf(second)

	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeDependencyAdded,
			Subject: second,
			Payload: DependencyChange{Edge: tx.NewID(), Blocker: first},
		}}, nil
	})
	refusalMentions(t, "a second live edge for one pair", err, "UNIQUE constraint failed: edges.blocked, edges.blocker")
	// The refusal left nothing behind — not a row and not an event.
	h.wantRowCount("a refused duplicate", "edges", "", nil, 1)
	h.wantRowCount("a refused duplicate", "events", "type = ?", []any{TypeDependencyAdded}, 1)

	// Removed, the pair is free again, and what comes back is a different edge.
	h.undepend(second, edge, first)
	removal := h.lastEventOf(second)
	reAdded := h.depend(second, first)
	reAddition := h.lastEventOf(second)
	if reAdded == edge {
		t.Error("the re-added edge reused the removed edge's identity")
	}

	h.wantRowCount("a re-added edge", "edges", "", nil, 2)
	h.wantRow("the edge that was removed", "edges", "id = ?", []any{edge}, map[string]any{
		"id":              edge,
		"blocked":         second,
		"blocker":         first,
		"birth_event":     added.ID,
		"last_event":      removal.ID,
		"tombstone_event": removal.ID,
	})
	h.wantRow("the edge that replaced it", "edges", "id = ?", []any{reAdded}, map[string]any{
		"id":              reAdded,
		"blocked":         second,
		"blocker":         first,
		"birth_event":     reAddition.ID,
		"last_event":      reAddition.ID,
		"tombstone_event": nil,
	})
	h.wantLiveEdges("a re-added edge", Edge{ID: reAdded, Blocked: second, Blocker: first})
	h.wantBlockers("a re-added edge", second, "step-01")
}

// TestARemovalMustNameAnEdgeThatIsActuallyTheSubjects is why `dependency.removed`
// carries a blocker it could have looked up.
//
// The event's subject is the blocked node and its payload names the blocker, so the
// event states which pair it is dissolving — and an event naming an edge belonging
// to some other pair is describing something that did not happen. Before this was
// checked, the removal tombstoned whatever row the id pointed at: one node's
// command could take out another node's edge, and the log would read as though the
// first node had had that edge all along.
func TestARemovalMustNameAnEdgeThatIsActuallyTheSubjects(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("edges", "Edges")
	first := h.step(matter, "step-01", "The Step that has to go first")
	second := h.step(matter, "step-02", "The Step that waits for it")
	third := h.step(matter, "step-03", "A Step with no edges of its own")

	edge := h.depend(second, first)

	// The right edge, the wrong blocked node.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{removeEdgeDraft(third, edge, first)}, nil
	})
	refusalMentions(t, "a removal from a node the edge does not belong to", err, "stops waiting for")

	// The right blocked node, the wrong blocker.
	err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{removeEdgeDraft(second, edge, third)}, nil
	})
	refusalMentions(t, "a removal naming the wrong blocker", err, "stops waiting for")

	// An edge that never existed.
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{removeEdgeDraft(second, tx.NewID(), first)}, nil
	})
	refusalMentions(t, "a removal naming no edge at all", err, "which is not an edge")

	// None of it moved the row, and the edge is still live.
	h.wantRow("an edge three refused removals left alone", "edges", "id = ?", []any{edge}, map[string]any{
		"id":              edge,
		"blocked":         second,
		"blocker":         first,
		"birth_event":     h.lastEventOf(second).ID,
		"last_event":      h.lastEventOf(second).ID,
		"tombstone_event": nil,
	})
	h.wantBlockers("an edge three refused removals left alone", second, "step-01")
}

// TestASelfEdgeIsRefusedByTheSubstrate is the floor under step-06's static check.
//
// A node blocked by itself is the degenerate cycle. WouldCycle refuses it before
// anything is written, and the `blocked <> blocker` CHECK refuses it even when
// nobody asked — so the substrate is what makes it unrepresentable, rather than
// the check being the only thing standing in the way.
func TestASelfEdgeIsRefusedByTheSubstrate(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("edges", "Edges")
	step := h.step(matter, "step-01", "A Step that cannot wait for itself")

	// Through the write path, past the static check a verb would have run first.
	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeDependencyAdded,
			Subject: step,
			Payload: DependencyChange{Edge: tx.NewID(), Blocker: step},
		}}, nil
	})
	refusalMentions(t, "a self-edge through the write path", err, "CHECK constraint failed: blocked <> blocker")

	// And around the API entirely.
	event := h.newEvent(step)
	rawRefusedBy(h, "a self-edge inserted around the API", "CHECK constraint failed: blocked <> blocker",
		rawEdgeSQL, h.NewID(), step, step, event, event)

	h.wantRowCount("a refused self-edge", "edges", "", nil, 0)
	h.wantRowCount("a refused self-edge", "events", "type = ?", []any{TypeDependencyAdded}, 0)
}

// ---------------------------------------------------------------------------
// 2. Any node -> any node (MODEL §9, D28, D29)
// ---------------------------------------------------------------------------

// TestAnyNodeMayBlockAnyNodeWithinAndAcrossMatters is why nodes share one table
// and why the edge table's two columns are both plain references into it.
//
// Dependencies are native and cross-Matter dependencies are first-class (D28,
// D29): a Step may wait on a grouping, a grouping on a Step in another Matter, and
// a Matter on a Matter. None of those is a special case here — they are five rows
// in one table, which is the point of not modelling edges per scale.
func TestAnyNodeMayBlockAnyNodeWithinAndAcrossMatters(t *testing.T) {
	h := newHarness(t)

	here := h.matter("here", "The Matter the work is in")
	stage := h.stage(here, "stage-1", "A grouping")
	grouped := h.step(stage, "step-01", "A Step inside the grouping")
	loose := h.step(here, "step-02", "A Step directly under the Matter")

	there := h.matter("there", "Another Matter entirely")
	elsewhere := h.step(there, "step-01", "A Step in the other Matter")

	var live []Edge
	for _, c := range []struct {
		what             string
		blocked, blocker string
		across           bool
	}{
		{what: "a Step blocked by a Step", blocked: loose, blocker: grouped},
		{what: "a Step blocked by a Stage", blocked: loose, blocker: stage},
		{what: "a Stage blocked by a Step in another Matter", blocked: stage, blocker: elsewhere, across: true},
		{what: "a Step blocked by a Step in another Matter", blocked: elsewhere, blocker: grouped, across: true},
		{what: "a Matter blocked by a Matter", blocked: there, blocker: here, across: true},
	} {
		edge := h.depend(c.blocked, c.blocker)
		event := h.lastEventOf(c.blocked)
		h.wantRow(c.what, "edges", "id = ?", []any{edge}, map[string]any{
			"id":              edge,
			"blocked":         c.blocked,
			"blocker":         c.blocker,
			"birth_event":     event.ID,
			"last_event":      event.ID,
			"tombstone_event": nil,
		})
		live = append(live, Edge{ID: edge, Blocked: c.blocked, Blocker: c.blocker})

		// The Matter boundary the case claims to cross is read off the rows rather
		// than assumed, so "across Matters" is an assertion and not a comment.
		blocked, err := h.Node(h.ctx, c.blocked)
		if err != nil {
			t.Fatalf("%s: read the blocked node: %v", c.what, err)
		}
		blocker, err := h.Node(h.ctx, c.blocker)
		if err != nil {
			t.Fatalf("%s: read the blocker: %v", c.what, err)
		}
		if across := blocked.Matter != blocker.Matter; across != c.across {
			t.Errorf("%s: crosses a Matter boundary = %v, want %v", c.what, across, c.across)
		}
	}

	// All five, in one table, all live.
	h.wantLiveEdges("edges at every pairing of scales", live...)
	h.wantBlockers("a Step waiting on a Step and a grouping", loose, "step-01", "stage-1")
	h.wantBlockers("a grouping waiting on another Matter's Step", stage, "step-01")
	h.wantBlockers("a Matter waiting on another Matter", there, "here")
}

// ---------------------------------------------------------------------------
// 3. One tombstone mechanism, and a removed node that still resolves
// ---------------------------------------------------------------------------

// TestOneTombstoneMechanismServesNodesAndEdges is step-05's generality claim:
// removal is one mechanism, general over the table.
//
// Both cases below go through the same helper, which is why both refusals are the
// same sentence with a different table in it. And both are refused rather than
// silently doing nothing a second time — a projection that shrugged at a removal
// it had already applied would have stopped being a projection (exactlyRows).
func TestOneTombstoneMechanismServesNodesAndEdges(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("removal", "Removal")
	keep := h.step(matter, "step-01", "The Step that stays")
	gone := h.step(matter, "step-02", "The Step that is taken out of the plan")
	edge := h.depend(gone, keep)

	for _, c := range []struct {
		what, table, id string
		remove          Draft
		// reissue tries to spend the identity again, which the row still holding
		// the primary key is what refuses.
		reissue        Draft
		reissueRefusal string
	}{
		{
			what:   "a removed edge",
			table:  "edges",
			id:     edge,
			remove: removeEdgeDraft(gone, edge, keep),
			// The same edge identity on a pair nothing occupies: the pair is free,
			// the primary key is not.
			reissue: Draft{
				Type:    TypeDependencyAdded,
				Subject: keep,
				Payload: DependencyChange{Edge: edge, Blocker: gone},
			},
			reissueRefusal: "UNIQUE constraint failed: edges.id",
		},
		{
			what:   "a removed node",
			table:  "nodes",
			id:     gone,
			remove: Draft{Type: TypeStepRemoved, Subject: gone, Payload: Removed{Reason: "the plan changed"}},
			reissue: Draft{
				Type:    TypeStepCreated,
				Subject: gone,
				Payload: NodeBirth{Title: "A Step with a spent identity", Locator: "step-03", Parent: matter, SortKey: 3 * sortKeyGap},
			},
			reissueRefusal: "UNIQUE constraint failed: nodes.id",
		},
	} {
		before := h.rowOf(c.table, "id = ?", c.id)
		removal := h.commit(c.remove)[0]
		after := h.rowOf(c.table, "id = ?", c.id)

		// The row is still there, born by the event that bore it, advanced to the
		// event that removed it, and marked by it.
		if after["birth_event"] != before["birth_event"] {
			t.Errorf("%s: birth_event moved from %s to %s", c.what, before["birth_event"], after["birth_event"])
		}
		for _, col := range []string{"last_event", "tombstone_event"} {
			if want := sqlLit(t, removal.ID); after[col] != want {
				t.Errorf("%s: %s.%s is %s, want %s", c.what, c.table, col, after[col], want)
			}
		}

		// A second removal is refused by the one helper — which names the table it
		// was pointed at, so this assertion is also what shows there is one.
		err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{c.remove}, nil
		})
		refusalMentions(t, "removing "+c.what+" a second time", err,
			c.id+" in "+c.table+" is already tombstoned or does not exist")

		// And the identity stays spent, because the row is still holding it.
		err = h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{c.reissue}, nil
		})
		refusalMentions(t, "reissuing "+c.what+"'s identity", err, c.reissueRefusal)
	}
}

// TestARemovedNodeStaysResolvableByIdentity is the other half of what a tombstone
// is for.
//
// Amendment is complete because identity removed the old obstruction (D44): a
// removed Step is a tombstoned row and not an erased one, so every prior event that
// named it stays a valid identity reference and its whole history stays readable.
// What it stops being is live — it answers no read that asks for a node, and it
// does not resolve by locator at all.
func TestARemovedNodeStaysResolvableByIdentity(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("removal", "Removal")
	keep := h.step(matter, "step-01", "The Step that stays")
	gone := h.step(matter, "step-02", "The Step that is taken out of the plan")
	h.depend(gone, keep)
	h.write(gone, KindBody, []byte("What this Step was going to be.\n"))

	before := h.eventsOf(gone)
	if len(before) != 3 {
		t.Fatalf("the Step has %d events before removal, want 3", len(before))
	}
	h.commit(Draft{Type: TypeStepRemoved, Subject: gone, Payload: Removed{Reason: "the plan changed"}})

	// Tombstoned answers by identity, and it answers true. It is not an error to
	// ask about a removed node — that is the difference between soft and hard.
	tombstoned, err := h.Tombstoned(h.ctx, gone)
	if err != nil {
		t.Fatalf("ask whether the removed Step is tombstoned: %v", err)
	}
	if !tombstoned {
		t.Error("the removed Step is not tombstoned")
	}

	// The whole history is still there, one event longer than before.
	after := h.eventsOf(gone)
	if len(after) != len(before)+1 {
		t.Errorf("the removed Step has %d events, want %d", len(after), len(before)+1)
	}
	// And every prior event that named it still resolves: its subject is still a
	// row, and so is each entity a payload of its pointed at.
	for _, ev := range before {
		h.wantRowCount("a prior event's subject", "nodes", "id = ?", []any{ev.Subject}, 1)
		if ev.Type != TypeDependencyAdded {
			continue
		}
		var p DependencyChange
		if err := decode(ev, &p); err != nil {
			t.Fatalf("decode %s: %v", ev.Type, err)
		}
		h.wantRowCount("the edge a prior event named", "edges", "id = ?", []any{p.Edge}, 1)
		h.wantRowCount("the blocker a prior event named", "nodes", "id = ?", []any{p.Blocker}, 1)
	}

	// What it is not is live, by either address.
	if _, err := h.Node(h.ctx, gone); err == nil {
		t.Error("a removed node still reads as live")
	}
	if _, err := h.NodeByLocator(h.ctx, matter, "step-02"); err == nil {
		t.Error("a removed node still resolves by locator")
	}
	if children, err := h.Children(h.ctx, matter); err != nil {
		t.Fatalf("read the Matter's children: %v", err)
	} else if len(children) != 1 || children[0].ID != keep {
		t.Errorf("the Matter has %d live children, want only step-01", len(children))
	}
}

// ---------------------------------------------------------------------------
// 4. Canceled is explicitly not a tombstone
// ---------------------------------------------------------------------------

// TestCanceledIsNotATombstoneAtAnyScale is the distinction the `schema` Brief
// resolves.
//
// Canceled is a lifecycle terminal state: the node still exists, stays addressable
// by identity *and* by locator, keeps its history, and shows up as work that ended
// without sealing. It is not a deletion, and it leaves no tombstone — which is why
// the assertion below reads the column rather than the state.
func TestCanceledIsNotATombstoneAtAnyScale(t *testing.T) {
	h := newHarness(t)

	// The lifecycle is uniform at every scale (MODEL §2.2), so all three cancel the
	// same way and all three have to survive it the same way.
	matter := h.matter("canceled", "A Matter that ended without sealing")
	stage := h.stage(matter, "stage-1", "A grouping that was called off")
	step := h.step(stage, "step-01", "A Step that was called off")

	for _, c := range []struct{ what, locator, node string }{
		{"a canceled Step", "step-01", step},
		{"a canceled Stage", "stage-1", stage},
		{"a canceled Matter", "canceled", matter},
	} {
		h.cancel(c.node)
		h.wantLifecycle(c.node, Canceled)

		// Still live: the read surface that filters tombstones still returns it.
		node, err := h.Node(h.ctx, c.node)
		if err != nil {
			t.Errorf("%s does not read as live: %v", c.what, err)
			continue
		}
		// Still addressable by locator, which is exactly what a tombstoned node
		// stops being.
		byLocator, err := h.NodeByLocator(h.ctx, matter, c.locator)
		if err != nil {
			t.Errorf("%s does not resolve by locator %q: %v", c.what, c.locator, err)
		} else if byLocator.ID != node.ID {
			t.Errorf("%s resolves by locator %q to %s", c.what, c.locator, byLocator.ID)
		}
		// Its history is intact: birth and the cancel, both still readable.
		var types []string
		for _, ev := range h.eventsOf(c.node) {
			types = append(types, ev.Type)
		}
		if len(types) != 2 {
			t.Errorf("%s has the history %v, want its birth and its cancel", c.what, types)
		}
		// And no tombstone, read off the column and not inferred from the state.
		tombstoned, err := h.Tombstoned(h.ctx, c.node)
		if err != nil {
			t.Fatalf("%s: ask whether it is tombstoned: %v", c.what, err)
		}
		if tombstoned {
			t.Errorf("%s is tombstoned", c.what)
		}
		if row := h.rowOf("nodes", "id = ?", c.node); row["tombstone_event"] != "NULL" {
			t.Errorf("%s carries tombstone_event %s, want NULL", c.what, row["tombstone_event"])
		}
	}

	// A canceled Matter appears everywhere a live node appears, and a tombstoned one
	// appears nowhere. Same read, two rows, and the difference is the tombstone.
	folded := h.matter("folded", "A Matter that was folded into another")
	h.commit(Draft{Type: TypeStepRemoved, Subject: folded, Payload: Removed{Reason: "folded into another"}})

	matters, err := h.Matters(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("list the Repo's Matters: %v", err)
	}
	var locators []string
	for _, m := range matters {
		locators = append(locators, m.Locator)
	}
	if !reflect.DeepEqual(locators, []string{"canceled"}) {
		t.Errorf("the Repo's Matters are %v, want only the canceled one", locators)
	}
	// And a canceled Matter's canceled children are still its children.
	nodes, err := h.MatterNodes(h.ctx, matter)
	if err != nil {
		t.Fatalf("list the canceled Matter's nodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("the canceled Matter holds %d live nodes, want all 3", len(nodes))
	}
}

// TestCanceledAndTombstonedAreOrthogonalInBothOrders is the Brief's own sentence
// as a test: a Step may be Canceled and later removed (a canceled node in a
// tombstoned row), or removed while still Planned.
//
// The two facts live in two different columns, which is what makes them orthogonal
// rather than two values of one thing — so both assertions below are about one row
// carrying both answers at once.
func TestCanceledAndTombstonedAreOrthogonalInBothOrders(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("orthogonal", "Two mechanisms, one row")

	// Canceled, then removed.
	ended := h.step(matter, "step-01", "A Step called off and then taken out")
	h.cancel(ended)
	endedRemoval := h.commit(Draft{
		Type:    TypeStepRemoved,
		Subject: ended,
		Payload: Removed{Reason: "and then out of the plan"},
	})[0]
	h.wantRow("canceled and then removed", "nodes", "id = ?", []any{ended}, map[string]any{
		"id":              ended,
		"kind":            ScaleStep,
		"repo":            h.Repo,
		"matter":          matter,
		"parent":          matter,
		"locator":         "step-01",
		"title":           "A Step called off and then taken out",
		"lifecycle":       Canceled,
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     h.birthEventOf(ended).ID,
		"last_event":      endedRemoval.ID,
		"tombstone_event": endedRemoval.ID,
	})

	// Removed while still Planned: a tombstoned row whose lifecycle never moved.
	untouched := h.step(matter, "step-02", "A Step taken out before anyone began")
	untouchedRemoval := h.commit(Draft{
		Type:    TypeStepRemoved,
		Subject: untouched,
		Payload: Removed{Reason: "out of the plan before it started"},
	})[0]
	plannedAndRemoved := map[string]any{
		"id":        untouched,
		"kind":      ScaleStep,
		"repo":      h.Repo,
		"matter":    matter,
		"parent":    matter,
		"locator":   "step-02",
		"title":     "A Step taken out before anyone began",
		"lifecycle": Planned,
		// step-01 was removed before this Step was born, and a tombstoned sibling
		// holds no position: NextSortKey reads live siblings only, and a sort key is
		// presentation and never a sequence number (D51).
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     h.birthEventOf(untouched).ID,
		"last_event":      untouchedRemoval.ID,
		"tombstone_event": untouchedRemoval.ID,
	}
	h.wantRow("removed while still Planned", "nodes", "id = ?", []any{untouched}, plannedAndRemoved)

	// And it stays Planned. "Removed while still Planned" is only a durable fact if
	// a removed node's lifecycle cannot move afterwards: the row is out of every
	// answer, so a transition against it has nothing to move. Before this was
	// checked, `step.canceled` against a tombstoned Step was accepted and the
	// projection recorded a lifecycle move on a row no reader can reach.
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeStepCanceled,
			Subject: untouched,
			Payload: Transition{From: Planned, To: Canceled},
		}}, nil
	})
	refusalMentions(t, "canceling a removed Step", err, "touched 0 projection rows")
	h.wantRow("removed while still Planned, and still Planned", "nodes", "id = ?", []any{untouched}, plannedAndRemoved)
}
