//go:build sharedstoreexperiment

package store_test

// Stage-1 sync-acceptance coverage (project-sync-matters/05-sync-acceptance.md):
// one combined real-transport fixture that crosses the whole portable family
// at once, a third-store echo proving original event IDs survive relay, and a
// synthetic-provenance receipt proving imported history re-exports as new
// originals with canonical baselines. Expectations are derived from the
// writesurface verbs and the stores' own projections — never from the sender's
// export helpers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labLaravelFeedAll reads the whole committed feed, following the server's
// `after` cursor across its 100-change pages — a combined fixture exceeds one
// page, so a single unpaginated GET would silently miss later originals.
func labLaravelFeedAll(t *testing.T, f *laravelFixture) []store.ExchangeChange {
	t.Helper()
	var all []store.ExchangeChange
	after := int64(0)
	for {
		var page struct {
			Changes []store.ExchangeChange `json:"changes"`
			Cursor  int64                  `json:"cursor"`
		}
		raw := fixtureHTTP(t, "GET", fmt.Sprintf("%s/changes?after=%d", f.endpoint(), after), f.Owner, nil, 200)
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Changes...)
		if len(page.Changes) == 0 || page.Cursor <= after {
			break
		}
		after = page.Cursor
	}
	return all
}

// labDecisionAny returns the retained decision for group regardless of
// disposition — sourceDecision's shape for groups whose outcome the test
// asserts separately (conflict, pending) rather than pins here.
func labDecisionAny(t *testing.T, s *store.Store, group string) store.ExperimentDecision {
	t.Helper()
	all, err := s.ExperimentActivities(labCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if len(d.Source.Actions) > 0 && d.Source.Actions[0].Event.ID == group {
			return d
		}
	}
	t.Fatalf("missing decision %s on %s", group, s.Path())
	return store.ExperimentDecision{}
}

// labActionIDs returns the original event IDs a decision's source claims, in
// order — the wire identity every store must retain verbatim.
func labActionIDs(d store.ExperimentDecision) []string {
	ids := make([]string, len(d.Source.Actions))
	for i, a := range d.Source.Actions {
		ids[i] = a.Event.ID
	}
	return ids
}

// labAssertWireIdentity proves every named store retained byte-identical
// source evidence for group and that each store's effects stayed local:
// receiver effects are its own integration-actor events, never the source's
// original IDs. origin, when non-nil, is the store that natively committed
// the group — its self-echo maps effects back onto the original IDs.
func labAssertWireIdentity(t *testing.T, origin *store.Store, group string, receivers ...*store.Store) {
	t.Helper()
	var want store.ExperimentSource
	var wantIDs []string
	stores := append([]*store.Store{origin}, receivers...)
	for i, s := range stores {
		if s == nil {
			continue
		}
		d := labDecisionAny(t, s, group)
		if i == 0 || want.Actions == nil {
			want, wantIDs = d.Source, labActionIDs(d)
		} else {
			if !reflect.DeepEqual(want, d.Source) {
				t.Fatalf("store %s retained different source evidence for %s", s.Path(), group)
			}
			if !reflect.DeepEqual(wantIDs, labActionIDs(d)) {
				t.Fatalf("store %s saw different original IDs for %s: %v vs %v", s.Path(), group, labActionIDs(d), wantIDs)
			}
		}
		// Every effect must be a real local event; on receivers it must be
		// stamped by the integration actor and never reuse a source ID.
		events := map[string]store.Event{}
		for _, ev := range labEvents(t, s) {
			events[ev.ID] = ev
		}
		sourceIDs := map[string]bool{}
		for _, id := range wantIDs {
			sourceIDs[id] = true
		}
		for _, eff := range d.Effects {
			ev, ok := events[eff]
			if !ok {
				t.Fatalf("store %s effect %s is not a local event", s.Path(), eff)
			}
			if s == origin {
				if !sourceIDs[eff] {
					t.Fatalf("origin effect %s is not a source original", eff)
				}
				continue
			}
			if ev.Actor != store.SystemActor("shared-store-experiment") {
				t.Fatalf("receiver effect %s actor = %s", eff, ev.Actor)
			}
			if sourceIDs[eff] {
				t.Fatalf("receiver effect reused source identity %s", eff)
			}
		}
	}
}

// Stage-1 flagship: the combined release fixture over real Laravel/PostgreSQL
// transport. One sender carries the whole portable family at once — a composed
// Matter/Stage/Step history with insert/reorder/replace/remove, a lifecycle
// matrix, planned/declined/delegated/entered backlog, prose and findings on
// every owner — plus the relations family (live and removed dependency edges,
// the declare/exempt/close/dismiss gate arc, reference rebinds) and spilled
// content past the 1 MiB blob threshold on every kind. The receiver already
// holds unrelated work, a same-title Matter and the locator the sender's
// colliding birth claims; the sender also owns a second unpaired project and
// a paused binding whose pending originals must never reach the feed. Two
// receiver passes converge: spilled blobs land between them.
func TestLaravelExchangeCombinedReleaseFixture(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	if a.repo == b.repo {
		t.Fatal("independent native Repos unexpectedly identical")
	}

	// The receiver's own history predates pairing: unrelated live work with
	// prose and a lifecycle, a same-title Matter that must never merge, a
	// Matter already holding the locator the sender's colliding birth claims,
	// and one local dependency edge that must coexist with synced edges.
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
	localEdge, err := writesurface.DependAdd(labCtx, sb, store.ActorHuman, b.repo, localStep.ID, localStage.ID)
	if err != nil {
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

	// Two more native Repos share the sender's store: one never pairs, one
	// pairs and is then paused. Both keep pending originals that must stay
	// local — the paired repo's work is retained, never uploaded.
	known := map[string]bool{a.repo: true}
	siblingRepo := func() string {
		t.Helper()
		p := labPair{dir: t.TempDir(), db: a.db}
		if out, err := exec.Command("git", "init", "--quiet", p.dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		labCLICommand(t, p, true, "init", "--json")
		repos, err := sa.Repos(labCtx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range repos {
			if !known[r.ID] {
				known[r.ID] = true
				return r.ID
			}
		}
		t.Fatal("new Repo not visible in shared store")
		return ""
	}
	unpairedRepo := siblingRepo()
	pausedRepo := siblingRepo()
	if _, err := sa.PairExchange(labCtx, pausedRepo, f.endpoint()); err != nil {
		t.Fatal(err)
	}
	pausedMatter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, pausedRepo, "Paused work", "paused-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, pausedRepo, pausedMatter.Locator, store.KindBrief, []byte("paused bytes")); err != nil {
		t.Fatal(err)
	}
	if err := sa.UnpairExchange(labCtx, pausedRepo); err != nil {
		t.Fatal(err)
	}
	unpairedMatter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, unpairedRepo, "Never paired", "never-paired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.WriteOnce(labCtx, sa, store.ActorHuman, unpairedRepo, unpairedMatter.Locator, store.KindBrief, []byte("unpaired bytes")); err != nil {
		t.Fatal(err)
	}
	siblingGroups := []string{
		sourceGroup(t, sa, unpairedMatter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, unpairedMatter.ID, store.TypeContentCreated),
		sourceGroup(t, sa, pausedMatter.ID, store.TypeMatterCreated),
		sourceGroup(t, sa, pausedMatter.ID, store.TypeContentCreated),
	}
	beforeUnpaired, err := sa.ExchangeStatus(labCtx, unpairedRepo)
	if err != nil || beforeUnpaired.Paired || len(beforeUnpaired.PendingUpload) != 0 || beforeUnpaired.EarlierGroups == 0 {
		t.Fatal("unpaired preview claimed outgoing work", beforeUnpaired, err)
	}
	beforePaused, err := sa.ExchangeStatus(labCtx, pausedRepo)
	if err != nil || !beforePaused.Paired || beforePaused.Active || len(beforePaused.PendingUpload) != 2 {
		t.Fatal("paused pairing lost its pending originals", beforePaused, err)
	}
	for _, repo := range []string{unpairedRepo, pausedRepo} {
		if _, err := sa.RunExchange(labCtx, store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Owner}, repo); err == nil || !strings.Contains(err.Error(), "not actively paired") {
			t.Fatalf("non-active Repo %s exchanged: %v", repo, err)
		}
	}

	// The combined fixture: the composed family (old+future Matter/Stage/Step
	// history, lifecycle matrix, backlog outcomes, prose, findings) and the
	// relations family (dependencies, gate arc, references) on one Repo.
	work := labComposeFamily(t, sa, a.repo, "Archive", "archive")
	rel := labComposeRelations(t, sa, a.repo, "Relwire", "relwire")

	// Spilled content past the 1 MiB blob threshold on every kind: a dedicated
	// Matter/Stage/Step so each create-once kind and findings each carry one
	// over-threshold payload through the real blob endpoints.
	spill, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Spill", "spill")
	if err != nil {
		t.Fatal(err)
	}
	spillStage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, spill.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	spillStep, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		spill.Locator+"/"+spillStage.Locator, "Carrier")
	if err != nil {
		t.Fatal(err)
	}
	spillBrief := labUnicodeBytes(store.SpillThreshold + 101)
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, spill.Locator, store.KindBrief, spillBrief); err != nil {
		t.Fatal(err)
	}
	spillPlan := labFillBytes(store.SpillThreshold+202, 9)
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo,
		spill.Locator+"/"+spillStage.Locator, store.KindWorkplan, spillPlan); err != nil {
		t.Fatal(err)
	}
	spillBody := labFillBytes(2*store.SpillThreshold+303, 4)
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo,
		spill.Locator+"/"+spillStage.Locator+"/"+spillStep.Locator, store.KindBody, spillBody); err != nil {
		t.Fatal(err)
	}
	spillFinding := labUnicodeBytes(store.SpillThreshold + 404)
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo,
		spill.Locator+"/"+spillStage.Locator+"/"+spillStep.Locator, spillFinding); err != nil {
		t.Fatal(err)
	}
	// Four spilled content groups pend on the receiver's first pass until
	// their blobs land; every other portable group integrates immediately.
	spillPending := 4

	// One deliberate locator collision: the receiver's occupant already holds
	// "taken", so this birth conflicts and must never partially apply.
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
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}
	done, err := sa.RunExchange(labCtx, ca, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(done.PendingUpload) != 0 {
		t.Fatalf("sender left %d groups un-uploaded: %v", len(done.PendingUpload), done.PendingUpload)
	}

	// Pass 1: every non-spilled group evaluates; the four spilled content
	// groups pend on their blobs and the collision conflicts.
	beforeSync := len(labEvents(t, sb))
	exchangePass(t, sb, cb, statusB.Binding)
	st, err := sb.ExchangeStatus(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != spillPending || st.Conflicts != 1 {
		t.Fatalf("receiver pass 1 status = %+v, want %d waiting spilled + 1 conflict", st, spillPending)
	}
	if st.Evaluated != st.Downloaded || int64(st.Integrated+st.Waiting+st.Conflicts) != st.Evaluated {
		t.Fatalf("receiver did not evaluate every download exactly once: %+v", st)
	}

	// Pass 2: the pending spilled groups' blobs arrive and they integrate.
	exchangePass(t, sb, cb, statusB.Binding)
	st, err = sb.ExchangeStatus(labCtx, b.repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != 0 || st.Conflicts != 1 {
		t.Fatalf("receiver pass 2 status = %+v", st)
	}
	if st.Evaluated != st.Downloaded || int64(st.Integrated+st.Conflicts) != st.Evaluated {
		t.Fatalf("receiver did not converge every download: %+v", st)
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

	// Observable parity, field by field: the composed family and the
	// relations family must both reproduce on the receiver.
	labAssertFamily(t, sa, sb, b.repo, work)
	labAssertRelations(t, sa, sb, b.repo, rel, localEdge)

	// Spilled content: every kind's segment is flagged spilled on both
	// stores with identical length/digest, and the bytes round-trip exact.
	spillChecks := []struct {
		subject string
		kind    store.ContentKind
		data    []byte
	}{
		{spill.ID, store.KindBrief, spillBrief},
		{spillStage.ID, store.KindWorkplan, spillPlan},
		{spillStep.ID, store.KindBody, spillBody},
		{spillStep.ID, store.KindFindings, spillFinding},
	}
	for _, check := range spillChecks {
		wantSegs := labSegments(t, sa, check.subject, check.kind)
		gotSegs := labSegments(t, sb, check.subject, check.kind)
		if len(gotSegs) != 1 || !gotSegs[0].Spilled {
			t.Fatalf("receiver %s on %s = %+v, want one spilled segment", check.kind, check.subject, gotSegs)
		}
		if !reflect.DeepEqual(wantSegs, gotSegs) {
			t.Fatalf("receiver %s segments on %s = %+v, want %+v", check.kind, check.subject, gotSegs, wantSegs)
		}
		if gotSegs[0].ByteLen != int64(len(check.data)) || gotSegs[0].SHA256 != labDigest(check.data) {
			t.Fatalf("receiver %s digest/length = %d/%s, want %d/%s",
				check.kind, gotSegs[0].ByteLen, gotSegs[0].SHA256, len(check.data), labDigest(check.data))
		}
		got, err := sb.Content(labCtx, check.subject, check.kind)
		if err != nil || !bytes.Equal(got, check.data) {
			t.Fatalf("receiver %s bytes = %d (%v), want %d", check.kind, len(got), err, len(check.data))
		}
	}
	for _, id := range []string{spill.ID, spillStage.ID, spillStep.ID} {
		labSameNode(t, sa, sb, id)
		if n := mustNode(t, sb, id); n.Repo != b.repo || n.Lifecycle != store.Planned {
			t.Fatalf("receiver spill node %s = repo %q lifecycle %s", id, n.Repo, n.Lifecycle)
		}
	}

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

	// Neither non-active sibling project reached the feed or the receiver.
	changes := labLaravelFeedAll(t, f)
	siblingSet := map[string]bool{}
	for _, g := range siblingGroups {
		siblingSet[g] = true
	}
	nonActive := map[string]bool{unpairedRepo: true, pausedRepo: true}
	for _, change := range changes {
		if siblingSet[change.GroupID] || nonActive[change.SourceRepoID] {
			t.Fatalf("non-active project work reached the feed: %+v", change)
		}
	}
	// Spot-check that the combined fixture's originals are on the feed under
	// the sender's Repo: the family birth, the gate declaration, a spilled
	// content group and the collision.
	wantFeed := map[string]bool{
		sourceGroup(t, sa, work.matter.ID, store.TypeMatterCreated): true,
		sourceGroup(t, sa, a.repo, store.TypeGateDeclared):          true,
		sourceGroup(t, sa, spill.ID, store.TypeContentCreated):      true,
		collideGroup: true,
	}
	for _, change := range changes {
		if wantFeed[change.GroupID] {
			if change.SourceRepoID != a.repo {
				t.Fatalf("feed group %s carries repo %s, want %s", change.GroupID, change.SourceRepoID, a.repo)
			}
			delete(wantFeed, change.GroupID)
		}
	}
	if len(wantFeed) != 0 {
		t.Fatalf("combined fixture groups missing from feed: %v", wantFeed)
	}
	for _, absent := range []string{unpairedMatter.ID, pausedMatter.ID} {
		if _, err := sb.Node(labCtx, absent); err == nil {
			t.Fatal("non-active project work reached the receiver", absent)
		}
	}

	// The sibling pairings are untouched by the active Repo's passes.
	afterUnpaired, err := sa.ExchangeStatus(labCtx, unpairedRepo)
	if err != nil || afterUnpaired.Paired || len(afterUnpaired.PendingUpload) != 0 {
		t.Fatal("ordinary pass touched the unpaired project", afterUnpaired, err)
	}
	afterPaused, err := sa.ExchangeStatus(labCtx, pausedRepo)
	if err != nil || !afterPaused.Paired || afterPaused.Active || !reflect.DeepEqual(afterPaused.PendingUpload, beforePaused.PendingUpload) {
		t.Fatal("ordinary pass touched the paused pairing", afterPaused, err)
	}

	// Future work: created only after pairing, on the same active Repo. It
	// must sync on the next ordinary pass exactly like the pre-pair history —
	// no cutoff, no second opt-in, no special-casing.
	future, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Future", "future-release")
	if err != nil {
		t.Fatal(err)
	}
	futureStage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, future.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	futureStep, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo,
		future.Locator+"/"+futureStage.Locator, "Later")
	if err != nil {
		t.Fatal(err)
	}
	futureBrief := []byte("# future brief\n\nwritten after pairing\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, future.Locator, store.KindBrief, futureBrief); err != nil {
		t.Fatal(err)
	}
	if _, err = writesurface.Start(labCtx, sa, store.ActorHuman, a.repo,
		future.Locator+"/"+futureStage.Locator+"/"+futureStep.Locator); err != nil {
		t.Fatal(err)
	}
	futureBirth := sourceGroup(t, sa, future.ID, store.TypeMatterCreated)
	futureStart := sourceGroup(t, sa, futureStep.ID, store.TypeStepStarted)
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, statusB.Binding)
	sourceDecision(t, sb, futureBirth, "integrated")
	sourceDecision(t, sb, futureStart, "integrated")
	for _, id := range []string{future.ID, futureStage.ID, futureStep.ID} {
		labSameNode(t, sa, sb, id)
	}
	labState(t, sb, future.ID, store.InProgress)
	labState(t, sb, futureStage.ID, store.InProgress)
	labState(t, sb, futureStep.ID, store.InProgress)
	if got, err := sb.Content(labCtx, future.ID, store.KindBrief); err != nil || !bytes.Equal(got, futureBrief) {
		t.Fatalf("receiver future brief = %q %v", got, err)
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
	labAssertRelations(t, sa, sb, b.repo, rel, localEdge)
	t.Logf("combined fixture over Laravel: %d downloads, %d integrated effects, %d spilled groups, 1 conflict preserved",
		st.Downloaded, effects, spillPending)
}

// Stage-1 third-store echo: three stores on one endpoint. A seeds a skeleton,
// B makes non-equivalent edits that propagate as new originals, A echoes one
// back with a baseline naming B's original event, and late-joining C
// converges in one pass. C's own edit then propagates, and a divergent
// finish-vs-pause is retained as a conflict — while every store's retained
// decision keeps byte-identical source evidence and original event IDs.
func TestLaravelExchangeThirdStoreEcho(t *testing.T) {
	f := startLaravelFixture(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	c, sc := exchangeHost(t)
	statusA, err := sa.PairExchange(labCtx, a.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := sb.PairExchange(labCtx, b.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Owner}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}
	cc := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}

	// A seeds the relay skeleton before anyone else exists.
	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Relay", "relay")
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
	brief := []byte("relay brief\n")
	if _, err = writesurface.WriteOnce(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, store.KindBrief, brief); err != nil {
		t.Fatal(err)
	}
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	stepPath := matter.Locator + "/" + stage.Locator + "/" + step.Locator
	exchangePass(t, sb, cb, statusB.Binding)
	if got, err := sb.Content(labCtx, matter.ID, store.KindBrief); err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("B did not receive the seed brief: %q %v", got, err)
	}

	// B makes non-equivalent edits across the portable families: a finding,
	// a body, a cascading start, a backlog entry and its own Matter.
	bFinding := []byte("B found a seam\n")
	if _, err = writesurface.AppendFinding(labCtx, sb, store.ActorHuman, b.repo, matter.Locator, bFinding); err != nil {
		t.Fatal(err)
	}
	gBFind := sourceGroup(t, sb, matter.ID, store.TypeContentAppended)
	bBody := []byte("B authored the relay body\n")
	if _, err = writesurface.WriteOnce(labCtx, sb, store.ActorHuman, b.repo, stepPath, store.KindBody, bBody); err != nil {
		t.Fatal(err)
	}
	gBBody := sourceGroup(t, sb, step.ID, store.TypeContentCreated)
	if _, err = writesurface.Start(labCtx, sb, store.ActorHuman, b.repo, stepPath); err != nil {
		t.Fatal(err)
	}
	gBStart := sourceGroup(t, sb, step.ID, store.TypeStepStarted)
	bEntry, err := writesurface.BacklogAdd(labCtx, sb, store.ActorHuman, b.repo,
		store.ProvenanceIntake, "B triage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	gBEntry := sourceGroup(t, sb, bEntry.ID, store.TypeBacklogEntered)
	bLocal, err := writesurface.CreateMatter(labCtx, sb, store.ActorHuman, b.repo, "B Local", "b-local")
	if err != nil {
		t.Fatal(err)
	}
	gBLocal := sourceGroup(t, sb, bLocal.ID, store.TypeMatterCreated)
	bGroups := []string{gBFind, gBBody, gBStart, gBEntry, gBLocal}
	if _, err = sb.RunExchange(labCtx, cb, b.repo); err != nil {
		t.Fatal(err)
	}

	// A receives B's edits as new originals: B's own event IDs, integrated
	// as A-local effects — never re-attributed to A's history.
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	for _, g := range bGroups {
		sourceDecision(t, sa, g, "integrated")
	}
	if segs := labSegments(t, sa, matter.ID, store.KindFindings); len(segs) != 1 {
		t.Fatalf("A matter findings = %+v", segs)
	} else if got, err := sa.SegmentBytes(labCtx, segs[0].ID); err != nil || !bytes.Equal(got, bFinding) {
		t.Fatalf("A finding bytes = %q %v", got, err)
	}
	if got, err := sa.Content(labCtx, step.ID, store.KindBody); err != nil || !bytes.Equal(got, bBody) {
		t.Fatalf("A step body = %q %v", got, err)
	}
	for _, id := range []string{matter.ID, stage.ID, step.ID} {
		labState(t, sa, id, store.InProgress)
	}
	if got := labBacklog(t, sa, a.repo, bEntry.ID); got.State != "entered" || got.Title != "B triage" {
		t.Fatalf("A backlog entry = %+v", got)
	}
	if n := mustNode(t, sa, bLocal.ID); n.Repo != a.repo || n.Title != "B Local" {
		t.Fatalf("A did not receive B's local Matter: %+v", n)
	}
	// Wire identity: B's originals, A-local effects.
	labAssertWireIdentity(t, sb, gBFind, sa)
	labAssertWireIdentity(t, sb, gBBody, sa)
	labAssertWireIdentity(t, sb, gBStart, sa)
	labAssertWireIdentity(t, sb, gBEntry, sa)
	labAssertWireIdentity(t, sb, gBLocal, sa)

	// A echoes B's seam and proves the baseline names B's original event,
	// not A's local effect of it.
	aFinding := []byte("A acknowledges B's seam\n")
	if _, err = writesurface.AppendFinding(labCtx, sa, store.ActorHuman, a.repo, matter.Locator, aFinding); err != nil {
		t.Fatal(err)
	}
	gAEcho := sourceGroup(t, sa, matter.ID, store.TypeContentAppended)
	// The echo's baseline names the portable tail of A's Matter — B's
	// matter.started original from the cascade group — canonicalized back to
	// B's own event ID, while NativeBaseline keeps A's local effect of it.
	dBStart := labDecisionAny(t, sa, gBStart)
	var bMatterStartID, aMatterStartEffect string
	for i, act := range dBStart.Source.Actions {
		if act.Event.Type == store.TypeMatterStarted {
			bMatterStartID = act.Event.ID
			aMatterStartEffect = dBStart.Effects[i]
		}
	}
	if bMatterStartID == "" || aMatterStartEffect == "" {
		t.Fatal("cascade source lacks a matter.started action/effect pair")
	}
	echoSrc, err := sa.ExperimentExport(labCtx, statusA.Binding.InstallationID, gAEcho)
	if err != nil {
		t.Fatal(err)
	}
	if len(echoSrc.Actions) != 1 {
		t.Fatalf("echo source actions = %d", len(echoSrc.Actions))
	}
	if echoSrc.Actions[0].Baseline != bMatterStartID {
		t.Fatalf("echo baseline = %q, want B's original matter.started %q", echoSrc.Actions[0].Baseline, bMatterStartID)
	}
	if echoSrc.Actions[0].NativeBaseline != aMatterStartEffect {
		t.Fatalf("echo native baseline = %q, want A's local effect %q", echoSrc.Actions[0].NativeBaseline, aMatterStartEffect)
	}
	// The baseline really is B's own native event — not merely a shared ID.
	var sawBStart bool
	for _, ev := range labEvents(t, sb) {
		if ev.ID == bMatterStartID {
			sawBStart = ev.Actor == store.ActorHuman && ev.Type == store.TypeMatterStarted
		}
	}
	if !sawBStart {
		t.Fatal("echo baseline does not name a native B matter.started event")
	}
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, statusB.Binding)
	dEcho := sourceDecision(t, sb, gAEcho, "integrated")
	// On B the retained source is identical — the baseline names B's own
	// original, receiver-side, with no export helper involved.
	if len(dEcho.Source.Actions) != 1 || dEcho.Source.Actions[0].Baseline != bMatterStartID {
		t.Fatalf("B-side echo baseline = %+v, want %q", dEcho.Source.Actions, bMatterStartID)
	}
	segs := labSegments(t, sb, matter.ID, store.KindFindings)
	if len(segs) != 2 {
		t.Fatalf("B matter findings = %+v", segs)
	}
	for i, want := range [][]byte{bFinding, aFinding} {
		got, err := sb.SegmentBytes(labCtx, segs[i].ID)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("B finding %d = %q %v", i, got, err)
		}
	}

	// C pairs late and converges in one ordinary pass: every group on the
	// feed integrates with identical source evidence and original IDs.
	statusC, err := sc.PairExchange(labCtx, c.repo, f.endpoint())
	if err != nil || !statusC.Paired {
		t.Fatal("late joiner pairing failed", statusC, err)
	}
	doneC, err := sc.RunExchange(labCtx, cc, c.repo)
	if err != nil {
		t.Fatal(err)
	}
	if doneC.Conflicts != 0 || doneC.Waiting != 0 {
		t.Fatalf("late joiner status = %+v", doneC)
	}
	for _, id := range []string{matter.ID, stage.ID, step.ID, bLocal.ID} {
		if _, err := sc.Node(labCtx, id); err != nil {
			t.Fatalf("C missing node %s: %v", id, err)
		}
	}
	for _, id := range []string{matter.ID, stage.ID, step.ID} {
		labState(t, sc, id, store.InProgress)
	}
	if got, err := sc.Content(labCtx, step.ID, store.KindBody); err != nil || !bytes.Equal(got, bBody) {
		t.Fatalf("C step body = %q %v", got, err)
	}
	if got, err := sc.Content(labCtx, matter.ID, store.KindBrief); err != nil || !bytes.Equal(got, brief) {
		t.Fatalf("C brief = %q %v", got, err)
	}
	// Every store retained the same wire evidence and original IDs for the
	// whole feed: each group's origin keeps its self-echo mapping, every
	// other store minted local integration effects.
	for _, g := range bGroups {
		labAssertWireIdentity(t, sb, g, sa, sc)
	}
	labAssertWireIdentity(t, sa, gAEcho, sb, sc)

	// C's own edit propagates to both existing stores as a new original.
	cFinding := []byte("C confirms relay\n")
	if _, err = writesurface.AppendFinding(labCtx, sc, store.ActorHuman, c.repo, matter.Locator, cFinding); err != nil {
		t.Fatal(err)
	}
	gCFind := sourceGroup(t, sc, matter.ID, store.TypeContentAppended)
	if _, err = sc.RunExchange(labCtx, cc, c.repo); err != nil {
		t.Fatal(err)
	}
	for _, pass := range []func(){
		func() { exchangePass(t, sa, ca, statusA.Binding) },
		func() { exchangePass(t, sb, cb, statusB.Binding) },
	} {
		pass()
	}
	sourceDecision(t, sa, gCFind, "integrated")
	sourceDecision(t, sb, gCFind, "integrated")
	for _, s := range []*store.Store{sa, sb} {
		segs := labSegments(t, s, matter.ID, store.KindFindings)
		if len(segs) != 3 {
			t.Fatalf("store %s matter findings = %d segments", s.Path(), len(segs))
		}
		got, err := s.SegmentBytes(labCtx, segs[2].ID)
		if err != nil || !bytes.Equal(got, cFinding) {
			t.Fatalf("store %s finding 2 = %q %v", s.Path(), got, err)
		}
	}
	labAssertWireIdentity(t, sc, gCFind, sa, sb)

	// Divergent terminal edits: A finishes the step while C pauses it. A
	// uploads first so its finish lands ahead of C's pause on the feed; the
	// pause conflicts everywhere it arrives, the finish conflicts only on C.
	if _, err = writesurface.Finish(labCtx, sa, store.ActorHuman, a.repo, stepPath); err != nil {
		t.Fatal(err)
	}
	gAFinish := sourceGroup(t, sa, step.ID, store.TypeStepFinished)
	if _, err = writesurface.Pause(labCtx, sc, store.ActorHuman, c.repo, stepPath); err != nil {
		t.Fatal(err)
	}
	gCPause := sourceGroup(t, sc, step.ID, store.TypeStepPaused)
	if _, err = sa.RunExchange(labCtx, ca, a.repo); err != nil {
		t.Fatal(err)
	}
	if _, err = sc.RunExchange(labCtx, cc, c.repo); err != nil {
		t.Fatal(err)
	}
	exchangePass(t, sb, cb, statusB.Binding)
	exchangePass(t, sa, ca, statusA.Binding)

	sourceDecision(t, sb, gAFinish, "integrated")
	sourceDecision(t, sb, gCPause, "conflict")
	sourceDecision(t, sa, gCPause, "conflict")
	sourceDecision(t, sc, gAFinish, "conflict")
	labState(t, sa, step.ID, store.Done)
	labState(t, sb, step.ID, store.Done)
	labState(t, sc, step.ID, store.Paused)
	// The divergent sources keep identical evidence on every store that
	// retained them; each side's effects stayed its own.
	labAssertWireIdentity(t, sa, gAFinish, sb, sc)
	labAssertWireIdentity(t, sc, gCPause, sa, sb)

	// Rebuild preserves the converged-and-divergent end state on all three.
	for _, s := range []*store.Store{sa, sb, sc} {
		labRebuild(t, s)
	}
	labState(t, sa, step.ID, store.Done)
	labState(t, sb, step.ID, store.Done)
	labState(t, sc, step.ID, store.Paused)
	sourceDecision(t, sa, gCPause, "conflict")
	sourceDecision(t, sc, gAFinish, "conflict")
	t.Log("third-store echo over Laravel: seed, non-equivalent edits, canonical-baseline echo, late-join convergence, propagated edit and one retained conflict")
}

// Stage-1 synthetic provenance: fabricated ExperimentSource receipts carrying
// foreign host/actor/repo identities are posted straight to the feed, imported
// by a paired store, natively edited, re-exported — and the edits arrive on a
// late receiver as new originals whose baselines canonicalize back to the
// foreign originals. An unpaired control store proves nothing crosses without
// an active binding.
func TestLaravelExchangeSyntheticProvenanceReceipts(t *testing.T) {
	f := startLaravelFixture(t)
	x, sx := exchangeHost(t)
	y, sy := exchangeHost(t)
	w, sw := exchangeHost(t)
	z, sz := exchangeHost(t)

	// X's real work supplies the event identities the foreign claims borrow:
	// a Matter, Stage, Step and Brief exported verbatim, then re-attributed.
	xMatter, err := writesurface.CreateMatter(labCtx, sx, store.ActorHuman, x.repo, "Foreign", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	xStage, err := writesurface.CreateStage(labCtx, sx, store.ActorHuman, x.repo, xMatter.Locator, "Phase")
	if err != nil {
		t.Fatal(err)
	}
	xStep, err := writesurface.CreateStep(labCtx, sx, store.ActorHuman, x.repo,
		xMatter.Locator+"/"+xStage.Locator, "Do")
	if err != nil {
		t.Fatal(err)
	}
	xBrief := []byte("foreign brief\n")
	if _, err = writesurface.WriteOnce(labCtx, sx, store.ActorHuman, x.repo, xMatter.Locator, store.KindBrief, xBrief); err != nil {
		t.Fatal(err)
	}
	xGroups := []string{
		sourceGroup(t, sx, xMatter.ID, store.TypeMatterCreated),
		sourceGroup(t, sx, xStage.ID, store.TypeStageCreated),
		sourceGroup(t, sx, xStep.ID, store.TypeStepCreated),
		sourceGroup(t, sx, xMatter.ID, store.TypeContentCreated),
	}

	// Forge foreign-provenance receipts over X's real exports: a foreign
	// installation host, a foreign Repo identity, a role actor and a foreign
	// clock — posted through the same /origins + /changes API the CLI uses.
	foreignHost := "55555555-5555-4555-8555-555555555555"
	foreignRepo := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	foreignActor := store.RoleActor("ForeignPeer")
	foreignAt := followTime("2025-06-01T12:00:00Z")
	var forged []store.ExperimentSource
	for i, g := range xGroups {
		src, err := sx.ExperimentExport(labCtx, foreignHost, g)
		if err != nil {
			t.Fatal(err)
		}
		for j := range src.Actions {
			src.Actions[j].Event.Actor = foreignActor
			src.Actions[j].Event.OccurredAt = foreignAt.Add(time.Duration(i) * time.Minute)
			src.Actions[j].Event.Repo = foreignRepo
		}
		forged = append(forged, src)
		raw, err := json.Marshal(src)
		if err != nil {
			t.Fatal(err)
		}
		change := store.ExchangeChange{
			Version:        1,
			InstallationID: foreignHost,
			GroupID:        g,
			SourceRepoID:   foreignRepo,
			Source:         string(raw),
		}
		if i == 0 {
			fixtureHTTP(t, "POST", f.endpoint()+"/origins", f.Owner, map[string]string{"installation_id": foreignHost}, 200)
		}
		fixtureHTTP(t, "POST", f.endpoint()+"/changes", f.Owner, map[string]any{"changes": []store.ExchangeChange{change}}, 200)
	}

	// Y pairs and receives the synthetic work: four integrated decisions whose
	// retained source is the forged evidence verbatim, effects stamped local.
	statusY, err := sy.PairExchange(labCtx, y.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	cy := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}
	doneY, err := sy.RunExchange(labCtx, cy, y.repo)
	if err != nil {
		t.Fatal(err)
	}
	if doneY.Conflicts != 0 || doneY.Waiting != 0 {
		t.Fatalf("Y status after synthetic import = %+v", doneY)
	}
	for i, g := range xGroups {
		d := sourceDecision(t, sy, g, "integrated")
		if !reflect.DeepEqual(d.Source, forged[i]) {
			t.Fatalf("Y retained different source for %s", g)
		}
		// The foreign claims survive in the retained evidence; the effects
		// are Y-local integration events under Y's own Repo — never the
		// source's original IDs or foreign actor.
		for _, a := range d.Source.Actions {
			if a.Event.Actor != foreignActor || a.Event.Repo != foreignRepo {
				t.Fatalf("Y source lost foreign provenance: %+v", a.Event)
			}
		}
		labAssertWireIdentity(t, nil, g, sy)
	}
	for _, id := range []string{xMatter.ID, xStage.ID, xStep.ID} {
		n := mustNode(t, sy, id)
		if n.Repo != y.repo {
			t.Fatalf("Y node %s repo = %q, want %q", id, n.Repo, y.repo)
		}
	}
	if got, err := sy.Content(labCtx, xMatter.ID, store.KindBrief); err != nil || !bytes.Equal(got, xBrief) {
		t.Fatalf("Y brief = %q %v", got, err)
	}

	// Y natively edits the imported work: a finding, a body, a cascading
	// start, a backlog entry and its own Matter — each a new original whose
	// baseline canonicalizes back to the foreign originals.
	yFinding := []byte("Y local note\n")
	if _, err = writesurface.AppendFinding(labCtx, sy, store.ActorHuman, y.repo, xMatter.Locator, yFinding); err != nil {
		t.Fatal(err)
	}
	gYFind := sourceGroup(t, sy, xMatter.ID, store.TypeContentAppended)
	yBody := []byte("Y body\n")
	xStepPath := xMatter.Locator + "/" + xStage.Locator + "/" + xStep.Locator
	if _, err = writesurface.WriteOnce(labCtx, sy, store.ActorHuman, y.repo, xStepPath, store.KindBody, yBody); err != nil {
		t.Fatal(err)
	}
	gYBody := sourceGroup(t, sy, xStep.ID, store.TypeContentCreated)
	if _, err = writesurface.Start(labCtx, sy, store.ActorHuman, y.repo, xStepPath); err != nil {
		t.Fatal(err)
	}
	gYStart := sourceGroup(t, sy, xStep.ID, store.TypeStepStarted)
	yEntry, err := writesurface.BacklogAdd(labCtx, sy, store.ActorHuman, y.repo,
		store.ProvenanceIntake, "Y triage", "", "")
	if err != nil {
		t.Fatal(err)
	}
	gYEntry := sourceGroup(t, sy, yEntry.ID, store.TypeBacklogEntered)
	yOwn, err := writesurface.CreateMatter(labCtx, sy, store.ActorHuman, y.repo, "Y Own", "y-own")
	if err != nil {
		t.Fatal(err)
	}
	gYOwn := sourceGroup(t, sy, yOwn.ID, store.TypeMatterCreated)
	yGroups := []string{gYFind, gYBody, gYStart, gYEntry, gYOwn}

	// Pre-upload baseline proof: Y's finding baseline canonicalizes to X's
	// content.created original (the portable tail of the Matter), its body
	// baseline to X's step.created, and both name Y-local effects natively.
	yContentEffect := sourceDecision(t, sy, xGroups[3], "integrated").Effects[0]
	yStepEffect := sourceDecision(t, sy, xGroups[2], "integrated").Effects[0]
	findSrc, err := sy.ExperimentExport(labCtx, statusY.Binding.InstallationID, gYFind)
	if err != nil {
		t.Fatal(err)
	}
	if len(findSrc.Actions) != 1 {
		t.Fatalf("finding source actions = %d", len(findSrc.Actions))
	}
	if findSrc.Actions[0].Baseline != xGroups[3] {
		t.Fatalf("finding baseline = %q, want foreign content.created %q", findSrc.Actions[0].Baseline, xGroups[3])
	}
	if findSrc.Actions[0].NativeBaseline != yContentEffect {
		t.Fatalf("finding native baseline = %q, want Y effect %q", findSrc.Actions[0].NativeBaseline, yContentEffect)
	}
	if findSrc.Actions[0].Event.Repo != y.repo || findSrc.Actions[0].Event.Actor != store.ActorHuman {
		t.Fatalf("Y edit did not export as a new original: %+v", findSrc.Actions[0].Event)
	}
	bodySrc, err := sy.ExperimentExport(labCtx, statusY.Binding.InstallationID, gYBody)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodySrc.Actions) != 1 || bodySrc.Actions[0].Baseline != xGroups[2] || bodySrc.Actions[0].NativeBaseline != yStepEffect {
		t.Fatalf("body baseline = %+v, want foreign step.created %q via Y effect %q",
			bodySrc.Actions, xGroups[2], yStepEffect)
	}

	if _, err = sy.RunExchange(labCtx, cy, y.repo); err != nil {
		t.Fatal(err)
	}

	// W pairs late and converges: the forged receipts and Y's new originals
	// all integrate, with identical source evidence and canonical baselines.
	statusW, err := sw.PairExchange(labCtx, w.repo, f.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	cw := store.ExchangeClient{HTTP: &http.Client{Timeout: 30 * time.Second}, Token: f.Member}
	doneW, err := sw.RunExchange(labCtx, cw, w.repo)
	if err != nil {
		t.Fatal(err)
	}
	if doneW.Conflicts != 0 || doneW.Waiting != 0 {
		t.Fatalf("W status = %+v", doneW)
	}
	for i, g := range xGroups {
		d := sourceDecision(t, sw, g, "integrated")
		if !reflect.DeepEqual(d.Source, forged[i]) {
			t.Fatalf("W retained different forged source for %s", g)
		}
	}
	for _, g := range yGroups {
		d := sourceDecision(t, sw, g, "integrated")
		// Y's edits arrive as new originals claiming Y's Repo and actor —
		// never the foreign identities — with baselines naming the foreign
		// originals they built on.
		for _, a := range d.Source.Actions {
			if a.Event.Repo != y.repo || a.Event.Actor != store.ActorHuman {
				t.Fatalf("W saw Y edit with wrong provenance: %+v", a.Event)
			}
		}
	}
	if d := labDecisionAny(t, sw, gYFind); len(d.Source.Actions) != 1 || d.Source.Actions[0].Baseline != xGroups[3] {
		t.Fatalf("W-side finding baseline = %+v, want %q", d.Source.Actions, xGroups[3])
	}
	if d := labDecisionAny(t, sw, gYBody); len(d.Source.Actions) != 1 || d.Source.Actions[0].Baseline != xGroups[2] {
		t.Fatalf("W-side body baseline = %+v, want %q", d.Source.Actions, xGroups[2])
	}
	if got, err := sw.Content(labCtx, xMatter.ID, store.KindBrief); err != nil || !bytes.Equal(got, xBrief) {
		t.Fatalf("W brief = %q %v", got, err)
	}
	if got, err := sw.Content(labCtx, xMatter.ID, store.KindFindings); err != nil || !bytes.Equal(got, yFinding) {
		t.Fatalf("W findings = %q %v", got, err)
	}
	labState(t, sw, xStep.ID, store.InProgress)

	// Z never pairs: it imports the same forged sources locally, edits them,
	// and none of it can reach the feed — RunExchange refuses before HTTP.
	for _, src := range forged {
		labImportBound(t, sz, src, z.repo, "integrated")
	}
	zFinding := []byte("Z local only\n")
	if _, err = writesurface.AppendFinding(labCtx, sz, store.ActorHuman, z.repo, xMatter.Locator, zFinding); err != nil {
		t.Fatal(err)
	}
	gZFind := sourceGroup(t, sz, xMatter.ID, store.TypeContentAppended)
	zOwn, err := writesurface.CreateMatter(labCtx, sz, store.ActorHuman, z.repo, "Z Own", "z-own")
	if err != nil {
		t.Fatal(err)
	}
	gZOwn := sourceGroup(t, sz, zOwn.ID, store.TypeMatterCreated)
	zGroups := []string{gZFind, gZOwn}
	zSet := map[string]bool{}
	for _, g := range zGroups {
		zSet[g] = true
	}
	zStatus, err := sz.ExchangeStatus(labCtx, z.repo)
	if err != nil || zStatus.Paired {
		t.Fatal("unpaired control claims a pairing", zStatus, err)
	}
	cz := store.ExchangeClient{HTTP: &http.Client{Timeout: 10 * time.Second}, Token: f.Member}
	if _, err = sz.RunExchange(labCtx, cz, z.repo); err == nil || !strings.Contains(err.Error(), "not actively paired") {
		t.Fatalf("unpaired store exchanged: %v", err)
	}
	// The feed carries the forged receipts under the foreign installation and
	// Y's originals under Y's — never anything claiming Z's Repo or groups.
	changes := labLaravelFeedAll(t, f)
	var sawForeign, sawY bool
	for _, change := range changes {
		if zSet[change.GroupID] || change.SourceRepoID == z.repo {
			t.Fatalf("unpaired store work reached the feed: %+v", change)
		}
		if change.InstallationID == foreignHost {
			sawForeign = true
		}
		if change.SourceRepoID == y.repo {
			sawY = true
		}
	}
	if !sawForeign || !sawY {
		t.Fatal("feed missing the forged receipts or Y's originals")
	}
	// Z's local state is intact and W's view is unchanged by Z's existence.
	if got, err := sz.Content(labCtx, xMatter.ID, store.KindFindings); err != nil || !bytes.Equal(got, zFinding) {
		t.Fatalf("Z findings = %q %v", got, err)
	}
	if n := mustNode(t, sz, zOwn.ID); n.Repo != z.repo {
		t.Fatalf("Z own Matter = %+v", n)
	}
	wEvents := len(labEvents(t, sw))
	exchangePass(t, sw, cw, statusW.Binding)
	if len(labEvents(t, sw)) != wEvents {
		t.Fatal("W appended events for work that never reached the feed")
	}
	t.Log("synthetic provenance over Laravel: foreign receipts imported, native edits re-exported as new originals with canonical baselines, unpaired control untouched")
}
