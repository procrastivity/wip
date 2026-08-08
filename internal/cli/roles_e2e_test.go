// End-to-end tests for the roles Matter's step-02: `wip role spawn/close/list`
// through the built binary, role instances bound to this worktree's open
// dispatch (D59), and the `--as-role` / WIP_AS_ROLE claim verified against an
// open spawned role on the write path.
package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type roleP struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Dispatch    string `json:"dispatch"`
	Open        bool   `json:"open"`
	CloseReason string `json:"close_reason"`
}

type roleListP struct {
	Dispatch string  `json:"dispatch"`
	Roles    []roleP `json:"roles"`
}

// refreshRepo opens the worktree's dispatch bracket the role verbs bind to.
func refreshRepo(t *testing.T, dir string, dbEnv []string) {
	t.Helper()
	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
}

func TestRole_SpawnCloseList_BoundToDispatch(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	// No dispatch yet: spawning has nothing to bind to.
	r := runIn(t, dir, dbEnv, "role", "spawn", "builder")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "no open dispatch") {
		t.Fatalf("spawn before refresh: exit=%d stderr=%q, want a no-open-dispatch refusal", r.exitCode, r.stderr)
	}

	refreshRepo(t, dir, dbEnv)

	r = runIn(t, dir, dbEnv, "role", "spawn", "builder", "--json")
	if r.exitCode != 0 {
		t.Fatalf("role spawn: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	spawned := mustJSON[roleP](t, r.stdout)
	if spawned.Name != "builder" || !spawned.Open || spawned.Dispatch == "" {
		t.Fatalf("spawned = %+v", spawned)
	}

	s := openTestStore(t, strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH="))
	ev := wantOneEvent(t, s, spawned.ID, store.TypeRoleSpawned)
	if ev.Actor != store.ActorHuman {
		t.Errorf("spawn actor = %q, want human (the spawner speaks, not the role)", ev.Actor)
	}

	// A second builder in the same bracket is contention.
	r = runIn(t, dir, dbEnv, "role", "spawn", "builder")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "already has an open builder") {
		t.Fatalf("duplicate spawn: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	// An unknown role name is refused before anything opens.
	r = runIn(t, dir, dbEnv, "role", "spawn", "reviewer")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "not one of the six roles") {
		t.Fatalf("unknown role: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r = runIn(t, dir, dbEnv, "role", "close", "builder", "--json")
	if r.exitCode != 0 {
		t.Fatalf("role close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	closed := mustJSON[roleP](t, r.stdout)
	if closed.Open || closed.CloseReason != "completed" {
		t.Fatalf("closed = %+v", closed)
	}
	// The close speaks as the role itself (MODEL §10).
	closeEv := wantAppendedEvent(t, s, spawned.ID, 1, store.TypeRoleClosed)
	if closeEv.Actor != store.RoleActor("builder") {
		t.Errorf("close actor = %q, want role:builder", closeEv.Actor)
	}

	// Closing again finds nothing open.
	r = runIn(t, dir, dbEnv, "role", "close", "builder")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "no open builder") {
		t.Fatalf("double close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r = runIn(t, dir, dbEnv, "role", "list", "--json")
	if r.exitCode != 0 {
		t.Fatalf("role list: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	list := mustJSON[roleListP](t, r.stdout)
	if list.Dispatch != spawned.Dispatch || len(list.Roles) != 1 || list.Roles[0].CloseReason != "completed" {
		t.Fatalf("list = %+v", list)
	}
}

func TestRole_AsRoleClaimIsBackedByAnOpenSpawn(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	refreshRepo(t, dir, dbEnv)

	// A claim with no spawn behind it is refused on the write path.
	r := runIn(t, dir, dbEnv, "matter", "create", "--title", "As role", "--as-role", "researcher")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "no open spawn") {
		t.Fatalf("unbacked claim: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if r := runIn(t, dir, dbEnv, "role", "spawn", "researcher"); r.exitCode != 0 {
		t.Fatalf("role spawn: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// The same command under the same claim now speaks as the role.
	r = runIn(t, dir, dbEnv, "matter", "create", "--title", "As role", "--as-role", "researcher", "--json")
	if r.exitCode != 0 {
		t.Fatalf("matter create --as-role: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	matter := mustJSON[nodePayload](t, r.stdout)
	s := openTestStore(t, strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH="))
	ev := wantOneEvent(t, s, matter.ID, store.TypeMatterCreated)
	if ev.Actor != store.RoleActor("researcher") {
		t.Errorf("actor = %q, want role:researcher", ev.Actor)
	}

	// WIP_AS_ROLE is the same claim spelled once per role session.
	env := append(append([]string{}, dbEnv...), "WIP_AS_ROLE=researcher")
	r = runIn(t, dir, env, "step", "create", matter.Locator, "--title", "Born under a role", "--json")
	if r.exitCode != 0 {
		t.Fatalf("step create under WIP_AS_ROLE: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	step := mustJSON[nodePayload](t, r.stdout)
	stepEv := wantOneEvent(t, s, step.ID, store.TypeStepCreated)
	if stepEv.Actor != store.RoleActor("researcher") {
		t.Errorf("step actor = %q, want role:researcher", stepEv.Actor)
	}

	// Once the role closes, the claim has nothing behind it again.
	if r := runIn(t, dir, dbEnv, "role", "close", "researcher"); r.exitCode != 0 {
		t.Fatalf("role close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, env, "step", "create", matter.Locator, "--title", "After the close")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "no open spawn") {
		t.Fatalf("claim after close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
}

func TestRole_GateConfigDrivesActivation_D14(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	refreshRepo(t, dir, dbEnv)

	// Dormant: no declared gate activates the Verifier.
	r := runIn(t, dir, dbEnv, "role", "spawn", "verifier")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "dormant") {
		t.Fatalf("dormant verifier: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Declaring `verified` is the whole of activation: no config names a role.
	if r := runIn(t, dir, dbEnv, "gate", "declare", "verified", "--scale", "step"); r.exitCode != 0 {
		t.Fatalf("gate declare verified: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	active := mustJSON[[]struct {
		Role   string `json:"role"`
		Active bool   `json:"active"`
		Inert  bool   `json:"inert"`
	}](t, runIn(t, dir, dbEnv, "role", "active", "--json").stdout)
	if len(active) != 2 || active[0].Role != "verifier" || !active[0].Active || active[1].Role != "warden" || active[1].Active {
		t.Fatalf("activation = %+v", active)
	}

	if r := runIn(t, dir, dbEnv, "role", "spawn", "verifier"); r.exitCode != 0 {
		t.Fatalf("spawn active verifier: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// A role-owned gate refuses the human and closes only under its owner.
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Verify me", "--json").stdout)
	st := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "One step", "--json").stdout)
	r = runIn(t, dir, dbEnv, "gate", "close", "verified", st.ID)
	if r.exitCode == 0 || !strings.Contains(r.stderr, "owning role") {
		t.Fatalf("human closing verified: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, dbEnv, "gate", "close", "verified", st.ID, "--as-role", "verifier")
	if r.exitCode != 0 {
		t.Fatalf("verifier closing verified: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s := openTestStore(t, strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH="))
	gates, err := s.ClosedGates(context.Background(), st.ID)
	if err != nil || len(gates) != 1 || gates[0].Gate != "verified" {
		t.Fatalf("closed gates = %+v, err=%v", gates, err)
	}
	gateEv := wantAppendedEvent(t, s, st.ID, 1, store.TypeGateClosed)
	if gateEv.Actor != store.RoleActor("verifier") {
		t.Errorf("gate.closed actor = %q, want role:verifier", gateEv.Actor)
	}

	// A human-owned gate refuses a role.
	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare reviewed-local: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, dbEnv, "gate", "close", "reviewed-local", m.ID, "--as-role", "verifier")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "human-owned") {
		t.Fatalf("verifier closing reviewed-local: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Warden: activation derives by the same rule, and stays inert even when
	// its gate is declared — nothing can answer `ci-green` before the seam.
	if r := runIn(t, dir, dbEnv, "gate", "declare", "ci-green", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare ci-green: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, dbEnv, "role", "spawn", "warden")
	if r.exitCode == 0 || !strings.Contains(r.stderr, "inert until the forge seam") {
		t.Fatalf("warden spawn: exit=%d stderr=%q, want the inert refusal", r.exitCode, r.stderr)
	}
}

func TestRole_DispatchCloseReapsOpenRoles_E2E(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	refreshRepo(t, dir, dbEnv)

	r := runIn(t, dir, dbEnv, "role", "spawn", "coordinator", "--json")
	if r.exitCode != 0 {
		t.Fatalf("role spawn: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	spawned := mustJSON[roleP](t, r.stdout)

	if r := runIn(t, dir, dbEnv, "dispatch", "close"); r.exitCode != 0 {
		t.Fatalf("dispatch close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH="))
	role, err := s.Role(context.Background(), spawned.ID)
	if err != nil || role.Open || role.CloseReason != store.RoleCloseReaped {
		t.Fatalf("role after dispatch close = %#v, err=%v (want reaped by the bracket)", role, err)
	}
}

// TestRole_ResearcherJITWorkplanFlow is MODEL §8.1's convention end to end:
// a Matter picked for dispatch arrives unplanned; a Researcher is spawned
// into the bracket, authors the Step list just-in-time through verbs — every
// birth speaking as role:researcher — and closes; Session then shows the
// role's footprint without special casing.
func TestRole_ResearcherJITWorkplanFlow(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	refreshRepo(t, dir, dbEnv)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Unplanned work", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if r := runIn(t, dir, dbEnv, "role", "spawn", "researcher"); r.exitCode != 0 {
		t.Fatalf("role spawn: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	asResearcher := append(append([]string{}, dbEnv...), "WIP_AS_ROLE=researcher")

	var stepIDs []string
	for _, title := range []string{"Survey the ground", "Do the work", "Prove it"} {
		r := runIn(t, dir, asResearcher, "step", "create", m.Locator, "--title", title, "--json")
		if r.exitCode != 0 {
			t.Fatalf("step create %q: exit=%d stderr=%q", title, r.exitCode, r.stderr)
		}
		stepIDs = append(stepIDs, mustJSON[nodePayload](t, r.stdout).ID)
	}
	if r := runIn(t, dir, asResearcher, "role", "close", "researcher"); r.exitCode != 0 {
		t.Fatalf("role close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH="))
	for _, id := range stepIDs {
		ev := wantOneEvent(t, s, id, store.TypeStepCreated)
		if ev.Actor != store.RoleActor("researcher") {
			t.Errorf("step %s born under %q, want role:researcher", id, ev.Actor)
		}
	}

	// Session sees the Researcher: its authored events, its spawn, its close.
	r := runIn(t, dir, dbEnv, "session", "--json")
	if r.exitCode != 0 {
		t.Fatalf("session: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	sessions := mustJSON[struct {
		Sessions []struct {
			Roles []struct {
				Name    string `json:"name"`
				Events  int    `json:"events"`
				Spawned int    `json:"spawned"`
				Closed  int    `json:"closed"`
			} `json:"roles"`
		} `json:"sessions"`
	}](t, r.stdout)
	if len(sessions.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one working period", sessions)
	}
	roles := sessions.Sessions[0].Roles
	if len(roles) != 1 || roles[0].Name != "researcher" ||
		roles[0].Events != 4 || roles[0].Spawned != 1 || roles[0].Closed != 1 {
		t.Fatalf("session roles = %+v, want researcher with 4 events, 1 spawned, 1 closed", roles)
	}
}
