// End-to-end coverage of the unknown-clone refusal's read-only handoff
// clause (tracker-config-discoverability step-07, D6): every copy of the
// message now ends with "(a write; if you cannot run it, ask the user)" so
// an agent that cannot run a write knows to stop and ask, rather than read
// wip's source. Same package and helpers as tiers_e2e_test.go.
package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The unknown-clone refusal carries a read-only handoff clause so an
// agent that cannot write knows to stop and ask, not read wip's source
// (tracker-config-discoverability step-07, D6).
func TestUnknownClone_RefusalNamesInitAsAWrite(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "totally-unknown")

	const clause = "run `wip init` here first (a write; if you cannot run it, ask the user)"

	// A read-surface verb: readsurface's copy.
	r := runIn(t, dir, dbEnv, "next", "--json")
	if r.exitCode != 3 {
		t.Fatalf("next exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
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
	if envelope.Error.Code != "refusal.unknown-clone" {
		t.Errorf("error.code = %q, want %q", envelope.Error.Code, "refusal.unknown-clone")
	}
	if !strings.Contains(envelope.Error.Message, clause) {
		t.Errorf("message = %q, want it to contain %q", envelope.Error.Message, clause)
	}

	// The clone verb: its copy leads with --repo.
	r = runIn(t, dir, dbEnv, "clone", "list")
	if r.exitCode != 3 {
		t.Fatalf("clone list exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	want := "pass --repo, or " + clause
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
}
