package wipdjournal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/wipdprofile"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("make test temp directory private: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat private test temp directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("test temp directory mode = %04o, want owner-only", info.Mode().Perm())
	}
	return dir
}

func TestPrivateTempDirRejectsOldWritableFixturePattern(t *testing.T) {
	unsafe := t.TempDir()
	if err := os.Chmod(unsafe, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := wipdprofile.Resolve(filepath.Join(unsafe, "journal")); !errors.Is(err, wipdprofile.ErrUnsafeRoot) {
		t.Fatalf("Resolve() for old writable fixture pattern = %v, want unsafe-root refusal", err)
	}

	if _, err := wipdprofile.Resolve(filepath.Join(privateTempDir(t), "journal")); err != nil {
		t.Fatalf("Resolve() for private fixture ancestor: %v", err)
	}
}
