// Worked examples for the `tiers` Matter's `worked-examples` Stage (PLAN
// 1.1's seven examples, workplans/tiers.md). Each test is one example,
// exercised through the actual built binary (binPath, run, runIn, gitIn come
// from e2e_test.go / tiers_e2e_test.go, same package) so the captured
// behavior is real, not asserted against an internal model of it.
//
// Example 7 is the one exception the workplan itself calls for: `write-surface`
// (Matter-birth verbs) did not exist yet when its Batch half landed, so that
// half's Matter/Batch rows are seeded directly through the store's data-access
// layer as fixtures, per the Stage's own posture note. Its Run half
// (TestWorkedExample7_CrossRepoRun, Phase 2's `cross-repo-run` Matter) rebuilds
// the same shape through the verbs that exist now.
package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/scheduler"
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
	// Batch's cross-repo membership — it never mentions repo-b or the
	// Batch (Batch-scoped rendering is D25's deferred territory, and status
	// composes over the tiers read scope, D66). What it *does* now render is
	// `read-surface`'s extension: the durable answer to the founding
	// questions over repo-a alone — here, both of repo-a's Matters are
	// Planned with no blockers, so they show up as the unblocked frontier
	// ("next to start"), and nothing about repo-b or the cross-repo Batch
	// leaks in.
	statusResult := runIn(t, repoA, dbEnv, "status")
	if statusResult.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", statusResult.exitCode, statusResult.stderr)
	}
	t.Logf("wip status (dispatched from repo-a, batch spans repo-a+repo-b):\n%s", statusResult.stdout)
	want := "acme/repo-a\n" +
		"  repo-a             Clone · current\n" +
		"\n" +
		"next to start:\n" +
		fmt.Sprintf("  %-24s %s · %s\n", "matter-a1", "matter", "planned") +
		fmt.Sprintf("  %-24s %s · %s\n", "matter-a2", "matter", "planned")
	if statusResult.stdout != want {
		t.Errorf("status =\n%s\nwant\n%s\n(status must stay tier-scoped and never mention repo-b or the Batch, but does now render repo-a's own founding-question content)", statusResult.stdout, want)
	}
}

// TestWorkedExample7_CrossRepoRun: the Run half of the same scenario —
// appendix-orchestration item 7, the `cross-repo-run` Matter — executed
// before cross-repo Runs are called supported. The same shape (four Matters,
// two Repos, one Batch, one clone) is rebuilt through the verbs that now
// exist (`matter create`, `batch create/join` — the Batch half's
// store-fixture posture is gone), then a Run over the Batch is driven
// through scheduler.Orchestrate at cap 1 from repo-a's single clone.
//
// What the example must record (workplan seal condition): the rows and
// events the Run leaves behind, and what `status` and `next` show from the
// dispatching clone. The load-bearing question is the tier stamp: the pass
// runs under one Env — repo-a's — so every event it commits for repo-b's
// Matters carries repo-a's repo dimension.
func TestWorkedExample7_CrossRepoRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	dbEnv := []string{"WIP_DB_PATH=" + dbPath}
	ctx := context.Background()

	repoA := newGitRepo(t, "repo-a")
	gitIn(t, repoA, "remote", "add", "origin", "git@github.com:acme/repo-a.git")
	initA := runIn(t, repoA, dbEnv, "init", "--json")
	if initA.exitCode != 0 {
		t.Fatalf("init repo-a: exit=%d stderr=%q", initA.exitCode, initA.stderr)
	}
	var envA struct {
		Repo     string `json:"repo"`
		Clone    string `json:"clone"`
		Worktree string `json:"worktree"`
	}
	if err := json.Unmarshal([]byte(initA.stdout), &envA); err != nil {
		t.Fatal(err)
	}

	repoB := newGitRepo(t, "repo-b")
	gitIn(t, repoB, "remote", "add", "origin", "git@github.com:acme/repo-b.git")
	if r := runIn(t, repoB, dbEnv, "init"); r.exitCode != 0 {
		t.Fatalf("init repo-b: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Four Matters through the birth verbs, two per Repo. Locator resolution
	// is repo-scoped, so each Matter is born — and joined to the Batch — from
	// its own Repo's clone; only the Run itself is dispatched cross-repo.
	seed := func(dir, locator, title string) string {
		t.Helper()
		r := runIn(t, dir, dbEnv, "matter", "create", "--locator", locator, "--title", title, "--json")
		if r.exitCode != 0 {
			t.Fatalf("matter create %s: exit=%d stderr=%q", locator, r.exitCode, r.stderr)
		}
		return mustJSON[nodePayload](t, r.stdout).ID
	}
	a1 := seed(repoA, "matter-a1", "Repo A, Matter 1")
	a2 := seed(repoA, "matter-a2", "Repo A, Matter 2")
	b1 := seed(repoB, "matter-b1", "Repo B, Matter 1")
	b2 := seed(repoB, "matter-b2", "Repo B, Matter 2")

	if r := runIn(t, repoA, dbEnv, "batch", "create", "cross-repo-run"); r.exitCode != 0 {
		t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, join := range []struct{ dir, locator string }{
		{repoA, "matter-a1"},
		{repoA, "matter-a2"},
		{repoB, "matter-b1"},
		{repoB, "matter-b2"},
	} {
		if r := runIn(t, join.dir, dbEnv, "batch", "join", "cross-repo-run", join.locator); r.exitCode != 0 {
			t.Fatalf("batch join %s: exit=%d stderr=%q", join.locator, r.exitCode, r.stderr)
		}
	}

	// The plain bracket the Orchestrator binds to (D59).
	if r := runIn(t, repoA, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	batch, err := s.BatchByName(ctx, "cross-repo-run")
	if err != nil {
		t.Fatalf("BatchByName: %v", err)
	}
	env := store.Env{Repo: envA.Repo, Clone: envA.Clone, Worktree: envA.Worktree}
	run := startRunDirect(t, s, env, batch.ID, "run-01", a1, a2, b1, b2)

	hooks := &noopHooks{}
	outcome, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1,
		scheduler.Policy{}, scheduler.Hooks{Work: hooks.work})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if outcome.State != scheduler.StateFinished {
		t.Fatalf("outcome = %+v, want Finished", outcome)
	}
	if len(outcome.Worked) != 4 {
		t.Fatalf("worked %d nodes, want 4 (both Repos' Matters)", len(outcome.Worked))
	}

	// The record the Run leaves: every event of the pass — claims, starts,
	// finishes, for repo-b's Matters as much as repo-a's — carries repo-a's
	// tier context, because a pass has exactly one Env (the dispatching
	// clone's) and the store stamps dimensions from Env, not from the
	// subject's own Repo.
	for _, m := range []struct{ id, name string }{{b1, "matter-b1"}, {b2, "matter-b2"}} {
		evs, err := s.EventsOfSubject(ctx, m.id)
		if err != nil {
			t.Fatalf("events for %s: %v", m.name, err)
		}
		lifecycle := 0
		for _, ev := range evs {
			if ev.Type == store.TypeMatterStarted || ev.Type == store.TypeMatterFinished {
				lifecycle++
				if ev.Repo != envA.Repo {
					t.Errorf("%s %s: repo dimension = %q, want the dispatching clone's repo %q (one Env per pass)",
						m.name, ev.Type, ev.Repo, envA.Repo)
				}
			}
		}
		if lifecycle != 2 {
			t.Errorf("%s: %d lifecycle events, want started+finished", m.name, lifecycle)
		}
	}

	// What the dispatching clone sees afterward: `status` stays repo-scoped —
	// repo-a's two Matters are sealed; repo-b's never appear, even though this
	// clone's Run just worked them.
	statusA := runIn(t, repoA, dbEnv, "status")
	if statusA.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", statusA.exitCode, statusA.stderr)
	}
	t.Logf("wip status (dispatching clone, after the cross-repo Run):\n%s", statusA.stdout)
	for _, want := range []string{"matter-a1", "matter-a2"} {
		if !strings.Contains(statusA.stdout, want) {
			t.Errorf("status from repo-a lacks %s:\n%s", want, statusA.stdout)
		}
	}
	for _, leak := range []string{"matter-b1", "matter-b2", "repo-b"} {
		if strings.Contains(statusA.stdout, leak) {
			t.Errorf("status from repo-a mentions %s — tier scope leaked:\n%s", leak, statusA.stdout)
		}
	}
	nextA := runIn(t, repoA, dbEnv, "next")
	if nextA.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", nextA.exitCode, nextA.stderr)
	}
	t.Logf("wip next (dispatching clone, after the cross-repo Run):\n%s", nextA.stdout)

	// And repo-b's own clone reads its Matters as sealed work it never saw
	// happen: the lifecycle is durable and repo-scoped reads pick it up, but
	// every event that moved them carries another Repo's dimension.
	statusB := runIn(t, repoB, dbEnv, "status")
	if statusB.exitCode != 0 {
		t.Fatalf("status from repo-b: exit=%d stderr=%q", statusB.exitCode, statusB.stderr)
	}
	t.Logf("wip status (repo-b's clone, after the cross-repo Run):\n%s", statusB.stdout)
	for _, want := range []string{"matter-b1", "matter-b2"} {
		if !strings.Contains(statusB.stdout, want) {
			t.Errorf("status from repo-b lacks %s:\n%s", want, statusB.stdout)
		}
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
