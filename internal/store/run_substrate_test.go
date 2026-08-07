package store

import (
	"context"
	"strings"
	"testing"
)

func TestRunLifecycleAndMatterDispatchClaim(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("run-matter", "Run substrate")
	batch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "run-batch"}}, nil
	})
	run := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{Batch: batch, Locator: "run-01", Matters: []string{matter}}}, nil
	})
	got, err := h.Run(h.ctx, run)
	if err != nil || !got.Open || got.Locator != "run-01" || got.Clone != h.Clone {
		t.Fatalf("Run = %#v, err=%v", got, err)
	}
	members, err := h.RunMatters(h.ctx, run)
	if err != nil || len(members) != 1 || members[0] != matter {
		t.Fatalf("frozen Matters = %v, err=%v", members, err)
	}

	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	read, err := h.Dispatch(h.ctx, dispatch)
	if err != nil || read.Run != run || read.Matter != matter {
		t.Fatalf("P2 Dispatch = %#v, err=%v", read, err)
	}
	other := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}}, nil
	})
	if other == nil || !strings.Contains(other.Error(), "refusal.dispatch-contention") {
		t.Fatalf("contention error = %v", other)
	}
	h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: CloseCompleted}})
	h.commit(Draft{Type: TypeRunFinished, Subject: run, Payload: RunFinished{}})
	got, err = h.Run(h.ctx, run)
	if err != nil || got.Open || got.CloseReason != CloseCompleted {
		t.Fatalf("closed Run = %#v, err=%v", got, err)
	}
}

func startRunForTest(h *harness, batch, locator string, matters ...string) string {
	h.t.Helper()
	return h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{
			Batch: batch, Locator: locator, Matters: matters,
		}}, nil
	})
}

func TestRunStartRefusesClosedBatchWithoutWriting(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("closed-batch-matter", "Closed Batch matter")
	named := h.newBatch("dismissed")
	h.commit(Draft{Type: TypeBatchDismissed, Subject: named, Payload: BatchDismissedPayload{}})

	beforeEvents := len(h.rowsOf("events", ""))
	beforeProjection := h.snapshotProjection()
	err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{
			Batch: named, Locator: "run-01", Matters: []string{matter},
		}}}, nil
	})
	if !strings.Contains(err.Error(), "closed Batch") {
		t.Fatalf("dismissed named Batch refusal = %v, want closed Batch", err)
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("dismissed Batch refusal appended an event: before=%d after=%d", beforeEvents, got)
	}
	h.wantSameProjection("dismissed Batch refusal", beforeProjection, h.snapshotProjection())

	anonymousMatter := h.matter("swept-batch-matter", "Swept Batch matter")
	anonymous := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Matter: anonymousMatter}}, nil
	})
	h.commit(Draft{Type: TypeBatchSwept, Subject: anonymous, Payload: BatchSweptPayload{}})
	beforeEvents = len(h.rowsOf("events", ""))
	beforeProjection = h.snapshotProjection()
	err = h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{
			Batch: anonymous, Locator: "run-01", Matters: []string{anonymousMatter},
		}}}, nil
	})
	if !strings.Contains(err.Error(), "closed Batch") {
		t.Fatalf("swept anonymous Batch refusal = %v, want closed Batch", err)
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("swept Batch refusal appended an event: before=%d after=%d", beforeEvents, got)
	}
	h.wantSameProjection("swept Batch refusal", beforeProjection, h.snapshotProjection())
}

func TestRunStartValidatesFrozenSetLocatorAndTaxonomy(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("one", "One")
	otherMatter := h.matter("two", "Two")
	stage := h.stage(matter, "stage-01", "A stage")
	tombstoned := h.matter("gone", "Gone")
	h.remove(tombstoned, "removed for the frozen-set negative case")
	batch := h.newBatch("frozen")

	run := startRunForTest(h, batch, "run-01", otherMatter, matter)
	started := h.birthEventOf(run)
	got, err := h.Run(h.ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != run || got.Clone != h.Clone || got.Batch != batch || got.Locator != "run-01" ||
		got.State != "open" || !got.Open || got.CloseReason != "" || got.ClosedAt != nil ||
		got.StartedAt.UTC() != started.OccurredAt.UTC() || got.BirthEvent != started.ID || got.LastEvent != started.ID {
		t.Fatalf("Run = %#v, started event = %#v", got, started)
	}
	members, err := h.RunMatters(h.ctx, run)
	if err != nil || len(members) != 2 || members[0] != otherMatter || members[1] != matter {
		t.Fatalf("frozen Matters = %v, err=%v", members, err)
	}
	rawRefusedBy(h, "deleting a frozen-set row outside rebuild", "only a rebuild may clear it", `DELETE FROM run_matters`)

	if got := len(h.rowsOf("event_types", "family = 'run'")); got != len(V2Taxonomy) {
		t.Fatalf("run taxonomy rows = %d, want %d", got, len(V2Taxonomy))
	}
	for _, typ := range V2Taxonomy {
		rows := h.rowsOf("event_types", "type = ? AND family = 'run' AND requires_repo = 1 AND requires_clone = 1 AND requires_worktree = 1", typ.Type)
		if len(rows) != 1 {
			t.Errorf("taxonomy row for %s is missing its run family or dimensions", typ.Type)
		}
	}

	for _, locator := range []string{"run-00", "run-1", "run-001", "run-+2", "run--2", "run-x"} {
		err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{Batch: batch, Locator: locator, Matters: []string{matter}}}}, nil
		})
		if !strings.Contains(err.Error(), "malformed Run locator") {
			t.Errorf("locator %q refusal = %v", locator, err)
		}
	}
	if err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunStarted, Subject: tx.NewID(), Payload: RunStarted{Batch: batch, Locator: "run-01", Matters: []string{matter}}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("same-batch locator refusal = %v", err)
	}
	batchTwo := h.newBatch("other-batch")
	_ = startRunForTest(h, batchTwo, "run-01", matter)

	for name, payload := range map[string]RunStarted{
		"empty set":  {Batch: batch, Locator: "run-03"},
		"duplicate":  {Batch: batch, Locator: "run-03", Matters: []string{matter, matter}},
		"unknown":    {Batch: batch, Locator: "run-03", Matters: []string{h.NewID()}},
		"stage":      {Batch: batch, Locator: "run-03", Matters: []string{stage}},
		"tombstoned": {Batch: batch, Locator: "run-03", Matters: []string{tombstoned}},
	} {
		t.Run(name, func(t *testing.T) {
			err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
				return []Draft{{Type: TypeRunStarted, Subject: tx.NewID(), Payload: payload}}, nil
			})
			if err == nil {
				t.Fatal("accepted invalid frozen set")
			}
		})
	}
}

func TestRunTransitionsValidateOwnershipAndStandDownAcrossClones(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("transition", "Transition")
	batch := h.newBatch("transitions")
	secondClone := h.attachClone(CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/second/.git", Label: "second"})
	secondWorktree := h.attachWorktree(h.Repo, WorktreeAttached{Clone: secondClone, Name: "second"})
	acting := h.with(Env{Repo: h.Repo, Clone: secondClone, Worktree: secondWorktree})

	stoodDown := startRunForTest(h, batch, "run-01", matter)
	standEvent := acting.commit(Draft{Type: TypeRunStoodDown, Subject: stoodDown, Payload: RunStoodDown{
		ActingClone: secondClone, OwningClone: h.Clone,
	}})[0]
	closed, err := h.Run(h.ctx, stoodDown)
	if err != nil || closed.Open || closed.CloseReason != CloseReason("stood-down") || closed.ClosedAt == nil || closed.LastEvent != standEvent.ID {
		t.Fatalf("stood-down Run = %#v, err=%v", closed, err)
	}

	finished := startRunForTest(h, batch, "run-02", matter)
	finishedEvent := h.commit(Draft{Type: TypeRunFinished, Subject: finished, Payload: RunFinished{}})[0]
	finishedRun, err := h.Run(h.ctx, finished)
	if err != nil || finishedRun.Open || finishedRun.CloseReason != CloseCompleted || finishedRun.LastEvent != finishedEvent.ID {
		t.Fatalf("finished Run = %#v, err=%v", finishedRun, err)
	}

	resumed := startRunForTest(h, batch, "run-03", matter)
	before, err := h.Run(h.ctx, resumed)
	if err != nil {
		t.Fatal(err)
	}
	resumedEvent := h.commit(Draft{Type: TypeRunResumed, Subject: resumed, Payload: RunResumed{}})[0]
	after, err := h.Run(h.ctx, resumed)
	if err != nil || after.LastEvent != before.LastEvent || resumedEvent.Type != TypeRunResumed {
		t.Fatalf("resumed no-op changed Run: before=%#v after=%#v event=%#v", before, after, resumedEvent)
	}

	skipped := h.commit(Draft{Type: TypeRunSkipped, Subject: matter, Payload: RunSkipped{Run: resumed, Reason: RunSkipBlocked}})[0]
	if skipped.Subject != matter {
		t.Fatalf("run.skipped subject = %s, want Matter %s", skipped.Subject, matter)
	}
	if err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunSkipped, Subject: matter, Payload: RunSkipped{Run: resumed, Reason: RunSkipReason("not-a-reason")}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid Run or reason") {
		t.Errorf("invalid run.skipped reason = %v", err)
	}
	if err := acting.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunResumed, Subject: resumed, Payload: RunResumed{}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Errorf("resumed from a non-owning Clone = %v", err)
	}
	if err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeRunStoodDown, Subject: resumed, Payload: RunStoodDown{}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid acting") {
		t.Errorf("empty stand-down payload = %v", err)
	}
}

func TestP2DispatchClaimReleaseAndCrossCloneContention(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("claimed", "Claimed")
	otherMatter := h.matter("free", "Free")
	batch := h.newBatch("claim-one")
	run := startRunForTest(h, batch, "run-01", matter, otherMatter)
	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	read, err := h.Dispatch(h.ctx, dispatch)
	if err != nil || read.Run != run || read.Matter != matter || !read.Open {
		t.Fatalf("P2 Dispatch = %#v, err=%v", read, err)
	}
	outside := h.matter("outside", "Outside frozen set")
	if err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: outside}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "outside the Run frozen set") {
		t.Errorf("Matter outside frozen set = %v", err)
	}
	closedRun := startRunForTest(h, batch, "run-02", matter)
	h.commit(Draft{Type: TypeRunFinished, Subject: closedRun, Payload: RunFinished{}})
	if err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: closedRun, Matter: matter}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "closed Run") {
		t.Errorf("Dispatch on closed Run = %v", err)
	}
	beforeEvents := len(h.rowsOf("events", ""))
	for _, payload := range []DispatchOpened{{Run: run}, {Matter: matter}} {
		err := h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: payload}}, nil
		})
		if !strings.Contains(err.Error(), "both Run and Matter") {
			t.Errorf("partial P2 payload refusal = %v", err)
		}
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("negative Dispatch writes changed event count from %d to %d", beforeEvents, got)
	}

	secondClone := h.attachClone(CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/claim-second/.git", Label: "second"})
	secondWorktree := h.attachWorktree(h.Repo, WorktreeAttached{Clone: secondClone, Name: "claim-second"})
	other := h.with(Env{Repo: h.Repo, Clone: secondClone, Worktree: secondWorktree})
	otherBatch := other.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeBatchCreated, Subject: tx.NewID(), Payload: BatchCreated{Name: "claim-two"}}, nil
	})
	otherRun := startRunForTest(other, otherBatch, "run-01", matter, otherMatter)
	beforeEvents = len(h.rowsOf("events", ""))
	contention := other.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: otherRun, Matter: matter}}}, nil
	})
	if !strings.Contains(contention.Error(), "refusal.dispatch-contention") {
		t.Fatalf("cross-Clone contention = %v", contention)
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("contention emitted an event: before=%d after=%d", beforeEvents, got)
	}

	freeDispatch := other.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: otherRun, Matter: otherMatter}}, nil
	})
	if got, err := other.Dispatch(other.ctx, freeDispatch); err != nil || got.Matter != otherMatter {
		t.Fatalf("different Matter Dispatch = %#v, err=%v", got, err)
	}
	h.commit(Draft{Type: TypeDispatchClosed, Subject: dispatch, Payload: DispatchClosed{Reason: CloseReaped}})
	newDispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	if got, err := h.Dispatch(h.ctx, newDispatch); err != nil || !got.Open {
		t.Fatalf("released Matter claim = %#v, err=%v", got, err)
	}
}

func TestRunProjectionRebuildAndRestartPreserveOpenClaim(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("restart", "Restart")
	batch := h.newBatch("restart")
	run := startRunForTest(h, batch, "run-01", matter)
	dispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: run, Matter: matter}}, nil
	})
	before := h.snapshotProjection()
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.wantSameProjection("run projection after rebuild", before, h.snapshotProjection())

	reopened, err := h.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Run(reopened.ctx, run)
	if err != nil || !got.Open {
		t.Fatalf("open Run after restart = %#v, err=%v", got, err)
	}
	claimed, err := reopened.Dispatch(reopened.ctx, dispatch)
	if err != nil || !claimed.Open || claimed.Matter != matter {
		t.Fatalf("open claim after restart = %#v, err=%v", claimed, err)
	}
}
