package guards_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/harness/amp"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/harness/registry"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

// isolateAllSkillsDirs points every registered harness at its own temp
// dir — HarnessTargets walks the whole registry, so a test that leaves one
// env unset would read the real host's install dirs.
func isolateAllSkillsDirs(t *testing.T) {
	t.Helper()
	for _, env := range []string{
		claudecode.SkillsDirEnv, amp.SkillsDirEnv, codex.SkillsDirEnv,
		devin.SkillsDirEnv, pi.SkillsDirEnv, opencode.SkillsDirEnv,
	} {
		t.Setenv(env, t.TempDir())
	}
}

// fakeRoot mirrors internal/manifest's own test fixture: a minimal root
// carrying one plumbing-namespaced verb, standing in for the real,
// fully-assembled NewRootCommand tree HarnessTargets reads in production.
// The synthetic verb lives under a "plumbing" group command, not directly
// on root, because harness.Projectable (D112) keys on the "plumbing " name
// prefix, not on kind alone — a verb registered straight on root would
// never project into any harness, and this fixture would test nothing.
func fakeRoot() *cobra.Command {
	root := &cobra.Command{Use: "wip"}
	plumbingGroup := &cobra.Command{Use: "plumbing", Short: "the deterministic substrate"}
	widget := &cobra.Command{
		Use:   "widget",
		Short: "a synthetic plumbing verb",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(widget, surface.Plumbing)
	plumbingGroup.AddCommand(widget)
	root.AddCommand(plumbingGroup)
	return root
}

func installHarness(t *testing.T, name string, root *cobra.Command, build buildinfo.Info) string {
	t.Helper()
	m, err := manifest.Build(root, build)
	if err != nil {
		t.Fatalf("manifest.Build: %v", err)
	}
	h, ok := registry.Lookup(name)
	if !ok {
		t.Fatalf("registry.Lookup(%q): not registered", name)
	}
	dir, err := h.Install(m)
	if err != nil {
		t.Fatalf("h.Install(%q): %v", name, err)
	}
	return dir
}

func stateOf(t *testing.T, targets []guards.TargetState, name string) harness.State {
	t.Helper()
	for _, target := range targets {
		if target.Harness == name {
			return target.State
		}
	}
	t.Fatalf("targets = %+v, no entry for %q", targets, name)
	return ""
}

func TestHarnessTargets_NeverInstalledReportsMissingAndNoFindings(t *testing.T) {
	isolateAllSkillsDirs(t)

	findings, targets, err := guards.HarnessTargets(fakeRoot(), buildinfo.Info{Version: "1.0.0"})
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — nothing has been installed to drift from", findings)
	}
	if len(targets) != len(registry.All) {
		t.Fatalf("targets = %d entries, want one per registered harness (%d)", len(targets), len(registry.All))
	}
	for _, target := range targets {
		if target.State != harness.Missing {
			t.Errorf("%s state = %q, want %q", target.Harness, target.State, harness.Missing)
		}
	}
}

func TestHarnessTargets_CurrentRightAfterInstall(t *testing.T) {
	isolateAllSkillsDirs(t)
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	installHarness(t, claudecode.Name, root, build)

	findings, targets, err := guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none immediately after install", findings)
	}
	if got := stateOf(t, targets, claudecode.Name); got != harness.Current {
		t.Errorf("claude-code state = %q, want %q", got, harness.Current)
	}
}

// TestHarnessTargets_FlagsDriftThenClearsOnReinstall is step-08's own
// test, kept through the six-state port: install -> mutate the manifest
// (add a verb) -> doctor flags stale drift; re-install -> clean.
func TestHarnessTargets_FlagsDriftThenClearsOnReinstall(t *testing.T) {
	isolateAllSkillsDirs(t)
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	installHarness(t, claudecode.Name, root, build)

	// Mutate the registered verb set after install, exactly as the workplan
	// asks: a synthetic verb added to the registry the stamp predates.
	extra := &cobra.Command{
		Use:   "gizmo",
		Short: "a second synthetic plumbing verb, added after install",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(extra, surface.Plumbing)
	for _, c := range root.Commands() {
		if c.Name() == "plumbing" {
			c.AddCommand(extra)
			break
		}
	}

	findings, targets, err := guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if got := stateOf(t, targets, claudecode.Name); got != harness.Stale {
		t.Errorf("claude-code state = %q, want %q", got, harness.Stale)
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
	installHarness(t, claudecode.Name, root, build)

	findings, targets, err = guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets after re-install: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none after re-install clears the drift", findings)
	}
	if got := stateOf(t, targets, claudecode.Name); got != harness.Current {
		t.Errorf("claude-code state after re-install = %q, want %q", got, harness.Current)
	}
}

func TestHarnessTargets_ModifiedTreeGetsAdvisoryNotRefusal(t *testing.T) {
	isolateAllSkillsDirs(t)
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	dir := installHarness(t, amp.Name, root, build)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, targets, err := guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if got := stateOf(t, targets, amp.Name); got != harness.Modified {
		t.Errorf("amp state = %q, want %q", got, harness.Modified)
	}
	var sawModified bool
	for _, f := range findings {
		if f.Code == "advisory.modified-harness-target" {
			sawModified = true
			if !strings.Contains(f.Message, amp.Name) {
				t.Errorf("modified finding %q does not name the harness", f.Message)
			}
		}
	}
	if !sawModified {
		t.Fatalf("findings = %+v, want an advisory.modified-harness-target entry", findings)
	}
}

func TestHarnessTargets_UnownedConflictGetsAdvisory(t *testing.T) {
	isolateAllSkillsDirs(t)
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	// Foreign content, no stamp: something wip never wrote.
	dir, err := mustRowInstallDir(codex.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-written, not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, targets, err := guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if got := stateOf(t, targets, codex.Name); got != harness.UnownedConflict {
		t.Errorf("codex state = %q, want %q", got, harness.UnownedConflict)
	}
	if len(findings) != 1 || findings[0].Code != "advisory.unowned-harness-target" {
		t.Fatalf("findings = %+v, want exactly one advisory.unowned-harness-target", findings)
	}
}

func TestHarnessTargets_IncompatibleStampFailsWithRefusalAndNoPerFileFindings(t *testing.T) {
	isolateAllSkillsDirs(t)
	build := buildinfo.Info{Version: "1.0.0"}
	root := fakeRoot()

	dir := installHarness(t, pi.Name, root, build)
	if err := os.WriteFile(filepath.Join(dir, manifest.StampFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, targets, err := guards.HarnessTargets(root, build)
	if err != nil {
		t.Fatalf("HarnessTargets: %v", err)
	}
	if got := stateOf(t, targets, pi.Name); got != harness.Incompatible {
		t.Errorf("pi state = %q, want %q", got, harness.Incompatible)
	}
	if len(findings) != 1 || findings[0].Code != harness.CodeIncompatible {
		t.Fatalf("findings = %+v, want exactly one %s and no per-file findings — a stamp Status cannot trust cannot be diffed", findings, harness.CodeIncompatible)
	}
}

func mustRowInstallDir(name string) (string, error) {
	h, ok := registry.Lookup(name)
	if !ok {
		return "", os.ErrNotExist
	}
	return h.InstallDir()
}
