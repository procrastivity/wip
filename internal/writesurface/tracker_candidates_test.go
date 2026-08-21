package writesurface

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func trackerEntries(t *testing.T, s *store.Store, repo string) []store.OutboxEntry {
	t.Helper()
	entries, err := s.Outbox(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func dispositionOf(t *testing.T, entry store.OutboxEntry) store.TrackerDisposition {
	t.Helper()
	var payload struct {
		Disposition store.TrackerDisposition `json:"disposition"`
	}
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Disposition
}

func TestMatterBoundariesQueueWithoutPrematureComposition(t *testing.T) {
	f := newBatchFixture(t, "candidate-boundaries")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "candidate-boundaries", "T-1"); err != nil {
		t.Fatal(err)
	}
	if entries := trackerEntries(t, f.s, f.env.Repo); len(entries) != 0 {
		t.Fatalf("Planned bind queued candidates: %+v", entries)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "candidate-boundaries"); err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "candidate-boundaries"); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)
	if len(entries) != 2 {
		t.Fatalf("outbox entries = %d, want start and finish candidates", len(entries))
	}
	for i, want := range []store.TrackerDisposition{store.TrackerActive, store.TrackerCompleted} {
		if entries[i].Kind != "state" || entries[i].Subject != f.matter || entries[i].Ref != "T-1" || dispositionOf(t, entries[i]) != want {
			t.Fatalf("candidate %d = %+v payload=%s, want state %s", i, entries[i], entries[i].Payload, want)
		}
	}
	if entries[0].ID == entries[1].ID || entries[0].IdempotencyKey == entries[1].IdempotencyKey {
		t.Fatal("two state boundaries collapsed before flush")
	}
}

func TestOffSuppressesCandidatesButBoundaryAndNarratedFanOut(t *testing.T) {
	f := newBatchFixture(t, "fanout")
	ctx := context.Background()
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout", "T-off"); err != nil {
		t.Fatal(err)
	}
	if got := trackerEntries(t, f.s, f.env.Repo); len(got) != 0 {
		t.Fatalf("off queued %+v", got)
	}
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "narrated"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout", "T-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout", "T-b"); err != nil {
		t.Fatal(err)
	}
	stage, err := CreateStage(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout", "Review stage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout/"+stage.Locator); err != nil {
		t.Fatal(err)
	}
	before := len(trackerEntries(t, f.s, f.env.Repo))
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "fanout/"+stage.Locator); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)[before:]
	if len(entries) != 3 { // T-off remains a live reference and receives equal fan-out.
		t.Fatalf("Stage closure queued %d comments, want one for each reference", len(entries))
	}
	for _, entry := range entries {
		var payload struct {
			Stage  string `json:"stage"`
			Title  string `json:"title"`
			Action string `json:"action"`
		}
		if entry.Kind != "comment" || entry.Subject != f.matter || json.Unmarshal(entry.Payload, &payload) != nil || payload.Stage != stage.ID || payload.Title != stage.Title || payload.Action != "closed" {
			t.Fatalf("Stage comment = %+v payload=%s", entry, entry.Payload)
		}
	}
}

func TestFinalGateCloseQueuesMatterCompletionAndLateStageCommentOnce(t *testing.T) {
	f := newBatchFixture(t, "gate-candidates")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "narrated"); err != nil {
		t.Fatal(err)
	}
	if err := DeclareGate(ctx, f.s, f.env.Repo, "review", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-candidates", "T-gate"); err != nil {
		t.Fatal(err)
	}
	stage, err := CreateStage(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-candidates", "Late stage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-candidates/"+stage.Locator); err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-candidates/"+stage.Locator); err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "gate-candidates"); err != nil {
		t.Fatal(err)
	}
	before := len(trackerEntries(t, f.s, f.env.Repo))
	if _, err := CloseGate(ctx, f.s, store.ActorHuman, f.env.Repo, "review", "gate-candidates"); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)[before:]
	if len(entries) != 2 {
		t.Fatalf("final gate candidates = %+v", entries)
	}
	kinds := map[string]bool{}
	for _, entry := range entries {
		kinds[entry.Kind] = true
		if entry.Kind == "state" && dispositionOf(t, entry) != store.TrackerCompleted {
			t.Fatalf("final gate state = %s", dispositionOf(t, entry))
		}
	}
	if !kinds["state"] || !kinds["comment"] {
		t.Fatalf("final gate candidates = %+v", entries)
	}
}

func TestSharedReferenceAggregationAndFinalUnbind(t *testing.T) {
	f := newBatchFixture(t, "shared-a")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	b, err := CreateMatter(ctx, f.s, store.ActorHuman, f.env.Repo, "Shared B", "shared-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{"shared-a", "shared-b"} {
		if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, locator, "T-shared"); err != nil {
			t.Fatal(err)
		}
		if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, locator); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Cancel(ctx, f.s, store.ActorHuman, f.env.Repo, "shared-a", ""); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)
	if got := dispositionOf(t, entries[len(entries)-1]); got != store.TrackerActive {
		t.Fatalf("one cancellation with another active = %s", got)
	}
	if _, err := Cancel(ctx, f.s, store.ActorHuman, f.env.Repo, "shared-b", ""); err != nil {
		t.Fatal(err)
	}
	entries = trackerEntries(t, f.s, f.env.Repo)
	if got := dispositionOf(t, entries[len(entries)-1]); got != store.TrackerCanceled {
		t.Fatalf("all canceled = %s", got)
	}
	if _, err := Unbind(ctx, f.s, store.ActorHuman, f.env.Repo, "shared-a", "T-shared"); err != nil {
		t.Fatal(err)
	}
	before := len(trackerEntries(t, f.s, f.env.Repo))
	if _, err := Unbind(ctx, f.s, store.ActorHuman, f.env.Repo, "shared-b", "T-shared"); err != nil {
		t.Fatal(err)
	}
	if after := len(trackerEntries(t, f.s, f.env.Repo)); after != before {
		t.Fatalf("final unbind queued %d cleanup candidate(s)", after-before)
	}
	_ = b
}

func TestAllPlannedSharedReferenceQueuesOnFirstStart(t *testing.T) {
	f := newBatchFixture(t, "planned-shared-a")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	b, err := CreateMatter(ctx, f.s, store.ActorHuman, f.env.Repo, "Planned shared B", "planned-shared-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{"planned-shared-a", b.Locator} {
		if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, locator, "T-planned-shared"); err != nil {
			t.Fatal(err)
		}
	}
	if entries := trackerEntries(t, f.s, f.env.Repo); len(entries) != 0 {
		t.Fatalf("all-Planned bindings queued candidates: %+v", entries)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, b.Locator); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)
	if len(entries) != 1 || entries[0].Ref != "T-planned-shared" || dispositionOf(t, entries[0]) != store.TrackerActive {
		t.Fatalf("first Matter start candidates = %+v, want one active shared-reference candidate", entries)
	}
}

func TestCandidateRebuildUsesEventSnapshotNotCurrentConfig(t *testing.T) {
	f := newBatchFixture(t, "rebuild-candidates")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "rebuild-candidates", "T-rebuild"); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "rebuild-candidates"); err != nil {
		t.Fatal(err)
	}
	before := trackerEntries(t, f.s, f.env.Repo)
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "off"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	after := trackerEntries(t, f.s, f.env.Repo)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("candidates changed across rebuild after config change:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestDescendantCascadeAndNonqualifyingTransitionsQueueNoChildCandidate(t *testing.T) {
	f := newBatchFixture(t, "cascade-candidates")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates", "T-cascade"); err != nil {
		t.Fatal(err)
	}
	step, err := CreateStep(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates", "Child")
	if err != nil {
		t.Fatal(err)
	}
	before := len(trackerEntries(t, f.s, f.env.Repo))
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates/"+step.Locator); err != nil {
		t.Fatal(err)
	}
	afterStart := trackerEntries(t, f.s, f.env.Repo)
	if len(afterStart)-before != 1 || afterStart[len(afterStart)-1].Subject != f.matter {
		t.Fatalf("descendant cascade candidates = %+v", afterStart[before:])
	}
	before = len(afterStart)
	if _, err := Pause(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates"); err != nil {
		t.Fatal(err)
	}
	if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, "cascade-candidates/"+step.Locator); err != nil {
		t.Fatal(err)
	}
	if after := len(trackerEntries(t, f.s, f.env.Repo)); after != before {
		t.Fatalf("pause/resume/Step finish queued %d candidate(s)", after-before)
	}
}

func TestRebindQueuesDestinationAndEligibleSourceWithoutIntermediateState(t *testing.T) {
	f := newBatchFixture(t, "rebind-a")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateMatter(ctx, f.s, store.ActorHuman, f.env.Repo, "Rebind B", "rebind-b"); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{"rebind-a", "rebind-b"} {
		if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, locator); err != nil {
			t.Fatal(err)
		}
		if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, locator, "T-source"); err != nil {
			t.Fatal(err)
		}
	}
	before := len(trackerEntries(t, f.s, f.env.Repo))
	if _, err := Rebind(ctx, f.s, store.ActorHuman, f.env.Repo, "rebind-a", "T-source", "T-destination"); err != nil {
		t.Fatal(err)
	}
	entries := trackerEntries(t, f.s, f.env.Repo)[before:]
	if len(entries) != 2 {
		t.Fatalf("rebind queued %+v, want source and destination", entries)
	}
	got := map[string]store.TrackerDisposition{}
	for _, entry := range entries {
		got[entry.Ref] = dispositionOf(t, entry)
	}
	want := map[string]store.TrackerDisposition{"T-source": store.TrackerActive, "T-destination": store.TrackerActive}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebind dispositions = %v, want %v", got, want)
	}
	refs, err := f.s.TrackerReferences(ctx, f.matter)
	if err != nil || !reflect.DeepEqual(refs, []string{"T-destination"}) {
		t.Fatalf("rebound references = %v, err=%v", refs, err)
	}
}

func TestPlannedRebindQueuesNoStateCandidate(t *testing.T) {
	f := newBatchFixture(t, "planned-rebind")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "planned-rebind", "T-planned-source"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rebind(ctx, f.s, store.ActorHuman, f.env.Repo, "planned-rebind", "T-planned-source", "T-planned-destination"); err != nil {
		t.Fatal(err)
	}
	if entries := trackerEntries(t, f.s, f.env.Repo); len(entries) != 0 {
		t.Fatalf("Planned rebind queued candidates: %+v", entries)
	}
	refs, err := f.s.TrackerReferences(ctx, f.matter)
	if err != nil || !reflect.DeepEqual(refs, []string{"T-planned-destination"}) {
		t.Fatalf("Planned rebound references = %v, err=%v", refs, err)
	}
}

func TestReferenceRefusalsAndProjectionFailureAreCandidateFree(t *testing.T) {
	f := newBatchFixture(t, "refusal-candidates")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "refusal-candidates", "T-one"); err != nil {
		t.Fatal(err)
	}
	step, err := CreateStep(ctx, f.s, store.ActorHuman, f.env.Repo, "refusal-candidates", "Child")
	if err != nil {
		t.Fatal(err)
	}
	beforeEntries := len(trackerEntries(t, f.s, f.env.Repo))
	beforeEvents, _ := f.s.Events(ctx)
	for _, refused := range []func() error{
		func() error {
			_, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "refusal-candidates", "T-one")
			return err
		},
		func() error {
			_, err := Unbind(ctx, f.s, store.ActorHuman, f.env.Repo, "refusal-candidates", "T-missing")
			return err
		},
		func() error {
			_, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "refusal-candidates/"+step.Locator, "T-child")
			return err
		},
	} {
		if err := refused(); err == nil {
			t.Fatal("reference refusal unexpectedly succeeded")
		}
	}
	afterEvents, _ := f.s.Events(ctx)
	if len(afterEvents) != len(beforeEvents) || len(trackerEntries(t, f.s, f.env.Repo)) != beforeEntries {
		t.Fatal("reference refusal appended an event or candidate")
	}

	beforeNode, _ := f.s.Node(ctx, f.matter)
	_, err = f.s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: f.env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeMatterStarted, Subject: f.matter, Payload: store.Transition{From: store.Planned, To: store.InProgress, TrackerPushLevel: "invalid"}}}, nil
	})
	if err == nil {
		t.Fatal("invalid event snapshot unexpectedly projected")
	}
	afterNode, _ := f.s.Node(ctx, f.matter)
	afterEvents, _ = f.s.Events(ctx)
	if afterNode.Lifecycle != beforeNode.Lifecycle || len(afterEvents) != len(beforeEvents) || len(trackerEntries(t, f.s, f.env.Repo)) != beforeEntries {
		t.Fatal("projection failure did not roll back event, lifecycle, and candidate together")
	}
}

func TestCandidateIdentitySurvivesReopen(t *testing.T) {
	f := newBatchFixture(t, "reopen-candidates")
	ctx := context.Background()
	if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, "reopen-candidates", "T-reopen"); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, "reopen-candidates"); err != nil {
		t.Fatal(err)
	}
	before := trackerEntries(t, f.s, f.env.Repo)
	fixturePath := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after := trackerEntries(t, reopened, f.env.Repo)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("candidates changed across reopen:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestBindQueuesCurrentStateForEveryMatterDisposition(t *testing.T) {
	tests := []struct {
		name string
		move func(context.Context, *store.Store, string, string) error
		want store.TrackerDisposition
	}{
		{name: "planned"},
		{name: "active", want: store.TrackerActive, move: func(ctx context.Context, s *store.Store, repo, locator string) error {
			_, err := Start(ctx, s, store.ActorHuman, repo, locator)
			return err
		}},
		{name: "canceled", want: store.TrackerCanceled, move: func(ctx context.Context, s *store.Store, repo, locator string) error {
			if _, err := Start(ctx, s, store.ActorHuman, repo, locator); err != nil {
				return err
			}
			_, err := Cancel(ctx, s, store.ActorHuman, repo, locator, "")
			return err
		}},
		{name: "sealed", want: store.TrackerCompleted, move: func(ctx context.Context, s *store.Store, repo, locator string) error {
			if _, err := Start(ctx, s, store.ActorHuman, repo, locator); err != nil {
				return err
			}
			_, err := Finish(ctx, s, store.ActorHuman, repo, locator)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator := "bind-" + test.name
			f := newBatchFixture(t, locator)
			ctx := context.Background()
			if test.move != nil {
				if err := test.move(ctx, f.s, f.env.Repo, locator); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
				t.Fatal(err)
			}
			if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, locator, "T-"+test.name); err != nil {
				t.Fatal(err)
			}
			entries := trackerEntries(t, f.s, f.env.Repo)
			if test.want == "" {
				if len(entries) != 0 {
					t.Fatalf("bind candidate = %+v, want none", entries)
				}
				return
			}
			if len(entries) != 1 || dispositionOf(t, entries[0]) != test.want {
				t.Fatalf("bind candidate = %+v, want %s", entries, test.want)
			}
		})
	}
}

func TestSharedReferenceTerminalAggregation(t *testing.T) {
	for _, test := range []struct {
		name       string
		cancelLast bool
	}{
		{name: "all-sealed"},
		{name: "mixed-sealed-canceled", cancelLast: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newBatchFixture(t, test.name+"-a")
			ctx := context.Background()
			b, err := CreateMatter(ctx, f.s, store.ActorHuman, f.env.Repo, test.name+" B", test.name+"-b")
			if err != nil {
				t.Fatal(err)
			}
			for i, locator := range []string{test.name + "-a", b.Locator} {
				if _, err := Start(ctx, f.s, store.ActorHuman, f.env.Repo, locator); err != nil {
					t.Fatal(err)
				}
				if test.cancelLast && i == 1 {
					if _, err := Cancel(ctx, f.s, store.ActorHuman, f.env.Repo, locator, ""); err != nil {
						t.Fatal(err)
					}
				} else if _, err := Finish(ctx, f.s, store.ActorHuman, f.env.Repo, locator); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.SetTrackerPushLevel(ctx, f.env.Repo, "boundary"); err != nil {
				t.Fatal(err)
			}
			for _, locator := range []string{test.name + "-a", b.Locator} {
				if _, err := Bind(ctx, f.s, store.ActorHuman, f.env.Repo, locator, "T-terminal"); err != nil {
					t.Fatal(err)
				}
			}
			entries := trackerEntries(t, f.s, f.env.Repo)
			if got := dispositionOf(t, entries[len(entries)-1]); got != store.TrackerCompleted {
				t.Fatalf("terminal aggregate = %s, want completed", got)
			}
		})
	}
}
