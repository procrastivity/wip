package writesurface

import (
	"context"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestSealTransitionReportsOnlyTheFinalMatterGate(t *testing.T) {
	f := newBatchFixture(t, "final-gate")
	ctx := context.Background()
	for _, gate := range []string{"review", "approve"} {
		if err := DeclareGate(ctx, f.s, f.env.Repo, gate, store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "final-gate"); err != nil {
		t.Fatal(err)
	}
	finished, err := FinishWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "final-gate")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Node.ID != f.matter || finished.BecameSealed {
		t.Fatalf("finish result = %#v, want Matter without seal transition", finished)
	}

	nonfinal, err := CloseGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "final-gate")
	if err != nil {
		t.Fatal(err)
	}
	if nonfinal.Node.ID != f.matter || nonfinal.BecameSealed {
		t.Fatalf("nonfinal gate result = %#v, want Matter without seal transition", nonfinal)
	}

	final, err := CloseGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "approve", "final-gate")
	if err != nil {
		t.Fatal(err)
	}
	if final.Node.ID != f.matter || !final.BecameSealed {
		t.Fatalf("final gate result = %#v, want Matter with seal transition", final)
	}
}

func TestSealTransitionReportsFinishAfterGateClosedFirst(t *testing.T) {
	f := newBatchFixture(t, "gate-first")
	ctx := context.Background()
	if err := DeclareGate(ctx, f.s, f.env.Repo, "review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-first"); err != nil {
		t.Fatal(err)
	}

	closed, err := CloseGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "review", "gate-first")
	if err != nil {
		t.Fatal(err)
	}
	if closed.BecameSealed {
		t.Fatalf("gate before finish result = %#v, want no seal transition", closed)
	}

	finished, err := FinishWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "gate-first")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Node.ID != f.matter || !finished.BecameSealed {
		t.Fatalf("finish after gate result = %#v, want Matter with seal transition", finished)
	}
}

func TestSealTransitionReportsFalseForChildGate(t *testing.T) {
	f := newBatchFixture(t, "child-gate")
	ctx := context.Background()
	step, err := CreateStep(ctx, f.s, store.ActorHuman, f.env.Repo, "child-gate", "Child")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeclareGate(ctx, f.s, f.env.Repo, "step-review", store.ScaleStep); err != nil {
		t.Fatal(err)
	}

	result, err := CloseGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "step-review", "child-gate/"+step.Locator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.ID != step.ID || result.BecameSealed {
		t.Fatalf("child gate result = %#v, want child without Matter seal transition", result)
	}
}

func TestCloseGateRefusesNodeAtWrongDeclaredScale(t *testing.T) {
	f := newBatchFixture(t, "wrong-close-scale")
	ctx := context.Background()
	step, err := CreateStep(ctx, f.s, store.ActorHuman, f.env.Repo, "wrong-close-scale", "Child")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeclareGate(ctx, f.s, f.env.Repo, "matter-review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}

	before := lenEvents(t, f.s)
	_, err = CloseGateWithEnvResult(ctx, f.s, store.ActorHuman, f.env, "matter-review", "wrong-close-scale/"+step.Locator)
	var structured *wiperr.Error
	if !errors.As(err, &structured) || structured.Code != "refusal.gate-scale" {
		t.Fatalf("wrong-scale close error = %v, want refusal.gate-scale", err)
	}
	if got := lenEvents(t, f.s); got != before {
		t.Fatalf("wrong-scale close changed event count from %d to %d", before, got)
	}
}
