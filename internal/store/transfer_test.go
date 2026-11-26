package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/transfer"
)

func exportForTest(t *testing.T, h *harness, selection transfer.Selection) *transfer.Snapshot {
	t.Helper()
	x, err := h.ExportTransfer(h.ctx, h.Repo, "source-lineage", selection)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func applyForTest(t *testing.T, h *harness, x *transfer.Snapshot) *TransferReceipt {
	t.Helper()
	p, err := h.PlanTransfer(h.ctx, x, h.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.ApplyTransfer(h.ctx, ActorHuman, x, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTransferRoundTripOverlapRebuild(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	other := dst.matter("unrelated", "Destination only")
	dst.write(other, KindBrief, []byte("private destination"))
	dst.setConfig("tracker.push-level", "off")
	m := src.matter("portable", "Portable Matter")
	stage := src.stage(m, "stage-01", "Stage")
	step := src.step(stage, "step-01", "Step")
	second := src.step(m, "step-02", "Second")
	src.depend(second, step)
	src.write(m, KindBrief, []byte("brief\x00\xff"))
	src.write(m, KindWorkplan, []byte("plan\n"))
	src.write(step, KindBody, []byte("body\n"))
	src.write(step, KindFindings, []byte("first\n"))
	spill := bytes.Repeat([]byte{0, 0xff, 42}, SpillThreshold/3+1)
	src.write(step, KindFindings, spill)
	src.write(step, KindFindings, []byte("last\n"))
	entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceFound, Title: "Planned intake", Detail: "original detail", OriginNode: step})
	src.commit(Draft{Type: TypeBacklogPlanned, Subject: entry, Payload: BacklogPlanned{Matter: m}})
	src.write(entry, KindFindings, []byte("backlog findings"))
	declined := src.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Declined"})
	src.commit(Draft{Type: TypeBacklogDeclined, Subject: declined, Payload: BacklogDeclined{Reason: "not needed"}})
	inventory, err := src.TransferInventory(src.ctx, src.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Backlog) != 2 {
		t.Fatalf("terminal inventory: %+v", inventory)
	}
	rootOnly := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	combined := exportForTest(t, src, transfer.Selection{Backlog: []string{entry, declined}})
	if len(combined.Selection.Matters) != 1 || combined.Selection.Matters[0] != m {
		t.Fatalf("planned result closure missing: %+v", combined.Selection)
	}
	before := dst.snapshotProjection()
	p, err := dst.PlanTransfer(dst.ctx, rootOnly, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after := dst.snapshotProjection(); !reflect.DeepEqual(before, after) {
		t.Fatal("preview changed projection")
	}
	// Round-tripped review JSON must work, not just an in-memory pointer.
	raw, _ := json.Marshal(p)
	var reviewed TransferPlan
	if err = json.Unmarshal(raw, &reviewed); err != nil {
		t.Fatal(err)
	}
	r, err := dst.ApplyTransfer(dst.ctx, ActorHuman, rootOnly, &reviewed)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := dst.PlanTransfer(dst.ctx, combined, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Reused[m] || p2.Mapping[m] != r.Mapping[m] || p2.Reused[entry] {
		t.Fatalf("overlap mapping wrong: %+v", p2)
	}
	r2, err := dst.ApplyTransfer(dst.ctx, ActorHuman, combined, p2)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range combined.Contents {
		got, err := dst.SegmentBytes(dst.ctx, r2.Mapping[c.ID])
		if err != nil || !bytes.Equal(got, c.Data) {
			t.Fatalf("content %s changed: %v", c.ID, err)
		}
	}
	want := append([]byte("first\n"), spill...)
	want = append(want, []byte("last\n")...)
	dst.wantContent("findings order", r.Mapping[step], KindFindings, want)
	dst.wantContent("unrelated", other, KindBrief, []byte("private destination"))
	planned, ok, err := dst.BacklogEntry(dst.ctx, dst.Repo, r2.Mapping[entry])
	if err != nil || !ok || planned.State != "planned" || planned.Matter != r.Mapping[m] || planned.OriginNode != r.Mapping[step] || planned.Detail != "original detail" {
		t.Fatalf("planned entry changed: %+v %v", planned, err)
	}
	decline, ok, err := dst.BacklogEntry(dst.ctx, dst.Repo, r2.Mapping[declined])
	if err != nil || !ok || decline.State != "declined" || decline.DeclineReason != "not needed" {
		t.Fatalf("declined entry changed: %+v %v", decline, err)
	}
	assertNoTransferAuthority(t, dst)
	state := dst.snapshotProjection()
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, dst.snapshotProjection()) {
		t.Fatal("rebuild differs")
	}
	assertNoTransferAuthority(t, dst)
	// Re-export retains origins despite native alias remapping.
	reexport := exportForTest(t, dst, transfer.Selection{Backlog: []string{r2.Mapping[entry]}})
	for _, n := range reexport.Nodes {
		if n.Origin.Repo != src.Repo || n.Origin.Lineage != "source-lineage" {
			t.Fatalf("lost origin: %+v", n)
		}
	}
	// Repeat receipts win even after target modifications/removal, with both
	// the original reviewed plan and a fresh no-op preview.
	dst.write(r.Mapping[step], KindFindings, []byte("local edit"))
	dst.commit(Draft{Type: TypeStepRemoved, Subject: r.Mapping[second]})
	prior := dst.snapshotProjection()
	retry, err := dst.ApplyTransfer(dst.ctx, ActorHuman, rootOnly, &reviewed)
	if err != nil || retry.Operation != r.Operation {
		t.Fatalf("lost-ack retry: %+v %v", retry, err)
	}
	noop, err := dst.PlanTransfer(dst.ctx, combined, dst.Repo, nil)
	if err != nil || !noop.AlreadyImported {
		t.Fatalf("receipt preview: %+v %v", noop, err)
	}
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, combined, noop); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prior, dst.snapshotProjection()) {
		t.Fatal("receipt retry resurrected or wrote")
	}
	// Changed source is not a new unrelated archive: origin conflict refuses it.
	src.write(step, KindFindings, []byte("new source revision"))
	changed := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	if _, err = dst.PlanTransfer(dst.ctx, changed, dst.Repo, nil); err == nil || !strings.Contains(err.Error(), "changed source origin") {
		t.Fatalf("changed source accepted: %v", err)
	}
}

func assertNoTransferAuthority(t *testing.T, h *harness) {
	t.Helper()
	for _, table := range []string{"outbox_entries", "tracker_references", "tracker_push_records", "runs", "dispatches", "roles", "batches", "batch_members"} {
		h.wantRowCount("no import authority", table, "1=1", nil, 0)
	}
}

func TestTransferCombinedThenSeparate(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("one", "One")
	entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Intake"})
	src.commit(Draft{Type: TypeBacklogPlanned, Subject: entry, Payload: BacklogPlanned{Matter: m}})
	combined := exportForTest(t, src, transfer.Selection{Backlog: []string{entry}})
	r := applyForTest(t, dst, combined)
	separate := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	p, err := dst.PlanTransfer(dst.ctx, separate, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Reused[m] || p.Mapping[m] != r.Mapping[m] {
		t.Fatal("separate overlap duplicated Matter")
	}
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, separate, p); err != nil {
		t.Fatal(err)
	}
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTransferPreconditionsAndUnsupported(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("same", "Source")
	step := src.step(m, "step-01", "Step")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	if _, err := src.PlanTransfer(src.ctx, x, src.Repo, map[string]string{m: "duplicate"}); err == nil || !strings.Contains(err.Error(), "self-import") {
		t.Fatalf("self import accepted: %v", err)
	}
	dst.matter("same", "Existing")
	if _, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil); err == nil {
		t.Fatal("locator conflict accepted")
	}
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, map[string]string{m: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	dst.declareGate("verified", ScaleStep)
	before := dst.snapshotProjection()
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err == nil {
		t.Fatal("new target gates not revalidated")
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("failed apply wrote projection")
	}
	if _, err = src.ExportTransfer(src.ctx, src.Repo, "lineage", transfer.Selection{Matters: []string{step}}); err == nil {
		t.Fatal("nonroot selection accepted")
	}
	other := src.matter("other", "Other")
	src.depend(step, other)
	if _, err = src.ExportTransfer(src.ctx, src.Repo, "lineage", transfer.Selection{Matters: []string{m}}); err == nil || !strings.Contains(err.Error(), "crosses") {
		t.Fatalf("cross-root dependency accepted: %v", err)
	}
	src.declareGate("verified", ScaleStep)
	if _, err = src.ExportTransfer(src.ctx, src.Repo, "lineage", transfer.Selection{Matters: []string{m, other}}); err == nil || !strings.Contains(err.Error(), "gate") {
		t.Fatalf("gated export accepted: %v", err)
	}
}

func TestTransferBlobNoOverwriteAndAtomicFailure(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("blob", "Blob")
	src.write(m, KindBrief, bytes.Repeat([]byte("x"), SpillThreshold+1))
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dst.blobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dst.blobDir, p.Mapping[x.Contents[0].ID])
	if err = os.WriteFile(path, []byte("existing orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dst.snapshotProjection()
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err == nil {
		t.Fatal("existing blob overwritten")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "existing orphan" {
		t.Fatal("existing blob changed")
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("blob failure committed work/map/receipt")
	}
}

func TestTransferPairingFencedWithoutNetwork(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("one", "One")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	// Pairing is local retained metadata. These public store methods never
	// perform HTTP, including with this deliberately unreachable endpoint.
	if _, err := dst.PairExchangeConnection(dst.ctx, dst.Repo, "https://invalid.test/destination", "connection", "account"); err != nil {
		t.Fatal(err)
	}
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Pairing.Active || p.Pairing.Connection != "connection" || p.Pairing.Generation == "" {
		t.Fatalf("pairing missing from review: %+v", p.Pairing)
	}
	if err = dst.UnpairExchange(dst.ctx, dst.Repo); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err == nil {
		t.Fatal("pairing changed without fencing plan")
	}
	p, err = dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err != nil {
		t.Fatal(err)
	}
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	bindings, err := dst.ExchangeBindings(dst.ctx)
	if err != nil || len(bindings) != 1 || bindings[0].Active {
		t.Fatalf("import resumed pairing: %+v %v", bindings, err)
	}
}

func TestTransferCannotRemapOriginToAnotherRepo(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("one", "One")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	other := dst.attachRepo(RepoAttached{Label: "other"})
	stale, err := dst.PlanTransfer(dst.ctx, x, other, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyForTest(t, dst, x)
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err = dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		before := dst.snapshotProjection()
		if _, err = dst.PlanTransfer(dst.ctx, x, other, nil); err == nil || !strings.Contains(err.Error(), "already maps to Repo") {
			t.Fatalf("cross-Repo origin remap accepted: %v", err)
		}
		if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, stale); err == nil || !strings.Contains(err.Error(), "already maps to Repo") {
			t.Fatalf("stale cross-Repo apply accepted: %v", err)
		}
		if !reflect.DeepEqual(before, dst.snapshotProjection()) {
			t.Fatal("cross-Repo refusal wrote")
		}
	}
}

func TestTransferRepoExistenceAndExclusions(t *testing.T) {
	h := newHarness(t)
	missing := h.NewID()
	if _, err := h.TransferInventory(h.ctx, missing); err == nil {
		t.Fatal("missing inventory Repo accepted")
	}
	if _, err := h.ExportTransfer(h.ctx, missing, "lineage", transfer.Selection{Matters: []string{missing}}); err == nil {
		t.Fatal("missing export Repo accepted")
	}
	m := h.matter("one", "One")
	x := exportForTest(t, h, transfer.Selection{Matters: []string{m}})
	for _, category := range []string{"configuration", "paths", "bindings", "credentials", "execution", "delivery", "historical"} {
		if !strings.Contains(strings.Join(x.Exclusions, "\n"), category) {
			t.Fatalf("missing exclusion %s: %+v", category, x.Exclusions)
		}
	}
}

func TestTransferLocalReferenceAndBatchChangesFenceOverlap(t *testing.T) {
	for _, change := range []string{"reference", "batch", "content"} {
		t.Run(change, func(t *testing.T) {
			src, dst := newHarness(t), newHarness(t)
			m := src.matter("one", "One")
			entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Intake"})
			src.commit(Draft{Type: TypeBacklogPlanned, Subject: entry, Payload: BacklogPlanned{Matter: m}})
			x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
			r := applyForTest(t, dst, x)
			overlap := exportForTest(t, src, transfer.Selection{Backlog: []string{entry}})
			p, err := dst.PlanTransfer(dst.ctx, overlap, dst.Repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "reference":
				dst.commit(Draft{Type: TypeReferenceAdded, Subject: r.Mapping[m], Payload: ReferenceAdded{Ref: "LOCAL-1"}})
			case "batch":
				dst.newAnonymousBatch(r.Mapping[m])
			case "content":
				dst.write(r.Mapping[m], KindFindings, []byte("local"))
			}
			if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, overlap, p); err == nil || !strings.Contains(err.Error(), "local edits") {
				t.Fatalf("%s edit did not fence apply: %v", change, err)
			}
			if _, err = dst.PlanTransfer(dst.ctx, overlap, dst.Repo, nil); err == nil {
				t.Fatalf("%s edit did not fence preview", change)
			}
			// The already-committed exact archive still returns its receipt.
			noop, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
			if err != nil || !noop.AlreadyImported {
				t.Fatalf("exact receipt lost: %+v %v", noop, err)
			}
		})
	}
}

func TestTransferLifecyclesCancellationAndOriginClosure(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	var roots []string
	for _, state := range []string{"planned", "in-progress", "paused", "done", "canceled"} {
		m := src.matter(state, state)
		roots = append(roots, m)
		switch state {
		case "in-progress":
			src.start(m)
		case "paused":
			src.start(m)
			src.pause(m)
		case "done":
			src.start(m)
			src.finish(m)
		case "canceled":
			src.commit(Draft{Type: TypeMatterCanceled, Subject: m, Payload: Transition{From: Planned, To: Canceled, Reason: "preserve this reason"}})
		}
	}
	entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceFound, Title: "Standalone found", OriginNode: roots[1]})
	closure := exportForTest(t, src, transfer.Selection{Backlog: []string{entry}})
	if len(closure.Nodes) != 1 || len(closure.Selection.Matters) != 1 || closure.Nodes[0].ID != roots[1] {
		t.Fatal("origin owner not explicitly closed")
	}
	x := exportForTest(t, src, transfer.Selection{Matters: roots, Backlog: []string{entry}})
	r := applyForTest(t, dst, x)
	for _, n := range x.Nodes {
		got, err := dst.Node(dst.ctx, r.Mapping[n.ID])
		if err != nil || string(got.Lifecycle) != n.Lifecycle {
			t.Fatalf("lifecycle %s: %+v %v", n.Lifecycle, got, err)
		}
	}
	assertNoTransferAuthority(t, dst)
	if err := dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	var mapped []string
	for _, id := range roots {
		mapped = append(mapped, r.Mapping[id])
	}
	reexport := exportForTest(t, dst, transfer.Selection{Matters: mapped})
	for _, n := range reexport.Nodes {
		if n.Lifecycle == "canceled" && n.Reason != "preserve this reason" {
			t.Fatalf("cancellation reason lost: %+v", n)
		}
	}
}

func TestTransferV14ScratchPlanSurvivesAdditiveMigration(t *testing.T) {
	src := newHarness(t)
	m := src.matter("one", "One")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	path := filepath.Join(t.TempDir(), "target.db")
	old := newHarnessAt(t, path, register, 14)
	repo := old.Repo
	old.matter("existing", "Unrelated existing work")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scratchPath := filepath.Join(t.TempDir(), "scratch.db")
	if err = os.WriteFile(scratchPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	scratch, err := Open(scratchPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scratch.Close() }()
	p, err := scratch.PlanTransfer(src.ctx, x, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = actual.Close() }()
	// An unrelated native event is not a stale affected-object precondition.
	old.wrap(actual).matter("later", "Created after preview")
	if _, err = actual.ApplyTransfer(src.ctx, ActorHuman, x, p); err != nil {
		t.Fatal(err)
	}
}

func TestTransferSourceUnsupportedFamilies(t *testing.T) {
	for _, family := range []string{"reference", "removal", "replacement", "batch", "delegated"} {
		t.Run(family, func(t *testing.T) {
			h := newHarness(t)
			m := h.matter("one", "One")
			step := h.step(m, "step-01", "Step")
			selection := transfer.Selection{Matters: []string{m}}
			switch family {
			case "reference":
				h.commit(Draft{Type: TypeReferenceAdded, Subject: m, Payload: ReferenceAdded{Ref: "TRACKER-1"}})
			case "removal":
				h.remove(step, "removed")
			case "replacement":
				h.replace(step, "step-02", "Replacement")
			case "batch":
				h.newAnonymousBatch(m)
			case "delegated":
				entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Delegate"})
				h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{Outbox: h.NewID(), IdempotencyKey: "delegate:" + entry}})
				selection = transfer.Selection{Backlog: []string{entry}}
			}
			before := h.snapshotProjection()
			if _, err := h.ExportTransfer(h.ctx, h.Repo, "lineage", selection); err == nil {
				t.Fatalf("%s export was not refused", family)
			}
			inventory, err := h.TransferInventory(h.ctx, h.Repo)
			if err != nil {
				t.Fatal(err)
			}
			if len(inventory.Notes) == 0 {
				t.Fatalf("%s has no unsupported inventory diagnostic", family)
			}
			if !reflect.DeepEqual(before, h.snapshotProjection()) {
				t.Fatal("unsupported export/inventory wrote")
			}
		})
	}
}

func TestTransferReexportBackToNativeOriginRefused(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("one", "One")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	r := applyForTest(t, dst, x)
	reexport := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[m]}})
	if _, err := src.PlanTransfer(src.ctx, reexport, src.Repo, nil); err == nil || !strings.Contains(err.Error(), "self-import") {
		t.Fatalf("re-export duplicated native origin: %v", err)
	}
}

func TestTransferPreviewClockRollbackAppendRebuild(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("clock", "Clock")
	src.write(m, KindFindings, []byte("old\n"))
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	future := time.Now().Add(time.Hour)
	dst.ids = newIDSourceWithClock(func() time.Time { return future })
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := dst.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reopened.ApplyTransfer(reopened.ctx, ActorHuman, x, p)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err = reopened.db.QueryRowContext(reopened.ctx, `SELECT id FROM events WHERE subject=? AND type=?`, r.Operation, TypeWorkImported).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	ids := []string{p.Operation}
	for _, id := range p.Mapping {
		ids = append(ids, id)
	}
	for _, id := range ids {
		if eventID <= id {
			t.Fatalf("import event %s did not advance past reviewed ID %s", eventID, id)
		}
	}
	// Reopen again: the import event must also durably carry the raised floor.
	reopened, err = reopened.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatal(err)
	}
	reopened.write(r.Mapping[m], KindFindings, []byte("new\n"))
	reopened.wantContent("append remains chronological", r.Mapping[m], KindFindings, []byte("old\nnew\n"))
	if err = reopened.Rebuild(reopened.ctx); err != nil {
		t.Fatal(err)
	}
	reopened.wantContent("append after rebuild", r.Mapping[m], KindFindings, []byte("old\nnew\n"))
}

func TestTransferRenameDescendantCollision(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("root", "Root")
	stage := src.stage(m, "stage", "Stage")
	src.step(stage, "child", "Child")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	for _, name := range []string{"stage", "child"} {
		if _, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, map[string]string{m: name}); err == nil || !strings.Contains(err.Error(), "conflicts with descendant") {
			t.Fatalf("rename to descendant %q accepted: %v", name, err)
		}
	}
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, map[string]string{m: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	// An altered reviewed plan cannot bypass the shared revalidation check.
	for _, name := range []string{"stage", "child"} {
		p.Renames[m] = name
		if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err == nil || !strings.Contains(err.Error(), "conflicts with descendant") {
			t.Fatalf("apply accepted descendant %q: %v", name, err)
		}
	}
	p.Renames[m] = "safe"
	r, err := dst.ApplyTransfer(dst.ctx, ActorHuman, x, p)
	if err != nil {
		t.Fatal(err)
	}
	n, err := dst.Node(dst.ctx, r.Mapping[m])
	if err != nil || n.Locator != "safe" {
		t.Fatalf("safe rename: %+v %v", n, err)
	}
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTransferEffectiveNodeOrder(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("ordered", "Ordered")
	stage := src.stage(m, "stage", "Stage")
	low, high := src.NewID(), src.NewID()
	// Birth order deliberately opposes identity order at the same sort key.
	src.commit(Draft{Type: TypeStepCreated, Subject: high, Payload: NodeBirth{Parent: stage, Locator: "first", Title: "First", SortKey: 7}})
	src.commit(Draft{Type: TypeStepCreated, Subject: low, Payload: NodeBirth{Parent: stage, Locator: "second", Title: "Second", SortKey: 7}})
	src.commit(Draft{Type: TypeStepCreated, Subject: src.NewID(), Payload: NodeBirth{Parent: m, Locator: "middle", Title: "Middle", SortKey: 18}})
	src.commit(Draft{Type: TypeStepCreated, Subject: src.NewID(), Payload: NodeBirth{Parent: stage, Locator: "last", Title: "Last", SortKey: 29}})
	untied := src.matter("untied", "Untied")
	src.commit(Draft{Type: TypeStepCreated, Subject: src.NewID(), Payload: NodeBirth{Parent: untied, Locator: "distinct", Title: "Distinct", SortKey: 83}})
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m, untied}})
	want, err := src.MatterNodes(src.ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]int64{}
	for _, n := range x.Nodes {
		keys[n.ID] = n.SortKey
	}
	for i, n := range want {
		if keys[n.ID] != int64(i) {
			t.Fatalf("tied Matter not ranked in effective order: %+v", x.Nodes)
		}
	}
	unique, err := src.MatterNodes(src.ctx, untied)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range unique {
		if keys[n.ID] != n.SortKey {
			t.Fatalf("distinct sort key changed: %+v", n)
		}
	}
	// The archive canonicalizes node slices by ID. Exercise that boundary so
	// birth-ordered input cannot accidentally preserve tied keys on import.
	archive := filepath.Join(t.TempDir(), "ordered.zip")
	if err = transfer.Write(archive, x); err != nil {
		t.Fatal(err)
	}
	x, err = transfer.Read(archive)
	if err != nil {
		t.Fatal(err)
	}
	r := applyForTest(t, dst, x)
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err = dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		got, err := dst.MatterNodes(dst.ctx, r.Mapping[m])
		if err != nil || len(got) != len(want) {
			t.Fatalf("MatterNodes: %+v %v", got, err)
		}
		for i := range want {
			if got[i].ID != r.Mapping[want[i].ID] {
				t.Fatalf("MatterNodes order changed: got %+v, want %+v", got, want)
			}
		}
		for _, parent := range []string{m, stage} {
			before, err := src.Children(src.ctx, parent)
			if err != nil {
				t.Fatal(err)
			}
			after, err := dst.Children(dst.ctx, r.Mapping[parent])
			if err != nil || len(after) != len(before) {
				t.Fatalf("Children: %+v %v", after, err)
			}
			for i := range before {
				if after[i].ID != r.Mapping[before[i].ID] {
					t.Fatalf("Children order changed: got %+v, want %+v", after, before)
				}
			}
		}
		reexport := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[m], r.Mapping[untied]}})
		for _, n := range reexport.Nodes {
			if n.SortKey != keys[n.Origin.ID] {
				t.Fatalf("normalized key not stable on re-export: %+v", n)
			}
		}
		if p, err := dst.PlanTransfer(dst.ctx, reexport, dst.Repo, nil); err != nil || len(p.Reused) != len(x.Nodes) {
			t.Fatalf("unchanged re-export origins rejected: %+v %v", p, err)
		}
	}
}

func TestTransferNoncanonicalContentOrdersReexport(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("orders", "Orders")
	src.write(m, KindBrief, []byte("brief"))
	src.write(m, KindFindings, []byte("first\n"))
	src.write(m, KindFindings, []byte("second\n"))
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	for i := range x.Contents {
		x.Contents[i].Order = 17 + 9*x.Contents[i].Order
	}
	r := applyForTest(t, dst, x)
	dst.wantContent("relative findings order", r.Mapping[m], KindFindings, []byte("first\nsecond\n"))
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		reexport := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[m]}})
		p, err := dst.PlanTransfer(dst.ctx, reexport, dst.Repo, nil)
		if err != nil {
			t.Fatalf("nonsemantic content orders changed origin: %v", err)
		}
		if len(p.Reused) != len(x.Nodes)+len(x.Contents) {
			t.Fatalf("unchanged origins not reused: %+v", p)
		}
		if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, reexport, p); err != nil {
			t.Fatal(err)
		}
		dst.wantContent("unchanged after re-import", r.Mapping[m], KindFindings, []byte("first\nsecond\n"))
	}
}

func TestTransferApplyActor(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("actor", "Actor")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Actor{"", RoleActor("Builder")} {
		before := dst.snapshotProjection()
		if _, err = dst.ApplyTransfer(dst.ctx, actor, x, p); err == nil {
			t.Fatalf("unverified actor %q accepted", actor)
		}
		if !reflect.DeepEqual(before, dst.snapshotProjection()) {
			t.Fatal("rejected actor wrote projection")
		}
	}
	actor := SystemActor("transfer-test")
	r, err := dst.ApplyTransfer(dst.ctx, actor, x, p)
	if err != nil {
		t.Fatal(err)
	}
	var got Actor
	if err = dst.db.QueryRowContext(dst.ctx, `SELECT actor FROM events WHERE subject=? AND type=?`, r.Operation, TypeWorkImported).Scan(&got); err != nil || got != actor {
		t.Fatalf("caller actor not retained: %q %v", got, err)
	}
}

func TestTransferIncomingTiesReexport(t *testing.T) {
	src, dst, third := newHarness(t), newHarness(t), newHarness(t)
	m := src.matter("ties", "Ties")
	low, high := src.NewID(), src.NewID()
	src.commit(Draft{Type: TypeStepCreated, Subject: high, Payload: NodeBirth{Parent: m, Locator: "high", Title: "High"}})
	src.commit(Draft{Type: TypeStepCreated, Subject: low, Payload: NodeBirth{Parent: m, Locator: "low", Title: "Low"}})
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	// Reverse the archive aliases relative to durable origins. Incoming ties
	// have no native birth order, so only the origins provide a stable tie-break.
	aliases := []string{src.NewID(), src.NewID(), src.NewID()}
	ids := map[string]string{m: aliases[2], low: aliases[1], high: aliases[0]}
	for i := range x.Nodes {
		n := &x.Nodes[i]
		n.ID, n.Matter, n.Parent = ids[n.ID], ids[n.Matter], ids[n.Parent]
		n.SortKey = 7
	}
	x.Selection.Matters = []string{ids[m]}
	archive := filepath.Join(t.TempDir(), "tied.zip")
	if err := transfer.Write(archive, x); err != nil {
		t.Fatal(err)
	}
	x, err := transfer.Read(archive)
	if err != nil {
		t.Fatal(err)
	}
	applyForTest(t, third, x)
	r := applyForTest(t, dst, x)
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		children, err := dst.Children(dst.ctx, r.Mapping[ids[m]])
		if err != nil || len(children) != 2 || children[0].ID != r.Mapping[ids[low]] || children[1].ID != r.Mapping[ids[high]] {
			t.Fatalf("incoming ties did not use durable origin order: %+v %v", children, err)
		}
		reexport := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[ids[m]]}})
		for _, target := range []*harness{dst, third} {
			p, err := target.PlanTransfer(target.ctx, reexport, target.Repo, nil)
			if err != nil || len(p.Reused) != len(x.Nodes) {
				t.Fatalf("unchanged tied-key origins rejected: %+v %v", p, err)
			}
			if _, err = target.ApplyTransfer(target.ctx, ActorHuman, reexport, p); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTransferImportAliasReexport(t *testing.T) {
	src, dst, third := newHarness(t), newHarness(t), newHarness(t)
	m := src.matter("root", "Root")
	src.step(m, "child", "Child")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	applyForTest(t, third, x)
	dst.matter("root", "Unrelated destination work")
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, map[string]string{m: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := dst.ApplyTransfer(dst.ctx, ActorHuman, x, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		n, err := dst.Node(dst.ctx, r.Mapping[m])
		if err != nil || n.Locator != "safe" {
			t.Fatalf("local alias lost: %+v %v", n, err)
		}
		inventory, err := dst.TransferInventory(dst.ctx, dst.Repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range inventory.Matters {
			if n.ID == r.Mapping[m] && n.Locator != "safe" {
				t.Fatalf("inventory hid local alias: %+v", n)
			}
		}
		reexport := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[m]}})
		for _, n := range reexport.Nodes {
			if n.Kind == "matter" && n.Locator != "root" {
				t.Fatalf("local alias escaped as upstream metadata: %+v", n)
			}
		}
		for _, target := range []*harness{dst, third} {
			p, err := target.PlanTransfer(target.ctx, reexport, target.Repo, nil)
			if err != nil || len(p.Reused) != len(x.Nodes) {
				t.Fatalf("unchanged aliased origins rejected: %+v %v", p, err)
			}
			if _, err = target.ApplyTransfer(target.ctx, ActorHuman, reexport, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Genuine source locator changes must still conflict, not be omitted from
	// fingerprints merely to make import-time aliases round-trip.
	for i := range x.Nodes {
		if x.Nodes[i].Kind == "matter" {
			x.Nodes[i].Locator = "source-edited"
		}
	}
	if _, err := third.PlanTransfer(third.ctx, x, third.Repo, nil); err == nil || !strings.Contains(err.Error(), "changed source origin") {
		t.Fatalf("real source locator change was hidden: %v", err)
	}
}

func TestTransferImportAliasDescendantCollision(t *testing.T) {
	src, dst, older := newHarness(t), newHarness(t), newHarness(t)
	m := src.matter("root", "Root")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	applyForTest(t, older, x)
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, map[string]string{m: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := dst.ApplyTransfer(dst.ctx, ActorHuman, x, p)
	if err != nil {
		t.Fatal(err)
	}
	stage := dst.stage(r.Mapping[m], "section", "Section")
	child := dst.step(stage, "root", "New local child")
	for _, rebuild := range []bool{false, true} {
		if rebuild {
			if err := dst.Rebuild(dst.ctx); err != nil {
				t.Fatal(err)
			}
		}
		exported := exportForTest(t, dst, transfer.Selection{Matters: []string{r.Mapping[m]}})
		archive := filepath.Join(t.TempDir(), "edited.zip")
		if err := transfer.Write(archive, exported); err != nil {
			t.Fatal(err)
		}
		exported, err = transfer.Read(archive)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range exported.Nodes {
			if n.Kind == "matter" && (n.Locator != "safe" || n.Origin != x.Nodes[0].Origin) {
				t.Fatalf("collision fallback lost locator or origin: %+v", n)
			}
		}
		if _, err := older.PlanTransfer(older.ctx, exported, older.Repo, nil); err == nil || !strings.Contains(err.Error(), "changed source origin") {
			t.Fatalf("changed closure was not refused by older recipient: %v", err)
		}
		fresh := newHarness(t)
		imported := applyForTest(t, fresh, exported)
		if err := fresh.Rebuild(fresh.ctx); err != nil {
			t.Fatal(err)
		}
		for id, locator := range map[string]string{r.Mapping[m]: "safe", stage: "section", child: "root"} {
			n, err := fresh.Node(fresh.ctx, imported.Mapping[id])
			if err != nil || n.Locator != locator {
				t.Fatalf("fresh import lost node %s: %+v %v", id, n, err)
			}
		}
		n, err := fresh.Node(fresh.ctx, imported.Mapping[child])
		if err != nil || n.Parent != imported.Mapping[stage] || n.Matter != imported.Mapping[r.Mapping[m]] {
			t.Fatalf("fresh import changed nested ownership: %+v %v", n, err)
		}
	}
}
