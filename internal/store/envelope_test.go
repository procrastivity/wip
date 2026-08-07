package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// Tests for the eleven-column event envelope (MODEL §10, the `schema` Brief §A).
//
// The envelope is the one part of the store that never branches: later phases add
// event *types*, and a column arriving as an increment would be exactly the
// retrofit "later phases add types; the envelope never changes" exists to
// prevent. So nothing here tests a verb — it tests the shape.
//
// Half of these assertions go *around* the API on purpose. Append-only,
// strictly-ascending ids, never-points-forward causation and D56's dimension rule
// are claims about the substrate, and a write path built to make them unreachable
// cannot demonstrate them. Raw-SQL bypass around the API was the losing spike's
// single best technique (docs/store-fork), and this is that technique applied to
// the envelope.

// ---------------------------------------------------------------------------
// Helpers these tests share
// ---------------------------------------------------------------------------

// renderDraft is the cheapest well-formed event in the P1 taxonomy: an execution
// event that projects nothing at all, because a render is a projection of the
// store onto disk that the store never reads back (D36, D40). Tests that are
// about the envelope and not about any projection use it, so a refusal is always
// attributable to the envelope.
func renderDraft(subject string) Draft {
	return Draft{
		Type:    TypeRenderPerformed,
		Subject: subject,
		Payload: RenderPerformed{Target: "scratch"},
	}
}

// rawRow is one `events` row as the database sees it: eleven values, the three
// tier dimensions nullable. Nothing in the API can express most of these rows —
// that is the point — so a test that holds the substrate to the contract builds
// one from an event the store really wrote and breaks exactly the field it is
// about.
type rawRow struct {
	id, eventType, occurredAt, actor string
	causation, correlation           string
	repo, clone, worktree            any
	subject, payload                 string
}

// rawRowFrom renders a real event as a raw row with a fresh id above everything
// in the log, that id's own timestamp, and self-referential chain pointers — an
// origin the store itself could have written, which the substrate therefore has
// no reason to refuse until a test spoils exactly one field.
// TestARawRowIsRefusedOnlyForTheFieldUnderTest is the control that says so.
func (h *harness) rawRowFrom(ev Event) rawRow {
	h.t.Helper()
	id := h.NewID()
	at, err := timeOfID(id)
	if err != nil {
		h.t.Fatalf("read the timestamp of %s: %v", id, err)
	}
	return rawRow{
		id:          id,
		eventType:   ev.Type,
		occurredAt:  at.UTC().Format(timestampLayout),
		actor:       string(ev.Actor),
		causation:   id,
		correlation: id,
		repo:        nullable(ev.Repo),
		clone:       nullable(ev.Clone),
		worktree:    nullable(ev.Worktree),
		subject:     ev.Subject,
		payload:     string(ev.Payload),
	}
}

const rawInsert = `INSERT INTO events (id, type, occurred_at, actor, causation, correlation,
                     repo, clone, worktree, subject, payload)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// args is the row in the order the INSERT names its columns.
func (r rawRow) args() []any {
	return []any{
		r.id, r.eventType, r.occurredAt, r.actor, r.causation, r.correlation,
		r.repo, r.clone, r.worktree, r.subject, r.payload,
	}
}

// insert puts the row in, around every guard the API provides.
func (r rawRow) insert(h *harness) error {
	h.t.Helper()
	return h.rawExec(rawInsert, r.args()...)
}

// refused asserts the substrate turns this row down, and names the guard.
func (r rawRow) refused(h *harness, what, want string) {
	h.t.Helper()
	rawRefusedBy(h, what, want, rawInsert, r.args()...)
}

// dimensionsOf reads the three tier columns with their NULLs intact. The read
// surface deliberately flattens them (View.eventList COALESCEs a null dimension
// into an empty string), so "clone is null" is an assertion that has to go round
// it.
func (h *harness) dimensionsOf(id string) (repo, clone, worktree sql.NullString) {
	h.t.Helper()
	if err := h.db.QueryRowContext(h.ctx,
		`SELECT repo, clone, worktree FROM events WHERE id = ?`, id).
		Scan(&repo, &clone, &worktree); err != nil {
		h.t.Fatalf("read the dimensions of %s: %v", id, err)
	}
	return repo, clone, worktree
}

// wantDimensions asserts one event's tier columns are exactly the D56 rule for
// its type: present-and-equal, or null.
func (h *harness) wantDimensions(ev Event, repo, clone, worktree string) {
	h.t.Helper()
	got := [3]sql.NullString{}
	got[0], got[1], got[2] = h.dimensionsOf(ev.ID)
	for i, w := range [3]struct {
		name string
		want string
	}{{"repo", repo}, {"clone", clone}, {"worktree", worktree}} {
		switch {
		case w.want == "" && got[i].Valid:
			h.t.Errorf("%s: %s = %q, want NULL", ev.Type, w.name, got[i].String)
		case w.want != "" && !got[i].Valid:
			h.t.Errorf("%s: %s is NULL, want %s", ev.Type, w.name, w.want)
		case w.want != "" && got[i].String != w.want:
			h.t.Errorf("%s: %s = %s, want %s", ev.Type, w.name, got[i].String, w.want)
		}
	}
}

// idBelow is the identity immediately below id: the same 128-bit value minus one,
// rendered back into Crockford base32. It exists so a raw insert can carry an id
// that is unambiguously below MAX(id) and collides with no primary key — the only
// way to make the monotonic trigger the thing that refuses, rather than
// uniqueness.
func idBelow(t *testing.T, id string) string {
	t.Helper()
	b := []byte(id)
	for i := len(b) - 1; i >= 0; i-- {
		if v := crockfordValue(b[i]); v > 0 {
			b[i] = crockford[v-1]
			for j := i + 1; j < len(b); j++ {
				b[j] = crockford[len(crockford)-1]
			}
			return string(b)
		}
	}
	t.Fatalf("%q is the lowest identity there is; nothing sorts below it", id)
	return ""
}

// ---------------------------------------------------------------------------
// 0. The control
// ---------------------------------------------------------------------------

// TestARawRowIsRefusedOnlyForTheFieldUnderTest is the control the ten tests below
// depend on. Each of them builds a raw `events` row from an event the store
// really wrote, breaks one field, and asserts the substrate turns it down; that
// only means anything if the *unbroken* row would have been accepted. Otherwise
// every one of those tests could be passing on a constraint nobody was testing.
func TestARawRowIsRefusedOnlyForTheFieldUnderTest(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("control", "The control")
	// render.performed projects nothing at all (D36, D40), so a row inserted around
	// the API leaves no projection to disagree with the log.
	template := h.commit(renderDraft(matter))[0]

	before, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}

	row := h.rawRowFrom(template)
	if err := row.insert(h); err != nil {
		t.Fatalf("the substrate refused a row the store itself could have written: %v", err)
	}

	after, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("the log holds %d events, want %d", len(after), len(before)+1)
	}
	landed := after[len(after)-1]
	if landed.ID != row.id {
		t.Errorf("the raw row landed as %s, want %s", landed.ID, row.id)
	}
	if !landed.IsOrigin() {
		t.Errorf("%s was written self-referential but IsOrigin() is false", landed.ID)
	}
}

// ---------------------------------------------------------------------------
// 1. Append-only
// ---------------------------------------------------------------------------

// TestEventsAreAppendOnly holds the log's central property to the substrate
// rather than to the discipline of the code above it. There is no update or
// delete path in this package; the point of the triggers is that there is none
// outside it either.
func TestEventsAreAppendOnly(t *testing.T) {
	h := newHarness(t)

	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(log) == 0 {
		t.Fatal("the harness wrote no events")
	}
	first := log[0]

	rawRefusedBy(h, "UPDATE of one event", "append-only",
		`UPDATE events SET actor = 'role:builder' WHERE id = ?`, first.ID)
	rawRefusedBy(h, "UPDATE of every event", "append-only",
		`UPDATE events SET payload = '{}'`)
	rawRefusedBy(h, "DELETE of one event", "append-only",
		`DELETE FROM events WHERE id = ?`, first.ID)
	rawRefusedBy(h, "DELETE of the whole log", "append-only",
		`DELETE FROM events`)

	after, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(after) != len(log) {
		t.Errorf("the log holds %d events after four refused writes, want %d", len(after), len(log))
	}
	if after[0].Actor != first.Actor || string(after[0].Payload) != string(first.Payload) {
		t.Errorf("event %s changed under a refused UPDATE", first.ID)
	}
}

// ---------------------------------------------------------------------------
// 2. Ids strictly ascend
// ---------------------------------------------------------------------------

// TestEventIdsAscendStrictly is the floor under the whole design: the id doubles
// as the total-order key (D44, D51), so there is no sequence column and nothing
// to reconcile — provided ids never repeat and never go backwards, within one
// command and across commands alike.
func TestEventIdsAscendStrictly(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("ordering", "Ids ascend")
	batch := h.commitWith(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{
			renderDraft(matter), renderDraft(matter), renderDraft(matter),
		}, nil
	})
	for i := 1; i < len(batch); i++ {
		if batch[i-1].ID >= batch[i].ID {
			t.Errorf("within one command, event %d id %s does not sort above event %d id %s",
				i, batch[i].ID, i-1, batch[i-1].ID)
		}
	}

	// The harness's bootstrap, the Matter, and the three-draft command above are
	// separate commands; the log as a whole is one strictly ascending sequence.
	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if len(log) < 6 {
		t.Fatalf("the log holds %d events, want at least 6", len(log))
	}
	for i := 1; i < len(log); i++ {
		if log[i-1].ID >= log[i].ID {
			t.Errorf("across commands, event %s does not sort above %s", log[i].ID, log[i-1].ID)
		}
	}

	// And an id below the high-water mark is refused by the substrate, not merely
	// unreachable through the API. The row is otherwise a copy of a real event, so
	// only the id is wrong.
	row := h.rawRowFrom(log[len(log)-1])
	below := idBelow(t, log[0].ID)
	row.id, row.causation, row.correlation = below, below, below
	row.refused(h, "an insert below MAX(id)", "ascend strictly")
}

// ---------------------------------------------------------------------------
// 3. Causation and correlation
// ---------------------------------------------------------------------------

// TestALinearCascadePointsAtItsPredecessor is the a:a:a / b:a:a / c:b:a form.
//
// The story is D57's cascade: `start` on a Planned Step entails starting its
// Stage and its Matter, and each of those entailed the next. Correlation names
// the origin throughout, so the whole command is one chain; causation names the
// event that actually entailed this one, so the chain has a shape and not just a
// membership.
func TestALinearCascadePointsAtItsPredecessor(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("cascade", "A linear cascade")
	stage := h.stage(matter, "stage-1", "The stage")
	step := h.step(stage, "step-01", "The step")

	// The actor is the requesting human on all three, cascade or not: a command is
	// a request and an act within the authority it granted is attributed to the
	// principal. `payload.cascade` is where "the human never named this node"
	// lives. See docs/schema/decisions.md.
	events := h.commit(
		Draft{
			Type: TypeMatterStarted, Subject: matter,
			Payload: Transition{From: Planned, To: InProgress, Cascade: true},
		},
		Draft{
			Type: TypeStageStarted, Subject: stage, Cause: 0,
			Payload: Transition{From: Planned, To: InProgress, Cascade: true},
		},
		Draft{
			Type: TypeStepStarted, Subject: step, Cause: 1,
			Payload: Transition{From: Planned, To: InProgress},
		},
	)
	if len(events) != 3 {
		t.Fatalf("the command wrote %d events, want 3", len(events))
	}
	a, b, c := events[0], events[1], events[2]

	// a:a:a — an origin self-references rather than carrying null, so "began its
	// own chain" and "nobody filled this in" are never the same value.
	if a.Causation != a.ID || a.Correlation != a.ID {
		t.Errorf("origin %s is %s:%s, want a:a:a", a.ID, a.Causation, a.Correlation)
	}
	// b:a:a
	if b.Causation != a.ID || b.Correlation != a.ID {
		t.Errorf("second event %s is %s:%s, want %s:%s", b.ID, b.Causation, b.Correlation, a.ID, a.ID)
	}
	// c:b:a — the cascade's shape: the predecessor, not the origin.
	if c.Causation != b.ID {
		t.Errorf("third event %s names cause %s, want its predecessor %s", c.ID, c.Causation, b.ID)
	}
	if c.Correlation != a.ID {
		t.Errorf("third event %s names correlation %s, want the origin %s", c.ID, c.Correlation, a.ID)
	}

	if !a.IsOrigin() {
		t.Errorf("%s began the chain but IsOrigin() is false", a.ID)
	}
	for _, ev := range []Event{b, c} {
		if ev.IsOrigin() {
			t.Errorf("%s was caused by %s but IsOrigin() is true", ev.ID, ev.Causation)
		}
	}

	// The whole command is recoverable from the origin's id, which is the reason
	// correlation is a column at all.
	chain, err := h.EventsOfChain(h.ctx, a.ID)
	if err != nil {
		t.Fatalf("read the chain of %s: %v", a.ID, err)
	}
	if len(chain) != 3 {
		t.Fatalf("the chain of %s holds %d events, want 3", a.ID, len(chain))
	}
	for i, want := range []Event{a, b, c} {
		if chain[i].ID != want.ID {
			t.Errorf("chain position %d is %s, want %s", i, chain[i].ID, want.ID)
		}
	}
	// And from any member of it, because correlation never points forward.
	fromMiddle, err := h.EventsOfChain(h.ctx, c.Correlation)
	if err != nil {
		t.Fatalf("read the chain from %s: %v", c.ID, err)
	}
	if len(fromMiddle) != 3 {
		t.Errorf("the chain read from %s holds %d events, want 3", c.ID, len(fromMiddle))
	}
}

// TestUnrelatedSiblingsPointAtTheOrigin is the a:a:a / b:a:a / c:a:a form: three
// events of one command with no causal relation to each other. They share a chain
// because they share a command, and each names the origin as its cause because
// nothing else entailed it.
func TestUnrelatedSiblingsPointAtTheOrigin(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("siblings", "Unrelated siblings")
	events := h.commitWith(func(_ context.Context, tx *Tx) ([]Draft, error) {
		drafts := make([]Draft, 0, 3)
		for i, title := range []string{"First", "Second", "Third"} {
			drafts = append(drafts, Draft{
				// Cause is left at its zero value on purpose: the origin is the
				// default cause, which is exactly this form.
				Type:    TypeStepCreated,
				Subject: tx.NewID(),
				Payload: NodeBirth{
					Title:   title,
					Locator: fmt.Sprintf("step-%02d", i+1),
					Parent:  matter,
					SortKey: int64(i+1) * sortKeyGap,
				},
			})
		}
		return drafts, nil
	})
	if len(events) != 3 {
		t.Fatalf("the command wrote %d events, want 3", len(events))
	}
	a := events[0]

	if !a.IsOrigin() {
		t.Errorf("origin %s is %s:%s, want a:a:a", a.ID, a.Causation, a.Correlation)
	}
	for i, ev := range events[1:] {
		if ev.Causation != a.ID || ev.Correlation != a.ID {
			t.Errorf("sibling %d (%s) is %s:%s, want %s:%s",
				i+1, ev.ID, ev.Causation, ev.Correlation, a.ID, a.ID)
		}
		if ev.IsOrigin() {
			t.Errorf("sibling %s is not the origin but IsOrigin() is true", ev.ID)
		}
	}

	chain, err := h.EventsOfChain(h.ctx, a.ID)
	if err != nil {
		t.Fatalf("read the chain of %s: %v", a.ID, err)
	}
	if len(chain) != 3 {
		t.Errorf("the chain of %s holds %d events, want 3", a.ID, len(chain))
	}
}

// ---------------------------------------------------------------------------
// 4. A cause never points forward
// ---------------------------------------------------------------------------

// TestACauseNeverPointsForward closes the door from both sides. The API cannot
// express a forward pointer because a Draft names a cause by index among earlier
// drafts; the substrate refuses one anyway, so a log written by something other
// than this package is still a log whose chains can be walked backwards.
func TestACauseNeverPointsForward(t *testing.T) {
	h := newHarness(t)

	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	template := log[len(log)-1]

	// Two fresh ids, in order: the row takes the lower and points at the higher.
	// Everything else is copied from a real event.
	earlier, later := h.NewID(), h.NewID()

	forwardCausation := h.rawRowFrom(template)
	forwardCausation.id, forwardCausation.correlation = earlier, earlier
	forwardCausation.causation = later
	forwardCausation.refused(h, "causation naming a later event", "never name a later event")

	forwardCorrelation := h.rawRowFrom(template)
	forwardCorrelation.id, forwardCorrelation.causation = earlier, earlier
	forwardCorrelation.correlation = later
	forwardCorrelation.refused(h, "correlation naming a later event", "never name a later event")

	// And through the API: a draft may only name a cause that is already an event
	// of this command.
	matter := h.matter("forward", "No forward causes")
	refusalMentions(t, "a draft naming itself as its cause",
		h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			second := renderDraft(matter)
			second.Cause = 1
			return []Draft{renderDraft(matter), second}, nil
		}),
		"not an earlier event in this command")

	refusalMentions(t, "a draft naming a later draft as its cause",
		h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			second := renderDraft(matter)
			second.Cause = 2
			return []Draft{renderDraft(matter), second, renderDraft(matter)}, nil
		}),
		"not an earlier event in this command")

	refusalMentions(t, "a draft naming a cause out of range",
		h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			second := renderDraft(matter)
			second.Cause = -1
			return []Draft{renderDraft(matter), second}, nil
		}),
		"not an earlier event in this command")
}

// ---------------------------------------------------------------------------
// 5. Actor
// ---------------------------------------------------------------------------

// TestActorIsNeverEmptyAndAlwaysPrefixed holds the attribution column to its
// contract. It is one open-ended column and not an enum, which is what lets P2
// add roles with no migration — so the check is the prefix and not the
// membership.
func TestActorIsNeverEmptyAndAlwaysPrefixed(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("actors", "Actors are prefixed")

	commitAs := func(actor Actor) ([]Event, error) {
		return h.Commit(h.ctx,
			Request{Actor: actor, Env: Env{Repo: h.Repo, Clone: h.Clone, Worktree: h.Worktree}},
			func(_ context.Context, _ *Tx) ([]Draft, error) {
				return []Draft{renderDraft(matter)}, nil
			})
	}

	for _, bad := range []Actor{"", "beau", "role:", "system:", "Human", "unknown:x"} {
		_, err := commitAs(bad)
		refusalMentions(t, fmt.Sprintf("actor %q", bad), err, "is not an actor")
	}

	for _, good := range []Actor{ActorHuman, RoleActor("builder"), SystemActor("ci")} {
		events, err := commitAs(good)
		if err != nil {
			t.Fatalf("actor %q: %v", good, err)
		}
		if events[0].Actor != good {
			t.Errorf("stamped actor %q, want %q", events[0].Actor, good)
		}
		var stored string
		if err := h.db.QueryRowContext(h.ctx,
			`SELECT actor FROM events WHERE id = ?`, events[0].ID).Scan(&stored); err != nil {
			t.Fatalf("read the actor of %s: %v", events[0].ID, err)
		}
		if stored != string(good) {
			t.Errorf("stored actor %q, want %q", stored, good)
		}
	}

	// The column carries the same rule, so a log written around this package is
	// still attributable.
	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	template := log[len(log)-1]
	for _, bad := range []string{"", "beau", "role:", "system:"} {
		row := h.rawRowFrom(template)
		row.actor = bad
		row.refused(h, fmt.Sprintf("a raw insert with actor %q", bad), "CHECK constraint failed")
	}
}

// ---------------------------------------------------------------------------
// 6. occurred_at
// ---------------------------------------------------------------------------

// TestOccurredAtIsTheIdsOwnTimestamp is `store-fork`'s "two clocks" finding
// closed with a rule rather than a twelfth column: occurred_at is *derived* from
// the event's own ULID, so the two clocks are one clock by construction and there
// is no recorded_at to reconcile.
func TestOccurredAtIsTheIdsOwnTimestamp(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("clocks", "One clock")
	written := h.commit(renderDraft(matter), renderDraft(matter), renderDraft(matter))

	for _, ev := range written {
		want, err := timeOfID(ev.ID)
		if err != nil {
			t.Fatalf("read the timestamp of %s: %v", ev.ID, err)
		}
		if !ev.OccurredAt.Equal(want) {
			t.Errorf("%s occurred_at = %s, want its id's own timestamp %s",
				ev.ID, ev.OccurredAt.Format(timestampLayout), want.Format(timestampLayout))
		}
	}

	// The stored text is fixed-width RFC3339 UTC to the millisecond, so lexical
	// order is chronological order — and it round-trips through the same layout it
	// was written with.
	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	for _, ev := range log {
		want, err := timeOfID(ev.ID)
		if err != nil {
			t.Fatalf("read the timestamp of %s: %v", ev.ID, err)
		}
		if !ev.OccurredAt.Equal(want) {
			t.Errorf("%s read back occurred_at = %s, want %s",
				ev.ID, ev.OccurredAt.Format(timestampLayout), want.Format(timestampLayout))
		}

		var stored string
		if err := h.db.QueryRowContext(h.ctx,
			`SELECT occurred_at FROM events WHERE id = ?`, ev.ID).Scan(&stored); err != nil {
			t.Fatalf("read the timestamp column of %s: %v", ev.ID, err)
		}
		if got := want.UTC().Format(timestampLayout); stored != got {
			t.Errorf("%s stores occurred_at %q, want %q", ev.ID, stored, got)
		}
		if len(stored) != len("2006-01-02T15:04:05.000Z") {
			t.Errorf("%s stores occurred_at %q, which is not fixed width", ev.ID, stored)
		}
		if stored[len(stored)-1] != 'Z' {
			t.Errorf("%s stores occurred_at %q, which is not UTC", ev.ID, stored)
		}
	}
	for i := 1; i < len(log); i++ {
		var previous, current string
		if err := h.db.QueryRowContext(h.ctx,
			`SELECT occurred_at FROM events WHERE id = ?`, log[i-1].ID).Scan(&previous); err != nil {
			t.Fatalf("read occurred_at: %v", err)
		}
		if err := h.db.QueryRowContext(h.ctx,
			`SELECT occurred_at FROM events WHERE id = ?`, log[i].ID).Scan(&current); err != nil {
			t.Fatalf("read occurred_at: %v", err)
		}
		if previous > current {
			t.Errorf("occurred_at %q sorts above %q, but their ids sort the other way",
				previous, current)
		}
	}

	// A malformed timestamp is refused by the column, which is what makes the
	// lexical-order claim safe.
	template := log[len(log)-1]
	for _, bad := range []string{
		"", "not a time", "2026-07-29T12:00:00Z", "2026-07-29T12:00:00.000+02:00",
		"2026-07-29 12:00:00.000Z",
	} {
		row := h.rawRowFrom(template)
		row.occurredAt = bad
		row.refused(h, fmt.Sprintf("a raw insert with occurred_at %q", bad), "CHECK constraint failed")
	}
}

// ---------------------------------------------------------------------------
// 7. Tier dimensions (D56)
// ---------------------------------------------------------------------------

// TestTierDimensionsAreAStaticFunctionOfType is D56, checked three ways: the
// stamped columns, the stamp-time refusal when the command's tier context cannot
// supply a required dimension, and the trigger that refuses a row violating the
// rule regardless of who wrote it.
//
// The rule is total over the taxonomy and lives in `event_types`, so `batch.*`
// carrying a null repo is a row in a table rather than a special case in code: a
// Batch keys at no tier (D39, D56).
func TestTierDimensionsAreAStaticFunctionOfType(t *testing.T) {
	h := newHarness(t)

	// A durable event: repo, and nothing else.
	matter := h.matter("dimensions", "Dimensions are static")
	matterEvents, err := h.EventsOfSubject(h.ctx, matter)
	if err != nil {
		t.Fatalf("read the events of %s: %v", matter, err)
	}
	durableEvent := matterEvents[0]
	h.wantDimensions(durableEvent, h.Repo, "", "")

	// An execution event: all three.
	executionEvent := h.commit(renderDraft(matter))[0]
	h.wantDimensions(executionEvent, h.Repo, h.Clone, h.Worktree)

	// A `batch.*` event: clone and worktree, and a null repo.
	var batchEvent Event
	h.commitWith(func(_ context.Context, tx *Tx) ([]Draft, error) {
		return []Draft{{
			Type:    TypeBatchCreated,
			Subject: tx.NewID(),
			Payload: BatchCreated{Name: "dimensions"},
		}}, nil
	})
	batchLog, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	batchEvent = batchLog[len(batchLog)-1]
	if batchEvent.Type != TypeBatchCreated {
		t.Fatalf("last event is %s, want %s", batchEvent.Type, TypeBatchCreated)
	}
	h.wantDimensions(batchEvent, "", h.Clone, h.Worktree)

	// stamp refuses a type whose required dimension the Request cannot supply. The
	// question is asked once, here, and never by a verb — a verb that could name a
	// tier dimension is a verb that could get one wrong.
	_, err = h.stamp(Request{Actor: ActorHuman, Env: Env{Clone: h.Clone, Worktree: h.Worktree}},
		Draft{Type: TypeMatterCreated, Subject: matter, Payload: NodeBirth{Title: "x", Locator: "y"}})
	refusalMentions(t, "a durable event with no Repo in its tier context", err, "requires a repo dimension")

	_, err = h.stamp(Request{Actor: ActorHuman, Env: Env{Repo: h.Repo, Worktree: h.Worktree}},
		renderDraft(matter))
	refusalMentions(t, "an execution event with no Clone in its tier context", err, "no Clone")

	_, err = h.stamp(Request{Actor: ActorHuman, Env: Env{Repo: h.Repo, Clone: h.Clone}},
		renderDraft(matter))
	refusalMentions(t, "an execution event with no Worktree in its tier context", err, "no Worktree")

	_, err = h.stamp(Request{Actor: ActorHuman, Env: Env{Repo: h.Repo}},
		Draft{Type: TypeBatchCreated, Subject: matter, Payload: BatchCreated{Matter: matter}})
	refusalMentions(t, "a batch event with no Clone in its tier context", err, "no Clone")

	// And the trigger refuses every way a row can disagree with its type's rule.
	extraClone := h.rawRowFrom(durableEvent)
	extraClone.clone = h.Clone
	extraClone.refused(h, "a durable event carrying a clone", "tier dimensions must match")

	missingRepo := h.rawRowFrom(durableEvent)
	missingRepo.repo = nil
	missingRepo.refused(h, "a durable event with no repo", "tier dimensions must match")

	batchWithRepo := h.rawRowFrom(batchEvent)
	batchWithRepo.repo = h.Repo
	batchWithRepo.refused(h, "a batch event carrying a repo", "tier dimensions must match")

	executionMissingWorktree := h.rawRowFrom(executionEvent)
	executionMissingWorktree.worktree = nil
	executionMissingWorktree.refused(h, "an execution event with no worktree", "tier dimensions must match")
}

// ---------------------------------------------------------------------------
// 8. type is a foreign key into the taxonomy
// ---------------------------------------------------------------------------

// TestEventTypeIsAForeignKeyIntoTheTaxonomy is the taxonomy-as-data arrangement
// working from both ends: requiredDimensions refuses an unregistered type at
// stamp time, and `events.type REFERENCES event_types(type)` refuses one written
// around the API. Adding a type in a later phase is one INSERT inside a numbered
// migration.
func TestEventTypeIsAForeignKeyIntoTheTaxonomy(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("taxonomy", "Types are data")
	refusalMentions(t, "an unregistered event type",
		h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{{Type: "matter.teleported", Subject: matter}}, nil
		}),
		"is not a registered event type")

	// The stamp-time check and the database's are the same rule read from the same
	// list, so neither can drift: this asserts the second half.
	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	row := h.rawRowFrom(log[len(log)-1])
	row.eventType = "matter.teleported"
	row.refused(h, "a raw insert with an unregistered type", "FOREIGN KEY constraint failed")

	// The taxonomy the binary can stamp and the taxonomy the store carries are the
	// same set, which is what verifyTaxonomy checks at open. Assert it here too, so
	// a type added to Go without its migration fails a test and not a user.
	for _, registered := range registeredTypes() {
		var present int
		if err := h.db.QueryRowContext(h.ctx,
			`SELECT COUNT(*) FROM event_types WHERE type = ?`, registered.Type).Scan(&present); err != nil {
			t.Fatalf("look up %s: %v", registered.Type, err)
		}
		if present != 1 {
			t.Errorf("%s is stampable but the store's taxonomy does not carry it", registered.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. subject is an identity
// ---------------------------------------------------------------------------

// TestSubjectMustBeAnIdentity holds MODEL §10's identity-not-locator rule at the
// point it would be broken. A locator may appear in a *payload* — it is a fact
// about the write — but what an event references is `subject`, and that is always
// a 26-character identity, so a rename or a removal loses no history.
func TestSubjectMustBeAnIdentity(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("subjects", "Subjects are identities")

	for _, bad := range []string{"", "step-01", "the-matter-slug", matter + "X", matter[:IDLen-1]} {
		_, err := h.stamp(h.req(), renderDraft(bad))
		refusalMentions(t, fmt.Sprintf("subject %q", bad), err, "which is not an identity")
	}

	// Through the write path too, since stamp is the only constructor.
	refusalMentions(t, "a draft naming a locator as its subject",
		h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
			return []Draft{renderDraft("step-01")}, nil
		}),
		"which is not an identity")

	// And the column, for a log written around this package.
	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	row := h.rawRowFrom(log[len(log)-1])
	row.subject = "step-01"
	row.refused(h, "a raw insert whose subject is a locator", "CHECK constraint failed")
}

// TestIdentitiesAreCrockfordBase32 is the other half of "26 characters" being
// identity-hood. occurred_at is *derived* from the id rather than read from a
// second clock — the store's answer to `store-fork`'s two-clocks finding — so an
// id that is the right length but outside the alphabet would be an event no
// reader could date, and a length check alone would let it in.
//
// causation and correlation need no case of their own: they are foreign keys into
// events(id), so they can only ever name something this rule already admitted.
func TestIdentitiesAreCrockfordBase32(t *testing.T) {
	h := newHarness(t)

	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	template := log[len(log)-1]

	// U, I, L and O are the four letters Crockford omits, precisely because they
	// are the ones a human misreads; lowercase is outside the alphabet too. Every
	// case here is 26 characters long, so only the alphabet rule can refuse it —
	// and every one sorts *above* every real identity, because the monotonic
	// trigger fires before any CHECK does and would otherwise be the thing that
	// refused. ('~' is above the whole printable alphabet; the Crockford letters
	// are above the digits a ULID's timestamp half begins with.)
	wholly := []string{
		strings.Repeat("U", IDLen),
		strings.Repeat("I", IDLen),
		strings.Repeat("L", IDLen),
		strings.Repeat("O", IDLen),
		strings.Repeat("a", IDLen),
		strings.Repeat("~", IDLen),
	}
	// timeOfID is what the rule protects, and it cannot date any of those.
	for _, bad := range wholly {
		if _, err := timeOfID(bad); err == nil {
			t.Errorf("timeOfID dated %q, so this case proves nothing", bad)
		}
	}

	// And one whose *timestamp half* is a real id's, so it dates fine and is
	// nevertheless not an identity — the case a length check plus a working
	// timeOfID would both wave through.
	late := h.NewID()
	partly := late[:IDLen-1] + "~"
	if _, err := timeOfID(partly); err != nil {
		t.Fatalf("timeOfID could not date %q, so it is not the case this means to make: %v", partly, err)
	}

	for _, bad := range append(wholly, partly) {
		if len(bad) != IDLen {
			t.Fatalf("%q is %d characters; this case is only about the alphabet", bad, len(bad))
		}

		asID := h.rawRowFrom(template)
		asID.id, asID.causation, asID.correlation = bad, bad, bad
		asID.refused(h, fmt.Sprintf("a raw insert with id %q", bad), "CHECK constraint failed")

		asSubject := h.rawRowFrom(template)
		asSubject.subject = bad
		asSubject.refused(h, fmt.Sprintf("a raw insert with subject %q", bad), "CHECK constraint failed")
	}

	// The three tier dimensions carry the same rule, so a dimension always names
	// something that could be a tier row. The template is an execution event, which
	// is the one kind that carries all three — a durable event would trip the D56
	// dimension trigger first and prove nothing about the alphabet.
	execution := h.commit(renderDraft(template.Subject))[0]
	if execution.Clone == "" || execution.Worktree == "" {
		t.Fatalf("%s was expected to carry all three dimensions", execution.Type)
	}
	for _, dimension := range []string{"repo", "clone", "worktree"} {
		row := h.rawRowFrom(execution)
		switch dimension {
		case "repo":
			row.repo = strings.Repeat("U", IDLen)
		case "clone":
			row.clone = strings.Repeat("U", IDLen)
		case "worktree":
			row.worktree = strings.Repeat("U", IDLen)
		}
		row.refused(h, "a raw insert with a non-Crockford "+dimension, "CHECK constraint failed")
	}
}

// ---------------------------------------------------------------------------
// 10. payload is a JSON object
// ---------------------------------------------------------------------------

// TestPayloadIsAJsonObject keeps the payload a bag of named type-specific fields
// rather than an arbitrary JSON value. A later phase adding a field to a payload
// is a Go change; a payload that is an array or a scalar has no room for one.
func TestPayloadIsAJsonObject(t *testing.T) {
	h := newHarness(t)

	log, err := h.Events(h.ctx)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	template := log[len(log)-1]

	for _, bad := range []string{
		`[]`, `[{"target":"scratch"}]`, `"scratch"`, `42`, `true`, `null`, ``, `{`,
	} {
		row := h.rawRowFrom(template)
		row.payload = bad
		row.refused(h, fmt.Sprintf("a raw insert with payload %s", bad), "CHECK constraint failed")
	}

	// A draft with no payload at all is the empty object, not a null: the column is
	// NOT NULL and an object, and stamp is what makes that true without every verb
	// remembering it.
	matter := h.matter("payloads", "Payloads are objects")
	ev := h.commit(Draft{Type: TypeRenderPerformed, Subject: matter})[0]
	if got := string(ev.Payload); got != `{}` {
		t.Errorf("a draft with no payload stamped %q, want %q", got, `{}`)
	}
}
