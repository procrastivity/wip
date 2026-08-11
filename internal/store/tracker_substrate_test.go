package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func wantTrackerReferences(t *testing.T, h *harness, matter string, want ...string) {
	t.Helper()
	got, err := h.TrackerReferences(h.ctx, matter)
	if err != nil {
		t.Fatalf("read tracker references: %v", err)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tracker references are [%s], want [%s]", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestTrackerReferencesAreAMatterOwnedSet(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("reference-set", "Reference set")
	step := h.step(matter, "step-01", "A child")

	// Historical Matter bindings retain singleton-replacement meaning. A child
	// binding remains readable through nodes.external_ref but is seam-inert.
	h.commit(Draft{Type: TypeReferenceBound, Subject: matter, Payload: ReferenceBound{Ref: "GH-1"}})
	h.commit(Draft{Type: TypeReferenceBound, Subject: matter, Payload: ReferenceBound{Ref: "GH-2"}})
	h.commit(Draft{Type: TypeReferenceBound, Subject: step, Payload: ReferenceBound{Ref: "GH-child"}})
	wantTrackerReferences(t, h, matter, "GH-2")

	// New events add independent, equally active memberships.
	h.commit(Draft{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GL-3"}})
	wantTrackerReferences(t, h, matter, "GH-2", "GL-3")

	before := len(h.eventsOf(matter))
	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GL-3"}}}, nil
	})
	refusalMentions(t, "adding an existing membership", err, "touched 0 projection rows")
	if got := len(h.eventsOf(matter)); got != before {
		t.Fatalf("a refused duplicate addition appended %d events", got-before)
	}

	h.commit(Draft{Type: TypeReferenceRemoved, Subject: matter, Payload: ReferenceRemoved{Ref: "GH-2"}})
	wantTrackerReferences(t, h, matter, "GL-3")

	// Rebind changes source and destination in one event and one transaction.
	h.commit(Draft{Type: TypeReferenceRebound, Subject: matter, Payload: ReferenceRebound{From: "GL-3", To: "BB-4"}})
	wantTrackerReferences(t, h, matter, "BB-4")

	// A failed destination leaves the source membership intact: no intermediate
	// set is observable even when the second half refuses.
	h.commit(Draft{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{Ref: "GH-5"}})
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceRebound, Subject: matter, Payload: ReferenceRebound{From: "BB-4", To: "GH-5"}}}, nil
	})
	refusalMentions(t, "rebinding onto an existing membership", err, "touched 0 projection rows")
	wantTrackerReferences(t, h, matter, "BB-4", "GH-5")

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild tracker references: %v", err)
	}
	wantTrackerReferences(t, h, matter, "BB-4", "GH-5")
}

func TestTrackerReferenceEventsRefuseChildrenWithoutAnEvent(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("children-are-inert", "Children are inert")
	step := h.step(matter, "step-01", "A child")
	before := len(h.eventsOf(step))

	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeReferenceAdded, Subject: step, Payload: ReferenceAdded{Ref: "GH-6"}}}, nil
	})
	refusalMentions(t, "adding a child reference", err, "not a Matter")
	if got := len(h.eventsOf(step)); got != before {
		t.Fatalf("a refused child binding appended %d events", got-before)
	}
}

func TestBacklogDelegationCreatesOneDurableStubAndCreationEntry(t *testing.T) {
	h := newHarness(t)
	entry := h.enterBacklog(BacklogEntered{
		Provenance: ProvenanceFound,
		Title:      "Move this work outward",
		Detail:     "The provider will own it",
	})
	outbox := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{
		Outbox: outbox, IdempotencyKey: "delegate:" + entry,
	}})

	backlog, err := h.Backlog(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read delegated backlog: %v", err)
	}
	if len(backlog) != 1 || backlog[0].State != "delegated" || backlog[0].Outbox != outbox {
		t.Fatalf("delegated backlog = %+v, want one stub linked to %s", backlog, outbox)
	}
	queued, err := h.Outbox(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read delegation outbox: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != outbox || queued[0].Kind != "create" || queued[0].State != "queued" || queued[0].Subject != entry {
		t.Fatalf("delegation outbox = %+v", queued)
	}
	var payload map[string]any
	if err := json.Unmarshal(queued[0].Payload, &payload); err != nil {
		t.Fatalf("decode creation payload: %v", err)
	}
	if payload["title"] != "Move this work outward" || payload["backlog"] != entry {
		t.Fatalf("creation payload = %v", payload)
	}

	before := len(h.eventsOf(entry))
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{
			Outbox: h.NewID(), IdempotencyKey: "delegate-again:" + entry,
		}}}, nil
	})
	refusalMentions(t, "delegating one entry twice", err, "touched 0 projection rows")
	if got := len(h.eventsOf(entry)); got != before {
		t.Fatalf("refused second delegation appended %d events", got-before)
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild delegated stub and outbox: %v", err)
	}
	queued, err = h.Outbox(h.ctx, h.Repo)
	if err != nil || len(queued) != 1 || queued[0].ID != outbox {
		t.Fatalf("rebuilt delegation outbox = %+v (err %v)", queued, err)
	}
	reopened, err := h.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatalf("reopen delegated stub and outbox: %v", err)
	}
	queued, err = reopened.Outbox(reopened.ctx, reopened.Repo)
	if err != nil || len(queued) != 1 || queued[0].ID != outbox {
		t.Fatalf("reopened delegation outbox = %+v (err %v)", queued, err)
	}
}

func TestTrackerItemCreatedRetiresDelegatedStubAfterConfirmation(t *testing.T) {
	h := newHarness(t)
	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Send outward"})
	outbox := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{
		Outbox: outbox, IdempotencyKey: "backlog:" + entry,
	}})
	h.commit(Draft{Type: TypeOutboxApproved, Subject: outbox, Payload: OutboxApproved{}})

	active, err := h.ActiveBacklog(h.ctx, h.Repo)
	if err != nil || len(active) != 1 || active[0].ID != entry {
		t.Fatalf("active backlog before confirmation = %+v (err %v)", active, err)
	}
	h.commit(Draft{Type: TypeTrackerItemCreated, Subject: outbox, Payload: TrackerItemCreated{Ref: "GH-99"}})
	active, err = h.ActiveBacklog(h.ctx, h.Repo)
	if err != nil || len(active) != 0 {
		t.Fatalf("active backlog after confirmation = %+v (err %v)", active, err)
	}
	all, err := h.Backlog(h.ctx, h.Repo)
	if err != nil || len(all) != 1 || all[0].State != "delegated" {
		t.Fatalf("historical delegated stub = %+v (err %v)", all, err)
	}
	queued, err := h.Outbox(h.ctx, h.Repo)
	if err != nil || len(queued) != 1 || queued[0].State != "flushed" || queued[0].Attempts != 1 {
		t.Fatalf("confirmed creation entry = %+v (err %v)", queued, err)
	}

	before := len(h.eventsOf(outbox))
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeTrackerItemCreated, Subject: outbox, Payload: TrackerItemCreated{Ref: "GH-99"}}}, nil
	})
	refusalMentions(t, "confirming one creation twice", err, "touched 0 projection rows")
	if got := len(h.eventsOf(outbox)); got != before {
		t.Fatalf("duplicate confirmation appended %d events", got-before)
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild confirmed delegation: %v", err)
	}
	active, err = h.ActiveBacklog(h.ctx, h.Repo)
	if err != nil || len(active) != 0 {
		t.Fatalf("rebuilt active backlog = %+v (err %v)", active, err)
	}
}

func TestOutboxLifecycleIsEventProjectedAndRetryPreservesIdentity(t *testing.T) {
	h := newHarness(t)
	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Deliver once"})
	outbox := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{
		Outbox: outbox, IdempotencyKey: "backlog:" + entry,
	}})

	h.commit(Draft{Type: TypeOutboxApproved, Subject: outbox, Payload: OutboxApproved{}})
	wantOutbox := func(state, reason string, attempts int) OutboxEntry {
		t.Helper()
		got, err := h.OutboxEntry(h.ctx, h.Repo, outbox)
		if err != nil {
			t.Fatalf("read outbox lifecycle: %v", err)
		}
		if got.State != state || got.Reason != reason || got.Attempts != attempts {
			t.Fatalf("outbox = %+v, want state=%s reason=%q attempts=%d", got, state, reason, attempts)
		}
		if got.ID != outbox || got.IdempotencyKey != "backlog:"+entry {
			t.Fatalf("retry changed identity or idempotency: %+v", got)
		}
		return got
	}
	wantOutbox("approved", "", 0)

	h.commit(Draft{Type: TypeOutboxDeliveryFailed, Subject: outbox, Payload: OutboxDeliveryFailed{Reason: "temporary outage"}})
	wantOutbox("queued", "temporary outage", 1)
	h.commit(Draft{Type: TypeOutboxRetried, Subject: outbox, Payload: OutboxRetried{}})
	wantOutbox("approved", "", 1)
	h.commit(Draft{Type: TypeOutboxWithheld, Subject: outbox, Payload: OutboxWithheld{Reason: "lease mismatch", Attempted: true}})
	wantOutbox("withheld", "lease mismatch", 2)
	h.commit(Draft{Type: TypeOutboxRetried, Subject: outbox, Payload: OutboxRetried{}})
	h.commit(Draft{Type: TypeTrackerItemCreated, Subject: outbox, Payload: TrackerItemCreated{Ref: "GH-123"}})
	wantOutbox("flushed", "", 3)

	events := h.eventsOf(outbox)
	wantTypes := []string{TypeOutboxApproved, TypeOutboxDeliveryFailed, TypeOutboxRetried, TypeOutboxWithheld, TypeOutboxRetried, TypeTrackerItemCreated}
	if len(events) != len(wantTypes) {
		t.Fatalf("outbox events = %d, want %d", len(events), len(wantTypes))
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("outbox event %d = %s, want %s", i, events[i].Type, want)
		}
	}

	for _, invalid := range []Draft{
		{Type: TypeOutboxApproved, Subject: outbox, Payload: OutboxApproved{}},
		{Type: TypeOutboxRetried, Subject: outbox, Payload: OutboxRetried{}},
		{Type: TypeOutboxDeclined, Subject: outbox, Payload: OutboxDeclined{Reason: "too late"}},
	} {
		before := len(h.eventsOf(outbox))
		err := h.commitError(func(context.Context, *Tx) ([]Draft, error) { return []Draft{invalid}, nil })
		refusalMentions(t, "a terminal outbox transition", err, "touched 0 projection rows")
		if got := len(h.eventsOf(outbox)); got != before {
			t.Fatalf("refused transition appended %d event(s)", got-before)
		}
	}

	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild outbox lifecycle: %v", err)
	}
	wantOutbox("flushed", "", 3)
	reopened, err := h.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatalf("reopen outbox lifecycle: %v", err)
	}
	got, err := reopened.OutboxEntry(reopened.ctx, reopened.Repo, outbox)
	if err != nil || got.State != "flushed" || got.Attempts != 3 {
		t.Fatalf("reopened outbox = %+v (err %v)", got, err)
	}
}

func TestOutboxDeclineAndLocalWithholdingDoNotCountAttempts(t *testing.T) {
	h := newHarness(t)
	newEntry := func(title string) string {
		t.Helper()
		entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceFound, Title: title})
		outbox := h.NewID()
		h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{Outbox: outbox, IdempotencyKey: "backlog:" + entry}})
		h.commit(Draft{Type: TypeOutboxApproved, Subject: outbox, Payload: OutboxApproved{}})
		return outbox
	}

	declined := newEntry("Decline")
	h.commit(Draft{Type: TypeOutboxDeclined, Subject: declined, Payload: OutboxDeclined{Reason: "human declined"}})
	got, _ := h.OutboxEntry(h.ctx, h.Repo, declined)
	if got.State != "declined" || got.Attempts != 0 || got.Reason != "human declined" {
		t.Fatalf("declined outbox = %+v", got)
	}

	withheld := newEntry("Compose")
	h.commit(Draft{Type: TypeOutboxWithheld, Subject: withheld, Payload: OutboxWithheld{Reason: "superseded"}})
	got, _ = h.OutboxEntry(h.ctx, h.Repo, withheld)
	if got.State != "withheld" || got.Attempts != 0 || got.Reason != "superseded" {
		t.Fatalf("locally withheld outbox = %+v", got)
	}
}

func TestOutboxLifecycleRefusalsAppendNoEvent(t *testing.T) {
	h := newHarness(t)
	unknown := h.NewID()
	for _, draft := range []Draft{
		{Type: TypeOutboxApproved, Subject: unknown, Payload: OutboxApproved{}},
		{Type: TypeOutboxDeclined, Subject: unknown, Payload: OutboxDeclined{}},
		{Type: TypeOutboxWithheld, Subject: unknown, Payload: OutboxWithheld{}},
		{Type: TypeOutboxDeliveryFailed, Subject: unknown, Payload: OutboxDeliveryFailed{}},
		{Type: TypeOutboxRetried, Subject: unknown, Payload: OutboxRetried{}},
		{Type: TypeOutboxFlushed, Subject: unknown, Payload: OutboxFlushed{}},
	} {
		before := len(h.eventsOf(unknown))
		err := h.commitError(func(context.Context, *Tx) ([]Draft, error) { return []Draft{draft}, nil })
		if err == nil {
			t.Fatalf("%s unexpectedly succeeded", draft.Type)
		}
		if got := len(h.eventsOf(unknown)); got != before {
			t.Fatalf("%s refusal appended %d events", draft.Type, got-before)
		}
	}

	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Strict lifecycle"})
	outbox := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{Outbox: outbox, IdempotencyKey: "backlog:" + entry}})
	before := len(h.eventsOf(outbox))
	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeOutboxApproved, Subject: outbox, Payload: map[string]any{"surprise": true}}}, nil
	})
	refusalMentions(t, "an unknown approval field", err, "unknown field")
	if got := len(h.eventsOf(outbox)); got != before {
		t.Fatalf("malformed approval appended %d events", got-before)
	}
}

func TestV7MigratesExistingOutboxRowsLosslessly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wip.db")
	h := newHarnessAt(t, path, register[:6], 6)
	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Queued before v7"})
	outbox := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{Outbox: outbox, IdempotencyKey: "backlog:" + entry}})
	before := h.rowsOf("outbox_entries", "")
	log := h.rowsOf("events", "")

	migrated, err := h.reopen(register, 7)
	if err != nil {
		t.Fatalf("migrate v6 to v7: %v", err)
	}
	wantSameRows(t, "outbox rows through v7", before, migrated.rowsOf("outbox_entries", ""))
	wantSameRows(t, "event log through v7", log, migrated.rowsOf("events", ""))
}

func TestTrackerStatePushAdvancesAnApprovedEntryAndNeverRegresses(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("push-record", "Push record")
	insertApproved := func(ref string) string {
		h.t.Helper()
		id := h.NewID()
		birth := h.birthEventOf(matter)
		if err := h.rawExec(`INSERT INTO outbox_entries
			(id,repo,kind,state,subject,ref,idempotency_key,payload,birth_event,last_event)
			VALUES (?,?,'state','approved',?,?,?,json_object('kind','state'),?,?)`,
			id, h.Repo, matter, ref, "state:"+id, birth.ID, birth.ID); err != nil {
			h.t.Fatalf("seed approved state entry: %v", err)
		}
		return id
	}

	completed := insertApproved("GH-42")
	h.commit(Draft{Type: TypeTrackerStatePushed, Subject: completed, Payload: TrackerStatePushed{
		Ref: "GH-42", Disposition: TrackerCompleted, Lease: "opaque-rev-1",
	}})
	record, err := h.TrackerPushRecord(h.ctx, "GH-42")
	if err != nil {
		t.Fatalf("read push record: %v", err)
	}
	if record.Disposition != TrackerCompleted || record.Lease != "opaque-rev-1" || record.Outbox != completed {
		t.Fatalf("push record = %+v", record)
	}
	var state string
	var attempts int
	if err := h.db.QueryRowContext(h.ctx, `SELECT state,attempts FROM outbox_entries WHERE id=?`, completed).Scan(&state, &attempts); err != nil {
		t.Fatalf("read flushed outbox row: %v", err)
	}
	if state != "flushed" || attempts != 1 {
		t.Fatalf("flushed outbox state/attempts = %s/%d", state, attempts)
	}

	regression := insertApproved("GH-42")
	before := len(h.eventsOf(regression))
	err = h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeTrackerStatePushed, Subject: regression, Payload: TrackerStatePushed{
			Ref: "GH-42", Disposition: TrackerActive, Lease: "opaque-rev-2",
		}}}, nil
	})
	refusalMentions(t, "regressing a completed reference", err, "touched 0 projection rows")
	if got := len(h.eventsOf(regression)); got != before {
		t.Fatalf("refused regression appended %d events", got-before)
	}
	if err := h.db.QueryRowContext(h.ctx, `SELECT state FROM outbox_entries WHERE id=?`, regression).Scan(&state); err != nil {
		t.Fatalf("read refused-regression outbox row: %v", err)
	}
	if state != "approved" {
		t.Fatalf("refused regression changed its outbox state to %s", state)
	}
}

func TestTrackerEventsDecodeStrictPayloads(t *testing.T) {
	h := newHarness(t)
	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Strict"})
	err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{Type: TypeBacklogDelegated, Subject: entry, Payload: map[string]any{
			"outbox": h.NewID(), "idempotency_key": "strict", "surprise": true,
		}}}, nil
	})
	refusalMentions(t, "an unknown delegation field", err, "unknown field")
}

func TestV5RefusesUnreconstructableProvisionalOutboxRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wip.db")
	h := newHarnessAt(t, path, register[:4], 4)
	matter := h.matter("legacy-outbox", "Legacy outbox")
	birth := h.birthEventOf(matter)
	if err := h.rawExec(`INSERT INTO outbox_entries
		(id,state,subject,idempotency_key,payload,birth_event,last_event)
		VALUES (?,'pending',?, 'legacy',json_object('kind','probe'),?,?)`,
		h.NewID(), matter, birth.ID, birth.ID); err != nil {
		t.Fatalf("seed provisional outbox row: %v", err)
	}
	log := h.rowsOf("events", "")

	_, err := h.reopen(register, 5)
	refusalMentions(t, "migrating an eventless provisional outbox row", err, "cannot migrate provisional outbox rows")

	unchanged, err := h.reopen(register[:4], 4)
	if err != nil {
		t.Fatalf("reopen v4 after refused v5: %v", err)
	}
	if got := len(unchanged.rowsOf("outbox_entries", "idempotency_key='legacy'")); got != 1 {
		t.Fatalf("refused migration retained %d legacy outbox rows, want 1", got)
	}
	wantSameRows(t, "event log after refused v5", log, unchanged.rowsOf("events", ""))
}
