// End-to-end tests for the `guards` Matter (workplans/guards.md, step-07),
// through the actual built binary (binPath, run, runIn, gitIn, newGitRepo,
// setupRepo, mustJSON, nodePayload, dbEnvPath, openTestStore, refreshPayload
// come from e2e_test.go / tiers_e2e_test.go / writesurface_birth_test.go /
// render_scratch_e2e_test.go, same package).
//
// Covers: a gate-order violation flagged by `doctor` and refused at `gate
// declare` time (step-03); a cyclic `blocked-by` graph flagged by `doctor`
// (step-02, the store-wide audit half — the add-time refusal half is
// write-surface's own, already-passing coverage in writesurface_gates_test.go
// and elsewhere, since a cycle can never be built through the write path
// alone); a tracked `.wip/` fixture flagged by `doctor` *and* refusing a real
// `wip refresh` before any file is written (step-04) — the two call sites
// checked independently so a regression in one can't hide behind the other;
// the pre-existing unknown-clone check still reporting correctly through the
// extended registry (step-01's regression case); and doctor's own exit
// posture, 0 clean / 1 with findings (step-06).
package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type findingsPayload struct {
	Findings []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"findings"`
}

func TestGuards_Doctor_CleanRepoExitsZeroWithEmptyFindings(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0 (clean); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty on a clean doctor run", r.stderr)
	}
	payload := mustJSON[findingsPayload](t, r.stdout)
	if payload.Findings == nil {
		t.Error("findings is null in JSON, want an empty list (never the literal absence of the key)")
	}
	if len(payload.Findings) != 0 {
		t.Errorf("findings = %+v, want none", payload.Findings)
	}

	human := runIn(t, dir, dbEnv, "doctor")
	if human.exitCode != 0 {
		t.Fatalf("doctor (human) exit code = %d, want 0; stderr=%q", human.exitCode, human.stderr)
	}
}

func TestGuards_Doctor_InertTrackerBindingsAreAdvisory(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Recorded work", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "bind", matter.Locator, "BDS-132", "--json"); r.exitCode != 0 {
		t.Fatalf("bind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0 (advisory only); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty for an advisory-only doctor run", r.stderr)
	}
	payload := mustJSON[findingsPayload](t, r.stdout)
	if len(payload.Findings) != 1 {
		t.Fatalf("findings = %+v, want one inert-binding advisory", payload.Findings)
	}
	var rawPayload struct {
		Findings []map[string]json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rawPayload); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	if got := len(rawPayload.Findings[0]); got != 2 {
		t.Errorf("advisory JSON fields = %v, want only code and message", rawPayload.Findings[0])
	}
	if got := payload.Findings[0].Code; got != "advisory.inert-tracker-binding" {
		t.Errorf("finding code = %q, want advisory.inert-tracker-binding", got)
	}
	wantMessage := "tracker reference BDS-132 on matter recorded-work is recorded as provenance only; wip will not push tracker updates"
	if got := payload.Findings[0].Message; got != wantMessage {
		t.Errorf("finding message = %q, want %q", got, wantMessage)
	}
	if r := runIn(t, dir, dbEnv, "unbind", matter.Locator, "BDS-132", "--json"); r.exitCode != 0 {
		t.Fatalf("unbind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 0 {
		t.Fatalf("doctor after unbind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	payload = mustJSON[findingsPayload](t, r.stdout)
	if len(payload.Findings) != 0 {
		t.Errorf("findings after unbind = %+v, want none", payload.Findings)
	}
	if r := runIn(t, dir, dbEnv, "bind", matter.Locator, "BDS-132", "--json"); r.exitCode != 0 {
		t.Fatalf("rebind after unbind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if r := runIn(t, dir, dbEnv, "outbox", "level", "boundary"); r.exitCode != 0 {
		t.Fatalf("outbox level boundary: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	r = runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 0 {
		t.Fatalf("doctor with effective pushes: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	payload = mustJSON[findingsPayload](t, r.stdout)
	for _, finding := range payload.Findings {
		if finding.Code == "advisory.inert-tracker-binding" {
			t.Errorf("findings = %+v, want no inert-binding advisory when pushes are effective", payload.Findings)
		}
	}

	if r := runIn(t, dir, dbEnv, "outbox", "level", "off"); r.exitCode != 0 {
		t.Fatalf("outbox level off: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s := openTestStore(t, dbEnvPath(dbEnv))
	repo := repoIDFromWorkingDir(t, s, dir)
	if err := s.DeclareGate(context.Background(), repo, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatalf("DeclareGate reviewed-local (direct): %v", err)
	}
	if err := s.DeclareGate(context.Background(), repo, "ci-green", store.ScaleStep); err != nil {
		t.Fatalf("DeclareGate ci-green (direct): %v", err)
	}
	r = runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 1 {
		t.Fatalf("doctor with advisory and failure: exit=%d, want 1; stderr=%q", r.exitCode, r.stderr)
	}
	payload = mustJSON[findingsPayload](t, r.stdout)
	wantCodes := map[string]bool{
		"advisory.inert-tracker-binding": false,
		"refusal.gate-order-violation":   false,
	}
	for _, finding := range payload.Findings {
		if _, ok := wantCodes[finding.Code]; ok {
			wantCodes[finding.Code] = true
		}
	}
	for code, found := range wantCodes {
		if !found {
			t.Errorf("findings = %+v, want %s", payload.Findings, code)
		}
	}
}

func TestGuards_GateDeclare_RefusesGateOrderViolation(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("declaring reviewed-local: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "gate", "declare", "ci-green", "--scale", "step", "--json")
	if r.exitCode != 3 {
		t.Fatalf("declaring ci-green at step scale: exit=%d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, r.stderr)
	}
	if envelope.Error.Code != "refusal.gate-order-violation" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "refusal.gate-order-violation")
	}
	want := "refused — ci-green at step scale would violate gate-order monotonicity against reviewed-local at matter scale"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q, want %q", envelope.Error.Message, want)
	}

	// doctor's own audit agrees this repo has no violation right now — the
	// refused declare never landed.
	dr := runIn(t, dir, dbEnv, "doctor", "--json")
	if dr.exitCode != 0 {
		t.Fatalf("doctor after the refused declare: exit=%d, want 0; stderr=%q", dr.exitCode, dr.stderr)
	}
}

func TestGuards_Doctor_FindsAGateOrderViolationLandedSomeOtherWay(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("declaring reviewed-local: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Land the violating binding directly against the store, bypassing
	// `gate declare`'s own precondition — the audit's job is exactly for
	// state that arrived some other way (a hand-edited database, an older
	// binary), the same rationale schema's own cycle_test.go documents.
	s := openTestStore(t, dbEnvPath(dbEnv))
	if err := s.DeclareGate(context.Background(), repoIDFromWorkingDir(t, s, dir), "ci-green", store.ScaleStep); err != nil {
		t.Fatalf("DeclareGate (direct): %v", err)
	}

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 1 {
		t.Fatalf("doctor exit code = %d, want 1 (findings present); stderr=%q", r.exitCode, r.stderr)
	}
	payload := mustJSON[findingsPayload](t, r.stdout)
	found := false
	for _, f := range payload.Findings {
		if f.Code == "refusal.gate-order-violation" {
			found = true
		}
	}
	if !found {
		t.Errorf("findings = %+v, want a refusal.gate-order-violation entry", payload.Findings)
	}
}

func TestGuards_Doctor_FindsABlockedByCycleLandedSomeOtherWay(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	a := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Cycle A", "--json").stdout)
	b := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Cycle B", "--json").stdout)

	// A cycle can never be built through `wip depend add` alone (WouldCycle
	// refuses the closing edge) — insert both edges directly, the same
	// technique internal/guards' own cycle tests use.
	s := openTestStore(t, dbEnvPath(dbEnv))
	repo := repoIDFromWorkingDir(t, s, dir)
	addRawEdge(t, s, repo, a.ID, b.ID)
	addRawEdge(t, s, repo, b.ID, a.ID)

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 1 {
		t.Fatalf("doctor exit code = %d, want 1 (findings present); stderr=%q", r.exitCode, r.stderr)
	}
	payload := mustJSON[findingsPayload](t, r.stdout)
	found := false
	for _, f := range payload.Findings {
		if f.Code == "refusal.blocked-by-cycle" {
			found = true
		}
	}
	if !found {
		t.Errorf("findings = %+v, want a refusal.blocked-by-cycle entry", payload.Findings)
	}

	// The add-time half of "two callers, one function" — refused directly
	// through the CLI, exit 3, never landing a second cycle.
	dep := runIn(t, dir, dbEnv, "depend", "add", a.Locator, "--blocked-by", b.Locator, "--json")
	if dep.exitCode != 3 {
		t.Fatalf("depend add closing an existing cycle: exit=%d, want 3; stderr=%q", dep.exitCode, dep.stderr)
	}
}

func TestGuards_TrackedWipDir_DoctorFindingAndRenderRefusal(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("initial refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// A fresh repo's first refresh renders nothing (no Matters exist yet)
	// and its scratch dir is empty — git tracks no empty directory, so
	// `.wip/` needs a real file under it before `add -f` has anything to
	// stage. Force-track it despite the .git/info/exclude entry
	// render-scratch wrote — exactly the accident MODEL §11 refuses against.
	if err := os.WriteFile(filepath.Join(dir, ".wip", "oops.txt"), []byte("should never be tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-f", ".wip")
	gitIn(t, dir, "commit", "-q", "-m", "oops, tracked .wip/")

	dr := runIn(t, dir, dbEnv, "doctor", "--json")
	if dr.exitCode != 1 {
		t.Fatalf("doctor exit code = %d, want 1 (findings present); stderr=%q", dr.exitCode, dr.stderr)
	}
	payload := mustJSON[findingsPayload](t, dr.stdout)
	found := false
	for _, f := range payload.Findings {
		if f.Code == "refusal.tracked-wip-dir" {
			found = true
		}
	}
	if !found {
		t.Errorf("findings = %+v, want a refusal.tracked-wip-dir entry", payload.Findings)
	}

	before, err := filepath.Glob(filepath.Join(dir, ".wip", "generated", "*"))
	if err != nil {
		t.Fatal(err)
	}

	rr := runIn(t, dir, dbEnv, "refresh", "--json")
	if rr.exitCode != 3 {
		t.Fatalf("refresh against a tracked .wip/: exit=%d, want 3 (refusal); stderr=%q", rr.exitCode, rr.stderr)
	}
	if rr.stdout != "" {
		t.Errorf("stdout = %q, want empty — the precondition must refuse before any write", rr.stdout)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(rr.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not the --json error envelope: %v (stderr=%q)", err, rr.stderr)
	}
	if envelope.Error.Code != "refusal.tracked-wip-dir" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "refusal.tracked-wip-dir")
	}

	after, err := filepath.Glob(filepath.Join(dir, ".wip", "generated", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("generated/ contents changed across the refused refresh (%v -> %v); the precondition must run before any write", before, after)
	}
}

// TestGuards_Doctor_UnknownCloneCheckStillWorksThroughTheRegistry is
// step-01's regression case: the pre-existing unknown-clone check (tiers
// step-04) still reports correctly now that it runs ahead of guards' new
// check registry, in both its "known" success shape and, combined with a
// clean repo, doctor's own overall clean exit.
func TestGuards_Doctor_UnknownCloneCheckStillWorksThroughTheRegistry(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	r := runIn(t, dir, dbEnv, "doctor", "--json")
	if r.exitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0; stderr=%q", r.exitCode, r.stderr)
	}
	var payload struct {
		Known    bool `json:"known"`
		Findings []struct {
			Code string `json:"code"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (stdout=%q)", err, r.stdout)
	}
	if !payload.Known {
		t.Error("known = false in a freshly-init'd clone, want true")
	}
	if len(payload.Findings) != 0 {
		t.Errorf("findings = %+v, want none", payload.Findings)
	}
}

// repoIDFromWorkingDir resolves the one Repo row a setupRepo fixture ever
// creates — a test-only shortcut these fixtures need to land raw store
// state, mirroring writesurface.CurrentRepo without importing a whole
// verb's worth of resolution. dir is unused beyond documenting intent (each
// fixture's store carries exactly one repo).
func repoIDFromWorkingDir(t *testing.T, s *store.Store, _ string) string {
	t.Helper()
	repos, err := s.Repos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected exactly one known repo, found %d", len(repos))
	}
	return repos[0].ID
}

// addRawEdge inserts a raw dependency.added event, bypassing DependAdd's
// WouldCycle precondition — the only way to land a cycle to audit.
func addRawEdge(t *testing.T, s *store.Store, repo, blocked, blocker string) {
	t.Helper()
	req := store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(context.Background(), req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeDependencyAdded,
			Subject: blocked,
			Payload: store.DependencyChange{Edge: tx.NewID(), Blocker: blocker},
		}}, nil
	}); err != nil {
		t.Fatalf("addRawEdge(%s <- %s): %v", blocked, blocker, err)
	}
}
