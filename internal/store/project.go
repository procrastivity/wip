package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
	return applyEventVersion(ctx, tx, ev, latestVersion(register))
}

func applyEventVersion(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
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
		if err := applyTransition(ctx, tx, ev); err != nil {
			return err
		}
		return projectTransitionCandidates(ctx, tx, ev)

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
		if err := closeGate(ctx, tx, ev); err != nil {
			return err
		}
		return projectGateCandidates(ctx, tx, ev)

	// ---- backlog ---------------------------------------------------------
	case TypeBacklogEntered:
		return insertBacklogEntry(ctx, tx, ev)
	case TypeBacklogPlanned:
		return planBacklogEntry(ctx, tx, ev)
	case TypeBacklogDeclined:
		return declineBacklogEntry(ctx, tx, ev)
	case TypeBacklogDelegated:
		return delegateBacklogEntry(ctx, tx, ev)

	// ---- reference -------------------------------------------------------
	case TypeReferenceBound:
		if err := bindReference(ctx, tx, ev); err != nil {
			return err
		}
		if schemaVersion >= 5 {
			return projectLegacyMatterReference(ctx, tx, ev)
		}
		return nil
	case TypeReferenceAdded:
		if err := addTrackerReference(ctx, tx, ev); err != nil {
			return err
		}
		return projectReferenceCandidates(ctx, tx, ev)
	case TypeReferenceRemoved:
		if err := removeTrackerReference(ctx, tx, ev); err != nil {
			return err
		}
		return projectReferenceCandidates(ctx, tx, ev)
	case TypeReferenceRebound:
		if err := reboundTrackerReference(ctx, tx, ev); err != nil {
			return err
		}
		return projectReferenceCandidates(ctx, tx, ev)

	// ---- tracker ---------------------------------------------------------
	case TypeTrackerItemCreated:
		return recordTrackerItemCreated(ctx, tx, ev)
	case TypeTrackerStatePushed:
		return recordTrackerStatePush(ctx, tx, ev)
	case TypeTrackerStateObserved:
		return recordTrackerStateObserved(ctx, tx, ev)
	case TypeOutboxApproved:
		return approveOutbox(ctx, tx, ev)
	case TypeOutboxDeclined:
		return declineOutbox(ctx, tx, ev)
	case TypeOutboxWithheld:
		return withholdOutbox(ctx, tx, ev)
	case TypeOutboxDeliveryFailed:
		return failOutbox(ctx, tx, ev)
	case TypeOutboxRetried:
		return retryOutbox(ctx, tx, ev)
	case TypeOutboxFlushed:
		return flushOutboxComment(ctx, tx, ev)

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
		return insertBatch(ctx, tx, ev, schemaVersion)
	case TypeBatchJoined:
		return joinBatch(ctx, tx, ev, schemaVersion)
	case TypeBatchLeft:
		return leaveBatch(ctx, tx, ev, schemaVersion)
	case TypeBatchDismissed:
		return dismissBatch(ctx, tx, ev)
	case TypeBatchSwept:
		return sweepBatch(ctx, tx, ev, schemaVersion)
	case TypeDispatchOpened:
		return openDispatch(ctx, tx, ev, schemaVersion)
	case TypeDispatchClosed:
		return closeDispatch(ctx, tx, ev, schemaVersion)
	case TypeRunStarted:
		return startRun(ctx, tx, ev, schemaVersion)
	case TypeRunFinished:
		return finishRun(ctx, tx, ev, "completed")
	case TypeRunStoodDown:
		return standDownRun(ctx, tx, ev)
	case TypeRunResumed:
		if err := decodeStrict(ev, &RunResumed{}); err != nil {
			return err
		}
		return validateRunContext(ctx, tx, ev, ev.Subject, true)
	case TypeRunSkipped:
		return validateRunSkipped(ctx, tx, ev)

	// ---- roles -----------------------------------------------------------
	case TypeRoleSpawned:
		return spawnRole(ctx, tx, ev)
	case TypeRoleClosed:
		return closeRole(ctx, tx, ev)

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
var projectionTables = append(append([]string{}, v1ProjectionTables...), "run_matters", "roles", "tracker_references", "tracker_push_records")

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
	for _, table := range projectionTablesForTx(ctx, tx, s.schemaVersion) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil { //nolint:gosec // table names are this package's own constants
			return fmt.Errorf("store: clear %s: %w", table, err)
		}
	}
	for _, ev := range events {
		if err := applyEventVersion(ctx, tx, ev, s.schemaVersion); err != nil {
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
	var (
		matter string
		live   bool
	)
	err := tx.QueryRowContext(ctx,
		`SELECT matter, tombstone_event IS NULL FROM nodes WHERE id = ?`, parent).Scan(&matter, &live)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("store: %s names parent %s, which is not a node", id, parent)
	}
	if err != nil {
		return "", fmt.Errorf("store: resolve the Matter of %s: %w", id, err)
	}
	// A removed node keeps its identity and its whole history (D44), but it is not a
	// place to put anything. A live row under a tombstoned parent is the projection
	// contradicting itself: Children never reaches it, MatterNodes still lists it,
	// and its parent — which the immutability guard treats as part of its identity —
	// names something no reader will resolve.
	if !live {
		return "", fmt.Errorf("store: %s names parent %s, which was removed; a tombstoned node is not a place to put a %s",
			id, parent, scale)
	}
	return matter, nil
}

// applyTransition moves a node's lifecycle, and only from the state the event
// says it was in, and only while the node is still there.
//
// The `AND lifecycle = ?` is load-bearing: with it, an event describing a
// transition that could not have happened fails at write time; without it, the
// store would append the event and quietly agree. This is `store-fork`'s
// "assert a guarded transition affected exactly one row" finding, kept.
//
// `AND tombstone_event IS NULL` is the same guard against the other way a
// transition can be impossible. A removed node keeps its identity and its history
// (D44) but it is out of every answer, so a lifecycle move against it would be a
// state change no reader can reach — and it would make "removed while still
// Planned" (the `schema` Brief, "Tombstones and Canceled") a fact that does not
// stay put. Every other rule here already reads only live rows; this one had been
// left out. See step-05's report.
func applyTransition(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Transition
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.From == "" || p.To == "" {
		return fmt.Errorf("store: %s must record both ends of the transition", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET lifecycle = ?, last_event = ?
		 WHERE id = ? AND lifecycle = ? AND tombstone_event IS NULL`,
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
		kind, repo, matter, parent string
		sortKey                    int64
	)
	err := tx.QueryRowContext(ctx,
		`SELECT kind, repo, matter, COALESCE(parent, ''), sort_key FROM nodes
		 WHERE id = ? AND tombstone_event IS NULL`, ev.Subject).
		Scan(&kind, &repo, &matter, &parent, &sortKey)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names %s, which is not a live node", ev.Type, ev.Subject)
	}
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	// This rule *births* a row, and the row it births is a Step. So unlike removal —
	// where a tombstone means the same thing at every scale, which is why
	// tombstoneNode is kind-agnostic — this one has to know what it is replacing:
	// a Stage arriving here would be silently converted into a Step, with everything
	// it grouped left under a tombstoned parent. Replacing a grouping would have to
	// say what becomes of what it groups, and no P1 event says that. So the scale is
	// checked rather than assumed, and the "stage equivalents" the Brief leaves
	// unearned are not four INSERTs in a migration for this type.
	if kind != string(ScaleStep) {
		return fmt.Errorf("store: %s names %s, which is a %s; replacement is step-scoped (a grouping cannot be replaced without saying what becomes of what it groups)",
			ev.Type, ev.Subject, kind)
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
	// A gate binds to a scale (D12) and closes against a node *at* that scale, so
	// the payload's scale is the subject's own kind and never a second opinion
	// about it. Nothing downstream re-checks it: `archived_matters` asks only
	// whether a declared gate has a row against the Matter, and gate state is one
	// row per (node, gate) — so a mis-scaled close would seal a Matter nobody
	// reviewed, and would take the slot the real close needs.
	//
	// The subject's liveness is deliberately not asked: a close against a removed
	// node is the same known gap `content.created` and `dependency.added` have,
	// and D44 keeps its history readable either way.
	var kind Scale
	switch err := tx.QueryRowContext(ctx,
		`SELECT kind FROM nodes WHERE id = ?`, ev.Subject).Scan(&kind); {
	case err == sql.ErrNoRows:
		return fmt.Errorf("store: %s names no node as its subject (%s)", ev.Type, ev.Subject)
	case err != nil:
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	if p.Scale != kind {
		return fmt.Errorf("store: %s closes %s at %s scale against a %s: a gate closes at the scale of its subject (D12)",
			ev.Type, p.Gate, p.Scale, kind)
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

func delegateBacklogEntry(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p BacklogDelegated
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if !IsIdentityShaped(p.Outbox) || p.IdempotencyKey == "" {
		return fmt.Errorf("store: %s requires an Outbox identity and idempotency key", ev.Type)
	}

	var repo, provenance, title, detail, origin string
	err := tx.QueryRowContext(ctx,
		`SELECT repo,provenance,title,detail,COALESCE(origin_node,'')
		 FROM backlog_entries WHERE id=? AND state='entered'`, ev.Subject).
		Scan(&repo, &provenance, &title, &detail, &origin)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s touched 0 projection rows", ev.Type)
	}
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	if repo != ev.Repo {
		return fmt.Errorf("store: %s backlog entry belongs to another Repo", ev.Type)
	}
	payload, err := json.Marshal(struct {
		Kind       string `json:"kind"`
		Backlog    string `json:"backlog"`
		Provenance string `json:"provenance"`
		Title      string `json:"title"`
		Detail     string `json:"detail,omitempty"`
		Origin     string `json:"origin_node,omitempty"`
	}{"create", ev.Subject, provenance, title, detail, origin})
	if err != nil {
		return fmt.Errorf("store: project %s payload: %w", ev.Type, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outbox_entries
		 (id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
		 VALUES (?,?,'create','queued',?,NULL,?,?,?,?)`,
		p.Outbox, ev.Repo, ev.Subject, p.IdempotencyKey, string(payload), ev.ID, ev.ID); err != nil {
		return fmt.Errorf("store: project %s outbox: %w", ev.Type, err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE backlog_entries SET state='delegated',outbox=?,last_event=?
		 WHERE id=? AND repo=? AND state='entered'`, p.Outbox, ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// ---------------------------------------------------------------------------
// Tracker references
// ---------------------------------------------------------------------------

func requireMatterSubject(ctx context.Context, tx *sql.Tx, ev Event) error {
	var kind string
	err := tx.QueryRowContext(ctx,
		`SELECT kind FROM nodes WHERE id = ? AND tombstone_event IS NULL`, ev.Subject).Scan(&kind)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s subject is not a live Matter", ev.Type)
	}
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	if kind != string(ScaleMatter) {
		return fmt.Errorf("store: %s subject is not a Matter", ev.Type)
	}
	return nil
}

func addTrackerReference(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p ReferenceAdded
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Ref == "" {
		return fmt.Errorf("store: %s must carry a non-empty reference", ev.Type)
	}
	if err := requireMatterSubject(ctx, tx, ev); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO tracker_references (matter,ref,birth_event,last_event)
		 VALUES (?,?,?,?)
		 ON CONFLICT (matter,ref) DO UPDATE SET removed_event=NULL,last_event=excluded.last_event
		 WHERE tracker_references.removed_event IS NOT NULL`,
		ev.Subject, p.Ref, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func removeTrackerReference(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p ReferenceRemoved
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if err := requireMatterSubject(ctx, tx, ev); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE tracker_references SET removed_event=?,last_event=?
		 WHERE matter=? AND ref=? AND removed_event IS NULL`,
		ev.ID, ev.ID, ev.Subject, p.Ref)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func reboundTrackerReference(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p ReferenceRebound
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.From == "" || p.To == "" || p.From == p.To {
		return fmt.Errorf("store: %s needs two different non-empty references", ev.Type)
	}
	if err := requireMatterSubject(ctx, tx, ev); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE tracker_references SET removed_event=?,last_event=?
		 WHERE matter=? AND ref=? AND removed_event IS NULL`,
		ev.ID, ev.ID, ev.Subject, p.From)
	if err != nil {
		return fmt.Errorf("store: project %s source: %w", ev.Type, err)
	}
	if err := exactlyRows(res, 1, ev); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx,
		`INSERT INTO tracker_references (matter,ref,birth_event,last_event)
		 VALUES (?,?,?,?)
		 ON CONFLICT (matter,ref) DO UPDATE SET removed_event=NULL,last_event=excluded.last_event
		 WHERE tracker_references.removed_event IS NOT NULL`,
		ev.Subject, p.To, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s destination: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

// projectLegacyMatterReference preserves the P1 meaning of reference.bound:
// singleton replacement on Matters. Historical child bindings remain inert.
func projectLegacyMatterReference(ctx context.Context, tx *sql.Tx, ev Event) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM nodes WHERE id=?`, ev.Subject).Scan(&kind); err != nil {
		return err
	}
	if kind != string(ScaleMatter) {
		return nil
	}
	var p ReferenceBound
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tracker_references SET removed_event=?,last_event=?
		 WHERE matter=? AND ref<>? AND removed_event IS NULL`, ev.ID, ev.ID, ev.Subject, p.Ref); err != nil {
		return fmt.Errorf("store: project legacy %s: %w", ev.Type, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO tracker_references (matter,ref,birth_event,last_event)
		 VALUES (?,?,?,?)
		 ON CONFLICT (matter,ref) DO UPDATE SET removed_event=NULL,last_event=excluded.last_event`,
		ev.Subject, p.Ref, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project legacy %s: %w", ev.Type, err)
	}
	return nil
}

// Tracker candidates are projections of the domain event that made them
// relevant. Their deterministic identities let live projection and rebuild
// reproduce the same outbox rows without a second event family.
func projectTransitionCandidates(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p Transition
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.TrackerPushLevel == "" || p.TrackerPushLevel == TrackerPushOff {
		return nil // legacy payload or an explicitly suppressed boundary
	}
	if _, err := ParseTrackerPushLevel(string(p.TrackerPushLevel)); err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	v := View{q: tx, schemaVersion: latestVersion(register)}
	n, err := v.Node(ctx, ev.Subject)
	if err != nil {
		return err
	}
	switch n.Kind {
	case ScaleMatter:
		switch ev.Type {
		case TypeMatterStarted, TypeMatterCanceled:
			return queueMatterStateCandidates(ctx, tx, ev, n.ID, nil)
		case TypeMatterFinished:
			sealed, err := trackerNodeSealed(ctx, v, n)
			if err != nil || !sealed {
				return err
			}
			return queueMatterStateCandidates(ctx, tx, ev, n.ID, nil)
		}
	case ScaleStage:
		if ev.Type == TypeStageFinished && p.TrackerPushLevel == TrackerPushNarrated {
			sealed, err := trackerNodeSealed(ctx, v, n)
			if err != nil || !sealed {
				return err
			}
			return queueStageComments(ctx, tx, ev, n)
		}
	}
	return nil
}

func projectGateCandidates(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p GateClosed
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.TrackerPushLevel == "" || p.TrackerPushLevel == TrackerPushOff {
		return nil
	}
	if _, err := ParseTrackerPushLevel(string(p.TrackerPushLevel)); err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	v := View{q: tx, schemaVersion: latestVersion(register)}
	n, err := v.Node(ctx, ev.Subject)
	if err != nil {
		return err
	}
	if n.Kind == ScaleMatter {
		sealed, err := trackerNodeSealed(ctx, v, n)
		if err != nil {
			return err
		}
		if sealed {
			if err := queueMatterStateCandidates(ctx, tx, ev, n.ID, nil); err != nil {
				return err
			}
		}
	}
	if p.TrackerPushLevel != TrackerPushNarrated {
		return nil
	}
	var stages []Node
	switch n.Kind {
	case ScaleMatter:
		rows, err := v.nodeList(ctx, `SELECT `+nodeColumns+` FROM nodes
			WHERE matter=? AND kind='stage' AND lifecycle='done' AND tombstone_event IS NULL
			ORDER BY birth_event`, n.ID)
		if err != nil {
			return err
		}
		stages = rows
	case ScaleStage:
		stages = []Node{n}
	default:
		return nil
	}
	for _, stage := range stages {
		sealed, err := trackerNodeSealed(ctx, v, stage)
		if err != nil {
			return err
		}
		if sealed {
			if err := queueStageComments(ctx, tx, ev, stage); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectReferenceCandidates(ctx context.Context, tx *sql.Tx, ev Event) error {
	var level TrackerPushLevel
	var refs []string
	switch ev.Type {
	case TypeReferenceAdded:
		var p ReferenceAdded
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		level, refs = p.TrackerPushLevel, []string{p.Ref}
	case TypeReferenceRemoved:
		var p ReferenceRemoved
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		level, refs = p.TrackerPushLevel, []string{p.Ref}
	case TypeReferenceRebound:
		var p ReferenceRebound
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		level, refs = p.TrackerPushLevel, []string{p.From, p.To}
	}
	if level == "" || level == TrackerPushOff {
		return nil
	}
	if _, err := ParseTrackerPushLevel(string(level)); err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return queueMatterStateCandidates(ctx, tx, ev, ev.Subject, refs)
}

func queueMatterStateCandidates(ctx context.Context, tx *sql.Tx, ev Event, subject string, onlyRefs []string) error {
	refs := onlyRefs
	if refs == nil {
		rows, err := tx.QueryContext(ctx, `SELECT ref FROM tracker_references
			WHERE matter=? AND removed_event IS NULL ORDER BY birth_event,ref`, subject)
		if err != nil {
			return fmt.Errorf("store: read candidate references: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				return err
			}
			refs = append(refs, ref)
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		disposition, found, err := trackerAggregate(ctx, tx, ref)
		if err != nil {
			return err
		}
		if !found { // removing the final Matter queues no cleanup transition
			continue
		}
		payload, err := json.Marshal(struct {
			Disposition TrackerDisposition `json:"disposition"`
		}{disposition})
		if err != nil {
			return err
		}
		if err := insertTrackerCandidate(ctx, tx, ev, "state", subject, ref, string(payload), ""); err != nil {
			return err
		}
	}
	return nil
}

func queueStageComments(ctx context.Context, tx *sql.Tx, ev Event, stage Node) error {
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_entries
		WHERE kind='comment' AND subject=? AND json_extract(payload,'$.stage')=?`, stage.Matter, stage.ID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil // a Stage closure narrates once; later config cannot replay it
	}
	rows, err := tx.QueryContext(ctx, `SELECT ref FROM tracker_references
		WHERE matter=? AND removed_event IS NULL ORDER BY birth_event,ref`, stage.Matter)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	payload, err := json.Marshal(struct {
		Stage  string `json:"stage"`
		Title  string `json:"title"`
		Action string `json:"action"`
	}{stage.ID, stage.Title, "closed"})
	if err != nil {
		return err
	}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		if err := insertTrackerCandidate(ctx, tx, ev, "comment", stage.Matter, ref, string(payload), stage.ID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func insertTrackerCandidate(ctx context.Context, tx *sql.Tx, ev Event, kind, subject, ref, payload, discriminator string) error {
	key := "tracker:" + ev.ID + ":" + kind + ":" + ref
	if discriminator != "" {
		key += ":" + discriminator
	}
	sum := sha256.Sum256([]byte(key))
	var raw [16]byte
	copy(raw[:], sum[:16])
	id := encodeCrockford(raw)
	_, err := tx.ExecContext(ctx, `INSERT INTO outbox_entries
		(id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
		VALUES (?,?,?,'queued',?,?,?,?,?,?)`, id, ev.Repo, kind, subject, ref, key, payload, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s tracker candidate: %w", ev.Type, err)
	}
	return nil
}

func trackerAggregate(ctx context.Context, tx *sql.Tx, ref string) (TrackerDisposition, bool, error) {
	v := View{q: tx, schemaVersion: latestVersion(register)}
	matters, err := v.nodeList(ctx, `SELECT `+nodeColumns+` FROM nodes
		WHERE id IN (SELECT matter FROM tracker_references WHERE ref=? AND removed_event IS NULL)
		AND kind='matter' AND tombstone_event IS NULL ORDER BY birth_event`, ref)
	if err != nil {
		return "", false, err
	}
	if len(matters) == 0 {
		return "", false, nil
	}
	sealedCount := 0
	for _, matter := range matters {
		if matter.Lifecycle == Canceled {
			continue
		}
		sealed, err := trackerNodeSealed(ctx, v, matter)
		if err != nil {
			return "", false, err
		}
		if sealed {
			sealedCount++
			continue
		}
		return TrackerActive, true, nil
	}
	if sealedCount > 0 {
		return TrackerCompleted, true, nil
	}
	return TrackerCanceled, true, nil
}

func trackerNodeSealed(ctx context.Context, v View, node Node) (bool, error) {
	if node.Lifecycle != Done {
		return false, nil
	}
	cur := node
	for {
		declared, err := v.GateDeclarations(ctx, cur.Repo)
		if err != nil {
			return false, err
		}
		for _, gate := range declared {
			if gate.Scale != cur.Kind {
				continue
			}
			satisfied, err := v.GateSatisfied(ctx, cur.Repo, cur.ID, gate.Gate)
			if err != nil {
				return false, err
			}
			if !satisfied {
				return false, nil
			}
		}
		if cur.Parent == "" {
			return true, nil
		}
		cur, err = v.Node(ctx, cur.Parent)
		if err != nil {
			return false, err
		}
	}
}

func recordTrackerStatePush(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p TrackerStatePushed
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Ref == "" || p.Lease == "" || (p.Disposition != TrackerActive && p.Disposition != TrackerCompleted && p.Disposition != TrackerCanceled) {
		return fmt.Errorf("store: %s has an invalid reference, disposition, or lease", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='flushed',reason='',attempts=attempts+1,last_event=?
		 WHERE id=? AND repo=? AND kind='state' AND state='approved' AND ref=?`,
		ev.ID, ev.Subject, ev.Repo, p.Ref)
	if err != nil {
		return fmt.Errorf("store: project %s outbox: %w", ev.Type, err)
	}
	if err := exactlyRows(res, 1, ev); err != nil {
		return err
	}

	res, err = tx.ExecContext(ctx,
		`INSERT INTO tracker_push_records
		 (ref,disposition,lease,outbox,birth_event,last_event) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(ref) DO UPDATE SET
		   disposition=excluded.disposition,
		   lease=excluded.lease,
		   outbox=excluded.outbox,
		   last_event=excluded.last_event
		 WHERE tracker_push_records.disposition='active'
		    OR tracker_push_records.disposition=excluded.disposition`,
		p.Ref, p.Disposition, p.Lease, ev.Subject, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s push record: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func recordTrackerStateObserved(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p TrackerStateObserved
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Ref == "" || p.Lease == "" || (p.Disposition != TrackerActive && p.Disposition != TrackerCompleted && p.Disposition != TrackerCanceled) {
		return fmt.Errorf("store: %s has an invalid reference, disposition, or lease", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='flushed',reason='',attempts=attempts+1,last_event=?
		 WHERE id=? AND repo=? AND kind='state' AND state='approved' AND ref=?`,
		ev.ID, ev.Subject, ev.Repo, p.Ref)
	if err != nil {
		return fmt.Errorf("store: project %s outbox: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func recordTrackerItemCreated(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p TrackerItemCreated
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Ref == "" {
		return fmt.Errorf("store: %s requires a non-empty reference", ev.Type)
	}

	var backlog string
	err := tx.QueryRowContext(ctx,
		`SELECT subject FROM outbox_entries
		 WHERE id=? AND repo=? AND kind='create' AND state IN ('queued','approved')`,
		ev.Subject, ev.Repo).Scan(&backlog)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s touched 0 projection rows", ev.Type)
	}
	if err != nil {
		return fmt.Errorf("store: project %s creation entry: %w", ev.Type, err)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='flushed',reason='',attempts=attempts+1,last_event=?
		 WHERE id=? AND repo=? AND kind='create' AND state IN ('queued','approved')`,
		ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s outbox: %w", ev.Type, err)
	}
	if err := exactlyRows(res, 1, ev); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE backlog_entries SET last_event=?
		 WHERE id=? AND repo=? AND state='delegated' AND outbox=?`,
		ev.ID, backlog, ev.Repo, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s stub: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func approveOutbox(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxApproved
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='approved',reason='',last_event=?
		 WHERE id=? AND repo=? AND state='queued' AND attempts=0 AND reason=''`,
		ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func declineOutbox(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxDeclined
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("store: %s requires a reason", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='declined',reason=?,last_event=?
		 WHERE id=? AND repo=? AND state IN ('queued','approved','withheld')`,
		p.Reason, ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func withholdOutbox(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxWithheld
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("store: %s requires a reason", ev.Type)
	}
	if p.Cause != "" && !validWithholdCause(p.Cause) {
		return fmt.Errorf("store: %s has invalid cause %q", ev.Type, p.Cause)
	}
	increment := 0
	if p.Attempted {
		increment = 1
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='withheld',reason=?,attempts=attempts+?,last_event=?
		 WHERE id=? AND repo=? AND state='approved'`,
		p.Reason, increment, ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func validWithholdCause(cause WithholdCause) bool {
	switch cause {
	case CauseMalformedCandidate, CauseLocalRegression, CauseSuperseded,
		CauseLeaseMismatch, CausePermanentRefusal, CauseMalformedProviderSuccess,
		CauseUnknownOutcome:
		return true
	default:
		return false
	}
}

func failOutbox(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxDeliveryFailed
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("store: %s requires a reason", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='queued',reason=?,attempts=attempts+1,last_event=?
		 WHERE id=? AND repo=? AND state='approved'`,
		p.Reason, ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func retryOutbox(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxRetried
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='approved',reason='',last_event=?
		 WHERE id=? AND repo=?
		   AND (state='withheld' OR (state='queued' AND attempts>0 AND reason<>''))`,
		ev.ID, ev.Subject, ev.Repo)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func flushOutboxComment(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p OutboxFlushed
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE outbox_entries SET state='flushed',reason='',attempts=attempts+1,last_event=?
		 WHERE id=? AND repo=? AND kind='comment' AND state='approved'`,
		ev.ID, ev.Subject, ev.Repo)
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

func insertBatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p BatchCreated
	if schemaVersion >= 3 {
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		if p.Name == "" && p.Matter == "" {
			// The legacy P1 shape: the old refresh path minted an anonymous
			// Batch with neither name nor Matter, one per dispatch. The event
			// is legal history and must replay (a fold that refuses it makes
			// Rebuild partial over real logs), and it folds the way the v3
			// migration converts the row: closed, swept, at its own birth.
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO batches (id,name,matter,state,close_reason,closed_at,birth_event,last_event)
				 VALUES (?,NULL,NULL,'closed','swept',?,?,?)`,
				ev.Subject, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID); err != nil {
				return fmt.Errorf("store: project %s: %w", ev.Type, err)
			}
			return nil
		}
		if p.Name != "" && p.Matter != "" {
			return fmt.Errorf("store: %s requires either a non-empty name or one Matter", ev.Type)
		}
		if p.Matter != "" {
			if err := validateLiveMatter(ctx, tx, ev.Type, p.Matter); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO batches (id,name,matter,state,birth_event,last_event) VALUES (?,?,?,'live',?,?)`,
			ev.Subject, nullable(p.Name), nullable(p.Matter), ev.ID, ev.ID); err != nil {
			return fmt.Errorf("store: project %s: %w", ev.Type, err)
		}
		if p.Matter != "" {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO batch_members (batch,matter,left_event,last_event) VALUES (?,?,NULL,?)`,
				ev.Subject, p.Matter, ev.ID); err != nil {
				return fmt.Errorf("store: project %s: %w", ev.Type, err)
			}
		}
		return nil
	}
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

func validateLiveMatter(ctx context.Context, tx *sql.Tx, eventType, matter string) error {
	var kind string
	if err := tx.QueryRowContext(ctx,
		`SELECT kind FROM nodes WHERE id=? AND tombstone_event IS NULL`, matter).Scan(&kind); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown or tombstoned Matter %s", eventType, matter)
	} else if err != nil {
		return err
	} else if kind != string(ScaleMatter) {
		return fmt.Errorf("store: %s names %s, which is not a Matter", eventType, matter)
	}
	return nil
}

// joinBatch is an upsert because joining is idempotent per (batch, matter)
// (D58): a Matter that leaves and rejoins is the same membership row moving
// forward, not a second one.
func joinBatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p BatchMembership
	if schemaVersion >= 3 {
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		var name, owner, state sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT name,matter,state FROM batches WHERE id=?`, ev.Subject).Scan(&name, &owner, &state); err == sql.ErrNoRows {
			return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
		} else if err != nil {
			return err
		}
		if state.String != "live" {
			return fmt.Errorf("store: %s names a closed Batch", ev.Type)
		}
		if owner.Valid && owner.String != p.Matter {
			return fmt.Errorf("store: %s cannot add membership to an anonymous Batch", ev.Type)
		}
		if err := validateLiveMatter(ctx, tx, ev.Type, p.Matter); err != nil {
			return err
		}
	} else if err := decode(ev, &p); err != nil {
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

func leaveBatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p BatchMembership
	if schemaVersion >= 3 {
		if err := decodeStrict(ev, &p); err != nil {
			return err
		}
		var owner, state sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT matter,state FROM batches WHERE id=?`, ev.Subject).Scan(&owner, &state); err == sql.ErrNoRows {
			return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
		} else if err != nil {
			return err
		}
		if state.String != "live" {
			return fmt.Errorf("store: %s names a closed Batch", ev.Type)
		}
		if owner.Valid {
			return fmt.Errorf("store: %s cannot remove the membership of an anonymous Batch", ev.Type)
		}
		if err := validateLiveMatter(ctx, tx, ev.Type, p.Matter); err != nil {
			return err
		}
	} else if err := decode(ev, &p); err != nil {
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

func dismissBatch(ctx context.Context, tx *sql.Tx, ev Event) error {
	if err := decodeStrict(ev, &BatchDismissedPayload{}); err != nil {
		return err
	}
	var matter, state sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT matter,state FROM batches WHERE id=?`, ev.Subject).Scan(&matter, &state); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
	} else if err != nil {
		return err
	}
	if matter.Valid {
		return fmt.Errorf("store: %s can dismiss only a named Batch", ev.Type)
	}
	if state.String != "live" {
		return fmt.Errorf("store: %s names a closed Batch", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE batches SET state='closed',close_reason='dismissed',closed_at=?,last_event=? WHERE id=? AND state='live'`,
		ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject)
	if err != nil {
		return err
	}
	return exactlyRows(res, 1, ev)
}

func sweepBatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	if err := decodeStrict(ev, &BatchSweptPayload{}); err != nil {
		return err
	}
	var matter, state sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT matter,state FROM batches WHERE id=?`, ev.Subject).Scan(&matter, &state); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
	} else if err != nil {
		return err
	}
	if !matter.Valid {
		return fmt.Errorf("store: %s can sweep only an anonymous Batch", ev.Type)
	}
	if state.String != "live" {
		return fmt.Errorf("store: %s names a closed Batch", ev.Type)
	}
	at := ev.OccurredAt.UTC().Format(timestampLayout)
	if schemaVersion >= 4 {
		// A role does not outlive its bracket: reaping a Dispatch reaps the
		// roles bound to it, the same way the sweep reaps the Dispatch itself.
		if _, err := tx.ExecContext(ctx,
			`UPDATE roles SET state='closed',close_reason='reaped',closed_at=?,last_event=?
			 WHERE state='open' AND dispatch IN (
				SELECT id FROM dispatches WHERE state='open' AND run IN (SELECT id FROM runs WHERE batch=?))`,
			at, ev.ID, ev.Subject); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE dispatches SET state='closed',close_reason='reaped',closed_at=?,last_event=?
		 WHERE state='open' AND run IN (SELECT id FROM runs WHERE batch=?)`, at, ev.ID, ev.Subject); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET state='closed',close_reason='reaped',closed_at=?,last_event=?
		 WHERE state='open' AND batch=?`, at, ev.ID, ev.Subject); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE batches SET state='closed',close_reason='swept',closed_at=?,last_event=? WHERE id=? AND state='live'`,
		at, ev.ID, ev.Subject)
	if err != nil {
		return err
	}
	return exactlyRows(res, 1, ev)
}

func openDispatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p DispatchOpened
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if (p.Run == "") != (p.Matter == "") {
		return fmt.Errorf("store: %s requires both Run and Matter or neither", ev.Type)
	}
	if p.Run != "" {
		if err := validateRunContext(ctx, tx, ev, p.Run, true); err != nil {
			return err
		}
		var ok int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_matters WHERE run=? AND matter=?`, p.Run, p.Matter).Scan(&ok); err != nil {
			return err
		}
		if ok != 1 {
			return fmt.Errorf("store: %s Matter is outside the Run frozen set", ev.Type)
		}
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM nodes WHERE id=? AND tombstone_event IS NULL`, p.Matter).Scan(&kind); err == sql.ErrNoRows {
			return fmt.Errorf("store: %s names an unknown or tombstoned Matter", ev.Type)
		} else if err != nil {
			return err
		} else if kind != string(ScaleMatter) {
			return fmt.Errorf("store: %s names a node that is not a Matter", ev.Type)
		}
	}

	var err error
	if schemaVersion >= 2 {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO dispatches (id, clone, worktree, run, matter, state, opened_at, birth_event, last_event)
			 VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
			ev.Subject, ev.Clone, ev.Worktree, nullable(p.Run), nullable(p.Matter), ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID)
	} else {
		if p.Run != "" {
			return fmt.Errorf("store: P2 Dispatch requires schema v2")
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO dispatches (id, clone, worktree, state, opened_at, birth_event, last_event) VALUES (?, ?, ?, 'open', ?, ?, ?)`,
			ev.Subject, ev.Clone, ev.Worktree, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID)
	}
	if err != nil {
		// modernc/sqlite reports this as a UNIQUE constraint on either the
		// partial index or its table-qualified column. Both are the same
		// domain refusal; do not make callers depend on the driver wording.
		message := err.Error()
		if strings.Contains(message, "UNIQUE constraint failed") &&
			strings.Contains(message, "dispatches") && strings.Contains(message, "matter") {
			return fmt.Errorf("refusal.dispatch-contention: Matter %s already has an open Dispatch", p.Matter)
		}
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func closeDispatch(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p DispatchClosed
	if err := decode(ev, &p); err != nil {
		return err
	}
	if p.Reason == "" {
		return fmt.Errorf("store: %s must carry a reason (D59)", ev.Type)
	}
	var clone, worktree string
	if err := tx.QueryRowContext(ctx, `SELECT clone,worktree FROM dispatches WHERE id=? AND state='open'`, ev.Subject).Scan(&clone, &worktree); err == sql.ErrNoRows {
		return fmt.Errorf("store: event %s (%s) touched 0 projection rows, want 1", ev.ID, ev.Type)
	} else if err != nil {
		return err
	}
	if clone != ev.Clone || worktree != ev.Worktree {
		return fmt.Errorf("store: %s dimensions disagree with the Dispatch", ev.Type)
	}
	if schemaVersion >= 4 {
		// A role does not outlive its bracket (D59): whatever closes the
		// Dispatch reaps any role still open inside it. An explicitly closed
		// role took its own role.closed first and is untouched here.
		if _, err := tx.ExecContext(ctx,
			`UPDATE roles SET state='closed',close_reason='reaped',closed_at=?,last_event=?
			 WHERE state='open' AND dispatch=?`,
			ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject); err != nil {
			return fmt.Errorf("store: project %s: %w", ev.Type, err)
		}
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

func startRun(ctx context.Context, tx *sql.Tx, ev Event, schemaVersion int) error {
	var p RunStarted
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Batch == "" || len(p.Matters) == 0 {
		return fmt.Errorf("store: %s requires a Batch and non-empty frozen set", ev.Type)
	}
	if err := validateExecutionContext(ctx, tx, ev, ev.Clone); err != nil {
		return err
	}
	if !validRunLocator(p.Locator) {
		return fmt.Errorf("store: %s has malformed Run locator %q", ev.Type, p.Locator)
	}
	if schemaVersion >= 3 {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM batches WHERE id=?`, p.Batch).Scan(&state); err == sql.ErrNoRows {
			return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
		} else if err != nil {
			return err
		}
		if state != "live" {
			return fmt.Errorf("store: %s names a closed Batch", ev.Type)
		}
	} else {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE id=?`, p.Batch).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("store: %s names an unknown Batch", ev.Type)
		}
	}
	seen := make(map[string]struct{}, len(p.Matters))
	for _, matter := range p.Matters {
		if _, duplicate := seen[matter]; duplicate {
			return fmt.Errorf("store: %s repeats Matter %s", ev.Type, matter)
		}
		seen[matter] = struct{}{}
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM nodes WHERE id=? AND tombstone_event IS NULL`, matter).Scan(&kind); err == sql.ErrNoRows {
			return fmt.Errorf("store: %s names an unknown or tombstoned Matter %s", ev.Type, matter)
		} else if err != nil {
			return err
		} else if kind != string(ScaleMatter) {
			return fmt.Errorf("store: %s names %s, which is not a Matter", ev.Type, matter)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id,clone,batch,locator,state,started_at,birth_event,last_event) VALUES (?,?,?,?,'open',?,?,?)`, ev.Subject, ev.Clone, p.Batch, p.Locator, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID); err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	for i, matter := range p.Matters {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_matters(run,matter,ordinal) VALUES(?,?,?)`, ev.Subject, matter, i); err != nil {
			return fmt.Errorf("store: project %s: %w", ev.Type, err)
		}
	}
	return nil
}

func validRunLocator(locator string) bool {
	if !strings.HasPrefix(locator, "run-") {
		return false
	}
	digits := strings.TrimPrefix(locator, "run-")
	if len(digits) < 2 || (len(digits) > 2 && digits[0] == '0') {
		return false
	}
	nonzero := false
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return false
		}
		nonzero = nonzero || digit != '0'
	}
	return nonzero
}

func validateExecutionContext(ctx context.Context, tx *sql.Tx, ev Event, clone string) error {
	var repo, worktreeClone string
	err := tx.QueryRowContext(ctx, `SELECT c.repo,w.clone FROM clones c JOIN worktrees w ON w.id=? WHERE c.id=?`, ev.Worktree, clone).Scan(&repo, &worktreeClone)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Clone or Worktree", ev.Type)
	}
	if err != nil {
		return err
	}
	if ev.Clone != clone || worktreeClone != clone || ev.Repo != repo {
		return fmt.Errorf("store: %s tier dimensions disagree with Clone %s", ev.Type, clone)
	}
	return nil
}

func validateRunContext(ctx context.Context, tx *sql.Tx, ev Event, run string, requireOpen bool) error {
	var owner, state string
	if err := tx.QueryRowContext(ctx, `SELECT clone,state FROM runs WHERE id=?`, run).Scan(&owner, &state); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Run", ev.Type)
	} else if err != nil {
		return err
	}
	if requireOpen && state != "open" {
		return fmt.Errorf("store: %s names a closed Run", ev.Type)
	}
	if err := validateExecutionContext(ctx, tx, ev, owner); err != nil {
		return err
	}
	return nil
}

func finishRun(ctx context.Context, tx *sql.Tx, ev Event, reason string) error {
	if err := decodeStrict(ev, &struct{}{}); err != nil {
		return err
	}
	if err := validateRunContext(ctx, tx, ev, ev.Subject, true); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET state='closed',close_reason=?,closed_at=?,last_event=? WHERE id=? AND state='open'`, reason, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject)
	if err != nil {
		return err
	}
	return exactlyRows(res, 1, ev)
}

func standDownRun(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RunStoodDown
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	var owner, state string
	if err := tx.QueryRowContext(ctx, `SELECT clone,state FROM runs WHERE id=?`, ev.Subject).Scan(&owner, &state); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Run", ev.Type)
	} else if err != nil {
		return err
	}
	if state != "open" {
		return fmt.Errorf("store: %s names a closed Run", ev.Type)
	}
	if p.ActingClone == "" || p.OwningClone == "" || p.OwningClone != owner || p.ActingClone != ev.Clone {
		return fmt.Errorf("store: %s has invalid acting or owning Clone", ev.Type)
	}
	if err := validateExecutionContext(ctx, tx, ev, p.ActingClone); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clones WHERE id=?`, p.OwningClone).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return fmt.Errorf("store: %s names an unknown owning Clone", ev.Type)
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET state='closed',close_reason='stood-down',closed_at=?,last_event=? WHERE id=? AND state='open'`, ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject)
	if err != nil {
		return err
	}
	return exactlyRows(res, 1, ev)
}

// spawnRole births a role instance bound to an open Dispatch (D59). Which
// roles a gate config *activates* (D14) is deliberately not asked here: gate
// declarations are config, not events, so a fold rule reading them would make
// a rebuild depend on state outside the log. Activation is the write surface's
// question; the fold checks only what the log itself can answer.
func spawnRole(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RoleSpawned
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if !KnownRole(p.Name) {
		return fmt.Errorf("store: %s names %q, which is not one of the six roles (MODEL §6)", ev.Type, p.Name)
	}
	var clone, worktree, state string
	if err := tx.QueryRowContext(ctx,
		`SELECT clone,worktree,state FROM dispatches WHERE id=?`, p.Dispatch).Scan(&clone, &worktree, &state); err == sql.ErrNoRows {
		return fmt.Errorf("store: %s names an unknown Dispatch %s", ev.Type, p.Dispatch)
	} else if err != nil {
		return err
	}
	if state != "open" {
		return fmt.Errorf("store: %s names a closed Dispatch %s: a role cannot be spawned into a closed bracket", ev.Type, p.Dispatch)
	}
	if clone != ev.Clone || worktree != ev.Worktree {
		return fmt.Errorf("store: %s dimensions disagree with Dispatch %s", ev.Type, p.Dispatch)
	}
	if err := validateExecutionContext(ctx, tx, ev, clone); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO roles (id,clone,dispatch,name,state,spawned_at,birth_event,last_event)
		 VALUES (?,?,?,?,'open',?,?,?)`,
		ev.Subject, clone, p.Dispatch, string(p.Name),
		ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.ID); err != nil {
		message := err.Error()
		if strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "roles") {
			return fmt.Errorf("refusal.role-contention: Dispatch %s already has an open %s", p.Dispatch, p.Name)
		}
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

func closeRole(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RoleClosed
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Reason != RoleCloseCompleted && p.Reason != RoleCloseReaped {
		return fmt.Errorf("store: %s must carry reason completed or reaped", ev.Type)
	}
	var clone, worktree string
	err := tx.QueryRowContext(ctx,
		`SELECT r.clone, d.worktree FROM roles r JOIN dispatches d ON d.id = r.dispatch
		 WHERE r.id=? AND r.state='open'`, ev.Subject).Scan(&clone, &worktree)
	if err == sql.ErrNoRows {
		return fmt.Errorf("store: event %s (%s) touched 0 projection rows, want 1", ev.ID, ev.Type)
	}
	if err != nil {
		return err
	}
	if clone != ev.Clone || worktree != ev.Worktree {
		return fmt.Errorf("store: %s dimensions disagree with the role's Dispatch", ev.Type)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE roles SET state='closed',close_reason=?,closed_at=?,last_event=?
		 WHERE id=? AND state='open'`,
		string(p.Reason), ev.OccurredAt.UTC().Format(timestampLayout), ev.ID, ev.Subject)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return exactlyRows(res, 1, ev)
}

func validateRunSkipped(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p RunSkipped
	if err := decodeStrict(ev, &p); err != nil {
		return err
	}
	if p.Run == "" || (p.Reason != RunSkipContention && p.Reason != RunSkipBlocked && p.Reason != RunSkipFailed) {
		return fmt.Errorf("store: %s has invalid Run or reason", ev.Type)
	}
	if err := validateRunContext(ctx, tx, ev, p.Run, true); err != nil {
		return err
	}
	var ok int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_matters WHERE run=? AND matter=?`, p.Run, ev.Subject).Scan(&ok); err != nil {
		return err
	}
	if ok != 1 {
		return fmt.Errorf("store: %s Matter is not in the Run frozen set", ev.Type)
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM nodes WHERE id=? AND tombstone_event IS NULL`, ev.Subject).Scan(&kind); err != nil {
		return err
	}
	if kind != string(ScaleMatter) {
		return fmt.Errorf("store: %s subject is not a live Matter", ev.Type)
	}
	return nil
}

func projectionTablesForTx(ctx context.Context, tx *sql.Tx, version int) []string {
	_ = ctx
	_ = tx
	if version < 2 {
		return append([]string{}, v1ProjectionTables...)
	}
	// Child projections first — run_matters under runs, roles under
	// dispatches — so the rebuild remains valid even when foreign-key
	// deferral is unavailable on a driver.
	var out []string
	if version >= 5 {
		out = append(out, "tracker_push_records", "tracker_references")
	}
	if version >= 4 {
		out = append(out, "roles")
	}
	out = append(out, "run_matters")
	return append(out, v1ProjectionTables...)
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

func decodeStrict(ev Event, into any) error {
	trimmed := bytes.TrimSpace(ev.Payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("store: event %s (%s) payload must be a JSON object", ev.ID, ev.Type)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("store: event %s (%s) payload: %w", ev.ID, ev.Type, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("store: event %s (%s) payload has trailing data", ev.ID, ev.Type)
		}
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
