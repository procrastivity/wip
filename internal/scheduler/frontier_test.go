package scheduler

// Unit tests for the Ready engine, replaying parallelism-decisions.md's four
// worked frontier traces (cases A–D) moment by moment, plus the predicate
// edges the traces take as given: only Done satisfies (a Paused or Canceled
// blocker still blocks, a tombstoned one does not), a blocked ancestor bars
// its interior from dispatch, terminal groupings take no new work, and
// selection never reads a sibling sort key.

import (
	"context"
	"fmt"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// ids extracts node IDs for order-insensitive comparison.
func ids(nodes []store.Node) map[string]bool {
	out := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		out[n.ID] = true
	}
	return out
}

func wantSet(t *testing.T, what string, got []store.Node, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Errorf("%s = %d nodes, want %d", what, len(g), len(want))
	}
	for _, id := range want {
		if !g[id] {
			t.Errorf("%s is missing %s", what, id)
		}
	}
}

func (f *fixture) derive(t *testing.T, run store.Run, runCap int) Frontier {
	t.Helper()
	fr, err := Derive(ctx, f.View, run, runCap)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return fr
}

// Case A — three independent Steps, then a join. Cap 3.
func TestCaseA_IndependentStepsThenJoin(t *testing.T) {
	f := newFixture(t)
	m := f.matter("case-a", "Case A")
	s1 := f.step(m, "step-01", "one")
	s2 := f.step(m, "step-02", "two")
	s3 := f.step(m, "step-03", "three")
	s4 := f.step(m, "step-04", "join")
	f.depend(s4, s1)
	f.depend(s4, s2)
	f.depend(s4, s3)
	batch := f.namedBatch("case-a-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)

	// A0: frontier is the three independent Steps; 4 is blocked.
	fr := f.derive(t, run, 3)
	wantSet(t, "A0 ready", fr.Ready, s1, s2, s3)
	wantSet(t, "A0 engaged", fr.Engaged)
	if fr.Slots != 3 {
		t.Errorf("A0 slots = %d, want 3", fr.Slots)
	}

	// A1: three slots filled.
	f.start(s1)
	f.start(s2)
	f.start(s3)
	fr = f.derive(t, run, 3)
	wantSet(t, "A1 ready", fr.Ready)
	wantSet(t, "A1 engaged", fr.Engaged, s1, s2, s3)
	if fr.Slots != 0 {
		t.Errorf("A1 slots = %d, want 0", fr.Slots)
	}

	// A2–A3: completions in an order the graph never chose.
	f.finish(s2)
	fr = f.derive(t, run, 3)
	wantSet(t, "A2 ready", fr.Ready)
	wantSet(t, "A2 engaged", fr.Engaged, s1, s3)

	f.finish(s1)
	fr = f.derive(t, run, 3)
	wantSet(t, "A3 ready", fr.Ready)
	wantSet(t, "A3 engaged", fr.Engaged, s3)

	// A4: the last edge satisfies at locally complete; 4 becomes Ready.
	f.finish(s3)
	fr = f.derive(t, run, 3)
	wantSet(t, "A4 ready", fr.Ready, s4)
	wantSet(t, "A4 engaged", fr.Engaged)

	// A5–A6.
	f.start(s4)
	fr = f.derive(t, run, 3)
	wantSet(t, "A5 engaged", fr.Engaged, s4)
	f.finish(s4)
	fr = f.derive(t, run, 3)
	wantSet(t, "A6 ready", fr.Ready)
	wantSet(t, "A6 engaged", fr.Engaged)
}

// Case B — two dependency tracks in one Stage. Cap 2.
func TestCaseB_TwoTracksConverge(t *testing.T) {
	f := newFixture(t)
	m := f.matter("case-b", "Case B")
	stage := f.stage(m, "stage-1", "the stage")
	a1 := f.step(stage, "step-01", "A1")
	a2 := f.step(stage, "step-02", "A2")
	a3 := f.step(stage, "step-03", "A3")
	b1 := f.step(stage, "step-04", "B1")
	b2 := f.step(stage, "step-05", "B2")
	b3 := f.step(stage, "step-06", "B3")
	f.depend(a2, a1)
	f.depend(a3, a2)
	f.depend(b2, b1)
	f.depend(b3, b2)
	batch := f.namedBatch("case-b-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)
	f.start(stage)

	// B0: heads of both tracks.
	fr := f.derive(t, run, 2)
	wantSet(t, "B0 ready", fr.Ready, a1, b1)

	// B1–B5: slots refill along whichever track freed one; the Stage itself
	// never occupies a slot.
	f.start(a1)
	f.start(b1)
	fr = f.derive(t, run, 2)
	wantSet(t, "B1 engaged", fr.Engaged, a1, b1)
	if fr.Slots != 0 {
		t.Errorf("B1 slots = %d, want 0", fr.Slots)
	}

	f.finish(a1)
	fr = f.derive(t, run, 2)
	wantSet(t, "B2 ready", fr.Ready, a2)
	wantSet(t, "B2 engaged", fr.Engaged, b1)

	f.start(a2)
	f.finish(b1)
	f.start(b2)
	f.finish(b2)
	f.start(b3)
	f.finish(a2)
	fr = f.derive(t, run, 2)
	wantSet(t, "B5 ready", fr.Ready, a3)
	wantSet(t, "B5 engaged", fr.Engaged, b3)

	f.start(a3)
	f.finish(b3)
	f.finish(a3)
	fr = f.derive(t, run, 2)
	wantSet(t, "B7 ready", fr.Ready)
	wantSet(t, "B7 engaged", fr.Engaged)
}

// Case C — sequence, fan-out, join. Cap 3.
func TestCaseC_SequenceFanOutJoin(t *testing.T) {
	f := newFixture(t)
	m := f.matter("case-c", "Case C")
	s := make(map[int]string, 6)
	for i := 1; i <= 6; i++ {
		s[i] = f.step(m, fmt.Sprintf("step-%02d", i), "step")
	}
	f.depend(s[2], s[1])
	for _, i := range []int{3, 4, 5} {
		f.depend(s[i], s[2])
		f.depend(s[6], s[i])
	}
	batch := f.namedBatch("case-c-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)

	fr := f.derive(t, run, 3)
	wantSet(t, "C0 ready", fr.Ready, s[1])

	f.start(s[1])
	f.finish(s[1])
	fr = f.derive(t, run, 3)
	wantSet(t, "C2 ready", fr.Ready, s[2])

	f.start(s[2])
	f.finish(s[2])
	fr = f.derive(t, run, 3)
	wantSet(t, "C4 ready", fr.Ready, s[3], s[4], s[5])

	f.start(s[3])
	f.start(s[4])
	f.start(s[5])
	f.finish(s[4])
	f.finish(s[5])
	fr = f.derive(t, run, 3)
	wantSet(t, "C7 ready", fr.Ready)
	wantSet(t, "C7 engaged", fr.Engaged, s[3])

	f.finish(s[3])
	fr = f.derive(t, run, 3)
	wantSet(t, "C8 ready", fr.Ready, s[6])
}

// Case D — four-Matter Batch, shared blocker, cap 2.
func TestCaseD_FourMatterBatchSharedBlocker(t *testing.T) {
	f := newFixture(t)
	mA := f.matter("matter-a", "A")
	mB := f.matter("matter-b", "B")
	mC := f.matter("matter-c", "C")
	mD := f.matter("matter-d", "D")
	f.depend(mC, mA)
	f.depend(mD, mA)
	batch := f.namedBatch("case-d-batch", mA, mB, mC, mD)
	run := f.startRun(batch, "run-01", mA, mB, mC, mD)

	// D0: A and B Ready at Matter grain (childless Matters are the work).
	fr := f.derive(t, run, 2)
	wantSet(t, "D0 ready", fr.Ready, mA, mB)

	// D1: both engaged; C and D blocked by A.
	f.start(mA)
	f.start(mB)
	fr = f.derive(t, run, 2)
	wantSet(t, "D1 ready", fr.Ready)
	wantSet(t, "D1 engaged", fr.Engaged, mA, mB)

	// D2: A locally complete; C and D Ready, one slot free.
	f.finish(mA)
	fr = f.derive(t, run, 2)
	wantSet(t, "D2 ready", fr.Ready, mC, mD)
	wantSet(t, "D2 engaged", fr.Engaged, mB)
	if fr.Slots != 1 {
		t.Errorf("D2 slots = %d, want 1", fr.Slots)
	}

	// D3: C takes the slot; D stays Ready — capacity, not blockage.
	f.start(mC)
	fr = f.derive(t, run, 2)
	wantSet(t, "D3 ready", fr.Ready, mD)
	wantSet(t, "D3 engaged", fr.Engaged, mB, mC)
	if fr.Slots != 0 {
		t.Errorf("D3 slots = %d, want 0", fr.Slots)
	}

	f.finish(mB)
	f.start(mD)
	f.finish(mC)
	f.finish(mD)
	fr = f.derive(t, run, 2)
	wantSet(t, "D7 ready", fr.Ready)
	wantSet(t, "D7 engaged", fr.Engaged)
}

// Only Done satisfies (D64): Paused and Canceled blockers still block; a
// tombstoned blocker does not (D63).
func TestOnlyDoneSatisfies(t *testing.T) {
	f := newFixture(t)
	m := f.matter("predicates", "Predicates")
	blocker := f.step(m, "step-01", "blocker")
	waiting := f.step(m, "step-02", "waiting")
	edge := f.depend(waiting, blocker)
	batch := f.namedBatch("pred-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)

	f.start(blocker)
	f.pause(blocker)
	fr := f.derive(t, run, 1)
	wantSet(t, "paused blocker: ready", fr.Ready)

	f.resume(blocker)
	f.cancel(blocker)
	fr = f.derive(t, run, 1)
	wantSet(t, "canceled blocker: ready", fr.Ready)

	// Tombstoning the edge's blocker takes the edge out of force.
	f.removeEdge(waiting, edge, blocker)
	fr = f.derive(t, run, 1)
	wantSet(t, "removed edge: ready", fr.Ready, waiting)
}

// A blocked Matter bars its interior from dispatch: the same predicate at
// every scale (G3), where the read surface deliberately differs.
func TestBlockedAncestorBarsInterior(t *testing.T) {
	f := newFixture(t)
	other := f.matter("elsewhere", "The blocker")
	m := f.matter("gated", "Gated")
	inner := f.step(m, "step-01", "inner work")
	f.depend(m, other)
	batch := f.namedBatch("gated-batch", m)
	run := f.startRun(batch, "run-01", m)

	fr := f.derive(t, run, 2)
	wantSet(t, "blocked matter: ready", fr.Ready)

	f.start(other)
	f.finish(other)
	fr = f.derive(t, run, 2)
	wantSet(t, "unblocked matter: ready", fr.Ready, inner)
}

// A terminal grouping takes no new work: a Planned Step under a Paused or
// Canceled ancestor is not the Run's to start.
func TestTerminalGroupingTakesNoNewWork(t *testing.T) {
	f := newFixture(t)
	m := f.matter("set-down", "Set down")
	stage := f.stage(m, "stage-1", "grouping")
	planned := f.step(stage, "step-01", "still planned")
	batch := f.namedBatch("set-down-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)
	f.start(stage)

	fr := f.derive(t, run, 1)
	wantSet(t, "live stage: ready", fr.Ready, planned)

	f.pause(stage)
	fr = f.derive(t, run, 1)
	wantSet(t, "paused stage: ready", fr.Ready)
	_ = planned
}

// Selection ignores sibling sort order: reordering changes sort keys, and
// neither Ready membership nor the selected subset moves (G1, D51).
func TestSelectionNeverReadsSortOrder(t *testing.T) {
	f := newFixture(t)
	m := f.matter("sorted", "Sorted")
	s1 := f.step(m, "step-01", "born first")
	s2 := f.step(m, "step-02", "born second")
	s3 := f.step(m, "step-03", "born third")
	batch := f.namedBatch("sorted-batch", m)
	run := f.startRun(batch, "run-01", m)
	f.start(m)

	before := f.derive(t, run, 2)
	picked := Select(before.Ready, before.Slots)

	// Reverse the presentation order.
	f.commit(store.Env{Repo: f.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeStepReordered, Subject: m,
			Payload: store.Reordered{Order: []string{s3, s2, s1}},
		}}, nil
	})

	after := f.derive(t, run, 2)
	wantSet(t, "ready after reorder", after.Ready, s1, s2, s3)
	repicked := Select(after.Ready, after.Slots)
	if len(picked) != 2 || len(repicked) != 2 {
		t.Fatalf("selection sizes = %d, %d, want 2, 2", len(picked), len(repicked))
	}
	for i := range picked {
		if picked[i].ID != repicked[i].ID {
			t.Errorf("selection moved with the sort key: %s became %s", picked[i].ID, repicked[i].ID)
		}
	}

	// The cap limits engagement but never shrinks Ready (G1).
	if len(after.Ready) != 3 {
		t.Errorf("ready = %d under cap 2, want 3 — the cap must not reduce Ready", len(after.Ready))
	}
}
