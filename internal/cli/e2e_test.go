// End-to-end tests for chassis step-11: exit codes and error shape,
// exercised through the actual built binary rather than in-process, so
// Cobra's own argument-parsing path is covered along with our own
// exitcode/wiperr machinery.
package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "wip-chassis-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmpDir, "wip")

	build := exec.Command("go", "build", "-o", binPath, "github.com/procrastivity/wip/cmd/wip")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building wip for e2e tests: %v\n%s", err, out)
		_ = os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

type result struct {
	stdout   string
	stderr   string
	exitCode int
}

// hermeticEnv is the process environment plus the caller's overrides, with
// WIP_CLAUDE_SKILLS_DIR, WIP_CODEX_SKILLS_DIR, WIP_DEVIN_SKILLS_DIR,
// WIP_PI_SKILLS_DIR, and WIP_OPENCODE_SKILLS_DIR each defaulted to their own
// per-test temp dir when the caller does not set them — the suite must never
// read this host's real skill installs (found live: a ~/.claude/skills/wip
// stamped by an older build failed doctor inside tests that never mentioned
// skills; install-target-codex, install-target-devin, install-target-pi, and
// install-target-opencode all carry the same risk for ~/.codex/skills/wip,
// ~/.config/devin/skills/wip, ~/.pi/agent/skills/wip, and
// ~/.config/opencode/skills/wip).
func hermeticEnv(t *testing.T, env []string) []string {
	t.Helper()
	out := append(os.Environ(), env...)
	for _, name := range []string{"WIP_CLAUDE_SKILLS_DIR", "WIP_CODEX_SKILLS_DIR", "WIP_DEVIN_SKILLS_DIR", "WIP_PI_SKILLS_DIR", "WIP_OPENCODE_SKILLS_DIR"} {
		set := false
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				set = true
				break
			}
		}
		if !set {
			out = append(out, name+"="+t.TempDir())
		}
	}
	return out
}

func run(t *testing.T, env []string, args ...string) result {
	t.Helper()
	return runInDir(t, "", env, args...)
}

func runInDir(t *testing.T, dir string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = hermeticEnv(t, env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func TestUsageError_BadFlag(t *testing.T) {
	r := run(t, nil, "--bogus")
	if r.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2 (usage error); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("stdout = %q, want empty on failure", r.stdout)
	}
	if !strings.HasPrefix(r.stderr, "wip: ") {
		t.Fatalf("stderr = %q, want it to start with %q", r.stderr, "wip: ")
	}
}

func TestRefusalError_HumanMode(t *testing.T) {
	r := run(t, []string{"WIP_SELFTEST=1"}, "__selftest-refusal")
	if r.exitCode != 3 {
		t.Fatalf("exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("stdout = %q, want empty on failure", r.stdout)
	}
	want := "wip: __selftest-refusal: refused — .wip/ is tracked by git in this repo; " +
		"untrack it and add it to .git/info/exclude, then re-run\n"
	if r.stderr != want {
		t.Fatalf("stderr = %q, want %q", r.stderr, want)
	}
}

func TestRefusalError_JSONMode(t *testing.T) {
	r := run(t, []string{"WIP_SELFTEST=1"}, "__selftest-refusal", "--json")
	if r.exitCode != 3 {
		t.Fatalf("exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("stdout = %q, want empty on failure even under --json", r.stdout)
	}

	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != "refusal.tracked-wip-dir" {
		t.Fatalf("error.code = %q, want %q", envelope.Error.Code, "refusal.tracked-wip-dir")
	}
	if !strings.Contains(envelope.Error.Message, ".wip/ is tracked by git") {
		t.Fatalf("error.message = %q, missing expected text", envelope.Error.Message)
	}
}

func TestSelftestRefusal_HiddenWithoutEnvVar(t *testing.T) {
	r := run(t, nil, "__selftest-refusal")
	if r.exitCode != 2 {
		t.Fatalf("exit code = %d, want 2 (unknown command) when WIP_SELFTEST is unset", r.exitCode)
	}
}

func TestVersion_HumanMode(t *testing.T) {
	r := run(t, nil, "version")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	if r.stderr != "" {
		t.Fatalf("stderr = %q, want empty on success", r.stderr)
	}
	if !strings.HasPrefix(r.stdout, "wip version ") {
		t.Fatalf("stdout = %q, want it to start with %q", r.stdout, "wip version ")
	}
}

func TestVersion_JSONMode(t *testing.T) {
	r := run(t, nil, "version", "--json")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	if r.stderr != "" {
		t.Fatalf("stderr = %q, want empty on success", r.stderr)
	}

	var payload struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("stdout is not one JSON value: %v (stdout=%q)", err, r.stdout)
	}
	if payload.Version == "" || payload.Commit == "" || payload.Date == "" {
		t.Fatalf("payload has an empty field: %+v", payload)
	}
}

func TestBatchCommands_EndToEndSurfaceAndProjection(t *testing.T) {
	dir := t.TempDir()
	gitInit := exec.Command("git", "init", "-q", dir)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	env := []string{"WIP_DB_PATH=" + dbPath}

	if r := runInDir(t, dir, env, "init", "--json"); r.exitCode != 0 {
		t.Fatalf("init exit code = %d, stderr=%q", r.exitCode, r.stderr)
	}
	matterResult := runInDir(t, dir, env, "matter", "create", "--title", "Batch Matter", "--locator", "batch-matter", "--json")
	if matterResult.exitCode != 0 {
		t.Fatalf("matter create exit code = %d, stderr=%q", matterResult.exitCode, matterResult.stderr)
	}
	var matter struct {
		ID      string `json:"id"`
		Locator string `json:"locator"`
	}
	if err := json.Unmarshal([]byte(matterResult.stdout), &matter); err != nil || matter.ID == "" || matter.Locator != "batch-matter" {
		t.Fatalf("matter create JSON = %q, err=%v", matterResult.stdout, err)
	}

	created := runInDir(t, dir, env, "batch", "create", "release", "--json")
	if created.exitCode != 0 {
		t.Fatalf("batch create exit code = %d, stderr=%q", created.exitCode, created.stderr)
	}
	var batch struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Matter      string `json:"matter"`
		State       string `json:"state"`
		CloseReason string `json:"close_reason"`
		Member      string `json:"member"`
		Action      string `json:"action"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &batch); err != nil {
		t.Fatalf("batch create JSON = %q, err=%v", created.stdout, err)
	}
	if batch.ID == "" || batch.Name != "release" || batch.Matter != "" || batch.State != "live" || batch.Action != "created" {
		t.Fatalf("batch create payload = %+v, want full live Batch identity and action", batch)
	}
	batchID := batch.ID

	joined := runInDir(t, dir, env, "batch", "join", "release", matter.Locator, "--json")
	if joined.exitCode != 0 {
		t.Fatalf("batch join exit code = %d, stderr=%q", joined.exitCode, joined.stderr)
	}
	if err := json.Unmarshal([]byte(joined.stdout), &batch); err != nil {
		t.Fatalf("batch join JSON = %q, err=%v", joined.stdout, err)
	}
	if batch.ID != batchID || batch.Member != matter.ID || batch.Action != "joined" {
		t.Fatalf("batch join payload = %+v, want Batch %s, member %s and action joined", batch, batchID, matter.ID)
	}

	partial := batch.ID[:12]
	refused := runInDir(t, dir, env, "batch", "join", partial, matter.Locator, "--json")
	if refused.exitCode != 1 || refused.stdout != "" {
		t.Fatalf("partial Batch identity result = %+v, want validation refusal", refused)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(refused.stderr), &envelope); err != nil || envelope.Error.Code != "validation.unknown-batch" {
		t.Fatalf("partial Batch identity error = %q, err=%v", refused.stderr, err)
	}

	left := runInDir(t, dir, env, "batch", "leave", batch.ID, matter.Locator, "--json")
	if left.exitCode != 0 {
		t.Fatalf("batch leave exit code = %d, stderr=%q", left.exitCode, left.stderr)
	}
	if err := json.Unmarshal([]byte(left.stdout), &batch); err != nil {
		t.Fatalf("batch leave JSON = %q, err=%v", left.stdout, err)
	}
	if batch.Member != matter.ID || batch.Action != "left" {
		t.Fatalf("batch leave payload = %+v, want member %s and action left", batch, matter.ID)
	}

	rejoined := runInDir(t, dir, env, "batch", "join", batch.ID, matter.Locator, "--json")
	if rejoined.exitCode != 0 {
		t.Fatalf("batch rejoin by full ULID exit code = %d, stderr=%q", rejoined.exitCode, rejoined.stderr)
	}
	if err := json.Unmarshal([]byte(rejoined.stdout), &batch); err != nil || batch.Action != "joined" {
		t.Fatalf("batch rejoin JSON = %q, err=%v", rejoined.stdout, err)
	}

	dismissed := runInDir(t, dir, env, "batch", "dismiss", "release", "--json")
	if dismissed.exitCode != 0 {
		t.Fatalf("batch dismiss exit code = %d, stderr=%q", dismissed.exitCode, dismissed.stderr)
	}
	if err := json.Unmarshal([]byte(dismissed.stdout), &batch); err != nil {
		t.Fatalf("batch dismiss JSON = %q, err=%v", dismissed.stdout, err)
	}
	if batch.ID == "" || batch.Name != "release" || batch.State != "closed" || batch.CloseReason != "dismissed" || batch.Action != "dismissed" {
		t.Fatalf("batch dismiss payload = %+v, want terminal Batch identity and reason", batch)
	}

	human := runInDir(t, dir, env, "batch", "create", "human-output")
	if human.exitCode != 0 || !strings.Contains(human.stdout, "created Batch human-output (") || human.stderr != "" {
		t.Fatalf("human batch output = %+v", human)
	}

	help := runInDir(t, dir, nil, "--help")
	if help.exitCode != 0 || !strings.Contains(help.stdout, "batch") {
		t.Fatalf("root help does not register batch: %+v", help)
	}
	manifestResult := runInDir(t, dir, nil, "manifest", "--json")
	if manifestResult.exitCode != 0 {
		t.Fatalf("manifest exit code = %d, stderr=%q", manifestResult.exitCode, manifestResult.stderr)
	}
	var manifest struct {
		Verbs []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(manifestResult.stdout), &manifest); err != nil {
		t.Fatalf("manifest JSON = %q, err=%v", manifestResult.stdout, err)
	}
	for _, name := range []string{"batch create", "batch join", "batch leave", "batch dismiss"} {
		found := false
		for _, verb := range manifest.Verbs {
			if verb.Name == name {
				found = true
				if verb.Kind != "plumbing" {
					t.Errorf("manifest %q kind = %q, want plumbing", name, verb.Kind)
				}
			}
		}
		if !found {
			t.Errorf("manifest does not register %q", name)
		}
	}

	skillsDir := t.TempDir()
	installed := runInDir(t, dir, []string{"WIP_PI_SKILLS_DIR=" + skillsDir}, "install", "pi", "--json")
	if installed.exitCode != 0 {
		t.Fatalf("pi install exit code = %d, stderr=%q", installed.exitCode, installed.stderr)
	}
	var installPayload struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installed.stdout), &installPayload); err != nil {
		t.Fatalf("pi install JSON = %q, err=%v", installed.stdout, err)
	}
	skill, err := os.ReadFile(filepath.Join(installPayload.Dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read generated Pi skill: %v", err)
	}
	if !strings.Contains(string(skill), "batch create") || strings.Contains(string(skill), "control-plane") {
		t.Fatalf("generated Pi skill does not project Batch as plumbing: %s", skill)
	}
}

func TestManifest_JSONMode(t *testing.T) {
	r := run(t, nil, "manifest", "--json")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}

	var m struct {
		Tool struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"tool"`
		SchemaVersion int `json:"schemaVersion"`
		Verbs         []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"verbs"`
		Assets []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("stdout is not the manifest JSON shape: %v (stdout=%q)", err, r.stdout)
	}
	if m.Tool.Name != "wip" {
		t.Errorf("tool.name = %q, want %q", m.Tool.Name, "wip")
	}
	if m.SchemaVersion == 0 {
		t.Error("schemaVersion is zero, want the manifest's own schema version")
	}
	foundVersion := false
	for _, v := range m.Verbs {
		if v.Name == "version" {
			foundVersion = true
			if v.Kind != "plumbing" {
				t.Errorf(`"version" verb kind = %q, want "plumbing"`, v.Kind)
			}
		}
	}
	if !foundVersion {
		t.Errorf("verbs = %+v, want it to include the version verb", m.Verbs)
	}
	if len(m.Assets) == 0 {
		t.Error("assets is empty, want at least the shipped fixture assets")
	}
	for _, a := range m.Assets {
		if len(a.SHA256) != 64 {
			t.Errorf("asset %q sha256 = %q, want a 64-char hex digest", a.Path, a.SHA256)
		}
	}
}

func TestInstallUninstall_ClaudeCode_RoundTrip(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_CLAUDE_SKILLS_DIR=" + skillsDir}

	installResult := run(t, env, "install", "claude-code", "--json")
	if installResult.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", installResult.exitCode, installResult.stderr)
	}
	var installPayload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installResult.stdout), &installPayload); err != nil {
		t.Fatalf("install stdout is not JSON: %v (stdout=%q)", err, installResult.stdout)
	}
	if installPayload.Harness != "claude-code" {
		t.Errorf("installed harness = %q, want %q", installPayload.Harness, "claude-code")
	}

	dir := installPayload.Dir
	for _, want := range []string{"SKILL.md", filepath.Join(".claude-plugin", "plugin.json"), ".wip-manifest-stamp.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected generated file %q missing: %v", want, err)
		}
	}

	uninstallResult := run(t, env, "uninstall", "claude-code", "--json")
	if uninstallResult.exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr=%q", uninstallResult.exitCode, uninstallResult.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
}

func TestInstallUninstall_Codex_RoundTrip(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_CODEX_SKILLS_DIR=" + skillsDir}

	installResult := run(t, env, "install", "codex", "--json")
	if installResult.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", installResult.exitCode, installResult.stderr)
	}
	var installPayload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installResult.stdout), &installPayload); err != nil {
		t.Fatalf("install stdout is not JSON: %v (stdout=%q)", err, installResult.stdout)
	}
	if installPayload.Harness != "codex" {
		t.Errorf("installed harness = %q, want %q", installPayload.Harness, "codex")
	}

	dir := installPayload.Dir
	for _, want := range []string{"SKILL.md", ".wip-manifest-stamp.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected generated file %q missing: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("codex install wrote .claude-plugin/plugin.json, want none — that mechanism is claude-code's own")
	}

	uninstallResult := run(t, env, "uninstall", "codex", "--json")
	if uninstallResult.exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr=%q", uninstallResult.exitCode, uninstallResult.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
}

func TestInstallUninstall_Pi_RoundTrip(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_PI_SKILLS_DIR=" + skillsDir}

	installResult := run(t, env, "install", "pi", "--json")
	if installResult.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", installResult.exitCode, installResult.stderr)
	}
	var installPayload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installResult.stdout), &installPayload); err != nil {
		t.Fatalf("install stdout is not JSON: %v (stdout=%q)", err, installResult.stdout)
	}
	if installPayload.Harness != "pi" {
		t.Errorf("installed harness = %q, want %q", installPayload.Harness, "pi")
	}

	dir := installPayload.Dir
	for _, want := range []string{"SKILL.md", ".wip-manifest-stamp.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected generated file %q missing: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("pi install wrote .claude-plugin/plugin.json, want none — that mechanism is claude-code's own")
	}

	uninstallResult := run(t, env, "uninstall", "pi", "--json")
	if uninstallResult.exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr=%q", uninstallResult.exitCode, uninstallResult.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
}

func TestInstallUninstall_Opencode_RoundTrip(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_OPENCODE_SKILLS_DIR=" + skillsDir}

	installResult := run(t, env, "install", "opencode", "--json")
	if installResult.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", installResult.exitCode, installResult.stderr)
	}
	var installPayload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installResult.stdout), &installPayload); err != nil {
		t.Fatalf("install stdout is not JSON: %v (stdout=%q)", err, installResult.stdout)
	}
	if installPayload.Harness != "opencode" {
		t.Errorf("installed harness = %q, want %q", installPayload.Harness, "opencode")
	}

	dir := installPayload.Dir
	for _, want := range []string{"SKILL.md", ".wip-manifest-stamp.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected generated file %q missing: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("opencode install wrote .claude-plugin/plugin.json, want none — that mechanism is claude-code's own")
	}

	uninstallResult := run(t, env, "uninstall", "opencode", "--json")
	if uninstallResult.exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr=%q", uninstallResult.exitCode, uninstallResult.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
}

func TestInstallUninstall_Devin_RoundTrip(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_DEVIN_SKILLS_DIR=" + skillsDir}

	installResult := run(t, env, "install", "devin", "--json")
	if installResult.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", installResult.exitCode, installResult.stderr)
	}
	var installPayload struct {
		Harness string `json:"harness"`
		Dir     string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installResult.stdout), &installPayload); err != nil {
		t.Fatalf("install stdout is not JSON: %v (stdout=%q)", err, installResult.stdout)
	}
	if installPayload.Harness != "devin" {
		t.Errorf("installed harness = %q, want %q", installPayload.Harness, "devin")
	}

	dir := installPayload.Dir
	for _, want := range []string{"SKILL.md", ".wip-manifest-stamp.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected generated file %q missing: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Errorf("devin install wrote .claude-plugin/plugin.json, want none — that mechanism is claude-code's own")
	}

	uninstallResult := run(t, env, "uninstall", "devin", "--json")
	if uninstallResult.exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, want 0; stderr=%q", uninstallResult.exitCode, uninstallResult.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
}

func TestUninstall_RefusesHandEditedTarget(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_CLAUDE_SKILLS_DIR=" + skillsDir}

	if r := run(t, env, "install", "claude-code"); r.exitCode != 0 {
		t.Fatalf("install exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}

	dir := filepath.Join(skillsDir, "wip")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-edited"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := run(t, env, "uninstall", "claude-code", "--json")
	if r.exitCode != 3 {
		t.Fatalf("uninstall exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != "refusal.unstamped-harness-target" {
		t.Fatalf("error.code = %q, want %q", envelope.Error.Code, "refusal.unstamped-harness-target")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("hand-edited install dir was removed despite the refusal: %v", err)
	}
}

func TestInstall_UnknownHarness(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_CLAUDE_SKILLS_DIR=" + skillsDir}

	r := run(t, env, "install", "some-unknown-harness", "--json")
	if r.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 (user-facing failure); stderr=%q", r.exitCode, r.stderr)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != "validation.unknown-harness" {
		t.Fatalf("error.code = %q, want %q", envelope.Error.Code, "validation.unknown-harness")
	}
}

func TestVersion_VerboseWritesOnlyToStderr(t *testing.T) {
	r := run(t, nil, "version", "-v")
	if r.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	if r.stderr == "" {
		t.Fatal("stderr is empty, want at least one -v diagnostic line")
	}
	if !strings.HasPrefix(r.stdout, "wip version ") {
		t.Fatalf("stdout = %q, want the success payload unaffected by -v", r.stdout)
	}
}
