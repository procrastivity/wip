package eventsourced

// ---------------------------------------------------------------------------
// v2 — the migration exercise, kept in one file on purpose.
//
// docs/store-fork/scenario.md: "nodes gain an optional `label` — a short
// mutable attribute set after birth by a new verb, emitting a new event type
// (matter.labeled / step.labeled), and surfaced in the 'what is in progress'
// answer."
//
// Everything the change needed is here, except two things that could not
// honestly be moved: the numbered migration itself (migrations.go, one
// `ALTER TABLE`) and two cases in the projection switch (project.go). Those
// three edits plus this file are the entire cost; see report.md §3.
// ---------------------------------------------------------------------------

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// The v2 event types. The envelope does not change — only the taxonomy grows,
// which is MODEL §10's own prediction about later phases.
const (
	TypeMatterLabeled = "matter.labeled"
	TypeStepLabeled   = "step.labeled"
)

// labelPayload is the whole of the new event's type-specific fields.
type labelPayload struct {
	Label string `json:"label"`
}

// LabeledNode is a node as v2 reports it: the v1 answer plus the new
// attribute. The shared harness's scenario.Node is fixed at v1 and cannot be
// edited by a spike, so the v2 read path is a second method rather than a
// wider return type — an artefact of the harness, not of the shape.
type LabeledNode struct {
	scenario.Node
	Label string
}

// ErrNeedsV2 is returned when a v2 verb or read is used against a handle
// opened at an older schema version.
var ErrNeedsV2 = errors.New("eventsourced: labels require schema v2")

// Label sets or replaces a node's label. Mutable attribute, set after birth,
// one event per call — the same rule as every other verb, through the same
// commit path, which is the point: adding a verb did not require adding a way
// to write.
func (s *Store) Label(ctx context.Context, nodeID, label string) error {
	if s.version < 2 {
		return ErrNeedsV2
	}
	return s.commit(ctx, func(ctx context.Context, tx *sql.Tx) ([]draft, error) {
		node, err := loadNode(ctx, tx, nodeID)
		if err != nil {
			return nil, err
		}
		return []draft{{
			typ:     labeledType(node.Kind),
			subject: node.ID,
			payload: labelPayload{Label: label},
		}}, nil
	})
}

func labeledType(k scenario.Kind) string {
	if k == scenario.KindMatter {
		return TypeMatterLabeled
	}
	return TypeStepLabeled
}

// setLabel is the v2 projection rule, called from applyEvent.
func setLabel(ctx context.Context, tx *sql.Tx, ev scenario.Event) error {
	var p labelPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("eventsourced: event %s (%s) payload: %w", ev.ID, ev.Type, err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE nodes SET label = ? WHERE id = ?`, p.Label, ev.Subject)
	if err != nil {
		return fmt.Errorf("eventsourced: project %s: %w", ev.Type, err)
	}
	return exactlyOneRow(res, ev)
}

// inProgressLabeledSQL is inProgressSQL with one more column. Rows born under
// v1 answer with the column default — the empty string — with no backfill.
const inProgressLabeledSQL = `
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, label
FROM   nodes
WHERE  lifecycle = 'in-progress'
ORDER  BY birth_event`

// InProgressLabeled is the v2 answer to the founding question.
func (s *Store) InProgressLabeled(ctx context.Context) ([]LabeledNode, error) {
	if s.version < 2 {
		return nil, ErrNeedsV2
	}
	rows, err := s.db.QueryContext(ctx, inProgressLabeledSQL)
	if err != nil {
		return nil, fmt.Errorf("eventsourced: in-progress (v2): %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LabeledNode
	for rows.Next() {
		var n LabeledNode
		if err := rows.Scan(&n.ID, &n.Kind, &n.Parent, &n.Locator, &n.Title, &n.Lifecycle, &n.Label); err != nil {
			return nil, fmt.Errorf("eventsourced: scan in-progress row (v2): %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventsourced: in-progress (v2): %w", err)
	}
	return out, nil
}
