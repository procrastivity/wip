package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cli"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/wipd"
)

func TestExperimentalProfileStatusIsLocalAndDoesNotTouchLegacyStore(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "private-profile")
	legacyPath := filepath.Join(t.TempDir(), "legacy-wip.db")
	sentinel := []byte("legacy store remains untouched")
	if err := os.WriteFile(legacyPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_DB_PATH", legacyPath)

	var stdout, stderr bytes.Buffer
	streams := &iostreams.Streams{Out: &stdout, Err: &stderr}
	root := cli.NewRootCommand(streams, buildinfo.Info{Version: "test", Commit: "test", Date: "test"})
	root.SetArgs([]string{"--experimental-wipd-profile", profile, "status", "--json"})
	if code := cli.Execute(root, streams); code != 0 {
		t.Fatalf("profile status exit = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	var status wipd.LocalStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode profile status %q: %v", stdout.String(), err)
	}
	want := wipd.LocalStatus{ProfileVerified: true, StatusCode: "transport.unavailable"}
	if status != want {
		t.Fatalf("profile status = %+v, want local-only unavailable status %+v", status, want)
	}
	if _, err := os.Lstat(profile); !os.IsNotExist(err) {
		t.Fatalf("status created or activated profile: %v", err)
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil || string(got) != string(sentinel) {
		t.Fatalf("legacy store sentinel = %q, err %v; want unchanged %q", got, err, sentinel)
	}
}

func TestExplicitExperimentalProfileNeverFallsBackToLegacyMatterCreate(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "private-profile")
	legacyPath := filepath.Join(t.TempDir(), "legacy-wip.db")
	sentinel := []byte("legacy store remains untouched")
	if err := os.WriteFile(legacyPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_DB_PATH", legacyPath)

	var stdout, stderr bytes.Buffer
	streams := &iostreams.Streams{Out: &stdout, Err: &stderr}
	root := cli.NewRootCommand(streams, buildinfo.Info{Version: "test", Commit: "test", Date: "test"})
	root.SetArgs([]string{
		"--experimental-wipd-profile", profile,
		"plumbing", "matter", "create", "--title", "Must Not Fall Back", "--locator", "must-not-fall-back",
	})
	if code := cli.Execute(root, streams); code == 0 {
		t.Fatalf("explicit experimental matter.create unexpectedly succeeded: stdout %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no legacy store fallback was attempted") {
		t.Fatalf("explicit experimental matter.create error = %q, want clear no-fallback explanation", stderr.String())
	}
	if _, err := os.Lstat(profile); !os.IsNotExist(err) {
		t.Fatalf("unsupported CLI route created or activated profile: %v", err)
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil || string(got) != string(sentinel) {
		t.Fatalf("legacy store sentinel = %q, err %v; want unchanged %q", got, err, sentinel)
	}
}

func TestExperimentalRootActivationStartsForegroundDaemonAndReportsLocalStatus(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "w7-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	profile := filepath.Join(base, "profile")
	dataHome := filepath.Join(base, "xdg-data")
	t.Setenv("WIP_DB_PATH", "")
	t.Setenv("XDG_DATA_HOME", dataHome)
	binary := buildTestWipd(t)
	t.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { stopTestWipdFromProfile(t, profile) })

	var stdout, stderr bytes.Buffer
	streams := &iostreams.Streams{Out: &stdout, Err: &stderr}
	root := cli.NewRootCommand(streams, buildinfo.Info{Version: "test", Commit: "test", Date: "test"})
	root.SetArgs([]string{"--experimental-wipd-profile", profile, "--json"})
	if code := cli.Execute(root, streams); code != 0 {
		t.Fatalf("experimental activation exit = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	var status wipd.LocalStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode activated status %q: %v", stdout.String(), err)
	}
	want := wipd.LocalStatus{ProcessReady: true, ProfileVerified: true, FixtureReady: true, StatusCode: "wipd.ready"}
	if status != want {
		t.Fatalf("activated status = %+v, want local readiness floor %+v", status, want)
	}
	if probed := wipd.ProbeStatus(context.Background(), profile); probed != want {
		t.Fatalf("status probe after activation = %+v, want %+v", probed, want)
	}
	stopTestWipdFromProfile(t, profile)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if probed := wipd.ProbeStatus(context.Background(), profile); probed.StatusCode == "transport.unavailable" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wipd did not become unavailable after foreground stop: %+v", wipd.ProbeStatus(context.Background(), profile))
}

func buildTestWipd(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate CLI test")
	}
	commandDir := filepath.Join(filepath.Dir(source), "../../cmd/wipd")
	binary := filepath.Join(t.TempDir(), "wipd")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = commandDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build foreground wipd for CLI activation: %v\n%s", err, output)
	}
	return binary
}

func stopTestWipdFromProfile(t *testing.T, profile string) {
	t.Helper()
	socket := filepath.Join(profile, "wipd.sock")
	if _, err := os.Lstat(socket); os.IsNotExist(err) {
		return
	}
	contents, err := os.ReadFile(filepath.Join(profile, "wipd.lock"))
	if err != nil {
		t.Errorf("read isolated test daemon PID: %v", err)
		return
	}
	pidText, ok := strings.CutPrefix(strings.TrimSpace(string(contents)), "pid=")
	if !ok {
		t.Errorf("invalid isolated test daemon PID file %q", contents)
		return
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Errorf("parse isolated test daemon PID %q: %v", pidText, err)
		return
	}
	commandLine, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil || !strings.Contains(string(commandLine), "--profile-root\x00"+profile) {
		t.Errorf("refusing to stop unrelated process for isolated test profile: cmdline %q, err %v", commandLine, err)
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Errorf("find isolated test daemon: %v", err)
		return
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !os.IsNotExist(err) {
		t.Errorf("stop isolated test daemon: %v", err)
	}
}
