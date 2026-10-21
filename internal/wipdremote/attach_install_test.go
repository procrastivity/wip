package wipdremote

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wipd"
)

func TestInstallAttachedCheckoutIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "wip.db")
	seed, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	repoID, cloneID, worktreeID := seed.NewID(), seed.NewID(), seed.NewID()
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	checkout := wipd.GitCheckout{
		CommonDir: "/tmp/attach/project/.git", GitDir: "/tmp/attach/project/.git",
		NormalizedRemote: "github.com/example/project", WorktreeName: "",
	}
	if err = installAttachedCheckout(ctx, database, repoID, cloneID, worktreeID, checkout); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err = installAttachedCheckout(ctx, database, repoID, cloneID, worktreeID, checkout); err != nil {
		t.Fatalf("exact install replay: %v", err)
	}
	local, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	installedStore := local
	t.Cleanup(func() {
		if closeErr := installedStore.Close(); closeErr != nil {
			t.Errorf("close installed local Store: %v", closeErr)
		}
	})
	repo, err := local.Repo(ctx, repoID)
	if err != nil || repo.RemoteURL != "" || repo.IdentityRemote != "" {
		t.Fatalf("receipt-driven Repo = %+v, %v", repo, err)
	}
	clone, found, err := local.CloneByCommonDir(ctx, checkout.CommonDir)
	if err != nil || !found || clone.ID != cloneID || clone.Repo != repoID {
		t.Fatalf("receipt-driven Clone = %+v found=%t err=%v", clone, found, err)
	}
	worktree, found, err := local.WorktreeByName(ctx, cloneID, "")
	if err != nil || !found || worktree.ID != worktreeID {
		t.Fatalf("receipt-driven Worktree = %+v found=%t err=%v", worktree, found, err)
	}

	beforeRepos, err := local.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeClones, err := local.ClonesOfRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	conflictingRepoID, conflictingCloneID, conflictingWorktreeID := local.NewID(), local.NewID(), local.NewID()
	if err = installAttachedCheckout(ctx, database, conflictingRepoID, conflictingCloneID, conflictingWorktreeID, checkout); err == nil {
		t.Fatal("conflicting common-dir mapping was accepted")
	}
	local, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	reopenedStore := local
	t.Cleanup(func() {
		if closeErr := reopenedStore.Close(); closeErr != nil {
			t.Errorf("close reopened local Store: %v", closeErr)
		}
	})
	afterRepos, err := local.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterClones, err := local.ClonesOfRepo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterRepos) != len(beforeRepos) || len(afterClones) != len(beforeClones) {
		t.Fatalf("conflict left partial local projection: repos %d→%d clones %d→%d", len(beforeRepos), len(afterRepos), len(beforeClones), len(afterClones))
	}
}
