package writesurface

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func wantOneQueuedCreate(ctx context.Context, t *testing.T, f batchFixture, entry store.BacklogEntry) {
	t.Helper()
	if entry.State != "delegated" || entry.Outbox == "" {
		t.Fatalf("entry = %+v, want state delegated with an outbox id", entry)
	}
	rows, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox has %d rows, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.ID != entry.Outbox || row.Kind != "create" || row.State != "queued" ||
		row.Subject != entry.ID || row.IdempotencyKey != "backlog:"+entry.ID {
		t.Fatalf("outbox row = %+v, want create/queued for %s with key backlog:%s", row, entry.ID, entry.ID)
	}
}

func TestBacklogAddUnderManualLeavesTheEntryEntered(t *testing.T) {
	f := newBatchFixture(t, "backlog-push")
	ctx := context.Background()

	if err := f.s.SetConfig(ctx, f.env.Repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}

	entry, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "found thing", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != "entered" || entry.Outbox != "" {
		t.Fatalf("entry = %+v, want state entered with no outbox", entry)
	}

	rows, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("outbox has %d rows, want 0: %+v", len(rows), rows)
	}

	events, err := f.s.EventsOfSubject(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != store.TypeBacklogEntered {
		t.Fatalf("events = %+v, want exactly 1 backlog.entered", events)
	}
}

func TestBacklogAddUnderAutoWithABackendDelegatesInTheSameCommit(t *testing.T) {
	f := newBatchFixture(t, "backlog-push")
	ctx := context.Background()

	if err := f.s.SetConfig(ctx, f.env.Repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetTrackerBacklogPush(ctx, f.env.Repo, "auto"); err != nil {
		t.Fatal(err)
	}

	entry, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "push me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantOneQueuedCreate(ctx, t, f, entry)

	events, err := f.s.EventsOfSubject(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want 2", events)
	}
	if events[0].Type != store.TypeBacklogEntered {
		t.Fatalf("events[0].Type = %s, want backlog.entered", events[0].Type)
	}
	if events[1].Type != store.TypeBacklogDelegated {
		t.Fatalf("events[1].Type = %s, want backlog.delegated", events[1].Type)
	}
	if !events[0].IsOrigin() {
		t.Fatalf("events[0] = %+v, want origin (a:a:a)", events[0])
	}
	if events[1].Causation != events[0].ID || events[1].Correlation != events[0].ID {
		t.Fatalf("events[1] = %+v, want Causation and Correlation = %s", events[1], events[0].ID)
	}

	var payload store.BacklogDelegated
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Outbox != entry.Outbox {
		t.Fatalf("payload.Outbox = %s, want %s", payload.Outbox, entry.Outbox)
	}
	if payload.IdempotencyKey != "backlog:"+entry.ID {
		t.Fatalf("payload.IdempotencyKey = %s, want backlog:%s", payload.IdempotencyKey, entry.ID)
	}
}

func TestBacklogAddUnderAutoWithoutABackendBehavesAsManual(t *testing.T) {
	f := newBatchFixture(t, "backlog-push")
	ctx := context.Background()

	if _, err := f.s.SetTrackerBacklogPush(ctx, f.env.Repo, "auto"); err != nil {
		t.Fatal(err)
	}

	entry, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "no backend yet", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != "entered" || entry.Outbox != "" {
		t.Fatalf("entry = %+v, want state entered with no outbox", entry)
	}
	rows, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("outbox has %d rows, want 0: %+v", len(rows), rows)
	}
	events, err := f.s.EventsOfSubject(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != store.TypeBacklogEntered {
		t.Fatalf("events = %+v, want exactly 1 backlog.entered", events)
	}

	if err := f.s.SetConfig(ctx, f.env.Repo, store.TrackerBackendKey, ""); err != nil {
		t.Fatal(err)
	}

	entry2, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "still no backend", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if entry2.State != "entered" || entry2.Outbox != "" {
		t.Fatalf("entry2 = %+v, want state entered with no outbox", entry2)
	}
	rows2, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 0 {
		t.Fatalf("outbox has %d rows, want 0: %+v", len(rows2), rows2)
	}
	events2, err := f.s.EventsOfSubject(ctx, entry2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events2) != 1 || events2[0].Type != store.TypeBacklogEntered {
		t.Fatalf("events2 = %+v, want exactly 1 backlog.entered", events2)
	}
}

func TestBacklogAddUnderAutoStillValidatesBeforeWriting(t *testing.T) {
	f := newBatchFixture(t, "backlog-push")
	ctx := context.Background()

	if err := f.s.SetConfig(ctx, f.env.Repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetTrackerBacklogPush(ctx, f.env.Repo, "auto"); err != nil {
		t.Fatal(err)
	}

	_, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceDeferred, "needs origin", "why", "")
	if err == nil {
		t.Fatal("want error for deferred entry with no origin, got nil")
	}

	rows, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("outbox has %d rows, want 0: %+v", len(rows), rows)
	}
	entries, err := f.s.Backlog(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backlog has %d entries, want 0: %+v", len(entries), entries)
	}
}

func TestBacklogAddUnderAutoKeepsManualDelegateRefusingASecondTime(t *testing.T) {
	f := newBatchFixture(t, "backlog-push")
	ctx := context.Background()

	if err := f.s.SetConfig(ctx, f.env.Repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetTrackerBacklogPush(ctx, f.env.Repo, "auto"); err != nil {
		t.Fatal(err)
	}

	entry, err := BacklogAdd(ctx, f.s, store.ActorHuman, f.env.Repo, store.ProvenanceIntake, "already delegated", "", "")
	if err != nil {
		t.Fatal(err)
	}
	wantOneQueuedCreate(ctx, t, f, entry)

	if _, err := BacklogDelegate(ctx, f.s, store.ActorHuman, f.env.Repo, entry.ID); err == nil {
		t.Fatal("want error delegating an already-delegated entry, got nil")
	}

	rows, err := f.s.Outbox(ctx, f.env.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox has %d rows, want 1: %+v", len(rows), rows)
	}
}
