//go:build sharedstoreexperiment

package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

func followTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func followClock(t *testing.T, s *store.Store, at *time.Time) *store.Store {
	t.Helper()
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenWithClock(path, func() time.Time { return *at })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func followPreview(t *testing.T, s *store.Store, origin string) readsurface.ExperimentActivityPreview {
	t.Helper()
	p, err := readsurface.PreviewExperimentActivity(labCtx, s, origin, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func followHistory(t *testing.T, s *store.Store, group string, count int) []store.ExperimentEvaluation {
	t.Helper()
	h, err := s.ExperimentDecisionHistory(labCtx, group)
	if err != nil || len(h) != count {
		t.Fatalf("history=%+v error=%v; want %d records", h, err, count)
	}
	return h
}

func followRecheck(t *testing.T, s *store.Store, source store.ExperimentSource, want string) store.ExperimentDecision {
	t.Helper()
	d, err := s.ExperimentRecheck(labCtx, source.Actions[0].Event.ID)
	if err != nil || d.Disposition != want {
		t.Fatalf("recheck=%+v error=%v; want %s", d, err, want)
	}
	if !reflect.DeepEqual(d.Source, source) {
		t.Fatal("recheck changed immutable input")
	}
	return d
}

func followCreate(t *testing.T, p labPair, s *store.Store, name string) string {
	t.Helper()
	if _, err := writesurface.CreateMatter(labCtx, s, store.ActorHuman, p.repo, name, name); err != nil {
		t.Fatal(err)
	}
	events := labEvents(t, s)
	return events[len(events)-1].ID
}

func followLogPreview(t *testing.T, p readsurface.ExperimentActivityPreview, since time.Time) {
	t.Helper()
	var sample readsurface.ExperimentActivityPreview
	for _, a := range p.Activities {
		if !a.Event.OccurredAt.Before(since) {
			sample.Activities = append(sample.Activities, a)
		}
	}
	for _, period := range p.Periods {
		if !period.FirstReportedAt.Before(since) {
			sample.Periods = append(sample.Periods, period)
		}
	}
	raw, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual opt-in preview (source-time window):\n%s", raw)
}

func TestSharedStoreFollowupLateTimeAndSessionBridge(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		t.Run(fmt.Sprint("bridge=", bridge), func(t *testing.T) {
			p := newLabPair(t)
			sourceTime := followTime("2026-12-01T09:00:00Z")
			p.hybrid = followClock(t, p.hybrid, &sourceTime)
			h := labStart(t, p, p.hybrid, "Hybrid", "shared")
			received := followTime("2026-12-02T15:00:00Z")
			if bridge {
				received = followTime("2026-12-02T09:00:00Z")
			}
			p.fluke = followClock(t, p.fluke, &received)
			var first, second string
			if bridge {
				first = followCreate(t, p, p.fluke, "first-work")
				received = followTime("2026-12-02T09:30:00Z")
			}
			d := labImport(t, p.fluke, h, "integrated")
			receiptAt := received
			if bridge {
				received = followTime("2026-12-02T10:00:00Z")
				second = followCreate(t, p, p.fluke, "second-work")
			}
			preview := followPreview(t, p.fluke, "Fluke")
			found := 0
			for _, a := range preview.Activities {
				if a.Event.ID == d.Effects[0] {
					t.Fatal("integration effect became work activity")
				}
				if a.Event.ID == h.Actions[0].Event.ID {
					found++
					if a.Origin != "Hybrid" || a.Event.Actor != store.ActorHuman || !a.Event.OccurredAt.Equal(sourceTime) || a.ReceivedAt == nil || !a.ReceivedAt.Equal(receiptAt) || !a.Effects[0].RecordedAt.Equal(receiptAt) {
						t.Fatalf("source and receipt clocks conflated: %+v", a)
					}
				}
			}
			if found != 1 {
				t.Fatal("original source missing or duplicated")
			}
			var localPeriods []readsurface.ExperimentPeriod
			for _, period := range preview.Periods {
				if period.Origin == "Fluke" && !period.FirstReportedAt.Before(followTime("2026-12-02T00:00:00Z")) {
					localPeriods = append(localPeriods, period)
				}
			}
			if !bridge && len(localPeriods) != 0 {
				t.Fatal("late receipt created day-two work")
			}
			if bridge && (len(localPeriods) != 2 || !reflect.DeepEqual(localPeriods[0].ActivityIDs, []string{first}) || !reflect.DeepEqual(localPeriods[1].ActivityIDs, []string{second})) {
				t.Fatalf("receipt bridged real work: %+v", localPeriods)
			}
			labImport(t, p.fluke, h, "integrated")
			followRecheck(t, p.fluke, h, "integrated")
			path := p.fluke.Path()
			_ = p.fluke.Close()
			p.fluke = labOpen(t, path)
			if !reflect.DeepEqual(preview, followPreview(t, p.fluke, "Fluke")) {
				t.Fatal("retry/recheck/reopen changed preview")
			}
			unknown := followPreview(t, p.fluke, "")
			for _, period := range unknown.Periods {
				if period.Origin != "Hybrid" {
					t.Fatal("unknown native origin got an invented session")
				}
			}
			followLogPreview(t, preview, sourceTime)
		})
	}
	for _, gap := range []time.Duration{30*time.Minute - time.Millisecond, 30 * time.Minute} {
		t.Run(gap.String(), func(t *testing.T) {
			p := newLabPair(t)
			at := followTime("2026-12-02T09:00:00Z")
			p.fluke = followClock(t, p.fluke, &at)
			first := followCreate(t, p, p.fluke, "first-boundary")
			at = at.Add(gap)
			second := followCreate(t, p, p.fluke, "second-boundary")
			periods := followPreview(t, p.fluke, "Fluke").Periods
			var selected []readsurface.ExperimentPeriod
			for _, p := range periods {
				for _, id := range p.ActivityIDs {
					if id == first {
						selected = append(selected, p)
						break
					}
					if id == second {
						selected = append(selected, p)
						break
					}
				}
			}
			want := 1
			if gap == 30*time.Minute {
				want = 2
			}
			if len(selected) != want {
				t.Fatalf("gap %s produced %d periods, want %d", gap, len(selected), want)
			}
		})
	}
}

func TestSharedStoreFollowupAdministrativeClose(t *testing.T) {
	for _, reason := range []store.CloseReason{store.CloseSuperseded, store.CloseReaped} {
		t.Run(string(reason), func(t *testing.T) {
			p := newLabPair(t)
			at := followTime("2026-12-02T08:00:00Z")
			p.fluke = followClock(t, p.fluke, &at)
			events := labEvents(t, p.fluke)
			env := store.Env{Repo: p.repo}
			for _, e := range events {
				if e.Type == store.TypeCloneAttached {
					env.Clone = e.Subject
				}
				if e.Type == store.TypeWorktreeAttached {
					env.Worktree = e.Subject
				}
			}
			var dispatch string
			if _, err := p.fluke.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: env}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
				dispatch = tx.NewID()
				return []store.Draft{{Type: store.TypeDispatchOpened, Subject: dispatch}}, nil
			}); err != nil {
				t.Fatal(err)
			}
			at = followTime("2026-12-02T09:00:00Z")
			first := followCreate(t, p, p.fluke, "before-close")
			at = followTime("2026-12-02T09:30:00Z")
			closed, err := p.fluke.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: env}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
				return []store.Draft{{Type: store.TypeDispatchClosed, Subject: dispatch, Payload: store.DispatchClosed{Reason: reason}}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			at = followTime("2026-12-02T10:00:00Z")
			second := followCreate(t, p, p.fluke, "after-close")
			preview := followPreview(t, p.fluke, "Fluke")
			found := false
			for _, a := range preview.Activities {
				if a.Event.ID == closed[0].ID {
					found = true
				}
			}
			if !found {
				t.Fatal("discounted close disappeared from combined activity")
			}
			var periods []readsurface.ExperimentPeriod
			for _, period := range preview.Periods {
				if !period.FirstReportedAt.Before(followTime("2026-12-02T09:00:00Z")) {
					periods = append(periods, period)
				}
			}
			if len(periods) != 2 || !reflect.DeepEqual(periods[0].ActivityIDs, []string{first}) || !reflect.DeepEqual(periods[1].ActivityIDs, []string{second}) {
				t.Fatalf("administrative close bridged source periods: %+v", periods)
			}
		})
	}
}

func TestSharedStoreFollowupOverlapAndClockDisagreement(t *testing.T) {
	for _, equal := range []bool{false, true} {
		t.Run(fmt.Sprint("equal=", equal), func(t *testing.T) {
			p := newLabPair(t)
			hTime, fTime := followTime("2026-12-01T09:00:00Z"), followTime("2026-12-03T09:00:00Z")
			if equal {
				fTime = hTime
			}
			p.hybrid = followClock(t, p.hybrid, &hTime)
			p.fluke = followClock(t, p.fluke, &fTime)
			h := labStart(t, p, p.hybrid, "Hybrid", "shared")
			f := labStart(t, p, p.fluke, "Fluke", "shared")
			// Reverse receipt orders, including a future source arriving at Hybrid.
			labImport(t, p.hybrid, f, "integrated")
			labImport(t, p.hybrid, h, "integrated")
			labImport(t, p.fluke, h, "integrated")
			labImport(t, p.fluke, f, "integrated")
			for _, local := range []struct {
				s    *store.Store
				host string
			}{{p.hybrid, "Hybrid"}, {p.fluke, "Fluke"}} {
				preview := followPreview(t, local.s, local.host)
				var starts []store.ExperimentActivity
				for _, a := range preview.Activities {
					if a.Event.Type == store.TypeMatterStarted {
						starts = append(starts, a)
					}
				}
				if len(starts) != 2 || starts[0].Event.ID == starts[1].Event.ID {
					t.Fatal("self echo duplicated or independent overlap collapsed")
				}
				wantFirst := "Hybrid"
				if equal {
					wantFirst = "Fluke"
				}
				if starts[0].Origin != wantFirst || starts[0].Origin == starts[1].Origin {
					t.Fatalf("nondeterministic source display: %+v", starts)
				}
				if starts[0].Effects[0].ID != starts[1].Effects[0].ID {
					t.Fatal("independent activities lost equivalent effect mapping")
				}
				for _, period := range preview.Periods {
					for _, id := range period.ActivityIDs {
						for _, a := range starts {
							if id == a.Event.ID && period.Origin != a.Origin {
								t.Fatal("period combined hosts")
							}
						}
					}
				}
				count := len(labEvents(t, local.s))
				labImport(t, local.s, f, "integrated")
				followRecheck(t, local.s, h, "integrated")
				path := local.s.Path()
				_ = local.s.Close()
				reopened := labOpen(t, path)
				if len(labEvents(t, reopened)) != count || !reflect.DeepEqual(preview, followPreview(t, reopened, local.host)) {
					t.Fatal("retry/reopen changed overlapping activity")
				}
			}
		})
	}
}

func followDependency(t *testing.T, p labPair) (store.ExperimentSource, store.ExperimentSource) {
	t.Helper()
	ancestor := labStart(t, p, p.hybrid, "Hybrid", "shared/stage")
	child := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
	if len(ancestor.Actions) != 2 || len(child.Actions) != 1 || len(child.Actions[0].Ancestors) != 2 {
		t.Fatal("fixture is not a real separate ancestor start and child-only start")
	}
	return ancestor, child
}

func TestSharedStoreFollowupDependencyArrival(t *testing.T) {
	for _, represented := range []bool{false, true} {
		t.Run(fmt.Sprint("already-represented=", represented), func(t *testing.T) {
			p := newLabPair(t)
			ancestor, child := followDependency(t, p)
			if represented {
				labStart(t, p, p.fluke, "Fluke", "shared/stage")
			}
			at := followTime("2026-12-02T15:00:00Z")
			p.fluke = followClock(t, p.fluke, &at)
			pending := labImport(t, p.fluke, child, "pending")
			receivedAt := at
			before := len(labEvents(t, p.fluke))
			for i := 0; i < 3; i++ {
				at = at.Add(time.Minute)
				labImport(t, p.fluke, child, "pending")
				followRecheck(t, p.fluke, child, "pending")
			}
			followHistory(t, p.fluke, child.Actions[0].Event.ID, 1)
			at = followTime("2026-12-02T16:00:00Z")
			labImport(t, p.fluke, ancestor, "integrated")
			afterAncestor := len(labEvents(t, p.fluke))
			wantAncestorEffects := 2
			if represented {
				wantAncestorEffects = 0
			}
			if afterAncestor != before+wantAncestorEffects {
				t.Fatal("ancestor delivery effects differ from expected")
			}
			// Redelivery remains inert even now that evidence could unblock it.
			labImport(t, p.fluke, child, "pending")
			if len(labEvents(t, p.fluke)) != afterAncestor {
				t.Fatal("delivery secretly reconsidered pending work")
			}
			at = followTime("2026-12-02T17:00:00Z")
			integrated := followRecheck(t, p.fluke, child, "integrated")
			history := followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
			if history[0].Decision.Reason != pending.Reason || history[1].PreviousDisposition != "pending" || history[1].Kind != "recheck" || reflect.DeepEqual(history[0].Evidence, history[1].Evidence) {
				t.Fatal("decision/evidence history lost transition")
			}
			if len(integrated.Effects) != 1 || len(labEvents(t, p.fluke)) != afterAncestor+1 {
				t.Fatal("recheck did not apply exactly the missing child")
			}
			preview := followPreview(t, p.fluke, "Fluke")
			for _, activity := range preview.Activities {
				if activity.Event.ID == integrated.Effects[0] {
					t.Fatal("recheck effect became work activity")
				}
				if activity.Event.ID == child.Actions[0].Event.ID && (activity.ReceivedAt == nil || !activity.ReceivedAt.Equal(receivedAt) || activity.LastRecordedEvaluationAt == nil || !activity.LastRecordedEvaluationAt.Equal(at) || !activity.Effects[0].RecordedAt.Equal(at)) {
					t.Fatal("recheck diagnostics lost original receipt or actual effect time")
				}
			}
			for _, period := range preview.Periods {
				if !period.FirstReportedAt.Before(receivedAt) {
					t.Fatal("dependency import/recheck created reported work")
				}
			}
			labState(t, p.fluke, p.step1, store.InProgress)
			if _, err := writesurface.Pause(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/step-01"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(integrated, followRecheck(t, p.fluke, child, "integrated")) {
				t.Fatal("integrated mapping changed after later local work")
			}
			followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
			labRebuild(t, p.fluke)
			t.Logf("before: %s (%s), native=%d; ancestor arrival native=%d; explicit recheck: %s effects=%v, native=%d; history pending -> integrated", pending.Disposition, pending.Reason, before, afterAncestor, integrated.Disposition, integrated.Effects, afterAncestor+1)
			raw, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("actual decision history: %s", raw)
		})
	}
}

func TestSharedStoreFollowupEvidenceOnlyHistory(t *testing.T) {
	p := newLabPair(t)
	_, child := followDependency(t, p)
	before := labImport(t, p.fluke, child, "pending")
	if _, err := p.fluke.ExperimentAcknowledge(labCtx, child.Actions[0].Event.ID); err == nil {
		t.Fatal("pending was silently acknowledged as resolved")
	}
	// Relevant evidence can change while the disposition and reason do not.
	if _, err := writesurface.AppendFinding(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage", []byte("local observation, still Planned")); err != nil {
		t.Fatal(err)
	}
	after := followRecheck(t, p.fluke, child, "pending")
	history := followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
	if !reflect.DeepEqual(before, after) || reflect.DeepEqual(history[0].Evidence, history[1].Evidence) {
		t.Fatal("meaningful evidence-only change was lost")
	}
	for i := 0; i < 3; i++ {
		followRecheck(t, p.fluke, child, "pending")
	}
	followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
}

func TestSharedStoreFollowupNewIncompatibleEvidence(t *testing.T) {
	for _, change := range []string{"done", "pause-resume"} {
		t.Run(change, func(t *testing.T) {
			p := newLabPair(t)
			_, child := followDependency(t, p)
			labImport(t, p.fluke, child, "pending")
			labStart(t, p, p.fluke, "Fluke", "shared/stage")
			if change == "done" {
				if _, err := writesurface.Finish(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := writesurface.Pause(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage"); err != nil {
					t.Fatal(err)
				}
				if _, err := writesurface.Resume(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage"); err != nil {
					t.Fatal(err)
				}
			}
			before := len(labEvents(t, p.fluke))
			d := followRecheck(t, p.fluke, child, "conflict")
			h := followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
			if h[0].Decision.Disposition != "pending" || h[1].Decision.Disposition != "conflict" || len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
				t.Fatal("worse evidence did not preserve whole-group conflict")
			}
			labState(t, p.fluke, p.step1, store.Planned)
			cli := p
			cli.db = p.fluke.Path()
			labCLICommand(t, cli, true, "plumbing", "matter", "create", "--title", "Unrelated", "--json")
			followRecheck(t, p.fluke, child, "conflict")
			followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
		})
	}
}

func TestSharedStoreFollowupHardConflictsAndAcknowledgement(t *testing.T) {
	for _, kind := range []string{"done", "pause-resume", "content", "policy"} {
		t.Run(kind, func(t *testing.T) {
			p := newLabPair(t)
			h := labStart(t, p, p.hybrid, "Hybrid", "shared")
			labStart(t, p, p.fluke, "Fluke", "shared")
			switch kind {
			case "done":
				if _, err := writesurface.Finish(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
					t.Fatal(err)
				}
			case "pause-resume":
				if _, err := writesurface.Pause(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
					t.Fatal(err)
				}
				if _, err := writesurface.Resume(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
					t.Fatal(err)
				}
			case "policy":
				var tr store.Transition
				_ = json.Unmarshal(h.Actions[0].Event.Payload, &tr)
				tr.TrackerPushLevel = "narrated"
				h.Actions[0].Event.Payload, _ = json.Marshal(tr)
			case "content":
				write := func(s *store.Store, text string) []store.Event {
					events, err := s.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: p.repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
						d, err := tx.ContentDraft(p.matter, store.KindBrief, []byte(text))
						return []store.Draft{d}, err
					})
					if err != nil {
						t.Fatal(err)
					}
					return events
				}
				events := write(p.hybrid, "foreign brief")
				write(p.fluke, "accepted brief")
				var err error
				h, err = p.hybrid.ExperimentExport(labCtx, "Hybrid", events[0].ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			labImport(t, p.fluke, h, "conflict")
			before := labEvents(t, p.fluke)
			for i := 0; i < 3; i++ {
				followRecheck(t, p.fluke, h, "conflict")
			}
			group := h.Actions[0].Event.ID
			ack, err := p.fluke.ExperimentAcknowledge(labCtx, group)
			if err != nil {
				t.Fatal(err)
			}
			again, err := p.fluke.ExperimentAcknowledge(labCtx, group)
			if err != nil || !reflect.DeepEqual(ack, again) || ack.Statement != "retained; not applied here" {
				t.Fatal("acknowledgement not stable and local")
			}
			followRecheck(t, p.fluke, h, "conflict")
			followHistory(t, p.fluke, group, 1)
			if !reflect.DeepEqual(before, labEvents(t, p.fluke)) {
				t.Fatal("recheck or acknowledgement changed accepted work")
			}
			preview := followPreview(t, p.fluke, "Fluke")
			found := false
			for _, a := range preview.Activities {
				if a.Event.ID == group {
					found = true
					if a.Disposition != "conflict" || a.Acknowledgement == nil || len(a.Effects) != 0 {
						t.Fatalf("acknowledgement hid conflict: %+v", a)
					}
				}
			}
			if !found {
				t.Fatal("conflicted activity vanished")
			}
			if kind == "content" {
				data, err := p.fluke.Content(labCtx, p.matter, store.KindBrief)
				if err != nil || string(data) != "accepted brief" {
					t.Fatal("ack overwrote content")
				}
			}
			if kind == "pause-resume" || kind == "policy" {
				dependent := labStart(t, p, p.hybrid, "Hybrid", "shared/step-01")
				labImport(t, p.fluke, dependent, map[string]string{"pause-resume": "conflict", "policy": "pending"}[kind])
				followRecheck(t, p.fluke, dependent, map[string]string{"pause-resume": "conflict", "policy": "pending"}[kind])
				labState(t, p.fluke, p.step1, store.Planned)
			}
			labRebuild(t, p.fluke)
		})
	}
}

func TestSharedStoreFollowupRollbackAndReopen(t *testing.T) {
	p := newLabPair(t)
	ancestor, child := followDependency(t, p)
	labImport(t, p.fluke, child, "pending")
	labImport(t, p.fluke, ancestor, "integrated")
	group := child.Actions[0].Event.ID
	db, err := sql.Open("sqlite", p.fluke.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Evaluation history is written AFTER native effects; fail its final current
	// decision update, proving neither history nor effects can commit alone.
	_, err = db.Exec(`CREATE TRIGGER followup_fail BEFORE UPDATE OF decision ON experiment_sources WHEN NEW.id='` + group + `' BEGIN SELECT RAISE(ABORT,'injected recheck failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	before := labEvents(t, p.fluke)
	if _, err := p.fluke.ExperimentRecheck(labCtx, group); err == nil || !strings.Contains(err.Error(), "injected recheck failure") {
		t.Fatalf("expected injected recheck failure: %v", err)
	}
	if !reflect.DeepEqual(before, labEvents(t, p.fluke)) {
		t.Fatal("failed recheck committed native effects")
	}
	followHistory(t, p.fluke, group, 1)
	labImport(t, p.fluke, child, "pending")
	labState(t, p.fluke, p.step1, store.Planned)
	var mapped sql.NullString
	if err := db.QueryRow(`SELECT effect FROM experiment_actions WHERE id=?`, group).Scan(&mapped); err != nil || mapped.Valid {
		t.Fatalf("failed recheck left mapping: %v %v", mapped, err)
	}
	cli := p
	cli.db = p.fluke.Path()
	labCLICommand(t, cli, true, "plumbing", "matter", "create", "--title", "After failed recheck", "--json")
	if _, err := db.Exec(`DROP TRIGGER followup_fail`); err != nil {
		t.Fatal(err)
	}
	path := p.fluke.Path()
	_ = p.fluke.Close()
	p.fluke = labOpen(t, path)
	followRecheck(t, p.fluke, child, "integrated")
	followHistory(t, p.fluke, group, 2)
	for i := 0; i < 3; i++ {
		followRecheck(t, p.fluke, child, "integrated")
	}
	followHistory(t, p.fluke, group, 2)
	labRebuild(t, p.fluke)
	t.Log("failed recheck: pending/history=1/no child effect; reopen retry: integrated/history=2/one child effect; unrelated CLI write succeeded")
}

func TestSharedStoreFollowupRecheckProcess(t *testing.T) {
	group := os.Getenv("WIP_LAB_RECHECK_GROUP")
	if group == "" {
		return
	}
	s, err := store.Open(os.Getenv("WIP_LAB_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.ExperimentRecheck(labCtx, group); err != nil {
		t.Fatal(err)
	}
}

func TestSharedStoreFollowupRecheckRaces(t *testing.T) {
	for _, verb := range []string{"start", "pause"} {
		t.Run(verb, func(t *testing.T) {
			p := newLabPair(t)
			ancestor, child := followDependency(t, p)
			labImport(t, p.fluke, child, "pending")
			labImport(t, p.fluke, ancestor, "integrated")
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					var cmd *exec.Cmd
					if i < 2 {
						cmd = exec.Command(os.Args[0], "-test.run=^TestSharedStoreFollowupRecheckProcess$")
						cmd.Env = append(os.Environ(), "WIP_LAB_RECHECK_GROUP="+child.Actions[0].Event.ID, "WIP_LAB_DB="+p.fluke.Path())
					} else {
						target := "shared/step-01"
						if verb == "pause" {
							target = "shared/stage"
						}
						cmd = exec.Command(cliBinary(t), "plumbing", verb, target, "--json")
						cmd.Dir = p.dir
						cmd.Env = append(os.Environ(), "WIP_DB_PATH="+p.fluke.Path(), "WIP_AS_ROLE=")
					}
					out, err := cmd.CombinedOutput()
					if err != nil && (i < 2 || verb != "start" || !strings.Contains(string(out), "refusal.invalid-transition")) {
						t.Errorf("process %d: %v %s", i, err, out)
					}
				}(i)
			}
			wg.Wait()
			decisions, err := p.fluke.ExperimentActivities(labCtx)
			if err != nil {
				t.Fatal(err)
			}
			var result store.ExperimentDecision
			for _, d := range decisions {
				if d.Source.Actions[0].Event.ID == child.Actions[0].Event.ID {
					result = d
				}
			}
			if result.Disposition != "integrated" && result.Disposition != "conflict" {
				t.Fatalf("race did not settle with retained outcome: %+v", result)
			}
			if verb == "start" && result.Disposition != "integrated" {
				t.Fatal("same-baseline CLI start was not represented")
			}
			count := 0
			for _, e := range labEvents(t, p.fluke) {
				if e.Subject == p.step1 && e.Type == store.TypeStepStarted {
					count++
				}
			}
			want := 0
			if result.Disposition == "integrated" {
				want = 1
			}
			if count != want {
				t.Fatalf("race produced %d child starts; want %d", count, want)
			}
			history := followHistory(t, p.fluke, child.Actions[0].Event.ID, 2)
			if history[1].Decision.Disposition != result.Disposition {
				t.Fatal("history and current outcome diverged")
			}
			// Every transition on this store arrived as an integration effect,
			// and an effect carries no delivery authority: no candidate —
			// native or imported — may sit in the outbox.
			outbox, err := p.fluke.Outbox(labCtx, p.repo)
			if err != nil || len(outbox) != 0 {
				t.Fatalf("integration effects queued delivery candidates: %+v %v", outbox, err)
			}
			labRebuild(t, p.fluke)
			t.Logf("two independent rechecks + CLI %s: %s, child starts=%d, candidates=0, decision records=2", verb, result.Disposition, count)
		})
	}
}

func TestSharedStoreFollowupDefaultOutputParity(t *testing.T) {
	baseline := os.Getenv("WIP_LAB_BASELINE_BINARY")
	if baseline == "" {
		baseline = cliBinary(t)
	}
	p := newLabPair(t)
	h := labStart(t, p, p.hybrid, "Hybrid", "shared")
	labStart(t, p, p.fluke, "Fluke", "shared")
	if _, err := writesurface.Pause(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Resume(labCtx, p.fluke, store.ActorHuman, p.repo, "shared"); err != nil {
		t.Fatal(err)
	}
	labImport(t, p.fluke, h, "conflict")
	commands := [][]string{{"plumbing", "session", "--idle-gap", "30m"}, {"plumbing", "session", "--idle-gap", "30m", "--json"}, {"status", "--json"}, {"plumbing", "status"}}
	run := func(binary string, args []string) string {
		cmd := exec.Command(binary, args...)
		cmd.Dir = p.dir
		cmd.Env = append(os.Environ(), "WIP_DB_PATH="+p.fluke.Path(), "WIP_AS_ROLE=", "TZ=UTC")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return string(out)
	}
	before := make([]string, len(commands))
	for i, args := range commands {
		before[i] = run(baseline, args)
		if current := run(cliBinary(t), args); current != before[i] {
			t.Fatalf("default output changed for %v\nbefore=%s\nafter=%s", args, before[i], current)
		}
	}
	db, err := sql.Open("sqlite", p.fluke.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TRIGGER parity_fail BEFORE UPDATE OF decision ON experiment_sources BEGIN SELECT RAISE(ABORT,'injected parity recheck failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.fluke.ExperimentRecheck(labCtx, h.Actions[0].Event.ID); err == nil || !strings.Contains(err.Error(), "injected parity recheck failure") {
		t.Fatalf("expected failed recheck: %v", err)
	}
	for i, args := range commands {
		if current := run(cliBinary(t), args); current != before[i] {
			t.Fatalf("failed companion recheck changed default output for %v", args)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER parity_fail`); err != nil {
		t.Fatal(err)
	}
	followRecheck(t, p.fluke, h, "conflict")
	if _, err := p.fluke.ExperimentAcknowledge(labCtx, h.Actions[0].Event.ID); err != nil {
		t.Fatal(err)
	}
	_ = followPreview(t, p.fluke, "Fluke")
	for i, args := range commands {
		if current := run(cliBinary(t), args); current != before[i] {
			t.Fatalf("lab operations changed default output for %v", args)
		}
	}
	t.Logf("default CLI: identical Session human/JSON and status human/JSON before/after failed recheck, preview, successful recheck and acknowledgement; no daemon; comparison binary=%s", baseline)
}
