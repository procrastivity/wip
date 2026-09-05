package amp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These tests exercise Available()'s home-directory path (no SkillsDirEnv
// override), which requires swapping the unexported userHomeDir seam — only
// reachable from within the package, hence this internal (white-box) test
// file alongside the external amp_test package's own tests.

func TestAvailable_HomeRootExists(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()
	if err := os.MkdirAll(rootDir(home), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = origLookPath }()

	if !Available() {
		t.Error("Available() = false, want true when the harness root exists under home")
	}
}

func TestAvailable_HomeRootMissing(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = origLookPath }()

	if Available() {
		t.Error("Available() = true, want false when the harness root does not exist under home")
	}
}

func TestAvailable_HomeRootIsRegularFile(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(rootDir(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootDir(home), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = origLookPath }()

	if Available() {
		t.Error("Available() = true, want false when the harness root path is a regular file")
	}
}

func TestAvailable_ExecutableOnPath(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(name string) (string, error) {
		if name != Name {
			t.Fatalf("lookPath name = %q, want %q", name, Name)
		}
		return "/usr/local/bin/amp", nil
	}
	defer func() { lookPath = origLookPath }()

	if !Available() {
		t.Error("Available() = false, want true when the amp executable is on PATH")
	}
}

func TestAvailable_HomeAndPathLookupFail(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")

	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = origLookPath }()

	if Available() {
		t.Error("Available() = true, want false when home and executable lookup fail")
	}
}

func TestAvailable_SharedSkillsDirAloneIsNotPresence(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()
	origLookPath := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { lookPath = origLookPath }()

	if Available() {
		t.Error("Available() = true, want false when only the shared skills directory exists")
	}
}

func TestSkillsDir_DefaultsToGlobalAgentSkills(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()

	dir, err := SkillsDir()
	if err != nil {
		t.Fatalf("SkillsDir: %v", err)
	}
	want := filepath.Join(home, ".config", "agents", "skills")
	if dir != want {
		t.Fatalf("SkillsDir = %q, want %q", dir, want)
	}
}

func TestSkillsDir_HomeLookupFailure(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")

	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	defer func() { userHomeDir = orig }()

	if _, err := SkillsDir(); err == nil {
		t.Fatal("SkillsDir error = nil, want home lookup failure")
	}
}
