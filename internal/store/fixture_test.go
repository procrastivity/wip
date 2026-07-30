package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The shared test harness.
//
// Almost nothing in this store can be written without a tier context: D56 makes
// the repo dimension mandatory on every event but `batch.*`, and the projection
// tables carry real foreign keys into repos/clones/worktrees. So a test that
// wants to say anything at all first needs a Repo, a Clone and a Worktree — and
// the only way to get them is through the same write path everything else uses,
// which is the point.
//
// harness is therefore not a convenience: it is the bootstrap every Step's tests
// share, so a change to the write path breaks one file rather than ten.

// harness is a fresh store plus the tier context and actor a command runs under.
type harness struct {
	*Store

	t   *testing.T
	ctx context.Context

	// Repo, Clone and Worktree are the bootstrapped tier rows, which are also
	// this harness's Env.
	Repo     string
	Clone    string
	Worktree string
}

// newHarness opens a store under t.TempDir() and attaches one Repo, one Clone
// and one main Worktree, so every dimension a P1 event can require is available.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	s, err := Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := &harness{Store: s, t: t, ctx: ctx}
	h.Repo = h.attachRepo(RepoAttached{Label: "fixture"})
	h.Clone = h.attachClone(CloneAttached{Repo: h.Repo, GitCommonDir: "/tmp/fixture/.git", Label: "main"})
	// The main worktree has no name — D37's null case, and the one the
	// COALESCE(name,'') unique index exists for.
	h.Worktree = h.attachWorktree(h.Repo, WorktreeAttached{Clone: h.Clone})
	return h
}

// with returns the same store under another tier context.
//
// Several assertions are about a boundary between two tier rows — a locator
// unique within a Matter and not across Matters, a gate one Repo declares saying
// nothing about another Repo's Matters, a cursor keyed per Worktree — and none of
// those can be written by a command that only ever runs in one place.
func (h *harness) with(env Env) *harness {
	return &harness{
		Store: h.Store, t: h.t, ctx: h.ctx,
		Repo: env.Repo, Clone: env.Clone, Worktree: env.Worktree,
	}
}

// attachRepo attaches a Repo and returns its identity.
//
// The Repo's identity has to exist before the command that creates it, because
// repo.attached carries its own Repo as its repo dimension (insertRepo refuses
// anything else). This is the one case Store.NewID is exported for.
func (h *harness) attachRepo(p RepoAttached) string {
	h.t.Helper()
	id := h.NewID()
	h.with(Env{Repo: id}).commit(Draft{Type: TypeRepoAttached, Subject: id, Payload: p})
	return id
}

// attachClone attaches a Clone to the Repo its payload names.
func (h *harness) attachClone(p CloneAttached) string {
	h.t.Helper()
	return h.with(Env{Repo: p.Repo}).commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeCloneAttached, Subject: tx.NewID(), Payload: p}, nil
	})
}

// attachWorktree attaches a Worktree to a Clone. A Worktree keys at its Clone and
// the payload says so, but the event still carries a repo dimension (D56), which
// is why the Repo travels alongside.
func (h *harness) attachWorktree(repo string, p WorktreeAttached) string {
	h.t.Helper()
	return h.with(Env{Repo: repo}).commitOne(func(_ context.Context, tx *Tx) (Draft, error) {
		return Draft{Type: TypeWorktreeAttached, Subject: tx.NewID(), Payload: p}, nil
	})
}

// req is the Request every harness command runs under: a human, in the
// bootstrapped tier context.
func (h *harness) req() Request {
	return Request{
		Actor: ActorHuman,
		Env:   Env{Repo: h.Repo, Clone: h.Clone, Worktree: h.Worktree},
	}
}

// commit writes a fixed set of drafts and fails the test if it does not land.
func (h *harness) commit(drafts ...Draft) []Event {
	h.t.Helper()
	return h.commitWith(func(context.Context, *Tx) ([]Draft, error) { return drafts, nil })
}

// commitWith runs a decide function through the one write path.
func (h *harness) commitWith(decide func(context.Context, *Tx) ([]Draft, error)) []Event {
	h.t.Helper()
	events, err := h.Commit(h.ctx, h.req(), decide)
	if err != nil {
		h.t.Fatalf("commit: %v", err)
	}
	return events
}

// commitError runs a decide function expecting it to be refused, and returns the
// error. It fails the test if the command succeeded.
func (h *harness) commitError(decide func(context.Context, *Tx) ([]Draft, error)) error {
	h.t.Helper()
	if _, err := h.Commit(h.ctx, h.req(), decide); err != nil {
		return err
	}
	h.t.Fatal("commit succeeded, want a refusal")
	return nil
}

// commitOne commits a single draft whose subject the decide function mints, and
// returns that subject — the common "birth one entity" shape.
func (h *harness) commitOne(decide func(context.Context, *Tx) (Draft, error)) string {
	h.t.Helper()
	var subject string
	h.commitWith(func(ctx context.Context, tx *Tx) ([]Draft, error) {
		d, err := decide(ctx, tx)
		if err != nil {
			return nil, err
		}
		subject = d.Subject
		return []Draft{d}, nil
	})
	return subject
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

// matter creates a Matter and returns its identity.
func (h *harness) matter(locator, title string) string {
	h.t.Helper()
	return h.node(TypeMatterCreated, "", locator, title)
}

// stage creates a Stage under a Matter.
func (h *harness) stage(parent, locator, title string) string {
	h.t.Helper()
	return h.node(TypeStageCreated, parent, locator, title)
}

// step creates a Step under a Matter or a Stage.
func (h *harness) step(parent, locator, title string) string {
	h.t.Helper()
	return h.node(TypeStepCreated, parent, locator, title)
}

// node is the shared birth path: one event, sort key after every live sibling.
func (h *harness) node(eventType, parent, locator, title string) string {
	h.t.Helper()
	return h.commitOne(func(ctx context.Context, tx *Tx) (Draft, error) {
		sortKey := int64(sortKeyGap)
		if parent != "" {
			k, err := tx.NextSortKey(ctx, parent)
			if err != nil {
				return Draft{}, err
			}
			sortKey = k
		}
		return Draft{
			Type:    eventType,
			Subject: tx.NewID(),
			Payload: NodeBirth{Title: title, Locator: locator, Parent: parent, SortKey: sortKey},
		}, nil
	})
}

// ---------------------------------------------------------------------------
// Content
// ---------------------------------------------------------------------------

// write puts content on a node through the one write path, and returns the event
// it produced.
//
// The event type is deliberately not an argument: ContentDraft derives it from the
// kind (the `schema` Brief §B), so a test that could name it would be able to
// write a combination the store is supposed to make unexpressible.
func (h *harness) write(node string, kind ContentKind, data []byte) Event {
	h.t.Helper()
	return h.commitWith(func(_ context.Context, tx *Tx) ([]Draft, error) {
		draft, err := tx.ContentDraft(node, kind, data)
		if err != nil {
			return nil, err
		}
		return []Draft{draft}, nil
	})[0]
}

// writeError is write expecting a refusal.
func (h *harness) writeError(node string, kind ContentKind, data []byte) error {
	h.t.Helper()
	return h.commitError(func(_ context.Context, tx *Tx) ([]Draft, error) {
		draft, err := tx.ContentDraft(node, kind, data)
		if err != nil {
			return nil, err
		}
		return []Draft{draft}, nil
	})
}

// wantContent asserts a node's whole content of one kind is byte-identical to
// what was written, through the data-access layer that resolves in-store bytes
// and sidecar files uniformly.
func (h *harness) wantContent(what, node string, kind ContentKind, want []byte) {
	h.t.Helper()
	got, err := h.Content(h.ctx, node, kind)
	if err != nil {
		h.t.Errorf("%s: read %s content: %v", what, kind, err)
		return
	}
	if !bytes.Equal(got, want) {
		h.t.Errorf("%s: %s content is %d bytes (%s), want %d bytes (%s)",
			what, kind, len(got), abbreviate(got), len(want), abbreviate(want))
	}
}

// abbreviate renders content for a failure message without pasting a mebibyte
// into the test log.
func abbreviate(data []byte) string {
	const limit = 48
	if len(data) <= limit {
		return strconv.Quote(string(data))
	}
	return strconv.Quote(string(data[:limit])) + "..."
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// lifecycleVerb is one of the five moves a node can make. The lifecycle is
// uniform at every scale (MODEL §2.2), so a test names the move and the harness
// picks the token — which is also what lets one test body run at all three
// scales.
type lifecycleVerb string

const (
	verbStart  lifecycleVerb = "start"
	verbFinish lifecycleVerb = "finish"
	verbCancel lifecycleVerb = "cancel"
	verbPause  lifecycleVerb = "pause"
	verbResume lifecycleVerb = "resume"
)

// lifecycleMove is what one verb does at one scale: the registered event type and
// the state it lands in.
type lifecycleMove struct {
	Type string
	To   Lifecycle
}

// lifecycleTaxonomy spells the fifteen lifecycle events out with their own
// constants rather than assembling `kind + "." + verb`, so a test drives a
// registered token and never a string that merely looks like one. It is keyed by
// verb and not by target state, because start and resume both land in-progress.
var lifecycleTaxonomy = map[lifecycleVerb]map[Scale]lifecycleMove{
	verbStart: {
		ScaleMatter: {TypeMatterStarted, InProgress},
		ScaleStage:  {TypeStageStarted, InProgress},
		ScaleStep:   {TypeStepStarted, InProgress},
	},
	verbFinish: {
		ScaleMatter: {TypeMatterFinished, Done},
		ScaleStage:  {TypeStageFinished, Done},
		ScaleStep:   {TypeStepFinished, Done},
	},
	verbCancel: {
		ScaleMatter: {TypeMatterCanceled, Canceled},
		ScaleStage:  {TypeStageCanceled, Canceled},
		ScaleStep:   {TypeStepCanceled, Canceled},
	},
	verbPause: {
		ScaleMatter: {TypeMatterPaused, Paused},
		ScaleStage:  {TypeStagePaused, Paused},
		ScaleStep:   {TypeStepPaused, Paused},
	},
	verbResume: {
		ScaleMatter: {TypeMatterResumed, InProgress},
		ScaleStage:  {TypeStageResumed, InProgress},
		ScaleStep:   {TypeStepResumed, InProgress},
	},
}

// transitionDraft is one lifecycle draft with an explicit From — the shape the
// tests that describe a transition which could not have happened need, since From
// is what applyTransition guards on.
func (h *harness) transitionDraft(node string, kind Scale, verb lifecycleVerb, from Lifecycle) Draft {
	h.t.Helper()
	move, ok := lifecycleTaxonomy[verb][kind]
	if !ok {
		h.t.Fatalf("no %s event at %s scale", verb, kind)
	}
	return Draft{Type: move.Type, Subject: node, Payload: Transition{From: from, To: move.To}}
}

// move performs one lifecycle transition, reading the node's own kind and current
// state rather than being told them: the From the event carries is then the state
// the projection is actually in, which is the only way a transition is supposed
// to be written.
func (h *harness) move(node string, verb lifecycleVerb) Event {
	h.t.Helper()
	n, err := h.Node(h.ctx, node)
	if err != nil {
		h.t.Fatalf("read %s before %sing it: %v", node, verb, err)
	}
	return h.commit(h.transitionDraft(node, n.Kind, verb, n.Lifecycle))[0]
}

func (h *harness) start(node string) Event  { return h.move(node, verbStart) }
func (h *harness) finish(node string) Event { return h.move(node, verbFinish) }
func (h *harness) cancel(node string) Event { return h.move(node, verbCancel) }
func (h *harness) pause(node string) Event  { return h.move(node, verbPause) }
func (h *harness) resume(node string) Event { return h.move(node, verbResume) }

// closeGate closes a gate against a node. Closing is the only gate event in P1;
// declaring one is config and emits nothing (D4, D54).
func (h *harness) closeGate(node, gate string, scale Scale) Event {
	h.t.Helper()
	return h.commit(Draft{Type: TypeGateClosed, Subject: node, Payload: GateClosed{Gate: gate, Scale: scale}})[0]
}

// wantLifecycle asserts a node is in one state, through the read surface.
func (h *harness) wantLifecycle(node string, want Lifecycle) {
	h.t.Helper()
	n, err := h.Node(h.ctx, node)
	if err != nil {
		h.t.Fatalf("read %s: %v", node, err)
	}
	if n.Lifecycle != want {
		h.t.Errorf("%s (%s) is %s, want %s", n.Locator, n.Kind, n.Lifecycle, want)
	}
}

// ---------------------------------------------------------------------------
// Assertions shared across Steps
// ---------------------------------------------------------------------------

// rawExec runs SQL straight at the database, around the API. Several Steps'
// assertions are about what the *substrate* refuses — append-only, the
// projection guards — and those cannot be demonstrated through a write path
// designed to make them unreachable.
func (h *harness) rawExec(query string, args ...any) error {
	h.t.Helper()
	_, err := h.db.ExecContext(h.ctx, query, args...)
	return err
}

// rawRefusedBy runs SQL around the API expecting the substrate to turn it down,
// and holds the refusal to naming the guard that fired: otherwise the assertion
// is passing on somebody else's constraint.
func rawRefusedBy(h *harness, what, want, query string, args ...any) {
	h.t.Helper()
	err := h.rawExec(query, args...)
	if err == nil {
		h.t.Errorf("%s: the database allowed it, want a refusal", what)
		return
	}
	if !strings.Contains(err.Error(), want) {
		h.t.Errorf("%s: refused with %v, want a refusal mentioning %q", what, err, want)
	}
}

// refusalMentions holds an API-level refusal to naming its reason, for the same
// reason rawRefusedBy does.
func refusalMentions(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: succeeded, want a refusal", what)
		return
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: refused with %v, want a refusal mentioning %q", what, err, want)
	}
}

// ---------------------------------------------------------------------------
// Reading the log back
// ---------------------------------------------------------------------------

// eventsOf is every event ever written about one subject, in order.
//
// It resolves by identity, which is the whole reason a Repo that adopted a remote
// and a Step that was renamed or removed lose no history (MODEL §10) — so it is
// also the assertion behind those claims.
func (h *harness) eventsOf(subject string) []Event {
	h.t.Helper()
	events, err := h.EventsOfSubject(h.ctx, subject)
	if err != nil {
		h.t.Fatalf("read the history of %s: %v", subject, err)
	}
	return events
}

// birthEventOf is the first event ever written about a subject — the one whose id
// its projection row carries as birth_event.
func (h *harness) birthEventOf(subject string) Event {
	h.t.Helper()
	events := h.eventsOf(subject)
	if len(events) == 0 {
		h.t.Fatalf("%s has no history at all", subject)
	}
	return events[0]
}

// ---------------------------------------------------------------------------
// Reading a projection row column for column
// ---------------------------------------------------------------------------

// columnsOf is a table's columns in declaration order, read from the database and
// never from a list written in a test. Every column-for-column assertion below
// goes through it, so a column a later migration adds cannot escape one.
func (h *harness) columnsOf(table string) []string {
	h.t.Helper()
	rows, err := h.db.QueryContext(h.ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		h.t.Fatalf("read the columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			h.t.Fatalf("read the columns of %s: %v", table, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read the columns of %s: %v", table, err)
	}
	if len(out) == 0 {
		h.t.Fatalf("%s has no columns; is it a table in this store?", table)
	}
	return out
}

// sqlLit renders a Go value as the literal SQLite's own quote() produces for it.
//
// One rendering serves both the row assertions and the rebuild snapshot: the
// expectation is written in Go, the database renders itself, and NULL, the empty
// string and the four characters "NULL" stay three different answers instead of
// collapsing into one — which matters more here than anywhere, because half of
// D37's contract is about a nullable natural key.
func sqlLit(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case nil:
		return "NULL"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case []byte:
		return "X'" + strings.ToUpper(hex.EncodeToString(x)) + "'"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	default:
		// Lifecycle, Scale, Provenance, CloseReason and ContentKind are named
		// string types in Go and plain text to the database.
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.String {
			return sqlLit(t, rv.String())
		}
		t.Fatalf("no SQL literal for %T", v)
		return ""
	}
}

// rowsOf reads every matching row of a table, each as column name -> the
// database's own rendering of the value, ordered by every column so two reads of
// one table agree.
func (h *harness) rowsOf(table, where string, args ...any) []map[string]string {
	h.t.Helper()
	cols := h.columnsOf(table)
	sel := make([]string, len(cols))
	order := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = `quote("` + c + `")`
		order[i] = strconv.Itoa(i + 1)
	}
	//nolint:gosec // the table and its columns come from the database itself
	query := `SELECT ` + strings.Join(sel, ", ") + ` FROM "` + table + `"`
	if where != "" {
		query += ` WHERE ` + where
	}
	query += ` ORDER BY ` + strings.Join(order, ", ")

	rows, err := h.db.QueryContext(h.ctx, query, args...)
	if err != nil {
		h.t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []map[string]string
	for rows.Next() {
		cells := make([]any, len(cols))
		values := make([]string, len(cols))
		for i := range cells {
			cells[i] = &values[i]
		}
		if err := rows.Scan(cells...); err != nil {
			h.t.Fatalf("read %s: %v", table, err)
		}
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			row[c] = values[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read %s: %v", table, err)
	}
	return out
}

// rowOf is rowsOf where exactly one row is expected.
func (h *harness) rowOf(table, where string, args ...any) map[string]string {
	h.t.Helper()
	rows := h.rowsOf(table, where, args...)
	if len(rows) != 1 {
		h.t.Fatalf("%s has %d rows matching %q, want exactly 1", table, len(rows), where)
	}
	return rows[0]
}

// wantRow asserts one projection row is exactly what the event should have
// projected — every column of it.
//
// want must name every column the table has. That strictness is the whole point:
// it is what makes "column by column" a claim the test keeps rather than a claim
// it makes about the columns somebody happened to think of, and it means a column
// a later migration adds fails here until somebody says what it holds.
func (h *harness) wantRow(what, table, where string, args []any, want map[string]any) {
	h.t.Helper()
	rows := h.rowsOf(table, where, args...)
	if len(rows) != 1 {
		h.t.Fatalf("%s: %s has %d rows matching %s, want exactly 1", what, table, len(rows), where)
	}
	got := rows[0]
	for _, col := range h.columnsOf(table) {
		w, named := want[col]
		if !named {
			h.t.Errorf("%s: %s.%s is %s and the test says nothing about it", what, table, col, got[col])
			continue
		}
		if lit := sqlLit(h.t, w); got[col] != lit {
			h.t.Errorf("%s: %s.%s = %s, want %s", what, table, col, got[col], lit)
		}
	}
	for col := range want {
		if _, ok := got[col]; !ok {
			h.t.Errorf("%s: the test names %s.%s, which is not a column of %s", what, table, col, table)
		}
	}
}

// wantRowCount asserts how many rows a table holds under a condition — the
// assertion behind "one row moving forward, not two" (D58) and behind a refused
// write having left nothing behind.
func (h *harness) wantRowCount(what, table, where string, args []any, want int) {
	h.t.Helper()
	if got := len(h.rowsOf(table, where, args...)); got != want {
		h.t.Errorf("%s: %s has %d rows matching %q, want %d", what, table, got, where, want)
	}
}
