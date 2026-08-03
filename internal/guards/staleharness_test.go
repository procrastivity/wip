package guards_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

// fakeRoot mirrors internal/manifest's own test fixture: a minimal root
// carrying one plumbing verb, standing in for the real, fully-assembled
// NewRootCommand tree CheckStaleHarnessArtifact reads in production.
func fakeRoot() *cobra.Command {
	root := &cobra.Command{Use: "wip"}
	plumbing := &cobra.Command{
		Use:   "widget",
		Short: "a synthetic plumbing verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(plumbing, surface.Plumbing)
	root.AddCommand(plumbing)
	return root
}

func TestCheckStaleHarnessArtifact_NeverInstalledReportsNothing(t *testing.T) {
	t.Setenv(claudecode.SkillsDirEnv, t.TempDir())

	findings, err := guards.CheckStaleHarnessArtifact(fakeRoot(), buildinfo.Info{Version: "1.0.0"})
	if err != nil {
		t.Fatalf("CheckStaleHarnessArtifact: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — nothing has been installed to drift from", findings)
	}
}

func TestCheckStaleHarnessArtifact_CleanRightAfterInstall(t *testing.T) {
	t.Setenv(claudecode.SkillsDirEnv, t.TempDir())
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	m, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	if _, err := claudecode.Install(m); err != nil {
		t.Fatalf("claudecode.Install: %v", err)
	}

	findings, err := guards.CheckStaleHarnessArtifact(root, build)
	if err != nil {
		t.Fatalf("CheckStaleHarnessArtifact: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none immediately after install", findings)
	}
}

// TestCheckStaleHarnessArtifact_FlagsDriftThenClearsOnReinstall is step-08's
// own test: install -> mutate the manifest (add a verb) -> doctor flags
// drift; re-install -> doctor reports clean.
func TestCheckStaleHarnessArtifact_FlagsDriftThenClearsOnReinstall(t *testing.T) {
	t.Setenv(claudecode.SkillsDirEnv, t.TempDir())
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	m, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	if _, err := claudecode.Install(m); err != nil {
		t.Fatalf("claudecode.Install: %v", err)
	}

	// Mutate the registered verb set after install, exactly as the workplan
	// asks: a synthetic verb added to the registry the stamp predates.
	extra := &cobra.Command{
		Use:   "gizmo",
		Short: "a second synthetic plumbing verb, added after install",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(extra, surface.Plumbing)
	root.AddCommand(extra)

	findings, err := guards.CheckStaleHarnessArtifact(root, build)
	if err != nil {
		t.Fatalf("CheckStaleHarnessArtifact: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("findings = none, want at least one — the installed skill no longer reflects the current verb set")
	}
	for _, f := range findings {
		if f.Code != "advisory.stale-harness-artifact" {
			t.Errorf("code = %q, want %q", f.Code, "advisory.stale-harness-artifact")
		}
	}

	// Re-install against the now-current manifest clears the drift.
	m2, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	if _, err := claudecode.Install(m2); err != nil {
		t.Fatalf("claudecode.Install (re-install): %v", err)
	}

	findings, err = guards.CheckStaleHarnessArtifact(root, build)
	if err != nil {
		t.Fatalf("CheckStaleHarnessArtifact after re-install: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none after re-install clears the drift", findings)
	}
}

// TestCheckStaleCodexHarnessArtifact_FlagsDriftThenClearsOnReinstall is
// install-target-codex/step-03's counterpart to the claude-code test above:
// same drift-then-reinstall shape, against the codex harness.
func TestCheckStaleCodexHarnessArtifact_FlagsDriftThenClearsOnReinstall(t *testing.T) {
	t.Setenv(codex.SkillsDirEnv, t.TempDir())
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	m, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	if _, err := codex.Install(m); err != nil {
		t.Fatalf("codex.Install: %v", err)
	}

	extra := &cobra.Command{
		Use:   "gizmo",
		Short: "a second synthetic plumbing verb, added after install",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(extra, surface.Plumbing)
	root.AddCommand(extra)

	findings, err := guards.CheckStaleCodexHarnessArtifact(root, build)
	if err != nil {
		t.Fatalf("CheckStaleCodexHarnessArtifact: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("findings = none, want at least one — the installed skill no longer reflects the current verb set")
	}

	m2, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	if _, err := codex.Install(m2); err != nil {
		t.Fatalf("codex.Install (re-install): %v", err)
	}

	findings, err = guards.CheckStaleCodexHarnessArtifact(root, build)
	if err != nil {
		t.Fatalf("CheckStaleCodexHarnessArtifact after re-install: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none after re-install clears the drift", findings)
	}
}
