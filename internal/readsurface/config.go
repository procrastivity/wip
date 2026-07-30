package readsurface

// Step-06: the default idle gap lives in tool config
// ($XDG_CONFIG_HOME/wip/config.yaml, resolved user-override -> shipped
// config.default.yaml, D54) — not project config in the store — loaded
// through chassis's own loader (its step-07) with no new store access from
// this Matter. `wip session --idle-gap` overrides it per invocation
// (MODEL §2.4's first-class call); the shipped default (6h) is documented
// next to the key in assets/config.default.yaml.

import (
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/config"
)

// idleGapKey is the tool-config key this Matter earns.
const idleGapKey = "idle_gap"

// DefaultIdleGap loads the configured idle gap (override -> shipped default,
// via chassis's config.Load) and parses it as a Go duration.
func DefaultIdleGap() (time.Duration, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	raw, ok := cfg[idleGapKey]
	if !ok {
		return 0, fmt.Errorf("readsurface: tool config carries no %q key; the shipped default is missing", idleGapKey)
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("readsurface: tool config's %q is %v, not a duration string", idleGapKey, raw)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("readsurface: tool config's %q (%q) is not a valid duration: %w", idleGapKey, s, err)
	}
	return d, nil
}
