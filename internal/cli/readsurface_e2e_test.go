// End-to-end tests for read-surface, through the actual built binary
// (binPath and run/runIn come from e2e_test.go/tiers_e2e_test.go, same
// package). Fixtures are seeded directly through store's data-access layer,
// exactly as worked_examples_test.go's own `matter` helper does: this
// Matter carries no edge to write-surface (workplan step-07), so its tests
// do not depend on the write verbs even though, in this tree, they already
// exist.
package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// seedMatter births a Planned Matter directly through the store, the one
// fixture path available at this point in the register.
func seedMatter(t *testing.T, s *store.Store, repo, locator, title string) string {
	t.Helper()
	req := store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}
	var id string
	if _, err := s.Commit(context.Background(), req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeMatterCreated,
			Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, SortKey: 1000},
		}}, nil
	}); err != nil {
		t.Fatalf("seed matter %s: %v", locator, err)
	}
	return id
}

// TestReadSurface_NextSet_EmitsCursorMoved is step-07(b): a cursor move
// appears in the event log, exercised through the real `wip next --set`
// path — not a seeded event.
func TestReadSurface_NextSet_EmitsCursorMoved(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	dbEnv := []string{"WIP_DB_PATH=" + dbPath}

	dir := newGitRepo(t, "widget")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	init := runIn(t, dir, dbEnv, "init", "--json")
	if init.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", init.exitCode, init.stderr)
	}
	var initPayload struct {
		Repo     string `json:"repo"`
		Clone    string `json:"clone"`
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(init.stdout), &initPayload); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	matter := seedMatter(t, s, initPayload.Repo, "fix-flaky-clone-detect", "Fix the flaky clone-detect test")
	_ = s.Close()

	before := runIn(t, dir, dbEnv, "next")
	if before.exitCode != 0 {
		t.Fatalf("next (before --set): exit=%d stderr=%q", before.exitCode, before.stderr)
	}
	t.Logf("wip next (no cursor set):\n%s", before.stdout)

	setResult := runIn(t, dir, dbEnv, "next", "--set", "fix-flaky-clone-detect")
	if setResult.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", setResult.exitCode, setResult.stderr)
	}

	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = s.Close() }()

	events, err := s.Events(context.Background())
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	var moves []store.Event
	for _, ev := range events {
		if ev.Type == store.TypeCursorMoved {
			moves = append(moves, ev)
		}
	}
	if len(moves) != 1 {
		t.Fatalf("cursor.moved events = %d, want exactly 1", len(moves))
	}
	mv := moves[0]
	if mv.Repo != initPayload.Repo {
		t.Errorf("cursor.moved repo = %s, want %s", mv.Repo, initPayload.Repo)
	}
	if mv.Clone != initPayload.Clone {
		t.Errorf("cursor.moved clone = %s, want %s", mv.Clone, initPayload.Clone)
	}
	if mv.Worktree != initPayload.Worktree {
		t.Errorf("cursor.moved worktree = %s, want %s", mv.Worktree, initPayload.Worktree)
	}
	// subject is the Worktree the cursor belongs to (schema's payloads.go),
	// never the node it now points at — that lives in the payload.
	if mv.Subject != initPayload.Worktree {
		t.Errorf("cursor.moved subject = %s, want the worktree %s", mv.Subject, initPayload.Worktree)
	}
	var payload store.CursorMoved
	if err := json.Unmarshal(mv.Payload, &payload); err != nil {
		t.Fatalf("decode cursor.moved payload: %v", err)
	}
	if payload.Node != matter {
		t.Errorf("cursor.moved payload.node = %s, want the matter %s", payload.Node, matter)
	}
	if payload.Previous != "" {
		t.Errorf("cursor.moved payload.previous = %q, want empty (no cursor was set before)", payload.Previous)
	}

	// And the read side agrees, through the real `next` path.
	after := runIn(t, dir, dbEnv, "next")
	if after.exitCode != 0 {
		t.Fatalf("next (after --set): exit=%d stderr=%q", after.exitCode, after.stderr)
	}
	t.Logf("wip next (cursor set):\n%s", after.stdout)
	if !strings.Contains(after.stdout, "fix-flaky-clone-detect") {
		t.Errorf("next output %q does not name the cursor target", after.stdout)
	}
	if !strings.Contains(after.stdout, "no plan — work it directly") {
		t.Errorf("next output %q does not read as a bare Matter (vocabulary output 1)", after.stdout)
	}
}
