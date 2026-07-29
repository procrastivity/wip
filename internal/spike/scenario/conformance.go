package scenario

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// OpenFunc opens a spike's store at a filesystem path, creating and migrating
// it if absent. It is called more than once against the same path by the
// conformance run: durability across a close/reopen is part of the contract,
// since a store that only answers from process memory has not answered
// invariant 2 at all.
type OpenFunc func(t *testing.T, path string, env Env) Store

// FixedEnv is the tier context every conformance run uses. Fixed, synthetic
// ULIDs: tier *resolution* is out of the pinned scope, tier *dimensions* are
// not (MODEL §10).
var FixedEnv = Env{
	Repo:     "01JQ0000000000000000000RPO",
	Clone:    "01JQ0000000000000000000CLN",
	Worktree: "01JQ00000000000000000000WT",
}

// RunConformance drives the pinned scenario against a spike's store and holds
// it to MODEL §10's two invariants plus the fidelity that binds from event one.
// Both spikes pass exactly this; anything either shape needs *beyond* passing
// it is what the rubric is scoring.
func RunConformance(t *testing.T, open OpenFunc) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "spike.db")

	st := open(t, path, FixedEnv)
	defer func() { _ = st.Close() }()

	// (a) create a Matter.
	matterID, err := st.CreateMatter(ctx, "first matter")
	if err != nil {
		t.Fatalf("CreateMatter: %v", err)
	}
	second, err := st.CreateMatter(ctx, "second matter")
	if err != nil {
		t.Fatalf("CreateMatter (second): %v", err)
	}

	// (b) add Steps under it.
	step1, err := st.AddStep(ctx, matterID, "step one")
	if err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	step2, err := st.AddStep(ctx, matterID, "step two")
	if err != nil {
		t.Fatalf("AddStep (second): %v", err)
	}

	// A born-but-unstarted Matter never reports In Progress (MODEL §2.2).
	assertInProgress(t, st, "after birth, before any start")

	// A Step under an unknown Matter is refused, and refusal writes nothing.
	before := events(t, st)
	if _, err := st.AddStep(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ", "orphan"); err == nil {
		t.Fatal("AddStep under an unknown Matter: want error, got nil")
	}
	assertPrefix(t, before, events(t, st), "refused AddStep must write no event")

	// (c) start work: D57 — one command, two verbs, two events, ancestor first.
	before = events(t, st)
	if err := st.Start(ctx, step1); err != nil {
		t.Fatalf("Start(step1): %v", err)
	}
	added := suffix(t, before, events(t, st))
	assertTypes(t, added, []string{TypeMatterStarted, TypeStepStarted},
		"starting a Step under a Planned Matter")
	if added[0].Subject != matterID || added[1].Subject != step1 {
		t.Fatalf("auto-start subjects: got %s,%s want %s,%s",
			added[0].Subject, added[1].Subject, matterID, step1)
	}

	// The founding question, answered: the Matter and the started Step, and
	// nothing else — not the sibling Step, not the untouched second Matter.
	assertInProgress(t, st, "after starting step-01", matterID, step1)

	// Starting an already-started node is refused; a second event for one
	// transition would break invariant 1's "exactly one".
	before = events(t, st)
	if err := st.Start(ctx, step1); err == nil {
		t.Fatal("Start on an already-started node: want error, got nil")
	}
	assertPrefix(t, before, events(t, st), "refused Start must write no event")

	// A second Step under an already-started Matter starts alone — the cascade
	// is over Planned ancestors only.
	before = events(t, st)
	if err := st.Start(ctx, step2); err != nil {
		t.Fatalf("Start(step2): %v", err)
	}
	assertTypes(t, suffix(t, before, events(t, st)),
		[]string{TypeStepStarted}, "starting a Step under a started Matter")
	assertInProgress(t, st, "both Steps started", matterID, step1, step2)

	// Done leaves the query.
	before = events(t, st)
	if err := st.Finish(ctx, step1); err != nil {
		t.Fatalf("Finish(step1): %v", err)
	}
	assertTypes(t, suffix(t, before, events(t, st)),
		[]string{TypeStepFinished}, "finishing a Step")
	assertInProgress(t, st, "after finishing step-01", matterID, step2)

	// Durability: close, reopen, ask again. Same answers, same log.
	logBefore := events(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened := open(t, path, FixedEnv)
	defer func() { _ = reopened.Close() }()
	assertInProgress(t, reopened, "after reopen", matterID, step2)
	assertPrefix(t, logBefore, events(t, reopened), "log must survive reopen unchanged")

	// The whole log, once, against the envelope rules.
	assertEnvelope(t, events(t, reopened))

	// The second Matter was born and never touched: still Planned, still
	// invisible to the query, still exactly one event to its name.
	countSubject(t, events(t, reopened), second, 1)
}

// assertInProgress checks the answer to the founding question, by identity and
// in creation order.
func assertInProgress(t *testing.T, st Store, when string, want ...string) {
	t.Helper()
	got, err := st.InProgress(context.Background())
	if err != nil {
		t.Fatalf("InProgress (%s): %v", when, err)
	}
	if len(got) != len(want) {
		t.Fatalf("InProgress (%s): got %d nodes %v, want %d %v",
			when, len(got), ids(got), len(want), want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("InProgress (%s) position %d: got %s, want %s", when, i, got[i].ID, want[i])
		}
		if got[i].Lifecycle != InProgress {
			t.Fatalf("InProgress (%s) node %s: lifecycle %q, want %q",
				when, got[i].ID, got[i].Lifecycle, InProgress)
		}
		if got[i].Locator == "" || got[i].Title == "" {
			t.Fatalf("InProgress (%s) node %s: locator/title must be populated, got %q/%q",
				when, got[i].ID, got[i].Locator, got[i].Title)
		}
	}
}

// assertEnvelope holds the whole log to MODEL §10: monotonic identity doubling
// as total order, populated timestamps in UTC, tier dimensions as a static
// function of type (D56), a subject that is an identity, and a payload that is
// a JSON object.
func assertEnvelope(t *testing.T, evs []Event) {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("event log is empty")
	}
	for i, e := range evs {
		if len(e.ID) != 26 {
			t.Fatalf("event %d (%s): id %q is not a 26-character ULID", i, e.Type, e.ID)
		}
		if i > 0 && evs[i-1].ID >= e.ID {
			t.Fatalf("event %d (%s): ids must ascend strictly; %q follows %q",
				i, e.Type, e.ID, evs[i-1].ID)
		}
		if e.OccurredAt.IsZero() {
			t.Fatalf("event %d (%s): occurred_at is zero", i, e.Type)
		}
		if _, off := e.OccurredAt.Zone(); off != 0 {
			t.Fatalf("event %d (%s): occurred_at must be UTC, got offset %d", i, e.Type, off)
		}
		if e.OccurredAt.After(time.Now().Add(time.Minute)) {
			t.Fatalf("event %d (%s): occurred_at is in the future", i, e.Type)
		}
		wantRepo, wantClone, wantWorktree := RequiredDimensions(e.Type)
		checkDim(t, i, e.Type, "repo", e.Repo, FixedEnv.Repo, wantRepo)
		checkDim(t, i, e.Type, "clone", e.Clone, FixedEnv.Clone, wantClone)
		checkDim(t, i, e.Type, "worktree", e.Worktree, FixedEnv.Worktree, wantWorktree)
		if len(e.Subject) != 26 {
			t.Fatalf("event %d (%s): subject %q must be an identity ULID, never a locator",
				i, e.Type, e.Subject)
		}
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("event %d (%s): payload is not a JSON object: %v", i, e.Type, err)
		}
	}
}

func checkDim(t *testing.T, i int, typ, name, got, want string, required bool) {
	t.Helper()
	switch {
	case required && got != want:
		t.Fatalf("event %d (%s): %s dimension is required; got %q want %q", i, typ, name, got, want)
	case !required && got != "":
		t.Fatalf("event %d (%s): %s dimension must be absent; got %q", i, typ, name, got)
	}
}

// assertPrefix holds the log append-only: whatever was there is still there,
// unchanged, in the same order.
func assertPrefix(t *testing.T, before, after []Event, when string) {
	t.Helper()
	if len(after) < len(before) {
		t.Fatalf("%s: log shrank from %d to %d events", when, len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Type != after[i].Type ||
			!before[i].OccurredAt.Equal(after[i].OccurredAt) ||
			before[i].Subject != after[i].Subject {
			t.Fatalf("%s: event %d was rewritten", when, i)
		}
	}
	if len(after) != len(before) {
		t.Fatalf("%s: log grew by %d events", when, len(after)-len(before))
	}
}

// suffix returns the events appended since `before`, holding the prefix
// unchanged on the way through.
func suffix(t *testing.T, before, after []Event) []Event {
	t.Helper()
	if len(after) < len(before) {
		t.Fatalf("log shrank from %d to %d events", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("event %d was rewritten: %q became %q", i, before[i].ID, after[i].ID)
		}
	}
	return after[len(before):]
}

func assertTypes(t *testing.T, got []Event, want []string, when string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d events %v, want %d %v", when, len(got), types(got), len(want), want)
	}
	for i := range want {
		if got[i].Type != want[i] {
			t.Fatalf("%s: event %d is %q, want %q", when, i, got[i].Type, want[i])
		}
	}
}

func countSubject(t *testing.T, evs []Event, subject string, want int) {
	t.Helper()
	n := 0
	for _, e := range evs {
		if e.Subject == subject {
			n++
		}
	}
	if n != want {
		t.Fatalf("subject %s: got %d events, want %d", subject, n, want)
	}
}

func events(t *testing.T, st Store) []Event {
	t.Helper()
	evs, err := st.Events(context.Background())
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	return evs
}

func ids(nodes []Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func types(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}
