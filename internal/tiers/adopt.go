package tiers

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// AdoptIfPossible checks a Clone's Repo for a null natural key and, if the
// clone's own directory now carries a remote under the name `init` recorded
// as its `identity_remote`, adopts it in place — MODEL §5.3's "acquired, not
// assigned" applied to the Repo's own key (tiers Brief, "Null-remote
// identity and remote adoption"). It is a no-op, not an error, when the Repo
// is already keyed or when no matching remote has shown up yet: a remote
// added under a different name is not adoption and is left alone.
func AdoptIfPossible(ctx context.Context, s *store.Store, actor store.Actor, clone store.Clone, dir string) (store.Repo, error) {
	repo, err := s.Repo(ctx, clone.Repo)
	if err != nil {
		return store.Repo{}, err
	}
	if repo.RemoteURL != "" {
		return repo, nil
	}

	remotes, err := gitRemotes(ctx, dir)
	if err != nil {
		return store.Repo{}, err
	}
	name := repo.IdentityRemote
	if name == "" {
		name = "origin"
	}
	raw, present := remotes[name]
	if !present {
		return repo, nil
	}
	normalized, err := NormalizeRemote(raw)
	if err != nil {
		// A remote wip cannot parse is not this function's refusal to raise;
		// adoption simply does not fire yet.
		return repo, nil
	}

	if other, found, err := s.RepoByRemote(ctx, normalized); err != nil {
		return store.Repo{}, err
	} else if found && other.ID != repo.ID {
		return store.Repo{}, wiperr.New("refusal.repo-key-conflict",
			fmt.Sprintf("refused — adopting %s would collide with repo %s, which already claims it; "+
				"there is no repo-merge verb in Phase 1, so repo %s stays local-only",
				normalized, other.ID, repo.ID))
	}

	req := store.Request{Actor: actor, Env: store.Env{Repo: repo.ID}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeRepoKeyAdopted,
			Subject: repo.ID,
			Payload: store.RepoKeyAdopted{RemoteURL: normalized, IdentityRemote: name},
		}}, nil
	}); err != nil {
		return store.Repo{}, err
	}
	return s.Repo(ctx, repo.ID)
}

// ResolveCurrentClone resolves the Clone at dir's git-common-dir, applying
// remote adoption opportunistically (AdoptIfPossible) before returning it —
// "any tier-resolving verb" runs this check, per the Brief, not just `init`.
// The false result is the unknown-clone case (MODEL §11); it is the
// caller's job to decide whether that is a hard failure or, for `status`
// alone, the host-wide fallback the Brief's "Read scope" section carves out.
func ResolveCurrentClone(ctx context.Context, s *store.Store, actor store.Actor, dir string) (store.Clone, bool, error) {
	commonDir, err := gitCommonDir(ctx, dir)
	if err != nil {
		return store.Clone{}, false, nil
	}
	clone, found, err := s.CloneByCommonDir(ctx, commonDir)
	if err != nil {
		return store.Clone{}, false, err
	}
	if !found {
		return store.Clone{}, false, nil
	}
	if _, err := AdoptIfPossible(ctx, s, actor, clone, dir); err != nil {
		return store.Clone{}, false, err
	}
	return clone, true, nil
}

// CurrentWorktreeName reports which Worktree (by its natural-key name, empty
// for the main worktree) dir is currently inside, per git-dir vs
// git-common-dir — see gitDir's doc comment for why that comparison is the
// right one.
func CurrentWorktreeName(ctx context.Context, dir, commonDir string) (string, error) {
	wtDir, err := gitDir(ctx, dir)
	if err != nil {
		return "", err
	}
	if wtDir == commonDir {
		return "", nil
	}
	return filepath.Base(wtDir), nil
}
