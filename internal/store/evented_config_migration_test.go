package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The synthetic-history migration (step-03, evented-config): a store that
// carried config, gate declarations and gate exemptions as bare rows before
// schema v13 gains the events that let those rows fold from the log like
// everything else, and the fold reproduces the rows exactly. `migrations_test.go`
// owns the framework's own claims (backup, all-or-nothing, ledger); what this
// file owns is what v13's dataMigrate (schema_v13.go) actually mints and what
// it refuses to mint.

// preV13Register is the shipped register up to and including v12 — the schema
// version at which config, gate declarations and gate exemptions are still the
// bare rows step-02 replaced with events. It exists so a test can populate a
// store the way that version could write it, exactly as richHistory does
// inline (rebuild_test.go's `h.SchemaVersion() >= 13` split).
func preV13Register() []migration { return append([]migration{}, register[:12]...) }

// TestEventedConfigMigrationMintsSyntheticHistoryReproducingEveryRow is the
// Step's done-criterion: a migration reads the existing rows and appends
// synthetic events under `system:migration`, and folding them reproduces the
// pre-migration state exactly.
func TestEventedConfigMigrationMintsSyntheticHistoryReproducingEveryRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, preV13Register(), 12)
	if got := h.SchemaVersion(); got != 12 {
		t.Fatalf("the fixture store is at v%d, want v12 (pre-evented config)", got)
	}

	// Config: two keys on the harness's own Repo, including "set to empty",
	// and one key on a second Repo — proving repo-scoping survives the
	// migration and not just the single-Repo case. Three rows total.
	if err := h.SetConfig(h.ctx, h.Repo, "strategy", "trunk"); err != nil {
		t.Fatalf("legacy SetConfig: %v", err)
	}
	if err := h.SetConfig(h.ctx, h.Repo, "empty-on-purpose", ""); err != nil {
		t.Fatalf("legacy SetConfig: %v", err)
	}
	other := h.attachRepo(RepoAttached{Label: "other"})
	if err := h.with(Env{Repo: other, Clone: h.Clone, Worktree: h.Worktree}).
		SetConfig(h.ctx, other, "strategy", "release-branch"); err != nil {
		t.Fatalf("legacy SetConfig on a second Repo: %v", err)
	}

	// A Matter-scale gate declared after a Matter already sealed: the v8-v12
	// direct-write branch snapshots it as an exemption automatically, the same
	// way DeclareGate always has.
	sealed := h.matter("sealed", "Already Done when the gate arrived")
	h.start(sealed)
	h.finish(sealed)
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("legacy DeclareGate: %v", err)
	}

	// A Step-scale gate declared with nothing yet to exempt automatically, plus
	// one exemption added afterward through the legacy repair path — the
	// pre-v13 case gate.exemption-repaired exists to represent.
	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		t.Fatalf("legacy DeclareGate: %v", err)
	}
	stepMatter := h.matter("stepped", "Holds the exempted Step")
	step := h.step(stepMatter, "step-01", "A Step repaired into the exemption")
	h.start(step)
	h.finish(step)
	if err := h.RepairGateExemption(h.ctx, h.Repo, "verified", step); err != nil {
		t.Fatalf("legacy RepairGateExemption: %v", err)
	}

	before := h.snapshotProjection()
	h.wantRowCount("before the migration", "config", "", nil, 3)
	h.wantRowCount("before the migration", "gate_declarations", "", nil, 2)
	h.wantRowCount("before the migration", "gate_exemptions", "", nil, 2)
	log := h.rowsOf("events", "")
	dir2 := filepath.Dir(h.Path())

	migrated, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("migrate v12 -> v13: %v", err)
	}
	if got := migrated.SchemaVersion(); got != latestVersion(shipped()) {
		t.Fatalf("schema version = %d, want %d", got, latestVersion(shipped()))
	}

	// The backup-before-migrate posture from `schema` is honored: one sidecar,
	// taken before the synthetic events ever existed.
	if got := backupsIn(t, dir2); len(got) != 1 {
		t.Fatalf("migrating to v13 left %v, want exactly one sidecar", got)
	}

	// Every original event is still there, in order; the migration only ever
	// appends.
	wantLogRetainsEveryRow(t, "the log through the evented-config migration", log, migrated.rowsOf("events", ""))

	// The oracle: folding the synthetic events reproduces the pre-migration
	// rows exactly, table by table (config, gate_declarations, gate_exemptions
	// included, since snapshotProjection reads every table in projectionTables
	// and all three have belonged to it since step-01).
	wantSameProjectionThroughMigration(t, "after migrating v12 -> v13", before, migrated.snapshotProjection())

	// The synthetic events themselves: one config.set per config row, one
	// gate.declared per declaration (with no exemption snapshot — the
	// resolved snapshot-vs-repair call), one gate.exemption-repaired per
	// exemption, every one of them stamped the honest migration actor and its
	// own origin (no causation chain: nothing in the log entailed a migration).
	all, err := migrated.Events(migrated.ctx)
	if err != nil {
		t.Fatalf("read the migrated log: %v", err)
	}
	byType := map[string][]Event{}
	for _, ev := range all {
		if ev.Actor == ActorMigration {
			byType[ev.Type] = append(byType[ev.Type], ev)
		}
	}
	wantMigratedEventCount := func(eventType string, want int) {
		t.Helper()
		got := byType[eventType]
		if len(got) != want {
			t.Errorf("%s: %d migration-minted events, want %d", eventType, len(got), want)
		}
		for _, ev := range got {
			if !ev.IsOrigin() {
				t.Errorf("%s: event %s is not its own origin (causation=%s correlation=%s)",
					eventType, ev.ID, ev.Causation, ev.Correlation)
			}
		}
	}
	wantMigratedEventCount(TypeConfigSet, 3)
	wantMigratedEventCount(TypeGateDeclared, 2)
	wantMigratedEventCount(TypeGateExemptionRepaired, 2)

	for _, ev := range byType[TypeGateDeclared] {
		var p GateDeclared
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal gate.declared payload: %v", err)
		}
		if len(p.Exempt) != 0 {
			t.Errorf("a synthetic gate.declared for %s carries an exemption snapshot %v, want none (every exemption rides as its own repair)", p.Gate, p.Exempt)
		}
	}

	// The read surface agrees with the raw rows: the migration changed
	// nothing observable about config or gate state.
	if value, present, err := migrated.Config(migrated.ctx, migrated.Repo, "strategy"); err != nil || !present || value != "trunk" {
		t.Errorf("migrated config strategy = %q (present=%v, err=%v), want trunk", value, present, err)
	}
	if value, present, err := migrated.Config(migrated.ctx, migrated.Repo, "empty-on-purpose"); err != nil || !present || value != "" {
		t.Errorf(`migrated config empty-on-purpose = %q (present=%v, err=%v), want "" and present`, value, present, err)
	}
	if exempt, err := migrated.GateExempt(migrated.ctx, migrated.Repo, sealed, "reviewed-local"); err != nil || !exempt {
		t.Errorf("the already-sealed Matter is exempt=%v (err=%v), want true", exempt, err)
	}
	if satisfied, err := migrated.GateSatisfied(migrated.ctx, migrated.Repo, step, "verified"); err != nil || !satisfied {
		t.Errorf("the repaired Step is satisfied=%v (err=%v), want true", satisfied, err)
	}

	// An explicit rebuild is idempotent over the synthetic events too — folding
	// them twice reproduces the same state as folding them once.
	if err := migrated.Rebuild(migrated.ctx); err != nil {
		t.Fatalf("explicit rebuild after the migration: %v", err)
	}
	wantSameProjectionThroughMigration(t, "after an explicit rebuild", before, migrated.snapshotProjection())
	wantLogRetainsEveryRow(t, "the log after an explicit rebuild", log, migrated.rowsOf("events", ""))

	// And the state survives a further close and reopen at the same version.
	reopened, err := migrated.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("reopen at v13: %v", err)
	}
	wantSameProjectionThroughMigration(t, "after reopening at v13", before, reopened.snapshotProjection())
}

// TestEventedConfigMigrationRefusesAWrongScaleExemption covers the audit
// hand-off note: the pre-v13 RepairGateExemption never checked that the node
// it exempted matched its gate's declared scale, so a store can carry an
// exemption row the fold would refuse. The migration must refuse rather than
// mint history the fold cannot accept — the schema_v2.go
// rejectLegacyRuns/rejectLegacyBatches precedent — and must leave the
// pre-migration store untouched.
func TestEventedConfigMigrationRefusesAWrongScaleExemption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wip.db")

	h := newHarnessAt(t, path, preV13Register(), 12)
	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		t.Fatalf("legacy DeclareGate: %v", err)
	}
	matter := h.matter("wrong-scale", "A Matter, not a Step")
	h.start(matter)
	h.finish(matter)
	// The legacy write path performs no scale check at all (config.go's
	// RepairGateExemption, s.version < 13 branch): this is how such a row
	// could exist on disk in the first place.
	if err := h.RepairGateExemption(h.ctx, h.Repo, "verified", matter); err != nil {
		t.Fatalf("legacy RepairGateExemption around the scale check: %v", err)
	}

	beforeSchema := schemaShape(t, h.Store)
	beforeConfig := h.rowsOf("config", "")
	beforeDeclarations := h.rowsOf("gate_declarations", "")
	beforeExemptions := h.rowsOf("gate_exemptions", "")
	log := h.rowsOf("events", "")

	_, err := h.reopen(shipped(), latestVersion(shipped()))
	refusalMentions(t, "migrating a wrong-scale exemption", err, "is not foldable")

	back, err := h.reopen(preV13Register(), 12)
	if err != nil {
		t.Fatalf("reopen at v12 after the refused migration: %v", err)
	}
	if got := back.SchemaVersion(); got != 12 {
		t.Errorf("schema version = %d after a refused migration, want 12", got)
	}
	wantSameSchema(t, "schema after the refused migration", schemaShape(t, back.Store), beforeSchema)
	wantSameRows(t, "config after the refused migration", beforeConfig, back.rowsOf("config", ""))
	wantSameRows(t, "gate_declarations after the refused migration", beforeDeclarations, back.rowsOf("gate_declarations", ""))
	wantSameRows(t, "gate_exemptions after the refused migration", beforeExemptions, back.rowsOf("gate_exemptions", ""))
	wantSameRows(t, "the log after the refused migration", log, back.rowsOf("events", ""))

	// A backup was still taken — the framework backs up before it knows a
	// migration will fail, and that copy is what makes the refusal costless.
	if got := backupsIn(t, dir); len(got) != 1 {
		t.Errorf("a refused v13 migration left %v, want exactly one sidecar", got)
	}
}

// TestEventedConfigMigrationRefusesATombstonedExemption covers the other
// unfoldable shape hand-off note 2 names: a node exempted while live and later
// removed. gate_exemptions rows are never pruned when their node is
// tombstoned, so this is also a real pre-v13 shape and not a contrived one.
func TestEventedConfigMigrationRefusesATombstonedExemption(t *testing.T) {
	h := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), preV13Register(), 12)
	if err := h.DeclareGate(h.ctx, h.Repo, "verified", ScaleStep); err != nil {
		t.Fatalf("legacy DeclareGate: %v", err)
	}
	matter := h.matter("dead-node", "Holds the Step that gets removed")
	step := h.step(matter, "step-01", "Exempted, then removed")
	h.start(step)
	h.finish(step)
	if err := h.RepairGateExemption(h.ctx, h.Repo, "verified", step); err != nil {
		t.Fatalf("legacy RepairGateExemption: %v", err)
	}
	h.commit(Draft{Type: TypeStepRemoved, Subject: step, Payload: Removed{Reason: "superseded before the migration ran"}})

	_, err := h.reopen(shipped(), latestVersion(shipped()))
	refusalMentions(t, "migrating an exemption for a removed node", err, "is not foldable")
}
