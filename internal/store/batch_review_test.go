package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBatchMembershipRejectsClosedInvalidAndDuplicateLeave(t *testing.T) {
	h := newHarness(t)
	batch := h.newBatch("membership-review")
	matter := h.matter("membership-matter", "Membership Matter")
	stage := h.stage(matter, "membership-stage", "Membership Stage")
	gone := h.matter("membership-gone", "Removed Matter")
	h.remove(gone, "not a live membership subject")

	h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: matter}})
	h.commit(Draft{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: matter}})
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: matter}}}, nil
	}); err == nil {
		t.Fatal("duplicate leave succeeded")
	}
	h.commit(Draft{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: matter}})
	if members, err := h.BatchMembers(h.ctx, batch); err != nil || len(members) != 1 || members[0] != matter {
		t.Fatalf("rejoined membership = %v, err=%v", members, err)
	}

	beforeEvents := len(h.rowsOf("events", ""))
	for name, payload := range map[string]BatchMembership{
		"unknown":    {Matter: h.NewID()},
		"stage":      {Matter: stage},
		"tombstoned": {Matter: gone},
	} {
		t.Run(name, func(t *testing.T) {
			err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
				return []Draft{{Type: TypeBatchJoined, Subject: batch, Payload: payload}}, nil
			})
			if err == nil {
				t.Fatal("invalid membership succeeded")
			}
		})
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("invalid membership changed event count from %d to %d", beforeEvents, got)
	}

	h.commit(Draft{Type: TypeBatchDismissed, Subject: batch, Payload: BatchDismissedPayload{}})
	beforeEvents = len(h.rowsOf("events", ""))
	before := h.snapshotProjection()
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchJoined, Subject: batch, Payload: BatchMembership{Matter: matter}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "closed Batch") {
		t.Fatalf("join to closed Batch error = %v", err)
	}
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchLeft, Subject: batch, Payload: BatchMembership{Matter: matter}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "closed Batch") {
		t.Fatalf("leave from closed Batch error = %v", err)
	}
	if got := len(h.rowsOf("events", "")); got != beforeEvents {
		t.Fatalf("closed Batch failures changed event count from %d to %d", beforeEvents, got)
	}
	h.wantSameProjection("closed Batch failures", before, h.snapshotProjection())
}

func TestBatchEventPayloadsAreStrictAndKindSafe(t *testing.T) {
	h := newHarness(t)
	named := h.newBatch("strict-dismiss")
	anonymousMatter := h.matter("strict-sweep-matter", "Strict sweep Matter")
	anonymous := h.newAnonymousBatch(anonymousMatter)

	badDismissals := []string{"[]", "{} {}", `{"extra":true}`}
	for _, payload := range badDismissals {
		err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeBatchDismissed, Subject: named, Payload: json.RawMessage(payload)}}, nil
		})
		if err == nil {
			t.Errorf("dismissal payload %q succeeded", payload)
		}
	}
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchDismissed, Subject: anonymous, Payload: BatchDismissedPayload{}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "named Batch") {
		t.Errorf("dismissal of anonymous Batch = %v", err)
	}

	badSweeps := []string{"[]", "{} {}", `{"extra":true}`}
	for _, payload := range badSweeps {
		err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeBatchSwept, Subject: anonymous, Payload: json.RawMessage(payload)}}, nil
		})
		if err == nil {
			t.Errorf("sweep payload %q succeeded", payload)
		}
	}
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchSwept, Subject: named, Payload: BatchSweptPayload{}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "anonymous Batch") {
		t.Errorf("sweep of named Batch = %v", err)
	}

	h.commit(Draft{Type: TypeBatchDismissed, Subject: named, Payload: BatchDismissedPayload{}})
	if err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBatchDismissed, Subject: named, Payload: BatchDismissedPayload{}}}, nil
	}); err == nil || !strings.Contains(err.Error(), "closed Batch") {
		t.Errorf("second dismissal = %v", err)
	}
}

func TestBatchSweepRebuildAndReopenPreserveTerminalExecution(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("rebuild-sweep", "Rebuild sweep Matter")
	named := h.newBatch("persistent-named")
	h.commit(Draft{Type: TypeBatchJoined, Subject: named, Payload: BatchMembership{Matter: matter}})
	anonymous := h.newAnonymousBatch(matter)

	openRun := startRunForTest(h, anonymous, "run-01", matter)
	openDispatch := h.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: openRun, Matter: matter}}, nil
	})
	closedRun := startRunForTest(h, anonymous, "run-02", matter)
	h.commit(Draft{Type: TypeRunFinished, Subject: closedRun, Payload: RunFinished{}})
	h.commit(Draft{Type: TypeBatchDismissed, Subject: named, Payload: BatchDismissedPayload{}})

	beforeEvents := len(h.rowsOf("events", ""))
	sweep := h.commit(Draft{Type: TypeBatchSwept, Subject: anonymous, Payload: BatchSweptPayload{}})[0]
	if got := len(h.rowsOf("events", "")); got != beforeEvents+1 {
		t.Fatalf("sweep emitted %d events, want one", got-beforeEvents)
	}
	before := h.snapshotProjection()
	sweptBatch, err := h.Batch(h.ctx, anonymous)
	if err != nil || sweptBatch.State != "closed" || sweptBatch.CloseReason != BatchSwept || sweptBatch.LastEvent != sweep.ID || sweptBatch.ClosedAt == nil {
		t.Fatalf("swept Batch = %#v, err=%v", sweptBatch, err)
	}
	for _, id := range []string{openRun, openDispatch} {
		if id == openRun {
			run, err := h.Run(h.ctx, id)
			if err != nil || run.Open || run.CloseReason != CloseReaped || run.LastEvent != sweep.ID || run.ClosedAt == nil {
				t.Fatalf("swept open Run = %#v, err=%v", run, err)
			}
		} else {
			dispatch, err := h.Dispatch(h.ctx, id)
			if err != nil || dispatch.Open || dispatch.CloseReason != CloseReaped || dispatch.LastEvent != sweep.ID {
				t.Fatalf("swept open Dispatch = %#v, err=%v", dispatch, err)
			}
		}
	}
	terminal, err := h.Run(h.ctx, closedRun)
	if err != nil || terminal.CloseReason != CloseCompleted {
		t.Fatalf("already closed Run changed during sweep = %#v, err=%v", terminal, err)
	}
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.wantSameProjection("sweep rebuild", before, h.snapshotProjection())

	reopened, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened.wantSameProjection("sweep reopen", before, reopened.snapshotProjection())
	if members, err := reopened.BatchMembers(reopened.ctx, anonymous); err != nil || len(members) != 1 || members[0] != matter {
		t.Fatalf("anonymous membership after reopen = %v, err=%v", members, err)
	}
	if _, err := reopened.Dispatch(reopened.ctx, openDispatch); err != nil {
		t.Fatal(err)
	}
	// The sweep released the host-wide Matter claim. A later Run in a live Batch can claim it.
	laterBatch := reopened.newBatch("post-sweep")
	later := startRunForTest(reopened, laterBatch, "run-01", matter)
	claim := reopened.commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeDispatchOpened, Subject: tx.NewID(), Payload: DispatchOpened{Run: later, Matter: matter}}, nil
	})
	if dispatch, err := reopened.Dispatch(reopened.ctx, claim); err != nil || !dispatch.Open {
		t.Fatalf("released claim after reopen = %#v, err=%v", dispatch, err)
	}
}
