package store

import (
	"context"
	"database/sql"
	"fmt"
)

// TrackerPushLevel controls which provider-neutral tracker candidates are
// queued. It is Repo-tier configuration, not domain history.
type TrackerPushLevel string

const (
	// TrackerPushOff suppresses lifecycle and narration candidates.
	TrackerPushOff TrackerPushLevel = "off"
	// TrackerPushBoundary queues Matter boundary state candidates.
	TrackerPushBoundary TrackerPushLevel = "boundary"
	// TrackerPushNarrated adds Stage closure comments to boundary candidates.
	TrackerPushNarrated TrackerPushLevel = "narrated"
)

const (
	// TrackerPushLevelKey is the explicit Repo-tier push-level config key.
	TrackerPushLevelKey = "tracker.push-level"
	// TrackerBackendKey is the provider-neutral configured-backend marker.
	TrackerBackendKey = "tracker.backend"
	// TrackerTargetKey is the provider-neutral configured-target value.
	TrackerTargetKey = "tracker.target"
	// TrackerCanceledLabelKey is the provider-neutral configured label a
	// provider may apply when it pushes a canceled disposition. Empty means
	// no label. Providers without a use for it ignore the value.
	TrackerCanceledLabelKey = "tracker.canceled-label"
)

// ParseTrackerPushLevel validates one push-level token.
func ParseTrackerPushLevel(value string) (TrackerPushLevel, error) {
	level := TrackerPushLevel(value)
	switch level {
	case TrackerPushOff, TrackerPushBoundary, TrackerPushNarrated:
		return level, nil
	default:
		return "", fmt.Errorf("store: %q is not a tracker push level; expected off, boundary, or narrated", value)
	}
}

// EffectiveTrackerPushLevel resolves the explicit Repo value first. A
// configured provider-neutral backend defaults an otherwise unset level to
// boundary; without one, tracker pushes default to off.
func (v View) EffectiveTrackerPushLevel(ctx context.Context, repo string) (TrackerPushLevel, error) {
	if value, present, err := v.Config(ctx, repo, TrackerPushLevelKey); err != nil {
		return "", err
	} else if present {
		return ParseTrackerPushLevel(value)
	}
	backend, present, err := v.Config(ctx, repo, TrackerBackendKey)
	if err != nil {
		return "", err
	}
	if present && backend != "" {
		return TrackerPushBoundary, nil
	}
	return TrackerPushOff, nil
}

// SetTrackerPushLevel validates and writes the explicit Repo-tier level.
// Configuration changes are lazy: this direct config write emits no event and
// queues no candidate.
func (s *Store) SetTrackerPushLevel(ctx context.Context, repo, value string) (TrackerPushLevel, error) {
	level, err := ParseTrackerPushLevel(value)
	if err != nil {
		return "", err
	}
	if err := s.SetConfig(ctx, repo, TrackerPushLevelKey, string(level)); err != nil {
		return "", err
	}
	return level, nil
}

// TrackerBacklogPush controls whether `wip backlog add` also delegates the new
// entry through the outbox. It is Repo-tier configuration, not domain history.
type TrackerBacklogPush string

const (
	// TrackerBacklogPushManual leaves a new entry entered; `wip backlog delegate`
	// is the explicit exit.
	TrackerBacklogPushManual TrackerBacklogPush = "manual"
	// TrackerBacklogPushAuto delegates every new entry as it is added. Approval
	// and flush stay human either way.
	TrackerBacklogPushAuto TrackerBacklogPush = "auto"
)

// TrackerBacklogPushKey is the explicit Repo-tier backlog-push config key.
const TrackerBacklogPushKey = "tracker.backlog-push"

// ParseTrackerBacklogPush validates one backlog-push token.
func ParseTrackerBacklogPush(value string) (TrackerBacklogPush, error) {
	mode := TrackerBacklogPush(value)
	switch mode {
	case TrackerBacklogPushManual, TrackerBacklogPushAuto:
		return mode, nil
	default:
		return "", fmt.Errorf("store: %q is not a tracker backlog-push mode; expected manual or auto", value)
	}
}

// EffectiveTrackerBacklogPush resolves the explicit Repo value, else manual.
// Unlike EffectiveTrackerPushLevel, a configured backend does not change the
// default: the local backlog holds found things not yet worth a tracker item,
// so pushing every entry is opt-in even when a tracker is wired up.
func (v View) EffectiveTrackerBacklogPush(ctx context.Context, repo string) (TrackerBacklogPush, error) {
	value, present, err := v.Config(ctx, repo, TrackerBacklogPushKey)
	if err != nil {
		return "", err
	}
	if present {
		return ParseTrackerBacklogPush(value)
	}
	return TrackerBacklogPushManual, nil
}

// SetTrackerBacklogPush validates and writes the explicit Repo-tier mode.
// Configuration changes are lazy: this direct config write emits no event and
// delegates no existing entry.
func (s *Store) SetTrackerBacklogPush(ctx context.Context, repo, value string) (TrackerBacklogPush, error) {
	mode, err := ParseTrackerBacklogPush(value)
	if err != nil {
		return "", err
	}
	if err := s.SetConfig(ctx, repo, TrackerBacklogPushKey, string(mode)); err != nil {
		return "", err
	}
	return mode, nil
}

// Project config, and the documented exception to "everything durable is a
// projection of the log."
//
// Gates are **declared up front**, per project, statically knowable and never
// runtime-conditional (D4) — a declaration is configuration, not something that
// happened, and there is no gate-declaration event in the P1 taxonomy to fold.
// The same goes for the rest of project config (strategies, and whatever later
// Matters earn): it binds to the Repo tier and lives in the store (D42, D54),
// distinct from `chassis`'s tool config on disk.
//
// These tables are written directly, and Rebuild leaves them untouched.
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

// DeclareGate binds a gate to a scale for a Repo (D4, D12). At schema v8 and
// later, the first declaration also exempts every node at that scale that was
// already sealed under the prior declaration set. The declaration and its
// snapshot land in one transaction and emit no event.
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
	if s.version < 8 {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO gate_declarations (repo, gate, scale) VALUES (?, ?, ?)
			 ON CONFLICT (repo, gate) DO UPDATE SET scale = excluded.scale`,
			repo, gate, string(scale)); err != nil {
			return fmt.Errorf("store: declare gate %s for %s: %w", gate, repo, err)
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin gate declaration %s for %s: %w", gate, repo, err)
	}
	defer func() { _ = tx.Rollback() }()

	var existing Scale
	err = tx.QueryRowContext(ctx,
		`SELECT scale FROM gate_declarations WHERE repo=? AND gate=?`, repo, gate).Scan(&existing)
	switch {
	case err == nil && existing == scale:
		return nil
	case err == nil:
		return fmt.Errorf("store: gate %s for %s already binds to %s scale; changing it to %s is refused",
			gate, repo, existing, scale)
	case err != sql.ErrNoRows:
		return fmt.Errorf("store: read gate declaration %s for %s: %w", gate, repo, err)
	}

	exempt, err := sealedNodesAtScale(ctx, tx, repo, scale)
	if err != nil {
		return fmt.Errorf("store: snapshot gate declaration %s for %s: %w", gate, repo, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO gate_declarations (repo, gate, scale) VALUES (?, ?, ?)`,
		repo, gate, string(scale)); err != nil {
		return fmt.Errorf("store: declare gate %s for %s: %w", gate, repo, err)
	}
	for _, node := range exempt {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gate_exemptions (repo,gate,node) VALUES (?,?,?)`, repo, gate, node); err != nil {
			return fmt.Errorf("store: exempt %s from gate %s for %s: %w", node, gate, repo, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit gate declaration %s for %s: %w", gate, repo, err)
	}
	return nil
}

// RepairGateExemption adds one exemption that a declaration made before schema
// v8 could not snapshot. The write is project configuration, emits no event,
// and is intentionally separate from DeclareGate so a repeated declaration
// can never extend its original applicability boundary.
//
// The write surface owns the incident-repair preconditions. This store method
// only makes the validated config write idempotent and preserves referential
// integrity.
func (s *Store) RepairGateExemption(ctx context.Context, repo, gate, node string) error {
	if s.version < 8 {
		return fmt.Errorf("store: gate exemptions require schema v8")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO gate_exemptions (repo,gate,node) VALUES (?,?,?)
		 ON CONFLICT (repo,gate,node) DO NOTHING`, repo, gate, node); err != nil {
		return fmt.Errorf("store: repair exemption for gate %s on %s: %w", gate, node, err)
	}
	return nil
}

// sealedNodesAtScale returns the live Done nodes whose own and enclosing gate
// declarations are satisfied before a new declaration becomes operative.
func sealedNodesAtScale(ctx context.Context, tx *sql.Tx, repo string, scale Scale) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE
		candidates(id) AS (
			SELECT id FROM nodes
			WHERE repo=? AND kind=? AND lifecycle='done' AND tombstone_event IS NULL
		),
		ancestry(candidate,node) AS (
			SELECT id,id FROM candidates
			UNION ALL
			SELECT a.candidate,n.parent
			FROM ancestry a JOIN nodes n ON n.id=a.node
			WHERE n.parent IS NOT NULL
		)
		SELECT c.id
		FROM candidates c
		WHERE NOT EXISTS (
			SELECT 1
			FROM ancestry a
			JOIN nodes n ON n.id=a.node
			JOIN gate_declarations d ON d.repo=? AND d.scale=n.kind
			WHERE a.candidate=c.id
			  AND NOT EXISTS (
				SELECT 1 FROM gate_state s WHERE s.node=n.id AND s.gate=d.gate)
			  AND NOT EXISTS (
				SELECT 1 FROM gate_exemptions x
				WHERE x.repo=? AND x.node=n.id AND x.gate=d.gate)
		)
		ORDER BY c.id`, repo, string(scale), repo, repo)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
