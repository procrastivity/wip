package guards

// step-03: the gate-order-monotonicity check (D12). Unlike the cycle check,
// `guards` owns this check's *implementation* — `write-surface` step-01 of
// gates-and-dependencies left the call site open, naming it settled: "Gate-
// order monotonicity (D12) is a `doctor` check owned by `guards`, called at
// declare time." Two callers, one function here (Violations), mirroring
// `schema`'s cycle-check pattern with ownership reversed: `write-surface`'s
// `wip gate declare` calls WouldViolate (itself built on Violations) as an
// add-time precondition; `doctor`'s audit calls Violations directly over
// every declared binding.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
)

// gateOrderCode is vocabulary step-12's ratified gate-order-violation
// refusal code, reused verbatim for the audit finding.
const gateOrderCode = "refusal.gate-order-violation"

// gateOrder is the fixed sequence position of each real, declarable gate
// name (MODEL §2.3, D12): verify → review-local → push → forge-review →
// ci-green. `push` is an ordering landmark in that chain, not a gate, and is
// never declared or checked here.
var gateOrder = map[string]int{
	"verified":       0,
	"reviewed-local": 1,
	"reviewed":       2,
	"ci-green":       3,
}

// scaleRank orders scale from finest to coarsest — the direction gate order
// must never decrease across, per D12's invariant.
var scaleRank = map[store.Scale]int{
	store.ScaleStep:   0,
	store.ScaleStage:  1,
	store.ScaleMatter: 2,
}

// Violation is one pair of declared gate bindings that breaks D12's
// invariant: Earlier sits before Later in the fixed gate order but is bound
// to a coarser scale than Later — going forward in the sequence, scale got
// finer instead of staying the same or getting coarser.
type Violation struct {
	Earlier store.GateDeclaration
	Later   store.GateDeclaration
}

// Violations reports every pair of declared bindings that violates D12 —
// every one, not just the first (D65), so an audit that finds two never
// sends its reader round again after fixing only one. A gate name outside
// the fixed order (there is none in P1, but the check is general) never
// participates.
func Violations(declared []store.GateDeclaration) []Violation {
	var out []Violation
	for i, a := range declared {
		ai, aok := gateOrder[a.Gate]
		if !aok {
			continue
		}
		for j, b := range declared {
			if i == j {
				continue
			}
			bi, bok := gateOrder[b.Gate]
			if !bok || bi <= ai {
				continue // only pairs where b comes strictly later than a
			}
			if scaleRank[a.Scale] > scaleRank[b.Scale] {
				out = append(out, Violation{Earlier: a, Later: b})
			}
		}
	}
	return out
}

// WouldViolate reports whether declaring gate at scale, given the Repo's
// already-declared bindings, would introduce a gate-order violation — the
// add-time precondition `wip gate declare` calls (mirrors WouldCycle:
// answer before anything is written). A redeclare of gate itself updates its
// binding in place (D4) rather than duplicating it in the check. On a
// violation, it returns the one already-declared binding gate conflicts
// with, for the refusal message.
func WouldViolate(declared []store.GateDeclaration, gate string, scale store.Scale) (store.GateDeclaration, bool) {
	merged := make([]store.GateDeclaration, 0, len(declared)+1)
	for _, d := range declared {
		if d.Gate != gate {
			merged = append(merged, d)
		}
	}
	merged = append(merged, store.GateDeclaration{Gate: gate, Scale: scale})

	for _, v := range Violations(merged) {
		if v.Earlier.Gate == gate {
			return v.Later, true
		}
		if v.Later.Gate == gate {
			return v.Earlier, true
		}
	}
	return store.GateDeclaration{}, false
}

// ViolationMessage is vocabulary step-12's ratified refusal wording,
// parameterized over subject (the gate being declared or, in a finding, the
// later-sequenced gate) and other (the already-declared binding it
// conflicts with).
func ViolationMessage(subject store.GateDeclaration, other store.GateDeclaration) string {
	return fmt.Sprintf("refused — %s at %s scale would violate gate-order monotonicity against %s at %s scale",
		subject.Gate, subject.Scale, other.Gate, other.Scale)
}

// CheckGateOrder is the store-wide audit: every Repo's declared gate
// bindings, checked for every violating pair (D65) — the `doctor` caller of
// Violations.
func CheckGateOrder(ctx context.Context, s *store.Store, _ string) ([]Finding, error) {
	repos, err := s.Repos(ctx)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0)
	for _, repo := range repos {
		declared, err := s.GateDeclarations(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range Violations(declared) {
			findings = append(findings, Finding{
				Code: gateOrderCode,
				Message: fmt.Sprintf("found: %s at %s scale violates gate-order monotonicity against %s at %s scale",
					v.Later.Gate, v.Later.Scale, v.Earlier.Gate, v.Earlier.Scale),
			})
		}
	}
	return findings, nil
}
