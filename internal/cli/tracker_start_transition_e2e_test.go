package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

// startTransitionSeam models the provider's guarded delivery unit. Every seam
// call reads current state. Only a Delivered result represents an external
// state write; Converged represents a guard read that found forge-ahead state.
type startTransitionSeam struct {
	result     tracker.Result
	deliveries []store.OutboxEntry
	reads      int
	writes     int
}

func (s *startTransitionSeam) Deliver(_ context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	s.deliveries = append(s.deliveries, entry)
	s.reads++
	if s.result.Outcome == tracker.Delivered {
		s.writes++
	}
	return s.result, nil
}

func setupStartTransitionCLI(t *testing.T, seam *startTransitionSeam) (*tracker.Registry, string) {
	t.Helper()
	dir := newGitRepo(t, "tracker-start-transition")
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
	providers := tracker.NewRegistry()
	providers.Register("guarded", func(tracker.FactoryInput) (tracker.Seam, error) { return seam, nil })
	mustRunStartTransition(t, providers, "init", "--json")
	return providers, dbPath
}

func mustRunStartTransition(t *testing.T, providers *tracker.Registry, args ...string) result {
	t.Helper()
	r := runWithProviders(t, providers, args...)
	if r.exitCode != 0 {
		t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
	}
	return r
}

func startTransitionOutbox(t *testing.T, providers *tracker.Registry) []outboxPayload {
	t.Helper()
	r := mustRunStartTransition(t, providers, "outbox", "list", "--json")
	var payload struct {
		Entries []outboxPayload `json:"entries"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("outbox JSON = %q: %v", r.stdout, err)
	}
	return payload.Entries
}

func wantActiveStartEntry(t *testing.T, entries []outboxPayload, matterID, ref string) outboxPayload {
	t.Helper()
	if len(entries) != 1 {
		t.Fatalf("start outbox entries = %+v, want exactly one", entries)
	}
	entry := entries[0]
	var payload struct {
		Disposition store.TrackerDisposition `json:"disposition"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("state candidate payload = %s: %v", entry.Payload, err)
	}
	if entry.Kind != "state" || entry.State != "queued" || entry.Subject != matterID || entry.Reference != ref || payload.Disposition != store.TrackerActive {
		t.Fatalf("start candidate = %+v payload=%s, want queued active state for %s on %s", entry, entry.Payload, matterID, ref)
	}
	return entry
}

func TestMatterStartTransitionDeliversBehindTrackerEndToEnd(t *testing.T) {
	const ref = "T-behind"
	seam := &startTransitionSeam{result: tracker.Result{Outcome: tracker.Delivered, Lease: "lease-after-start"}}
	providers, dbPath := setupStartTransitionCLI(t, seam)

	mustRunStartTransition(t, providers, "outbox", "backend", "guarded")
	if r := mustRunStartTransition(t, providers, "outbox", "level"); r.stdout != "boundary\n" {
		t.Fatalf("configured backend level = %q, want boundary", r.stdout)
	}
	matter := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
		"matter", "create", "--title", "Deliver start", "--locator", "deliver-start", "--json").stdout)
	mustRunStartTransition(t, providers, "bind", matter.Locator, ref, "--json")
	if entries := startTransitionOutbox(t, providers); len(entries) != 0 {
		t.Fatalf("Planned bind queued outbox entries: %+v", entries)
	}

	mustRunStartTransition(t, providers, "start", matter.Locator, "--json")
	entry := wantActiveStartEntry(t, startTransitionOutbox(t, providers), matter.ID, ref)
	mustRunStartTransition(t, providers, "outbox", "approve", entry.ID, "--json")
	flush := mustJSON[tracker.Report](t, mustRunStartTransition(t, providers, "outbox", "flush", "--json").stdout)
	if len(flush.Entries) != 1 || flush.Entries[0].ID != entry.ID || flush.Entries[0].State != "flushed" {
		t.Fatalf("flush report = %+v, want one delivered entry", flush)
	}
	if seam.reads != 1 || seam.writes != 1 || len(seam.deliveries) != 1 || seam.deliveries[0].ID != entry.ID {
		t.Fatalf("guarded seam reads/writes/deliveries = %d/%d/%+v, want 1/1/%s", seam.reads, seam.writes, seam.deliveries, entry.ID)
	}

	s := openTestStore(t, dbPath)
	durable, err := s.OutboxEntry(context.Background(), seam.deliveries[0].Repo, entry.ID)
	if err != nil || durable.State != "flushed" || durable.Attempts != 1 {
		t.Fatalf("durable delivered entry = %+v, err=%v", durable, err)
	}
	events, err := s.EventsOfSubject(context.Background(), entry.ID)
	if err != nil || len(events) != 2 || events[0].Type != store.TypeOutboxApproved || events[1].Type != store.TypeTrackerStatePushed {
		t.Fatalf("delivered entry events = %+v, err=%v", events, err)
	}
	var pushed store.TrackerStatePushed
	if err := json.Unmarshal(events[1].Payload, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.Ref != ref || pushed.Disposition != store.TrackerActive || pushed.Lease != "lease-after-start" {
		t.Fatalf("tracker.state-pushed payload = %+v", pushed)
	}
	record, err := s.TrackerPushRecord(context.Background(), ref)
	if err != nil || record.Disposition != store.TrackerActive || record.Lease != "lease-after-start" || record.Outbox != entry.ID {
		t.Fatalf("active push record = %+v, err=%v", record, err)
	}
}

func TestMatterStartTransitionConvergesWhenForgeIsAhead(t *testing.T) {
	const ref = "T-forge-ahead"
	seam := &startTransitionSeam{result: tracker.Result{Outcome: tracker.Converged, Lease: "lease-observed-ahead"}}
	providers, dbPath := setupStartTransitionCLI(t, seam)

	mustRunStartTransition(t, providers, "outbox", "backend", "guarded")
	matter := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
		"matter", "create", "--title", "Forge ahead", "--locator", "forge-ahead", "--json").stdout)
	mustRunStartTransition(t, providers, "bind", matter.Locator, ref, "--json")
	if entries := startTransitionOutbox(t, providers); len(entries) != 0 {
		t.Fatalf("Planned bind queued outbox entries: %+v", entries)
	}
	mustRunStartTransition(t, providers, "start", matter.Locator, "--json")
	entry := wantActiveStartEntry(t, startTransitionOutbox(t, providers), matter.ID, ref)
	mustRunStartTransition(t, providers, "outbox", "approve", entry.ID, "--json")
	flush := mustJSON[tracker.Report](t, mustRunStartTransition(t, providers, "outbox", "flush", "--json").stdout)
	if len(flush.Entries) != 1 || flush.Entries[0].ID != entry.ID || flush.Entries[0].State != "converged" {
		t.Fatalf("flush report = %+v, want one converged entry", flush)
	}
	if seam.reads != 1 || seam.writes != 0 || len(seam.deliveries) != 1 || seam.deliveries[0].ID != entry.ID {
		t.Fatalf("forge-ahead seam reads/writes/deliveries = %d/%d/%+v, want 1/0/%s", seam.reads, seam.writes, seam.deliveries, entry.ID)
	}

	s := openTestStore(t, dbPath)
	durable, err := s.OutboxEntry(context.Background(), seam.deliveries[0].Repo, entry.ID)
	if err != nil || durable.State != "flushed" || durable.Attempts != 1 {
		t.Fatalf("durable converged entry = %+v, err=%v", durable, err)
	}
	events, err := s.EventsOfSubject(context.Background(), entry.ID)
	if err != nil || len(events) != 2 || events[0].Type != store.TypeOutboxApproved || events[1].Type != store.TypeTrackerStateObserved {
		t.Fatalf("converged entry events = %+v, err=%v", events, err)
	}
	var observed store.TrackerStateObserved
	if err := json.Unmarshal(events[1].Payload, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Ref != ref || observed.Disposition != store.TrackerActive || observed.Lease != "lease-observed-ahead" {
		t.Fatalf("tracker.state-observed payload = %+v", observed)
	}
	if record, found, err := s.FindTrackerPushRecord(context.Background(), ref); err != nil || found {
		t.Fatalf("forge convergence push record = %+v, found=%t, err=%v", record, found, err)
	}
}

func TestMatterStartTransitionRespectsInertPushLevels(t *testing.T) {
	t.Run("no backend defaults off", func(t *testing.T) {
		seam := &startTransitionSeam{}
		providers, _ := setupStartTransitionCLI(t, seam)
		if r := mustRunStartTransition(t, providers, "outbox", "level"); r.stdout != "off\n" {
			t.Fatalf("unset backend level = %q, want off", r.stdout)
		}
		matter := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
			"matter", "create", "--title", "No backend", "--locator", "no-backend-start", "--json").stdout)
		mustRunStartTransition(t, providers, "bind", matter.Locator, "T-no-backend", "--json")
		mustRunStartTransition(t, providers, "start", matter.Locator, "--json")
		if entries := startTransitionOutbox(t, providers); len(entries) != 0 {
			t.Fatalf("no-backend bind/start queued entries: %+v", entries)
		}
		if seam.reads != 0 || seam.writes != 0 || len(seam.deliveries) != 0 {
			t.Fatalf("no-backend seam was called: %+v", seam)
		}
	})

	t.Run("explicit off overrides backend", func(t *testing.T) {
		seam := &startTransitionSeam{}
		providers, _ := setupStartTransitionCLI(t, seam)
		mustRunStartTransition(t, providers, "outbox", "backend", "guarded")
		mustRunStartTransition(t, providers, "outbox", "level", "off")
		matter := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
			"matter", "create", "--title", "Explicit off", "--locator", "explicit-off-start", "--json").stdout)
		mustRunStartTransition(t, providers, "bind", matter.Locator, "T-explicit-off", "--json")
		mustRunStartTransition(t, providers, "start", matter.Locator, "--json")
		if entries := startTransitionOutbox(t, providers); len(entries) != 0 {
			t.Fatalf("explicit-off bind/start queued entries: %+v", entries)
		}
		if seam.reads != 0 || seam.writes != 0 || len(seam.deliveries) != 0 {
			t.Fatalf("explicit-off seam was called: %+v", seam)
		}
	})
}

func TestMatterStartTransitionQueuesOnDescendantCascade(t *testing.T) {
	seam := &startTransitionSeam{}
	providers, _ := setupStartTransitionCLI(t, seam)
	mustRunStartTransition(t, providers, "outbox", "backend", "guarded")
	matter := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
		"matter", "create", "--title", "Cascade start", "--locator", "cascade-start", "--json").stdout)
	mustRunStartTransition(t, providers, "bind", matter.Locator, "T-cascade", "--json")
	step := mustJSON[nodePayload](t, mustRunStartTransition(t, providers,
		"step", "create", matter.Locator, "--title", "Begin work", "--json").stdout)

	mustRunStartTransition(t, providers, "start", step.ID, "--json")
	wantActiveStartEntry(t, startTransitionOutbox(t, providers), matter.ID, "T-cascade")
}
