package tiers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// newRepo creates a fresh git repo (no remotes) under t's temp dir, at the
// given relative name, and returns its absolute path.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	return dir
}

// moveDir renames a directory in place, the way a user's `mv` would.
func moveDir(from, to string) error {
	return os.Rename(from, to)
}

func addRemote(t *testing.T, dir, name, url string) {
	t.Helper()
	run(t, dir, "remote", "add", name, url)
}

func addWorktree(t *testing.T, mainDir, worktreeDir, branch string) string {
	t.Helper()
	run(t, mainDir, "commit", "--allow-empty", "-q", "-m", "init")
	run(t, mainDir, "worktree", "add", "-q", "-b", branch, worktreeDir)
	return worktreeDir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// newStore opens a fresh store under t's temp dir.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var ctx = context.Background()
