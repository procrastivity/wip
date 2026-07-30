package store

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// Tests for gate storage (step-07 of this Matter).
//
// MODEL §9 puts gate bindings and gate state at *any* scale — Matter, Stage and
// Step — so the store holds them generically, keyed by (node, gate, scale). That
// is a **storage capability** and not a declaration: this dogfood declares exactly
// one gate, `reviewed-local: matter` (HANDOFF §1.2), because declaring `verified`,
// `reviewed` or `ci-green` would make its Matters structurally unable to seal
// (MODEL §2.3). Being able to hold what one is careful not to declare is the
// whole of what this Step owes.
//
// The two halves are stored differently on purpose and the seam between them is
// most of what is asserted here: a *declaration* is configuration (D4, D54), has
// no event and survives a rebuild; a *close* is an event and its row is a
// projection of it. `archived_matters` is the one place the two meet.

// ---------------------------------------------------------------------------
// 1. The storage capability: gate state at every scale
// ---------------------------------------------------------------------------

// TestGateStateIsHeldAtEveryScale exercises the capability MODEL §9 requires at
// all three scales, with one gate name, so what is being shown is that the scale
// travels with the state rather than being implied by the table.
func TestGateStateIsHeldAtEveryScale(t *testing.T) {
	h := newHarness(t)

	held := make(map[Scale]string, 3)
	for _, scale := range []Scale{ScaleMatter, ScaleStage, ScaleStep} {
		node := h.nodeAt(scale, string(scale)+"-subject", "A "+string(scale)+" with a gate of its own")
		held[scale] = node

		closed := h.closeGate(node, "reviewed-local", scale)
		h.wantRow("a gate closed at "+string(scale)+" scale", "gate_state", "node = ?", []any{node},
			map[string]any{
				"node":       node,
				"gate":       "reviewed-local",
				"scale":      scale,
				"closed_at":  closed.OccurredAt.UTC().Format(timestampLayout),
				"last_event": closed.ID,
			})

		// And back out through the read surface, which is where every caller of
		// this storage will meet it.
		gates, err := h.ClosedGates(h.ctx, node)
		if err != nil {
			t.Fatalf("read the gate state of the %s: %v", scale, err)
		}
		if len(gates) != 1 {
			t.Fatalf("the %s has %d gates closed against it, want 1", scale, len(gates))
		}
		got := gates[0]
		if got.Node != node || got.Gate != "reviewed-local" || got.Scale != scale {
			t.Errorf("the %s reads back as %+v", scale, got)
		}
		if !got.ClosedAt.Equal(closed.OccurredAt.UTC()) {
			t.Errorf("the %s's gate closed at %s, want %s", scale, got.ClosedAt, closed.OccurredAt.UTC())
		}
	}

	// One gate name held at three scales at once, three rows, no collision: the
	// key is the node's identity and the gate's name, and the scale is carried
	// rather than inferred.
	h.wantRowCount("one gate name at three scales", "gate_state", "gate = 'reviewed-local'", nil, 3)
	for scale, node := range held {
		h.wantRowCount("the "+string(scale)+"'s own row", "gate_state",
			"node = ? AND scale = ?", []any{node, string(scale)}, 1)
	}
}

// ---------------------------------------------------------------------------
// 2. The payload round-trip
// ---------------------------------------------------------------------------

// TestGateClosedRoundTripsThroughItsPayload writes a close, reads it back off the
// log by identity, and holds the projection to what the payload says.
//
// `gate.closed` carries gate name + scale + subject (CONTRACT §B), and the
// subject is the *envelope's* — so the round-trip is also the assertion that the
// payload does not carry a second copy of it, which two writers could disagree
// about.
func TestGateClosedRoundTripsThroughItsPayload(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("round-trip", "A Matter whose gate close is read back")
	written := h.closeGate(matter, "reviewed-local", ScaleMatter)

	// Off the log, resolved by identity rather than from the value the command
	// returned.
	stored := h.lastEventOf(matter)
	if stored.ID != written.ID {
		t.Fatalf("the last event about the Matter is %s, want the gate close %s", stored.ID, written.ID)
	}
	if stored.Type != TypeGateClosed {
		t.Errorf("the gate close is a %s", stored.Type)
	}
	if stored.Subject != matter {
		t.Errorf("the gate close names %s as its subject, want the Matter %s", stored.Subject, matter)
	}

	var p GateClosed
	if err := json.Unmarshal(stored.Payload, &p); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	if p.Gate != "reviewed-local" || p.Scale != ScaleMatter {
		t.Errorf("the payload round-tripped as %+v", p)
	}

	// The payload's own shape: the gate and the scale, and nothing else. The
	// subject is the envelope's and is not repeated here.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stored.Payload, &fields); err != nil {
		t.Fatalf("decode the payload: %v", err)
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, ", ") != "gate, scale" {
		t.Errorf("gate.closed's payload is (%s), want (gate, scale)", strings.Join(names, ", "))
	}

	// And the projection agrees with the payload, column for column, including
	// the timestamp it derives from the event's own id.
	h.wantRow("the projection of the round-tripped close", "gate_state", "node = ?", []any{matter},
		map[string]any{
			"node":       stored.Subject,
			"gate":       p.Gate,
			"scale":      p.Scale,
			"closed_at":  stored.OccurredAt.UTC().Format(timestampLayout),
			"last_event": stored.ID,
		})
}

// ---------------------------------------------------------------------------
// 3. Exactly one declared gate
// ---------------------------------------------------------------------------

// TestTheDogfoodDeclaresExactlyOneGate pins the declaration set to what the code
// ships rather than to what a test declares for its own convenience.
//
// The store ships *none*: there is no gate in the v1 baseline and nothing in this
// package declares one, because a declaration is an operator's act against a Repo
// and not a fact about the schema. The dogfood's single `reviewed-local: matter`
// (HANDOFF §1.2) is therefore one row a caller writes, and this test is the only
// place in this Matter that writes it as *the* declaration.
func TestTheDogfoodDeclaresExactlyOneGate(t *testing.T) {
	h := newHarness(t)

	// A fresh store declares nothing at all. Sealing quantifies over declarations,
	// so a gate baked into the baseline would be a gate every dogfood Matter had
	// to close without ever asking for it.
	h.wantRowCount("a freshly opened store", "gate_declarations", "", nil, 0)

	// Nothing in the package's own code declares a gate either. Declaring is
	// configuration (D4, D54) and the caller's business; if this ever fails, the
	// store has started shipping a policy.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(source), ".DeclareGate(") {
			t.Errorf("%s calls DeclareGate; declaring a gate is the caller's act, not the store's", name)
		}
	}

	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the dogfood's one gate: %v", err)
	}
	h.wantRow("the dogfood's one gate", "gate_declarations", "repo = ?", []any{h.Repo},
		map[string]any{"repo": h.Repo, "gate": "reviewed-local", "scale": ScaleMatter})

	// Declaring it again is the same declaration and not a second one: the row is
	// keyed (repo, gate), so config converges rather than accumulating.
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("re-declare the dogfood's one gate: %v", err)
	}
	h.wantRowCount("after declaring it twice", "gate_declarations", "", nil, 1)

	declarations, err := h.GateDeclarations(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read the declarations: %v", err)
	}
	if len(declarations) != 1 || declarations[0] != (GateDeclaration{Repo: h.Repo, Gate: "reviewed-local", Scale: ScaleMatter}) {
		t.Errorf("this Repo declares %+v, want exactly reviewed-local: matter", declarations)
	}
}

// ---------------------------------------------------------------------------
// 4. The seam: a declaration and a close are separate facts
// ---------------------------------------------------------------------------

// TestDeclaringAGateAndClosingOneAreSeparateFacts walks the seam in both
// directions.
//
// Neither half requires the other: the store will hold a close for a gate nobody
// declared (it is storage, and `guards` owns the precondition), and a declaration
// takes effect against Matters that finished long before it existed. What binds
// them is `archived_matters`, and only there.
func TestDeclaringAGateAndClosingOneAreSeparateFacts(t *testing.T) {
	h := newHarness(t)

	matter := h.matter("seam", "A Matter at the seam")
	h.start(matter)
	h.finish(matter)
	h.wantArchive("Done, in a Repo that declares nothing", matter)

	// Closing a gate nobody declared is permitted storage. It is not the store's
	// business to know which gates a project has agreed to; what it must not do is
	// let an undeclared close change an answer.
	h.closeGate(matter, "never-declared", ScaleMatter)
	h.wantRowCount("an undeclared gate closed", "gate_state", "gate = 'never-declared'", nil, 1)
	h.wantArchive("Done, with a gate nobody declared closed against it", matter)

	// Declared afterwards, the close that was already there satisfies it: sealing
	// is a predicate over the pair and neither half has to come first (D55).
	if err := h.DeclareGate(h.ctx, h.Repo, "never-declared", ScaleMatter); err != nil {
		t.Fatalf("declare the gate that was closed first: %v", err)
	}
	h.wantArchive("declared after it was closed", matter)

	// A second declaration un-seals it, with no event and no write to the
	// projection: config is consulted by the predicate every time it is asked.
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the second gate: %v", err)
	}
	h.wantArchive("a second matter-scale gate declared and open")
	h.closeGate(matter, "reviewed-local", ScaleMatter)
	h.wantArchive("both declared gates closed", matter)

	// Re-declaring one of them at another scale un-seals it again — the predicate
	// asks for matter-scale gates, and a gate that no longer binds to Matters is
	// no longer one of them.
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleStep); err != nil {
		t.Fatalf("re-declare the gate at Step scale: %v", err)
	}
	h.wantArchive("one of the two gates re-bound to Steps", matter)
}

// TestAGateClosesAtTheScaleOfItsSubject holds the payload's scale to the node it
// is closed against.
//
// A gate binds to a scale (D12) and closes against a node at that scale, so a
// `gate.closed` whose scale is not its subject's kind describes a close that
// could not have happened. It matters twice over: `archived_matters` asks only
// whether a declared gate has a row, so a mis-scaled close would seal a Matter
// that had reviewed nothing — and since gate state is one row per (node, gate),
// the mis-scaled row would take the slot the real close needs and leave the
// Matter unable to seal ever again.
func TestAGateClosesAtTheScaleOfItsSubject(t *testing.T) {
	h := newHarness(t)

	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}
	matter := h.matter("mis-scaled", "A Matter whose gate was closed at the wrong scale")
	stage := h.stage(matter, "stage-01", "A Stage of it")
	step := h.step(stage, "step-01", "A Step of it")
	h.start(matter)
	h.finish(matter)
	h.wantArchive("Done with its matter-scale gate open")

	for _, wrong := range []struct {
		node  string
		kind  Scale
		scale Scale
	}{
		{matter, ScaleMatter, ScaleStage},
		{matter, ScaleMatter, ScaleStep},
		{stage, ScaleStage, ScaleMatter},
		{stage, ScaleStage, ScaleStep},
		{step, ScaleStep, ScaleMatter},
		{step, ScaleStep, ScaleStage},
	} {
		refusalMentions(t, "closing a gate at "+string(wrong.scale)+" scale against a "+string(wrong.kind),
			h.closeGateError(wrong.node, "reviewed-local", wrong.scale),
			"closes at the scale of its subject")
	}

	// Six refusals, nothing written: the projection rule turns the event down
	// rather than folding it and leaving the slot occupied.
	h.wantRowCount("after six mis-scaled closes", "gate_state", "", nil, 0)
	h.wantArchive("after six mis-scaled closes")

	// The slot is still free, so the close that was meant still seals it.
	h.closeGate(matter, "reviewed-local", ScaleMatter)
	h.wantArchive("closed at Matter scale", matter)
}

// ---------------------------------------------------------------------------
// 5. The cases nobody asked for
// ---------------------------------------------------------------------------

// TestOneReposDeclarationsSayNothingAboutAnothersMatters is the multi-Repo case.
//
// Declarations key at the Repo (D42, D54) and the sealing predicate joins on it,
// so two Repos may declare the same gate name at different scales and neither
// answer moves the other. The second half is a fact about what the store permits
// rather than about what it should: the *event* that closes a gate carries its own
// repo dimension (D56), and nothing checks it against the subject's Repo — so a
// command running in one Repo's tier context can close a gate on another Repo's
// Matter and seal it.
func TestOneReposDeclarationsSayNothingAboutAnothersMatters(t *testing.T) {
	h := newHarness(t)

	otherRepo := h.attachRepo(RepoAttached{Label: "other"})
	otherClone := h.attachClone(CloneAttached{Repo: otherRepo, GitCommonDir: "/tmp/other/.git", Label: "main"})
	otherWorktree := h.attachWorktree(otherRepo, WorktreeAttached{Clone: otherClone})
	other := h.with(Env{Repo: otherRepo, Clone: otherClone, Worktree: otherWorktree})

	// The same gate name, bound to different scales by two Repos.
	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare this Repo's gate: %v", err)
	}
	if err := h.DeclareGate(h.ctx, otherRepo, "reviewed-local", ScaleStep); err != nil {
		t.Fatalf("declare the other Repo's gate: %v", err)
	}

	mine := h.matter("mine", "A Matter of this Repo")
	h.start(mine)
	h.finish(mine)
	theirs := other.matter("theirs", "A Matter of the other Repo")
	other.start(theirs)
	other.finish(theirs)

	h.wantArchive("Done here, with this Repo's matter-scale gate open")
	other.wantArchive("Done there, where the same gate name binds to Steps", theirs)

	// Closing it in the other Repo's tier context seals a Matter of this one.
	// Recorded because it is what the store does, not because it is right: the
	// event's repo dimension is the other Repo's and the node's is not.
	other.closeGate(mine, "reviewed-local", ScaleMatter)
	h.wantArchive("sealed by a close that ran in another Repo's tier context", mine)

	closed := h.lastEventOf(mine)
	if closed.Repo != otherRepo {
		t.Errorf("the close carries repo %s, want the other Repo %s", closed.Repo, otherRepo)
	}
	h.wantRow("a gate closed across Repos", "gate_state", "node = ?", []any{mine}, map[string]any{
		"node":       mine,
		"gate":       "reviewed-local",
		"scale":      ScaleMatter,
		"closed_at":  closed.OccurredAt.UTC().Format(timestampLayout),
		"last_event": closed.ID,
	})
}

// TestAGateClosedAgainstARemovedNodeSealsNothing is the tombstone case.
//
// A close against a structurally removed node is permitted — the same known gap
// `content.created`, `cursor.moved` and `dependency.added` have, and for the same
// reason: D44 guarantees prior references stay valid, and a *new* reference to a
// corpse is `doctor`'s business rather than the projection's. What must hold is
// that it moves no answer, which it does not, because the Archive excludes
// tombstoned Matters before it ever looks at a gate.
func TestAGateClosedAgainstARemovedNodeSealsNothing(t *testing.T) {
	h := newHarness(t)

	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}
	matter := h.matter("folded", "A Matter folded into another before it was reviewed")
	h.start(matter)
	h.finish(matter)
	h.wantArchive("Done with its gate open")

	removed := h.remove(matter, "folded into another Matter")
	closed := h.closeGate(matter, "reviewed-local", ScaleMatter)
	h.wantRow("a gate closed against a removed Matter", "gate_state", "node = ?", []any{matter},
		map[string]any{
			"node":       matter,
			"gate":       "reviewed-local",
			"scale":      ScaleMatter,
			"closed_at":  closed.OccurredAt.UTC().Format(timestampLayout),
			"last_event": closed.ID,
		})
	h.wantArchive("a removed Matter whose gate was closed afterwards")

	// The history stays readable by identity, in order, tombstone and all.
	events := h.eventsOf(matter)
	var types []string
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	want := []string{TypeMatterCreated, TypeMatterStarted, TypeMatterFinished, TypeStepRemoved, TypeGateClosed}
	if strings.Join(types, " ") != strings.Join(want, " ") {
		t.Errorf("the removed Matter's history is [%s], want [%s]",
			strings.Join(types, " "), strings.Join(want, " "))
	}
	if events[len(events)-1].ID != closed.ID || events[len(events)-2].ID != removed.ID {
		t.Error("the gate close and the removal did not land in the order they were written")
	}
}

// TestGateStateIsAProjectionAndDeclarationsAreNot is the rebuild seam, from the
// gate side. `rebuild_test.go` owns the assertion that config survives a rebuild;
// what this adds is the other half of the pair — that gate *state* is rebuilt
// from the log while the declaration it answers is not, and that sealing, which
// reads both, is unchanged.
func TestGateStateIsAProjectionAndDeclarationsAreNot(t *testing.T) {
	h := newHarness(t)

	if err := h.DeclareGate(h.ctx, h.Repo, "reviewed-local", ScaleMatter); err != nil {
		t.Fatalf("declare the gate: %v", err)
	}
	matter := h.matter("rebuilt", "A Matter sealed before a rebuild")
	step := h.step(matter, "step-01", "A Step with a gate of its own")
	h.start(matter)
	h.finish(matter)
	h.closeGate(matter, "reviewed-local", ScaleMatter)
	h.closeGate(step, "verified", ScaleStep)
	h.wantArchive("before the rebuild", matter)

	before := h.rowsOf("gate_state", "")
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	after := h.rowsOf("gate_state", "")
	if len(after) != 2 || len(before) != 2 {
		t.Fatalf("gate_state holds %d rows before the rebuild and %d after, want 2 and 2", len(before), len(after))
	}
	for i := range before {
		for column, value := range before[i] {
			if after[i][column] != value {
				t.Errorf("after the rebuild gate_state.%s is %s, want %s", column, after[i][column], value)
			}
		}
	}
	h.wantArchive("after the rebuild", matter)

	// The declaration is not in the log and was not folded back — a rebuild that
	// cleared it would have un-sealed the Matter above without a single event
	// saying so.
	h.wantRow("the declaration after a rebuild", "gate_declarations", "repo = ?", []any{h.Repo},
		map[string]any{"repo": h.Repo, "gate": "reviewed-local", "scale": ScaleMatter})
	for _, ev := range h.eventsOf(h.Repo) {
		if strings.Contains(ev.Type, "gate") {
			t.Errorf("declaring a gate emitted %s; a declaration is config, not an event", ev.Type)
		}
	}
}

// A gate close whose subject is not a node at all is refused before it reaches
// the foreign key, because the projection rule has to read the subject's kind to
// check the scale — so the refusal names the missing node rather than a
// constraint.
func TestAGateClosesAgainstANodeAndNothingElse(t *testing.T) {
	h := newHarness(t)

	refusalMentions(t, "a gate closed against a Repo",
		h.closeGateError(h.Repo, "reviewed-local", ScaleMatter), "names no node")
	h.wantRowCount("after closing a gate against a Repo", "gate_state", "", nil, 0)

	// The check is on the payload, not on the taxonomy: an unregistered event type
	// never reaches a projection rule at all.
	matter := h.matter("only-gate", "A Matter with a gate to not open")
	err := h.commitError(func(_ context.Context, _ *Tx) ([]Draft, error) {
		return []Draft{{Type: "gate.opened", Subject: matter, Payload: GateClosed{Gate: "g", Scale: ScaleMatter}}}, nil
	})
	refusalMentions(t, "a gate.opened", err, "is not a registered event type")
}
