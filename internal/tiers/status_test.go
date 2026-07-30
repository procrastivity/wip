package tiers

import (
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestStatus_HostWideOutsideAnyKnownClone is the Brief's "Read scope"
// exception in the other direction: outside any known Clone, status never
// refuses — it degrades to every Repo on the host.
func TestStatus_HostWideOutsideAnyKnownClone(t *testing.T) {
	s := newStore(t)
	dir := newRepo(t, "widget")
	addRemote(t, dir, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	elsewhere := newRepo(t, "elsewhere") // never wip init'd

	view, err := Status(ctx, s, store.ActorHuman, elsewhere)
	if err != nil {
		t.Fatalf("Status outside a known clone must never refuse (Brief's own carve-out): %v", err)
	}
	if !view.HostWide {
		t.Fatal("HostWide = false, want true")
	}
	if len(view.Repos) != 1 || view.Repos[0].Repo.RemoteURL != "github.com/acme/widget" {
		t.Errorf("host-wide repos = %+v, want exactly the one known repo", view.Repos)
	}
}

// TestStatus_RepoWideInsideAKnownClone confirms the repo-wide view from
// inside a known Clone, with the current Clone marked.
func TestStatus_RepoWideInsideAKnownClone(t *testing.T) {
	s := newStore(t)
	a := newRepo(t, "widget-a")
	addRemote(t, a, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, a, ""); err != nil {
		t.Fatalf("Init(a): %v", err)
	}
	b := newRepo(t, "widget-b")
	addRemote(t, b, "origin", "https://github.com/acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, b, ""); err != nil {
		t.Fatalf("Init(b): %v", err)
	}

	view, err := Status(ctx, s, store.ActorHuman, a)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if view.HostWide {
		t.Fatal("HostWide = true from inside a known clone, want false")
	}
	if len(view.Repo.Clones) != 2 {
		t.Fatalf("repo-wide clones = %d, want 2 (widget-a, widget-b)", len(view.Repo.Clones))
	}
	var sawCurrent int
	for _, c := range view.Repo.Clones {
		if c.Current {
			sawCurrent++
			if c.Clone.Label != "widget-a" {
				t.Errorf("current clone = %q, want %q", c.Clone.Label, "widget-a")
			}
		}
	}
	if sawCurrent != 1 {
		t.Errorf("exactly one clone should be marked current, saw %d", sawCurrent)
	}
}

// TestStatus_CurrentWorktreeMarksTheWorktreeNotTheClone confirms "current"
// belongs to the most specific location: a linked worktree, not its parent
// Clone row, when the command runs from inside that worktree.
func TestStatus_CurrentWorktreeMarksTheWorktreeNotTheClone(t *testing.T) {
	s := newStore(t)
	main := newRepo(t, "widget")
	addRemote(t, main, "origin", "git@github.com:acme/widget.git")
	if _, err := Init(ctx, s, store.ActorHuman, main, ""); err != nil {
		t.Fatalf("Init(main): %v", err)
	}
	wtDir := addWorktree(t, main, main+"-feature", "feature")
	if _, err := Init(ctx, s, store.ActorHuman, wtDir, ""); err != nil {
		t.Fatalf("Init(worktree): %v", err)
	}

	view, err := Status(ctx, s, store.ActorHuman, wtDir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(view.Repo.Clones) != 1 {
		t.Fatalf("clones = %d, want 1", len(view.Repo.Clones))
	}
	clone := view.Repo.Clones[0]
	if clone.Current {
		t.Error("the Clone row is marked current, want only its linked worktree to be")
	}
	if len(clone.Worktrees) != 1 || !clone.Worktrees[0].Current {
		t.Errorf("worktrees = %+v, want exactly one, marked current", clone.Worktrees)
	}
}

func TestRepoHeader(t *testing.T) {
	remote := store.Repo{RemoteURL: "github.com/acme/widget"}
	if got := RepoHeader(remote); got != "acme/widget" {
		t.Errorf("RepoHeader(remote) = %q, want %q", got, "acme/widget")
	}
	local := store.Repo{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	if got := RepoHeader(local); got != "local repo 01ARZ3NDEKTSV4RRFFQ69G5FAV" {
		t.Errorf("RepoHeader(local) = %q, want the local-repo form", got)
	}
}
