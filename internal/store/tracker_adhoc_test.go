package store

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
)

// The store half of ad-hoc tracker proposals: one Repo-tier event that becomes
// one queued outbox candidate, validated per kind at the fold.
//
// What this file owns is the substrate's side of that — the projected row, the
// refusals, determinism across a rebuild, the v14 migration, and the one
// interaction that needed an existing fold changed: an ad-hoc create is a
// create with no backlog entry behind it, which is a shape
// recordTrackerItemCreated could not previously express.

// adhocKey is the idempotency key insertTrackerCandidate derives for a
// proposal, spelled out here on purpose. The key is the outbox's own
// de-duplication contract (outbox_idempotency is a UNIQUE index), and a test
// that read it back off the row it is asserting on would be asserting nothing.
func adhocKey(ev Event, kind, ref string) string {
	return "tracker:" + ev.ID + ":" + kind + ":" + ref
}

// candidateID is the identity insertTrackerCandidate mints from that key —
// derived, not random, which is what makes a candidate row reproducible by a
// rebuild folding the same event twice.
func candidateID(key string) string {
	sum := sha256.Sum256([]byte(key))
	var raw [16]byte
	copy(raw[:], sum[:16])
	return encodeCrockford(raw)
}

// proposeAdhoc commits one tracker.adhoc-proposed against the harness's Repo
// and returns the event that carried it.
func (h *harness) proposeAdhoc(p TrackerAdhocProposed) Event {
	h.t.Helper()
	return h.commit(Draft{Type: TypeTrackerAdhocProposed, Subject: h.Repo, Payload: p})[0]
}

// TestAdhocProposalsQueueOneCandidatePerKind is the Step's central assertion:
// each kind lands exactly one queued row, column for column, with the ref
// nullability the v7 CHECK demands and the provider-facing payload the adapters
// read.
func TestAdhocProposalsQueueOneCandidatePerKind(t *testing.T) {
	h := newHarness(t)

	created := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocCreate, Title: "Publish the runbook", Detail: "Operators need it",
	})
	createKey := adhocKey(created, "create", "")
	h.wantRow("an ad-hoc create proposal", "outbox_entries", "idempotency_key=?",
		[]any{createKey}, map[string]any{
			"id":              candidateID(createKey),
			"repo":            h.Repo,
			"kind":            "create",
			"state":           "queued",
			"subject":         h.Repo,
			"ref":             nil,
			"idempotency_key": createKey,
			"payload":         `{"kind":"create","provenance":"adhoc","title":"Publish the runbook","detail":"Operators need it"}`,
			"reason":          "",
			"attempts":        0,
			"birth_event":     created.ID,
			"last_event":      created.ID,
		})

	commented := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocComment, Ref: "GH-7", Body: "Rolled the release back",
	})
	commentKey := adhocKey(commented, "comment", "GH-7")
	h.wantRow("an ad-hoc comment proposal", "outbox_entries", "idempotency_key=?",
		[]any{commentKey}, map[string]any{
			"id":              candidateID(commentKey),
			"repo":            h.Repo,
			"kind":            "comment",
			"state":           "queued",
			"subject":         h.Repo,
			"ref":             "GH-7",
			"idempotency_key": commentKey,
			"payload":         `{"body":"Rolled the release back"}`,
			"reason":          "",
			"attempts":        0,
			"birth_event":     commented.ID,
			"last_event":      commented.ID,
		})

	moved := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocState, Ref: "GH-7", Disposition: TrackerCompleted,
	})
	stateKey := adhocKey(moved, "state", "GH-7")
	h.wantRow("an ad-hoc state proposal", "outbox_entries", "idempotency_key=?",
		[]any{stateKey}, map[string]any{
			"id":              candidateID(stateKey),
			"repo":            h.Repo,
			"kind":            "state",
			"state":           "queued",
			"subject":         h.Repo,
			"ref":             "GH-7",
			"idempotency_key": stateKey,
			"payload":         `{"disposition":"completed"}`,
			"reason":          "",
			"attempts":        0,
			"birth_event":     moved.ID,
			"last_event":      moved.ID,
		})

	// The same three rows through the read surface, in birth order, with the
	// create's absent ref reading as the empty string the reader COALESCEs it
	// to rather than as a reference nobody has yet.
	entries, err := h.Outbox(h.ctx, h.Repo)
	if err != nil {
		t.Fatalf("read the ad-hoc outbox: %v", err)
	}
	want := []struct{ id, kind, ref string }{
		{candidateID(createKey), "create", ""},
		{candidateID(commentKey), "comment", "GH-7"},
		{candidateID(stateKey), "state", "GH-7"},
	}
	if len(entries) != len(want) {
		t.Fatalf("the outbox holds %d entries, want %d", len(entries), len(want))
	}
	for i, w := range want {
		got := entries[i]
		if got.ID != w.id || got.Kind != w.kind || got.Ref != w.ref ||
			got.State != "queued" || got.Subject != h.Repo {
			t.Errorf("outbox entry %d = %+v, want id=%s kind=%s ref=%q queued at the Repo",
				i, got, w.id, w.kind, w.ref)
		}
	}
}

// TestAdhocStateCandidateIsByteIdenticalToALifecycleOne holds the seam's one
// non-negotiable payload claim: the flush path must not be able to tell an
// operator's state proposal from one a Matter's lifecycle produced, because
// there is nothing different about them to tell.
func TestAdhocStateCandidateIsByteIdenticalToALifecycleOne(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("shared", "A Matter that shares a reference")
	h.start(matter)
	h.commit(Draft{Type: TypeReferenceAdded, Subject: matter, Payload: ReferenceAdded{
		Ref: "GH-7", TrackerPushLevel: TrackerPushBoundary,
	}})
	lifecycle := h.rowOf("outbox_entries", "kind='state' AND ref='GH-7'")

	moved := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocState, Ref: "GH-7", Disposition: TrackerActive,
	})
	adhoc := h.rowOf("outbox_entries", "idempotency_key=?", adhocKey(moved, "state", "GH-7"))
	if adhoc["payload"] != lifecycle["payload"] {
		t.Fatalf("the ad-hoc state payload is %s and the lifecycle one is %s; they must be byte-identical",
			adhoc["payload"], lifecycle["payload"])
	}
	if adhoc["kind"] != lifecycle["kind"] {
		t.Fatalf("the ad-hoc state kind is %s and the lifecycle one is %s", adhoc["kind"], lifecycle["kind"])
	}
}

// TestAdhocProposalsRefuseEveryMalformedPerKindPayload is the fold's
// validation, one case per rule. Each refusal must also append nothing: the
// fold runs inside the command's transaction, so a refused proposal leaves no
// event and no row.
func TestAdhocProposalsRefuseEveryMalformedPerKindPayload(t *testing.T) {
	h := newHarness(t)
	matter := h.matter("not-the-repo", "A node, which a proposal is never about")

	for _, tc := range []struct {
		name    string
		subject string
		payload any
		want    string
	}{
		{
			"a create with no title", h.Repo,
			TrackerAdhocProposed{Kind: AdhocCreate, Detail: "why"},
			"must carry a title",
		},
		{
			"a create naming a reference", h.Repo,
			TrackerAdhocProposed{Kind: AdhocCreate, Title: "t", Ref: "GH-7"},
			"must not name a reference",
		},
		{
			"a create carrying a body", h.Repo,
			TrackerAdhocProposed{Kind: AdhocCreate, Title: "t", Body: "b"},
			"carries a body or a disposition",
		},
		{
			"a create carrying a disposition", h.Repo,
			TrackerAdhocProposed{Kind: AdhocCreate, Title: "t", Disposition: TrackerActive},
			"carries a body or a disposition",
		},
		{
			"a comment with no reference", h.Repo,
			TrackerAdhocProposed{Kind: AdhocComment, Body: "b"},
			"must name the reference",
		},
		{
			"a comment with no body", h.Repo,
			TrackerAdhocProposed{Kind: AdhocComment, Ref: "GH-7"},
			"must carry a body",
		},
		{
			"a comment carrying a title", h.Repo,
			TrackerAdhocProposed{Kind: AdhocComment, Ref: "GH-7", Body: "b", Title: "t"},
			"carries a title, a detail or a disposition",
		},
		{
			"a state with no reference", h.Repo,
			TrackerAdhocProposed{Kind: AdhocState, Disposition: TrackerActive},
			"must name the reference",
		},
		{
			"a state with no disposition", h.Repo,
			TrackerAdhocProposed{Kind: AdhocState, Ref: "GH-7"},
			"not a provider-neutral disposition",
		},
		{
			"a state with an invented disposition", h.Repo,
			TrackerAdhocProposed{Kind: AdhocState, Ref: "GH-7", Disposition: TrackerDisposition("shipped")},
			"not a provider-neutral disposition",
		},
		{
			"a state carrying a body", h.Repo,
			TrackerAdhocProposed{Kind: AdhocState, Ref: "GH-7", Disposition: TrackerActive, Body: "b"},
			"carries a title, a detail or a body",
		},
		{
			"an invented kind", h.Repo,
			TrackerAdhocProposed{Kind: AdhocKind("escalate"), Ref: "GH-7"},
			"which is not create, comment or state",
		},
		{
			"no kind at all", h.Repo,
			TrackerAdhocProposed{Title: "t"},
			"which is not create, comment or state",
		},
		{
			"a proposal about a node", matter,
			TrackerAdhocProposed{Kind: AdhocCreate, Title: "t"},
			"keys at its Repo",
		},
		{
			"an unknown field", h.Repo,
			map[string]any{"kind": "create", "title": "t", "surprise": true},
			"unknown field",
		},
	} {
		before := len(h.rowsOf("events", ""))
		err := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
			return []Draft{{Type: TypeTrackerAdhocProposed, Subject: tc.subject, Payload: tc.payload}}, nil
		})
		refusalMentions(t, tc.name, err, tc.want)
		if got := len(h.rowsOf("events", "")); got != before {
			t.Errorf("%s appended %d event(s)", tc.name, got-before)
		}
	}
	h.wantRowCount("after every refusal", "outbox_entries", "", nil, 0)
}

// TestAdhocCandidatesAreDeterministicAcrossRebuildAndReopen: a candidate row is
// derived from its event and nothing else, so folding the same log again has to
// put back the same identity, the same idempotency key and the same NULL ref.
func TestAdhocCandidatesAreDeterministicAcrossRebuildAndReopen(t *testing.T) {
	h := newHarness(t)
	// A full log underneath, so the ad-hoc rows are folded in among everything
	// else rather than alone. richHistory binds GH-18 to a live Matter, which
	// is what the comment and state proposals below name.
	richHistory(h)

	created := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocCreate, Title: "An operator-authored item", Detail: "no backlog entry behind it",
	})
	commented := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocComment, Ref: "GH-18", Body: "A comment nobody's Stage closure wrote",
	})
	moved := h.proposeAdhoc(TrackerAdhocProposed{
		Kind: AdhocState, Ref: "GH-18", Disposition: TrackerCompleted,
	})
	keys := []string{
		adhocKey(created, "create", ""),
		adhocKey(commented, "comment", "GH-18"),
		adhocKey(moved, "state", "GH-18"),
	}
	for _, key := range keys {
		h.wantRowCount("the ad-hoc candidate for "+key, "outbox_entries", "idempotency_key=?", []any{key}, 1)
	}
	h.wantRowCount("the ad-hoc create's NULL ref", "outbox_entries",
		"idempotency_key=? AND ref IS NULL", []any{keys[0]}, 1)

	before := h.snapshotProjection()
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild with ad-hoc candidates: %v", err)
	}
	h.wantSameProjection("after a rebuild", before, h.snapshotProjection())
	// Twice: a rebuild has to be idempotent, and a derived identity is exactly
	// the thing a second fold could get wrong.
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild again: %v", err)
	}
	h.wantSameProjection("after a second rebuild", before, h.snapshotProjection())

	reopened, err := h.reopen(register, latestVersion(register))
	if err != nil {
		t.Fatalf("reopen with ad-hoc candidates: %v", err)
	}
	wantSameRows(t, "the outbox through a reopen", before["outbox_entries"], reopened.rowsOf("outbox_entries", ""))
}

// TestAnAdhocCreateTouchesNoBacklogRow is the interaction that forced
// recordTrackerItemCreated to change. Two create entries sit side by side —
// one delegated from a backlog entry, one authored out of nothing — and
// confirming either must move exactly what belongs to it.
func TestAnAdhocCreateTouchesNoBacklogRow(t *testing.T) {
	h := newHarness(t)
	entry := h.enterBacklog(BacklogEntered{Provenance: ProvenanceIntake, Title: "Delegated outward"})
	delegated := h.NewID()
	h.commit(Draft{Type: TypeBacklogDelegated, Subject: entry, Payload: BacklogDelegated{
		Outbox: delegated, IdempotencyKey: "backlog:" + entry,
	}})
	backlogBefore := h.rowsOf("backlog_entries", "")

	proposed := h.proposeAdhoc(TrackerAdhocProposed{Kind: AdhocCreate, Title: "Nobody's backlog entry"})
	adhoc := candidateID(adhocKey(proposed, "create", ""))
	wantSameRows(t, "the backlog through an ad-hoc create proposal", backlogBefore, h.rowsOf("backlog_entries", ""))

	// Two creates, both with a NULL ref, distinguished by what their payloads
	// name: one a backlog entry, one nothing at all.
	h.wantRowCount("both create candidates", "outbox_entries", "kind='create'", nil, 2)
	h.wantRowCount("the ad-hoc create", "outbox_entries",
		"id=? AND ref IS NULL AND subject=? AND json_extract(payload,'$.backlog') IS NULL",
		[]any{adhoc, h.Repo}, 1)
	h.wantRowCount("the delegated create", "outbox_entries",
		"id=? AND ref IS NULL AND subject=? AND json_extract(payload,'$.backlog')=?",
		[]any{delegated, entry, entry}, 1)

	// Confirming the ad-hoc creation flushes its own entry and leaves every
	// backlog row exactly where it was.
	h.commit(Draft{Type: TypeTrackerItemCreated, Subject: adhoc, Payload: TrackerItemCreated{Ref: "GH-501"}})
	h.wantRowCount("the flushed ad-hoc entry", "outbox_entries",
		"id=? AND state='flushed' AND attempts=1", []any{adhoc}, 1)
	wantSameRows(t, "the backlog through an ad-hoc confirmation", backlogBefore, h.rowsOf("backlog_entries", ""))

	// And the delegated one still retires its stub, which is what the
	// conditional had to keep true.
	confirmed := h.commit(Draft{
		Type: TypeTrackerItemCreated, Subject: delegated,
		Payload: TrackerItemCreated{Ref: "GH-502"},
	})[0]
	h.wantRowCount("the retired stub", "backlog_entries",
		"id=? AND state='delegated' AND last_event=?", []any{entry, confirmed.ID}, 1)
	active, err := h.ActiveBacklog(h.ctx, h.Repo)
	if err != nil || len(active) != 0 {
		t.Fatalf("active backlog after confirmation = %+v (err %v), want empty", active, err)
	}

	before := h.snapshotProjection()
	if err := h.Rebuild(h.ctx); err != nil {
		t.Fatalf("rebuild both creations: %v", err)
	}
	h.wantSameProjection("after rebuilding both creations", before, h.snapshotProjection())
}

// TestV14AddsTheAdhocTypeToAnExistingStore is the migration claim: a v13 store
// opens, gains one taxonomy row and nothing else, and can then express a
// proposal it could not express a moment earlier.
func TestV14AddsTheAdhocTypeToAnExistingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wip.db")
	preV14 := append([]migration{}, register[:13]...)
	h := newHarnessAt(t, path, preV14, 13)
	if got := h.SchemaVersion(); got != 13 {
		t.Fatalf("the fixture store is at v%d, want v13 (pre-tracker-adhoc)", got)
	}
	richHistory(h)

	// events.type is a foreign key into event_types, so a store whose taxonomy
	// has never held this token refuses the event at the substrate rather than
	// in the code above it.
	tooEarly := h.commitError(func(context.Context, *Tx) ([]Draft, error) {
		return []Draft{{
			Type: TypeTrackerAdhocProposed, Subject: h.Repo,
			Payload: TrackerAdhocProposed{Kind: AdhocCreate, Title: "Too early"},
		}}, nil
	})
	refusalMentions(t, "proposing before v14", tooEarly, TypeTrackerAdhocProposed)
	h.wantRowCount("the type before v14", "event_types", "type=?", []any{TypeTrackerAdhocProposed}, 0)

	before := h.snapshotProjection()
	log := h.rowsOf("events", "")
	dir := filepath.Dir(h.Path())

	migrated, err := h.reopen(shipped(), latestVersion(shipped()))
	if err != nil {
		t.Fatalf("migrate v13 -> v14: %v", err)
	}
	if got, want := migrated.SchemaVersion(), latestVersion(shipped()); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	if got := backupsIn(t, dir); len(got) != 1 {
		t.Fatalf("migrating to v14 left %v, want exactly one sidecar", got)
	}

	// v14 has no dataMigrate and no projectionVersion bump, so the log is
	// untouched and the projection is exactly what it was.
	wantSameRows(t, "the log through v14", log, migrated.rowsOf("events", ""))
	wantLogRetainsEveryRow(t, "the log order through v14", log, migrated.rowsOf("events", ""))
	wantSameProjectionThroughMigration(t, "after migrating v13 -> v14", before, migrated.snapshotProjection())
	migrated.wantRowCount("the seeded type", "event_types", "type=?", []any{TypeTrackerAdhocProposed}, 1)

	// And the type now works, through the one write path.
	proposed := migrated.proposeAdhoc(TrackerAdhocProposed{Kind: AdhocCreate, Title: "Now it lands"})
	migrated.wantRowCount("the migrated store's first ad-hoc candidate", "outbox_entries",
		"idempotency_key=?", []any{adhocKey(proposed, "create", "")}, 1)

	// The schema a store reaches by migrating is the schema a store created at
	// v14 directly has — the comparative assertion migrations_test.go makes of
	// every migration.
	direct := newHarnessAt(t, filepath.Join(t.TempDir(), "wip.db"), shipped(), latestVersion(shipped()))
	wantSameSchema(t, "a store migrated to v14 against one created at v14",
		schemaShape(t, migrated.Store), schemaShape(t, direct.Store))
}
