//go:build sharedstoreexperiment

package store_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// relationFixture is a paired exchange with a synced skeleton: a Matter, a
// Stage and two Steps born on host a and delivered to host b, so dependency
// facts always have live endpoints on both sides unless a test says otherwise.
type relationFixture struct {
	a, b                        labPair
	sa, sb                      *store.Store
	ca, cb                      store.ExchangeClient
	ba, bb                      store.ExchangeBinding
	matter, stage, step1, step2 store.Node
}

func newRelationFixture(t *testing.T) relationFixture {
	t.Helper()
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: http.DefaultClient}
	cb := store.ExchangeClient{HTTP: http.DefaultClient}
	statusA, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba, bb := statusA.Binding, statusB.Binding
	f := relationFixture{a: a, b: b, sa: sa, sb: sb, ca: ca, cb: cb, ba: ba, bb: bb}
	if f.matter, err = writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Rel", "rel"); err != nil {
		t.Fatal(err)
	}
	if f.stage, err = writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, "rel", "Phase"); err != nil {
		t.Fatal(err)
	}
	if f.step1, err = writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "rel/"+f.stage.Locator, "One"); err != nil {
		t.Fatal(err)
	}
	if f.step2, err = writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "rel/"+f.stage.Locator, "Two"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sa.RunExchange(labCtx, f.ca, f.a.repo); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, f.sb, f.cb, f.bb)
	for _, id := range []string{f.matter.ID, f.stage.ID, f.step1.ID, f.step2.ID} {
		if _, err := f.sb.Node(labCtx, id); err != nil {
			t.Fatalf("birth did not reach the receiver: %v", err)
		}
	}
	return f
}

func relationEdge(t *testing.T, s *store.Store, blocked, blocker string) (store.Edge, bool) {
	t.Helper()
	e, found, err := s.EdgeBetween(labCtx, blocked, blocker)
	if err != nil {
		t.Fatal(err)
	}
	return e, found
}

// A sender's depend add lands on the receiver as a live edge carrying the
// source edge identity verbatim; the matching removal tombstones it. Retry,
// re-upload and the sender's own echo are all inert, and a rebuild replays
// the same edge set from the log.
func TestSharedStoreDependencyEdgeRoundTrip(t *testing.T) {
	f := newRelationFixture(t)

	edge, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID)
	if err != nil {
		t.Fatal(err)
	}
	addGroup := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)
	exchangePass(t, f.sa, f.ca, f.ba, addGroup)
	exchangePass(t, f.sb, f.cb, f.bb)

	got, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID)
	if !found || got.ID != edge.ID || got.Blocked != f.step1.ID || got.Blocker != f.step2.ID {
		t.Fatalf("receiver edge = %+v found=%v, want source edge %s", got, found, edge.ID)
	}
	blocked, err := f.sb.BlockedBy(labCtx, f.step1.ID)
	if err != nil || len(blocked) != 1 || blocked[0].ID != edge.ID {
		t.Fatalf("BlockedBy = %+v %v", blocked, err)
	}
	if edges, err := f.sb.LiveEdges(labCtx); err != nil || len(edges) != 1 {
		t.Fatalf("LiveEdges = %+v %v", edges, err)
	}
	sourceDecision(t, f.sb, addGroup, "integrated")

	// Exact retry and re-export after the pass are inert: the retained
	// decision answers, no second edge or effect appears.
	sbEvents := len(labEvents(t, f.sb))
	saEvents := len(labEvents(t, f.sa))
	exchangePass(t, f.sa, f.ca, f.ba, addGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	exchangePass(t, f.sa, f.ca, f.ba)
	if len(labEvents(t, f.sb)) != sbEvents || len(labEvents(t, f.sa)) != saEvents {
		t.Fatal("retry or self-echo appended effects")
	}
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 1 {
		t.Fatalf("LiveEdges after retry = %+v", edges)
	}

	if err := writesurface.DependRemove(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	remGroup := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyRemoved)
	exchangePass(t, f.sa, f.ca, f.ba, remGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, remGroup, "integrated")
	if _, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); found {
		t.Fatal("synced dependency.removed left the edge in force")
	}
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 0 {
		t.Fatalf("LiveEdges after removal = %+v", edges)
	}
	if err := f.sb.Rebuild(labCtx); err != nil {
		t.Fatal(err)
	}
	if _, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); found {
		t.Fatal("rebuild resurrected the removed edge")
	}
}

// The edge fact can overtake its blocker's birth: the group pends with a
// named reason, ExchangeStatus counts it as waiting, and the endpoint's own
// group resolving on a later pass flips the retained decision to integrated.
func TestSharedStoreDependencyAwaitsEndpoint(t *testing.T) {
	f := newRelationFixture(t)
	late, err := writesurface.CreateMatter(labCtx, f.sa, store.ActorHuman, f.a.repo, "Late", "late")
	if err != nil {
		t.Fatal(err)
	}
	lateBirth := sourceGroup(t, f.sa, late.ID, store.TypeMatterCreated)
	if _, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, late.ID); err != nil {
		t.Fatal(err)
	}
	depGroup := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)

	exchangePass(t, f.sa, f.ca, f.ba, depGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, depGroup, "pending")
	if !strings.Contains(d.Reason, "awaiting") {
		t.Fatalf("pending reason = %q, want a named wait", d.Reason)
	}
	if _, found := relationEdge(t, f.sb, f.step1.ID, late.ID); found {
		t.Fatal("pending edge materialized before its endpoint")
	}
	st, err := f.sb.ExchangeStatus(labCtx, f.b.repo)
	if err != nil || st.Waiting != 1 {
		t.Fatalf("status = %+v %v, want one waiting group", st, err)
	}

	exchangePass(t, f.sa, f.ca, f.ba, lateBirth)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, depGroup, "integrated")
	if _, found := relationEdge(t, f.sb, f.step1.ID, late.ID); !found {
		t.Fatal("edge did not integrate once its endpoint arrived")
	}
	st, err = f.sb.ExchangeStatus(labCtx, f.b.repo)
	if err != nil || st.Waiting != 0 {
		t.Fatalf("status after endpoint = %+v %v", st, err)
	}
}

// A birth and the edge naming it can share one committed group: the unit
// classifies member-by-member against candidate state, so the depend fact
// sees the endpoints earlier members just folded and the whole group lands
// atomically.
func TestSharedStoreDependencyBirthAndEdgeSameGroup(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: http.DefaultClient}
	cb := store.ExchangeClient{HTTP: http.DefaultClient}
	statusA, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba, bb := statusA.Binding, statusB.Binding
	var mid, sid, eid string
	events := labCommitMixed(t, sa, a.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		mid, sid, eid = tx.NewID(), tx.NewID(), tx.NewID()
		return []store.Draft{
			{Type: store.TypeMatterCreated, Subject: mid, Payload: store.NodeBirth{Title: "Atomic", Locator: "atomic", SortKey: 0}},
			{Type: store.TypeStepCreated, Subject: sid, Payload: store.NodeBirth{Title: "Do", Locator: "step-01", Parent: mid, SortKey: 0}, Cause: 0},
			{Type: store.TypeDependencyAdded, Subject: sid, Payload: store.DependencyChange{Edge: eid, Blocker: mid}, Cause: 1},
		}, nil
	})
	group := events[0].Correlation
	exchangePass(t, sa, ca, ba, group)
	exchangePass(t, sb, cb, bb)
	sourceDecision(t, sb, group, "integrated")
	n, err := sb.Node(labCtx, sid)
	if err != nil || n.Kind != store.ScaleStep || n.Parent != mid {
		t.Fatalf("step = %+v %v", n, err)
	}
	if got, found := relationEdge(t, sb, sid, mid); !found || got.ID != eid {
		t.Fatalf("same-group edge = %+v found=%v", got, found)
	}
}

// Two hosts each add the opposite edge offline: each is valid where it was
// written, and together they would loop. Neither side's classifier can call
// that pending — no retained evidence can break the loop — so both become
// inspectable conflicts that a later local removal plus explicit recheck
// resolves.
func TestSharedStoreDependencyCycleConflict(t *testing.T) {
	f := newRelationFixture(t)
	edgeA, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID)
	if err != nil {
		t.Fatal(err)
	}
	gA := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)
	edgeB, err := writesurface.DependAdd(labCtx, f.sb, store.ActorHuman, f.b.repo, f.step2.ID, f.step1.ID)
	if err != nil {
		t.Fatal(err)
	}
	gB := sourceGroup(t, f.sb, f.step2.ID, store.TypeDependencyAdded)

	exchangePass(t, f.sa, f.ca, f.ba, gA)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, gA, "conflict")
	if !strings.Contains(d.Reason, "cycle") {
		t.Fatalf("cycle conflict reason = %q", d.Reason)
	}
	exchangePass(t, f.sb, f.cb, f.bb, gB)
	exchangePass(t, f.sa, f.ca, f.ba)
	sourceDecision(t, f.sa, gB, "conflict")

	// Each side keeps exactly its own edge; the synced claims never merged.
	if got, found := relationEdge(t, f.sa, f.step1.ID, f.step2.ID); !found || got.ID != edgeA.ID {
		t.Fatalf("sender edge = %+v found=%v", got, found)
	}
	if got, found := relationEdge(t, f.sb, f.step2.ID, f.step1.ID); !found || got.ID != edgeB.ID {
		t.Fatalf("receiver local edge = %+v found=%v", got, found)
	}
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 1 {
		t.Fatalf("LiveEdges = %+v", edges)
	}
	st, err := f.sb.ExchangeStatus(labCtx, f.b.repo)
	if err != nil || st.Conflicts != 1 {
		t.Fatalf("status = %+v %v, want one visible conflict", st, err)
	}

	// The receiver removes its local edge; the conflicted synced add is not
	// auto-rechecked (conflicts are settled evidence) but re-evaluates on
	// explicit recheck and then integrates.
	if err := writesurface.DependRemove(labCtx, f.sb, store.ActorHuman, f.b.repo, f.step2.ID, f.step1.ID); err != nil {
		t.Fatal(err)
	}
	gR := sourceGroup(t, f.sb, f.step2.ID, store.TypeDependencyRemoved)
	exchangePass(t, f.sb, f.cb, f.bb, gR)
	exchangePass(t, f.sa, f.ca, f.ba)
	d = sourceDecision(t, f.sb, gA, "conflict")
	followRecheck(t, f.sb, d.Source, "integrated")
	if got, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); !found {
		t.Fatalf("rechecked edge = %+v found=%v", got, found)
	}
	// On the sender the removal names an edge that never integrated there:
	// it waits visibly rather than conflict or vanish.
	sourceDecision(t, f.sa, gR, "pending")
}

// The same loop can be transient: a removal of the closing edge is already
// retained pending when the add evaluates, so the add waits on "cycle
// resolution" instead of conflicting. Once the removal lands the pending
// group integrates on a later pass and the add follows — no permanent
// conflict, no silent acceptance.
func TestSharedStoreDependencyCycleAwaitsResolution(t *testing.T) {
	f := newRelationFixture(t)
	// On the sender each fact is legal when written: add/remove E1, then
	// add/remove E2 — the log never holds a loop.
	if _, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DependRemove(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step2.ID, f.step1.ID); err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DependRemove(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step2.ID, f.step1.ID); err != nil {
		t.Fatal(err)
	}
	gE1add := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)
	gE1rem := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyRemoved)
	gE2add := sourceGroup(t, f.sa, f.step2.ID, store.TypeDependencyAdded)
	gE2rem := sourceGroup(t, f.sa, f.step2.ID, store.TypeDependencyRemoved)

	// Feed order is the fixture's own: E2's removal arrives before its add
	// (pending "awaiting edge"), E2's add lands, then E1's add would close
	// the loop E2 still holds — but E2's pending removal is exactly the
	// in-flight evidence that can break it, so the add waits too.
	exchangePass(t, f.sa, f.ca, f.ba, gE2rem, gE2add, gE1add)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, gE1add, "pending")
	if !strings.Contains(d.Reason, "cycle") {
		t.Fatalf("cycle-waiting reason = %q", d.Reason)
	}
	sourceDecision(t, f.sb, gE2add, "integrated")
	sourceDecision(t, f.sb, gE2rem, "integrated")
	if _, found := relationEdge(t, f.sb, f.step2.ID, f.step1.ID); found {
		t.Fatal("E2 outlived its synced removal")
	}
	if _, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); found {
		t.Fatal("E1 materialized while its cycle was unresolved")
	}

	// The next pass reconsiders the waiting add against the now-broken loop.
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, gE1add, "integrated")
	if got, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); !found {
		t.Fatalf("resolved edge = %+v found=%v", got, found)
	}
	// E1's own removal was never sent yet; releasing it completes the
	// round trip with every group integrated and no edge left in force.
	exchangePass(t, f.sa, f.ca, f.ba, gE1rem)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, gE1rem, "integrated")
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 0 {
		t.Fatalf("LiveEdges at end = %+v", edges)
	}
	st, err := f.sb.ExchangeStatus(labCtx, f.b.repo)
	if err != nil || st.Waiting != 0 || st.Conflicts != 0 {
		t.Fatalf("final status = %+v %v", st, err)
	}
}

// A removal can precede its add on the wire: the tombstone request pends
// "awaiting edge" and integrates on the same pass the add lands, because
// ApplyExchangeInbox reconsiders pending groups after the batch.
func TestSharedStoreDependencyRemovedBeforeAdd(t *testing.T) {
	f := newRelationFixture(t)
	if _, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	gAdd := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)
	if err := writesurface.DependRemove(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	gRem := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyRemoved)

	exchangePass(t, f.sa, f.ca, f.ba, gRem)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, gRem, "pending")
	if !strings.Contains(d.Reason, "awaiting") {
		t.Fatalf("pending reason = %q", d.Reason)
	}
	exchangePass(t, f.sa, f.ca, f.ba, gAdd)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, gAdd, "integrated")
	sourceDecision(t, f.sb, gRem, "integrated")
	if _, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); found {
		t.Fatal("edge outlived the removal that arrived first")
	}
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 0 {
		t.Fatalf("LiveEdges = %+v", edges)
	}
}

// Permanent incompatibilities stay conflicts, never merges: a spent edge
// identity, a self-edge, a malformed payload, a tombstoned endpoint and an
// endpoint that never arrives each get their own inspectable outcome.
func TestSharedStoreDependencyConflicts(t *testing.T) {
	p := newLabPair(t)
	// fluke holds a local edge the sender knows nothing about.
	var localEdge string
	labCommitMixed(t, p.fluke, p.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		localEdge = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeDependencyAdded,
			Subject: p.step1,
			Payload: store.DependencyChange{Edge: localEdge, Blocker: p.matter},
		}}, nil
	})
	// hybrid exports a real depend group: step1 blocked-by step2.
	var remoteEdge string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		remoteEdge = tx.NewID()
		return []store.Draft{{
			Type:    store.TypeDependencyAdded,
			Subject: p.step1,
			Payload: store.DependencyChange{Edge: remoteEdge, Blocker: p.step2},
		}}, nil
	})
	// Each variant is its own source group: a fresh event identity keeps the
	// immutable-source check from reading payload mutations as identity reuse.
	mutate := func(edge, blocker string) store.ExperimentSource {
		f := source
		f.Actions = slices.Clone(source.Actions)
		var d store.DependencyChange
		if err := json.Unmarshal(f.Actions[0].Event.Payload, &d); err != nil {
			t.Fatal(err)
		}
		d.Edge, d.Blocker = edge, blocker
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		f.Actions[0].Event.Payload = raw
		f.Actions[0].Event.ID = p.fluke.NewID()
		f.Actions[0].Event.Causation = f.Actions[0].Event.ID
		f.Actions[0].Event.Correlation = f.Actions[0].Event.ID
		return f
	}

	// A foreign claim on the spent edge identity conflicts even though it
	// names different endpoints — identities are never merged.
	d := labImportBound(t, p.fluke, mutate(localEdge, p.step2), p.repo, "conflict")
	if !strings.Contains(d.Reason, "edge identity") {
		t.Fatalf("occupied-identity reason = %q", d.Reason)
	}
	if got, found := relationEdge(t, p.fluke, p.step1, p.matter); !found || got.ID != localEdge {
		t.Fatalf("local edge after collision = %+v found=%v", got, found)
	}
	// A self-edge is the degenerate cycle the fold's CHECK would refuse.
	d = labImportBound(t, p.fluke, mutate(p.fluke.NewID(), p.step1), p.repo, "conflict")
	if !strings.Contains(d.Reason, "self-edge") && !strings.Contains(d.Reason, "invalid") {
		t.Fatalf("self-edge reason = %q", d.Reason)
	}
	// Malformed payloads are retained evidence, not pending work.
	bad := mutate(p.fluke.NewID(), p.step2)
	bad.Actions[0].Event.Payload = json.RawMessage(`{"edge":"x","blocker":"y","extra":1}`)
	labImportBound(t, p.fluke, bad, p.repo, "conflict")
	// An endpoint that never arrives stays a named wait.
	d = labImportBound(t, p.fluke, mutate(p.fluke.NewID(), p.fluke.NewID()), p.repo, "pending")
	if !strings.Contains(d.Reason, "awaiting") {
		t.Fatalf("missing-endpoint reason = %q", d.Reason)
	}
	// A tombstoned endpoint can never hold an edge: permanent, not pending.
	labCommitMixed(t, p.fluke, p.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    store.TypeStepRemoved,
			Subject: p.step2,
			Payload: store.Removed{},
		}}, nil
	})
	d = labImportBound(t, p.fluke, source, p.repo, "conflict")
	if !strings.Contains(d.Reason, "removed") {
		t.Fatalf("tombstoned-endpoint reason = %q", d.Reason)
	}
	if _, found := relationEdge(t, p.fluke, p.step1, p.step2); found {
		t.Fatal("conflicting adds materialized an edge")
	}
}

// A mixed commit uploads only its portable unit: the dependency edge is the
// derivative's single action while the config.set stays retained evidence —
// never classified, never applied.
func TestSharedStoreDependencyMixedGroup(t *testing.T) {
	f := newRelationFixture(t)
	var eid string
	events := labCommitMixed(t, f.sa, f.a.repo, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		eid = tx.NewID()
		return []store.Draft{
			{Type: store.TypeDependencyAdded, Subject: f.step1.ID, Payload: store.DependencyChange{Edge: eid, Blocker: f.step2.ID}},
			{Type: store.TypeConfigSet, Subject: f.a.repo, Payload: store.ConfigSet{Key: "rel.key", Value: "v"}, Cause: 0},
		}, nil
	})
	group := events[0].Correlation
	exchangePass(t, f.sa, f.ca, f.ba, group)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, group, "integrated")
	if d.Source.Derivative == nil || len(d.Effects) != 1 {
		t.Fatalf("decision is not a one-action derivative: %+v", d)
	}
	var sawConfig bool
	for _, a := range d.Source.Derivative.Omitted {
		if a.Event.Type == store.TypeConfigSet {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Fatal("omitted evidence lost the config.set")
	}
	if got, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); !found || got.ID != eid {
		t.Fatalf("derivative edge = %+v found=%v", got, found)
	}
	if value, found, err := f.sb.Config(labCtx, f.b.repo, "rel.key"); err != nil || found || value != "" {
		t.Fatalf("omitted config produced an effect: %q %v %v", value, found, err)
	}
}

// gateSyncGroup exports the committed group that produced subject's newest
// event of kind on s — sourceGroup's shape for the gate/reference verbs,
// which return no correlation of their own.
func gateSyncGroup(t *testing.T, s *store.Store, host, subject, kind string) store.ExperimentSource {
	t.Helper()
	return labBacklogSource(t, s, host, subject, kind)
}

// The gate family crosses the wire as retained facts: a Repo-keyed
// declaration lands under the paired Repo (subject remapped, never a
// second opinion), a close satisfies the receiver's gate_state, an
// exemption repair lands exempt, and a dismissal — stamped
// integrationEffectActor on import — satisfies dismissalActorAllowed as
// evidence rather than a local actor claim. Outbox stays empty throughout:
// imported gate facts queue no delivery candidates.
func TestSharedStoreGateFamilyRoundTrip(t *testing.T) {
	f := newRelationFixture(t)

	if err := writesurface.DeclareGate(labCtx, f.sa, f.a.repo, "sync-gate", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	declared := gateSyncGroup(t, f.sa, "A", f.a.repo, store.TypeGateDeclared)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.a.repo, store.TypeGateDeclared))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, declared.Actions[0].Event.ID, "integrated")
	declarations, err := f.sb.GateDeclarations(labCtx, f.b.repo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range declarations {
		if d.Gate == "sync-gate" && d.Scale == store.ScaleStep {
			found = true
		}
	}
	if !found {
		t.Fatalf("receiver declarations = %+v, want sync-gate@step", declarations)
	}
	// The stamped effect keys at the paired Repo, proving the subject remap.
	for _, ev := range labEvents(t, f.sb) {
		if ev.Type == store.TypeGateDeclared {
			if ev.Subject != f.b.repo || ev.Repo != f.b.repo {
				t.Fatalf("imported declaration subject/repo = %s/%s", ev.Subject, ev.Repo)
			}
		}
	}

	// A close before its declaration would wait; the declaration already
	// landed, so this integrates and satisfies the receiver's gate_state.
	if _, err := writesurface.CloseGate(labCtx, f.sa, store.ActorHuman, f.a.repo, "sync-gate", f.step1.ID); err != nil {
		t.Fatal(err)
	}
	closed := gateSyncGroup(t, f.sa, "A", f.step1.ID, store.TypeGateClosed)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.step1.ID, store.TypeGateClosed))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, closed.Actions[0].Event.ID, "integrated")
	satisfied, err := f.sb.GateSatisfied(labCtx, f.b.repo, f.step1.ID, "sync-gate")
	if err != nil || !satisfied {
		t.Fatalf("receiver gate satisfaction = %v %v", satisfied, err)
	}

	// The exemption repair is the store verb's own event: it needs no
	// Done lifecycle at the fold, only a live node at the declared scale.
	if err := f.sa.RepairGateExemption(labCtx, f.a.repo, "sync-gate", f.step2.ID); err != nil {
		t.Fatal(err)
	}
	repaired := gateSyncGroup(t, f.sa, "A", f.step2.ID, store.TypeGateExemptionRepaired)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.step2.ID, store.TypeGateExemptionRepaired))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, repaired.Actions[0].Event.ID, "integrated")
	exempt, err := f.sb.GateExempt(labCtx, f.b.repo, f.step2.ID, "sync-gate")
	if err != nil || !exempt {
		t.Fatalf("receiver exemption = %v %v", exempt, err)
	}

	// Dismissal needs a Done subject with the gate still open: step-02 is
	// exempt and step-01 is closed, so a third step runs the whole arc.
	step3, err := writesurface.CreateStep(labCtx, f.sa, store.ActorHuman, f.a.repo, "rel/"+f.stage.Locator, "Three")
	if err != nil {
		t.Fatal(err)
	}
	birth3 := gateSyncGroup(t, f.sa, "A", step3.ID, store.TypeStepCreated)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, step3.ID, store.TypeStepCreated))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, birth3.Actions[0].Event.ID, "integrated")
	step3Path := "rel/" + f.stage.Locator + "/" + step3.Locator
	if _, err := writesurface.Start(labCtx, f.sa, store.ActorHuman, f.a.repo, step3Path); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, step3.ID, store.TypeStepStarted))
	exchangePass(t, f.sb, f.cb, f.bb)
	if _, err := writesurface.Finish(labCtx, f.sa, store.ActorHuman, f.a.repo, step3Path); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, step3.ID, store.TypeStepFinished))
	exchangePass(t, f.sb, f.cb, f.bb)
	labState(t, f.sb, step3.ID, store.Done)
	if _, err := writesurface.DismissGate(labCtx, f.sa, store.ActorHuman, f.a.repo, "sync-gate", step3.ID, "gate superseded"); err != nil {
		t.Fatal(err)
	}
	dismissed := gateSyncGroup(t, f.sa, "A", step3.ID, store.TypeGateDismissed)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, step3.ID, store.TypeGateDismissed))
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, dismissed.Actions[0].Event.ID, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("dismissal effects = %v", d.Effects)
	}
	// The imported dismissal is stamped integrationEffectActor — the actor
	// admission that lets the fold accept retained source evidence.
	var sawDismiss bool
	for _, ev := range labEvents(t, f.sb) {
		if ev.Type == store.TypeGateDismissed && ev.ID == d.Effects[0] {
			sawDismiss = true
			if ev.Actor != store.SystemActor("shared-store-experiment") {
				t.Fatalf("imported dismissal actor = %s", ev.Actor)
			}
		}
	}
	if !sawDismiss {
		t.Fatal("no imported gate.dismissed effect on the receiver")
	}
	satisfied, err = f.sb.GateSatisfied(labCtx, f.b.repo, step3.ID, "sync-gate")
	if err != nil || !satisfied {
		t.Fatalf("receiver dismissal satisfaction = %v %v", satisfied, err)
	}
	if outbox, err := f.sb.Outbox(labCtx, f.b.repo); err != nil || len(outbox) != 0 {
		t.Fatalf("imported gate facts queued candidates: %+v %v", outbox, err)
	}
	if err := f.sb.Rebuild(labCtx); err != nil {
		t.Fatal(err)
	}
	if satisfied, err = f.sb.GateSatisfied(labCtx, f.b.repo, step3.ID, "sync-gate"); err != nil || !satisfied {
		t.Fatalf("post-rebuild dismissal satisfaction = %v %v", satisfied, err)
	}
}

// referenceSyncGroup exports the committed group that produced subject's
// newest event of kind — sourceGroup's shape for the bind verbs.
func referenceSyncGroup(t *testing.T, s *store.Store, host, subject, kind string) store.ExperimentSource {
	t.Helper()
	return labBacklogSource(t, s, host, subject, kind)
}

// matterRefs reads one Matter's active tracker references on s.
func matterRefs(t *testing.T, s *store.Store, matter string) []string {
	t.Helper()
	refs, err := s.TrackerReferences(labCtx, matter)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

// The reference family crosses as inert membership facts: add, remove and
// rebind land in tracker_references under the shared Matter identity, the
// fold's revive-on-readd path works, retries and rebuilds are stable, and —
// the delivery-authority half of the claim — every imported reference event
// queues zero outbox rows even with tracker push configured on the receiver.
func TestSharedStoreReferenceFamilyRoundTrip(t *testing.T) {
	f := newRelationFixture(t)
	// Give the receiver delivery-bearing config so a leaked candidate would
	// be visible: imported refs must stay inert data, never queued work.
	if _, err := f.sb.SetTrackerPushLevel(labCtx, f.b.repo, string(store.TrackerPushBoundary)); err != nil {
		t.Fatal(err)
	}
	outboxBefore := labOutboxRows(t, f.b.db, f.b.repo)

	if _, err := writesurface.Bind(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.Locator, "fixture:GH-1"); err != nil {
		t.Fatal(err)
	}
	added := referenceSyncGroup(t, f.sa, "A", f.matter.ID, store.TypeReferenceAdded)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.matter.ID, store.TypeReferenceAdded))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, added.Actions[0].Event.ID, "integrated")
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:GH-1"}) {
		t.Fatalf("receiver refs = %v", refs)
	}
	if got := labOutboxRows(t, f.b.db, f.b.repo); got != outboxBefore {
		t.Fatalf("imported reference.added queued %d outbox rows", got-outboxBefore)
	}

	// Rebind replaces the membership atomically: From tombstones, To is
	// born under the same event.
	if _, err := writesurface.Rebind(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.Locator, "fixture:GH-1", "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	rebound := referenceSyncGroup(t, f.sa, "A", f.matter.ID, store.TypeReferenceRebound)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.matter.ID, store.TypeReferenceRebound))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, rebound.Actions[0].Event.ID, "integrated")
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:BB-7"}) {
		t.Fatalf("receiver refs after rebind = %v", refs)
	}

	// Unbind tombstones the membership; a later re-add revives the same
	// row rather than duplicating it.
	if _, err := writesurface.Unbind(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.Locator, "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	removed := referenceSyncGroup(t, f.sa, "A", f.matter.ID, store.TypeReferenceRemoved)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.matter.ID, store.TypeReferenceRemoved))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, removed.Actions[0].Event.ID, "integrated")
	if refs := matterRefs(t, f.sb, f.matter.ID); len(refs) != 0 {
		t.Fatalf("receiver refs after unbind = %v", refs)
	}
	if _, err := writesurface.Bind(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.Locator, "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	reader := referenceSyncGroup(t, f.sa, "A", f.matter.ID, store.TypeReferenceAdded)
	exchangePass(t, f.sa, f.ca, f.ba, sourceGroup(t, f.sa, f.matter.ID, store.TypeReferenceAdded))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, reader.Actions[0].Event.ID, "integrated")
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:BB-7"}) {
		t.Fatalf("receiver refs after re-add = %v", refs)
	}
	if got := labOutboxRows(t, f.b.db, f.b.repo); got != outboxBefore {
		t.Fatalf("imported reference events queued %d outbox rows", got-outboxBefore)
	}
	if err := f.sb.Rebuild(labCtx); err != nil {
		t.Fatal(err)
	}
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:BB-7"}) {
		t.Fatalf("post-rebuild receiver refs = %v", refs)
	}
}

// Reference and gate facts wait on evidence that can still land — a bind
// ahead of its Matter's birth, a close ahead of its declaration, a
// dismissal ahead of Done — then integrate on the pass that lands it.
// Conflicts stay inspectable: a second satisfaction of the same (node,
// gate) and a locally-bound ref on another Matter are never merged.
func TestSharedStoreRelationsPendingAndConflicts(t *testing.T) {
	f := newRelationFixture(t)

	// A bind whose Matter has not arrived waits by name, then integrates
	// once the birth group lands.
	late, err := writesurface.CreateMatter(labCtx, f.sa, store.ActorHuman, f.a.repo, "Late", "late-rel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Bind(labCtx, f.sa, store.ActorHuman, f.a.repo, late.Locator, "fixture:GH-9"); err != nil {
		t.Fatal(err)
	}
	bindGroup := sourceGroup(t, f.sa, late.ID, store.TypeReferenceAdded)
	birthGroup := sourceGroup(t, f.sa, late.ID, store.TypeMatterCreated)
	exchangePass(t, f.sa, f.ca, f.ba, bindGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	d := sourceDecision(t, f.sb, bindGroup, "pending")
	if !strings.Contains(d.Reason, "awaiting") {
		t.Fatalf("bind-before-birth reason = %q", d.Reason)
	}
	exchangePass(t, f.sa, f.ca, f.ba, birthGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, bindGroup, "integrated")
	if refs := matterRefs(t, f.sb, late.ID); !slices.Equal(refs, []string{"fixture:GH-9"}) {
		t.Fatalf("late matter refs = %v", refs)
	}

	// A close whose declaration has not been sent yet waits by name; once
	// the declaration group lands the close integrates on the next pass.
	if err := writesurface.DeclareGate(labCtx, f.sa, f.a.repo, "late-gate", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	lateDecl := sourceGroup(t, f.sa, f.a.repo, store.TypeGateDeclared)
	if _, err := writesurface.CloseGate(labCtx, f.sa, store.ActorHuman, f.a.repo, "late-gate", f.step1.ID); err != nil {
		t.Fatal(err)
	}
	lateClose := sourceGroup(t, f.sa, f.step1.ID, store.TypeGateClosed)
	exchangePass(t, f.sa, f.ca, f.ba, lateClose)
	exchangePass(t, f.sb, f.cb, f.bb)
	d = sourceDecision(t, f.sb, lateClose, "pending")
	if !strings.Contains(d.Reason, "awaiting") {
		t.Fatalf("close-before-declare reason = %q", d.Reason)
	}
	exchangePass(t, f.sa, f.ca, f.ba, lateDecl)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, lateDecl, "integrated")
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, lateClose, "integrated")
	satisfied, err := f.sb.GateSatisfied(labCtx, f.b.repo, f.step1.ID, "late-gate")
	if err != nil || !satisfied {
		t.Fatalf("late gate satisfaction = %v %v", satisfied, err)
	}

	// A second satisfaction of the same (node, gate) is a retained
	// conflict, never a merge: the receiver closes late-gate on step-02
	// locally while the sender closes it there too — each side's claim is
	// valid where written and together they diverge on one (node, gate)
	// slot.
	if _, err := writesurface.CloseGate(labCtx, f.sb, store.ActorHuman, f.b.repo, "late-gate", f.step2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.CloseGate(labCtx, f.sa, store.ActorHuman, f.a.repo, "late-gate", f.step2.ID); err != nil {
		t.Fatal(err)
	}
	raceClose := sourceGroup(t, f.sa, f.step2.ID, store.TypeGateClosed)
	exchangePass(t, f.sa, f.ca, f.ba, raceClose)
	exchangePass(t, f.sb, f.cb, f.bb)
	d = sourceDecision(t, f.sb, raceClose, "conflict")
	if !strings.Contains(d.Reason, "already satisfied") {
		t.Fatalf("double-satisfaction reason = %q", d.Reason)
	}

	// A ref the receiver bound locally to a different Matter is divergent
	// occupancy: the sender's bind of the same ref to the synced Matter
	// conflicts rather than silently widening the aggregate. The local bind
	// is committed first so it is still local-only when the synced claim
	// evaluates; it uploads on the same pass and conflicts symmetrically,
	// which is the honest outcome for genuinely divergent bindings.
	if _, err := writesurface.Bind(labCtx, f.sb, store.ActorHuman, f.b.repo, f.matter.Locator, "fixture:OCC"); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Bind(labCtx, f.sa, store.ActorHuman, f.a.repo, late.Locator, "fixture:OCC"); err != nil {
		t.Fatal(err)
	}
	occGroup := sourceGroup(t, f.sa, late.ID, store.TypeReferenceAdded)
	exchangePass(t, f.sa, f.ca, f.ba, occGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	d = sourceDecision(t, f.sb, occGroup, "conflict")
	if !strings.Contains(d.Reason, "occupied") {
		t.Fatalf("occupied-reference reason = %q", d.Reason)
	}
}

// ---------------------------------------------------------------------------
// Stage-3 proofs: the composed relations family, its field-level comparison,
// and the three remaining scenarios — real-Laravel transport, a
// tracker-configured receiver, and missing targets with unrelated work
// proceeding.
// ---------------------------------------------------------------------------

// labRelationsWork is the composed relations family the Stage-3 proofs
// share, built only from writesurface verbs and the store's own gate
// repair: a Matter with a Stage and three Steps carrying live edges in both
// hierarchy directions, one added-then-removed edge, the full gate arc
// (declare, close, exemption repair, dismiss on a Done node) and the
// reference add/rebind/unbind/re-add arc, plus a delegated backlog entry
// whose deferred provenance originates at the Stage — the linked backlog
// outcome riding the same family as the reference work.
type labRelationsWork struct {
	matter, stage, step1, step2, step3 store.Node
	entry                              store.BacklogEntry
	sibling, descendant, ancestor      store.Edge
	removed                            store.Edge
}

// labComposeRelations drives every portable relation family on s in an
// order that never closes a loop: the sibling edge, the descendant-waits
// edge, the add/remove pair, then the ancestor-waits edge — kept live, the
// removed edge would have made step3 -> step1 -> step2 -> matter -> step3.
func labComposeRelations(t *testing.T, s *store.Store, repo, title, locator string) labRelationsWork {
	t.Helper()
	var w labRelationsWork
	var err error
	if w.matter, err = writesurface.CreateMatter(labCtx, s, store.ActorHuman, repo, title, locator); err != nil {
		t.Fatal(err)
	}
	if w.stage, err = writesurface.CreateStage(labCtx, s, store.ActorHuman, repo, locator, "Phase"); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"One", "Two", "Three"} {
		n, err := writesurface.CreateStep(labCtx, s, store.ActorHuman, repo,
			locator+"/"+w.stage.Locator, name)
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			w.step1 = n
		case 1:
			w.step2 = n
		case 2:
			w.step3 = n
		}
	}
	// Live edges: a sibling wait, a descendant waiting on its Matter, and —
	// added last — the Matter waiting on its own Step: the reverse of the
	// containment direction, legal because any node may block any node.
	if w.sibling, err = writesurface.DependAdd(labCtx, s, store.ActorHuman, repo, w.step1.ID, w.step2.ID); err != nil {
		t.Fatal(err)
	}
	if w.descendant, err = writesurface.DependAdd(labCtx, s, store.ActorHuman, repo, w.step2.ID, w.matter.ID); err != nil {
		t.Fatal(err)
	}
	if w.removed, err = writesurface.DependAdd(labCtx, s, store.ActorHuman, repo, w.step3.ID, w.step1.ID); err != nil {
		t.Fatal(err)
	}
	if err = writesurface.DependRemove(labCtx, s, store.ActorHuman, repo, w.step3.ID, w.step1.ID); err != nil {
		t.Fatal(err)
	}
	if w.ancestor, err = writesurface.DependAdd(labCtx, s, store.ActorHuman, repo, w.matter.ID, w.step3.ID); err != nil {
		t.Fatal(err)
	}

	// The gate arc. step2 runs to Done so the writesurface exemption repair
	// — which requires a Done node at the bound scale — has a real subject;
	// step3 runs to Done so the dismissal has one.
	if err = writesurface.DeclareGate(labCtx, s, repo, "wire-gate", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.CloseGate(labCtx, s, store.ActorHuman, repo, "wire-gate", w.step1.ID); err != nil {
		t.Fatal(err)
	}
	step2Path := locator + "/" + w.stage.Locator + "/" + w.step2.Locator
	if _, err = writesurface.Start(labCtx, s, store.ActorHuman, repo, step2Path); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Finish(labCtx, s, store.ActorHuman, repo, step2Path); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.RepairGateExemption(labCtx, s, repo, "wire-gate", step2Path); err != nil {
		t.Fatal(err)
	}
	step3Path := locator + "/" + w.stage.Locator + "/" + w.step3.Locator
	if _, err = writesurface.Start(labCtx, s, store.ActorHuman, repo, step3Path); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Finish(labCtx, s, store.ActorHuman, repo, step3Path); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.DismissGate(labCtx, s, store.ActorHuman, repo, "wire-gate", step3Path, "superseded by review"); err != nil {
		t.Fatal(err)
	}

	// The reference arc ends where the delegated backlog entry's detail
	// points: the rebind moved the tracker item, and the deferred entry —
	// originated at the Stage — records the follow-up as delegated evidence.
	if _, err = writesurface.Bind(labCtx, s, store.ActorHuman, repo, locator, "fixture:GH-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Rebind(labCtx, s, store.ActorHuman, repo, locator, "fixture:GH-1", "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Unbind(labCtx, s, store.ActorHuman, repo, locator, "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Bind(labCtx, s, store.ActorHuman, repo, locator, "fixture:BB-7"); err != nil {
		t.Fatal(err)
	}
	var entry store.BacklogEntry
	if entry, err = writesurface.BacklogAdd(labCtx, s, store.ActorHuman, repo,
		store.ProvenanceDeferred, "Rebind follow-up", "tracker item moved with the rebind",
		locator+"/"+w.stage.Locator); err != nil {
		t.Fatal(err)
	}
	if w.entry, err = writesurface.BacklogDelegate(labCtx, s, store.ActorHuman, repo, entry.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

// labAssertRelations compares got's observable relation state against the
// composed family field for field: live edges by source identity (plus
// exactly the named receiver-local extras), the removed edge's absence, the
// gate arc's declaration/satisfaction/exemption/dismissal, the reference
// membership and the delegated backlog row — and then the per-subject event
// trails, proving the receiver's retained effects mirror the source's
// portable history event for event. wantRepo is masked exactly as
// labAssertFamily masks Repo: each store keeps its own native identity.
func labAssertRelations(t *testing.T, want, got *store.Store, gotRepo string, w labRelationsWork, local ...store.Edge) {
	t.Helper()

	// Edges: the three live synced edges verbatim plus exactly the named
	// local extras; the removed pair is out of force on both stores.
	type edgeKey struct{ blocked, blocker string }
	wantEdges := map[edgeKey]string{}
	for _, e := range []store.Edge{w.sibling, w.descendant, w.ancestor} {
		wantEdges[edgeKey{e.Blocked, e.Blocker}] = e.ID
	}
	for _, e := range local {
		wantEdges[edgeKey{e.Blocked, e.Blocker}] = e.ID
	}
	gotEdges, err := got.LiveEdges(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotEdges) != len(wantEdges) {
		t.Fatalf("receiver live edges = %+v, want %d", gotEdges, len(wantEdges))
	}
	for _, e := range gotEdges {
		id, ok := wantEdges[edgeKey{e.Blocked, e.Blocker}]
		if !ok || id != e.ID {
			t.Fatalf("receiver live edge %+v is not a synced or named-local edge", e)
		}
	}
	sourceEdges, err := want.LiveEdges(labCtx)
	if err != nil || len(sourceEdges) != 3 {
		t.Fatalf("source live edges = %+v %v, want the three composed edges", sourceEdges, err)
	}
	for _, s := range []*store.Store{want, got} {
		if _, found := relationEdge(t, s, w.step3.ID, w.step1.ID); found {
			t.Fatalf("removed edge still in force on %s", s.Path())
		}
	}

	// Gate arc: the declaration landed at the paired Repo's scale, the
	// close and the dismissal satisfy gate_state with their own evidence,
	// and the repair landed exempt.
	decls, err := got.GateDeclarations(labCtx, gotRepo)
	if err != nil {
		t.Fatal(err)
	}
	var declared bool
	for _, d := range decls {
		if d.Repo == gotRepo && d.Gate == "wire-gate" && d.Scale == store.ScaleStep {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("receiver declarations = %+v, want wire-gate@step in %s", decls, gotRepo)
	}
	for _, check := range []struct {
		node   string
		state  store.GateRequirementState
		reason string
	}{
		{w.step1.ID, store.GateRequirementClosed, ""},
		{w.step3.ID, store.GateRequirementDismissed, "superseded by review"},
	} {
		closed, err := got.ClosedGates(labCtx, check.node)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, g := range closed {
			if g.Gate != "wire-gate" {
				continue
			}
			found = true
			if g.State != check.state || g.Scale != store.ScaleStep || g.DismissalReason != check.reason {
				t.Fatalf("gate state on %s = %+v, want %s", check.node, g, check.state)
			}
		}
		if !found {
			t.Fatalf("no wire-gate state on %s: %+v", check.node, closed)
		}
		satisfied, err := got.GateSatisfied(labCtx, gotRepo, check.node, "wire-gate")
		if err != nil || !satisfied {
			t.Fatalf("GateSatisfied(%s, wire-gate) = %v %v", check.node, satisfied, err)
		}
	}
	exempt, err := got.GateExempt(labCtx, gotRepo, w.step2.ID, "wire-gate")
	if err != nil || !exempt {
		t.Fatalf("GateExempt(step2, wire-gate) = %v %v", exempt, err)
	}

	// References: the add/rebind/unbind/re-add arc lands as the final
	// membership, and the delegated backlog entry keeps the source's outbox
	// identity as audit evidence with Repo masked.
	if refs := matterRefs(t, got, w.matter.ID); !slices.Equal(refs, []string{"fixture:BB-7"}) {
		t.Fatalf("receiver refs = %v", refs)
	}
	gotEntry := labBacklog(t, got, gotRepo, w.entry.ID)
	wantEntry := w.entry
	wantEntry.Repo, gotEntry.Repo = "", ""
	if !reflect.DeepEqual(wantEntry, gotEntry) {
		t.Fatalf("receiver backlog entry = %+v, want %+v", gotEntry, wantEntry)
	}

	// Source history: each synced subject's ordered portable event types on
	// the sender must equal the receiver's retained integration effects —
	// the repo-keyed gate.declared is the one subject remap and is asserted
	// through GateDeclarations instead.
	subjects := []string{w.matter.ID, w.stage.ID, w.step1.ID, w.step2.ID, w.step3.ID, w.entry.ID}
	source := labPortableTrail(t, want, store.ActorHuman, subjects...)
	effects := labPortableTrail(t, got, store.SystemActor("shared-store-experiment"), subjects...)
	for _, id := range subjects {
		if !reflect.DeepEqual(source[id], effects[id]) {
			t.Fatalf("trail on %s: source %v, receiver effects %v", id, source[id], effects[id])
		}
	}
}

// labPortableTrail returns each named subject's ordered event types recorded
// by one actor — the source's originals or the receiver's retained
// integration effects — so a test can prove effect history mirrors source
// history event for event.
func labPortableTrail(t *testing.T, s *store.Store, actor store.Actor, subjects ...string) map[string][]string {
	t.Helper()
	want := make(map[string]bool, len(subjects))
	for _, id := range subjects {
		want[id] = true
	}
	out := map[string][]string{}
	for _, ev := range labEvents(t, s) {
		if ev.Actor != actor || !want[ev.Subject] {
			continue
		}
		out[ev.Subject] = append(out[ev.Subject], ev.Type)
	}
	return out
}

// Step-3 flagship over real Laravel/PostgreSQL transport: the whole
// relations family — dependencies and their removal, the gate
// declaration/exemption/close/dismiss arc, reference rebinds and a linked
// delegated backlog outcome — crosses into a receiver that already holds
// unrelated local work, and the receiver's relationship, gate, reference,
// backlog and event-trail state matches the source after retry, reopen and
// rebuild.
func TestLaravelExchangeRelationsWire(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}

	// The receiver's own pre-existing work: a Matter with its own edge, its
	// own declared-and-closed gate and its own tracker reference — local
	// facts that must stay untouched and distinguishable from synced ones.
	local, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Local", "local-rel")
	if err != nil {
		t.Fatal(err)
	}
	localStage, err := writesurface.CreateStage(labCtx, sb, store.ActorHuman, b.repo, "local-rel", "Phase")
	if err != nil {
		t.Fatal(err)
	}
	localStep, err := writesurface.CreateStep(labCtx, sb, store.ActorHuman, b.repo,
		"local-rel/"+localStage.Locator, "Solo")
	if err != nil {
		t.Fatal(err)
	}
	localEdge, err := writesurface.DependAdd(labCtx, sb, store.ActorHuman, b.repo, localStep.ID, localStage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = writesurface.DeclareGate(labCtx, sb, b.repo, "local-gate", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.CloseGate(labCtx, sb, store.ActorHuman, b.repo, "local-gate", localStep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Bind(labCtx, sb, store.ActorHuman, b.repo, "local-rel", "fixture:LOCAL-1"); err != nil {
		t.Fatal(err)
	}

	// The sender's full relations family, then real pairing and transport.
	work := labComposeRelations(t, sa, a.repo, "Relwire", "relwire")
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
	if st.Waiting != 0 || st.Conflicts != 0 {
		t.Fatalf("receiver status = %+v", st)
	}
	if st.Evaluated != st.Downloaded || int64(st.Integrated+st.Conflicts) != st.Evaluated {
		t.Fatalf("receiver did not evaluate every download exactly once: %+v", st)
	}
	// Every appended event is an accounted integration effect.
	decisions, err := sb.ExperimentActivities(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) == 0 {
		t.Fatal("receiver recorded no decisions")
	}
	effects := 0
	for _, d := range decisions {
		if d.Disposition != "integrated" {
			t.Fatalf("decision = %s (%s), want integrated", d.Disposition, d.Reason)
		}
		effects += len(d.Effects)
	}
	if appended := len(labEvents(t, sb)) - beforeSync; appended != effects {
		t.Fatalf("receiver appended %d events for %d integrated effects", appended, effects)
	}
	labAssertRelations(t, sa, sb, b.repo, work, localEdge)

	// The receiver's own work is untouched: same lifecycle, same gate
	// satisfaction, same reference, same local edge.
	if ln := mustNode(t, sb, localStep.ID); ln.Lifecycle != store.Planned {
		t.Fatalf("local step lifecycle = %s", ln.Lifecycle)
	}
	if satisfied, err := sb.GateSatisfied(labCtx, b.repo, localStep.ID, "local-gate"); err != nil || !satisfied {
		t.Fatalf("local gate satisfaction = %v %v", satisfied, err)
	}
	if refs := matterRefs(t, sb, local.ID); !slices.Equal(refs, []string{"fixture:LOCAL-1"}) {
		t.Fatalf("local refs = %v", refs)
	}
	if value, found, err := sb.Config(labCtx, b.repo, "tracker.backend"); err != nil || found || value != "" {
		t.Fatalf("sender-less receiver gained tracker config: %q %v %v", value, found, err)
	}

	// Retry and reopen stay inert; the sender's echo and an explicit
	// re-upload of the declaration group append nothing; rebuild preserves
	// every relationship and the whole effect trail.
	declaredGroup := sourceGroup(t, sa, a.repo, store.TypeGateDeclared)
	before := len(labEvents(t, sb))
	exchangePass(t, sb, cb, statusB.Binding)
	exchangePass(t, sa, ca, statusA.Binding, declaredGroup)
	exchangePass(t, sb, cb, statusB.Binding)
	if len(labEvents(t, sb)) != before {
		t.Fatal("retry, echo or redelivery appended effects")
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
	labAssertRelations(t, sa, sb, b.repo, work, localEdge)
	if satisfied, err := sb.GateSatisfied(labCtx, b.repo, localStep.ID, "local-gate"); err != nil || !satisfied {
		t.Fatalf("post-rebuild local gate satisfaction = %v %v", satisfied, err)
	}
	t.Logf("relations wire over Laravel: %d downloads, %d integrated effects, rebuild-clean",
		st.Downloaded, effects)
}

// With full tracker configuration on the receiver — backend, target,
// project and narrated push, all Repo-tier config.set that never crosses —
// the received relation/gate/reference facts mint no outbox rows, no open
// execution projections and no provider HTTP, before and after rebuild,
// while the receiver's own native gate and reference operations keep
// queueing exactly what local policy dictates.
func TestSharedStoreRelationsSyncMintsNoDelivery(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	feedURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}

	// Both stores are delivery-bearing before any relation work: the
	// sender's payloads carry the narrated push level, so the only thing
	// keeping the receiver clean is the integration-effect boundary itself.
	for repo, s := range map[string]*store.Store{a.repo: sa, b.repo: sb} {
		if err := s.SetConfig(labCtx, repo, store.TrackerBackendKey, "fixture"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetConfig(labCtx, repo, store.TrackerTargetKey, "fixture-target"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetConfig(labCtx, repo, store.TrackerProjectKey, "fixture-project"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetTrackerPushLevel(labCtx, repo, string(store.TrackerPushNarrated)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sb.SetTrackerBacklogPush(labCtx, b.repo, string(store.TrackerBacklogPushAuto)); err != nil {
		t.Fatal(err)
	}

	// The receiver's own pre-existing work, authored under the live
	// configuration: a local edge, a declared-and-closed gate, a bound
	// reference, a started Matter and an auto-delegated backlog entry — the
	// native baseline every later count is measured against.
	local, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "Local", "local-rel")
	if err != nil {
		t.Fatal(err)
	}
	localStage, err := writesurface.CreateStage(labCtx, sb, store.ActorHuman, b.repo, "local-rel", "Phase")
	if err != nil {
		t.Fatal(err)
	}
	localStep, err := writesurface.CreateStep(labCtx, sb, store.ActorHuman, b.repo,
		"local-rel/"+localStage.Locator, "Solo")
	if err != nil {
		t.Fatal(err)
	}
	localEdge, err := writesurface.DependAdd(labCtx, sb, store.ActorHuman, b.repo, localStep.ID, localStage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = writesurface.DeclareGate(labCtx, sb, b.repo, "local-gate", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.CloseGate(labCtx, sb, store.ActorHuman, b.repo, "local-gate", localStep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Bind(labCtx, sb, store.ActorHuman, b.repo, "local-rel", "fixture:LOCAL-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Start(labCtx, sb, store.ActorHuman, b.repo, "local-rel/"+localStep.Locator); err != nil {
		t.Fatal(err)
	}
	nativeEntry, err := writesurface.BacklogAdd(labCtx, sb, store.ActorHuman, b.repo,
		store.ProvenanceIntake, "Native item", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if nativeEntry.State != "delegated" {
		t.Fatalf("auto-push entry = %+v, want delegated", nativeEntry)
	}
	outboxBefore := labOutboxRows(t, sb.Path(), b.repo)
	if outboxBefore == 0 {
		t.Fatal("native control queued nothing; the configuration was not delivery-bearing")
	}

	// The sender's relations family rides narrated payloads to the
	// configured receiver.
	work := labComposeRelations(t, sa, a.repo, "Relwire", "relwire")
	if _, err = sa.PairExchange(labCtx, a.repo, endpoint); err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, endpoint)
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
	labAssertRelations(t, sa, sb, b.repo, work, localEdge)

	// Zero delivery minted by the import: the outbox count is exactly the
	// native baseline, no execution projection opened, and every HTTP
	// request the receiver made went to the exchange feed — never a
	// provider.
	if got := labOutboxRows(t, sb.Path(), b.repo); got != outboxBefore {
		t.Fatalf("imported relations minted %d outbox rows (baseline %d)", got-outboxBefore, outboxBefore)
	}
	for _, entry := range mustOutbox(t, sb, b.repo) {
		if entry.Subject == work.matter.ID || entry.Subject == work.entry.ID ||
			entry.Ref == "fixture:GH-1" || entry.Ref == "fixture:BB-7" {
			t.Fatalf("imported relation queued a delivery candidate: %+v", entry)
		}
	}
	if open := labOpenExecution(t, sb.Path()); open != 0 {
		t.Fatalf("imported relations opened %d execution rows", open)
	}
	if len(rec.hosts) == 0 {
		t.Fatal("receiver made no exchange requests")
	}
	for host, n := range rec.hosts {
		if host != feedURL.Host {
			t.Fatalf("receiver called unexpected host %s (%d requests)", host, n)
		}
	}

	// Rebuild preserves both halves: the relations landed and the delivery
	// surface is still exactly the native baseline.
	labRebuild(t, sb)
	labAssertRelations(t, sa, sb, b.repo, work, localEdge)
	if got := labOutboxRows(t, sb.Path(), b.repo); got != outboxBefore {
		t.Fatalf("rebuild changed outbox rows to %d (baseline %d)", got, outboxBefore)
	}
	if open := labOpenExecution(t, sb.Path()); open != 0 {
		t.Fatalf("rebuild opened %d execution rows", open)
	}

	// Native control: the same verbs authored locally still behave exactly
	// as configured — the rebind queues its state candidate for the new
	// reference only, the gate close records gate_state, and nothing names
	// an imported subject or reference.
	if _, err = writesurface.Rebind(labCtx, sb, store.ActorHuman, b.repo, "local-rel", "fixture:LOCAL-1", "fixture:LOCAL-9"); err != nil {
		t.Fatal(err)
	}
	if err = writesurface.DeclareGate(labCtx, sb, b.repo, "local-gate-2", store.ScaleStep); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.CloseGate(labCtx, sb, store.ActorHuman, b.repo, "local-gate-2", localStep.ID); err != nil {
		t.Fatal(err)
	}
	outbox, err := sb.Outbox(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) <= outboxBefore {
		t.Fatalf("native rebind queued nothing: %+v", outbox)
	}
	var sawNative bool
	for _, entry := range outbox {
		if entry.Subject == work.matter.ID || entry.Subject == work.entry.ID ||
			entry.Ref == "fixture:GH-1" || entry.Ref == "fixture:BB-7" {
			t.Fatalf("native op queued a candidate for imported work: %+v", entry)
		}
		if entry.Kind == "state" && entry.Ref == "fixture:LOCAL-9" && entry.Subject == local.ID {
			sawNative = true
		}
	}
	if !sawNative {
		t.Fatalf("native rebind queued no state candidate for fixture:LOCAL-9: %+v", outbox)
	}
	if satisfied, err := sb.GateSatisfied(labCtx, b.repo, localStep.ID, "local-gate-2"); err != nil || !satisfied {
		t.Fatalf("native gate close = %v %v", satisfied, err)
	}
	if refs := matterRefs(t, sb, local.ID); !slices.Equal(refs, []string{"fixture:LOCAL-2", "fixture:LOCAL-9"}) && !slices.Equal(refs, []string{"fixture:LOCAL-9"}) {
		t.Fatalf("local refs after rebind = %v", refs)
	}
}

// mustOutbox reads a Repo's outbox entries, failing the test on error.
func mustOutbox(t *testing.T, s *store.Store, repo string) []store.OutboxEntry {
	t.Helper()
	entries, err := s.Outbox(labCtx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// A dependency whose blocker lives outside what sync carries — the target
// exists on the sender but its birth is never uploaded — stays a named
// pending wait across passes while unrelated valid units, including
// reverse-direction edges, integrate in the same pass and afterwards
// without any acknowledgement. Accepted work is retained and the missing
// target never materializes.
func TestSharedStoreDependencyMissingTargetProceeds(t *testing.T) {
	f := newRelationFixture(t)

	// The external target: a real Matter on the sender whose birth group is
	// deliberately never uploaded, so the dependent edge waits on evidence
	// that cannot arrive through this exchange. It hangs off the Stage — a
	// subject with no other synced facts — so nothing else's baseline can
	// chain onto the wait.
	late, err := writesurface.CreateMatter(labCtx, f.sa, store.ActorHuman, f.a.repo, "Late Ext", "late-ext")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.stage.ID, late.ID); err != nil {
		t.Fatal(err)
	}
	missingGroup := sourceGroup(t, f.sa, f.stage.ID, store.TypeDependencyAdded)

	// Reverse-direction edges plus a sibling wait and a valid reference —
	// every one legal where written, each on its own subject so no baseline
	// chains onto the pending wait, forming the chain
	// step1 -> step2 -> matter -> stage on the receiver.
	siblingEdge, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID)
	if err != nil {
		t.Fatal(err)
	}
	siblingGroup := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyAdded)
	descendantEdge, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step2.ID, f.matter.ID)
	if err != nil {
		t.Fatal(err)
	}
	descendantGroup := sourceGroup(t, f.sa, f.step2.ID, store.TypeDependencyAdded)
	ancestorEdge, err := writesurface.DependAdd(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.ID, f.stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	ancestorGroup := sourceGroup(t, f.sa, f.matter.ID, store.TypeDependencyAdded)
	if _, err = writesurface.Bind(labCtx, f.sa, store.ActorHuman, f.a.repo, f.matter.Locator, "fixture:OK-1"); err != nil {
		t.Fatal(err)
	}
	bindGroup := sourceGroup(t, f.sa, f.matter.ID, store.TypeReferenceAdded)

	// One pass carries the missing-target edge first, then every valid
	// unit: pending evidence must not hold back unrelated work.
	exchangePass(t, f.sa, f.ca, f.ba, missingGroup, siblingGroup, descendantGroup, ancestorGroup, bindGroup)
	exchangePass(t, f.sb, f.cb, f.bb)

	d := sourceDecision(t, f.sb, missingGroup, "pending")
	if !strings.Contains(d.Reason, "awaiting") || !strings.Contains(d.Reason, "blocker") {
		t.Fatalf("missing-target reason = %q, want the unresolved endpoint named", d.Reason)
	}
	for _, group := range []string{siblingGroup, descendantGroup, ancestorGroup, bindGroup} {
		sourceDecision(t, f.sb, group, "integrated")
	}
	for _, e := range []store.Edge{siblingEdge, descendantEdge, ancestorEdge} {
		got, found := relationEdge(t, f.sb, e.Blocked, e.Blocker)
		if !found || got.ID != e.ID {
			t.Fatalf("receiver edge %s<-%s = %+v found=%v, want %s", e.Blocked, e.Blocker, got, found, e.ID)
		}
	}
	if _, found := relationEdge(t, f.sb, f.stage.ID, late.ID); found {
		t.Fatal("pending edge materialized before its target")
	}
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:OK-1"}) {
		t.Fatalf("receiver refs = %v", refs)
	}
	if _, err := f.sb.Node(labCtx, late.ID); err == nil {
		t.Fatal("unsent target materialized on the receiver")
	}
	st, err := f.sb.ExchangeStatus(labCtx, f.b.repo)
	if err != nil || st.Waiting != 1 || st.Conflicts != 0 {
		t.Fatalf("status = %+v %v, want one named wait and no conflicts", st, err)
	}

	// A second pass re-evaluates the wait — it is retained evidence, never
	// acknowledged — and appends nothing.
	before := len(labEvents(t, f.sb))
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, missingGroup, "pending")
	history, err := f.sb.ExperimentDecisionHistory(labCtx, missingGroup)
	if err != nil || len(history) < 2 {
		t.Fatalf("missing-target history = %+v %v, want repeated pending evaluations", history, err)
	}
	for _, h := range history {
		if h.Decision.Disposition != "pending" {
			t.Fatalf("missing-target evaluation = %s, want pending throughout", h.Decision.Disposition)
		}
	}
	if len(labEvents(t, f.sb)) != before {
		t.Fatal("pending re-evaluation appended effects")
	}

	// Accepted work continues while the target stays missing: the sender
	// removes the sibling edge and the removal integrates; the remaining
	// edges, the reference and the pending wait are all retained.
	if err = writesurface.DependRemove(labCtx, f.sa, store.ActorHuman, f.a.repo, f.step1.ID, f.step2.ID); err != nil {
		t.Fatal(err)
	}
	removedGroup := sourceGroup(t, f.sa, f.step1.ID, store.TypeDependencyRemoved)
	exchangePass(t, f.sa, f.ca, f.ba, removedGroup)
	exchangePass(t, f.sb, f.cb, f.bb)
	sourceDecision(t, f.sb, removedGroup, "integrated")
	sourceDecision(t, f.sb, missingGroup, "pending")
	if _, found := relationEdge(t, f.sb, f.step1.ID, f.step2.ID); found {
		t.Fatal("removed edge outlived its synced removal")
	}
	for _, e := range []store.Edge{ancestorEdge, descendantEdge} {
		got, found := relationEdge(t, f.sb, e.Blocked, e.Blocker)
		if !found || got.ID != e.ID {
			t.Fatalf("accepted edge lost: %+v found=%v", got, found)
		}
	}
	if refs := matterRefs(t, f.sb, f.matter.ID); !slices.Equal(refs, []string{"fixture:OK-1"}) {
		t.Fatalf("receiver refs after removal = %v", refs)
	}
	if edges, _ := f.sb.LiveEdges(labCtx); len(edges) != 2 {
		t.Fatalf("LiveEdges = %+v, want the two retained edges", edges)
	}
	if _, err := f.sb.Node(labCtx, late.ID); err == nil {
		t.Fatal("unsent target materialized after later passes")
	}

	// Rebuild preserves the retained edges, the reference and the still-
	// pending named wait.
	labRebuild(t, f.sb)
	for _, e := range []store.Edge{ancestorEdge, descendantEdge} {
		got, found := relationEdge(t, f.sb, e.Blocked, e.Blocker)
		if !found || got.ID != e.ID {
			t.Fatalf("post-rebuild edge lost: %+v found=%v", got, found)
		}
	}
	sourceDecision(t, f.sb, missingGroup, "pending")
}
