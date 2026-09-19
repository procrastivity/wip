package next

// Package-level coverage of the generated-content-navigation contract's
// step-03 test matrix (model-session-plan/handoffs/generated-content-
// navigation-contract.md §9): the JSON/human rendering this step changes —
// nodeJSON's two new fields, their placement at every render site, the one
// new human line, and --set's JSON participation. Every case here builds a
// readsurface.View directly (its fields are all exported) rather than
// driving readsurface.Next's own cursor/frontier computation — that
// computation is unchanged by this step (contract §6) and is readsurface's
// own to test; this file exercises only the rendering layer step-03 touches.
//
// Fixtures are seeded directly through store's exported data-access layer,
// the same posture internal/readsurface/testutil_test.go takes.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
)

var ctx = context.Background()

// testRoot is an arbitrary worktree root for every test in this file — the
// rendering functions under test only ever join it with ".wip/generated"
// and a Matter locator (render.GeneratedDir), never touch the filesystem.
const testRoot = "/fixture/root"

func wantGeneratedDir(matterLocator string) string {
	return filepath.Join(testRoot, ".wip", "generated", matterLocator)
}

type fixture struct {
	*store.Store
	t testing.TB

	Repo string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "wip.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f := &fixture{Store: s, t: t}
	f.Repo = f.attachRepo("fixture")
	return f
}

func (f *fixture) commit(env store.Env, decide func(context.Context, *store.Tx) ([]store.Draft, error)) []store.Event {
	f.t.Helper()
	events, err := f.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, decide)
	if err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return events
}

func (f *fixture) attachRepo(label string) string {
	f.t.Helper()
	id := f.NewID()
	f.commit(store.Env{Repo: id}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRepoAttached, Subject: id, Payload: store.RepoAttached{Label: label}}}, nil
	})
	return id
}

func (f *fixture) node(repo, eventType, parent, locator, title string) string {
	f.t.Helper()
	var id string
	f.commit(store.Env{Repo: repo}, func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		sortKey := int64(1000)
		if parent != "" {
			k, err := tx.NextSortKey(ctx, parent)
			if err != nil {
				return nil, err
			}
			sortKey = k
		}
		id = tx.NewID()
		return []store.Draft{{
			Type: eventType, Subject: id,
			Payload: store.NodeBirth{Title: title, Locator: locator, Parent: parent, SortKey: sortKey},
		}}, nil
	})
	return id
}

func (f *fixture) matter(locator, title string) string { return f.matterInRepo(f.Repo, locator, title) }

func (f *fixture) matterInRepo(repo, locator, title string) string {
	return f.node(repo, store.TypeMatterCreated, "", locator, title)
}

func (f *fixture) stage(parent, locator, title string) string {
	return f.node(f.Repo, store.TypeStageCreated, parent, locator, title)
}

func (f *fixture) step(parent, locator, title string) string {
	return f.node(f.Repo, store.TypeStepCreated, parent, locator, title)
}

func (f *fixture) transition(node string, to store.Lifecycle, verb string) {
	f.t.Helper()
	n, err := f.Node(ctx, node)
	if err != nil {
		f.t.Fatalf("read %s before %s: %v", node, verb, err)
	}
	from := n.Lifecycle
	events := map[store.Scale]map[string]string{
		store.ScaleMatter: {
			"start": store.TypeMatterStarted, "finish": store.TypeMatterFinished, "cancel": store.TypeMatterCanceled,
		},
		store.ScaleStage: {
			"start": store.TypeStageStarted, "finish": store.TypeStageFinished,
		},
		store.ScaleStep: {
			"start": store.TypeStepStarted, "finish": store.TypeStepFinished, "cancel": store.TypeStepCanceled,
		},
	}
	f.commit(store.Env{Repo: n.Repo}, func(_ context.Context, _ *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: events[n.Kind][verb], Subject: node,
			Payload: store.Transition{From: from, To: to},
		}}, nil
	})
}

func (f *fixture) start(node string)  { f.transition(node, store.InProgress, "start") }
func (f *fixture) finish(node string) { f.transition(node, store.Done, "finish") }
func (f *fixture) cancel(node string) { f.transition(node, store.Canceled, "cancel") }

func (f *fixture) depend(blocked, blocker string) {
	f.t.Helper()
	n, err := f.Node(ctx, blocked)
	if err != nil {
		f.t.Fatalf("read %s before depend: %v", blocked, err)
	}
	f.commit(store.Env{Repo: n.Repo}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeDependencyAdded, Subject: blocked,
			Payload: store.DependencyChange{Edge: tx.NewID(), Blocker: blocker},
		}}, nil
	})
}

func (f *fixture) mustNode(id string) store.Node {
	f.t.Helper()
	n, err := f.Node(ctx, id)
	if err != nil {
		f.t.Fatalf("read %s: %v", id, err)
	}
	return n
}

func newStreams() (*iostreams.Streams, *bytes.Buffer) {
	var buf bytes.Buffer
	return &iostreams.Streams{Out: &buf, Err: io.Discard}, &buf
}

// ---------------------------------------------------------------------------
// 3.1 — bare-matter JSON
// ---------------------------------------------------------------------------

func TestNav_BareMatterJSON_CarriesOwnMatterAndGeneratedDir(t *testing.T) {
	f := newFixture(t)
	m := f.matter("bare-widget", "Bare widget")
	node := f.mustNode(m)
	view := readsurface.View{Kind: readsurface.BareMatter, Node: node, Address: node.Locator}

	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Node struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"node"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, buf.String())
	}
	if payload.Node.Matter != "bare-widget" {
		t.Errorf("node.matter = %q, want %q", payload.Node.Matter, "bare-widget")
	}
	if payload.Node.GeneratedDir != wantGeneratedDir("bare-widget") {
		t.Errorf("node.generatedDir = %q, want %q", payload.Node.GeneratedDir, wantGeneratedDir("bare-widget"))
	}
}

// ---------------------------------------------------------------------------
// 3.2 / 3.3 — positioned JSON, Stage-grouped and Matter-direct Steps
// ---------------------------------------------------------------------------

func TestNav_PositionedJSON_StageGroupedStep_UnmetMetCarryFields(t *testing.T) {
	f := newFixture(t)
	m := f.matter("widget", "Widget")
	stage := f.stage(m, "build", "Build")
	target := f.step(stage, "step-02", "Assemble")

	doneBlocker := f.step(m, "step-01", "Prep")
	f.finish(doneBlocker) // Done, no gates declared — locally complete (met)
	openBlocker := f.matter("blocker-widget", "Blocker widget")

	f.depend(target, doneBlocker)
	f.depend(target, openBlocker)

	targetNode := f.mustNode(target)
	view := readsurface.View{
		Kind: readsurface.Positioned, Node: targetNode, Address: "widget/build · step-02",
		Stage: &readsurface.StagePosition{StageLocator: "build", Index: 1, Total: 1},
		Met:   []store.Node{f.mustNode(doneBlocker)},
		Unmet: []store.Node{f.mustNode(openBlocker)},
	}

	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Node struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"node"`
		Stage struct {
			Locator string `json:"locator"`
			Index   int    `json:"index"`
			Total   int    `json:"total"`
		} `json:"stage"`
		Unmet []struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"unmet"`
		Met []struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"met"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (raw=%s)", err, buf.String())
	}
	if payload.Node.Matter != "widget" || payload.Node.GeneratedDir != wantGeneratedDir("widget") {
		t.Errorf("node = %+v, want matter=widget generatedDir=%s", payload.Node, wantGeneratedDir("widget"))
	}
	if payload.Stage.Locator != "build" || payload.Stage.Index != 1 || payload.Stage.Total != 1 {
		t.Errorf("stage = %+v, want unchanged build/1/1", payload.Stage)
	}
	if len(payload.Unmet) != 1 || payload.Unmet[0].Matter != "blocker-widget" || payload.Unmet[0].GeneratedDir != wantGeneratedDir("blocker-widget") {
		t.Errorf("unmet = %+v", payload.Unmet)
	}
	if len(payload.Met) != 1 || payload.Met[0].Matter != "widget" || payload.Met[0].GeneratedDir != wantGeneratedDir("widget") {
		t.Errorf("met = %+v", payload.Met)
	}
}

func TestNav_PositionedJSON_MatterDirectStep_NoStageField(t *testing.T) {
	f := newFixture(t)
	m := f.matter("direct-widget", "Direct widget")
	target := f.step(m, "step-01", "Do it")
	targetNode := f.mustNode(target)
	view := readsurface.View{Kind: readsurface.Positioned, Node: targetNode, Address: "direct-widget · step-01"}

	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["stage"]; present {
		t.Errorf("payload = %v, want no stage field for a Matter-direct Step", payload)
	}
	node, _ := payload["node"].(map[string]any)
	if node["matter"] != "direct-widget" || node["generatedDir"] != wantGeneratedDir("direct-widget") {
		t.Errorf("node = %v", node)
	}
}

// ---------------------------------------------------------------------------
// 3.4 — no-cursor JSON, candidates spanning >=2 Matters
// ---------------------------------------------------------------------------

func TestNav_NoCursorJSON_CandidatesCarryDistinctOwnMatters(t *testing.T) {
	f := newFixture(t)
	a := f.matter("candidate-a", "Candidate A")
	b := f.matter("candidate-b", "Candidate B")
	view := readsurface.View{Kind: readsurface.NoCursor, Candidates: []store.Node{f.mustNode(a), f.mustNode(b)}}

	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Candidates []struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want 2", payload.Candidates)
	}
	if payload.Candidates[0].Matter != "candidate-a" || payload.Candidates[1].Matter != "candidate-b" {
		t.Errorf("candidates = %+v, want distinct own matters", payload.Candidates)
	}
	if payload.Candidates[0].GeneratedDir == payload.Candidates[1].GeneratedDir {
		t.Errorf("candidates share one generatedDir: %+v", payload.Candidates)
	}
}

// ---------------------------------------------------------------------------
// 3.5 — in-progress-no-cursor JSON
// ---------------------------------------------------------------------------

func TestNav_InProgressNoCursorJSON_EntriesCarryBothFields(t *testing.T) {
	f := newFixture(t)
	m := f.matter("underway", "Underway")
	f.start(m)
	view := readsurface.View{Kind: readsurface.InProgressNoCursor, InProgress: []store.Node{f.mustNode(m)}}

	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		InProgress []struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"inProgress"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.InProgress) != 1 || payload.InProgress[0].Matter != "underway" || payload.InProgress[0].GeneratedDir != wantGeneratedDir("underway") {
		t.Errorf("inProgress = %+v", payload.InProgress)
	}
}

// ---------------------------------------------------------------------------
// 3.6 / 3.6b — nothing-unblocked, blocked and idle variants
// ---------------------------------------------------------------------------

func TestNav_NothingUnblockedJSON_BlockedVariant_EntriesCarryBothFields(t *testing.T) {
	f := newFixture(t)
	blocker := f.matter("blocker", "Blocker")
	f.start(blocker)
	held := f.matter("held", "Held")
	heldStep := f.step(held, "step-01", "Waits")
	f.depend(heldStep, blocker)

	view := readsurface.View{
		Kind: readsurface.NothingUnblocked,
		Blocked: []readsurface.Blocked{
			{Node: f.mustNode(heldStep), Blockers: []store.Node{f.mustNode(blocker)}},
		},
	}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Blocked []struct {
			Node struct {
				Matter       string `json:"matter"`
				GeneratedDir string `json:"generatedDir"`
			} `json:"node"`
			BlockedBy []struct {
				Matter       string `json:"matter"`
				GeneratedDir string `json:"generatedDir"`
			} `json:"blockedBy"`
		} `json:"blocked"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Blocked) != 1 {
		t.Fatalf("blocked = %+v, want 1 entry", payload.Blocked)
	}
	if payload.Blocked[0].Node.Matter != "held" || payload.Blocked[0].Node.GeneratedDir != wantGeneratedDir("held") {
		t.Errorf("blocked[0].node = %+v", payload.Blocked[0].Node)
	}
	if len(payload.Blocked[0].BlockedBy) != 1 || payload.Blocked[0].BlockedBy[0].Matter != "blocker" ||
		payload.Blocked[0].BlockedBy[0].GeneratedDir != wantGeneratedDir("blocker") {
		t.Errorf("blocked[0].blockedBy = %+v", payload.Blocked[0].BlockedBy)
	}
}

// TestNav_NothingUnblockedJSON_IdleVariant_ByteIdenticalToPreChangeShape is
// contract test 3.6b: the idle variant (§3.6's first recorded exclusion)
// carries no metadata anywhere — the payload is exactly what it was before
// this step, because view.Blocked/Node/Candidates/InProgress are all their
// zero value on this shape (readsurface/next.go's second NothingUnblocked
// return, unchanged by this step).
func TestNav_NothingUnblockedJSON_IdleVariant_ByteIdenticalToPreChangeShape(t *testing.T) {
	f := newFixture(t)
	view := readsurface.View{Kind: readsurface.NothingUnblocked}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `{"kind":"nothing-unblocked"}`+"\n"; got != want {
		t.Errorf("idle nothing-unblocked JSON = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3.7 — everything-sealed, byte-identical
// ---------------------------------------------------------------------------

func TestNav_EverythingSealedJSON_ByteIdenticalToPreChangeShape(t *testing.T) {
	f := newFixture(t)
	view := readsurface.View{Kind: readsurface.EverythingSealed}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `{"kind":"everything-sealed"}`+"\n"; got != want {
		t.Errorf("everything-sealed JSON = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3.8 / 3.9 — choose-next sealed / canceled
// ---------------------------------------------------------------------------

func TestNav_ChooseNextJSON_Sealed_NodeAndListsCarryFields(t *testing.T) {
	f := newFixture(t)
	m := f.matter("sealed-target", "Sealed target")
	f.start(m)
	f.finish(m) // no gate declared — Done and immediately sealed

	candidate := f.matter("candidate", "Candidate")
	underway := f.matter("underway", "Underway")
	f.start(underway)

	view := readsurface.View{
		Kind: readsurface.ChooseNext, Node: f.mustNode(m), EndedReason: "sealed",
		Candidates: []store.Node{f.mustNode(candidate)}, InProgress: []store.Node{f.mustNode(underway)},
	}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Node struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"node"`
		Candidates []struct {
			Matter string `json:"matter"`
		} `json:"candidates"`
		InProgress []struct {
			Matter string `json:"matter"`
		} `json:"inProgress"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Node.Matter != "sealed-target" || payload.Node.GeneratedDir != wantGeneratedDir("sealed-target") {
		t.Errorf("node = %+v", payload.Node)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].Matter != "candidate" {
		t.Errorf("candidates = %+v", payload.Candidates)
	}
	if len(payload.InProgress) != 1 || payload.InProgress[0].Matter != "underway" {
		t.Errorf("inProgress = %+v", payload.InProgress)
	}
}

func TestNav_ChooseNextJSON_Canceled_NodeCarriesFields(t *testing.T) {
	f := newFixture(t)
	m := f.matter("abandoned", "Abandoned")
	f.start(m)
	f.cancel(m)

	view := readsurface.View{Kind: readsurface.ChooseNext, Node: f.mustNode(m), EndedReason: "canceled"}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Node struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"node"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Node.Matter != "abandoned" || payload.Node.GeneratedDir != wantGeneratedDir("abandoned") {
		t.Errorf("node = %+v", payload.Node)
	}
}

// ---------------------------------------------------------------------------
// 3.10 / 3.10b — choose-next removed, lists non-empty and both-empty
// ---------------------------------------------------------------------------

func TestNav_ChooseNextJSON_Removed_ListsNonEmpty_NoNodeListsCarryFields(t *testing.T) {
	f := newFixture(t)
	candidate := f.matter("still-here", "Still here")

	view := readsurface.View{
		Kind: readsurface.ChooseNext, Node: store.Node{}, EndedReason: "removed",
		Candidates: []store.Node{f.mustNode(candidate)},
	}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["node"]; present {
		t.Errorf("payload = %v, want no node for a removed target", payload)
	}
	cands, _ := payload["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("candidates = %v", payload["candidates"])
	}
	first, _ := cands[0].(map[string]any)
	if first["matter"] != "still-here" || first["generatedDir"] != wantGeneratedDir("still-here") {
		t.Errorf("candidates[0] = %v", first)
	}
}

// TestNav_ChooseNextJSON_Removed_ListsBothEmpty is contract test 3.10b:
// {"kind":"choose-next","reason":"removed"} with no metadata anywhere — the
// second §3.6 recorded exclusion.
func TestNav_ChooseNextJSON_Removed_ListsBothEmpty(t *testing.T) {
	f := newFixture(t)
	view := readsurface.View{Kind: readsurface.ChooseNext, Node: store.Node{}, EndedReason: "removed"}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `{"kind":"choose-next","reason":"removed"}`+"\n"; got != want {
		t.Errorf("removed/empty JSON = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3.11 / 3.12 / 3.13 — human "generated:" line placement
// ---------------------------------------------------------------------------

func TestNav_HumanBareMatter_GeneratedLineIsLastAfterPendingGates(t *testing.T) {
	f := newFixture(t)
	m := f.matter("bare-gate", "Bare with a gate")
	node := f.mustNode(m)

	// No pending gates: the generated line is the last line, right after
	// "no plan — work it directly".
	view := readsurface.View{Kind: readsurface.BareMatter, Node: node, Address: node.Locator}
	streams, buf := newStreams()
	if err := renderHuman(ctx, streams, f.View, view, testRoot); err != nil {
		t.Fatal(err)
	}
	want := "bare-gate                          matter · planned\n  no plan — work it directly\n" +
		"  generated: " + wantGeneratedDir("bare-gate") + " (refresh: wip plumbing refresh bare-gate)\n"
	if buf.String() != want {
		t.Errorf("bare-matter human = %q, want %q", buf.String(), want)
	}

	// With a pending gate, the generated line comes after the pending-gates
	// line, still last.
	viewWithGate := view
	viewWithGate.Pending = []store.GateRequirement{{Gate: "reviewed-local", Scale: store.ScaleMatter, Relationship: store.GateOwn, Subject: node}}
	streams2, buf2 := newStreams()
	if err := renderHuman(ctx, streams2, f.View, viewWithGate, testRoot); err != nil {
		t.Fatal(err)
	}
	wantWithGate := "bare-gate                          matter · planned\n  no plan — work it directly\n" +
		"  pending gates: reviewed-local (own on bare-gate)\n" +
		"  generated: " + wantGeneratedDir("bare-gate") + " (refresh: wip plumbing refresh bare-gate)\n"
	if buf2.String() != wantWithGate {
		t.Errorf("bare-matter human (with pending gate) = %q, want %q", buf2.String(), wantWithGate)
	}
}

func TestNav_HumanPositioned_GeneratedLineIsLastAfterBlockedByAndPendingGates(t *testing.T) {
	f := newFixture(t)
	m := f.matter("positioned-widget", "Positioned widget")
	step := f.step(m, "step-01", "Do it")
	node := f.mustNode(step)

	view := readsurface.View{Kind: readsurface.Positioned, Node: node, Address: "positioned-widget · step-01"}
	streams, buf := newStreams()
	if err := renderHuman(ctx, streams, f.View, view, testRoot); err != nil {
		t.Fatal(err)
	}
	want := "positioned-widget · step-01        step · planned\n  blocked-by: none\n" +
		"  generated: " + wantGeneratedDir("positioned-widget") + " (refresh: wip plumbing refresh positioned-widget)\n"
	if buf.String() != want {
		t.Errorf("positioned human = %q, want %q", buf.String(), want)
	}
}

func TestNav_HumanChooseNext_GeneratedLinePlacementBySubReason(t *testing.T) {
	f := newFixture(t)
	m := f.matter("sealed-target", "Sealed target")
	f.start(m)
	f.finish(m)
	node := f.mustNode(m)

	sealedView := readsurface.View{Kind: readsurface.ChooseNext, Node: node, EndedReason: "sealed"}
	streams, buf := newStreams()
	if err := renderHuman(ctx, streams, f.View, sealedView, testRoot); err != nil {
		t.Fatal(err)
	}
	wantPrefix := "sealed-target is sealed — choose what's next\n" +
		"  generated: " + wantGeneratedDir("sealed-target") + " (refresh: wip plumbing refresh sealed-target)\n"
	if got := buf.String(); len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("choose-next sealed human = %q, want it to start with %q", got, wantPrefix)
	}

	removedView := readsurface.View{Kind: readsurface.ChooseNext, Node: store.Node{}, EndedReason: "removed"}
	streams2, buf2 := newStreams()
	if err := renderHuman(ctx, streams2, f.View, removedView, testRoot); err != nil {
		t.Fatal(err)
	}
	if got := buf2.String(); containsSubstring(got, "generated:") {
		t.Errorf("choose-next removed human = %q, want no generated line", got)
	}
}

func TestNav_HumanListShapes_ByteUnchanged(t *testing.T) {
	f := newFixture(t)
	a := f.matter("cand-a", "Candidate A")
	view := readsurface.View{Kind: readsurface.NoCursor, Candidates: []store.Node{f.mustNode(a)}}
	streams, buf := newStreams()
	if err := renderHuman(ctx, streams, f.View, view, testRoot); err != nil {
		t.Fatal(err)
	}
	want := "no cursor set for this clone — 1 unblocked:\n  cand-a                 matter · planned\npick one: wip next --set <locator>\n"
	if buf.String() != want {
		t.Errorf("no-cursor human = %q, want %q (and no generated line)", buf.String(), want)
	}
	if containsSubstring(buf.String(), "generated:") {
		t.Errorf("no-cursor human = %q, want no generated line", buf.String())
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// ---------------------------------------------------------------------------
// 3.14 — pendingGates orthogonality
// ---------------------------------------------------------------------------

func TestNav_PendingGatesOrthogonality(t *testing.T) {
	f := newFixture(t)

	done := f.matter("done-with-gate", "Done with an open gate")
	f.start(done)
	f.finish(done)
	doneNode := f.mustNode(done)
	doneView := readsurface.View{
		Kind: readsurface.BareMatter, Node: doneNode, Address: doneNode.Locator,
		Pending: []store.GateRequirement{{Gate: "reviewed-local", Scale: store.ScaleMatter, Relationship: store.GateOwn, Subject: doneNode}},
	}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, doneView, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var donePayload struct {
		Node struct {
			Matter       string `json:"matter"`
			GeneratedDir string `json:"generatedDir"`
		} `json:"node"`
		Pending []struct {
			Name string `json:"name"`
		} `json:"pendingGates"`
	}
	if err := json.Unmarshal(buf.Bytes(), &donePayload); err != nil {
		t.Fatal(err)
	}
	if donePayload.Node.Matter != "done-with-gate" || donePayload.Node.GeneratedDir != wantGeneratedDir("done-with-gate") {
		t.Errorf("Done node = %+v, want matter/generatedDir present", donePayload.Node)
	}
	if len(donePayload.Pending) != 1 || donePayload.Pending[0].Name != "reviewed-local" {
		t.Errorf("Done pendingGates = %+v, want the one open gate", donePayload.Pending)
	}

	inProgress := f.matter("in-progress-target", "In progress target")
	f.start(inProgress)
	ipNode := f.mustNode(inProgress)
	ipView := readsurface.View{Kind: readsurface.BareMatter, Node: ipNode, Address: ipNode.Locator}
	streams2, buf2 := newStreams()
	if err := renderJSON(ctx, streams2, f.View, ipView, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var ipPayload map[string]any
	if err := json.Unmarshal(buf2.Bytes(), &ipPayload); err != nil {
		t.Fatal(err)
	}
	node, _ := ipPayload["node"].(map[string]any)
	if node["matter"] != "in-progress-target" || node["generatedDir"] != wantGeneratedDir("in-progress-target") {
		t.Errorf("In-Progress node = %v, want matter/generatedDir present", node)
	}
	if _, present := ipPayload["pendingGates"]; present {
		t.Errorf("In-Progress payload = %v, want no pendingGates field", ipPayload)
	}
}

// ---------------------------------------------------------------------------
// 3.18 / 3.19 — --set JSON, --clear unchanged
// ---------------------------------------------------------------------------

func TestNav_RenderSetJSON_CarriesMatterAndGeneratedDir(t *testing.T) {
	f := newFixture(t)
	m := f.matter("set-target", "Set target")
	node := f.mustNode(m)

	streams, buf := newStreams()
	if err := renderSet(ctx, streams, f.View, true, node, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Node         string `json:"node"`
		Address      string `json:"address"`
		Matter       string `json:"matter"`
		GeneratedDir string `json:"generatedDir"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Node != node.ID || payload.Address != "set-target" ||
		payload.Matter != "set-target" || payload.GeneratedDir != wantGeneratedDir("set-target") {
		t.Errorf("--set JSON = %+v", payload)
	}

	streamsHuman, bufHuman := newStreams()
	if err := renderSet(ctx, streamsHuman, f.View, false, node, testRoot); err != nil {
		t.Fatal(err)
	}
	if want := "cursor set to set-target\n"; bufHuman.String() != want {
		t.Errorf("--set human = %q, want %q", bufHuman.String(), want)
	}
}

func TestNav_RenderClear_UnchangedByThisStep(t *testing.T) {
	streams, buf := newStreams()
	if err := renderClear(streams, true, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["matter"]; present {
		t.Errorf("--clear JSON = %v, want no matter field", payload)
	}
	if _, present := payload["generatedDir"]; present {
		t.Errorf("--clear JSON = %v, want no generatedDir field", payload)
	}

	streams2, buf2 := newStreams()
	if err := renderClear(streams2, false, ""); err != nil {
		t.Fatal(err)
	}
	if want := "cursor cleared — everything open\n"; buf2.String() != want {
		t.Errorf("--clear human = %q, want %q", buf2.String(), want)
	}
}

// ---------------------------------------------------------------------------
// Foreign-repo carve-out (§2's presence rule): matter/generatedDir omitted
// together on a node whose Repo differs from the resolved current Repo.
// ---------------------------------------------------------------------------

func TestNav_ForeignRepoNode_OmitsMatterAndGeneratedDirTogether(t *testing.T) {
	f := newFixture(t)
	otherRepo := f.attachRepo("other")
	foreign := f.matterInRepo(otherRepo, "foreign-matter", "Foreign matter")

	view := readsurface.View{
		Kind: readsurface.NothingUnblocked,
		Blocked: []readsurface.Blocked{
			{Node: f.mustNode(foreign), Blockers: nil},
		},
	}
	streams, buf := newStreams()
	if err := renderJSON(ctx, streams, f.View, view, f.Repo, testRoot); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Blocked []struct {
			Node map[string]any `json:"node"`
		} `json:"blocked"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Blocked) != 1 {
		t.Fatalf("blocked = %+v", payload.Blocked)
	}
	if _, present := payload.Blocked[0].Node["matter"]; present {
		t.Errorf("foreign-repo node = %v, want no matter field", payload.Blocked[0].Node)
	}
	if _, present := payload.Blocked[0].Node["generatedDir"]; present {
		t.Errorf("foreign-repo node = %v, want no generatedDir field", payload.Blocked[0].Node)
	}
	if payload.Blocked[0].Node["id"] != foreign {
		t.Errorf("foreign-repo node id = %v, want %q (id/address/kind/lifecycle still present)", payload.Blocked[0].Node["id"], foreign)
	}
}
