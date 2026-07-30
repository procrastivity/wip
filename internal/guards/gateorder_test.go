package guards_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/store"
)

func TestViolations_MonotonicOrderIsClean(t *testing.T) {
	declared := []store.GateDeclaration{
		{Gate: "verified", Scale: store.ScaleStep},
		{Gate: "reviewed-local", Scale: store.ScaleStep},
		{Gate: "reviewed", Scale: store.ScaleStage},
		{Gate: "ci-green", Scale: store.ScaleMatter},
	}
	if got := guards.Violations(declared); len(got) != 0 {
		t.Fatalf("Violations = %+v, want none (order is non-decreasing)", got)
	}
}

func TestViolations_CatchesEveryViolatingPair(t *testing.T) {
	// vocabulary step-12's own scenario, plus a second, independent
	// violation (D65: exhaustive per run, not just the first).
	declared := []store.GateDeclaration{
		{Gate: "reviewed-local", Scale: store.ScaleMatter},
		{Gate: "ci-green", Scale: store.ScaleStep},
		{Gate: "verified", Scale: store.ScaleMatter},
	}
	got := guards.Violations(declared)
	if len(got) != 2 {
		t.Fatalf("Violations = %+v, want exactly 2", got)
	}
	want := map[[2]string]bool{
		{"reviewed-local", "ci-green"}: false, // reviewed-local (matter) precedes ci-green (step): finer, violates
		{"verified", "ci-green"}:       false, // verified (matter) precedes ci-green (step): finer, violates
	}
	for _, v := range got {
		key := [2]string{v.Earlier.Gate, v.Later.Gate}
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected violation %+v", v)
			continue
		}
		want[key] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("expected violation %v not reported", k)
		}
	}
}

func TestWouldViolate_DeclareTimePrecondition(t *testing.T) {
	declared := []store.GateDeclaration{
		{Gate: "reviewed-local", Scale: store.ScaleMatter},
	}

	// vocabulary step-12's exact scenario: declaring ci-green at Step scale
	// against reviewed-local already bound at Matter scale.
	other, violates := guards.WouldViolate(declared, "ci-green", store.ScaleStep)
	if !violates {
		t.Fatal("WouldViolate = false, want true (ci-green at step violates against reviewed-local at matter)")
	}
	if other.Gate != "reviewed-local" || other.Scale != store.ScaleMatter {
		t.Errorf("other = %+v, want reviewed-local at matter", other)
	}

	if _, violates := guards.WouldViolate(declared, "ci-green", store.ScaleMatter); violates {
		t.Error("declaring ci-green at matter scale should not violate against reviewed-local at matter")
	}
}

func TestWouldViolate_RedeclareUpdatesInPlace(t *testing.T) {
	// Redeclaring reviewed-local itself (at a new scale) must never be
	// compared against its own prior binding — that would make every
	// redeclare a false-positive violation against itself.
	declared := []store.GateDeclaration{
		{Gate: "reviewed-local", Scale: store.ScaleStage},
	}
	if _, violates := guards.WouldViolate(declared, "reviewed-local", store.ScaleMatter); violates {
		t.Error("redeclaring a gate coarser than its own prior binding should never violate against itself")
	}
}

func TestViolationMessage_MatchesVocabularyStep12(t *testing.T) {
	subject := store.GateDeclaration{Gate: "ci-green", Scale: store.ScaleStep}
	other := store.GateDeclaration{Gate: "reviewed-local", Scale: store.ScaleMatter}
	want := "refused — ci-green at step scale would violate gate-order monotonicity against reviewed-local at matter scale"
	if got := guards.ViolationMessage(subject, other); got != want {
		t.Errorf("ViolationMessage = %q, want %q", got, want)
	}
}

func TestCheckGateOrder_AuditFindsAPreexistingViolation(t *testing.T) {
	s := newStore(t)
	repo := newRepo(t, s)

	if err := s.DeclareGate(ctx, repo, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatalf("DeclareGate: %v", err)
	}
	// Bypasses writesurface.DeclareGate's own precondition (the very thing
	// this Matter just wired in) to land a pre-existing violation directly —
	// the audit's job is exactly for state that arrived some other way.
	if err := s.DeclareGate(ctx, repo, "ci-green", store.ScaleStep); err != nil {
		t.Fatalf("DeclareGate: %v", err)
	}

	findings, err := guards.CheckGateOrder(ctx, s, "")
	if err != nil {
		t.Fatalf("CheckGateOrder: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly 1", findings)
	}
	if findings[0].Code != "refusal.gate-order-violation" {
		t.Errorf("code = %q, want %q", findings[0].Code, "refusal.gate-order-violation")
	}
	want := "found: ci-green at step scale violates gate-order monotonicity against reviewed-local at matter scale"
	if findings[0].Message != want {
		t.Errorf("message = %q, want %q", findings[0].Message, want)
	}
}

func TestCheckGateOrder_CleanRepoReportsNothing(t *testing.T) {
	s := newStore(t)
	repo := newRepo(t, s)
	if err := s.DeclareGate(ctx, repo, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatalf("DeclareGate: %v", err)
	}

	findings, err := guards.CheckGateOrder(ctx, s, "")
	if err != nil {
		t.Fatalf("CheckGateOrder: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none", findings)
	}
}
