//go:build sharedstoreexperiment

package store_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labProse is one create-once content expectation: exact bytes on one
// subject under one kind.
type labProse struct {
	subject string
	kind    store.ContentKind
	data    []byte
}

// labFamilyWork is one composed portable workload plus the observable state a
// receiver must reproduce: live node identities with parents, locators,
// lifecycles and sibling order; tombstoned identities; backlog rows; prose
// bytes; finding payloads. It is built only from writesurface verbs, so every
// expectation is what the source store itself recorded — never a hand-derived
// guess about what the wire should carry.
type labFamilyWork struct {
	matter, stage store.Node
	live          []string
	gone          []string
	states        map[string]store.Lifecycle
	stageSteps    []string
	matterKids    []string
	entries       map[string]store.BacklogEntry
	prose         []labProse
	findings      map[string][][]byte
}

// labComposeFamily drives every portable family on s: a Matter with a Stage
// and Steps carrying insert/reorder/replace/remove history, a lifecycle
// matrix ending in a paused Stage, planned/declined/delegated/entered
// backlog, prose on three different node kinds and findings on nodes and a
// backlog entry.
func labComposeFamily(t *testing.T, s *store.Store, repo, title, locator string) labFamilyWork {
	t.Helper()
	w := labFamilyWork{
		states:   map[string]store.Lifecycle{},
		entries:  map[string]store.BacklogEntry{},
		findings: map[string][][]byte{},
	}
	var err error
	if w.matter, err = writesurface.CreateMatter(labCtx, s, store.ActorHuman, repo, title, locator); err != nil {
		t.Fatal(err)
	}
	if w.stage, err = writesurface.CreateStage(labCtx, s, store.ActorHuman, repo, w.matter.Locator, "Phase"); err != nil {
		t.Fatal(err)
	}
	path := func(n store.Node) string { return w.matter.Locator + "/" + n.Locator }
	stagePath := w.matter.Locator + "/" + w.stage.Locator
	var alpha, beta, gamma store.Node
	for _, spec := range []struct {
		title string
		node  *store.Node
	}{{"Alpha", &alpha}, {"Beta", &beta}, {"Gamma", &gamma}} {
		if *spec.node, err = writesurface.CreateStep(labCtx, s, store.ActorHuman, repo, stagePath, spec.title); err != nil {
			t.Fatal(err)
		}
	}
	var delta, repl, direct store.Node
	if delta, err = writesurface.InsertStep(labCtx, s, store.ActorHuman, repo, stagePath, "Delta", path(alpha), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.ReorderStep(labCtx, s, store.ActorHuman, repo, stagePath,
		[]string{path(beta), path(delta), path(alpha), path(gamma)}); err != nil {
		t.Fatal(err)
	}
	if repl, err = writesurface.ReplaceStep(labCtx, s, store.ActorHuman, repo, path(gamma), "Gamma prime"); err != nil {
		t.Fatal(err)
	}
	if err = writesurface.RemoveStep(labCtx, s, store.ActorHuman, repo, path(delta), "descoped"); err != nil {
		t.Fatal(err)
	}
	if direct, err = writesurface.CreateStep(labCtx, s, store.ActorHuman, repo, w.matter.Locator, "Direct note"); err != nil {
		t.Fatal(err)
	}
	// Lifecycle history. Every start lands while the Stage is still live:
	// pausing the Stage early would make the later starts' ancestor evidence
	// incompatible on arrival.
	if _, err = writesurface.Start(labCtx, s, store.ActorHuman, repo, path(alpha)); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []func() error{
		labVerbPause(s, repo, path(alpha)),
		labVerbResume(s, repo, path(alpha)),
		labVerbFinish(s, repo, path(alpha)),
	} {
		if err := verb(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = writesurface.Start(labCtx, s, store.ActorHuman, repo, path(beta)); err != nil {
		t.Fatal(err)
	}
	if err = labVerbCancel(s, repo, path(beta), "descoped")(); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Start(labCtx, s, store.ActorHuman, repo, path(repl)); err != nil {
		t.Fatal(err)
	}
	if err = labVerbPause(s, repo, path(repl))(); err != nil {
		t.Fatal(err)
	}
	if err = labVerbPause(s, repo, stagePath)(); err != nil {
		t.Fatal(err)
	}
	// Backlog outcomes: planned into the Matter, declined with a reason,
	// delegated (recorded evidence, never delivery authority here), entered.
	var e1, e2, e3, e4 store.BacklogEntry
	if e1, err = writesurface.BacklogAdd(labCtx, s, store.ActorHuman, repo,
		store.ProvenanceFound, "Planned item", "found during archive work", stagePath); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.BacklogPlan(labCtx, s, store.ActorHuman, repo, e1.ID, w.matter.Locator); err != nil {
		t.Fatal(err)
	}
	if e2, err = writesurface.BacklogAdd(labCtx, s, store.ActorHuman, repo,
		store.ProvenanceIntake, "Declined item", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.BacklogDecline(labCtx, s, store.ActorHuman, repo, e2.ID, "the premise changed"); err != nil {
		t.Fatal(err)
	}
	if e3, err = writesurface.BacklogAdd(labCtx, s, store.ActorHuman, repo,
		store.ProvenanceDeferred, "Delegated item", "pushed out of phase", stagePath); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.BacklogDelegate(labCtx, s, store.ActorHuman, repo, e3.ID); err != nil {
		t.Fatal(err)
	}
	if e4, err = writesurface.BacklogAdd(labCtx, s, store.ActorHuman, repo,
		store.ProvenanceIntake, "Entered item", "", ""); err != nil {
		t.Fatal(err)
	}
	// Asymmetric inline prose: different kinds on different node kinds.
	brief := []byte("# Archive brief\n\nλ inline bytes, verbatim.\n")
	if _, err = writesurface.WriteOnce(labCtx, s, store.ActorHuman, repo, w.matter.Locator, store.KindBrief, brief); err != nil {
		t.Fatal(err)
	}
	plan := []byte("phase workplan\nsecond line\n")
	if _, err = writesurface.WriteOnce(labCtx, s, store.ActorHuman, repo, stagePath, store.KindWorkplan, plan); err != nil {
		t.Fatal(err)
	}
	body := []byte("alpha body\n")
	if _, err = writesurface.WriteOnce(labCtx, s, store.ActorHuman, repo, path(alpha), store.KindBody, body); err != nil {
		t.Fatal(err)
	}
	w.prose = []labProse{
		{w.matter.ID, store.KindBrief, brief},
		{w.stage.ID, store.KindWorkplan, plan},
		{alpha.ID, store.KindBody, body},
	}
	// Findings on Steps, the Matter and a backlog entry — several segments on
	// the Matter so ordering and multiplicity are both observable.
	for _, spec := range []struct {
		locator string
		data    []byte
	}{
		{path(beta), []byte("beta was descoped mid-flight")},
		{path(repl), []byte("gamma prime needs a second look")},
		{e4.ID, []byte("triage note on the open entry")},
		{w.matter.Locator, []byte("matter finding one")},
		{w.matter.Locator, []byte("matter finding two")},
	} {
		subject, err := writesurface.AppendFinding(labCtx, s, store.ActorHuman, repo, spec.locator, spec.data)
		if err != nil {
			t.Fatal(err)
		}
		w.findings[subject.ID] = append(w.findings[subject.ID], spec.data)
	}
	w.live = []string{w.matter.ID, w.stage.ID, alpha.ID, beta.ID, repl.ID, direct.ID}
	w.gone = []string{gamma.ID, delta.ID}
	w.stageSteps = []string{beta.ID, alpha.ID, repl.ID}
	w.matterKids = []string{w.stage.ID, direct.ID}
	for _, spec := range []struct {
		id    string
		state store.Lifecycle
	}{
		{w.matter.ID, store.InProgress},
		{w.stage.ID, store.Paused},
		{alpha.ID, store.Done},
		{beta.ID, store.Canceled},
		{repl.ID, store.Paused},
		{direct.ID, store.Planned},
	} {
		w.states[spec.id] = spec.state
	}
	for _, id := range []string{e1.ID, e2.ID, e3.ID, e4.ID} {
		w.entries[id] = labBacklog(t, s, repo, id)
	}
	// Self-check: the fixture itself must have produced the state the
	// assertions will demand of a receiver before it is allowed to sync.
	for id, state := range w.states {
		labState(t, s, id, state)
	}
	if got := labChildIDs(t, s, w.stage.ID); !reflect.DeepEqual(got, w.stageSteps) {
		t.Fatalf("source stage order = %v, want %v", got, w.stageSteps)
	}
	if got := labChildIDs(t, s, w.matter.ID); !reflect.DeepEqual(got, w.matterKids) {
		t.Fatalf("source matter children = %v, want %v", got, w.matterKids)
	}
	for _, id := range w.gone {
		if _, err := s.Node(labCtx, id); err == nil {
			t.Fatalf("source kept tombstoned node %s live", id)
		}
	}
	return w
}

// labSameNode requires got to hold want's node field-for-field. Repo is the
// one allowed difference: each store keeps its own native identity.
func labSameNode(t *testing.T, want, got *store.Store, id string) {
	t.Helper()
	wn, err := want.Node(labCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	gn, err := got.Node(labCtx, id)
	if err != nil {
		t.Fatalf("receiver lost node %s: %v", id, err)
	}
	wn.Repo, gn.Repo = "", ""
	if !reflect.DeepEqual(wn, gn) {
		t.Fatalf("node %s = %+v on receiver, want %+v", id, gn, wn)
	}
}

// labSegments lists a subject's content segments with the birth timestamp
// masked: effect events legitimately carry the receiver's own clock, so only
// identity, shape and bytes are comparable across stores.
func labSegments(t *testing.T, s *store.Store, subject string, kind store.ContentKind) []store.ContentSegment {
	t.Helper()
	segs, err := s.ContentSegments(labCtx, subject, kind)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]store.ContentSegment, len(segs))
	for i, seg := range segs {
		seg.OccurredAt = time.Time{}
		out[i] = seg
	}
	return out
}

// labAssertFamily compares got's observable work state against want's — the
// independently populated source — field by field: node identity, parenting,
// locator, lifecycle, sort order, backlog rows, prose bytes and finding
// segments. Counts alone never satisfy it.
func labAssertFamily(t *testing.T, want, got *store.Store, gotRepo string, w labFamilyWork) {
	t.Helper()
	for _, id := range w.live {
		labSameNode(t, want, got, id)
		n := mustNode(t, got, id)
		if n.Repo != gotRepo || n.Lifecycle != w.states[id] {
			t.Fatalf("receiver node %s = repo %q lifecycle %s, want %s in %s",
				id, n.Repo, n.Lifecycle, w.states[id], gotRepo)
		}
	}
	for _, id := range w.gone {
		if _, err := got.Node(labCtx, id); err == nil {
			t.Fatalf("receiver resurrected tombstoned node %s", id)
		}
	}
	if order := labChildIDs(t, got, w.stage.ID); !reflect.DeepEqual(order, w.stageSteps) {
		t.Fatalf("receiver stage order = %v, want %v", order, w.stageSteps)
	}
	if order := labChildIDs(t, got, w.matter.ID); !reflect.DeepEqual(order, w.matterKids) {
		t.Fatalf("receiver matter children = %v, want %v", order, w.matterKids)
	}
	for id, wantEntry := range w.entries {
		gotEntry := labBacklog(t, got, gotRepo, id)
		wantEntry.Repo = ""
		gotEntry.Repo = ""
		if !reflect.DeepEqual(wantEntry, gotEntry) {
			t.Fatalf("receiver backlog %s = %+v, want %+v", id, gotEntry, wantEntry)
		}
	}
	for _, p := range w.prose {
		data, err := got.Content(labCtx, p.subject, p.kind)
		if err != nil || !bytes.Equal(data, p.data) {
			t.Fatalf("receiver %s on %s = %q (%v), want %q", p.kind, p.subject, data, err, p.data)
		}
		wantSegs := labSegments(t, want, p.subject, p.kind)
		gotSegs := labSegments(t, got, p.subject, p.kind)
		if !reflect.DeepEqual(wantSegs, gotSegs) {
			t.Fatalf("receiver %s segments on %s = %+v, want %+v", p.kind, p.subject, gotSegs, wantSegs)
		}
	}
	for subject, payloads := range w.findings {
		segs := labSegments(t, got, subject, store.KindFindings)
		if len(segs) != len(payloads) {
			t.Fatalf("receiver findings on %s = %d segments, want %d", subject, len(segs), len(payloads))
		}
		wantSegs := labSegments(t, want, subject, store.KindFindings)
		if !reflect.DeepEqual(wantSegs, segs) {
			t.Fatalf("receiver finding segments on %s = %+v, want %+v", subject, segs, wantSegs)
		}
		for i, payload := range payloads {
			data, err := got.SegmentBytes(labCtx, segs[i].ID)
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("receiver finding %d on %s = %q (%v), want %q", i, subject, data, err, payload)
			}
		}
	}
}

// labRecordTransport counts which hosts an ExchangeClient actually reaches —
// the proof that receiving, rechecking and rebuilding never dialed a provider.
type labRecordTransport struct {
	mu    sync.Mutex
	hosts map[string]int
}

func (r *labRecordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hosts[req.URL.Host]++
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

// Step-07 flagship: two independently populated stores converge over real
// Laravel/PostgreSQL transport. The sender carries a historical Matter —
// amended Stage and Steps, a lifecycle matrix, every backlog outcome and
// asymmetric inline prose; the receiver holds unrelated work plus deliberate
// identity collisions. Observable state, not received counts, must match.
func TestLaravelExchangeComposedFamilyState(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}

	// The receiver's own history predates pairing: unrelated live work with
	// prose and a lifecycle, a same-title Matter that must never merge, and a
	// Matter already holding the locator the sender's colliding birth claims.
	local, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Local Only", "local-only")
	if err != nil {
		t.Fatal(err)
	}
	localStage, err := writesurface.CreateStage(labCtx, sb, store.ActorHuman, b.repo, local.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	localStep, err := writesurface.CreateStep(labCtx, sb, store.ActorHuman, b.repo,
		local.Locator+"/"+localStage.Locator, "Solo")
	if err != nil {
		t.Fatal(err)
	}
	localBrief := []byte("the receiver's own brief\n")
	if _, err = writesurface.WriteOnce(labCtx, sb, store.ActorHuman, b.repo, local.Locator, store.KindBrief, localBrief); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Start(labCtx, sb, store.ActorHuman, b.repo, local.Locator+"/"+localStep.Locator); err != nil {
		t.Fatal(err)
	}
	twin, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Archive", "archive-twin")
	if err != nil {
		t.Fatal(err)
	}
	occupant, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Receiver Occupant", "taken")
	if err != nil {
		t.Fatal(err)
	}

	// The sender's full family, then one deliberate locator collision.
	work := labComposeFamily(t, sa, a.repo, "Archive", "archive")
	collide, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Colliding", "taken")
	if err != nil {
		t.Fatal(err)
	}
	collideGroup := sourceGroup(t, sa, collide.ID, store.TypeMatterCreated)

	statusA, err := sa.PairExchange(labCtx, a.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Member}
	done, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.PendingUpload) != 0 {
		t.Fatalf("sender left %d groups un-uploaded: %v", len(done.PendingUpload), done.PendingUpload)
	}
	beforeSync := len(labEvents(t, sb))
	exchangePass(t, sb, cb, statusB.Binding)

	st, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != 0 || st.Conflicts != 1 {
		t.Fatalf("receiver status = %+v", st)
	}
	if st.Evaluated != st.Downloaded || int64(st.Integrated+st.Conflicts) != st.Evaluated {
		t.Fatalf("receiver did not evaluate every download exactly once: %+v", st)
	}
	// Every decision integrated except the deliberate locator collision.
	decisions, err := sb.ExperimentActivities(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) == 0 {
		t.Fatal("receiver recorded no decisions")
	}
	effects := 0
	for _, d := range decisions {
		if len(d.Source.Actions) == 0 {
			t.Fatalf("decision without actions: %+v", d)
		}
		group := d.Source.Actions[0].Event.ID
		want := "integrated"
		if group == collideGroup {
			want = "conflict"
		}
		if d.Disposition != want {
			t.Fatalf("decision for %s = %s (%s), want %s", group, d.Disposition, d.Reason, want)
		}
		if d.Disposition == "integrated" {
			effects += len(d.Effects)
		}
	}
	// Observable parity: the whole family, field by field.
	labAssertFamily(t, sa, sb, b.repo, work)

	// The collision stayed a conflict: no partial apply, no merge, and the
	// sender's Matter still arrived beside the receiver's occupant.
	sourceDecision(t, sb, collideGroup, "conflict")
	if _, err := sb.Node(labCtx, collide.ID); err == nil {
		t.Fatal("locator collision merged or partially applied")
	}
	if _, err := sb.MatterByLocator(labCtx, b.repo, "archive"); err != nil {
		t.Fatal("sender's archive Matter did not arrive despite the unrelated collision", err)
	}

	// The receiver's own work is untouched: same lifecycle, same bytes, same
	// identities — and every appended event is an accounted integration
	// effect, nothing else.
	labState(t, sb, localStep.ID, store.InProgress)
	if got, err := sb.Content(labCtx, local.ID, store.KindBrief); err != nil || !bytes.Equal(got, localBrief) {
		t.Fatalf("receiver's local brief = %q (%v)", got, err)
	}
	if tw := mustNode(t, sb, twin.ID); tw.Title != "Archive" || tw.Locator != "archive-twin" || tw.Lifecycle != store.Planned {
		t.Fatalf("same-title Matter merged or mutated: %+v", tw)
	}
	if oc := mustNode(t, sb, occupant.ID); oc.Title != "Receiver Occupant" || oc.Locator != "taken" || oc.Lifecycle != store.Planned {
		t.Fatalf("collided address was taken over: %+v", oc)
	}
	if appended := len(labEvents(t, sb)) - beforeSync; appended != effects {
		t.Fatalf("receiver appended %d events for %d integrated effects", appended, effects)
	}

	// Retry and reopen stay inert; rebuild preserves everything.
	before := len(labEvents(t, sb))
	exchangePass(t, sb, cb, statusB.Binding)
	exchangePass(t, sa, ca, statusA.Binding, collideGroup)
	exchangePass(t, sb, cb, statusB.Binding)
	if len(labEvents(t, sb)) != before {
		t.Fatal("retry or redelivery appended effects")
	}
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	sb = labOpen(t, b.db)
	if err := sb.ApplyExchangeInbox(labCtx, statusB.Binding); err != nil {
		t.Fatal(err)
	}
	if len(labEvents(t, sb)) != before {
		t.Fatal("offline inbox apply duplicated effects")
	}
	labRebuild(t, sb)
	labAssertFamily(t, sa, sb, b.repo, work)
	t.Logf("family sync over Laravel: %d downloads, %d integrated effects, 1 conflict preserved",
		st.Downloaded, effects)
}

// Step-08a: reverse prerequisite arrival over real transport. Children
// uploaded before their root stay pending — and the ordinary pass that
// finally delivers the root reconsiders them automatically through
// ApplyExchangeInbox's pending recheck, with no explicit ExperimentRecheck
// anywhere in the test.
func TestSharedStoreExchangePendingAutoRecheck(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Root", "root")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	step, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		matter.Locator+"/"+stage.Locator, "Do")
	if err != nil {
		t.Fatal(err)
	}
	brief := []byte("arrives after its subject\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, store.KindBrief, brief); err != nil {
		t.Fatal(err)
	}
	matterGroup := sourceGroup(t, sa, matter.ID, store.TypeMatterCreated)
	stageGroup := sourceGroup(t, sa, stage.ID, store.TypeStageCreated)
	stepGroup := sourceGroup(t, sa, step.ID, store.TypeStepCreated)
	briefGroup := sourceGroup(t, sa, matter.ID, store.TypeContentCreated)

	// Children first: the Stage and Step births and the Brief outrun their
	// root Matter birth.
	exchangePass(t, sa, ca, ba, stageGroup, stepGroup, briefGroup)
	exchangePass(t, sb, cb, bb)
	for _, group := range []string{stageGroup, stepGroup, briefGroup} {
		sourceDecision(t, sb, group, "pending")
	}
	if _, err := sb.Node(labCtx, stage.ID); err == nil {
		t.Fatal("pending birth projected a node")
	}
	before := len(labEvents(t, sb))

	// The ordinary next pass delivers only the root. Receipt's own pending
	// recheck must integrate the whole waiting chain in causal order.
	exchangePass(t, sa, ca, ba, matterGroup)
	exchangePass(t, sb, cb, bb)
	integrated := 0
	for _, group := range []string{matterGroup, stageGroup, stepGroup, briefGroup} {
		d := sourceDecision(t, sb, group, "integrated")
		integrated += len(d.Effects)
	}
	if sn := mustNode(t, sb, stage.ID); sn.Parent != matter.ID {
		t.Fatalf("stage parent = %q, want matter %s", sn.Parent, matter.ID)
	}
	if tn := mustNode(t, sb, step.ID); tn.Parent != stage.ID {
		t.Fatalf("step parent = %q, want stage %s", tn.Parent, stage.ID)
	}
	if got, err := sb.Content(labCtx, matter.ID, store.KindBrief); err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("brief = %q (%v)", got, err)
	}
	if appended := len(labEvents(t, sb)) - before; appended != integrated {
		t.Fatalf("auto-recheck appended %d events for %d integrated effects", appended, integrated)
	}
	// The reconsideration is on the record as rechecks, not fresh imports.
	h := followHistory(t, sb, stageGroup, 2)
	if h[0].Kind != "import" || h[0].Decision.Disposition != "pending" ||
		h[1].Kind != "recheck" || h[1].Decision.Disposition != "integrated" {
		t.Fatalf("stage decision history = %+v", h)
	}
	st, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil || st.Waiting != 0 {
		t.Fatalf("receiver status = %+v %v", st, err)
	}
	labRebuild(t, sb)
}

// Step-08b/c: conflicting terminal edits stay conflicts over real transport —
// each host keeps its own outcome and appends nothing — while finding appends
// made concurrently against the same observed tail both integrate.
func TestSharedStoreExchangeConflictsAndConcurrentFindings(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
	bb := exchangeBind(t, sb, b, endpoint)

	// Shared structure both sides agree on before diverging.
	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Shared", "shared")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	step, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		matter.Locator+"/"+stage.Locator, "Do")
	if err != nil {
		t.Fatal(err)
	}
	stepPath := matter.Locator + "/" + step.Locator
	exchangePass(t, sa, ca, ba,
		sourceGroup(t, sa, matter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, stage.ID, store.TypeStageCreated),
		sourceGroup(t, sa, step.ID, store.TypeStepCreated))
	exchangePass(t, sb, cb, bb)
	if _, err = writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, stepPath); err != nil {
		t.Fatal(err)
	}
	startGroup := sourceGroup(t, sa, step.ID, store.TypeStepStarted)
	exchangePass(t, sa, ca, ba, startGroup)
	exchangePass(t, sb, cb, bb)
	for _, id := range []string{matter.ID, stage.ID, step.ID} {
		labState(t, sb, id, store.InProgress)
	}

	// Divergent terminal edits committed before either host sees the other's.
	if _, err = writesurface.Finish(labCtx, sa, store.ActorHuman, a.repo, stepPath); err != nil {
		t.Fatal(err)
	}
	finishGroup := sourceGroup(t, sa, step.ID, store.TypeStepFinished)
	if _, err = writesurface.Cancel(labCtx, sb, store.ActorHuman, b.repo, stepPath, "local-descoped"); err != nil {
		t.Fatal(err)
	}
	cancelGroup := sourceGroup(t, sb, step.ID, store.TypeStepCanceled)
	exchangePass(t, sa, ca, ba, finishGroup)
	exchangePass(t, sb, cb, bb, cancelGroup)
	beforeA, beforeB := len(labEvents(t, sa)), len(labEvents(t, sb))
	exchangePass(t, sa, ca, ba) // sa receives the cancel
	exchangePass(t, sb, cb, bb) // sb receives the finish
	if len(labEvents(t, sa)) != beforeA || len(labEvents(t, sb)) != beforeB {
		t.Fatal("conflicting terminal edits appended effects")
	}
	sourceDecision(t, sa, cancelGroup, "conflict")
	sourceDecision(t, sb, finishGroup, "conflict")
	labState(t, sa, step.ID, store.Done)
	labState(t, sb, step.ID, store.Canceled)

	// Concurrent finding appends: both hosts append against the same observed
	// tail before either sees the other's segment.
	alpha := []byte("alpha saw the seal")
	bravo := []byte("bravo saw the cancel")
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, "shared", alpha); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.AppendFinding(labCtx, sb, store.ActorHuman, b.repo, "shared", bravo); err != nil {
		t.Fatal(err)
	}
	alphaGroup := sourceGroup(t, sa, matter.ID, store.TypeContentAppended)
	bravoGroup := sourceGroup(t, sb, matter.ID, store.TypeContentAppended)
	exchangePass(t, sa, ca, ba, alphaGroup)
	exchangePass(t, sb, cb, bb, bravoGroup)
	exchangePass(t, sa, ca, ba)
	exchangePass(t, sb, cb, bb)
	for _, s := range []*store.Store{sa, sb} {
		segs, err := s.ContentSegments(labCtx, matter.ID, store.KindFindings)
		if err != nil || len(segs) != 2 {
			t.Fatalf("findings = %+v %v, want 2 segments", segs, err)
		}
		got := map[string]bool{}
		for _, seg := range segs {
			data, err := s.SegmentBytes(labCtx, seg.ID)
			if err != nil {
				t.Fatal(err)
			}
			got[string(data)] = true
		}
		if !got[string(alpha)] || !got[string(bravo)] {
			t.Fatalf("converged findings = %v", got)
		}
	}
	for _, d := range []struct {
		s     *store.Store
		group string
	}{
		{sa, alphaGroup}, {sa, bravoGroup}, {sb, alphaGroup}, {sb, bravoGroup},
	} {
		sourceDecision(t, d.s, d.group, "integrated")
	}

	// Retry, reopen and rebuild all preserve the divergent outcomes and the
	// merged findings.
	before := len(labEvents(t, sb))
	exchangePass(t, sb, cb, bb)
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}
	sb = labOpen(t, b.db)
	if err := sb.ApplyExchangeInbox(labCtx, bb); err != nil {
		t.Fatal(err)
	}
	if len(labEvents(t, sb)) != before {
		t.Fatal("reopen duplicated effects")
	}
	labRebuild(t, sa)
	labRebuild(t, sb)
	labState(t, sa, step.ID, store.Done)
	labState(t, sb, step.ID, store.Canceled)
	segs, err := sb.ContentSegments(labCtx, matter.ID, store.KindFindings)
	if err != nil || len(segs) != 2 {
		t.Fatalf("post-rebuild findings = %+v %v", segs, err)
	}
}

// Step-09: a full family sync from a delivery-bearing source — narrated push
// level, configured backend, bound tracker references and auto backlog
// delegation — leaves the receiver with zero outbox rows, zero open
// execution projections and no provider HTTP, while native control work on
// the receiver still queues and rebuild stays clean.
func TestSharedStoreExchangeFamilySyncMintsNoDelivery(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)

	// Both stores carry delivery-bearing configuration: a configured backend
	// and narrated push level. The receiver is configured too, so any leaked
	// authority would queue for real rather than silently pass.
	for repo, s := range map[string]*store.Store{a.repo: sa, b.repo: sb} {
		if err := s.SetConfig(labCtx, repo, store.TrackerBackendKey, "fixture"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetTrackerPushLevel(labCtx, repo, string(store.TrackerPushNarrated)); err != nil {
			t.Fatal(err)
		}
	}

	// Source control: a bound Matter plus an operator proposal queue native
	// candidates — proof the source really exercised delivery authority. The
	// bind lands after the Matter's last portable event; reference.added is
	// portable now, so its import must itself mint no delivery rows — the
	// inert-data half of this test's claim.
	if _, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Control", "control"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, "control"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Bind(labCtx, sa, store.ActorHuman, a.repo, "control", "fixture:S-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.ProposeTrackerState(labCtx, sa, store.ActorHuman, a.repo,
		"fixture:S-1", store.TrackerCompleted); err != nil {
		t.Fatal(err)
	}
	sourceOutbox, err := sa.Outbox(labCtx, a.repo)
	if err != nil || len(sourceOutbox) == 0 {
		t.Fatalf("source queued no candidates; settings were not delivery-bearing: %+v %v", sourceOutbox, err)
	}
	sawState := false
	for _, entry := range sourceOutbox {
		if entry.Kind == "state" && entry.Ref == "fixture:S-1" {
			sawState = true
		}
	}
	if !sawState {
		t.Fatalf("source queued no state candidate for fixture:S-1: %+v", sourceOutbox)
	}

	// The full family rides narrated payloads; manual backlog push keeps the
	// planned/declined outcomes reachable during the workload.
	work := labComposeFamily(t, sa, a.repo, "Archive", "archive")
	// Auto backlog delegation joins the delivery-bearing settings for one
	// more add: its [entered, delegated] group rides the same family sync.
	if _, err = sa.SetTrackerBacklogPush(labCtx, a.repo, string(store.TrackerBacklogPushAuto)); err != nil {
		t.Fatal(err)
	}
	auto, err := writesurface.BacklogAdd(labCtx, sa, store.ActorHuman, a.repo,
		store.ProvenanceFound, "Auto item", "", "")
	if err != nil {
		t.Fatal(err)
	}
	work.entries[auto.ID] = labBacklog(t, sa, a.repo, auto.ID)
	if got := labBacklog(t, sa, a.repo, auto.ID); got.State != "delegated" {
		t.Fatalf("auto-push entry = %+v, want delegated", got)
	}
	sourceOutbox, err = sa.Outbox(labCtx, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	sawCreate := false
	for _, entry := range sourceOutbox {
		if entry.Kind == "create" {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Fatalf("source queued no create candidate for the auto-delegated entry: %+v", sourceOutbox)
	}

	if _, err = sa.PairExchange(labCtx, a.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	feedURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	rec := &labRecordTransport{hosts: map[string]int{}}
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Transport: rec, Timeout: 10 * time.Second}}
	done, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.PendingUpload) != 0 {
		t.Fatalf("sender left %d groups un-uploaded", len(done.PendingUpload))
	}
	exchangePass(t, sb, cb, statusB.Binding)

	st, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != 0 || st.Conflicts != 0 || st.Integrated != int(st.Evaluated) || st.Evaluated != st.Downloaded {
		t.Fatalf("receiver status = %+v", st)
	}
	labAssertFamily(t, sa, sb, b.repo, work)
	// The bound control Matter arrived too — its narrated start is imported
	// work like any other.
	if cn, err := sb.MatterByLocator(labCtx, b.repo, "control"); err != nil || cn.Lifecycle != store.InProgress {
		t.Fatalf("control Matter = %+v %v", cn, err)
	}

	// Zero delivery side effects: no outbox rows, no open execution
	// projections — and every HTTP request the receiver made went to the
	// exchange feed, never a provider.
	outbox, err := sb.Outbox(labCtx, b.repo)
	if err != nil || len(outbox) != 0 {
		t.Fatalf("imported family queued %d outbox rows: %+v %v", len(outbox), outbox, err)
	}
	if rows := labOutboxRows(t, sb.Path(), b.repo); rows != 0 {
		t.Fatalf("outbox_entries rows = %d", rows)
	}
	if open := labOpenExecution(t, sb.Path()); open != 0 {
		t.Fatalf("imported family opened %d execution rows", open)
	}
	if len(rec.hosts) == 0 {
		t.Fatal("receiver made no exchange requests")
	}
	for host, n := range rec.hosts {
		if host != feedURL.Host {
			t.Fatalf("receiver called unexpected host %s (%d requests)", host, n)
		}
	}

	// Rebuild preserves the zero.
	labRebuild(t, sb)
	if outbox, err = sb.Outbox(labCtx, b.repo); err != nil || len(outbox) != 0 {
		t.Fatalf("rebuild resurrected delivery candidates: %+v %v", outbox, err)
	}
	if open := labOpenExecution(t, sb.Path()); open != 0 {
		t.Fatalf("rebuild opened %d execution rows", open)
	}
	labAssertFamily(t, sa, sb, b.repo, work)

	// Native control: the same verbs authored locally still queue delivery.
	if _, err = writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Post", "post"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Bind(labCtx, sb, store.ActorHuman, b.repo, "post", "fixture:R-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Start(labCtx, sb, store.ActorHuman, b.repo, "post"); err != nil {
		t.Fatal(err)
	}
	outbox, err = sb.Outbox(labCtx, b.repo)
	if err != nil || len(outbox) != 1 || outbox[0].Kind != "state" || outbox[0].Ref != "fixture:R-1" {
		t.Fatalf("native control outbox = %+v %v, want one state candidate for fixture:R-1", outbox, err)
	}
}

// Portable-tail baselines end to end over the real wire. Every portable
// family — births, the lifecycle matrix, amendments, backlog outcomes,
// create-once prose and findings appends — is interleaved on its subject
// with events that never sync (tracker binds, blocked-by edges, render
// bookkeeping). Each portable action must baseline on the last portable
// predecessor, so a fresh peer resolves every dependency instead of
// pending "unknown baseline dependency" forever; the receiver's own
// non-portable divergence must not conflict, while divergent portable work
// still must.
func TestSharedStoreExchangeNonPortableBaseline(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	statusA, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba, bb := statusA.Binding, statusB.Binding

	// render.performed folds to nothing but still lands on any subject as
	// local-only history; execution-scoped types need the repo's tier env.
	clones, err := sa.ClonesOfRepo(labCtx, a.repo)
	if err != nil || len(clones) == 0 {
		t.Fatalf("host repo has no clone: %+v %v", clones, err)
	}
	worktrees, err := sa.WorktreesOfClone(labCtx, clones[0].ID)
	if err != nil || len(worktrees) == 0 {
		t.Fatalf("host clone has no worktree: %+v %v", worktrees, err)
	}
	tier := store.Env{Repo: a.repo, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	stamp := func(subject string) {
		t.Helper()
		if _, err := sa.Commit(labCtx, store.Request{Actor: store.ActorHuman, Env: tier},
			func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
				return []store.Draft{{Type: store.TypeRenderPerformed, Subject: subject}}, nil
			}); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 1 — births and starts, with non-portable predecessors already
	// sitting on the Matter, Stage and Step subjects.
	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Wedge", "wedge")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	step1, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		matter.Locator+"/"+stage.Locator, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	step2, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		matter.Locator+"/"+stage.Locator, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	mp := func(n store.Node) string { return matter.Locator + "/" + n.Locator }
	// reference.bound is the still-non-portable predecessor on this subject:
	// the v5 reference family syncs now, the legacy singleton bind does not.
	labCommitMixed(t, sa, a.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceBound,
			Subject: matter.ID,
			Payload: store.ReferenceBound{Ref: "fixture:W-1"},
		}}, nil
	})
	if _, err = writesurface.DependAdd(labCtx, sa, store.ActorHuman, a.repo, mp(stage), matter.Locator); err != nil {
		t.Fatal(err)
	}
	stageBlocked := sourceGroup(t, sa, stage.ID, store.TypeDependencyAdded)
	if _, err = writesurface.DependAdd(labCtx, sa, store.ActorHuman, a.repo, mp(step1), matter.Locator); err != nil {
		t.Fatal(err)
	}
	stepBlocked := sourceGroup(t, sa, step1.ID, store.TypeDependencyAdded)
	stamp(step1.ID)
	stamp(step2.ID)
	if _, err = writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, matter.Locator); err != nil {
		t.Fatal(err)
	}
	matterStart := sourceGroup(t, sa, matter.ID, store.TypeMatterStarted)
	if _, err = writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, mp(step1)); err != nil {
		t.Fatal(err)
	}
	cascadeGroup := sourceGroup(t, sa, step1.ID, store.TypeStepStarted)
	if _, err = writesurface.Start(labCtx, sa, store.ActorHuman, a.repo, mp(step2)); err != nil {
		t.Fatal(err)
	}
	step2Start := sourceGroup(t, sa, step2.ID, store.TypeStepStarted)
	birthGroups := []string{
		sourceGroup(t, sa, matter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, stage.ID, store.TypeStageCreated),
		sourceGroup(t, sa, step1.ID, store.TypeStepCreated),
		sourceGroup(t, sa, step2.ID, store.TypeStepCreated),
	}

	// The exported start names the portable birth as baseline, never the
	// reference.bound that literally precedes it.
	var createdID string
	for _, ev := range labEvents(t, sa) {
		if ev.Subject == matter.ID && ev.Type == store.TypeMatterCreated {
			createdID = ev.ID
		}
	}
	startSource, err := sa.ExperimentExport(labCtx, "pin", matterStart)
	if err != nil {
		t.Fatal(err)
	}
	pinned := false
	for _, act := range startSource.Actions {
		if act.Event.Type == store.TypeMatterStarted {
			pinned = true
			if act.Baseline != createdID {
				t.Fatalf("started baseline = %q, want portable birth %q", act.Baseline, createdID)
			}
		}
	}
	if !pinned {
		t.Fatal("start source has no matter.started action")
	}

	done, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil || len(done.PendingUpload) != 0 {
		t.Fatalf("phase 1 left groups un-uploaded: %+v %v", done, err)
	}
	exchangePass(t, sb, cb, bb)
	st, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil || st.Waiting != 0 || st.Conflicts != 0 {
		t.Fatalf("phase 1 receiver status = %+v %v", st, err)
	}
	for _, group := range append(birthGroups, matterStart, cascadeGroup, step2Start, stageBlocked, stepBlocked) {
		sourceDecision(t, sb, group, "integrated")
	}
	for _, id := range []string{matter.ID, stage.ID, step1.ID, step2.ID} {
		labState(t, sb, id, store.InProgress)
	}
	// The synced edges landed verbatim: Stage and Step both wait on the Matter.
	for _, blocked := range []string{stage.ID, step1.ID} {
		edge, found, err := sb.EdgeBetween(labCtx, blocked, matter.ID)
		if err != nil || !found || edge.Blocked != blocked || edge.Blocker != matter.ID {
			t.Fatalf("receiver edge %s<-matter = %+v %v %v", blocked, edge, found, err)
		}
	}
	// No non-portable family ever crossed the wire: the receiver has not
	// committed a legacy bind of its own yet, so any reference.bound here
	// could only be the sender's.
	for _, ev := range labEvents(t, sb) {
		switch ev.Type {
		case store.TypeRenderPerformed, store.TypeReferenceBound:
			t.Fatalf("non-portable event %s reached the receiver", ev.Type)
		}
	}

	// Phase 2 — every remaining portable family, each behind a non-portable
	// predecessor on its own subject.
	brief := []byte("# wedge brief\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, store.KindBrief, brief); err != nil {
		t.Fatal(err)
	}
	workplan := []byte("phase workplan\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, mp(stage), store.KindWorkplan, workplan); err != nil {
		t.Fatal(err)
	}
	stamp(stage.ID)
	if _, err = writesurface.ReorderStep(labCtx, sa, store.ActorHuman, a.repo, mp(stage),
		[]string{mp(step2), mp(step1)}); err != nil {
		t.Fatal(err)
	}
	stamp(step1.ID)
	body := []byte("alpha body\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, mp(step1), store.KindBody, body); err != nil {
		t.Fatal(err)
	}
	// dependency.removed is portable too: the Stage's edge on the Matter is
	// dissolved on the sender and must tombstone on the receiver.
	if err = writesurface.DependRemove(labCtx, sa, store.ActorHuman, a.repo, mp(stage), matter.Locator); err != nil {
		t.Fatal(err)
	}
	stageUnblocked := sourceGroup(t, sa, stage.ID, store.TypeDependencyRemoved)
	if _, err = writesurface.Finish(labCtx, sa, store.ActorHuman, a.repo, mp(step1)); err != nil {
		t.Fatal(err)
	}
	// Findings on the Matter and a backlog entry; the entry's plan waits
	// behind render bookkeeping on its own subject.
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, matter.Locator,
		[]byte("first matter finding")); err != nil {
		t.Fatal(err)
	}
	entry, err := writesurface.BacklogAdd(labCtx, sa, store.ActorHuman, a.repo,
		store.ProvenanceIntake, "Entry item", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, entry.ID,
		[]byte("triage note")); err != nil {
		t.Fatal(err)
	}
	stamp(entry.ID)
	if _, err = writesurface.BacklogPlan(labCtx, sa, store.ActorHuman, a.repo, entry.ID, matter.Locator); err != nil {
		t.Fatal(err)
	}
	stamp(matter.ID)
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, matter.Locator,
		[]byte("second matter finding")); err != nil {
		t.Fatal(err)
	}

	done, err = sa.RunExchange(labCtx, ca, a.repo)
	if err != nil || len(done.PendingUpload) != 0 {
		t.Fatalf("phase 2 left groups un-uploaded: %+v %v", done, err)
	}
	exchangePass(t, sb, cb, bb)
	st, err = sb.ExchangeStatus(labCtx, b.repo)
	if err != nil || st.Waiting != 0 || st.Conflicts != 0 {
		t.Fatalf("phase 2 receiver status = %+v %v", st, err)
	}
	if got, err := sb.Content(labCtx, matter.ID, store.KindBrief); err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("receiver brief = %q (%v)", got, err)
	}
	if got, err := sb.Content(labCtx, stage.ID, store.KindWorkplan); err != nil || !bytes.Equal(got, workplan) {
		t.Fatalf("receiver workplan = %q (%v)", got, err)
	}
	if got, err := sb.Content(labCtx, step1.ID, store.KindBody); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("receiver body = %q (%v)", got, err)
	}
	if order := labChildIDs(t, sb, stage.ID); !reflect.DeepEqual(order, []string{step2.ID, step1.ID}) {
		t.Fatalf("receiver stage order = %v", order)
	}
	sourceDecision(t, sb, stageUnblocked, "integrated")
	if _, found, err := sb.EdgeBetween(labCtx, stage.ID, matter.ID); err != nil || found {
		t.Fatalf("receiver kept the removed stage edge: found=%v err=%v", found, err)
	}
	if _, found, err := sb.EdgeBetween(labCtx, step1.ID, matter.ID); err != nil || !found {
		t.Fatalf("receiver lost the surviving step edge: found=%v err=%v", found, err)
	}
	labState(t, sb, step1.ID, store.Done)
	if got := labBacklog(t, sb, b.repo, entry.ID); got.State != "planned" || got.Matter != matter.ID {
		t.Fatalf("receiver backlog entry = %+v", got)
	}
	segs, err := sb.ContentSegments(labCtx, matter.ID, store.KindFindings)
	if err != nil || len(segs) != 2 {
		t.Fatalf("receiver matter findings = %+v %v", segs, err)
	}

	// Phase 3 — the receiver's own legacy bind is local-only divergence on
	// the same subject; the sender's pause and resume must still integrate.
	labCommitMixed(t, sb, b.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeReferenceBound,
			Subject: matter.ID,
			Payload: store.ReferenceBound{Ref: "fixture:LOCAL"},
		}}, nil
	})
	if _, err = writesurface.Pause(labCtx, sa, store.ActorHuman, a.repo, matter.Locator); err != nil {
		t.Fatal(err)
	}
	matterPause := sourceGroup(t, sa, matter.ID, store.TypeMatterPaused)
	if _, err = writesurface.Resume(labCtx, sa, store.ActorHuman, a.repo, matter.Locator); err != nil {
		t.Fatal(err)
	}
	matterResume := sourceGroup(t, sa, matter.ID, store.TypeMatterResumed)
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, matterPause, "integrated")
	sourceDecision(t, sb, matterResume, "integrated")
	labState(t, sb, matter.ID, store.InProgress)
	// The receiver's local legacy bind never became sender history.
	var binds int
	for _, ev := range labEvents(t, sa) {
		if ev.Subject == matter.ID && ev.Type == store.TypeReferenceBound {
			binds++
		}
	}
	if binds != 1 {
		t.Fatalf("sender matter carries %d reference.bound events, want only its own", binds)
	}

	// Phase 4 — divergent portable work still conflicts both ways: the
	// receiver pauses Beta locally while the sender finishes it.
	if _, err = writesurface.Pause(labCtx, sb, store.ActorHuman, b.repo, mp(step2)); err != nil {
		t.Fatal(err)
	}
	step2Pause := sourceGroup(t, sb, step2.ID, store.TypeStepPaused)
	if _, err = writesurface.Finish(labCtx, sa, store.ActorHuman, a.repo, mp(step2)); err != nil {
		t.Fatal(err)
	}
	step2Finish := sourceGroup(t, sa, step2.ID, store.TypeStepFinished)
	exchangePass(t, sa, ca, ba, step2Finish)
	exchangePass(t, sb, cb, bb, step2Pause)
	exchangePass(t, sa, ca, ba)
	sourceDecision(t, sa, step2Pause, "conflict")
	sourceDecision(t, sb, step2Finish, "conflict")
	labState(t, sa, step2.ID, store.Done)
	labState(t, sb, step2.ID, store.Paused)

	// Retry, reopen and rebuild preserve the outcomes.
	before := len(labEvents(t, sb))
	exchangePass(t, sb, cb, bb)
	if len(labEvents(t, sb)) != before {
		t.Fatal("retry appended effects")
	}
	labRebuild(t, sa)
	labRebuild(t, sb)
	labState(t, sa, step2.ID, store.Done)
	labState(t, sb, step2.ID, store.Paused)
	st, err = sb.ExchangeStatus(labCtx, b.repo)
	if err != nil || st.Waiting != 0 || st.Conflicts != 1 {
		t.Fatalf("final receiver status = %+v %v", st, err)
	}
}
