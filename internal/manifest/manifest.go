// Package manifest builds wip's machine-readable declaration of itself
// (manifest-install Brief, "What the manifest is"): the flat JSON object
// `wip manifest --json` emits — tool identity, every registered verb
// regardless of kind, and the shipped-asset list with checksums. It reads
// chassis's single verb-registration point (the built root command) and
// chassis's asset-resolution chain; it adds no second registry of either.
package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/surface"
)

// SchemaVersion is the manifest JSON shape's own version — a separate
// integer from Tool.Version. It bumps only when this Step's own JSON shape
// changes, never for an ordinary verb or asset addition (Brief, "What the
// manifest is").
const SchemaVersion = 1

// Tool identifies the binary that produced the manifest. Its three fields
// reuse chassis step-08's exact version/commit/date vars — the same names,
// the same package path — so `wip version` and `wip manifest --json`'s tool
// object are never two sources for one fact.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Arg describes one flag a verb declares on itself (its LocalFlags — the
// global --json/-v pair is chassis's, not a per-verb arg, and is excluded).
// Positional arguments are not introspectable generically from a Cobra
// command and are not described here.
type Arg struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Verb describes one registered command, regardless of its surface kind —
// the manifest itself never filters (Brief, "verbs"); that happens only on
// the harness-projection side (internal/harness).
type Verb struct {
	Name         string          `json:"name"`
	Kind         surface.Kind    `json:"kind"`
	Args         []Arg           `json:"args"`
	Description  string          `json:"description"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

// Asset describes one file under the shipped assets/ tree.
type Asset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Manifest is the flat object `wip manifest --json` emits.
type Manifest struct {
	Tool          Tool    `json:"tool"`
	SchemaVersion int     `json:"schemaVersion"`
	Verbs         []Verb  `json:"verbs"`
	Assets        []Asset `json:"assets"`
}

// Build walks root's registered command tree (chassis's single registration
// point) and the shipped assets/ tree, and returns the manifest they
// describe. root must already carry every verb it will ever carry for this
// process — callers pass the fully-constructed root command, not one still
// being assembled.
func Build(root *cobra.Command, build buildinfo.Info) (Manifest, error) {
	verbs, err := walkVerbs(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: walking verb registry: %w", err)
	}

	assets, err := walkAssets()
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: walking shipped assets: %w", err)
	}

	return Manifest{
		Tool: Tool{
			Name:    root.Name(),
			Version: build.Version,
			Commit:  build.Commit,
			Date:    build.Date,
		},
		SchemaVersion: SchemaVersion,
		Verbs:         verbs,
		Assets:        assets,
	}, nil
}
