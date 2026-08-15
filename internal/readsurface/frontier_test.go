package readsurface

// Tests for step-02 (the unblocked frontier and the locally-complete/sealed
// predicate it and step-03's "finished" bucket share) and step-03's
// sealed-vs-locally-complete distinction (D13, D55, D62).

import (
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

func TestLocallyComplete_NotDoneIsNeverLocallyComplete(t *testing.T) {
	f := newFixture(t)

	planned := f.matter("planned", "Never started")
	inProgress := f.matter("in-progress", "Started, not finished")
	f.start(inProgress)
	paused := f.matter("paused", "Started, then set down")
	f.start(paused)
	f.pause(paused)
	canceled := f.matter("canceled", "Started, then abandoned")
	f.start(canceled)
	f.cancel(canceled)
	resumed := f.matter("resumed", "Paused, then picked back up — In Progress again, not Done")
	f.start(resumed)
	f.pause(resumed)
	f.resume(resumed)

	for _, id := range []string{planned, inProgress, paused, canceled, resumed} {
		n, err := f.Node(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := LocallyComplete(ctx, f.View, n)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("%s (%s) is locally complete, want false", n.Locator, n.Lifecycle)
		}
	}
}

func TestLocallyComplete_DoneWithNoOwnScaleGateDeclared(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")
	f.start(m)
	f.finish(m)
	n, err := f.Node(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := LocallyComplete(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Done with no declared gate at its own scale is not locally complete, want true (D62: empty declaration set completes at Done)")
	}
}

func TestLocallyComplete_DoneButOwnGateOpen(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "A Matter")
	f.start(m)
	f.finish(m)
	n, err := f.Node(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := LocallyComplete(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Done with its own declared gate still open is locally complete, want false")
	}

	f.closeGate(m, "reviewed-local", store.ScaleMatter)
	n, err = f.Node(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	ok, err = LocallyComplete(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Done with its own gate now closed is not locally complete, want true")
	}
}

// TestSealed_CoincidesWithLocallyCompleteAtMatterScale is D13/D55: at Matter
// scale the two completions coincide — accepted.
func TestSealed_CoincidesWithLocallyCompleteAtMatterScale(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "A Matter")
	f.start(m)
	f.finish(m)
	f.closeGate(m, "reviewed-local", store.ScaleMatter)

	n, err := f.Node(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := LocallyComplete(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := Sealed(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !lc || !sl || lc != sl {
		t.Errorf("locallyComplete=%v sealed=%v, want both true and equal at Matter scale", lc, sl)
	}
}

// TestSealed_BelowMatterScaleCanDifferFromLocallyComplete is the case the
// Matter-scale coincidence hides: a Done Step is locally complete the
// moment it's Done (nothing is declared at Step scale in this dogfood), but
// not sealed until its Matter's own gate is also closed — archivable vs.
// handoff-ready (D13).
func TestSealed_BelowMatterScaleCanDifferFromLocallyComplete(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "A Matter")
	step := f.step(m, "step-01", "A Step")
	f.start(m)
	f.start(step)
	f.finish(step)

	n, err := f.Node(ctx, step)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := LocallyComplete(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !lc {
		t.Fatal("a Done Step with nothing declared at Step scale must be locally complete")
	}
	sl, err := Sealed(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if sl {
		t.Error("sealed=true before the enclosing Matter's own gate is closed, want false")
	}

	f.closeGate(m, "reviewed-local", store.ScaleMatter)
	n, err = f.Node(ctx, step)
	if err != nil {
		t.Fatal(err)
	}
	sl, err = Sealed(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !sl {
		t.Error("sealed=false after the enclosing Matter's own gate is closed, want true")
	}
}

// TestFrontier_ReadyWhenUnblocked confirms the trivial case: a Planned node
// with no edges at all is ready.
func TestFrontier_ReadyWhenUnblocked(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")

	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want none", blocked)
	}
	if len(ready) != 1 || ready[0].ID != m {
		t.Errorf("ready = %+v, want just %s", ready, m)
	}
}

// TestFrontier_BlockedByAnUnfinishedBlocker is D29/D64's ordinary case: a
// blocker that has not reached locally complete still blocks.
func TestFrontier_BlockedByAnUnfinishedBlocker(t *testing.T) {
	f := newFixture(t)
	first := f.matter("first", "Has to go first")
	second := f.matter("second", "Waits for it")
	f.depend(second, first)

	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != first {
		t.Errorf("ready = %+v, want just %s", ready, first)
	}
	if len(blocked) != 1 || blocked[0].Node.ID != second || len(blocked[0].Blockers) != 1 || blocked[0].Blockers[0].ID != first {
		t.Errorf("blocked = %+v, want %s blocked by %s", blocked, second, first)
	}
}

// TestFrontier_SatisfiedOnceBlockerIsLocallyComplete confirms the edge
// clears once (and only once) the blocker reaches Done + its own gates
// closed — not merely Done (D29).
func TestFrontier_SatisfiedOnceBlockerIsLocallyComplete(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	first := f.matter("first", "Has to go first")
	second := f.matter("second", "Waits for it")
	f.depend(second, first)

	f.start(first)
	f.finish(first)
	_, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 {
		t.Fatalf("Done but gate still open must still block, blocked = %+v", blocked)
	}

	f.closeGate(first, "reviewed-local", store.ScaleMatter)
	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want none once the blocker is locally complete", blocked)
	}
	if len(ready) != 1 || ready[0].ID != second {
		t.Errorf("ready = %+v, want just %s", ready, second)
	}
}

// TestFrontier_CanceledBlockerStillBlocks is D64 in the other direction: a
// Canceled blocker still blocks — only Done satisfies.
func TestFrontier_CanceledBlockerStillBlocks(t *testing.T) {
	f := newFixture(t)
	first := f.matter("first", "Abandoned mid-flight")
	second := f.matter("second", "Waits for it anyway")
	f.depend(second, first)

	f.start(first)
	f.cancel(first)

	_, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Node.ID != second {
		t.Errorf("blocked = %+v, want %s still blocked by its Canceled blocker", blocked, second)
	}
}

// TestFrontier_TombstonedBlockerDoesNotBlock is D64's other half: cancel
// keeps the question alive, remove answers it — a removed blocker's edge is
// out of force (D63) and BlockedBy never returns it.
func TestFrontier_TombstonedBlockerDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter that hosts a removable Step")
	first := f.step(m, "step-01", "Removed before it ever finished")
	second := f.step(m, "step-02", "Waits for it")
	f.depend(second, first)

	_, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 {
		t.Fatalf("before removal, blocked = %+v, want step-02 blocked", blocked)
	}

	f.remove(first)

	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want none — a tombstoned blocker's edge is out of force", blocked)
	}
	var sawSecond bool
	for _, n := range ready {
		if n.ID == second {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Errorf("ready = %+v, want step-02 (%s) among it once its blocker is removed", ready, second)
	}
}

// TestFrontier_RemovedEdgeDoesNotBlock is D44/D64 by the direct route: a
// `dependency.removed` tombstones the edge itself (never the nodes it named),
// and once it's gone the relation it recorded is gone too — cancel keeps
// the question alive, remove answers it.
func TestFrontier_RemovedEdgeDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	first := f.matter("first", "Has to go first")
	second := f.matter("second", "Waits for it, until the edge is removed")
	edge := f.depend(second, first)

	_, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 {
		t.Fatalf("before removal, blocked = %+v, want second blocked", blocked)
	}

	f.removeEdge(second, edge, first)

	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want none — the edge itself was removed", blocked)
	}
	var sawSecond bool
	for _, n := range ready {
		if n.ID == second {
			sawSecond = true
		}
	}
	if !sawSecond {
		t.Errorf("ready = %+v, want %s among it once its edge is removed", ready, second)
	}
}

// TestCollapseFinished_FullySealedMatterCollapsesToOneRow: a sealed Matter
// with a sealed Step child (no gates declared, so Done alone seals both)
// stands for its whole subtree.
func TestCollapseFinished_FullySealedMatterCollapsesToOneRow(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")
	step := f.step(m, "step-01", "A Step")
	f.start(m)
	f.start(step)
	f.finish(step)
	f.finish(m)

	finished, err := FinishedNodes(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	collapsed, err := CollapseFinished(ctx, f.View, finished, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(collapsed) != 1 || collapsed[0].Node.ID != m {
		t.Errorf("collapsed = %+v, want just the sealed Matter %s", collapsed, m)
	}
}

// TestCollapseFinished_SealedStageInOpenMatterShowsBareRow: the Matter is
// still In Progress (not Done, so it's not in the finished list at all), but
// its Stage is sealed and absorbs its own sealed Step.
func TestCollapseFinished_SealedStageInOpenMatterShowsBareRow(t *testing.T) {
	f := newFixture(t)
	m := f.matter("m", "A Matter")
	stage := f.stage(m, "stage-a", "A Stage")
	step := f.step(stage, "step-01", "A Step")
	f.start(m)
	f.start(stage)
	f.start(step)
	f.finish(step)
	f.finish(stage)

	finished, err := FinishedNodes(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	collapsed, err := CollapseFinished(ctx, f.View, finished, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(collapsed) != 1 || collapsed[0].Node.ID != stage {
		t.Errorf("collapsed = %+v, want just the sealed Stage %s (its Step dropped, the open Matter never in the finished list)", collapsed, stage)
	}
}

// TestCollapseFinished_PartlySealedStageStaysExpanded: one Step under the
// Stage is sealed (its own declared gate closed) and gets dropped, but its
// sibling is Done and only locally complete (own gate still open) — an
// unsealed entry is never dropped, so it stays beside the Stage row.
func TestCollapseFinished_PartlySealedStageStaysExpanded(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-step", store.ScaleStep)
	m := f.matter("m", "A Matter")
	stage := f.stage(m, "stage-a", "A Stage")
	sealedStep := f.step(stage, "step-01", "Sealed step")
	openStep := f.step(stage, "step-02", "Still awaiting its own gate")
	f.start(m)
	f.start(stage)
	f.start(sealedStep)
	f.finish(sealedStep)
	f.closeGate(sealedStep, "reviewed-step", store.ScaleStep)
	f.start(openStep)
	f.finish(openStep)
	f.finish(stage)

	finished, err := FinishedNodes(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	collapsed, err := CollapseFinished(ctx, f.View, finished, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range collapsed {
		ids[c.Node.ID] = true
	}
	if !ids[stage] {
		t.Errorf("collapsed = %+v, want the Stage %s present", collapsed, stage)
	}
	if ids[sealedStep] {
		t.Errorf("collapsed = %+v, want the sealed Step %s dropped (its sealed ancestor Stage is in the list)", collapsed, sealedStep)
	}
	if !ids[openStep] {
		t.Errorf("collapsed = %+v, want the unsealed Step %s to stay — it is never dropped", collapsed, openStep)
	}
}

// TestCollapseFinished_AwaitingGateChildStillSurfaces: a Done child whose own
// enclosing Matter gate is still open is locally complete but never sealed,
// so it is never dropped and always surfaces.
func TestCollapseFinished_AwaitingGateChildStillSurfaces(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "A Matter")
	step := f.step(m, "step-01", "A Step")
	f.start(m)
	f.start(step)
	f.finish(step)

	finished, err := FinishedNodes(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	collapsed, err := CollapseFinished(ctx, f.View, finished, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(collapsed) != 1 || collapsed[0].Node.ID != step || collapsed[0].Sealed {
		t.Errorf("collapsed = %+v, want the awaiting-gate Step %s present and not sealed", collapsed, step)
	}
}

// TestSealedAt_LaterOfFinishAndGateClose covers both orders: finish then
// gate-close, and gate-close then finish (order-independence, D62).
func TestSealedAt_LaterOfFinishAndGateClose(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	m := f.matter("m", "Finish then gate-close")
	f.start(m)
	finishEv := f.finish(m)
	time.Sleep(2 * time.Millisecond)
	gateEv := f.closeGate(m, "reviewed-local", store.ScaleMatter)

	n, err := f.Node(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	at, ok, err := SealedAt(ctx, f.View, n)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("SealedAt ok = false, want true")
	}
	if !at.Equal(gateEv.OccurredAt) {
		t.Errorf("SealedAt = %v, want the later gate-close time %v (finish was %v)", at, gateEv.OccurredAt, finishEv.OccurredAt)
	}

	f2 := newFixture(t)
	f2.declareGate("reviewed-local", store.ScaleMatter)
	m2 := f2.matter("m", "Gate-close then finish")
	f2.start(m2)
	gateEv2 := f2.closeGate(m2, "reviewed-local", store.ScaleMatter)
	time.Sleep(2 * time.Millisecond)
	finishEv2 := f2.finish(m2)

	n2, err := f2.Node(ctx, m2)
	if err != nil {
		t.Fatal(err)
	}
	at2, ok, err := SealedAt(ctx, f2.View, n2)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("SealedAt ok = false, want true")
	}
	if !at2.Equal(finishEv2.OccurredAt) {
		t.Errorf("SealedAt = %v, want the later finish time %v (gate-close was %v)", at2, finishEv2.OccurredAt, gateEv2.OccurredAt)
	}
}

// TestFrontier_CreationOrder pins the presentation-only ordering (D51):
// ready nodes come back in the order they were born.
func TestFrontier_CreationOrder(t *testing.T) {
	f := newFixture(t)
	a := f.matter("a", "First born")
	b := f.matter("b", "Second born")
	c := f.matter("c", "Third born")

	ready, _, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 3 || ready[0].ID != a || ready[1].ID != b || ready[2].ID != c {
		t.Errorf("ready = %+v, want [a b c] in creation order", ready)
	}
}
