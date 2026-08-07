package run

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
)

func TestRunOutputDerivesOwnershipAndLivenessIndependently(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	fixture := newReadFixture(t)
	ctx := context.Background()

	ownedFree, err := output(ctx, fixture.s, fixture.ownerClone, fixture.run)
	if err != nil {
		t.Fatal(err)
	}
	wantRead(t, "owned free", ownedFree, "owned", runlock.LivenessInterrupted)

	ownedLock, err := runlock.Acquire(fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownedLive, err := output(ctx, fixture.s, fixture.ownerClone, fixture.run)
	_ = ownedLock.Release()
	if err != nil {
		t.Fatal(err)
	}
	wantRead(t, "owned held", ownedLive, "owned", runlock.LivenessLive)

	strandedFree, err := output(ctx, fixture.s, fixture.otherClone, fixture.run)
	if err != nil {
		t.Fatal(err)
	}
	wantRead(t, "stranded free", strandedFree, "stranded", runlock.LivenessInterrupted)

	strandedLock, err := runlock.Acquire(fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	strandedLive, err := output(ctx, fixture.s, fixture.otherClone, fixture.run)
	_ = strandedLock.Release()
	if err != nil {
		t.Fatal(err)
	}
	wantRead(t, "stranded held", strandedLive, "stranded", runlock.LivenessLive)
}

func TestRunOutputClosedShapeHasNullableLiveness(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	fixture := newReadFixture(t)
	ctx := context.Background()
	if _, err := fixture.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: fixture.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRunFinished, Subject: fixture.run.ID, Payload: store.RunFinished{}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	closed, err := fixture.s.Run(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := output(ctx, fixture.s, fixture.ownerClone, closed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Liveness != nil || got.LivenessCause != nil || got.CloseReason == nil || *got.CloseReason != "completed" {
		t.Fatalf("closed output = %+v, want null liveness and completed reason", got)
	}
}

func wantRead(t *testing.T, label string, got runOutput, ownership, liveness string) {
	t.Helper()
	if got.Ownership != ownership || got.Liveness == nil || *got.Liveness != liveness || got.LivenessCause != nil {
		t.Fatalf("%s = %+v, want ownership=%q liveness=%q", label, got, ownership, liveness)
	}
}

type readFixture struct {
	s          *store.Store
	env        store.Env
	run        store.Run
	ownerClone string
	otherClone string
}

func newReadFixture(t *testing.T) readFixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	commit := func(env store.Env, draft store.Draft) {
		if _, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
			return []store.Draft{draft}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	repo := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeRepoAttached, Subject: repo, Payload: store.RepoAttached{Label: "run-read"}})
	owner := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeCloneAttached, Subject: owner, Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/run-read-owner/.git", Label: "owner"}})
	ownerWorktree := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeWorktreeAttached, Subject: ownerWorktree, Payload: store.WorktreeAttached{Clone: owner}})
	other := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeCloneAttached, Subject: other, Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/run-read-other/.git", Label: "other"}})
	matter := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeMatterCreated, Subject: matter, Payload: store.NodeBirth{Locator: "run-read", Title: "Run read"}})
	batch := s.NewID()
	commit(store.Env{Repo: repo, Clone: owner, Worktree: ownerWorktree}, store.Draft{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "run-read"}})
	runID := s.NewID()
	commit(store.Env{Repo: repo, Clone: owner, Worktree: ownerWorktree}, store.Draft{Type: store.TypeRunStarted, Subject: runID, Payload: store.RunStarted{Batch: batch, Locator: "run-01", Matters: []string{matter}}})
	r, err := s.Run(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return readFixture{
		s: s, env: store.Env{Repo: repo, Clone: owner, Worktree: ownerWorktree},
		run: r, ownerClone: owner, otherClone: other,
	}
}
