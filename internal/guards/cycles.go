package guards

// step-02: the dependency-cycle check (D29). This does not reimplement
// cycle detection — it is the second caller of `store.Cycles`, the whole-
// store audit `schema` step-06 built and reserved for exactly this ("the two
// callers, one function"; `write-surface`'s `wip depend add` precondition is
// the first). A cycle present in the store here is always one that arrived
// some other way — an edge added before the check existed, a hand-edited
// database — since the add-time precondition already refuses one going in.

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// cycleCode is vocabulary step-11's ratified `blocked-by`-cycle refusal
// code, reused verbatim: the same condition, caught here by audit instead of
// at write time, keeps the same machine-readable identity (guards.md's
// resolved "Doctor output shape" call).
const cycleCode = "refusal.blocked-by-cycle"

// CheckCycles reports every cycle in the live `blocked-by` edge set, each
// named by its nodes' locators in waits-for order and rotated back to its
// own start — vocabulary step-11's exact path notation ("render-scratch →
// write-surface → render-scratch") adapted from imperative refusal phrasing
// to descriptive finding phrasing ("found: …").
func CheckCycles(ctx context.Context, s *store.Store, _ string) ([]Finding, error) {
	cycles, err := s.Cycles(ctx)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0, len(cycles))
	for _, path := range cycles {
		names := make([]string, 0, len(path)+1)
		for _, id := range path {
			n, err := s.Node(ctx, id)
			if err != nil {
				return nil, err
			}
			names = append(names, n.Locator)
		}
		names = append(names, names[0])
		findings = append(findings, Finding{
			Code:    cycleCode,
			Message: fmt.Sprintf("found: a blocked-by cycle — %s", strings.Join(names, " → ")),
		})
	}
	return findings, nil
}
