// End-to-end tests for D67's choose-next reframing, through the actual built
// binary (binPath, run, runIn, gitIn, newGitRepo, setupRepo, mustJSON,
// nodePayload come from e2e_test.go / tiers_e2e_test.go / writesurface_birth_test.go,
// same package): `wip next --clear`, the `choose-next` JSON kind, and the
// post-commit hand-off finish/gate-close/cancel print.
package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNext_Clear_HumanThenNoCursor clears a cursor set with `--set` and
// checks the human-mode lines on both sides: the cleared confirmation, then
// `wip next` reading as no-cursor afterward.
func TestNext_Clear_HumanThenNoCursor(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Clear me", "--json").stdout)

	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	cleared := runIn(t, dir, dbEnv, "next", "--clear")
	if cleared.exitCode != 0 {
		t.Fatalf("next --clear: exit=%d stderr=%q", cleared.exitCode, cleared.stderr)
	}
	if cleared.stdout != "cursor cleared — everything open\n" {
		t.Errorf("next --clear stdout = %q, want the cleared line", cleared.stdout)
	}

	after := runIn(t, dir, dbEnv, "next")
	if after.exitCode != 0 {
		t.Fatalf("next (after clear): exit=%d stderr=%q", after.exitCode, after.stderr)
	}
	if !strings.Contains(after.stdout, "no cursor set for this clone") {
		t.Errorf("next output after clear = %q, want no-cursor", after.stdout)
	}
}

// TestNext_ClearJSON_CarriesPreviousThenNoOps checks `--clear --json`'s
// payload shape, and that clearing an already-clear cursor is a no-op
// success with an empty (omitted) previous.
func TestNext_ClearJSON_CarriesPreviousThenNoOps(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Clear me", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	first := runIn(t, dir, dbEnv, "next", "--clear", "--json")
	if first.exitCode != 0 {
		t.Fatalf("next --clear --json: exit=%d stderr=%q", first.exitCode, first.stderr)
	}
	var payload struct {
		Cleared  bool   `json:"cleared"`
		Previous string `json:"previous"`
	}
	if err := json.Unmarshal([]byte(first.stdout), &payload); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, first.stdout)
	}
	if !payload.Cleared || payload.Previous != m.ID {
		t.Errorf("payload = %+v, want cleared=true previous=%s", payload, m.ID)
	}

	second := runIn(t, dir, dbEnv, "next", "--clear", "--json")
	if second.exitCode != 0 {
		t.Fatalf("next --clear --json (already clear): exit=%d stderr=%q", second.exitCode, second.stderr)
	}
	var payload2 struct {
		Cleared  bool   `json:"cleared"`
		Previous string `json:"previous"`
	}
	if err := json.Unmarshal([]byte(second.stdout), &payload2); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, second.stdout)
	}
	if !payload2.Cleared || payload2.Previous != "" {
		t.Errorf("payload (already clear) = %+v, want cleared=true previous=\"\"", payload2)
	}
}

// TestNext_SetAndClearTogether_Refuses is the mutual-exclusion check:
// `--set` and `--clear` are two different answers to "what's next", and
// giving both at once is a usage error, never a silent pick between them.
func TestNext_SetAndClearTogether_Refuses(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	r := runIn(t, dir, dbEnv, "next", "--set", "anything", "--clear")
	if r.exitCode != 2 {
		t.Fatalf("next --set --clear: exit=%d, want 2 (usage error); stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
}

// TestNext_ChooseNextJSON_SealedTarget checks the `choose-next` JSON kind
// end to end: reason, and (via the human closing line, checked separately
// below) that both `--set` and `--clear` are offered as the way out.
func TestNext_ChooseNextJSON_SealedTarget(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Sealed target", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	human := runIn(t, dir, dbEnv, "next")
	if human.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	if !strings.Contains(human.stdout, "choose what's next") {
		t.Errorf("next output = %q, want the choose-next header", human.stdout)
	}
	if !strings.Contains(human.stdout, "wip next --set") || !strings.Contains(human.stdout, "wip next --clear") {
		t.Errorf("next output = %q, want the closing line to mention both --set and --clear", human.stdout)
	}

	j := runIn(t, dir, dbEnv, "next", "--json")
	if j.exitCode != 0 {
		t.Fatalf("next --json: exit=%d stderr=%q", j.exitCode, j.stderr)
	}
	var payload struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(j.stdout), &payload); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, j.stdout)
	}
	if payload.Kind != "choose-next" || payload.Reason != "sealed" {
		t.Errorf("payload = %+v, want kind=choose-next reason=sealed", payload)
	}
}

// TestFinish_PrintsSuccessorHandoffAndJSONCarriesCursorEnded is the finish
// path's hand-off: cursor on step-01, a ready step-02 sibling exists, and
// finishing step-01 (which seals immediately — no gate is declared) both
// prints the successor line and carries `cursorEnded` in JSON mode.
func TestFinish_PrintsSuccessorHandoffAndJSONCarriesCursorEnded(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Successor hand-off", "--json").stdout)
	step1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "First", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "Second", "--json").stdout)

	if r := runIn(t, dir, dbEnv, "start", m.Locator+"/"+step1.Locator); r.exitCode != 0 {
		t.Fatalf("start step-01 (cascades the matter): exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator+"/"+step1.Locator); r.exitCode != 0 {
		t.Fatalf("next --set step-01: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	human := runIn(t, dir, dbEnv, "finish", m.Locator+"/"+step1.Locator)
	if human.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	if !strings.Contains(human.stdout, "step-02 is next in "+m.Locator) {
		t.Errorf("finish stdout = %q, want the step-02 successor hand-off", human.stdout)
	}
	if !strings.Contains(human.stdout, "wip next --set step-02") {
		t.Errorf("finish stdout = %q, want the --set step-02 suggestion", human.stdout)
	}

	// A second Matter for the JSON assertion — step1 above is already Done
	// and cannot be finished twice.
	m2 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Successor hand-off (JSON)", "--json").stdout)
	step2a := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m2.Locator, "--title", "First", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m2.Locator, "--title", "Second", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m2.Locator+"/"+step2a.Locator); r.exitCode != 0 {
		t.Fatalf("start step-01 (m2): exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m2.Locator+"/"+step2a.Locator); r.exitCode != 0 {
		t.Fatalf("next --set step-01 (m2): exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	j := runIn(t, dir, dbEnv, "finish", m2.Locator+"/"+step2a.Locator, "--json")
	if j.exitCode != 0 {
		t.Fatalf("finish --json: exit=%d stderr=%q", j.exitCode, j.stderr)
	}
	var payload struct {
		ID          string `json:"id"`
		Lifecycle   string `json:"lifecycle"`
		CursorEnded struct {
			Reason    string `json:"reason"`
			Target    string `json:"target"`
			Suggested string `json:"suggested"`
		} `json:"cursorEnded"`
	}
	if err := json.Unmarshal([]byte(j.stdout), &payload); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, j.stdout)
	}
	if payload.CursorEnded.Reason != "sealed" || payload.CursorEnded.Suggested != "step-02" {
		t.Errorf("cursorEnded = %+v, want reason=sealed suggested=step-02", payload.CursorEnded)
	}
	if payload.CursorEnded.Target != m2.Locator+" · "+step2a.Locator {
		t.Errorf("cursorEnded.target = %q, want %q", payload.CursorEnded.Target, m2.Locator+" · "+step2a.Locator)
	}
}

// TestGateClose_DescendantSealing_PrintsHandoff is the gate-close path's own
// case: a Matter-scale gate closes against the Matter — not the cursor's own
// target — but the close still seals the cursor's Done step underneath it
// (D62), and the hand-off fires because EndedCursor evaluates the target's
// own state fresh rather than comparing subjects.
func TestGateClose_DescendantSealing_PrintsHandoff(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Descendant sealing", "--json").stdout)
	step1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "First", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "Second", "--json").stdout)

	if r := runIn(t, dir, dbEnv, "start", m.Locator+"/"+step1.Locator); r.exitCode != 0 {
		t.Fatalf("start step-01: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator+"/"+step1.Locator); r.exitCode != 0 {
		t.Fatalf("next --set step-01: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", m.Locator+"/"+step1.Locator); r.exitCode != 0 {
		t.Fatalf("finish step-01: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// step-01 is Done but not yet sealed — the Matter's own gate is still
	// open — so this close is what actually ends the cursor's work.
	closed := runIn(t, dir, dbEnv, "gate", "close", "reviewed-local", m.Locator)
	if closed.exitCode != 0 {
		t.Fatalf("gate close: exit=%d stderr=%q", closed.exitCode, closed.stderr)
	}
	if !strings.Contains(closed.stdout, "step-02 is next in "+m.Locator) {
		t.Errorf("gate close stdout = %q, want the step-02 successor hand-off", closed.stdout)
	}
}

// TestCancel_PrintsGenericHandoff is the cancel path's own case: a canceled
// Matter has no successor to name (canceled is never a Step-sibling
// situation the way sealed is here), so the generic line is what prints.
func TestCancel_PrintsGenericHandoff(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Abandoned", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	cancel := runIn(t, dir, dbEnv, "cancel", m.Locator)
	if cancel.exitCode != 0 {
		t.Fatalf("cancel: exit=%d stderr=%q", cancel.exitCode, cancel.stderr)
	}
	want := "the cursor's work is done — wip next to choose what's next, or wip next --clear"
	if !strings.Contains(cancel.stdout, want) {
		t.Errorf("cancel stdout = %q, want it to contain %q", cancel.stdout, want)
	}
}
