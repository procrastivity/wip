package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Tests for step-10: the store holds and returns every entity there is, across a
// close and an open; tier dimensions are a static function of event type over the
// *whole* registered taxonomy and not a sample of it; `sort_key` reorders without
// implying execution order (D51); and a command that mutated nothing is not a
// command (MODEL §10 invariant 1, from the side ErrNoEvent guards).
//
// Most of what step-10 names is already held elsewhere, by the Step that built
// the thing: every entity's projection rule column for column in
// entities_test.go, content and its spill in content_test.go, the tombstone in
// edges_test.go, the log as the source of truth in rebuild_test.go. This file is
// deliberately what those do not say, and the two claims it carries alone are the
// ones a per-entity test cannot make:
//
//   - a *round trip*. Every other test in this package asks the store questions
//     through the same handle that wrote the answers. Nothing yet has closed the
//     database and opened it again, which is the only thing wip actually does
//     between one command and the next — every invocation is a fresh process.
//   - the dimension rule over the register itself, so a type added in a later
//     phase is covered on the day it is added and this test cannot go stale.

// ---------------------------------------------------------------------------
// Asking the store everything it can answer
// ---------------------------------------------------------------------------

// idsIn reads one column as raw values, rather than the quoted literals rowsOf
// renders — these are the identities the read surface is then asked about.
func (h *harness) idsIn(query string, args ...any) []string {
	h.t.Helper()
	rows, err := h.db.QueryContext(h.ctx, query, args...)
	if err != nil {
		h.t.Fatalf("read identities: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			h.t.Fatalf("read identities: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read identities: %v", err)
	}
	return out
}

// readBack is everything the read surface says about a store, keyed by the
// question and rendered as a string.
//
// It is a different claim from the projection snapshot and a stronger one for
// this Step. The snapshot says the *tables* are the same; this says the
// *answers* are, through the exported API a caller actually has — including the
// content whose bytes live in a sidecar file beside the database, which a store
// that came back without its blob directory would hold an identical `content`
// table for and answer wrongly.
//
// Errors are recorded rather than failed on, because several of these questions
// are supposed to have no answer: a tombstoned node is not a live node, and
// "still refuses in exactly the same words" is part of coming back unchanged.
func readBack(h *harness) map[string]string {
	h.t.Helper()
	out := map[string]string{}
	say := func(question string, answer any, err error) {
		if err != nil {
			out[question] = "refused: " + err.Error()
			return
		}
		out[question] = fmt.Sprintf("%+v", answer)
	}

	for _, id := range h.idsIn(`SELECT id FROM nodes ORDER BY id`) {
		node, err := h.Node(h.ctx, id)
		say("node/"+id, node, err)
		tombstoned, err := h.Tombstoned(h.ctx, id)
		say("tombstoned/"+id, tombstoned, err)
		children, err := h.Children(h.ctx, id)
		say("children/"+id, children, err)
		blockers, err := h.BlockedBy(h.ctx, id)
		say("blocked-by/"+id, blockers, err)
		gates, err := h.ClosedGates(h.ctx, id)
		say("gates/"+id, gates, err)
	}

	for _, id := range h.idsIn(`SELECT id FROM nodes WHERE kind = 'matter' ORDER BY id`) {
		nodes, err := h.MatterNodes(h.ctx, id)
		say("matter-nodes/"+id, nodes, err)
		locator, err := h.NextStepLocator(h.ctx, id)
		say("next-locator/"+id, locator, err)
	}

	for _, id := range h.idsIn(`SELECT id FROM repos ORDER BY id`) {
		repo, err := h.Store.Repo(h.ctx, id)
		say("repo/"+id, repo, err)
		matters, err := h.Matters(h.ctx, id)
		say("matters/"+id, matters, err)
		archived, err := h.ArchivedMatters(h.ctx, id)
		say("archive/"+id, archived, err)
		backlog, err := h.Backlog(h.ctx, id)
		say("backlog/"+id, backlog, err)
		declarations, err := h.GateDeclarations(h.ctx, id)
		say("declarations/"+id, declarations, err)
	}

	for _, id := range h.idsIn(`SELECT id FROM clones ORDER BY id`) {
		clone, err := h.Store.Clone(h.ctx, id)
		say("clone/"+id, clone, err)
	}
	for _, id := range h.idsIn(`SELECT id FROM worktrees ORDER BY id`) {
		worktree, err := h.Store.Worktree(h.ctx, id)
		say("worktree/"+id, worktree, err)
		dispatch, open, err := h.OpenDispatch(h.ctx, id)
		say("open-dispatch/"+id, fmt.Sprintf("%+v open=%v", dispatch, open), err)
	}
	for _, id := range h.idsIn(`SELECT id FROM batches ORDER BY id`) {
		members, err := h.BatchMembers(h.ctx, id)
		say("batch-members/"+id, members, err)
	}
	for _, id := range h.idsIn(`SELECT id FROM dispatches ORDER BY id`) {
		dispatch, err := h.Dispatch(h.ctx, id)
		say("dispatch/"+id, dispatch, err)
	}

	// The pairs. A cursor keys at Clone + Worktree (D38), content at node + kind,
	// config at Repo + key, so each is read back by the pair it is keyed on.
	for _, pair := range h.idsIn(`SELECT clone || '|' || worktree FROM cursors ORDER BY 1`) {
		clone, worktree, _ := strings.Cut(pair, "|")
		node, present, err := h.Cursor(h.ctx, clone, worktree)
		say("cursor/"+pair, fmt.Sprintf("%s present=%v", node, present), err)
	}
	for _, pair := range h.idsIn(
		`SELECT DISTINCT node || '|' || kind FROM content WHERE tombstone_event IS NULL ORDER BY 1`) {
		node, kind, _ := strings.Cut(pair, "|")
		data, err := h.Content(h.ctx, node, ContentKind(kind))
		say("content/"+pair, fmt.Sprintf("%d bytes sha256=%x", len(data), sha256.Sum256(data)), err)
		segments, err := h.ContentSegments(h.ctx, node, ContentKind(kind))
		say("segments/"+pair, segments, err)
	}
	for _, pair := range h.idsIn(`SELECT repo || '|' || key FROM config ORDER BY 1`) {
		repo, key, _ := strings.Cut(pair, "|")
		value, present, err := h.Config(h.ctx, repo, key)
		say("config/"+pair, fmt.Sprintf("%q present=%v", value, present), err)
	}

	edges, err := h.LiveEdges(h.ctx)
	say("live-edges", edges, err)
	cycles, err := h.Cycles(h.ctx)
	say("cycles", cycles, err)
	inProgress, err := h.InProgress(h.ctx)
	say("in-progress", inProgress, err)

	return out
}

// diffAnswers reports every question two reads of a store answer differently.
func diffAnswers(before, after map[string]string) []string {
	var diffs []string
	for question, was := range before {
		got, asked := after[question]
		switch {
		case !asked:
			diffs = append(diffs, fmt.Sprintf("%s is no longer answered at all", question))
		case got != was:
			diffs = append(diffs, fmt.Sprintf("%s is %s, was %s", question, got, was))
		}
	}
	for question := range after {
		if _, ok := before[question]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s is answered now and was not before", question))
		}
	}
	return diffs
}

// wantSameAnswers asserts two reads of a store agree question for question.
func wantSameAnswers(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for _, diff := range diffAnswers(before, after) {
		t.Errorf("%s: %s", what, diff)
	}
}

// ---------------------------------------------------------------------------
// 1. The round trip
// ---------------------------------------------------------------------------

// TestTheStoreComesBackExactlyAsItWasLeft is the round trip, and it is the one
// thing nothing else in this package does: every other test asks the store
// questions through the same handle that wrote the answers, and wip never does
// that — each invocation is a fresh process opening a file somebody else wrote.
//
// Four claims, and each of them fails a different way. The schema is what a v1
// store's schema is. The log is byte for byte what was appended. Every answer the
// read surface gives is the answer it gave before, including the ones that
// resolve bytes from a sidecar file and the ones that are refusals. And the
// identity high-water mark came back off disk, so the first id the new process
// mints is above every id the last one wrote — which is the single fact the whole
// design rests on, an event's identity doubling as the total order (D44, D51).
func TestTheStoreComesBackExactlyAsItWasLeft(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	before := h.snapshotProjection()
	h.wantProjectionIsPopulated(before)
	log := h.rowsOf("events", "")
	shape := schemaShape(t, h.Store)
	answers := readBack(h)
	if len(answers) < 100 {
		t.Fatalf("the read surface was asked %d questions; a round-trip test wants the whole store", len(answers))
	}
	floor := h.floor

	reopened, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen the store: %v", err)
	}

	wantSameSchema(t, "across a close and an open", schemaShape(t, reopened.Store), shape)
	wantSameRows(t, "the log across a close and an open", log, reopened.rowsOf("events", ""))
	reopened.wantSameProjection("across a close and an open", before, reopened.snapshotProjection())
	wantSameAnswers(t, "across a close and an open", answers, readBack(reopened))

	// Identity survives the process boundary. The id source is monotonic within
	// one process and knows nothing about the last one, so this is the assertion
	// behind loadFloor: without it a reopen inside the same millisecond could mint
	// an id below the last one on disk.
	if reopened.floor != floor {
		t.Errorf("the reopened store's high-water mark is %s, want %s", reopened.floor, floor)
	}
	if next := reopened.NewID(); next <= floor {
		t.Errorf("the reopened store minted %s, which does not sort above the log's last id %s", next, floor)
	}

	// And the answers are a function of the log alone: clear the whole projection
	// and fold it again, and the store says exactly the same things. This is the
	// source-of-truth claim stated in terms of answers rather than of tables (D61).
	if err := reopened.Rebuild(reopened.ctx); err != nil {
		t.Fatalf("rebuild the reopened store: %v", err)
	}
	wantSameAnswers(t, "after refolding the reopened store", answers, readBack(reopened))

	// The control. Every assertion above claims two reads agree; that is worth
	// something only if a read that should disagree does. A locator is the right
	// thing to move, because it is the one attribute of a node that is mutable and
	// the one nothing in the log references (MODEL §10).
	matter := reopened.idsIn(`SELECT id FROM nodes WHERE kind = 'matter' ORDER BY id LIMIT 1`)[0]
	newer := reopened.commit(renderDraft(matter))[0]
	if err := reopened.rawExec(`UPDATE nodes SET locator = 'renamed', last_event = ? WHERE id = ?`,
		newer.ID, matter); err != nil {
		t.Fatalf("rename a node: %v", err)
	}
	if diffs := diffAnswers(answers, readBack(reopened)); len(diffs) == 0 {
		t.Error("a renamed node changed none of the store's answers; the comparison above is blind")
	}
}

// ---------------------------------------------------------------------------
// 2. Tier dimensions over the whole register (D56)
// ---------------------------------------------------------------------------

// executionFamilies is D56's execution set, restated from CONTRACT §A rather
// than read back out of taxonomy.go: clone and worktree are carried by exactly
// the families that describe an execution, and by nothing else.
//
// It is written out here on purpose. A test that asked `taxonomy.go` which types
// are execution types and then checked they carry execution dimensions would be
// asking one expression of the rule to agree with itself. Run has no P1 event —
// the row exists from P1 and the verb arrives in P2 (MODEL §10) — so it is named
// here and contributes nothing yet, which is the honest way to record that.
var executionFamilies = map[Family]bool{
	FamilyCursor:   true,
	FamilyBatch:    true,
	FamilyDispatch: true,
	FamilyRun:      true,
	FamilyRender:   true,
}

// TestEveryRegisteredTypeCarriesExactlyTheDimensionsItsRuleNames is D56 as a
// total function, over the register itself.
//
// The envelope tests already take this three ways — a durable event, an execution
// event and a `batch.*` one — and three samples is what a sample is: the type
// that gets its dimensions wrong will be the forty-third, added by a later phase,
// and no sample-based test would notice. This one enumerates
// `registeredTypes()`, so a type added to the taxonomy is covered on the day it
// is added, and asserts four things about each: the rule matches D56 as stated in
// the contract, the database's own copy of the rule matches the binary's, `stamp`
// produces exactly those columns and refuses a tier context that cannot supply
// one of them, and the substrate refuses a row disagreeing with the rule in any
// of the three positions.
func TestEveryRegisteredTypeCarriesExactlyTheDimensionsItsRuleNames(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("dimensions", "Dimensions are a static function of type")
	full := Env{Repo: h.Repo, Clone: h.Clone, Worktree: h.Worktree}

	var durables, executions, batchScoped int
	for _, registered := range registeredTypes() {
		// --- the rule is D56's rule, restated -------------------------------
		// Repo on everything but `batch.*`, because a Batch keys at no tier
		// (D39, D56); clone and worktree on exactly the execution families.
		wantRepo := !strings.HasPrefix(registered.Type, "batch.")
		wantExecution := executionFamilies[registered.Family]
		if registered.Repo != wantRepo {
			t.Errorf("%s requires repo=%v, and D56 says %v", registered.Type, registered.Repo, wantRepo)
		}
		if registered.Clone != wantExecution || registered.Worktree != wantExecution {
			t.Errorf("%s requires clone=%v worktree=%v, and D56 says %v for both",
				registered.Type, registered.Clone, registered.Worktree, wantExecution)
		}
		switch {
		case !wantRepo:
			batchScoped++
		case wantExecution:
			executions++
		default:
			durables++
		}

		// --- the binary's rule and the database's are the same rule ---------
		rule, err := requiredDimensions(registered.Type)
		if err != nil {
			t.Errorf("%s is registered and requiredDimensions does not know it: %v", registered.Type, err)
			continue
		}
		h.wantRow("the taxonomy row for "+registered.Type, "event_types", "type = ?",
			[]any{registered.Type}, map[string]any{
				"type":              registered.Type,
				"family":            registered.Family,
				"requires_repo":     boolToInt(rule.Repo),
				"requires_clone":    boolToInt(rule.Clone),
				"requires_worktree": boolToInt(rule.Worktree),
			})

		// --- stamp carries exactly those, and picks up nothing else ---------
		// The tier context here has all three; a durable event still comes out
		// with a null clone, because the question is asked of the type and never
		// of what happened to be available.
		draft := Draft{Type: registered.Type, Subject: matter}
		ev, err := h.stamp(Request{Actor: ActorHuman, Env: full}, draft)
		if err != nil {
			t.Errorf("stamp %s in a full tier context: %v", registered.Type, err)
			continue
		}
		for _, dimension := range []struct {
			name     string
			got      string
			required bool
			want     string
		}{
			{"repo", ev.Repo, rule.Repo, h.Repo},
			{"clone", ev.Clone, rule.Clone, h.Clone},
			{"worktree", ev.Worktree, rule.Worktree, h.Worktree},
		} {
			want := ""
			if dimension.required {
				want = dimension.want
			}
			if dimension.got != want {
				t.Errorf("%s stamped %s = %q, want %q", registered.Type, dimension.name, dimension.got, want)
			}
		}

		// --- and refuses a tier context that cannot supply a required one ----
		for _, missing := range []struct {
			name     string
			required bool
			env      Env
			refusal  string
		}{
			{"Repo", rule.Repo, Env{Clone: full.Clone, Worktree: full.Worktree}, "requires a repo dimension"},
			{"Clone", rule.Clone, Env{Repo: full.Repo, Worktree: full.Worktree}, "no Clone"},
			{"Worktree", rule.Worktree, Env{Repo: full.Repo, Clone: full.Clone}, "no Worktree"},
		} {
			if !missing.required {
				continue
			}
			_, err := h.stamp(Request{Actor: ActorHuman, Env: missing.env}, draft)
			refusalMentions(t, fmt.Sprintf("%s with no %s in its tier context", registered.Type, missing.name),
				err, missing.refusal)
		}

		// --- the substrate refuses every disagreement, in all three positions -
		// The control first: the row the rule describes is one the database has no
		// reason to turn down, so each refusal below is attributable to the one
		// dimension it flipped and not to some other constraint.
		if err := h.rawRowFrom(ev).insert(h); err != nil {
			t.Errorf("the substrate refused a well-formed %s row: %v", registered.Type, err)
			continue
		}
		for _, position := range []struct {
			name    string
			present bool
			set     func(*rawRow)
		}{
			{"repo", rule.Repo, func(r *rawRow) { r.repo = flipDimension(rule.Repo, h.Repo) }},
			{"clone", rule.Clone, func(r *rawRow) { r.clone = flipDimension(rule.Clone, h.Clone) }},
			{"worktree", rule.Worktree, func(r *rawRow) { r.worktree = flipDimension(rule.Worktree, h.Worktree) }},
		} {
			mutant := h.rawRowFrom(ev)
			position.set(&mutant)
			had := "carrying a"
			if position.present {
				had = "with no"
			}
			mutant.refused(h, fmt.Sprintf("%s %s %s", registered.Type, had, position.name),
				"tier dimensions must match")
		}
	}

	// The loop is not vacuous, and it covers all three shapes the rule has.
	if durables == 0 || executions == 0 || batchScoped == 0 {
		t.Errorf("the register holds %d durable, %d execution and %d batch-scoped types; D56 has three shapes",
			durables, executions, batchScoped)
	}
	if got := durables + executions + batchScoped; got != len(registeredTypes()) {
		t.Errorf("%d types were classified out of %d registered", got, len(registeredTypes()))
	}
}

// flipDimension is the value a dimension takes when it disagrees with its type's
// rule: absent where the rule requires it, present where the rule forbids it.
func flipDimension(required bool, id string) any {
	if required {
		return nil
	}
	return id
}

// ---------------------------------------------------------------------------
// 3. sort_key is presentation and never sequence (D51)
// ---------------------------------------------------------------------------

// wantChildOrder asserts the order Children returns a parent's live children in.
func wantChildOrder(h *harness, what, parent string, want ...string) {
	h.t.Helper()
	children, err := h.Children(h.ctx, parent)
	if err != nil {
		h.t.Fatalf("%s: read the children of %s: %v", what, h.locatorOf(parent), err)
	}
	got := make([]string, 0, len(children))
	for _, c := range children {
		got = append(got, c.ID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Errorf("%s: %s reads as %v, want %v", what, h.locatorOf(parent), h.locators(got), h.locators(want))
	}
}

// whatMeansSomething is every answer this store gives that a sort key is not
// allowed to be part of: the dependency graph, the loops in it, the lifecycle of
// every node, and what is in progress.
//
// It reads nodes by identity rather than as whole rows on purpose. A Node carries
// its own SortKey, so a struct-level comparison would report a reorder as a
// change to what is in progress — which is the confusion D51 exists to prevent,
// restated as a test that could not tell the difference.
func whatMeansSomething(h *harness) map[string]string {
	h.t.Helper()
	out := map[string]string{}

	for _, id := range h.idsIn(`SELECT id FROM nodes ORDER BY id`) {
		node, err := h.Node(h.ctx, id)
		if err != nil {
			out["lifecycle/"+id] = "refused: " + err.Error()
		} else {
			out["lifecycle/"+id] = string(node.Lifecycle)
		}
		blockers, err := h.BlockedBy(h.ctx, id)
		if err != nil {
			h.t.Fatalf("read what %s waits for: %v", id, err)
		}
		out["blocked-by/"+id] = strings.Join(h.edgePicture(blockers), " ")
	}

	edges, err := h.LiveEdges(h.ctx)
	if err != nil {
		h.t.Fatalf("read the live edge set: %v", err)
	}
	out["live-edges"] = strings.Join(h.edgePicture(edges), " ")

	cycles, err := h.Cycles(h.ctx)
	if err != nil {
		h.t.Fatalf("read the loops: %v", err)
	}
	out["cycles"] = fmt.Sprint(cycles)

	inProgress, err := h.InProgress(h.ctx)
	if err != nil {
		h.t.Fatalf("read what is in progress: %v", err)
	}
	ids := make([]string, 0, len(inProgress))
	for _, n := range inProgress {
		ids = append(ids, n.ID)
	}
	out["in-progress"] = strings.Join(ids, " ")

	return out
}

// TestSortKeyReordersWithoutImplyingExecutionOrder is D51, and the whole of it is
// what a reorder does *not* touch.
//
// The Steps are laid out so that the presentation order and the dependency run
// opposite ways: the Step shown first is the one waiting on the Step shown last.
// Nothing can be reading the sort key as a sequence and getting the right answer
// by accident, and a reorder that reversed the page could not quietly reverse the
// work.
//
// The precise claim: the only columns in the entire projection a reorder moves
// are `nodes.sort_key` and the `nodes.last_event` that has to account for it.
// Ordering that means something comes from `blocked-by` edges and from nowhere
// else, so every one of those rows is untouched — and the sort keys themselves
// come out as dense multiples of the gap in the order the event named, because
// magnitude carries nothing.
func TestSortKeyReordersWithoutImplyingExecutionOrder(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("ordering", "Presentation order is not execution order")
	h.start(matter)

	first := h.step(matter, "step-01", "First on the page, last in the work")
	second := h.step(matter, "step-02", "Second")
	third := h.step(matter, "step-03", "Third")
	fourth := h.step(matter, "step-04", "Last on the page, first in the work")

	// The dependencies run against the page order on purpose.
	h.depend(first, fourth)
	h.depend(second, third)
	h.start(fourth)

	wantChildOrder(h, "as created", matter, first, second, third, fourth)
	for i, node := range []string{first, second, third, fourth} {
		n, err := h.Node(h.ctx, node)
		if err != nil {
			t.Fatalf("read %s: %v", node, err)
		}
		if want := int64(i+1) * sortKeyGap; n.SortKey != want {
			t.Errorf("%s was born with sort key %d, want %d", n.Locator, n.SortKey, want)
		}
	}

	before := h.snapshotProjection()
	meaning := whatMeansSomething(h)

	h.reorder(matter, fourth, third, second, first)

	// The page reversed.
	wantChildOrder(h, "after a reorder", matter, fourth, third, second, first)
	for i, node := range []string{fourth, third, second, first} {
		n, err := h.Node(h.ctx, node)
		if err != nil {
			t.Fatalf("read %s: %v", node, err)
		}
		// Dense multiples of the gap, assigned by position: no Step keeps the
		// number it was born with, and nothing may read one as a sequence number.
		if want := int64(i+1) * sortKeyGap; n.SortKey != want {
			t.Errorf("%s sorts at %d after a reorder, want %d", n.Locator, n.SortKey, want)
		}
	}

	// Nothing else in the whole projection moved. Four rows, two columns each: the
	// sort key, and the last_event that has to account for it moving.
	diffs := h.diffProjection(before, h.snapshotProjection())
	if len(diffs) != 8 {
		t.Errorf("a reorder produced %d differences (%v), want 8: four sort keys and four last_events", len(diffs), diffs)
	}
	for _, diff := range diffs {
		if !strings.HasPrefix(diff, "nodes row") ||
			(!strings.Contains(diff, "column sort_key") && !strings.Contains(diff, "column last_event")) {
			t.Errorf("a reorder moved %s, and a sort key is presentation (D51)", diff)
		}
	}

	// And every answer that means anything is the answer it was — the dependency
	// graph, what is in progress, the lifecycle, what loops the store has.
	wantSameAnswers(t, "a reorder changed something that is not presentation",
		meaning, whatMeansSomething(h))
	h.wantBlockers("after a reorder", first, "step-04")
	h.wantBlockers("after a reorder", second, "step-03")
	h.wantLifecycle(fourth, InProgress)
	h.wantLifecycle(first, Planned)

	// A Step added afterwards sorts after every live sibling and nowhere else: it
	// arrives last on the page while waiting for nothing at all, which is the
	// clearest statement there is that the two orders are unrelated.
	fifth := h.step(matter, "step-05", "Added after the reorder")
	wantChildOrder(h, "after a late arrival", matter, fourth, third, second, first, fifth)
	h.wantBlockers("a late arrival waits for nothing", fifth)
}

// ---------------------------------------------------------------------------
// 4. Invariant 1, from the side ErrNoEvent guards
// ---------------------------------------------------------------------------

// TestACommandThatProducedNoEventIsNotACommit is MODEL §10 invariant 1 from the
// other end.
//
// The projection guards say a row cannot move without an event. This says the
// converse: a command that decided nothing happened does not quietly succeed,
// because a caller that got a nil error from a command which appended nothing
// would have no way to tell "done" from "did nothing" — and under D61 a mutation
// with no event is a mutation the log cannot narrate and a rebuild would lose.
func TestACommandThatProducedNoEventIsNotACommit(t *testing.T) {
	h := newHarness(t)
	richHistory(h)

	before := h.snapshotProjection()
	log := h.rowsOf("events", "")

	// A decide function that read the store and concluded nothing happened.
	_, err := h.Commit(h.ctx, h.req(), func(ctx context.Context, tx *Tx) ([]Draft, error) {
		if _, err := tx.InProgress(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if !errors.Is(err, ErrNoEvent) {
		t.Errorf("a command that produced no draft returned %v, want ErrNoEvent", err)
	}

	// An empty slice is the same claim as a nil one, and must not be the shape
	// that slips through.
	_, err = h.Commit(h.ctx, h.req(), func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{}, nil
	})
	if !errors.Is(err, ErrNoEvent) {
		t.Errorf("a command that produced an empty draft slice returned %v, want ErrNoEvent", err)
	}

	// A verb that refused outright keeps its own reason, rather than having it
	// replaced by the store's.
	refused := errors.New("test: the verb turned it down")
	_, err = h.Commit(h.ctx, h.req(), func(context.Context, *Tx) ([]Draft, error) {
		return nil, refused
	})
	if !errors.Is(err, refused) {
		t.Errorf("a command whose verb refused returned %v, want the verb's own error", err)
	}

	// Three commands, no events, nothing moved.
	wantSameRows(t, "the log after three commands that wrote nothing", log, h.rowsOf("events", ""))
	h.wantSameProjection("after three commands that wrote nothing", before, h.snapshotProjection())

	// The other direction, so the assertion is about the boundary and not about
	// commands failing: a command that did decide something wrote exactly one
	// event per decision, and the events it handed back are the events in the log.
	matter := h.idsIn(`SELECT id FROM nodes WHERE kind = 'matter' ORDER BY id LIMIT 1`)[0]
	written := h.commit(renderDraft(matter), renderDraft(matter), renderDraft(matter))
	if len(written) != 3 {
		t.Fatalf("a command with three drafts returned %d events", len(written))
	}
	grown := h.rowsOf("events", "")
	if len(grown) != len(log)+3 {
		t.Errorf("the log holds %d events after three drafts, want %d", len(grown), len(log)+3)
	}
	for _, ev := range written {
		found := false
		for _, row := range grown {
			if row["id"] == sqlLit(t, ev.ID) {
				found = true
				if row["type"] != sqlLit(t, ev.Type) || row["subject"] != sqlLit(t, ev.Subject) {
					t.Errorf("event %s is %s about %s in the log, and %s about %s in the return",
						ev.ID, row["type"], row["subject"], ev.Type, ev.Subject)
				}
			}
		}
		if !found {
			t.Errorf("Commit returned event %s, which is not in the log", ev.ID)
		}
	}
}
