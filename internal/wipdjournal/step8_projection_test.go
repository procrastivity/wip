package wipdjournal

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/wipdwire"
)

func step8ProjectionMatters(t *testing.T) []wipdwire.EventRecord {
	t.Helper()
	return []wipdwire.EventRecord{
		step8InstallEvent(t, 100, "matter.created", step8A, testRepoID, map[string]any{"id": step8A, "title": "A", "locator": "a"}, nil),
		step8InstallEvent(t, 101, "matter.created", step8B, step8B, map[string]any{"id": step8B, "title": "B", "locator": "b"}, nil),
	}
}

func TestStep8DependencyDomainDirectionCrossRepoAndEndpointTombstones(t *testing.T) {
	const step = "00000000000000000000000043"
	const contextC = "00000000000000000000000044"
	const contextD = "00000000000000000000000045"
	records := step8ProjectionMatters(t)
	records = append(records,
		step8InstallEvent(t, 102, "step.created", step, testRepoID, map[string]any{"title": "Leaf", "locator": "step-01", "parent": step8A, "sort_key": uint64(1000)}, nil),
		// Context C/D have no nodes. They are authority-validated members,
		// not endpoint scope or graph owners.
		step8InstallEvent(t, 103, "dependency.added", step8A, contextC, map[string]any{"edge": step8Edge, "blocker": step8B}, nil),
		step8InstallEvent(t, 104, "dependency.added", step, contextD, map[string]any{"edge": contextD, "blocker": step8B}, nil),
		step8InstallEvent(t, 105, "dependency.removed", step8A, contextD, map[string]any{"edge": step8Edge, "blocker": step8B}, nil),
		step8InstallEvent(t, 106, "step.removed", step, testRepoID, map[string]any{"reason": "obsolete"}, nil),
	)
	p, err := FoldStep8Projection(records, testDomainID)
	if err != nil || p == nil || len(p.Dependencies) != 2 {
		t.Fatalf("cross Repo: %+v %v", p, err)
	}
	byID := map[string]DependencyProjection{}
	for _, e := range p.Dependencies {
		byID[e.EdgeID] = e
	}
	if e := byID[step8Edge]; e.BlockedID != step8A || e.BlockerID != step8B || e.RepoID != contextC || e.Live || e.TombstoneEventID != records[5].EventID || e.LastEventID != records[5].EventID {
		t.Fatalf("remove changed direction/identity/context: %+v", e)
	}
	if e := byID[contextD]; e.Live || e.TombstoneEventID != "" || e.LastEventID != records[4].EventID {
		t.Fatalf("endpoint tombstone rewrote edge history: %+v", e)
	}
	for _, test := range []struct {
		name   string
		record wipdwire.EventRecord
	}{
		{"cycle", step8InstallEvent(t, 107, "dependency.added", step8B, contextD, map[string]any{"edge": contextD, "blocker": step8A}, nil)},
		{"duplicate", step8InstallEvent(t, 107, "dependency.added", step8A, contextD, map[string]any{"edge": contextD, "blocker": step8B}, nil)},
		{"future endpoint", step8InstallEvent(t, 107, "dependency.added", step8A, contextD, map[string]any{"edge": contextD, "blocker": contextC}, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := FoldStep8Projection(append(append([]wipdwire.EventRecord{}, records[:4]...), test.record), testDomainID); !errors.Is(err, ErrInvalidTransfer) {
				t.Fatalf("invalid graph accepted: %v", err)
			}
		})
	}
	if _, err := FoldStep8Projection(append(append([]wipdwire.EventRecord{}, records...), step8InstallEvent(t, 107, "dependency.added", step, testRepoID, map[string]any{"edge": contextC, "blocker": step8B}, nil)), testDomainID); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("tombstoned endpoint accepted: %v", err)
	}
	// Foreign-domain endpoint history is not usable in this domain, even with
	// the same Repo or node spelling.
	foreign := append([]wipdwire.EventRecord{}, records[:4]...)
	f, _ := wipdwire.DecodeCanonicalMap(foreign[1].Record, "schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	f["domain_id"] = contextC
	foreign[1].Record, _ = wipdwire.EncodeCanonical(f)
	if _, err := FoldStep8Projection(foreign, testDomainID); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("foreign endpoint history accepted: %v", err)
	}
}

func TestStep8ReferenceBindLifecycleRefreshReopenAndSharedRepo(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	j := openInstallTestJournal(t, root)
	defer func() { _ = j.Close() }()
	records := step8ProjectionMatters(t)
	records = append(records,
		step8InstallEvent(t, 102, "reference.added", step8A, testRepoID, map[string]any{"ref": "SHARED", "tracker_push_level": "off"}, nil),
		step8InstallEvent(t, 103, "reference.added", step8B, step8B, map[string]any{"ref": "SHARED", "tracker_push_level": "off"}, nil),
	)
	s, err := j.InstallPull(ctx, mustStep8Snapshot(t, j).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
	if err != nil {
		t.Fatal(err)
	}
	for i, phase := range []string{"planned", "in-progress", "canceled"} {
		if i > 0 {
			kind, from := "matter.started", "planned"
			if i == 2 {
				kind, from = "matter.canceled", "in-progress"
			}
			e := step8InstallEvent(t, 103+i, kind, step8A, testRepoID, map[string]any{"from": from, "to": phase}, nil)
			records = append(records, e)
			s, err = j.InstallPull(ctx, s.Expectation(), verifiedInstallTestTransfer(t, s.Anchor, []wipdwire.EventRecord{e}))
			if err != nil {
				t.Fatal(err)
			}
		}
		want := ""
		if i > 0 {
			want = "active"
		} // Planned B prevents cancellation of the shared aggregate
		if p := s.Step8Projection; p == nil || len(p.Aggregates) != 1 || p.Aggregates[0].Members != 2 || p.Aggregates[0].Disposition != want || len(p.Candidates) != 0 {
			t.Fatalf("bind/%s stale aggregate: %+v", phase, p)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		j = openInstallTestJournal(t, root)
		if reopened := mustStep8Snapshot(t, j); !reflect.DeepEqual(reopened, s) {
			t.Fatalf("%s reopen stale", phase)
		}
	}
	// Rebinding canceled A changes the shared side and the destination side
	// differently; final unbinding has no target to emit for.
	tail := []wipdwire.EventRecord{
		step8InstallEvent(t, 106, "config.set", testRepoID, testRepoID, map[string]any{"key": "tracker.backend", "value": "tracker"}, nil),
		step8InstallEvent(t, 107, "reference.rebound", step8A, testRepoID, map[string]any{"from": "SHARED", "to": "SOLO", "tracker_push_level": "boundary"}, nil),
		step8InstallEvent(t, 108, "reference.removed", step8A, testRepoID, map[string]any{"ref": "SOLO", "tracker_push_level": "boundary"}, nil),
		step8InstallEvent(t, 109, "config.set", testRepoID, testRepoID, map[string]any{"key": "tracker.push-level", "value": "off"}, nil),
	}
	s, err = j.InstallPull(ctx, s.Expectation(), verifiedInstallTestTransfer(t, s.Anchor, tail))
	if err != nil {
		t.Fatal(err)
	}
	p := s.Step8Projection
	if len(p.Candidates) != 1 || p.Candidates[0].Reference != "SOLO" || p.Candidates[0].Payload != `{"disposition":"canceled"}` || len(p.Aggregates) != 1 || p.Aggregates[0].Reference != "SHARED" || p.Aggregates[0].Disposition != "" || p.Aggregates[0].Members != 1 {
		t.Fatalf("asymmetric rebind/final unbind: %+v", p)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openInstallTestJournal(t, root)
	if !reflect.DeepEqual(mustStep8Snapshot(t, j), s) {
		t.Fatal("config change rewrote original candidate snapshot")
	}
	// Reference mutations remain Repo-owned, unlike dependencies.
	wrong := step8InstallEvent(t, 110, "reference.added", step8B, testRepoID, map[string]any{"ref": "BAD", "tracker_push_level": "off"}, nil)
	if _, err := j.InstallPull(ctx, s.Expectation(), verifiedInstallTestTransfer(t, s.Anchor, []wipdwire.EventRecord{wrong})); !errors.Is(err, ErrInvalidTransfer) {
		t.Fatalf("reference cross-Repo target accepted: %v", err)
	}
	if !reflect.DeepEqual(mustStep8Snapshot(t, j), s) {
		t.Fatal("failed pull left partial state")
	}
}

func TestStep8BoundaryCandidateLifecycleRefreshInstallAndReopen(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	j := openInstallTestJournal(t, root)
	defer func() { _ = j.Close() }()

	// Until the Step 17 authority config operation exists, authenticated M5
	// process fixtures cannot enable tracker candidates. A valid config.set
	// history record is the strongest in-scope Environment projection proof.
	records := step8ProjectionMatters(t)[:1]
	config := step8InstallEvent(t, 102, "config.set", testRepoID, testRepoID, map[string]any{"key": "tracker.push-level", "value": "boundary"}, nil)
	bind := step8InstallEvent(t, 103, "reference.added", step8A, testRepoID, map[string]any{"ref": "TRACKER-42", "tracker_push_level": "boundary"}, nil)
	records = append(records, config, bind)
	s, err := j.InstallPull(ctx, mustStep8Snapshot(t, j).Expectation(), verifiedInstallTestTransfer(t, emptyTransferAnchor(), records))
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Step8Projection; p == nil || len(p.Candidates) != 0 || len(p.Aggregates) != 1 || p.Aggregates[0] != (ReferenceAggregate{DomainID: testDomainID, Reference: "TRACKER-42", Members: 1}) || len(p.ConfigHistory) != 1 || p.ConfigHistory[0] != (ConfigHistoryEntry{RepoID: testRepoID, Key: "tracker.push-level", Value: "boundary", EventID: config.EventID}) {
		t.Fatalf("bound planned reference/config history: %+v", p)
	}

	started := step8InstallEvent(t, 104, "matter.started", step8A, testRepoID, map[string]any{"from": "planned", "to": "in-progress", "tracker_push_level": "boundary"}, nil)
	s, err = j.InstallPull(ctx, s.Expectation(), verifiedInstallTestTransfer(t, s.Anchor, []wipdwire.EventRecord{started}))
	if err != nil {
		t.Fatal(err)
	}
	wantStarted := []TrackerCandidate{{
		DomainID: testDomainID, ID: "3P4ZFR5N60PAX3JMACHMGCD4RS", RepoID: testRepoID,
		Kind: "state", SubjectID: step8A, Reference: "TRACKER-42",
		Key: "tracker:00000000000000000000000104:state:TRACKER-42", Payload: `{"disposition":"active"}`,
		EventID: "00000000000000000000000104",
	}}
	if p := s.Step8Projection; p == nil || !reflect.DeepEqual(p.Candidates, wantStarted) || len(p.Aggregates) != 1 || p.Aggregates[0] != (ReferenceAggregate{DomainID: testDomainID, Reference: "TRACKER-42", Disposition: "active", Members: 1}) {
		t.Fatalf("start candidate/aggregate refresh: %+v", p)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openInstallTestJournal(t, root)
	if reopened := mustStep8Snapshot(t, j); !reflect.DeepEqual(reopened, s) {
		t.Fatal("started candidate/aggregate changed after reopen")
	}

	canceled := step8InstallEvent(t, 105, "matter.canceled", step8A, testRepoID, map[string]any{"from": "in-progress", "to": "canceled", "tracker_push_level": "boundary"}, nil)
	s, err = j.InstallPull(ctx, s.Expectation(), verifiedInstallTestTransfer(t, s.Anchor, []wipdwire.EventRecord{canceled}))
	if err != nil {
		t.Fatal(err)
	}
	wantFinal := []TrackerCandidate{
		{
			DomainID: testDomainID, ID: "1EH3SVQNM4MFXJ64P18TDKWAKJ", RepoID: testRepoID,
			Kind: "state", SubjectID: step8A, Reference: "TRACKER-42",
			Key: "tracker:00000000000000000000000105:state:TRACKER-42", Payload: `{"disposition":"canceled"}`,
			EventID: "00000000000000000000000105",
		},
		wantStarted[0],
	}
	if p := s.Step8Projection; p == nil || !reflect.DeepEqual(p.Candidates, wantFinal) || len(p.Aggregates) != 1 || p.Aggregates[0] != (ReferenceAggregate{DomainID: testDomainID, Reference: "TRACKER-42", Disposition: "canceled", Members: 1}) {
		t.Fatalf("cancel candidate/aggregate refresh: %+v", p)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openInstallTestJournal(t, root)
	if reopened := mustStep8Snapshot(t, j); !reflect.DeepEqual(reopened, s) {
		t.Fatal("canceled candidates/aggregate changed after reopen")
	}
}

func TestStep8NarratedStageGateBoundaryAndPolicyFallback(t *testing.T) {
	const stage = "00000000000000000000000043"
	for _, level := range []string{"off", "boundary", "narrated"} {
		t.Run(level, func(t *testing.T) {
			records := step8ProjectionMatters(t)[:1]
			records = append(records,
				step8InstallEvent(t, 101, "config.set", testRepoID, testRepoID, map[string]any{"key": "tracker.push-level", "value": level}, nil),
				step8InstallEvent(t, 102, "reference.added", step8A, testRepoID, map[string]any{"ref": "TRACKER", "tracker_push_level": level}, nil),
				step8InstallEvent(t, 103, "stage.created", stage, testRepoID, map[string]any{"matter_id": step8A, "title": "Review Stage", "locator": "review", "sort_key": uint64(1000)}, nil),
				step8InstallEvent(t, 104, "gate.declared", testRepoID, testRepoID, map[string]any{"gate": "reviewed", "scale": "stage"}, nil),
				step8InstallEvent(t, 105, "matter.started", step8A, testRepoID, map[string]any{"from": "planned", "to": "in-progress", "tracker_push_level": level}, nil),
				step8InstallEvent(t, 106, "stage.started", stage, testRepoID, map[string]any{"from": "planned", "to": "in-progress"}, nil),
				step8InstallEvent(t, 107, "stage.finished", stage, testRepoID, map[string]any{"from": "in-progress", "to": "done", "tracker_push_level": level}, nil),
			)
			p, err := FoldStep8Projection(records, testDomainID)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range p.Candidates {
				if c.Kind == "comment" {
					t.Fatal("open gate emitted narrated comment")
				}
			}
			records = append(records, step8InstallEvent(t, 108, "gate.closed", stage, testRepoID, map[string]any{"gate": "reviewed", "scale": "stage", "tracker_push_level": level}, nil))
			p, err = FoldStep8Projection(records, testDomainID)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if level == "boundary" {
				want = 1
			}
			if level == "narrated" {
				want = 2
			}
			if len(p.Candidates) != want {
				t.Fatalf("candidate count=%d want=%d", len(p.Candidates), want)
			}
			if level == "narrated" {
				found := false
				for _, c := range p.Candidates {
					if c.Kind == "comment" {
						found = c.SubjectID == step8A && c.EventID == records[len(records)-1].EventID && c.Payload == `{"stage":"00000000000000000000000043","title":"Review Stage","action":"closed"}`
					}
				}
				if !found {
					t.Fatal("missing exact narrated candidate")
				}
			}
		})
	}
}
