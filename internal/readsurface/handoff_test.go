package readsurface

// Tests for EndedCursor: the read-only, best-effort data lifecycle and gate
// verbs turn into a hand-off line after a write ends the cursor's work.
// Mirrors next_test.go's own fixture/seam style — a resolved Current, the
// package-internal setCursor write path, and store-level assertions.

import (
	"testing"
)

// TestEndedCursor_SealedStepSuggestsReadySibling is the hand-off's main
// case: the cursor's target is a sealed Step, and a ready sibling step
// exists — EndedCursor names it.
func TestEndedCursor_SealedStepSuggestsReadySibling(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Host Matter")
	step1 := f.step(m, "step-01", "First")
	step2 := f.step(m, "step-02", "Second")
	f.start(m)
	f.start(step1)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step1); err != nil {
		t.Fatal(err)
	}
	f.finish(step1)

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended == nil {
		t.Fatal("EndedCursor = nil, want a sealed result")
	}
	if ended.Reason != "sealed" || ended.Target.ID != step1 {
		t.Errorf("Reason/Target = %q/%s, want sealed/%s", ended.Reason, ended.Target.ID, step1)
	}
	if ended.Suggested == nil || ended.Suggested.ID != step2 {
		t.Fatalf("Suggested = %+v, want step-02", ended.Suggested)
	}

	line, err := HandoffLine(ctx, f.View, ended)
	if err != nil {
		t.Fatal(err)
	}
	want := "step-02 is next in m: wip next --set step-02 — or wip next / wip next --clear"
	if line != want {
		t.Errorf("HandoffLine = %q, want %q", line, want)
	}
}

// TestEndedCursor_SealedStepBlockedSibling_NoSuggestion checks that a
// sibling step which exists but is not ready (still blocked) is never
// suggested — EndedCursor names only a *ready* sibling, not merely a next
// one in creation order.
func TestEndedCursor_SealedStepBlockedSibling_NoSuggestion(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Host Matter")
	step1 := f.step(m, "step-01", "First")
	step2 := f.step(m, "step-02", "Second, blocked by something else")
	blocker := f.matter("blocker", "Still open")
	f.depend(step2, blocker)
	f.start(m)
	f.start(step1)

	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step1); err != nil {
		t.Fatal(err)
	}
	f.finish(step1)

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended == nil || ended.Reason != "sealed" {
		t.Fatalf("ended = %+v, want a sealed result", ended)
	}
	if ended.Suggested != nil {
		t.Errorf("Suggested = %+v, want nil (step-02 is still blocked)", ended.Suggested)
	}

	line, err := HandoffLine(ctx, f.View, ended)
	if err != nil {
		t.Fatal(err)
	}
	want := "the cursor's work is done — wip next to choose what's next, or wip next --clear"
	if line != want {
		t.Errorf("HandoffLine = %q, want %q", line, want)
	}
}

// TestEndedCursor_LiveTarget_IsNil checks the "still live" case — a cursor
// on In Progress work — reports nothing to hand off.
func TestEndedCursor_LiveTarget_IsNil(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended != nil {
		t.Errorf("EndedCursor = %+v, want nil for a still-live target", ended)
	}
}

// TestEndedCursor_NoCursor_IsNil checks the unset case reports nothing to
// hand off either — there is no cursor's work to have ended.
func TestEndedCursor_NoCursor_IsNil(t *testing.T) {
	f := newFixture(t)
	cur := f.current()

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended != nil {
		t.Errorf("EndedCursor = %+v, want nil when no cursor is set", ended)
	}
}

// TestEndedCursor_Canceled reports the canceled reason with no successor
// lookup (only a sealed Step target is eligible for one).
func TestEndedCursor_Canceled(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Abandoned")
	f.start(m)
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, m); err != nil {
		t.Fatal(err)
	}
	f.cancel(m)

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended == nil || ended.Reason != "canceled" || ended.Suggested != nil {
		t.Errorf("ended = %+v, want canceled with no suggestion", ended)
	}

	line, err := HandoffLine(ctx, f.View, ended)
	if err != nil {
		t.Fatal(err)
	}
	want := "the cursor's work is done — wip next to choose what's next, or wip next --clear"
	if line != want {
		t.Errorf("HandoffLine = %q, want %q", line, want)
	}
}

// TestEndedCursor_Removed reports the removed reason with a zero Target and
// no successor lookup.
func TestEndedCursor_Removed(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "Host Matter")
	step := f.step(m, "step-01", "Removed after the cursor was set")
	cur := f.current()
	if _, err := SetCursorForTest(f, cur, step); err != nil {
		t.Fatal(err)
	}
	f.remove(step)

	ended, err := EndedCursor(ctx, f.View, cur.Clone.ID, cur.Worktree.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended == nil || ended.Reason != "removed" || ended.Target.ID != "" || ended.Suggested != nil {
		t.Errorf("ended = %+v, want removed with a zero Target and no suggestion", ended)
	}
}
