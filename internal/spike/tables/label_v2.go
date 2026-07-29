package tables

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/procrastivity/wip/internal/spike/scenario"
)

// This file is the entire Go half of the v2 migration exercise: a new verb
// setting a new optional attribute, emitting a new event type. It is kept in
// its own file so the increment over v1 is legible as a unit — see report.md
// for the count.

// Event types added by v2. They are registered in the schema by v2Statements;
// this is the Go-side name for them.
const (
	typeMatterLabeled = "matter.labeled"
	typeStepLabeled   = "step.labeled"
)

const labelSQL = `
UPDATE nodes SET label = :label, last_event_id = :event
WHERE id = :id`

// Label sets a node's short mutable label (v2). Like every other verb it is a
// pre-check plus one mutation, so it inherits invariant 1 for free: the same
// apply, the same triggers, no new coupling code.
func (s *Store) Label(ctx context.Context, nodeID, label string) error {
	if s.version < 2 {
		return fmt.Errorf("%w: labels need schema v2, this store is at v%d", ErrRefused, s.version)
	}
	if label == "" {
		return fmt.Errorf("%w: an empty label is a removal, which is a separate verb", ErrRefused)
	}
	return s.inTx(ctx, func(t *txn) error {
		n, err := t.node(nodeID)
		if err != nil {
			return err
		}
		eventType := typeStepLabeled
		if n.Kind == scenario.KindMatter {
			eventType = typeMatterLabeled
		}
		return t.apply(mutation{
			eventType: eventType,
			subject:   n.ID,
			payload:   map[string]any{"label": label, "locator": n.Locator},
			sql:       labelSQL,
			args: []any{
				sql.Named("id", n.ID),
				sql.Named("label", label),
			},
		})
	})
}
