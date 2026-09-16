// Package registry holds the one table of harnesses that install,
// uninstall, and doctor all render from — help text, bare-invocation
// listings, unknown-harness errors, and the stale-artifact checks all read
// it, so those verbs can never disagree about what is installable or how
// to drive it. It is a leaf: it imports each harness subpackage for its
// Name constant and function set, and nothing imports it but the verbs and
// guards. internal/harness itself cannot hold this table because every
// subpackage imports it (a cycle), and neither verb package should own a
// fact both read.
package registry

import (
	"fmt"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/harness/amp"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/manifest"
)

// Harness is one row of the install/uninstall/doctor table: a harness's
// name plus the functions that generate, locate, and probe its projection.
// Every field is required — Lookup's callers dispatch through them
// unconditionally, in place of the five-way switches this table replaces.
// Install and Uninstall are methods, not fields: their bodies are the same
// for every harness (internal/harness.Install/Uninstall), parameterized by
// exactly the three fields a row already carries.
type Harness struct {
	Name       string
	InstallDir func() (string, error)
	Generate   func(manifest.Manifest) (map[string][]byte, error)
	Available  func() bool
}

// Install renders h's projection into its install dir and stamps it — the
// shared body in internal/harness, applied to this row.
func (h Harness) Install(m manifest.Manifest) (string, error) {
	return harness.Install(h.Name, h.InstallDir, h.Generate, m)
}

// Uninstall removes exactly the stamped tree at h's install dir — the
// shared body in internal/harness, applied to this row.
func (h Harness) Uninstall() (string, error) {
	return harness.Uninstall(h.Name, h.InstallDir)
}

// All lists every harness wip can project itself into, in the same order
// Names has always listed them: claude-code leads as the reference
// install, and the rest follow alphabetically.
var All = []Harness{
	{
		Name:       claudecode.Name,
		InstallDir: claudecode.InstallDir,
		Generate:   claudecode.Generate,
		Available:  claudecode.Available,
	},
	{
		Name:       amp.Name,
		InstallDir: amp.InstallDir,
		Generate:   amp.Generate,
		Available:  amp.Available,
	},
	{
		Name:       codex.Name,
		InstallDir: codex.InstallDir,
		Generate:   codex.Generate,
		Available:  codex.Available,
	},
	{
		Name:       devin.Name,
		InstallDir: devin.InstallDir,
		Generate:   devin.Generate,
		Available:  devin.Available,
	},
	{
		Name:       pi.Name,
		InstallDir: pi.InstallDir,
		Generate:   pi.Generate,
		Available:  pi.Available,
	},
	{
		Name:       opencode.Name,
		InstallDir: opencode.InstallDir,
		Generate:   opencode.Generate,
		Available:  opencode.Available,
	},
}

// Names lists every harness wip can project itself into, in All's order —
// help text, bare-invocation listings, and unknown-harness errors all read
// this rather than walking All themselves.
var Names = namesOf(All)

// namesOf panics on a duplicate Name (C4.2: registration panics on
// duplicates, because a row in All is static program construction, not
// user input). It runs once, as Names's own initializer, so the panic
// surfaces at program startup rather than letting two rows silently
// collapse into one name that install, uninstall, and doctor would then
// read inconsistently. Rejected: returning an error instead — nothing at
// this call site could act on it, and the guard is cheap enough to pay
// unconditionally for the next harness row.
func namesOf(all []Harness) []string {
	names := make([]string, len(all))
	seen := make(map[string]struct{}, len(all))
	for i, h := range all {
		if _, dup := seen[h.Name]; dup {
			panic(fmt.Sprintf("registry: duplicate harness name %q in All", h.Name))
		}
		seen[h.Name] = struct{}{}
		names[i] = h.Name
	}
	return names
}

// Lookup finds the Harness registered under name, in All's order. It
// reports false for any name not in All — the same unknown-harness case
// install and uninstall already validate against Names before calling
// Lookup.
func Lookup(name string) (Harness, bool) {
	for _, h := range All {
		if h.Name == name {
			return h, true
		}
	}
	return Harness{}, false
}
