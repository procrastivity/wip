// Package harness holds the pure filter that selects which verbs project
// into a harness (D53's mechanical enforcement point, manifest-install
// Brief), plus the per-harness generators that consume it. The manifest's
// own --json output is never filtered — this package is the only consumer
// of the filter, and each subpackage (e.g. claudecode) is the only consumer
// of that harness's rendered output.
package harness

import (
	"strings"

	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

// plumbingPrefix is the verb-name prefix every verb registered under the
// `wip plumbing` namespace carries in the manifest walk
// (internal/manifest's verbPath: "plumbing gate declare", "plumbing
// step create", …). The group command itself declares no RunE, so it is
// never Runnable and never appears as its own Verb row
// (internal/manifest/verbs.go's collect recurses through any command with
// children) — no separate "bare plumbing" case is needed.
const plumbingPrefix = "plumbing "

// Projectable selects exactly the `plumbing` namespace (D112/C8.3).
//
// "plumbing" now carries two distinct meanings in this codebase, and this
// filter keys on the second:
//
//   - surface.Kind "plumbing" (D53, C3.2) — the determinism axis: a verb is
//     deterministic, JSON + exit codes, no LLM call. Several top-level
//     porcelain verbs (init, doctor, install, uninstall, version, manifest)
//     are themselves kind=plumbing while sitting outside the namespace.
//   - the `plumbing` command-tree namespace (D112, C8.3) — the audience
//     axis: everything under it is substrate for scripts and skills, and
//     none of it is listed among wip's own top-level (porcelain) verbs.
//
// D53's substance survives here: every verb still declares a kind (collect
// still hard-errors without one), and the kind test below is kept as a
// conjunct so the porcelain/plumbing split stays enforced mechanically —
// the day an llm-kind verb lands inside the namespace, it still will not
// project.
func Projectable(verbs []manifest.Verb) []manifest.Verb {
	out := make([]manifest.Verb, 0, len(verbs))
	for _, v := range verbs {
		if strings.HasPrefix(v.Name, plumbingPrefix) && v.Kind == surface.Plumbing {
			out = append(out, v)
		}
	}
	return out
}
