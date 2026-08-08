// End-to-end tests for the scheduler Matter's seal condition (step-05): an
// Orchestrator consumes a real multi-member Batch through the CLI-created
// store, driving the one dispatch path (MODEL §6) directly — starting a Run
// and running a pass are both control-plane with no CLI verb (S6; run.go's
// own doc says as much), exactly like scheduler_e2e_test.go's Run seeding.
// These tests seed real Matters/Steps/edges/gates through the built binary,
// seed the Batch and Run the same way scheduler_e2e_test.go and
// run_e2e_test.go do, then drive scheduler.Orchestrate in-process against
// that same store, and assert the result back through CLI reads and the
// event log.
package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/scheduler"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// orchestratorEnv resolves the one Repo/Clone/Worktree setupRepo seeded —
// the tier context every direct Run/Batch commit and every Orchestrate call
// below needs (D56).
func orchestratorEnv(t *testing.T, s *store.Store) store.Env {
	t.Helper()
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
	return store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
}

// startRunDirect seeds run.started over an already-live Batch, in whatever
// member order the caller hands it — control-plane, no CLI verb (S6).
func startRunDirect(t *testing.T, s *store.Store, env store.Env, batch, locator string, matters ...string) store.Run {
	t.Helper()
	ctx := context.Background()
	var id string
	_, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{Type: store.TypeRunStarted, Subject: id, Payload: store.RunStarted{
			Batch: batch, Locator: locator, Matters: matters,
		}}}, nil
	})
	if err != nil {
		t.Fatalf("seed run.started: %v", err)
	}
	run, err := s.Run(ctx, id)
	if err != nil {
		t.Fatalf("read seeded run: %v", err)
	}
	return run
}

func createMatter(t *testing.T, dir string, env []string, title string) nodePayload {
	t.Helper()
	r := runIn(t, dir, env, "matter", "create", "--title", title, "--json")
	if r.exitCode != 0 {
		t.Fatalf("matter create %q: exit=%d stderr=%q", title, r.exitCode, r.stderr)
	}
	return mustJSON[nodePayload](t, r.stdout)
}

func joinBatch(t *testing.T, dir string, env []string, batchName, matterLocator string) {
	t.Helper()
	if r := runIn(t, dir, env, "batch", "join", batchName, matterLocator); r.exitCode != 0 {
		t.Fatalf("batch join %s: exit=%d stderr=%q", matterLocator, r.exitCode, r.stderr)
	}
}

func addDependency(t *testing.T, dir string, env []string, blocked, blocker string) {
	t.Helper()
	if r := runIn(t, dir, env, "depend", "add", blocked, "--blocked-by", blocker); r.exitCode != 0 {
		t.Fatalf("depend add %s <- %s: exit=%d stderr=%q", blocked, blocker, r.exitCode, r.stderr)
	}
}

// noopHooks is a Work hook that does nothing but record engagement order —
// the same shape as scheduler's own workRecorder, since the porcelain's
// actual build/edit work is out of this Matter's scope (loop.go's own doc:
// "the engine drives, it does not think").
type noopHooks struct{ order []string }

func (w *noopHooks) work(_ context.Context, n store.Node) error {
	w.order = append(w.order, n.ID)
	return nil
}

// ---------------------------------------------------------------------------
// A real multi-member Batch, end to end.
// ---------------------------------------------------------------------------

// TestOrchestrator_MultiMemberBatch_OrderDerivesFromEdgesAlone runs
// parallelism-decisions.md's Case D (a shared blocker, an independent
// sibling, cap 2) through the real CLI surface for everything that has one,
// and proves the observed order comes only from the in-force `blocked-by`
// edges: run.started's own Matters slice is handed in a scrambled order that
// contradicts both edges and creation order, and the pass still finishes
// with C and D's work only ever starting after A is Done (D24, D63, D64).
func TestOrchestrator_MultiMemberBatch_OrderDerivesFromEdgesAlone(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	a := createMatter(t, dir, dbEnv, "A")
	b := createMatter(t, dir, dbEnv, "B")
	c := createMatter(t, dir, dbEnv, "C")
	d := createMatter(t, dir, dbEnv, "D")
	addDependency(t, dir, dbEnv, c.Locator, a.Locator)
	addDependency(t, dir, dbEnv, d.Locator, a.Locator)

	if r := runIn(t, dir, dbEnv, "batch", "create", "case-d", "--json"); r.exitCode != 0 {
		t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	for _, m := range []nodePayload{a, b, c, d} {
		joinBatch(t, dir, dbEnv, "case-d", m.Locator)
	}

	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	env := orchestratorEnv(t, s)
	batch, err := s.BatchByName(ctx, "case-d")
	if err != nil {
		t.Fatalf("read seeded batch: %v", err)
	}
	// Deliberately not creation order and not dependency order: proves the
	// Run's own frozen-membership slice cannot be read as a sequence (D24).
	run := startRunDirect(t, s, env, batch.ID, "run-01", d.ID, b.ID, c.ID, a.ID)

	w := &noopHooks{}
	seenBeforeAReady := map[string]bool{} // records A's lifecycle when C or D's hook fires
	hooks := scheduler.Hooks{
		Work: func(ctx context.Context, n store.Node) error {
			if n.Matter == c.ID || n.Matter == d.ID {
				fresh, err := s.Node(ctx, a.ID)
				if err != nil {
					t.Fatal(err)
				}
				seenBeforeAReady[n.Matter] = fresh.Lifecycle == store.Done
			}
			return w.work(ctx, n)
		},
	}
	out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 2, scheduler.Policy{}, hooks)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if out.State != scheduler.StateFinished {
		t.Fatalf("state = %s, want finished (skips: %+v)", out.State, out.Skipped)
	}
	if len(out.Worked) != 4 {
		t.Fatalf("worked %d nodes, want 4 (one per member Matter, none planned)", len(out.Worked))
	}
	if !seenBeforeAReady[c.ID] || !seenBeforeAReady[d.ID] {
		t.Fatalf("C or D worked before A reached Done — edges were not the ordering source: %+v", seenBeforeAReady)
	}
	// The scrambled Matters slice changed nothing about which member worked
	// first: A must precede both C and D in the recorded trace regardless of
	// its position (last) in the Run's frozen membership order.
	posA, posC, posD := indexOf(w.order, a.ID), indexOf(w.order, c.ID), indexOf(w.order, d.ID)
	if posA < 0 || posC < 0 || posD < 0 || posA > posC || posA > posD {
		t.Fatalf("work order = %v, want A before both C and D", w.order)
	}

	closedRun, _ := s.Run(ctx, run.ID)
	if closedRun.Open || closedRun.CloseReason != store.CloseCompleted {
		t.Fatalf("run = %+v, want closed completed", closedRun)
	}

	// The result reads back through the real CLI surface too.
	show := runIn(t, dir, dbEnv, "run", "show", run.ID, "--json")
	if show.exitCode != 0 {
		t.Fatalf("run show: exit=%d stderr=%q", show.exitCode, show.stderr)
	}
	var shown struct {
		State       string `json:"state"`
		CloseReason string `json:"close_reason"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &shown); err != nil || shown.State != "closed" || shown.CloseReason != "completed" {
		t.Fatalf("run show = %+v, err=%v", shown, err)
	}
}

func indexOf(order []string, id string) int {
	for i, v := range order {
		if v == id {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Cap, including cap 1 as plain sequential.
// ---------------------------------------------------------------------------

// TestOrchestrator_CapRespected_IncludingCapOne runs the same three
// independent (edge-free) Matters under cap 1 and cap 3, probing
// scheduler.Derive from inside the Work hook — while the current node is the
// only one the pass has touched — to show the accounting the cap produces:
// under cap 1 no slot is ever free while a member is engaged (plain
// sequential, D31); under cap 3 the same single-goroutine pass still worked
// one member at a time, but with slots open the whole time, proving the cap
// never forced anything and a smaller cap is what removes the headroom.
func TestOrchestrator_CapRespected_IncludingCapOne(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cap           int
		wantSlotsSeen []int // per member, in ID/engagement order
	}{
		{name: "cap-1-plain-sequential", cap: 1, wantSlotsSeen: []int{0, 0, 0}},
		{name: "cap-3-headroom-but-still-worked-one-at-a-time", cap: 3, wantSlotsSeen: []int{2, 2, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, dbEnv := setupRepo(t)
			if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
				t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
			}
			var members []nodePayload
			for _, title := range []string{"one", "two", "three"} {
				members = append(members, createMatter(t, dir, dbEnv, title))
			}
			if r := runIn(t, dir, dbEnv, "batch", "create", "cap-batch", "--json"); r.exitCode != 0 {
				t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
			}
			var ids []string
			for _, m := range members {
				joinBatch(t, dir, dbEnv, "cap-batch", m.Locator)
				ids = append(ids, m.ID)
			}

			dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
			s := openTestStore(t, dbPath)
			ctx := context.Background()
			env := orchestratorEnv(t, s)
			batch, err := s.BatchByName(ctx, "cap-batch")
			if err != nil {
				t.Fatal(err)
			}
			run := startRunDirect(t, s, env, batch.ID, "run-01", ids...)

			var slotsSeen []int
			hooks := scheduler.Hooks{
				Work: func(ctx context.Context, n store.Node) error {
					fr, err := scheduler.Derive(ctx, s.View, run, tc.cap)
					if err != nil {
						return err
					}
					slotsSeen = append(slotsSeen, fr.Slots)
					if len(fr.Engaged) != 1 || fr.Engaged[0].ID != n.ID {
						t.Errorf("engaged during %s = %+v, want just itself", n.ID, fr.Engaged)
					}
					return nil
				},
			}
			out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, tc.cap, scheduler.Policy{}, hooks)
			if err != nil {
				t.Fatalf("Orchestrate: %v", err)
			}
			if out.State != scheduler.StateFinished {
				t.Fatalf("state = %s, want finished (skips: %+v)", out.State, out.Skipped)
			}
			sort.Ints(slotsSeen)
			sort.Ints(tc.wantSlotsSeen)
			if len(slotsSeen) != len(tc.wantSlotsSeen) {
				t.Fatalf("slots seen = %v, want %d observations", slotsSeen, len(tc.wantSlotsSeen))
			}
			for i := range slotsSeen {
				if slotsSeen[i] != tc.wantSlotsSeen[i] {
					t.Fatalf("slots seen = %v, want %v", slotsSeen, tc.wantSlotsSeen)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Skip, halt, and park — each reachable and each evented.
// ---------------------------------------------------------------------------

// TestOrchestrator_SkipReachableAndEvented covers two of D30's skip reasons
// (blocked and failed) under the default skip handling: the pass emits
// run.skipped, keeps going, and leaves the Run standing by rather than
// closing it.
func TestOrchestrator_SkipReachableAndEvented(t *testing.T) {
	t.Run("blocked", func(t *testing.T) {
		dir, dbEnv := setupRepo(t)
		if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
			t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		outside := createMatter(t, dir, dbEnv, "Outside the batch")
		waiting := createMatter(t, dir, dbEnv, "Waiting on it")
		free := createMatter(t, dir, dbEnv, "Free")
		addDependency(t, dir, dbEnv, waiting.Locator, outside.Locator)

		if r := runIn(t, dir, dbEnv, "batch", "create", "blocked-batch", "--json"); r.exitCode != 0 {
			t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		joinBatch(t, dir, dbEnv, "blocked-batch", waiting.Locator)
		joinBatch(t, dir, dbEnv, "blocked-batch", free.Locator)

		dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
		s := openTestStore(t, dbPath)
		ctx := context.Background()
		env := orchestratorEnv(t, s)
		batch, err := s.BatchByName(ctx, "blocked-batch")
		if err != nil {
			t.Fatal(err)
		}
		run := startRunDirect(t, s, env, batch.ID, "run-01", waiting.ID, free.ID)

		w := &noopHooks{}
		out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1, scheduler.Policy{}, scheduler.Hooks{Work: w.work})
		if err != nil {
			t.Fatalf("Orchestrate: %v", err)
		}
		if out.State != scheduler.StateStandingBy {
			t.Fatalf("state = %s, want standing-by", out.State)
		}
		if len(out.Skipped) != 1 || out.Skipped[0].Matter.ID != waiting.ID || out.Skipped[0].Reason != store.RunSkipBlocked {
			t.Fatalf("skipped = %+v, want blocked on %s", out.Skipped, waiting.ID)
		}
		mustHaveRunSkipped(t, s, run.ID, waiting.ID, store.RunSkipBlocked)

		openRun, _ := s.Run(ctx, run.ID)
		if !openRun.Open {
			t.Fatalf("run closed %s, want open and standing by", openRun.CloseReason)
		}
	})

	t.Run("failed", func(t *testing.T) {
		dir, dbEnv := setupRepo(t)
		if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
			t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		fragile := createMatter(t, dir, dbEnv, "Fragile")
		solid := createMatter(t, dir, dbEnv, "Solid")
		if r := runIn(t, dir, dbEnv, "batch", "create", "fail-batch", "--json"); r.exitCode != 0 {
			t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		joinBatch(t, dir, dbEnv, "fail-batch", fragile.Locator)
		joinBatch(t, dir, dbEnv, "fail-batch", solid.Locator)

		dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
		s := openTestStore(t, dbPath)
		ctx := context.Background()
		env := orchestratorEnv(t, s)
		batch, err := s.BatchByName(ctx, "fail-batch")
		if err != nil {
			t.Fatal(err)
		}
		run := startRunDirect(t, s, env, batch.ID, "run-01", fragile.ID, solid.ID)

		var worked []string
		hooks := scheduler.Hooks{Work: func(_ context.Context, n store.Node) error {
			worked = append(worked, n.ID)
			if n.Matter == fragile.ID {
				return errWorkBroke
			}
			return nil
		}}
		out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1, scheduler.Policy{}, hooks)
		if err != nil {
			t.Fatalf("Orchestrate: %v", err)
		}
		if out.State != scheduler.StateStandingBy {
			t.Fatalf("state = %s, want standing-by", out.State)
		}
		if len(out.Skipped) != 1 || out.Skipped[0].Matter.ID != fragile.ID || out.Skipped[0].Reason != store.RunSkipFailed {
			t.Fatalf("skipped = %+v, want failed on %s", out.Skipped, fragile.ID)
		}
		if len(worked) != 2 {
			t.Fatalf("worked = %v, want both attempted (skip continues the pass)", worked)
		}
		mustHaveRunSkipped(t, s, run.ID, fragile.ID, store.RunSkipFailed)
	})
}

var errWorkBroke = errors.New("the build broke")

// TestOrchestrator_HaltReachableAndEvented covers D30's halt handling: the
// pass still emits run.skipped for the failure that triggered it, but stops
// rather than continuing to the next Ready member, and the Run is left open
// (never run.finished — a halted pass decides nothing about the Batch).
func TestOrchestrator_HaltReachableAndEvented(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	first := createMatter(t, dir, dbEnv, "First")
	second := createMatter(t, dir, dbEnv, "Second")
	if r := runIn(t, dir, dbEnv, "batch", "create", "halt-batch", "--json"); r.exitCode != 0 {
		t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	joinBatch(t, dir, dbEnv, "halt-batch", first.Locator)
	joinBatch(t, dir, dbEnv, "halt-batch", second.Locator)

	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	env := orchestratorEnv(t, s)
	batch, err := s.BatchByName(ctx, "halt-batch")
	if err != nil {
		t.Fatal(err)
	}
	run := startRunDirect(t, s, env, batch.ID, "run-01", first.ID, second.ID)

	var worked []string
	hooks := scheduler.Hooks{Work: func(_ context.Context, n store.Node) error {
		worked = append(worked, n.ID)
		return errWorkBroke
	}}
	out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1, scheduler.Policy{Failed: scheduler.HandleHalt}, hooks)
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if out.State != scheduler.StateHalted || out.HaltedOn == nil || out.HaltedOn.Reason != store.RunSkipFailed {
		t.Fatalf("outcome = %+v, want halted on a failure", out)
	}
	if len(worked) != 1 || worked[0] != first.ID {
		t.Fatalf("worked = %v, want only the first member before the halt", worked)
	}
	mustHaveRunSkipped(t, s, run.ID, first.ID, store.RunSkipFailed)

	openRun, _ := s.Run(ctx, run.ID)
	if !openRun.Open {
		t.Fatalf("run closed %s, want open — a halted pass finishes nothing", openRun.CloseReason)
	}

	show := runIn(t, dir, dbEnv, "run", "show", run.ID, "--json")
	if show.exitCode != 0 {
		t.Fatalf("run show: exit=%d stderr=%q", show.exitCode, show.stderr)
	}
	var shown struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &shown); err != nil || shown.State != "open" {
		t.Fatalf("run show = %+v, err=%v", shown, err)
	}
}

// TestOrchestrator_ParkReachableAndEvented covers D60: a member Matter
// finishes its work but sits behind a declared human-owned gate, so the pass
// parks it — a third normal outcome, not a skip — and the Run stands by
// rather than finishing, with no run.skipped for the parked member.
func TestOrchestrator_ParkReachableAndEvented(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	gated := createMatter(t, dir, dbEnv, "Gated")
	if r := runIn(t, dir, dbEnv, "step", "create", gated.Locator, "--title", "the work", "--json"); r.exitCode != 0 {
		t.Fatalf("step create: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "batch", "create", "gated-batch", "--json"); r.exitCode != 0 {
		t.Fatalf("batch create: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	joinBatch(t, dir, dbEnv, "gated-batch", gated.Locator)

	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	env := orchestratorEnv(t, s)
	batch, err := s.BatchByName(ctx, "gated-batch")
	if err != nil {
		t.Fatal(err)
	}
	run := startRunDirect(t, s, env, batch.ID, "run-01", gated.ID)

	w := &noopHooks{}
	out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1, scheduler.Policy{}, scheduler.Hooks{Work: w.work})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}
	if out.State != scheduler.StateStandingBy {
		t.Fatalf("state = %s, want standing-by (D60: parked, not failed)", out.State)
	}
	if len(out.Parked) != 1 || out.Parked[0].ID != gated.ID {
		t.Fatalf("parked = %+v, want the gated matter", out.Parked)
	}
	if len(out.Skipped) != 0 {
		t.Fatalf("skipped = %+v, want none — awaiting a gate is not a skip", out.Skipped)
	}
	events, err := s.EventsOfSubject(ctx, gated.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == store.TypeRunSkipped {
			t.Fatalf("parked member carries a run.skipped event: %+v", ev)
		}
	}

	openRun, _ := s.Run(ctx, run.ID)
	if !openRun.Open {
		t.Fatalf("run closed %s, want open and standing by", openRun.CloseReason)
	}

	show := runIn(t, dir, dbEnv, "run", "show", run.ID, "--json")
	if show.exitCode != 0 {
		t.Fatalf("run show: exit=%d stderr=%q", show.exitCode, show.stderr)
	}
	var shown struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &shown); err != nil || shown.State != "open" {
		t.Fatalf("run show = %+v, err=%v", shown, err)
	}
}

func mustHaveRunSkipped(t *testing.T, s *store.Store, run, matter string, reason store.RunSkipReason) {
	t.Helper()
	events, err := s.EventsOfSubject(context.Background(), matter)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type != store.TypeRunSkipped {
			continue
		}
		var p store.RunSkipped
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("run.skipped payload: %v", err)
		}
		if p.Run == run && p.Reason == reason {
			return
		}
	}
	t.Fatalf("no run.skipped(run=%s, reason=%s) on %s", run, reason, matter)
}

// ---------------------------------------------------------------------------
// The bare "work this Matter" path: one Matter, an anonymous Batch of one,
// the same Orchestrate function — one dispatch path (MODEL §6).
// ---------------------------------------------------------------------------

// TestOrchestrator_AnonymousBatchWrapsBareMatter proves the second dispatch
// path the seal condition names is not actually a second path: a bare
// Matter with no user-facing Batch still runs through
// writesurface.CreateAnonymousBatch and the very same scheduler.Orchestrate,
// with no branch in the engine for "anonymous" versus "named."
func TestOrchestrator_AnonymousBatchWrapsBareMatter(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "refresh"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	solo := createMatter(t, dir, dbEnv, "Solo")

	dbPath := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
	s := openTestStore(t, dbPath)
	ctx := context.Background()
	env := orchestratorEnv(t, s)

	anon, err := writesurface.CreateAnonymousBatch(ctx, s, store.ActorHuman, env, solo.ID)
	if err != nil {
		t.Fatalf("CreateAnonymousBatch: %v", err)
	}
	if anon.Name != "" || anon.Matter != solo.ID {
		t.Fatalf("anonymous batch = %+v, want no name and Matter=%s", anon, solo.ID)
	}
	// Re-derived, the same anonymous Batch comes back — one per Matter.
	again, found, err := s.AnonymousBatchForMatter(ctx, solo.ID)
	if err != nil || !found || again.ID != anon.ID {
		t.Fatalf("AnonymousBatchForMatter = %+v, found=%v, err=%v, want %s", again, found, err, anon.ID)
	}

	run := startRunDirect(t, s, env, anon.ID, "run-01", solo.ID)
	w := &noopHooks{}
	out, err := scheduler.Orchestrate(ctx, s, store.ActorHuman, env, run.ID, 1, scheduler.Policy{}, scheduler.Hooks{Work: w.work})
	if err != nil {
		t.Fatalf("Orchestrate over an anonymous Batch: %v", err)
	}
	if out.State != scheduler.StateFinished {
		t.Fatalf("state = %s, want finished (skips: %+v)", out.State, out.Skipped)
	}
	if len(w.order) != 1 || w.order[0] != solo.ID {
		t.Fatalf("worked = %v, want just the solo Matter", w.order)
	}
	matter, err := s.Node(ctx, solo.ID)
	if err != nil || matter.Lifecycle != store.Done {
		t.Fatalf("matter = %+v, err=%v, want done", matter, err)
	}
}

// ---------------------------------------------------------------------------
// D24: a Batch declaring its own sequence is impossible by construction.
// ---------------------------------------------------------------------------

// TestOrchestrator_BatchDeclaredSequenceIsImpossibleByConstruction checks
// the structural half of D24 directly: neither the payload a named Batch is
// born from nor the payload that freezes a Run's membership carries any
// field that could encode a sequence, and the CLI surface that creates a
// Batch offers no flag for one either. The behavioral half — that execution
// order tracks edges regardless of Run membership order — is
// OrderDerivesFromEdgesAlone, above.
func TestOrchestrator_BatchDeclaredSequenceIsImpossibleByConstruction(t *testing.T) {
	wantFields := func(t *testing.T, v any, want []string) {
		t.Helper()
		typ := reflect.TypeOf(v)
		if typ.NumField() != len(want) {
			t.Fatalf("%s has %d fields, want exactly %v (a new field here is a schema change this test must see)", typ.Name(), typ.NumField(), want)
		}
		for i, name := range want {
			if typ.Field(i).Name != name {
				t.Fatalf("%s field %d = %q, want %q", typ.Name(), i, typ.Field(i).Name, name)
			}
		}
	}
	wantFields(t, store.BatchCreated{}, []string{"Name", "Matter"})
	wantFields(t, store.RunStarted{}, []string{"Batch", "Locator", "Matters"})

	dir, dbEnv := setupRepo(t)
	for _, args := range [][]string{
		{"batch", "create", "--help"},
		{"batch", "join", "--help"},
	} {
		r := runIn(t, dir, dbEnv, args...)
		if r.exitCode != 0 {
			t.Fatalf("%v --help: exit=%d stderr=%q", args, r.exitCode, r.stderr)
		}
		for _, forbidden := range []string{"--sequence", "--order", "--position"} {
			if strings.Contains(r.stdout, forbidden) {
				t.Fatalf("%v --help offers %q — a Batch must not be able to declare its own sequence (D24)", args, forbidden)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// S6: no CLI resume path.
// ---------------------------------------------------------------------------

// TestOrchestrator_NoCLIResumePath is the negative half of S6 (MODEL §11):
// resuming an interrupted Run is engine-only. `wip run` exposes list, show,
// and stand-down and nothing else; a literal `run resume` is an unknown
// subcommand, not a refusal — the surface does not know the verb exists.
func TestOrchestrator_NoCLIResumePath(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	help := runIn(t, dir, dbEnv, "run", "--help")
	if help.exitCode != 0 {
		t.Fatalf("run --help: exit=%d stderr=%q", help.exitCode, help.stderr)
	}
	if strings.Contains(help.stdout, "resume") {
		t.Fatalf("run --help mentions resume: %s", help.stdout)
	}
	for _, want := range []string{"list", "show", "stand-down"} {
		if !strings.Contains(help.stdout, want) {
			t.Fatalf("run --help missing %q:\n%s", want, help.stdout)
		}
	}

	// "resume" matches none of run's subcommands, so cobra falls back to the
	// group's own usage rather than resuming anything — no Run identity was
	// even parsed, let alone acted on.
	unknown := runIn(t, dir, dbEnv, "run", "resume", "01KZ842G5HB589KF5P525DQYA2")
	if unknown.exitCode != 0 || !strings.Contains(unknown.stdout, "Usage:") || strings.Contains(unknown.stdout, "resume") {
		t.Fatalf("run resume = %+v, want the run group's own usage with no mention of resume", unknown)
	}

	manifest := runIn(t, dir, dbEnv, "manifest", "--json")
	if manifest.exitCode != 0 {
		t.Fatalf("manifest: exit=%d stderr=%q", manifest.exitCode, manifest.stderr)
	}
	var m struct {
		Verbs []struct {
			Name string `json:"name"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(manifest.stdout), &m); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	// Lifecycle's own "matter resume" / "step resume" (Paused -> In Progress)
	// are unrelated verbs that happen to share the word; only "run resume"
	// itself is what S6 forbids.
	for _, v := range m.Verbs {
		if v.Name == "run resume" {
			t.Fatalf("manifest projects a run resume verb: %q", v.Name)
		}
	}
}
