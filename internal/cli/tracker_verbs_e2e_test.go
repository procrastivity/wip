// End-to-end tests for the `wip plumbing tracker` verb group (tracker-plumbing
// step-13). They prove the wiring, not provider behaviour: the three provider
// packages carry their own hermetic coverage, so every seam here is a fake
// registered through the injected registry (runWithProviders, from
// alignment_e2e_test.go) and nothing in this file opens a socket.
//
// Two harnesses appear, both house patterns. Tests that need a fake provider
// run the CLI in-process through runWithProviders, which means chdir and
// WIP_DB_PATH (outbox_e2e_test.go's own target/project/canceled-label tests do
// exactly this). Tests that need the production registry — the real github
// factory refusing to construct, the manifest walk — run the built binary
// through runIn/setupRepo.
package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

// trackerVerbSeam is the full-capability fake: it delivers, reads state and
// reads content. Deliver records the entry it was handed verbatim, which is
// what the round-trip assertions read — the provider-facing payload, the
// reference and the stable idempotency key all reach the seam or they do not.
type trackerVerbSeam struct {
	results   map[string]tracker.Result
	content   map[string]tracker.Content
	delivered []store.OutboxEntry
	reads     []string
}

func (f *trackerVerbSeam) Deliver(_ context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	f.delivered = append(f.delivered, entry)
	return f.results[entry.Kind], nil
}

func (f *trackerVerbSeam) ReadContent(_ context.Context, ref string) (tracker.Content, error) {
	f.reads = append(f.reads, ref)
	content, ok := f.content[ref]
	if !ok {
		return tracker.Content{}, fmt.Errorf("fake tracker: no item %s", ref)
	}
	return content, nil
}

func (f *trackerVerbSeam) ReadState(_ context.Context, ref string) (tracker.LiveState, error) {
	f.reads = append(f.reads, ref)
	return f.content[ref].State, nil
}

// trackerVerbDeliverOnlySeam implements Seam and nothing else: the backend that
// can write but cannot read, which `tracker read` reports as an absent
// capability rather than a failure.
type trackerVerbDeliverOnlySeam struct{}

func (trackerVerbDeliverOnlySeam) Deliver(context.Context, store.OutboxEntry) (tracker.Result, error) {
	return tracker.Result{}, nil
}

// trackerVerbRegistry registers seam under the name "fake". Each
// runWithProviders call takes its own registry, so one test can point the same
// stored `tracker.backend` at differently-capable seams in turn.
func trackerVerbRegistry(seam tracker.Seam) *tracker.Registry {
	providers := tracker.NewRegistry()
	providers.Register("fake", func(tracker.FactoryInput) (tracker.Seam, error) { return seam, nil })
	return providers
}

// trackerVerbFactoryRegistry is trackerVerbRegistry for the two construction
// outcomes that are not a seam: a factory that fails, and one that answers with
// no seam at all (the shape internal/cli/manifest_trackers_e2e_test.go
// registers).
func trackerVerbFactoryRegistry(factory tracker.Factory) *tracker.Registry {
	providers := tracker.NewRegistry()
	providers.Register("fake", factory)
	return providers
}

// trackerVerbRepo is the in-process harness: a fresh git clone as the working
// directory and a scratch store, returned so a test can open the store and read
// the log the verbs wrote.
func trackerVerbRepo(t *testing.T, name string) string {
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
	return dbPath
}

func trackerVerbRun(t *testing.T, providers *tracker.Registry, args ...string) result {
	t.Helper()
	r := runWithProviders(t, providers, args...)
	if r.exitCode != 0 {
		t.Fatalf("%v: exit=%d stdout=%q stderr=%q", args, r.exitCode, r.stdout, r.stderr)
	}
	return r
}

// trackerVerbRepoID reads the one Repo a test's store holds.
func trackerVerbRepoID(t *testing.T, s *store.Store) string {
	t.Helper()
	repos, err := s.Repos(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v (err %v), want exactly one", repos, err)
	}
	return repos[0].ID
}

// wantWipError asserts one failed run rendered the --json error envelope on
// stderr with code, and exited with the code the chassis maps that prefix to.
func wantWipError(t *testing.T, r result, wantExit int, wantCode string) string {
	t.Helper()
	if r.exitCode != wantExit {
		t.Fatalf("exit=%d, want %d; stdout=%q stderr=%q", r.exitCode, wantExit, r.stdout, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("stdout = %q, want empty on failure", r.stdout)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q (message %q)", envelope.Error.Code, wantCode, envelope.Error.Message)
	}
	return envelope.Error.Message
}

// -- the JSON shapes step-12 §5 fixed -------------------------------------

type trackerProposalPayload struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	State          string          `json:"state"`
	Subject        string          `json:"subject"`
	Reference      string          `json:"reference"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
	NextStep       string          `json:"nextStep"`
}

type trackerReadPayload struct {
	Ref        string `json:"ref"`
	Backend    string `json:"backend"`
	Capability string `json:"capability"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	URL        string `json:"url"`
	State      struct {
		Class   string `json:"class"`
		Display string `json:"display"`
	} `json:"state"`
	UpdatedAt string `json:"updatedAt"`
}

type trackerRefPayload struct {
	Matter      string `json:"matter"`
	Locator     string `json:"locator"`
	Ref         string `json:"ref"`
	Disposition string `json:"disposition"`
}

type trackerRefsPayload struct {
	Repo       string              `json:"repo"`
	References []trackerRefPayload `json:"references"`
}

type trackerCapabilitiesPayload struct {
	Repo          string `json:"repo"`
	Backend       string `json:"backend"`
	Target        string `json:"target"`
	Project       string `json:"project"`
	CanceledLabel string `json:"canceledLabel"`
	PushLevel     string `json:"pushLevel"`
	BacklogPush   string `json:"backlogPush"`
	Seam          struct {
		Constructed bool   `json:"constructed"`
		Error       string `json:"error"`
		Deliver     bool   `json:"deliver"`
		ReadState   bool   `json:"readState"`
		ReadContent bool   `json:"readContent"`
	} `json:"seam"`
}

// -- 1/2/3: the propose → approve → flush round trip, all three kinds ------

// The whole point of the propose trio is that it stops at `queued` and that the
// flush path cannot tell an operator-authored candidate from a lifecycle one.
// One repo, three proposals, one flush: the fake records exactly what reached
// the seam, the log records exactly what the outcome minted, and the backlog is
// asserted untouched from end to end — an ad-hoc create is nobody's delegation.
func TestTrackerProposeApproveFlushRoundTripsEveryKindWithoutTouchingTheBacklog(t *testing.T) {
	dbPath := trackerVerbRepo(t, "tracker-propose")
	fake := &trackerVerbSeam{results: map[string]tracker.Result{
		"create":  {Outcome: tracker.Delivered, Ref: "FAKE-7"},
		"comment": {Outcome: tracker.Delivered},
		"state":   {Outcome: tracker.Delivered, Lease: "lease-7"},
	}}
	providers := trackerVerbRegistry(fake)
	for _, args := range [][]string{
		{"init", "--json"},
		{"plumbing", "outbox", "backend", "fake"},
	} {
		trackerVerbRun(t, providers, args...)
	}

	created := mustJSON[trackerProposalPayload](t, trackerVerbRun(t, providers,
		"plumbing", "tracker", "propose", "create", "--title", "Investigate flake", "--detail", "seen twice", "--json").stdout)
	commented := mustJSON[trackerProposalPayload](t, trackerVerbRun(t, providers,
		"plumbing", "tracker", "propose", "comment", "FAKE-1", "--body", "an operator wrote this", "--json").stdout)
	moved := mustJSON[trackerProposalPayload](t, trackerVerbRun(t, providers,
		"plumbing", "tracker", "propose", "state", "FAKE-1", "--disposition", "completed", "--json").stdout)

	s := openTestStore(t, dbPath)
	repo := trackerVerbRepoID(t, s)

	for _, tc := range []struct {
		name        string
		payload     trackerProposalPayload
		wantKind    string
		wantRef     string
		wantKeyTail string
		wantBody    string
	}{
		{"create", created, "create", "", ":create:", `{"kind":"create","provenance":"adhoc","title":"Investigate flake","detail":"seen twice"}`},
		{"comment", commented, "comment", "FAKE-1", ":comment:FAKE-1", `{"body":"an operator wrote this"}`},
		{"state", moved, "state", "FAKE-1", ":state:FAKE-1", `{"disposition":"completed"}`},
	} {
		if tc.payload.Kind != tc.wantKind || tc.payload.State != "queued" || tc.payload.Subject != repo {
			t.Errorf("%s proposal = %+v, want kind %q, queued, subject %s", tc.name, tc.payload, tc.wantKind, repo)
		}
		if tc.payload.Reference != tc.wantRef {
			t.Errorf("%s proposal reference = %q, want %q", tc.name, tc.payload.Reference, tc.wantRef)
		}
		if !strings.HasPrefix(tc.payload.IdempotencyKey, "tracker:") || !strings.HasSuffix(tc.payload.IdempotencyKey, tc.wantKeyTail) {
			t.Errorf("%s idempotency key = %q, want tracker:<event>%s", tc.name, tc.payload.IdempotencyKey, tc.wantKeyTail)
		}
		if string(tc.payload.Payload) != tc.wantBody {
			t.Errorf("%s provider payload = %s, want %s", tc.name, tc.payload.Payload, tc.wantBody)
		}
		if tc.payload.NextStep != "wip plumbing outbox approve "+tc.payload.ID {
			t.Errorf("%s next step = %q, want the approve line for %s", tc.name, tc.payload.NextStep, tc.payload.ID)
		}
	}

	// The queue a person is asked to approve is exactly these three.
	listed := mustJSON[struct {
		Entries []outboxPayload `json:"entries"`
	}](t, trackerVerbRun(t, providers, "plumbing", "outbox", "list", "--json").stdout)
	if len(listed.Entries) != 3 {
		t.Fatalf("outbox list = %+v, want the three proposals", listed.Entries)
	}
	for i, want := range []trackerProposalPayload{created, commented, moved} {
		if listed.Entries[i].ID != want.ID || listed.Entries[i].State != "queued" || listed.Entries[i].Kind != want.Kind {
			t.Errorf("outbox entry %d = %+v, want the queued %s proposal %s", i, listed.Entries[i], want.Kind, want.ID)
		}
	}
	if len(fake.delivered) != 0 {
		t.Fatalf("proposing reached the seam: %+v", fake.delivered)
	}

	// Approval is the human boundary; flush is the only thing that delivers.
	for _, id := range []string{created.ID, commented.ID, moved.ID} {
		approved := mustJSON[outboxPayload](t, trackerVerbRun(t, providers, "plumbing", "outbox", "approve", id, "--json").stdout)
		if approved.State != "approved" {
			t.Fatalf("approve %s = %+v, want approved", id, approved)
		}
	}
	flushed := mustJSON[tracker.Report](t, trackerVerbRun(t, providers, "plumbing", "outbox", "flush", "--json").stdout)
	if len(flushed.Entries) != 3 {
		t.Fatalf("flush report = %+v, want three results", flushed)
	}
	for _, entry := range flushed.Entries {
		if entry.State != "flushed" || entry.Reason != "" {
			t.Errorf("flush result %+v, want flushed with no reason", entry)
		}
	}

	// What actually crossed the seam: the stored payload, reference and
	// idempotency key of each candidate, verbatim.
	if len(fake.delivered) != 3 {
		t.Fatalf("seam saw %d deliveries, want 3", len(fake.delivered))
	}
	for i, want := range []trackerProposalPayload{created, commented, moved} {
		got := fake.delivered[i]
		if got.ID != want.ID || got.Kind != want.Kind || got.Ref != want.Reference {
			t.Errorf("delivery %d = %+v, want %s %s ref %q", i, got, want.Kind, want.ID, want.Reference)
		}
		if got.IdempotencyKey != want.IdempotencyKey {
			t.Errorf("delivery %d idempotency key = %q, want %q", i, got.IdempotencyKey, want.IdempotencyKey)
		}
		if string(got.Payload) != string(want.Payload) {
			t.Errorf("delivery %d payload = %s, want %s", i, got.Payload, want.Payload)
		}
	}

	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		id        string
		wantEvent string
	}{
		{"create", created.ID, store.TypeTrackerItemCreated},
		{"comment", commented.ID, store.TypeOutboxFlushed},
		{"state", moved.ID, store.TypeTrackerStatePushed},
	} {
		entry, err := s.OutboxEntry(ctx, repo, tc.id)
		if err != nil || entry.State != "flushed" || entry.Attempts != 1 || entry.Reason != "" {
			t.Errorf("durable %s entry = %+v (err %v), want flushed after one attempt", tc.name, entry, err)
		}
		events, err := s.EventsOfSubject(ctx, tc.id)
		if err != nil || len(events) != 2 || events[0].Type != store.TypeOutboxApproved || events[1].Type != tc.wantEvent {
			t.Fatalf("%s history = %+v (err %v), want approval then %s", tc.name, events, err, tc.wantEvent)
		}
		if tc.wantEvent == store.TypeTrackerItemCreated {
			var binding store.TrackerItemCreated
			if err := json.Unmarshal(events[1].Payload, &binding); err != nil || binding.Ref != "FAKE-7" {
				t.Errorf("creation confirmation = %+v (err %v), want the provider's reference", binding, err)
			}
		}
	}

	// No backlog row, and no backlog event, anywhere in this history: an
	// ad-hoc create is nobody's delegation (store's own fold test asserts the
	// rows; this asserts it through the verb surface).
	entries, err := s.Backlog(ctx, repo)
	if err != nil || len(entries) != 0 {
		t.Errorf("backlog = %+v (err %v), want empty", entries, err)
	}
	active, err := s.ActiveBacklog(ctx, repo)
	if err != nil || len(active) != 0 {
		t.Errorf("active backlog = %+v (err %v), want empty", active, err)
	}
	events, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.HasPrefix(event.Type, "backlog.") {
			t.Errorf("an ad-hoc tracker round trip emitted %s", event.Type)
		}
	}
}

// -- 4: read ---------------------------------------------------------------

// read prefers ContentReader, falls back to StateReader, and reports the
// absence of both as a coded refusal rather than a crash. Each answer comes
// from a differently-capable seam registered under the same stored backend
// name, which is the whole discrimination the `capability` field exists for.
func TestTrackerReadPrefersContentFallsBackToStateAndRefusesWhenNeitherExists(t *testing.T) {
	trackerVerbRepo(t, "tracker-read")
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	content := &trackerVerbSeam{content: map[string]tracker.Content{
		"FAKE-1": {
			Ref: "FAKE-1", Title: "Investigate flake", Body: "seen twice\n", URL: "https://fake.test/items/1",
			State:     tracker.LiveState{Class: tracker.LiveActive, Display: "In Progress", Lease: "lease-1"},
			UpdatedAt: updated,
		},
	}}
	contentProviders := trackerVerbRegistry(content)
	for _, args := range [][]string{
		{"init", "--json"},
		{"plumbing", "outbox", "backend", "fake"},
	} {
		trackerVerbRun(t, contentProviders, args...)
	}

	read := mustJSON[trackerReadPayload](t, trackerVerbRun(t, contentProviders,
		"plumbing", "tracker", "read", "FAKE-1", "--json").stdout)
	if read.Ref != "FAKE-1" || read.Backend != "fake" || read.Capability != "content" {
		t.Fatalf("content read = %+v, want the fake backend's content answer", read)
	}
	if read.Title != "Investigate flake" || read.Body != "seen twice\n" || read.URL != "https://fake.test/items/1" {
		t.Errorf("content read = %+v, want the provider's own title, body and url", read)
	}
	if read.State.Class != "active" || read.State.Display != "In Progress" || read.UpdatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("content read state = %+v / %q, want active (In Progress) at RFC3339 UTC", read.State, read.UpdatedAt)
	}
	// The observed lease is the flush path's own token and is deliberately
	// unpublished (step-12 §3).
	if strings.Contains(read.Body, "lease-1") {
		t.Errorf("read published the observed lease: %+v", read)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trackerVerbRun(t, contentProviders,
		"plumbing", "tracker", "read", "FAKE-1", "--json").stdout), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["lease"]; present {
		t.Errorf("read payload carries a lease key: %v", raw)
	}

	human := trackerVerbRun(t, contentProviders, "plumbing", "tracker", "read", "FAKE-1")
	wantHuman := "Investigate flake\n  ref: FAKE-1\n  state: active (In Progress)\n" +
		"  url: https://fake.test/items/1\n  updated: 2026-01-02T03:04:05Z\n\nseen twice\n"
	if human.stdout != wantHuman {
		t.Errorf("human read = %q, want %q", human.stdout, wantHuman)
	}

	// The same stored backend, behind a seam that reads lifecycle only.
	stateOnly := trackerVerbRegistry(&cliAlignmentReader{states: map[string]tracker.LiveState{
		"FAKE-1": {Class: tracker.LiveCompleted, Display: "Done", Lease: "lease-2"},
	}})
	fallbackRaw := trackerVerbRun(t, stateOnly, "plumbing", "tracker", "read", "FAKE-1", "--json").stdout
	fallback := mustJSON[trackerReadPayload](t, fallbackRaw)
	if fallback.Capability != "state" || fallback.State.Class != "completed" || fallback.State.Display != "Done" {
		t.Fatalf("state fallback = %+v, want the state-only answer", fallback)
	}
	var fallbackKeys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fallbackRaw), &fallbackKeys); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"title", "body", "url", "updatedAt"} {
		if _, present := fallbackKeys[absent]; present {
			t.Errorf("state fallback carries %q, want it absent so a caller can tell an absent title from an empty one", absent)
		}
	}
	humanFallback := trackerVerbRun(t, stateOnly, "plumbing", "tracker", "read", "FAKE-1")
	wantFallback := "FAKE-1\n  ref: FAKE-1\n  state: completed (Done)\n" +
		"  (the fake backend reads lifecycle only; it cannot read titles or bodies)\n"
	if humanFallback.stdout != wantFallback {
		t.Errorf("human state fallback = %q, want %q", humanFallback.stdout, wantFallback)
	}

	// A seam that reads neither is a capability answer, not a crash.
	unsupported := runWithProviders(t, trackerVerbRegistry(trackerVerbDeliverOnlySeam{}),
		"plumbing", "tracker", "read", "FAKE-1", "--json")
	message := wantWipError(t, unsupported, 1, "validation.tracker-read-unsupported")
	if !strings.Contains(message, "fake backend cannot read") {
		t.Errorf("unsupported message = %q, want it to name the backend", message)
	}

	// A whitespace-only reference is the verb's own refusal, before any seam.
	blank := runWithProviders(t, contentProviders, "plumbing", "tracker", "read", "   ", "--json")
	wantWipError(t, blank, 1, "validation.missing-reference")

	// And no backend at all is the refusal a person can act on.
	trackerVerbRun(t, contentProviders, "plumbing", "outbox", "backend", "none")
	none := runWithProviders(t, contentProviders, "plumbing", "tracker", "read", "FAKE-1", "--json")
	noneMessage := wantWipError(t, none, 1, "validation.no-tracker-backend")
	if !strings.Contains(noneMessage, "wip plumbing outbox backend") {
		t.Errorf("no-backend message = %q, want it to name the verb that fixes it", noneMessage)
	}
}

// -- 5: refs ---------------------------------------------------------------

// refs is a local read: it lists this repo's live bindings with the
// disposition the local model expects, and the locator form scopes to the
// Matter that owns the addressed node (D44). No backend is configured
// anywhere in this test, which is the point — refs constructs no seam.
func TestTrackerRefsListsLiveBindingsWithAggregateDispositionAndScopesByLocator(t *testing.T) {
	dbPath := trackerVerbRepo(t, "tracker-refs")
	providers := tracker.NewRegistry()
	trackerVerbRun(t, providers, "init", "--json")

	s := openTestStore(t, dbPath)
	repo := trackerVerbRepoID(t, s)

	empty := mustJSON[trackerRefsPayload](t, trackerVerbRun(t, providers, "plumbing", "tracker", "refs", "--json").stdout)
	if empty.Repo != repo || empty.References == nil || len(empty.References) != 0 {
		t.Fatalf("empty refs = %+v, want this repo and an empty (never null) list", empty)
	}
	if human := trackerVerbRun(t, providers, "plumbing", "tracker", "refs"); human.stdout != "no live tracker references\n" {
		t.Errorf("empty human refs = %q", human.stdout)
	}

	for _, args := range [][]string{
		{"plumbing", "matter", "create", "--title", "Alpha", "--locator", "alpha"},
		{"plumbing", "bind", "alpha", "A-1"},
		{"plumbing", "matter", "create", "--title", "Beta", "--locator", "beta"},
		{"plumbing", "bind", "beta", "B-2"},
	} {
		trackerVerbRun(t, providers, args...)
	}
	stage := mustJSON[nodePayload](t, trackerVerbRun(t, providers,
		"plumbing", "stage", "create", "beta", "--title", "Investigate", "--json").stdout)

	planned := mustJSON[trackerRefsPayload](t, trackerVerbRun(t, providers, "plumbing", "tracker", "refs", "--json").stdout)
	if len(planned.References) != 2 {
		t.Fatalf("refs = %+v, want both bindings", planned.References)
	}
	for _, ref := range planned.References {
		if ref.Disposition != "" || ref.Matter == "" {
			t.Errorf("planned binding = %+v, want an empty disposition and the Matter's identity", ref)
		}
	}

	// Starting Alpha makes its reference deliverable; Beta stays planned.
	trackerVerbRun(t, providers, "plumbing", "start", "alpha")
	started := mustJSON[trackerRefsPayload](t, trackerVerbRun(t, providers, "plumbing", "tracker", "refs", "--json").stdout)
	want := map[string]trackerRefPayload{
		"A-1": {Locator: "alpha", Ref: "A-1", Disposition: "active"},
		"B-2": {Locator: "beta", Ref: "B-2", Disposition: ""},
	}
	for _, got := range started.References {
		expected, ok := want[got.Ref]
		if !ok {
			t.Fatalf("unexpected binding %+v", got)
		}
		if got.Locator != expected.Locator || got.Disposition != expected.Disposition {
			t.Errorf("binding %+v, want locator %q disposition %q", got, expected.Locator, expected.Disposition)
		}
	}
	human := trackerVerbRun(t, providers, "plumbing", "tracker", "refs").stdout
	for _, fragment := range []string{"alpha", "active", "A-1", "beta", "-", "B-2"} {
		if !strings.Contains(human, fragment) {
			t.Errorf("human refs = %q, want it to mention %q", human, fragment)
		}
	}

	// The locator form scopes to one Matter, whether the locator names the
	// Matter or a node inside it.
	for _, locator := range []string{"beta", "beta/" + stage.Locator, stage.ID} {
		scoped := mustJSON[trackerRefsPayload](t, trackerVerbRun(t, providers,
			"plumbing", "tracker", "refs", locator, "--json").stdout)
		if len(scoped.References) != 1 || scoped.References[0].Ref != "B-2" || scoped.References[0].Locator != "beta" {
			t.Errorf("refs %s = %+v, want only Beta's binding", locator, scoped.References)
		}
	}
	unknown := runWithProviders(t, providers, "plumbing", "tracker", "refs", "nope", "--json")
	wantWipError(t, unknown, 1, "validation.unknown-locator")
}

// -- 6: capabilities -------------------------------------------------------

// capabilities describes the configuration and what the constructed seam can
// do. It never contacts a tracker, and a seam that will not construct is data
// on a successful run, because describing exactly that is why the verb exists.
func TestTrackerCapabilitiesReportsConfigurationAndConstructedSeamCapabilities(t *testing.T) {
	dbPath := trackerVerbRepo(t, "tracker-capabilities")
	full := &trackerVerbSeam{}
	providers := trackerVerbRegistry(full)
	trackerVerbRun(t, providers, "init", "--json")

	s := openTestStore(t, dbPath)
	repo := trackerVerbRepoID(t, s)

	unconfiguredRaw := trackerVerbRun(t, providers, "plumbing", "tracker", "capabilities", "--json").stdout
	unconfigured := mustJSON[trackerCapabilitiesPayload](t, unconfiguredRaw)
	if unconfigured.Repo != repo || unconfigured.Backend != "" || unconfigured.PushLevel != "off" || unconfigured.BacklogPush != "manual" {
		t.Fatalf("unconfigured capabilities = %+v", unconfigured)
	}
	if unconfigured.Seam.Constructed || unconfigured.Seam.Error != "" || unconfigured.Seam.Deliver {
		t.Errorf("unconfigured seam = %+v, want nothing constructed and no error", unconfigured.Seam)
	}
	if strings.Contains(unconfiguredRaw, `"error"`) {
		t.Errorf("unconfigured payload carries an error key: %s", unconfiguredRaw)
	}
	wantHuman := "backend: none\ntarget: none\nproject: none\ncanceled-label: none\n" +
		"push-level: off\nbacklog-push: manual\nseam: not constructed (no tracker backend configured)\n"
	if human := trackerVerbRun(t, providers, "plumbing", "tracker", "capabilities"); human.stdout != wantHuman {
		t.Errorf("unconfigured human capabilities = %q, want %q", human.stdout, wantHuman)
	}

	for _, args := range [][]string{
		{"plumbing", "outbox", "backend", "fake"},
		{"plumbing", "outbox", "target", "team-uuid"},
		{"plumbing", "outbox", "project", "project-uuid"},
		{"plumbing", "outbox", "canceled-label", "wf::canceled"},
	} {
		trackerVerbRun(t, providers, args...)
	}
	configured := mustJSON[trackerCapabilitiesPayload](t, trackerVerbRun(t, providers,
		"plumbing", "tracker", "capabilities", "--json").stdout)
	if configured.Backend != "fake" || configured.Target != "team-uuid" || configured.Project != "project-uuid" || configured.CanceledLabel != "wf::canceled" {
		t.Fatalf("configured capabilities = %+v, want the four opaque config values echoed", configured)
	}
	if configured.PushLevel != "boundary" || configured.BacklogPush != "manual" {
		t.Errorf("configured effective settings = %+v, want boundary/manual", configured)
	}
	if !configured.Seam.Constructed || !configured.Seam.Deliver || !configured.Seam.ReadState || !configured.Seam.ReadContent {
		t.Errorf("full seam = %+v, want every capability true", configured.Seam)
	}
	wantConfiguredHuman := "backend: fake\ntarget: team-uuid\nproject: project-uuid\ncanceled-label: wf::canceled\n" +
		"push-level: boundary\nbacklog-push: manual\nseam: constructed; deliver=true readState=true readContent=true\n"
	if human := trackerVerbRun(t, providers, "plumbing", "tracker", "capabilities"); human.stdout != wantConfiguredHuman {
		t.Errorf("configured human capabilities = %q, want %q", human.stdout, wantConfiguredHuman)
	}
	if len(full.delivered) != 0 || len(full.reads) != 0 {
		t.Errorf("capabilities contacted the seam: delivered=%+v reads=%v", full.delivered, full.reads)
	}

	// A seam that reads lifecycle only reports exactly that.
	stateOnly := mustJSON[trackerCapabilitiesPayload](t, trackerVerbRun(t,
		trackerVerbRegistry(&cliAlignmentReader{states: map[string]tracker.LiveState{}}),
		"plumbing", "tracker", "capabilities", "--json").stdout)
	if !stateOnly.Seam.Constructed || !stateOnly.Seam.ReadState || stateOnly.Seam.ReadContent {
		t.Errorf("state-only seam = %+v, want readState without readContent", stateOnly.Seam)
	}

	// A factory that fails, and one that answers with no seam at all: both are
	// data on an exit-0 run.
	broken := mustJSON[trackerCapabilitiesPayload](t, trackerVerbRun(t,
		trackerVerbFactoryRegistry(func(tracker.FactoryInput) (tracker.Seam, error) {
			return nil, errors.New("fake tracker: set WIP_FAKE_TOKEN")
		}),
		"plumbing", "tracker", "capabilities", "--json").stdout)
	if broken.Seam.Constructed || broken.Seam.Error != "fake tracker: set WIP_FAKE_TOKEN" {
		t.Errorf("broken seam = %+v, want the provider's own words as data", broken.Seam)
	}
	brokenHuman := trackerVerbRun(t, trackerVerbFactoryRegistry(func(tracker.FactoryInput) (tracker.Seam, error) {
		return nil, errors.New("fake tracker: set WIP_FAKE_TOKEN")
	}), "plumbing", "tracker", "capabilities")
	if !strings.HasSuffix(brokenHuman.stdout, "seam: not constructed: fake tracker: set WIP_FAKE_TOKEN\n") {
		t.Errorf("broken human capabilities = %q", brokenHuman.stdout)
	}

	empty := mustJSON[trackerCapabilitiesPayload](t, trackerVerbRun(t,
		trackerVerbFactoryRegistry(func(tracker.FactoryInput) (tracker.Seam, error) { return nil, nil }),
		"plumbing", "tracker", "capabilities", "--json").stdout)
	if empty.Seam.Constructed || empty.Seam.Error != "the fake factory returned no seam" {
		t.Errorf("nil-seam capabilities = %+v, want a described absence rather than a capability", empty.Seam)
	}
}

// capabilities against a registered provider that will not construct — the
// production registry's own github factory, in a clone with no GitHub remote.
// A missing token or an unusable remote is the case the verb exists to
// describe, so it is data and the exit code stays 0.
func TestTrackerCapabilitiesReportsAnUnconstructableBackendAsDataAndExitsZero(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "outbox", "backend", "github"); r.exitCode != 0 {
		t.Fatalf("set backend: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r := runIn(t, dir, dbEnv, "plumbing", "tracker", "capabilities", "--json")
	if r.exitCode != 0 {
		t.Fatalf("capabilities: exit=%d stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	payload := mustJSON[trackerCapabilitiesPayload](t, r.stdout)
	if payload.Backend != "github" || payload.PushLevel != "boundary" {
		t.Errorf("capabilities = %+v, want the configured github backend at its default level", payload)
	}
	if payload.Seam.Constructed || payload.Seam.Deliver || payload.Seam.ReadState || payload.Seam.ReadContent {
		t.Errorf("seam = %+v, want nothing constructed", payload.Seam)
	}
	if !strings.HasPrefix(payload.Seam.Error, "github tracker: ") {
		t.Errorf("seam.error = %q, want the github factory's own words", payload.Seam.Error)
	}
}

// -- validation ------------------------------------------------------------

// The propose trio's refusals, and the one Cobra owns. A flag passed empty is
// the write surface's coded refusal; a flag left out entirely is Cobra's usage
// error, exit 2 — the established convention (step-12 §5). Nothing here needs a
// backend: a proposal with no provider behind it is a legitimate queue.
func TestTrackerProposeRefusesMalformedInputAndCommitsNothing(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{"blank title", []string{"propose", "create", "--title", "   "}, "validation.missing-title"},
		{"blank reference", []string{"propose", "comment", "   ", "--body", "text"}, "validation.missing-reference"},
		{"blank body", []string{"propose", "comment", "FAKE-1", "--body", "   "}, "validation.missing-body"},
		{"unknown disposition", []string{"propose", "state", "FAKE-1", "--disposition", "nope"}, "validation.invalid-disposition"},
		{"blank state reference", []string{"propose", "state", " ", "--disposition", "active"}, "validation.missing-reference"},
	} {
		args := append([]string{"plumbing", "tracker"}, tc.args...)
		r := runIn(t, dir, dbEnv, append(args, "--json")...)
		wantWipError(t, r, 1, tc.code)
	}

	// A required flag left out is Cobra's usage error, not one of ours.
	missing := runIn(t, dir, dbEnv, "plumbing", "tracker", "propose", "comment", "FAKE-1", "--json")
	if missing.exitCode != 2 || !strings.Contains(missing.stderr, `required flag(s) "body" not set`) {
		t.Fatalf("absent required flag: exit=%d stderr=%q", missing.exitCode, missing.stderr)
	}

	listed := mustJSON[struct {
		Entries []outboxPayload `json:"entries"`
	}](t, runIn(t, dir, dbEnv, "plumbing", "outbox", "list", "--json").stdout)
	if len(listed.Entries) != 0 {
		t.Fatalf("refused proposals queued %+v", listed.Entries)
	}
}

// -- manifest and group placement -----------------------------------------

// The generated surface: every leaf the new group adds reaches the manifest as
// plumbing (the walk hard-errors on an unannotated leaf, so this also proves
// D112 compliance), and the group sits between step and unbind in the
// hand-kept `wip plumbing --help` order.
func TestManifestIncludesTrackerPlumbing(t *testing.T) {
	manifest := run(t, nil, "manifest", "--json")
	if manifest.exitCode != 0 {
		t.Fatalf("manifest: exit=%d stderr=%q", manifest.exitCode, manifest.stderr)
	}
	var m struct {
		Verbs []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(manifest.stdout), &m); err != nil {
		t.Fatalf("manifest JSON = %q, err=%v", manifest.stdout, err)
	}
	for _, name := range []string{
		"plumbing tracker read",
		"plumbing tracker refs",
		"plumbing tracker capabilities",
		"plumbing tracker propose create",
		"plumbing tracker propose comment",
		"plumbing tracker propose state",
	} {
		found := false
		for _, verb := range m.Verbs {
			if verb.Name == name {
				found = true
				if verb.Kind != "plumbing" {
					t.Errorf("manifest %q kind = %q, want plumbing", name, verb.Kind)
				}
			}
		}
		if !found {
			t.Errorf("manifest does not register %q", name)
		}
	}

	help := run(t, nil, "plumbing", "--help")
	if help.exitCode != 0 {
		t.Fatalf("plumbing --help: exit=%d stderr=%q", help.exitCode, help.stderr)
	}
	step := strings.Index(help.stdout, "\n  step ")
	trackerAt := strings.Index(help.stdout, "\n  tracker ")
	unbind := strings.Index(help.stdout, "\n  unbind ")
	if step < 0 || trackerAt < 0 || unbind < 0 || step >= trackerAt || trackerAt >= unbind {
		t.Fatalf("plumbing --help does not list tracker between step and unbind:\n%s", help.stdout)
	}
}
