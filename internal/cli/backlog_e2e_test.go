// End-to-end tests for the backlog exits' validation shape (plan, decline,
// delegate on a non-entered or unknown entry), through the built binary.
// Helpers come from e2e_test.go and writesurface_birth_test.go (setupRepo,
// runIn, mustJSON).
package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// wantJSONError asserts the shape of a --json-mode failure: exit 1, empty
// stdout, stderr decodable as the error envelope, and error.code matching
// code with error.message containing every fragment. label identifies the
// call under test in any failure message.
func wantJSONError(t *testing.T, label string, r result, code string, fragments ...string) {
	t.Helper()
	if r.exitCode != 1 {
		t.Fatalf("%s: exit=%d, want 1; stderr=%q", label, r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("%s: stdout = %q, want empty", label, r.stdout)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &envelope); err != nil {
		t.Fatalf("%s: stderr is not the --json error envelope: %v (stderr=%q)", label, err, r.stderr)
	}
	if envelope.Error.Code != code {
		t.Fatalf("%s: error.code = %q, want %q", label, envelope.Error.Code, code)
	}
	for _, fragment := range fragments {
		if !strings.Contains(envelope.Error.Message, fragment) {
			t.Fatalf("%s: error.message = %q, want it to contain %q", label, envelope.Error.Message, fragment)
		}
	}
}

func TestBacklogExitsOnANonEnteredEntryAreValidationNotProjectionErrors(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Target", "--locator", "target", "--json").stdout)

	added := mustJSON[struct {
		ID string `json:"id"`
	}](t, runIn(t, dir, dbEnv, "backlog", "add", "--title", "Push me", "--provenance", "intake", "--json").stdout)

	if r := runIn(t, dir, dbEnv, "backlog", "delegate", added.ID, "--json"); r.exitCode != 0 {
		t.Fatalf("delegate: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "backlog", "plan", added.ID, m.Locator)
	if r.exitCode != 1 {
		t.Fatalf("plan (human mode): exit=%d, want 1; stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("plan (human mode): stdout = %q, want empty", r.stdout)
	}
	if !strings.Contains(r.stderr, "is delegated; only an entered entry can be planned") {
		t.Fatalf("plan (human mode): stderr = %q, want it to contain the not-entered message", r.stderr)
	}
	if strings.Contains(r.stderr, "touched 0 projection rows") {
		t.Fatalf("plan (human mode): stderr = %q, want no projection-error leak", r.stderr)
	}
	if !strings.HasPrefix(r.stderr, "wip: ") {
		t.Fatalf("plan (human mode): stderr = %q, want it to start with %q", r.stderr, "wip: ")
	}

	wantJSONError(t, "plan (json mode)",
		runIn(t, dir, dbEnv, "backlog", "plan", added.ID, m.Locator, "--json"),
		"validation.backlog-not-entered", added.ID, "delegated")

	wantJSONError(t, "decline (json mode)",
		runIn(t, dir, dbEnv, "backlog", "decline", added.ID, "--reason", "x", "--json"),
		"validation.backlog-not-entered", "can be declined")

	wantJSONError(t, "delegate (json mode)",
		runIn(t, dir, dbEnv, "backlog", "delegate", added.ID, "--json"),
		"validation.backlog-not-entered", "can be delegated")

	wantJSONError(t, "plan unknown id (json mode)",
		runIn(t, dir, dbEnv, "backlog", "plan", "01NOPE", m.Locator, "--json"),
		"validation.unknown-backlog-entry")
}
