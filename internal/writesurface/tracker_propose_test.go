package writesurface

import (
	"context"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// The write surface half of ad-hoc tracker proposals: three verbs that each
// commit one event and hand back the one queued candidate it minted.
//
// What this file owns is the surface's side of that — the coded refusals, the
// entry each success returns, the fact that a proposal is never approved by the
// act of proposing it, and the birth-event read that connects the two.

// wantProposed is the shared assertion on a returned candidate: queued, at the
// Repo, with the kind, reference and provider-facing payload the seam will read.
func wantProposed(t *testing.T, what string, got store.OutboxEntry, repo, kind, ref, payload string) {
	t.Helper()
	if got.ID == "" {
		t.Fatalf("%s returned an entry with no identity: %+v", what, got)
	}
	if got.State != "queued" {
		t.Errorf("%s returned state %q, want queued — proposing never approves", what, got.State)
	}
	if got.Repo != repo || got.Subject != repo {
		t.Errorf("%s returned repo=%q subject=%q, want both %q: a proposal is Repo-tier and node-less",
			what, got.Repo, got.Subject, repo)
	}
	if got.Kind != kind {
		t.Errorf("%s returned kind %q, want %q", what, got.Kind, kind)
	}
	if got.Ref != ref {
		t.Errorf("%s returned ref %q, want %q", what, got.Ref, ref)
	}
	if string(got.Payload) != payload {
		t.Errorf("%s returned payload %s, want %s", what, got.Payload, payload)
	}
	if got.Attempts != 0 || got.Reason != "" {
		t.Errorf("%s returned attempts=%d reason=%q, want a fresh candidate", what, got.Attempts, got.Reason)
	}
	if !strings.HasPrefix(got.IdempotencyKey, "tracker:") {
		t.Errorf("%s returned idempotency key %q, want the tracker candidate key", what, got.IdempotencyKey)
	}
}

// TestProposeRefusesEveryMalformedRequestAndCommitsNothing is the surface's
// validation, one case per rule. Each refusal must be a coded wiperr *and*
// append nothing: the check runs before Commit opens a transaction, so a
// refused proposal leaves no event and no candidate.
func TestProposeRefusesEveryMalformedRequestAndCommitsNothing(t *testing.T) {
	f := newBatchFixture(t, "propose-refusals")
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		call     func() (store.OutboxEntry, error)
		code     string
		fragment string
	}{
		{
			"a create with no title",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerCreate(ctx, f.s, store.ActorHuman, f.env.Repo, "", "a detail with nothing to detail")
			},
			"validation.missing-title", "needs a title",
		},
		{
			"a comment with no reference",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerComment(ctx, f.s, store.ActorHuman, f.env.Repo, "", "a body")
			},
			"validation.missing-reference", "needs the reference it comments on",
		},
		{
			"a comment with no body",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerComment(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", "")
			},
			"validation.missing-body", "needs a body",
		},
		{
			"a state with no reference",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerState(ctx, f.s, store.ActorHuman, f.env.Repo, "", store.TrackerCompleted)
			},
			"validation.missing-reference", "needs the reference it moves",
		},
		{
			"a state with no disposition",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerState(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", "")
			},
			"validation.invalid-disposition", "is not active, completed or canceled",
		},
		{
			"a state with an invented disposition",
			func() (store.OutboxEntry, error) {
				return ProposeTrackerState(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", store.TrackerDisposition("shipped"))
			},
			"validation.invalid-disposition", `"shipped"`,
		},
	} {
		before, err := f.s.Events(ctx)
		if err != nil {
			t.Fatal(err)
		}
		entry, err := tc.call()
		wantValidation(t, err, tc.code, tc.fragment)
		if entry.ID != "" {
			t.Errorf("%s returned entry %+v, want the zero value", tc.name, entry)
		}
		after, err := f.s.Events(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("%s appended %d event(s)", tc.name, len(after)-len(before))
		}
	}

	if got := trackerEntries(t, f.s, f.env.Repo); len(got) != 0 {
		t.Fatalf("after every refusal the outbox holds %d entries: %+v", len(got), got)
	}
}

// TestProposeQueuesOneCandidatePerKind is the Step's central assertion: each
// verb returns one queued candidate, with the reference nullability the v7
// CHECK demands (a create names none, and reads back as the empty string the
// reader COALESCEs it to) and the provider-facing payload the adapters read.
func TestProposeQueuesOneCandidatePerKind(t *testing.T) {
	f := newBatchFixture(t, "propose-kinds")
	ctx := context.Background()

	created, err := ProposeTrackerCreate(ctx, f.s, store.ActorHuman, f.env.Repo, "Publish the runbook", "Operators need it")
	if err != nil {
		t.Fatal(err)
	}
	wantProposed(t, "ProposeTrackerCreate", created, f.env.Repo, "create", "",
		`{"kind":"create","provenance":"`+store.ProvenanceAdhoc+`","title":"Publish the runbook","detail":"Operators need it"}`)

	bare, err := ProposeTrackerCreate(ctx, f.s, store.ActorHuman, f.env.Repo, "No detail at all", "")
	if err != nil {
		t.Fatal(err)
	}
	wantProposed(t, "ProposeTrackerCreate with no detail", bare, f.env.Repo, "create", "",
		`{"kind":"create","provenance":"`+store.ProvenanceAdhoc+`","title":"No detail at all"}`)

	commented, err := ProposeTrackerComment(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", "Rolled the release back")
	if err != nil {
		t.Fatal(err)
	}
	wantProposed(t, "ProposeTrackerComment", commented, f.env.Repo, "comment", "GH-7",
		`{"body":"Rolled the release back"}`)

	moved, err := ProposeTrackerState(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", store.TrackerCompleted)
	if err != nil {
		t.Fatal(err)
	}
	wantProposed(t, "ProposeTrackerState", moved, f.env.Repo, "state", "GH-7",
		`{"disposition":"completed"}`)

	// Four proposals, four distinct candidates, in birth order, and nothing
	// else: one event in, one row out, every time.
	entries := trackerEntries(t, f.s, f.env.Repo)
	want := []string{created.ID, bare.ID, commented.ID, moved.ID}
	if len(entries) != len(want) {
		t.Fatalf("the outbox holds %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, id := range want {
		if entries[i].ID != id {
			t.Errorf("outbox entry %d = %s, want %s (birth order)", i, entries[i].ID, id)
		}
	}
	keys := map[string]bool{}
	for _, e := range entries {
		if keys[e.IdempotencyKey] {
			t.Errorf("two candidates share the idempotency key %q", e.IdempotencyKey)
		}
		keys[e.IdempotencyKey] = true
	}
}

// TestProposeNeedsNoBackendAndLeavesApprovalToTheHuman holds the two postures
// the propose trio inherits: a proposal queues with no provider configured
// (absence bites at flush, D4), and proposing is never approving (D15) — the
// entry only moves when OutboxApprove says so, through the ordinary outbox
// path, which must accept an ad-hoc entry exactly as it accepts a delegated one.
func TestProposeNeedsNoBackendAndLeavesApprovalToTheHuman(t *testing.T) {
	f := newBatchFixture(t, "propose-approval")
	ctx := context.Background()

	// The fixture configures no tracker.backend; assert that rather than
	// assume it, because the whole posture claim rests on it.
	if backend, _, err := f.s.Config(ctx, f.env.Repo, store.TrackerBackendKey); err != nil || backend != "" {
		t.Fatalf("fixture tracker.backend = %q (err %v), want unset", backend, err)
	}

	created, err := ProposeTrackerCreate(ctx, f.s, store.ActorHuman, f.env.Repo, "Approve me", "")
	if err != nil {
		t.Fatalf("propose a create with no backend configured: %v", err)
	}
	commented, err := ProposeTrackerComment(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", "Approve me too")
	if err != nil {
		t.Fatalf("propose a comment with no backend configured: %v", err)
	}
	moved, err := ProposeTrackerState(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", store.TrackerActive)
	if err != nil {
		t.Fatalf("propose a state with no backend configured: %v", err)
	}

	type proposal struct {
		name  string
		entry store.OutboxEntry
	}
	for _, p := range []proposal{{"create", created}, {"comment", commented}, {"state", moved}} {
		if p.entry.State != "queued" {
			t.Fatalf("the proposed %s is %q before approval, want queued", p.name, p.entry.State)
		}
		approved, err := OutboxApprove(ctx, f.s, store.ActorHuman, f.env.Repo, p.entry.ID)
		if err != nil {
			t.Fatalf("approve the proposed %s: %v", p.name, err)
		}
		if approved.ID != p.entry.ID || approved.State != "approved" {
			t.Fatalf("approved %s = %+v, want %s approved", p.name, approved, p.entry.ID)
		}
		if approved.Kind != p.entry.Kind || approved.Ref != p.entry.Ref ||
			approved.IdempotencyKey != p.entry.IdempotencyKey ||
			string(approved.Payload) != string(p.entry.Payload) {
			t.Fatalf("approving the %s changed the candidate: %+v, was %+v", p.name, approved, p.entry)
		}
		if approved.Attempts != 0 {
			t.Fatalf("approving the %s counted an attempt: %d", p.name, approved.Attempts)
		}
	}
}

// TestOutboxEntryByBirthFindsTheMintedCandidateAndRefusesOtherwise covers the
// read the propose trio is built on, in both directions: the event that minted
// a candidate finds it, and an event that minted none — or the right event in
// the wrong Repo — is a not-found error in the view's own words.
func TestOutboxEntryByBirthFindsTheMintedCandidateAndRefusesOtherwise(t *testing.T) {
	f := newBatchFixture(t, "birth-lookup")
	ctx := context.Background()

	entry, err := ProposeTrackerComment(ctx, f.s, store.ActorHuman, f.env.Repo, "GH-7", "find me by my birth")
	if err != nil {
		t.Fatal(err)
	}

	log, err := f.s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var proposal, innocent store.Event
	for _, ev := range log {
		switch ev.Type {
		case store.TypeTrackerAdhocProposed:
			proposal = ev
		case store.TypeRepoAttached:
			innocent = ev
		}
	}
	if proposal.ID == "" || innocent.ID == "" {
		t.Fatalf("the log is missing the events this test reads: %+v", log)
	}

	got, err := f.s.OutboxEntryByBirth(ctx, f.env.Repo, proposal.ID)
	if err != nil {
		t.Fatalf("read the entry born by %s: %v", proposal.ID, err)
	}
	if got.ID != entry.ID || got.IdempotencyKey != entry.IdempotencyKey ||
		got.Kind != entry.Kind || got.State != entry.State ||
		string(got.Payload) != string(entry.Payload) {
		t.Fatalf("OutboxEntryByBirth returned %+v, want the proposed entry %+v", got, entry)
	}

	// An event that minted no candidate.
	if _, err := f.s.OutboxEntryByBirth(ctx, f.env.Repo, innocent.ID); err == nil {
		t.Fatalf("an event that minted no candidate found one")
	} else if !strings.Contains(err.Error(), "no outbox entry born by "+innocent.ID) {
		t.Fatalf("err = %q, want the view's not-found wording naming %s", err, innocent.ID)
	}

	// The right event, scoped to a Repo it did not happen in.
	other := f.s.NewID()
	if _, err := f.s.OutboxEntryByBirth(ctx, other, proposal.ID); err == nil {
		t.Fatalf("a lookup in another Repo found the entry")
	} else if !strings.Contains(err.Error(), "no outbox entry born by "+proposal.ID) {
		t.Fatalf("err = %q, want the view's not-found wording", err)
	}
}

// TestOutboxEntryByBirthRefusesAGenuineMultiRowBirth covers the third branch
// OutboxEntryByBirth's switch has to take: more than one row born by the same
// event. A propose call never produces that shape — it always mints exactly
// one candidate — so this drives the one write path in the store that
// legitimately does: a narrated Stage closure fans its comment out across
// every live tracker reference on the Matter, one candidate per reference,
// all born by the single stage.finished event (queueStageComments,
// internal/store/project.go). Two live references make that birth genuinely
// plural, which is what TestOffSuppressesCandidatesButBoundaryAndNarratedFanOut
// (tracker_candidates_test.go) already exercises for the fan-out itself; this
// test mirrors that setup and reads the same birth back through
// OutboxEntryByBirth to prove the "not one" branch.
func TestOutboxEntryByBirthRefusesAGenuineMultiRowBirth(t *testing.T) {
	f := newBatchFixture(t, "multi-birth")
	ctx := context.Background()

	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "narrated"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "multi-birth", "T-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "multi-birth", "T-b"); err != nil {
		t.Fatal(err)
	}
	stage, err := CreateStage(ctx, f.s, store.ActorHuman, f.env.Repo, "multi-birth", "Review stage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "multi-birth/"+stage.Locator); err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "multi-birth/"+stage.Locator); err != nil {
		t.Fatal(err)
	}

	log, err := f.s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var finished store.Event
	for _, ev := range log {
		if ev.Type == store.TypeStageFinished {
			finished = ev
		}
	}
	if finished.ID == "" {
		t.Fatalf("the log is missing the stage.finished event this test reads: %+v", log)
	}

	var comments int
	for _, e := range trackerEntries(t, f.s, f.env.Repo) {
		if e.Kind == "comment" {
			comments++
		}
	}
	if comments != 2 {
		t.Fatalf("the Stage closure queued %d comment candidates, want one for each of two references: %+v", comments, trackerEntries(t, f.s, f.env.Repo))
	}

	_, err = f.s.OutboxEntryByBirth(ctx, f.env.Repo, finished.ID)
	if err == nil {
		t.Fatalf("a genuinely plural birth found one entry")
	}
	if !strings.Contains(err.Error(), "minted 2") || !strings.Contains(err.Error(), "not one") {
		t.Fatalf("err = %q, want it to mention \"minted 2\" and \"not one\"", err)
	}
}
