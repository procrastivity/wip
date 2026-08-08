package scheduler

// The default Run cap lives in tool config, next to read-surface's idle_gap
// (D54: tool config, resolved user-override -> shipped default). The engine
// takes its cap per invocation; this default shapes reads — `wip next`'s
// slot display — and callers that have no better answer.

import (
	"fmt"

	"github.com/procrastivity/wip/internal/config"
)

const runCapKey = "run_cap"

// DefaultRunCap loads the configured Run-wide cap (G4; 1 is plain
// sequential, D31).
func DefaultRunCap() (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	raw, ok := cfg[runCapKey]
	if !ok {
		return 0, fmt.Errorf("scheduler: tool config carries no %q key; the shipped default is missing", runCapKey)
	}
	n, ok := raw.(int)
	if !ok || n < 1 {
		return 0, fmt.Errorf("scheduler: tool config's %q is %v, want an integer of at least 1", runCapKey, raw)
	}
	return n, nil
}
