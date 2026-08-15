// End-to-end tests for `wip status`'s finished-section collapsing and `--all`
// escape hatch, plus `wip cancel --reason`. binPath, run, runIn, gitIn,
// newGitRepo come from e2e_test.go / tiers_e2e_test.go; setupRepo,
// mustJSON, nodePayload, openTestStore come from writesurface_birth_test.go
// (same package). Recency (the 14-day hide-old-sealed-matter window) is
// exercised at the readsurface unit level with an injected Now
// (internal/readsurface/status_test.go) — time manipulation is not
// practical through the real binary, so this file covers collapsing, `--all`,
// the JSON field, and cancel's optional reason instead.
package cli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestStatus_CollapsesSealedSubtreeAndAllRestoresIt: a Matter with a sealed
// Step child collapses to the Matter's own row by default, and `--all`
// expands it back out.
func TestStatus_CollapsesSealedSubtreeAndAllRestoresIt(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Sealed matter", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "Sealed step", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m.ID); r.exitCode != 0 {
		t.Fatalf("start matter: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "start", step.ID); r.exitCode != 0 {
		t.Fatalf("start step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", step.ID); r.exitCode != 0 {
		t.Fatalf("finish step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", m.ID); r.exitCode != 0 {
		t.Fatalf("finish matter: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	def := runIn(t, dir, dbEnv, "status")
	if def.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", def.exitCode, def.stderr)
	}
	if strings.Contains(def.stdout, step.Locator) {
		t.Errorf("default status = %q, want the sealed step collapsed into its sealed matter", def.stdout)
	}
	if !strings.Contains(def.stdout, m.Locator) {
		t.Errorf("default status = %q, want the sealed matter %s present", def.stdout, m.Locator)
	}

	all := runIn(t, dir, dbEnv, "status", "--all")
	if all.exitCode != 0 {
		t.Fatalf("status --all: exit=%d stderr=%q", all.exitCode, all.stderr)
	}
	if !strings.Contains(all.stdout, step.Locator) {
		t.Errorf("status --all = %q, want the sealed step expanded back out", all.stdout)
	}
	if !strings.Contains(all.stdout, m.Locator) {
		t.Errorf("status --all = %q, want the sealed matter also present", all.stdout)
	}
}

// TestStatus_JSONCarriesHiddenSealedMatters confirms the JSON field exists
// and is zero when nothing is hidden (a matter sealed moments ago, well
// inside the recency window).
func TestStatus_JSONCarriesHiddenSealedMatters(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Sealed matter", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "start", m.ID); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", m.ID); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	out := runIn(t, dir, dbEnv, "status", "--json")
	if out.exitCode != 0 {
		t.Fatalf("status --json: exit=%d stderr=%q", out.exitCode, out.stderr)
	}
	var payload struct {
		Repo struct {
			Content struct {
				HiddenSealedMatters int `json:"hiddenSealedMatters"`
				Finished            []struct {
					Address string `json:"address"`
				} `json:"finished"`
			} `json:"content"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(out.stdout), &payload); err != nil {
		t.Fatalf("status --json not JSON: %v (raw=%q)", err, out.stdout)
	}
	if payload.Repo.Content.HiddenSealedMatters != 0 {
		t.Errorf("hiddenSealedMatters = %d, want 0 (recently sealed)", payload.Repo.Content.HiddenSealedMatters)
	}
	var found bool
	for _, f := range payload.Repo.Content.Finished {
		if f.Address == m.Locator {
			found = true
		}
	}
	if !found {
		t.Errorf("finished = %+v, want %s present", payload.Repo.Content.Finished, m.Locator)
	}
}

// TestCancel_ReasonLandsInThePayload: `wip cancel <locator> --reason "..."`
// records the reason in the canceled event's payload; a reasonless cancel
// carries no "reason" key at all (omitempty keeps the old shape byte-
// identical).
func TestCancel_ReasonLandsInThePayload(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Origin matter", "--json").stdout)
	withReason := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "Abandoned with a reason", "--json").stdout)
	noReason := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "Abandoned without one", "--json").stdout)

	for _, id := range []string{withReason.ID, noReason.ID} {
		if r := runIn(t, dir, dbEnv, "start", id); r.exitCode != 0 {
			t.Fatalf("start %s: exit=%d stderr=%q", id, r.exitCode, r.stderr)
		}
	}

	s := openTestStore(t, dbPath)
	ctx := context.Background()
	beforeWith, err := s.EventsOfSubject(ctx, withReason.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeNo, err := s.EventsOfSubject(ctx, noReason.ID)
	if err != nil {
		t.Fatal(err)
	}

	if r := runIn(t, dir, dbEnv, "cancel", withReason.ID, "--reason", "obsolete"); r.exitCode != 0 {
		t.Fatalf("cancel --reason: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "cancel", noReason.ID); r.exitCode != 0 {
		t.Fatalf("cancel (no reason): exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s = openTestStore(t, dbPath)

	withEv := wantAppendedEvent(t, s, withReason.ID, len(beforeWith), store.TypeStepCanceled)
	if !strings.Contains(string(withEv.Payload), `"reason":"obsolete"`) {
		t.Errorf("payload = %s, want it to contain \"reason\":\"obsolete\"", withEv.Payload)
	}

	noEv := wantAppendedEvent(t, s, noReason.ID, len(beforeNo), store.TypeStepCanceled)
	if strings.Contains(string(noEv.Payload), `"reason"`) {
		t.Errorf("payload = %s, want no \"reason\" key at all for a reasonless cancel", noEv.Payload)
	}

	n, err := s.Node(ctx, withReason.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Lifecycle != store.Canceled {
		t.Errorf("lifecycle = %q, want canceled", n.Lifecycle)
	}
}
