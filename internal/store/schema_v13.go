package store

import (
	"context"
	"database/sql"
	"fmt"
)

// v13 closes the store's one documented persistence exception. Config, gate
// declarations and gate exemptions were mutable primary rows written outside
// the log, so Rebuild left them alone and D36's "the store is the source of
// truth" carried an asterisk. They fold from events from here on, which makes
// the log plus its referenced blobs the whole durable truth.
//
// Two things and no more: the three new event types, and the delete guard that
// makes each table a projection the substrate protects. The tables keep their
// v1/v8 shapes on purpose — no birth_event or last_event column is added.
// `run_matters` is the precedent: a projection keyed by a natural key, with no
// identity and no history of its own, carries the no-delete guard alone
// (projectionGuards' `eventLinked`/`born` split exists for exactly this). It is
// also the only honest option before the synthetic-history migration: a NOT NULL
// `REFERENCES events(id)` column has nothing to hold for the rows a store
// already carries.
func v13Statements() []string {
	return []string{
		seedTaxonomy(V13Taxonomy),

		`CREATE TRIGGER config_no_delete BEFORE DELETE ON config
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'config is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER gate_declarations_no_delete BEFORE DELETE ON gate_declarations
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'gate_declarations is a projection: only a rebuild may clear it'); END`,
		`CREATE TRIGGER gate_exemptions_no_delete BEFORE DELETE ON gate_exemptions
		 WHEN NOT EXISTS (SELECT 1 FROM store_meta WHERE key = 'rebuilding')
		 BEGIN SELECT RAISE(ABORT, 'gate_exemptions is a projection: only a rebuild may clear it'); END`,
	}
}

// v13SyntheticHistory is v13's Go-side half (migrations.go's dataMigrate
// hook): it reads the config, gate-declaration and gate-exemption rows a
// pre-v13 store already carries — written by the direct upserts step-02
// retired — and mints the events that let those three tables fold from the log
// like everything else. It lives inside v13 rather than a v14 of its own
// because v13 is unreleased and the two halves are one design move: no store
// has ever carried a v13 schema without this history behind it, so there is no
// "v13 without synthetic history" state to be backward compatible with.
//
// It only mints; it never folds. The three tables already hold the rows this
// backfill is explaining, so folding here would collide with what is already
// there (declareGateProjection refuses a second gate.declared for a
// (repo,gate) that already has one). The projectionVersion bump alongside this
// migration is what actually makes the events count: the next
// ensureProjectionVersion, still inside the same Open, finds an outdated
// projection, clears all three tables and refolds the whole log — the events
// minted here included.
//
// Every event is stamped ActorMigration and is its own origin (no causation
// chain: nothing in the log entailed a migration). Config comes first, then
// every gate.declared, then every gate.exemption-repaired — an order chosen
// because it is also the id order (ids are minted strictly ascending), and
// repairGateExemptionProjection requires its (repo,gate) declaration to have
// already folded when Rebuild reaches the repair.
//
// Snapshot vs. repair (the resolved call, hand-off note 3): a pre-migration
// exemption row cannot say whether it was present at its declaration or added
// later — it is just a row. Bundling all of them into the synthetic
// gate.declared's Exempt would claim a fact this migration cannot know (that
// they existed at declaration time), and would place them earlier, in id
// order, than exemptions the real DeclareGate/RepairGateExemption split
// deliberately keeps apart. So every exemption rides as its own
// gate.exemption-repaired event and gate.declared always mints with an empty
// Exempt: this is exactly GateExemptionRepaired's documented meaning — an
// exemption a declaration could not snapshot — and it is true of literally
// every row this migration is reconstructing, since the synthetic declaration
// mints with nothing to snapshot at all.
//
// An exemption row the fold would refuse (a tombstoned, wrong-Repo or
// wrong-scale node — possible because the pre-v13 RepairGateExemption never
// checked scale) aborts the whole migration rather than being dropped or
// silently repaired into a different shape: the schema_v2.go
// rejectLegacyRuns/rejectLegacyBatches precedent. A backup was already taken
// before this transaction began (migrations.go's backup-before-migrate), so
// refusing here costs nothing the operator cannot recover from after fixing
// the offending row.
func v13SyntheticHistory(ctx context.Context, tx *sql.Tx, ids *migrationIDs) error {
	if err := v13BackfillConfig(ctx, tx, ids); err != nil {
		return err
	}
	declarations, err := v13BackfillGateDeclarations(ctx, tx, ids)
	if err != nil {
		return err
	}
	return v13BackfillGateExemptions(ctx, tx, ids, declarations)
}

// v13BackfillConfig mints one config.set per existing (repo,key) row, at its
// current value. A pre-migration store kept only the last value a key ever
// held, so "one event that sets it to its current value" is the whole honest
// reconstruction available — the values a key held earlier were never
// recorded anywhere this migration can read them back from.
func v13BackfillConfig(ctx context.Context, tx *sql.Tx, ids *migrationIDs) error {
	rows, err := tx.QueryContext(ctx, `SELECT repo, key, value FROM config ORDER BY repo, key`)
	if err != nil {
		return fmt.Errorf("store: migration v13: read config: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var repo, key, value string
		if err := rows.Scan(&repo, &key, &value); err != nil {
			return fmt.Errorf("store: migration v13: read config: %w", err)
		}
		if key == "" {
			return fmt.Errorf("store: migration v13: config row for repo %s has an empty key, which config.set cannot represent", repo)
		}
		if _, err := appendMigrationEvent(ctx, tx, ids, ActorMigration, TypeConfigSet, repo, repo,
			ConfigSet{Key: key, Value: value}); err != nil {
			return fmt.Errorf("store: migration v13: mint config.set for %s/%s: %w", repo, key, err)
		}
	}
	return rows.Err()
}

// gateDeclarationRow is one existing gate_declarations row, carried from
// v13BackfillGateDeclarations to v13BackfillGateExemptions so the latter knows
// the scale each exemption's declaration binds to without re-reading a table
// its caller already read.
type gateDeclarationRow struct {
	repo  string
	gate  string
	scale Scale
}

// v13BackfillGateDeclarations mints one gate.declared per existing
// (repo,gate) row, with no exemption snapshot (see v13SyntheticHistory's
// doc). It returns what it read so the exemption pass below never has to ask
// the table a second time.
func v13BackfillGateDeclarations(ctx context.Context, tx *sql.Tx, ids *migrationIDs) ([]gateDeclarationRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT repo, gate, scale FROM gate_declarations ORDER BY repo, gate`)
	if err != nil {
		return nil, fmt.Errorf("store: migration v13: read gate declarations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var declarations []gateDeclarationRow
	for rows.Next() {
		var d gateDeclarationRow
		if err := rows.Scan(&d.repo, &d.gate, &d.scale); err != nil {
			return nil, fmt.Errorf("store: migration v13: read gate declarations: %w", err)
		}
		if d.gate == "" {
			return nil, fmt.Errorf("store: migration v13: gate declaration for repo %s has an empty gate name, which gate.declared cannot represent", d.repo)
		}
		switch d.scale {
		case ScaleMatter, ScaleStage, ScaleStep:
		default:
			return nil, fmt.Errorf("store: migration v13: gate declaration %s/%s has scale %q, which gate.declared cannot represent", d.repo, d.gate, d.scale)
		}
		if _, err := appendMigrationEvent(ctx, tx, ids, ActorMigration, TypeGateDeclared, d.repo, d.repo,
			GateDeclared{Gate: d.gate, Scale: d.scale}); err != nil {
			return nil, fmt.Errorf("store: migration v13: mint gate.declared for %s/%s: %w", d.repo, d.gate, err)
		}
		declarations = append(declarations, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: migration v13: read gate declarations: %w", err)
	}
	return declarations, nil
}

// v13BackfillGateExemptions mints one gate.exemption-repaired per existing
// gate_exemptions row. Every row is checked against requireExemptNode — the
// literal predicate declareGateProjection and repairGateExemptionProjection
// both fold against — before it is minted, so an audit failure here and a
// fold failure at Rebuild are the same failure, not two rules that can drift
// apart.
func v13BackfillGateExemptions(ctx context.Context, tx *sql.Tx, ids *migrationIDs, declarations []gateDeclarationRow) error {
	type key struct{ repo, gate string }
	scaleOf := make(map[key]Scale, len(declarations))
	for _, d := range declarations {
		scaleOf[key{d.repo, d.gate}] = d.scale
	}

	rows, err := tx.QueryContext(ctx, `SELECT repo, gate, node FROM gate_exemptions ORDER BY repo, gate, node`)
	if err != nil {
		return fmt.Errorf("store: migration v13: read gate exemptions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var repo, gate, node string
		if err := rows.Scan(&repo, &gate, &node); err != nil {
			return fmt.Errorf("store: migration v13: read gate exemptions: %w", err)
		}
		scale, ok := scaleOf[key{repo, gate}]
		if !ok {
			// gate_exemptions carries a FOREIGN KEY into gate_declarations(repo,gate)
			// (schema_v8.go), so this is unreachable outside a corrupted database.
			return fmt.Errorf("store: migration v13: exemption for %s on %s names no gate declaration", node, gate)
		}
		// Validate before minting: a row the fold would refuse must never reach
		// appendMigrationEvent, so the migration fails with this readable reason
		// instead of a harder-to-read one from the events table's own checks.
		probe := Event{Type: TypeGateExemptionRepaired, Repo: repo}
		if err := requireExemptNode(ctx, tx, probe, node, scale); err != nil {
			return fmt.Errorf("store: migration v13: exemption %s on %s for gate %s is not foldable, and the migration refuses to mint history the fold would reject: %w",
				node, repo, gate, err)
		}
		if _, err := appendMigrationEvent(ctx, tx, ids, ActorMigration, TypeGateExemptionRepaired, repo, node,
			GateExemptionRepaired{Gate: gate}); err != nil {
			return fmt.Errorf("store: migration v13: mint gate.exemption-repaired for %s on %s/%s: %w", node, repo, gate, err)
		}
	}
	return rows.Err()
}
