package runlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAcquireAndProbeHeldVersusFree(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	const run = "01KZ7XHAQT1S46NYPN1PW1DX36"

	free, err := Probe(run)
	if err != nil || free.State != LivenessInterrupted || free.Cause != "" {
		t.Fatalf("free probe = %#v, err=%v", free, err)
	}
	lock, err := Acquire(run)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	held, err := Probe(run)
	if err != nil || held.State != LivenessLive {
		t.Fatalf("held probe = %#v, err=%v", held, err)
	}
	if _, err := Acquire(run); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire = %v, want ErrHeld", err)
	}
	path := filepath.Join(mustRoot(t), run+".lock")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o, want 600", info.Mode().Perm())
	}
}

func TestProcessExitReleasesLock(t *testing.T) {
	if os.Getenv("WIP_RUNLOCK_HELPER") == "1" {
		lock, err := Acquire("01KZ7XHAQT1S46NYPN1PW1DX36")
		if err != nil {
			os.Exit(2)
		}
		_ = lock
		os.Exit(0)
	}
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessExitReleasesLock")
	cmd.Env = append(os.Environ(), "WIP_RUNLOCK_HELPER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v (%s)", err, out)
	}
	got, err := Probe("01KZ7XHAQT1S46NYPN1PW1DX36")
	if err != nil || got.State != LivenessInterrupted {
		t.Fatalf("post-exit probe = %#v, err=%v", got, err)
	}
}

func TestRootSelection(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		t.Setenv("WIP_RUNTIME_DIR", "/override")
		got, err := Root()
		if err != nil || got != "/override" {
			t.Fatalf("Root = %q, err=%v", got, err)
		}
	})
	t.Run("runtime", func(t *testing.T) {
		t.Setenv("WIP_RUNTIME_DIR", "")
		t.Setenv("XDG_RUNTIME_DIR", "/runtime")
		t.Setenv("XDG_STATE_HOME", "/state")
		got, err := Root()
		want := filepath.Join("/runtime", "wip", mustHost(t), "run")
		if err != nil || got != want {
			t.Fatalf("Root = %q, err=%v, want %q", got, err, want)
		}
	})
	t.Run("state", func(t *testing.T) {
		t.Setenv("WIP_RUNTIME_DIR", "")
		t.Setenv("XDG_RUNTIME_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/state")
		got, err := Root()
		want := filepath.Join("/state", "wip", mustHost(t), "run")
		if err != nil || got != want {
			t.Fatalf("Root = %q, err=%v, want %q", got, err, want)
		}
	})
}

func mustRoot(t *testing.T) string {
	t.Helper()
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustHost(t *testing.T) string {
	t.Helper()
	got, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return got
}
