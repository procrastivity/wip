package store

import (
	"context"
	"encoding/json"
	"testing"
)

// The three tables that stopped being the exception at v13: config, gate
// declarations and gate exemptions.
//
// `rebuild_test.go` owns the end-to-end claim — a rebuild reconstructs all three
// from the log. What this file owns is the seam underneath it: the three new
// event types, what their payloads carry, what their fold rules refuse, and the
// delete guard the tables gained on the way in.
//
// The events are driven straight through Commit. The public write paths do not
// emit them yet — that is the Step after this one — and the fold is testable
// before they do, which is the whole point of the projection having no argument
// but an event.

func TestConfigSetFoldsAndTheLogKeepsEveryValue(t *testing.T) {
	h := newHarness(t)

	first := h.setConfig(TrackerPushLevelKey, string(TrackerPushBoundary))
	h.setConfig(TrackerPushLevelKey, string(TrackerPushNarrated))
	h.setConfig("empty-on-purpose", "")

	// The table is last-write-wins, keyed at Repo so clones cannot diverge (D42).
	h.wantRow("the folded config key", "config", "repo = ? AND key = ?", []any{h.Repo, TrackerPushLevelKey},
		map[string]any{"repo": h.Repo, "key": TrackerPushLevelKey, "value": string(TrackerPushNarrated)})
	level, err := h.EffectiveTrackerPushLevel(h.ctx, h.Repo)
	if err != nil || level != TrackerPushNarrated {
		t.Errorf("effective push level = %q (err=%v), want narrated", level, err)
	}

	// "Set to empty" stays distinguishable from "never set" through the fold.
	if value, present, err := h.Config(h.ctx, h.Repo, "empty-on-purpose"); err != nil || !present || value != "" {
		t.Errorf(`config empty-on-purpose = %q (present=%v, err=%v), want "" and present`, value, present, err)
	}
	if _, present, err := h.Config(h.ctx, h.Repo, "never-set"); err != nil || present {
		t.Errorf("config never-set is present=%v (err=%v), want absent", present, err)
	}

	// And the value the table no longer holds is still in the log, which is the
	// history config had none of before.
	var values []string
	for _, ev := range h.eventsOf(h.Repo) {
		if ev.Type != TypeConfigSet {
			continue
		}
		var p ConfigSet
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Key == TrackerPushLevelKey {
			values = append(values, p.Value)
		}
	}
	if len(values) != 2 || values[0] != string(TrackerPushBoundary) || values[1] != string(TrackerPushNarrated) {
		t.Errorf("the log narrates %v for %s, want boundary then narrated", values, TrackerPushLevelKey)
	}
	if first.Actor != ActorHuman || first.Repo != h.Repo || first.Clone != "" || first.Worktree != "" {
		t.Errorf("config.set envelope = %+v, want a durable event carrying repo alone (D56)", first)
	}
}

func TestConfigSetRefusesAKeylessOrMisdirectedWrite(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("elsewhere", "Not a Repo")

	refusalMentions(t, "config.set with no key", h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeConfigSet, Subject: h.Repo, Payload: ConfigSet{Value: "trunk"}}}, nil
	}), "must name a config key")

	refusalMentions(t, "config.set keyed at something other than its Repo",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeConfigSet, Subject: matter, Payload: ConfigSet{Key: "strategy", Value: "trunk"}}}, nil
		}), "config keys at its Repo")
}

// TestGateDeclaredCarriesItsExemptionSnapshotExplicitly is the resolved open
// call, asserted where it is decided: the payload.
//
// Both options were deterministic — recompute the sealed-node set at fold time,
// or carry it — so the choice is about what the log narrates. Carrying it means a
// reader sees exactly which nodes the declaration stepped over without replaying
// fold logic, and it means a snapshot cannot silently change meaning when the
// completion predicate does.
func TestGateDeclaredCarriesItsExemptionSnapshotExplicitly(t *testing.T) {
	h := newHarness(t)

	// A Matter that sealed under the empty declaration set: the declaration below
	// is prospective, so this one is exempt rather than retroactively un-sealed.
	already := h.matter("already-sealed", "Sealed before anybody declared a gate")
	h.start(already)
	h.finish(already)
	later := h.matter("later", "Still in flight when the gate arrived")
	h.start(later)

	declared := h.declareGate("reviewed-local", ScaleMatter)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(declared.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["gate"] == nil || fields["scale"] == nil || fields["exempt"] == nil {
		t.Fatalf("gate.declared payload fields = %v, want exactly gate, scale and the exemption snapshot", fields)
	}
	var p GateDeclared
	if err := json.Unmarshal(declared.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Exempt) != 1 || p.Exempt[0] != already {
		t.Errorf("the snapshot names %v, want only the Matter that had already sealed", h.locators(p.Exempt))
	}

	h.wantRow("the folded declaration", "gate_declarations", "repo = ?", []any{h.Repo},
		map[string]any{"repo": h.Repo, "gate": "reviewed-local", "scale": ScaleMatter})
	h.wantRow("the folded exemption", "gate_exemptions", "repo = ?", []any{h.Repo},
		map[string]any{"repo": h.Repo, "gate": "reviewed-local", "node": already})

	if exempt, err := h.GateExempt(h.ctx, h.Repo, already, "reviewed-local"); err != nil || !exempt {
		t.Errorf("the already-sealed Matter is exempt=%v (err=%v), want true", exempt, err)
	}
	if exempt, err := h.GateExempt(h.ctx, h.Repo, later, "reviewed-local"); err != nil || exempt {
		t.Errorf("the in-flight Matter is exempt=%v (err=%v), want false", exempt, err)
	}

	// A declaration with nothing to step over carries no snapshot at all, and
	// `omitempty` keeps that payload the shape an empty declaration should have.
	empty := h.declareGate("approved", ScaleStep)
	emptyFields := map[string]json.RawMessage{}
	if err := json.Unmarshal(empty.Payload, &emptyFields); err != nil {
		t.Fatal(err)
	}
	if _, present := emptyFields["exempt"]; present {
		t.Errorf("a declaration with nothing exempt carries %v, want no exempt field", emptyFields)
	}
}

func TestGateDeclaredRefusesASecondDeclarationOrAMisshapenSnapshot(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("bound", "A Matter")
	h.start(matter)
	h.finish(matter)
	step := h.step(matter, "step-01", "A Step")
	h.declareGate("reviewed-local", ScaleMatter)

	// The first declaration fixes the prospective applicability boundary (D12): a
	// second event for the same gate could move or widen it, so the fold refuses
	// it rather than letting the projection decide which one won.
	refusalMentions(t, "re-declaring a gate at another scale",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "reviewed-local", Scale: ScaleStep},
			}}, nil
		}), "already binds to matter scale")
	refusalMentions(t, "re-declaring a gate at the same scale",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "reviewed-local", Scale: ScaleMatter},
			}}, nil
		}), "already binds to matter scale")
	h.wantRowCount("after two refused re-declarations", "gate_declarations", "", nil, 1)

	refusalMentions(t, "a declaration that names no gate",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeGateDeclared, Subject: h.Repo, Payload: GateDeclared{Scale: ScaleMatter}}}, nil
		}), "must name a gate")
	refusalMentions(t, "a declaration bound to something that is not a scale",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "invented", Scale: Scale("repo")},
			}}, nil
		}), "not a scale a gate can bind to")
	refusalMentions(t, "a declaration keyed at something other than its Repo",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: matter,
				Payload: GateDeclared{Gate: "invented", Scale: ScaleMatter},
			}}, nil
		}), "keys at its Repo")

	// A gate is satisfied at the scale of its subject (D12), so an exemption
	// against another scale would satisfy a gate for a node the declaration never
	// covered. The snapshot is taken from the payload and never recomputed, which
	// is exactly why its shape is checked here.
	refusalMentions(t, "a snapshot naming a node at the wrong scale",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "verified", Scale: ScaleMatter, Exempt: []string{step}},
			}}, nil
		}), "from a matter-scale gate, and it is a step")
	refusalMentions(t, "a snapshot naming something that is not a node",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "verified", Scale: ScaleMatter, Exempt: []string{h.NewID()}},
			}}, nil
		}), "which is not a node")
	h.wantRowCount("after every refused declaration", "gate_declarations", "", nil, 1)
}

func TestGateDeclaredIsScopedToItsOwnRepo(t *testing.T) {
	h := newHarness(t)
	other := h.attachRepo(RepoAttached{Label: "other"})
	elsewhere := h.with(Env{Repo: other, Clone: h.Clone, Worktree: h.Worktree}).
		matter("elsewhere", "A Matter in the other Repo")
	h.start(elsewhere) // the harness Env only names the Repo the event carries

	refusalMentions(t, "exempting another Repo's node",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateDeclared, Subject: h.Repo,
				Payload: GateDeclared{Gate: "reviewed-local", Scale: ScaleMatter, Exempt: []string{elsewhere}},
			}}, nil
		}), "which belongs to Repo")
	h.wantRowCount("after the refused declaration", "gate_declarations", "", nil, 0)
}

func TestGateExemptionRepairedFoldsIdempotentlyAgainstItsDeclaration(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("legacy", "Sealed before v8 could snapshot it")
	h.start(matter)
	h.finish(matter)
	h.declareGate("verified", ScaleMatter)

	repaired := h.repairExemption(matter, "verified")
	if repaired.Subject != matter || repaired.Repo != h.Repo {
		t.Errorf("gate.exemption-repaired envelope = %+v, want the node as subject under its Repo", repaired)
	}
	h.wantRow("the repaired exemption", "gate_exemptions", "repo = ?", []any{h.Repo},
		map[string]any{"repo": h.Repo, "gate": "verified", "node": matter})

	// A repeat repair is a legitimate no-op — the store method it replaces is
	// idempotent, and the write surface owns the incident preconditions — so the
	// fold lands it once and does not treat the second as a divergence.
	h.repairExemption(matter, "verified")
	h.wantRowCount("after a repeated repair", "gate_exemptions", "", nil, 1)

	if satisfied, err := h.GateSatisfied(h.ctx, h.Repo, matter, "verified"); err != nil || !satisfied {
		t.Errorf("the repaired gate is satisfied=%v (err=%v), want true", satisfied, err)
	}

	refusalMentions(t, "repairing an exemption for a gate nobody declared",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{
				Type: TypeGateExemptionRepaired, Subject: matter,
				Payload: GateExemptionRepaired{Gate: "undeclared"},
			}}, nil
		}), "which is not declared for Repo")
	refusalMentions(t, "a repair that names no gate",
		h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeGateExemptionRepaired, Subject: matter, Payload: GateExemptionRepaired{}}}, nil
		}), "must name a gate")
}

// TestTheConfigTablesCarryTheProjectionDeleteGuard is the substrate half of
// "these three are projections now".
//
// They carry the delete guard and not the birth/advance pair, for the reason
// `run_matters` does not either: a row here is a natural-keyed lookup with no
// identity and no history of its own, and the history it used to lack is in the
// log rather than in a column. What the guard buys is that no ordinary code path
// can clear one of these tables by mistake — only a rebuild may, and a rebuild
// can put it back.
func TestTheConfigTablesCarryTheProjectionDeleteGuard(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("guarded", "A Matter")
	h.start(matter)
	h.finish(matter)
	h.setConfig("strategy", "trunk")
	h.declareGate("reviewed-local", ScaleMatter)

	for _, table := range []string{"config", "gate_declarations", "gate_exemptions"} {
		rawRefusedBy(h, "deleting every row of "+table, "only a rebuild may clear it",
			`DELETE FROM "`+table+`"`)
	}
	h.wantRowCount("after the refused deletes", "config", "", nil, 1)
	h.wantRowCount("after the refused deletes", "gate_declarations", "", nil, 1)
	h.wantRowCount("after the refused deletes", "gate_exemptions", "", nil, 1)
}
