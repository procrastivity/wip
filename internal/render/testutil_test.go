package render

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

var ctx = context.Background()

// newRepo creates a fresh git repo (no remotes) under t's temp dir and
// returns its absolute, real (symlink-resolved) path — matching what `git
// rev-parse --path-format=absolute` itself returns, so path comparisons in
// tests don't trip over a macOS /tmp -> /private/tmp symlink.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	run(t, real, "init", "-q")
	run(t, real, "config", "user.email", "test@example.com")
	run(t, real, "config", "user.name", "test")
	run(t, real, "commit", "--allow-empty", "-q", "-m", "init")
	return real
}

func addRemote(t *testing.T, dir, name, url string) {
	t.Helper()
	run(t, dir, "remote", "add", name, url)
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

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// setup opens a fresh store and a fresh git repo, runs `wip init` against it,
// and resolves Current the way every verb in this package does.
func setup(t *testing.T) (*store.Store, string, Current) {
	t.Helper()
	s := newStore(t)
	dir := newRepo(t)
	if _, err := tiers.Init(ctx, s, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("tiers.Init: %v", err)
	}
	cur, err := ResolveCurrent(ctx, s, store.ActorHuman, dir)
	if err != nil {
		t.Fatalf("ResolveCurrent: %v", err)
	}
	return s, dir, cur
}

// matter births a Matter and returns its locator.
func matter(t *testing.T, s *store.Store, repo, title string) string {
	t.Helper()
	n, err := writesurface.CreateMatter(ctx, s, store.ActorHuman, repo, title)
	if err != nil {
		t.Fatalf("CreateMatter: %v", err)
	}
	return n.Locator
}

// stage births a Stage under matterLocator.
func stage(t *testing.T, s *store.Store, repo, matterLocator, title string) string {
	t.Helper()
	n, err := writesurface.CreateStage(ctx, s, store.ActorHuman, repo, matterLocator, title)
	if err != nil {
		t.Fatalf("CreateStage: %v", err)
	}
	return n.Locator
}

func writeOnce(t *testing.T, s *store.Store, repo, locator string, kind store.ContentKind, data []byte) {
	t.Helper()
	if _, err := writesurface.WriteOnce(ctx, s, store.ActorHuman, repo, locator, kind, data); err != nil {
		t.Fatalf("WriteOnce(%s, %s): %v", locator, kind, err)
	}
}

// seal births nothing new: it finishes matterLocator and closes
// reviewed-local against it (declaring the gate first if this Repo hasn't
// yet) — Done + own gates closed coincides with sealed at Matter scale
// (D55).
func seal(t *testing.T, s *store.Store, repo, matterLocator string) {
	t.Helper()
	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range declared {
		if d.Gate == "reviewed-local" {
			found = true
		}
	}
	if !found {
		if err := writesurface.DeclareGate(ctx, s, repo, "reviewed-local", store.ScaleMatter); err != nil {
			t.Fatalf("DeclareGate: %v", err)
		}
	}
	if _, err := writesurface.Start(ctx, s, store.ActorHuman, repo, matterLocator); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := writesurface.Finish(ctx, s, store.ActorHuman, repo, matterLocator); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := writesurface.CloseGate(ctx, s, store.ActorHuman, repo, "reviewed-local", matterLocator); err != nil {
		t.Fatalf("CloseGate: %v", err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
