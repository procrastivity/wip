// End-to-end tests for the write-surface Matter's gates-and-dependencies
// Stage (workplans/write-surface.md, step-05), through the actual built
// binary.
package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func gateEvents(t *testing.T, dbPath string) []store.Event {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(context.Background())
	if closeErr := s.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertNoGateReadEvents(t *testing.T, dir string, dbEnv []string, dbPath string, args ...string) result {
	t.Helper()
	before := gateEvents(t, dbPath)
	r := runIn(t, dir, dbEnv, args...)
	after := gateEvents(t, dbPath)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%v changed the event log", args)
	}
	return r
}

func declareGateCLI(t *testing.T, dir string, dbEnv []string, name, scale string) {
	t.Helper()
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", name, "--scale", scale); r.exitCode != 0 {
		t.Fatalf("declare %s: exit=%d stderr=%q", name, r.exitCode, r.stderr)
	}
}

func TestGateList_ReadsDeclarationsInOrderAndEmitsNoEvents(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	empty := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "list")
	if empty.exitCode != 0 || empty.stdout != "no gates declared\n" || empty.stderr != "" {
		t.Fatalf("empty gate list = %+v", empty)
	}
	emptyJSON := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "list", "--json")
	var emptyPayload struct {
		Repo  string           `json:"repo"`
		Gates []map[string]any `json:"gates"`
	}
	if err := json.Unmarshal([]byte(emptyJSON.stdout), &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if emptyPayload.Repo == "" || emptyPayload.Gates == nil || len(emptyPayload.Gates) != 0 {
		t.Fatalf("empty gate list JSON = %+v, want a non-nil empty gates array", emptyPayload)
	}

	for _, declaration := range [][2]string{
		{"verified", "step"},
		{"stage-check", "stage"},
		{"reviewed-local", "matter"},
		{"ci-green", "matter"},
		{"approved", "matter"},
	} {
		declareGateCLI(t, dir, dbEnv, declaration[0], declaration[1])
	}

	first := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "list")
	wantHuman := "approved         matter  human\n" +
		"ci-green         matter  role:warden\n" +
		"reviewed-local   matter  human\n" +
		"stage-check      stage   human\n" +
		"verified         step    role:verifier\n"
	if first.exitCode != 0 || first.stdout != wantHuman || first.stderr != "" {
		t.Fatalf("gate list human = %+v, want stdout %q", first, wantHuman)
	}

	jsonResult := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "list", "--json")
	jsonAgain := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "list", "--json")
	if jsonResult.stdout != jsonAgain.stdout {
		t.Fatalf("gate list JSON is not deterministic: %q vs %q", jsonResult.stdout, jsonAgain.stdout)
	}
	var payload struct {
		Repo  string `json:"repo"`
		Gates []struct {
			Name  string `json:"name"`
			Scale string `json:"scale"`
			Owner string `json:"owner"`
		} `json:"gates"`
	}
	if err := json.Unmarshal([]byte(jsonResult.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Repo == "" || len(payload.Gates) != 5 {
		t.Fatalf("gate list JSON = %+v", payload)
	}
	for i, want := range []struct{ name, scale, owner string }{
		{"approved", "matter", "human"},
		{"ci-green", "matter", "role:warden"},
		{"reviewed-local", "matter", "human"},
		{"stage-check", "stage", "human"},
		{"verified", "step", "role:verifier"},
	} {
		if got := payload.Gates[i]; got.Name != want.name || got.Scale != want.scale || got.Owner != want.owner {
			t.Errorf("gate %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestGateStatus_ReportsMixedEffectiveRequirementsForNestedLocators(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)
	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Checkout", "--json").stdout)
	stage := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "stage", "create", matter.ID, "--title", "Build", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", stage.ID, "--title", "Verify", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", step.ID); r.exitCode != 0 {
		t.Fatalf("start step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", step.ID); r.exitCode != 0 {
		t.Fatalf("finish step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	declareGateCLI(t, dir, dbEnv, "approved", "matter")
	declareGateCLI(t, dir, dbEnv, "verified", "step")
	declareGateCLI(t, dir, dbEnv, "local-check", "step")
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "approved", matter.ID); r.exitCode != 0 {
		t.Fatalf("close approved: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "local-check", step.ID); r.exitCode != 0 {
		t.Fatalf("close local-check: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "repair", "verified", step.ID); r.exitCode != 0 {
		t.Fatalf("repair verified: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	declareGateCLI(t, dir, dbEnv, "ci-green", "matter")

	statusArgs := []string{"plumbing", "gate", "status", matter.Locator + "/presentation-only/" + step.Locator}
	human := assertNoGateReadEvents(t, dir, dbEnv, dbPath, statusArgs...)
	if human.exitCode != 0 || human.stderr != "" {
		t.Fatalf("gate status human = %+v", human)
	}
	for _, want := range []string{
		"checkout/build · step-01  step · done\n",
		"locally-complete: yes\n",
		"sealed: no\n",
		"  local-check [step, own, checkout/build · step-01, owner human]: closed by human at ",
		"  verified [step, own, checkout/build · step-01, owner role:verifier]: exempt\n",
		"  approved [matter, enclosing, checkout, owner human]: closed by human at ",
		"  ci-green [matter, enclosing, checkout, owner role:warden]: open\n",
	} {
		if !strings.Contains(human.stdout, want) {
			t.Errorf("gate status human = %q, missing %q", human.stdout, want)
		}
	}

	jsonResult := assertNoGateReadEvents(t, dir, dbEnv, dbPath, append(statusArgs, "--json")...)
	jsonByULID := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", step.ID, "--json")
	if jsonResult.stdout != jsonByULID.stdout {
		t.Fatalf("nested and ULID status differ: %q vs %q", jsonResult.stdout, jsonByULID.stdout)
	}
	jsonAgain := assertNoGateReadEvents(t, dir, dbEnv, dbPath, append(statusArgs, "--json")...)
	if jsonResult.stdout != jsonAgain.stdout {
		t.Fatalf("gate status JSON is not deterministic: %q vs %q", jsonResult.stdout, jsonAgain.stdout)
	}
	var payload struct {
		Node struct {
			ID        string `json:"id"`
			Address   string `json:"address"`
			Kind      string `json:"kind"`
			Lifecycle string `json:"lifecycle"`
		} `json:"node"`
		LocallyComplete bool             `json:"locallyComplete"`
		Sealed          bool             `json:"sealed"`
		Requirements    []map[string]any `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(jsonResult.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Node.ID != step.ID || payload.Node.Address != "checkout/build · step-01" || payload.Node.Kind != "step" ||
		payload.Node.Lifecycle != "done" || !payload.LocallyComplete || payload.Sealed || len(payload.Requirements) != 4 {
		t.Fatalf("gate status JSON = %+v", payload)
	}
	for i, want := range []struct {
		name, scale, owner, relationship, subject, state string
	}{
		{"local-check", "step", "human", "own", step.ID, "closed"},
		{"verified", "step", "role:verifier", "own", step.ID, "exempt"},
		{"approved", "matter", "human", "enclosing", matter.ID, "closed"},
		{"ci-green", "matter", "role:warden", "enclosing", matter.ID, "open"},
	} {
		got := payload.Requirements[i]
		if got["name"] != want.name || got["scale"] != want.scale || got["owner"] != want.owner ||
			got["relationship"] != want.relationship || got["state"] != want.state {
			t.Errorf("requirement %d = %+v, want %+v", i, got, want)
		}
		if subject, ok := got["subject"].(map[string]any); !ok || subject["id"] != want.subject {
			t.Errorf("requirement %d subject = %#v, want %s", i, got["subject"], want.subject)
		}
		_, hasClosedBy := got["closedBy"]
		_, hasClosedAt := got["closedAt"]
		if want.state == "closed" {
			if !hasClosedBy || !hasClosedAt {
				t.Errorf("closed requirement %d omits closure metadata: %+v", i, got)
			}
		} else if hasClosedBy || hasClosedAt {
			t.Errorf("%s requirement %d includes closure metadata: %+v", want.state, i, got)
		}
	}
}

func TestGateStatus_NoDeclarationsAndLocatorRefusalsAreReadOnly(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)
	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Plain Task", "--json").stdout)
	for _, args := range [][]string{{"plumbing", "start", matter.Locator}, {"plumbing", "finish", matter.Locator}} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	human := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", matter.Locator)
	wantHuman := "plain-task  matter · done\nlocally-complete: yes\nsealed: yes\nrequirements: none\n"
	if human.exitCode != 0 || human.stdout != wantHuman || human.stderr != "" {
		t.Fatalf("empty gate status human = %+v, want stdout %q", human, wantHuman)
	}
	jsonResult := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", matter.ID, "--json")
	if !strings.Contains(jsonResult.stdout, `"requirements":[]`) || !strings.Contains(jsonResult.stdout, `"sealed":true`) {
		t.Fatalf("empty gate status JSON = %q", jsonResult.stdout)
	}

	unknown := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", "missing")
	if unknown.exitCode != 1 || unknown.stderr != "wip: plumbing gate status: no matter labeled \"missing\"\n" {
		t.Fatalf("unknown human locator = %+v", unknown)
	}
	unknownJSON := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", "missing", "--json")
	if unknownJSON.exitCode != 1 || unknownJSON.stderr != `{"error":{"code":"validation.unknown-locator","message":"no matter labeled \"missing\""}}`+"\n" {
		t.Fatalf("unknown JSON locator = %+v", unknownJSON)
	}

	otherDir := newGitRepo(t, "other")
	if r := runIn(t, otherDir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("other init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	otherMatter := mustJSON[nodePayload](t, runIn(t, otherDir, dbEnv, "plumbing", "matter", "create", "--title", "Other", "--json").stdout)
	crossRepo := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", otherMatter.ID, "--json")
	if crossRepo.exitCode != 1 || crossRepo.stderr != fmt.Sprintf("{\"error\":{\"code\":\"validation.unknown-locator\",\"message\":\"no node %s in this repo\"}}\n", otherMatter.ID) {
		t.Fatalf("cross-repository ULID = %+v", crossRepo)
	}

	tombstoneMatter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Tombstone", "--json").stdout)
	tombstoneStep := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", tombstoneMatter.ID, "--title", "Removed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "step", "remove", tombstoneStep.ID); r.exitCode != 0 {
		t.Fatalf("remove step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	tombstoned := assertNoGateReadEvents(t, dir, dbEnv, dbPath, "plumbing", "gate", "status", tombstoneStep.ID, "--json")
	if tombstoned.exitCode != 1 || tombstoned.stderr != fmt.Sprintf("{\"error\":{\"code\":\"validation.unknown-locator\",\"message\":\"no node %s in this repo\"}}\n", tombstoneStep.ID) {
		t.Fatalf("tombstoned ULID = %+v", tombstoned)
	}
	wrongArity := runIn(t, dir, dbEnv, "plumbing", "gate", "status")
	if wrongArity.exitCode != 2 {
		t.Fatalf("wrong gate status arity exit=%d, want 2; stderr=%q", wrongArity.exitCode, wrongArity.stderr)
	}
}

func TestGateCommands_ManifestAndHarnessProjection(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	manifestResult := runIn(t, dir, dbEnv, "manifest", "--json")
	if manifestResult.exitCode != 0 {
		t.Fatalf("manifest: exit=%d stderr=%q", manifestResult.exitCode, manifestResult.stderr)
	}
	var manifest struct {
		SchemaVersion int `json:"schemaVersion"`
		Verbs         []struct {
			Name         string          `json:"name"`
			Kind         string          `json:"kind"`
			Usage        string          `json:"usage"`
			Description  string          `json:"description"`
			OutputSchema json.RawMessage `json:"outputSchema"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(manifestResult.stdout), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 {
		t.Fatalf("manifest schema version = %d, want unchanged version 1", manifest.SchemaVersion)
	}
	for _, want := range []struct {
		name, usage, description string
	}{
		{"plumbing gate list", "", "list this repo's gate declarations - read-only, emits no event"},
		{"plumbing gate status", "<locator>", "show one live node's effective gate requirements - read-only, emits no event"},
	} {
		found := false
		for _, verb := range manifest.Verbs {
			if verb.Name != want.name {
				continue
			}
			found = true
			if verb.Kind != "plumbing" || verb.Usage != want.usage || verb.Description != want.description || verb.OutputSchema != nil {
				t.Errorf("manifest %q = %+v, want plumbing usage=%q description=%q and no outputSchema", want.name, verb, want.usage, want.description)
			}
		}
		if !found {
			t.Errorf("manifest does not contain %q", want.name)
		}
	}

	skillsDir := t.TempDir()
	installed := runIn(t, dir, []string{"WIP_PI_SKILLS_DIR=" + skillsDir}, "install", "pi", "--json")
	if installed.exitCode != 0 {
		t.Fatalf("install pi: exit=%d stderr=%q", installed.exitCode, installed.stderr)
	}
	var installPayload struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installed.stdout), &installPayload); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(installPayload.Dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(skill)
	for _, want := range []string{
		"| `wip plumbing gate list` | list this repo's gate declarations - read-only, emits no event |",
		"| `wip plumbing gate status <locator>` | show one live node's effective gate requirements - read-only, emits no event |",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("generated Pi skill does not contain %q", want)
		}
	}
}

func TestGateDeclare_EmitsOneGateDeclaredEventPerDeclaration(t *testing.T) {
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

	r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter", "--json")
	if r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s = openTestStore(t, dbPathStr)
	after, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("gate declare appended %d events, want exactly one gate.declared", len(after)-len(before))
	}

	// The verb is general — it can express a forge gate too (the schema
	// Brief: "the schema must be *able* to hold gate state at any scale").
	// What actually bars declaring verified/reviewed/ci-green in this
	// dogfood is HANDOFF §1.2's operating discipline plus the structural
	// fact that they can never close without their owning role (MODEL
	// §2.3) — not a hardcoded refusal here. Declaring one is technically
	// legal and emits its own gate.declared like any other declaration.
	forge := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "verified", "--scale", "step", "--json")
	if forge.exitCode != 0 {
		t.Fatalf("declaring a forge gate should be technically legal (the verb is general): exit=%d stderr=%q", forge.exitCode, forge.stderr)
	}
	s = openTestStore(t, dbPathStr)
	afterForge, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(afterForge) != len(before)+2 {
		t.Errorf("declaring a forge gate appended %d events total, want two", len(afterForge)-len(before))
	}
}

func TestGateDeclare_IsProspectiveAndRefusesScaleChanges(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Already sealed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "verified", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("declare prospective gate: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s := openTestStore(t, dbEnvPath(dbEnv))
	repo := repoIDFromWorkingDir(t, s, dir)
	exempt, err := s.GateExempt(context.Background(), repo, m.ID, "verified")
	if err != nil {
		t.Fatal(err)
	}
	if !exempt {
		t.Fatal("the already-sealed Matter has no prospective gate exemption")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	closeResult := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "verified", m.Locator, "--json")
	if closeResult.exitCode != 3 {
		t.Fatalf("close exempt gate: exit=%d, want 3; stderr=%q", closeResult.exitCode, closeResult.stderr)
	}
	var closeEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(closeResult.stderr), &closeEnvelope); err != nil {
		t.Fatalf("decode close refusal: %v", err)
	}
	if closeEnvelope.Error.Code != "refusal.gate-already-satisfied" {
		t.Errorf("close refusal code = %q, want refusal.gate-already-satisfied", closeEnvelope.Error.Code)
	}

	change := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "verified", "--scale", "step", "--json")
	if change.exitCode != 3 {
		t.Fatalf("change gate scale: exit=%d, want 3; stderr=%q", change.exitCode, change.stderr)
	}
	var changeEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(change.stderr), &changeEnvelope); err != nil {
		t.Fatalf("decode scale-change refusal: %v", err)
	}
	if changeEnvelope.Error.Code != "refusal.gate-scale-change" {
		t.Errorf("scale-change refusal code = %q, want refusal.gate-scale-change", changeEnvelope.Error.Code)
	}
}

func TestGateRepair_AddsLegacyExemptionAsOneEvent(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, gate := range []string{"reviewed-local", "verified"} {
		if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", gate, "--scale", "matter"); r.exitCode != 0 {
			t.Fatalf("declare %s: exit=%d stderr=%q", gate, r.exitCode, r.stderr)
		}
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Legacy sealed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "reviewed-local", m.Locator); r.exitCode != 0 {
		t.Fatalf("close reviewed-local: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbEnvPath(dbEnv))
	before, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	r := runIn(t, dir, dbEnv, "plumbing", "gate", "repair", "verified", m.Locator, "--json")
	if r.exitCode != 0 {
		t.Fatalf("repair verified: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	var got struct {
		Gate  string `json:"gate"`
		Node  string `json:"node"`
		Scale string `json:"scale"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("decode repair output: %v", err)
	}
	if got.Gate != "verified" || got.Node != m.ID || got.Scale != "matter" {
		t.Fatalf("repair output = %+v", got)
	}

	s = openTestStore(t, dbEnvPath(dbEnv))
	after, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("gate repair appended %d events, want exactly one gate.exemption-repaired", len(after)-len(before))
	}
	repo := repoIDFromWorkingDir(t, s, dir)
	exempt, err := s.GateExempt(context.Background(), repo, m.ID, "verified")
	if err != nil || !exempt {
		t.Fatalf("repaired exemption = %v, err=%v", exempt, err)
	}
}

func TestGateClose_OneEventRightSubjectAndScale(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Gate me", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	// Closing an undeclared gate is refused, no event appended.
	badClose := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "no-such-gate", m.ID)
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

	r := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "reviewed-local", m.ID, "--json")
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
	m1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Downstream", "--json").stdout)
	m2 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Upstream", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	// m1 blocked-by m2.
	addR := runIn(t, dir, dbEnv, "plumbing", "depend", "add", m1.ID, "--blocked-by", m2.ID, "--json")
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
	cycleR := runIn(t, dir, dbEnv, "plumbing", "depend", "add", m2.ID, "--blocked-by", m1.ID, "--json")
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
	remR := runIn(t, dir, dbEnv, "plumbing", "depend", "remove", m1.ID, "--blocked-by", m2.ID, "--json")
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

	remAgain := runIn(t, dir, dbEnv, "plumbing", "depend", "remove", m1.ID, "--blocked-by", m2.ID)
	if remAgain.exitCode == 0 {
		t.Fatalf("removing an already-tombstoned edge should be refused, not silently succeed")
	}
}

func TestBindUnbindRebind_MatterReferenceSetOneEventEach(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Bind me", "--json").stdout)
	dbPathStr := dbEnvPath(dbEnv)

	r := runIn(t, dir, dbEnv, "plumbing", "bind", m.ID, "https://tracker.example.com/issue/42", "--json")
	if r.exitCode != 0 {
		t.Fatalf("bind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s := openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 1, store.TypeReferenceAdded) // m already carried matter.created

	refs, err := s.TrackerReferences(context.Background(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != "https://tracker.example.com/issue/42" {
		t.Fatalf("tracker refs = %v, want the bound URL", refs)
	}

	second := "https://tracker.example.com/issue/43"
	if r := runIn(t, dir, dbEnv, "plumbing", "rebind", m.ID, refs[0], second, "--json"); r.exitCode != 0 {
		t.Fatalf("rebind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 2, store.TypeReferenceRebound)
	refs, err = s.TrackerReferences(context.Background(), m.ID)
	if err != nil || len(refs) != 1 || refs[0] != second {
		t.Fatalf("tracker refs after rebind = %v (err %v), want %s", refs, err, second)
	}

	if r := runIn(t, dir, dbEnv, "plumbing", "unbind", m.ID, second, "--json"); r.exitCode != 0 {
		t.Fatalf("unbind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	s = openTestStore(t, dbPathStr)
	wantAppendedEvent(t, s, m.ID, 3, store.TypeReferenceRemoved)
	refs, err = s.TrackerReferences(context.Background(), m.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("tracker refs after unbind = %v (err %v), want none", refs, err)
	}
}

func TestBindHumanOutputDisclosesInertReferenceOnlyWhenTrackerPushesAreOff(t *testing.T) {
	tests := []struct {
		name      string
		configure func(t *testing.T, dir string, dbEnv []string)
		want      string
	}{
		{
			name: "off by default without a backend",
			want: "bound %s to https://tracker.example.com/issue/42; reference recorded and inert: no tracker update will be sent\n",
		},
		{
			name: "configured backend enables boundary pushing",
			configure: func(t *testing.T, dir string, dbEnv []string) {
				t.Helper()
				if r := runIn(t, dir, dbEnv, "plumbing", "outbox", "backend", "github"); r.exitCode != 0 {
					t.Fatalf("configure tracker backend: exit=%d stderr=%q", r.exitCode, r.stderr)
				}
			},
			want: "bound %s to https://tracker.example.com/issue/42\n",
		},
		{
			name: "explicit off overrides a configured backend",
			configure: func(t *testing.T, dir string, dbEnv []string) {
				t.Helper()
				if r := runIn(t, dir, dbEnv, "plumbing", "outbox", "backend", "github"); r.exitCode != 0 {
					t.Fatalf("configure tracker backend: exit=%d stderr=%q", r.exitCode, r.stderr)
				}
				if r := runIn(t, dir, dbEnv, "plumbing", "outbox", "level", "off"); r.exitCode != 0 {
					t.Fatalf("turn tracker pushing off: exit=%d stderr=%q", r.exitCode, r.stderr)
				}
			},
			want: "bound %s to https://tracker.example.com/issue/42; reference recorded and inert: no tracker update will be sent\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
			dir := newGitRepo(t, "widget")
			if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
				t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
			}
			m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Bind me", "--json").stdout)
			if test.configure != nil {
				test.configure(t, dir, dbEnv)
			}

			r := runIn(t, dir, dbEnv, "plumbing", "bind", m.ID, "https://tracker.example.com/issue/42")
			if r.exitCode != 0 {
				t.Fatalf("bind: exit=%d stderr=%q", r.exitCode, r.stderr)
			}
			if want := fmt.Sprintf(test.want, m.ID); r.stdout != want {
				t.Errorf("bind stdout = %q, want %q", r.stdout, want)
			}
		})
	}
}

func TestBindJSONOutputIsUnchangedWhenTrackerPushesAreOff(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "widget")
	if r := runIn(t, dir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Bind me", "--json").stdout)

	r := runIn(t, dir, dbEnv, "plumbing", "bind", m.ID, "https://tracker.example.com/issue/42", "--json")
	if r.exitCode != 0 {
		t.Fatalf("bind: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if want := fmt.Sprintf("{\"node\":%q,\"ref\":\"https://tracker.example.com/issue/42\"}\n", m.ID); r.stdout != want {
		t.Errorf("bind JSON stdout = %q, want %q", r.stdout, want)
	}
}

func TestGateDismiss_TwoDoneMatterGatesSealOnlyAfterTheFinalDismissal(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	for _, gate := range []string{"approve", "review"} {
		declareGateCLI(t, dir, dbEnv, gate, "matter")
	}
	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Emergency gate", "--json").stdout)
	for _, args := range [][]string{{"plumbing", "start", matter.ID}, {"plumbing", "finish", matter.ID}} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	first := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "approve", matter.ID,
		"--reason", "the approving reviewer is unavailable", "--json")
	if first.exitCode != 0 || first.stderr != "" {
		t.Fatalf("first dismissal = %+v", first)
	}
	var firstPayload struct {
		Gate        string `json:"gate"`
		Node        string `json:"node"`
		Scale       string `json:"scale"`
		State       string `json:"state"`
		DismissedBy string `json:"dismissedBy"`
		DismissedAt string `json:"dismissedAt"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(first.stdout), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if firstPayload.Gate != "approve" || firstPayload.Node != matter.ID || firstPayload.Scale != "matter" ||
		firstPayload.State != "dismissed" || firstPayload.DismissedBy != "human" || firstPayload.DismissedAt == "" ||
		firstPayload.Reason != "the approving reviewer is unavailable" {
		t.Fatalf("first dismissal JSON = %+v", firstPayload)
	}

	status1 := runIn(t, dir, dbEnv, "plumbing", "gate", "status", matter.ID, "--json")
	if status1.exitCode != 0 || status1.stderr != "" {
		t.Fatalf("status after first dismissal = %+v", status1)
	}
	var statusPayload struct {
		Sealed       bool `json:"sealed"`
		Requirements []struct {
			Name            string `json:"name"`
			State           string `json:"state"`
			ClosedBy        string `json:"closedBy"`
			ClosedAt        string `json:"closedAt"`
			DismissedBy     string `json:"dismissedBy"`
			DismissedAt     string `json:"dismissedAt"`
			DismissalReason string `json:"dismissalReason"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(status1.stdout), &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload.Sealed || len(statusPayload.Requirements) != 2 {
		t.Fatalf("status after first dismissal = %+v, want unsealed with two requirements", statusPayload)
	}
	if got := statusPayload.Requirements[0]; got.Name != "approve" || got.State != "dismissed" || got.DismissedBy != "human" ||
		got.DismissedAt != firstPayload.DismissedAt || got.DismissalReason != firstPayload.Reason || got.ClosedBy != "" || got.ClosedAt != "" {
		t.Errorf("dismissed status = %+v, want dismissal metadata without close metadata", got)
	}
	if got := statusPayload.Requirements[1]; got.Name != "review" || got.State != "open" {
		t.Errorf("remaining gate status = %+v, want open", got)
	}
	humanStatus := runIn(t, dir, dbEnv, "plumbing", "gate", "status", matter.ID)
	if humanStatus.exitCode != 0 || !strings.Contains(humanStatus.stdout, "dismissed by human at ") ||
		!strings.Contains(humanStatus.stdout, ": the approving reviewer is unavailable") {
		t.Fatalf("human status after first dismissal = %+v, want dismissal actor, timestamp and reason", humanStatus)
	}

	second := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID,
		"--reason", "the review system is unavailable during the incident", "--json")
	if second.exitCode != 0 || second.stderr != "" {
		t.Fatalf("second dismissal = %+v", second)
	}
	var secondPayload struct {
		State       string `json:"state"`
		DismissedBy string `json:"dismissedBy"`
		DismissedAt string `json:"dismissedAt"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(second.stdout), &secondPayload); err != nil {
		t.Fatal(err)
	}
	if secondPayload.State != "dismissed" || secondPayload.DismissedBy != "human" || secondPayload.DismissedAt == "" ||
		secondPayload.Reason != "the review system is unavailable during the incident" {
		t.Fatalf("second dismissal JSON = %+v", secondPayload)
	}

	status2 := runIn(t, dir, dbEnv, "plumbing", "gate", "status", matter.ID, "--json")
	if status2.exitCode != 0 || status2.stderr != "" {
		t.Fatalf("status after final dismissal = %+v", status2)
	}
	var finalStatus struct {
		Sealed       bool `json:"sealed"`
		Requirements []struct {
			State           string `json:"state"`
			DismissedBy     string `json:"dismissedBy"`
			DismissedAt     string `json:"dismissedAt"`
			DismissalReason string `json:"dismissalReason"`
			ClosedBy        string `json:"closedBy"`
			ClosedAt        string `json:"closedAt"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal([]byte(status2.stdout), &finalStatus); err != nil {
		t.Fatal(err)
	}
	if !finalStatus.Sealed || len(finalStatus.Requirements) != 2 {
		t.Fatalf("final status = %+v, want sealed", finalStatus)
	}
	for _, requirement := range finalStatus.Requirements {
		if requirement.State != "dismissed" || requirement.DismissedBy != "human" || requirement.DismissedAt == "" ||
			requirement.DismissalReason == "" || requirement.ClosedBy != "" || requirement.ClosedAt != "" {
			t.Errorf("final dismissed requirement = %+v, want dismissal-only metadata", requirement)
		}
	}

	events := gateEvents(t, dbEnvPath(dbEnv))
	var dismissed []store.Event
	for _, event := range events {
		if event.Type == store.TypeGateDismissed {
			dismissed = append(dismissed, event)
		}
	}
	if len(dismissed) != 2 || dismissed[0].Subject != matter.ID || dismissed[1].Subject != matter.ID {
		t.Fatalf("dismissal events = %+v, want exactly two Matter-subject events", dismissed)
	}
	for _, event := range dismissed {
		var payload store.GateDismissed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Gate == "" || payload.Scale != store.ScaleMatter || payload.Reason == "" {
			t.Errorf("dismissal event payload = %+v, want gate, matter scale and reason", payload)
		}
	}
}

func TestGateDismiss_ReasonLifecycleAndRepeatRefusalsDoNotAppendEvents(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	declareGateCLI(t, dir, dbEnv, "review", "matter")
	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Dismissal refusals", "--json").stdout)

	before := len(gateEvents(t, dbEnvPath(dbEnv)))
	missing := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID, "--json")
	if missing.exitCode != 2 || missing.stdout != "" || missing.stderr == "" {
		t.Fatalf("missing reason = %+v, want usage refusal", missing)
	}
	if got := len(gateEvents(t, dbEnvPath(dbEnv))); got != before {
		t.Fatalf("missing reason changed event count from %d to %d", before, got)
	}

	blank := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID, "--reason", " \t", "--json")
	if blank.exitCode != 1 || blank.stdout != "" {
		t.Fatalf("blank reason = %+v, want validation refusal", blank)
	}
	if got := len(gateEvents(t, dbEnvPath(dbEnv))); got != before {
		t.Fatalf("blank reason changed event count from %d to %d", before, got)
	}

	if r := runIn(t, dir, dbEnv, "plumbing", "start", matter.ID); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	notDone := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID, "--reason", "not ready", "--json")
	if notDone.exitCode != 3 || notDone.stdout != "" {
		t.Fatalf("not-Done dismissal = %+v, want refusal", notDone)
	}
	if got := len(gateEvents(t, dbEnvPath(dbEnv))); got != before+1 {
		t.Fatalf("not-Done dismissal changed event count from %d to %d", before+1, got)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", matter.ID); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	humanDismiss := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID, "--reason", "the review is waived")
	if humanDismiss.exitCode != 0 || !strings.Contains(humanDismiss.stdout, "dismissed review on ") ||
		!strings.Contains(humanDismiss.stdout, " by human at ") || !strings.Contains(humanDismiss.stdout, ": the review is waived") {
		t.Fatalf("dismiss human output = %+v, want dismissal actor, timestamp and reason", humanDismiss)
	}
	afterDismiss := len(gateEvents(t, dbEnvPath(dbEnv)))
	repeated := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "review", matter.ID, "--reason", "a different reason", "--json")
	if repeated.exitCode != 3 || repeated.stdout != "" {
		t.Fatalf("repeated dismissal = %+v, want refusal", repeated)
	}
	if got := len(gateEvents(t, dbEnvPath(dbEnv))); got != afterDismiss {
		t.Fatalf("repeated dismissal changed event count from %d to %d", afterDismiss, got)
	}
}

func TestGateDismiss_VerifierRoleCanDismissVerifiedButUnrelatedRoleCannot(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	refreshRepo(t, dir, dbEnv)
	declareGateCLI(t, dir, dbEnv, "verified", "step")
	for _, role := range []string{"verifier", "researcher"} {
		if r := runIn(t, dir, dbEnv, "plumbing", "role", "spawn", role); r.exitCode != 0 {
			t.Fatalf("spawn %s: exit=%d stderr=%q", role, r.exitCode, r.stderr)
		}
	}
	matter := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Verifier dismissal", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", matter.ID, "--title", "Check", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", step.ID); r.exitCode != 0 {
		t.Fatalf("start step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", step.ID); r.exitCode != 0 {
		t.Fatalf("finish step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	before := len(gateEvents(t, dbEnvPath(dbEnv)))
	unrelated := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "verified", step.ID,
		"--as-role", "researcher", "--reason", "researcher is not the verifier", "--json")
	if unrelated.exitCode != 3 || unrelated.stdout != "" {
		t.Fatalf("unrelated role dismissal = %+v, want refusal", unrelated)
	}
	if got := len(gateEvents(t, dbEnvPath(dbEnv))); got != before {
		t.Fatalf("unrelated role changed event count from %d to %d", before, got)
	}

	allowed := runIn(t, dir, dbEnv, "plumbing", "gate", "dismiss", "verified", step.ID,
		"--as-role", "verifier", "--reason", "the verification service is unavailable", "--json")
	if allowed.exitCode != 0 || allowed.stderr != "" {
		t.Fatalf("verifier dismissal = %+v", allowed)
	}
	var payload struct {
		State       string `json:"state"`
		DismissedBy string `json:"dismissedBy"`
		DismissedAt string `json:"dismissedAt"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(allowed.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State != "dismissed" || payload.DismissedBy != "role:verifier" || payload.DismissedAt == "" ||
		payload.Reason != "the verification service is unavailable" {
		t.Fatalf("verifier dismissal JSON = %+v", payload)
	}
	events := gateEvents(t, dbEnvPath(dbEnv))
	var found *store.Event
	for i := range events {
		if events[i].Type == store.TypeGateDismissed {
			found = &events[i]
		}
	}
	if found == nil || found.Subject != step.ID || found.Actor != store.RoleActor("verifier") {
		t.Fatalf("dismissal event = %+v, want step subject and role:verifier actor", found)
	}
}

func TestGateHelpDistinguishesCloseFromEmergencyDismissal(t *testing.T) {
	r := run(t, nil, "plumbing", "gate", "--help")
	if r.exitCode != 0 {
		t.Fatalf("gate help: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{
		"close       close a declared gate against a node",
		"dismiss     dismiss an open gate on a Done node with a required emergency reason",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("gate help = %q, missing %q", r.stdout, want)
		}
	}
	dismissHelp := run(t, nil, "plumbing", "gate", "dismiss", "--help")
	if dismissHelp.exitCode != 0 || !strings.Contains(dismissHelp.stdout, "--reason") {
		t.Fatalf("dismiss help = %+v, want required reason vocabulary", dismissHelp)
	}
}
