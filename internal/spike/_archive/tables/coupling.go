package tables

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// This file is the Go half of MODEL §10 invariant 1. The SQL half is in
// schema.go; between them the rule is "no nodes write without exactly one
// event about that node, in the same transaction".
//
// The containment argument, in three parts:
//
//  1. No verb ever holds a *sql.Tx. `inTx` hands verbs a *txn, whose write
//     surface is the single method `apply`.
//  2. `apply` is the only function in the package that executes a statement
//     against `nodes`. It mints the event ULID, writes the audit row, then
//     writes the node row binding that same ULID — the caller cannot supply,
//     skip, or reuse an event id.
//  3. If (1) or (2) is ever broken — by a later edit, by a different package,
//     by the sqlite3 CLI — the triggers in schema.go abort the transaction.
//     The guarantee does not depend on this file being obeyed.

// mutation is a verb's whole effect on the store: one event, and one nodes row
// write that must be caused by it. It is data, not a callback, so that no
// caller ever receives a handle it could write through.
//
// `sql` is executed with the mutation's own named args plus `:event`, bound by
// apply to the ULID of the event it just wrote. Every mutation statement must
// bind :event into last_event_id — a statement that does not is rejected by
// the nodes triggers, not by convention.
type mutation struct {
	eventType string
	subject   string
	payload   map[string]any
	sql       string
	args      []any
}

// txn is the only thing a verb body is given. Reads are free; the sole write
// path is apply.
type txn struct {
	ctx context.Context
	tx  *sql.Tx
	s   *Store
}

// inTx runs one verb. A verb that returns an error writes nothing: the
// transaction rolls back, so a refused command leaves no event behind (the
// conformance run checks this directly).
func (s *Store) inTx(ctx context.Context, fn func(*txn) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(&txn{ctx: ctx, tx: tx, s: s}); err != nil {
		return err
	}
	return tx.Commit()
}

const insertEventSQL = `
INSERT INTO events (id, type, occurred_at, repo, clone, worktree, subject, payload)
VALUES (:id, :type, :occurred_at, :repo, :clone, :worktree, :subject, :payload)`

// apply writes exactly one event and exactly one nodes row, in that order,
// inside the caller's transaction.
func (t *txn) apply(m mutation) error {
	eventID := t.s.ulid.New()

	payload, err := json.Marshal(m.payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", m.eventType, err)
	}
	repo, clone, worktree := t.s.dimensions(m.eventType)

	if _, err := t.tx.ExecContext(t.ctx, insertEventSQL,
		sql.Named("id", eventID),
		sql.Named("type", m.eventType),
		sql.Named("occurred_at", t.s.now().UTC().Format(time.RFC3339Nano)),
		sql.Named("repo", repo),
		sql.Named("clone", clone),
		sql.Named("worktree", worktree),
		sql.Named("subject", m.subject),
		sql.Named("payload", string(payload)),
	); err != nil {
		return fmt.Errorf("append %s to the log: %w", m.eventType, err)
	}

	args := make([]any, 0, len(m.args)+1)
	args = append(args, m.args...)
	args = append(args, sql.Named("event", eventID))

	res, err := t.tx.ExecContext(t.ctx, m.sql, args...)
	if err != nil {
		return fmt.Errorf("apply %s: %w", m.eventType, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("apply %s: %w", m.eventType, err)
	}
	if n != 1 {
		// Not reachable through the verbs (each is keyed on a primary key and
		// pre-checked), but a mutation that touched 0 or 2 rows would mean one
		// event standing for something other than one node transition.
		return fmt.Errorf("apply %s: touched %d node rows, want exactly 1", m.eventType, n)
	}
	return nil
}

// dimensions stamps the tier context per D56's static-function-of-type rule.
// Go and the schema hold the same rule from two directions: this decides what
// to write, the events_dimensions trigger refuses anything that disagrees.
func (s *Store) dimensions(eventType string) (repo, clone, worktree any) {
	wantRepo, wantClone, wantWorktree := scenario.RequiredDimensions(eventType)
	return dim(wantRepo, s.env.Repo), dim(wantClone, s.env.Clone), dim(wantWorktree, s.env.Worktree)
}

func dim(required bool, value string) any {
	if !required {
		return nil
	}
	return value
}
