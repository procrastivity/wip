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
	for _, want := range []string{"in progress:", "pair-matter · step-02", "next to start:", "pair-matter · step-03", "finished", "blocked", "wip status --full"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status = %q, want it to contain %q", r.stdout, want)
		}
	}
	for _, absent := range []string{"Clone · current", "· in-progress", "· planned", "· sealed", "finished:\n", "blocked-by"} {
		if strings.Contains(r.stdout, absent) {
			t.Errorf("status = %q, want %q absent from the minimal render", r.stdout, absent)
		}
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
