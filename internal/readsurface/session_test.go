package readsurface

// Tests for step-05: Session derivation over the event log — never
// persisted, drives nothing (D17) — including step-07(c)'s named trace: one
// Session at a 12-hour idle gap, two at a 2-hour gap, over one seeded
// stream. This Matter owns and unit-tests the derivation; `events-traces`
// (← read-surface) renders and audits the trace artifact itself.
//
// occurred_at is derived from an event's own id and the log is append-only
// (schema's events_no_update trigger), so there is no way to place events at
// chosen, widely-spaced timestamps after the fact — controlling the clock
// they were minted under, via store.OpenWithClock, is the seam these tests
// use instead of a test that really waits hours.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// fixtureClock is a monotonically-advanceable fake clock.
type fixtureClock struct{ t time.Time }

func (c *fixtureClock) now() time.Time  { return c.t }
func (c *fixtureClock) set(t time.Time) { c.t = t }

// newFixtureWithClock is newFixture with an injectable clock, so a test can
// place events at chosen, widely-spaced timestamps by calling clk.set
// before each action.
func newFixtureWithClock(t *testing.T, start time.Time) (*fixture, *fixtureClock) {
	t.Helper()
	clk := &fixtureClock{t: start}
	s, err := store.OpenWithClock(filepath.Join(t.TempDir(), "wip.db"), clk.now)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	f := &fixture{Store: s, t: t}
	f.Repo = f.attachRepo()
	f.Clone = f.attachClone(f.Repo)
	f.Worktree = f.attachWorktree(f.Repo, f.Clone, "")
	return f, clk
}

// TestSession_OneSessionAt12hGapTwoAt2hGap is step-07(c)'s named trace: one
// seeded stream of events, three hours apart, read back at both idle-gap
// thresholds.
func TestSession_OneSessionAt12hGapTwoAt2hGap(t *testing.T) {
	base := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	f, clk := newFixtureWithClock(t, base)

	a := f.matter("a", "First thing worked")
	clk.set(base.Add(1 * time.Hour))
	f.start(a)

	clk.set(base.Add(4 * time.Hour))
	b := f.matter("b", "Picked up three hours after the last event")
	clk.set(base.Add(5 * time.Hour))
	f.start(b)

	at12h, err := Derive(ctx, f.Store, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(at12h) != 1 {
		t.Fatalf("sessions at a 12h gap = %d, want 1", len(at12h))
	}
	if !at12h[0].Start.Equal(base) || !at12h[0].End.Equal(base.Add(5*time.Hour)) {
		t.Errorf("session = [%s, %s], want [%s, %s]", at12h[0].Start, at12h[0].End, base, base.Add(5*time.Hour))
	}

	at2h, err := Derive(ctx, f.Store, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(at2h) != 2 {
		t.Fatalf("sessions at a 2h gap = %d, want 2 (the 3h gap between hour 1 and hour 4 splits them)", len(at2h))
	}
	if !at2h[0].Start.Equal(base) || !at2h[0].End.Equal(base.Add(1*time.Hour)) {
		t.Errorf("first session = [%s, %s], want [%s, %s]", at2h[0].Start, at2h[0].End, base, base.Add(1*time.Hour))
	}
	if !at2h[1].Start.Equal(base.Add(4*time.Hour)) || !at2h[1].End.Equal(base.Add(5*time.Hour)) {
		t.Errorf("second session = [%s, %s], want [%s, %s]", at2h[1].Start, at2h[1].End, base.Add(4*time.Hour), base.Add(5*time.Hour))
	}
}

// TestSession_DiscountedDispatchCloseNeitherExtendsNorBridges is D59: a
// dispatch.closed with reason superseded/reaped carries an administrative
// occurred_at, not evidence of work — it must not bridge a gap that would
// otherwise split two sessions, even though, sitting between them, it looks
// like it should.
func TestSession_DiscountedDispatchCloseNeitherExtendsNorBridges(t *testing.T) {
	base := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	f, clk := newFixtureWithClock(t, base)

	f.matter("a", "Before the gap")

	clk.set(base.Add(1 * time.Hour))
	dispatch := f.openDispatch()
	// Administratively closed six hours later — deep inside what would
	// otherwise be the idle gap between the two real working stretches.
	clk.set(base.Add(7 * time.Hour))
	closeEv := f.closeDispatch(dispatch, store.CloseSuperseded)

	clk.set(base.Add(13 * time.Hour))
	f.matter("b", "After the gap")

	sessions, err := Derive(ctx, f.Store, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 — the discounted close must not bridge the gap", len(sessions))
	}
	for _, sess := range sessions {
		for _, ev := range sess.Events {
			if ev.ID == closeEv.ID {
				t.Errorf("the discounted close (at +7h) fell inside a session window [%s, %s], want it outside both",
					sess.Start, sess.End)
			}
		}
	}
	if !sessions[0].End.Equal(base.Add(1 * time.Hour)) {
		t.Errorf("first session end = %s, want %s (the discounted close must not extend it)", sessions[0].End, base.Add(1*time.Hour))
	}
	if !sessions[1].Start.Equal(base.Add(13 * time.Hour)) {
		t.Errorf("second session start = %s, want %s", sessions[1].Start, base.Add(13*time.Hour))
	}
}

// TestSession_DiscountedCloseInsideARealWindowIsStillShown confirms the
// other half: a discounted close surrounded by real evidence on both sides
// (so it never has to bridge anything) still shows up in that session's
// event list, since it did happen and D59 only discounts it for boundary
// purposes.
func TestSession_DiscountedCloseInsideARealWindowIsStillShown(t *testing.T) {
	base := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	f, clk := newFixtureWithClock(t, base)

	a := f.matter("a", "Evidence before")

	clk.set(base.Add(10 * time.Minute))
	dispatch := f.openDispatch()
	clk.set(base.Add(20 * time.Minute))
	closeEv := f.closeDispatch(dispatch, store.CloseReaped)

	clk.set(base.Add(30 * time.Minute))
	f.start(a)

	sessions, err := Derive(ctx, f.Store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	var saw bool
	for _, ev := range sessions[0].Events {
		if ev.ID == closeEv.ID {
			saw = true
		}
	}
	if !saw {
		t.Error("the discounted close, surrounded by real evidence, is missing from its session's event list")
	}
}

// TestSession_EmptyLogDerivesNoSessions confirms Derive always finds at
// least the bootstrap tier-attach events — there is no such thing as a
// truly empty log once a Repo/Clone/Worktree exist — and folds them into
// exactly one session.
func TestSession_EmptyLogDerivesNoSessions(t *testing.T) {
	f := newFixture(t)
	sessions, err := Derive(ctx, f.Store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want exactly 1 (the bootstrap attach events)", len(sessions))
	}
}

func (f *fixture) openDispatch() string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: f.Repo, Clone: f.Clone, Worktree: f.Worktree}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: id}}, nil
	})
	return id
}

func (f *fixture) closeDispatch(dispatch string, reason store.CloseReason) store.Event {
	f.t.Helper()
	evs := f.commit(store.Env{Repo: f.Repo, Clone: f.Clone, Worktree: f.Worktree}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeDispatchClosed, Subject: dispatch, Payload: store.DispatchClosed{Reason: reason}}}, nil
	})
	return evs[0]
}
