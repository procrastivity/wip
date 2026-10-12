package wipdjournal

import (
	"context"
	"database/sql"

	"github.com/procrastivity/wip/internal/wipdwire"
)

// ValidateInstalledPrefix proves an exact historical anchor against the
// Environment's retained, verified authority-event prefix.
func (j *Journal) ValidateInstalledPrefix(ctx context.Context, anchor wipdwire.PrefixAnchor) error {
	if j == nil || ctx == nil {
		return ErrClosed
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return ErrClosed
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = verifyLocalReleasePrefix(tx, j.identity, anchor); err != nil {
		return err
	}
	return tx.Commit()
}
