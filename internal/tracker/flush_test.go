package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

type fakeSeam struct {
	results map[string]Result
	errors  map[string]error
	calls   []store.OutboxEntry
	created map[string]string
}

func (f *fakeSeam) Deliver(_ context.Context, entry store.OutboxEntry) (Result, error) {
	f.calls = append(f.calls, entry)
	if err := f.errors[entry.ID]; err != nil {
		return Result{}, err
	}
	result, ok := f.results[entry.ID]
	if !ok {
		return Result{Outcome: Delivered, Lease: "lease-" + entry.ID}, nil
	}
	if entry.Kind == "create" && result.Outcome == Delivered {
		if f.created == nil {
			f.created = make(map[string]string)
		}
		if ref := f.created[entry.IdempotencyKey]; ref != "" {
			result.Ref = ref
		} else {
			f.created[entry.IdempotencyKey] = result.Ref
		}
	}
	return result, nil
}

type fixture struct {
	t      *testing.T
	ctx    context.Context
	path   string
	s      *store.Store
	repo   string
	actor  store.Actor
	anchor store.Event
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wip.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: context.Background(), path: path, s: s, actor: store.ActorHuman}
	events, err := s.Commit(f.ctx, store.Request{Actor: f.actor}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		f.repo = tx.NewID()
		return []store.Draft{{Type: store.TypeRepoAttached, Subject: f.repo, Env: &store.Env{Repo: f.repo}, Payload: store.RepoAttached{}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.anchor = events[0]
	t.Cleanup(func() { _ = f.s.Close() })
	return f
}

func (f *fixture) createEntry(title string) store.OutboxEntry {
	f.t.Helper()
	var backlog, outbox string
	_, err := f.s.Commit(f.ctx, store.Request{Actor: f.actor, Env: store.Env{Repo: f.repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		backlog = tx.NewID()
		outbox = tx.NewID()
		return []store.Draft{
			{Type: store.TypeBacklogEntered, Subject: backlog, Payload: store.BacklogEntered{Provenance: store.ProvenanceIntake, Title: title}},
			{Type: store.TypeBacklogDelegated, Subject: backlog, Payload: store.BacklogDelegated{Outbox: outbox, IdempotencyKey: "backlog:" + backlog}, Cause: 0},
		}, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	entry, err := writesurface.OutboxApprove(f.ctx, f.s, f.actor, f.repo, outbox)
	if err != nil {
		f.t.Fatal(err)
	}
	return entry
}

func (f *fixture) seedCandidate(kind, ref, payload string) store.OutboxEntry {
	f.t.Helper()
	var subject, id string
	events, err := f.s.Commit(f.ctx, store.Request{Actor: f.actor, Env: store.Env{Repo: f.repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		subject = tx.NewID()
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeBacklogEntered, Subject: subject, Payload: store.BacklogEntered{Provenance: store.ProvenanceFound, Title: "candidate anchor"}}}, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+f.path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`INSERT INTO outbox_entries
		(id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
		VALUES (?,?,?,'approved',?,?,?,?,?,?)`,
		id, f.repo, kind, subject, ref, "candidate:"+id, payload, events[0].ID, events[0].ID)
	if err != nil {
		f.t.Fatal(err)
	}
	entry, err := f.s.OutboxEntry(f.ctx, f.repo, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return entry
}

func (f *fixture) entry(id string) store.OutboxEntry {
	f.t.Helper()
	e, err := f.s.OutboxEntry(f.ctx, f.repo, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return e
}

func TestFlushContinuesAcrossEntriesAndRetriesCreationIdempotently(t *testing.T) {
	f := newFixture(t)
	failed := f.createEntry("Fails once")
	succeeded := f.createEntry("Independent success")
	seam := &fakeSeam{results: map[string]Result{
		failed.ID:    {Outcome: RetryableFailure, Reason: "temporary outage"},
		succeeded.ID: {Outcome: Delivered, Ref: "GH-2"},
	}}

	report, err := Flush(f.ctx, f.s, f.actor, f.repo, seam)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 2 || len(seam.calls) != 2 {
		t.Fatalf("report/calls = %d/%d, want 2/2", len(report.Entries), len(seam.calls))
	}
	if got := f.entry(failed.ID); got.State != "queued" || got.Attempts != 1 || got.Reason != "temporary outage" {
		t.Fatalf("retryable failure = %+v", got)
	}
	if got := f.entry(succeeded.ID); got.State != "flushed" || got.Attempts != 1 {
		t.Fatalf("independent success = %+v", got)
	}

	if _, err := writesurface.OutboxRetry(f.ctx, f.s, f.actor, f.repo, failed.ID); err != nil {
		t.Fatal(err)
	}
	seam.results[failed.ID] = Result{Outcome: Delivered, Ref: "GH-1"}
	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	if got := f.entry(failed.ID); got.State != "flushed" || got.Attempts != 2 || got.IdempotencyKey != failed.IdempotencyKey {
		t.Fatalf("retried success = %+v", got)
	}
	if len(seam.created) != 2 {
		t.Fatalf("fake provider created %d external identities, want 2", len(seam.created))
	}
}

func TestFlushComposesStatesKeepsCommentsAndGuardsEachReference(t *testing.T) {
	f := newFixture(t)
	oldState := f.seedCandidate("state", "GH-10", `{"kind":"state","disposition":"active"}`)
	newState := f.seedCandidate("state", "GH-10", `{"kind":"state","disposition":"completed"}`)
	comment1 := f.seedCandidate("comment", "GH-10", `{"kind":"comment","body":"one"}`)
	comment2 := f.seedCandidate("comment", "GH-10", `{"kind":"comment","body":"two"}`)
	drifted := f.seedCandidate("state", "GH-20", `{"kind":"state","disposition":"active"}`)
	seam := &fakeSeam{results: map[string]Result{
		newState.ID: {Outcome: Delivered, Lease: "rev-10"},
		comment1.ID: {Outcome: Delivered},
		comment2.ID: {Outcome: Delivered},
		drifted.ID:  {Outcome: LeaseMismatch, Reason: "lease changed"},
	}}

	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	if got := f.entry(oldState.ID); got.State != "withheld" || got.Attempts != 0 {
		t.Fatalf("superseded state = %+v", got)
	}
	for _, entry := range []store.OutboxEntry{newState, comment1, comment2} {
		if got := f.entry(entry.ID); got.State != "flushed" || got.Attempts != 1 {
			t.Fatalf("delivered %s = %+v", entry.ID, got)
		}
	}
	if got := f.entry(drifted.ID); got.State != "withheld" || got.Attempts != 1 || got.Reason != "lease changed" {
		t.Fatalf("lease mismatch = %+v", got)
	}
	record, err := f.s.TrackerPushRecord(f.ctx, "GH-10")
	if err != nil || record.Disposition != store.TrackerCompleted || record.Lease != "rev-10" {
		t.Fatalf("push record = %+v (err %v)", record, err)
	}
	if _, found, err := f.s.FindTrackerPushRecord(f.ctx, "GH-20"); err != nil || found {
		t.Fatalf("lease mismatch changed push record: found=%v err=%v", found, err)
	}
	if len(seam.calls) != 4 {
		t.Fatalf("seam calls = %d, want newest state + two comments + independent ref", len(seam.calls))
	}

	regression := f.seedCandidate("state", "GH-10", `{"kind":"state","disposition":"active"}`)
	beforeCalls := len(seam.calls)
	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	if got := f.entry(regression.ID); got.State != "withheld" || got.Attempts != 0 {
		t.Fatalf("local regression = %+v", got)
	}
	if len(seam.calls) != beforeCalls {
		t.Fatalf("local regression reached seam: calls %d -> %d", beforeCalls, len(seam.calls))
	}
}

func TestApprovedWorkResumesAfterRestart(t *testing.T) {
	f := newFixture(t)
	approved := f.createEntry("Crash-safe approval")
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s = reopened
	seam := &fakeSeam{results: map[string]Result{approved.ID: {Outcome: Delivered, Ref: "GH-restarted"}}}
	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	if got := f.entry(approved.ID); got.State != "flushed" || got.IdempotencyKey != approved.IdempotencyKey {
		t.Fatalf("restart result = %+v", got)
	}
}

func TestFlushRecordsReturnedErrorsAsRetryableFailures(t *testing.T) {
	f := newFixture(t)
	entry := f.createEntry("Transport error")
	seam := &fakeSeam{errors: map[string]error{entry.ID: errors.New("connection reset")}}
	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	got := f.entry(entry.ID)
	if got.State != "queued" || got.Attempts != 1 || got.Reason != "connection reset" {
		t.Fatalf("transport error result = %+v", got)
	}
}

func TestFlushRefusesWithoutAConfiguredSeam(t *testing.T) {
	f := newFixture(t)
	entry := f.createEntry("No provider")
	_, err := Flush(f.ctx, f.s, f.actor, f.repo, nil)
	if err == nil || err.Error() != "tracker: no provider seam configured" {
		t.Fatalf("nil seam error = %v", err)
	}
	if got := f.entry(entry.ID); got.State != "approved" || got.Attempts != 0 {
		t.Fatalf("nil seam changed approved work: %+v", got)
	}
}

func TestMalformedProviderSuccessIsPermanentlyVisible(t *testing.T) {
	f := newFixture(t)
	entry := f.createEntry("Bad success")
	seam := &fakeSeam{results: map[string]Result{entry.ID: {Outcome: Delivered}}}
	if _, err := Flush(f.ctx, f.s, f.actor, f.repo, seam); err != nil {
		t.Fatal(err)
	}
	got := f.entry(entry.ID)
	if got.State != "withheld" || got.Attempts != 1 {
		t.Fatalf("malformed success = %+v", got)
	}
}

func ExampleSeam() {
	fmt.Println("provider-neutral")
	// Output: provider-neutral
}
