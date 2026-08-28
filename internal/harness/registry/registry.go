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
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/manifest"
)

// Harness is one row of the install/uninstall/doctor table: a harness's
// name plus the functions that generate, install, uninstall, locate, and
// probe its projection. Every field is required — Lookup's callers dispatch
// through them unconditionally, in place of the five-way switches this
// table replaces.
type Harness struct {
	Name       string
	InstallDir func() (string, error)
	Generate   func(manifest.Manifest) (map[string][]byte, error)
	Install    func(manifest.Manifest) (string, error)
	Uninstall  func() (string, error)
	Available  func() bool
}

// All lists every harness wip can project itself into, in the same order
// Names has always listed them: claude-code leads as the reference
// install, and the rest follow alphabetically.
var All = []Harness{
	{
		Name:       claudecode.Name,
		InstallDir: claudecode.InstallDir,
		Generate:   claudecode.Generate,
		Install:    claudecode.Install,
		Uninstall:  claudecode.Uninstall,
		Available:  claudecode.Available,
	},
	{
		Name:       codex.Name,
		InstallDir: codex.InstallDir,
		Generate:   codex.Generate,
		Install:    codex.Install,
		Uninstall:  codex.Uninstall,
		Available:  codex.Available,
	},
	{
		Name:       devin.Name,
		InstallDir: devin.InstallDir,
		Generate:   devin.Generate,
		Install:    devin.Install,
		Uninstall:  devin.Uninstall,
		Available:  devin.Available,
	},
	{
		Name:       pi.Name,
		InstallDir: pi.InstallDir,
		Generate:   pi.Generate,
		Install:    pi.Install,
		Uninstall:  pi.Uninstall,
		Available:  pi.Available,
	},
	{
		Name:       opencode.Name,
		InstallDir: opencode.InstallDir,
		Generate:   opencode.Generate,
		Install:    opencode.Install,
		Uninstall:  opencode.Uninstall,
		Available:  opencode.Available,
	},
}

// Names lists every harness wip can project itself into, in All's order —
// help text, bare-invocation listings, and unknown-harness errors all read
// this rather than walking All themselves.
var Names = namesOf(All)

func namesOf(all []Harness) []string {
	names := make([]string, len(all))
	for i, h := range all {
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
