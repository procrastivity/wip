package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Tests for MODEL §10 invariant 2 — current state cheaply queryable, never an
// ad-hoc replay (step-08 of this Matter).
//
// D46 resolved event-sourced (D61), so the answer to the founding "what is in
// progress" is a read of the *maintained* `nodes` projection: one indexed SELECT,
// updated in the same transaction as each lifecycle append. Two things therefore
// have to be shown, and they are different things.
//
// **Cheap**: the query is an index seek with no sort step, asserted against the
// real `inProgressSQL` constant and never a copy of it — a test that pasted the
// SQL would pass while the code it claims to be about drifted into a table scan.
//
// **Correct**: the projection is the answer, so the projection's own fidelity is
// all that stands between the store and a wrong one. That is what the fold below
// is for: the log replayed in Go, by hand, and compared to what the table says —
// the replay this shape exists to avoid, run once in a test so that it never has
// to run in a verb.

// wantInProgress asserts exactly what is in progress, in the order the query
// returns it — creation order, which is a column and not a sort.
func (h *harness) wantInProgress(what string, want ...string) {
	h.t.Helper()
	nodes, err := h.InProgress(h.ctx)
	if err != nil {
		h.t.Fatalf("%s: read what is in progress: %v", what, err)
	}
	got := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Lifecycle != InProgress {
			h.t.Errorf("%s: %s is %s and is in the answer anyway", what, n.Locator, n.Lifecycle)
		}
		got = append(got, n.ID)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		h.t.Errorf("%s: in progress: [%s], want [%s]", what,
			strings.Join(h.locators(got), " "), strings.Join(h.locators(want), " "))
	}
}

// queryPlan is SQLite's own plan for a query, one line per step.
func (h *harness) queryPlan(query string) []string {
	h.t.Helper()
	rows, err := h.db.QueryContext(h.ctx, "EXPLAIN QUERY PLAN "+query)
	if err != nil {
		h.t.Fatalf("explain the query plan: %v", err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil {
		h.t.Fatalf("explain the query plan: %v", err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(any)
		}
		if err := rows.Scan(cells...); err != nil {
			h.t.Fatalf("explain the query plan: %v", err)
		}
		detail := *(cells[len(cells)-1].(*any))
		if raw, ok := detail.([]byte); ok {
			out = append(out, string(raw))
			continue
		}
		out = append(out, fmt.Sprint(detail))
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("explain the query plan: %v", err)
	}
	if len(out) == 0 {
		h.t.Fatal("the query has no plan at all")
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. Cheap: the plan
// ---------------------------------------------------------------------------

// TestInProgressIsAnIndexSeek holds invariant 2's "cheaply queryable" to
// something a machine can check.
//
// It asserts the plan of `inProgressSQL` itself — the constant `InProgress` runs
// — because the claim is about the code and not about a string a test wrote to
// resemble it. What must hold: a seek into `nodes_lifecycle` rather than a scan
// of `nodes`; no temporary B-tree, because `birth_event` is the index's second
// term and creation order therefore comes back for free; and no sight of
// `events`, because this answer is never a replay.
func TestInProgressIsAnIndexSeek(t *testing.T) {
	h := newHarness(t)

	// A store with rows in it: SQLite plans against what it knows, and a plan for
	// an empty table is not the plan the founding question will run under.
	for i := range 20 {
		matter := h.matter(fmt.Sprintf("matter-%02d", i), "A Matter")
		step := h.step(matter, "step-01", "A Step")
		if i%2 == 0 {
			h.start(matter)
			h.start(step)
		}
	}
	if _, err := h.db.ExecContext(h.ctx, `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	plan := strings.Join(h.queryPlan(inProgressSQL), "\n")

	if !strings.Contains(plan, "USING INDEX nodes_lifecycle") {
		t.Errorf("the plan does not use nodes_lifecycle:\n%s", plan)
	}
	if !strings.Contains(plan, "SEARCH") {
		t.Errorf("the plan is not a seek:\n%s", plan)
	}
	// A seek, not a sweep: `SEARCH nodes USING INDEX ...` is the wanted line and
	// `SCAN nodes` is the answer that would still be correct and no longer cheap.
	if strings.Contains(plan, "SCAN") {
		t.Errorf("the plan scans a table:\n%s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("the plan sorts in a temp b-tree; creation order is the index's second term:\n%s", plan)
	}
	if strings.Contains(plan, "events") {
		t.Errorf("the plan reads the log; invariant 2 forbids a replay per query:\n%s", plan)
	}

	// The same claim from the other side: the index the plan names is the partial
	// one the schema declares for this, and its second column is what ORDER BY
	// asks for.
	var sql string
	if err := h.db.QueryRowContext(h.ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'nodes_lifecycle'`).Scan(&sql); err != nil {
		t.Fatalf("read the index definition: %v", err)
	}
	if !strings.Contains(sql, "nodes(lifecycle, birth_event)") || !strings.Contains(sql, "WHERE tombstone_event IS NULL") {
		t.Errorf("nodes_lifecycle is %q, which is not the index this query needs", sql)
	}
}

// ---------------------------------------------------------------------------
// 2. Correct: the projection tracks every move, at every scale
// ---------------------------------------------------------------------------

// TestInProgressTracksEveryLifecycleMoveAtEveryScale walks all five moves at each
// scale and asserts membership after every one.
//
// The lifecycle is uniform at every scale (MODEL §2.2), so this is one body run
// three times. Only In Progress is in progress: Planned is not yet, Paused is set
// down rather than under way, and Done and Canceled are over — three different
// ways of not being the answer to the founding question.
func TestInProgressTracksEveryLifecycleMoveAtEveryScale(t *testing.T) {
	for _, scale := range []Scale{ScaleMatter, ScaleStage, ScaleStep} {
		t.Run(string(scale), func(t *testing.T) {
			h := newHarness(t)
			node := h.nodeAt(scale, "subject", "The node under test")

			// nodeAt gives a Stage and a Step a host Matter, which stays Planned
			// throughout: an ancestor is not started by its child's start.
			h.wantInProgress("newly created")
			h.start(node)
			h.wantInProgress("started", node)
			h.pause(node)
			h.wantInProgress("paused")
			h.resume(node)
			h.wantInProgress("resumed", node)
			h.finish(node)
			h.wantInProgress("finished")

			// Canceled is over too, and by a different door.
			canceled := h.nodeAt(scale, "canceled", "A node that was abandoned")
			h.start(canceled)
			h.wantInProgress("a second node started", canceled)
			h.cancel(canceled)
			h.wantInProgress("canceled")

			// A node that was In Progress when it was structurally removed is not
			// in progress: the tombstone is not a lifecycle state (the row still
			// says in-progress), and the partial index leaves it out regardless.
			removed := h.nodeAt(scale, "removed", "A node removed mid-flight")
			h.start(removed)
			h.wantInProgress("a third node started", removed)
			tombstone := h.remove(removed, "no longer part of the plan")
			h.wantInProgress("removed while In Progress")
			row := h.rowOf("nodes", "id = ?", removed)
			if row["lifecycle"] != sqlLit(t, InProgress) {
				t.Errorf("the removed node's lifecycle is %s, want %s — a tombstone is not a state",
					row["lifecycle"], sqlLit(t, InProgress))
			}
			if row["tombstone_event"] != sqlLit(t, tombstone.ID) {
				t.Errorf("the removed node's tombstone_event is %s, want %s",
					row["tombstone_event"], sqlLit(t, tombstone.ID))
			}
		})
	}
}

// TestInProgressIsPerNodeAndNotPerMatter is the case the scale table cannot make:
// the lifecycle is a property of each node and nothing propagates it. A Step is
// in progress under a Matter that is Done, a Matter is in progress with nothing
// under it started, and the answer holds both without preferring either.
func TestInProgressIsPerNodeAndNotPerMatter(t *testing.T) {
	h := newHarness(t)

	done := h.matter("done", "A Matter that was finished with work still under it")
	orphan := h.step(done, "step-01", "A Step still going")
	h.start(done)
	h.start(orphan)
	h.finish(done)
	h.wantInProgress("a Step under a Done Matter", orphan)

	running := h.matter("running", "A Matter in progress with nothing under it started")
	h.step(running, "step-01", "A Step nobody started")
	h.start(running)
	h.wantInProgress("and a Matter whose Steps are all Planned", orphan, running)

	// Creation order, not start order: the answer is ordered by birth_event, so
	// starting the older one last does not move it to the end.
	stage := h.stage(running, "stage-01", "A Stage started after everything else")
	h.start(stage)
	h.wantInProgress("a Stage born last and started last", orphan, running, stage)
	h.pause(running)
	h.resume(running)
	h.wantInProgress("the middle one paused and resumed", orphan, running, stage)
}

// ---------------------------------------------------------------------------
// 3. Never a replay
// ---------------------------------------------------------------------------

// TestInProgressReadsTheProjectionAndNotTheLog shows what "maintained projection"
// buys and what it costs, in one test.
//
// The answer comes from the table, so moving the table around the API moves the
// answer even though not one event says anything of the kind — which is exactly
// what "never an ad-hoc replay" means, and exactly why `Rebuild` is the repair:
// the log is still the source of truth, and folding it again puts the answer back.
func TestInProgressReadsTheProjectionAndNotTheLog(t *testing.T) {
	h := newHarness(t)

	started := h.matter("started", "A Matter that was really started")
	planned := h.matter("planned", "A Matter nobody started")
	h.start(started)
	h.wantInProgress("as the log tells it", started)

	// Around the API: the row moves, the log does not. A projection row may only
	// advance to a strictly newer event, so the vandal has to name one.
	newer := h.newEvent(planned)
	if err := h.rawExec(
		`UPDATE nodes SET lifecycle = 'in-progress', last_event = ? WHERE id = ?`, newer, planned); err != nil {
		t.Fatalf("move the projection around the API: %v", err)
	}
	if err := h.rawExec(
		`UPDATE nodes SET lifecycle = 'planned', last_event = ? WHERE id = ?`, newer, started); err != nil {
		t.Fatalf("move the projection around the API: %v", err)
	}
	h.wantInProgress("with the projection tampered with", planned)

	// Nothing in the log ever said so: the Matter the query now reports has never
	// been the subject of a lifecycle event at all.
	for _, ev := range h.eventsOf(planned) {
		if ev.Type == TypeMatterStarted {
			t.Fatal("the tampered Matter really was started; the test proved nothing")
		}
	}

	// And the fold puts it back, because the log is the source of truth and the
	// projection is only its shadow.
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	h.wantInProgress("after a rebuild", started)
}

// TestARefusedCommandLeavesTheAnswerAlone is the other half of "maintained in
// the same transaction as the append."
//
// The projection is only as good as the atomicity under it: a command whose
// second event is refused must leave neither the event nor the row it would have
// moved, or the answer would report work nobody ever started.
func TestARefusedCommandLeavesTheAnswerAlone(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("atomic", "A Matter started inside a command that was refused")
	before := len(h.eventsOf(matter))

	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{
			h.transitionDraft(matter, ScaleMatter, verbStart, Planned),
			// A second transition out of a state the first one just left: the
			// projection refuses it, and the whole command goes with it.
			h.transitionDraft(matter, ScaleMatter, verbFinish, Planned),
		}, nil
	})
	refusalMentions(t, "a command whose second event could not have happened", err,
		"touched 0 projection rows, want 1")

	h.wantInProgress("after a refused command")
	h.wantLifecycle(matter, Planned)
	if got := len(h.eventsOf(matter)); got != before {
		t.Errorf("the refused command left %d events behind", got-before)
	}
}

// TestInProgressAgreesWithAFoldOfTheLog is the oracle.
//
// The named cases above pin the shapes a reader can picture, one move at a time.
// What they cannot pin is a projection rule that is wrong only in company —
// under a removal, a replacement, a second Repo, a node moved five times. So the
// whole log is folded in Go, by hand, over a few hundred pseudorandom-but-fixed
// histories, and the maintained table has to agree with it at every step.
//
// The fold is deliberately the naive thing: replay everything, keep the last
// state per identity. It is the answer invariant 2 forbids computing per query,
// which is precisely what makes it worth comparing against.
func TestInProgressAgreesWithAFoldOfTheLog(t *testing.T) {
	const moves = 240

	h := newHarness(t)

	// Two Repos, because the answer is store-wide and a per-Repo projection bug
	// would otherwise never show.
	otherRepo := h.attachRepo(RepoAttached{Label: "other"})
	otherClone := h.attachClone(CloneAttached{Repo: otherRepo, GitCommonDir: "/tmp/oracle/.git", Label: "main"})
	other := h.with(Env{
		Repo:     otherRepo,
		Clone:    otherClone,
		Worktree: h.attachWorktree(otherRepo, WorktreeAttached{Clone: otherClone}),
	})

	// Every node keeps the tier context it was born in: a command runs where it
	// runs (D56), and a history that moved another Repo's nodes would be testing
	// the leak instead of the projection.
	var (
		live  []string
		owner []*harness
	)
	for i, on := range []*harness{h, h, other} {
		matter := on.matter(fmt.Sprintf("m%d", i), "A Matter")
		stage := on.stage(matter, fmt.Sprintf("s%d", i), "A Stage")
		for _, node := range []string{
			matter, stage,
			on.step(matter, fmt.Sprintf("step-%02d", 2*i+1), "A Step of the Matter"),
			on.step(stage, fmt.Sprintf("step-%02d", 2*i+2), "A Step of the Stage"),
		} {
			live = append(live, node)
			owner = append(owner, on)
		}
	}

	// A two-line xorshift rather than math/rand, the same choice cycle_test made:
	// this history is the same history in every run of this test, forever.
	state := uint64(20250729)
	next := func(n int) int {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return int(state % uint64(n))
	}

	verbs := []lifecycleVerb{verbStart, verbFinish, verbCancel, verbPause, verbResume}
	births, replacements := 0, 0
	for move := range moves {
		at := next(len(live))
		node, on := live[at], owner[at]
		n, err := h.Node(h.ctx, node)
		if err != nil {
			t.Fatalf("move %d: read %s: %v", move, node, err)
		}
		switch roll := next(12); {
		case roll == 0 && len(live) > 6:
			// Removed: out of the answer whatever state it was in, and out of the
			// pool, since a transition against a tombstone is refused.
			on.remove(node, "removed by the oracle")
			live = append(live[:at], live[at+1:]...)
			owner = append(owner[:at], owner[at+1:]...)
		case roll == 1 && n.Kind != ScaleStep:
			// Born mid-history, under a node whose neighbours have been moving for
			// a while: a new row starts Planned and is nobody's business until
			// something starts it.
			births++
			live = append(live, on.step(node, fmt.Sprintf("step-n%02d", births), "A Step born mid-history"))
			owner = append(owner, on)
		case roll == 2 && n.Kind == ScaleStep:
			// Replaced: the Step is tombstoned and a Planned one is born in its
			// place, so this move both takes a node out of the answer and puts a
			// new identity into the pool.
			replacements++
			live[at] = on.replace(node, fmt.Sprintf("step-r%02d", replacements), "A replacement Step")
		default:
			on.move(node, verbs[next(len(verbs))])
		}

		if move%8 == 0 || move == moves-1 {
			h.wantInProgress(fmt.Sprintf("move %d", move), h.foldInProgress()...)
		}
	}
	if births == 0 || replacements == 0 {
		t.Errorf("the history contained %d mid-history births and %d replacements; the oracle covered less than it claims",
			births, replacements)
	}

	// And the fold and the projection still agree after the projection is thrown
	// away and rebuilt from the same log.
	want := h.foldInProgress()
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	h.wantInProgress("after a rebuild of the whole history", want...)
}

// foldInProgress replays the whole log in Go and returns what is in progress, in
// creation order — the answer computed the way invariant 2 forbids computing it.
//
// It knows nothing about SQL, and it derives its lifecycle event set from
// lifecycleTaxonomy rather than from a list of strings, so an event type added at
// some scale and not another cannot slip past it.
func (h *harness) foldInProgress() []string {
	h.t.Helper()

	lifecycleEvents := map[string]bool{}
	for _, atScale := range lifecycleTaxonomy {
		for _, move := range atScale {
			lifecycleEvents[move.Type] = true
		}
	}

	events, err := h.Events(h.ctx)
	if err != nil {
		h.t.Fatalf("read the whole log: %v", err)
	}
	var (
		lifecycle = map[string]Lifecycle{}
		birth     = map[string]string{}
		removed   = map[string]bool{}
	)
	for _, ev := range events {
		switch {
		case ev.Type == TypeMatterCreated, ev.Type == TypeStageCreated,
			ev.Type == TypeStepCreated, ev.Type == TypeStepInserted:
			lifecycle[ev.Subject] = Planned
			birth[ev.Subject] = ev.ID
		case ev.Type == TypeStepRemoved:
			removed[ev.Subject] = true
		case ev.Type == TypeStepReplaced:
			var p Replaced
			h.decodePayload(ev, &p)
			removed[ev.Subject] = true
			lifecycle[p.Replacement] = Planned
			birth[p.Replacement] = ev.ID
		case lifecycleEvents[ev.Type]:
			var p Transition
			h.decodePayload(ev, &p)
			lifecycle[ev.Subject] = p.To
		}
	}

	var out []string
	for node, state := range lifecycle {
		if state == InProgress && !removed[node] {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return birth[out[i]] < birth[out[j]] })
	return out
}

// decodePayload reads an event's payload into a payload struct, failing the test
// rather than returning an error: a log this package wrote that this package
// cannot read is not a condition a test should carry on past.
func (h *harness) decodePayload(ev Event, into any) {
	h.t.Helper()
	if err := decode(ev, into); err != nil {
		h.t.Fatalf("decode the payload of %s (%s): %v", ev.ID, ev.Type, err)
	}
}
