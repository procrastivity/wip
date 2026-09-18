package readsurface

// Tests for step-04: `next` resolving each of vocabulary's five drafted
// output shapes — including the no-cursor and everything-sealed cases — plus
// D67's choose-next extension, and the invariant that a read verb repairs
// nothing (an ended cursor is reported, never moved).

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestNext_BareMatter is vocabulary output 1: the cursor sits on a Matter
// that is its own smallest node (D2).
func TestNext_BareMatter(t *testing.T) {
	f := newFixture(t)
	m := f.matter("fix-flaky-clone-detect", "Fix the flaky clone-detect test")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}

	view := nextFor(t, f, cur)
	if view.Kind != BareMatter {
		t.Fatalf("Kind = %v, want BareMatter", view.Kind)
	}
	if view.Node.ID != m || view.Address != "fix-flaky-clone-detect" {
		t.Errorf("Node/Address = %s/%q, want %s/fix-flaky-clone-detect", view.Node.ID, view.Address, m)
	}
}

// TestNext_Positioned is vocabulary output 2: the cursor sits on a Step
// mid-Stage, with its Stage position and blocked-by state reported.
func TestNext_Positioned(t *testing.T) {
	f := newFixture(t)
	m := f.matter("tiers", "Tiers")
	stage := f.stage(m, "tier-verbs", "tier-verbs")
	step1 := f.step(stage, "step-01", "First")
	step2 := f.step(stage, "step-02", "Second")
	_ = f.step(stage, "step-03", "Third")
	f.depend(step2, step1)
	f.start(m)
	f.start(stage)
	f.start(step1)
	f.finish(step1)
	f.start(step2)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step2); err != nil {
		t.Fatal(err)
	}
	view := nextFor(t, f, cur)
	if view.Kind != Positioned {
		t.Fatalf("Kind = %v, want Positioned", view.Kind)
	}
	if view.Address != "tiers/tier-verbs · step-02" {
		t.Errorf("Address = %q, want %q", view.Address, "tiers/tier-verbs · step-02")
	}
	if view.Stage == nil || view.Stage.StageLocator != "tier-verbs" || view.Stage.Index != 2 || view.Stage.Total != 3 {
		t.Errorf("Stage = %+v, want tier-verbs, 2 of 3", view.Stage)
	}
	if len(view.Unmet) != 0 || len(view.Met) != 1 || view.Met[0].ID != step1 {
		t.Errorf("Unmet/Met = %+v/%+v, want none unmet and step-01 met", view.Unmet, view.Met)
	}
}

func TestNext_PositionedDoneUnsealedCarriesPendingGates(t *testing.T) {
	f := newFixture(t)
	f.declareGate("approved", store.ScaleMatter)
	f.declareGate("verified", store.ScaleStep)
	m := f.matter("checkout", "Checkout")
	step := f.step(m, "step-01", "Build")
	f.start(m)
	f.start(step)
	f.finish(step)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step); err != nil {
		t.Fatal(err)
	}

	view := nextFor(t, f, cur)
	if len(view.Pending) != 2 || view.Pending[0].Gate != "verified" || view.Pending[1].Gate != "approved" {
		t.Fatalf("Pending = %+v, want verified then approved", view.Pending)
	}
	if view.Pending[0].Relationship != store.GateOwn || view.Pending[1].Relationship != store.GateEnclosing {
		t.Errorf("Pending relationships = %+v, want own then enclosing", view.Pending)
	}
}

func TestNext_BareMatterDoneUnsealedCarriesPendingGates(t *testing.T) {
	f := newFixture(t)
	f.declareGate("approved", store.ScaleMatter)
	m := f.matter("checkout", "Checkout")
	f.start(m)
	f.finish(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}

	view := nextFor(t, f, cur)
	if view.Kind != BareMatter {
		t.Fatalf("Kind = %v, want BareMatter", view.Kind)
	}
	if len(view.Pending) != 1 || view.Pending[0].Gate != "approved" || view.Pending[0].Relationship != store.GateOwn {
		t.Fatalf("Pending = %+v, want the own approved gate", view.Pending)
	}

	f.closeGate(m, "approved", store.ScaleMatter)
	view = nextFor(t, f, cur)
	if view.Kind != ChooseNext || view.EndedReason != "sealed" || len(view.Pending) != 0 {
		t.Fatalf("sealed BareMatter view = %+v, want ChooseNext with no pending gates", view)
	}
}

func TestNext_DismissedDoneMatterIsEndedNotAwaitingAGate(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("dismissed", "Dismissed")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	f.finish(m)
	f.dismissGate(m, "reviewed-local", store.ScaleMatter, "the verifier is unavailable")

	before := countCursorMoves(t, f)
	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext || view.EndedReason != "sealed" || len(view.Pending) != 0 {
		t.Fatalf("dismissed Matter view = %+v, want sealed ChooseNext with no pending gate", view)
	}
	after := countCursorMoves(t, f)
	if after != before {
		t.Fatalf("next changed cursor state from %d to %d; reads must not write", before, after)
	}
}

// TestNext_NothingUnblocked is vocabulary output 3: no cursor, and every
// Planned node in scope is still waiting on something.
func TestNext_NothingUnblocked(t *testing.T) {
	f := newFixture(t)
	first := f.matter("write-surface", "write-surface")
	second := f.matter("render-scratch", "render-scratch")
	f.depend(second, first)
	f.start(first) // first is In Progress, not locally complete: still blocks

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != NothingUnblocked {
		t.Fatalf("Kind = %v, want NothingUnblocked", view.Kind)
	}
	if len(view.Blocked) != 1 || view.Blocked[0].Node.ID != second {
		t.Errorf("Blocked = %+v, want render-scratch blocked", view.Blocked)
	}
	if len(view.Waiting) != 1 || view.Waiting[0].Matter.ID != second {
		t.Errorf("Waiting = %+v, want render-scratch grouped once", view.Waiting)
	}
}

func TestNext_GateOnlyWaitIsNothingUnblocked(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("awaiting-review", "Awaiting review")
	f.start(m)
	f.finish(m)

	view := nextFor(t, f, f.current())
	if view.Kind != NothingUnblocked {
		t.Fatalf("Kind = %v, want NothingUnblocked instead of EverythingSealed", view.Kind)
	}
	if len(view.Blocked) != 0 {
		t.Errorf("Blocked = %+v, want no dependency waits", view.Blocked)
	}
	if len(view.Waiting) != 1 || view.Waiting[0].Matter.ID != m || len(view.Waiting[0].Gates) != 1 {
		t.Fatalf("Waiting = %+v, want one gate wait for awaiting-review", view.Waiting)
	}
}

// TestNext_NoCursorSet is vocabulary output 4: no cursor, frontier
// non-empty — candidates listed, never guessed among.
func TestNext_NoCursorSet(t *testing.T) {
	f := newFixture(t)
	a := f.matter("scaffold", "scaffold")
	b := f.matter("vocabulary", "vocabulary")

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != NoCursor {
		t.Fatalf("Kind = %v, want NoCursor", view.Kind)
	}
	if len(view.Candidates) != 2 || view.Candidates[0].ID != a || view.Candidates[1].ID != b {
		t.Errorf("Candidates = %+v, want [scaffold vocabulary]", view.Candidates)
	}
}

// TestNext_EverythingSealed is vocabulary output 5: no cursor, nothing
// Planned outstanding at all, nudging toward Backlog.
func TestNext_EverythingSealed(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("done-and-sealed", "Done and sealed")
	f.start(m)
	f.finish(m)
	f.closeGate(m, "reviewed-local", store.ScaleMatter)

	f.seedBacklogEntry("something found mid-flight")

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != EverythingSealed {
		t.Fatalf("Kind = %v, want EverythingSealed", view.Kind)
	}
	if view.BacklogCount != 1 {
		t.Errorf("BacklogCount = %d, want 1", view.BacklogCount)
	}
}

// TestNext_NoCursorCandidates_CollapseToOutermostReady is the first-dogfood
// finding: a ready Planned Matter must appear in the candidate list alone —
// its own Planned interior (steps, stages) collapses into it, per drafted
// vocabulary output 4, which lists Matters and one Stage, never a Matter
// beside its own steps.
func TestNext_NoCursorCandidates_CollapseToOutermostReady(t *testing.T) {
	f := newFixture(t)
	a := f.matter("release-engineering", "release-engineering")
	_ = f.step(a, "step-01", "First")
	_ = f.step(a, "step-02", "Second")
	b := f.matter("install-target-codex", "install-target-codex")

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != NoCursor {
		t.Fatalf("Kind = %v, want NoCursor", view.Kind)
	}
	if len(view.Candidates) != 2 || view.Candidates[0].ID != a || view.Candidates[1].ID != b {
		t.Errorf("Candidates = %+v, want the two Matters only — no interior steps", view.Candidates)
	}
}

// TestNext_NoCursorCandidates_ReadyStageOfBlockedMatterStays pins drafted
// output 4's own example: a Stage whose blockers are met surfaces even while
// its Matter is blocked — collapse removes interiors of *ready* ancestors
// only, never a workable frontier inside a blocked one.
func TestNext_NoCursorCandidates_ReadyStageOfBlockedMatterStays(t *testing.T) {
	f := newFixture(t)
	other := f.matter("scaffold", "scaffold")
	m := f.matter("tiers", "tiers")
	f.depend(m, other) // tiers blocked by scaffold, which is Planned: unmet
	stage := f.stage(m, "identity-rules", "identity-rules")

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != NoCursor {
		t.Fatalf("Kind = %v, want NoCursor", view.Kind)
	}
	if len(view.Candidates) != 2 || view.Candidates[0].ID != other || view.Candidates[1].ID != stage {
		t.Errorf("Candidates = %+v, want [scaffold tiers/identity-rules]", view.Candidates)
	}
}

// TestNext_InProgressNoCursor is the trace-1 finding: no cursor, an empty
// Planned frontier, nothing blocked — but work actively In Progress (a
// Matter started immediately with no plan, before any `wip next --set`).
// next must report the in-progress work, never EverythingSealed — output 5
// was being reused for a state it does not describe.
func TestNext_InProgressNoCursor(t *testing.T) {
	f := newFixture(t)
	m := f.matter("bugfix-no-plan", "Started immediately, no plan, no cursor")
	f.start(m)

	cur := f.current()
	view := nextFor(t, f, cur)
	if view.Kind != InProgressNoCursor {
		t.Fatalf("Kind = %v, want InProgressNoCursor", view.Kind)
	}
	if len(view.InProgress) != 1 || view.InProgress[0].ID != m {
		t.Errorf("InProgress = %+v, want [bugfix-no-plan]", view.InProgress)
	}
}

func TestNext_InProgressNoCursorSuppressesFutureSiblingWait(t *testing.T) {
	f := newFixture(t)
	m := f.matter("active-plan", "A plan with active leaf work")
	stage := f.stage(m, "build", "Build")
	current := f.step(stage, "step-01", "Current work")
	next := f.step(stage, "step-02", "Future work")
	f.depend(next, current)
	f.start(m)
	f.start(stage)
	f.start(current)

	view := nextFor(t, f, f.current())
	if view.Kind != InProgressNoCursor {
		t.Fatalf("view = %+v, want InProgressNoCursor instead of an empty NothingUnblocked summary", view)
	}
	if len(view.InProgress) != 3 || view.InProgress[2].ID != current {
		t.Errorf("InProgress = %+v, want the active Matter, Stage, and current Step", view.InProgress)
	}
}

// TestNext_ChooseNext_Sealed is D67: the cursor's target has itself become
// sealed since it was set — reported as a fact, and the cursor is never
// moved by a read.
func TestNext_ChooseNext_Sealed(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "A Matter")
	other := f.matter("other", "Still open, so the frontier isn't empty")
	f.start(m)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	before := countCursorMoves(t, f)

	f.finish(m)
	f.closeGate(m, "reviewed-local", store.ScaleMatter)

	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext {
		t.Fatalf("Kind = %v, want ChooseNext", view.Kind)
	}
	if view.EndedReason != "sealed" {
		t.Errorf("EndedReason = %q, want %q", view.EndedReason, "sealed")
	}
	if len(view.Candidates) != 1 || view.Candidates[0].ID != other {
		t.Errorf("Candidates = %+v, want [other]", view.Candidates)
	}

	after := countCursorMoves(t, f)
	if after != before {
		t.Errorf("cursor.moved count changed from %d to %d — a read verb must never move the cursor (D67)", before, after)
	}
}

// TestNext_ChooseNext_Canceled and TestNext_ChooseNext_Removed cover D67's
// other two ended-cursor reasons.
func TestNext_ChooseNext_Canceled(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Abandoned after the cursor was set")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	f.cancel(m)

	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext || view.EndedReason != "canceled" {
		t.Errorf("Kind/Reason = %v/%q, want ChooseNext/canceled", view.Kind, view.EndedReason)
	}
}

func TestNext_ChooseNext_Removed(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Host Matter")
	step := f.step(m, "step-01", "Removed after the cursor was set")
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step); err != nil {
		t.Fatal(err)
	}
	f.remove(step)

	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext || view.EndedReason != "removed" {
		t.Errorf("Kind/Reason = %v/%q, want ChooseNext/removed", view.Kind, view.EndedReason)
	}
}

// TestNext_ChooseNext_ListsInProgress checks the choose-next reframing's own
// addition: alongside the ready candidates, the view also lists work already
// under way — the same repo-scoped fetch noCursorView makes, factored once
// into chooseNextView.
func TestNext_ChooseNext_ListsInProgress(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "Sealed after the cursor was set")
	other := f.matter("other", "Started with no plan — in progress, not ready")
	f.start(m)
	f.start(other)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	f.finish(m)
	f.closeGate(m, "reviewed-local", store.ScaleMatter)

	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext {
		t.Fatalf("Kind = %v, want ChooseNext", view.Kind)
	}
	if len(view.Candidates) != 0 {
		t.Errorf("Candidates = %+v, want none (other is In Progress, not Planned)", view.Candidates)
	}
	if len(view.InProgress) != 1 || view.InProgress[0].ID != other {
		t.Errorf("InProgress = %+v, want [other]", view.InProgress)
	}
}

func TestNext_ChooseNext_IdleIncludesWaitingSummary(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	ended := f.matter("ended", "The cursor target")
	waiting := f.matter("waiting", "Waiting for review")
	f.start(ended)
	f.start(waiting)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, ended); err != nil {
		t.Fatal(err)
	}
	f.finish(ended)
	f.closeGate(ended, "reviewed-local", store.ScaleMatter)
	f.finish(waiting)

	view := nextFor(t, f, cur)
	if view.Kind != ChooseNext || view.EndedReason != "sealed" {
		t.Fatalf("view = %+v, want sealed ChooseNext", view)
	}
	if len(view.Candidates) != 0 || len(view.InProgress) != 0 {
		t.Fatalf("Candidates/InProgress = %+v/%+v, want an idle ended-cursor view", view.Candidates, view.InProgress)
	}
	if len(view.Waiting) != 1 || view.Waiting[0].Matter.ID != waiting {
		t.Fatalf("Waiting = %+v, want the waiting Matter summary", view.Waiting)
	}
}

// ---------------------------------------------------------------------------
// Test-only seams: package-internal functions exercised directly against a
// resolved Current, bypassing ResolveCurrent's git shell-outs (tiers'
// own tests already cover that resolution; the e2e suite in internal/cli
// covers the real `wip next --set` path end to end, including the git
// resolution — step-07(b)).
// ---------------------------------------------------------------------------

// nextFor is Next's own composition (next.go's unexported `next`),
// re-entered here against an already-resolved Current instead of a real
// directory — the same code Next itself calls, minus the ResolveCurrent
// call the e2e suite in internal/cli exercises separately (step-07(b)).
func nextFor(t *testing.T, f *fixture, cur Current) View {
	t.Helper()
	view, err := next(ctx, f.View, cur)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// SetCursorForTest exercises the real setCursor write path against an
// already-resolved Current, so package tests don't need a real git clone
// for ResolveCurrent to shell out to.
func SetCursorForTest(f *fixture, cur Current, locator string) (store.Node, error) {
	return setCursor(ctx, f.Store, store.ActorHuman, cur, locator)
}

// countCursorMoves counts cursor.moved events in the whole log — the D67
// assertion that a read never adds one.
func countCursorMoves(t *testing.T, f *fixture) int {
	t.Helper()
	events, err := f.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == store.TypeCursorMoved {
			n++
		}
	}
	return n
}
