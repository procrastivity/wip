package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests for the `(D46-dependent)` half of step-03: these tables are *projections*.
//
// D46 was closed in favour of the event-sourced shape (D61), which turns the
// entity tables from primary data into a maintained derivation of the log. That
// claim is worth exactly as much as the tests below make it worth, because it is
// the one claim the shape was chosen for:
//
//   - a rebuild reproduces the maintained projection column for column, over a
//     history that exercises every rule there is;
//   - config and gate declarations are the documented exception and a rebuild
//     leaves them alone (D4, D54);
//   - the log itself is untouched by a rebuild;
//   - a projection row cannot appear, move backwards, or be deleted except the way
//     the shape says, and the substrate is what says so rather than this package;
//   - applyEvent is total over the registered taxonomy, so a type an emitter in a
//     later Matter adds cannot arrive with no rule.

// ---------------------------------------------------------------------------
// Writing a history worth rebuilding
// ---------------------------------------------------------------------------

// insertStep is step.inserted: a Step arriving in a plan that already exists,
// which is amendment and not creation even though it projects the same row.
func (h *harness) insertStep(parent, locator, title string, sortKey int64) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{
			Type:    TypeStepInserted,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: title, Locator: locator, Parent: parent, SortKey: sortKey},
		}, nil
	})
}

// reorder rewrites a parent's whole sibling order. The event carries the entire
// live set, so the log narrates the result rather than a delta.
func (h *harness) reorder(parent string, order ...string) Event {
	h.t.Helper()
	return h.commit(Draft{Type: TypeStepReordered, Subject: parent, Payload: Reordered{Order: order}})[0]
}

// replace tombstones a Step and births its replacement in the same sibling
// position — the amendment identity made expressible (D44).
func (h *harness) replace(step, locator, title string) string {
	h.t.Helper()
	replacement := h.NewID()
	h.commit(Draft{
		Type:    TypeStepReplaced,
		Subject: step,
		Payload: Replaced{Replacement: replacement, Locator: locator, Title: title},
	})
	return replacement
}

// remove tombstones a node.
func (h *harness) remove(node, reason string) Event {
	h.t.Helper()
	return h.commit(Draft{Type: TypeStepRemoved, Subject: node, Payload: Removed{Reason: reason}})[0]
}

// richHistory writes a log that exercises every projection rule this package has,
// and returns the Matter left In Progress.
//
// It is deliberately not a tidy story. A rebuild that reproduces a store built by
// three creates and a finish proves very little; what has to hold is that it
// reproduces one where nodes were reordered and replaced, edges came and went,
// membership was left and rejoined, a cursor was cleared, and a Repo changed its
// natural key — because those are the rules where the fold order and the
// projection's own guards actually interact.
func richHistory(h *harness) string {
	h.t.Helper()

	// --- Tiers: all three, mutated the three ways they can be (D37) ----------
	h.commit(Draft{
		Type:    TypeRepoKeyAdopted,
		Subject: h.Repo,
		Payload: RepoKeyAdopted{RemoteURL: "git@example.com:team/wip.git", IdentityRemote: "origin"},
	})
	h.commit(Draft{Type: TypeCloneRelinked, Subject: h.Clone, Payload: CloneRelinked{GitCommonDir: "/tmp/moved/.git"}})
	h.commit(Draft{Type: TypeCloneLabeled, Subject: h.Clone, Payload: CloneLabeled{Label: "primary"}})
	feature := h.attachWorktree(h.Repo, WorktreeAttached{Clone: h.Clone, Name: "feature"})

	// A second Repo, local-only, with its own Clone and main worktree: two Repos
	// distinguished by identity where one has a remote and one has none.
	second := h.attachRepo(RepoAttached{Label: "second"})
	secondClone := h.attachClone(CloneAttached{Repo: second, GitCommonDir: "/tmp/second/.git", Label: "main"})
	h.attachWorktree(second, WorktreeAttached{Clone: secondClone})

	// --- Matters at every lifecycle state ------------------------------------
	sealed := h.matter("sealed", "A Matter that finished")
	h.start(sealed)
	h.finish(sealed)

	running := h.matter("running", "A Matter in flight")
	h.start(running)

	abandoned := h.matter("abandoned", "A Matter that ended without sealing")
	h.start(abandoned)
	h.cancel(abandoned)

	resting := h.matter("resting", "A Matter set down")
	h.start(resting)
	h.pause(resting)

	planned := h.matter("planned", "A Matter nobody has begun")

	// A Matter in the other Repo, so `nodes.repo` is not constant.
	elsewhere := h.with(Env{Repo: second, Clone: secondClone, Worktree: h.Worktree}).
		matter("elsewhere", "A Matter in the other Repo")

	// --- Stages and Steps, at every state, both under a Stage and not --------
	stage := h.stage(running, "stage-1", "A grouping")
	h.start(stage)
	restingStage := h.stage(running, "stage-2", "A grouping set down")
	h.start(restingStage)
	h.pause(restingStage)

	first := h.step(stage, "step-01", "A finished Step")
	h.start(first)
	h.finish(first)

	resumed := h.step(stage, "step-02", "A Step picked back up")
	h.start(resumed)
	h.pause(resumed)
	h.resume(resumed)

	dropped := h.step(running, "step-03", "A Step that was canceled")
	h.cancel(dropped)

	waiting := h.step(running, "step-04", "A Step still waiting")

	// --- Amendment: insert, reorder, replace, remove -------------------------
	inserted := h.insertStep(stage, "step-05", "A Step that arrived late", 3*sortKeyGap)
	// The whole live sibling set, in its new order.
	h.reorder(stage, inserted, resumed, first)
	// Replaced *after* a reorder, so the replacement inherits a sort key the
	// reorder assigned rather than the one its predecessor was born with — and then
	// reordered again, so the reorder has to name the replacement and not the Step
	// it replaced. That interaction is the one amendment could not express before
	// identity removed the obstruction (D44).
	replacement := h.replace(inserted, "step-05", "The Step that took its place")
	h.reorder(stage, first, replacement, resumed)
	removed := h.insertStep(stage, "step-06", "A Step that was taken out again", 4*sortKeyGap)
	h.remove(removed, "no longer part of the plan")
	// A Step directly under a Matter is amended the same way: `nodes.parent` is a
	// Matter here and a Stage above, and the rule is kind-agnostic.
	h.reorder(running, waiting, dropped)
	h.replace(dropped, "step-03", "The Step that replaced a canceled one")

	// --- Content: a create-once kind, an accumulating one, and a spill -------
	h.write(running, KindBrief, []byte("# The Brief\n\nWhy this Matter exists.\n"))
	h.write(running, KindWorkplan, []byte("# The Workplan\n\nstep-01 .. step-05.\n"))
	h.write(first, KindBody, []byte("What this Step is.\n"))
	h.write(first, KindFindings, []byte("The first finding.\n"))
	h.write(first, KindFindings, []byte("A second finding, appended.\n"))
	// Over SpillThreshold, so the row carries a reference and not bytes. Spilled
	// bytes are the one projection fact not recoverable from the log alone, which
	// makes them the interesting case for a rebuild: what the fold restores is the
	// row, and the row still has to point at the sidecar file.
	h.write(resumed, KindFindings, append([]byte("an ingested log:\n"), make([]byte, SpillThreshold)...))

	// --- Edges, added and removed, within and across Matters (D28, D29) ------
	h.depend(waiting, first)
	transient := h.depend(waiting, resumed)
	h.undepend(waiting, transient, resumed)
	h.depend(planned, sealed)
	h.depend(elsewhere, sealed)

	// --- Gates: declared as config (no event), closed as events --------------
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		h.t.Fatalf("declare the matter-scale gate: %v", err)
	}
	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		h.t.Fatalf("declare the step-scale gate: %v", err)
	}
	h.closeGate(sealed, "reviewed-local", ScaleMatter)
	h.closeGate(first, "verified", ScaleStep)
	h.closeGate(stage, "reviewed-local", ScaleStage)

	// --- Backlog in all three states ----------------------------------------
	entered := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Not acted upon yet"})
	toPlan := h.enterBacklog(BacklogEntered{
		Provenance: ProvenanceFound, Title: "Found mid-flight", Detail: "noticed in step-01", OriginNode: first,
	})
	toDecline := h.enterBacklog(BacklogEntered{
		Provenance: ProvenanceDeferred, Title: "Deferred out of the plan", Detail: "out of scope",
	})
	h.commit(Draft{Type: TypeBacklogPlanned, Subject: toPlan, Payload: BacklogPlanned{Matter: planned}})
	h.commit(Draft{Type: TypeBacklogDeclined, Subject: toDecline, Payload: BacklogDeclined{Reason: "the premise changed"}})
	_ = entered

	// --- Batches, joined and left (D58) -------------------------------------
	named := h.newBatch("release-1")
	h.commit(Draft{Type: TypeBatchJoined, Subject: named, Payload: BatchMembership{Matter: running}})
	h.commit(Draft{Type: TypeBatchJoined, Subject: named, Payload: BatchMembership{Matter: elsewhere}})
	h.commit(Draft{Type: TypeBatchLeft, Subject: named, Payload: BatchMembership{Matter: elsewhere}})
	h.commit(Draft{Type: TypeBatchJoined, Subject: named, Payload: BatchMembership{Matter: elsewhere}})
	anonymous := h.newBatch("")
	h.commit(Draft{Type: TypeBatchJoined, Subject: anonymous, Payload: BatchMembership{Matter: sealed}})
	h.commit(Draft{Type: TypeBatchLeft, Subject: anonymous, Payload: BatchMembership{Matter: sealed}})

	// --- Cursors: one pointing somewhere, one deliberately nowhere (D38) ----
	onFeature := h.with(Env{Repo: h.Repo, Clone: h.Clone, Worktree: feature})
	onFeature.commit(Draft{Type: TypeCursorMoved, Subject: feature, Payload: CursorMoved{Node: waiting}})
	h.commit(Draft{Type: TypeCursorMoved, Subject: h.Worktree, Payload: CursorMoved{Node: first}})
	h.commit(Draft{Type: TypeCursorMoved, Subject: h.Worktree, Payload: CursorMoved{Previous: first}})

	// --- Dispatches, closed for each reason and one left open (D59) ---------
	for _, reason := range []CloseReason{CloseCompleted, CloseSuperseded, CloseReaped} {
		dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
			return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
		})
		h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: reason}})
	}
	onFeature.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID()}, nil
	})

	// --- A reference bound, and a render that projects nothing --------------
	h.commit(Draft{Type: TypeReferenceBound, Subject: waiting, Payload: ReferenceBound{Ref: "GH-17"}})
	h.commit(renderDraft(running))

	_ = replacement
	return running
}

// enterBacklog puts one entry in the backlog and returns it.
func (h *harness) enterBacklog(p BacklogEntered) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBacklogEntered, Subject: tx.NewID(), Payload: p}, nil
	})
}

// newBatch creates a Batch; an empty name is the anonymous one (D23).
func (h *harness) newBatch(name string) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: name}}, nil
	})
}

// ---------------------------------------------------------------------------
// Snapshotting the projection
// ---------------------------------------------------------------------------

// projectionSnapshot is every projection table, every row, every column, as the
// database's own rendering of each value.
//
// It reads the column list out of the database rather than naming columns, which
// is the only version of this assertion worth having: a hand-written list would
// let a column added by a later migration diverge silently, and that column would
// be exactly the kind a rebuild forgets.
type projectionSnapshot map[string][]map[string]string

func (h *harness) snapshotProjection() projectionSnapshot {
	h.t.Helper()
	out := make(projectionSnapshot, len(projectionTables))
	for _, table := range projectionTables {
		out[table] = h.rowsOf(table, "")
	}
	return out
}

// diffProjection reports every place two snapshots disagree.
//
// It is a value rather than a set of assertions so that it can itself be tested:
// TestTheProjectionSnapshotNoticesADifference is the control, and without it the
// rebuild assertions could be passing because the comparison is blind.
func (h *harness) diffProjection(before, after projectionSnapshot) []string {
	h.t.Helper()
	var diffs []string
	for _, table := range projectionTables {
		b, a := before[table], after[table]
		if len(b) != len(a) {
			diffs = append(diffs, fmt.Sprintf("%s holds %d rows, held %d", table, len(a), len(b)))
			continue
		}
		for i := range b {
			for _, col := range h.columnsOf(table) {
				if b[i][col] != a[i][col] {
					diffs = append(diffs, fmt.Sprintf("%s row %d column %s is %s, was %s",
						table, i, col, a[i][col], b[i][col]))
				}
			}
		}
	}
	return diffs
}

// wantSameProjection asserts two snapshots are identical, column for column.
func (h *harness) wantSameProjection(what string, before, after projectionSnapshot) {
	h.t.Helper()
	for _, diff := range h.diffProjection(before, after) {
		h.t.Errorf("%s: %s", what, diff)
	}
}

// wantProjectionIsPopulated is the control under the assertion above: two empty
// snapshots are also identical, so a rebuild test over an empty store proves
// nothing. Every projection table with a P1 verb has to have rows in it.
func (h *harness) wantProjectionIsPopulated(snapshot projectionSnapshot) {
	h.t.Helper()
	for _, table := range projectionTables {
		// Run and OutboxEntry have no P1 event and therefore no projection rule;
		// they are provisioned to exist from event one and nothing more (MODEL §10).
		if table == "runs" || table == "outbox_entries" {
			if len(snapshot[table]) != 0 {
				h.t.Errorf("%s has rows, and no P1 event can have put them there", table)
			}
			continue
		}
		if len(snapshot[table]) == 0 {
			h.t.Errorf("%s is empty, so the rebuild assertion says nothing about it", table)
		}
	}
}

// ---------------------------------------------------------------------------
// 0. The control
// ---------------------------------------------------------------------------

// TestTheProjectionSnapshotNoticesADifference is the control the rebuild
// assertions depend on. They claim a rebuild reproduces the projection column for
// column; that is worth something only if the comparison would have said so
// otherwise — a snapshot that read no columns, or compared nothing, would make
// every one of them pass.
func TestTheProjectionSnapshotNoticesADifference(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("control", "The control")

	before := h.snapshotProjection()
	if diffs := h.diffProjection(before, h.snapshotProjection()); len(diffs) != 0 {
		t.Errorf("two reads of one unchanged store differ: %v", diffs)
	}

	// The smallest divergence there is: one column of one row, moved around the API
	// so no event accounts for it. Two columns move, because a projection row may
	// not move without naming the event that moved it.
	newer := h.commit(renderDraft(matter))[0]
	if err := h.rawExec(`UPDATE nodes SET locator = 'renamed', last_event = ? WHERE id = ?`,
		newer.ID, matter); err != nil {
		t.Fatalf("rename the node: %v", err)
	}
	diffs := h.diffProjection(before, h.snapshotProjection())
	if len(diffs) != 2 {
		t.Errorf("a renamed node produced %d differences (%v), want 2: locator and last_event", len(diffs), diffs)
	}
	for _, diff := range diffs {
		if !strings.HasPrefix(diff, "nodes row") {
			t.Errorf("the difference %q is not attributed to the row that moved", diff)
		}
	}

	// A row that appeared is noticed too, and by row count rather than by column.
	populated := h.snapshotProjection()
	h.matter("another", "Another Matter")
	if diffs := h.diffProjection(populated, h.snapshotProjection()); len(diffs) != 1 {
		t.Errorf("a new node produced %d differences (%v), want 1", len(diffs), diffs)
	}
}

// ---------------------------------------------------------------------------
// 1. A rebuild reproduces the projection
// ---------------------------------------------------------------------------

// TestRebuildReproducesTheProjectionColumnForColumn is the assertion the whole
// shape rests on.
//
// The live write path and the rebuild path call the same applyEvent, so this is
// not a test that two implementations agree — it is a test that the maintained
// projection holds no fact the log does not. If it ever fails, the projection has
// acquired state from somewhere other than an event, which under D61 is the one
// thing that must not be possible.
func TestRebuildReproducesTheProjectionColumnForColumn(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	before := h.snapshotProjection()
	h.wantProjectionIsPopulated(before)

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	after := h.snapshotProjection()
	h.wantSameProjection("after a rebuild", before, after)

	// Twice, because a rebuild has to be idempotent for it to be the recovery
	// operation the shape claims: the second one folds the same log over the
	// projection the first one produced.
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild again: %v", err)
	}
	h.wantSameProjection("after a second rebuild", before, h.snapshotProjection())
}

// TestRebuildRestoresACorruptedProjection is the operation's actual purpose.
//
// Rebuild is the recovery operation the shape buys, and the test above cannot show
// that on its own: a Rebuild that did nothing at all would also reproduce the
// projection column for column. So this one damages the projection first, in the
// one way the guards deliberately cannot prevent — nothing checks that the event a
// row names is an event *about* that row, because amendment is legitimately not
// one-event-one-row — and then asserts the fold puts every column back.
func TestRebuildRestoresACorruptedProjection(t *testing.T) {
	h := newHarness(t)
	running := richHistory(h)
	sound := h.snapshotProjection()

	// A locator and a title rewritten with no event behind them, moved to an event
	// that projects nothing so the log still accounts for the row's last_event being
	// wrong. This is what a bug, or a hand-edited database, leaves behind.
	newer := h.commit(renderDraft(running))[0]
	if err := h.rawExec(
		`UPDATE nodes SET locator = 'corrupted', title = 'corrupted', last_event = ? WHERE id = ?`,
		newer.ID, running); err != nil {
		t.Fatalf("corrupt the projection: %v", err)
	}
	if diffs := h.diffProjection(sound, h.snapshotProjection()); len(diffs) != 3 {
		t.Fatalf("the corruption produced %d differences (%v), want 3", len(diffs), diffs)
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	// Every column is back, including last_event: render.performed projects nothing,
	// so the sound projection is exactly what the log says even after it.
	h.wantSameProjection("after rebuilding a corrupted projection", sound, h.snapshotProjection())
}

// TestRebuildLeavesConfigAndGateDeclarationsAlone is the documented exception.
//
// Declaring a gate is configuration and not something that happened (D4, D54), so
// there is no event to fold and a rebuild that cleared these two tables would
// destroy data the log cannot restore. The seam: config says how the project is
// set up, the log says what happened, a rebuild reconstructs only the latter.
func TestRebuildLeavesConfigAndGateDeclarationsAlone(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	if err := h.SetConfig(h.ctx, h.Repo, "strategy", "trunk"); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := h.SetConfig(h.ctx, h.Repo, "empty-on-purpose", ""); err != nil {
		t.Fatalf("write config: %v", err)
	}
	config := h.rowsOf("config", "")
	declarations := h.rowsOf("gate_declarations", "")
	if len(config) < 2 || len(declarations) < 2 {
		t.Fatalf("the fixture wrote %d config rows and %d declarations; both must be non-trivial",
			len(config), len(declarations))
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	for _, table := range []struct {
		name   string
		before []map[string]string
	}{{"config", config}, {"gate_declarations", declarations}} {
		after := h.rowsOf(table.name, "")
		if len(after) != len(table.before) {
			t.Errorf("%s holds %d rows after a rebuild, held %d", table.name, len(after), len(table.before))
			continue
		}
		for i := range table.before {
			for _, col := range h.columnsOf(table.name) {
				if table.before[i][col] != after[i][col] {
					t.Errorf("%s row %d column %s is %s after a rebuild, was %s",
						table.name, i, col, after[i][col], table.before[i][col])
				}
			}
		}
	}

	// And the declarations still mean what they meant: sealing is computed against
	// them, so a rebuild that dropped one would silently seal a Matter.
	value, present, err := h.Config(h.ctx, h.Repo, "empty-on-purpose")
	if err != nil || !present || value != "" {
		t.Errorf(`config empty-on-purpose = %q (present=%v, err=%v), want "" and present`, value, present, err)
	}
}

// TestRebuildLeavesTheLogAlone is the other direction. A rebuild reads the log and
// writes the projection; nothing in it may touch the source of truth, and the
// append-only triggers are not the only reason — Rebuild stands the *projection's*
// delete guards down for the length of its transaction, and this asserts that
// standing-down does not extend to the events.
func TestRebuildLeavesTheLogAlone(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	before, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(before) < 60 {
		t.Fatalf("the fixture wrote %d events; a rebuild test wants a log worth folding", len(before))
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	after, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("the log holds %d events after a rebuild, held %d", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Type != before[i].Type ||
			string(after[i].Payload) != string(before[i].Payload) {
			t.Errorf("event %d changed: %s (%s), was %s (%s)",
				i, after[i].ID, after[i].Type, before[i].ID, before[i].Type)
		}
	}

	// A rebuild appends nothing either: it is a fold, not a command.
	if h.floor != before[len(before)-1].ID {
		t.Errorf("the identity high-water mark is %s, want the log's last event %s",
			h.floor, before[len(before)-1].ID)
	}
}

// ---------------------------------------------------------------------------
// 2. The projection guard triggers
// ---------------------------------------------------------------------------

// TestEveryProjectionTableCarriesItsGuards is the structural half: the guards are
// generated from three frozen lists, so what has to hold is that every table in
// them really got its triggers — and, more usefully, that a table added to
// projectionTables later without guards fails here.
func TestEveryProjectionTableCarriesItsGuards(t *testing.T) {
	h := newHarness(t)

	want := map[string]string{}
	for _, table := range projectionTables {
		want[table+"_no_delete"] = "only a rebuild may clear " + table
	}
	for _, table := range v1EventLinkedTables {
		want[table+"_advance"] = table + " may only advance to a newer event"
	}
	for _, table := range v1BornTables {
		want[table+"_born_by_event"] = table + " is born by exactly one event"
		want[table+"_immutable_birth"] = table + "'s birth event is immutable"
	}
	want["nodes_immutable_identity"] = "a node's kind, repo, matter and parent are immutable"

	present := map[string]bool{}
	rows, err := h.db.QueryContext(h.ctx, `SELECT name FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		t.Fatalf("read the triggers: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read the triggers: %v", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the triggers: %v", err)
	}

	for trigger, why := range want {
		if !present[trigger] {
			t.Errorf("%s is missing, so nothing enforces that %s", trigger, why)
		}
	}
}

// TestAProjectionRowIsBornByOneEventAndOnlyEverAdvances holds the first two guards
// to the substrate.
//
// A row is born naming exactly one event, and may only ever move to a strictly
// newer one — which is what makes a stale or replayed event unable to move a row,
// on the live path (ids ascend) and on the rebuild path (the fold ascends) alike.
// Both assertions have to go around the API, because applyEvent is built so that
// neither is reachable through it.
func TestAProjectionRowIsBornByOneEventAndOnlyEverAdvances(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("guarded", "A guarded row")
	birth := h.birthEventOf(matter)
	// An event older than the node's birth, and a newer one: both are real events,
	// because last_event is a foreign key and a test about ordering must not be
	// passing on a missing reference.
	older := h.birthEventOf(h.Repo)
	newer := h.commit(renderDraft(matter))[0]

	const nodeInsert = `INSERT INTO nodes
		 (id, kind, repo, matter, parent, locator, title, lifecycle, sort_key, birth_event, last_event)
		 VALUES (?, 'matter', ?, ?, NULL, 'two-births', 'A row born twice', 'planned', 1000, ?, ?)`
	twice := h.NewID()
	rawRefusedBy(h, "a node born by two events", "a row is born by exactly one event",
		nodeInsert, twice, h.Repo, twice, birth.ID, newer.ID)

	rawRefusedBy(h, "a row changing the event that bore it", "birth event is immutable",
		`UPDATE nodes SET birth_event = ?, last_event = ? WHERE id = ?`, newer.ID, newer.ID, matter)

	rawRefusedBy(h, "a row rewritten by the event it already names", "may only advance to a newer event",
		`UPDATE nodes SET title = 'rewritten', last_event = ? WHERE id = ?`, birth.ID, matter)

	rawRefusedBy(h, "a row moved by an older event", "may only advance to a newer event",
		`UPDATE nodes SET title = 'rewritten', last_event = ? WHERE id = ?`, older.ID, matter)

	// The row is exactly as its birth left it: four refusals, no partial writes.
	h.wantRow("after four refused writes", "nodes", "id = ?", []any{matter}, map[string]any{
		"id":              matter,
		"kind":            ScaleMatter,
		"repo":            h.Repo,
		"matter":          matter,
		"parent":          nil,
		"locator":         "guarded",
		"title":           "A guarded row",
		"lifecycle":       Planned,
		"sort_key":        int64(sortKeyGap),
		"external_ref":    nil,
		"birth_event":     birth.ID,
		"last_event":      birth.ID,
		"tombstone_event": nil,
	})

	// A move to a strictly newer event is the legal shape, and it is the only one.
	if err := h.rawExec(`UPDATE nodes SET title = 'renamed', last_event = ? WHERE id = ?`,
		newer.ID, matter); err != nil {
		t.Fatalf("the substrate refused a row advancing to a newer event: %v", err)
	}
}

// TestANodesIdentityIsImmutableButItsLocatorIsNot is the distinction MODEL §10
// rests on. Every prior event that referenced a node relied on its identity, its
// kind and its place in the tree; none of them relied on its locator, which is a
// mutable attribute nothing in the log references. So four columns are frozen and
// the fifth is not, and the trigger is where that asymmetry is stated.
func TestANodesIdentityIsImmutableButItsLocatorIsNot(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("identity", "Identity")
	stage := h.stage(matter, "stage-1", "A grouping")
	step := h.step(stage, "step-01", "A Step")
	other := h.matter("other", "Another Matter")
	elsewhere := h.attachRepo(RepoAttached{Label: "elsewhere"})
	newer := h.commit(renderDraft(step))[0]

	const immutable = "kind, repo, matter and parent are immutable"
	rawRefusedBy(h, "a Step becoming a Stage", immutable,
		`UPDATE nodes SET kind = 'stage', last_event = ? WHERE id = ?`, newer.ID, step)
	rawRefusedBy(h, "a node moving to another Repo", immutable,
		`UPDATE nodes SET repo = ?, last_event = ? WHERE id = ?`, elsewhere, newer.ID, step)
	rawRefusedBy(h, "a node moving to another Matter", immutable,
		`UPDATE nodes SET matter = ?, last_event = ? WHERE id = ?`, other, newer.ID, step)
	rawRefusedBy(h, "a node re-parented", immutable,
		`UPDATE nodes SET parent = ?, last_event = ? WHERE id = ?`, matter, newer.ID, step)
	// COALESCE, so acquiring or losing a parent is caught too and not just changing
	// one.
	rawRefusedBy(h, "a node losing its parent", immutable,
		`UPDATE nodes SET parent = NULL, last_event = ? WHERE id = ?`, newer.ID, step)

	if err := h.rawExec(`UPDATE nodes SET locator = 'step-99', last_event = ? WHERE id = ?`,
		newer.ID, step); err != nil {
		t.Fatalf("the substrate refused a rename, and locators are mutable: %v", err)
	}
	if got := h.rowOf("nodes", "id = ?", step)["locator"]; got != sqlLit(t, "step-99") {
		t.Errorf("the locator is %s after a rename, want 'step-99'", got)
	}
}

// TestAProjectionRowMayNotBeDeletedOutsideARebuild is the third guard, and the one
// that closes the hole the winning spike named as its largest: the log had
// triggers and the projection could not, because Rebuild has to be able to clear
// it. The sentinel is how "only a rebuild may clear it" becomes something the
// database can check — a deletion has to say, in the same transaction, that it is
// a rebuild.
func TestAProjectionRowMayNotBeDeletedOutsideARebuild(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	// Run and OutboxEntry have no P1 verb, so their rows are put in around the API —
	// otherwise a DELETE over an empty table would succeed for want of a row to
	// refuse and the guard would look tested when it was not.
	ev := h.birthEventOf(h.Repo)
	batch := h.newBatch("for-a-run")
	if err := h.rawExec(
		`INSERT INTO runs (id, clone, batch, state, birth_event, last_event) VALUES (?, ?, ?, 'queued', ?, ?)`,
		h.NewID(), h.Clone, batch, ev.ID, ev.ID); err != nil {
		t.Fatalf("seed a Run row: %v", err)
	}
	if err := h.rawExec(
		`INSERT INTO outbox_entries (id, state, subject, idempotency_key, payload, birth_event, last_event)
		 VALUES (?, 'pending', NULL, 'seed', '{}', ?, ?)`,
		h.NewID(), ev.ID, ev.ID); err != nil {
		t.Fatalf("seed an outbox row: %v", err)
	}

	for _, table := range projectionTables {
		if len(h.rowsOf(table, "")) == 0 {
			t.Fatalf("%s is empty, so a DELETE over it would find nothing to refuse", table)
		}
		rawRefusedBy(h, "deleting every row of "+table, "only a rebuild may clear it",
			`DELETE FROM `+table) //nolint:gosec // table names are this package's own constants
	}

	// Nothing was deleted, and the whole projection is still there.
	before := h.snapshotProjection()
	for _, table := range projectionTables {
		if len(before[table]) == 0 {
			t.Errorf("%s is empty after a refused DELETE", table)
		}
	}

	// A rebuild may clear it, because it says so in the same transaction — and the
	// rows with no rule do not come back, which is the honest consequence of being
	// in projectionTables with nothing to project them.
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	for _, table := range []string{"runs", "outbox_entries"} {
		if got := len(h.rowsOf(table, "")); got != 0 {
			t.Errorf("%s holds %d rows after a rebuild, want 0: no event can restore them", table, got)
		}
	}
	// And a single-row delete is refused as flatly as a whole-table one.
	rawRefusedBy(h, "deleting one node", "only a rebuild may clear it",
		`DELETE FROM nodes WHERE id = (SELECT id FROM nodes LIMIT 1)`)
}

// ---------------------------------------------------------------------------
// 3. The projection is total over the taxonomy
// ---------------------------------------------------------------------------

// TestApplyEventIsTotalOverTheRegisteredTaxonomy is what makes a later Matter's
// emitter need no projection work of its own: `schema` owns a rule for every
// registered type, so a type with no rule is a mistake and not a default.
//
// It is asserted structurally rather than against a list. Every registered type is
// pushed through applyEvent inside a transaction that is thrown away; a skeleton
// event fails most rules for its own reasons, and what is checked is only which
// branch of the switch it reached. A type added to the taxonomy without a rule
// therefore fails here on the day it is added, with no list for anybody to forget.
func TestApplyEventIsTotalOverTheRegisteredTaxonomy(t *testing.T) {
	h := newHarness(t)

	const noRule = "no projection rule"
	for _, registered := range registeredTypes() {
		ev := Event{
			ID:         h.NewID(),
			Type:       registered.Type,
			OccurredAt: time.Now().UTC(),
			Actor:      ActorHuman,
			Subject:    h.NewID(),
			Payload:    []byte(`{}`),
		}
		tx, err := h.db.BeginTx(h.ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		err = applyEvent(h.ctx, tx, ev)
		_ = tx.Rollback()
		if err != nil && strings.Contains(err.Error(), noRule) {
			t.Errorf("%s reached applyEvent's default branch: %v", registered.Type, err)
		}
	}

	// The control. Without it every assertion above could be passing because the
	// default branch is unreachable rather than because nothing reaches it.
	tx, err := h.db.BeginTx(h.ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = applyEvent(h.ctx, tx, Event{
		ID: h.NewID(), Type: "invented.type", OccurredAt: time.Now().UTC(),
		Actor: ActorHuman, Subject: h.NewID(), Payload: []byte(`{}`),
	})
	_ = tx.Rollback()
	refusalMentions(t, "an event type nothing projects", err, noRule)
}

// TestRenderPerformedProjectsNothingByDesign makes the one exception explicit
// rather than incidental.
//
// A render is a projection of the store onto disk, and the store never reads it
// back (D36, D40) — so the event exists to be read as history and there is nothing
// to fold. That is a decision, and a decision is worth a test: the assertion is
// not "the rule is missing" but "the whole projection is byte-identical either
// side of one".
func TestRenderPerformedProjectsNothingByDesign(t *testing.T) {
	h := newHarness(t)
	running := richHistory(h)

	before := h.snapshotProjection()
	h.wantProjectionIsPopulated(before)

	rendered := h.commit(Draft{
		Type:    TypeRenderPerformed,
		Subject: running,
		Payload: RenderPerformed{Target: "scratch/wip.md"},
	})[0]

	h.wantSameProjection("after a render", before, h.snapshotProjection())

	// It is in the log all the same: the event happened, and history is what the
	// log is for.
	found := false
	for _, ev := range h.eventsOf(running) {
		if ev.ID == rendered.ID {
			found = true
		}
	}
	if !found {
		t.Error("render.performed projects nothing and was also not written to the log")
	}
}
