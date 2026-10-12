package authoritystore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/schedulerhook"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestSchedulerHookResultCommitsOneBoundCompletionEvent(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 101, 102, 103)
	f.acquire(t, 11, 2, installed, allocation)
	var claimID, dispatchID string
	var claimEpoch uint64
	if err := f.s.db.QueryRow(`SELECT claim_id,dispatch_id,claim_epoch FROM claims WHERE domain_id=? AND matter_id=? AND close_command_id IS NULL`,
		domainA, f.matter).Scan(&claimID, &dispatchID, &claimEpoch); err != nil {
		t.Fatal(err)
	}
	workplan := []byte("# Plan\n- [ ] First asymmetric task\n- [ ] Second task\n")
	digest := digestBytes(workplan)
	if _, err := f.s.StartBlob(ctx, domainA, 7, digest, uint64(len(workplan)), f.now); err != nil {
		t.Fatal(err)
	}
	if offset, err := f.s.StageBlobChunk(ctx, domainA, 7, digest, 0, workplan); err != nil || offset != uint64(len(workplan)) {
		t.Fatalf("stage pinned Plan input: offset=%d err=%v", offset, err)
	}
	if err := f.s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	runID, roleID := claimTestID(90), claimTestID(91)
	roleCommand := operation.Command{
		ID: claimTestID(13), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 3, ActedAt: "2026-09-23T12:00:00Z", CorrelationCommandID: claimTestID(13),
		Request: operation.Request{
			Operation: operation.RoleSpawnV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: fmt.Sprint(claimEpoch)},
			Input: operation.RoleLifecycleInput{
				RunID: runID, RunLocator: "step-10-hook-test-run", MatterID: f.matter,
				DispatchID: dispatchID, RoleID: roleID, Role: "researcher",
			},
		},
	}
	roleHash := hashCommand(t, roleCommand)
	rolePending, err := f.s.SubmitCommand(ctx, roleCommand, roleHash, f.peer, f.now)
	if err != nil || rolePending.Owner == nil {
		t.Fatalf("submit researcher Role spawn: status=%+v err=%v", rolePending, err)
	}
	roleOutput := operation.RoleLifecycleOutput{
		RunID: runID, MatterID: f.matter, DispatchID: dispatchID,
		RoleID: roleID, Role: "researcher", State: "open",
	}
	if _, err = f.s.CompleteCommand(ctx, rolePending.Owner,
		operation.Result{Code: operation.ResultSucceeded, Output: roleOutput}, roleID,
		claimTestID(198), f.now, signWith(f.key), claimTestID(199)); err != nil {
		t.Fatalf("complete researcher Role spawn: %v", err)
	}
	prefix := f.anchor(t)
	var prefixEventID *string
	if prefix.EventCount > 0 {
		prefixEventID = &prefix.EventID
	}
	commandID := claimTestID(12)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 4, ActedAt: "2026-09-23T12:00:00Z", CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation, Actor: "role:researcher",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: fmt.Sprint(claimEpoch)},
			Input: operation.SchedulerHookInput{
				Kind: "plan", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
				RunID: runID, MatterID: f.matter, TargetID: f.matter, TargetKind: "matter",
				Role: "researcher", RoleID: roleID, DispatchID: dispatchID,
				Revision:        "builtin.markdown-checklist-plan@v1",
				TargetClaims:    []operation.TargetClaim{{MatterID: f.matter, ClaimID: claimID, ClaimEpoch: claimEpoch}},
				InstalledPrefix: operation.SchedulerPrefixAnchor{EventCount: prefix.EventCount, EventID: prefixEventID, Digest: prefix.Digest},
			},
			Blobs: []operation.BlobInput{{Name: "workplan", Digest: digest, Size: int64(len(workplan))}},
		},
	}
	hash := hashCommand(t, command)
	submitted, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || !submitted.Pending || submitted.Owner == nil {
		t.Fatalf("submit Plan hook: status=%+v err=%v", submitted, err)
	}
	if err = f.s.ReleasePendingCommandExecution(submitted.Owner); err != nil {
		t.Fatalf("release admission owner for split result phase: %v", err)
	}
	request, err := schedulerhook.RequestFromCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := schedulerhook.InputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	typed := schedulerhook.TypedResult{
		Kind: schedulerhook.KindPlan, Version: 1,
		Plan: &schedulerhook.PlanResult{Outcome: schedulerhook.PlanPlanned, Steps: []schedulerhook.StepSpec{
			{Title: "First asymmetric task"}, {Title: "Second task"},
		}},
	}
	key := schedulerhook.Key{DomainID: domainA, CommandID: commandID}
	resultDigest, err := schedulerhook.ResultDigest(key, inputDigest, typed)
	if err != nil {
		t.Fatal(err)
	}
	product := schedulerhook.Result{Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest}
	output, err := schedulerhook.OperationOutput(product)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.RecoverCommand(ctx, command, hash)
	if err != nil {
		t.Fatalf("recover exact submitted command for result phase: %v", err)
	}
	status, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded, Output: output},
		f.matter, claimTestID(200), f.now, signWith(f.key))
	if err != nil || status.Pending || len(status.Receipt) == 0 {
		t.Fatalf("complete hook engagement: status=%+v err=%v", status, err)
	}
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Range == nil ||
		receipt.Range.Count != 1 || receipt.Range.First != claimTestID(200) || receipt.Range.Last != claimTestID(200) {
		t.Fatalf("hook receipt lacks its exact event range: %+v err=%v", receipt, err)
	}
	var eventRecord []byte
	if err = f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, claimTestID(200)).Scan(&eventRecord); err != nil {
		t.Fatal(err)
	}
	fields, err := wipdwire.DecodeCanonicalMap(eventRecord,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || fields["kind"] != "scheduler.hook-completed" || fields["command_id"] != commandID || fields["subject_id"] != f.matter {
		t.Fatalf("hook completion event identity is invalid: fields=%v err=%v", fields, err)
	}
	payload, ok := fields["payload"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(payload, "effect_key", "kind", "version", "input_digest", "typed_result", "result_digest") ||
		payload["kind"] != "plan" || payload["version"] != uint64(1) || payload["input_digest"] != inputDigest || payload["result_digest"] != resultDigest {
		t.Fatalf("event does not retain the original effect identity and useful Plan result: %+v", payload)
	}
	effectKey, ok := payload["effect_key"].(map[string]any)
	if !ok || !wipdwire.ExactMapKeys(effectKey, "domain_id", "command_id") || effectKey["domain_id"] != domainA || effectKey["command_id"] != commandID {
		t.Fatalf("event changed the immutable hook key: %+v", effectKey)
	}
	var outputBytes []byte
	if outputBytes, err = artifactEncoder.Marshal(output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receipt.Result.Output, outputBytes) {
		t.Fatal("terminal receipt output differs from the exact typed adapter result")
	}
	replayed, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || replayed.Pending || !bytes.Equal(replayed.Receipt, status.Receipt) {
		t.Fatalf("exact original-command replay changed the receipt: %+v err=%v", replayed, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatalf("close store after hook completion: %v", err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen completed hook store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, err := reopened.QueryCommand(ctx, domainA, commandID, hash, 7, f.peer, envA, f.now)
	if err != nil || recovered.Pending || !bytes.Equal(recovered.Receipt, status.Receipt) {
		t.Fatalf("restart did not preserve the original typed hook receipt: %+v err=%v", recovered, err)
	}
}

func TestSchedulerHookProducedEvidenceSurvivesStagingExpiry(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	f.acquire(t, 11, 2, installed, claimTestAllocation(1, installed, 101, 102, 103))
	var claimID, dispatchID string
	var claimEpoch uint64
	if err := f.s.db.QueryRow(`SELECT claim_id,dispatch_id,claim_epoch FROM claims WHERE domain_id=? AND matter_id=? AND close_command_id IS NULL`,
		domainA, f.matter).Scan(&claimID, &dispatchID, &claimEpoch); err != nil {
		t.Fatal(err)
	}
	runID, roleID := claimTestID(90), claimTestID(91)
	roleCommand := operation.Command{
		ID: claimTestID(13), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 3, ActedAt: "2026-09-23T12:00:00Z", CorrelationCommandID: claimTestID(13),
		Request: operation.Request{
			Operation: operation.RoleSpawnV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: fmt.Sprint(claimEpoch)},
			Input: operation.RoleLifecycleInput{
				RunID: runID, RunLocator: "step-10-hook-evidence-test", MatterID: f.matter,
				DispatchID: dispatchID, RoleID: roleID, Role: "builder",
			},
		},
	}
	rolePending, err := f.s.SubmitCommand(ctx, roleCommand, hashCommand(t, roleCommand), f.peer, f.now)
	if err != nil || rolePending.Owner == nil {
		t.Fatalf("submit Builder Role spawn: status=%+v error=%v", rolePending, err)
	}
	roleOutput := operation.RoleLifecycleOutput{
		RunID: runID, MatterID: f.matter, DispatchID: dispatchID,
		RoleID: roleID, Role: "builder", State: "open",
	}
	if _, err = f.s.CompleteCommand(ctx, rolePending.Owner,
		operation.Result{Code: operation.ResultSucceeded, Output: roleOutput}, roleID,
		claimTestID(198), f.now, signWith(f.key), claimTestID(199)); err != nil {
		t.Fatalf("complete Builder Role spawn: %v", err)
	}
	source := []byte("# Pinned source\n")
	sourceDigest := digestBytes(source)
	if _, err = f.s.StartBlob(ctx, domainA, 7, sourceDigest, uint64(len(source)), f.now); err != nil {
		t.Fatal(err)
	}
	if offset, stageErr := f.s.StageBlobChunk(ctx, domainA, 7, sourceDigest, 0, source); stageErr != nil || offset != uint64(len(source)) {
		t.Fatalf("stage pinned source: offset=%d error=%v", offset, stageErr)
	}
	if err = f.s.FinishBlob(ctx, domainA, 7, sourceDigest); err != nil {
		t.Fatal(err)
	}
	prefix := f.anchor(t)
	var prefixEventID *string
	if prefix.EventCount > 0 {
		prefixEventID = &prefix.EventID
	}
	commandID := claimTestID(12)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 4, ActedAt: "2026-09-23T12:00:01Z", CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation, Actor: "role:builder",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: fmt.Sprint(claimEpoch)},
			Input: operation.SchedulerHookInput{
				Kind: "work", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
				RunID: runID, MatterID: f.matter, TargetID: f.matter, TargetKind: "matter",
				Role: "builder", RoleID: roleID, DispatchID: dispatchID,
				Revision:        "builtin.markdown-outline@v1",
				TargetClaims:    []operation.TargetClaim{{MatterID: f.matter, ClaimID: claimID, ClaimEpoch: claimEpoch}},
				InstalledPrefix: operation.SchedulerPrefixAnchor{EventCount: prefix.EventCount, EventID: prefixEventID, Digest: prefix.Digest},
			},
			Blobs: []operation.BlobInput{{Name: "source", Digest: sourceDigest, Size: int64(len(source))}},
		},
	}
	hash := hashCommand(t, command)
	submitted, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || submitted.Owner == nil {
		t.Fatalf("submit Work hook: status=%+v error=%v", submitted, err)
	}
	if err = f.s.ReleasePendingCommandExecution(submitted.Owner); err != nil {
		t.Fatalf("release admission owner for result phase: %v", err)
	}
	evidence := []byte("revision-bound verifier evidence\n")
	digest := digestBytes(evidence)
	if _, err = f.s.StartBlob(ctx, domainA, 7, digest, uint64(len(evidence)), f.now); err != nil {
		t.Fatal(err)
	}
	if offset, err := f.s.StageBlobChunk(ctx, domainA, 7, digest, 0, evidence); err != nil || offset != uint64(len(evidence)) {
		t.Fatalf("stage verifier evidence: offset=%d error=%v", offset, err)
	}
	if err := f.s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.RecoverCommand(ctx, command, hash)
	if err != nil {
		t.Fatalf("recover submitted Work for result phase: %v", err)
	}
	request, err := schedulerhook.RequestFromCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := schedulerhook.InputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	typed := schedulerhook.TypedResult{
		Kind: schedulerhook.KindWork, Version: 1,
		Work: &schedulerhook.WorkResult{
			Outcome: schedulerhook.WorkCompleted, Revision: "builtin.markdown-outline@v1", EvidenceRefs: []string{digest},
		},
	}
	key := schedulerhook.Key{DomainID: domainA, CommandID: commandID}
	resultDigest, err := schedulerhook.ResultDigest(key, inputDigest, typed)
	if err != nil {
		t.Fatal(err)
	}
	output, err := schedulerhook.OperationOutput(schedulerhook.Result{
		Key: key, InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := f.s.CompleteCommand(ctx, owner, operation.Result{Code: operation.ResultSucceeded, Output: output},
		f.matter, claimTestID(200), f.now, signWith(f.key))
	if err != nil || completed.Pending || len(completed.Receipt) == 0 {
		t.Fatalf("complete Work with produced evidence: status=%+v error=%v", completed, err)
	}
	var completionPosition uint64
	if err = f.s.db.QueryRow(`SELECT position FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, commandID).
		Scan(&completionPosition); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CollectExpired(ctx, f.now.Add(48*time.Hour)); err != nil {
		t.Fatalf("collect expired unreferenced staging products: %v", err)
	}
	var verified int
	var firstPosition uint64
	if err = f.s.db.QueryRow(`SELECT p.verified,r.first_position FROM blob_products p JOIN blob_references r USING(domain_id,digest)
		WHERE p.domain_id=? AND p.digest=?`, domainA, digest).Scan(&verified, &firstPosition); err != nil ||
		verified != 1 || firstPosition != completionPosition {
		t.Fatalf("produced evidence was not retained at its completion anchor: verified=%d position=%d want=%d error=%v",
			verified, firstPosition, completionPosition, err)
	}
	if err = checkStep4State(f.s.db); err != nil {
		t.Fatalf("validate Work result history before reopen: %v", err)
	}
	if err = schedulerHookProducedEvidenceAt(f.s.db, domainA, completionPosition, digest); err != nil {
		t.Fatalf("completion event does not independently validate its produced evidence: %v", err)
	}
	if err = checkStep6State(f.s.db); err != nil {
		t.Fatalf("validate durable Work result evidence before reopen: %v", err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		info, statErr := os.Stat(f.root)
		var mode os.FileMode
		if statErr == nil {
			mode = info.Mode().Perm()
		}
		t.Fatalf("reopen store with retained verifier evidence: %v (root mode=%#o stat=%v)", err, mode, statErr)
	}
	_ = reopened.Close()
}

func TestSchedulerHookSubmissionRefusesNonOwnerEnvironment(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 101, 102, 103)
	_, _, acquired, _ := f.acquire(t, 11, 2, installed, allocation)
	if acquired.Pending || acquired.Owner != nil || len(acquired.Receipt) == 0 {
		t.Fatalf("acquire owning Environment claim: status=%+v", acquired)
	}
	var claimID, dispatchID string
	var claimEpoch uint64
	if err := f.s.db.QueryRow(`SELECT claim_id,dispatch_id,claim_epoch FROM claims WHERE domain_id=? AND matter_id=? AND close_command_id IS NULL`,
		domainA, f.matter).Scan(&claimID, &dispatchID, &claimEpoch); err != nil {
		t.Fatal(err)
	}
	runID, roleID := claimTestID(90), claimTestID(91)
	roleCommand := operation.Command{
		ID: claimTestID(13), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 3, ActedAt: "2026-09-23T12:00:00Z", CorrelationCommandID: claimTestID(13),
		Request: operation.Request{
			Operation: operation.RoleSpawnV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: fmt.Sprint(claimEpoch)},
			Input: operation.RoleLifecycleInput{
				RunID: runID, RunLocator: "non-owner-hook-run", MatterID: f.matter,
				DispatchID: dispatchID, RoleID: roleID, Role: "researcher",
			},
		},
	}
	rolePending, err := f.s.SubmitCommand(ctx, roleCommand, hashCommand(t, roleCommand), f.peer, f.now)
	if err != nil || rolePending.Owner == nil {
		t.Fatalf("submit owning Environment Role spawn: status=%+v err=%v", rolePending, err)
	}
	roleOutput := operation.RoleLifecycleOutput{
		RunID: runID, MatterID: f.matter, DispatchID: dispatchID,
		RoleID: roleID, Role: "researcher", State: "open",
	}
	if _, err = f.s.CompleteCommand(ctx, rolePending.Owner,
		operation.Result{Code: operation.ResultSucceeded, Output: roleOutput}, roleID,
		claimTestID(198), f.now, signWith(f.key), claimTestID(199)); err != nil {
		t.Fatalf("complete owning Environment Role spawn: %v", err)
	}
	anchor := f.anchor(t)
	eventID := anchor.EventID
	peerB := dependencyEnvironmentB(t, f)
	task := operation.Command{
		ID: claimTestID(14), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envB, EnvironmentSequence: 1, ActedAt: "2026-09-23T12:00:01Z", CorrelationCommandID: claimTestID(14),
		Request: operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation, Actor: "system:scheduler",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Input: operation.SchedulerHookInput{
				Kind: "ask", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
				RunID: runID, MatterID: f.matter, TargetID: f.matter, TargetKind: "matter", Revision: "builtin.run-policy@v1",
				Reason: "blocked", Contention: "skip", Blocked: "halt", Failed: "halt",
				TargetClaims: []operation.TargetClaim{},
				InstalledPrefix: operation.SchedulerPrefixAnchor{
					EventCount: anchor.EventCount, EventID: &eventID, Digest: anchor.Digest,
				},
			},
		},
	}
	hash := hashCommand(t, task)
	if _, err = f.s.SubmitCommand(ctx, task, hash, peerB, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("non-owner Environment Ask submission error = %v, want ErrFenced", err)
	}
	if _, err = f.s.QueryCommand(ctx, domainA, task.ID, hash, 7, peerB, envB, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner Environment Ask created an authority submission: %v", err)
	}
	if after := f.anchor(t); after != anchor {
		t.Fatalf("non-owner Environment Ask changed authority prefix: before=%+v after=%+v", anchor, after)
	}
}
