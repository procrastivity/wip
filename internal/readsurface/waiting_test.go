package readsurface

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestWaitingSuppressesPlanDependenciesBeneathReadyMatter(t *testing.T) {
	f := newFixture(t)
	matter := f.matter("planned", "A ready Matter with a sequenced plan")
	firstStage := f.stage(matter, "first-stage", "First stage")
	secondStage := f.stage(matter, "second-stage", "Second stage")
	firstStep := f.step(firstStage, "step-01", "First step")
	secondStep := f.step(firstStage, "step-02", "Second step")
	f.depend(secondStage, firstStage)
	f.depend(secondStep, firstStep)

	if got := waitingForFixture(t, f); len(got) != 0 {
		t.Fatalf("waiting = %+v, want no future plan waits beneath the ready Matter", got)
	}
}

func TestWaitingSuppressesFutureSequencingWhileLeafWorkIsInProgress(t *testing.T) {
	f := newFixture(t)
	matter := f.matter("active", "A Matter with active leaf work")
	stage := f.stage(matter, "build", "Build")
	current := f.step(stage, "step-01", "Current work")
	next := f.step(stage, "step-02", "Future work")
	f.depend(next, current)
	f.start(matter)
	f.start(stage)
	f.start(current)

	if got := waitingForFixture(t, f); len(got) != 0 {
		t.Fatalf("waiting = %+v, want the active leaf to suppress future sibling sequencing", got)
	}
}

func TestWaitingKeepsBlockedStepUnderActiveContainersAndOmitsInheritedGate(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	matter := f.matter("stalled", "A stalled active Matter")
	stage := f.stage(matter, "build", "Build")
	finished := f.step(stage, "step-01", "Finished child")
	blocked := f.step(stage, "step-02", "Blocked child")
	blocker := f.matter("external", "External blocker")
	f.depend(blocked, blocker)
	f.start(matter)
	f.start(stage)
	f.start(finished)
	f.finish(finished)

	waiting := waitingForFixture(t, f)
	if len(waiting) != 1 || waiting[0].Matter.ID != matter {
		t.Fatalf("waiting = %+v, want the stalled Matter despite its active container nodes", waiting)
	}
	if len(waiting[0].Gates) != 0 {
		t.Fatalf("gates = %+v, want the active Matter's inherited gate omitted", waiting[0].Gates)
	}
	if got := waiting[0].Dependencies; len(got) != 1 || got[0].Blocker.ID != blocker || !sameNodeIDs(got[0].Held, blocked) {
		t.Fatalf("dependencies = %+v, want the blocked Step retained", got)
	}
}

func TestWaitingSelectsTheShallowestBlockedFrontier(t *testing.T) {
	f := newFixture(t)
	blocker := f.matter("external", "External blocker")
	f.start(blocker)
	target := f.matter("target", "Blocked at every level")
	stage := f.stage(target, "stage", "Blocked stage")
	step := f.step(stage, "step-01", "Blocked step")
	f.depend(target, blocker)
	f.depend(stage, blocker)
	f.depend(step, blocker)

	waiting := waitingForFixture(t, f)
	if len(waiting) != 1 || waiting[0].Matter.ID != target {
		t.Fatalf("waiting = %+v, want one target Matter group", waiting)
	}
	if got := waiting[0].Dependencies; len(got) != 1 || got[0].Blocker.ID != blocker || !sameNodeIDs(got[0].Held, target) {
		t.Fatalf("dependencies = %+v, want only the shallowest blocked Matter", got)
	}
}

func TestWaitingPreservesExactNodesAtTheActionableFrontier(t *testing.T) {
	f := newFixture(t)
	held := f.matter("held", "Several pieces of work are blocked")
	stage := f.stage(held, "build", "Build")
	heldFirst := f.step(stage, "step-01", "Held by one external node")
	heldSecond := f.step(stage, "step-02", "Held by that external node too")
	heldThird := f.step(stage, "step-03", "Held by two nodes in one Matter")
	heldWithin := f.step(stage, "step-04", "Held inside its own Matter")
	internalBlocker := f.step(stage, "step-05", "The intra-Matter blocker")
	f.start(held)
	f.start(stage)
	f.start(internalBlocker)
	f.pause(internalBlocker)

	blockers := f.matter("blockers", "Hosts distinct blocker nodes")
	externalFirst := f.step(blockers, "step-01", "First external blocker")
	externalSecond := f.step(blockers, "step-02", "Second external blocker")
	for _, blocker := range []string{externalFirst, externalSecond} {
		f.start(blocker)
		f.pause(blocker)
	}
	f.depend(heldFirst, externalFirst)
	f.depend(heldSecond, externalFirst)
	f.depend(heldThird, externalFirst)
	f.depend(heldThird, externalSecond)
	f.depend(heldWithin, internalBlocker)

	waiting := waitingForFixture(t, f)
	if len(waiting) != 1 || waiting[0].Matter.ID != held {
		t.Fatalf("waiting = %+v, want one held Matter group", waiting)
	}
	dependencies := waiting[0].Dependencies
	if len(dependencies) != 3 {
		t.Fatalf("dependencies = %+v, want two distinct external blockers and one intra-Matter blocker", dependencies)
	}
	if dependencies[0].Blocker.ID != externalFirst || !sameNodeIDs(dependencies[0].Held, heldFirst, heldSecond, heldThird) {
		t.Errorf("first dependency = %+v, want externalFirst holding three exact children", dependencies[0])
	}
	if dependencies[1].Blocker.ID != externalSecond || !sameNodeIDs(dependencies[1].Held, heldThird) {
		t.Errorf("second dependency = %+v, want externalSecond kept distinct despite sharing a Matter", dependencies[1])
	}
	if dependencies[2].Blocker.ID != internalBlocker || !sameNodeIDs(dependencies[2].Held, heldWithin) {
		t.Errorf("intra-Matter dependency = %+v, want both direct node identities preserved", dependencies[2])
	}
}

func TestWaitingIncludesOnlyOpenGatesWhoseSubjectIsDone(t *testing.T) {
	f := newFixture(t)
	f.declareGate("reviewed-local", store.ScaleMatter)
	done := f.matter("done", "Done and awaiting review")
	f.start(done)
	f.finish(done)
	unfinished := f.matter("unfinished", "A declaration alone is not a wait")
	child := f.step(unfinished, "step-01", "Finished under an active Matter")
	f.start(unfinished)
	f.start(child)
	f.finish(child)

	waiting := waitingForFixture(t, f)
	if len(waiting) != 1 || waiting[0].Matter.ID != done {
		t.Fatalf("waiting = %+v, want only the Done Matter", waiting)
	}
	if got := waiting[0].Gates; len(got) != 1 || got[0].Gate != "reviewed-local" ||
		got[0].Subject.ID != done || got[0].Owner != store.ActorHuman || got[0].State != store.GateRequirementOpen {
		t.Fatalf("gates = %+v, want one canonical actionable gate", got)
	}
}

func TestWaitingOmitsSatisfiedDependencies(t *testing.T) {
	f := newFixture(t)
	blocker := f.matter("blocker", "A satisfied blocker")
	blocked := f.matter("blocked", "No longer blocked")
	f.depend(blocked, blocker)
	f.start(blocker)
	f.finish(blocker)

	if got := waitingForFixture(t, f); len(got) != 0 {
		t.Fatalf("waiting = %+v, want no wait from a satisfied dependency", got)
	}
}

func waitingForFixture(t *testing.T, f *fixture) []WaitingMatter {
	t.Helper()
	inProgress, err := f.InProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := FinishedNodes(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	ready, blocked, err := Frontier(ctx, f.View)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := Waiting(ctx, f.View, f.Repo, inProgress, ready, finished, blocked)
	if err != nil {
		t.Fatal(err)
	}
	return waiting
}

func sameNodeIDs(nodes []store.Node, want ...string) bool {
	if len(nodes) != len(want) {
		return false
	}
	for i := range nodes {
		if nodes[i].ID != want[i] {
			return false
		}
	}
	return true
}
