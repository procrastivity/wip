// Package guards implements PLAN 1.6: the four checks `guards` adds to
// `tiers` step-04's pre-existing `wip doctor` (dependency cycles, gate-order
// monotonicity, tracked `.wip/`, and stale generated harness artifacts —
// unknown clone is tiers' own, already built and left untouched here) and
// the tracked-`.wip/` render precondition (MODEL §11, D33/D41's inverse: a
// tracked `.wip/` had inverted meaning under the old design, so tracked-ness
// is the test, never mere presence). The tracked-`.wip/` check and the
// render precondition live in the trackedwip subpackage (see its own doc
// comment) to keep internal/writesurface's import of this package from
// cycling back through internal/render.
//
// Every check here is exhaustive per run (D65): it reports every instance of
// its condition present at the time of the run — every cycle, every
// gate-order violation, every drifted artifact — never the first found,
// never one per tangle (the lesson of `schema`'s own `Cycles` bug).
package guards

import (
	"context"

	"github.com/procrastivity/wip/internal/store"
)

// Finding is one problem `wip doctor` reports: a stable machine code — reused
// from vocabulary's ratified refusal codes for the four conditions that
// already have one, a new `advisory.`-prefixed code for the one that doesn't
// — and a human-readable message, in chassis's envelope shape even though
// `doctor`'s own exit posture (0 clean / 1 with findings, no severity
// levels) never reaches the refusal exit code (3) three of these codes carry
// at their other, refusal-raising call sites.
type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Check is one doctor check, run against the open store and the resolved
// working directory. It returns every finding present right now — never the
// first found (D65) — or a non-nil error only when the check itself failed
// to run (an I/O or store failure), never for "the check found a problem,"
// which is a Finding, not an error.
type Check func(ctx context.Context, s *store.Store, dir string) ([]Finding, error)

// Run executes every check in order and concatenates their findings — the
// registry `wip doctor` grows by registering into (`tiers` step-04's
// unknown-clone check stays its own call site, ahead of this registry, in
// `internal/verbs/doctor`, since it alone can hard-fail per MODEL §11 rather
// than ever report a finding — see that package for why the two are kept
// distinct rather than forced into one shape).
func Run(ctx context.Context, s *store.Store, dir string, checks ...Check) ([]Finding, error) {
	findings := make([]Finding, 0)
	for _, check := range checks {
		f, err := check(ctx, s, dir)
		if err != nil {
			return nil, err
		}
		findings = append(findings, f...)
	}
	return findings, nil
}
