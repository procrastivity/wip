// End-to-end coverage of D112's command-tree move: the porcelain list bare
// `wip --help` shows, the `wip plumbing` substrate list, the
// validation.moved-verb flat-invocation seam (root.Args + root RunE in
// internal/cli/root.go), and confirmation that a genuinely unknown command
// is unaffected.
package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// porcelainVerbs is D112's own list, in the registration order root.go
// declares (status, next, init, doctor, install, uninstall, version,
// plumbing, manifest last).
var porcelainVerbs = []string{"status", "next", "init", "doctor", "install", "uninstall", "version", "plumbing", "manifest"}

// movedGroups is the workplan's 28-group moved set (step-02 workplan, "The
// moved set — the exact list").
var movedGroups = []string{
	"backlog", "batch", "bind", "body", "brief", "cancel", "clean", "clone",
	"depend", "dispatch", "finding", "finish", "gate", "label", "matter",
	"outbox", "pause", "rebind", "refresh", "resume", "role", "run",
	"session", "stage", "start", "step", "unbind", "workplan",
}

func TestPlumbingNamespace_BareHelpListsPorcelainInD112Order(t *testing.T) {
	r := run(t, nil, "--help")
	if r.exitCode != 0 {
		t.Fatalf("--help: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	lastIdx := -1
	for _, verb := range porcelainVerbs {
		idx := strings.Index(r.stdout, "\n  "+verb+" ")
		if idx == -1 {
			t.Fatalf("bare --help does not list %q as a top-level entry:\n%s", verb, r.stdout)
		}
		if idx <= lastIdx {
			t.Fatalf("%q does not appear after the previous porcelain entry (D112 order):\n%s", verb, r.stdout)
		}
		lastIdx = idx
	}

	for _, moved := range movedGroups {
		if strings.Contains(r.stdout, "\n  "+moved+" ") {
			t.Errorf("bare --help lists moved verb %q as a top-level entry, want it absent:\n%s", moved, r.stdout)
		}
	}
}

func TestPlumbingNamespace_BarePlumbingListsSubstrateAndExitsZero(t *testing.T) {
	r := run(t, nil, "plumbing")
	if r.exitCode != 0 {
		t.Fatalf("bare plumbing: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, moved := range movedGroups {
		if !strings.Contains(r.stdout, "\n  "+moved+" ") {
			t.Errorf("bare plumbing does not list %q:\n%s", moved, r.stdout)
		}
	}
	for _, porcelain := range []string{"status", "next", "init", "doctor", "install", "uninstall", "version", "manifest"} {
		if strings.Contains(r.stdout, "\n  "+porcelain+" ") {
			t.Errorf("bare plumbing lists porcelain verb %q, want it absent:\n%s", porcelain, r.stdout)
		}
	}
}

func TestPlumbingNamespace_FlatInvocationFailsWithMovedVerb(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		names string // the qualified path the message must name
	}{
		{"leaf group, no args", []string{"workplan"}, "wip plumbing workplan"},
		{"noun group, no args", []string{"step"}, "wip plumbing step"},
		{"full flat path", []string{"step", "create", "foo"}, "wip plumbing step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, nil, tc.args...)
			if r.exitCode != 1 {
				t.Fatalf("%v: exit=%d, want 1; stdout=%q stderr=%q", tc.args, r.exitCode, r.stdout, r.stderr)
			}
			if r.stdout != "" {
				t.Fatalf("%v: stdout = %q, want empty on failure", tc.args, r.stdout)
			}
			if !strings.HasPrefix(r.stderr, "wip: moved") {
				t.Fatalf("%v: stderr = %q, want it to start with %q", tc.args, r.stderr, "wip: moved")
			}
			if !strings.Contains(r.stderr, tc.names) {
				t.Errorf("%v: stderr = %q, want it to name %q", tc.args, r.stderr, tc.names)
			}
		})
	}

	// A global flag (recognized on root) alongside a moved verb still
	// reaches the seam — only a verb-local flag short-circuits earlier
	// (TestPlumbingNamespace_VerbLocalFlagOnAFlatInvocationIsAKnownLimitation).
	withGlobalFlag := run(t, nil, "--verbose", "step", "create", "foo")
	if withGlobalFlag.exitCode != 1 {
		t.Fatalf("--verbose step create foo: exit=%d, want 1; stderr=%q", withGlobalFlag.exitCode, withGlobalFlag.stderr)
	}
	if !strings.Contains(withGlobalFlag.stderr, "wip plumbing step") {
		t.Errorf("--verbose step create foo: stderr = %q, want it to name the qualified path", withGlobalFlag.stderr)
	}

	// --json carries the machine-readable code and names the qualified path.
	r := run(t, nil, "--json", "step", "create", "foo")
	if r.exitCode != 1 {
		t.Fatalf("--json step create foo: exit=%d stderr=%q", r.exitCode, r.stderr)
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
	if envelope.Error.Code != "validation.moved-verb" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "validation.moved-verb")
	}
	if !strings.Contains(envelope.Error.Message, "wip plumbing step") {
		t.Errorf("error.message = %q, want it to name the qualified path", envelope.Error.Message)
	}

	// Human mode for the same case: exit 1, message names the qualified path.
	human := run(t, nil, "step", "create", "foo")
	if human.exitCode != 1 {
		t.Fatalf("step create foo: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	if !strings.Contains(human.stderr, "wip plumbing step") {
		t.Errorf("human stderr = %q, want it to name the qualified path", human.stderr)
	}
}

// TestPlumbingNamespace_VerbLocalFlagOnAFlatInvocationIsAKnownLimitation
// documents a workplan/code conflict discovered while implementing this
// Step (recorded in a finding on plumbing-namespace/step-02): a verb-local
// flag that is not one of root's own global flags (--json/--verbose/
// --as-role) makes Cobra's ParseFlags fail with its own "unknown flag"
// error before root.Args (and so validation.moved-verb) ever runs, because
// flag parsing for an unresolved command happens against root's FlagSet,
// which never knows a subcommand's local flags. This is not a regression:
// a never-existed command with an unrecognized flag hits the identical
// short-circuit today (also asserted here).
func TestPlumbingNamespace_VerbLocalFlagOnAFlatInvocationIsAKnownLimitation(t *testing.T) {
	movedWithLocalFlag := run(t, nil, "gate", "declare", "somenode", "--scale", "step")
	if movedWithLocalFlag.exitCode != 2 {
		t.Fatalf("gate declare somenode --scale step: exit=%d, want 2 (Cobra's own flag-parsing path); stderr=%q", movedWithLocalFlag.exitCode, movedWithLocalFlag.stderr)
	}
	if !strings.Contains(movedWithLocalFlag.stderr, "unknown flag") {
		t.Errorf("stderr = %q, want it to mention the unknown flag", movedWithLocalFlag.stderr)
	}

	neverExistedWithLocalFlag := run(t, nil, "bogus", "--scale", "step")
	if neverExistedWithLocalFlag.exitCode != 2 {
		t.Fatalf("bogus --scale step: exit=%d, want 2; stderr=%q", neverExistedWithLocalFlag.exitCode, neverExistedWithLocalFlag.stderr)
	}
	if !strings.Contains(neverExistedWithLocalFlag.stderr, "unknown flag") {
		t.Errorf("stderr = %q, want it to mention the unknown flag", neverExistedWithLocalFlag.stderr)
	}
}

func TestPlumbingNamespace_UnknownCommandIsUnchanged(t *testing.T) {
	r := run(t, nil, "bogus")
	if r.exitCode != 2 {
		t.Fatalf("bogus: exit=%d, want 2; stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.HasPrefix(r.stderr, `wip: unknown command "bogus" for "wip"`) {
		t.Errorf("stderr = %q, want Cobra's own unknown-command wording", r.stderr)
	}

	// `step` moved, so a typo of it (`stpe`) no longer has anything to
	// match against at the root level — Cobra's suggestion machinery
	// (legacyArgs -> findSuggestions) only ever compares against the
	// command actually found, and that is root itself here (recorded as a
	// finding: the workplan's own "`wip stpe` still offers a suggestion"
	// example no longer holds post-move). A porcelain-verb typo still
	// exercises the same suggestionsBlock code path in root.go.
	typo := run(t, nil, "statu")
	if typo.exitCode != 2 {
		t.Fatalf("statu: exit=%d, want 2; stderr=%q", typo.exitCode, typo.stderr)
	}
	if !strings.Contains(typo.stderr, "Did you mean this?") || !strings.Contains(typo.stderr, "status") {
		t.Errorf("stderr = %q, want a suggestion naming %q", typo.stderr, "status")
	}
}
