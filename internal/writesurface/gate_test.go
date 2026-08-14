package writesurface

import (
	"context"
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
