package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func gateClaimedFixture(t *testing.T) (*claimTestFixture, AcquireAllocation) {
	t.Helper()
	f := newClaimTestFixture(t)
	t.Cleanup(func() { _ = f.s.Close() })
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 101, 102, 103)
	f.acquire(t, 11, 2, anchor, allocation)
	return f, allocation
}

func submitGateOperation(t *testing.T, f *claimTestFixture, command operation.Command) *Execution {
	t.Helper()
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.s.SubmitCommand(context.Background(), command, hash, f.peer, f.now)
	if err != nil || !status.Pending || status.Owner == nil {
		t.Fatalf("submit %s: status=%+v err=%v", command.Request.Operation, status, err)
	}
	return status.Owner
}

func TestGateDeclareAndCloseUseAuthorityProjectionAndExactReplay(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	declare := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	declareHash, _ := declare.RequestHash()
	declareOwner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, declareOwner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete gate declaration: %v", err)
	}
	claimTestReceipt(t, declared, "result.succeeded", map[string]any{"gate": "reviewed-local", "scale": "matter"}, 104)
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)
	var declarationCount int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM m6_gate_declarations WHERE domain_id=? AND repo_id=? AND gate=? AND scale=?`, domainA, repoA, "reviewed-local", "matter").Scan(&declarationCount); err != nil || declarationCount != 1 {
		t.Fatalf("authority gate declaration projection count=%d err=%v", declarationCount, err)
	}
	replay, err := f.s.SubmitCommand(ctx, declare, declareHash, f.peer, f.now)
	if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, declared.Receipt) {
		t.Fatalf("exact declaration replay changed receipt: status=%+v err=%v", replay, err)
	}

	close := step12Command(f, 13, 4, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "reviewed-local", NodeID: f.matter}, allocation.ClaimID)
	closeHash, _ := close.RequestHash()
	closeOwner := submitGateOperation(t, f, close)
	closed, err := f.s.CompleteCommand(ctx, closeOwner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete gate close: %v", err)
	}
	claimTestReceipt(t, closed, "result.succeeded", map[string]any{"gate": "reviewed-local", "node_id": f.matter, "scale": "matter"}, 105)
	claimTestAcknowledge(t, f, allocation.JournalID, 2, closed)
	var state, stateEvent string
	if err = f.s.db.QueryRow(`SELECT state,source_event_id FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, domainA, f.matter, "reviewed-local").Scan(&state, &stateEvent); err != nil || state != "closed" || stateEvent != claimTestID(105) {
		t.Fatalf("gate state projection = %s/%s err=%v", state, stateEvent, err)
	}
	if err = checkStep13State(f.s.db); err != nil {
		t.Fatalf("event-derived Step 13 projection validation: %v", err)
	}
	replayed, err := f.s.SubmitCommand(ctx, close, closeHash, f.peer, f.now)
	if err != nil || replayed.Pending || !bytes.Equal(replayed.Receipt, closed.Receipt) {
		t.Fatalf("exact close replay changed receipt: status=%+v err=%v", replayed, err)
	}
}

func TestGateDeclareNoopAndCloseRefusalDoNotAppendEvents(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	declare := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	owner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)

	noop := step12Command(f, 13, 4, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	noopOwner := submitGateOperation(t, f, noop)
	noEvent, err := f.s.CompleteCommand(ctx, noopOwner, operation.Result{Code: operation.ResultSucceeded}, "", "", f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete same-scale declaration no-op: %v", err)
	}
	noEventReceipt, err := readReceipt(noEvent.Receipt)
	if err != nil || noEventReceipt.Result.Code != "result.succeeded" || noEventReceipt.Range != nil ||
		!bytes.Equal(noEventReceipt.Result.Output, encodeTest(t, map[string]any{"gate": "reviewed-local", "scale": "matter"})) {
		t.Fatalf("same-scale declaration did not produce success/no-event receipt: %+v err=%v", noEventReceipt, err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 2, noEvent)
	if err = checkStep13State(f.s.db); err != nil {
		t.Fatalf("projection after declaration no-op: %v", err)
	}

	close := step12Command(f, 14, 5, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "reviewed-local", NodeID: f.matter}, allocation.ClaimID)
	closeOwner := submitGateOperation(t, f, close)
	closed, err := f.s.CompleteCommand(ctx, closeOwner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 3, closed)
	duplicate := step12Command(f, 15, 6, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "reviewed-local", NodeID: f.matter}, allocation.ClaimID)
	duplicateOwner := submitGateOperation(t, f, duplicate)
	refused, err := f.s.CompleteCommand(ctx, duplicateOwner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(106), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	var duplicateEvents int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, duplicate.ID).Scan(&duplicateEvents); err != nil || duplicateEvents != 0 {
		t.Fatalf("duplicate close emitted %d events, query error %v", duplicateEvents, err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 4, refused)
}

func TestGateDeclarationSnapshotsPreviouslySealedSubjects(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	stageID := claimTestID(120)
	create := step12Command(f, 12, 3, operation.StageCreateV1,
		operation.StageCreateInput{MatterID: f.matter, Title: "Already sealed"}, allocation.ClaimID)
	_, created := completeStep12ClaimCommand(t, f, create, allocation.JournalID, 1, 120, 104)
	claimTestReceipt(t, created, "result.succeeded", map[string]any{
		"id": stageID, "matter_id": f.matter, "locator": "already-sealed", "title": "Already sealed", "sort_key": int64(1000), "state": "planned",
	}, 104)
	start := step12Command(f, 13, 4, operation.StageStartV1,
		operation.NodeLifecycleInput{NodeID: stageID}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, start, []string{claimTestID(105), claimTestID(106)})
	claimTestAcknowledge(t, f, allocation.JournalID, 2, started)
	finish := step12Command(f, 14, 5, operation.StageFinishV1,
		operation.NodeLifecycleInput{NodeID: stageID}, allocation.ClaimID)
	finished := completeLifecycleCommand(t, f, finish, []string{claimTestID(107)})
	claimTestAcknowledge(t, f, allocation.JournalID, 3, finished)

	declare := step12Command(f, 15, 6, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "stage"}, allocation.ClaimID)
	owner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(108), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("declare gate after sealed Stage: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 4, declared)
	var raw []byte
	var position uint64
	var eventID string
	if err = f.s.db.QueryRow(`SELECT position,event_id,record FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, declare.ID).Scan(&position, &eventID, &raw); err != nil {
		t.Fatal(err)
	}
	event, err := parseStep12Event(raw, domainA, position, eventID, declare.ID)
	if err != nil {
		t.Fatal(err)
	}
	var exempt []string
	if err = artifactDecoder.Unmarshal(event.payload["exempt"], &exempt); err != nil || len(exempt) != 1 || exempt[0] != stageID {
		t.Fatalf("gate declaration exemption snapshot = %v, err %v; want [%s]", exempt, err, stageID)
	}
	var state string
	if err = f.s.db.QueryRow(`SELECT state FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, domainA, stageID, "reviewed-local").Scan(&state); err != nil || state != "exempt" {
		t.Fatalf("snapshot gate projection state = %q, err %v; want exempt", state, err)
	}
	if err = checkStep13State(f.s.db); err != nil {
		t.Fatalf("validate sealed-subject snapshot projection: %v", err)
	}
}

func TestGateSubmissionFencesWrongClaimRepoAndMalformedPayload(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	valid := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	wrongClaim := valid
	wrongClaim.ID = claimTestID(13)
	wrongClaim.CorrelationCommandID = wrongClaim.ID
	wrongClaim.EnvironmentSequence = 3
	wrongClaim.Request.Claim = &operation.ClaimContext{ID: claimTestID(999), Epoch: "1"}
	if _, err := f.s.SubmitCommand(context.Background(), wrongClaim, hashCommand(t, wrongClaim), f.peer, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong claim submission error = %v, want ErrFenced", err)
	}
	wrongEpoch := valid
	wrongEpoch.ID = claimTestID(14)
	wrongEpoch.CorrelationCommandID = wrongEpoch.ID
	wrongEpoch.EnvironmentSequence = 3
	wrongEpoch.Request.Claim = &operation.ClaimContext{ID: allocation.ClaimID, Epoch: "2"}
	if _, err := f.s.SubmitCommand(context.Background(), wrongEpoch, hashCommand(t, wrongEpoch), f.peer, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong claim epoch submission error = %v, want ErrFenced", err)
	}
	wrongRepo := valid
	wrongRepo.ID = claimTestID(15)
	wrongRepo.CorrelationCommandID = wrongRepo.ID
	wrongRepo.EnvironmentSequence = 3
	wrongRepo.Request.Context.Repo = repoB
	if _, err := f.s.SubmitCommand(context.Background(), wrongRepo, hashCommand(t, wrongRepo), f.peer, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong Repo submission error = %v, want ErrFenced", err)
	}
	malformed := valid
	malformed.Request.Input = operation.GateDeclareInput{Gate: "", Scale: "matter"}
	malformed.ID = claimTestID(16)
	malformed.CorrelationCommandID = malformed.ID
	if err := operation.GateDeclareV1.ValidateRequest(malformed.Request); err == nil {
		t.Fatal("malformed zero gate name passed operation validation")
	}
}

func TestGateDismissRequiresDoneSubjectAndRetainsReason(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	stageID := claimTestID(120)
	stage := step12Command(f, 12, 3, operation.StageCreateV1,
		operation.StageCreateInput{MatterID: f.matter, Title: "Review"}, allocation.ClaimID)
	stageOwner := submitGateOperation(t, f, stage)
	created, err := f.s.CompleteCommand(ctx, stageOwner, operation.Result{Code: operation.ResultSucceeded}, stageID, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("create stage for gate dismissal: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, created)

	declare := step12Command(f, 13, 4, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "stage"}, allocation.ClaimID)
	declareOwner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, declareOwner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("declare Stage gate before completion: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 2, declared)

	start := step12Command(f, 14, 5, operation.StageStartV1,
		operation.NodeLifecycleInput{NodeID: stageID}, allocation.ClaimID)
	startOwner := submitGateOperation(t, f, start)
	started, err := f.s.CompleteConnectedLifecycle(ctx, startOwner, []string{claimTestID(106), claimTestID(107)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("start stage for gate dismissal: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 3, started)

	finish := step12Command(f, 15, 6, operation.StageFinishV1,
		operation.NodeLifecycleInput{NodeID: stageID}, allocation.ClaimID)
	finishOwner := submitGateOperation(t, f, finish)
	finished, err := f.s.CompleteConnectedLifecycle(ctx, finishOwner, []string{claimTestID(108)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("finish stage for gate dismissal: %v", err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 4, finished)

	dismiss := step12Command(f, 16, 7, operation.GateDismissV1,
		operation.GateDismissInput{Gate: "reviewed-local", NodeID: stageID, Reason: "emergency exception"}, allocation.ClaimID)
	dismissOwner := submitGateOperation(t, f, dismiss)
	dismissed, err := f.s.CompleteCommand(ctx, dismissOwner, operation.Result{Code: operation.ResultSucceeded}, stageID, claimTestID(109), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("dismiss Done Stage gate: %v", err)
	}
	claimTestReceipt(t, dismissed, "result.succeeded", map[string]any{"gate": "reviewed-local", "node_id": stageID, "scale": "stage"}, 109)
	claimTestAcknowledge(t, f, allocation.JournalID, 5, dismissed)
	var state, reason string
	if err = f.s.db.QueryRow(`SELECT state,reason FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, domainA, stageID, "reviewed-local").Scan(&state, &reason); err != nil || state != "dismissed" || reason != "emergency exception" {
		t.Fatalf("dismissal projection state=%q reason=%q err=%v", state, reason, err)
	}
	if err = checkStep13State(f.s.db); err != nil {
		t.Fatalf("dismissal event fold validation: %v", err)
	}
}

func TestGateProjectionFailureRollsBackEventAndReceipt(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	declare := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	declareOwner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, declareOwner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)

	close := step12Command(f, 13, 4, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "reviewed-local", NodeID: f.matter}, allocation.ClaimID)
	owner := submitGateOperation(t, f, close)
	var before int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`CREATE TEMP TRIGGER reject_gate_projection BEFORE INSERT ON m6_gate_states BEGIN SELECT RAISE(ABORT,'injected gate projection error'); END`); err != nil {
		t.Fatal(err)
	}
	result := operation.Result{Code: operation.ResultSucceeded}
	if _, err = f.s.CompleteCommand(ctx, owner, result, f.matter, claimTestID(105), f.now, signWith(f.key)); err == nil {
		t.Fatal("gate completion succeeded despite injected projection failure")
	}
	var after, stateCount, receiptCount int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, domainA, f.matter, "reviewed-local").Scan(&stateCount); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, close.ID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if after != before || stateCount != 0 || receiptCount != 0 {
		t.Fatalf("failed gate projection left event/projection/receipt state: events %d->%d states=%d receipts=%d", before, after, stateCount, receiptCount)
	}
	if _, err = f.s.db.Exec(`DROP TRIGGER reject_gate_projection`); err != nil {
		t.Fatal(err)
	}
	completed, err := f.s.CompleteCommand(ctx, owner, result, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("retry exact gate completion after rollback: %v", err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{"gate": "reviewed-local", "node_id": f.matter, "scale": "matter"}, 105)
}

func TestGateRoleOwnershipRefusesHumanAndUnprovableRoleActor(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	ctx := context.Background()
	declare := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "verified", Scale: "matter"}, allocation.ClaimID)
	owner := submitGateOperation(t, f, declare)
	declared, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)

	close := step12Command(f, 13, 4, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "verified", NodeID: f.matter}, allocation.ClaimID)
	owner = submitGateOperation(t, f, close)
	human, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, human, "result.refused", nil)
	claimTestAcknowledge(t, f, allocation.JournalID, 2, human)

	// A refusal quarantines its claim-journal entry. Verify the role-actor path
	// under a fresh claim instead of continuing the quarantined journal.
	f, allocation = gateClaimedFixture(t)
	declare = step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "verified", Scale: "matter"}, allocation.ClaimID)
	owner = submitGateOperation(t, f, declare)
	declared, err = f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)

	role := step12Command(f, 13, 4, operation.GateCloseV1,
		operation.GateCloseInput{Gate: "verified", NodeID: f.matter}, allocation.ClaimID)
	role.Request.Actor = "role:verifier"
	owner = submitGateOperation(t, f, role)
	unproven, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, unproven, "result.refused", nil)
	var roleEvents, roleStates int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, role.ID).Scan(&roleEvents); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM m6_gate_states WHERE domain_id=? AND node_id=? AND gate=?`, domainA, f.matter, "verified").Scan(&roleStates); err != nil {
		t.Fatal(err)
	}
	if roleEvents != 0 || roleStates != 0 {
		t.Fatalf("unproven owning-role attempt wrote %d events and %d projected states", roleEvents, roleStates)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 2, unproven)

	// Dismissal is also fail-closed for role-owned gates until authority-backed
	// active-spawn proof exists; actor text alone must not produce a write.
	f, allocation = gateClaimedFixture(t)
	declare = step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "verified", Scale: "matter"}, allocation.ClaimID)
	owner = submitGateOperation(t, f, declare)
	declared, err = f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, declared)
	dismiss := step12Command(f, 13, 4, operation.GateDismissV1,
		operation.GateDismissInput{Gate: "verified", NodeID: f.matter, Reason: "must fail closed"}, allocation.ClaimID)
	dismiss.Request.Actor = "role:verifier"
	owner = submitGateOperation(t, f, dismiss)
	refused, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded}, f.matter, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	var dismissEvents int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, dismiss.ID).Scan(&dismissEvents); err != nil {
		t.Fatal(err)
	}
	if dismissEvents != 0 {
		t.Fatalf("unproven owning-role dismissal wrote %d events", dismissEvents)
	}
}

func TestGateReceiptOutputIsRevalidatedAgainstCommandAndProjection(t *testing.T) {
	f, allocation := gateClaimedFixture(t)
	command := step12Command(f, 12, 3, operation.GateDeclareV1,
		operation.GateDeclareInput{Gate: "reviewed-local", Scale: "matter"}, allocation.ClaimID)
	owner := submitGateOperation(t, f, command)
	completed, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded}, repoA, claimTestID(104), f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestAcknowledge(t, f, allocation.JournalID, 1, completed)
	receipt, err := readReceipt(completed.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Result.Output = encodeTest(t, map[string]any{"gate": "different", "scale": "matter"})
	if err = validateGateCommandEffects(f.s.db, command, nil, receipt); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("altered gate receipt output validation error = %v, want ErrInvalidStore", err)
	}
}

func TestGateHistoryValidationRejectsSuccessfulRoleOwnedEffects(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation operation.Definition
		input     operation.Input
		kind      string
		payload   map[string]any
		output    map[string]any
	}{
		{
			name: "role-owned close", operation: operation.GateCloseV1,
			input: operation.GateCloseInput{Gate: "verified", NodeID: claimTestID(301)}, kind: "gate.closed",
			payload: map[string]any{"gate": "verified", "scale": "matter", "tracker_push_level": "off"},
			output:  map[string]any{"gate": "verified", "node_id": claimTestID(301), "scale": "matter"},
		},
		{
			name: "role-owned dismissal", operation: operation.GateDismissV1,
			input: operation.GateDismissInput{Gate: "verified", NodeID: claimTestID(301), Reason: "reason"}, kind: "gate.dismissed",
			payload: map[string]any{"gate": "verified", "scale": "matter", "reason": "reason", "tracker_push_level": "off"},
			output:  map[string]any{"gate": "verified", "node_id": claimTestID(301), "scale": "matter"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := step13TestEvent(1, test.kind, claimTestID(301), test.payload).step12Event
			command := operation.Command{Request: operation.Request{
				Operation: test.operation.Metadata().Operation, Actor: "role:verifier",
				Context: operation.Context{Repo: repoA}, Input: test.input,
			}}
			output, err := artifactEncoder.Marshal(test.output)
			if err != nil {
				t.Fatal(err)
			}
			receipt := receiptRecord{Result: struct {
				Code    string  `cbor:"code"`
				Output  []byte  `cbor:"output"`
				Problem *string `cbor:"problem_code"`
			}{Code: string(operation.ResultSucceeded), Output: output}}
			receipt.Range = &struct {
				First string `cbor:"first_event_id"`
				Last  string `cbor:"last_event_id"`
				Count uint64 `cbor:"event_count"`
			}{First: event.id, Last: event.id, Count: 1}
			if err = validateGateCommandEffects(nil, command, []step12Event{event}, receipt); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("successful role-owned event/receipt validation = %v, want ErrInvalidStore", err)
			}
		})
	}
}
