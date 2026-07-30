// End-to-end tests for the write-surface Matter's birth-and-amendment Stage
// (workplans/write-surface.md, step-06), through the actual built binary
// (binPath, run, runIn, gitIn, newGitRepo come from e2e_test.go /
// tiers_e2e_test.go, same package). Each writing verb is exercised and
// asserted to append exactly one event of the expected type (§10 invariant
// 1) by opening the resulting store directly and reading its log — the same
// technique worked example 7 uses.
package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// openTestStore opens the store a test's WIP_DB_PATH points at, for
// assertions the CLI itself has no verb to make yet (events-traces' job).
func openTestStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store at %s: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// wantOneEvent asserts subject carries exactly one event, of type wantType —
// for a subject that did not exist before the command under test (a fresh
// birth or insert): the whole point is that the command appended exactly
// one event, and here that means exactly one, full stop.
func wantOneEvent(t *testing.T, s *store.Store, subject, wantType string) store.Event {
	t.Helper()
	events, err := s.EventsOfSubject(context.Background(), subject)
	if err != nil {
		t.Fatalf("read events of %s: %v", subject, err)
	}
	if len(events) != 1 {
		t.Fatalf("%s carries %d events, want exactly 1", subject, len(events))
	}
	if events[0].Type != wantType {
		t.Fatalf("%s's event is %q, want %q", subject, events[0].Type, wantType)
	}
	return events[0]
}

// wantAppendedEvent asserts a command appended exactly one new event to a
// subject that already existed — its own history grew by exactly one entry,
// and that entry is wantType. before is the event count immediately prior to
// the command under test.
func wantAppendedEvent(t *testing.T, s *store.Store, subject string, before int, wantType string) store.Event {
	t.Helper()
	events, err := s.EventsOfSubject(context.Background(), subject)
	if err != nil {
		t.Fatalf("read events of %s: %v", subject, err)
	}
	if len(events) != before+1 {
		t.Fatalf("%s carries %d events, want exactly %d (one appended)", subject, len(events), before+1)
	}
	last := events[len(events)-1]
	if last.Type != wantType {
		t.Fatalf("%s's newest event is %q, want %q", subject, last.Type, wantType)
	}
	return last
}

func mustJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("not JSON: %v (raw=%q)", err, raw)
	}
	return v
}

type nodePayload struct {
	ID      string `json:"id"`
	Locator string `json:"locator"`
	Title   string `json:"title"`
}

func setupRepo(t *testing.T) (dir string, dbEnv []string) {
	t.Helper()
	dbEnv = []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir = newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	return dir, dbEnv
}

func TestBirth_MatterStageStep_OneEventEach(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := runIn(t, dir, dbEnv, "matter", "create", "--title", "Fix flaky detect", "--json")
	if m.exitCode != 0 {
		t.Fatalf("matter create: exit=%d stderr=%q", m.exitCode, m.stderr)
	}
	matter := mustJSON[nodePayload](t, m.stdout)
	if matter.Locator != "fix-flaky-detect" {
		t.Errorf("matter locator = %q, want %q", matter.Locator, "fix-flaky-detect")
	}

	st := runIn(t, dir, dbEnv, "stage", "create", matter.Locator, "--title", "Investigate", "--json")
	if st.exitCode != 0 {
		t.Fatalf("stage create: exit=%d stderr=%q", st.exitCode, st.stderr)
	}
	stage := mustJSON[nodePayload](t, st.stdout)

	sp := runIn(t, dir, dbEnv, "step", "create", stage.ID, "--title", "Reproduce", "--json")
	if sp.exitCode != 0 {
		t.Fatalf("step create: exit=%d stderr=%q", sp.exitCode, sp.stderr)
	}
	step := mustJSON[nodePayload](t, sp.stdout)
	if step.Locator != "step-01" {
		t.Errorf("step locator = %q, want %q", step.Locator, "step-01")
	}

	s := openTestStore(t, dbEnvPath(dbEnv))
	wantOneEvent(t, s, matter.ID, store.TypeMatterCreated)
	wantOneEvent(t, s, stage.ID, store.TypeStageCreated)
	wantOneEvent(t, s, step.ID, store.TypeStepCreated)

	for _, id := range []string{matter.ID, stage.ID, step.ID} {
		n, err := s.Node(context.Background(), id)
		if err != nil {
			t.Fatalf("read node %s: %v", id, err)
		}
		if n.Lifecycle != store.Planned {
			t.Errorf("%s lifecycle = %q, want planned", id, n.Lifecycle)
		}
	}

	// A second create with the same title collides on the derived locator
	// rather than silently minting a duplicate.
	dup := runIn(t, dir, dbEnv, "matter", "create", "--title", "Fix flaky detect", "--json")
	if dup.exitCode != 1 {
		t.Fatalf("duplicate matter create: exit=%d, want 1 (validation)", dup.exitCode)
	}
}

func dbEnvPath(dbEnv []string) string {
	const prefix = "WIP_DB_PATH="
	for _, e := range dbEnv {
		if len(e) > len(prefix) && e[:len(prefix)] == prefix {
			return e[len(prefix):]
		}
	}
	return ""
}

func TestAmendment_InsertReorderReplaceRemove(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Amend me", "--json").stdout)
	s1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "One", "--json").stdout)
	s3 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "Three", "--json").stdout)

	// Insert between the two existing siblings.
	insR := runIn(t, dir, dbEnv, "step", "insert", m.ID, "--title", "Two", "--after", s1.ID, "--json")
	if insR.exitCode != 0 {
		t.Fatalf("step insert: exit=%d stderr=%q", insR.exitCode, insR.stderr)
	}
	s2 := mustJSON[nodePayload](t, insR.stdout)

	dbPath := dbEnvPath(dbEnv)
	s := openTestStore(t, dbPath)
	ev := wantOneEvent(t, s, s2.ID, store.TypeStepInserted)
	if ev.Causation != ev.ID {
		t.Errorf("a plain insert (no rebalance needed) should be its own chain origin")
	}
	children, err := s.Children(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 3 || children[0].ID != s1.ID || children[1].ID != s2.ID || children[2].ID != s3.ID {
		t.Fatalf("children in sort order = %+v, want [one, two, three]", children)
	}

	// Reorder: swap two to three, three to two.
	reoR := runIn(t, dir, dbEnv, "step", "reorder", m.ID, s3.ID, s2.ID, s1.ID)
	if reoR.exitCode != 0 {
		t.Fatalf("step reorder: exit=%d stderr=%q", reoR.exitCode, reoR.stderr)
	}
	s = openTestStore(t, dbPath)
	reordered, err := s.EventsOfSubject(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range reordered {
		if e.Type == store.TypeStepReordered {
			found = true
		}
	}
	if !found {
		t.Errorf("matter %s carries no step.reordered event", m.ID)
	}
	children, err = s.Children(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 3 || children[0].ID != s3.ID || children[1].ID != s2.ID || children[2].ID != s1.ID {
		t.Fatalf("children after reorder = %+v, want [three, two, one]; identity of each unchanged", children)
	}

	// Replace step one.
	repR := runIn(t, dir, dbEnv, "step", "replace", s1.ID, "--title", "Replacement", "--json")
	if repR.exitCode != 0 {
		t.Fatalf("step replace: exit=%d stderr=%q", repR.exitCode, repR.stderr)
	}
	replacement := mustJSON[nodePayload](t, repR.stdout)
	s = openTestStore(t, dbPath)
	wantAppendedEvent(t, s, s1.ID, 1, store.TypeStepReplaced) // s1 already carried its own step.created
	tombstoned, err := s.Tombstoned(context.Background(), s1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !tombstoned {
		t.Errorf("replaced step %s is not tombstoned", s1.ID)
	}
	if replacement.Locator == s1.Locator {
		t.Errorf("replacement reused the old locator %q; locators are never renamed or reused (D44)", s1.Locator)
	}

	// Remove step three.
	remR := runIn(t, dir, dbEnv, "step", "remove", s3.ID, "--reason", "no longer needed")
	if remR.exitCode != 0 {
		t.Fatalf("step remove: exit=%d stderr=%q", remR.exitCode, remR.stderr)
	}
	s = openTestStore(t, dbPath)
	wantAppendedEvent(t, s, s3.ID, 1, store.TypeStepRemoved) // s3 already carried its own step.created
	tombstoned, err = s.Tombstoned(context.Background(), s3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !tombstoned {
		t.Errorf("removed step %s is not tombstoned", s3.ID)
	}
	// A tombstoned node still resolves as a live identity reference (D44) —
	// its history is intact even though it is no longer addressable by
	// locator.
	if _, err := s.EventsOfSubject(context.Background(), s3.ID); err != nil {
		t.Errorf("removed step's history should stay readable: %v", err)
	}
}

func TestLifecycle_ScaleCorrectTypesAndCascade(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Cascade me", "--json").stdout)
	stg := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "stage", "create", m.ID, "--title", "Stage one", "--json").stdout)
	stp := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", stg.ID, "--title", "Step one", "--json").stdout)

	r := runIn(t, dir, dbEnv, "start", stp.ID)
	if r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	dbPath := dbEnvPath(dbEnv)
	s := openTestStore(t, dbPath)
	ctx := context.Background()

	// Each of the three already carried its own *.created event; start
	// appends exactly one more to each — the cascade's whole point.
	matterEv := wantAppendedEvent(t, s, m.ID, 1, store.TypeMatterStarted)
	stageEv := wantAppendedEvent(t, s, stg.ID, 1, store.TypeStageStarted)
	stepEv := wantAppendedEvent(t, s, stp.ID, 1, store.TypeStepStarted)

	// One command, one correlation shared by the whole chain, causation
	// walking parent-to-child (D57).
	if matterEv.Causation != matterEv.ID || matterEv.Correlation != matterEv.ID {
		t.Errorf("matter.started should be the chain's origin (self-referential): %+v", matterEv)
	}
	if stageEv.Causation != matterEv.ID || stageEv.Correlation != matterEv.ID {
		t.Errorf("stage.started should be caused by matter.started and share its correlation: %+v", stageEv)
	}
	if stepEv.Causation != stageEv.ID || stepEv.Correlation != matterEv.ID {
		t.Errorf("step.started should be caused by stage.started and share the origin's correlation: %+v", stepEv)
	}

	var matterP, stageP, stepP struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Cascade bool   `json:"cascade,omitempty"`
	}
	if err := json.Unmarshal(matterEv.Payload, &matterP); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stageEv.Payload, &stageP); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stepEv.Payload, &stepP); err != nil {
		t.Fatal(err)
	}
	if !matterP.Cascade || !stageP.Cascade {
		t.Errorf("both auto-started ancestors should carry payload.cascade=true: matter=%v stage=%v", matterP.Cascade, stageP.Cascade)
	}
	if stepP.Cascade {
		t.Errorf("the explicitly named step should not itself carry cascade=true")
	}
	if matterEv.Actor != store.ActorHuman || stageEv.Actor != store.ActorHuman || stepEv.Actor != store.ActorHuman {
		t.Errorf("every event in the cascade should carry the caller's actor, not a distinct system actor")
	}

	// finish requires in-progress; a finish on a still-planned sibling
	// matter is refused at write time with no event appended.
	other := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Untouched", "--json").stdout)
	badFinish := runIn(t, dir, dbEnv, "finish", other.ID)
	if badFinish.exitCode == 0 {
		t.Fatalf("finish on a Planned matter should be refused")
	}
	events, err := s.EventsOfSubject(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 { // only matter.created
		t.Errorf("%s carries %d events after a refused finish, want 1 (no event appended)", other.ID, len(events))
	}

	// finish/cancel/pause/resume round trip on the step.
	if r := runIn(t, dir, dbEnv, "finish", stp.ID); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPath)
	n, err := s.Node(ctx, stp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Lifecycle != store.Done {
		t.Errorf("step lifecycle = %q, want done", n.Lifecycle)
	}

	stp2 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", stg.ID, "--title", "Step two", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", stp2.ID); r.exitCode != 0 {
		t.Fatalf("start step two: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "pause", stp2.ID); r.exitCode != 0 {
		t.Fatalf("pause: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "resume", stp2.ID); r.exitCode != 0 {
		t.Fatalf("resume: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "cancel", stp2.ID); r.exitCode != 0 {
		t.Fatalf("cancel: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPath)
	n, err = s.Node(ctx, stp2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Lifecycle != store.Canceled {
		t.Errorf("step two lifecycle = %q, want canceled", n.Lifecycle)
	}
}

func TestBacklog_ProvenanceAndReadOnlyList(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Origin matter", "--json").stdout)

	intake := runIn(t, dir, dbEnv, "backlog", "add", "--title", "An arrival", "--provenance", "intake", "--json")
	if intake.exitCode != 0 {
		t.Fatalf("backlog add (intake): exit=%d stderr=%q", intake.exitCode, intake.stderr)
	}
	found := runIn(t, dir, dbEnv, "backlog", "add", "--title", "Discovered mid-flight", "--provenance", "found", "--origin", m.ID, "--json")
	if found.exitCode != 0 {
		t.Fatalf("backlog add (found): exit=%d stderr=%q", found.exitCode, found.stderr)
	}
	deferred := runIn(t, dir, dbEnv, "backlog", "add", "--title", "Pushed out", "--provenance", "deferred", "--origin", m.ID, "--detail", "descoped", "--json")
	if deferred.exitCode != 0 {
		t.Fatalf("backlog add (deferred): exit=%d stderr=%q", deferred.exitCode, deferred.stderr)
	}
	// deferred without an origin is refused (MODEL §4: records origin + why).
	badDeferred := runIn(t, dir, dbEnv, "backlog", "add", "--title", "No origin", "--provenance", "deferred", "--json")
	if badDeferred.exitCode == 0 {
		t.Fatalf("deferred with no --origin should be refused")
	}

	type entry struct {
		ID         string `json:"id"`
		Provenance string `json:"provenance"`
	}
	intakeEntry := mustJSON[entry](t, intake.stdout)
	foundEntry := mustJSON[entry](t, found.stdout)
	deferredEntry := mustJSON[entry](t, deferred.stdout)

	s := openTestStore(t, dbPath)
	ctx := context.Background()
	e := wantOneEvent(t, s, intakeEntry.ID, store.TypeBacklogEntered)
	var p store.BacklogEntered
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Provenance != store.ProvenanceIntake {
		t.Errorf("provenance = %q, want intake", p.Provenance)
	}
	wantOneEvent(t, s, foundEntry.ID, store.TypeBacklogEntered)
	wantOneEvent(t, s, deferredEntry.ID, store.TypeBacklogEntered)

	beforeList, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := runIn(t, dir, dbEnv, "backlog", "list"); r.exitCode != 0 {
		t.Fatalf("backlog list: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPath)
	afterList, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterList) != len(beforeList) {
		t.Errorf("backlog list appended %d events, want 0 (read-only)", len(afterList)-len(beforeList))
	}

	// Plan promotes the intake entry into the matter; decline needs a reason.
	plan := runIn(t, dir, dbEnv, "backlog", "plan", intakeEntry.ID, m.ID, "--json")
	if plan.exitCode != 0 {
		t.Fatalf("backlog plan: exit=%d stderr=%q", plan.exitCode, plan.stderr)
	}
	s = openTestStore(t, dbPath)
	wantAppendedEvent(t, s, intakeEntry.ID, 1, store.TypeBacklogPlanned) // already carried its own backlog.entered

	decline := runIn(t, dir, dbEnv, "backlog", "decline", foundEntry.ID, "--reason", "not worth it", "--json")
	if decline.exitCode != 0 {
		t.Fatalf("backlog decline: exit=%d stderr=%q", decline.exitCode, decline.stderr)
	}
	s = openTestStore(t, dbPath)
	dEv := wantAppendedEvent(t, s, foundEntry.ID, 1, store.TypeBacklogDeclined) // already carried its own backlog.entered
	var dp store.BacklogDeclined
	if err := json.Unmarshal(dEv.Payload, &dp); err != nil {
		t.Fatal(err)
	}
	if dp.Reason == "" {
		t.Errorf("declined entry must carry a reason (distinguishable from not-yet-acted-upon, MODEL §4)")
	}
}
