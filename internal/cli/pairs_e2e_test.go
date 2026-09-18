// End-to-end coverage of D112's verb pairs (plumbing-namespace step-03):
// the `next` alias pair's golden sameness — one implementation registered
// twice, byte-identical output under both spellings — and the `status`
// split pair's human-sized porcelain over the unchanged agent-grade
// plumbing member. binPath, run, runIn come from e2e_test.go /
// tiers_e2e_test.go; setupRepo, mustJSON, nodePayload from
// writesurface_birth_test.go (same package).
package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Generated node IDs are data, not contract — the golden sameness
// comparisons mask them so two independently seeded fixtures can differ
// in IDs while proving identical output.
var nodeIDPattern = regexp.MustCompile(`\b01[0-9A-HJKMNP-TV-Z]{24}\b`)

func maskIDs(s string) string {
	return nodeIDPattern.ReplaceAllString(s, "<id>")
}

// seedStatusFixture builds the four-section repo the status assertions
// need: pair-matter holds a finished step (step-01), an in-progress step
// (step-02, its cascade having pulled the matter to In Progress), and a
// planned step (step-03, the unblocked frontier); blocked-matter waits on
// pair-matter. Locators derive from titles, so every fixture built this
// way is identical — the property the golden comparisons rely on.
func seedStatusFixture(t *testing.T) (dir string, dbEnv []string) {
	t.Helper()
	dir, dbEnv = setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Pair matter", "--json").stdout)
	s1 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "First", "--json").stdout)
	s2 := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Second", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Third", "--json").stdout)
	for _, args := range [][]string{
		{"plumbing", "start", m.Locator + "/" + s1.Locator},
		{"plumbing", "finish", m.Locator + "/" + s1.Locator},
		{"plumbing", "start", m.Locator + "/" + s2.Locator},
	} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	blocked := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Blocked matter", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", blocked.Locator, "--blocked-by", m.Locator); r.exitCode != 0 {
		t.Fatalf("depend add: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	return dir, dbEnv
}

// TestPair_NextGoldenSameness is D112's alias-pair contract made literal:
// `wip next` and `wip plumbing next` are one implementation registered
// twice, so every invocation — read or write, human or --json — produces
// byte-identical stdout, stderr, and exit code. Write invocations (--set,
// --clear) run each spelling against its own identically-seeded fixture so
// no state leaks between the two.
func TestPair_NextGoldenSameness(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		withCursor bool // seed the cursor (via the plumbing spelling) first
	}{
		{"bare", []string{"next"}, false},
		{"json", []string{"next", "--json"}, false},
		{"set", []string{"next", "--set", "pair-matter/step-02"}, false},
		{"set json", []string{"next", "--set", "pair-matter/step-02", "--json"}, false},
		{"clear", []string{"next", "--clear"}, true},
		{"clear json", []string{"next", "--clear", "--json"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := make([]result, 2)
			for i, prefix := range [][]string{nil, {"plumbing"}} {
				dir, dbEnv := seedStatusFixture(t)
				if tc.withCursor {
					if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", "pair-matter/step-02"); r.exitCode != 0 {
						t.Fatalf("seeding cursor: exit=%d stderr=%q", r.exitCode, r.stderr)
					}
				}
				args := append(append([]string{}, prefix...), tc.args...)
				results[i] = runIn(t, dir, dbEnv, args...)
			}
			porcelain, plumbing := results[0], results[1]
			if porcelain.exitCode != plumbing.exitCode {
				t.Fatalf("exit codes differ: %d vs %d (stderr %q vs %q)",
					porcelain.exitCode, plumbing.exitCode, porcelain.stderr, plumbing.stderr)
			}
			if maskIDs(porcelain.stdout) != maskIDs(plumbing.stdout) {
				t.Errorf("stdout differs:\nwip next → %q\nwip plumbing next → %q", porcelain.stdout, plumbing.stdout)
			}
			if maskIDs(porcelain.stderr) != maskIDs(plumbing.stderr) {
				t.Errorf("stderr differs:\nwip next → %q\nwip plumbing next → %q", porcelain.stderr, plumbing.stderr)
			}
		})
	}
}

func TestPair_NextIdleReusesWaitingSummaryWithoutChangingJSONShape(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Awaiting review", "--json").stdout)
	for _, verb := range []string{"start", "finish"} {
		if r := runIn(t, dir, dbEnv, "plumbing", verb, m.Locator); r.exitCode != 0 {
			t.Fatalf("%s: exit=%d stderr=%q", verb, r.exitCode, r.stderr)
		}
	}

	porcelain := runIn(t, dir, dbEnv, "next")
	plumbing := runIn(t, dir, dbEnv, "plumbing", "next")
	if porcelain.exitCode != 0 || plumbing.exitCode != 0 {
		t.Fatalf("next exits = %d/%d, stderr=%q/%q", porcelain.exitCode, plumbing.exitCode, porcelain.stderr, plumbing.stderr)
	}
	if porcelain.stdout != plumbing.stdout {
		t.Fatalf("next aliases differ:\n%s\n%s", porcelain.stdout, plumbing.stdout)
	}
	want := "nothing unblocked\n" +
		"waiting:\n" +
		"  awaiting-review\n" +
		"    gate reviewed-local · owner human · open\n" +
		"run `wip status` for the full working set\n"
	if porcelain.stdout != want {
		t.Errorf("next = %q, want %q", porcelain.stdout, want)
	}

	nextJSON := runIn(t, dir, dbEnv, "next", "--json")
	if nextJSON.exitCode != 0 {
		t.Fatalf("next --json: exit=%d stderr=%q", nextJSON.exitCode, nextJSON.stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(nextJSON.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != "nothing-unblocked" {
		t.Errorf("next JSON = %v, want the existing nothing-unblocked kind", payload)
	}
	if _, exists := payload["waiting"]; exists {
		t.Errorf("next JSON = %v, want no new waiting field", payload)
	}
}

func TestPair_NextEndedCursorIncludesWaitingSummaryWhenIdle(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	ended := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Ended", "--json").stdout)
	waiting := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Waiting", "--json").stdout)
	for _, matter := range []nodePayload{ended, waiting} {
		if r := runIn(t, dir, dbEnv, "plumbing", "start", matter.Locator); r.exitCode != 0 {
			t.Fatalf("start %s: exit=%d stderr=%q", matter.Locator, r.exitCode, r.stderr)
		}
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", ended.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, args := range [][]string{
		{"plumbing", "finish", ended.Locator},
		{"plumbing", "gate", "close", "reviewed-local", ended.Locator},
		{"plumbing", "finish", waiting.Locator},
	} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	r := runIn(t, dir, dbEnv, "next")
	if r.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{
		"ended is sealed — choose what's next",
		"  nothing unblocked",
		"waiting:\n  waiting\n    gate reviewed-local · owner human · open",
		"wip next --set <locator>",
		"wip next --clear",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("next = %q, want %q", r.stdout, want)
		}
	}
}

func TestPair_NextFailureSameness(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
		fixture    bool
	}{
		{
			name:       "extra positional argument",
			args:       []string{"next", "extra"},
			wantExit:   2,
			wantStderr: "wip: unknown command \"extra\" for \"wip plumbing next\"\n",
		},
		{
			name:       "unknown locator",
			args:       []string{"next", "--set", "missing"},
			wantExit:   1,
			wantStderr: "wip: plumbing next: no matter labeled \"missing\"\n",
			fixture:    true,
		},
		{
			name:       "mutually exclusive flags",
			args:       []string{"next", "--set", "missing", "--clear"},
			wantExit:   2,
			wantStderr: "wip: if any flags in the group [set clear] are set none of the others can be; [clear set] were all set\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := make([]result, 2)
			for i, prefix := range [][]string{nil, {"plumbing"}} {
				var dir string
				var dbEnv []string
				if tc.fixture {
					dir, dbEnv = seedStatusFixture(t)
				}
				args := append(append([]string{}, prefix...), tc.args...)
				if tc.fixture {
					results[i] = runIn(t, dir, dbEnv, args...)
				} else {
					results[i] = run(t, nil, args...)
				}
			}

			porcelain, plumbing := results[0], results[1]
			for _, r := range results {
				if r.exitCode != tc.wantExit {
					t.Errorf("exit code = %d, want %d; stdout=%q stderr=%q", r.exitCode, tc.wantExit, r.stdout, r.stderr)
				}
				if r.stdout != "" {
					t.Errorf("stdout = %q, want empty on failure", r.stdout)
				}
				if r.stderr != tc.wantStderr {
					t.Errorf("stderr = %q, want %q", r.stderr, tc.wantStderr)
				}
			}
			if porcelain.exitCode != plumbing.exitCode || porcelain.stdout != plumbing.stdout || porcelain.stderr != plumbing.stderr {
				t.Errorf("failure outputs differ:\nwip next → %#v\nwip plumbing next → %#v", porcelain, plumbing)
			}
		})
	}
}

// TestPair_NextHelpDiffersOnlyInCommandPath pins the one place the two
// spellings may legitimately diverge: the usage line's own command path.
func TestPair_NextHelpDiffersOnlyInCommandPath(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	porcelain := runIn(t, dir, dbEnv, "next", "--help")
	plumbing := runIn(t, dir, dbEnv, "plumbing", "next", "--help")
	if porcelain.exitCode != 0 || plumbing.exitCode != 0 {
		t.Fatalf("help exits = %d/%d (stderr %q / %q)", porcelain.exitCode, plumbing.exitCode, porcelain.stderr, plumbing.stderr)
	}
	normalized := strings.ReplaceAll(plumbing.stdout, "wip plumbing next", "wip next")
	if normalized != porcelain.stdout {
		t.Errorf("help output differs beyond the command path:\nwip next --help → %q\nwip plumbing next --help → %q",
			porcelain.stdout, plumbing.stdout)
	}
}

// TestPair_PorcelainStatusIsTheMinimalWorkingSet pins the split pair's
// human face: in progress + next to start, a counts footer for the elided
// sections, and none of the plumbing member's tier internals or
// section-redundant lifecycle words.
func TestPair_PorcelainStatusIsTheMinimalWorkingSet(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)

	r := runIn(t, dir, dbEnv, "status")
	if r.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{"in progress:", "pair-matter · step-02", "next to start:", "pair-matter · step-03", "waiting:", "blocked-matter", "blocked by pair-matter", "finished", "wip status --full"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status = %q, want it to contain %q", r.stdout, want)
		}
	}
	for _, absent := range []string{"Clone · current", "· in-progress", "· planned", "· sealed", "finished:\n", "blocked:\n"} {
		if strings.Contains(r.stdout, absent) {
			t.Errorf("status = %q, want %q absent from the minimal render", r.stdout, absent)
		}
	}
}

func TestPair_PorcelainStatusSuppressesReadyPlanWaitsButDetailedStatusKeepsThem(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	gated := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Gated", "--json").stdout)
	gatedFirst := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", gated.Locator, "--title", "First", "--json").stdout)
	gatedSecond := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", gated.Locator, "--title", "Second", "--json").stdout)
	for _, step := range []nodePayload{gatedFirst, gatedSecond} {
		for _, verb := range []string{"start", "finish"} {
			if r := runIn(t, dir, dbEnv, "plumbing", verb, gated.Locator+"/"+step.Locator); r.exitCode != 0 {
				t.Fatalf("%s %s: exit=%d stderr=%q", verb, step.Locator, r.exitCode, r.stderr)
			}
		}
	}

	held := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Held", "--json").stdout)
	heldFirst := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", held.Locator, "--title", "First held", "--json").stdout)
	heldSecond := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", held.Locator, "--title", "Second held", "--json").stdout)
	blockers := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Blockers", "--json").stdout)
	blocker := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", blockers.Locator, "--title", "External blocker", "--json").stdout)
	for _, blocked := range []nodePayload{heldFirst, heldSecond} {
		if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", held.Locator+"/"+blocked.Locator, "--blocked-by", blockers.Locator+"/"+blocker.Locator); r.exitCode != 0 {
			t.Fatalf("depend add %s: exit=%d stderr=%q", blocked.Locator, r.exitCode, r.stderr)
		}
	}

	status := runIn(t, dir, dbEnv, "status")
	if status.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", status.exitCode, status.stderr)
	}
	if strings.Contains(status.stdout, "waiting:\n") {
		t.Errorf("status = %q, want ready-plan dependencies and the active Matter's inherited gate suppressed", status.stdout)
	}

	for _, args := range [][]string{{"status", "--full"}, {"plumbing", "status"}} {
		r := runIn(t, dir, dbEnv, args...)
		if r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
		for _, want := range []string{"blocked:\n", "held · step-01", "blocked-by: blockers · step-01"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("%v = %q, want detailed dependency %q retained", args, r.stdout, want)
			}
		}
	}
}

func TestPair_PorcelainStatusBoundsConciseActionableWaits(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	review := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Review", "--json").stdout)
	for _, verb := range []string{"start", "finish"} {
		if r := runIn(t, dir, dbEnv, "plumbing", verb, review.Locator); r.exitCode != 0 {
			t.Fatalf("%s review: exit=%d stderr=%q", verb, r.exitCode, r.stderr)
		}
	}

	external := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "External", "--json").stdout)
	stalled := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Stalled", "--json").stdout)
	stage := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "stage", "create", stalled.Locator, "--title", "Build", "--json").stdout)
	for _, title := range []string{"Blocked one", "Blocked two", "Blocked three", "Blocked four"} {
		step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", stalled.Locator+"/"+stage.Locator, "--title", title, "--json").stdout)
		if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", stalled.Locator+"/"+step.Locator, "--blocked-by", external.Locator); r.exitCode != 0 {
			t.Fatalf("depend add %s: exit=%d stderr=%q", step.Locator, r.exitCode, r.stderr)
		}
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "start", stalled.Locator+"/"+stage.Locator); r.exitCode != 0 {
		t.Fatalf("start stage: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	status := runIn(t, dir, dbEnv, "status")
	if status.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", status.exitCode, status.stderr)
	}
	want := "waiting:\n" +
		"  review\n" +
		"    gate reviewed-local · owner human · open\n" +
		"  stalled\n" +
		"    “Blocked one” waits for external\n" +
		"    “Blocked two” waits for external\n" +
		"    “Blocked three” waits for external\n" +
		"    … 1 more — wip status --full\n"
	if !strings.Contains(status.stdout, want) {
		t.Errorf("status = %q, want bounded actionable summary %q", status.stdout, want)
	}

	statusJSON := runIn(t, dir, dbEnv, "status", "--json")
	plumbingJSON := runIn(t, dir, dbEnv, "plumbing", "status", "--json")
	if statusJSON.exitCode != 0 || plumbingJSON.exitCode != 0 {
		t.Fatalf("status JSON exits = %d/%d, stderr=%q/%q", statusJSON.exitCode, plumbingJSON.exitCode, statusJSON.stderr, plumbingJSON.stderr)
	}
	if statusJSON.stdout != plumbingJSON.stdout {
		t.Fatalf("status JSON aliases differ:\n%s\n%s", statusJSON.stdout, plumbingJSON.stdout)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(statusJSON.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	repo, ok := payload["repo"].(map[string]any)
	if !ok {
		t.Fatalf("status JSON = %v, want a repo object", payload)
	}
	content, ok := repo["content"].(map[string]any)
	if !ok {
		t.Fatalf("status JSON repo = %v, want a content object", repo)
	}
	if _, exists := content["waiting"]; exists {
		t.Errorf("status JSON content = %v, want no new waiting field", content)
	}
}

func TestPair_PorcelainStatusKeepsBlockedCursorVisible(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)
	if r := runIn(t, dir, dbEnv, "next", "--set", "blocked-matter"); r.exitCode != 0 {
		t.Fatalf("next --set blocked-matter: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "status")
	if r.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{"cursor:\n", "blocked-matter", "blocked-by: pair-matter", "· cursor"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status = %q, want it to contain %q", r.stdout, want)
		}
	}
	if strings.Contains(r.stdout, "blocked:\n") {
		t.Errorf("status = %q, want unrelated blocked rows collapsed", r.stdout)
	}
}

func TestPair_PorcelainStatusKeepsFinishedCursorVisible(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)
	if r := runIn(t, dir, dbEnv, "next", "--set", "pair-matter/step-01"); r.exitCode != 0 {
		t.Fatalf("next --set pair-matter/step-01: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "status")
	if r.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{"cursor:\n", "pair-matter · step-01", "· sealed", "· cursor"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status = %q, want it to contain %q", r.stdout, want)
		}
	}
	if strings.Contains(r.stdout, "finished:\n") {
		t.Errorf("status = %q, want unrelated finished rows collapsed", r.stdout)
	}
}

// TestPair_PorcelainStatusFullRendersEverySection covers `--full`: all
// four sections minus the clone/worktree enumeration — finished keeps its
// sealed/locally-complete words, blocked keeps its blocked-by list.
func TestPair_PorcelainStatusFullRendersEverySection(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)

	r := runIn(t, dir, dbEnv, "status", "--full")
	if r.exitCode != 0 {
		t.Fatalf("status --full: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{"in progress:", "finished:", "· sealed", "next to start:", "blocked:", "blocked-by: pair-matter"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status --full = %q, want it to contain %q", r.stdout, want)
		}
	}
	for _, absent := range []string{"Clone · current", "· in-progress", "· planned"} {
		if strings.Contains(r.stdout, absent) {
			t.Errorf("status --full = %q, want %q absent", r.stdout, absent)
		}
	}

	// The plumbing member's render is the unchanged full contract: clone
	// enumeration and lifecycle words intact.
	plumb := runIn(t, dir, dbEnv, "plumbing", "status")
	if plumb.exitCode != 0 {
		t.Fatalf("plumbing status: exit=%d stderr=%q", plumb.exitCode, plumb.stderr)
	}
	for _, want := range []string{"Clone · current", "· in-progress", "· planned", "· sealed"} {
		if !strings.Contains(plumb.stdout, want) {
			t.Errorf("plumbing status = %q, want the unchanged agent-grade render to contain %q", plumb.stdout, want)
		}
	}
}

// TestPair_StatusJSONIsOneContract: the pair splits only on the human
// face — `wip status --json` emits the plumbing member's payload
// byte-for-byte.
func TestPair_StatusJSONIsOneContract(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)
	porcelain := runIn(t, dir, dbEnv, "status", "--json")
	plumbing := runIn(t, dir, dbEnv, "plumbing", "status", "--json")
	if porcelain.exitCode != 0 || plumbing.exitCode != 0 {
		t.Fatalf("exits = %d/%d (stderr %q / %q)", porcelain.exitCode, plumbing.exitCode, porcelain.stderr, plumbing.stderr)
	}
	if porcelain.stdout != plumbing.stdout {
		t.Errorf("--json payloads differ:\nwip status → %s\nwip plumbing status → %s", porcelain.stdout, plumbing.stdout)
	}
}

func TestOrientationSurfacesNamePendingGates(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Checkout", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Build", "--json").stdout)
	for _, args := range [][]string{
		{"plumbing", "gate", "declare", "approved", "--scale", "matter"},
		{"plumbing", "gate", "declare", "verified", "--scale", "step"},
		{"plumbing", "start", m.Locator},
		{"plumbing", "start", m.Locator + "/" + step.Locator},
		{"plumbing", "finish", m.Locator + "/" + step.Locator},
		{"plumbing", "next", "--set", m.Locator + "/" + step.Locator},
	} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	status := runIn(t, dir, dbEnv, "plumbing", "status", "--all")
	if status.exitCode != 0 {
		t.Fatalf("status --all: exit=%d stderr=%q", status.exitCode, status.stderr)
	}
	wantPending := "pending gates: verified (own on " + m.Locator + " · " + step.Locator + "), approved (enclosing on " + m.Locator + ")"
	if !strings.Contains(status.stdout, wantPending) {
		t.Errorf("status = %q, want %q", status.stdout, wantPending)
	}

	next := runIn(t, dir, dbEnv, "next")
	if next.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", next.exitCode, next.stderr)
	}
	if !strings.Contains(next.stdout, wantPending) {
		t.Errorf("next = %q, want %q", next.stdout, wantPending)
	}
	nextJSON := runIn(t, dir, dbEnv, "next", "--json")
	var nextPayload struct {
		Pending []struct {
			Name         string `json:"name"`
			Scale        string `json:"scale"`
			Relationship string `json:"relationship"`
			Subject      string `json:"subject"`
		} `json:"pendingGates"`
	}
	if err := json.Unmarshal([]byte(nextJSON.stdout), &nextPayload); err != nil {
		t.Fatal(err)
	}
	if len(nextPayload.Pending) != 2 || nextPayload.Pending[0].Name != "verified" ||
		nextPayload.Pending[0].Scale != "step" || nextPayload.Pending[0].Subject == "" ||
		nextPayload.Pending[1].Relationship != "enclosing" {
		t.Errorf("next pending JSON = %+v", nextPayload.Pending)
	}

	statusJSON := runIn(t, dir, dbEnv, "status", "--json")
	plumbingJSON := runIn(t, dir, dbEnv, "plumbing", "status", "--json")
	if statusJSON.stdout != plumbingJSON.stdout {
		t.Fatalf("status JSON aliases differ:\n%s\n%s", statusJSON.stdout, plumbingJSON.stdout)
	}
	var statusPayload struct {
		Repo struct {
			Content struct {
				Finished []struct {
					Pending []struct {
						Name         string `json:"name"`
						Relationship string `json:"relationship"`
					} `json:"pendingGates"`
				} `json:"finished"`
			} `json:"content"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(statusJSON.stdout), &statusPayload); err != nil {
		t.Fatal(err)
	}
	if len(statusPayload.Repo.Content.Finished) != 1 || len(statusPayload.Repo.Content.Finished[0].Pending) != 2 {
		t.Fatalf("status pending JSON = %+v", statusPayload.Repo.Content.Finished)
	}
	if statusPayload.Repo.Content.Finished[0].Pending[0].Name != "verified" ||
		statusPayload.Repo.Content.Finished[0].Pending[1].Relationship != "enclosing" {
		t.Errorf("status pending JSON = %+v", statusPayload.Repo.Content.Finished[0].Pending)
	}
}

func TestStage4_StatusPendingGateMatrixAndCursorException(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Checkout", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Build", "--json").stdout)
	sealed := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Sealed", "--json").stdout)
	for _, args := range [][]string{
		{"plumbing", "gate", "declare", "approved", "--scale", "matter"},
		{"plumbing", "gate", "declare", "verified", "--scale", "step"},
		{"plumbing", "start", m.Locator},
		{"plumbing", "start", m.Locator + "/" + step.Locator},
		{"plumbing", "finish", m.Locator + "/" + step.Locator},
		{"plumbing", "start", sealed.Locator},
		{"plumbing", "finish", sealed.Locator},
		{"plumbing", "gate", "close", "approved", sealed.Locator},
		{"plumbing", "next", "--set", m.Locator + "/" + step.Locator},
	} {
		if r := runIn(t, dir, dbEnv, args...); r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
	}

	wantPending := "pending gates: verified (own on " + m.Locator + " · " + step.Locator + "), approved (enclosing on " + m.Locator + ")"
	for _, args := range [][]string{
		{"plumbing", "status", "--all"},
		{"status", "--full"},
		{"status"},
	} {
		r := runIn(t, dir, dbEnv, args...)
		if r.exitCode != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
		if !strings.Contains(r.stdout, wantPending) {
			t.Errorf("%v = %q, want exact pending continuation %q", args, r.stdout, wantPending)
		}
	}
	if status := runIn(t, dir, dbEnv, "plumbing", "status", "--all"); !strings.Contains(status.stdout, "· awaiting gate · cursor\n    pending gates: "+strings.TrimPrefix(wantPending, "pending gates: ")) {
		t.Errorf("status --all = %q, want an awaiting-gate row followed by its pending continuation", status.stdout)
	}

	statusJSON := runIn(t, dir, dbEnv, "status", "--json")
	plumbingJSON := runIn(t, dir, dbEnv, "plumbing", "status", "--json")
	if statusJSON.stdout != plumbingJSON.stdout {
		t.Fatalf("status JSON aliases differ:\n%s\n%s", statusJSON.stdout, plumbingJSON.stdout)
	}
	var payload struct {
		Repo struct {
			Content struct {
				Finished []struct {
					Address      string `json:"address"`
					Sealed       bool   `json:"sealed"`
					PendingGates []struct {
						Name         string `json:"name"`
						Scale        string `json:"scale"`
						Relationship string `json:"relationship"`
						Subject      string `json:"subject"`
					} `json:"pendingGates"`
				} `json:"finished"`
			} `json:"content"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(statusJSON.stdout), &payload); err != nil {
		t.Fatal(err)
	}
	var foundPending, foundSealed bool
	for _, finished := range payload.Repo.Content.Finished {
		switch finished.Address {
		case m.Locator + " · " + step.Locator:
			foundPending = len(finished.PendingGates) == 2 &&
				finished.PendingGates[0].Name == "verified" &&
				finished.PendingGates[0].Scale == "step" &&
				finished.PendingGates[0].Relationship == "own" &&
				finished.PendingGates[0].Subject != ""
		case sealed.Locator:
			foundSealed = finished.Sealed && finished.PendingGates != nil && len(finished.PendingGates) == 0
		}
	}
	if !foundPending || !foundSealed {
		t.Fatalf("finished JSON = %+v, want pending and explicit empty sealed pendingGates", payload.Repo.Content.Finished)
	}
}

// TestPair_PorcelainStatusHasNoAllFlag: audit expansion is the plumbing
// member's knob — `wip status --all` fails Cobra's unknown-flag path.
func TestPair_PorcelainStatusHasNoAllFlag(t *testing.T) {
	dir, dbEnv := seedStatusFixture(t)
	r := runIn(t, dir, dbEnv, "status", "--all")
	if r.exitCode != 2 {
		t.Fatalf("status --all: exit=%d, want 2; stdout=%q stderr=%q", r.exitCode, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "unknown flag") {
		t.Errorf("stderr = %q, want Cobra's unknown-flag error", r.stderr)
	}
}

// TestPair_ManifestRecordsAliasOf checks the manifest's record of both
// pairs: `next` carries alias-of pointing at the canonical member, the
// plumbing member does not, and the split pair's two same-named members
// appear with no alias link.
func TestPair_ManifestRecordsAliasOf(t *testing.T) {
	r := run(t, nil, "manifest", "--json")
	if r.exitCode != 0 {
		t.Fatalf("manifest: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	var m struct {
		Verbs []struct {
			Name    string `json:"name"`
			AliasOf string `json:"alias-of"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("manifest JSON: %v (stdout=%q)", err, r.stdout)
	}
	byName := map[string]string{}
	for _, v := range m.Verbs {
		byName[v.Name] = v.AliasOf
	}
	if byName["next"] != "plumbing next" {
		t.Errorf("next alias-of = %q, want %q", byName["next"], "plumbing next")
	}
	if _, ok := byName["plumbing next"]; !ok || byName["plumbing next"] != "" {
		t.Errorf("plumbing next entry = %q (present %v), want present with no alias-of", byName["plumbing next"], ok)
	}
	if _, ok := byName["plumbing status"]; !ok {
		t.Error("manifest lacks plumbing status — the split pair's canonical member")
	}
	if byName["status"] != "" {
		t.Errorf("status alias-of = %q, want empty — a split pair is a different deliverable, not an alias", byName["status"])
	}
}

// TestPair_SkillTablesProjectPlumbingMembers: a real install renders the
// regenerated verb table — the pair members appear at their plumbing
// spellings and the porcelain spellings appear nowhere as table rows.
func TestPair_SkillTablesProjectPlumbingMembers(t *testing.T) {
	skillsDir := t.TempDir()
	env := []string{"WIP_CLAUDE_SKILLS_DIR=" + skillsDir}
	installed := run(t, env, "install", "claude-code", "--json")
	if installed.exitCode != 0 {
		t.Fatalf("install claude-code: exit=%d stderr=%q", installed.exitCode, installed.stderr)
	}
	skill, err := os.ReadFile(filepath.Join(skillsDir, "wip", "SKILL.md"))
	if err != nil {
		t.Fatalf("read generated SKILL.md: %v", err)
	}
	body := string(skill)
	for _, want := range []string{"`wip plumbing status`", "`wip plumbing next`"} {
		if !strings.Contains(body, want) {
			t.Errorf("SKILL.md lacks the projected row %s", want)
		}
	}
	for _, absent := range []string{"`wip status`", "`wip next`"} {
		if strings.Contains(body, absent) {
			t.Errorf("SKILL.md names the porcelain spelling %s — agents get the plumbing member", absent)
		}
	}
}
