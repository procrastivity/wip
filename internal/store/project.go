package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// applyEvent folds one event into the projection.
//
// This function is the projection's entire definition. Its only data argument is
// an event, so there is no expressible way to move the projection that is not a
// consequence of something in the log — and the same function serves the live
// write path (Store.Commit) and the rebuild path (Store.Rebuild), which is what
// makes "the projection holds no fact the log does not" a property of the code
// rather than a claim in a comment.
//
// A type with no rule here is a mistake, not a default: the switch is total over
// the registered taxonomy, and render.performed is the one member that
// deliberately projects nothing.
func applyEvent(ctx context.Context, tx *sql.Tx, ev Event) error {
	switch ev.Type {
	// ---- lifecycle -------------------------------------------------------
	case TypeMatterCreated:
		return insertNode(ctx, tx, ev, ScaleMatter)
	case TypeStageCreated:
		return insertNode(ctx, tx, ev, ScaleStage)
	case TypeStepCreated, TypeStepInserted:
		return insertNode(ctx, tx, ev, ScaleStep)
	case TypeMatterStarted, TypeStageStarted, TypeStepStarted,
		TypeMatterFinished, TypeStageFinished, TypeStepFinished,
		TypeMatterCanceled, TypeStageCanceled, TypeStepCanceled,
		TypeMatterPaused, TypeStagePaused, TypeStepPaused,
		TypeMatterResumed, TypeStageResumed, TypeStepResumed:
		return applyTransition(ctx, tx, ev)

	// ---- content ---------------------------------------------------------
	case TypeContentCreated, TypeContentAppended:
		return insertContent(ctx, tx, ev)

	// ---- amendment -------------------------------------------------------
	case TypeStepReordered:
		return applyReorder(ctx, tx, ev)
	case TypeStepReplaced:
		return applyReplace(ctx, tx, ev)
	case TypeStepRemoved:
		return tombstoneNode(ctx, tx, ev)

	// ---- dependency ------------------------------------------------------
	case TypeDependencyAdded:
		return insertEdge(ctx, tx, ev)
	case TypeDependencyRemoved:
		return tombstoneEdge(ctx, tx, ev)

	// ---- gate ------------------------------------------------------------
	case TypeGateClosed:
		return closeGate(ctx, tx, ev)

	// ---- backlog ---------------------------------------------------------
	case TypeBacklogEntered:
		return insertBacklogEntry(ctx, tx, ev)
	case TypeBacklogPlanned:
		return planBacklogEntry(ctx, tx, ev)
	case TypeBacklogDeclined:
		return declineBacklogEntry(ctx, tx, ev)

	// ---- reference -------------------------------------------------------
	case TypeReferenceBound:
		return bindReference(ctx, tx, ev)

	// ---- tiers -----------------------------------------------------------
	case TypeRepoAttached:
		return insertRepo(ctx, tx, ev)
	case TypeCloneAttached:
		return insertClone(ctx, tx, ev)
	case TypeWorktreeAttached:
		return insertWorktree(ctx, tx, ev)
	case TypeRepoKeyAdopted:
		return adoptRepoKey(ctx, tx, ev)
	case TypeCloneRelinked:
		return relinkClone(ctx, tx, ev)
	case TypeCloneLabeled:
		return labelClone(ctx, tx, ev)

	// ---- cursor ----------------------------------------------------------
	case TypeCursorMoved:
		return moveCursor(ctx, tx, ev)

	// ---- batch and dispatch ---------------------------------------------
	case TypeBatchCreated:
		return insertBatch(ctx, tx, ev)
	case TypeBatchJoined:
		return joinBatch(ctx, tx, ev)
	case TypeBatchLeft:
		return leaveBatch(ctx, tx, ev)
	case TypeDispatchOpened:
		return openDispatch(ctx, tx, ev)
	case TypeDispatchClosed:
		return closeDispatch(ctx, tx, ev)

	// ---- render ----------------------------------------------------------
	case TypeRenderPerformed:
		// Nothing to fold: a render is a projection of the store onto disk, and
		// the store never reads it back (D36, D40). The event is history.
		return nil

	default:
		return fmt.Errorf("store: no projection rule for event type %q", ev.Type)
	}
}

// projectionTables is the current list of tables Rebuild clears and re-folds. It
// is deliberately not v1ProjectionTables: that one is frozen because shipped SQL
// was generated from it, this one grows with the store.
//
// Order matters only for readability; Rebuild defers foreign-key checks to
// commit rather than sorting the deletes.
var projectionTables = v1ProjectionTables

// Rebuild throws the projection away and folds the whole log back over it.
//
// It is the recovery and verification operation the shape buys, not a query
// path. Because the live path and this one call the same applyEvent, a rebuild
// reproducing the maintained projection column-for-column is a test rather than
// an argument.
//
// The whole rebuild is one transaction, which is also its safety property: a
// failure halfway rolls back and leaves the live projection intact. The
// shadow-table-and-swap `store-fork` flagged for scale buys shorter write locks,
// which a single-user single-host store with one connection (D34, D43) does not
// need; if that ever stops being true, this is the function that changes.
func (s *Store) Rebuild(ctx context.Context) error {
	events, err := s.Events(ctx)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Parent and tier references make a wholesale clear order-dependent; defer
	// the checks to commit instead of topologically sorting the deletes.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("store: rebuild: %w", err)
	}
	// Stand the delete guards down, in the same transaction, for exactly as long
	// as the rebuild lasts.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO store_meta (key, value) VALUES (?, '1')
		 ON CONFLICT (key) DO UPDATE SET value = '1'`, rebuildSentinel); err != nil {
		return fmt.Errorf("store: rebuild: %w", err)
	}
	for _, table := range projectionTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil { //nolint:gosec // table names are this package's own constants
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}
	for _, ev := range events {
		if err := applyEvent(ctx, tx, ev); err != nil {
			return fmt.Errorf("store: rebuild at event %s: %w", ev.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ?`, rebuildSentinel); err != nil {
		return fmt.Errorf("store: rebuild: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rebuild: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

func insertNode(ctx context.Context, tx *sql.Tx, ev Event, scale Scale) error {
	var p NodeBirth
	if err := decode(ev, &p); err != nil {
		return err
	}
	matter, err := matterOf(ctx, tx, scale, ev.Subject, p.Parent)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO nodes (id, kind, repo, matter, parent, locator, title, lifecycle,
		                    sort_key, birth_event, last_event)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Subject, string(scale), ev.Repo, matter, nullable(p.Parent),
		p.Locator, p.Title, string(Planned), p.SortKey, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

// matterOf resolves the Matter a node belongs to. A Matter is its own Matter;
// anything else inherits its parent's, which is what makes `matter` a derived
// fact the payload cannot contradict.
func matterOf(ctx context.Context, tx *sql.Tx, scale Scale, id, parent string) (string, error) {
	if scale == ScaleMatter {
		if parent != "" {
			return "", fmt.Errorf("store: a Matter has no parent (D20), but %s names %s", id, parent)
		}
		return id, nil
	}
	if parent == "" {
		return "", fmt.Errorf("store: a %s needs a parent", scale)
	}
	var matter string
	err := tx.QueryRowContext(ctx, `SELECT matter FROM nodes WHERE id = ?`, parent).Scan(&matter)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("store: %s names parent %s, which is not a node", id, parent)
	}
	if err != nil {
		return "", fmt.Errorf("store: resolve the Matter of %s: %w", id, err)
	}
	return matter, nil
}

// applyTransition moves a node's lifecycle, and only from the state the event
// says it was in.
//
// The `AND lifecycle = ?` is load-bearing: with it, an event describing a
// transition that could not have happened fails at write time; without it, the
// store would append the event and quietly agree. This is `store-fork`'s
// "assert a guarded transition affected exactly one row" finding, kept.
func applyTransition(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Transition
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.From == "" || p.To == "" {
		return fmt.Errorf("store: %s must record both ends of the transition", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET lifecycle = ?, last_event = ? WHERE id = ? AND lifecycle = ?`,
		string(p.To), ev.ID, ev.Subject, string(p.From))
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// applyReorder rewrites the sibling order of subject's children.
//
// sort_key is presentation-only (D51): the values below are dense multiples of
// sortKeyGap with no meaning beyond their relative order, and no code path
// infers sequence from row order or from a key's magnitude. Ordering that means
// something comes only from `blocked-by` edges.
func applyReorder(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Reordered
	if err := decode(ev, &p); err != nil {
		return err
	}
	if len(p.Order) == 0 {
		return fmt.Errorf("store: %s names no order", ev.Type)
	}
	for i, id := range p.Order {
		res, err := tx.ExecContext(ctx,
			`UPDATE nodes SET sort_key = ?, last_event = ?
			 WHERE id = ? AND parent = ? AND kind = 'step' AND tombstone_event IS NULL`,
			int64(i+1)*sortKeyGap, ev.ID, id, ev.Subject)
		if err != nil {
			return fmt.Errorf("store: project %s: %w", ev.Type, err)
		}
		if err := exactlyRows(res, 1, ev); err != nil {
			return fmt.Errorf("%w (position %d names %s, which is not a live Step under %s)", err, i, id, ev.Subject)
		}
	}
	return nil
}

// applyReplace tombstones a Step and births its replacement in the same sibling
// position. Amendment is complete for the first time because identity removed
// the old obstruction (D44): the replaced Step keeps its history and its ULID
// stays spent, and every event that referenced it stays a valid reference.
func applyReplace(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Replaced
	if err := decode(ev, &p); err != nil {
		return err
	}
	if len(p.Replacement) != IDLen {
		return fmt.Errorf("store: %s names replacement %q, which is not an identity", ev.Type, p.Replacement)
	}

	var (
		repo, matter, parent string
		sortKey              int64
	)
	err := tx.QueryRowContext(ctx,
		`SELECT repo, matter, COALESCE(parent, ''), sort_key FROM nodes
		 WHERE id = ? AND tombstone_event IS NULL`, ev.Subject).
		Scan(&repo, &matter, &parent, &sortKey)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names %s, which is not a live node", ev.Type, ev.Subject)
	}
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}

	if err := tombstone(ctx, tx, "nodes", ev.Subject, ev); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO nodes (id, kind, repo, matter, parent, locator, title, lifecycle,
		                    sort_key, birth_event, last_event)
		 VALUES (?, 'step', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Replacement, repo, matter, nullable(parent), p.Locator, p.Title,
		string(Planned), sortKey, ev.ID, ev.ID); err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func tombstoneNode(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Removed
	if err := decode(ev, &p); err != nil {
		return err
	}
	return tombstone(ctx, tx, "nodes", ev.Subject, ev)
}

func bindReference(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p ReferenceBound
	if err := decode(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET external_ref = ?, last_event = ? WHERE id = ? AND tombstone_event IS NULL`,
		p.Ref, ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

func closeGate(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p GateClosed
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.Gate == "" || p.Scale == "" {
		return fmt.Errorf("store: %s must name a gate and a scale", ev.Type)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO gate_state (node, gate, scale, closed_at, last_event) VALUES (?, ?, ?, ?, ?)`,
		ev.Subject, p.Gate, string(p.Scale), ev.OccurredAt.UTC().Format(timestampLayout), ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backlog
// ---------------------------------------------------------------------------

func insertBacklogEntry(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BacklogEntered
	if err := decode(ev, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO backlog_entries (id, repo, provenance, state, title, detail,
		                              origin_node, birth_event, last_event)
		 VALUES (?, ?, ?, 'entered', ?, ?, ?, ?, ?)`,
		ev.Subject, ev.Repo, string(p.Provenance), p.Title, p.Detail,
		nullable(p.OriginNode), ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func planBacklogEntry(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BacklogPlanned
	if err := decode(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE backlog_entries SET state = 'planned', matter = ?, last_event = ?
		 WHERE id = ? AND state = 'entered'`, p.Matter, ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func declineBacklogEntry(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BacklogDeclined
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.Reason == "" {
		return fmt.Errorf("store: %s must carry a reason: declined has to be distinguishable from not-yet-acted-upon", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE backlog_entries SET state = 'declined', detail = ?, last_event = ?
		 WHERE id = ? AND state = 'entered'`, p.Reason, ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// ---------------------------------------------------------------------------
// Tiers
// ---------------------------------------------------------------------------

func insertRepo(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RepoAttached
	if err := decode(ev, &p); err != nil {
		return err
	}
	if ev.Repo != ev.Subject {
		return fmt.Errorf("store: %s must carry its own Repo as its repo dimension", ev.Type)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO repos (id, remote_url, identity_remote, label, birth_event, last_event)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Subject, nullable(p.RemoteURL), nullable(p.IdentityRemote), nullable(p.Label), ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func insertClone(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p CloneAttached
	if err := decode(ev, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO clones (id, repo, git_common_dir, label, birth_event, last_event)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Subject, p.Repo, p.GitCommonDir, p.Label, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func insertWorktree(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p WorktreeAttached
	if err := decode(ev, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO worktrees (id, clone, name, birth_event, last_event) VALUES (?, ?, ?, ?, ?)`,
		ev.Subject, p.Clone, nullable(p.Name), ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

// adoptRepoKey is remote adoption: the Repo keeps its ULID and its history, and
// only its natural key changes (D37, PLAN 1.1). Nothing downstream keys on the
// natural key, which is exactly why this is an UPDATE and not a new Repo.
func adoptRepoKey(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RepoKeyAdopted
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.RemoteURL == "" {
		return fmt.Errorf("store: %s must name the remote being adopted", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE repos SET remote_url = ?, identity_remote = ?, last_event = ?
		 WHERE id = ? AND remote_url IS NULL`,
		p.RemoteURL, nullable(p.IdentityRemote), ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func relinkClone(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p CloneRelinked
	if err := decode(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE clones SET git_common_dir = ?, last_event = ? WHERE id = ?`,
		p.GitCommonDir, ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func labelClone(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p CloneLabeled
	if err := decode(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE clones SET label = ?, last_event = ? WHERE id = ?`, p.Label, ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// ---------------------------------------------------------------------------
// Cursor, batch, dispatch
// ---------------------------------------------------------------------------

func moveCursor(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p CursorMoved
	if err := decode(ev, &p); err != nil {
		return err
	}
	if ev.Subject != ev.Worktree {
		return fmt.Errorf("store: %s must have the Worktree it moves in as its subject", ev.Type)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO cursors (clone, worktree, node, last_event) VALUES (?, ?, ?, ?)
		 ON CONFLICT (clone, worktree) DO UPDATE SET node = excluded.node, last_event = excluded.last_event`,
		ev.Clone, ev.Worktree, nullable(p.Node), ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func insertBatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BatchCreated
	if err := decode(ev, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO batches (id, name, birth_event, last_event) VALUES (?, ?, ?, ?)`,
		ev.Subject, nullable(p.Name), ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

// joinBatch is an upsert because joining is idempotent per (batch, matter)
// (D58): a Matter that leaves and rejoins is the same membership row moving
// forward, not a second one.
func joinBatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BatchMembership
	if err := decode(ev, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO batch_members (batch, matter, left_event, last_event) VALUES (?, ?, NULL, ?)
		 ON CONFLICT (batch, matter) DO UPDATE SET left_event = NULL, last_event = excluded.last_event`,
		ev.Subject, p.Matter, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func leaveBatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BatchMembership
	if err := decode(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE batch_members SET left_event = ?, last_event = ?
		 WHERE batch = ? AND matter = ? AND left_event IS NULL`,
		ev.ID, ev.ID, ev.Subject, p.Matter)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func openDispatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO dispatches (id, clone, worktree, state, opened_at, birth_event, last_event)
		 VALUES (?, ?, ?, 'open', ?, ?, ?)`,
		ev.Subject, ev.Clone, ev.Worktree, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func closeDispatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p DispatchClosed
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.Reason == "" {
		return fmt.Errorf("store: %s must carry a reason (D59)", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE dispatches SET state = 'closed', close_reason = ?, closed_at = ?, last_event = ?
		 WHERE id = ? AND state = 'open'`,
		string(p.Reason), ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// ---------------------------------------------------------------------------
// Shared projection mechanics
// ---------------------------------------------------------------------------

func decode(ev Event, into any) error {
	if err := json.Unmarshal(ev.Payload, into); err != nil {
		return fmt.Errorf("store: event %s (%s) payload: %w", ev.ID, ev.Type, err)
	}
	return nil
}

// exactlyRows is the projection's own sanity check. An event that moves no row,
// or more rows than it should, means the log and the projection have diverged —
// and silently succeeding there is how a projection quietly stops being one.
func exactlyRows(res sql.Result, want int64, ev Event) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	if n != want {
		return fmt.Errorf("store: event %s (%s) touched %d projection rows, want %d",
			ev.ID, ev.Type, n, want)
	}
	return nil
}
