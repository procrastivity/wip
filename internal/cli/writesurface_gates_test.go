// End-to-end tests for the write-surface Matter's gates-and-dependencies
// Stage (workplans/write-surface.md, step-05), through the actual built
// binary.
package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestGateDeclare_WritesConfigEmitsNoEvent(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	dbPathStr := dbEnvPath(dbEnv)

	s := openTestStore(t, dbPathStr)
	before, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter", "--json")
	if r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s = openTestStore(t, dbPathStr)
	after, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("gate declare appended %d events, want 0 (config, not a taxonomy event)", len(after)-len(before))
	}

	// The verb is general — it can express a forge gate too (the schema
	// Brief: "the schema must be *able* to hold gate state at any scale").
	// What actually bars declaring verified/reviewed/ci-green in this
	// dogfood is HANDOFF §1.2's operating discipline plus the structural
	// fact that they can never close without their owning role (MODEL
	// §2.3) — not a hardcoded refusal here. Declaring one is technically
	// legal and still emits no event.
	forge := runIn(t, dir, dbEnv, "gate", "declare", "verified", "--scale", "step", "--json")
	if forge.exitCode != 0 {
		t.Fatalf("declaring a forge gate should be technically legal (the verb is general): exit=%d stderr=%q", forge.exitCode, forge.stderr)
	}
	s = openTestStore(t, dbPathStr)
	afterForge, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterForge) != len(before) {
		t.Errorf("declaring a forge gate appended %d events, want 0", len(afterForge)-len(before))
	}
}

func TestGateClose_OneEventRightSubjectAndScale(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Gate me", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	// Closing an undeclared gate is refused, no event appended.
	badClose := runIn(t, dir, dbEnv, "gate", "close", "no-such-gate", m.ID)
	if badClose.exitCode == 0 {
		t.Fatalf("closing an undeclared gate should be refused")
	}
	s := openTestStore(t, dbPathStr)
	stillOne, err := s.EventsOfSubject(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stillOne) != 1 {
		t.Errorf("a refused gate close appended events; %s carries %d, want 1", m.ID, len(stillOne))
	}

	r := runIn(t, dir, dbEnv, "gate", "close", "reviewed-local", m.ID, "--json")
	if r.exitCode != 0 {
		t.Fatalf("gate close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPathStr)
	ev := wantAppendedEvent(t, s, m.ID, 1, store.TypeGateClosed) // m already carried matter.created
	if ev.Subject != m.ID {
		t.Errorf("gate.closed subject = %s, want %s", ev.Subject, m.ID)
	}
	var p store.GateClosed
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Gate != "reviewed-local" || p.Scale != store.ScaleMatter {
		t.Errorf("gate.closed payload = %+v, want gate=reviewed-local scale=matter", p)
	}

	closed, err := s.ClosedGates(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || closed[0].Gate != "reviewed-local" {
		t.Errorf("closed gates on %s = %+v, want exactly reviewed-local", m.ID, closed)
	}
}

func TestDepend_AddRemoveAndCycleRefusal(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Downstream", "--json").stdout)
	m2 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Upstream", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	// m1 blocked-by m2.
	addR := runIn(t, dir, dbEnv, "depend", "add", m1.ID, "--blocked-by", m2.ID, "--json")
	if addR.exitCode != 0 {
		t.Fatalf("depend add: exit=%d stderr=%q", addR.exitCode, addR.stderr)
	}
	s := openTestStore(t, dbPathStr)
	ev := wantAppendedEvent(t, s, m1.ID, 1, store.TypeDependencyAdded) // m1 already carried matter.created
	if ev.Subject != m1.ID {
		t.Errorf("dependency.added subject = %s, want %s (the blocked node)", ev.Subject, m1.ID)
	}
	blockers, err := s.BlockedBy(context.Background(), m1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 1 || blockers[0].Blocker != m2.ID {
		t.Fatalf("blockers of m1 = %+v, want exactly m2", blockers)
	}

	// The reverse edge would close a cycle: refused, no event, no edge.
	cycleR := runIn(t, dir, dbEnv, "depend", "add", m2.ID, "--blocked-by", m1.ID, "--json")
	if cycleR.exitCode != 3 {
		t.Fatalf("cycle-creating depend add: exit=%d, want 3 (refusal); stderr=%q", cycleR.exitCode, cycleR.stderr)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(cycleR.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, cycleR.stderr)
	}
	if envelope.Error.Code != "refusal.blocked-by-cycle" {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, "refusal.blocked-by-cycle")
	}
	s = openTestStore(t, dbPathStr)
	m2Events, err := s.EventsOfSubject(context.Background(), m2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2Events) != 1 {
		t.Errorf("m2 carries %d events after a refused cycle-creating add, want 1 (matter.created only)", len(m2Events))
	}

	// Remove: tombstoned, not deleted; a second remove finds no live edge.
	remR := runIn(t, dir, dbEnv, "depend", "remove", m1.ID, "--blocked-by", m2.ID, "--json")
	if remR.exitCode != 0 {
		t.Fatalf("depend remove: exit=%d stderr=%q", remR.exitCode, remR.stderr)
	}
	s = openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m1.ID, 2, store.TypeDependencyRemoved) // matter.created + dependency.added, then this
	blockers, err = s.BlockedBy(context.Background(), m1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 0 {
		t.Errorf("blockers of m1 after remove = %+v, want none (edges_in_force excludes tombstoned edges)", blockers)
	}

	remAgain := runIn(t, dir, dbEnv, "depend", "remove", m1.ID, "--blocked-by", m2.ID)
	if remAgain.exitCode == 0 {
		t.Fatalf("removing an already-tombstoned edge should be refused, not silently succeed")
	}
}

func TestBind_ShipsInertOneEvent(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Bind me", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	r := runIn(t, dir, dbEnv, "bind", m.ID, "https://tracker.example.com/issue/42", "--json")
	if r.exitCode != 0 {
		t.Fatalf("bind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s := openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 1, store.TypeReferenceBound) // m already carried matter.created

	n, err := s.Node(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.ExternalRef != "https://tracker.example.com/issue/42" {
		t.Errorf("external ref = %q, want the bound URL", n.ExternalRef)
	}
}
