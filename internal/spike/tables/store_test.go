package tables

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

func adapter(t *testing.T, path string, env scenario.Env) scenario.Store {
	t.Helper()
	st, err := Open(path, env)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return st
}

// TestConformance is the shared scenario, unmodified.
func TestConformance(t *testing.T) {
	scenario.RunConformance(t, adapter)
}

// ---------------------------------------------------------------------------
// The write-path / audit-log coupling, held to its word by going *around* the
// Go API and writing raw SQL against the same connection — which is the only
// interesting test for this shape, since a shape whose guarantee lives in Go
// discipline would pass every test that goes through Go.
// ---------------------------------------------------------------------------

func TestNodeWritesCannotBypassTheLog(t *testing.T) {
	ctx := context.Background()
	st, matter, step := populated(t)
	defer func() { _ = st.Close() }()

	// A freshly appended event, available to be smuggled into a node write.
	// Appending it is legal (see the honest hole below); *using* it is where
	// the schema pushes back.
	fresh := st.ulid.New()
	mustExec(t, st, `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
		VALUES (?,'matter.started','2026-07-29T00:00:00Z',?,NULL,NULL,?,'{}')`,
		fresh, scenario.FixedEnv.Repo, matter)

	cases := []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{
			name: "update with no event at all",
			sql:  `UPDATE nodes SET lifecycle = 'done' WHERE id = ?`,
			args: []any{step},
			want: "must advance to a newer event",
		},
		{
			name: "update reusing the row's own current event",
			sql:  `UPDATE nodes SET lifecycle = 'done', last_event_id = last_event_id WHERE id = ?`,
			args: []any{step},
			want: "must advance to a newer event",
		},
		{
			name: "update naming an event about a different node",
			sql:  `UPDATE nodes SET lifecycle = 'done', last_event_id = ? WHERE id = ?`,
			args: []any{fresh, step},
			want: "must advance to a newer event",
		},
		{
			name: "insert naming an event about a different node",
			sql: `INSERT INTO nodes (id,kind,parent,locator,title,lifecycle,born_event_id,last_event_id)
			      VALUES ('01JQAAAAAAAAAAAAAAAAAAAAAA','matter',NULL,'matter','smuggled','planned',?,?)`,
			args: []any{fresh, fresh},
			want: "must carry an event about that node",
		},
		{
			name: "one event standing for two node writes",
			sql:  `UPDATE nodes SET lifecycle = 'done', last_event_id = ?`,
			args: []any{fresh},
			want: "must advance to a newer event",
		},
		{
			name: "rewriting a logged event",
			sql:  `UPDATE events SET payload = '{"tampered":true}' WHERE id = ?`,
			args: []any{fresh},
			want: "append-only",
		},
		{
			name: "deleting a logged event",
			sql:  `DELETE FROM events WHERE id = ?`,
			args: []any{fresh},
			want: "append-only",
		},
		{
			name: "deleting a node",
			sql:  `DELETE FROM nodes WHERE id = ?`,
			args: []any{step},
			want: "never deleted",
		},
		{
			name: "back-dating an event into the middle of the log",
			sql: `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
			      VALUES ('01AAAAAAAAAAAAAAAAAAAAAAAA','matter.started','2026-07-29T00:00:00Z',?,NULL,NULL,?,'{}')`,
			args: []any{scenario.FixedEnv.Repo, matter},
			want: "ascend strictly",
		},
		{
			name: "an event type outside the taxonomy",
			sql: `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
			      VALUES ('0ZZZZZZZZZZZZZZZZZZZZZZZZZ','matter.vibed','2026-07-29T00:00:00Z',?,NULL,NULL,?,'{}')`,
			args: []any{scenario.FixedEnv.Repo, matter},
			want: "FOREIGN KEY",
		},
		{
			name: "a durable-write event carrying an execution dimension (D56)",
			sql: `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
			      VALUES ('0ZZZZZZZZZZZZZZZZZZZZZZZZZ','matter.started','2026-07-29T00:00:00Z',?,?,NULL,?,'{}')`,
			args: []any{scenario.FixedEnv.Repo, scenario.FixedEnv.Clone, matter},
			want: "tier dimensions",
		},
		{
			name: "a durable-write event with no repo dimension (D56)",
			sql: `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
			      VALUES ('0ZZZZZZZZZZZZZZZZZZZZZZZZZ','matter.started','2026-07-29T00:00:00Z',NULL,NULL,NULL,?,'{}')`,
			args: []any{matter},
			want: "tier dimensions",
		},
		{
			name: "an event whose subject is a locator, not an identity",
			sql: `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
			      VALUES ('0ZZZZZZZZZZZZZZZZZZZZZZZZZ','step.started','2026-07-29T00:00:00Z',?,NULL,NULL,'step-01','{}')`,
			args: []any{scenario.FixedEnv.Repo},
			want: "CHECK",
		},
		{
			name: "re-parenting a node",
			sql:  `UPDATE nodes SET parent = NULL WHERE id = ?`,
			args: []any{step},
			want: "immutable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.db.ExecContext(ctx, tc.sql, tc.args...)
			if err == nil {
				t.Fatalf("want the schema to refuse this write, got nil error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused, but for the wrong reason:\n got: %v\nwant substring: %q", err, tc.want)
			}
		})
	}

	// Nothing above landed: the store still answers exactly as it did.
	assertInProgressIDs(t, st, matter, step)
}

// TestTheHonestHole records the one direction the schema does *not* close:
// a row may be appended to the log without any node write to justify it. The
// log can be padded with lies even though it can never be silently bypassed or
// rewritten. This is asserted, not hand-waved, because the report claims it.
func TestTheHonestHole(t *testing.T) {
	ctx := context.Background()
	st, matter, _ := populated(t)
	defer func() { _ = st.Close() }()

	before, err := st.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, st, `INSERT INTO events (id,type,occurred_at,repo,clone,worktree,subject,payload)
		VALUES (?,'matter.finished','2026-07-29T00:00:00Z',?,NULL,NULL,?,'{}')`,
		st.ulid.New(), scenario.FixedEnv.Repo, matter)

	after, err := st.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("expected the spurious event to be accepted: %d -> %d", len(before), len(after))
	}
	// And it is a lie: the Matter is still in progress.
	assertInProgressContains(t, st, matter)
}

// TestRefusedCommandsWriteNothing exercises the rollback boundary through the
// Go API for each refusal the model defines.
func TestRefusedCommandsWriteNothing(t *testing.T) {
	ctx := context.Background()
	st, matter, step := populated(t)
	defer func() { _ = st.Close() }()

	planned, err := st.CreateMatter(ctx, "never started")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(ctx, matter); err != nil { // setup for "finish a node twice"
		t.Fatal(err)
	}

	refusals := []struct {
		name string
		fn   func() error
	}{
		{"start an already-started node", func() error { return st.Start(ctx, step) }},
		{"finish a planned node", func() error { return st.Finish(ctx, planned) }},
		{"finish a node twice", func() error { return st.Finish(ctx, matter) }},
		{"start an unknown node", func() error { return st.Start(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ") }},
		{"add a Step to a Step", func() error { _, err := st.AddStep(ctx, step, "nested"); return err }},
		{"add a Step to nothing", func() error { _, err := st.AddStep(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ", "x"); return err }},
		{"label with an empty label", func() error { return st.Label(ctx, matter, "") }},
		{"label an unknown node", func() error { return st.Label(ctx, "01JQZZZZZZZZZZZZZZZZZZZZZZ", "x") }},
		{"create a Matter with no title", func() error { _, err := st.CreateMatter(ctx, ""); return err }},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			before := len(mustEvents(t, st))
			if err := tc.fn(); err == nil {
				t.Fatalf("want refusal, got nil")
			}
			if got := len(mustEvents(t, st)); got != before {
				t.Fatalf("refusal wrote %d event(s)", got-before)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The v2 migration exercise: a database populated by v1, migrated forward,
// still answering — and now answering with labels.
// ---------------------------------------------------------------------------

func TestMigrationV1PopulatedThenV2(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "spike.db")

	// --- v1: populate, and confirm the v1 store answers the founding question.
	v1, err := OpenAtVersion(path, scenario.FixedEnv, 1)
	if err != nil {
		t.Fatalf("open v1: %v", err)
	}
	if v1.SchemaVersion() != 1 {
		t.Fatalf("want v1, got v%d", v1.SchemaVersion())
	}
	matter, err := v1.CreateMatter(ctx, "old matter")
	if err != nil {
		t.Fatal(err)
	}
	step, err := v1.AddStep(ctx, matter, "old step")
	if err != nil {
		t.Fatal(err)
	}
	if err := v1.Start(ctx, step); err != nil {
		t.Fatal(err)
	}
	if err := v1.Label(ctx, step, "hot"); err == nil {
		t.Fatal("v1 store must refuse the v2 verb")
	}
	v1Answer, err := v1.InProgressLabeled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v1Answer) != 2 {
		t.Fatalf("v1 in-progress: got %d, want 2", len(v1Answer))
	}
	v1Log := mustEvents(t, v1)
	if err := v1.Close(); err != nil {
		t.Fatal(err)
	}

	// --- migrate by simply opening at the latest version.
	v2, err := Open(path, scenario.FixedEnv)
	if err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}
	defer func() { _ = v2.Close() }()
	if v2.SchemaVersion() != 2 {
		t.Fatalf("want v2 after Open, got v%d", v2.SchemaVersion())
	}

	// backup-before-migrate (PLAN 1.2).
	if _, err := os.Stat(path + ".bak-v1"); err != nil {
		t.Fatalf("expected a pre-migration backup at %s.bak-v1: %v", path, err)
	}

	// --- old rows still answer, unchanged, with an empty label.
	after, err := v2.InProgressLabeled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].ID != matter || after[1].ID != step {
		t.Fatalf("post-migration in-progress: got %v", after)
	}
	for _, n := range after {
		if n.Label != "" {
			t.Fatalf("node %s born under v1 should have no label, got %q", n.ID, n.Label)
		}
		if n.Locator == "" || n.Title == "" {
			t.Fatalf("node %s lost locator/title across the migration", n.ID)
		}
	}

	// --- the v1 log survived the migration byte for byte.
	v2Log := mustEvents(t, v2)
	if len(v2Log) != len(v1Log) {
		t.Fatalf("log length changed across migration: %d -> %d", len(v1Log), len(v2Log))
	}
	for i := range v1Log {
		if v1Log[i].ID != v2Log[i].ID || v1Log[i].Type != v2Log[i].Type ||
			v1Log[i].Subject != v2Log[i].Subject {
			t.Fatalf("migration rewrote event %d", i)
		}
	}

	// --- the new verb works on a row that predates the column, and the new
	// event type joins the same log under the same envelope.
	if err := v2.Label(ctx, step, "hot"); err != nil {
		t.Fatalf("Label on a v1-born node: %v", err)
	}
	labeled := mustEvents(t, v2)
	if len(labeled) != len(v2Log)+1 {
		t.Fatalf("Label emitted %d events, want exactly 1", len(labeled)-len(v2Log))
	}
	last := labeled[len(labeled)-1]
	if last.Type != typeStepLabeled || last.Subject != step {
		t.Fatalf("label event: got %s/%s want %s/%s", last.Type, last.Subject, typeStepLabeled, step)
	}
	if last.Repo != scenario.FixedEnv.Repo || last.Clone != "" || last.Worktree != "" {
		t.Fatalf("new event type did not inherit the D56 dimension rule: %+v", last)
	}
	if string(last.Payload) == "" {
		t.Fatal("label event has an empty payload")
	}

	// --- and it is surfaced in the founding question.
	final, err := v2.InProgressLabeled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final[1].ID != step || final[1].Label != "hot" {
		t.Fatalf("label not surfaced in the in-progress answer: %+v", final[1])
	}
	if final[0].Label != "" {
		t.Fatalf("labelling one node labelled another: %+v", final[0])
	}

	// --- the v2 verb is coupled to the log by the same triggers, with no new
	// coupling code: a raw label write is still refused.
	if _, err := v2.db.ExecContext(ctx, `UPDATE nodes SET label = 'smuggled' WHERE id = ?`, matter); err == nil {
		t.Fatal("v2's new column must inherit the coupling, got nil error")
	}
}

// ---------------------------------------------------------------------------

func populated(t *testing.T) (st *Store, matter, step string) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "spike.db"), scenario.FixedEnv)
	if err != nil {
		t.Fatal(err)
	}
	if matter, err = st.CreateMatter(ctx, "a matter"); err != nil {
		t.Fatal(err)
	}
	if step, err = st.AddStep(ctx, matter, "a step"); err != nil {
		t.Fatal(err)
	}
	if err := st.Start(ctx, step); err != nil {
		t.Fatal(err)
	}
	return st, matter, step
}

func mustExec(t *testing.T, st *Store, query string, args ...any) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustEvents(t *testing.T, st *Store) []scenario.Event {
	t.Helper()
	evs, err := st.Events(context.Background())
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	return evs
}

func assertInProgressIDs(t *testing.T, st *Store, want ...string) {
	t.Helper()
	got, err := st.InProgress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("in-progress: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("in-progress[%d]: got %s want %s", i, got[i].ID, want[i])
		}
	}
}

func assertInProgressContains(t *testing.T, st *Store, id string) {
	t.Helper()
	got, err := st.InProgress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n.ID == id {
			return
		}
	}
	t.Fatalf("%s is not in progress", id)
}

// TestLogAloneReconstructsState is the fidelity-list item this shape is most
// likely to under-build: "enough to reconstruct in-flight work". In a
// tables-first store the tables answer that question trivially, which is
// exactly why nobody notices when the *log* stops being sufficient on its own.
//
// So: rebuild the whole node set from `events` and nothing else, and hold the
// result against what the tables say. This passes today because every payload
// carries the facts a rebuild needs — but nothing structural keeps it passing.
// A future verb whose payload omits a field breaks this silently, and only
// this test would notice. See report.md, axis 4.
func TestLogAloneReconstructsState(t *testing.T) {
	ctx := context.Background()
	st, matter, step := populated(t)
	defer func() { _ = st.Close() }()

	second, err := st.CreateMatter(ctx, "second matter")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.AddStep(ctx, matter, "another step")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Start(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.Finish(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.Label(ctx, step, "hot"); err != nil {
		t.Fatal(err)
	}
	_ = second

	rebuilt := replay(t, mustEvents(t, st))

	live, err := st.InProgressLabeled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fromLog []LabeledNode
	for _, id := range replayOrder {
		if n, ok := rebuilt[id]; ok && n.Lifecycle == scenario.InProgress {
			fromLog = append(fromLog, n)
		}
	}
	if len(fromLog) != len(live) {
		t.Fatalf("log rebuild: %d in-progress, tables say %d", len(fromLog), len(live))
	}
	for i := range live {
		if fromLog[i] != live[i] {
			t.Fatalf("log rebuild disagrees with the tables at %d:\n log: %+v\ntbls: %+v", i, fromLog[i], live[i])
		}
	}
}

// replayOrder is the creation order the rebuild observed; the log's own total
// order supplies it, so no sort key has to be carried in a payload.
var replayOrder []string

func replay(t *testing.T, evs []scenario.Event) map[string]LabeledNode {
	t.Helper()
	replayOrder = nil
	out := map[string]LabeledNode{}
	for _, e := range evs {
		var p struct {
			Kind, Title, Locator, Parent, To, Label string
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %s payload: %v", e.ID, err)
		}
		n := out[e.Subject]
		switch e.Type {
		case scenario.TypeMatterCreated, scenario.TypeStepCreated:
			n = LabeledNode{Node: scenario.Node{
				ID: e.Subject, Kind: scenario.Kind(p.Kind), Parent: p.Parent,
				Locator: p.Locator, Title: p.Title, Lifecycle: scenario.Planned,
			}}
			replayOrder = append(replayOrder, e.Subject)
		case scenario.TypeMatterStarted, scenario.TypeStepStarted,
			scenario.TypeMatterFinished, scenario.TypeStepFinished:
			n.Lifecycle = scenario.Lifecycle(p.To)
		case typeMatterLabeled, typeStepLabeled:
			n.Label = p.Label
		default:
			t.Fatalf("event %s: unhandled type %q — the rebuild is behind the taxonomy", e.ID, e.Type)
		}
		out[e.Subject] = n
	}
	return out
}
