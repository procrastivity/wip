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
// The reported code, `advisory.stale-harness-artifact`, is provisional: it
// is not one of vocabulary's five drafted messages (its own step-14 audit
// confirms that list — 0444, tracked-`.wip/`, unknown-clone, cycle,
// gate-order — is exhaustive and closed), so this Matter drafts it itself,
// in the same structural shape chassis's envelope fixes, flagged here as a
// gap for `manifest-install`'s Brief to reconcile.

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/harness/registry"
	"github.com/procrastivity/wip/internal/manifest"
)

// staleHarnessCode is this Matter's own provisional draft, not a
// vocabulary-ratified code — see the package-level note above.
const staleHarnessCode = "advisory.stale-harness-artifact"

// CheckStaleHarnessArtifact compares what the current binary would generate
// for the claude-code harness against what the last `wip install
// claude-code` stamped on disk, reporting every drifted, added, or removed
// generated file (D65) — never just the first. root is the *cobra.Command
// NewRootCommand is assembling (the same reference `wip manifest`/`wip
// install` already capture), so the manifest this reads reflects every verb
// actually registered. A never-installed target carries no stamp and is
// not a finding — there is nothing to have drifted from.
func CheckStaleHarnessArtifact(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	return checkStaleHarnessArtifact(root, build, claudecode.Name, claudecode.InstallDir, claudecode.Generate)
}

// CheckStaleCodexHarnessArtifact is CheckStaleHarnessArtifact's codex
// counterpart (install-target-codex/step-03): same drift comparison,
// against what `wip install codex` last stamped.
func CheckStaleCodexHarnessArtifact(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	return checkStaleHarnessArtifact(root, build, codex.Name, codex.InstallDir, codex.Generate)
}

// CheckStalePiHarnessArtifact is CheckStaleHarnessArtifact's pi counterpart
// (install-target-pi/step-03): same drift comparison, against what `wip
// install pi` last stamped.
func CheckStalePiHarnessArtifact(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	return checkStaleHarnessArtifact(root, build, pi.Name, pi.InstallDir, pi.Generate)
}

// CheckStaleDevinHarnessArtifact is CheckStaleHarnessArtifact's devin
// counterpart (install-target-devin): same drift comparison, against what
// `wip install devin` last stamped.
func CheckStaleDevinHarnessArtifact(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	return checkStaleHarnessArtifact(root, build, devin.Name, devin.InstallDir, devin.Generate)
}

// CheckStaleOpencodeHarnessArtifact is CheckStaleHarnessArtifact's opencode
// counterpart (install-target-opencode/step-03): same drift comparison,
// against what `wip install opencode` last stamped.
func CheckStaleOpencodeHarnessArtifact(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	return checkStaleHarnessArtifact(root, build, opencode.Name, opencode.InstallDir, opencode.Generate)
}

// CheckStaleHarnessArtifacts is CheckStaleHarnessArtifact generalized over
// every registered harness (registry.All), in that table's order —
// claude-code, codex, devin, pi, opencode — concatenating each harness's
// findings rather than reporting only the first drifted one. It replaces
// the five separate per-harness closures doctor previously registered with
// guards.Run; the five single-harness functions above remain for their own
// tests and for any caller wanting one harness's drift in isolation.
func CheckStaleHarnessArtifacts(root *cobra.Command, build buildinfo.Info) ([]Finding, error) {
	var findings []Finding
	for _, h := range registry.All {
		fs, err := checkStaleHarnessArtifact(root, build, h.Name, h.InstallDir, h.Generate)
		if err != nil {
			return nil, err
		}
		findings = append(findings, fs...)
	}
	return findings, nil
}

func checkStaleHarnessArtifact(
	root *cobra.Command, build buildinfo.Info,
	harnessName string,
	installDir func() (string, error),
	generate func(manifest.Manifest) (map[string][]byte, error),
) ([]Finding, error) {
	dir, err := installDir()
	if err != nil {
		return nil, err
	}
	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	m, err := manifest.Build(root, build)
	if err != nil {
		return nil, err
	}
	files, err := generate(m)
	if err != nil {
		return nil, err
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
