package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// querier is the shape *sql.DB and *sql.Tx share. Every read in this package
// goes through it, which is what lets one set of read methods serve both the
// store and the inside of a transaction — so a verb's guards read exactly what
// the projection is about to be written against.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// View is the store's read surface. It reads and never writes: Store embeds one
// over the database and Tx embeds one over the open transaction.
type View struct {
	q querier
}

// Node is a Matter, Stage or Step. Identity is the ULID; Locator is the
// human-facing, mutable name (a Matter's slug, `step-NN`) that events never
// reference.
type Node struct {
	ID   string
	Kind Scale
	Repo string
	// Matter is the Matter this node belongs to, and is the node's own identity
	// when it is a Matter. It is the addressing scope: `step-NN` is globally
	// sequential within a Matter, and a Stage grouping Steps neither renames nor
	// renumbers them (D16).
	Parent      string
	Matter      string
	Locator     string
	Title       string
	Lifecycle   Lifecycle
	SortKey     int64
	ExternalRef string
}

const nodeColumns = `id, kind, repo, matter, COALESCE(parent, ''), locator, title,
	lifecycle, sort_key, COALESCE(external_ref, '')`

func scanNode(row interface{ Scan(...any) error }) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.Kind, &n.Repo, &n.Matter, &n.Parent, &n.Locator,
		&n.Title, &n.Lifecycle, &n.SortKey, &n.ExternalRef)
	return n, err
}

// Node reads one live node by identity.
func (v View) Node(ctx context.Context, id string) (Node, error) {
	n, err := scanNode(v.q.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ? AND tombstone_event IS NULL`, id))
	if err == sql.ErrNoRows {
		return Node{}, fmt.Errorf("store: no live node %s", id)
	}
	if err != nil {
		return Node{}, fmt.Errorf("store: read node %s: %w", id, err)
	}
	return n, nil
}

// NodeByLocator resolves a locator within a Matter. Locators are mutable and
// nothing in the log references them, so this is an addressing convenience and
// never an identity lookup.
func (v View) NodeByLocator(ctx context.Context, matter, locator string) (Node, error) {
	n, err := scanNode(v.q.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE matter = ? AND locator = ? AND tombstone_event IS NULL`, matter, locator))
	if err == sql.ErrNoRows {
		return Node{}, fmt.Errorf("store: %s addresses no live node in %s", locator, matter)
	}
	if err != nil {
		return Node{}, fmt.Errorf("store: resolve %s in %s: %w", locator, matter, err)
	}
	return n, nil
}

// Tombstoned reports whether an identity names a node that was structurally
// removed. It answers by identity and never by locator, which is what keeps a
// removed node's history readable: every prior event that named it still
// resolves (D44, MODEL §10).
func (v View) Tombstoned(ctx context.Context, id string) (bool, error) {
	var tombstone sql.NullString
	err := v.q.QueryRowContext(ctx, `SELECT tombstone_event FROM nodes WHERE id = ?`, id).Scan(&tombstone)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("store: no node %s has ever existed", id)
	}
	if err != nil {
		return false, fmt.Errorf("store: read the tombstone of %s: %w", id, err)
	}
	return tombstone.Valid, nil
}

func (v View) nodeList(ctx context.Context, query string, args ...any) ([]Node, error) {
	rows, err := v.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("store: read nodes: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read nodes: %w", err)
	}
	return out, nil
}

// Children lists a node's live children in sibling order — which is
// presentation-only (D51).
func (v View) Children(ctx context.Context, parent string) ([]Node, error) {
	return v.nodeList(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE parent = ? AND tombstone_event IS NULL ORDER BY sort_key, birth_event`, parent)
}

// MatterNodes lists every live node in a Matter, the Matter included.
func (v View) MatterNodes(ctx context.Context, matter string) ([]Node, error) {
	return v.nodeList(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE matter = ? AND tombstone_event IS NULL ORDER BY sort_key, birth_event`, matter)
}

// Matters lists a Repo's live Matters. All Matters are siblings in every
// presentation (D19).
func (v View) Matters(ctx context.Context, repo string) ([]Node, error) {
	return v.nodeList(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE repo = ? AND kind = 'matter' AND tombstone_event IS NULL ORDER BY birth_event`, repo)
}

// inProgressSQL is the whole of MODEL §10 invariant 2 under this shape: one
// indexed read of the maintained projection. No replay, no join, no fold.
//
// birth_event is the ULID of the *.created event that produced the row, so
// creation order is a column and the index's second term — the answer comes back
// ordered without a sort step. See TestInProgressIsAnIndexSeek.
const inProgressSQL = `SELECT ` + nodeColumns + ` FROM nodes
	 WHERE lifecycle = 'in-progress' AND tombstone_event IS NULL
	 ORDER BY birth_event`

// InProgress answers the founding question's first third (MODEL §1): every node
// where someone has actually begun, in creation order.
//
// Under D61 the log is the source of truth and this table is a projection of it —
// so this is a read of a maintained projection, never an ad-hoc replay, which is
// what invariant 2 requires. A merely-planned Matter never appears here, because
// `started` means work began (D57).
func (v View) InProgress(ctx context.Context) ([]Node, error) {
	return v.nodeList(ctx, inProgressSQL)
}

// NextSortKey returns a sort key that places a new child after every existing
// sibling. Presentation-only (D51).
func (v View) NextSortKey(ctx context.Context, parent string) (int64, error) {
	var highest sql.NullInt64
	if err := v.q.QueryRowContext(ctx,
		`SELECT MAX(sort_key) FROM nodes WHERE parent = ? AND tombstone_event IS NULL`, parent).Scan(&highest); err != nil {
		return 0, fmt.Errorf("store: read the sibling order under %s: %w", parent, err)
	}
	if !highest.Valid {
		return sortKeyGap, nil
	}
	return highest.Int64 + sortKeyGap, nil
}

// SortKeyBetween returns a sort key that places a node between two siblings, or
// reports that the gap is exhausted — in which case the caller reorders the whole
// sibling set with a step.reordered rather than inventing a key.
func SortKeyBetween(before, after int64) (int64, bool) {
	if after-before < 2 {
		return 0, false
	}
	return before + (after-before)/2, true
}

// NextStepLocator returns the next `step-NN` within a Matter.
//
// The numbering is globally sequential within the Matter and Stages do not
// renumber it: a grouping is not a namespace (D16). The result is a locator — a
// mutable attribute — and never an identity.
func (v View) NextStepLocator(ctx context.Context, matter string) (string, error) {
	var count int
	if err := v.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE matter = ? AND kind = 'step'`, matter).Scan(&count); err != nil {
		return "", fmt.Errorf("store: count the Steps of %s: %w", matter, err)
	}
	// Tombstoned Steps are counted on purpose: a removed step-03 does not hand
	// its number to the next Step, because the log is full of events that call
	// that Step step-03 and a reader of the history should not find two.
	return fmt.Sprintf("step-%02d", count+1), nil
}

// ---------------------------------------------------------------------------
// Edges
// ---------------------------------------------------------------------------

// BlockedBy lists the edges in force on which a node waits.
//
// It reads edges_in_force and not `edges`, so a blocker that was structurally
// removed does not go on blocking: removing the blocker is how amendment clears
// an obstruction (D44), and an edge reported here always names something that
// could still complete.
func (v View) BlockedBy(ctx context.Context, node string) ([]Edge, error) {
	return v.edgeList(ctx,
		`SELECT id, blocked, blocker FROM edges_in_force WHERE blocked = ? ORDER BY id`, node)
}

// LiveEdges lists every edge in force in the store — every `blocked-by` relation
// that is still a relation, both of whose ends are still there.
func (v View) LiveEdges(ctx context.Context) ([]Edge, error) {
	return v.edgeList(ctx, `SELECT id, blocked, blocker FROM edges_in_force ORDER BY id`)
}

func (v View) edgeList(ctx context.Context, query string, args ...any) ([]Edge, error) {
	rows, err := v.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read edges: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.ID, &e.Blocked, &e.Blocker); err != nil {
			return nil, fmt.Errorf("store: read edges: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read edges: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

// ClosedGate is one gate closed against one node.
type ClosedGate struct {
	Node     string
	Gate     string
	Scale    Scale
	ClosedAt time.Time
}

// ClosedGates lists the gates closed against a node.
func (v View) ClosedGates(ctx context.Context, node string) ([]ClosedGate, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT node, gate, scale, closed_at FROM gate_state WHERE node = ? ORDER BY gate`, node)
	if err != nil {
		return nil, fmt.Errorf("store: read the gate state of %s: %w", node, err)
	}
	defer func() { _ = rows.Close() }()

	var out []ClosedGate
	for rows.Next() {
		var (
			g  ClosedGate
			at string
		)
		if err := rows.Scan(&g.Node, &g.Gate, &g.Scale, &at); err != nil {
			return nil, fmt.Errorf("store: read the gate state of %s: %w", node, err)
		}
		g.ClosedAt, err = time.Parse(timestampLayout, at)
		if err != nil {
			return nil, fmt.Errorf("store: gate %s on %s carries an unreadable timestamp %q: %w", g.Gate, node, at, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the gate state of %s: %w", node, err)
	}
	return out, nil
}

// GateDeclaration is one gate declared for a Repo, bound to a scale (D4, D12).
type GateDeclaration struct {
	Repo  string
	Gate  string
	Scale Scale
}

// GateDeclarations lists a Repo's declared gates.
func (v View) GateDeclarations(ctx context.Context, repo string) ([]GateDeclaration, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT repo, gate, scale FROM gate_declarations WHERE repo = ? ORDER BY gate`, repo)
	if err != nil {
		return nil, fmt.Errorf("store: read the gate declarations of %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()

	var out []GateDeclaration
	for rows.Next() {
		var d GateDeclaration
		if err := rows.Scan(&d.Repo, &d.Gate, &d.Scale); err != nil {
			return nil, fmt.Errorf("store: read the gate declarations of %s: %w", repo, err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the gate declarations of %s: %w", repo, err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cursor, tiers, batch, dispatch, backlog, archive
// ---------------------------------------------------------------------------

// Cursor reads the cursor for one Clone + Worktree. The second result is false
// when no cursor has been set here, which is a first-class answer and not an
// error: the cursor is attention, and attention is allowed to be nowhere (D38).
func (v View) Cursor(ctx context.Context, clone, worktree string) (string, bool, error) {
	var node sql.NullString
	err := v.q.QueryRowContext(ctx,
		`SELECT node FROM cursors WHERE clone = ? AND worktree = ?`, clone, worktree).Scan(&node)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read the cursor for %s/%s: %w", clone, worktree, err)
	}
	if !node.Valid {
		return "", false, nil
	}
	return node.String, true, nil
}

// Repo is a Repo row (MODEL §7, D37).
type Repo struct {
	ID string
	// RemoteURL is the normalised remote in its normal form, empty for a
	// local-only repo: the natural key is nullable and two local-only repos are
	// distinguished by identity alone (D37, D39).
	RemoteURL      string
	IdentityRemote string
	Label          string
}

// Repo reads one Repo by identity.
func (v View) Repo(ctx context.Context, id string) (Repo, error) {
	var r Repo
	err := v.q.QueryRowContext(ctx,
		`SELECT id, COALESCE(remote_url, ''), COALESCE(identity_remote, ''), COALESCE(label, '')
		 FROM repos WHERE id = ?`, id).Scan(&r.ID, &r.RemoteURL, &r.IdentityRemote, &r.Label)
	if err == sql.ErrNoRows {
		return Repo{}, fmt.Errorf("store: no repo %s", id)
	}
	if err != nil {
		return Repo{}, fmt.Errorf("store: read repo %s: %w", id, err)
	}
	return r, nil
}

// RepoByRemote resolves a Repo by its natural key.
func (v View) RepoByRemote(ctx context.Context, remoteURL string) (Repo, bool, error) {
	var r Repo
	err := v.q.QueryRowContext(ctx,
		`SELECT id, COALESCE(remote_url, ''), COALESCE(identity_remote, ''), COALESCE(label, '')
		 FROM repos WHERE remote_url = ?`, remoteURL).Scan(&r.ID, &r.RemoteURL, &r.IdentityRemote, &r.Label)
	if err == sql.ErrNoRows {
		return Repo{}, false, nil
	}
	if err != nil {
		return Repo{}, false, fmt.Errorf("store: resolve repo %s: %w", remoteURL, err)
	}
	return r, true, nil
}

// Clone is a Clone row. The git-common-dir is its natural key: it resolves
// identically from a subdirectory and from a linked worktree.
type Clone struct {
	ID           string
	Repo         string
	GitCommonDir string
	Label        string
}

// CloneByCommonDir resolves a Clone by its natural key. The false result is the
// unknown-clone case, which is a hard failure for the caller and never a guess
// (MODEL §11).
func (v View) CloneByCommonDir(ctx context.Context, gitCommonDir string) (Clone, bool, error) {
	var c Clone
	err := v.q.QueryRowContext(ctx,
		`SELECT id, repo, git_common_dir, label FROM clones WHERE git_common_dir = ?`, gitCommonDir).
		Scan(&c.ID, &c.Repo, &c.GitCommonDir, &c.Label)
	if err == sql.ErrNoRows {
		return Clone{}, false, nil
	}
	if err != nil {
		return Clone{}, false, fmt.Errorf("store: resolve clone %s: %w", gitCommonDir, err)
	}
	return c, true, nil
}

// Clone reads one Clone by identity.
func (v View) Clone(ctx context.Context, id string) (Clone, error) {
	var c Clone
	err := v.q.QueryRowContext(ctx,
		`SELECT id, repo, git_common_dir, label FROM clones WHERE id = ?`, id).
		Scan(&c.ID, &c.Repo, &c.GitCommonDir, &c.Label)
	if err == sql.ErrNoRows {
		return Clone{}, fmt.Errorf("store: no clone %s", id)
	}
	if err != nil {
		return Clone{}, fmt.Errorf("store: read clone %s: %w", id, err)
	}
	return c, nil
}

// Worktree is a Worktree row. Name is empty for the main worktree (D37's null
// case).
type Worktree struct {
	ID    string
	Clone string
	Name  string
}

// Worktree reads one Worktree by identity.
func (v View) Worktree(ctx context.Context, id string) (Worktree, error) {
	var w Worktree
	err := v.q.QueryRowContext(ctx,
		`SELECT id, clone, COALESCE(name, '') FROM worktrees WHERE id = ?`, id).
		Scan(&w.ID, &w.Clone, &w.Name)
	if err == sql.ErrNoRows {
		return Worktree{}, fmt.Errorf("store: no worktree %s", id)
	}
	if err != nil {
		return Worktree{}, fmt.Errorf("store: read worktree %s: %w", id, err)
	}
	return w, nil
}

// BatchMembers lists the Matters currently in a Batch. Membership implies
// nothing about ordering or structure (D18, D24); an empty Batch is legal (D58).
func (v View) BatchMembers(ctx context.Context, batch string) ([]string, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT matter FROM batch_members WHERE batch = ? AND left_event IS NULL ORDER BY matter`, batch)
	if err != nil {
		return nil, fmt.Errorf("store: read the members of batch %s: %w", batch, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var matter string
		if err := rows.Scan(&matter); err != nil {
			return nil, fmt.Errorf("store: read the members of batch %s: %w", batch, err)
		}
		out = append(out, matter)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the members of batch %s: %w", batch, err)
	}
	return out, nil
}

// Dispatch is one bracketed working session against a worktree (D59).
type Dispatch struct {
	ID          string
	Clone       string
	Worktree    string
	Open        bool
	CloseReason CloseReason
}

// OpenDispatch reads the open dispatch on a worktree, if there is one. A stale
// open bracket is what `refresh` supersedes.
func (v View) OpenDispatch(ctx context.Context, worktree string) (Dispatch, bool, error) {
	var d Dispatch
	err := v.q.QueryRowContext(ctx,
		`SELECT id, clone, worktree FROM dispatches WHERE worktree = ? AND state = 'open'`, worktree).
		Scan(&d.ID, &d.Clone, &d.Worktree)
	if err == sql.ErrNoRows {
		return Dispatch{}, false, nil
	}
	if err != nil {
		return Dispatch{}, false, fmt.Errorf("store: read the open dispatch on %s: %w", worktree, err)
	}
	d.Open = true
	return d, true, nil
}

// Dispatch reads one dispatch by identity.
func (v View) Dispatch(ctx context.Context, id string) (Dispatch, error) {
	var (
		d      Dispatch
		state  string
		reason sql.NullString
	)
	err := v.q.QueryRowContext(ctx,
		`SELECT id, clone, worktree, state, close_reason FROM dispatches WHERE id = ?`, id).
		Scan(&d.ID, &d.Clone, &d.Worktree, &state, &reason)
	if err == sql.ErrNoRows {
		return Dispatch{}, fmt.Errorf("store: no dispatch %s", id)
	}
	if err != nil {
		return Dispatch{}, fmt.Errorf("store: read dispatch %s: %w", id, err)
	}
	d.Open = state == "open"
	d.CloseReason = CloseReason(reason.String)
	return d, nil
}

// BacklogEntry is one Backlog row (MODEL §4). Deferred is a provenance, not a
// second list (D9).
type BacklogEntry struct {
	ID         string
	Repo       string
	Provenance Provenance
	State      string
	Title      string
	Detail     string
	OriginNode string
	Matter     string
}

// Backlog lists a Repo's entries.
func (v View) Backlog(ctx context.Context, repo string) ([]BacklogEntry, error) {
	rows, err := v.q.QueryContext(ctx,
		`SELECT id, repo, provenance, state, title, detail,
		        COALESCE(origin_node, ''), COALESCE(matter, '')
		 FROM   backlog_entries WHERE repo = ? ORDER BY id`, repo)
	if err != nil {
		return nil, fmt.Errorf("store: read the backlog of %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()

	var out []BacklogEntry
	for rows.Next() {
		var e BacklogEntry
		if err := rows.Scan(&e.ID, &e.Repo, &e.Provenance, &e.State, &e.Title,
			&e.Detail, &e.OriginNode, &e.Matter); err != nil {
			return nil, fmt.Errorf("store: read the backlog of %s: %w", repo, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the backlog of %s: %w", repo, err)
	}
	return out, nil
}

// ArchivedMatters lists the sealed Matters of a Repo — the Archive (MODEL §9).
//
// It is a query and not a table because D55 makes *sealed* a predicate over
// lifecycle and gates rather than a state or a frame, and there is no `*.sealed`
// event to project: a Matter is sealed when it is Done with every matter-scale
// gate its Repo declares closed against it. Finish and gate-close are
// order-independent, which is exactly what a predicate expresses and a state
// would not.
func (v View) ArchivedMatters(ctx context.Context, repo string) ([]Node, error) {
	return v.nodeList(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE id IN (SELECT id FROM archived_matters WHERE repo = ?)
		 ORDER BY birth_event`, repo)
}

// ---------------------------------------------------------------------------
// The log
// ---------------------------------------------------------------------------

// Events returns the whole log in total order — which is id order, because the
// id is the order (D44, D51).
func (v View) Events(ctx context.Context) ([]Event, error) {
	return v.eventList(ctx, `SELECT `+eventColumns+` FROM events ORDER BY id`)
}

// EventsOfSubject returns every event about one entity, in order. It resolves by
// identity, which is why a renamed locator or a removed node loses no history.
func (v View) EventsOfSubject(ctx context.Context, subject string) ([]Event, error) {
	return v.eventList(ctx,
		`SELECT `+eventColumns+` FROM events WHERE subject = ? ORDER BY id`, subject)
}

// EventsOfChain returns every event of one command's chain, given any event in
// it. A command's whole chain is recoverable from any one of its events, because
// correlation names the origin and never points forward (MODEL §10).
func (v View) EventsOfChain(ctx context.Context, correlation string) ([]Event, error) {
	return v.eventList(ctx,
		`SELECT `+eventColumns+` FROM events WHERE correlation = ? ORDER BY id`, correlation)
}

const eventColumns = `id, type, occurred_at, actor, causation, correlation,
	repo, clone, worktree, subject, payload`

func (v View) eventList(ctx context.Context, query string, args ...any) ([]Event, error) {
	rows, err := v.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read the log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var (
			ev                    Event
			at, actor, payload    string
			repo, clone, worktree sql.NullString
		)
		if err := rows.Scan(&ev.ID, &ev.Type, &at, &actor, &ev.Causation, &ev.Correlation,
			&repo, &clone, &worktree, &ev.Subject, &payload); err != nil {
			return nil, fmt.Errorf("store: read the log: %w", err)
		}
		ts, err := time.Parse(timestampLayout, at)
		if err != nil {
			return nil, fmt.Errorf("store: event %s carries an unreadable timestamp %q: %w", ev.ID, at, err)
		}
		ev.OccurredAt = ts.UTC()
		ev.Actor = Actor(actor)
		ev.Repo, ev.Clone, ev.Worktree = repo.String, clone.String, worktree.String
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the log: %w", err)
	}
	return out, nil
}
