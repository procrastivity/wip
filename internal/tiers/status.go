package tiers

import (
	"context"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// StatusView is what `wip status` (tier-scoped stub, step-08) renders: the
// whole of the tiers Brief's "Read scope" section and nothing else.
// `read-surface` extends this same verb with the founding "what is in
// progress" content, `next`, and Session on top of this Step's output,
// reading it as settled input rather than redoing the scoping.
type StatusView struct {
	// HostWide is true outside any known Clone: every Repo on this host,
	// per the Brief's deliberate, narrow exception to the unknown-clone hard
	// failure.
	HostWide bool
	Repos    []RepoStatus // populated when HostWide
	Repo     RepoStatus   // populated when !HostWide: the current Repo only
}

// RepoStatus is one Repo's clones, for repo-wide or host-wide rendering.
type RepoStatus struct {
	Repo   store.Repo
	Clones []CloneStatus
}

// CloneStatus is one Clone, its linked (non-main) worktrees, and whether
// this is where the command ran from.
type CloneStatus struct {
	Clone     store.Clone
	Current   bool
	Worktrees []WorktreeStatus
}

// WorktreeStatus is one linked Worktree of a Clone. The main worktree is
// never listed separately here — it is represented by its Clone row, D37's
// null-name case.
type WorktreeStatus struct {
	Worktree store.Worktree
	Current  bool
}

// Status implements `wip status`'s tier scoping exactly as the Brief states
// it: repo-wide with the current Clone (and current Worktree, if linked)
// marked inside a known Clone; host-wide outside one.
func Status(ctx context.Context, s *store.Store, actor store.Actor, dir string) (StatusView, error) {
	clone, found, err := ResolveCurrentClone(ctx, s, actor, dir)
	if err != nil {
		return StatusView{}, err
	}
	if !found {
		repos, err := s.Repos(ctx)
		if err != nil {
			return StatusView{}, err
		}
		out := StatusView{HostWide: true}
		for _, r := range repos {
			rs, err := repoStatus(ctx, s, r, "", "")
			if err != nil {
				return StatusView{}, err
			}
			out.Repos = append(out.Repos, rs)
		}
		return out, nil
	}

	repo, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		return StatusView{}, err
	}
	commonDir, err := gitCommonDir(ctx, dir)
	if err != nil {
		return StatusView{}, err
	}
	currentWTName, err := CurrentWorktreeName(ctx, dir, commonDir)
	if err != nil {
		return StatusView{}, err
	}
	rs, err := repoStatus(ctx, s, repo, clone.ID, currentWTName)
	if err != nil {
		return StatusView{}, err
	}
	return StatusView{Repo: rs}, nil
}

func repoStatus(ctx context.Context, s *store.Store, repo store.Repo, currentClone, currentWTName string) (RepoStatus, error) {
	clones, err := s.ClonesOfRepo(ctx, repo.ID)
	if err != nil {
		return RepoStatus{}, err
	}
	out := RepoStatus{Repo: repo}
	for _, c := range clones {
		// "Current" belongs to the most specific location: the Clone row
		// only when the command ran at its main worktree, never when it ran
		// inside one of that Clone's linked Worktrees (which mark
		// themselves current instead, below).
		cs := CloneStatus{Clone: c, Current: c.ID == currentClone && currentWTName == ""}
		wts, err := s.WorktreesOfClone(ctx, c.ID)
		if err != nil {
			return RepoStatus{}, err
		}
		for _, w := range wts {
			if w.Name == "" {
				continue // the main worktree is the Clone row itself
			}
			cs.Worktrees = append(cs.Worktrees, WorktreeStatus{
				Worktree: w,
				Current:  c.ID == currentClone && w.Name == currentWTName,
			})
		}
		out.Clones = append(out.Clones, cs)
	}
	return out, nil
}

// RepoHeader is the display name `wip status` prints above a Repo's clones:
// the normalized remote's path with the host dropped (github.com/acme/widget
// -> acme/widget) when the Repo has a remote, or a local-only marker naming
// its ULID when it does not.
func RepoHeader(repo store.Repo) string {
	if repo.RemoteURL == "" {
		return "local repo " + repo.ID
	}
	if i := strings.Index(repo.RemoteURL, "/"); i >= 0 {
		return repo.RemoteURL[i+1:]
	}
	return repo.RemoteURL
}
