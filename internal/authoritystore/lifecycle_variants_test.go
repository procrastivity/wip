package authoritystore

import (
	"context"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestM6NestedLifecycleCascadeAndAuthoritySealBoundary(t *testing.T) {
	f := newClaimTestFixture(t)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 104, 105, 106)
	f.acquire(t, 11, 2, anchor, allocation)
	claimID, journalID := allocation.ClaimID, allocation.JournalID

	stageID, stepID := claimTestID(201), claimTestID(202)
	stage := step12Command(f, 12, 3, operation.StageCreateV1, operation.StageCreateInput{MatterID: f.matter, Title: "Nested"}, claimID)
	_, stageStatus := completeStep12ClaimCommand(t, f, stage, journalID, 1, 201, 107)
	claimTestReceipt(t, stageStatus, "result.succeeded", map[string]any{
		"id": stageID, "matter_id": f.matter, "locator": "nested", "title": "Nested", "sort_key": int64(1000), "state": "planned",
	}, 107)
	step := step12Command(f, 13, 4, operation.StepCreateV2, operation.StepCreateInput{ParentID: stageID, Title: "Leaf"}, claimID)
	_, stepStatus := completeStep12ClaimCommand(t, f, step, journalID, 2, 202, 108)
	claimTestReceipt(t, stepStatus, "result.succeeded", map[string]any{
		"id": stepID, "parent_id": stageID, "matter_id": f.matter, "locator": "step-01", "title": "Leaf", "sort_key": int64(1000), "state": "planned",
	}, 108)

	start := step12Command(f, 14, 5, operation.StepStartV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	startStatus := completeLifecycleCommand(t, f, start, []string{claimTestID(109), claimTestID(110), claimTestID(111)})
	claimTestReceipt(t, startStatus, "result.succeeded", map[string]any{"step_id": stepID, "matter_id": f.matter, "state": "in-progress"}, 109, 110, 111)
	claimTestAcknowledge(t, f, journalID, 3, startStatus)
	claimTestEvent(t, f, 7, 109, 14, hashCommand(t, start), "matter.started", f.matter, 5,
		map[string]any{"from": "planned", "to": "in-progress", "cascade": true})
	claimTestEvent(t, f, 8, 110, 14, hashCommand(t, start), "stage.started", stageID, 5,
		map[string]any{"from": "planned", "to": "in-progress", "cascade": true, "cause_event_id": claimTestID(109)})
	claimTestEvent(t, f, 9, 111, 14, hashCommand(t, start), "step.started", stepID, 5,
		map[string]any{"from": "planned", "to": "in-progress", "cause_event_id": claimTestID(110)})

	stepFinish := step12Command(f, 15, 6, operation.StepFinishV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	stepFinishStatus := completeLifecycleCommand(t, f, stepFinish, []string{claimTestID(112)})
	claimTestReceipt(t, stepFinishStatus, "result.succeeded", map[string]any{"step_id": stepID, "matter_id": f.matter, "state": "done"}, 112)
	claimTestAcknowledge(t, f, journalID, 4, stepFinishStatus)
	stageFinish := step12Command(f, 16, 7, operation.StageFinishV1, operation.NodeLifecycleInput{NodeID: stageID}, claimID)
	stageFinishStatus := completeLifecycleCommand(t, f, stageFinish, []string{claimTestID(113)})
	claimTestReceipt(t, stageFinishStatus, "result.succeeded", map[string]any{"node_id": stageID, "matter_id": f.matter, "state": "done"}, 113)
	claimTestAcknowledge(t, f, journalID, 5, stageFinishStatus)
	var stageFinishRecord []byte
	if err := f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, stageFinish.ID).Scan(&stageFinishRecord); err != nil {
		t.Fatalf("read Stage finish event: %v", err)
	}
	var stageFinishEvent struct {
		Kind string `cbor:"kind"`
	}
	if err := artifactDecoder.Unmarshal(stageFinishRecord, &stageFinishEvent); err != nil || stageFinishEvent.Kind != "stage.finished" {
		t.Fatalf("Stage finish event kind = %q, %v; want stage.finished with no aggregate sweep", stageFinishEvent.Kind, err)
	}

	var before int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, domainA, journalID).Scan(&before); err != nil || before != 5 {
		t.Fatalf("claim journal count before Matter seal = %d, %v; want 5", before, err)
	}
	finish := step12Command(f, 17, 8, operation.MatterFinishV1, operation.MatterFinishInput{MatterID: f.matter}, claimID)
	if operation.MatterFinishV1.Metadata().Delivery != operation.DeliveryAuthority {
		t.Fatal("Matter finish must remain authority delivered")
	}
	finishStatus := completeLifecycleCommand(t, f, finish, []string{claimTestID(114), claimTestID(115)})
	claimTestReceipt(t, finishStatus, "result.succeeded", map[string]any{"matter_id": f.matter, "state": "done", "became_sealed": true}, 114, 115)
	var after int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, domainA, journalID).Scan(&after); err != nil || after != before {
		t.Fatalf("authority Matter finish changed claim journal count %d -> %d, %v", before, after, err)
	}
	var sweeps int
	var record []byte
	if err := f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND command_id=? AND event_id=?`, domainA, finish.ID, claimTestID(115)).Scan(&record); err == nil {
		var event struct {
			Kind string `cbor:"kind"`
		}
		if artifactDecoder.Unmarshal(record, &event) == nil && event.Kind == "batch.swept" {
			sweeps = 1
		}
	} else {
		t.Fatalf("read aggregate seal event: %v", err)
	}
	if sweeps != 1 {
		t.Fatalf("sealing Matter finish batch sweep count = %d; want 1", sweeps)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen lifecycle event fold: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
}

func TestM6StartSkipsStartedAncestorAndSimpleTransitionsUseOneEvent(t *testing.T) {
	f := newClaimTestFixture(t)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 104, 105, 106)
	f.acquire(t, 11, 2, anchor, allocation)
	claimID, journalID := allocation.ClaimID, allocation.JournalID

	matterStart := step12Command(f, 12, 3, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: f.matter}, claimID)
	matterStatus := completeLifecycleCommand(t, f, matterStart, []string{claimTestID(107)})
	claimTestReceipt(t, matterStatus, "result.succeeded", map[string]any{"matter_id": f.matter, "state": "in-progress"}, 107)
	claimTestAcknowledge(t, f, journalID, 1, matterStatus)

	stageID, stepID := claimTestID(201), claimTestID(202)
	stage := step12Command(f, 13, 4, operation.StageCreateV1, operation.StageCreateInput{MatterID: f.matter, Title: "Nested"}, claimID)
	_, stageStatus := completeStep12ClaimCommand(t, f, stage, journalID, 2, 201, 108)
	claimTestReceipt(t, stageStatus, "result.succeeded", map[string]any{
		"id": stageID, "matter_id": f.matter, "locator": "nested", "title": "Nested", "sort_key": int64(1000), "state": "planned",
	}, 108)
	step := step12Command(f, 14, 5, operation.StepCreateV2, operation.StepCreateInput{ParentID: stageID, Title: "Leaf"}, claimID)
	_, stepStatus := completeStep12ClaimCommand(t, f, step, journalID, 3, 202, 109)
	claimTestReceipt(t, stepStatus, "result.succeeded", map[string]any{
		"id": stepID, "parent_id": stageID, "matter_id": f.matter, "locator": "step-01", "title": "Leaf", "sort_key": int64(1000), "state": "planned",
	}, 109)

	start := step12Command(f, 15, 6, operation.StepStartV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	startStatus := completeLifecycleCommand(t, f, start, []string{claimTestID(110), claimTestID(111)})
	claimTestReceipt(t, startStatus, "result.succeeded", map[string]any{"step_id": stepID, "matter_id": f.matter, "state": "in-progress"}, 110, 111)
	claimTestAcknowledge(t, f, journalID, 4, startStatus)
	claimTestEvent(t, f, 8, 110, 15, hashCommand(t, start), "stage.started", stageID, 6,
		map[string]any{"from": "planned", "to": "in-progress", "cascade": true})
	claimTestEvent(t, f, 9, 111, 15, hashCommand(t, start), "step.started", stepID, 6,
		map[string]any{"from": "planned", "to": "in-progress", "cause_event_id": claimTestID(110)})

	for index, item := range []struct {
		definition operation.Definition
		input      operation.Input
		state      string
		kind       string
	}{
		{operation.StepPauseV1, operation.StepLifecycleInput{StepID: stepID}, "paused", "step.paused"},
		{operation.StepResumeV1, operation.StepLifecycleInput{StepID: stepID}, "in-progress", "step.resumed"},
		{operation.StepCancelV1, operation.StepCancelInput{StepID: stepID}, "canceled", "step.canceled"},
	} {
		command := step12Command(f, 16+index, uint64(7+index), item.definition, item.input, claimID)
		eventID := claimTestID(112 + index)
		status := completeLifecycleCommand(t, f, command, []string{eventID})
		claimTestReceipt(t, status, "result.succeeded", map[string]any{"step_id": stepID, "matter_id": f.matter, "state": item.state}, 112+index)
		claimTestAcknowledge(t, f, journalID, uint64(5+index), status)
		claimTestEvent(t, f, 10+index, 112+index, 16+index, hashCommand(t, command), item.kind, stepID, uint64(7+index),
			map[string]any{"from": map[string]string{"step.paused": "in-progress", "step.resumed": "paused", "step.canceled": "in-progress"}[item.kind], "to": item.state})
	}
	refusedStart := step12Command(f, 19, 10, operation.StepStartV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	refusedStatus := completeLifecycleCommand(t, f, refusedStart, []string{claimTestID(120)})
	claimTestReceipt(t, refusedStatus, "result.refused", nil)
	claimTestAcknowledge(t, f, journalID, 8, refusedStatus)
	var refusalEvents int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, refusedStart.ID).Scan(&refusalEvents); err != nil || refusalEvents != 0 {
		t.Fatalf("invalid start emitted %d authority events, %v; want no events", refusalEvents, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen lifecycle successes and refusal: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
}

func TestM6LifecycleHistorySurvivesLaterStepReplaceAndRemove(t *testing.T) {
	f := newClaimTestFixture(t)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 104, 105, 106)
	f.acquire(t, 11, 2, anchor, allocation)
	claimID, journalID := allocation.ClaimID, allocation.JournalID
	stageID, stepID, replacementID := claimTestID(201), claimTestID(202), claimTestID(203)

	stage := step12Command(f, 12, 3, operation.StageCreateV1, operation.StageCreateInput{MatterID: f.matter, Title: "Lifecycle"}, claimID)
	_, status := completeStep12ClaimCommand(t, f, stage, journalID, 1, 201, 107)
	claimTestReceipt(t, status, "result.succeeded", map[string]any{
		"id": stageID, "matter_id": f.matter, "locator": "lifecycle", "title": "Lifecycle", "sort_key": int64(1000), "state": "planned",
	}, 107)
	step := step12Command(f, 13, 4, operation.StepCreateV2, operation.StepCreateInput{ParentID: stageID, Title: "Original"}, claimID)
	_, status = completeStep12ClaimCommand(t, f, step, journalID, 2, 202, 108)
	claimTestReceipt(t, status, "result.succeeded", map[string]any{
		"id": stepID, "parent_id": stageID, "matter_id": f.matter, "locator": "step-01", "title": "Original", "sort_key": int64(1000), "state": "planned",
	}, 108)

	start := step12Command(f, 14, 5, operation.StepStartV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	startStatus := completeLifecycleCommand(t, f, start, []string{claimTestID(109), claimTestID(110), claimTestID(111)})
	claimTestAcknowledge(t, f, journalID, 3, startStatus)
	finish := step12Command(f, 15, 6, operation.StepFinishV1, operation.StepLifecycleInput{StepID: stepID}, claimID)
	finishStatus := completeLifecycleCommand(t, f, finish, []string{claimTestID(112)})
	claimTestAcknowledge(t, f, journalID, 4, finishStatus)

	replace := step12Command(f, 16, 7, operation.StepReplaceV1,
		operation.StepReplaceInput{StepID: stepID, Title: "Replacement"}, claimID)
	_, replaceStatus := completeStep12ClaimCommand(t, f, replace, journalID, 5, 203, 113)
	claimTestReceipt(t, replaceStatus, "result.succeeded", map[string]any{
		"removed_step_id": stepID,
		"replacement": map[string]any{
			"id": replacementID, "parent_id": stageID, "matter_id": f.matter, "locator": "step-02",
			"title": "Replacement", "sort_key": int64(1000), "state": "planned",
		},
	}, 113)
	remove := step12Command(f, 17, 8, operation.StepRemoveV1,
		operation.StepRemoveInput{StepID: replacementID, Reason: "superseded"}, claimID)
	_, removeStatus := completeStep12ClaimCommand(t, f, remove, journalID, 6, 204, 114)
	claimTestReceipt(t, removeStatus, "result.succeeded", map[string]any{"step_id": replacementID}, 114)

	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen lifecycle history after later tombstones: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
	for _, node := range []string{stepID, replacementID} {
		var tombstone, last string
		if err = reopened.db.QueryRow(`SELECT tombstone_event_id,last_event_id FROM m6_nodes WHERE domain_id=? AND node_id=?`, domainA, node).Scan(&tombstone, &last); err != nil || tombstone == "" || tombstone != last {
			t.Fatalf("folded tombstone projection for Step %s = tombstone:%q last:%q err:%v", node, tombstone, last, err)
		}
	}
}

func completeLifecycleCommand(t *testing.T, f *claimTestFixture, command operation.Command, eventIDs []string) CommandStatus {
	t.Helper()
	hash := hashCommand(t, command)
	pending, err := f.s.SubmitCommand(context.Background(), command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit lifecycle %s: status=%+v err=%v", command.Request.Operation, pending, err)
	}
	completed, err := f.s.CompleteConnectedLifecycle(context.Background(), pending.Owner, eventIDs, f.now, signWith(f.key))
	if err != nil || completed.Pending || completed.Owner != nil || len(completed.Receipt) == 0 {
		t.Fatalf("complete lifecycle %s: status=%+v err=%v", command.Request.Operation, completed, err)
	}
	return completed
}
