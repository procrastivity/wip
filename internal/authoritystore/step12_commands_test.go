package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func step12Command(f *claimTestFixture, id int, sequence uint64, definition operation.Definition, input operation.Input, claimID string) operation.Command {
	command := operation.Command{
		ID: claimTestID(id), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: sequence, ActedAt: "2026-09-23T11:59:00Z",
		CorrelationCommandID: claimTestID(id),
		Request: operation.Request{
			Operation: definition.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA}, Input: input,
		},
	}
	if definition.Metadata().Claim == operation.ClaimExact {
		command.Request.Context.Clone = f.clone
		command.Request.Context.Worktree = f.worktree
		command.Request.Claim = &operation.ClaimContext{ID: claimID, Epoch: "1"}
	}
	return command
}

func completeStep12Command(t *testing.T, f *claimTestFixture, command operation.Command, subject, event int, additional ...int) (string, CommandStatus) {
	t.Helper()
	hash := hashCommand(t, command)
	pending, err := f.s.SubmitCommand(context.Background(), command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("submit %s: status=%+v err=%v", command.Request.Operation, pending, err)
	}
	extra := make([]string, len(additional))
	for index, value := range additional {
		extra[index] = claimTestID(value)
	}
	completed, err := f.s.CompleteCommand(context.Background(), pending.Owner, operation.Result{Code: operation.ResultSucceeded},
		claimTestID(subject), claimTestID(event), f.now, signWith(f.key), extra...)
	if err != nil {
		t.Fatalf("complete %s: %v", command.Request.Operation, err)
	}
	return hash, completed
}

func completeStep12ClaimCommand(t *testing.T, f *claimTestFixture, command operation.Command, journalID string, position uint64, subject, event int, additional ...int) (string, CommandStatus) {
	t.Helper()
	hash, completed := completeStep12Command(t, f, command, subject, event, additional...)
	claimTestAcknowledge(t, f, journalID, position, completed)
	return hash, completed
}

func TestStep4EventSequencesRejectOmittedOrReorderedEffects(t *testing.T) {
	sequence := func(kinds ...string) []step12Event {
		events := make([]step12Event, len(kinds))
		for index, kind := range kinds {
			events[index].kind = kind
		}
		return events
	}
	for _, test := range []struct {
		name      string
		id        operation.ID
		events    []step12Event
		wantValid bool
	}{
		{"v2 without collision", operation.MatterCreateV2.Metadata().Operation, sequence("matter.created"), true},
		{"v2 collision order", operation.MatterCreateV2.Metadata().Operation, sequence("matter.created", "matter.locator-repair-required"), true},
		{"v2 missing repair event", operation.MatterCreateV2.Metadata().Operation, sequence("matter.created", "matter.locator-repaired"), false},
		{"insert without rebalance", operation.StepInsertV1.Metadata().Operation, sequence("step.inserted"), true},
		{"insert after rebalance", operation.StepInsertV1.Metadata().Operation, sequence("step.reordered", "step.inserted"), true},
		{"insert effects reversed", operation.StepInsertV1.Metadata().Operation, sequence("step.inserted", "step.reordered"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateStep12EventSequence(test.id, test.events)
			if (err == nil) != test.wantValid {
				t.Fatalf("validateStep12EventSequence(%v) = %v, want valid=%t", test.events, err, test.wantValid)
			}
		})
	}
}

func TestStep4ClaimOperationsReplayReopenAndRefusalAreAtomic(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, _, _ = claimTestBirthStep(t, f, 31, 101)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 104, 105, 106)
	f.acquire(t, 11, 3, anchor, allocation)
	claimID := allocation.ClaimID
	stageID := claimTestID(200)
	stepA, stepB, inserted, replacement := claimTestID(202), claimTestID(203), claimTestID(204), claimTestID(205)

	stageCommand := step12Command(f, 12, 4, operation.StageCreateV1, operation.StageCreateInput{MatterID: f.matter, Title: "Roadmap"}, claimID)
	stageHash, stageStatus := completeStep12ClaimCommand(t, f, stageCommand, allocation.JournalID, 1, 200, 107)
	claimTestReceipt(t, stageStatus, "result.succeeded", map[string]any{
		"id": stageID, "matter_id": f.matter, "locator": "roadmap", "title": "Roadmap", "sort_key": int64(2000), "state": "planned",
	}, 107)

	firstCommand := step12Command(f, 13, 5, operation.StepCreateV2, operation.StepCreateInput{ParentID: stageID, Title: "First"}, claimID)
	firstHash, firstStatus := completeStep12ClaimCommand(t, f, firstCommand, allocation.JournalID, 2, 202, 108)
	claimTestReceipt(t, firstStatus, "result.succeeded", map[string]any{
		"id": stepA, "parent_id": stageID, "matter_id": f.matter, "locator": "step-02", "title": "First", "sort_key": int64(1000), "state": "planned",
	}, 108)
	secondCommand := step12Command(f, 14, 6, operation.StepCreateV2, operation.StepCreateInput{ParentID: stageID, Title: "Second"}, claimID)
	_, secondStatus := completeStep12ClaimCommand(t, f, secondCommand, allocation.JournalID, 3, 203, 109)
	claimTestReceipt(t, secondStatus, "result.succeeded", map[string]any{
		"id": stepB, "parent_id": stageID, "matter_id": f.matter, "locator": "step-03", "title": "Second", "sort_key": int64(2000), "state": "planned",
	}, 109)

	insertCommand := step12Command(f, 15, 7, operation.StepInsertV1,
		operation.StepInsertInput{ParentID: stageID, Title: "Between", BeforeID: stepB}, claimID)
	_, insertStatus := completeStep12ClaimCommand(t, f, insertCommand, allocation.JournalID, 4, 204, 110, 111)
	claimTestReceipt(t, insertStatus, "result.succeeded", map[string]any{
		"id": inserted, "parent_id": stageID, "matter_id": f.matter, "locator": "step-04", "title": "Between", "sort_key": int64(1500), "state": "planned",
	}, 110)

	reorderCommand := step12Command(f, 16, 8, operation.StepReorderV1,
		operation.StepReorderInput{ParentID: stageID, Order: []string{stepB, inserted, stepA}}, claimID)
	_, reorderStatus := completeStep12ClaimCommand(t, f, reorderCommand, allocation.JournalID, 5, 200, 112)
	claimTestReceipt(t, reorderStatus, "result.succeeded", map[string]any{
		"parent_id": stageID, "order": []any{stepB, inserted, stepA},
	}, 112)

	replaceCommand := step12Command(f, 17, 9, operation.StepReplaceV1,
		operation.StepReplaceInput{StepID: stepB, Title: "Second replacement"}, claimID)
	_, replaceStatus := completeStep12ClaimCommand(t, f, replaceCommand, allocation.JournalID, 6, 205, 113)
	claimTestReceipt(t, replaceStatus, "result.succeeded", map[string]any{
		"removed_step_id": stepB,
		"replacement": map[string]any{
			"id": replacement, "parent_id": stageID, "matter_id": f.matter, "locator": "step-05",
			"title": "Second replacement", "sort_key": int64(1000), "state": "planned",
		},
	}, 113)

	removeCommand := step12Command(f, 18, 10, operation.StepRemoveV1,
		operation.StepRemoveInput{StepID: stepA, Reason: "superseded"}, claimID)
	_, removeStatus := completeStep12ClaimCommand(t, f, removeCommand, allocation.JournalID, 7, 202, 114)
	claimTestReceipt(t, removeStatus, "result.succeeded", map[string]any{"step_id": stepA}, 114)

	var eventCount int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&eventCount); err != nil || eventCount != 12 {
		t.Fatalf("event count after Step 4 writes = %d, %v; want 12", eventCount, err)
	}
	var nodeCount, stepCount, tombstoneCount int
	if err := f.s.db.QueryRow(`SELECT count(*),sum(kind='step'),sum(tombstone_event_id IS NOT NULL) FROM m6_nodes WHERE domain_id=?`, domainA).
		Scan(&nodeCount, &stepCount, &tombstoneCount); err != nil || nodeCount != 7 || stepCount != 5 || tombstoneCount != 2 {
		t.Fatalf("Step 4 projection footprint = nodes:%d Steps:%d tombstones:%d err:%v; want 7/5/2", nodeCount, stepCount, tombstoneCount, err)
	}
	var liveOrder string
	if err := f.s.db.QueryRow(`SELECT group_concat(node_id, ',') FROM (SELECT node_id FROM m6_nodes WHERE domain_id=? AND parent_id=? AND kind='step' AND tombstone_event_id IS NULL ORDER BY sort_key)`, domainA, stageID).Scan(&liveOrder); err != nil || liveOrder != replacement+","+inserted {
		t.Fatalf("reordered live sibling projection = %q, %v; want replacement then inserted", liveOrder, err)
	}

	duplicateStage := step12Command(f, 19, 11, operation.StageCreateV1, operation.StageCreateInput{MatterID: f.matter, Title: "Roadmap"}, claimID)
	duplicateHash := hashCommand(t, duplicateStage)
	pending, err := f.s.SubmitCommand(ctx, duplicateStage, duplicateHash, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit duplicate Stage locator: %+v %v", pending, err)
	}
	refused, err := f.s.CompleteCommand(ctx, pending.Owner, operation.Result{Code: operation.ResultSucceeded}, claimTestID(206), claimTestID(115), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete duplicate Stage: %v", err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	var afterRefusal int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&afterRefusal); err != nil || afterRefusal != eventCount {
		t.Fatalf("refused Stage changed event count to %d, %v", afterRefusal, err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 8, refused)

	replay, err := f.s.SubmitCommand(ctx, firstCommand, firstHash, f.peer, f.now)
	if err != nil || replay.Owner != nil || !bytes.Equal(replay.Receipt, firstStatus.Receipt) {
		t.Fatalf("same-ID/hash Step retry = %+v %v; want exact durable receipt", replay, err)
	}
	conflict := firstCommand
	conflict.Request.Input = operation.StepCreateInput{ParentID: stageID, Title: "different intent"}
	if _, err = f.s.SubmitCommand(ctx, conflict, hashCommand(t, conflict), f.peer, f.now); !errors.Is(err, ErrConflict) {
		t.Fatalf("same ID with different hash = %v, want ErrConflict", err)
	}
	replay, err = f.s.SubmitCommand(ctx, stageCommand, stageHash, f.peer, f.now)
	if err != nil || replay.Owner != nil || !bytes.Equal(replay.Receipt, stageStatus.Receipt) {
		t.Fatalf("Stage replay = %+v %v", replay, err)
	}

	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen M6 projection: %v", err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	reopened, err := f.s.SubmitCommand(ctx, firstCommand, firstHash, f.peer, f.now)
	if err != nil || reopened.Owner != nil || !bytes.Equal(reopened.Receipt, firstStatus.Receipt) {
		t.Fatalf("post-reopen Step replay = %+v %v", reopened, err)
	}
	var persistedOrder string
	if err = f.s.db.QueryRow(`SELECT group_concat(node_id, ',') FROM (SELECT node_id FROM m6_nodes WHERE domain_id=? AND parent_id=? AND kind='step' AND tombstone_event_id IS NULL ORDER BY sort_key)`, domainA, stageID).Scan(&persistedOrder); err != nil || persistedOrder != replacement+","+inserted {
		t.Fatalf("reopened Step projection = %q, %v", persistedOrder, err)
	}
}

func TestMatterCreateV2CollisionRepairAndExplicitAcceptanceSurviveReopen(t *testing.T) {
	f := newClaimTestFixture(t)
	command := matterCommand(claimTestID(11), 2, "alpha")
	command.Request.Operation = operation.MatterCreateV2.Metadata().Operation
	hash, completed := completeStep12Command(t, f, command, 200, 101, 102)
	matterID := claimTestID(200)
	assigned := "alpha-" + strings.ToLower(matterID[:6])
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"id": matterID, "title": "A title", "requested_locator": "alpha", "assigned_locator": assigned, "locator_repair_required": true,
	}, 101, 102)
	eventRows, err := f.s.db.Query(`SELECT position,event_id,record FROM authority_events WHERE domain_id=? AND command_id=? ORDER BY position`, domainA, command.ID)
	if err != nil {
		t.Fatal(err)
	}
	var eventKinds []string
	for eventRows.Next() {
		var position uint64
		var eventID string
		var record []byte
		if err = eventRows.Scan(&position, &eventID, &record); err != nil {
			break
		}
		event, parseErr := parseStep12Event(record, domainA, position, eventID, command.ID)
		if parseErr != nil {
			err = parseErr
			break
		}
		eventKinds = append(eventKinds, event.kind)
	}
	if rowsErr := eventRows.Err(); err == nil {
		err = rowsErr
	}
	_ = eventRows.Close()
	if err != nil || !reflect.DeepEqual(eventKinds, []string{"matter.created", "matter.locator-repair-required"}) {
		t.Fatalf("M6 collision event order = %v, %v; want creation then repair-required", eventKinds, err)
	}
	var repairRequired int
	var storedLocator string
	if err := f.s.db.QueryRow(`SELECT repair_required,locator FROM m6_nodes WHERE domain_id=? AND node_id=?`, domainA, matterID).Scan(&repairRequired, &storedLocator); err != nil || repairRequired != 1 || storedLocator != assigned {
		t.Fatalf("collision projection = repair:%d locator:%q err:%v", repairRequired, storedLocator, err)
	}

	anchor := f.anchor(t)
	raw, acquireHash := f.command(t, 21, 3, "claim.acquire", nil, map[string]any{
		"matter_id": matterID, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(61),
	})
	acquire, err := f.s.SubmitClaimAcquire(context.Background(), raw, acquireHash, anchor, f.peer, f.now)
	if err != nil || acquire.Owner == nil {
		t.Fatalf("acquire collision Matter: %+v %v", acquire, err)
	}
	allocation := claimTestAllocation(2, anchor, 103, 104, 105)
	acquired, _, err := f.s.CompleteClaimAcquire(context.Background(), acquire.Owner, allocation, f.now, signWith(f.key))
	if err != nil || acquired.Pending || acquired.Owner != nil || len(acquired.Receipt) == 0 {
		t.Fatalf("complete collision Matter claim: %v", err)
	}
	claimID := allocation.ClaimID
	beforeRepair, err := f.s.PinSnapshot(context.Background(), domainA, 7, emptyAnchor(), claimTestID(90), f.now, time.Minute)
	if err != nil {
		t.Fatalf("pin pre-repair Matter snapshot: %v", err)
	}
	repair := step12Command(f, 22, 4, operation.MatterLocatorRepairV1,
		operation.MatterLocatorRepairInput{MatterID: matterID, AssignedLocator: "alpha-renamed", Action: "rename"}, claimID)
	_, repaired := completeStep12Command(t, f, repair, 200, 106)
	claimTestReceipt(t, repaired, "result.succeeded", map[string]any{
		"id": matterID, "action": "rename", "requested_locator": "alpha", "previous_locator": assigned, "assigned_locator": "alpha-renamed",
	}, 106)
	if err = f.s.db.QueryRow(`SELECT repair_required FROM m6_nodes WHERE domain_id=? AND node_id=?`, domainA, matterID).Scan(&repairRequired); err != nil || repairRequired != 0 {
		t.Fatalf("renamed locator repair remains unresolved: repair=%d err=%v", repairRequired, err)
	}
	afterRepair, err := f.s.PinSnapshot(context.Background(), domainA, 7, emptyAnchor(), claimTestID(91), f.now, time.Minute)
	if err != nil {
		t.Fatalf("pin post-repair Matter snapshot: %v", err)
	}

	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		db, dbErr := connect(filepath.Join(f.root, "authority.db"), "ro", false)
		if dbErr != nil {
			t.Fatalf("reopen renamed locator repair: %v (database: %v)", err, dbErr)
		}
		t.Fatalf("reopen renamed locator repair: %v (schema:%v step4:%v step5:%v step6:%v step12:%v)", err,
			checkSchemaVersion(db, 11), checkStep4State(db), checkStep5State(db), checkStep6State(db), checkStep12State(db))
	}
	t.Cleanup(func() { _ = f.s.Close() })
	replay, err := f.s.SubmitCommand(context.Background(), repair, hashCommand(t, repair), f.peer, f.now)
	if err != nil || replay.Owner != nil || !bytes.Equal(replay.Receipt, repaired.Receipt) {
		t.Fatalf("replay locator rename after reopen = %+v %v", replay, err)
	}
	assertSnapshotLocator := func(snapshotID, want string) {
		t.Helper()
		items, complete, pageErr := f.s.SnapshotPage(context.Background(), domainA, 7, snapshotID, 0, 100, f.now)
		if pageErr != nil || !complete {
			t.Fatalf("read snapshot %s after reopen: complete=%t err=%v", snapshotID, complete, pageErr)
		}
		for _, item := range items {
			if item.ID != matterID {
				continue
			}
			var value struct {
				Locator string `cbor:"locator"`
			}
			if decodeErr := artifactDecoder.Unmarshal(item.Value, &value); decodeErr != nil || value.Locator != want {
				t.Fatalf("snapshot %s Matter locator = %q, decode error %v; want %q", snapshotID, value.Locator, decodeErr, want)
			}
			return
		}
		t.Fatalf("snapshot %s omitted Matter %s", snapshotID, matterID)
	}
	assertSnapshotLocator(beforeRepair.ID, assigned)
	assertSnapshotLocator(afterRepair.ID, "alpha-renamed")
	var legacyLocator, currentLocator string
	if err = f.s.db.QueryRow(`SELECT locator FROM matters WHERE domain_id=? AND matter_id=?`, domainA, matterID).Scan(&legacyLocator); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT locator FROM m6_nodes WHERE domain_id=? AND node_id=?`, domainA, matterID).Scan(&currentLocator); err != nil || legacyLocator != "alpha-renamed" || currentLocator != legacyLocator {
		t.Fatalf("renamed Matter locator projections = legacy:%q current:%q err:%v", legacyLocator, currentLocator, err)
	}
	if _, err = f.s.db.Exec(`UPDATE matters SET title='unauthorized title' WHERE domain_id=? AND matter_id=?`, domainA, matterID); err == nil {
		t.Fatal("M6 locator repair migration weakened Matter title immutability")
	}

	freedLocator := step12Command(f, 23, 5, operation.MatterCreateV2,
		operation.MatterCreateInput{Title: "Freed locator", Locator: assigned}, "")
	_, freed := completeStep12Command(t, f, freedLocator, 201, 107)
	freedID := claimTestID(201)
	claimTestReceipt(t, freed, "result.succeeded", map[string]any{
		"id": freedID, "title": "Freed locator", "requested_locator": assigned, "assigned_locator": assigned, "locator_repair_required": false,
	}, 107)

	acceptBirth := step12Command(f, 24, 6, operation.MatterCreateV2,
		operation.MatterCreateInput{Title: "Accepted collision", Locator: "alpha"}, "")
	_, acceptCreated := completeStep12Command(t, f, acceptBirth, 202, 108, 109)
	acceptMatterID := claimTestID(202)
	acceptAssigned := "alpha-" + strings.ToLower(acceptMatterID[:7])
	claimTestReceipt(t, acceptCreated, "result.succeeded", map[string]any{
		"id": acceptMatterID, "title": "Accepted collision", "requested_locator": "alpha", "assigned_locator": acceptAssigned, "locator_repair_required": true,
	}, 108, 109)
	anchor = f.anchor(t)
	raw, acquireHash = f.command(t, 25, 7, "claim.acquire", nil, map[string]any{
		"matter_id": acceptMatterID, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(62),
	})
	acquire, err = f.s.SubmitClaimAcquire(context.Background(), raw, acquireHash, anchor, f.peer, f.now)
	if err != nil || acquire.Owner == nil {
		t.Fatalf("acquire second collision Matter: %+v %v", acquire, err)
	}
	allocation = claimTestAllocation(3, anchor, 110, 111, 112)
	acquired, _, err = f.s.CompleteClaimAcquire(context.Background(), acquire.Owner, allocation, f.now, signWith(f.key))
	if err != nil || acquired.Pending || acquired.Owner != nil || len(acquired.Receipt) == 0 {
		t.Fatalf("complete second collision Matter claim: %v", err)
	}
	accept := step12Command(f, 26, 8, operation.MatterLocatorRepairV1,
		operation.MatterLocatorRepairInput{MatterID: acceptMatterID, AssignedLocator: acceptAssigned, Action: "accept"}, allocation.ClaimID)
	_, accepted := completeStep12Command(t, f, accept, 202, 113)
	claimTestReceipt(t, accepted, "result.succeeded", map[string]any{
		"id": acceptMatterID, "action": "accept", "requested_locator": "alpha", "previous_locator": acceptAssigned, "assigned_locator": acceptAssigned,
	}, 113)
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen accepted locator repair: %v", err)
	}
	if hash == "" {
		t.Fatal("Matter v2 request hash was empty")
	}
	var eventCount int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&eventCount); err != nil || eventCount != 14 {
		t.Fatalf("collision, rename, free-locator reuse and accept event count = %d, %v; want 14", eventCount, err)
	}
}
