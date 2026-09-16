package guards

// step-08 — carries the sole `guards/stale-artifact-step ← manifest-install`
// edge (HANDOFF §5's documented single-Step exception), placed last so
// steps 01-07 never wait on it. `doctor`'s fifth check compares the
// manifest the current binary would generate against what `wip install
// claude-code` last stamped on disk, via `internal/manifest.Drift` —
// `manifest-install`'s own drift-detection function, exposed by that Matter
// and called rather than reimplemented here ("one function, called by the
// Matter that needs it," the same pattern steps 02-04 follow).
//
// contract-backport step-04 reshaped the check into HarnessTargets: doctor
// now reports every registered target's six-state drift state (C4.5)
// beside its findings, and the per-file stale comparison became the detail
// under the stale and modified states.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/harness/registry"
	"github.com/procrastivity/wip/internal/manifest"
)

// staleHarnessCode is this Matter's own provisional draft, not a
// vocabulary-ratified code — see the package-level note above.
const staleHarnessCode = "advisory.stale-harness-artifact"

// TargetState is one registered harness's reported drift state — the
// per-target fact doctor's --json and text output carry beside findings
// (C4.5).
type TargetState struct {
	Harness string        `json:"harness"`
	Dir     string        `json:"dir"`
	State   harness.State `json:"state"`
}

// HarnessTargets derives every registered harness's drift state
// (harness.Status, C4.6's three comparisons folded into C4.5's six
// states) in registry.All's order, and returns both doctor's findings and
// the per-target states doctor reports beside them. root is the
// *cobra.Command NewRootCommand is assembling, captured by reference — the
// same pattern install uses — so the manifest this builds reflects every
// verb actually registered.
//
// One registry walk gives both, because the state already needs each
// target's install dir and generated files.
//
// Findings by state (C4.7: advisory codes never fail the run;
// toolsmith docs/contract-v1-2-reconcile/decisions.md §1.5):
//   - Current, Missing: no finding.
//   - Stale: the per-file advisory.stale-harness-artifact findings, one
//     per drifted file, exhaustively.
//   - Modified: those same per-file findings too (binary-vs-stamp is
//     independent of disk, C4.6), plus one
//     advisory.modified-harness-target finding.
//   - UnownedConflict: one advisory.unowned-harness-target finding.
//   - Incompatible: one refusal.incompatible-harness-target finding, and
//     no per-file findings — a stamp Status could not trust cannot be
//     diffed against either. This is the one state that fails the run,
//     because its code keeps the "refusal." prefix.
func HarnessTargets(root *cobra.Command, build buildinfo.Info) ([]Finding, []TargetState, error) {
	m, err := manifest.Build(root, build)
	if err != nil {
		return nil, nil, err
	}

	var findings []Finding
	targets := make([]TargetState, 0, len(registry.All))

	for _, h := range registry.All {
		dir, err := h.InstallDir()
		if err != nil {
			return nil, nil, err
		}
		files, err := h.Generate(m)
		if err != nil {
			return nil, nil, err
		}
		state, err := harness.Status(dir, files)
		if err != nil {
			return nil, nil, err
		}
		targets = append(targets, TargetState{Harness: h.Name, Dir: dir, State: state})

		switch state {
		case harness.Stale:
			fs, err := staleFileFindings(h.Name, dir, files)
			if err != nil {
				return nil, nil, err
			}
			findings = append(findings, fs...)
		case harness.Modified:
			fs, err := staleFileFindings(h.Name, dir, files)
			if err != nil {
				return nil, nil, err
			}
			findings = append(findings, fs...)
			findings = append(findings, driftFinding(h.Name, dir, state))
		case harness.UnownedConflict, harness.Incompatible:
			findings = append(findings, driftFinding(h.Name, dir, state))
		}
	}

	return findings, targets, nil
}

// staleFileFindings reports the per-file advisory.stale-harness-artifact
// findings for a target whose stamp is known to parse and match
// manifest.SchemaVersion (Stale and Modified both establish that before
// calling this) — the binary-vs-stamp comparison, independent of whatever
// disk-vs-stamp said (C4.6). files is the current binary's generated
// output, already rendered by the caller, so this never re-generates it.
func staleFileFindings(harnessName, dir string, files map[string][]byte) ([]Finding, error) {
	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	want := manifest.ChecksumFiles(files)
	drifted := manifest.Drift(want, stamp)
	findings := make([]Finding, 0, len(drifted))
	for _, d := range drifted {
		findings = append(findings, Finding{
			Code: staleHarnessCode,
			Message: fmt.Sprintf("found: %s harness artifact %s %s since last install; run `wip install %s` to refresh it",
				harnessName, d.Path, d.Reason, harnessName),
		})
	}
	return findings, nil
}

// driftFindingCodes gives doctor's finding code for each unsafe state.
// Only incompatible fails the run, so it alone keeps the refusal code;
// the other two are advisory (C4.7,
// toolsmith docs/contract-v1-2-reconcile/decisions.md §1.5).
var driftFindingCodes = map[harness.State]string{
	harness.UnownedConflict: "advisory.unowned-harness-target",
	harness.Modified:        "advisory.modified-harness-target",
	harness.Incompatible:    harness.CodeIncompatible,
}

// driftFinding reports an unsafe state with the same fact and remedy that
// install's refusal names (harness.Risk, harness.ForceRemedy).
func driftFinding(harnessName, dir string, state harness.State) Finding {
	return Finding{
		Code:    driftFindingCodes[state],
		Message: fmt.Sprintf("found: %s — %s", harness.Risk(harnessName, dir, state), harness.ForceRemedy(harnessName, state)),
	}
}
