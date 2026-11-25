//go:build sharedstoreexperiment

package store_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

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
