package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Project config, and the one documented exception to "everything durable is a
// projection of the log."
//
// Gates are **declared up front**, per project, statically knowable and never
// runtime-conditional (D4) — a declaration is configuration, not something that
// happened, and there is no gate-declaration event in the P1 taxonomy to fold.
// The same goes for the rest of project config (strategies, and whatever later
// Matters earn): it binds to the Repo tier and lives in the store (D42, D54),
// distinct from `chassis`'s tool config on disk.
//
// So these two tables are written directly, and Rebuild leaves them untouched.
// The seam is deliberate and narrow: config says how the project is *set up*,
// the log says what *happened*, and a rebuild reconstructs only the latter.

// SetConfig writes one project-config key at the Repo tier. Clones may not
// diverge (D42), which is exactly what keying it at Repo rather than Clone buys.
func (s *Store) SetConfig(ctx context.Context, repo, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO config (repo, key, value) VALUES (?, ?, ?)
		 ON CONFLICT (repo, key) DO UPDATE SET value = excluded.value`, repo, key, value); err != nil {
		return fmt.Errorf("store: write config %s for %s: %w", key, repo, err)
	}
	return nil
}

// Config reads one project-config key. The second result reports presence, so a
// caller can tell "set to empty" from "never set".
func (v View) Config(ctx context.Context, repo, key string) (string, bool, error) {
	var value string
	err := v.q.QueryRowContext(ctx,
		`SELECT value FROM config WHERE repo = ? AND key = ?`, repo, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read config %s for %s: %w", key, repo, err)
	}
	return value, true, nil
}

// DeclareGate binds a gate to a scale for a Repo (D4, D12).
//
// The store holds gate state at any scale (see gate_state) because MODEL §9 puts
// gate bindings and gate state at Matter, Stage and Step. That is a storage
// capability and not a declaration: this dogfood declares exactly one gate,
// `reviewed-local: matter` (HANDOFF §1.2), and declaring `verified`, `reviewed`
// or `ci-green` would make its Matters structurally unable to seal (MODEL §2.3).
//
// Gate-order monotonicity (D12) is not checked here: `guards` owns that check and
// `write-surface`'s declare verb calls it as a precondition, the mirror of the
// cycle check this package owns and those Matters call.
func (s *Store) DeclareGate(ctx context.Context, repo, gate string, scale Scale) error {
	switch scale {
	case ScaleMatter, ScaleStage, ScaleStep:
	default:
		return fmt.Errorf("store: %q is not a scale a gate can bind to", scale)
	}
	if gate == "" {
		return fmt.Errorf("store: a gate declaration needs a gate name")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO gate_declarations (repo, gate, scale) VALUES (?, ?, ?)
		 ON CONFLICT (repo, gate) DO UPDATE SET scale = excluded.scale`,
		repo, gate, string(scale)); err != nil {
		return fmt.Errorf("store: declare gate %s for %s: %w", gate, repo, err)
	}
	return nil
}
