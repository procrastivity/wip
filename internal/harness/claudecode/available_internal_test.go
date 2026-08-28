package claudecode

import (
	"os"
	"testing"
)

// These tests exercise Available()'s home-directory path (no SkillsDirEnv
// override), which requires swapping the unexported userHomeDir seam — only
// reachable from within the package, hence this internal (white-box) test
// file alongside the external claudecode_test package's own tests.

func TestAvailable_HomeRootExists(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()
	if err := os.MkdirAll(rootDir(home), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()

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

	if Available() {
		t.Error("Available() = true, want false when the harness root does not exist under home")
	}
}

func TestAvailable_HomeRootIsRegularFile(t *testing.T) {
	t.Setenv(SkillsDirEnv, "")
	home := t.TempDir()
	if err := os.WriteFile(rootDir(home), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = orig }()

	if Available() {
		t.Error("Available() = true, want false when the harness root path is a regular file")
	}
}
