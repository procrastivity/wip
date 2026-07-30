package trackedwip_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/guards/trackedwip"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
)

var ctx = context.Background()

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	run(t, resolved, "init", "-q")
	run(t, resolved, "config", "user.email", "test@example.com")
	run(t, resolved, "config", "user.name", "test")
	run(t, resolved, "commit", "--allow-empty", "-q", "-m", "init")
	return resolved
}

func TestIsWipTracked_UntrackedByDefault(t *testing.T) {
	dir := newRepo(t)
	tracked, err := trackedwip.IsWipTracked(ctx, dir)
	if err != nil {
		t.Fatalf("IsWipTracked: %v", err)
	}
	if tracked {
		t.Error("a fresh repo with no .wip/ committed should not report tracked")
	}
}

func TestIsWipTracked_TrueOnceCommitted(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wip", "old-committed-file"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".wip")
	run(t, dir, "commit", "-q", "-m", "accidentally track .wip/")

	tracked, err := trackedwip.IsWipTracked(ctx, dir)
	if err != nil {
		t.Fatalf("IsWipTracked: %v", err)
	}
	if !tracked {
		t.Error("a repo with .wip/ committed should report tracked")
	}
}

func TestIsWipTracked_FalseAfterUntracking(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wip", "old-committed-file"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".wip")
	run(t, dir, "commit", "-q", "-m", "accidentally track .wip/")
	run(t, dir, "rm", "-r", "--cached", "-q", ".wip")
	run(t, dir, "commit", "-q", "-m", "untrack .wip/, per the guidance")

	tracked, err := trackedwip.IsWipTracked(ctx, dir)
	if err != nil {
		t.Fatalf("IsWipTracked: %v", err)
	}
	if tracked {
		t.Error("following the ratified guidance (git rm -r --cached) should leave .wip/ untracked")
	}
}

func TestRenderPrecondition_RefusesOnTrackedWipDir(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wip", "old-committed-file"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".wip")
	run(t, dir, "commit", "-q", "-m", "accidentally track .wip/")

	err := trackedwip.RenderPrecondition(ctx, render.Current{Root: dir})
	if err == nil {
		t.Fatal("RenderPrecondition = nil, want a refusal against a tracked .wip/")
	}
	if err.Error() == "" {
		t.Error("refusal carries no message")
	}
}

func TestRenderPrecondition_PassesOnUntrackedWipDir(t *testing.T) {
	dir := newRepo(t)
	if err := trackedwip.RenderPrecondition(ctx, render.Current{Root: dir}); err != nil {
		t.Fatalf("RenderPrecondition = %v, want nil against an untracked repo", err)
	}
}

func TestCheckTrackedWipDir_DoctorFinding(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".wip"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".wip", "old-committed-file"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".wip")
	run(t, dir, "commit", "-q", "-m", "accidentally track .wip/")

	findings, err := trackedwip.CheckTrackedWipDir(ctx, (*store.Store)(nil), dir)
	if err != nil {
		t.Fatalf("CheckTrackedWipDir: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly 1", findings)
	}
	if findings[0].Code != "refusal.tracked-wip-dir" {
		t.Errorf("code = %q, want %q", findings[0].Code, "refusal.tracked-wip-dir")
	}
}

func TestCheckTrackedWipDir_CleanRepoReportsNothing(t *testing.T) {
	dir := newRepo(t)
	findings, err := trackedwip.CheckTrackedWipDir(ctx, (*store.Store)(nil), dir)
	if err != nil {
		t.Fatalf("CheckTrackedWipDir: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none", findings)
	}
}

// TestCheckTrackedWipDir_ResolvesRootFromASubdirectory confirms the doctor
// call site (unlike the render precondition's already-resolved Current) has
// to find the worktree root itself, from wherever `wip doctor` was run.
func TestCheckTrackedWipDir_ResolvesRootFromASubdirectory(t *testing.T) {
	dir := newRepo(t)
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	findings, err := trackedwip.CheckTrackedWipDir(ctx, (*store.Store)(nil), sub)
	if err != nil {
		t.Fatalf("CheckTrackedWipDir: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none from an untracked repo run from a subdirectory", findings)
	}
}
