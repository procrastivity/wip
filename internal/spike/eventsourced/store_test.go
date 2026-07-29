package eventsourced

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// adapter is the thin bridge from Open to the harness's OpenFunc.
func adapter(t *testing.T, path string, env scenario.Env) scenario.Store {
	t.Helper()
	st, err := Open(path, env)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return st
}

// TestConformance is the floor: the pinned scenario, both invariants, the
// envelope, append-only, durability across a reopen.
func TestConformance(t *testing.T) {
	scenario.RunConformance(t, adapter)
}

// nodeRow is a whole projection row, including the columns the harness's Node
// type does not carry, so the rebuild comparison is over everything.
type nodeRow struct {
	ID         string
	Kind       string
	Parent     string
	Locator    string
	Title      string
	Lifecycle  string
	BirthEvent string
	Label      string
}

func snapshot(t *testing.T, s *Store) []nodeRow {
	t.Helper()
	query := `SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, birth_event, ''
	          FROM nodes ORDER BY id`
	if s.Version() >= 2 {
		query = `SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, birth_event, label
		         FROM nodes ORDER BY id`
	}
	rows, err := s.db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []nodeRow
	for rows.Next() {
		var r nodeRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Parent, &r.Locator, &r.Title,
			&r.Lifecycle, &r.BirthEvent, &r.Label); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

// populate runs the pinned scenario's writes and returns the ids it made.
func populate(t *testing.T, s *Store) (matter, step1, step2 string) {
	t.Helper()
	ctx := context.Background()

	matter, err := s.CreateMatter(ctx, "first matter")
	if err != nil {
		t.Fatalf("CreateMatter: %v", err)
	}
	if _, err := s.CreateMatter(ctx, "second matter"); err != nil {
		t.Fatalf("CreateMatter (second): %v", err)
	}
	if step1, err = s.AddStep(ctx, matter, "step one"); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	if step2, err = s.AddStep(ctx, matter, "step two"); err != nil {
		t.Fatalf("AddStep (second): %v", err)
	}
	if err := s.Start(ctx, step1); err != nil {
		t.Fatalf("Start(step1): %v", err)
	}
	if err := s.Start(ctx, step2); err != nil {
		t.Fatalf("Start(step2): %v", err)
	}
	if err := s.Finish(ctx, step1); err != nil {
		t.Fatalf("Finish(step1): %v", err)
	}
	return matter, step1, step2
}

func openTemp(t *testing.T, version int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spike.db")
	st, err := OpenAt(path, scenario.FixedEnv, version)
	if err != nil {
		t.Fatalf("OpenAt(v%d): %v", version, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

// TestRebuildEqualsMaintainedProjection is the load-bearing claim of this
// shape: the projection holds no fact the log does not, so folding the log
// over an empty table reproduces it exactly.
func TestRebuildEqualsMaintainedProjection(t *testing.T) {
	st, _ := openTemp(t, LatestVersion)
	matter, _, step2 := populate(t, st)
	if err := st.Label(context.Background(), step2, "hot"); err != nil {
		t.Fatalf("Label: %v", err)
	}
	if err := st.Label(context.Background(), matter, "q3"); err != nil {
		t.Fatalf("Label(matter): %v", err)
	}

	maintained := snapshot(t, st)
	logBefore, err := st.Events(context.Background())
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	if err := st.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	rebuilt := snapshot(t, st)

	if !reflect.DeepEqual(maintained, rebuilt) {
		t.Fatalf("rebuilt projection differs from maintained\n maintained: %+v\n rebuilt:    %+v",
			maintained, rebuilt)
	}
	logAfter, err := st.Events(context.Background())
	if err != nil {
		t.Fatalf("Events after rebuild: %v", err)
	}
	if !reflect.DeepEqual(logBefore, logAfter) {
		t.Fatal("Rebuild must not touch the log")
	}
}

// TestLogIsAppendOnlyInTheSubstrate shows where append-only actually lives:
// not in a convention this package follows, but in triggers that refuse.
func TestLogIsAppendOnlyInTheSubstrate(t *testing.T) {
	st, _ := openTemp(t, LatestVersion)
	populate(t, st)
	ctx := context.Background()

	if _, err := st.db.ExecContext(ctx, `UPDATE events SET type = 'tampered'`); err == nil {
		t.Fatal("UPDATE on events: want refusal, got nil")
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM events`); err == nil {
		t.Fatal("DELETE on events: want refusal, got nil")
	}

	evs, err := st.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	// 2 created + 2 created + (started, started) + started + finished.
	if len(evs) != 8 {
		t.Fatalf("log length after two refused tampering attempts: got %d, want 8", len(evs))
	}
}

// TestRefusedVerbWritesNothing holds the other half of invariant 1: a verb
// that refuses leaves the log and the projection untouched, because the guard
// runs inside the same transaction that would have appended.
func TestRefusedVerbWritesNothing(t *testing.T) {
	st, _ := openTemp(t, LatestVersion)
	matter, step1, _ := populate(t, st)
	ctx := context.Background()

	before, err := st.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	beforeNodes := snapshot(t, st)

	cases := []struct {
		name string
		run  func() error
	}{
		{"AddStep under an unknown Matter", func() error {
			_, err := st.AddStep(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ", "orphan")
			return err
		}},
		{"AddStep under a Step", func() error {
			_, err := st.AddStep(ctx, step1, "nested")
			return err
		}},
		{"Start an already-started node", func() error { return st.Start(ctx, matter) }},
		{"Start a finished node", func() error { return st.Start(ctx, step1) }},
		{"Finish an unknown node", func() error { return st.Finish(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ") }},
		{"Finish an already-finished node", func() error { return st.Finish(ctx, step1) }},
		{"Label an unknown node", func() error { return st.Label(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ", "x") }},
	}
	for _, c := range cases {
		if err := c.run(); err == nil {
			t.Fatalf("%s: want error, got nil", c.name)
		}
	}

	after, err := st.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("refused verbs wrote to the log: %d events became %d", len(before), len(after))
	}
	if !reflect.DeepEqual(beforeNodes, snapshot(t, st)) {
		t.Fatal("refused verbs moved the projection")
	}
}

// TestStartCascadeIsExactlyTwoEvents pins D57's one-command-several-verbs case
// to the letter: ancestor first, one event each, and nothing more.
func TestStartCascadeIsExactlyTwoEvents(t *testing.T) {
	st, _ := openTemp(t, LatestVersion)
	ctx := context.Background()

	matter, err := st.CreateMatter(ctx, "m")
	if err != nil {
		t.Fatalf("CreateMatter: %v", err)
	}
	step, err := st.AddStep(ctx, matter, "s")
	if err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	if err := st.Start(ctx, step); err != nil {
		t.Fatalf("Start: %v", err)
	}

	evs, err := st.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	want := []struct{ typ, subject string }{
		{scenario.TypeMatterCreated, matter},
		{scenario.TypeStepCreated, step},
		{scenario.TypeMatterStarted, matter},
		{scenario.TypeStepStarted, step},
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d", len(evs), len(want))
	}
	for i, w := range want {
		if evs[i].Type != w.typ || evs[i].Subject != w.subject {
			t.Fatalf("event %d: got %s/%s, want %s/%s", i, evs[i].Type, evs[i].Subject, w.typ, w.subject)
		}
	}
}
