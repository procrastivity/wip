package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
)

func TestRunReadsAndStandDownEndToEnd(t *testing.T) {
	dir, env := setupRepo(t)
	dbPath := strings.TrimPrefix(env[0], "WIP_DB_PATH=")
	runtimeDir := t.TempDir()
	t.Setenv("WIP_RUNTIME_DIR", runtimeDir)
	env = append(env, "WIP_RUNTIME_DIR="+runtimeDir)
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	repos, err := s.Repos(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, err=%v", repos, err)
	}
	clones, err := s.ClonesOfRepo(ctx, repos[0].ID)
	if err != nil || len(clones) != 1 {
		t.Fatalf("clones = %v, err=%v", clones, err)
	}
	worktrees, err := s.WorktreesOfClone(ctx, clones[0].ID)
	if err != nil || len(worktrees) != 1 {
		t.Fatalf("worktrees = %v, err=%v", worktrees, err)
	}
	envStore := store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	matter := s.NewID()
	batch := s.NewID()
	run := s.NewID()
	dispatch := s.NewID()
	_, err = s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: envStore}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: matter, Payload: store.NodeBirth{Title: "Run Matter", Locator: "run-matter"}},
			{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "run-batch"}, Cause: 0},
			{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: batch, Locator: "run-01", Matters: []string{matter}}, Cause: 1},
			{Type: store.TypeDispatchOpened, Subject: dispatch, Payload: store.DispatchOpened{Run: run, Matter: matter}, Cause: 2},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	show := runInDir(t, dir, env, "run", "show", run, "--json")
	if show.exitCode != 0 {
		t.Fatalf("run show exit=%d stderr=%q", show.exitCode, show.stderr)
	}
	var shown struct {
		ID            string  `json:"id"`
		State         string  `json:"state"`
		Liveness      *string `json:"liveness"`
		LivenessCause *string `json:"liveness_cause"`
		Ownership     string  `json:"ownership"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &shown); err != nil {
		t.Fatalf("run show JSON: %v (%q)", err, show.stdout)
	}
	if shown.ID != run || shown.State != "open" || shown.Liveness == nil || *shown.Liveness != "interrupted" || shown.LivenessCause != nil || shown.Ownership != "owned" {
		t.Fatalf("run show = %+v", shown)
	}

	listJSON := runInDir(t, dir, env, "run", "list", "--json")
	if listJSON.exitCode != 0 {
		t.Fatalf("run list --json exit=%d stderr=%q", listJSON.exitCode, listJSON.stderr)
	}
	var listed struct {
		Runs []struct {
			ID       string  `json:"id"`
			Liveness *string `json:"liveness"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(listJSON.stdout), &listed); err != nil {
		t.Fatalf("run list JSON: %v (%q)", err, listJSON.stdout)
	}
	if len(listed.Runs) != 1 || listed.Runs[0].ID != run || listed.Runs[0].Liveness == nil || *listed.Runs[0].Liveness != "interrupted" {
		t.Fatalf("run list = %+v", listed)
	}
	listHuman := runInDir(t, dir, env, "run", "list")
	if listHuman.exitCode != 0 || !strings.Contains(listHuman.stdout, run) || !strings.Contains(listHuman.stdout, "interrupted") || !strings.Contains(listHuman.stdout, "owned") {
		t.Fatalf("run list human = %+v", listHuman)
	}

	otherDir := newGitRepo(t, "run-reader-other-clone")
	if initialized := runInDir(t, otherDir, env, "init"); initialized.exitCode != 0 {
		t.Fatalf("second clone init exit=%d stderr=%q", initialized.exitCode, initialized.stderr)
	}
	strandedFree := runInDir(t, otherDir, env, "run", "show", run, "--json")
	if strandedFree.exitCode != 0 {
		t.Fatalf("stranded free Run show exit=%d stderr=%q", strandedFree.exitCode, strandedFree.stderr)
	}
	var stranded struct {
		Liveness  *string `json:"liveness"`
		Ownership string  `json:"ownership"`
	}
	if err := json.Unmarshal([]byte(strandedFree.stdout), &stranded); err != nil || stranded.Liveness == nil || *stranded.Liveness != "interrupted" || stranded.Ownership != "stranded" {
		t.Fatalf("stranded free Run show = %+v, err=%v", stranded, err)
	}

	liveLock, err := runlock.Acquire(run)
	if err != nil {
		t.Fatal(err)
	}
	liveShow := runInDir(t, otherDir, env, "run", "show", run, "--json")
	_ = liveLock.Release()
	if liveShow.exitCode != 0 {
		t.Fatalf("held stranded Run show exit=%d stderr=%q", liveShow.exitCode, liveShow.stderr)
	}
	var held struct {
		Liveness  *string `json:"liveness"`
		Ownership string  `json:"ownership"`
	}
	if err := json.Unmarshal([]byte(liveShow.stdout), &held); err != nil || held.Liveness == nil || *held.Liveness != "live" || held.Ownership != "stranded" {
		t.Fatalf("held stranded Run show = %+v, err=%v", held, err)
	}

	stood := runInDir(t, dir, env, "run", "stand-down", run, "--json")
	if stood.exitCode != 0 {
		t.Fatalf("run stand-down exit=%d stderr=%q", stood.exitCode, stood.stderr)
	}
	var result struct {
		Run              string   `json:"run"`
		State            string   `json:"state"`
		Reason           string   `json:"reason"`
		ReapedDispatches []string `json:"reaped_dispatches"`
	}
	if err := json.Unmarshal([]byte(stood.stdout), &result); err != nil {
		t.Fatalf("stand-down JSON: %v (%q)", err, stood.stdout)
	}
	if result.Run != run || result.State != "closed" || result.Reason != "stood-down" || len(result.ReapedDispatches) != 1 || result.ReapedDispatches[0] != dispatch {
		t.Fatalf("stand-down result = %+v", result)
	}

	human := runInDir(t, dir, env, "run", "show", run)
	if human.exitCode != 0 || !strings.Contains(human.stdout, "liveness: -") || !strings.Contains(human.stdout, "ownership: owned") {
		t.Fatalf("closed human show = %+v", human)
	}
	if help := runInDir(t, dir, env, "--help"); help.exitCode != 0 || !strings.Contains(help.stdout, "run") {
		t.Fatalf("root help does not list run: %+v", help)
	}

	manifest := runInDir(t, dir, env, "manifest", "--json")
	if manifest.exitCode != 0 || !strings.Contains(manifest.stdout, `"name":"run stand-down"`) || !strings.Contains(manifest.stdout, `"kind":"plumbing"`) {
		t.Fatalf("manifest does not project run stand-down: %+v", manifest)
	}
	badIdentity := runInDir(t, dir, env, "run", "show", "short", "--json")
	if badIdentity.exitCode != 1 || !strings.Contains(badIdentity.stderr, "validation.invalid-run-identity") {
		t.Fatalf("malformed Run read = %+v", badIdentity)
	}
	unknown := runInDir(t, dir, env, "run", "show", "01KZ842G5HB589KF5P525DQYA2", "--json")
	if unknown.exitCode != 1 || !strings.Contains(unknown.stderr, "validation.unknown-run") {
		t.Fatalf("unknown Run read = %+v", unknown)
	}

	skillsDir := t.TempDir()
	installed := runInDir(t, dir, []string{"WIP_PI_SKILLS_DIR=" + skillsDir}, "install", "pi", "--json")
	if installed.exitCode != 0 {
		t.Fatalf("pi harness install exit=%d stderr=%q", installed.exitCode, installed.stderr)
	}
	var installPayload struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(installed.stdout), &installPayload); err != nil {
		t.Fatalf("pi install JSON: %v (%q)", err, installed.stdout)
	}
	skill, err := os.ReadFile(filepath.Join(installPayload.Dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "run stand-down") || strings.Contains(string(skill), "run resume") {
		t.Fatalf("pi harness Run projection = %q", skill)
	}

	lock, err := runlock.Acquire(run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	before, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refused := runInDir(t, dir, env, "run", "stand-down", run, "--json")
	if refused.exitCode == 0 || !strings.Contains(refused.stderr, "refusal.run-closed") {
		t.Fatalf("closed stand-down refusal = %+v", refused)
	}
	after, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("refused stand-down changed event count from %d to %d", len(before), len(after))
	}
}
