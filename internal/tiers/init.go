package tiers

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// InitResult is what `wip init` produced.
type InitResult struct {
	Repo     store.Repo
	Clone    store.Clone
	Worktree store.Worktree

	RepoCreated  bool
	CloneCreated bool
}

// Init implements `wip init` against the tiers Brief's "Move detection",
// "Multi-remote rule" and "Null-remote identity and remote adoption"
// sections.
//
// It always births exactly one Worktree row for the location it runs from:
// the main worktree (empty name) when git-dir and git-common-dir agree, or a
// linked worktree's own git-assigned name when they do not. Whether it also
// births a Clone row, and a Repo row above that, depends on whether this
// common-dir and this remote are already known — worked example 2 is the
// case where neither is: a second `wip init`, run inside a linked worktree
// of an already-known Clone, creates only the Worktree row.
func Init(ctx context.Context, s *store.Store, actor store.Actor, dir, identityRemoteFlag string) (InitResult, error) {
	commonDir, err := gitCommonDir(ctx, dir)
	if err != nil {
		return InitResult{}, notAGitRepo(dir, err)
	}
	wtDir, err := gitDir(ctx, dir)
	if err != nil {
		return InitResult{}, notAGitRepo(dir, err)
	}
	worktreeName := ""
	if wtDir != commonDir {
		worktreeName = filepath.Base(wtDir)
	}

	existingClone, cloneFound, err := s.CloneByCommonDir(ctx, commonDir)
	if err != nil {
		return InitResult{}, err
	}
	if cloneFound {
		return initExistingClone(ctx, s, actor, existingClone, worktreeName)
	}
	return initNewClone(ctx, s, actor, dir, commonDir, worktreeName, identityRemoteFlag)
}

func notAGitRepo(dir string, err error) error {
	return wiperr.New("validation.not-a-git-repo",
		fmt.Sprintf("%s is not inside a git repository wip can read: %v", dir, err))
}

// initExistingClone is worked example 2's path: the Clone already exists,
// but this worktree of it does not yet — so `wip init` here births only the
// Worktree row.
func initExistingClone(ctx context.Context, s *store.Store, actor store.Actor, clone store.Clone, worktreeName string) (InitResult, error) {
	if _, found, err := s.WorktreeByName(ctx, clone.ID, worktreeName); err != nil {
		return InitResult{}, err
	} else if found {
		where := "the main worktree"
		if worktreeName != "" {
			where = fmt.Sprintf("worktree %q", worktreeName)
		}
		return InitResult{}, wiperr.New("validation.already-initialized",
			fmt.Sprintf("%s of clone %q is already known to wip", where, clone.Label))
	}

	repo, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		return InitResult{}, err
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo.ID}}
	var wtID string
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		wtID = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeWorktreeAttached,
			Subject: wtID,
			Payload: store.WorktreeAttached{Clone: clone.ID, Name: worktreeName},
		}}, nil
	}); err != nil {
		return InitResult{}, err
	}

	wt, err := s.Worktree(ctx, wtID)
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{Repo: repo, Clone: clone, Worktree: wt}, nil
}

// initNewClone births a Clone (and, if needed, the Repo above it) plus the
// Worktree row for the location `wip init` ran from — the ordinary first
// encounter with a directory wip has never seen.
func initNewClone(ctx context.Context, s *store.Store, actor store.Actor, dir, commonDir, worktreeName, identityRemoteFlag string) (InitResult, error) {
	remotes, err := gitRemotes(ctx, dir)
	if err != nil {
		return InitResult{}, err
	}

	identityName := identityRemoteFlag
	if identityName == "" {
		identityName = "origin"
	}

	var remoteURL string
	// A clone with literally no remotes at all is the unambiguous local-only
	// case (tiers Brief, "Null-remote identity"): there is nothing to guess
	// among, so it never hits the refusals below. Those refusals are for a
	// clone that *has* remotes but none of them is the one wip was told —
	// explicitly or by the `origin` convention — to trust.
	if len(remotes) > 0 {
		raw, present := remotes[identityName]
		if !present {
			if identityRemoteFlag != "" {
				return InitResult{}, wiperr.New("validation.unresolvable-identity-remote",
					fmt.Sprintf("--identity-remote %s names no remote on this clone", identityRemoteFlag))
			}
			return InitResult{}, wiperr.New("validation.no-identity-remote",
				"no origin remote and no --identity-remote given; pass --identity-remote <name> to choose which remote provides this Repo's identity")
		}
		remoteURL, err = NormalizeRemote(raw)
		if err != nil {
			return InitResult{}, wiperr.New("validation.unparseable-remote", err.Error())
		}
	}

	repoCreated := true
	var repo store.Repo
	if remoteURL != "" {
		found, ok, err := s.RepoByRemote(ctx, remoteURL)
		if err != nil {
			return InitResult{}, err
		}
		if ok {
			repo, repoCreated = found, false
		}
	}

	label := DefaultLabel(dir)
	if !repoCreated {
		clones, err := s.ClonesOfRepo(ctx, repo.ID)
		if err != nil {
			return InitResult{}, err
		}
		taken := map[string]bool{}
		for _, c := range clones {
			taken[c.Label] = true
		}
		if taken[label] {
			suggested, ok := SuggestLabel(dir, taken)
			if !ok {
				return InitResult{}, wiperr.New("validation.label-collision",
					fmt.Sprintf("clone label %q is already used in this Repo, and both fallback names are also taken", label))
			}
			label = suggested
		}
	}

	repoID := repo.ID
	if repoCreated {
		repoID = s.NewID()
	}

	var cloneID, wtID string
	req := store.Request{Actor: actor, Env: store.Env{Repo: repoID}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		var drafts []store.Draft
		if repoCreated {
			drafts = append(drafts, store.Draft{
				Type:    store.TypeRepoAttached,
				Subject: repoID,
				Payload: store.RepoAttached{RemoteURL: remoteURL, IdentityRemote: identityName},
			})
		}

		cloneID = tx.NewID()
		cloneIdx := len(drafts)
		drafts = append(drafts, store.Draft{
			Type:    store.TypeCloneAttached,
			Subject: cloneID,
			Cause:   0, // the Repo draft, when present, is always index 0; ignored otherwise
			Payload: store.CloneAttached{Repo: repoID, GitCommonDir: commonDir, Label: label},
		})

		wtID = tx.NewID()
		drafts = append(drafts, store.Draft{
			Type:    store.TypeWorktreeAttached,
			Subject: wtID,
			Cause:   cloneIdx,
			Payload: store.WorktreeAttached{Clone: cloneID, Name: worktreeName},
		})
		return drafts, nil
	}); err != nil {
		return InitResult{}, err
	}

	finalRepo, err := s.Repo(ctx, repoID)
	if err != nil {
		return InitResult{}, err
	}
	finalClone, err := s.Clone(ctx, cloneID)
	if err != nil {
		return InitResult{}, err
	}
	finalWT, err := s.Worktree(ctx, wtID)
	if err != nil {
		return InitResult{}, err
	}
	return InitResult{
		Repo: finalRepo, Clone: finalClone, Worktree: finalWT,
		RepoCreated: repoCreated, CloneCreated: true,
	}, nil
}
