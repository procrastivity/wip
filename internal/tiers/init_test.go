package tiers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

func wantWiperr(t *testing.T, err error, code string) {
	t.Helper()
	var werr *wiperr.Error
	if err == nil {
		t.Fatal("got nil error, want a wiperr")
	}
	if !asWiperr(err, &werr) {
		t.Fatalf("error %v is not a *wiperr.Error", err)
	}
	if werr.Code != code {
		t.Fatalf("error code = %q, want %q (message: %s)", werr.Code, code, werr.Message)
	}
}

func asWiperr(err error, target **wiperr.Error) bool {
	if w, ok := err.(*wiperr.Error); ok {
		*target = w
		return true
	}
	return false
}

// TestInit_LocalOnly is worked example 4's setup: a repo with no remotes at
// all is the unambiguous local-only case — no refusal, natural_key null.
func TestInit_LocalOnly(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "local-only")

	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.RepoCreated || !result.CloneCreated {
		t.Errorf("RepoCreated=%v CloneCreated=%v, want both true", result.RepoCreated, result.CloneCreated)
	}
	if result.Repo.RemoteURL != "" {
		t.Errorf("RemoteURL = %q, want empty (local-only, D37's nullable key)", result.Repo.RemoteURL)
	}
	if result.Repo.IdentityRemote != "origin" {
		t.Errorf("IdentityRemote = %q, want %q recorded even with no matching remote yet", result.Repo.IdentityRemote, "origin")
	}
	if result.Worktree.Name != "" {
		t.Errorf("Worktree.Name = %q, want empty (main worktree)", result.Worktree.Name)
	}
}

// TestInit_RemoteBecomesRepoIdentity confirms an ordinary remote clone gets
// its Repo keyed by the normalized origin URL.
func TestInit_RemoteBecomesRepoIdentity(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	result, err := Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.Repo.RemoteURL != "github.com/acme/widget" {
		t.Errorf("RemoteURL = %q, want the normalized origin", result.Repo.RemoteURL)
	}
}

// TestInit_SecondCloneAttachesToExistingRepo is worked example 1's
// widget-a/widget-b shape: a second clone of the same remote joins the same
// Repo rather than minting a new one.
func TestInit_SecondCloneAttachesToExistingRepo(t *testing.T) {
	s := newStore(t)
	a := newRepo(t, "widget-a")
	addRemote(t, a, "origin", "git@github.com:acme/widget.git")
	b := newRepo(t, "widget-b")
	addRemote(t, b, "origin", "https://github.com/acme/widget.git")

	first, err := Init(ctx, s, store.ActorHuman, a, "")
	if err != nil {
		t.Fatalf("Init(a): %v", err)
	}
	second, err := Init(ctx, s, store.ActorHuman, b, "")
	if err != nil {
		t.Fatalf("Init(b): %v", err)
	}
	if second.RepoCreated {
		t.Errorf("second init RepoCreated = %v, want false (same repo)", second.RepoCreated)
	}
	if first.Repo.ID != second.Repo.ID {
		t.Fatalf("two clones of the same normalized remote got different Repos: %s vs %s", first.Repo.ID, second.Repo.ID)
	}
	if first.Clone.ID == second.Clone.ID {
		t.Fatal("two different clones ended up sharing one Clone row")
	}
}

// TestInit_IdentityRemoteOverride is the origin-is-my-fork case: --identity-
// remote names which remote provides the key, and origin stays an ordinary
// remote alongside it.
func TestInit_IdentityRemoteOverride(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:me/widget-fork.git")
	addRemote(t, dir, "upstream", "git@github.com:acme/widget.git")

	result, err := Init(ctx, s, store.ActorHuman, dir, "upstream")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.Repo.RemoteURL != "github.com/acme/widget" {
		t.Errorf("RemoteURL = %q, want upstream's normal form", result.Repo.RemoteURL)
	}
	if result.Repo.IdentityRemote != "upstream" {
		t.Errorf("IdentityRemote = %q, want %q", result.Repo.IdentityRemote, "upstream")
	}
}

// TestInit_NoOriginNoOverrideRefuses covers the ambiguous case: remotes
// exist, but none is named origin and no override was given.
func TestInit_NoOriginNoOverrideRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "upstream", "git@github.com:acme/widget.git")

	_, err := Init(ctx, s, store.ActorHuman, dir, "")
	wantWiperr(t, err, "validation.no-identity-remote")
}

// TestInit_UnresolvableIdentityRemoteRefuses covers --identity-remote naming
// a remote that does not exist on this clone.
func TestInit_UnresolvableIdentityRemoteRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	_, err := Init(ctx, s, store.ActorHuman, dir, "nonexistent")
	wantWiperr(t, err, "validation.unresolvable-identity-remote")
}

// TestInit_ForkGetsADistinctRepo is worked example 5: two clones of
// genuinely different remotes, neither using --identity-remote, resolve to
// two distinct Repos — the multi-remote rule's default behavior.
func TestInit_ForkGetsADistinctRepo(t *testing.T) {
	s := newStore(t)
	upstream := newRepo(t, "widget-upstream")
	addRemote(t, upstream, "origin", "git@github.com:acme/widget.git")
	fork := newRepo(t, "widget-fork")
	addRemote(t, fork, "origin", "git@github.com:someone/widget-fork.git")

	a, err := Init(ctx, s, store.ActorHuman, upstream, "")
	if err != nil {
		t.Fatalf("Init(upstream): %v", err)
	}
	b, err := Init(ctx, s, store.ActorHuman, fork, "")
	if err != nil {
		t.Fatalf("Init(fork): %v", err)
	}
	if a.Repo.ID == b.Repo.ID {
		t.Fatal("upstream and fork resolved to the same Repo, want distinct Repos")
	}
}

// TestInit_LabelCollisionAutoResolves confirms init runs the
// collision-suggestion ladder silently rather than refusing outright, when
// the default basename is already taken in the target Repo.
func TestInit_LabelCollisionAutoResolves(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()

	first := filepath.Join(root, "widget")
	run(t, root, "init", "-q", "widget")
	addRemote(t, first, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, first, ""); err != nil {
		t.Fatalf("Init(first): %v", err)
	}

	// A second clone whose directory basename also happens to be "widget"
	// (a sibling checkout under a differently-named parent) collides on the
	// default label and must fall through to the parent-dir tier.
	collisionParent := filepath.Join(root, "widget-collision-parent")
	if err := os.MkdirAll(collisionParent, 0o755); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(collisionParent, "widget")
	run(t, collisionParent, "init", "-q", "widget")
	addRemote(t, second, "origin", "https://github.com/acme/widget.git")

	result, err := Init(ctx, s, store.ActorHuman, second, "")
	if err != nil {
		t.Fatalf("Init(second): %v", err)
	}
	if result.Clone.Label == "widget" {
		t.Fatal("second clone kept the colliding label, want the collision-suggestion ladder to have fired")
	}
	if !strings.Contains(result.Clone.Label, "widget-collision-parent") {
		t.Errorf("resolved label = %q, want it derived from the parent dir", result.Clone.Label)
	}
}

// TestInit_WorktreeInsideAlreadyKnownClone is worked example 2's setup: a
// linked worktree of an already-known Clone gets only a Worktree row, no new
// Clone or Repo.
func TestInit_WorktreeInsideAlreadyKnownClone(t *testing.T) {
	s := newStore(t)
	main := newRepo(t, "widget")
	addRemote(t, main, "origin", "git@github.com:acme/widget.git")

	first, err := Init(ctx, s, store.ActorHuman, main, "")
	if err != nil {
		t.Fatalf("Init(main): %v", err)
	}

	wtDir := addWorktree(t, main, main+"-wt", "feature")
	second, err := Init(ctx, s, store.ActorHuman, wtDir, "")
	if err != nil {
		t.Fatalf("Init(worktree): %v", err)
	}
	if second.RepoCreated || second.CloneCreated {
		t.Errorf("RepoCreated=%v CloneCreated=%v, want both false (only a Worktree row is new)", second.RepoCreated, second.CloneCreated)
	}
	if second.Clone.ID != first.Clone.ID {
		t.Fatal("the worktree's init resolved to a different Clone than its main worktree")
	}
	// git names a linked worktree after its target directory's basename by
	// default, not the branch checked out into it — "widget-wt" here, not
	// "feature". That basename is what `git rev-parse --git-dir` reports
	// (`<main>/.git/worktrees/<name>`), so it's what wip uses as the
	// Worktree's natural key too.
	if second.Worktree.Name != "widget-wt" {
		t.Errorf("Worktree.Name = %q, want git's own worktree name %q", second.Worktree.Name, "widget-wt")
	}
}

// TestInit_AlreadyInitializedRefuses covers re-running init against a
// location wip already knows — a location, not just a directory: the second
// call here targets the exact same (clone, worktree) pair as the first.
func TestInit_AlreadyInitializedRefuses(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")

	if _, err := Init(ctx, s, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	_, err := Init(ctx, s, store.ActorHuman, dir, "")
	wantWiperr(t, err, "validation.already-initialized")
}
