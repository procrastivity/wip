// End-to-end coverage of the two resolution gaps: a known Clone's un-init'd
// linked worktree refuses by name, a never-seen repo still refuses as an
// unknown clone, and `wip init` in the worktree makes the same verb succeed.
package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func wantRefusal(t *testing.T, label string, r result, code string, fragments ...string) {
	t.Helper()
	if r.exitCode != 3 {
		t.Fatalf("%s: exit=%d, want 3 (refusal); stderr=%q", label, r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("%s: stdout = %q, want empty on a refusal", label, r.stdout)
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

func TestUnknownWorktree_LinkedWorktreeOfKnownCloneRefusesByName(t *testing.T) {
	dir, dbEnv := setupRepo(t) // clone label is "widget" (dir basename)
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	wtDir := filepath.Join(filepath.Dir(dir), "widget-feature")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "feature", wtDir)

	r := runIn(t, wtDir, dbEnv, "refresh")
	if r.exitCode != 3 {
		t.Fatalf("refresh exit code = %d, want 3 (refusal); stderr=%q", r.exitCode, r.stderr)
	}
	if r.stdout != "" {
		t.Fatalf("refresh stdout = %q, want empty on a refusal", r.stdout)
	}
	if !strings.Contains(r.stderr, `worktree "widget-feature" of clone "widget" is unknown to wip`) {
		t.Errorf("refresh stderr = %q, want it to contain %q", r.stderr, `worktree "widget-feature" of clone "widget" is unknown to wip`)
	}
	if !strings.Contains(r.stderr, "run `wip init` here to attach it") {
		t.Errorf("refresh stderr = %q, want it to contain %q", r.stderr, "run `wip init` here to attach it")
	}
	if strings.Contains(r.stderr, "this clone is unknown") {
		t.Errorf("refresh stderr = %q, want it to NOT contain %q", r.stderr, "this clone is unknown")
	}
	if !strings.HasPrefix(r.stderr, "wip: refresh: ") {
		t.Errorf("refresh stderr = %q, want prefix %q", r.stderr, "wip: refresh: ")
	}

	wantRefusal(t, "refresh --json", runIn(t, wtDir, dbEnv, "refresh", "--json"),
		"refusal.unknown-worktree", `worktree "widget-feature"`, `clone "widget"`)

	wantRefusal(t, "next --json", runIn(t, wtDir, dbEnv, "next", "--json"),
		"refusal.unknown-worktree", `worktree "widget-feature"`, `clone "widget"`)

	doctorResult := runIn(t, wtDir, dbEnv, "doctor")
	if doctorResult.exitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0; stderr=%q", doctorResult.exitCode, doctorResult.stderr)
	}
	if !strings.Contains(doctorResult.stdout, "this clone is known to wip") {
		t.Errorf("doctor stdout = %q, want it to contain %q", doctorResult.stdout, "this clone is known to wip")
	}

	initResult := runIn(t, wtDir, dbEnv, "init")
	if initResult.exitCode != 0 {
		t.Fatalf("init exit code = %d, want 0; stderr=%q", initResult.exitCode, initResult.stderr)
	}
	if !strings.Contains(initResult.stdout, "widget-feature") {
		t.Errorf("init stdout = %q, want it to contain %q", initResult.stdout, "widget-feature")
	}

	refreshAfterInit := runIn(t, wtDir, dbEnv, "refresh")
	if refreshAfterInit.exitCode != 0 {
		t.Fatalf("refresh (after init) exit code = %d, want 0; stderr=%q", refreshAfterInit.exitCode, refreshAfterInit.stderr)
	}
	if !strings.HasPrefix(refreshAfterInit.stdout, "opened dispatch ") {
		t.Errorf("refresh (after init) stdout = %q, want prefix %q", refreshAfterInit.stdout, "opened dispatch ")
	}

	nextAfterInit := runIn(t, wtDir, dbEnv, "next")
	if nextAfterInit.exitCode != 0 {
		t.Fatalf("next (after init) exit code = %d, want 0; stderr=%q", nextAfterInit.exitCode, nextAfterInit.stderr)
	}

	mainRefresh := runIn(t, dir, dbEnv, "refresh")
	if mainRefresh.exitCode != 0 {
		t.Fatalf("refresh (main worktree) exit code = %d, want 0; stderr=%q", mainRefresh.exitCode, mainRefresh.stderr)
	}
}

func TestUnknownWorktree_NeverSeenRepoStillRefusesAsUnknownClone(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "never-init")

	wantRefusal(t, "refresh --json", runIn(t, dir, dbEnv, "refresh", "--json"),
		"refusal.unknown-clone", "this clone is unknown to wip", "run `wip init` here first")

	wantRefusal(t, "next --json", runIn(t, dir, dbEnv, "next", "--json"),
		"refusal.unknown-clone", "this clone is unknown to wip")

	r := runIn(t, dir, dbEnv, "refresh")
	if strings.Contains(r.stderr, "worktree") {
		t.Errorf("refresh stderr = %q, want it to NOT contain %q", r.stderr, "worktree")
	}
}
