package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/harness/amp"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/devin"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/wiperr"
)

func TestCommand_UnknownHarnessPrecedesManifestBuild(t *testing.T) {
	root := &cobra.Command{Use: "wip"}
	root.AddCommand(&cobra.Command{
		Use:  "malformed",
		RunE: func(*cobra.Command, []string) error { return nil },
	})

	cmd := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, buildinfo.Info{}, root, tracker.NewRegistry())
	cmd.SetArgs([]string{"some-unknown-harness"})
	err := cmd.Execute()

	var got *wiperr.Error
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want *wiperr.Error", err, err)
	}
	if got.Code != "validation.unknown-harness" {
		t.Fatalf("error code = %q, want %q", got.Code, "validation.unknown-harness")
	}
}

func TestCommand_HelpListsHarnesses(t *testing.T) {
	root := &cobra.Command{Use: "wip"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, buildinfo.Info{}, root, tracker.NewRegistry())
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	if !strings.Contains(out.String(), "Available harnesses: claude-code, amp, codex, devin, pi, opencode.") {
		t.Fatalf("--help output does not list the harnesses:\n%s", out.String())
	}
}

func TestCommand_Force(t *testing.T) {
	tempSkillsDir := t.TempDir()
	t.Setenv(claudecode.SkillsDirEnv, tempSkillsDir)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	// A plain install writes a stamped tree.
	out := &bytes.Buffer{}
	first := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{"claude-code"})
	if err := first.Execute(); err != nil {
		t.Fatalf("first install: %v", err)
	}

	installDir, err := claudecode.InstallDir()
	if err != nil {
		t.Fatalf("InstallDir: %v", err)
	}
	skillMD := filepath.Join(installDir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatalf("hand-editing SKILL.md: %v", err)
	}

	// A re-install without --force refuses, and leaves the hand edit in
	// place.
	second := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetArgs([]string{"claude-code"})
	err = second.Execute()

	var got *wiperr.Error
	if !errors.As(err, &got) {
		t.Fatalf("re-install error = %T %v, want *wiperr.Error", err, err)
	}
	if got.Code != "refusal.unstamped-harness-target" {
		t.Fatalf("re-install error code = %q, want %q", got.Code, "refusal.unstamped-harness-target")
	}

	edited, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("reading SKILL.md after refused re-install: %v", err)
	}
	if string(edited) != "hand-edited after install" {
		t.Fatalf("SKILL.md changed despite the refusal: %q", edited)
	}

	// --force overrides the refusal and restores the generated content.
	third := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	third.SetArgs([]string{"claude-code", "--force"})
	if err := third.Execute(); err != nil {
		t.Fatalf("forced re-install: %v", err)
	}

	restored, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("reading SKILL.md after forced re-install: %v", err)
	}
	if string(restored) == "hand-edited after install" {
		t.Fatal("SKILL.md was not overwritten despite --force")
	}
}

func TestCommand_ForceIsARegisteredFlag(t *testing.T) {
	root := &cobra.Command{Use: "wip"}
	cmd := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, buildinfo.Info{}, root, tracker.NewRegistry())

	flag := cmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal(`Command does not register a "force" flag`)
	}
	if flag.Value.Type() != "bool" {
		t.Fatalf(`"force" flag type = %q, want "bool"`, flag.Value.Type())
	}
}

// absentSkillsDir returns a path under a fresh temp dir that does not
// exist — the shape Available() treats as "not detected" when a harness's
// WIP_*_SKILLS_DIR override points at it.
func absentSkillsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "absent")
}

// setAllSkillsDirs pins every harness's WIP_*_SKILLS_DIR override for the
// duration of the test, so the bare `wip install` path never probes this
// host's real ~/.claude, ~/.config/amp, ~/.codex, ~/.config/devin, ~/.pi,
// or ~/.config/opencode. available lists the harness names that should be
// detected — Available() requires the directory itself to exist, so those
// harnesses get a directory made with t.TempDir(); the rest get a sibling
// path that is never created.
func setAllSkillsDirs(t *testing.T, available ...string) {
	t.Helper()
	isAvailable := func(name string) bool {
		for _, a := range available {
			if a == name {
				return true
			}
		}
		return false
	}

	envFor := map[string]string{
		claudecode.Name: claudecode.SkillsDirEnv,
		amp.Name:        amp.SkillsDirEnv,
		codex.Name:      codex.SkillsDirEnv,
		devin.Name:      devin.SkillsDirEnv,
		pi.Name:         pi.SkillsDirEnv,
		opencode.Name:   opencode.SkillsDirEnv,
	}
	for name, env := range envFor {
		if isAvailable(name) {
			t.Setenv(env, t.TempDir())
		} else {
			t.Setenv(env, absentSkillsDir(t))
		}
	}
}

func TestCommand_BareInstallsEveryDetectedHarness(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, amp.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}

	wantPrefix := []string{
		"installed claude-code skill at ",
		"installed amp skill at ",
		"skipped codex — not detected",
		"skipped devin — not detected",
		"skipped pi — not detected",
		"installed opencode skill at ",
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}

	claudeDir, err := claudecode.InstallDir()
	if err != nil {
		t.Fatalf("claudecode.InstallDir: %v", err)
	}
	opencodeDir, err := opencode.InstallDir()
	if err != nil {
		t.Fatalf("opencode.InstallDir: %v", err)
	}
	ampDir, err := amp.InstallDir()
	if err != nil {
		t.Fatalf("amp.InstallDir: %v", err)
	}
	for _, dir := range []string{claudeDir, ampDir, opencodeDir} {
		for _, file := range []string{"SKILL.md", manifest.StampFileName} {
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				t.Fatalf("stat %s in %s: %v", file, dir, err)
			}
		}
	}
}

func TestCommand_BareInstallsEveryDetectedHarness_JSON(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, amp.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	// --json is a persistent flag bound at the real root
	// (internal/cli/root.go) and threaded down via cliflags context; this
	// test builds the install command standalone, so it sets that context
	// directly rather than parsing a --json flag this command never
	// registers itself.
	cmd.SetContext(cliflags.WithFlags(context.Background(), cliflags.Flags{JSON: true}))
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	type resultRow struct {
		Harness string `json:"harness"`
		Status  string `json:"status"`
		Dir     string `json:"dir"`
		Reason  string `json:"reason"`
	}
	var payload struct {
		Results []resultRow `json:"results"`
	}
	trimmed := strings.TrimSpace(out.String())
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("stdout is not exactly one JSON line: %q", out.String())
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		t.Fatalf("decoding JSON output: %v (stdout=%q)", err, out.String())
	}

	if len(payload.Results) != 6 {
		t.Fatalf("got %d results, want 6: %+v", len(payload.Results), payload.Results)
	}

	want := map[string]string{
		claudecode.Name: "installed",
		amp.Name:        "installed",
		codex.Name:      "skipped",
		devin.Name:      "skipped",
		pi.Name:         "skipped",
		opencode.Name:   "installed",
	}
	for _, r := range payload.Results {
		wantStatus, ok := want[r.Harness]
		if !ok {
			t.Fatalf("unexpected harness %q in results", r.Harness)
		}
		if r.Status != wantStatus {
			t.Fatalf("harness %q status = %q, want %q", r.Harness, r.Status, wantStatus)
		}
		switch r.Status {
		case "installed":
			if r.Dir == "" {
				t.Fatalf("harness %q installed but has no dir", r.Harness)
			}
		case "skipped":
			if r.Reason != "not detected" {
				t.Fatalf("harness %q reason = %q, want %q", r.Harness, r.Reason, "not detected")
			}
		}
	}
}

func TestCommand_BareRefusesHandEditedAmongDetected(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, pi.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	// Install pi once so it has a stamped tree, then hand-edit it so the
	// next bare run refuses it.
	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{"pi"})
	if err := first.Execute(); err != nil {
		t.Fatalf("priming pi install: %v", err)
	}
	piDir, err := pi.InstallDir()
	if err != nil {
		t.Fatalf("pi.InstallDir: %v", err)
	}
	piSkillMD := filepath.Join(piDir, "SKILL.md")
	if err := os.WriteFile(piSkillMD, []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatalf("hand-editing pi SKILL.md: %v", err)
	}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	cmd.SetArgs([]string{})
	err = cmd.Execute()

	var got *wiperr.Error
	if !errors.As(err, &got) {
		t.Fatalf("Execute() error = %T %v, want *wiperr.Error", err, err)
	}
	if got.Code != "refusal.unstamped-harness-target" {
		t.Fatalf("error code = %q, want %q", got.Code, "refusal.unstamped-harness-target")
	}
	if !strings.Contains(got.Message, "pi") {
		t.Fatalf("error message %q does not name the refused harness pi", got.Message)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "installed claude-code skill at ") {
		t.Fatalf("line 0 = %q, want the claude-code install line", lines[0])
	}
	if !strings.HasPrefix(lines[4], "refused pi — ") {
		t.Fatalf("line 4 = %q, want a refused-pi line", lines[4])
	}

	edited, err := os.ReadFile(piSkillMD)
	if err != nil {
		t.Fatalf("reading pi SKILL.md after refused bare install: %v", err)
	}
	if string(edited) != "hand-edited after install" {
		t.Fatalf("pi SKILL.md changed despite the refusal: %q", edited)
	}
}

func TestCommand_BareForceOverridesHandEditedAmongDetected(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, pi.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{"pi"})
	if err := first.Execute(); err != nil {
		t.Fatalf("priming pi install: %v", err)
	}
	piDir, err := pi.InstallDir()
	if err != nil {
		t.Fatalf("pi.InstallDir: %v", err)
	}
	piSkillMD := filepath.Join(piDir, "SKILL.md")
	if err := os.WriteFile(piSkillMD, []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatalf("hand-editing pi SKILL.md: %v", err)
	}

	cmd := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() with --force = %v, want nil", err)
	}

	restored, err := os.ReadFile(piSkillMD)
	if err != nil {
		t.Fatalf("reading pi SKILL.md after forced bare install: %v", err)
	}
	if string(restored) == "hand-edited after install" {
		t.Fatal("pi SKILL.md was not overwritten despite --force")
	}

	claudeDir, err := claudecode.InstallDir()
	if err != nil {
		t.Fatalf("claudecode.InstallDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "SKILL.md")); err != nil {
		t.Fatalf("claude-code SKILL.md missing after forced bare install: %v", err)
	}
}

func TestCommand_BareNoHarnessDetected(t *testing.T) {
	setAllSkillsDirs(t)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	out := &bytes.Buffer{}
	cmd := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() = %v, want nil", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("output has %d lines, want 7 (6 skips + hint):\n%s", len(lines), out.String())
	}
	wantPrefixes := []string{
		"skipped claude-code — not detected",
		"skipped amp — not detected",
		"skipped codex — not detected",
		"skipped devin — not detected",
		"skipped pi — not detected",
		"skipped opencode — not detected",
	}
	for i, want := range wantPrefixes {
		if lines[i] != want {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	if lines[6] != "no harness detected on this host; install one explicitly: wip install <harness>" {
		t.Fatalf("hint line = %q", lines[6])
	}
}

func TestCommand_BareSecondRunReportsCurrent(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{})
	if err := first.Execute(); err != nil {
		t.Fatalf("first (bare) install: %v", err)
	}

	out := &bytes.Buffer{}
	second := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetArgs([]string{})
	if err := second.Execute(); err != nil {
		t.Fatalf("second (bare) install: %v, want nil", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}
	wantPrefix := []string{
		"current claude-code skill at ",
		"skipped amp — not detected",
		"skipped codex — not detected",
		"skipped devin — not detected",
		"skipped pi — not detected",
		"current opencode skill at ",
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}
}

func TestCommand_BareSecondRunReportsCurrent_JSON(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{})
	if err := first.Execute(); err != nil {
		t.Fatalf("first (bare) install: %v", err)
	}

	out := &bytes.Buffer{}
	second := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetContext(cliflags.WithFlags(context.Background(), cliflags.Flags{JSON: true}))
	second.SetArgs([]string{})
	if err := second.Execute(); err != nil {
		t.Fatalf("second (bare, json) install: %v, want nil", err)
	}

	type resultRow struct {
		Harness string `json:"harness"`
		Status  string `json:"status"`
		Dir     string `json:"dir"`
		Reason  string `json:"reason"`
	}
	var payload struct {
		Results []resultRow `json:"results"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &payload); err != nil {
		t.Fatalf("decoding JSON output: %v (stdout=%q)", err, out.String())
	}

	want := map[string]string{
		claudecode.Name: "current",
		amp.Name:        "skipped",
		codex.Name:      "skipped",
		devin.Name:      "skipped",
		pi.Name:         "skipped",
		opencode.Name:   "current",
	}
	for _, r := range payload.Results {
		wantStatus, ok := want[r.Harness]
		if !ok {
			t.Fatalf("unexpected harness %q in results", r.Harness)
		}
		if r.Status != wantStatus {
			t.Fatalf("harness %q status = %q, want %q", r.Harness, r.Status, wantStatus)
		}
		if r.Status == "current" && r.Dir == "" {
			t.Fatalf("harness %q current but has no dir", r.Harness)
		}
	}
}

func TestCommand_BareSecondRunForceReinstalls(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{})
	if err := first.Execute(); err != nil {
		t.Fatalf("first (bare) install: %v", err)
	}

	out := &bytes.Buffer{}
	second := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetArgs([]string{"--force"})
	if err := second.Execute(); err != nil {
		t.Fatalf("second (bare, --force) install: %v, want nil", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}
	wantPrefix := []string{
		"installed claude-code skill at ",
		"skipped amp — not detected",
		"skipped codex — not detected",
		"skipped devin — not detected",
		"skipped pi — not detected",
		"installed opencode skill at ",
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}
}

// TestCommand_BareManifestChangeReportsInstalledNotCurrent installs once,
// then registers a new plumbing verb on root — changing what the current
// binary would generate (its rendered verb list) without touching anything
// on disk — and checks that a second bare run detects the binary drift and
// reinstalls, rather than reporting current.
func TestCommand_BareManifestChangeReportsInstalledNotCurrent(t *testing.T) {
	setAllSkillsDirs(t, claudecode.Name, opencode.Name)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{})
	if err := first.Execute(); err != nil {
		t.Fatalf("first (bare) install: %v", err)
	}

	// Register a new plumbing verb on root after the first install — the
	// manifest a second run builds now differs from what was stamped.
	newVerb := &cobra.Command{
		Use:   "newly-added",
		Short: "a verb added since the last install",
		RunE:  func(*cobra.Command, []string) error { return nil },
	}
	surface.Annotate(newVerb, surface.Plumbing)
	root.AddCommand(newVerb)

	out := &bytes.Buffer{}
	second := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetArgs([]string{})
	if err := second.Execute(); err != nil {
		t.Fatalf("second (bare) install after manifest change: %v, want nil", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("output has %d lines, want 6:\n%s", len(lines), out.String())
	}
	wantPrefix := []string{
		"installed claude-code skill at ",
		"skipped amp — not detected",
		"skipped codex — not detected",
		"skipped devin — not detected",
		"skipped pi — not detected",
		"installed opencode skill at ",
	}
	for i, want := range wantPrefix {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want)
		}
	}
}

func TestCommand_TargetedSecondRunReportsCurrent(t *testing.T) {
	tempSkillsDir := t.TempDir()
	t.Setenv(claudecode.SkillsDirEnv, tempSkillsDir)

	root := &cobra.Command{Use: "wip"}
	build := buildinfo.Info{Version: "1.0.0"}

	first := Command(&iostreams.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	first.SetArgs([]string{"claude-code"})
	if err := first.Execute(); err != nil {
		t.Fatalf("first install: %v", err)
	}

	installDir, err := claudecode.InstallDir()
	if err != nil {
		t.Fatalf("InstallDir: %v", err)
	}

	out := &bytes.Buffer{}
	second := Command(&iostreams.Streams{Out: out, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	second.SetArgs([]string{"claude-code"})
	if err := second.Execute(); err != nil {
		t.Fatalf("second install: %v, want nil", err)
	}

	want := "claude-code skill at " + installDir + " is already current\n"
	if out.String() != want {
		t.Fatalf("second install output = %q, want %q", out.String(), want)
	}

	jsonOut := &bytes.Buffer{}
	third := Command(&iostreams.Streams{Out: jsonOut, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	third.SetContext(cliflags.WithFlags(context.Background(), cliflags.Flags{JSON: true}))
	third.SetArgs([]string{"claude-code"})
	if err := third.Execute(); err != nil {
		t.Fatalf("third (json) install: %v, want nil", err)
	}

	var payload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut.String())), &payload); err != nil {
		t.Fatalf("decoding JSON output: %v (stdout=%q)", err, jsonOut.String())
	}
	if payload.Status != "current" {
		t.Fatalf("status = %q, want %q", payload.Status, "current")
	}
	if payload.Harness != "claude-code" {
		t.Fatalf("harness = %q, want %q", payload.Harness, "claude-code")
	}
	if payload.Dir != installDir {
		t.Fatalf("dir = %q, want %q", payload.Dir, installDir)
	}

	forceOut := &bytes.Buffer{}
	fourth := Command(&iostreams.Streams{Out: forceOut, Err: &bytes.Buffer{}}, build, root, tracker.NewRegistry())
	fourth.SetArgs([]string{"claude-code", "--force"})
	if err := fourth.Execute(); err != nil {
		t.Fatalf("fourth (--force) install: %v, want nil", err)
	}
	wantForce := "installed claude-code skill at " + installDir + "\n"
	if forceOut.String() != wantForce {
		t.Fatalf("--force install output = %q, want %q", forceOut.String(), wantForce)
	}
}
