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

func run(t *testing.T, env []string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
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
