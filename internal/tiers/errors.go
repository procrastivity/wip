package tiers

import "github.com/procrastivity/wip/internal/wiperr"

// unknownClone is vocabulary's ratified unknown-clone refusal
// (workplans/vocabulary.md step-10), reused verbatim: "running against an
// unknown clone without `init` (hard failure, never a guess)" — MODEL §11.
// Every verb in this Matter that needs a resolved current Clone raises this
// when it has none, except `status`, which the Brief's "Read scope" section
// carves out into a host-wide fallback instead.
//
// chassis's wiperr.Error carries one Message rendered identically in human
// and --json mode (its Brief's envelope has no second field for optional
// guidance lines), so this uses vocabulary's --json message body — the
// denser of its two drafted forms — for both.
func unknownClone() error {
	return wiperr.New("refusal.unknown-clone",
		"refused — this clone is unknown to wip; run `wip init` here first")
}
