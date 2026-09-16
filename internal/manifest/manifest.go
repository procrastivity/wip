// Package manifest builds wip's machine-readable declaration of itself
// (manifest-install Brief, "What the manifest is"): the flat JSON object
// `wip manifest --json` emits — tool identity, contract version, every
// registered verb regardless of kind, the shipped-asset list with
// checksums, the registered tracker backend names, and a self-committing
// digest. It reads chassis's single
// verb-registration point (the built root command) and chassis's
// asset-resolution chain; it adds no second registry of either.
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
// changes incompatibly, never for an ordinary verb or asset addition (Brief,
// "What the manifest is") and never for an additive top-level field such as
// trackers — every harness bakes this integer into its generated SKILL.md, so
// a bump would flag drift on every install.
const SchemaVersion = 1

// Contract names the toolsmith contract version this tool conforms to
// (C3.6). The conformance checker and the fleet register read it from the
// manifest rather than asserting it from memory.
const Contract = "toolsmith/v1"

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
// A verb's positional arguments are not flags and are recorded separately,
// as Verb.Usage.
type Arg struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Verb describes one registered command, regardless of its surface kind —
// the manifest itself never filters (Brief, "verbs"); that happens only on
// the harness-projection side (internal/harness).
type Verb struct {
	Name string       `json:"name"`
	Kind surface.Kind `json:"kind"`

	// Usage is the positional-argument portion of the verb's own cobra Use
	// line, recorded verbatim (toolsmith C3.8): "<name>" for a verb
	// declared as `Use: "new <name>"`, empty for one that takes no
	// positionals. Without it a consumer reading only the manifest — the
	// harness projection is one — cannot tell that a verb requires an
	// argument at all.
	//
	// It is recorded, never parsed. Cobra's Args validator is an opaque
	// func, so the shape of a verb's positionals is not mechanically
	// introspectable; the Use line is the one place a verb declares it, and
	// the angle/bracket convention it uses is a convention nothing
	// enforces. Splitting it into {name, required, variadic} would be
	// inventing structure from that convention, which is what C3.7 forbids
	// doing speculatively. A consumer that needs structure earns the field
	// then.
	Usage string `json:"usage,omitempty"`

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
	Contract      string  `json:"contract"`
	Verbs         []Verb  `json:"verbs"`
	Assets        []Asset `json:"assets"`

	// Trackers lists the tracker backend names the binary registered
	// (tracker.Registry.Names(), sorted). It is the same list `wip plumbing outbox
	// backend --help` enumerates; the manifest carries it so an agent can
	// answer "which trackers can wip use" without reading source. Always
	// present — an empty list marshals as [] rather than null.
	Trackers []string `json:"trackers"`

	// ManifestDigest is a sha256 over this document's own canonical JSON
	// with this field held empty (toolsmith C3.4, adopted from duo): a
	// comparable identity for the whole manifest, so drift can be detected
	// without re-walking. Build sets it last.
	ManifestDigest string `json:"manifest_digest"`
}

// Option adjusts what Build records beyond the verb walk and asset tree.
type Option func(*Manifest)

// WithTrackers records the registered tracker backend names. Callers pass
// tracker.Registry.Names(); Build copies the slice so a later mutation of
// the caller's slice cannot reach the manifest.
func WithTrackers(names []string) Option {
	return func(m *Manifest) {
		m.Trackers = append(make([]string, 0, len(names)), names...)
	}
}

// Build walks root's registered command tree (chassis's single registration
// point) and the shipped assets/ tree, and returns the manifest they
// describe. root must already carry every verb it will ever carry for this
// process — callers pass the fully-constructed root command, not one still
// being assembled.
func Build(root *cobra.Command, build buildinfo.Info, opts ...Option) (Manifest, error) {
	verbs, err := walkVerbs(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: walking verb registry: %w", err)
	}

	assets, err := walkAssets()
	if err != nil {
		return Manifest{}, fmt.Errorf("manifest: walking shipped assets: %w", err)
	}

	m := Manifest{
		Tool: Tool{
			Name:    root.Name(),
			Version: build.Version,
			Commit:  build.Commit,
			Date:    build.Date,
		},
		SchemaVersion: SchemaVersion,
		Contract:      Contract,
		Verbs:         verbs,
		Assets:        assets,
		Trackers:      []string{},
	}
	for _, opt := range opts {
		opt(&m)
	}

	// The digest commits to the exact document the caller receives, so it
	// is computed only after every other field — options included — is set.
	digest, err := manifestDigest(m)
	if err != nil {
		return Manifest{}, err
	}
	m.ManifestDigest = digest
	return m, nil
}
