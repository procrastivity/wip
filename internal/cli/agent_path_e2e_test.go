// End-to-end tests for the agent-path Matter (workplans/agent-path.md),
// through the actual built binary (binPath, run, runIn, runInStdin, gitIn,
// newGitRepo, setupRepo, openTestStore, dbEnvPath, mustJSON, nodePayload,
// wantAppendedEvent, refreshPayload come from e2e_test.go/tiers_e2e_test.go/
// writesurface_birth_test.go/writesurface_content_test.go/
// render_scratch_e2e_test.go, same package).
//
// Steps 01-05's mechanics — the per-verb argument shapes, --file's
// single-writer consumption, the scratch-dir handoff at `wip refresh`, the
// 0444 message's harness-guidance wiring, and the D40 boundary — were
// already implemented and confirmed by write-surface, render-scratch,
// vocabulary, and manifest-install respectively, each of which read this
// Matter's already-written workplan ahead of its own dispatch
// (docs/agent-path/decisions.md records the confirmation in full). What
// this Matter still owed the register is the tests those upstream Matters
// left for it: step-06's cross-shape half, step-07's acceptance test (the
// seal condition's direct discharge), and step-08's live-dispatch scratch
// consumption. This file carries exactly those three, plus a companion
// negative test for step-05's D40 boundary.
package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

// TestAgentPath_CreateOnce_CrossShapeRefusal is step-06's cross-shape
// half, explicitly asked for by the workplan: "a create-once verb's second
// call via a *different* shape than its first ... is still refused as a
// repeat create-once write ... confirming the shape layer never bypasses
// that property." write-surface's own tests only ever repeat the same shape
// on the refused second call; this covers the case that shape actually
// changes.
func TestAgentPath_CreateOnce_CrossShapeRefusal(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	kinds := map[string]store.ContentKind{
		"brief":    store.KindBrief,
		"workplan": store.KindWorkplan,
		"body":     store.KindBody,
	}
	for verb, kind := range kinds {
		m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Cross-shape "+verb, "--json").stdout)

		// First write: stdin (the default shape).
		if r := runInStdin(t, dir, dbEnv, "first via stdin", verb, m.ID, "--json"); r.exitCode != 0 {
			t.Fatalf("%s (stdin): exit=%d stderr=%q", verb, r.exitCode, r.stderr)
		}
		s := openTestStore(t, dbPath)
		before, err := s.EventsOfSubject(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}

		// Second write, a *different* shape (--file): still refused, not a
		// second content.created for one logical piece of content.
		filePath := filepath.Join(t.TempDir(), verb+".md")
		if err := os.WriteFile(filePath, []byte("second via file, should never land"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused := runIn(t, dir, dbEnv, verb, m.ID, "--file", filePath, "--json")
		if refused.exitCode == 0 {
			t.Errorf("%s: a second create-once call via a different shape (--file after stdin) should be refused", verb)
		}
		s = openTestStore(t, dbPath)
		after, err := s.EventsOfSubject(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("%s: a refused cross-shape second write appended %d events, want 0", verb, len(after)-len(before))
		}

		got, err := s.Content(context.Background(), m.ID, kind)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "first via stdin" {
			t.Errorf("%s: content = %q, want the original stdin write left untouched by the refused --file attempt", verb, got)
		}
	}
}

// TestAgentPath_D40Boundary_UnconsumedScratchFileHasNoEffect is step-05's
// companion negative case to step-08's positive one: a file dropped under
// `.wip/work/<dispatch-id>/` that is *never* passed to a verb via --file has
// no effect on wip state whatsoever — not read, watched, or globbed for by
// any code path, confirming there is no ambient "pick up whatever's in the
// scratch dir" behavior anywhere.
func TestAgentPath_D40Boundary_UnconsumedScratchFileHasNoEffect(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Untouched by drops", "--json").stdout)

	first := mustJSON[refreshPayload](t, runIn(t, dir, dbEnv, "refresh", "--json").stdout)
	if !first.Opened {
		t.Fatal("first refresh should have opened a dispatch")
	}
	if first.ScratchDir == "" {
		t.Fatal("refresh did not report a scratch dir")
	}

	s := openTestStore(t, dbPath)
	before, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// An agent's own draft, dropped in its scratch dir, never handed to any
	// verb via --file.
	dropped := filepath.Join(first.ScratchDir, "unread-draft.md")
	if err := os.WriteFile(dropped, []byte("an agent's own draft, never consumed"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second refresh (same still-open dispatch) just re-renders — it never
	// globs the scratch dir looking for something to pick up.
	if r := runIn(t, dir, dbEnv, "refresh", "--json"); r.exitCode != 0 {
		t.Fatalf("second refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s = openTestStore(t, dbPath)
	after, err := s.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after[len(before):] {
		if e.Type == store.TypeContentCreated || e.Type == store.TypeContentAppended {
			t.Errorf("an unconsumed scratch file produced a content event: %+v", e)
		}
	}

	segs, err := s.ContentSegments(context.Background(), m.ID, store.KindBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 0 {
		t.Errorf("matter carries body content it was never given through a verb")
	}

	data, err := os.ReadFile(dropped)
	if err != nil {
		t.Fatalf("dropped scratch file vanished on its own: %v", err)
	}
	if string(data) != "an agent's own draft, never consumed" {
		t.Errorf("dropped scratch file content changed on its own: %q", data)
	}
}

// TestAgentPath_ScratchFileConsumption_LiveDispatch is step-08: unlike
// step-06/07's direct fixture files, this runs a real `wip refresh`
// (opening an actual dispatch), writes a draft file into the dispatch's
// real `.wip/work/<dispatch-id>/` directory, and consumes it via --file on
// a content verb — confirming the end-to-end path (real dispatch -> real
// scratch dir -> real one-time consumption -> one event) and that the file
// is left untouched afterward (D45/step-02's "never touched, renamed, or
// deleted" property, observed against a live scratch dir).
func TestAgentPath_ScratchFileConsumption_LiveDispatch(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	refreshed := mustJSON[refreshPayload](t, runIn(t, dir, dbEnv, "refresh", "--json").stdout)
	if !refreshed.Opened {
		t.Fatal("refresh should have opened a dispatch")
	}
	if refreshed.ScratchDir == "" || refreshed.Dispatch == "" {
		t.Fatalf("refresh did not report a dispatch-id and scratch dir: %+v", refreshed)
	}
	if filepath.Base(refreshed.ScratchDir) != refreshed.Dispatch {
		t.Errorf("scratch dir %q is not namespaced by its own dispatch-id %q", refreshed.ScratchDir, refreshed.Dispatch)
	}

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Live scratch consumption", "--json").stdout)

	// The agent's own draft, written into the *real* scratch dir a real
	// dispatch opened.
	draftPath := filepath.Join(refreshed.ScratchDir, "brief-draft.md")
	draftContent := "# Brief\n\ndrafted in the live scratch dir\n"
	if err := os.WriteFile(draftPath, []byte(draftContent), 0o644); err != nil {
		t.Fatal(err)
	}

	r := runIn(t, dir, dbEnv, "brief", m.ID, "--file", draftPath, "--json")
	if r.exitCode != 0 {
		t.Fatalf("brief --file <live scratch path>: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath)
	wantAppendedEvent(t, s, m.ID, 1, store.TypeContentCreated) // m already carried matter.created
	got, err := s.Content(context.Background(), m.ID, store.KindBrief)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != draftContent {
		t.Errorf("content = %q, want %q", got, draftContent)
	}

	// The scratch file itself is never touched, renamed, or deleted — the
	// store owns the content, not the file (D45).
	data, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("scratch file was removed or renamed after --file consumption: %v", err)
	}
	if string(data) != draftContent {
		t.Errorf("scratch file content changed after consumption: %q", data)
	}
}

// agentPathContentPayload mirrors content.go's JSON success shape for
// brief/workplan/body/finding-add — {node, kind, bytes} or {node, bytes}.
type agentPathContentPayload struct {
	Node  string `json:"node"`
	Kind  string `json:"kind"`
	Bytes int    `json:"bytes"`
}

// TestAgentPath_AcceptanceSession_IntakeThroughGate is step-07: the seal
// condition's direct discharge. A single scripted sequence drives, through
// the built binary only, an agent-shaped session — intake, plan, work,
// gate, stand-down — exactly the sequence step-07 specifies, and asserts
// every prose write in it produced exactly one event of the correct type,
// and that the Matter reaches locally complete (and, having no enclosing
// scale, sealed) once `reviewed-local` closes. This is deliberately a
// scripted stand-in for a live LLM-driven session (HANDOFF §1.1: no roles,
// no harness to drive one yet) — shaped like an agent's session, not
// literally performed by one.
func TestAgentPath_AcceptanceSession_IntakeThroughGate(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)
	ctx := context.Background()

	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter", "--json"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Spawn: the launching process runs `wip refresh` first (render-scratch
	// step-02/this Matter's step-03) and would hand the agent this
	// dispatch-id and scratch dir — nothing in the sequence below composes
	// either value itself.
	spawned := mustJSON[refreshPayload](t, runIn(t, dir, dbEnv, "refresh", "--json").stdout)
	if !spawned.Opened || spawned.Dispatch == "" || spawned.ScratchDir == "" {
		t.Fatalf("spawn: refresh did not open a dispatch with a scratch dir: %+v", spawned)
	}

	// Intake: birth the Matter.
	matterR := runIn(t, dir, dbEnv, "matter", "create", "--title", "Reduce p99 latency", "--json")
	if matterR.exitCode != 0 {
		t.Fatalf("matter create: exit=%d stderr=%q", matterR.exitCode, matterR.stderr)
	}
	m := mustJSON[nodePayload](t, matterR.stdout)

	// Plan: the Workplan, via stdin — this Matter's resolved shape for the
	// three create-once prose verbs.
	workplanText := "# Workplan: reduce p99 latency\n\nProfile the hot path, then cut it.\n"
	workplanR := runInStdin(t, dir, dbEnv, workplanText, "workplan", m.ID, "--json")
	if workplanR.exitCode != 0 {
		t.Fatalf("workplan (stdin): exit=%d stderr=%q", workplanR.exitCode, workplanR.stderr)
	}
	workplanPayload := mustJSON[agentPathContentPayload](t, workplanR.stdout)
	if workplanPayload.Kind != string(store.KindWorkplan) {
		t.Errorf("workplan payload.kind = %q, want %q", workplanPayload.Kind, store.KindWorkplan)
	}

	// Work: a Step directly under the Matter (D2), started, findings logged
	// via the positional-argument shape (this Matter's resolved default for
	// `wip finding add`), then finished.
	stepR := runIn(t, dir, dbEnv, "step", "create", m.ID, "--title", "Profile the hot path", "--json")
	if stepR.exitCode != 0 {
		t.Fatalf("step create: exit=%d stderr=%q", stepR.exitCode, stepR.stderr)
	}
	step := mustJSON[nodePayload](t, stepR.stdout)

	if r := runIn(t, dir, dbEnv, "start", step.ID); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	findings := []string{"the flame graph points at the marshal step", "cutting the marshal alone gets p99 under budget"}
	for i, text := range findings {
		findingR := runIn(t, dir, dbEnv, "finding", "add", step.ID, text, "--json")
		if findingR.exitCode != 0 {
			t.Fatalf("finding add #%d (positional): exit=%d stderr=%q", i+1, findingR.exitCode, findingR.stderr)
		}
	}

	if r := runIn(t, dir, dbEnv, "finish", step.ID); r.exitCode != 0 {
		t.Fatalf("finish step: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", m.ID); r.exitCode != 0 {
		t.Fatalf("finish matter: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	// Gate: the only gate this dogfood ever declares or closes.
	gateR := runIn(t, dir, dbEnv, "gate", "close", "reviewed-local", m.ID, "--json")
	if gateR.exitCode != 0 {
		t.Fatalf("gate close: exit=%d stderr=%q", gateR.exitCode, gateR.stderr)
	}

	// Stand-down: the explicit dispatch close, reason = completed (D59) —
	// the agent's last act per the porcelain contract.
	closeR := runIn(t, dir, dbEnv, "dispatch", "close", "--json")
	if closeR.exitCode != 0 {
		t.Fatalf("dispatch close: exit=%d stderr=%q", closeR.exitCode, closeR.stderr)
	}
	var closePayload struct {
		Dispatch string `json:"dispatch"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(closeR.stdout), &closePayload); err != nil {
		t.Fatalf("dispatch close output is not JSON: %v (stdout=%q)", err, closeR.stdout)
	}
	if closePayload.Dispatch != spawned.Dispatch {
		t.Errorf("dispatch close closed %q, want the session's own dispatch %q", closePayload.Dispatch, spawned.Dispatch)
	}
	if closePayload.Reason != "completed" {
		t.Errorf("dispatch close reason = %q, want %q", closePayload.Reason, "completed")
	}

	// Assert: every prose write in the sequence is visible as exactly one
	// event of the correct type (§10 invariant 1) — the Matter's own
	// events, in order: matter.created, content.created (workplan),
	// matter.started (cascade from starting the step), content.appended x2
	// (findings, on the step, not the matter — checked separately below),
	// matter.finished, gate.closed.
	s := openTestStore(t, dbPath)
	matterEvents, err := s.EventsOfSubject(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantMatterTypes := []string{
		store.TypeMatterCreated,
		store.TypeContentCreated,
		store.TypeMatterStarted,
		store.TypeMatterFinished,
		store.TypeGateClosed,
	}
	if len(matterEvents) != len(wantMatterTypes) {
		t.Fatalf("matter carries %d events, want %d (%v)", len(matterEvents), len(wantMatterTypes), wantMatterTypes)
	}
	for i, want := range wantMatterTypes {
		if matterEvents[i].Type != want {
			t.Errorf("matter event[%d] = %q, want %q", i, matterEvents[i].Type, want)
		}
	}
	var workplanPayloadEvent store.ContentWritten
	if err := json.Unmarshal(matterEvents[1].Payload, &workplanPayloadEvent); err != nil {
		t.Fatal(err)
	}
	if workplanPayloadEvent.Kind != store.KindWorkplan {
		t.Errorf("workplan event payload.kind = %q, want %q", workplanPayloadEvent.Kind, store.KindWorkplan)
	}

	stepEvents, err := s.EventsOfSubject(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantStepTypes := []string{
		store.TypeStepCreated,
		store.TypeStepStarted,
		store.TypeContentAppended,
		store.TypeContentAppended,
		store.TypeStepFinished,
	}
	if len(stepEvents) != len(wantStepTypes) {
		t.Fatalf("step carries %d events, want %d (%v)", len(stepEvents), len(wantStepTypes), wantStepTypes)
	}
	for i, want := range wantStepTypes {
		if stepEvents[i].Type != want {
			t.Errorf("step event[%d] = %q, want %q", i, stepEvents[i].Type, want)
		}
	}
	for i, idx := range []int{2, 3} {
		var p store.ContentWritten
		if err := json.Unmarshal(stepEvents[idx].Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Kind != store.KindFindings {
			t.Errorf("finding #%d event payload.kind = %q, want %q", i+1, p.Kind, store.KindFindings)
		}
	}
	allFindings, err := s.Content(ctx, step.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range findings {
		if !strings.Contains(string(allFindings), text) {
			t.Errorf("accumulated findings %q missing %q", allFindings, text)
		}
	}

	// Assert: the Matter reaches locally complete (and sealed, having no
	// enclosing scale) now that its one gate has closed — the D13
	// predicate `read-surface` computes, observed here as evidence.
	statusR := runIn(t, dir, dbEnv, "status", "--json")
	if statusR.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", statusR.exitCode, statusR.stderr)
	}
	var statusPayload struct {
		HostWide bool `json:"hostWide"`
		Repo     struct {
			Content struct {
				Finished []struct {
					ID              string `json:"id"`
					LocallyComplete bool   `json:"locallyComplete"`
					Sealed          bool   `json:"sealed"`
				} `json:"finished"`
			} `json:"content"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(statusR.stdout), &statusPayload); err != nil {
		t.Fatalf("status output is not JSON: %v (stdout=%q)", err, statusR.stdout)
	}
	found := false
	for _, f := range statusPayload.Repo.Content.Finished {
		if f.ID != m.ID {
			continue
		}
		found = true
		if !f.LocallyComplete {
			t.Errorf("matter reported locallyComplete=false after its one gate closed")
		}
		if !f.Sealed {
			t.Errorf("matter reported sealed=false after its one gate closed (a Matter has no enclosing scale, so sealed should agree with locallyComplete)")
		}
	}
	if !found {
		t.Fatalf("matter %s did not appear in status's finished section at all", m.ID)
	}

	// Every event this session emitted stamps actor=human — the P1
	// vocabulary has no other option yet (this Matter's own raised, not
	// resolved, open call; docs/agent-path/decisions.md records it in full
	// for `orchestration`).
	for _, e := range append(append([]store.Event{}, matterEvents...), stepEvents...) {
		if e.Actor != store.ActorHuman {
			t.Errorf("event %s (%s) has actor %q, want %q", e.ID, e.Type, e.Actor, store.ActorHuman)
		}
	}
}
