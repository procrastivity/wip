package store

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/transfer"
)

func snapshotEventForTest(t *testing.T, h *harness, operation string) Event {
	t.Helper()
	events, err := h.Events(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == TypeWorkSnapshot && ev.Subject == operation {
			return ev
		}
	}
	t.Fatal("missing public snapshot root")
	return Event{}
}

func TestTransferSnapshotPublicPrivateAndRemoteRebuild(t *testing.T) {
	src, local, remote := newHarness(t), newHarness(t), newHarness(t)
	m := src.matter("original", "Upstream title")
	child := src.step(m, "child", "Canceled step")
	src.commit(Draft{Type: TypeStepCanceled, Subject: child, Payload: Transition{From: Planned, To: Canceled, Reason: "upstream cancellation"}})
	src.write(m, KindBrief, []byte("portable bytes"))
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	plan, err := local.PlanTransfer(local.ctx, x, local.Repo, map[string]string{m: "effective"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := local.ApplyTransfer(local.ctx, ActorHuman, x, plan)
	if err != nil {
		t.Fatal(err)
	}
	root := snapshotEventForTest(t, local, r.Operation)
	p, err := decodeWorkSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 1 || p.Snapshot.Repo != local.Repo || len(p.References) != 0 {
		t.Fatalf("wrong root metadata: %+v", p)
	}
	for _, n := range p.Snapshot.Nodes {
		if n.ID == r.Mapping[m] && (n.Locator != "effective" || n.Matter != n.ID) {
			t.Fatalf("not effective native metadata: %+v", n)
		}
		if n.Origin.Repo != src.Repo {
			t.Fatal("upstream provenance lost")
		}
	}
	for _, key := range []string{`"plan"`, `"pairing"`, `"account"`, `"receipt"`, `"preconditions"`} {
		if bytes.Contains(root.Payload, []byte(key)) {
			t.Fatalf("private key escaped root: %s", key)
		}
	}
	events, err := local.Events(local.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var private Event
	for _, ev := range events {
		if ev.Type == TypeWorkImported && ev.Subject == r.Operation {
			private = ev
		}
	}
	var companion transferImported
	if err = decodeStrict(private, &companion); err != nil {
		t.Fatal(err)
	}
	if !companion.Root || private.Causation != root.ID || private.Correlation != root.ID || companion.Plan == nil {
		t.Fatal("private companion lacks real root link")
	}
	// Receiver changes only the event envelope Repo. The public payload remains
	// byte-for-byte immutable and is independently sufficient for projection.
	remoteEvents, err := remote.Commit(remote.ctx, Request{Actor: ActorHuman, Env: Env{Repo: remote.Repo}}, func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeWorkSnapshot, Subject: root.Subject, Payload: json.RawMessage(root.Payload)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(root.Payload, remoteEvents[0].Payload) {
		t.Fatal("receiver rewrote root")
	}
	remote.wantRowCount("remote has no receipt", "transfer_receipts", "1=1", nil, 0)
	remote.wantRowCount("remote has no private metadata", "transfer_snapshot_imports", "1=1", nil, 0)
	remote.wantRowCount("remote retains public origins", "transfer_origins", "1=1", nil, len(p.Snapshot.Nodes)+len(p.Snapshot.Contents))
	for _, h := range []*harness{local, remote} {
		before := h.snapshotProjection()
		if err = h.Rebuild(h.ctx); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, h.snapshotProjection()) {
			t.Fatal("snapshot rebuild changed projection")
		}
		reexport := exportForTest(t, h, transfer.Selection{Matters: []string{r.Mapping[m]}})
		for _, n := range reexport.Nodes {
			if n.ID == r.Mapping[child] && n.Reason != "upstream cancellation" {
				t.Fatal("reexport lost cancellation reason")
			}
		}
		assertNoTransferAuthority(t, h)
	}
}

func TestTransferSnapshotReferencesAndStrictShapes(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("root", "Root")
	src.write(m, KindBrief, []byte("unchanged content"))
	entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Entry"})
	r := applyForTest(t, dst, exportForTest(t, src, transfer.Selection{Matters: []string{m}}))
	first := snapshotEventForTest(t, dst, r.Operation)
	combined := exportForTest(t, src, transfer.Selection{Matters: []string{m}, Backlog: []string{entry}})
	r2 := applyForTest(t, dst, combined)
	root := snapshotEventForTest(t, dst, r2.Operation)
	p, err := decodeWorkSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.References[r.Mapping[m]] != first.ID || len(p.References) != 2 {
		t.Fatalf("reused births were not references: %+v", p.References)
	}
	var birth string
	if err = dst.db.QueryRow(`SELECT birth_event FROM nodes WHERE id=?`, r.Mapping[m]).Scan(&birth); err != nil || birth != first.ID {
		t.Fatalf("reused object reborn: %s %v", birth, err)
	}
	for name, mutate := range map[string]func(*workSnapshot){
		"version":         func(p *workSnapshot) { p.Version = 2 },
		"extra reference": func(p *workSnapshot) { p.References[dst.NewID()] = first.ID },
		"bad anchor":      func(p *workSnapshot) { p.References[r.Mapping[m]] = "not-an-id" },
		"extra content":   func(p *workSnapshot) { p.Contents[dst.NewID()] = ContentWritten{} },
		"wrong bytes": func(p *workSnapshot) {
			for id, w := range p.Contents {
				w.Bytes = []byte("wrong")
				p.Contents[id] = w
				break
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := decodeWorkSnapshot(root)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			raw, _ := json.Marshal(candidate)
			ev := root
			ev.Payload = raw
			if _, err = decodeWorkSnapshot(ev); err == nil {
				t.Fatal("malformed public root accepted")
			}
		})
	}
	for _, raw := range [][]byte{
		bytes.Replace(root.Payload, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(root.Payload, []byte(`"version":1`), []byte(`"Version":1`), 1),
		bytes.Replace(root.Payload, []byte(`"version":1`), []byte(`"version":1,"plan":{}`), 1),
	} {
		ev := root
		ev.Payload = raw
		if _, err := decodeWorkSnapshot(ev); err == nil {
			t.Fatal("open or ambiguous public schema accepted")
		}
	}
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	// Fold admission must reject edited reused members before attempting any
	// births. Savepoint probes and actual application share this guard.
	dst.write(r.Mapping[m], KindFindings, []byte("independent edit"))
	before := dst.snapshotProjection()
	_, err = dst.Commit(dst.ctx, Request{Actor: ActorHuman, Env: Env{Repo: dst.Repo}}, func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeWorkSnapshot, Subject: dst.NewID(), Payload: p}}, nil
	})
	if err == nil {
		t.Fatal("edited reference accepted")
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("failed root was not atomic")
	}
}

func TestTransferSnapshotLegacyImportedFold(t *testing.T) {
	src := newHarness(t)
	dst := newHarnessAt(t, filepath.Join(t.TempDir(), "v15.db"), register, 15)
	m := src.matter("legacy", "Legacy")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := transferImported{Snapshot: x, Plan: p, Contents: map[string]ContentWritten{}}
	events, err := dst.Commit(dst.ctx, Request{Actor: ActorHuman, Env: Env{Repo: dst.Repo}}, func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeWorkImported, Subject: p.Operation, Payload: legacy}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(events[0].Payload, []byte(`"root"`)) {
		t.Fatal("legacy encoding changed")
	}
	before := dst.snapshotProjection()
	log := dst.rowsOf("events", "")
	dst, err = dst.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatal("upgrade private-only v15 import", err)
	}
	dst.wantSameProjection("v15 import survives v16 migration", before, dst.snapshotProjection())
	wantSameRows(t, "v15 import history is not rewritten", log, dst.rowsOf("events", ""))
	before = dst.snapshotProjection()
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("legacy rebuild changed")
	}
	receipt, err := transferReceipt(dst.ctx, dst.db, dst.Repo, p.Digest)
	if err != nil || receipt == nil || receipt.Mapping[m] != p.Mapping[m] {
		t.Fatalf("legacy receipt lost: %+v %v", receipt, err)
	}
}

func TestTransferSnapshotCompanionFailureRollsBackRoot(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("atomic", "Atomic")
	x := exportForTest(t, src, transfer.Selection{Matters: []string{m}})
	p, err := dst.PlanTransfer(dst.ctx, x, dst.Repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dst.db.Exec(`CREATE TRIGGER fail_private_import BEFORE INSERT ON events WHEN NEW.type='work.imported' BEGIN SELECT RAISE(ABORT,'injected companion failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := dst.snapshotProjection()
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err == nil {
		t.Fatal("companion failure ignored")
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("private failure left root projections")
	}
	dst.wantRowCount("no public event after private failure", "events", "type=?", []any{TypeWorkSnapshot}, 0)
	dst.wantRowCount("no public origins after private failure", "transfer_origins", "1=1", nil, 0)
	dst.wantRowCount("no private metadata after private failure", "transfer_snapshot_imports", "1=1", nil, 0)
	if _, err = dst.db.Exec(`DROP TRIGGER fail_private_import`); err != nil {
		t.Fatal(err)
	}
	if _, err = dst.ApplyTransfer(dst.ctx, ActorHuman, x, p); err != nil {
		t.Fatal(err)
	}
}

func TestTransferSnapshotReferenceAnchorAliasStableOnRebuild(t *testing.T) {
	src, dst := newHarness(t), newHarness(t)
	m := src.matter("aliases", "Aliases")
	r := applyForTest(t, dst, exportForTest(t, src, transfer.Selection{Matters: []string{m}}))
	first := snapshotEventForTest(t, dst, r.Operation)
	if err := dst.EnableSharedStoreExperiment(dst.ctx); err != nil {
		t.Fatal(err)
	}
	group := dst.NewID()
	if _, err := dst.db.Exec(`INSERT INTO experiment_sources(id,source,decision,target_repo) VALUES(?,?,?,?)`, group, `{}`, `{}`, dst.Repo); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.db.Exec(`INSERT INTO experiment_effects(id) VALUES(?)`, first.ID); err != nil {
		t.Fatal(err)
	}
	// Choose an original alias, then introduce a lexicographically earlier alias
	// later. Earlier committed companion bytes must not depend on today's MIN.
	earlier, recorded := dst.NewID(), dst.NewID()
	if _, err := dst.db.Exec(`INSERT INTO experiment_actions VALUES(?,?,?,?)`, dst.Repo+":"+recorded, group, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	entry := src.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "New entry"})
	r2 := applyForTest(t, dst, exportForTest(t, src, transfer.Selection{Matters: []string{m}, Backlog: []string{entry}}))
	root := snapshotEventForTest(t, dst, r2.Operation)
	p, err := decodeWorkSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.References[r.Mapping[m]] != recorded {
		t.Fatalf("generated effect ID leaked: %+v", p.References)
	}
	if _, err := dst.db.Exec(`INSERT INTO experiment_actions VALUES(?,?,?,?)`, dst.Repo+":"+earlier, group, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	before := dst.snapshotProjection()
	if err = dst.Rebuild(dst.ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, dst.snapshotProjection()) {
		t.Fatal("later alias changed rebuild")
	}
}
