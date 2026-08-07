package writesurface

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
)

func TestStandDownReapsDispatchAcrossKnownCloneAtomically(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	f := newRunFixture(t)
	ctx := context.Background()
	batch := f.commit(f.env, store.Draft{Type: store.TypeBatchCreated, Subject: f.s.NewID(), Payload: store.BatchCreated{Name: "stand-down"}})
	run := f.commit(f.env, store.Draft{Type: store.TypeRunStarted, Subject: f.s.NewID(), Payload: store.RunStarted{Batch: batch, Locator: "run-01", Matters: []string{f.matter}}})
	dispatch := f.commit(f.env, store.Draft{Type: store.TypeDispatchOpened, Subject: f.s.NewID(), Payload: store.DispatchOpened{Run: run, Matter: f.matter}})

	secondClone := f.s.NewID()
	f.commit(store.Env{Repo: f.env.Repo}, store.Draft{Type: store.TypeCloneAttached, Subject: secondClone, Payload: store.CloneAttached{Repo: f.env.Repo, GitCommonDir: "/tmp/stand-down-second/.git", Label: "second"}})
	secondWorktree := f.s.NewID()
	f.commit(store.Env{Repo: f.env.Repo}, store.Draft{Type: store.TypeWorktreeAttached, Subject: secondWorktree, Payload: store.WorktreeAttached{Clone: secondClone}})
	acting := store.Env{Repo: f.env.Repo, Clone: secondClone, Worktree: secondWorktree}

	closed, events, err := StandDownRun(ctx, f.s, store.ActorHuman, acting, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != store.TypeRunStoodDown || events[1].Type != store.TypeDispatchClosed {
		t.Fatalf("stand-down events = %#v, want run.stood-down then dispatch.closed", events)
	}
	if events[0].Clone != secondClone || events[0].Worktree != secondWorktree {
		t.Fatalf("stand-down dimensions = %s/%s, want acting Clone/Worktree", events[0].Clone, events[0].Worktree)
	}
	if events[1].Clone != f.env.Clone || events[1].Worktree != f.env.Worktree {
		t.Fatalf("reap dimensions = %s/%s, want Dispatch dimensions", events[1].Clone, events[1].Worktree)
	}
	if closed.Open || closed.CloseReason != store.CloseReason("stood-down") {
		t.Fatalf("closed Run = %#v", closed)
	}
	gotDispatch, err := f.s.Dispatch(ctx, dispatch)
	if err != nil || gotDispatch.Open || gotDispatch.CloseReason != store.CloseReaped {
		t.Fatalf("reaped Dispatch = %#v, err=%v", gotDispatch, err)
	}
	secondRun := f.commit(acting, store.Draft{Type: store.TypeRunStarted, Subject: f.s.NewID(), Payload: store.RunStarted{Batch: batch, Locator: "run-02", Matters: []string{f.matter}}})
	reopenedDispatch := f.commit(acting, store.Draft{Type: store.TypeDispatchOpened, Subject: f.s.NewID(), Payload: store.DispatchOpened{Run: secondRun, Matter: f.matter}})
	gotOpen, err := f.s.Dispatch(ctx, reopenedDispatch)
	if err != nil || !gotOpen.Open {
		t.Fatalf("released Dispatch claim did not reopen: %#v, err=%v", gotOpen, err)
	}
	if err := f.s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	gotDispatch, err = f.s.Dispatch(ctx, dispatch)
	if err != nil || gotDispatch.Open || gotDispatch.CloseReason != store.CloseReaped {
		t.Fatalf("reaped Dispatch after rebuild = %#v, err=%v", gotDispatch, err)
	}
	path := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	gotDispatch, err = reopened.Dispatch(ctx, dispatch)
	if err != nil || gotDispatch.Open || gotDispatch.CloseReason != store.CloseReaped {
		t.Fatalf("reaped Dispatch after reopen = %#v, err=%v", gotDispatch, err)
	}
}

func TestStandDownOwnCloneReapsMultipleDispatchesInBirthOrder(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	f := newRunFixture(t)
	ctx := context.Background()
	matterTwo := f.s.NewID()
	if _, err := f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: f.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeMatterCreated, Subject: matterTwo, Payload: store.NodeBirth{Locator: "stand-down-two", Title: "Stand down two"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	secondWorktree := f.s.NewID()
	if _, err := f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: f.env.Repo}}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeWorktreeAttached, Subject: secondWorktree, Payload: store.WorktreeAttached{Clone: f.env.Clone, Name: "second"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	secondEnv := store.Env{Repo: f.env.Repo, Clone: f.env.Clone, Worktree: secondWorktree}
	batch := f.commit(f.env, store.Draft{Type: store.TypeBatchCreated, Subject: f.s.NewID(), Payload: store.BatchCreated{Name: "multiple-dispatches"}})
	run := f.commit(f.env, store.Draft{Type: store.TypeRunStarted, Subject: f.s.NewID(), Payload: store.RunStarted{Batch: batch, Locator: "run-01", Matters: []string{f.matter, matterTwo}}})
	first := f.commit(f.env, store.Draft{Type: store.TypeDispatchOpened, Subject: f.s.NewID(), Payload: store.DispatchOpened{Run: run, Matter: f.matter}})
	second := f.commit(secondEnv, store.Draft{Type: store.TypeDispatchOpened, Subject: f.s.NewID(), Payload: store.DispatchOpened{Run: run, Matter: matterTwo}})

	before := eventCount(t, f.s)
	closed, events, err := StandDownRun(ctx, f.s, store.ActorHuman, f.env, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Type != store.TypeRunStoodDown || events[1].Subject != first || events[2].Subject != second {
		t.Fatalf("stand-down events = %#v, want Run then Dispatches in birth order", events)
	}
	if eventCount(t, f.s) != before+3 {
		t.Fatalf("stand-down appended %d events, want 3", eventCount(t, f.s)-before)
	}
	if closed.Open || closed.CloseReason != store.CloseReason("stood-down") {
		t.Fatalf("closed Run = %#v", closed)
	}
	for _, id := range []string{first, second} {
		dispatch, err := f.s.Dispatch(ctx, id)
		if err != nil || dispatch.Open || dispatch.CloseReason != store.CloseReaped {
			t.Fatalf("reaped Dispatch %s = %#v, err=%v", id, dispatch, err)
		}
	}
}

func TestStandDownRefusesHeldClosedAndMalformedRunsWithoutEvents(t *testing.T) {
	t.Setenv("WIP_RUNTIME_DIR", t.TempDir())
	f := newRunFixture(t)
	ctx := context.Background()
	batch := f.commit(f.env, store.Draft{Type: store.TypeBatchCreated, Subject: f.s.NewID(), Payload: store.BatchCreated{Name: "refusals"}})
	run := f.commit(f.env, store.Draft{Type: store.TypeRunStarted, Subject: f.s.NewID(), Payload: store.RunStarted{Batch: batch, Locator: "run-01", Matters: []string{f.matter}}})

	before := eventCount(t, f.s)
	lock, err := runlock.Acquire(run)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := StandDownRun(ctx, f.s, store.ActorHuman, f.env, run); err == nil {
		t.Fatal("held Run stand-down succeeded")
	}
	_ = lock.Release()
	if got := eventCount(t, f.s); got != before {
		t.Fatalf("held refusal changed event count from %d to %d", before, got)
	}

	f.commit(f.env, store.Draft{Type: store.TypeRunFinished, Subject: run, Payload: store.RunFinished{}})
	before = eventCount(t, f.s)
	if _, _, err := StandDownRun(ctx, f.s, store.ActorHuman, f.env, run); err == nil {
		t.Fatal("closed Run stand-down succeeded")
	}
	if got := eventCount(t, f.s); got != before {
		t.Fatalf("closed refusal changed event count from %d to %d", before, got)
	}
	if _, _, err := StandDownRun(ctx, f.s, store.ActorHuman, f.env, "short"); err == nil {
		t.Fatal("malformed Run identity succeeded")
	}
	before = eventCount(t, f.s)
	if _, _, err := StandDownRun(ctx, f.s, store.ActorHuman, f.env, f.s.NewID()); err == nil {
		t.Fatal("unknown Run succeeded")
	}
	if got := eventCount(t, f.s); got != before {
		t.Fatalf("unknown Run refusal changed event count from %d to %d", before, got)
	}
}

type runFixture struct {
	s      *store.Store
	env    store.Env
	matter string
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	commit := func(env store.Env, drafts ...store.Draft) {
		if _, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
			return drafts, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	repo := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeRepoAttached, Subject: repo, Payload: store.RepoAttached{Label: "fixture"}})
	clone := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeCloneAttached, Subject: clone, Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/stand-down/.git", Label: "main"}})
	worktree := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeWorktreeAttached, Subject: worktree, Payload: store.WorktreeAttached{Clone: clone}})
	matter := s.NewID()
	commit(store.Env{Repo: repo}, store.Draft{Type: store.TypeMatterCreated, Subject: matter, Payload: store.NodeBirth{Locator: "stand-down", Title: "Stand down"}})
	return &runFixture{s: s, env: store.Env{Repo: repo, Clone: clone, Worktree: worktree}, matter: matter}
}

func (f *runFixture) commit(env store.Env, draft store.Draft) string {
	ctx := context.Background()
	if draft.Subject == "" {
		draft.Subject = f.s.NewID()
	}
	if _, err := f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{draft}, nil
	}); err != nil {
		panic(err)
	}
	return draft.Subject
}

func eventCount(t *testing.T, s *store.Store) int {
	t.Helper()
	events, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}
