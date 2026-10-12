package wipd

import (
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestSchedulerResumeFrontierAppliesRetainedAskHandling(t *testing.T) {
	firstMatter := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	secondMatter := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	state := schedulerResumeState{
		matters:        []string{firstMatter, secondMatter},
		statuses:       map[string]string{firstMatter: "in-progress", secondMatter: "planned"},
		skippedMatters: map[string]bool{firstMatter: true},
	}
	frontier, disposition := schedulerResumeFrontier(state, CommandStartSnapshot{})
	if frontier != secondMatter || disposition != "ready" {
		t.Fatalf("retained skip did not advance to the next Matter: frontier=%q disposition=%q", frontier, disposition)
	}

	state.policyHalt = true
	frontier, disposition = schedulerResumeFrontier(state, CommandStartSnapshot{})
	if frontier != "" || disposition != "policy-halt" {
		t.Fatalf("retained halt did not park the Run: frontier=%q disposition=%q", frontier, disposition)
	}

	state.policyHalt = false
	state.skippedMatters[secondMatter] = true
	frontier, disposition = schedulerResumeFrontier(state, CommandStartSnapshot{})
	if frontier != "" || disposition != "policy-skipped" {
		t.Fatalf("all-skipped Run did not remain parked: frontier=%q disposition=%q", frontier, disposition)
	}

	input := operation.SchedulerHookInput{Reason: "blocked", Blocked: "skip"}
	if !schedulerResumeAskMatchesPolicy(input, "skip") || schedulerResumeAskMatchesPolicy(input, "halt") ||
		schedulerResumeAskMatchesPolicy(input, "continue") {
		t.Fatal("Ask policy validation accepted a handling that differs from its configured blocked policy")
	}
}
