// Package tables is Spike B of the `store-fork` Matter (MODEL D46): the
// tables + audit-log shape.
//
// Normalised entity tables are the source of truth. A verb writes the entity
// row as its primary act; the same transaction additionally appends a row to a
// separate audit-log table, so MODEL §10 invariant 1 is satisfied as a
// property of the write path rather than as the write path itself. "What is in
// progress" (MODEL §1) is a plain SELECT over `nodes` — invariant 2 costs one
// indexed query and no projection to keep correct.
//
// Nothing here is production code; it exists to produce evidence for a
// decision record. See report.md.
package tables

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/spike/scenario"

	_ "modernc.org/sqlite" // pure-Go driver; CGO_ENABLED=0 is load-bearing
)

// ErrRefused is returned by every command the model does not permit. A refused
// command writes nothing at all: the transaction rolls back before commit.
var ErrRefused = errors.New("refused")

// ErrNotFound is returned when a verb names a node that does not exist.
var ErrNotFound = errors.New("no such node")

const matterLocator = "matter"

// Store is the tables+audit-log store. It satisfies scenario.Store.
type Store struct {
	db      *sql.DB
	env     scenario.Env
	ulid    *scenario.ULIDSource
	now     func() time.Time
	version int // schema version this handle was opened at
}

var _ scenario.Store = (*Store)(nil)

// Open opens (creating and migrating if absent) the store at path, bringing it
// to the latest schema version.
func Open(path string, env scenario.Env) (*Store, error) {
	return OpenAtVersion(path, env, latestVersion())
}

// OpenAtVersion opens the store pinned at a specific schema version. It exists
// for the migration exercise: it is how a v1-populated database is
// manufactured so that a later Open can migrate it forward. Production would
// only ever call Open.
func OpenAtVersion(path string, env scenario.Env, version int) (*Store, error) {
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(delete)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Spike-grade: one connection, so "same transaction" needs no thought.
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	applied, err := migrate(ctx, db, path, version)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:      db,
		env:     env,
		ulid:    scenario.NewULIDSource(),
		now:     time.Now,
		version: applied,
	}, nil
}

// Close releases the store.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion reports the schema version this handle is operating at.
func (s *Store) SchemaVersion() int { return s.version }

// ---------------------------------------------------------------------------
// Verbs. Each is a pre-check followed by one or more mutations; a mutation is
// one event plus the one node write it caused (see coupling.go).
// ---------------------------------------------------------------------------

const insertNodeSQL = `
INSERT INTO nodes (id, kind, parent, locator, title, lifecycle, born_event_id, last_event_id)
VALUES (:id, :kind, :parent, :locator, :title, 'planned', :event, :event)`

// CreateMatter births a Matter in Planned and returns its ULID.
func (s *Store) CreateMatter(ctx context.Context, title string) (string, error) {
	if title == "" {
		return "", fmt.Errorf("%w: a Matter needs a title", ErrRefused)
	}
	id := s.ulid.New()
	err := s.inTx(ctx, func(t *txn) error {
		return t.apply(mutation{
			eventType: scenario.TypeMatterCreated,
			subject:   id,
			payload: map[string]any{
				"kind":    string(scenario.KindMatter),
				"title":   title,
				"locator": matterLocator, // a fact about the write, not a reference
			},
			sql: insertNodeSQL,
			args: []any{
				sql.Named("id", id),
				sql.Named("kind", string(scenario.KindMatter)),
				sql.Named("parent", nil),
				sql.Named("locator", matterLocator),
				sql.Named("title", title),
			},
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// AddStep births a Step under a Matter in Planned and returns its ULID. The
// locator (`step-NN`) is assigned by the store, sequential within the Matter.
func (s *Store) AddStep(ctx context.Context, matterID, title string) (string, error) {
	if title == "" {
		return "", fmt.Errorf("%w: a Step needs a title", ErrRefused)
	}
	id := s.ulid.New()
	err := s.inTx(ctx, func(t *txn) error {
		parent, err := t.node(matterID)
		if err != nil {
			return err
		}
		if parent.Kind != scenario.KindMatter {
			return fmt.Errorf("%w: %s is a %s, Steps hang off Matters", ErrRefused, matterID, parent.Kind)
		}
		locator, err := t.nextStepLocator(matterID)
		if err != nil {
			return err
		}
		return t.apply(mutation{
			eventType: scenario.TypeStepCreated,
			subject:   id,
			payload: map[string]any{
				"kind":    string(scenario.KindStep),
				"title":   title,
				"locator": locator,
				"parent":  matterID, // identity, never the parent's locator
			},
			sql: insertNodeSQL,
			args: []any{
				sql.Named("id", id),
				sql.Named("kind", string(scenario.KindStep)),
				sql.Named("parent", matterID),
				sql.Named("locator", locator),
				sql.Named("title", title),
			},
		})
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

const transitionSQL = `
UPDATE nodes SET lifecycle = :to, last_event_id = :event
WHERE id = :id AND lifecycle = :from`

// Start moves a node to In Progress, auto-starting Planned ancestors
// ancestor-first (D57). One command, several verbs, one event each.
func (s *Store) Start(ctx context.Context, nodeID string) error {
	return s.inTx(ctx, func(t *txn) error {
		n, err := t.node(nodeID)
		if err != nil {
			return err
		}
		if n.Lifecycle != scenario.Planned {
			return fmt.Errorf("%w: %s is already %s", ErrRefused, nodeID, n.Lifecycle)
		}
		chain, err := t.plannedAncestors(n)
		if err != nil {
			return err
		}
		for _, target := range append(chain, n) {
			if err := t.apply(transition(target, scenario.Planned, scenario.InProgress)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Finish moves a node to Done. Ancestors are not auto-finished — MODEL has no
// such rule; only start cascades.
func (s *Store) Finish(ctx context.Context, nodeID string) error {
	return s.inTx(ctx, func(t *txn) error {
		n, err := t.node(nodeID)
		if err != nil {
			return err
		}
		if n.Lifecycle != scenario.InProgress {
			return fmt.Errorf("%w: %s is %s, only in-progress work finishes", ErrRefused, nodeID, n.Lifecycle)
		}
		return t.apply(transition(n, scenario.InProgress, scenario.Done))
	})
}

// transition builds the lifecycle mutation for one node. The event type is a
// function of (kind, verb) and lands in the taxonomy; the schema's foreign key
// to event_types refuses anything that does not.
func transition(n nodeRow, from, to scenario.Lifecycle) mutation {
	verb := "started"
	if to == scenario.Done {
		verb = "finished"
	}
	return mutation{
		eventType: string(n.Kind) + "." + verb,
		subject:   n.ID,
		payload: map[string]any{
			"from":    string(from),
			"to":      string(to),
			"locator": n.Locator, // a fact about the write, not a reference
		},
		sql: transitionSQL,
		args: []any{
			sql.Named("id", n.ID),
			sql.Named("from", string(from)),
			sql.Named("to", string(to)),
		},
	}
}

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

// The founding question (MODEL §1), in full. Invariant 2 is a WHERE clause
// over the source of truth; `born_event_id` is the creation-order sort key for
// free, because event ULIDs ascend and a node's birth event is its first.
const inProgressSQLv1 = `
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, ''
FROM nodes
WHERE lifecycle = 'in-progress'
ORDER BY born_event_id`

// v2 differs from v1 by one column.
const inProgressSQLv2 = `
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, COALESCE(label, '')
FROM nodes
WHERE lifecycle = 'in-progress'
ORDER BY born_event_id`

// LabeledNode is what the v2 read path returns: a scenario.Node plus the
// optional label. Nodes born before v2 report an empty label.
type LabeledNode struct {
	scenario.Node
	Label string
}

// InProgress answers the founding question by identity, in creation order.
func (s *Store) InProgress(ctx context.Context) ([]scenario.Node, error) {
	rows, err := s.InProgressLabeled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scenario.Node, len(rows))
	for i, r := range rows {
		out[i] = r.Node
	}
	return out, nil
}

// InProgressLabeled is InProgress with the v2 label surfaced. Against a v1
// database every label is empty.
func (s *Store) InProgressLabeled(ctx context.Context) ([]LabeledNode, error) {
	query := inProgressSQLv1
	if s.version >= 2 {
		query = inProgressSQLv2
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("in-progress: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LabeledNode
	for rows.Next() {
		var n LabeledNode
		if err := rows.Scan(&n.ID, &n.Kind, &n.Parent, &n.Locator, &n.Title, &n.Lifecycle, &n.Label); err != nil {
			return nil, fmt.Errorf("in-progress scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Events returns the whole audit log in total order — the event ULID is the
// order key, so there is no sequence column (D44, D51).
func (s *Store) Events(ctx context.Context) ([]scenario.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, occurred_at, repo, clone, worktree, subject, payload
		FROM events ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []scenario.Event
	for rows.Next() {
		var (
			e                     scenario.Event
			occurred, payload     string
			repo, clone, worktree sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.Type, &occurred, &repo, &clone, &worktree, &e.Subject, &payload); err != nil {
			return nil, fmt.Errorf("events scan: %w", err)
		}
		ts, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, fmt.Errorf("events: bad occurred_at %q: %w", occurred, err)
		}
		e.OccurredAt = ts.UTC()
		e.Repo, e.Clone, e.Worktree = repo.String, clone.String, worktree.String
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Transaction-scoped reads.
// ---------------------------------------------------------------------------

type nodeRow struct {
	ID        string
	Kind      scenario.Kind
	Parent    string
	Locator   string
	Lifecycle scenario.Lifecycle
}

func (t *txn) node(id string) (nodeRow, error) {
	var n nodeRow
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT id, kind, COALESCE(parent, ''), locator, lifecycle FROM nodes WHERE id = ?`, id,
	).Scan(&n.ID, &n.Kind, &n.Parent, &n.Locator, &n.Lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return n, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return n, fmt.Errorf("read node %s: %w", id, err)
	}
	return n, nil
}

// plannedAncestors returns the node's Planned ancestors, ancestor-first,
// excluding the node itself (D57).
func (t *txn) plannedAncestors(n nodeRow) ([]nodeRow, error) {
	var chain []nodeRow
	for cur := n; cur.Parent != ""; {
		parent, err := t.node(cur.Parent)
		if err != nil {
			return nil, err
		}
		if parent.Lifecycle == scenario.Planned {
			chain = append([]nodeRow{parent}, chain...)
		}
		cur = parent
	}
	return chain, nil
}

// nextStepLocator assigns `step-NN`, sequential within the Matter. It is a
// locator: mutable, human-facing, and never what an event references.
func (t *txn) nextStepLocator(matterID string) (string, error) {
	var n int
	if err := t.tx.QueryRowContext(t.ctx,
		`SELECT COUNT(*) FROM nodes WHERE parent = ?`, matterID).Scan(&n); err != nil {
		return "", fmt.Errorf("next locator: %w", err)
	}
	return fmt.Sprintf("step-%02d", n+1), nil
}
