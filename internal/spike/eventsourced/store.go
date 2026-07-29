// Package eventsourced is Spike A of the `store-fork` Matter (MODEL D46):
// the event-sourced side of the fork, implemented against the shared,
// shape-neutral contract in internal/spike/scenario.
//
// The shape, in one sentence: the `events` table is the only authoritative
// thing in the database, every verb is a function from current state to a
// list of events, and everything else — including the `nodes` table that
// answers "what is in progress" — is a projection of the log that can be
// thrown away and rebuilt (see Store.Rebuild).
//
// Nothing here is production code. It exists to produce evidence for a
// decision record; see report.md next to it.
package eventsourced

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/spike/scenario"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is the event-sourced spike store. It satisfies scenario.Store.
type Store struct {
	db  *sql.DB
	env scenario.Env
	ids *scenario.ULIDSource

	// version is the schema version this handle was opened at.
	version int

	// floor is the highest ULID this store has ever seen or issued. The
	// shared source is monotonic within one instance but knows nothing about
	// a previous process, so after a reopen inside the same millisecond it
	// could otherwise mint an id below the last one on disk — which would
	// break the one thing the whole design rests on, an event's identity
	// doubling as the total order (D44, D51).
	floor string
}

// compile-time proof that the spike answers the pinned contract.
var _ scenario.Store = (*Store)(nil)

// Open opens (creating and migrating if absent) the spike store at path, at
// the latest schema version.
func Open(path string, env scenario.Env) (*Store, error) {
	return OpenAt(path, env, LatestVersion)
}

// OpenAt is Open pinned to a specific schema version. It exists so a test can
// populate a database under v1 only and then reopen it at v2 — the migration
// exercise from docs/store-fork/scenario.md.
func OpenAt(path string, env scenario.Env, target int) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("eventsourced: open %s: %w", path, err)
	}
	// Spike-grade: one connection means one writer and no lock contention to
	// reason about. wip is single-user, single-host by refusal (D34).
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("eventsourced: connect %s: %w", path, err)
	}
	version, err := migrate(ctx, db, path, target)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db, env: env, ids: scenario.NewULIDSource(), version: version}
	if err := s.loadFloor(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Version reports the schema version this handle was opened at.
func (s *Store) Version() int { return s.version }

// Close releases the store.
func (s *Store) Close() error { return s.db.Close() }

// loadFloor reads the highest identity already on disk, from either table:
// node ids and event ids come from the same source and share one space.
func (s *Store) loadFloor(ctx context.Context) error {
	var evMax, nodeMax sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&evMax); err != nil {
		return fmt.Errorf("eventsourced: read event high-water mark: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM nodes`).Scan(&nodeMax); err != nil {
		return fmt.Errorf("eventsourced: read node high-water mark: %w", err)
	}
	for _, v := range []sql.NullString{evMax, nodeMax} {
		if v.Valid && v.String > s.floor {
			s.floor = v.String
		}
	}
	return nil
}

// nextID returns the next identity, strictly above everything this store has
// issued or read. See Store.floor.
func (s *Store) nextID() string {
	for {
		id := s.ids.New()
		if id > s.floor {
			s.floor = id
			return id
		}
		// The floor's timestamp cannot be in the future by more than clock
		// skew, so one tick of the millisecond clock is enough to clear it.
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// The one write path
// ---------------------------------------------------------------------------

// draft is a verb's decision: what happened, to whom, and the type-specific
// facts. A verb never sees an id, a timestamp or a tier dimension — those are
// stamped in exactly one place (Store.stamp), which is what makes MODEL §10's
// envelope structurally impossible for a verb to get wrong or forget.
type draft struct {
	typ     string
	subject string
	payload any

	// actor is who performed this particular event. It is per-draft, not
	// per-command, because D57's cascade is precisely the case where one
	// command produces an event nobody asked for: the caller started a Step,
	// and wip started its Matter on its own initiative.
	actor scenario.Actor

	// cause is the index, within this command's drafts, of the draft that
	// entailed this one. Ignored for the first draft, which is the chain
	// origin. A linear cascade points at its predecessor (a:a:a, b:a:a,
	// c:b:a); same-command siblings with no causal relation to each other
	// point at the origin (a:a:a, b:a:a, c:a:a).
	cause int
}

// errNoEvent is the defensive floor under invariant 1 from the other side: a
// verb that mutates nothing must not silently succeed.
var errNoEvent = errors.New("eventsourced: verb produced no event")

// commit is the *only* function in this package that writes. A verb hands it
// a decision function which reads current state and returns the events that
// follow; commit stamps them, appends them, and folds each one into the
// projection — in that order, in one transaction.
//
// Two consequences worth naming, because they are the answer to rubric axis 1:
//   - there is no way to reach the projection except by way of an event, since
//     applyEvent's only argument is an event;
//   - there is no way to append an event without the full envelope, since
//     stamp is the only constructor and it is not optional.
func (s *Store) commit(ctx context.Context, decide func(context.Context, *sql.Tx) ([]draft, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventsourced: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	drafts, err := decide(ctx, tx)
	if err != nil {
		return err
	}
	if len(drafts) == 0 {
		return errNoEvent
	}
	// Causation and correlation are assigned here, never by a verb — the same
	// reason ids and tier dimensions are. A verb knows what entailed what; it
	// does not know the ids, because they do not exist until stamp runs.
	ids := make([]string, len(drafts))
	for i, d := range drafts {
		ev, err := s.stamp(d)
		if err != nil {
			return err
		}
		ids[i] = ev.ID
		if i == 0 {
			// The origin of the chain: a:a:a.
			ev.Causation, ev.Correlation = ev.ID, ev.ID
		} else {
			if d.cause < 0 || d.cause >= i {
				return fmt.Errorf("eventsourced: draft %d names cause %d, which is not an earlier event in this command", i, d.cause)
			}
			ev.Causation, ev.Correlation = ids[d.cause], ids[0]
		}
		if err := appendEvent(ctx, tx, ev); err != nil {
			return err
		}
		if err := applyEvent(ctx, tx, ev); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventsourced: commit: %w", err)
	}
	return nil
}

// stamp completes MODEL §10's envelope around a verb's decision. Tier
// dimensions are a static function of event type (D56) — asked here, once,
// never decided by a verb and never conditional on anything at runtime.
func (s *Store) stamp(d draft) (scenario.Event, error) {
	raw, err := json.Marshal(d.payload)
	if err != nil {
		return scenario.Event{}, fmt.Errorf("eventsourced: marshal payload for %s: %w", d.typ, err)
	}
	if d.actor == "" {
		return scenario.Event{}, fmt.Errorf("eventsourced: %s has no actor", d.typ)
	}
	ev := scenario.Event{
		ID:         s.nextID(),
		Type:       d.typ,
		OccurredAt: time.Now().UTC(),
		Actor:      d.actor,
		Subject:    d.subject,
		Payload:    raw,
	}
	repo, clone, worktree := scenario.RequiredDimensions(d.typ)
	if repo {
		ev.Repo = s.env.Repo
	}
	if clone {
		ev.Clone = s.env.Clone
	}
	if worktree {
		ev.Worktree = s.env.Worktree
	}
	return ev, nil
}

// appendEvent is the single INSERT into the log.
func appendEvent(ctx context.Context, tx *sql.Tx, ev scenario.Event) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO events (id, type, occurred_at, actor, causation, correlation,
		                     repo, clone, worktree, subject, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.Type, ev.OccurredAt.Format(time.RFC3339Nano),
		string(ev.Actor), ev.Causation, ev.Correlation,
		nullable(ev.Repo), nullable(ev.Clone), nullable(ev.Worktree),
		ev.Subject, string(ev.Payload))
	if err != nil {
		return fmt.Errorf("eventsourced: append %s: %w", ev.Type, err)
	}
	return nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// Payloads
// ---------------------------------------------------------------------------

// birthPayload carries what the projection needs to place a node, including
// its locator. MODEL §10 permits `step-NN` inside a payload as a fact about
// the write — it is never what an event *references*; `subject` is.
type birthPayload struct {
	Title   string `json:"title"`
	Locator string `json:"locator"`
	Parent  string `json:"parent,omitempty"`
}

// transitionPayload records both ends of a lifecycle move. Not needed to
// rebuild the projection (the type alone determines the new state) — it is
// there so the log narrates rather than merely replays.
type transitionPayload struct {
	From scenario.Lifecycle `json:"from"`
	To   scenario.Lifecycle `json:"to"`
}

// ---------------------------------------------------------------------------
// The verbs
// ---------------------------------------------------------------------------

// CreateMatter births a Matter in Planned and returns its ULID.
func (s *Store) CreateMatter(ctx context.Context, actor scenario.Actor, title string) (string, error) {
	if title == "" {
		return "", errors.New("eventsourced: a Matter needs a title")
	}
	var id string
	err := s.commit(ctx, func(_ context.Context, _ *sql.Tx) ([]draft, error) {
		id = s.nextID()
		return []draft{{
			typ:     scenario.TypeMatterCreated,
			subject: id,
			actor:   actor,
			payload: birthPayload{Title: title, Locator: "matter"},
		}}, nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// AddStep births a Step under a Matter in Planned and returns its ULID. The
// locator is assigned here, sequentially within the Matter, and recorded in
// the event payload so a rebuild does not have to re-derive it.
func (s *Store) AddStep(ctx context.Context, actor scenario.Actor, matterID, title string) (string, error) {
	if title == "" {
		return "", errors.New("eventsourced: a Step needs a title")
	}
	var id string
	err := s.commit(ctx, func(ctx context.Context, tx *sql.Tx) ([]draft, error) {
		parent, err := loadNode(ctx, tx, matterID)
		if err != nil {
			return nil, err
		}
		if parent.Kind != scenario.KindMatter {
			return nil, fmt.Errorf("eventsourced: %s is a %s, Steps hang under a Matter", matterID, parent.Kind)
		}
		var siblings int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE parent = ?`, matterID).Scan(&siblings); err != nil {
			return nil, fmt.Errorf("eventsourced: count siblings of %s: %w", matterID, err)
		}
		id = s.nextID()
		return []draft{{
			typ:     scenario.TypeStepCreated,
			subject: id,
			actor:   actor,
			payload: birthPayload{
				Title:   title,
				Locator: fmt.Sprintf("step-%02d", siblings+1),
				Parent:  matterID,
			},
		}}, nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Start moves a node to In Progress, auto-starting Planned ancestors
// ancestor-first (D57). One command, several verbs, one event each.
func (s *Store) Start(ctx context.Context, actor scenario.Actor, nodeID string) error {
	return s.commit(ctx, func(ctx context.Context, tx *sql.Tx) ([]draft, error) {
		node, err := loadNode(ctx, tx, nodeID)
		if err != nil {
			return nil, err
		}
		if node.Lifecycle != scenario.Planned {
			return nil, fmt.Errorf("eventsourced: %s is already %s; start moves Planned work only",
				nodeID, node.Lifecycle)
		}
		// Walk to the root, then emit downward: ancestor first.
		chain := []scenario.Node{node}
		for cur := node; cur.Parent != ""; {
			cur, err = loadNode(ctx, tx, cur.Parent)
			if err != nil {
				return nil, err
			}
			chain = append(chain, cur)
		}
		drafts := make([]draft, 0, len(chain))
		for i := len(chain) - 1; i >= 0; i-- {
			n := chain[i]
			// The cascade is over Planned ancestors only: an ancestor already
			// In Progress is left alone, because a second `started` for one
			// transition is exactly what invariant 1 forbids.
			if n.Lifecycle != scenario.Planned {
				continue
			}
			// Everything the cascade starts on its own initiative is wip's
			// doing; only the node the caller actually named is theirs. Each
			// generation is entailed by the one above it, so a deeper tree
			// (Matter -> Stage -> Step, once Stage exists) yields a:a:a,
			// b:a:a, c:b:a rather than a flat fan from the origin.
			act := scenario.ActorSystemWip
			if n.ID == nodeID {
				act = actor
			}
			cause := len(drafts) - 1
			if cause < 0 {
				cause = 0
			}
			drafts = append(drafts, draft{
				typ:     startedType(n.Kind),
				subject: n.ID,
				actor:   act,
				cause:   cause,
				payload: transitionPayload{From: scenario.Planned, To: scenario.InProgress},
			})
		}
		return drafts, nil
	})
}

// Finish moves a node to Done. Ancestors are not auto-finished — MODEL has no
// such rule; only start cascades.
func (s *Store) Finish(ctx context.Context, actor scenario.Actor, nodeID string) error {
	return s.commit(ctx, func(ctx context.Context, tx *sql.Tx) ([]draft, error) {
		node, err := loadNode(ctx, tx, nodeID)
		if err != nil {
			return nil, err
		}
		if node.Lifecycle != scenario.InProgress {
			return nil, fmt.Errorf("eventsourced: %s is %s; finish moves In Progress work only",
				nodeID, node.Lifecycle)
		}
		return []draft{{
			typ:     finishedType(node.Kind),
			subject: node.ID,
			actor:   actor,
			payload: transitionPayload{From: scenario.InProgress, To: scenario.Done},
		}}, nil
	})
}

func startedType(k scenario.Kind) string {
	if k == scenario.KindMatter {
		return scenario.TypeMatterStarted
	}
	return scenario.TypeStepStarted
}

func finishedType(k scenario.Kind) string {
	if k == scenario.KindMatter {
		return scenario.TypeMatterFinished
	}
	return scenario.TypeStepFinished
}

// ---------------------------------------------------------------------------
// The reads
// ---------------------------------------------------------------------------

// inProgressSQL is the whole of invariant 2 under this shape: one indexed
// SELECT against the maintained projection. No replay, no join, no fold.
const inProgressSQL = `
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle
FROM   nodes
WHERE  lifecycle = 'in-progress'
ORDER  BY birth_event`

// InProgress answers the founding question (MODEL §1) in creation order.
func (s *Store) InProgress(ctx context.Context) ([]scenario.Node, error) {
	rows, err := s.db.QueryContext(ctx, inProgressSQL)
	if err != nil {
		return nil, fmt.Errorf("eventsourced: in-progress: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []scenario.Node
	for rows.Next() {
		var n scenario.Node
		if err := rows.Scan(&n.ID, &n.Kind, &n.Parent, &n.Locator, &n.Title, &n.Lifecycle); err != nil {
			return nil, fmt.Errorf("eventsourced: scan in-progress row: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventsourced: in-progress: %w", err)
	}
	return out, nil
}

// Events returns the whole log in total order — which is id order, because
// the id *is* the order.
func (s *Store) Events(ctx context.Context) ([]scenario.Event, error) {
	return readEvents(ctx, s.db)
}

// rowQuerier is the shared shape of *sql.DB and *sql.Tx that the readers need.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readEvents(ctx context.Context, q rowQuerier) ([]scenario.Event, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, type, occurred_at, actor, causation, correlation,
		        repo, clone, worktree, subject, payload
		 FROM events ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("eventsourced: read log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []scenario.Event
	for rows.Next() {
		var (
			ev                    scenario.Event
			at, actor, payload    string
			repo, clone, worktree sql.NullString
		)
		if err := rows.Scan(&ev.ID, &ev.Type, &at, &actor, &ev.Causation, &ev.Correlation,
			&repo, &clone, &worktree, &ev.Subject, &payload); err != nil {
			return nil, fmt.Errorf("eventsourced: scan event: %w", err)
		}
		ts, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("eventsourced: event %s has an unreadable timestamp %q: %w", ev.ID, at, err)
		}
		ev.OccurredAt = ts.UTC()
		ev.Actor = scenario.Actor(actor)
		ev.Repo, ev.Clone, ev.Worktree = repo.String, clone.String, worktree.String
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventsourced: read log: %w", err)
	}
	return out, nil
}

// loadNode reads one node from the projection. Every guard in every verb goes
// through here — which is the honest cost of the shape: the projection is not
// only a read optimisation, it is also what the write path consults to decide
// whether a verb is legal at all.
func loadNode(ctx context.Context, q rowQuerier, id string) (scenario.Node, error) {
	var n scenario.Node
	err := q.QueryRowContext(ctx,
		`SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Kind, &n.Parent, &n.Locator, &n.Title, &n.Lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return n, fmt.Errorf("eventsourced: no node %s", id)
	}
	if err != nil {
		return n, fmt.Errorf("eventsourced: load node %s: %w", id, err)
	}
	return n, nil
}
