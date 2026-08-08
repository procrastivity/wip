package scheduler

// Tests for the Orchestrator loop core: the one dispatch path end to end at
// unit grain — claims (F4), roles in the brackets, D30 policy per condition,
// D60 parking, and run.finished only when nothing further can be dispatched.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
)

// bracket opens the worktree's plain P1 dispatch the Orchestrator binds to.
func (f *fixture) bracket() string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: f.Repo, Clone: f.Clone, Worktree: f.Worktree}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: id}}, nil
	})
	return id
}

func (f *fixture) env() store.Env {
	return store.Env{Repo: f.Repo, Clone: f.Clone, Worktree: f.Worktree}
}

// workRecorder is a Work hook that records engagement order and can fail
// chosen Matters.
type workRecorder struct {
	order []string
	fail  map[string]bool
}

func (w *workRecorder) work(_ context.Context, n store.Node) error {
	w.order = append(w.order, n.ID)
	if w.fail[n.Matter] {
		return errors.New("the build broke")
	}
	return nil
}

func orchestrate(t *testing.T, f *fixture, run store.Run, runCap int, policy Policy, hooks Hooks) Outcome {
	t.Helper()
	out, err := Orchestrate(ctx, f.Store, store.ActorHuman, f.env(), run.ID, runCap, policy, hooks)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	return out
}

func TestLoop_SequentialPassFinishesTheRun(t *testing.T) {
	f := newFixture(t)
	m := f.matter("seq", "Sequential")
	s1 := f.step(m, "step-01", "first")
	s2 := f.step(m, "step-02", "second")
	f.depend(s2, s1)
	batch := f.namedBatch("seq-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()

	w := &workRecorder{}
	out := orchestrate(t, f, run, 1, Policy{}, Hooks{Work: w.work})

	if out.State != StateFinished {
		t.Fatalf("state = %s, want finished (skips: %+v)", out.State, out.Skipped)
	}
	if len(w.order) != 2 || w.order[0] != s1 || w.order[1] != s2 {
		t.Errorf("work order = %v, want [%s %s] — edge order, nothing else", w.order, s1, s2)
	}
	// The member finished, its claim closed, the Run closed completed.
	matter, _ := f.Node(ctx, m)
	if matter.Lifecycle != store.Done {
		t.Errorf("matter = %s, want done", matter.Lifecycle)
	}
	closedRun, _ := f.Run(ctx, run.ID)
	if closedRun.Open || closedRun.CloseReason != store.CloseCompleted {
		t.Errorf("run = %+v, want closed completed", closedRun)
	}
	open, found, err := f.OpenDispatch(ctx, f.Worktree)
	if err != nil || !found {
		t.Fatalf("the plain bracket should survive the pass: %v", err)
	}
	if open.Run != "" {
		t.Errorf("OpenDispatch returned a claim, want the plain bracket")
	}

	// Events tell the role story: orchestrator spawned and closed, one
	// builder per step, every step finished under role:builder.
	events, _ := f.Events(ctx)
	var stepFinishActors []store.Actor
	skips := 0
	for _, ev := range events {
		if ev.Type == store.TypeStepFinished {
			stepFinishActors = append(stepFinishActors, ev.Actor)
		}
		if ev.Type == store.TypeRunSkipped {
			skips++
		}
	}
	for _, actor := range stepFinishActors {
		if actor != store.RoleActor("builder") {
			t.Errorf("a step finished under %q, want role:builder", actor)
		}
	}
	if len(stepFinishActors) != 2 || skips != 0 {
		t.Errorf("finishes=%d skips=%d, want 2 and 0", len(stepFinishActors), skips)
	}
}

func TestLoop_ContentionSkipsAndTheRestProceeds(t *testing.T) {
	f := newFixture(t)
	mA := f.matter("contended", "Contended")
	mB := f.matter("free", "Free")
	batch := f.namedBatch("cont-batch", mA, mB)
	run := f.startRun(batch, "run-01", mA, mB)
	f.bracket()

	// A holds an open Dispatch claim from elsewhere: a second worktree's
	// bare bracket cannot claim, so seed a competing run's claim.
	otherBatch := f.namedBatch("other-batch", mA)
	otherRun := f.startRun(otherBatch, "run-01", mA)
	f.commit(f.env(), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: tx.NewID(), Payload: store.DispatchOpened{Run: otherRun.ID, Matter: mA}}}, nil
	})

	w := &workRecorder{}
	out := orchestrate(t, f, run, 2, Policy{}, Hooks{Work: w.work})

	if out.State != StateStandingBy {
		t.Fatalf("state = %s, want standing-by", out.State)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Matter.ID != mA || out.Skipped[0].Reason != store.RunSkipContention {
		t.Fatalf("skipped = %+v, want contention on A", out.Skipped)
	}
	if len(w.order) != 1 || w.order[0] != mB {
		t.Errorf("worked = %v, want just B", w.order)
	}
}

func TestLoop_FailurePolicySkipVersusHalt(t *testing.T) {
	f := newFixture(t)
	mA := f.matter("fragile", "Fragile")
	mB := f.matter("solid", "Solid")
	batch := f.namedBatch("fail-batch", mA, mB)
	run := f.startRun(batch, "run-01", mA, mB)
	f.bracket()

	w := &workRecorder{fail: map[string]bool{mA: true}}
	out := orchestrate(t, f, run, 1, Policy{}, Hooks{Work: w.work})
	if out.State != StateStandingBy {
		t.Fatalf("state = %s, want standing-by after a skipped failure", out.State)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Reason != store.RunSkipFailed {
		t.Fatalf("skipped = %+v, want one failed skip", out.Skipped)
	}
	if len(w.order) != 2 {
		t.Errorf("worked = %v, want the failure and then B — skip continues the pass", w.order)
	}
	// The failed Matter's claim closed, and the node it broke on stays In
	// Progress: the skip is about the pass, not a rewrite of the work.
	a, _ := f.Node(ctx, mA)
	if a.Lifecycle != store.InProgress {
		t.Errorf("failed matter = %s, want in-progress", a.Lifecycle)
	}

	// The same shape under halt: nothing after the failure.
	f2 := newFixture(t)
	m1 := f2.matter("first", "First")
	m2 := f2.matter("second", "Second")
	b2 := f2.namedBatch("halt-batch", m1, m2)
	r2 := f2.startRun(b2, "run-01", m1, m2)
	f2.bracket()
	w2 := &workRecorder{fail: map[string]bool{m1: true}}
	out2 := orchestrate(t, f2, r2, 1, Policy{Failed: HandleHalt}, Hooks{Work: w2.work})
	if out2.State != StateHalted || out2.HaltedOn == nil || out2.HaltedOn.Reason != store.RunSkipFailed {
		t.Fatalf("halted outcome = %+v", out2)
	}
	if len(w2.order) != 1 {
		t.Errorf("worked = %v, want only the failure before the halt", w2.order)
	}
}

func TestLoop_ParkedAtHumanGateStandsBy(t *testing.T) {
	f := newFixture(t)
	if err := f.DeclareGate(ctx, f.Repo, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	m := f.matter("gated", "Gated")
	f.step(m, "step-01", "the work")
	batch := f.namedBatch("gated-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()

	w := &workRecorder{}
	out := orchestrate(t, f, run, 1, Policy{}, Hooks{Work: w.work})

	if out.State != StateStandingBy {
		t.Fatalf("state = %s, want standing-by (D60: parked, not failed)", out.State)
	}
	if len(out.Parked) != 1 || out.Parked[0].ID != m {
		t.Fatalf("parked = %+v, want the gated matter", out.Parked)
	}
	if len(out.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none — awaiting a gate is not a skip", out.Skipped)
	}
	// The Run stands by: open, no run.finished.
	openRun, _ := f.Run(ctx, run.ID)
	if !openRun.Open {
		t.Errorf("run closed %s, want open and standing by", openRun.CloseReason)
	}
	matter, _ := f.Node(ctx, m)
	if matter.Lifecycle != store.Done {
		t.Errorf("matter = %s, want done and awaiting its gate", matter.Lifecycle)
	}
}

func TestLoop_ResearcherPlansAnUnplannedMember(t *testing.T) {
	f := newFixture(t)
	m := f.matter("unplanned", "Unplanned")
	batch := f.namedBatch("plan-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()

	w := &workRecorder{}
	out := orchestrate(t, f, run, 1, Policy{}, Hooks{
		Work: w.work,
		Plan: func(_ context.Context, _ store.Node) ([]StepSpec, error) {
			return []StepSpec{{Title: "Survey"}, {Title: "Do"}, {Title: "Prove"}}, nil
		},
	})
	if out.State != StateFinished {
		t.Fatalf("state = %s, want finished", out.State)
	}
	if len(w.order) != 3 {
		t.Fatalf("worked %d nodes, want the 3 authored steps", len(w.order))
	}

	// The steps were born through verbs under role:researcher, inside the
	// member's claim bracket.
	events, _ := f.Events(ctx)
	births := 0
	for _, ev := range events {
		if ev.Type == store.TypeStepCreated {
			births++
			if ev.Actor != store.RoleActor("researcher") {
				t.Errorf("step born under %q, want role:researcher", ev.Actor)
			}
		}
	}
	if births != 3 {
		t.Errorf("step births = %d, want 3", births)
	}
}

func TestLoop_BlockedMemberSkipsAtSettle(t *testing.T) {
	f := newFixture(t)
	outside := f.matter("outside", "Not in the batch")
	m := f.matter("waiting", "Waiting")
	f.depend(m, outside)
	free := f.matter("free", "Free")
	batch := f.namedBatch("blocked-batch", m, free)
	run := f.startRun(batch, "run-01", m, free)
	f.bracket()

	w := &workRecorder{}
	out := orchestrate(t, f, run, 1, Policy{}, Hooks{Work: w.work})
	if out.State != StateStandingBy {
		t.Fatalf("state = %s, want standing-by", out.State)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Matter.ID != m || out.Skipped[0].Reason != store.RunSkipBlocked {
		t.Fatalf("skipped = %+v, want blocked on the waiting member", out.Skipped)
	}
	if len(w.order) != 1 || w.order[0] != free {
		t.Errorf("worked = %v, want just the free member", w.order)
	}
}

func TestLoop_RefusesStrandedAndLiveRuns(t *testing.T) {
	f := newFixture(t)
	m := f.matter("owned", "Owned elsewhere")
	batch := f.namedBatch("owned-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()

	// A different Clone: stranded (F9).
	otherClone := f.attachClone(f.Repo)
	otherWt := f.attachWorktree(f.Repo, otherClone, "feature")
	_, err := Orchestrate(ctx, f.Store, store.ActorHuman,
		store.Env{Repo: f.Repo, Clone: otherClone, Worktree: otherWt},
		run.ID, 1, Policy{}, Hooks{Work: func(context.Context, store.Node) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "stand it down") {
		t.Fatalf("stranded refusal = %v", err)
	}

	// A held lock: live (S2).
	lock, err := runlock.Acquire(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	_, err = Orchestrate(ctx, f.Store, store.ActorHuman, f.env(), run.ID, 1, Policy{},
		Hooks{Work: func(context.Context, store.Node) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "is live") {
		t.Fatalf("live refusal = %v", err)
	}
}

// TestResume_ReapsOrphansAndFinishesWithoutReplay is F8 end to end: an
// interrupted pass leaves an open claim, an open Builder, and a step In
// Progress; Resume emits run.resumed, reaps the orphaned Dispatch (Builder
// with it, D59), re-engages the interrupted step without a second start
// event, does not replay the completed step, and finishes the Run.
func TestResume_ReapsOrphansAndFinishesWithoutReplay(t *testing.T) {
	f := newFixture(t)
	m := f.matter("interrupted", "Interrupted")
	s1 := f.step(m, "step-01", "done before the crash")
	s2 := f.step(m, "step-02", "mid-flight at the crash")
	s3 := f.step(m, "step-03", "never reached")
	f.depend(s3, s2)
	batch := f.namedBatch("resume-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()

	// The interrupted pass, replayed by hand: claim opened, s1 worked to
	// Done, s2 started with a Builder in the bracket — then the process
	// died. The kernel released the lock; every row stays as it was.
	var claim string
	f.commit(f.env(), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		claim = tx.NewID()
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: claim, Payload: store.DispatchOpened{Run: run.ID, Matter: m}}}, nil
	})
	f.start(s1)
	f.finish(s1)
	f.start(s2)
	f.commit(f.env(), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRoleSpawned, Subject: tx.NewID(), Payload: store.RoleSpawned{Dispatch: claim, Name: store.RoleBuilder}}}, nil
	})

	w := &workRecorder{}
	out, err := Resume(ctx, f.Store, store.ActorHuman, f.env(), run.ID, 1, Policy{}, Hooks{Work: w.work})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if out.State != StateFinished {
		t.Fatalf("state = %s, want finished (skips: %+v)", out.State, out.Skipped)
	}
	// The orphaned mid-flight step first, then the one behind it; the
	// completed step is not replayed.
	if len(w.order) != 2 || w.order[0] != s2 || w.order[1] != s3 {
		t.Fatalf("work order = %v, want [%s %s]", w.order, s2, s3)
	}

	// The orphan claim reaped, with the Builder reaped inside it.
	reaped, _ := f.Dispatch(ctx, claim)
	if reaped.Open || reaped.CloseReason != store.CloseReaped {
		t.Errorf("orphan claim = %+v, want closed reaped", reaped)
	}

	events, _ := f.Events(ctx)
	var resumed, s2Starts int
	for _, ev := range events {
		if ev.Type == store.TypeRunResumed {
			resumed++
		}
		if ev.Type == store.TypeStepStarted && ev.Subject == s2 {
			s2Starts++
		}
	}
	if resumed != 1 {
		t.Errorf("run.resumed events = %d, want 1", resumed)
	}
	if s2Starts != 1 {
		t.Errorf("s2 start events = %d, want exactly the pre-crash one — no second start on resume", s2Starts)
	}
	closedRun, _ := f.Run(ctx, run.ID)
	if closedRun.Open || closedRun.CloseReason != store.CloseCompleted {
		t.Errorf("run = %+v, want closed completed", closedRun)
	}
}

// Resume refuses what F2/F9/S2 say it must: a closed Run, a non-owning
// Clone, a live Run.
func TestResume_Refusals(t *testing.T) {
	f := newFixture(t)
	m := f.matter("refusals", "Refusals")
	batch := f.namedBatch("refusal-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.bracket()
	hooks := Hooks{Work: func(context.Context, store.Node) error { return nil }}

	otherClone := f.attachClone(f.Repo)
	otherWt := f.attachWorktree(f.Repo, otherClone, "feature")
	_, err := Resume(ctx, f.Store, store.ActorHuman, store.Env{Repo: f.Repo, Clone: otherClone, Worktree: otherWt}, run.ID, 1, Policy{}, hooks)
	if err == nil || !strings.Contains(err.Error(), "never adopt") {
		t.Fatalf("stranded resume = %v", err)
	}

	lock, err := runlock.Acquire(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resume(ctx, f.Store, store.ActorHuman, f.env(), run.ID, 1, Policy{}, hooks)
	if err == nil || !strings.Contains(err.Error(), "is live") {
		t.Fatalf("live resume = %v", err)
	}
	_ = lock.Release()

	// Finish it, then try to resume the closed Run.
	out, err := Orchestrate(ctx, f.Store, store.ActorHuman, f.env(), run.ID, 1, Policy{}, hooks)
	if err != nil || out.State != StateFinished {
		t.Fatalf("finishing pass: %v %+v", err, out)
	}
	_, err = Resume(ctx, f.Store, store.ActorHuman, f.env(), run.ID, 1, Policy{}, hooks)
	if err == nil || !strings.Contains(err.Error(), "never resumed") {
		t.Fatalf("closed resume = %v", err)
	}
}
