package asset

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureName is embedded in the real assets tree (assets/chassis-selftest.txt)
// purely to exercise this resolution chain — see that file's own comment.
const fixtureName = "chassis-selftest.txt"

// withFakeInstall points DefaultDir's executable-resolution at a temporary
// <prefix>/bin/wip, and returns the temporary <prefix>/share/wip/assets
// directory the "installed" default would live in.
func withFakeInstall(t *testing.T) string {
	t.Helper()
	prefix := t.TempDir()

	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(binDir, "wip")
	if err := os.WriteFile(fakeExe, []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}

	prevExecutable := executable
	executable = func() (string, error) { return fakeExe, nil }
	t.Cleanup(func() { executable = prevExecutable })

	shareDir := filepath.Join(prefix, "share", appName, "assets")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return shareDir
}

// withOverrideDir points OverrideDir at a temporary $XDG_CONFIG_HOME/wip and
// returns it.
func withOverrideDir(t *testing.T) string {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	return filepath.Join(configHome, appName)
}

func TestResolve_OverridePresentWins(t *testing.T) {
	shareDir := withFakeInstall(t)
	overrideDir := withOverrideDir(t)

	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	overridePath := filepath.Join(overrideDir, fixtureName)
	if err := os.WriteFile(overridePath, []byte("override content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A default also present at the same relative path must lose — shadow
	// by name, no merging.
	if err := os.WriteFile(filepath.Join(shareDir, fixtureName), []byte("default content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(fixtureName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceOverride {
		t.Fatalf("Source = %v, want %v", resolved.Source, SourceOverride)
	}
	if resolved.Path != overridePath {
		t.Fatalf("Path = %q, want %q", resolved.Path, overridePath)
	}
	if got := string(resolved.Bytes()); got != "override content\n" {
		t.Fatalf("Bytes = %q, want override content", got)
	}
}

func TestResolve_OverrideAbsentDefaultWins(t *testing.T) {
	shareDir := withFakeInstall(t)
	withOverrideDir(t) // present but empty — no override file written

	defaultPath := filepath.Join(shareDir, fixtureName)
	if err := os.WriteFile(defaultPath, []byte("default content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve(fixtureName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceDefault {
		t.Fatalf("Source = %v, want %v", resolved.Source, SourceDefault)
	}
	if resolved.Path != defaultPath {
		t.Fatalf("Path = %q, want %q", resolved.Path, defaultPath)
	}
	if got := string(resolved.Bytes()); got != "default content\n" {
		t.Fatalf("Bytes = %q, want default content", got)
	}
}

func TestResolve_BothAbsentEmbeddedFallbackTriggers(t *testing.T) {
	withFakeInstall(t) // share dir exists but has no fixtureName file
	withOverrideDir(t) // config dir exists but has no fixtureName file

	resolved, err := Resolve(fixtureName)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceEmbedded {
		t.Fatalf("Source = %v, want %v", resolved.Source, SourceEmbedded)
	}
	if resolved.Path != "" {
		t.Fatalf("Path = %q, want empty for an embedded asset", resolved.Path)
	}
	if len(resolved.Bytes()) == 0 {
		t.Fatal("Bytes is empty, want the embedded fixture content")
	}
}

func TestResolve_NotFoundAnywhere(t *testing.T) {
	withFakeInstall(t)
	withOverrideDir(t)

	if _, err := Resolve("does-not-exist.txt"); err == nil {
		t.Fatal("Resolve: want an error when the asset is absent from every link in the chain")
	}
}
