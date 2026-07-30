// End-to-end tests for the tiers Matter's verbs, through the actual built
// binary (binPath and run come from e2e_test.go, same package). Each test
// points WIP_DB_PATH at its own scratch store so it never touches the real
// one on the host that ran it.
package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runIn is run, plus control over the working directory — every tiers verb
// resolves against the current git clone, so these tests need real cwd
// control that run alone does not offer.
func runIn(t *testing.T, dir string, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v in %s: %v", args, dir, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

func newGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	gitIn(t, dir, "config", "user.name", "test")
	return dir
}

func TestTiers_InitThenStatus_JSONMode(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")

	initResult := runIn(t, dir, dbEnv, "init", "--json")
	if initResult.exitCode != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%q", initResult.exitCode, initResult.stderr)
	}
	var initPayload struct {
		Repo       string `json:"repo"`
		Clone      string `json:"clone"`
		CloneLabel string `json:"cloneLabel"`
	}
	if err := json.Unmarshal([]byte(initResult.stdout), &initPayload); err != nil {
		t.Fatalf("init stdout is not JSON: %v (stdout=%q)", err, initResult.stdout)
	}
	if initPayload.CloneLabel != "widget" {
		t.Errorf("cloneLabel = %q, want %q", initPayload.CloneLabel, "widget")
	}

	statusResult := runIn(t, dir, dbEnv, "status", "--json")
	if statusResult.exitCode != 0 {
		t.Fatalf("status exit code = %d, want 0; stderr=%q", statusResult.exitCode, statusResult.stderr)
	}
	var statusPayload struct {
		HostWide bool `json:"hostWide"`
		Repo     struct {
			Header string `json:"header"`
			Clones []struct {
				Label   string `json:"label"`
				Current bool   `json:"current"`
			} `json:"clones"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(statusResult.stdout), &statusPayload); err != nil {
		t.Fatalf("status stdout is not JSON: %v (stdout=%q)", err, statusResult.stdout)
	}
	if statusPayload.HostWide {
		t.Error("hostWide = true from inside a known clone, want false")
	}
	if statusPayload.Repo.Header != "acme/widget" {
		t.Errorf("repo header = %q, want %q", statusPayload.Repo.Header, "acme/widget")
	}
	if len(statusPayload.Repo.Clones) != 1 || !statusPayload.Repo.Clones[0].Current {
		t.Errorf("clones = %+v, want exactly one, marked current", statusPayload.Repo.Clones)
	}
}

func TestTiers_Status_HostWideOutsideAnyClone_HumanMode(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "never-init")

	r := runIn(t, dir, dbEnv, "status")
	if r.exitCode != 0 {
		t.Fatalf("status exit code = %d, want 0 (status never refuses); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "no repos known to wip on this host — run `wip init` in a clone\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestTiers_UnknownClone_Doctor_RefusalJSONMode(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "totally-unknown")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:nobody/unknown.git")

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 3 {
		t.Fatalf("doctor exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
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
	if envelope.Error.Code != "refusal.unknown-clone" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "refusal.unknown-clone")
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty on a refusal", r.stdout)
	}
}

func TestTiers_Doctor_OffersRelinkOrNewClone_HumanMode(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	known := newGitRepo(t, "widget-known")
	gitIn(t, known, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if r := runIn(t, known, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init exit code = %d; stderr=%q", r.exitCode, r.stderr)
	}

	newClone := newGitRepo(t, "widget-not-yet-init")
	gitIn(t, newClone, "remote", "add", "origin", "https://github.com/acme/widget.git")

	r := runIn(t, newClone, dbEnv, "doctor")
	if r.exitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0 (an offer is not a refusal); stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stdout, "relink") || !strings.Contains(r.stdout, "wip init") {
		t.Errorf("doctor output missing the relink-or-new offer: %q", r.stdout)
	}
}

func TestTiers_Label_RefusesULIDShapedLabel(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init exit code = %d; stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "label", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json")
	if r.exitCode != 1 {
		t.Fatalf("label exit code = %d, want 1 (validation failure); stderr=%q", r.exitCode, r.stderr)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != "validation.label-ulid-shaped" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "validation.label-ulid-shaped")
	}
}

func TestTiers_CloneList(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init exit code = %d; stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "clone", "list", "--json")
	if r.exitCode != 0 {
		t.Fatalf("clone list exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	var payload struct {
		Clones []struct {
			Label string `json:"label"`
		} `json:"clones"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("clone list stdout is not JSON: %v (stdout=%q)", err, r.stdout)
	}
	if len(payload.Clones) != 1 || payload.Clones[0].Label != "widget" {
		t.Errorf("clones = %+v, want exactly one labeled %q", payload.Clones, "widget")
	}
}
