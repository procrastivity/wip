package store

import (
	"context"
	"database/sql"
	"fmt"
)

// sortKeyGap is the spacing between sibling sort keys. It exists so an insert in
// the middle of a plan usually needs no other row to move; when the gap runs
// out, the emitter reorders the whole sibling set, which is a `step.reordered`
// like any other.
//
// The magnitude is meaningless (D51). Nothing may read a sort key as a sequence
// number, a position count, or an execution order — it orders siblings for
// presentation and nothing else.
const sortKeyGap = 1000

// Tombstones and Canceled are two different mechanisms, and the difference is
// the one the `schema` Brief resolves:
//
//   - **Canceled** is a lifecycle terminal state (`*.canceled`). The node still
//     exists, stays addressable, keeps its whole history, and appears in status
//     and the archive as work that ended without sealing. It is not a deletion,
//     and nothing below touches it.
//   - **A tombstone** is what a structurally *removed* node or edge leaves
//     (`step.removed`, `dependency.removed`, D44). The row is soft-deleted: its
//     identity is never reissued, and every prior event that referenced it stays
//     a valid identity reference (MODEL §10).
//
// The two are orthogonal. A Step may be Canceled and later removed (a canceled
// node in a tombstoned row), or removed while still Planned.

// tombstone soft-deletes one row. It is deliberately generic over the table: the
// same mechanism serves removed nodes and removed edges, so there is one answer
// to "what does removal leave behind" rather than one per entity.
func tombstone(ctx context.Context, tx *sql.Tx, table, id string, ev Event) error {
	res, err := tx.ExecContext(ctx,
		//nolint:gosec // table is one of this package's own constants
		`UPDATE `+table+` SET tombstone_event = ?, last_event = ?
		 WHERE id = ? AND tombstone_event IS NULL`,
		ev.ID, ev.ID, id)
	if err != nil {
		return fmt.Errorf("store: tombstone %s in %s: %w", id, table, err)
	}
	if err := exactlyRows(res, 1, ev); err != nil {
		return fmt.Errorf("%w (%s in %s is already tombstoned or does not exist)", err, id, table)
	}
	return nil
}

// insertEdge projects dependency.added. Edges are rows, not an array on a node,
// so an added edge is one insert and a removed edge is one tombstone — and each
// edge carries its own identity.
//
// The static cycle check does not run here. A cycle is refused *before* the edge
// is persisted, by the verb, so a cycle is never recorded as an event at all
// (MODEL §10's taxonomy is explicit that cycles are refused statically). By the
// time the projection sees a dependency.added, the question has been settled.
func insertEdge(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p DependencyChange
	if err := decode(ev, &p); err != nil {
		return err
	}
	if len(p.Edge) != IDLen {
		return fmt.Errorf("store: %s names edge %q, which is not an identity", ev.Type, p.Edge)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO edges (id, blocked, blocker, birth_event, last_event) VALUES (?, ?, ?, ?, ?)`,
		p.Edge, ev.Subject, p.Blocker, ev.ID, ev.ID)
	if err != nil {
		return fmt.Errorf("store: project %s: %w", ev.Type, err)
	}
	return nil
}

// tombstoneEdge projects dependency.removed.
func tombstoneEdge(ctx context.Context, tx *sql.Tx, ev Event) error {
	var p DependencyChange
	if err := decode(ev, &p); err != nil {
		return err
	}
	return tombstone(ctx, tx, "edges", p.Edge, ev)
}

// Edge is one `blocked-by` relation: Blocked waits for Blocker.
type Edge struct {
	ID      string
	Blocked string
	Blocker string
}
