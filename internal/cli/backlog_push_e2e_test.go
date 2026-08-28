package cli_test

// End-to-end coverage of tracker.backlog-push through `wip backlog add`:
// manual (the default) leaves an entry entered even with a backend; auto with
// a backend delegates in the same command and queues one create that reaches
// the provider only after outbox approve and outbox flush; auto with no
// backend behaves as manual and the verb warns.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

// countingSeam records how many times flush reached the provider. Every
// delivery succeeds with a fake reference so a create lands as delivered.
type countingSeam struct {
	deliveries int
}

func (c *countingSeam) Deliver(context.Context, store.OutboxEntry) (tracker.Result, error) {
	c.deliveries++
	return tracker.Result{Outcome: tracker.Delivered, Ref: "fake#1"}, nil
}

// backlogPushRepo chdirs into a fresh git repo, points WIP_DB_PATH at a fresh
// db, registers the fake backend, and runs init. It returns the db path, the
// registry to pass to runWithProviders, and the seam whose counter proves
// whether anything reached the provider.
func backlogPushRepo(t *testing.T, name string) (string, *tracker.Registry, *countingSeam) {
	t.Helper()
	dir := newGitRepo(t, name)
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	t.Setenv("WIP_DB_PATH", dbPath)

	seam := &countingSeam{}
	providers := tracker.NewRegistry()
	providers.Register("fake", func(tracker.FactoryInput) (tracker.Seam, error) {
		return seam, nil
	})
	if r := runWithProviders(t, providers, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	return dbPath, providers, seam
}

type backlogAddPayload struct {
	ID         string `json:"id"`
	Provenance string `json:"provenance"`
	State      string `json:"state"`
	Outbox     string `json:"outbox"`
}

func outboxRows(t *testing.T, providers *tracker.Registry) []outboxPayload {
	t.Helper()
	listed := runWithProviders(t, providers, "outbox", "list", "--json")
	if listed.exitCode != 0 {
		t.Fatalf("outbox list: exit=%d stderr=%q", listed.exitCode, listed.stderr)
	}
	return mustJSON[struct {
		Entries []outboxPayload `json:"entries"`
	}](t, listed.stdout).Entries
}

func TestBacklogAddUnderManualLeavesTheEntryEnteredEvenWithABackend(t *testing.T) {
	_, providers, seam := backlogPushRepo(t, "backlog-push-manual")

	if r := runWithProviders(t, providers, "outbox", "backend", "fake"); r.exitCode != 0 {
		t.Fatalf("outbox backend fake: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	push := runWithProviders(t, providers, "outbox", "backlog-push")
	if push.exitCode != 0 || push.stdout != "manual\n" || push.stderr != "" {
		t.Fatalf("outbox backlog-push: exit=%d stdout=%q stderr=%q", push.exitCode, push.stdout, push.stderr)
	}

	added := runWithProviders(t, providers, "backlog", "add", "--title", "found thing", "--provenance", "intake", "--json")
	if added.exitCode != 0 {
		t.Fatalf("backlog add: exit=%d stderr=%q", added.exitCode, added.stderr)
	}
	if strings.Contains(added.stdout, `"outbox"`) {
		t.Fatalf("backlog add --json under manual should not carry an outbox key: %q", added.stdout)
	}
	entry := mustJSON[backlogAddPayload](t, added.stdout)
	if entry.State != "entered" {
		t.Fatalf("state = %q, want entered", entry.State)
	}
	if entry.Outbox != "" {
		t.Fatalf("outbox = %q, want empty", entry.Outbox)
	}

	rows := outboxRows(t, providers)
	if len(rows) != 0 {
		t.Fatalf("outbox rows = %d, want 0: %+v", len(rows), rows)
	}
	if seam.deliveries != 0 {
		t.Fatalf("seam.deliveries = %d, want 0", seam.deliveries)
	}
}

func TestBacklogAddUnderAutoWithABackendQueuesOneCreateThatWaitsForApproval(t *testing.T) {
	dbPath, providers, seam := backlogPushRepo(t, "backlog-push-auto")

	if r := runWithProviders(t, providers, "outbox", "backend", "fake"); r.exitCode != 0 {
		t.Fatalf("outbox backend fake: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	push := runWithProviders(t, providers, "outbox", "backlog-push", "auto")
	if push.exitCode != 0 || push.stdout != "auto\n" || push.stderr != "" {
		t.Fatalf("outbox backlog-push auto: exit=%d stdout=%q stderr=%q", push.exitCode, push.stdout, push.stderr)
	}

	added := runWithProviders(t, providers, "backlog", "add", "--title", "Push me", "--provenance", "intake", "--json")
	if added.exitCode != 0 {
		t.Fatalf("backlog add: exit=%d stderr=%q", added.exitCode, added.stderr)
	}
	entry := mustJSON[backlogAddPayload](t, added.stdout)
	if entry.State != "delegated" {
		t.Fatalf("state = %q, want delegated", entry.State)
	}
	if entry.Outbox == "" {
		t.Fatal("outbox is empty, want a queued outbox id")
	}
	if entry.Provenance != "intake" {
		t.Fatalf("provenance = %q, want intake", entry.Provenance)
	}

	rows := outboxRows(t, providers)
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.ID != entry.Outbox {
		t.Fatalf("row id = %q, want %q", row.ID, entry.Outbox)
	}
	if row.Kind != "create" {
		t.Fatalf("row kind = %q, want create", row.Kind)
	}
	if row.State != "queued" {
		t.Fatalf("row state = %q, want queued", row.State)
	}
	if row.Subject != entry.ID {
		t.Fatalf("row subject = %q, want %q", row.Subject, entry.ID)
	}
	if row.IdempotencyKey != "backlog:"+entry.ID {
		t.Fatalf("row idempotencyKey = %q, want %q", row.IdempotencyKey, "backlog:"+entry.ID)
	}
	if row.Attempts != 0 {
		t.Fatalf("row attempts = %d, want 0", row.Attempts)
	}
	if len(row.Payload) == 0 {
		t.Fatal("row payload is empty")
	}

	s := openTestStore(t, dbPath)
	events, err := s.EventsOfSubject(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("events of %s: %v", entry.ID, err)
	}
	if len(events) != 2 {
		t.Fatalf("events of %s = %d, want 2: %+v", entry.ID, len(events), events)
	}
	if events[0].Type != store.TypeBacklogEntered {
		t.Fatalf("events[0].Type = %q, want %q", events[0].Type, store.TypeBacklogEntered)
	}
	if events[1].Type != store.TypeBacklogDelegated {
		t.Fatalf("events[1].Type = %q, want %q", events[1].Type, store.TypeBacklogDelegated)
	}
	if events[1].Causation != events[0].ID {
		t.Fatalf("events[1].Causation = %q, want %q", events[1].Causation, events[0].ID)
	}

	human := runWithProviders(t, providers, "backlog", "add", "--title", "Push me too", "--provenance", "found")
	if human.exitCode != 0 {
		t.Fatalf("backlog add (human): exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	if !strings.HasPrefix(human.stdout, "entered backlog item ") {
		t.Fatalf("human stdout = %q, want prefix %q", human.stdout, "entered backlog item ")
	}
	if !strings.Contains(human.stdout, " (found); delegated — outbox ") {
		t.Fatalf("human stdout = %q, want to contain %q", human.stdout, " (found); delegated — outbox ")
	}
	if !strings.HasSuffix(human.stdout, " queued, awaiting approve\n") {
		t.Fatalf("human stdout = %q, want suffix %q", human.stdout, " queued, awaiting approve\n")
	}

	rows = outboxRows(t, providers)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.Kind != "create" {
			t.Fatalf("row kind = %q, want create: %+v", row.Kind, row)
		}
		if row.State != "queued" {
			t.Fatalf("row state = %q, want queued: %+v", row.State, row)
		}
	}

	if seam.deliveries != 0 {
		t.Fatalf("seam.deliveries = %d, want 0 (nothing approved or flushed yet)", seam.deliveries)
	}

	flushedNone := runWithProviders(t, providers, "outbox", "flush")
	if flushedNone.exitCode != 0 {
		t.Fatalf("outbox flush (nothing approved): exit=%d stderr=%q", flushedNone.exitCode, flushedNone.stderr)
	}
	if flushedNone.stdout != "flushed 0 outbox result(s)\n" {
		t.Fatalf("outbox flush stdout = %q, want %q", flushedNone.stdout, "flushed 0 outbox result(s)\n")
	}
	if seam.deliveries != 0 {
		t.Fatalf("seam.deliveries = %d, want 0 after a flush with nothing approved", seam.deliveries)
	}
	rows = outboxRows(t, providers)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.State != "queued" {
			t.Fatalf("row state = %q, want queued: %+v", row.State, row)
		}
	}

	approved := runWithProviders(t, providers, "outbox", "approve", entry.Outbox, "--json")
	if approved.exitCode != 0 {
		t.Fatalf("outbox approve: exit=%d stderr=%q", approved.exitCode, approved.stderr)
	}
	approvedRow := mustJSON[outboxPayload](t, approved.stdout)
	if approvedRow.State != "approved" {
		t.Fatalf("approved state = %q, want approved", approvedRow.State)
	}

	flushedOne := runWithProviders(t, providers, "outbox", "flush")
	if flushedOne.exitCode != 0 {
		t.Fatalf("outbox flush (one approved): exit=%d stderr=%q", flushedOne.exitCode, flushedOne.stderr)
	}
	if flushedOne.stdout != "flushed 1 outbox result(s)\n" {
		t.Fatalf("outbox flush stdout = %q, want %q", flushedOne.stdout, "flushed 1 outbox result(s)\n")
	}
	if seam.deliveries != 1 {
		t.Fatalf("seam.deliveries = %d, want 1", seam.deliveries)
	}

	rows = outboxRows(t, providers)
	if len(rows) != 2 {
		t.Fatalf("outbox rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.ID == entry.Outbox {
			if row.State == "queued" || row.State == "approved" {
				t.Fatalf("delivered row state = %q, want a terminal state other than queued/approved", row.State)
			}
			continue
		}
		if row.State != "queued" {
			t.Fatalf("other row state = %q, want queued: %+v", row.State, row)
		}
	}
}

func TestBacklogAddUnderAutoWithoutABackendBehavesAsManualAndWarns(t *testing.T) {
	_, providers, seam := backlogPushRepo(t, "backlog-push-no-backend")

	if r := runWithProviders(t, providers, "outbox", "backend", "fake"); r.exitCode != 0 {
		t.Fatalf("outbox backend fake: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	none := runWithProviders(t, providers, "outbox", "backend", "none")
	if none.exitCode != 0 || none.stdout != "none\n" {
		t.Fatalf("outbox backend none: exit=%d stdout=%q stderr=%q", none.exitCode, none.stdout, none.stderr)
	}

	push := runWithProviders(t, providers, "outbox", "backlog-push", "auto")
	if push.exitCode != 0 || push.stdout != "auto\n" {
		t.Fatalf("outbox backlog-push auto: exit=%d stdout=%q stderr=%q", push.exitCode, push.stdout, push.stderr)
	}
	if !strings.Contains(push.stderr, "tracker.backlog-push is auto but no tracker backend is configured") {
		t.Fatalf("outbox backlog-push auto stderr = %q, want the no-backend warning", push.stderr)
	}

	added := runWithProviders(t, providers, "backlog", "add", "--title", "found thing", "--provenance", "intake", "--json")
	if added.exitCode != 0 {
		t.Fatalf("backlog add: exit=%d stderr=%q", added.exitCode, added.stderr)
	}
	if strings.Contains(added.stdout, `"outbox"`) {
		t.Fatalf("backlog add --json under auto with no backend should not carry an outbox key: %q", added.stdout)
	}
	entry := mustJSON[backlogAddPayload](t, added.stdout)
	if entry.State != "entered" {
		t.Fatalf("state = %q, want entered", entry.State)
	}
	if entry.Outbox != "" {
		t.Fatalf("outbox = %q, want empty", entry.Outbox)
	}

	rows := outboxRows(t, providers)
	if len(rows) != 0 {
		t.Fatalf("outbox rows = %d, want 0: %+v", len(rows), rows)
	}
	if seam.deliveries != 0 {
		t.Fatalf("seam.deliveries = %d, want 0", seam.deliveries)
	}

	read := runWithProviders(t, providers, "outbox", "backlog-push")
	if read.exitCode != 0 || read.stdout != "auto\n" || read.stderr != "" {
		t.Fatalf("outbox backlog-push (read): exit=%d stdout=%q stderr=%q", read.exitCode, read.stdout, read.stderr)
	}
}
