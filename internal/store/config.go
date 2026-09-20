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
	// TrackerProjectKey is the provider-neutral configured-project value.
	// Empty means none. Providers without a use for it ignore the value.
	TrackerProjectKey = "tracker.project"
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
// Configuration changes are lazy: this config.set write queues no candidate.
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

// TrackerBacklogPush controls whether `wip plumbing backlog add` also delegates the new
// entry through the outbox. It is Repo-tier configuration, not domain history.
type TrackerBacklogPush string

const (
	// TrackerBacklogPushManual leaves a new entry entered; `wip plumbing backlog delegate`
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
// Configuration changes are lazy: this config.set write delegates no existing
// entry.
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

// Project config, which is no longer the exception to "everything durable is a
// projection of the log."
//
// Gates are still **declared up front**, per project, statically knowable and
// never runtime-conditional (D4), and config still binds to the Repo tier and
// lives in the store (D42, D54), distinct from `chassis`'s tool config on disk.
// What changed at schema v13 is where the truth lives: `config.set`,
// `gate.declared` and `gate.exemption-repaired` are registered event types,
// project.go folds them into these three tables, and Rebuild clears and refolds
// them like every other projection. Config is evented like everything else,
// still Repo-tier, still D42-bound — so D36's "the store is the source of
// truth" loses its asterisk.
//
// The three functions below are ordinary verbs from schema v13: each builds
// exactly one Draft and goes through Commit (§10 invariant 1; D61
// append-plus-projection atomicity), the same shape every writesurface verb
// uses. Below v13 a store's taxonomy has no room for these event types at all
// (events.type references event_types, and the migration that seeds them
// has not run), so each keeps the direct write it always made at that
// version — a version-gating floor, not a stylistic choice.

// SetConfig writes one project-config key at the Repo tier, through the one
// write path from schema v13 on. Clones may not diverge (D42), which is
// exactly what keying it at Repo rather than Clone buys. The key set is
// last-write-wins in the table, and every value a key has ever held stays
// readable in the log.
func (s *Store) SetConfig(ctx context.Context, repo, key, value string) error {
	if s.version < 13 {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO config (repo, key, value) VALUES (?, ?, ?)
			 ON CONFLICT (repo, key) DO UPDATE SET value = excluded.value`, repo, key, value); err != nil {
			return fmt.Errorf("store: write config %s for %s: %w", key, repo, err)
		}
		return nil
	}
	_, err := s.Commit(ctx, Request{Actor: ActorHuman, Env: Env{Repo: repo}},
		func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeConfigSet, Subject: repo, Payload: ConfigSet{Key: key, Value: value}}}, nil
		})
	return err
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

// DeclareGate binds a gate to a scale for a Repo (D4, D12), through the one
// write path from schema v13 on. At schema v8 and later, the first
// declaration also exempts every node at that scale that is already sealed
// under the prior declaration set — computed from Tx state at declaration
// time and carried explicitly in the gate.declared payload, never recomputed
// at fold time (the resolved open call: explicit beats fold-time
// recomputation for narratability, the fidelity posture MODEL §10 already
// pays for elsewhere).
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
		// Pre-v8 has no exemption table to snapshot into and no evented
		// equivalent to reach for — this upsert predates the whole notion of
		// a prospective exemption.
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO gate_declarations (repo, gate, scale) VALUES (?, ?, ?)
			 ON CONFLICT (repo, gate) DO UPDATE SET scale = excluded.scale`,
			repo, gate, string(scale)); err != nil {
			return fmt.Errorf("store: declare gate %s for %s: %w", gate, repo, err)
		}
		return nil
	}

	if s.version < 13 {
		// gate.declared is not in this store's taxonomy below v13 (no
		// event_types row for events.type to reference), so the write stays
		// what it was at schema 8-12: one transaction, no event.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin gate declaration %s for %s: %w", gate, repo, err)
		}
		defer func() { _ = tx.Rollback() }()
		v := View{q: tx, schemaVersion: s.version}

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

		exempt, err := v.sealedNodesAtScale(ctx, repo, scale)
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

	// v13+: an ordinary verb through Commit. The fold refuses a second
	// gate.declared for the same (repo, gate) at any scale — the first
	// declaration fixes the prospective boundary (D12) — so an identical
	// re-declaration is caught here and emits nothing rather than reach the
	// fold with a doomed draft.
	declared, err := s.GateDeclarations(ctx, repo)
	if err != nil {
		return err
	}
	for _, d := range declared {
		if d.Gate != gate {
			continue
		}
		if d.Scale == scale {
			return nil
		}
		return fmt.Errorf("store: gate %s for %s already binds to %s scale; changing it to %s is refused",
			gate, repo, d.Scale, scale)
	}

	_, err = s.Commit(ctx, Request{Actor: ActorHuman, Env: Env{Repo: repo}}, func(ctx context.Context, tx *Tx) ([]Draft, error) {
		exempt, err := tx.sealedNodesAtScale(ctx, repo, scale)
		if err != nil {
			return nil, err
		}
		return []Draft{{
			Type:    TypeGateDeclared,
			Subject: repo,
			Payload: GateDeclared{Gate: gate, Scale: scale, Exempt: exempt},
		}}, nil
	})
	return err
}

// RepairGateExemption adds one exemption that a declaration made before
// schema v8 could not snapshot, through the one write path from schema v13
// on. It is intentionally separate from DeclareGate so a repeated declaration
// can never extend its original applicability boundary.
//
// The write surface owns the incident-repair preconditions. The fold this
// lands through (repairGateExemptionProjection, project.go) is idempotent
// over the row it writes — the same guarantee the direct write it replaces
// made — so a repeat repair is a legitimate no-op, not a divergence.
func (s *Store) RepairGateExemption(ctx context.Context, repo, gate, node string) error {
	if s.version < 8 {
		return fmt.Errorf("store: gate exemptions require schema v8")
	}
	if s.version < 13 {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO gate_exemptions (repo,gate,node) VALUES (?,?,?)
			 ON CONFLICT (repo,gate,node) DO NOTHING`, repo, gate, node); err != nil {
			return fmt.Errorf("store: repair exemption for gate %s on %s: %w", gate, node, err)
		}
		return nil
	}
	_, err := s.Commit(ctx, Request{Actor: ActorHuman, Env: Env{Repo: repo}},
		func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type:    TypeGateExemptionRepaired,
				Subject: node,
				Payload: GateExemptionRepaired{Gate: gate},
			}}, nil
		})
	return err
}

// sealedNodesAtScale returns the live Done nodes at scale whose own and
// enclosing gate declarations are already satisfied — the exemption set a new
// declaration's gate.declared payload snapshots explicitly. It is a View
// method, not a *sql.Tx function, so it is reachable both from the pre-v13
// direct-write branch above and from a live Tx inside DeclareGate's decide
// function at v13+; the fold itself (declareGateProjection, project.go) never
// calls it, because the payload is where the snapshot comes from once an
// event exists to carry it.
func (v View) sealedNodesAtScale(ctx context.Context, repo string, scale Scale) ([]string, error) {
	rows, err := v.nodeList(ctx, `SELECT `+nodeColumns+` FROM nodes
		WHERE repo=? AND kind=? AND lifecycle='done' AND tombstone_event IS NULL
		ORDER BY id`, repo, string(scale))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, node := range rows {
		completion, err := v.NodeCompletion(ctx, node)
		if err != nil {
			return nil, err
		}
		if completion.Sealed {
			out = append(out, node.ID)
		}
	}
	return out, nil
}
