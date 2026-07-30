// External test package (not `guards`): internal/writesurface itself
// imports internal/guards (the gate-order precondition, step-03), so a
// white-box `package guards` test file that also imports writesurface would
// be an import cycle — the fixture helpers below need writesurface's node
// constructors, so this file, and its siblings in this directory, build
// fixtures from the outside and assert only against guards' exported API.
package guards_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

var ctx = context.Background()

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newRepo inits a fresh git repo and returns its Repo ID — the same route
// every other Matter's tests use (render/testutil_test.go's setup), rather
// than hand-inserting a repo row, so these tests exercise the real tier
// resolution path Cycles/GateDeclarations are read against in production.
func newRepo(t *testing.T, s *store.Store) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	result, err := tiers.Init(ctx, s, store.ActorHuman, dir, "")
	if err != nil {
		t.Fatalf("tiers.Init: %v", err)
	}
	return result.Repo.ID
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// matter births a Matter and returns its ID.
func matter(t *testing.T, s *store.Store, repo, title string) string {
	t.Helper()
	n, err := writesurface.CreateMatter(ctx, s, store.ActorHuman, repo, title)
	if err != nil {
		t.Fatalf("CreateMatter(%q): %v", title, err)
	}
	return n.ID
}

// addEdge inserts a raw dependency.added event, bypassing DependAdd's
// WouldCycle precondition entirely — the only way to put a cycle in the
// store to audit, since the add-time check makes one unreachable through the
// write path (the same rationale schema's own cycle_test.go documents: "a
// cycle in a store is always one that arrived some other way").
func addEdge(t *testing.T, s *store.Store, repo, blocked, blocker string) {
	t.Helper()
	req := store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeDependencyAdded,
			Subject: blocked,
			Payload: store.DependencyChange{Edge: tx.NewID(), Blocker: blocker},
		}}, nil
	}); err != nil {
		t.Fatalf("addEdge(%s <- %s): %v", blocked, blocker, err)
	}
}
