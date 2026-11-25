//go:build sharedstoreexperiment

package store_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// labChildIDs lists a node's live children in sibling order — the observable
// result of step.reordered and of where step.inserted landed.
func labChildIDs(t *testing.T, s *store.Store, parent string) []string {
	t.Helper()
	children, err := s.Children(labCtx, parent)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	return ids
}

func labVerbInsert(s *store.Store, repo, parent, title, after, before string) func() error {
	return func() error {
		_, err := writesurface.InsertStep(labCtx, s, store.ActorHuman, repo, parent, title, after, before)
		return err
	}
}

func labVerbReorder(s *store.Store, repo, parent string, order ...string) func() error {
	return func() error {
		_, err := writesurface.ReorderStep(labCtx, s, store.ActorHuman, repo, parent, order)
		return err
	}
}

func labVerbReplace(s *store.Store, repo, locator, title string) func() error {
	return func() error {
		_, err := writesurface.ReplaceStep(labCtx, s, store.ActorHuman, repo, locator, title)
		return err
	}
}

func labVerbRemove(s *store.Store, repo, locator, reason string) func() error {
	return func() error {
		return writesurface.RemoveStep(labCtx, s, store.ActorHuman, repo, locator, reason)
	}
}

// labExportNode runs a birth verb that returns its new node, then exports the
// group that produced the node's own event — step.inserted's subject only
// exists once the verb commits.
func labExportNode(t *testing.T, s *store.Store, host, kind string, verb func() (store.Node, error)) store.ExperimentSource {
	t.Helper()
	n, err := verb()
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.ExperimentExport(labCtx, host, sourceGroup(t, s, n.ID, kind))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// step.inserted is the positioned-birth verb's event — the same NodeBirth fold
// as step.created — and it is bound-only like every birth. A Step may hang
// directly under a Matter (D2) or under a Stage, and both parents round-trip.
func TestSharedStoreStepInsertBound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)

	// Positioned between the two Stage steps: after step-01.
	insertSource := labExportNode(t, p.hybrid, "Hybrid", store.TypeStepInserted, func() (store.Node, error) {
		return writesurface.InsertStep(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared/stage", "Inserted", "shared/step-01", "")
	})
	d := labImportBound(t, p.fluke, insertSource, p.repo, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("insert effects = %v", d.Effects)
	}
	insertedID := insertSource.Actions[0].Event.Subject
	n, err := p.fluke.Node(labCtx, insertedID)
	if err != nil || n.Kind != store.ScaleStep || n.Parent != stage || n.Locator != "step-03" ||
		n.Lifecycle != store.Planned || n.Matter != p.matter {
		t.Fatalf("inserted node = %+v %v", n, err)
	}
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step1, insertedID, p.step2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stage children = %v, want %v", got, want)
	}
	// The verified self-echo maps back to the native id without new events.
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, insertSource, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != insertSource.Actions[0].Event.ID || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("insert self-echo was not inert", echo)
	}

	// A Step may also hang directly under the Matter (D2).
	directSource := labExportNode(t, p.hybrid, "Hybrid", store.TypeStepInserted, func() (store.Node, error) {
		return writesurface.InsertStep(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared", "Direct", "", "")
	})
	labImportBound(t, p.fluke, directSource, p.repo, "integrated")
	directID := directSource.Actions[0].Event.Subject
	direct, err := p.fluke.Node(labCtx, directID)
	if err != nil || direct.Kind != store.ScaleStep || direct.Parent != p.matter || direct.Locator != "step-04" {
		t.Fatalf("matter-direct step = %+v %v", direct, err)
	}
	labRebuild(t, p.fluke)
	labRebuild(t, p.hybrid)
}

// step.reordered names the parent and carries the whole live sibling set in
// its new order. The fold only rewrites sort_key — presentation-only (D51) —
// so the receiver's observable order must match exactly, and the self-echo
// must map back to the native effect without new events.
func TestSharedStoreStepReorderBound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)

	source := labExportAfter(t, p.hybrid, "Hybrid", stage, store.TypeStepReordered,
		labVerbReorder(p.hybrid, p.repo, "shared/stage", "shared/step-02", "shared/step-01"))
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("reorder effects = %v", d.Effects)
	}
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step2, p.step1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fluke stage children = %v, want %v", got, want)
	}
	if got, want := labChildIDs(t, p.hybrid, stage), []string{p.step2, p.step1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hybrid stage children = %v, want %v", got, want)
	}
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != source.Actions[0].Event.ID || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("reorder self-echo was not inert", echo)
	}

	// The same amendment under a Matter parent: two Steps born directly
	// beneath it, then reversed — subject kind 'matter' is admitted too.
	var direct []string
	for _, title := range []string{"Direct A", "Direct B"} {
		src := labExportNode(t, p.hybrid, "Hybrid", store.TypeStepInserted, func() (store.Node, error) {
			return writesurface.InsertStep(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared", title, "", "")
		})
		labImportBound(t, p.fluke, src, p.repo, "integrated")
		direct = append(direct, src.Actions[0].Event.Subject)
	}
	matterReorder := labExportAfter(t, p.hybrid, "Hybrid", p.matter, store.TypeStepReordered,
		labVerbReorder(p.hybrid, p.repo, "shared", "shared/step-04", "shared/step-03"))
	labImportBound(t, p.fluke, matterReorder, p.repo, "integrated")
	ids := labChildIDs(t, p.fluke, p.matter)
	pos := map[string]int{}
	for i, id := range ids {
		pos[id] = i
	}
	if pos[direct[1]] > pos[direct[0]] {
		t.Fatalf("matter-direct steps not reordered: children=%v", ids)
	}
	labRebuild(t, p.fluke)
}

// step.replaced tombstones the old Step and births its replacement in the
// same sibling position under a fresh identity. The replacement's birth is
// filed under the *replaced* node's subject, so its own subject history starts
// empty — later events on it must still recover parent and baseline evidence,
// which is what the export's birth_event lookup provides.
func TestSharedStoreStepReplaceBound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)
	oldSort := mustNode(t, p.fluke, p.step1).SortKey

	source := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepReplaced,
		labVerbReplace(p.hybrid, p.repo, "shared/step-01", "Replacement"))
	var replaced store.Replaced
	if err := json.Unmarshal(source.Actions[0].Event.Payload, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.Locator != "step-03" {
		t.Fatalf("replacement locator = %q", replaced.Locator)
	}
	repID := replaced.Replacement
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("replace effects = %v", d.Effects)
	}

	// The old Step is tombstoned; the replacement is live, planned, in the
	// same sibling slot — same parent, same sort_key, next step-NN locator.
	if _, err := p.fluke.Node(labCtx, p.step1); err == nil {
		t.Fatal("replaced step is still live on the receiver")
	}
	rep, err := p.fluke.Node(labCtx, repID)
	if err != nil || rep.Kind != store.ScaleStep || rep.Parent != stage || rep.Locator != "step-03" ||
		rep.Title != "Replacement" || rep.Lifecycle != store.Planned || rep.SortKey != oldSort {
		t.Fatalf("replacement = %+v %v", rep, err)
	}
	if got, want := labChildIDs(t, p.fluke, stage), []string{repID, p.step2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stage children = %v, want %v", got, want)
	}
	// Its birth is cross-subject: the node's own history is empty while the
	// replaced node's history ends with the fold event.
	if history, err := p.fluke.EventsOfSubject(labCtx, repID); err != nil || len(history) != 0 {
		t.Fatalf("replacement subject history = %+v %v", history, err)
	}
	oldHistory, err := p.fluke.EventsOfSubject(labCtx, p.step1)
	if err != nil || len(oldHistory) == 0 || oldHistory[len(oldHistory)-1].Type != store.TypeStepReplaced {
		t.Fatalf("replaced subject history tail = %+v %v", oldHistory, err)
	}
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != source.Actions[0].Event.ID || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("replace self-echo was not inert", echo)
	}

	// The replacement-born node's first lifecycle event carries no subject
	// history at all: export anchors its baseline on nodes.birth_event and
	// recovers the parent across the subject boundary, so the start cascade
	// integrates with correct Parent/Baseline evidence.
	startSource := labStart(t, p, p.hybrid, "Hybrid", "shared/stage/step-03")
	labImportBound(t, p.fluke, startSource, p.repo, "integrated")
	labState(t, p.fluke, repID, store.InProgress)
	labState(t, p.fluke, stage, store.InProgress)
	labState(t, p.fluke, p.matter, store.InProgress)

	// And an amendment on the replacement-born node resolves the same way:
	// its baseline is the birth anchor, its parent the inherited one.
	removeSource := labExportAfter(t, p.hybrid, "Hybrid", repID, store.TypeStepRemoved,
		labVerbRemove(p.hybrid, p.repo, "shared/stage/step-03", "withdrawn"))
	labImportBound(t, p.fluke, removeSource, p.repo, "integrated")
	if _, err := p.fluke.Node(labCtx, repID); err == nil {
		t.Fatal("removed replacement is still live on the receiver")
	}
	labRebuild(t, p.fluke)
	labRebuild(t, p.hybrid)
}

func mustNode(t *testing.T, s *store.Store, id string) store.Node {
	t.Helper()
	n, err := s.Node(labCtx, id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// step.removed tombstones the Step; the identity stays spent and the event
// round-trips with its reason. The self-echo maps back to the native effect.
func TestSharedStoreStepRemoveBound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)

	source := labExportAfter(t, p.hybrid, "Hybrid", p.step2, store.TypeStepRemoved,
		labVerbRemove(p.hybrid, p.repo, "shared/step-02", "descoped"))
	var removed store.Removed
	if err := json.Unmarshal(source.Actions[0].Event.Payload, &removed); err != nil {
		t.Fatal(err)
	}
	if removed.Reason != "descoped" {
		t.Fatalf("removal reason = %q", removed.Reason)
	}
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 1 {
		t.Fatalf("remove effects = %v", d.Effects)
	}
	if _, err := p.fluke.Node(labCtx, p.step2); err == nil {
		t.Fatal("removed step is still live on the receiver")
	}
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stage children = %v, want %v", got, want)
	}
	// The tombstone is kind-agnostic at the fold but the verb is step-scoped:
	// the removed node's identity and history stay valid references.
	if n := mustNode(t, p.fluke, p.step1); n.Lifecycle != store.Planned {
		t.Fatalf("sibling lifecycle = %s", n.Lifecycle)
	}
	hybridBefore := len(labEvents(t, p.hybrid))
	echo := labImportBound(t, p.hybrid, source, p.repo, "integrated")
	if len(echo.Effects) != 1 || echo.Effects[0] != source.Actions[0].Event.ID || len(labEvents(t, p.hybrid)) != hybridBefore {
		t.Fatal("remove self-echo was not inert", echo)
	}
	labRebuild(t, p.fluke)
}

// The amendment family is admitted on the unbound path too — it mutates an
// already-paired object exactly like the lifecycle matrix, so bound and
// unbound classification agree.
func TestSharedStoreStepAmendmentUnbound(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)

	source := labExportAfter(t, p.hybrid, "Hybrid", stage, store.TypeStepReordered,
		labVerbReorder(p.hybrid, p.repo, "shared/stage", "shared/step-02", "shared/step-01"))
	labImport(t, p.fluke, source, "integrated")
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step2, p.step1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unbound stage children = %v, want %v", got, want)
	}

	removeSource := labExportAfter(t, p.hybrid, "Hybrid", p.step2, store.TypeStepRemoved,
		labVerbRemove(p.hybrid, p.repo, "shared/step-02", "unbound-descoped"))
	labImport(t, p.fluke, removeSource, "integrated")
	if _, err := p.fluke.Node(labCtx, p.step2); err == nil {
		t.Fatal("unbound removal left the step live")
	}
	labRebuild(t, p.fluke)
}

// InsertStep's exhausted-gap fallback commits [step.reordered, step.inserted]
// as one group — the reorder is the origin draft, the insert is caused by it.
// The portable unit carries both members atomically: the receiver folds the
// renumbering and the positioned birth together or not at all.
func TestSharedStoreStepInsertReorderGroup(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)
	var insertedID string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		insertedID = tx.NewID()
		return []store.Draft{
			{Type: store.TypeStepReordered, Subject: stage, Payload: store.Reordered{Order: []string{p.step2, p.step1}}},
			{Type: store.TypeStepInserted, Subject: insertedID, Payload: store.NodeBirth{Title: "Inserted", Locator: "step-03", Parent: stage, SortKey: 1500}},
		}, nil
	})
	d := labImportBound(t, p.fluke, source, p.repo, "integrated")
	if len(d.Effects) != 2 {
		t.Fatalf("group effects = %v", d.Effects)
	}
	// The reorder renumbered step2=1000, step1=2000; the insert's 1500 lands
	// between them — one atomic sibling order on the receiver.
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step2, insertedID, p.step1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stage children = %v, want %v", got, want)
	}
	n := mustNode(t, p.fluke, insertedID)
	if n.Parent != stage || n.Locator != "step-03" || n.Kind != store.ScaleStep || n.Lifecycle != store.Planned {
		t.Fatalf("inserted node = %+v", n)
	}
	labRebuild(t, p.fluke)
}

// Group atomicity under conflict: the reorder member classifies against
// candidate state and folds, then the inserted member hits an occupied
// locator — the savepoint rolls the whole unit back, so the receiver keeps
// its prior sibling order and never sees the inserted identity.
func TestSharedStoreStepAmendmentGroupConflict(t *testing.T) {
	p := newLabPair(t)
	stage := labStageID(t, p.hybrid, p.repo)
	// fluke independently births a Step directly under the Matter: it holds
	// locator step-03 in the Matter scope but is not a Stage sibling, so the
	// reorder member still sees its complete live set and integrates first.
	if _, err := writesurface.CreateStep(labCtx, p.fluke, store.ActorHuman, p.repo, "shared", "Fluke direct"); err != nil {
		t.Fatal(err)
	}
	var insertedID string
	source := labGroup(t, p.hybrid, "Hybrid", p.repo, func(tx *store.Tx) ([]store.Draft, error) {
		insertedID = tx.NewID()
		return []store.Draft{
			{Type: store.TypeStepReordered, Subject: stage, Payload: store.Reordered{Order: []string{p.step2, p.step1}}},
			{Type: store.TypeStepInserted, Subject: insertedID, Payload: store.NodeBirth{Title: "Synced", Locator: "step-03", Parent: stage, SortKey: 1500}},
		}, nil
	})
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, source, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("conflicting member produced effects: %+v", d)
	}
	if got, want := labChildIDs(t, p.fluke, stage), []string{p.step1, p.step2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rolled-back reorder changed stage children: %v", got)
	}
	if _, err := p.fluke.Node(labCtx, insertedID); err == nil {
		t.Fatal("conflicted group's inserted step exists")
	}
}

// An amendment that outruns its subject waits as pending — never wedges the
// import — and integrates on recheck once the missing evidence lands. Here a
// removal arrives before the Step's birth and stage exist locally at all.
func TestSharedStoreStepAmendmentPending(t *testing.T) {
	p := newLabPair(t)
	exportGroup := func(subject, kind string) store.ExperimentSource {
		t.Helper()
		source, err := p.hybrid.ExperimentExport(labCtx, "Hybrid", sourceGroup(t, p.hybrid, subject, kind))
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	stage2, err := writesurface.CreateStage(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared", "Late")
	if err != nil {
		t.Fatal(err)
	}
	step3, err := writesurface.CreateStep(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared/"+stage2.Locator, "Late step")
	if err != nil {
		t.Fatal(err)
	}
	if err := writesurface.RemoveStep(labCtx, p.hybrid, store.ActorHuman, p.repo, "shared/"+stage2.Locator+"/"+step3.Locator, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	stageSource := exportGroup(stage2.ID, store.TypeStageCreated)
	stepSource := exportGroup(step3.ID, store.TypeStepCreated)
	removeSource := exportGroup(step3.ID, store.TypeStepRemoved)

	// The removal's baseline is the Step's own birth — unknown locally — so
	// it pends rather than conflicts or wedges.
	d := labImportBound(t, p.fluke, removeSource, p.repo, "pending")
	if d.Reason != "unknown baseline dependency" {
		t.Fatalf("remove-before-birth reason = %q", d.Reason)
	}
	labImportBound(t, p.fluke, stageSource, p.repo, "integrated")
	labImportBound(t, p.fluke, stepSource, p.repo, "integrated")
	labState(t, p.fluke, step3.ID, store.Planned)
	followRecheck(t, p.fluke, removeSource, "integrated")
	if _, err := p.fluke.Node(labCtx, step3.ID); err == nil {
		t.Fatal("rechecked removal left the step live")
	}
	labRebuild(t, p.fluke)
}

// An occupied address is a retained conflict, never a merge: fluke's own
// step-03 holds the locator the replacement would claim, so the whole
// single-member group conflicts with zero effects and the old Step survives.
func TestSharedStoreStepReplaceConflict(t *testing.T) {
	p := newLabPair(t)
	if _, err := writesurface.CreateStep(labCtx, p.fluke, store.ActorHuman, p.repo, "shared/stage", "Local"); err != nil {
		t.Fatal(err)
	}
	source := labExportAfter(t, p.hybrid, "Hybrid", p.step1, store.TypeStepReplaced,
		labVerbReplace(p.hybrid, p.repo, "shared/step-01", "Replacement"))
	var replaced store.Replaced
	if err := json.Unmarshal(source.Actions[0].Event.Payload, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.Locator != "step-03" {
		t.Fatalf("replacement locator = %q, want the collided step-03", replaced.Locator)
	}
	before := len(labEvents(t, p.fluke))
	d := labImportBound(t, p.fluke, source, p.repo, "conflict")
	if len(d.Effects) != 0 || len(labEvents(t, p.fluke)) != before {
		t.Fatalf("locator collision produced effects: %+v", d)
	}
	labState(t, p.fluke, p.step1, store.Planned)
	if _, err := p.fluke.Node(labCtx, replaced.Replacement); err == nil {
		t.Fatal("conflicted replacement exists")
	}
	labRebuild(t, p.fluke)
}

// The widened whitelist is what ExchangeStatus selects automatically: the
// amendment groups upload as portable units and the receiver folds them
// against its own repo binding — reorder included, removal included.
func TestSharedStoreStepAmendmentWire(t *testing.T) {
	_, endpoint := newLabExchangeFeed(t)
	a, sa := exchangeHost(t)
	b, sb := exchangeHost(t)
	ca := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	cb := store.ExchangeClient{HTTP: &http.Client{Timeout: 5 * time.Second}}
	ba := exchangeBind(t, sa, a, endpoint)
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
	stage, err := writesurface.CreateStage(labCtx, sa, store.ActorHuman, a.repo, "wire", "Stage")
	if err != nil {
		t.Fatal(err)
	}
	step1, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "wire/stage", "One")
	if err != nil {
		t.Fatal(err)
	}
	step2, err := writesurface.CreateStep(labCtx, sa, store.ActorHuman, a.repo, "wire/stage", "Two")
	if err != nil {
		t.Fatal(err)
	}
	births := []struct {
		node store.Node
		kind string
	}{
		{matter, store.TypeMatterCreated},
		{stage, store.TypeStageCreated},
		{step1, store.TypeStepCreated},
		{step2, store.TypeStepCreated},
	}
	for _, b := range births {
		upload(sourceGroup(t, sa, b.node.ID, b.kind))
	}
	for _, n := range []store.Node{matter, stage, step1, step2} {
		if _, err := sb.Node(labCtx, n.ID); err != nil {
			t.Fatalf("birth %s did not arrive: %v", n.ID, err)
		}
	}

	if _, err := writesurface.ReorderStep(labCtx, sa, store.ActorHuman, a.repo, "wire/stage", []string{"wire/step-02", "wire/step-01"}); err != nil {
		t.Fatal(err)
	}
	reorderGroup := sourceGroup(t, sa, stage.ID, store.TypeStepReordered)
	upload(reorderGroup)
	sourceDecision(t, sb, reorderGroup, "integrated")
	if got, want := labChildIDs(t, sb, stage.ID), []string{step2.ID, step1.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("receiver stage children = %v, want %v", got, want)
	}

	if err := writesurface.RemoveStep(labCtx, sa, store.ActorHuman, a.repo, "wire/step-02", "wire-done"); err != nil {
		t.Fatal(err)
	}
	removeGroup := sourceGroup(t, sa, step2.ID, store.TypeStepRemoved)
	upload(removeGroup)
	sourceDecision(t, sb, removeGroup, "integrated")
	if _, err := sb.Node(labCtx, step2.ID); err == nil {
		t.Fatal("receiver kept the removed step live")
	}
	labRebuild(t, sb)
}
