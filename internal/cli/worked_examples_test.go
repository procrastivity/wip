// Worked examples for the `tiers` Matter's `worked-examples` Stage (PLAN
// 1.1's seven examples, workplans/tiers.md). Each test is one example,
// exercised through the actual built binary (binPath, run, runIn, gitIn come
// from e2e_test.go / tiers_e2e_test.go, same package) so the captured
// behavior is real, not asserted against an internal model of it.
//
// Example 7 is the one exception the workplan itself calls for: because
// `write-surface` (Matter-birth verbs) does not exist yet, its Matter/Batch
// rows are seeded directly through the store's data-access layer as
// fixtures, per the Stage's own posture note.
package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestWorkedExample1_ThreeClonesOneRepo: a fresh clone, a second clone moved
// on disk after init (recovered via doctor's relink offer, not a duplicate
// row), and a linked worktree of that second clone with its own Worktree
// row. All three resolve to the same Repo; `wip status` from each location
// is captured.
func TestWorkedExample1_ThreeClonesOneRepo(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	root := t.TempDir()

	// widget-a: a fresh clone.
	a := filepath.Join(root, "widget-a")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, a, "init", "-q")
	gitIn(t, a, "config", "user.email", "test@example.com")
	gitIn(t, a, "config", "user.name", "test")
	gitIn(t, a, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if r := runIn(t, a, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init widget-a: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// widget-b: a second clone of the same remote, then moved on disk.
	b := filepath.Join(root, "widget-b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, b, "init", "-q")
	gitIn(t, b, "config", "user.email", "test@example.com")
	gitIn(t, b, "config", "user.name", "test")
	gitIn(t, b, "remote", "add", "origin", "https://github.com/acme/widget.git")
	if r := runIn(t, b, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init widget-b: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	bMoved := filepath.Join(root, "widget-b-moved")
	if err := os.Rename(b, bMoved); err != nil {
		t.Fatal(err)
	}

	// Recovery via doctor's offer, then the accepted relink.
	doctorReport := runIn(t, bMoved, dbEnv, "doctor")
	if doctorReport.exitCode != 0 {
		t.Fatalf("doctor at moved widget-b: exit=%d stderr=%q", doctorReport.exitCode, doctorReport.stderr)
	}
	relinkResult := runIn(t, bMoved, dbEnv, "clone", "relink", "widget-b")
	if relinkResult.exitCode != 0 {
		t.Fatalf("clone relink: exit=%d stderr=%q", relinkResult.exitCode, relinkResult.stderr)
	}

	// A linked worktree of widget-b (now at widget-b-moved) gets its own
	// Worktree row.
	wtDir := filepath.Join(root, "widget-b-feature")
	gitIn(t, bMoved, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, bMoved, "worktree", "add", "-q", "-b", "feature", wtDir)
	if r := runIn(t, wtDir, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init linked worktree: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Recovery left exactly one clone row for widget-b, not a duplicate.
	list := runIn(t, a, dbEnv, "clone", "list", "--json")
	var listPayload struct {
		Clones []struct{ Label string } `json:"clones"`
	}
	if err := json.Unmarshal([]byte(list.stdout), &listPayload); err != nil {
		t.Fatalf("clone list stdout not JSON: %v (%q)", err, list.stdout)
	}
	if len(listPayload.Clones) != 2 {
		t.Fatalf("clones = %+v, want exactly 2 (widget-a, widget-b) — a duplicate row means relink failed to recover", listPayload.Clones)
	}

	// `wip status` from each location, captured verbatim.
	fromA := runIn(t, a, dbEnv, "status")
	fromBMoved := runIn(t, bMoved, dbEnv, "status")
	fromFeature := runIn(t, wtDir, dbEnv, "status")

	t.Logf("wip status (from widget-a):\n%s", fromA.stdout)
	t.Logf("wip status (from widget-b, moved+relinked):\n%s", fromBMoved.stdout)
	t.Logf("wip status (from widget-b-feature, linked worktree):\n%s", fromFeature.stdout)

	want := "acme/widget\n" +
		"  widget-a           Clone · current\n" +
		"  widget-b           Clone\n" +
		"  widget-b-feature   Worktree (of widget-b)\n"
	if fromA.stdout != want {
		t.Errorf("status from widget-a =\n%s\nwant\n%s", fromA.stdout, want)
	}
}

// TestWorkedExample2_LinkedWorktreeThenMoved: a Worktree row created via
// `wip init` inside a linked worktree, then the worktree itself moved with
// `git worktree move`; the Worktree's natural key (its git-assigned name) is
// unaffected — only a Clone's common-dir-keyed identity is move-detection's
// concern.
func TestWorkedExample2_LinkedWorktreeThenMoved(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	root := t.TempDir()

	main := filepath.Join(root, "widget")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	gitIn(t, main, "config", "user.email", "test@example.com")
	gitIn(t, main, "config", "user.name", "test")
	gitIn(t, main, "remote", "add", "origin", "git@github.com:acme/widget.git")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	if r := runIn(t, main, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init main: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	wtDir := filepath.Join(root, "widget-feature")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feature", wtDir)
	before := runIn(t, wtDir, dbEnv, "init", "--json")
	if before.exitCode != 0 {
		t.Fatalf("init worktree: exit=%d stderr=%q", before.exitCode, before.stderr)
	}
	var beforePayload struct {
		Worktree     string `json:"worktree"`
		WorktreeName string `json:"worktreeName"`
	}
	if err := json.Unmarshal([]byte(before.stdout), &beforePayload); err != nil {
		t.Fatalf("init stdout not JSON: %v", err)
	}

	movedWT := filepath.Join(root, "widget-feature-moved")
	gitIn(t, main, "worktree", "move", wtDir, movedWT)

	after := runIn(t, movedWT, dbEnv, "status", "--json")
	if after.exitCode != 0 {
		t.Fatalf("status after worktree move: exit=%d stderr=%q", after.exitCode, after.stderr)
	}
	var afterPayload struct {
		Repo struct {
			Clones []struct {
				Worktrees []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Current bool   `json:"current"`
				} `json:"worktrees"`
			} `json:"clones"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &afterPayload); err != nil {
		t.Fatalf("status stdout not JSON: %v (%q)", err, after.stdout)
	}
	if len(afterPayload.Repo.Clones) != 1 || len(afterPayload.Repo.Clones[0].Worktrees) != 1 {
		t.Fatalf("unexpected shape after move: %+v", afterPayload)
	}
	wt := afterPayload.Repo.Clones[0].Worktrees[0]
	if wt.ID != beforePayload.Worktree {
		t.Errorf("worktree identity changed across the filesystem move: %s -> %s", beforePayload.Worktree, wt.ID)
	}
	if wt.Name != beforePayload.WorktreeName {
		t.Errorf("worktree name changed across the filesystem move: %q -> %q", beforePayload.WorktreeName, wt.Name)
	}
	if !wt.Current {
		t.Error("the moved worktree is not marked current from its own new location")
	}
}

// TestWorkedExample3_MovedCloneSameCommonDir: a clone directory moved such
// that git-common-dir itself is unaffected (a separate git-dir, moved
// alongside a symlinked-parent scenario in spirit) is a no-op for wip — no
// relink offered, no new row, because the natural key genuinely has not
// changed. Distinguishes this from example 1's Clone-B case.
func TestWorkedExample3_MovedCloneSameCommonDir(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	root := t.TempDir()

	externalGit := filepath.Join(root, "external-git")
	detached := filepath.Join(root, "detached")
	gitInitSeparate(t, root, "detached", externalGit)
	gitIn(t, detached, "config", "user.email", "test@example.com")
	gitIn(t, detached, "config", "user.name", "test")
	gitIn(t, detached, "remote", "add", "origin", "git@github.com:acme/detached.git")

	before := runIn(t, detached, dbEnv, "init", "--json")
	if before.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", before.exitCode, before.stderr)
	}
	var beforePayload struct {
		Clone string `json:"clone"`
	}
	if err := json.Unmarshal([]byte(before.stdout), &beforePayload); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(root, "detached-renamed")
	if err := os.Rename(detached, moved); err != nil {
		t.Fatal(err)
	}

	// No-op: doctor reports the clone as already known, not a move to
	// resolve.
	doctorResult := runIn(t, moved, dbEnv, "doctor", "--json")
	if doctorResult.exitCode != 0 {
		t.Fatalf("doctor after a same-common-dir move: exit=%d stderr=%q", doctorResult.exitCode, doctorResult.stderr)
	}
	var doctorPayload struct {
		Known bool   `json:"known"`
		Clone string `json:"clone"`
	}
	if err := json.Unmarshal([]byte(doctorResult.stdout), &doctorPayload); err != nil {
		t.Fatal(err)
	}
	if !doctorPayload.Known {
		t.Fatal("doctor reports the clone unknown after a move that left common-dir unchanged, want known=true (a true no-op)")
	}
	if doctorPayload.Clone != beforePayload.Clone {
		t.Errorf("clone identity changed across a same-common-dir move: %s -> %s", beforePayload.Clone, doctorPayload.Clone)
	}

	// A second init here refuses as already-initialized, not as a new clone
	// — confirming no new row was ever tempting.
	reInit := runIn(t, moved, dbEnv, "init", "--json")
	if reInit.exitCode != 1 {
		t.Fatalf("re-init after the no-op move: exit=%d, want 1 (already-initialized)", reInit.exitCode)
	}
}

// TestWorkedExample4_LocalOnlyThenAdoptsARemote: `wip init` on a repo with
// no remotes (Repo row, natural_key = NULL); a remote added afterward; the
// next verb run adopts the key in place — same Repo ULID, newly-populated
// natural key.
func TestWorkedExample4_LocalOnlyThenAdoptsARemote(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}
	dir := newGitRepo(t, "local-only")

	before := runIn(t, dir, dbEnv, "init", "--json")
	if before.exitCode != 0 {
		t.Fatalf("init: exit=%d stderr=%q", before.exitCode, before.stderr)
	}
	var beforePayload struct {
		Repo      string `json:"repo"`
		RemoteURL string `json:"remoteUrl"`
	}
	if err := json.Unmarshal([]byte(before.stdout), &beforePayload); err != nil {
		t.Fatal(err)
	}
	if beforePayload.RemoteURL != "" {
		t.Fatalf("RemoteURL = %q before any remote exists, want empty", beforePayload.RemoteURL)
	}

	gitIn(t, dir, "remote", "add", "origin", "git@github.com:acme/local-only.git")

	after := runIn(t, dir, dbEnv, "status", "--json")
	if after.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", after.exitCode, after.stderr)
	}
	var afterPayload struct {
		Repo struct {
			ID     string `json:"id"`
			Header string `json:"header"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &afterPayload); err != nil {
		t.Fatal(err)
	}
	if afterPayload.Repo.ID != beforePayload.Repo {
		t.Errorf("Repo identity changed on adoption: %s -> %s, want the same ULID", beforePayload.Repo, afterPayload.Repo.ID)
	}
	if afterPayload.Repo.Header != "acme/local-only" {
		t.Errorf("Repo header after adoption = %q, want %q", afterPayload.Repo.Header, "acme/local-only")
	}
}

// TestWorkedExample5_ForkGetsADistinctRepo: two clones of genuinely
// different remotes (a fork and its upstream, neither using
// --identity-remote) resolve to two distinct Repo rows — the multi-remote
// rule's default behavior, no override needed.
func TestWorkedExample5_ForkGetsADistinctRepo(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}

	upstream := newGitRepo(t, "widget-upstream")
	gitIn(t, upstream, "remote", "add", "origin", "git@github.com:acme/widget.git")
	upstreamResult := runIn(t, upstream, dbEnv, "init", "--json")
	if upstreamResult.exitCode != 0 {
		t.Fatalf("init upstream: exit=%d stderr=%q", upstreamResult.exitCode, upstreamResult.stderr)
	}

	fork := newGitRepo(t, "widget-fork")
	gitIn(t, fork, "remote", "add", "origin", "git@github.com:someone/widget-fork.git")
	forkResult := runIn(t, fork, dbEnv, "init", "--json")
	if forkResult.exitCode != 0 {
		t.Fatalf("init fork: exit=%d stderr=%q", forkResult.exitCode, forkResult.stderr)
	}

	var up, fk struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal([]byte(upstreamResult.stdout), &up); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(forkResult.stdout), &fk); err != nil {
		t.Fatal(err)
	}
	if up.Repo == fk.Repo {
		t.Fatal("upstream and fork resolved to the same Repo, want two distinct Repos")
	}

	// wip status, host-wide, sees both as separate repos.
	elsewhere := t.TempDir()
	hostWide := runIn(t, elsewhere, dbEnv, "status", "--json")
	if hostWide.exitCode != 0 {
		t.Fatalf("status host-wide: exit=%d stderr=%q", hostWide.exitCode, hostWide.stderr)
	}
	var hostWidePayload struct {
		HostWide bool `json:"hostWide"`
		Repos    []struct {
			Header string `json:"header"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(hostWide.stdout), &hostWidePayload); err != nil {
		t.Fatal(err)
	}
	if !hostWidePayload.HostWide || len(hostWidePayload.Repos) != 2 {
		t.Errorf("host-wide status = %+v, want two distinct repos listed", hostWidePayload)
	}
}

// TestWorkedExample6_UnknownClone covers both sub-cases of the Brief's "Move
// detection" unknown-clone offer: (a) remote known, doctor offers both
// relink and new-clone, and the accepted new-clone path is `wip init`
// succeeding normally; (b) remote also unknown, any verb needing a resolved
// Clone hard-fails, no relink offer attempted.
func TestWorkedExample6_UnknownClone(t *testing.T) {
	dbEnv := []string{"WIP_DB_PATH=" + filepath.Join(t.TempDir(), "wip.db")}

	known := newGitRepo(t, "widget-known")
	gitIn(t, known, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if r := runIn(t, known, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init known: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	t.Run("6a_remote_known_offers_relink_or_new", func(t *testing.T) {
		unregistered := newGitRepo(t, "widget-new-clone")
		gitIn(t, unregistered, "remote", "add", "origin", "https://github.com/acme/widget.git")

		doctorResult := runIn(t, unregistered, dbEnv, "doctor")
		if doctorResult.exitCode != 0 {
			t.Fatalf("doctor: exit=%d stderr=%q", doctorResult.exitCode, doctorResult.stderr)
		}
		t.Logf("wip doctor (unknown clone, known remote):\n%s", doctorResult.stdout)

		// Accepted new-clone path: wip init succeeds normally.
		initResult := runIn(t, unregistered, dbEnv, "init")
		if initResult.exitCode != 0 {
			t.Fatalf("init (accepting the new-clone offer): exit=%d stderr=%q", initResult.exitCode, initResult.stderr)
		}
	})

	t.Run("6b_remote_also_unknown_hard_fails", func(t *testing.T) {
		unknown := newGitRepo(t, "totally-unknown")
		gitIn(t, unknown, "remote", "add", "origin", "git@github.com:nobody/unknown.git")

		doctorResult := runIn(t, unknown, dbEnv, "doctor", "--json")
		if doctorResult.exitCode != 3 {
			t.Fatalf("doctor exit code = %d, want 3 (refusal, no relink offer possible)", doctorResult.exitCode)
		}
		t.Logf("wip doctor --json (unknown clone, unknown remote):\n%s", doctorResult.stderr)
		var envelope struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal([]byte(doctorResult.stderr), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error.Code != "refusal.unknown-clone" {
			t.Errorf("code = %q, want %q", envelope.Error.Code, "refusal.unknown-clone")
		}
	})
}

// TestWorkedExample7_CrossRepoBatch: two Repo rows and four Matter rows
// (two per Repo) seeded via the schema data-access layer as fixtures — the
// birth verbs (`write-surface`) don't exist yet, which is this Stage's
// stated posture, not a shortcut — all four joined to one Batch row (Batch
// "keys at no tier", D39, so spanning two Repos is legal by construction).
// Dispatched from a single clone of one of the two Repos. `wip status` from
// that clone is captured showing its own repo-wide/current-clone-marked
// scope is unaffected by the Batch's cross-repo membership; the Batch's
// legality is confirmed directly against the store.
//
// Explicitly scoped, per the seed card: Run and dispatch mechanics for this
// same scenario are appendix-orchestration.md item 7, Phase 2 — not
// attempted here.
func TestWorkedExample7_CrossRepoBatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	dbEnv := []string{"WIP_DB_PATH=" + dbPath}

	repoA := newGitRepo(t, "repo-a")
	gitIn(t, repoA, "remote", "add", "origin", "git@github.com:acme/repo-a.git")
	initA := runIn(t, repoA, dbEnv, "init", "--json")
	if initA.exitCode != 0 {
		t.Fatalf("init repo-a: exit=%d stderr=%q", initA.exitCode, initA.stderr)
	}
	var initAPayload struct {
		Repo     string `json:"repo"`
		Clone    string `json:"clone"`
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(initA.stdout), &initAPayload); err != nil {
		t.Fatal(err)
	}

	repoB := newGitRepo(t, "repo-b")
	gitIn(t, repoB, "remote", "add", "origin", "git@github.com:acme/repo-b.git")
	initB := runIn(t, repoB, dbEnv, "init", "--json")
	if initB.exitCode != 0 {
		t.Fatalf("init repo-b: exit=%d stderr=%q", initB.exitCode, initB.stderr)
	}
	var initBPayload struct {
		Repo string `json:"repo"`
	}
	if err := json.Unmarshal([]byte(initB.stdout), &initBPayload); err != nil {
		t.Fatal(err)
	}

	// Fixtures: four Matters (two per Repo) and one Batch spanning both,
	// seeded directly through the store — the only fixture available at
	// this point in the register (the Stage's own posture note).
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	matter := func(repo, locator, title string) string {
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

	a1 := matter(initAPayload.Repo, "matter-a1", "Repo A, Matter 1")
	a2 := matter(initAPayload.Repo, "matter-a2", "Repo A, Matter 2")
	b1 := matter(initBPayload.Repo, "matter-b1", "Repo B, Matter 1")
	b2 := matter(initBPayload.Repo, "matter-b2", "Repo B, Matter 2")

	// One Batch, keyed at no tier (D39) — carrying a null repo, but still an
	// execution event requiring a Clone+Worktree context (D56): the clone it
	// is dispatched from, repo-a's.
	dispatchEnv := store.Env{Clone: initAPayload.Clone, Worktree: initAPayload.Worktree}
	batchID := ""
	if _, err := s.Commit(context.Background(), store.Request{Actor: store.ActorHuman, Env: dispatchEnv}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		batchID = tx.NewID()
		return []store.Draft{{Type: store.TypeBatchCreated, Subject: batchID, Payload: store.BatchCreated{Name: "cross-repo-example-7"}}}, nil
	}); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	for _, m := range []string{a1, a2, b1, b2} {
		if _, err := s.Commit(context.Background(), store.Request{Actor: store.ActorHuman, Env: dispatchEnv}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
			return []store.Draft{{Type: store.TypeBatchJoined, Subject: batchID, Payload: store.BatchMembership{Matter: m}}}, nil
		}); err != nil {
			t.Fatalf("join batch: %v", err)
		}
	}

	// The Batch legitimately spans two Repos — legal by construction (D39).
	members, err := s.BatchMembers(context.Background(), batchID)
	if err != nil {
		t.Fatalf("BatchMembers: %v", err)
	}
	if len(members) != 4 {
		t.Fatalf("batch members = %v, want all four Matters", members)
	}

	// Dispatched from a single clone of repo-a: `wip status`'s own
	// repo-wide/current-clone-marked scope (step-08) is unaffected by the
	// Batch's cross-repo membership — it never mentions repo-b or the Batch,
	// because tier-scoped status doesn't render Matter or Batch content at
	// all (that is `read-surface`'s extension, reading this Step's output
	// as settled input).
	statusResult := runIn(t, repoA, dbEnv, "status")
	if statusResult.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", statusResult.exitCode, statusResult.stderr)
	}
	t.Logf("wip status (dispatched from repo-a, batch spans repo-a+repo-b):\n%s", statusResult.stdout)
	want := "acme/repo-a\n  repo-a             Clone · current\n"
	if statusResult.stdout != want {
		t.Errorf("status =\n%s\nwant\n%s\n(status must stay tier-scoped regardless of Batch/Matter content elsewhere in the store)", statusResult.stdout, want)
	}
}

// gitInitSeparate runs `git init --separate-git-dir=<gitDir> <name>` from
// parent, producing a clone whose git-common-dir lives outside its working
// tree — the shape example 3 needs to move the working tree without moving
// common-dir.
func gitInitSeparate(t *testing.T, parent, name, gitDir string) {
	t.Helper()
	gitIn(t, parent, "init", "-q", "--separate-git-dir="+gitDir, name)
}
