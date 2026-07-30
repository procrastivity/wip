// Package harness holds the pure filter that selects which verbs project
// into a harness (D53's mechanical enforcement point, manifest-install
// Brief), plus the per-harness generators that consume it. The manifest's
// own --json output is never filtered — this package is the only consumer
// of the filter, and each subpackage (e.g. claudecode) is the only consumer
// of that harness's rendered output.
package harness

import (
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

// Projectable selects the plumbing-kind subset of verbs (D53, D47): only
// verbs that are deterministic, JSON + exit codes, no LLM belong in a
// harness projection, because inside a harness the model already is the
// LLM (llm-kind verbs) or the projection has no live MCP surface to run
// against (control-plane-kind verbs).
func Projectable(verbs []manifest.Verb) []manifest.Verb {
	out := make([]manifest.Verb, 0, len(verbs))
	for _, v := range verbs {
		if v.Kind == surface.Plumbing {
			out = append(out, v)
		}
	}
	return out
}
