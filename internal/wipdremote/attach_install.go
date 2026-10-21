package wipdremote

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wipd"
)

func preflightAttachedCheckout(ctx context.Context, databasePath, repoID string, checkout wipd.GitCheckout) error {
	if _, err := os.Lstat(databasePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	local, err := store.Open(databasePath)
	if err != nil {
		return err
	}
	defer func() { _ = local.Close() }()
	repo, found, err := local.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if found && (repo.RemoteURL != "" || repo.IdentityRemote != "") {
		return errors.New("wipdremote: local Repo has an incompatible remote before attach submission")
	}
	if _, found, err = local.CloneByCommonDir(ctx, checkout.CommonDir); err != nil {
		return err
	} else if found {
		return errors.New("wipdremote: live checkout already has a local Clone before attach submission")
	}
	return nil
}

// installAttachedCheckout projects an already verified authority receipt into
// the host Store. The caller supplies only receipt-derived identity and a
// daemon-derived checkout; this function never allocates Repo/Clone/Worktree
// identities or changes a Repo remote binding.
func installAttachedCheckout(ctx context.Context, databasePath string, repoID, cloneID, worktreeID string, checkout wipd.GitCheckout) (resultErr error) {
	if ctx == nil || databasePath == "" || repoID == "" || cloneID == "" || worktreeID == "" ||
		!filepath.IsAbs(checkout.CommonDir) || !filepath.IsAbs(checkout.GitDir) {
		return errors.New("wipdremote: invalid receipt-driven checkout installation")
	}
	local, err := store.Open(databasePath)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, local.Close()) }()
	req := store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repoID}}
	_, err = local.Commit(ctx, req, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		drafts := make([]store.Draft, 0, 3)
		repo, repoFound, err := tx.RepoByID(ctx, repoID)
		if err != nil {
			return nil, err
		}
		if !repoFound {
			// Repo absence is the only case in which the exact D115 identity is
			// materialized. A different remote-bearing Repo is never rewritten.
			drafts = append(drafts, store.Draft{
				Type: store.TypeRepoAttached, Subject: repoID,
				Payload: store.RepoAttached{},
			})
		} else if repo.ID != repoID || repo.RemoteURL != "" || repo.IdentityRemote != "" {
			return nil, errors.New("wipdremote: local Repo identity conflicts with the D115 receipt")
		}

		clone, cloneFound, err := tx.CloneByCommonDir(ctx, checkout.CommonDir)
		if err != nil {
			return nil, err
		}
		if cloneFound {
			if clone.ID != cloneID || clone.Repo != repoID {
				return nil, errors.New("wipdremote: local common-dir mapping conflicts with attach receipt")
			}
		} else {
			if _, found, lookupErr := tx.CloneByID(ctx, cloneID); lookupErr != nil {
				return nil, lookupErr
			} else if found {
				return nil, errors.New("wipdremote: attach CloneID is already assigned elsewhere")
			}
			drafts = append(drafts, store.Draft{
				Type: store.TypeCloneAttached, Subject: cloneID,
				Cause: 0,
				Payload: store.CloneAttached{
					Repo: repoID, GitCommonDir: checkout.CommonDir,
					Label: filepath.Base(checkout.CommonDir),
				},
			})
		}

		worktree, worktreeFound, err := tx.WorktreeByName(ctx, cloneID, checkout.WorktreeName)
		if err != nil {
			return nil, err
		}
		if worktreeFound {
			if worktree.ID != worktreeID || worktree.Clone != cloneID {
				return nil, errors.New("wipdremote: local Worktree mapping conflicts with attach receipt")
			}
		} else {
			if _, found, lookupErr := tx.WorktreeByID(ctx, worktreeID); lookupErr != nil {
				return nil, lookupErr
			} else if found {
				return nil, errors.New("wipdremote: attach WorktreeID is already assigned elsewhere")
			}
			cause := len(drafts) - 1
			if cause < 0 {
				// Existing Repo and Clone: preserve the event causation rule by
				// making the Worktree event its own command origin.
				cause = 0
			}
			drafts = append(drafts, store.Draft{
				Type: store.TypeWorktreeAttached, Subject: worktreeID, Cause: cause,
				Payload: store.WorktreeAttached{Clone: cloneID, Name: checkout.WorktreeName},
			})
		}
		return drafts, nil
	})
	if errors.Is(err, store.ErrNoEvent) {
		return nil
	}
	return err
}
