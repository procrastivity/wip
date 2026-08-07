package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// timestampLayout is how the store writes every timestamp: RFC3339 UTC to the
// millisecond, fixed width, so lexical order is chronological order and the
// column-level CHECK in schema_v1.go can recognise a malformed one.
const timestampLayout = "2006-01-02T15:04:05.000Z07:00"

// Actor is who or what performed a command. One prefixed token: `human`,
// `role:<name>` for an inner- or outer-loop role, `system:<source>` for
// anything a foreign system did on its own initiative. New roles arrive in P2
// with no migration, because this is one open-ended column and not an enum.
type Actor string

// ActorHuman is the actor for anything a person invoked. In P1, with no roles
// yet (HANDOFF §1.1), it is very nearly the only actor in use.
const ActorHuman Actor = "human"

// RoleActor names an inner- or outer-loop role: Builder, Verifier, Warden.
func RoleActor(name string) Actor { return Actor("role:" + name) }

// SystemActor names a system acting on its own initiative — a CI webhook, a
// watcher. MODEL §10 requires the envelope to be able to say this ("a Builder
// closing and CI going red are events, or outer-loop roles are invisible to
// Session"); nothing in P1 emits one, and that is a fact about P1's role set
// rather than a gap in the envelope.
func SystemActor(source string) Actor { return Actor("system:" + source) }

func (a Actor) valid() bool {
	switch {
	case a == ActorHuman:
		return true
	case len(a) > len("role:") && a[:len("role:")] == "role:":
		return true
	case len(a) > len("system:") && a[:len("system:")] == "system:":
		return true
	default:
		return false
	}
}

// Env is the tier context a command runs in: which Repo, Clone and Worktree it
// was invoked against. `tiers` resolves it; the store stamps whichever of the
// three the event type requires (D56) and never asks a verb which.
type Env struct {
	Repo     string
	Clone    string
	Worktree string
}

// Request is one command's invocation context. A command is a request, and it
// may invoke several verbs in one transaction chain (D57) — the actor and the
// tier context belong to the request, not to each verb inside it.
type Request struct {
	Actor Actor
	Env   Env
}

// Event is the eleven-column envelope (MODEL §10, the `schema` Brief §A). Later
// phases add event types; these columns never change.
type Event struct {
	// ID is the event's own ULID, monotonic per store, doubling as the
	// total-order sort key (D44, D51). There is no sequence column.
	ID string
	// Type is a token from the registered taxonomy.
	Type string
	// OccurredAt is UTC, and is the timestamp ID itself carries: one clock, so
	// there is nothing to reconcile and no recorded_at.
	OccurredAt time.Time
	// Actor is who performed the command — never empty.
	Actor Actor
	// Causation is the event that entailed this one; its own ID if it began the
	// chain. Correlation is the chain's origin; likewise self-referential on an
	// origin event. Neither ever names a later event.
	Causation   string
	Correlation string
	// Repo, Clone and Worktree are the tier dimensions this type requires —
	// repo on everything but `batch.*` (D56), clone and worktree on execution
	// events.
	Repo     string
	Clone    string
	Worktree string
	// Subject is the ULID of the entity the event is about: identity, never a
	// locator (MODEL §10).
	Subject string
	// Payload holds the type-specific fields.
	Payload json.RawMessage
}

// IsOrigin reports whether this event began its own chain — the a:a:a form.
func (e Event) IsOrigin() bool {
	return e.ID == e.Causation && e.ID == e.Correlation
}

// Draft is a verb's decision: what happened, to whom, and the type-specific
// facts. There is no field here for an id, a timestamp, a tier dimension, an
// actor or a chain pointer, so a verb cannot express an event that is missing
// them and cannot get one wrong. Store.stamp adds them, in one place, for every
// event the store will ever write.
type Draft struct {
	Type    string
	Subject string
	Payload any

	// Env overrides the request tier context for this event. It is used only
	// when one atomic command emits events for already-existing execution
	// objects whose original dimensions differ from the acting command, such
	// as reaping a Dispatch from a different known Clone.
	Env *Env

	// Cause is the index, within this command's own drafts, of the draft that
	// entailed this one. Ignored for the first draft, which is the chain
	// origin. A linear cascade points at its predecessor (a:a:a, b:a:a, c:b:a);
	// same-command events with no causal relation to each other point at the
	// origin (a:a:a, b:a:a, c:a:a).
	Cause int
}

// ErrNoEvent is the floor under invariant 1 from the other side: a command that
// mutated nothing must not silently succeed.
var ErrNoEvent = errors.New("store: the command produced no event")

// Store is wip's store. See doc.go for the shape.
type Store struct {
	View

	db   *sql.DB
	path string
	// blobDir is where oversized content spills; see content.go.
	blobDir string

	ids     *idSource
	version int

	// floor is the highest identity this store has ever issued or read. The id
	// source is monotonic within one process but knows nothing about a previous
	// one, so after a reopen inside the same millisecond it could otherwise mint
	// an id below the last one on disk — which would break the single thing the
	// whole design rests on, an event's identity doubling as the total order
	// (D44, D51).
	floor string
}

// Open opens the store at path, creating and migrating it if needed.
func Open(path string) (*Store, error) {
	return openAt(path, register, latestVersion(register), time.Now)
}

// OpenWithClock is Open with an injectable clock for the id source — a test
// seam. read-surface's Session tests are the motivating case: occurred_at is
// derived from an event's own id (ulid.go's timeOfID) and the log is
// append-only (events_no_update, schema_v1.go), so there is no way to place
// events at chosen, widely-spaced timestamps after the fact; controlling the
// clock they were minted under is the only way to test an idle-gap threshold
// without a test that really waits hours. Production code always calls Open.
func OpenWithClock(path string, clock func() time.Time) (*Store, error) {
	return openAt(path, register, latestVersion(register), clock)
}

// openAt is Open pinned to a register, a target version and a clock. The
// register/target injection exists so a test can populate a database under
// one version and reopen it under the next; the clock injection is
// OpenWithClock's (see its doc).
func openAt(path string, reg []migration, target int, clock func() time.Time) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: preparing %s: %w", filepath.Dir(path), err)
	}

	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection means one writer and no lock contention to reason about.
	// wip is single-user, single-host by refusal (D34) and one Matter is worked
	// from one clone at a time (D43); nothing here wants concurrency.
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: connect %s: %w", path, err)
	}
	version, err := migrate(ctx, db, path, reg, target)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{
		View:    View{q: db, schemaVersion: version},
		db:      db,
		path:    path,
		blobDir: blobDirFor(path),
		ids:     newIDSourceWithClock(clock),
		version: version,
	}
	if err := s.loadFloor(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.verifyTaxonomy(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureProjectionVersion(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the store.
func (s *Store) Close() error { return s.db.Close() }

// Path is the file the store lives in.
func (s *Store) Path() string { return s.path }

// SchemaVersion reports the schema version this handle was opened at.
func (s *Store) SchemaVersion() int { return s.version }

// loadFloor reads the highest identity already on disk.
//
// MAX(events.id) dominates every identity in the store, and for a structural
// reason worth stating: an entity's ULID is minted inside the command that
// births it, before that command's event is stamped, and no command commits
// without appending at least one event (ErrNoEvent). So every identity ever
// persisted is below some event id.
func (s *Store) loadFloor(ctx context.Context) error {
	var highest sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM events`).Scan(&highest); err != nil {
		return fmt.Errorf("store: read the identity high-water mark: %w", err)
	}
	if highest.Valid {
		s.floor = highest.String
	}
	return nil
}

// verifyTaxonomy confirms every event type this binary can stamp is registered
// in the database. It catches the one mistake the taxonomy-as-data arrangement
// makes possible: a type added to Go without the migration that seeds it.
func (s *Store) verifyTaxonomy(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT type FROM event_types`)
	if err != nil {
		return fmt.Errorf("store: read the event taxonomy: %w", err)
	}
	defer func() { _ = rows.Close() }()

	known := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return fmt.Errorf("store: read the event taxonomy: %w", err)
		}
		known[t] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read the event taxonomy: %w", err)
	}
	types := append([]EventType{}, P1Taxonomy...)
	if s.version >= 2 {
		types = append(types, V2Taxonomy...)
	}
	if s.version >= 3 {
		types = append(types, V3Taxonomy...)
	}
	for _, t := range types {
		if !known[t.Type] {
			return fmt.Errorf(
				"store: this wip can emit %q but the store's taxonomy does not carry it; the migration that seeds it is missing", t.Type)
		}
	}
	return nil
}

// NewID mints the next identity, strictly above everything this store has
// issued or read.
//
// Most callers never need it: a verb that births an entity mints the identity
// inside its own decide function, from the Tx. It is exported for the one case
// that cannot — the tier bootstrap, where the Repo's identity has to exist
// before the command that creates it can name its own tier context.
func (s *Store) NewID() string {
	for {
		id := s.ids.next()
		if id > s.floor {
			s.floor = id
			return id
		}
		// The floor's timestamp cannot be ahead of now by more than clock skew,
		// so one tick of the millisecond clock is enough to clear it.
		time.Sleep(time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// The one write path
// ---------------------------------------------------------------------------

// Tx is what a decide function sees: the store as it stands inside the
// transaction that is about to append, plus the ability to mint identities.
//
// It deliberately exposes no way to write. The winning spike handed its verbs a
// live transaction handle and called the resulting "no verb writes directly" a
// matter of discipline rather than structure; this closes that, at the cost of
// every guard a verb needs having to be a read method on View.
type Tx struct {
	View

	store *Store
}

// NewID mints an identity for an entity this command is about to birth.
func (t *Tx) NewID() string { return t.store.NewID() }

// Commit is the only function in this package that writes.
//
// A caller hands it a decide function which reads current state and returns the
// events that follow; Commit stamps them, appends them, and folds each one into
// the projection — in that order, in one transaction. Two consequences, and
// they are the whole of MODEL §10 invariant 1:
//
//   - there is no way to reach a projection except by way of an event, because
//     applyEvent's only data argument is an event;
//   - there is no way to append an event without the full envelope, because
//     stamp is the only constructor and it is not optional.
//
// It returns the events it wrote, in order, so a caller can assert on them and
// a trace can be read straight out of a command.
func (s *Store) Commit(ctx context.Context, req Request, decide func(context.Context, *Tx) ([]Draft, error)) ([]Event, error) {
	if !req.Actor.valid() {
		return nil, fmt.Errorf("store: %q is not an actor; expected human, role:<name> or system:<source>", req.Actor)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	handle := &Tx{View: View{q: tx, schemaVersion: s.version}, store: s}
	drafts, err := decide(ctx, handle)
	if err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, ErrNoEvent
	}

	events := make([]Event, len(drafts))
	for i, d := range drafts {
		ev, err := s.stamp(req, d)
		if err != nil {
			return nil, err
		}
		// Causation and correlation are assigned here, never by a verb, for the
		// same reason ids and tier dimensions are: a verb knows what entailed
		// what, but it does not know the ids, because they do not exist until
		// stamp runs.
		if i == 0 {
			ev.Causation, ev.Correlation = ev.ID, ev.ID
		} else {
			if d.Cause < 0 || d.Cause >= i {
				return nil, fmt.Errorf(
					"store: draft %d names cause %d, which is not an earlier event in this command", i, d.Cause)
			}
			ev.Causation, ev.Correlation = events[d.Cause].ID, events[0].ID
		}
		if err := appendEvent(ctx, tx, ev); err != nil {
			return nil, err
		}
		if err := applyEventVersion(ctx, tx, ev, s.version); err != nil {
			return nil, err
		}
		events[i] = ev
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit: %w", err)
	}
	return events, nil
}

// stamp completes the envelope around a verb's decision. Tier dimensions are a
// static function of event type (D56) — asked here, once, never decided by a
// verb and never conditional on anything at runtime.
func (s *Store) stamp(req Request, d Draft) (Event, error) {
	rule, err := requiredDimensions(d.Type)
	if err != nil {
		return Event{}, err
	}
	if len(d.Subject) != IDLen {
		return Event{}, fmt.Errorf("store: %s names subject %q, which is not an identity", d.Type, d.Subject)
	}
	payload := d.Payload
	if payload == nil {
		payload = struct{}{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("store: marshal payload for %s: %w", d.Type, err)
	}

	id := s.NewID()
	occurredAt, err := timeOfID(id)
	if err != nil {
		return Event{}, err
	}
	ev := Event{
		ID:         id,
		Type:       d.Type,
		OccurredAt: occurredAt,
		Actor:      req.Actor,
		Subject:    d.Subject,
		Payload:    raw,
	}
	env := req.Env
	if d.Env != nil {
		env = *d.Env
	}
	if rule.Repo {
		if env.Repo == "" {
			return Event{}, fmt.Errorf("store: %s requires a repo dimension and this command has no Repo in its tier context", d.Type)
		}
		ev.Repo = env.Repo
	}
	if rule.Clone {
		if env.Clone == "" {
			return Event{}, fmt.Errorf("store: %s is an execution event and this command has no Clone in its tier context", d.Type)
		}
		ev.Clone = env.Clone
	}
	if rule.Worktree {
		if env.Worktree == "" {
			return Event{}, fmt.Errorf("store: %s is an execution event and this command has no Worktree in its tier context", d.Type)
		}
		ev.Worktree = env.Worktree
	}
	return ev, nil
}

// appendEvent is the single INSERT into the log.
func appendEvent(ctx context.Context, tx *sql.Tx, ev Event) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO events (id, type, occurred_at, actor, causation, correlation,
		                     repo, clone, worktree, subject, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.Type, ev.OccurredAt.UTC().Format(timestampLayout),
		string(ev.Actor), ev.Causation, ev.Correlation,
		nullable(ev.Repo), nullable(ev.Clone), nullable(ev.Worktree),
		ev.Subject, string(ev.Payload))
	if err != nil {
		return fmt.Errorf("store: append %s: %w", ev.Type, err)
	}
	return nil
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
