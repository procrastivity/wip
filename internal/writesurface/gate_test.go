package writesurface

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestRepairGateExemptionPreservesHistoryAcrossRebuildAndReopen(t *testing.T) {
	f := newBatchFixture(t, "legacy-sealed")
	ctx := context.Background()
	for _, gate := range []string{"reviewed-local", "verified"} {
		if err := DeclareGate(ctx, f.s, f.env.Repo, gate, store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "legacy-sealed"); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "legacy-sealed"); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseGateWithEnv(ctx, f.s, store.ActorHuman, f.env, "reviewed-local", "legacy-sealed"); err != nil {
		t.Fatal(err)
	}

	before := lenEvents(t, f.s)
	n, err := RepairGateExemption(ctx, f.s, f.env.Repo, "verified", "legacy-sealed")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != f.matter {
		t.Fatalf("repaired node = %s, want %s", n.ID, f.matter)
	}
	if got := lenEvents(t, f.s); got != before {
		t.Fatalf("repair changed event count from %d to %d", before, got)
	}
	if _, err := RepairGateExemption(ctx, f.s, f.env.Repo, "verified", "legacy-sealed"); err != nil {
		t.Fatalf("repeat repair: %v", err)
	}
	if got := lenEvents(t, f.s); got != before {
		t.Fatalf("repeat repair changed event count from %d to %d", before, got)
	}

	wantExempt := func(label string, s *store.Store) {
		t.Helper()
		exempt, err := s.GateExempt(ctx, f.env.Repo, f.matter, "verified")
		if err != nil || !exempt {
			t.Fatalf("%s exemption = %v, err=%v", label, exempt, err)
		}
		satisfied, err := s.GateSatisfied(ctx, f.env.Repo, f.matter, "verified")
		if err != nil || !satisfied {
			t.Fatalf("%s satisfaction = %v, err=%v", label, satisfied, err)
		}
	}
	wantExempt("before rebuild", f.s)
	if err := f.s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	wantExempt("after rebuild", f.s)

	path := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	wantExempt("after reopen", reopened)
	if got := lenEvents(t, reopened); got != before {
		t.Fatalf("rebuild and reopen changed event count from %d to %d", before, got)
	}
}

func TestRepairGateExemptionRefusesUnsafeTargetsWithoutEvents(t *testing.T) {
	ctx := context.Background()

	t.Run("not Done", func(t *testing.T) {
		f := newBatchFixture(t, "planned")
		if err := DeclareGate(ctx, f.s, f.env.Repo, "verified", store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
		wantRepairCode(t, f, "verified", "planned", "refusal.gate-repair-lifecycle")
	})

	t.Run("wrong scale", func(t *testing.T) {
		f := newBatchFixture(t, "wrong-scale")
		step, err := CreateStep(ctx, f.s, store.ActorHuman, f.env.Repo, "wrong-scale", "Step")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "wrong-scale/"+step.Locator); err != nil {
			t.Fatal(err)
		}
		if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "wrong-scale/"+step.Locator); err != nil {
			t.Fatal(err)
		}
		if err := DeclareGate(ctx, f.s, f.env.Repo, "verified", store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
		wantRepairCode(t, f, "verified", "wrong-scale/"+step.Locator, "refusal.gate-repair-scale")
	})

	t.Run("other gate open", func(t *testing.T) {
		f := newBatchFixture(t, "not-sealed")
		for _, gate := range []string{"reviewed-local", "verified"} {
			if err := DeclareGate(ctx, f.s, f.env.Repo, gate, store.ScaleMatter); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "not-sealed"); err != nil {
			t.Fatal(err)
		}
		if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "not-sealed"); err != nil {
			t.Fatal(err)
		}
		wantRepairCode(t, f, "verified", "not-sealed", "refusal.gate-repair-prerequisite")
	})
}

func wantRepairCode(t *testing.T, f batchFixture, gate, locator, want string) {
	t.Helper()
	before := lenEvents(t, f.s)
	_, err := RepairGateExemption(context.Background(), f.s, f.env.Repo, gate, locator)
	var structured *wiperr.Error
	if !errors.As(err, &structured) || structured.Code != want {
		t.Fatalf("repair error = %v, want %s", err, want)
	}
	if got := lenEvents(t, f.s); got != before {
		t.Fatalf("refused repair changed event count from %d to %d", before, got)
	}
}

func TestDismissGateUsesOneEventAndSealsOnlyOnTheFinalOpenGate(t *testing.T) {
	f := newBatchFixture(t, "dismissal-boundary")
	ctx := context.Background()
	for _, gate := range []string{"approve", "review"} {
		if err := DeclareGate(ctx, f.s, f.env.Repo, gate, store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "dismissal-boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "dismissal-boundary"); err != nil {
		t.Fatal(err)
	}

	before := lenEvents(t, f.s)
	first, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "approve", "dismissal-boundary", "the approving reviewer is unavailable")
	if err != nil {
		t.Fatal(err)
	}
	if first.Node.ID != f.matter || first.BecameSealed {
		t.Fatalf("first dismissal result = %#v, want an unsealed Matter", first)
	}
	if got := lenEvents(t, f.s); got != before+1 {
		t.Fatalf("first dismissal appended %d events, want exactly one", got-before)
	}
	events, err := f.s.EventsOfSubject(ctx, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != store.TypeGateDismissed || last.Subject != f.matter || last.Actor != store.ActorHuman {
		t.Fatalf("dismissal event = %+v, want gate.dismissed by human on the Matter", last)
	}
	var payload store.GateDismissed
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Gate != "approve" || payload.Scale != store.ScaleMatter || payload.Reason != "the approving reviewer is unavailable" {
		t.Fatalf("dismissal payload = %+v", payload)
	}

	node, err := f.s.Node(ctx, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := f.s.NodeCompletion(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Sealed || len(completion.Pending) != 1 || completion.Pending[0].Gate != "review" {
		t.Fatalf("completion after first dismissal = %+v, want review pending and Matter unsealed", completion)
	}

	final, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "dismissal-boundary", "the review system is unavailable during the incident")
	if err != nil {
		t.Fatal(err)
	}
	if final.Node.ID != f.matter || !final.BecameSealed {
		t.Fatalf("final dismissal result = %#v, want the seal transition", final)
	}
	if got := lenEvents(t, f.s); got != before+2 {
		t.Fatalf("two dismissals appended %d events, want exactly two", got-before)
	}
	node, err = f.s.Node(ctx, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	completion, err = f.s.NodeCompletion(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if !completion.Sealed || len(completion.Pending) != 0 {
		t.Fatalf("completion after final dismissal = %+v, want sealed with no pending gates", completion)
	}

	requirements, err := f.s.EffectiveGateRequirements(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range requirements {
		if requirement.State != store.GateRequirementDismissed || requirement.DismissedBy != store.ActorHuman ||
			requirement.DismissedAt == nil || requirement.DismissalReason == "" || requirement.ClosedBy != "" || requirement.ClosedAt != nil {
			t.Errorf("dismissed requirement = %+v, want dismissal-only metadata", requirement)
		}
	}
}

func TestDismissGateRefusesBlankReasonNonDoneAndRepeatedDismissalWithoutEvents(t *testing.T) {
	f := newBatchFixture(t, "dismissal-refusals")
	ctx := context.Background()
	if err := DeclareGate(ctx, f.s, f.env.Repo, "review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}

	assertRefused := func(label string, fn func() error, wantCode string) {
		t.Helper()
		before := lenEvents(t, f.s)
		err := fn()
		var structured *wiperr.Error
		if !errors.As(err, &structured) || structured.Code != wantCode {
			t.Fatalf("%s error = %v, want %s", label, err, wantCode)
		}
		if got := lenEvents(t, f.s); got != before {
			t.Fatalf("%s refusal changed event count from %d to %d", label, before, got)
		}
	}

	assertRefused("blank reason", func() error {
		_, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "dismissal-refusals", " \t")
		return err
	}, "validation.missing-reason")

	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "dismissal-refusals"); err != nil {
		t.Fatal(err)
	}
	assertRefused("in-progress target", func() error {
		_, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "dismissal-refusals", "a real reason")
		return err
	}, "refusal.gate-dismissal-lifecycle")
	if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "dismissal-refusals"); err != nil {
		t.Fatal(err)
	}
	if _, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "dismissal-refusals", "a real reason"); err != nil {
		t.Fatal(err)
	}
	assertRefused("repeated dismissal", func() error {
		_, err := DismissGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "dismissal-refusals", "another reason")
		return err
	}, "refusal.gate-already-satisfied")
}
