// End-to-end tests for the scheduler Matter's read surface: `wip next`'s
// parallel-frontier display for an open Run (parallelism-decisions.md's
// ratified draft). The Run itself is seeded directly through the store —
// starting a Run is control-plane and has no CLI verb (S6), which is itself
// the posture under test: the read shows, and only shows.
package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func TestNext_ParallelFrontierForAnOpenRun(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Docs refresh", "--json").stdout)
	var steps []nodePayload
	for _, title := range []string{"three", "four", "five"} {
		steps = append(steps, mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", title, "--json").stdout))
	}
	blocked := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "step", "create", m.Locator, "--title", "join", "--json").stdout)
	for _, s := range steps {
		if r := runIn(t, dir, dbEnv, "depend", "add", m.Locator+"/"+blocked.Locator, "--blocked-by", m.Locator+"/"+s.Locator); r.exitCode != 0 {
			t.Fatalf("depend add: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
	}
	if r := runIn(t, dir, dbEnv, "start", m.Locator); r.exitCode != 0 {
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

	// With one Ready node the singular output stands: satisfy two edges so
	// only the join remains.
	for _, st := range steps[:2] {
		if r := runIn(t, dir, dbEnv, "start", m.Locator+"/"+st.Locator); r.exitCode != 0 {
			t.Fatalf("start step: %q", r.stderr)
		}
		if r := runIn(t, dir, dbEnv, "finish", m.Locator+"/"+st.Locator); r.exitCode != 0 {
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
