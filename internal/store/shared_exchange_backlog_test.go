//go:build sharedstoreexperiment

package store_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labBacklog reads one entry by identity on s — the projection fact every
// backlog outcome test asserts against.
func labBacklog(t *testing.T, s *store.Store, repo, id string) store.BacklogEntry {
	t.Helper()
	got, found, err := s.BacklogEntry(labCtx, repo, id)
	if err != nil || !found {
		t.Fatalf("backlog entry %s: found=%v err=%v", id, found, err)
	}
	return got
}

// labBacklogSource exports the committed group that produced subject's newest
// event of kind — labExportAfter's shape for verbs that return no node.
func labBacklogSource(t *testing.T, s *store.Store, host, subject, kind string) store.ExperimentSource {
	t.Helper()
	source, err := s.ExperimentExport(labCtx, host, sourceGroup(t, s, subject, kind))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// labOutboxRows counts the delivery-authority rows one Repo holds — an
// imported delegation must leave this number untouched.
func labOutboxRows(t *testing.T, path, repo string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbox_entries WHERE repo=?`, repo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The three exits share entered -> terminal: each is its own committed group,
// the outcome's baseline is the entered effect earlier groups mapped, and the
// receiver's entry row lands in the same terminal state the source observed.
// delegated records the source's outbox identity as audit evidence and queues
// nothing locally — the fold's integrationEffectActor guard.
func TestSharedStoreBacklogOutcomesBound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)

	// entered alone: provenance, title, detail and a resolved origin node
	// round-trip; the verified self-echo maps back to the native identity.
	plannedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Plan me", "found mid-flight", "")
	if err != nil {
		t.Fatal(err)
	}
	enteredSource := labBacklogSource(t, p.hybrid, "Hybrid", plannedEntry.ID, store.TypeBacklogEntered)
	d := labImportBound(t, p.fluke, enteredSource, p.repo, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("entered effects = %v", d.Effects)
	}
	got := labBacklog(t, p.fluke, p.repo, plannedEntry.ID)
	if got.State != "entered" || got.Provenance != store.ProvenanceFound ||
		got.Title != "Plan me" || got.Detail != "found mid-flight" {
		t.Fatalf("imported entry = %+v", got)
	}
	again := labImportBound(t, p.fluke, enteredSource, p.repo, "integrated")
	if !reflect.DeepEqual(d, again) {
		t.Fatal("entered retry was not inert", again)
	}
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, enteredSource, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != enteredSource.Actions[0].Event.ID ||
		len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("entered self-echo was not inert", echo)
	}

	// planned consumes the entry into the shared Matter.
	plannedSource := labExportAfter(t, p.hybrid, "Hybrid", plannedEntry.ID, store.TypeBacklogPlanned,
		func() error {
			_, err := writesurface.BacklogPlan(labCtx, p.hybrid, store.ActorHuman, p.repo, plannedEntry.ID, "shared")
			return err
		})
	labImportBound(t, p.fluke, plannedSource, p.repo, "integrated")
	got = labBacklog(t, p.fluke, p.repo, plannedEntry.ID)
	if got.State != "planned" || got.Matter != p.matter {
		t.Fatalf("planned entry = %+v", got)
	}
	hybridBefore = len(labEvents(t, p.hybrid))
	echo = labImportBound(t, p.hybrid, plannedSource, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != plannedSource.Actions[0].Event.ID ||
		len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("planned self-echo was not inert", echo)
	}

	// declined keeps its reason — the fold's schema>=10 decline_reason column.
	declinedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Decline me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", declinedEntry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	declinedSource := labExportAfter(t, p.hybrid, "Hybrid", declinedEntry.ID, store.TypeBacklogDeclined,
		func() error {
			_, err := writesurface.BacklogDecline(labCtx, p.hybrid, store.ActorHuman, p.repo, declinedEntry.ID, "the premise changed")
			return err
		})
	labImportBound(t, p.fluke, declinedSource, p.repo, "integrated")
	got = labBacklog(t, p.fluke, p.repo, declinedEntry.ID)
	if got.State != "declined" || got.DeclineReason != "the premise changed" {
		t.Fatalf("declined entry = %+v", got)
	}

	// deferred provenance carries the origin node the verb resolved.
	deferredEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceDeferred, "Found late", "pushed out of the stage", "shared/stage")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", deferredEntry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	got = labBacklog(t, p.fluke, p.repo, deferredEntry.ID)
	if got.State != "entered" || got.Provenance != store.ProvenanceDeferred || got.OriginNode != stage {
		t.Fatalf("deferred entry = %+v, want origin %s", got, stage)
	}

	// delegated records the source's outbox identity as audit evidence and
	// queues nothing locally — the fold's integrationEffectActor guard.
	delegatedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Delegate me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	before := labOutboxRows(t, p.fluke.Path(), p.repo)
	var delegated store.BacklogEntry
	delegatedSource := labExportAfter(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogDelegated,
		func() error {
			var err error
			delegated, err = writesurface.BacklogDelegate(labCtx, p.hybrid, store.ActorHuman, p.repo, delegatedEntry.ID)
			return err
		})
	labImportBound(t, p.fluke, delegatedSource, p.repo, "integrated")
	got = labBacklog(t, p.fluke, p.repo, delegatedEntry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox || delegated.Outbox == "" {
		t.Fatalf("delegated entry = %+v, want outbox %s", got, delegated.Outbox)
	}
	if after := labOutboxRows(t, p.fluke.Path(), p.repo); after != before {
		t.Fatalf("imported delegation minted %d outbox rows", after-before)
	}
	// Only the still-entered deferred entry remains active; every terminal
	// state — planned, declined, delegated-without-local-outbox — is done.
	active, err := p.fluke.ActiveBacklog(labCtx, p.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != deferredEntry.ID {
		t.Fatalf("active backlog = %+v", active)
	}
	labRebuild(t, p.fluke)
	got = labBacklog(t, p.fluke, p.repo, delegatedEntry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox {
		t.Fatalf("rebuild lost the delegation fact: %+v", got)
	}
}

// BacklogAdd's native auto-push shape commits [backlog.entered,
// backlog.delegated] as ONE group: the delegated member classifies after the
// entered member's effect is visible in the same pass, and the whole unit
// lands atomically — entry delegated, source outbox identity recorded, zero
// local delivery authority.
func TestSharedStoreBacklogDelegatedGroup(t *testing.T) {
	p := newLabPair(t)
	if err := p.hybrid.SetConfig(labCtx, p.repo, store.TrackerBackendKey, "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.hybrid.SetTrackerBacklogPush(labCtx, p.repo, "auto"); err != nil {
		t.Fatal(err)
	}
	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Auto push", "", "")
	if err != nil {
		t.Fatal(err)
	}
	source := labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered)
	if len(source.Actions) != 2 || source.Actions[0].Event.Type != store.TypeBacklogEntered ||
		source.Actions[1].Event.Type != store.TypeBacklogDelegated ||
		source.Actions[1].Baseline != source.Actions[0].Event.ID {
		t.Fatalf("auto-push group = %+v", source.Actions)
	}
	var delegated store.BacklogDelegated
	if err := json.Unmarshal(source.Actions[1].Event.Payload, &delegated); err != nil {
		t.Fatal(err)
	}
	before := labOutboxRows(t, p.fluke.Path(), p.repo)
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 2 {
		t.Fatalf("group effects = %v", d.Effects)
	}
	got := labBacklog(t, p.fluke, p.repo, entry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox {
		t.Fatalf("auto-delegated entry = %+v, want outbox %s", got, delegated.Outbox)
	}
	if after := labOutboxRows(t, p.fluke.Path(), p.repo); after != before {
		t.Fatalf("atomic delegation minted %d outbox rows", after-before)
	}
	active, err := p.fluke.ActiveBacklog(labCtx, p.repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range active {
		if e.ID == entry.ID {
			t.Fatal("inert delegation stayed active", e)
		}
	}
	// The origin store's echo maps both members back to native identities.
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if len(echo.Effects) != 2 || echo.Effects[0] != source.Actions[0].Event.ID ||
		echo.Effects[1] != source.Actions[1].Event.ID || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("group self-echo was not inert", echo)
	}
	labRebuild(t, p.fluke)
	got = labBacklog(t, p.fluke, p.repo, entry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox {
		t.Fatalf("rebuild lost the delegation fact: %+v", got)
	}
}

// An outcome that outruns its entry's birth waits as pending — the mapped
// baseline names evidence that has not arrived — and integrates on recheck
// once the entered group lands. An entered naming an origin node that has
// not arrived waits the same way.
func TestSharedStoreBacklogPending(t *testing.T) {
	p := newLabPair(t)

	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Plan later", "", "")
	if err != nil {
		t.Fatal(err)
	}
	plannedSource := labExportAfter(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogPlanned,
		func() error {
			_, err := writesurface.BacklogPlan(labCtx, p.hybrid, store.ActorHuman, p.repo, entry.ID, "shared")
			return err
		})
	d := labImportBound(t, p.fluke, plannedSource, p.repo, "pending")
	if d.Reason != "unknown baseline dependency" {
		t.Fatalf("plan-before-enter reason = %q", d.Reason)
	}
	enteredSource := labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered)
	labImportBound(t, p.fluke, enteredSource, p.repo, "integrated")
	followRecheck(t, p.fluke, plannedSource, "integrated")
	got := labBacklog(t, p.fluke, p.repo, entry.ID)
	if got.State != "planned" || got.Matter != p.matter {
		t.Fatalf("rechecked entry = %+v", got)
	}

	// deferred provenance resolved its origin on the source; here the node
	// has not arrived, so the entry waits rather than wedging or guessing.
	stage2, err := writesurface.CreateStage(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared", "Late")
	if err != nil {
		t.Fatal(err)
	}
	late, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceDeferred, "Found in the late stage", "pushed out mid-flight", "shared/"+stage2.Locator)
	if err != nil {
		t.Fatal(err)
	}
	lateSource := labBacklogSource(t, p.hybrid, "Hybrid", late.ID, store.TypeBacklogEntered)
	d = labImportBound(t, p.fluke, lateSource, p.repo, "pending")
	if d.Reason != "backlog origin awaiting node" {
		t.Fatalf("missing-origin reason = %q", d.Reason)
	}
	stageSource := labBacklogSource(t, p.hybrid, "Hybrid", stage2.ID, store.TypeStageCreated)
	labImportBound(t, p.fluke, stageSource, p.repo, "integrated")
	followRecheck(t, p.fluke, lateSource, "integrated")
	got = labBacklog(t, p.fluke, p.repo, late.ID)
	if got.State != "entered" || got.OriginNode != stage2.ID {
		t.Fatalf("rechecked deferred entry = %+v, want origin %s", got, stage2.ID)
	}
	labRebuild(t, p.fluke)
}

// Terminal preservation: the exits are final. A declined claim on an entry
// the receiver already planned diverges from observed history and conflicts;
// a second delegated claim naming a different outbox identity is a divergent
// echo, not a retry. Neither mints an effect.
func TestSharedStoreBacklogTerminalConflicts(t *testing.T) {
	p := newLabPair(t)

	entry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Exit once", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enteredSource := labBacklogSource(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogEntered)
	labImportBound(t, p.fluke, enteredSource, p.repo, "integrated")
	plannedSource := labExportAfter(t, p.hybrid, "Hybrid", entry.ID, store.TypeBacklogPlanned,
		func() error {
			_, err := writesurface.BacklogPlan(labCtx, p.hybrid, store.ActorHuman, p.repo, entry.ID, "shared")
			return err
		})
	labImportBound(t, p.fluke, plannedSource, p.repo, "integrated")

	// Forge a declined claim on the same entry: fresh event identity, same
	// subject and baseline, different type and payload — what a second
	// terminal exit would look like if one could be committed.
	forged := store.ExperimentSource{
		Version: plannedSource.Version,
		Host:    plannedSource.Host,
		Actions: append([]store.ExperimentAction(nil), plannedSource.Actions...),
	}
	action := &forged.Actions[0]
	payload, err := json.Marshal(store.BacklogDeclined{Reason: "changed my mind"})
	if err != nil {
		t.Fatal(err)
	}
	action.Event.ID = p.hybrid.NewID()
	action.Event.Type = store.TypeBacklogDeclined
	action.Event.Payload = payload
	action.Event.Causation, action.Event.Correlation = action.Event.ID, action.Event.ID
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, forged, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("divergent exit produced effects: %+v", d)
	}
	if got := labBacklog(t, p.fluke, p.repo, entry.ID); got.State != "planned" {
		t.Fatalf("conflicted entry = %+v", got)
	}

	// A delegated claim that already landed cannot be re-claimed with a
	// different outbox identity: same type and position, divergent payload.
	delegatedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Delegate once", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImportBound(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogEntered), p.repo, "integrated")
	delegatedSource := labExportAfter(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogDelegated,
		func() error {
			_, err := writesurface.BacklogDelegate(labCtx, p.hybrid, store.ActorHuman, p.repo, delegatedEntry.ID)
			return err
		})
	labImportBound(t, p.fluke, delegatedSource, p.repo, "integrated")
	accepted := labBacklog(t, p.fluke, p.repo, delegatedEntry.ID)

	second := store.ExperimentSource{
		Version: delegatedSource.Version,
		Host:    delegatedSource.Host,
		Actions: append([]store.ExperimentAction(nil), delegatedSource.Actions...),
	}
	action = &second.Actions[0]
	payload, err = json.Marshal(store.BacklogDelegated{
		Outbox:         p.hybrid.NewID(),
		IdempotencyKey: "backlog:" + delegatedEntry.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	action.Event.ID = p.hybrid.NewID()
	action.Event.Payload = payload
	action.Event.Causation, action.Event.Correlation = action.Event.ID, action.Event.ID
	before = len(labEvents(t, p.fluke))
	d = labImportBound(t, p.fluke, second, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("divergent delegation produced effects: %+v", d)
	}
	if got := labBacklog(t, p.fluke, p.repo, delegatedEntry.ID); got != accepted {
		t.Fatalf("conflicted delegation changed the entry: %+v -> %+v", accepted, got)
	}
	labRebuild(t, p.fluke)
}

// The family is Repo-durable paired state, so bound and unbound classification
// agree — the same four facts integrate through the legacy unscoped import.
func TestSharedStoreBacklogUnbound(t *testing.T) {
	p := newLabPair(t)

	plannedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Unbound plan", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImport(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", plannedEntry.ID, store.TypeBacklogEntered), "integrated")
	plannedSource := labExportAfter(t, p.hybrid, "Hybrid", plannedEntry.ID, store.TypeBacklogPlanned,
		func() error {
			_, err := writesurface.BacklogPlan(labCtx, p.hybrid, store.ActorHuman, p.repo, plannedEntry.ID, "shared")
			return err
		})
	labImport(t, p.fluke, plannedSource, "integrated")
	if got := labBacklog(t, p.fluke, p.repo, plannedEntry.ID); got.State != "planned" || got.Matter != p.matter {
		t.Fatalf("unbound planned entry = %+v", got)
	}

	declinedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceIntake, "Unbound decline", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImport(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", declinedEntry.ID, store.TypeBacklogEntered), "integrated")
	declinedSource := labExportAfter(t, p.hybrid, "Hybrid", declinedEntry.ID, store.TypeBacklogDeclined,
		func() error {
			_, err := writesurface.BacklogDecline(labCtx, p.hybrid, store.ActorHuman, p.repo, declinedEntry.ID, "unbound no")
			return err
		})
	labImport(t, p.fluke, declinedSource, "integrated")
	if got := labBacklog(t, p.fluke, p.repo, declinedEntry.ID); got.State != "declined" || got.DeclineReason != "unbound no" {
		t.Fatalf("unbound declined entry = %+v", got)
	}

	delegatedEntry, err := writesurface.BacklogAdd(labCtx, p.hybrid, store.ActorHuman, p.repo,
		store.ProvenanceFound, "Unbound delegate", "", "")
	if err != nil {
		t.Fatal(err)
	}
	labImport(t, p.fluke, labBacklogSource(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogEntered), "integrated")
	before := labOutboxRows(t, p.fluke.Path(), p.repo)
	var delegated store.BacklogEntry
	delegatedSource := labExportAfter(t, p.hybrid, "Hybrid", delegatedEntry.ID, store.TypeBacklogDelegated,
		func() error {
			var err error
			delegated, err = writesurface.BacklogDelegate(labCtx, p.hybrid, store.ActorHuman, p.repo, delegatedEntry.ID)
			return err
		})
	labImport(t, p.fluke, delegatedSource, "integrated")
	got := labBacklog(t, p.fluke, p.repo, delegatedEntry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox {
		t.Fatalf("unbound delegated entry = %+v, want outbox %s", got, delegated.Outbox)
	}
	if after := labOutboxRows(t, p.fluke.Path(), p.repo); after != before {
		t.Fatalf("unbound delegation minted %d outbox rows", after-before)
	}
	labRebuild(t, p.fluke)
}

// The widened whitelist is what ExchangeStatus selects automatically and what
// the wire carries: entered and its exits upload as portable units and the
// receiver folds them against its own repo binding — delegated inertly.
func TestSharedStoreBacklogWire(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	status, err := sa.PairExchange(labCtx, a.repo, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ba := status.Binding
	bb := exchangeBind(t, sb, b, endpoint)
	upload := func(groups ...string) {
		t.Helper()
		exchangePass(t, sa, ca, ba, groups...)
		exchangePass(t, sb, cb, bb)
	}

	matter, err := writesurface.CreateMatter(labCtx, sa, store.ActorHuman, a.repo, "Wire", "wire")
	if err != nil {
		t.Fatal(err)
	}
	upload(sourceGroup(t, sa, matter.ID, store.TypeMatterCreated))

	entry, err := writesurface.BacklogAdd(labCtx, sa, store.ActorHuman, a.repo,
		store.ProvenanceFound, "Wire entry", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enteredGroup := sourceGroup(t, sa, entry.ID, store.TypeBacklogEntered)
	// The whitelist makes the group pending upload automatically.
	st, err := sa.ExchangeStatus(labCtx, a.repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.PendingUpload, enteredGroup) {
		t.Fatalf("backlog group not pending upload: %+v", st.PendingUpload)
	}
	upload(enteredGroup)
	sourceDecision(t, sb, enteredGroup, "integrated")
	if got := labBacklog(t, sb, b.repo, entry.ID); got.State != "entered" {
		t.Fatalf("receiver entry = %+v", got)
	}

	if _, err := writesurface.BacklogPlan(labCtx, sa, store.ActorHuman, a.repo, entry.ID, "wire"); err != nil {
		t.Fatal(err)
	}
	plannedGroup := sourceGroup(t, sa, entry.ID, store.TypeBacklogPlanned)
	upload(plannedGroup)
	sourceDecision(t, sb, plannedGroup, "integrated")
	if got := labBacklog(t, sb, b.repo, entry.ID); got.State != "planned" || got.Matter != matter.ID {
		t.Fatalf("receiver planned entry = %+v", got)
	}

	// delegated crosses the wire as evidence only: state and source outbox
	// identity arrive, delivery authority does not.
	delegatedEntry, err := writesurface.BacklogAdd(labCtx, sa, store.ActorHuman, a.repo,
		store.ProvenanceIntake, "Wire delegate", "", "")
	if err != nil {
		t.Fatal(err)
	}
	upload(sourceGroup(t, sa, delegatedEntry.ID, store.TypeBacklogEntered))
	if _, err := writesurface.BacklogDelegate(labCtx, sa, store.ActorHuman, a.repo, delegatedEntry.ID); err != nil {
		t.Fatal(err)
	}
	delegated, found, err := sa.BacklogEntry(labCtx, a.repo, delegatedEntry.ID)
	if err != nil || !found {
		t.Fatalf("source delegated entry: found=%v err=%v", found, err)
	}
	delegatedGroup := sourceGroup(t, sa, delegatedEntry.ID, store.TypeBacklogDelegated)
	before := labOutboxRows(t, sb.Path(), b.repo)
	upload(delegatedGroup)
	sourceDecision(t, sb, delegatedGroup, "integrated")
	got := labBacklog(t, sb, b.repo, delegatedEntry.ID)
	if got.State != "delegated" || got.Outbox != delegated.Outbox {
		t.Fatalf("receiver delegated entry = %+v, want outbox %s", got, delegated.Outbox)
	}
	if after := labOutboxRows(t, sb.Path(), b.repo); after != before {
		t.Fatalf("receiver minted %d outbox rows", after-before)
	}
	labRebuild(t, sb)
}
