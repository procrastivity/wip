// End-to-end tests for the scheduler Matter's read surface: `wip next`'s
// parallel-frontier display for an open Run (parallelism-decisions.md's
// ratified draft). The Run itself is seeded directly through the store —
// starting a Run is control-plane and has no CLI verb (S6), which is itself
// the posture under test: the read shows, and only shows.
package cli_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestNext_ParallelFrontierForAnOpenRun(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Docs refresh", "--json").stdout)
	var steps []nodePayload
	for _, title := range []string{"three", "four", "five"} {
		steps = append(steps, mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", title, "--json").stdout))
	}
	blocked := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "join", "--json").stdout)
	for _, s := range steps {
		if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", m.Locator+"/"+blocked.Locator, "--blocked-by", m.Locator+"/"+s.Locator); r.exitCode != 0 {
			t.Fatalf("depend add: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Seed the open Run directly: control-plane, no CLI path (S6).
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	repos, _ := s.Repos(ctx)
	clones, _ := s.ClonesOfRepo(ctx, repos[0].ID)
	worktrees, _ := s.WorktreesOfClone(ctx, clones[0].ID)
	env := store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	batch, run := s.NewID(), s.NewID()
	_, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{
			{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "release-docs"}},
			{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: batch, Locator: "run-02", Matters: []string{m.ID}}, Cause: 0},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	r := runIn(t, dir, dbEnv, "next")
	if r.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, want := range []string{
		"release-docs · run-02",
		"Run · open",
		"3 ready in parallel · cap 1 · 1 slot available",
		"order shown is presentation-only",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("next output missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, blocked.Locator) {
		t.Errorf("the blocked join step appears in the frontier:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "pending gate") {
		t.Errorf("the unchanged Run frontier gained pending-gate output:\n%s", r.stdout)
	}

	// JSON carries the same facts.
	j := runIn(t, dir, dbEnv, "next", "--json")
	frontier := mustJSON[struct {
		Run   string `json:"run"`
		Batch string `json:"batch"`
		Cap   int    `json:"cap"`
		Slots int    `json:"slots"`
		Ready []struct {
			Address string `json:"address"`
		} `json:"ready"`
	}](t, j.stdout)
	if frontier.Run != run || frontier.Batch != "release-docs" || frontier.Cap != 1 || frontier.Slots != 1 || len(frontier.Ready) != 3 {
		t.Fatalf("frontier json = %+v", frontier)
	}
	if strings.Contains(j.stdout, "pendingGates") {
		t.Errorf("the unchanged Run frontier gained a pendingGates field: %s", j.stdout)
	}

	// With one Ready node the singular output stands: satisfy two edges so
	// only the join remains.
	for _, st := range steps[:2] {
		if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator+"/"+st.Locator); r.exitCode != 0 {
			t.Fatalf("start step: %q", r.stderr)
		}
		if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator+"/"+st.Locator); r.exitCode != 0 {
			t.Fatalf("finish step: %q", r.stderr)
		}
	}
	// Frontier: steps[2] alone (join still blocked) — singular, so the run
	// block must not render.
	r = runIn(t, dir, dbEnv, "next")
	if r.exitCode != 0 {
		t.Fatalf("next singular: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if strings.Contains(r.stdout, "ready in parallel") {
		t.Errorf("singular frontier still rendered the run block:\n%s", r.stdout)
	}
}

// TestNav_RunFrontierJSON_KindDiscriminatorAndSharedNodeJSON is contract
// tests 3.15 and 3.17: the Run-frontier payload gains "kind":"run-frontier"
// as its first field, ready[] entries use the shared nodeJSON (id/address/
// kind unchanged, lifecycle/matter/generatedDir additive), run/locator/
// batch/cap/slots stay untouched, and the human frontier lines gain no new
// line (list shapes rest on the JSON surface alone, §3.4).
func TestNav_RunFrontierJSON_KindDiscriminatorAndSharedNodeJSON(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	root := worktreeRoot(t, dir)
	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Docs refresh (nav)", "--json").stdout)
	for _, title := range []string{"three", "four"} {
		_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", title, "--json").stdout)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath)
	ctx := context.Background()
	repos, _ := s.Repos(ctx)
	clones, _ := s.ClonesOfRepo(ctx, repos[0].ID)
	worktrees, _ := s.WorktreesOfClone(ctx, clones[0].ID)
	env := store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	batch, run := s.NewID(), s.NewID()
	_, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{
			{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "release-docs-nav"}},
			{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: batch, Locator: "run-03", Matters: []string{m.ID}}, Cause: 0},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	human := runIn(t, dir, dbEnv, "next")
	if human.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	for _, want := range []string{"release-docs-nav · run-03", "Run · open", "2 ready in parallel", "order shown is presentation-only"} {
		if !strings.Contains(human.stdout, want) {
			t.Errorf("next output missing %q:\n%s", want, human.stdout)
		}
	}
	if strings.Contains(human.stdout, "generated:") {
		t.Errorf("run-frontier human output gained a generated: line:\n%s", human.stdout)
	}

	j := runIn(t, dir, dbEnv, "next", "--json")
	if j.exitCode != 0 {
		t.Fatalf("next --json: exit=%d stderr=%q", j.exitCode, j.stderr)
	}
	if !strings.HasPrefix(j.stdout, `{"kind":"run-frontier",`) {
		t.Fatalf("next --json = %q, want it to start with the kind discriminator", j.stdout)
	}
	var payload struct {
		Kind    string `json:"kind"`
		Run     string `json:"run"`
		Locator string `json:"locator"`
		Batch   string `json:"batch"`
		Cap     int    `json:"cap"`
		Slots   int    `json:"slots"`
		Ready   []struct {
			ID           string `json:"id"`
			Address      string `json:"address"`
			Kind         string `json:"kind"`
			Lifecycle    string `json:"lifecycle"`
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(j.stdout), &payload); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, j.stdout)
	}
	if payload.Run != run || payload.Locator != "run-03" || payload.Batch != "release-docs-nav" || payload.Cap != 1 || payload.Slots != 1 {
		t.Fatalf("frontier fields = %+v", payload)
	}
	if len(payload.Ready) != 2 {
		t.Fatalf("ready = %+v, want 2 entries", payload.Ready)
	}
	wantDir := filepath.Join(root, ".wip", "generated", m.Locator)
	for _, entry := range payload.Ready {
		if entry.Kind != "step" || entry.Lifecycle != "planned" {
			t.Errorf("ready entry = %+v, want kind=step lifecycle=planned", entry)
		}
		if entry.Matter != m.Locator {
			t.Errorf("ready entry matter = %q, want %q", entry.Matter, m.Locator)
		}
		if entry.GeneratedDir != wantDir {
			t.Errorf("ready entry generatedDir = %q, want %q", entry.GeneratedDir, wantDir)
		}
	}
}

// TestNav_RunFrontierJSON_CrossMatterDistinctGeneratedDir is contract test
// 3.16: a Batch joining two Matters yields one distinct generatedDir per
// ready node — one per Matter, never a shared or per-result value.
func TestNav_RunFrontierJSON_CrossMatterDistinctGeneratedDir(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	root := worktreeRoot(t, dir)
	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")

	alpha := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Alpha work", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", alpha.Locator, "--title", "one", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", alpha.Locator); r.exitCode != 0 {
		t.Fatalf("start alpha: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	beta := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Beta work", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", beta.Locator, "--title", "one", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", beta.Locator); r.exitCode != 0 {
		t.Fatalf("start beta: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath)
	ctx := context.Background()
	repos, _ := s.Repos(ctx)
	clones, _ := s.ClonesOfRepo(ctx, repos[0].ID)
	worktrees, _ := s.WorktreesOfClone(ctx, clones[0].ID)
	env := store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	batch, run := s.NewID(), s.NewID()
	_, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{
			{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "cross-matter"}},
			{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: batch, Locator: "run-04", Matters: []string{alpha.ID, beta.ID}}, Cause: 0},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	j := runIn(t, dir, dbEnv, "next", "--json")
	if j.exitCode != 0 {
		t.Fatalf("next --json: exit=%d stderr=%q", j.exitCode, j.stderr)
	}
	var payload struct {
		Ready []struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(j.stdout), &payload); err != nil {
		t.Fatalf("decode: %v (stdout=%q)", err, j.stdout)
	}
	if len(payload.Ready) != 2 {
		t.Fatalf("ready = %+v, want 2 entries (one per Matter)", payload.Ready)
	}
	wantAlpha := filepath.Join(root, ".wip", "generated", alpha.Locator)
	wantBeta := filepath.Join(root, ".wip", "generated", beta.Locator)
	gotDirs := map[string]bool{payload.Ready[0].GeneratedDir: true, payload.Ready[1].GeneratedDir: true}
	if !gotDirs[wantAlpha] || !gotDirs[wantBeta] || len(gotDirs) != 2 {
		t.Errorf("ready = %+v, want distinct generatedDir %q and %q", payload.Ready, wantAlpha, wantBeta)
	}
}
