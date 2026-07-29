package eventsourced

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// applyEvent folds one event into the `nodes` projection.
//
// This function is the projection's entire definition. Its only argument is an
// event, so there is no expressible way to move the projection that is not a
// consequence of something in the log — and the same function is used by the
// live write path (Store.commit) and by the rebuild path (Store.Rebuild),
// which is what makes "the projection is derivable from the log alone" a
// property of the code rather than a claim in a comment.
func applyEvent(ctx context.Context, tx *sql.Tx, ev scenario.Event) error {
	switch ev.Type {
	case scenario.TypeMatterCreated:
		return insertNode(ctx, tx, ev, scenario.KindMatter)
	case scenario.TypeStepCreated:
		return insertNode(ctx, tx, ev, scenario.KindStep)

	case scenario.TypeMatterStarted, scenario.TypeStepStarted:
		return setLifecycle(ctx, tx, ev, scenario.InProgress)
	case scenario.TypeMatterFinished, scenario.TypeStepFinished:
		return setLifecycle(ctx, tx, ev, scenario.Done)

	// v2 (see label.go). Reached only on a database migrated to v2; a v1
	// database cannot contain these events because the verb that emits them
	// refuses to run below v2.
	case TypeMatterLabeled, TypeStepLabeled:
		return setLabel(ctx, tx, ev)

	default:
		return fmt.Errorf("eventsourced: no projection rule for event type %q", ev.Type)
	}
}

func insertNode(ctx context.Context, tx *sql.Tx, ev scenario.Event, kind scenario.Kind) error {
	var p birthPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("eventsourced: event %s (%s) payload: %w", ev.ID, ev.Type, err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO nodes (id, kind, parent, locator, title, lifecycle, birth_event)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.Subject, string(kind), nullable(p.Parent), p.Locator, p.Title,
		string(scenario.Planned), ev.ID)
	if err != nil {
		return fmt.Errorf("eventsourced: project %s: %w", ev.Type, err)
	}
	return nil
}

func setLifecycle(ctx context.Context, tx *sql.Tx, ev scenario.Event, to scenario.Lifecycle) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE nodes SET lifecycle = ? WHERE id = ?`, string(to), ev.Subject)
	if err != nil {
		return fmt.Errorf("eventsourced: project %s: %w", ev.Type, err)
	}
	return exactlyOneRow(res, ev)
}

// exactlyOneRow is the projection's own sanity check: an event that names a
// subject the projection has never heard of means the two have diverged, and
// silently succeeding there is how a projection quietly stops being a
// projection.
func exactlyOneRow(res sql.Result, ev scenario.Event) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("eventsourced: project %s: %w", ev.Type, err)
	}
	if n != 1 {
		return fmt.Errorf("eventsourced: event %s (%s) touched %d projection rows, want 1",
			ev.ID, ev.Type, n)
	}
	return nil
}

// Rebuild throws the projection away and folds the whole log back over an
// empty table. Nothing outside this package needs it; it exists so the claim
// "state is derived" is testable rather than architectural.
//
// A real implementation would rebuild into a shadow table and swap, so a
// failure halfway leaves the live projection intact. This is spike-grade: the
// whole thing is one transaction, so a failure rolls back.
func (s *Store) Rebuild(ctx context.Context) error {
	evs, err := readEvents(ctx, s.db)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventsourced: begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Parent references make a wholesale DELETE order-dependent; defer the
	// check to commit instead of sorting the delete.
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("eventsourced: rebuild: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM nodes`); err != nil {
		return fmt.Errorf("eventsourced: clear projection: %w", err)
	}
	for _, ev := range evs {
		if err := applyEvent(ctx, tx, ev); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventsourced: commit rebuild: %w", err)
	}
	return nil
}
