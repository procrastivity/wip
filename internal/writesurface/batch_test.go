package writesurface

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

type batchFixture struct {
	s      *store.Store
	env    store.Env
	matter string
}

func newBatchFixture(t *testing.T, title string) batchFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	repo := s.NewID()
	commit := func(req store.Request, drafts ...store.Draft) {
		if _, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) { return drafts, nil }); err != nil {
			t.Fatal(err)
		}
	}
	commit(store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}, store.Draft{Type: store.TypeRepoAttached, Subject: repo, Payload: store.RepoAttached{Label: "fixture"}})
	clone := s.NewID()
	commit(store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}, store.Draft{Type: store.TypeCloneAttached, Subject: clone, Payload: store.CloneAttached{Repo: repo, GitCommonDir: "/tmp/fixture/.git", Label: "main"}})
	worktree := s.NewID()
	commit(store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}, store.Draft{Type: store.TypeWorktreeAttached, Subject: worktree, Payload: store.WorktreeAttached{Clone: clone}})
	matter := s.NewID()
	commit(store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}, store.Draft{Type: store.TypeMatterCreated, Subject: matter, Payload: store.NodeBirth{Locator: title, Title: title}})
	return batchFixture{s: s, env: store.Env{Repo: repo, Clone: clone, Worktree: worktree}, matter: matter}
}

func TestNamedBatchCommandsAndLifecycle(t *testing.T) {
	f := newBatchFixture(t, "batch-matter")
	ctx := context.Background()
	b, err := CreateBatch(ctx, f.s, store.ActorHuman, f.env, "release")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := JoinBatch(ctx, f.s, store.ActorHuman, f.env, b.ID, f.matter); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LeaveBatch(ctx, f.s, store.ActorHuman, f.env, "release", f.matter); err != nil {
		t.Fatal(err)
	}
	b, err = DismissBatch(ctx, f.s, store.ActorHuman, f.env, "release")
	if err != nil || b.State != "closed" || b.CloseReason != store.BatchDismissed {
		t.Fatalf("dismissed Batch = %#v, err=%v", b, err)
	}
	if _, err := CreateBatch(ctx, f.s, store.ActorHuman, f.env, "release"); err == nil {
		t.Fatal("duplicate Batch name succeeded")
	}
}

func TestUserBatchCommandsRefuseAnonymousMembership(t *testing.T) {
	f := newBatchFixture(t, "anonymous-membership")
	ctx := context.Background()
	anonymous, err := CreateAnonymousBatch(ctx, f.s, store.ActorHuman, f.env, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := JoinBatch(ctx, f.s, store.ActorHuman, f.env, anonymous.ID, f.matter); err == nil {
		t.Fatal("user-facing join accepted an anonymous Batch")
	} else {
		var structured *wiperr.Error
		if !errors.As(err, &structured) || structured.Code != "refusal.anonymous-batch-membership" {
			t.Fatalf("anonymous join error = %v, want refusal.anonymous-batch-membership", err)
		}
	}
	if _, _, err := LeaveBatch(ctx, f.s, store.ActorHuman, f.env, anonymous.ID, f.matter); err == nil {
		t.Fatal("user-facing leave accepted an anonymous Batch")
	} else {
		var structured *wiperr.Error
		if !errors.As(err, &structured) || structured.Code != "refusal.anonymous-batch-membership" {
			t.Fatalf("anonymous leave error = %v, want refusal.anonymous-batch-membership", err)
		}
	}
	members, err := f.s.BatchMembers(ctx, anonymous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0] != f.matter {
		t.Fatalf("anonymous members = %v, want exactly [%s]", members, f.matter)
	}
	if _, err := CreateAnonymousBatch(ctx, f.s, store.ActorHuman, f.env, f.matter); err == nil {
		t.Fatal("created a second anonymous Batch for one Matter")
	} else {
		var structured *wiperr.Error
		if !errors.As(err, &structured) || structured.Code != "refusal.anonymous-batch-exists" {
			t.Fatalf("second anonymous Batch error = %v, want refusal.anonymous-batch-exists", err)
		}
	}
}

func TestAnonymousBatchSweepIsAtomicWithMatterSeal(t *testing.T) {
	f := newBatchFixture(t, "sweep-me")
	ctx := context.Background()
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "sweep-me"); err != nil {
		t.Fatal(err)
	}
	b, err := CreateAnonymousBatch(ctx, f.s, store.ActorHuman, f.env, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	run := f.s.NewID()
	if _, err := f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: f.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: b.ID, Locator: "run-01", Matters: []string{f.matter}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	dispatch := f.s.NewID()
	if _, err := f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: f.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeDispatchOpened, Subject: dispatch, Payload: store.DispatchOpened{Run: run, Matter: f.matter}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "sweep-me"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.s.Batch(ctx, b.ID)
	if got.State != "closed" || got.CloseReason != store.BatchSwept {
		t.Fatalf("Batch after Matter finish = %#v", got)
	}
	gotRun, _ := f.s.Run(ctx, run)
	gotDispatch, _ := f.s.Dispatch(ctx, dispatch)
	if gotRun.Open || gotRun.CloseReason != store.CloseReaped || gotRun.LastEvent != got.LastEvent {
		t.Fatalf("Run after sweep = %#v", gotRun)
	}
	if gotDispatch.Open || gotDispatch.CloseReason != store.CloseReaped || gotDispatch.LastEvent != got.LastEvent {
		t.Fatalf("Dispatch after sweep = %#v", gotDispatch)
	}
}

func TestBatchSealBoundaryCasesAndEventCounts(t *testing.T) {
	ctx := context.Background()

	stepFixture := newBatchFixture(t, "step-gate-boundary")
	step, err := CreateStep(ctx, stepFixture.s, store.ActorHuman, stepFixture.env.Repo, "step-gate-boundary", "Step gate")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeclareGate(ctx, stepFixture.s, stepFixture.env.Repo, "step-review", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	anonymous, err := CreateAnonymousBatch(ctx, stepFixture.s, store.ActorHuman, stepFixture.env, stepFixture.matter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CloseGateWithEnv(ctx, stepFixture.s, store.ActorHuman, stepFixture.env, "step-review", "step-gate-boundary/"+step.Locator); err != nil {
		t.Fatal(err)
	}
	live, _ := stepFixture.s.Batch(ctx, anonymous.ID)
	if live.State != "live" {
		t.Fatalf("Step gate closed an anonymous Batch before Matter seal: %#v", live)
	}
	if _, err := Start(ctx, stepFixture.s, store.ActorHuman, stepFixture.env.Repo, stepFixture.matter); err != nil {
		t.Fatal(err)
	}
	before := lenEvents(t, stepFixture.s)
	if _, err := FinishWithEnv(ctx, stepFixture.s, store.ActorHuman, stepFixture.env, "step-gate-boundary"); err != nil {
		t.Fatal(err)
	}
	if got := lenEvents(t, stepFixture.s) - before; got != 2 {
		t.Fatalf("Matter finish with anonymous sweep emitted %d events, want sealing event plus one sweep", got)
	}

	gateFixture := newBatchFixture(t, "incomplete-matter-gates")
	for _, gate := range []string{"review", "approve"} {
		if err := DeclareGate(ctx, gateFixture.s, gateFixture.env.Repo, gate, store.ScaleMatter); err != nil {
			t.Fatal(err)
		}
	}
	gateBatch, err := CreateAnonymousBatch(ctx, gateFixture.s, store.ActorHuman, gateFixture.env, gateFixture.matter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, gateFixture.s, store.ActorHuman, gateFixture.env.Repo, gateFixture.matter); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, gateFixture.s, store.ActorHuman, gateFixture.env, "incomplete-matter-gates"); err != nil {
		t.Fatal(err)
	}
	if got, _ := gateFixture.s.Batch(ctx, gateBatch.ID); got.State != "live" {
		t.Fatalf("incomplete Matter gates swept Batch: %#v", got)
	}
	if _, err := CloseGateWithEnv(ctx, gateFixture.s, store.ActorHuman, gateFixture.env, "review", "incomplete-matter-gates"); err != nil {
		t.Fatal(err)
	}
	if got, _ := gateFixture.s.Batch(ctx, gateBatch.ID); got.State != "live" {
		t.Fatalf("first Matter gate swept Batch: %#v", got)
	}
	if _, err := CloseGateWithEnv(ctx, gateFixture.s, store.ActorHuman, gateFixture.env, "approve", "incomplete-matter-gates"); err != nil {
		t.Fatal(err)
	}
	if got, _ := gateFixture.s.Batch(ctx, gateBatch.ID); got.State != "closed" || got.CloseReason != store.BatchSwept {
		t.Fatalf("final Matter gate did not sweep Batch: %#v", got)
	}

	unsealed := newBatchFixture(t, "unsealed-boundary")
	if err := DeclareGate(ctx, unsealed.s, unsealed.env.Repo, "review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	unsealedBatch, err := CreateAnonymousBatch(ctx, unsealed.s, store.ActorHuman, unsealed.env, unsealed.matter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CloseGateWithEnv(ctx, unsealed.s, store.ActorHuman, unsealed.env, "review", "unsealed-boundary"); err != nil {
		t.Fatal(err)
	}
	if got, _ := unsealed.s.Batch(ctx, unsealedBatch.ID); got.State != "live" {
		t.Fatalf("gate close on an unsealed Matter swept Batch: %#v", got)
	}

	namedFixture := newBatchFixture(t, "named-does-not-sweep")
	named, err := CreateBatch(ctx, namedFixture.s, store.ActorHuman, namedFixture.env, "curated")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := JoinBatch(ctx, namedFixture.s, store.ActorHuman, namedFixture.env, named.ID, namedFixture.matter); err != nil {
		t.Fatal(err)
	}
	anon, err := CreateAnonymousBatch(ctx, namedFixture.s, store.ActorHuman, namedFixture.env, namedFixture.matter)
	if err != nil {
		t.Fatal(err)
	}
	runID := namedFixture.s.NewID()
	if _, err := namedFixture.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: namedFixture.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRunStarted, Subject: runID, Payload: store.RunStarted{Batch: anon.ID, Locator: "run-01", Matters: []string{namedFixture.matter}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := namedFixture.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: namedFixture.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRunFinished, Subject: runID, Payload: store.RunFinished{}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := namedFixture.s.Batch(ctx, anon.ID); got.State != "live" {
		t.Fatalf("closing a Run swept its anonymous Batch: %#v", got)
	}
	if _, err := Start(ctx, namedFixture.s, store.ActorHuman, namedFixture.env.Repo, namedFixture.matter); err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, namedFixture.s, store.ActorHuman, namedFixture.env, "named-does-not-sweep"); err != nil {
		t.Fatal(err)
	}
	if got, _ := namedFixture.s.Batch(ctx, named.ID); got.State != "live" {
		t.Fatalf("named Batch auto-swept after its Matter sealed: %#v", got)
	}
}

func lenEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	events, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

func TestAnonymousBatchSweepWaitsForFinalMatterGate(t *testing.T) {
	f := newBatchFixture(t, "gated-sweep")
	ctx := context.Background()
	if err := f.s.DeclareGate(ctx, f.env.Repo, "review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "gated-sweep"); err != nil {
		t.Fatal(err)
	}
	b, err := CreateAnonymousBatch(ctx, f.s, store.ActorHuman, f.env, f.matter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FinishWithEnv(ctx, f.s, store.ActorHuman, f.env, "gated-sweep"); err != nil {
		t.Fatal(err)
	}
	live, _ := f.s.Batch(ctx, b.ID)
	if live.State != "live" {
		t.Fatalf("Batch swept before final gate: %#v", live)
	}
	if _, err := CloseGateWithEnv(ctx, f.s, store.ActorHuman, f.env, "review", "gated-sweep"); err != nil {
		t.Fatal(err)
	}
	closed, _ := f.s.Batch(ctx, b.ID)
	if closed.State != "closed" || closed.CloseReason != store.BatchSwept {
		t.Fatalf("Batch after gate close: %#v", closed)
	}
}
