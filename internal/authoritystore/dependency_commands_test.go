package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

type dependencyReceiptOutput struct {
	Edge    string `cbor:"edge"`
	Blocked string `cbor:"blocked_id"`
	Blocker string `cbor:"blocker_id"`
}

func dependencyAuthorityCommand(f *claimTestFixture, id int, sequence uint64, definition operation.Definition,
	input operation.Input, repo, environment string,
) operation.Command {
	command := step12Command(f, id, sequence, definition, input, "")
	command.Request.Context.Repo = repo
	if environment != "" {
		command.EnvironmentID = environment
	}
	return command
}

func dependencyV2Command(f *claimTestFixture, id int, sequence uint64, definition operation.Definition,
	input operation.Input, repo string,
) operation.Command {
	command := dependencyAuthorityCommand(f, id, sequence, definition, input, repo, "")
	command.EnvironmentID = envB
	return command
}

func submitDependencyForTest(t *testing.T, f *claimTestFixture, command operation.Command, peer tls.ConnectionState) *Execution {
	t.Helper()
	status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), peer, f.now)
	if err != nil || !status.Pending || status.Owner == nil {
		t.Fatalf("submit %s: status=%+v error=%v", command.Request.Operation, status, err)
	}
	var journalEntries int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&journalEntries); err != nil || journalEntries != 0 {
		t.Fatalf("dependency command entered a claim journal: count=%d error=%v", journalEntries, err)
	}
	return status.Owner
}

func completeDependencyForTest(t *testing.T, f *claimTestFixture, owner *Execution, event int) CommandStatus {
	t.Helper()
	status, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
		"", claimTestID(event), f.now, signWith(f.key))
	if err != nil || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("complete dependency command: status=%+v error=%v", status, err)
	}
	return status
}

func acquireStep8TargetClaims(t *testing.T, f *claimTestFixture, matterIDs ...string) (tls.ConnectionState, []operation.TargetClaim) {
	t.Helper()
	peer := dependencyEnvironmentB(t, f)
	f.peerB = peer
	f.clone, f.worktree = claimTestID(23), claimTestID(22)
	claims := make([]operation.TargetClaim, 0, len(matterIDs))
	for index, matter := range matterIDs {
		repo := repoA
		switch matter {
		case claimTestID(21):
			repo = repoB
		case claimTestID(22):
			repo = repoC
		case claimTestID(23):
			repo = repoB
		}
		id := 700 + index
		sequence := uint64(index + 1)
		installed := f.anchor(t)
		raw := encodeTest(t, map[string]any{
			"schema": "wipd.command/1", "command_id": claimTestID(id),
			"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
			"environment": map[string]any{"id": envB, "sequence": sequence},
			"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
			"causation_command_id": nil, "correlation_command_id": claimTestID(id),
			"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
			"context":   map[string]any{"repo_id": repo, "clone_id": f.clone, "worktree_id": f.worktree},
			"claim":     nil,
			"input": map[string]any{
				"matter_id": matter, "worktree_id": f.worktree,
				"dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(750 + index),
			},
			"blobs": []any{},
		})
		hash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
		status, err := f.s.SubmitClaimAcquire(context.Background(), raw, hash, installed, peer, f.now)
		if err != nil || status.Owner == nil {
			t.Fatalf("submit target claim for Matter %s: status=%+v error=%v", matter, status, err)
		}
		allocation := claimTestAllocation(index+1, installed, 500+index*3, 501+index*3, 502+index*3)
		if _, _, err = f.s.CompleteClaimAcquire(context.Background(), status.Owner, allocation, f.now, signWith(f.key)); err != nil {
			t.Fatalf("complete target claim for Matter %s: %v", matter, err)
		}
		claims = append(claims, operation.TargetClaim{MatterID: matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1})
	}
	return peer, claims
}

func acquireStrictD121EOwnedTargetClaim(t *testing.T, f *claimTestFixture, commandID int, sequence uint64,
	matter, repo string, allocation AcquireAllocation,
) operation.TargetClaim {
	t.Helper()
	commandName := claimTestID(commandID)
	dispatch := claimTestID(40 + commandID)
	raw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": commandName,
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": envA, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": commandName,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": repo, "clone_id": f.clone, "worktree_id": f.worktree},
		"claim":     nil,
		"input": map[string]any{
			"matter_id": matter, "worktree_id": f.worktree,
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": dispatch,
		},
		"blobs": []any{},
	})
	hash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
	pending, err := f.s.SubmitClaimAcquire(context.Background(), raw, hash, allocation.Installed, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("submit E-owned claim for Matter %s in Repo %s: status=%+v error=%v", matter, repo, pending, err)
	}
	completed, grant, err := f.s.CompleteClaimAcquire(context.Background(), pending.Owner, allocation, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete E-owned claim for Matter %s: %v", matter, err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"claim":     map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)},
		"matter_id": matter, "batch_id": allocation.BatchID, "dispatch_id": dispatch,
	}, 590, 591, 592)
	if grant.ID != allocation.GrantID || len(grant.Wrapper) == 0 || len(grant.Manifest) == 0 {
		t.Fatalf("E-owned acquisition did not retain its grant: %+v", grant)
	}
	return operation.TargetClaim{MatterID: matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}
}

func assertStrictD121StoredTargetClaim(t *testing.T, f *claimTestFixture, proof operation.TargetClaim, owner string) {
	t.Helper()
	rows, err := f.s.db.Query(`SELECT c.matter_id,c.claim_id,c.claim_epoch,c.authority_epoch,c.owner_environment_id,j.state
		FROM claims c JOIN claim_journals j USING(domain_id,claim_id)
		WHERE c.domain_id=? AND c.matter_id=? AND c.close_command_id IS NULL
		AND c.claim_epoch=(SELECT max(current.claim_epoch) FROM claims current WHERE current.domain_id=c.domain_id AND current.matter_id=c.matter_id)
		ORDER BY c.claim_epoch`, domainA, proof.MatterID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var count int
	for rows.Next() {
		var matter, claim, actualOwner, state string
		var claimEpoch, authorityEpoch uint64
		if err = rows.Scan(&matter, &claim, &claimEpoch, &authorityEpoch, &actualOwner, &state); err != nil {
			t.Fatal(err)
		}
		count++
		if matter != proof.MatterID || claim != proof.ClaimID || claimEpoch != proof.ClaimEpoch ||
			authorityEpoch != 7 || actualOwner != owner || state != "open" {
			t.Fatalf("target proof does not match its stored current open claim: proof=%+v stored=%s/%s epoch=%d authority-epoch=%d owner=%s state=%s want-owner=%s",
				proof, matter, claim, claimEpoch, authorityEpoch, actualOwner, state, owner)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("target proof for Matter %s matched %d current open claims, want exactly one", proof.MatterID, count)
	}
}

func dependencyClaimsForMatterIDs(claims []operation.TargetClaim, matters ...string) []operation.TargetClaim {
	set := make(map[string]struct{}, len(matters))
	for _, matter := range matters {
		set[matter] = struct{}{}
	}
	proof := make([]operation.TargetClaim, 0, len(set))
	for _, claim := range claims {
		if _, required := set[claim.MatterID]; required {
			proof = append(proof, claim)
		}
	}
	return proof
}

func dependencySuccess(t *testing.T, f *claimTestFixture, command operation.Command, status CommandStatus) string {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != receipt.Range.Last {
		var problem any
		if receipt.Result.Problem != nil {
			problem = *receipt.Result.Problem
		}
		t.Fatalf("dependency success receipt = %+v problem=%v error=%v", receipt, problem, err)
	}
	inputBlocked, inputBlocker := "", ""
	switch input := command.Request.Input.(type) {
	case operation.DependencyAddInput:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	case operation.DependencyAddV2Input:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	case operation.DependencyRemoveInput:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	case operation.DependencyRemoveV2Input:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	default:
		t.Fatalf("unexpected dependency input %T", command.Request.Input)
	}
	var output dependencyReceiptOutput
	names := []string{"blocked_id", "blocker_id"}
	if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation || command.Request.Operation == operation.DependencyAddV2.Metadata().Operation {
		names = append(names, "edge")
	}
	if err = closedPayload(receipt.Result.Output, &output, names...); err != nil || output.Blocked != inputBlocked || output.Blocker != inputBlocker {
		t.Fatalf("dependency output = %+v error=%v, want %s blocked by %s", output, err, inputBlocked, inputBlocker)
	}
	var position uint64
	var record []byte
	if err = f.s.db.QueryRow(`SELECT position,record FROM authority_events WHERE domain_id=? AND event_id=? AND command_id=?`,
		domainA, receipt.Range.First, command.ID).Scan(&position, &record); err != nil {
		t.Fatalf("read dependency event: %v", err)
	}
	event, err := parseStep12Event(record, domainA, position, receipt.Range.First, command.ID)
	if err != nil || event.repo != command.Request.Context.Repo || event.subject != inputBlocked {
		t.Fatalf("dependency event = %+v error=%v", event, err)
	}
	wantKind := "dependency.added"
	if command.Request.Operation == operation.DependencyRemoveV1.Metadata().Operation || command.Request.Operation == operation.DependencyRemoveV2.Metadata().Operation {
		wantKind = "dependency.removed"
	}
	if event.kind != wantKind {
		t.Fatalf("dependency event kind=%q, want %q", event.kind, wantKind)
	}
	var eventBlocker, edgeID string
	if canonicalDecode(event.payload["blocker"], &eventBlocker) != nil || canonicalDecode(event.payload["edge"], &edgeID) != nil || eventBlocker != inputBlocker {
		t.Fatalf("dependency event payload = %+v", event.payload)
	}
	if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation || command.Request.Operation == operation.DependencyAddV2.Metadata().Operation {
		if !ulid.MatchString(output.Edge) || output.Edge != edgeID {
			t.Fatalf("add output edge=%q, event edge=%q", output.Edge, edgeID)
		}
	} else if output.Edge != "" {
		t.Fatalf("remove output exposed an edge identity: %q", output.Edge)
	}
	definition, ok := dependencyHistoryDefinition(command.Request.Operation)
	if !ok || definition.ValidateResult(operation.Result{Code: operation.ResultSucceeded, Output: operation.DependencyOutput{
		EdgeID: output.Edge, BlockedID: output.Blocked, BlockerID: output.Blocker,
	}}) != nil {
		t.Fatalf("dependency output violates the operation contract: %+v", output)
	}
	projection, err := readDependencyProjection(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	var projected bool
	for _, edge := range projection {
		if edge.domain != domainA || edge.id != edgeID {
			continue
		}
		projected = edge.blocked == inputBlocked && edge.blocker == inputBlocker && edge.last == receipt.Range.First
		if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation || command.Request.Operation == operation.DependencyAddV2.Metadata().Operation {
			projected = projected && edge.repo == command.Request.Context.Repo && edge.birth == receipt.Range.First && edge.tombstone == ""
		} else {
			projected = projected && edge.tombstone == receipt.Range.First
		}
	}
	if !projected {
		t.Fatalf("dependency event/output do not match the projection: event=%+v output=%+v projection=%+v", event, output, projection)
	}
	return edgeID
}

func dependencyRefusal(t *testing.T, f *claimTestFixture, command operation.Command, status CommandStatus, code string, before PrefixAnchor, beforeEdges []dependencyEdge) {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Result.Code != string(operation.ResultRefused) || receipt.Result.Problem == nil ||
		string(*receipt.Result.Problem) != code || receipt.Result.Output != nil || receipt.Range != nil {
		t.Fatalf("dependency refusal receipt = %+v error=%v, want %s without output/range", receipt.Result, err, code)
	}
	if len(status.SignedReceipt) == 0 {
		t.Fatal("dependency semantic refusal lacks a signed terminal receipt")
	}
	if after := f.anchor(t); after != before {
		t.Fatalf("refusal changed event prefix: before=%+v after=%+v", before, after)
	}
	afterEdges, err := readDependencyProjection(f.s.db)
	if err != nil || !reflect.DeepEqual(afterEdges, beforeEdges) {
		t.Fatalf("refusal changed dependency projection: before=%+v after=%+v error=%v", beforeEdges, afterEdges, err)
	}
	var events int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("refusal retained %d model events: %v", events, err)
	}
	var receipts int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("refusal retained %d terminal receipts, want exactly one: %v", receipts, err)
	}
}

func authorizedStandDownForStrictD121(t *testing.T, f *claimTestFixture, peer tls.ConnectionState,
	commandID int, sequence uint64, nonce byte, claim AcquireAllocation, claimEpoch uint64, dispatchID, repo string, firstEvent int,
) CommandStatus {
	t.Helper()
	ctx := context.Background()
	domain, ownerKey := identity(domainA, 7)
	reason := "Owner accepts loss of unreturned work"
	input := map[string]any{
		"target": map[string]any{"claim_id": claim.ClaimID, "claim_epoch": claimEpoch, "owner_environment_id": envA},
		"reason": reason, "acknowledge_unreturned_work_loss": true,
	}
	commandName := claimTestID(commandID)
	raw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": commandName,
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": envB, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": commandName,
		"operation": map[string]any{"name": "claim.stand-down", "version": uint64(1)},
		"context":   map[string]any{"repo_id": repo, "clone_id": nil, "worktree_id": nil},
		"claim":     nil, "input": input, "blobs": []any{},
	})
	hash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
	subject := encodeTest(t, map[string]any{
		"schema": "wipd.claim-stand-down-subject/1", "command_id": commandName, "request_hash": hash,
		"claim_id": claim.ClaimID, "claim_epoch": claimEpoch, "owner_environment_id": envA,
		"acting_environment_id": envB, "reason_digest": digestBytes([]byte(reason)),
	})
	proof := signedTest(t, ownerKey, "owner-attestation", "wipd.owner-attestation/1", domainA, domain.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.owner-attestation/1", "action": "claim-stand-down", "domain_id": domainA,
		"current_epoch": uint64(7), "next_epoch": nil, "subject_schema": "wipd.claim-stand-down-subject/1",
		"subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
		"issued_at":  f.now.Add(-time.Minute).Format(time.RFC3339Nano),
		"expires_at": f.now.Add(time.Minute).Format(time.RFC3339Nano), "loss_accepted": true,
	})
	pending, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, peer, f.now, proof)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("submit authorized cross-Environment stand-down: status=%+v err=%v", pending, err)
	}
	completed, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(firstEvent), claimTestID(firstEvent + 1)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete authorized cross-Environment stand-down: %v", err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"claim_id": claim.ClaimID, "claim_epoch": claimEpoch, "dispatch_id": dispatchID,
		"reason_digest": digestBytes([]byte(reason)),
	}, firstEvent, firstEvent+1)
	return completed
}

func TestStrictD121SameMatterDependencyUsesOneExactClaim(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	claimTestBirthStep(t, f, 31, 101)
	firstStep, secondStep := claimTestID(131), claimTestID(132)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, allocation)
	secondStepCommand := step12Command(f, 12, 4, operation.StepCreateV2,
		operation.StepCreateInput{ParentID: f.matter, Title: "second Step"}, allocation.ClaimID)
	completeStep12ClaimCommand(t, f, secondStepCommand, allocation.JournalID, 1, 132, 105)
	proof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}}
	command := dependencyAuthorityCommand(f, 13, 5, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: firstStep, BlockerID: secondStep, TargetClaims: proof}, repoA, "")
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 106)
	edge := dependencySuccess(t, f, command, status)
	if edge == "" {
		t.Fatal("successful same-Matter dependency lacks edge identity")
	}
	remove := dependencyAuthorityCommand(f, 14, 6, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: firstStep, BlockerID: secondStep, TargetClaims: proof}, repoA, "")
	removed := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, f.peer), 107)
	if removedEdge := dependencySuccess(t, f, remove, removed); removedEdge != edge {
		t.Fatalf("same-Matter removal tombstoned %q, want %q", removedEdge, edge)
	}

	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyAuthorityCommand(f, 15, 7, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: firstStep, BlockerID: secondStep}, repoA, "")
	refused := completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, f.peer), 108)
	dependencyRefusal(t, f, missing, refused, "refusal.claim-fenced", before, edges)

	self := dependencyAuthorityCommand(f, 16, 8, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: firstStep, BlockerID: firstStep, TargetClaims: proof}, repoA, "")
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	selfStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, self, f.peer), 109)
	dependencyRefusal(t, f, self, selfStatus, "refusal.dependency-cycle", before, edges)
}

func TestStrictD121WriteBeforeAuthorizedStandDownStaysInReplacementGrantPrefix(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	ctx := context.Background()
	claimTestBirthStep(t, f, 31, 101)
	step := claimTestID(131)
	installed := f.anchor(t)
	oldClaim := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, oldClaim)
	write := dependencyAuthorityCommand(f, 12, 4, operation.DependencyAddV2,
		operation.DependencyAddV2Input{
			BlockedID: step, BlockerID: f.matter,
			TargetClaims: []operation.TargetClaim{{MatterID: f.matter, ClaimID: oldClaim.ClaimID, ClaimEpoch: 1}},
		}, repoA, "")
	writeStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, write, f.peer), 105)
	edge := dependencySuccess(t, f, write, writeStatus)
	beforeStandDown := f.anchor(t)
	if beforeStandDown.EventID != claimTestID(105) {
		t.Fatalf("write did not commit immediately before stand-down: anchor=%+v", beforeStandDown)
	}

	peerF := dependencyEnvironmentB(t, f)
	authorizedStandDownForStrictD121(t, f, peerF, 700, 1, 0x72, oldClaim, 1, claimTestID(51), repoA, 106)
	replacementInstalled := f.anchor(t)
	if replacementInstalled.EventCount <= beforeStandDown.EventCount {
		t.Fatalf("stand-down prefix did not retain the committed write: before=%+v after=%+v", beforeStandDown, replacementInstalled)
	}
	replacementRaw, replacementHash := f.command(t, 701, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(751),
	}, envB)
	replacementPending, err := f.s.SubmitClaimAcquire(ctx, replacementRaw, replacementHash, replacementInstalled, peerF, f.now)
	if err != nil || replacementPending.Owner == nil || !replacementPending.Pending {
		t.Fatalf("submit replacement claim acquisition after committed write: status=%+v err=%v", replacementPending, err)
	}
	replacement := claimTestAllocation(2, replacementInstalled, 108, 109)
	replacementStatus, grant, err := f.s.CompleteClaimAcquire(ctx, replacementPending.Owner, replacement, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete replacement claim acquisition after committed write: %v", err)
	}
	replacementReceipt, err := readReceipt(replacementStatus.Receipt)
	if err != nil || replacementReceipt.Result.Code != string(operation.ResultSucceeded) {
		t.Fatalf("replacement claim was not acquired: %+v err=%v", replacementReceipt.Result, err)
	}
	if grant.Snapshot.Delta.Start != replacementInstalled || grant.Snapshot.Delta.Start.EventCount <= beforeStandDown.EventCount {
		t.Fatalf("replacement grant did not pin the prefix containing the prior write: start=%+v installed=%+v prior-write-prefix=%+v",
			grant.Snapshot.Delta.Start, replacementInstalled, beforeStandDown)
	}
	if current := mustDependencyProjection(t, f); len(current) != 1 || current[0].id != edge || current[0].tombstone != "" {
		t.Fatalf("authorized stand-down/replacement changed the committed Step 8 edge: %+v", current)
	}
}

func TestStrictD121CrossEnvironmentStandDownFencesPendingWrite(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	ctx := context.Background()
	claimTestBirthStep(t, f, 31, 101)
	step := claimTestID(131)
	installed := f.anchor(t)
	oldClaim := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, oldClaim)

	// Submit the v2 write first, with E's still-current exact claim, but leave
	// semantic completion pending while F performs an authorized stand-down.
	write := dependencyAuthorityCommand(f, 12, 4, operation.DependencyAddV2,
		operation.DependencyAddV2Input{
			BlockedID: step, BlockerID: f.matter,
			TargetClaims: []operation.TargetClaim{{MatterID: f.matter, ClaimID: oldClaim.ClaimID, ClaimEpoch: 1}},
		}, repoA, "")
	writeHash := hashCommand(t, write)
	submitDependencyForTest(t, f, write, f.peer)

	peerF := dependencyEnvironmentB(t, f)
	authorizedStandDownForStrictD121(t, f, peerF, 700, 1, 0x71, oldClaim, 1, claimTestID(51), repoA, 105)

	// F acquires the replacement using its independent next sequence. This
	// proves the stale completion is fenced by closure/replacement, not by an
	// unavailable target or a failed stand-down.
	replacementInstalled := f.anchor(t)
	replacementRaw, replacementHash := f.command(t, 701, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(751),
	}, envB)
	replacementPending, err := f.s.SubmitClaimAcquire(ctx, replacementRaw, replacementHash, replacementInstalled, peerF, f.now)
	if err != nil || replacementPending.Owner == nil || !replacementPending.Pending {
		t.Fatalf("submit replacement claim acquisition from F: status=%+v err=%v", replacementPending, err)
	}
	replacement := claimTestAllocation(2, replacementInstalled, 107, 108, 109)
	replacementStatus, _, err := f.s.CompleteClaimAcquire(ctx, replacementPending.Owner, replacement, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete replacement claim acquisition from F: %v", err)
	}
	replacementReceipt, err := readReceipt(replacementStatus.Receipt)
	if err != nil || replacementReceipt.Result.Code != string(operation.ResultSucceeded) {
		var problem any
		if replacementReceipt.Result.Problem != nil {
			problem = *replacementReceipt.Result.Problem
		}
		t.Fatalf("replacement claim was not acquired: result=%+v problem=%v err=%v", replacementReceipt.Result, problem, err)
	}

	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	if err = f.s.Close(); err != nil {
		t.Fatalf("close with pending stale Step 8 write: %v", err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen with pending stale Step 8 write: %v", err)
	}
	recoveredWrite, err := f.s.RecoverCommand(ctx, write, writeHash)
	if err != nil {
		t.Fatalf("recover pending Step 8 write after stand-down and replacement: %v", err)
	}
	refused, err := f.s.CompleteCommand(ctx, recoveredWrite, operation.Result{Code: operation.ResultSucceeded},
		"", claimTestID(110), f.now, signWith(f.key))
	if err != nil || len(refused.SignedReceipt) == 0 {
		t.Fatalf("complete pending v2 write after authorized stand-down and replacement: status=%+v err=%v", refused, err)
	}
	dependencyRefusal(t, f, write, refused, "refusal.claim-fenced", before, edges)
	replay, err := f.s.SubmitCommand(ctx, write, writeHash, f.peer, f.now)
	if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, refused.Receipt) || !bytes.Equal(replay.SignedReceipt, refused.SignedReceipt) {
		t.Fatalf("stale-write terminal replay = %+v err=%v; want identical signed refusal", replay, err)
	}
	if after := f.anchor(t); after != before {
		t.Fatalf("stale-write replay changed event prefix: before=%+v after=%+v", before, after)
	}
}

func TestStrictD121RemovedDependencyReplayPreservesForeignReplacement(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() {
		if f.s != nil {
			_ = f.s.Close()
		}
	}()
	claimTestBirthStep(t, f, 31, 101)
	blocked, blocker := claimTestID(131), f.matter
	installed := f.anchor(t)
	oldClaim := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, oldClaim)
	oldProof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: oldClaim.ClaimID, ClaimEpoch: 1}}
	add := dependencyAuthorityCommand(f, 12, 4, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: oldProof}, repoA, "")
	added := completeDependencyForTest(t, f, submitDependencyForTest(t, f, add, f.peer), 105)
	oldEdge := dependencySuccess(t, f, add, added)
	remove := dependencyAuthorityCommand(f, 13, 5, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: oldProof}, repoA, "")
	removeHash := hashCommand(t, remove)
	removed := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, f.peer), 106)
	if got := dependencySuccess(t, f, remove, removed); got != oldEdge {
		t.Fatalf("original removal edge %q, want %q", got, oldEdge)
	}

	peerF := dependencyEnvironmentB(t, f)
	authorizedStandDownForStrictD121(t, f, peerF, 700, 1, 0x75, oldClaim, 1, claimTestID(51), repoA, 107)
	replacementStart := f.anchor(t)
	replacementRaw, replacementHash := f.command(t, 701, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(751),
	}, envB)
	replacementPending, err := f.s.SubmitClaimAcquire(context.Background(), replacementRaw, replacementHash, replacementStart, peerF, f.now)
	if err != nil || replacementPending.Owner == nil || !replacementPending.Pending {
		t.Fatalf("submit foreign replacement claim: status=%+v error=%v", replacementPending, err)
	}
	replacement := claimTestAllocation(2, replacementStart, 109, 110, 111)
	replacementStatus, _, err := f.s.CompleteClaimAcquire(context.Background(), replacementPending.Owner, replacement, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete foreign replacement claim: %v", err)
	}
	if receipt, readErr := readReceipt(replacementStatus.Receipt); readErr != nil || receipt.Result.Code != string(operation.ResultSucceeded) {
		t.Fatalf("foreign replacement claim result=%+v error=%v", receipt.Result, readErr)
	}
	newProof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: replacement.ClaimID, ClaimEpoch: 2}}
	replacementAdd := dependencyV2Command(f, 702, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: newProof}, repoA)
	replacementWriteStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, replacementAdd, peerF), 112)
	newEdge := dependencySuccess(t, f, replacementAdd, replacementWriteStatus)
	if newEdge == oldEdge {
		t.Fatalf("foreign replacement reused removed edge identity %s", oldEdge)
	}
	beforeReplay, projection := f.anchor(t), mustDependencyProjection(t, f)
	replay, err := f.s.SubmitCommand(context.Background(), remove, removeHash, f.peer, f.now)
	if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, removed.Receipt) || !bytes.Equal(replay.SignedReceipt, removed.SignedReceipt) {
		t.Fatalf("E replay changed original terminal receipt/range: status=%+v error=%v", replay, err)
	}
	if after := f.anchor(t); after != beforeReplay {
		t.Fatalf("E replay appended events after F replacement: before=%+v after=%+v", beforeReplay, after)
	}
	if !reflect.DeepEqual(mustDependencyProjection(t, f), projection) {
		t.Fatalf("E replay changed F's replacement edge: before=%+v after=%+v", projection, mustDependencyProjection(t, f))
	}
}

func TestStrictD121V2CompletionFailuresRollbackAndRevalidateAfterRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		boundary    string
		changeClaim bool
	}{
		{name: "signer", boundary: "signer", changeClaim: true},
		{name: "projection", boundary: "projection"},
		{name: "install", boundary: "install"},
		{name: "projection-changed-claim", boundary: "projection", changeClaim: true},
		{name: "install-changed-claim", boundary: "install", changeClaim: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			failure := scenario.name
			f := newClaimTestFixture(t)
			defer func() {
				if f.s != nil {
					_ = f.s.Close()
				}
			}()
			claimTestBirthStep(t, f, 31, 101)
			installed := f.anchor(t)
			claim := claimTestAllocation(1, installed, 102, 103, 104)
			f.acquire(t, 11, 3, installed, claim)
			proof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: claim.ClaimID, ClaimEpoch: 1}}
			command := dependencyAuthorityCommand(f, 12, 4, operation.DependencyAddV2,
				operation.DependencyAddV2Input{BlockedID: claimTestID(131), BlockerID: f.matter, TargetClaims: proof}, repoA, "")
			hash := hashCommand(t, command)
			owner := submitDependencyForTest(t, f, command, f.peer)
			before, beforeEdges := f.anchor(t), mustDependencyProjection(t, f)
			switch scenario.boundary {
			case "projection":
				if _, err := f.s.db.Exec(`CREATE TEMP TRIGGER fail_v2_dependency_projection BEFORE INSERT ON m6_dependencies BEGIN SELECT RAISE(ABORT,'injected v2 projection failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "install":
				if _, err := f.s.db.Exec(`CREATE TEMP TRIGGER fail_v2_terminal_install BEFORE INSERT ON terminal_receipts BEGIN SELECT RAISE(ABORT,'injected v2 terminal install failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			signer := signWith(f.key)
			if scenario.boundary == "signer" {
				signer = signWith(key("invalid v2 completion signer"))
			}
			if _, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
				"", claimTestID(105), f.now, signer); err == nil {
				t.Fatalf("injected %s failure committed the v2 Step 8 effect", failure)
			}
			switch scenario.boundary {
			case "projection":
				if _, err := f.s.db.Exec(`DROP TRIGGER fail_v2_dependency_projection`); err != nil {
					t.Fatal(err)
				}
			case "install":
				if _, err := f.s.db.Exec(`DROP TRIGGER fail_v2_terminal_install`); err != nil {
					t.Fatal(err)
				}
			}
			if after := f.anchor(t); after != before || !reflect.DeepEqual(mustDependencyProjection(t, f), beforeEdges) {
				t.Fatalf("%s failure retained model effects: before=%+v after=%+v", failure, before, after)
			}
			var events, receipts, candidates int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=?`, domainA).Scan(&candidates); err != nil {
				t.Fatal(err)
			}
			if events != 0 || receipts != 0 || candidates != 0 {
				t.Fatalf("%s failure retained events=%d receipts=%d candidates=%d", failure, events, receipts, candidates)
			}
			var state string
			if err := f.s.db.QueryRow(`SELECT state FROM submissions WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&state); err != nil || state != "submitted" {
				t.Fatalf("%s failure submission state=%q error=%v; want recoverable submitted command", failure, state, err)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenExisting(f.root)
			if err != nil {
				t.Fatalf("reopen after %s rollback: %v", failure, err)
			}
			f.s = reopened
			recovered, err := f.s.RecoverCommand(context.Background(), command, hash)
			if err != nil {
				t.Fatalf("recover v2 command after %s rollback: %v", failure, err)
			}
			if scenario.changeClaim {
				peerF := dependencyEnvironmentB(t, f)
				authorizedStandDownForStrictD121(t, f, peerF, 700, 1, 0x76, claim, 1, claimTestID(51), repoA, 105)
				replacementStart := f.anchor(t)
				raw, replacementHash := f.command(t, 701, 2, "claim.acquire", nil, map[string]any{
					"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
					"requested_dispatch_id": claimTestID(751),
				}, envB)
				pending, acquireErr := f.s.SubmitClaimAcquire(context.Background(), raw, replacementHash, replacementStart, peerF, f.now)
				if acquireErr != nil || pending.Owner == nil {
					t.Fatalf("submit replacement claim after rollback: status=%+v error=%v", pending, acquireErr)
				}
				replacement := claimTestAllocation(2, replacementStart, 107, 108, 109)
				if _, _, acquireErr = f.s.CompleteClaimAcquire(context.Background(), pending.Owner, replacement, f.now, signWith(f.key)); acquireErr != nil {
					t.Fatalf("complete replacement claim after rollback: %v", acquireErr)
				}
				before, edges := f.anchor(t), mustDependencyProjection(t, f)
				stale, staleErr := f.s.CompleteCommand(context.Background(), recovered, operation.Result{Code: operation.ResultSucceeded},
					"", claimTestID(110), f.now, signWith(f.key))
				if staleErr != nil {
					t.Fatalf("complete stale v2 command after recovery: %v", staleErr)
				}
				dependencyRefusal(t, f, command, stale, "refusal.claim-fenced", before, edges)
			} else {
				completed, completeErr := f.s.CompleteCommand(context.Background(), recovered, operation.Result{Code: operation.ResultSucceeded},
					"", claimTestID(105), f.now, signWith(f.key))
				if completeErr != nil {
					t.Fatalf("retry recovered v2 command after %s rollback: %v", failure, completeErr)
				}
				dependencySuccess(t, f, command, completed)
			}
		})
	}
}

func TestStrictD121CrossMatterCrossRepoDependencyRequiresBothExactClaims(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	secondMatter := claimTestID(21)
	f.clone = claimTestID(23)
	f.worktree = claimTestID(22)
	installed := f.anchor(t)
	firstAllocation := claimTestAllocation(1, installed, 110, 111, 112)
	f.acquire(t, 30, 4, installed, firstAllocation)
	installed = f.anchor(t)
	secondCommandID := claimTestID(31)
	secondRaw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": secondCommandID,
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": envA, "sequence": uint64(5)},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": secondCommandID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": repoB, "clone_id": f.clone, "worktree_id": f.worktree},
		"claim":     nil,
		"input": map[string]any{
			"matter_id": secondMatter, "worktree_id": f.worktree,
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(71),
		},
		"blobs": []any{},
	})
	secondHash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), secondRaw...))
	secondStatus, err := f.s.SubmitClaimAcquire(context.Background(), secondRaw, secondHash, installed, f.peer, f.now)
	if err != nil || secondStatus.Owner == nil {
		t.Fatalf("submit second Matter claim: %+v error=%v", secondStatus, err)
	}
	secondAllocation := claimTestAllocation(2, installed, 113, 114, 115)
	secondCompleted, _, err := f.s.CompleteClaimAcquire(context.Background(), secondStatus.Owner, secondAllocation, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete second Matter claim: %v", err)
	}
	secondReceipt, err := readReceipt(secondCompleted.Receipt)
	if err != nil || secondReceipt.Result.Code != string(operation.ResultSucceeded) {
		var problem any
		if secondReceipt.Result.Problem != nil {
			problem = *secondReceipt.Result.Problem
		}
		t.Fatalf("second Matter claim was not granted: %+v problem=%v error=%v", secondReceipt.Result, problem, err)
	}
	proof := []operation.TargetClaim{
		{MatterID: f.matter, ClaimID: firstAllocation.ClaimID, ClaimEpoch: 1},
		{MatterID: secondMatter, ClaimID: secondAllocation.ClaimID, ClaimEpoch: 1},
	}
	command := dependencyAuthorityCommand(f, 32, 6, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: secondMatter, TargetClaims: proof}, repoC, "")
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 116)
	edge := dependencySuccess(t, f, command, status)
	if edge == "" {
		t.Fatal("cross-Repo success lacks edge identity")
	}

	remove := dependencyAuthorityCommand(f, 33, 7, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: f.matter, BlockerID: secondMatter, TargetClaims: proof}, repoB, "")
	removed := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, f.peer), 117)
	dependencySuccess(t, f, remove, removed)
	reverse := dependencyAuthorityCommand(f, 34, 8, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: secondMatter, BlockerID: f.matter, TargetClaims: proof}, repoC, "")
	reverseStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, reverse, f.peer), 118)
	if got := dependencySuccess(t, f, reverse, reverseStatus); got == "" {
		t.Fatal("reverse endpoint order did not retain its edge")
	}

	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyAuthorityCommand(f, 35, 9, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{
			BlockedID: secondMatter, BlockerID: f.matter,
			TargetClaims: proof[:1],
		}, repoB, "")
	refused := completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, f.peer), 119)
	dependencyRefusal(t, f, missing, refused, "refusal.claim-fenced", before, edges)

	wrongAssociation := []operation.TargetClaim{
		{MatterID: f.matter, ClaimID: secondAllocation.ClaimID, ClaimEpoch: 1},
		{MatterID: secondMatter, ClaimID: firstAllocation.ClaimID, ClaimEpoch: 1},
	}
	wrong := dependencyAuthorityCommand(f, 36, 10, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: secondMatter, BlockerID: f.matter, TargetClaims: wrongAssociation}, repoA, "")
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	wrongStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, wrong, f.peer), 120)
	dependencyRefusal(t, f, wrong, wrongStatus, "refusal.claim-fenced", before, edges)

	wrongEpoch := []operation.TargetClaim{
		proof[0], {MatterID: secondMatter, ClaimID: secondAllocation.ClaimID, ClaimEpoch: 2},
	}
	stale := dependencyAuthorityCommand(f, 37, 11, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: secondMatter, BlockerID: f.matter, TargetClaims: wrongEpoch}, repoA, "")
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	staleStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, stale, f.peer), 121)
	dependencyRefusal(t, f, stale, staleStatus, "refusal.claim-fenced", before, edges)

	foreignDomain := dependencyAuthorityCommand(f, 38, 12, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: domainB, TargetClaims: proof}, repoC, "")
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	foreignStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, foreignDomain, f.peer), 122)
	dependencyRefusal(t, f, foreignDomain, foreignStatus, "refusal.dependency-endpoint", before, edges)
}

func acquireStrictD121ClaimAtEpoch(t *testing.T, f *claimTestFixture, peerF tls.ConnectionState,
	matter, repo string, targetEpoch uint64, sequence, standDownSequence *uint64, allocationIndex, eventIndex *int,
) operation.TargetClaim {
	t.Helper()
	var latest AcquireAllocation
	for epoch := uint64(1); epoch <= targetEpoch; epoch++ {
		installed := f.anchor(t)
		index := *allocationIndex
		*allocationIndex++
		firstEvent := *eventIndex
		*eventIndex += 3
		allocation := claimTestAllocation(index, installed, firstEvent, firstEvent+1, firstEvent+2)
		commandID := 2200 + index
		raw := encodeTest(t, map[string]any{
			"schema": "wipd.command/1", "command_id": claimTestID(commandID),
			"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
			"environment": map[string]any{"id": envA, "sequence": *sequence},
			"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
			"causation_command_id": nil, "correlation_command_id": claimTestID(commandID),
			"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
			"context":   map[string]any{"repo_id": repo, "clone_id": f.clone, "worktree_id": f.worktree},
			"claim":     nil,
			"input": map[string]any{
				"matter_id": matter, "worktree_id": f.worktree,
				"dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(2300 + index),
			},
			"blobs": []any{},
		})
		hash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
		pending, err := f.s.SubmitClaimAcquire(context.Background(), raw, hash, installed, f.peer, f.now)
		if err != nil || pending.Owner == nil || !pending.Pending {
			t.Fatalf("submit epoch-%d claim for %s: status=%+v error=%v", epoch, matter, pending, err)
		}
		if _, _, err = f.s.CompleteClaimAcquire(context.Background(), pending.Owner, allocation, f.now, signWith(f.key)); err != nil {
			t.Fatalf("complete epoch-%d claim for %s: %v", epoch, matter, err)
		}
		latest = allocation
		*sequence++
		if epoch == targetEpoch {
			break
		}
		standDownID := 2400 + index
		authorizedStandDownForStrictD121(t, f, peerF, standDownID, *standDownSequence,
			byte(index), allocation, epoch, claimTestID(2300+index), repo, *eventIndex)
		*eventIndex += 2
		*standDownSequence++
	}
	return operation.TargetClaim{MatterID: matter, ClaimID: latest.ClaimID, ClaimEpoch: targetEpoch}
}

func strictD121CreateStep(t *testing.T, f *claimTestFixture, commandID, sequence, stepID, eventID, parentCommandID int, matter, repo string) {
	t.Helper()
	command := step12Command(f, commandID, uint64(sequence), operation.StepCreateV1,
		operation.StepCreateInput{ParentID: matter, Title: fmt.Sprintf("Step %d", stepID)}, "")
	command.CausationCommandID = claimTestID(parentCommandID)
	command.CorrelationCommandID = claimTestID(parentCommandID)
	command.Request.Context.Repo = repo
	command.Request.Claim = &operation.ClaimContext{ID: matter, Epoch: "1"}
	status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit cross-Matter Step %s: status=%+v error=%v", claimTestID(stepID), status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
		ParentID: matter, Title: fmt.Sprintf("Step %d", stepID),
	}}
	if _, err = f.s.CompleteCommand(context.Background(), status.Owner, result, claimTestID(stepID), claimTestID(eventID), f.now, signWith(f.key)); err != nil {
		t.Fatalf("complete cross-Matter Step %s: %v", claimTestID(stepID), err)
	}
}

func strictD121ForeignDomainClaim(t *testing.T, store *Store, now time.Time) operation.TargetClaim {
	t.Helper()
	ctx := context.Background()
	domain, owner := identity(domainB, 7)
	repo, matter, environment := claimTestID(980), claimTestID(981), envB
	if err := store.BootstrapDomain(ctx, domain, repo); err != nil {
		t.Fatalf("bootstrap foreign proof domain: %v", err)
	}
	artifactKey := key("strict-d121-foreign-proof-artifact")
	artifactID, _ := spkiID(artifactKey.Public())
	artifactCertificate := signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", domainB, domain.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainB, "authority_epoch": uint64(7), "key_generation": uint64(1), "key_id": artifactID,
		"ed25519_public_key": []byte(artifactKey.Public().(ed25519.PublicKey)), "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err := store.RegisterArtifactKey(ctx, domainB, artifactCertificate, now); err != nil {
		t.Fatalf("register foreign proof artifact key: %v", err)
	}
	caKey := key("strict-d121-foreign-proof-ca")
	caDER := caFixture(t, caKey, now)
	caID, _ := spkiID(caKey.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainB, domain.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainB, "authority_epoch": uint64(7), "owner_key_id": domain.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := store.InstallEnvironmentCA(ctx, domainB, delegation, now); err != nil {
		t.Fatalf("install foreign proof Environment CA: %v", err)
	}
	leafKey := key("strict-d121-foreign-proof-environment")
	leafDER := leafFixture(t, leafKey, caKey, caDER, domainB, environment, domain.OwnerKeyID, 7, now, 982)
	peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leafDER), mustCert(t, caDER)}}
	grant := grantFixture(t, owner, domain, "environment-enroll", claimTestID(983), environment, leafKey, 0x32)
	if _, err := store.IssueEnvironmentCertificate(ctx, domainB, environment, grant, csrFixture(t, leafKey, "Foreign proof Environment"), [][]byte{leafDER, caDER}, now); err != nil {
		t.Fatalf("enroll foreign proof Environment: %v", err)
	}
	birth := operation.Command{
		ID: claimTestID(984), AuthorityDomainID: domainB, ExpectedAuthorityEpoch: 7,
		EnvironmentID: environment, EnvironmentSequence: 1, ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: claimTestID(984),
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repo}, Input: operation.MatterCreateInput{Title: "Foreign proof target", Locator: "foreign-proof"},
		},
	}
	birthStatus, err := store.SubmitCommand(ctx, birth, hashCommand(t, birth), peer, now)
	if err != nil || birthStatus.Owner == nil {
		t.Fatalf("submit foreign proof Matter: status=%+v error=%v", birthStatus, err)
	}
	if _, err = store.CompleteCommand(ctx, birthStatus.Owner, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: matter, Title: "Foreign proof target", Locator: "foreign-proof"},
	}, matter,
		claimTestID(985), now, signWith(artifactKey)); err != nil {
		t.Fatalf("complete foreign proof Matter: %v", err)
	}
	anchor, err := store.CurrentPrefixAnchor(ctx, domainB)
	if err != nil {
		t.Fatal(err)
	}
	acquireID := claimTestID(986)
	raw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": acquireID,
		"authority":   map[string]any{"domain_id": domainB, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": environment, "sequence": uint64(2)},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": acquireID,
		"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
		"context":   map[string]any{"repo_id": repo, "clone_id": claimTestID(987), "worktree_id": claimTestID(988)},
		"claim":     nil, "input": map[string]any{
			"matter_id": matter, "worktree_id": claimTestID(988),
			"dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(989),
		}, "blobs": []any{},
	})
	hash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
	pending, err := store.SubmitClaimAcquire(ctx, raw, hash, anchor, peer, now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit foreign-domain target claim: status=%+v error=%v", pending, err)
	}
	allocation := AcquireAllocation{
		ClaimID: claimTestID(990), BatchID: claimTestID(991), GrantID: claimTestID(992),
		SnapshotID: claimTestID(993), JournalID: claimTestID(994), Installed: anchor,
		EventIDs: []string{claimTestID(995), claimTestID(996), claimTestID(997)},
	}
	if _, _, err = store.CompleteClaimAcquire(ctx, pending.Owner, allocation, now, signWith(artifactKey)); err != nil {
		t.Fatalf("complete foreign-domain target claim: %v", err)
	}
	return operation.TargetClaim{MatterID: matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}
}

func TestStrictD121CrossMatterStepProofIsolation(t *testing.T) {
	s, root, peer, key, now := commandFixture(t)
	for _, repo := range []string{repoB, repoC} {
		if err := s.AttachRepo(context.Background(), domainA, repo); err != nil {
			t.Fatal(err)
		}
	}
	f := &claimTestFixture{s: s, root: root, peer: peer, key: key, now: now, matter: claimTestID(20)}
	defer func() { _ = f.s.Close() }()
	f.clone, f.worktree = claimTestID(23), claimTestID(22)
	for index, repo := range []string{repoA, repoB, repoC} {
		matterID, commandID := claimTestID(20+index), claimTestID(10+index)
		command := matterCommand(commandID, uint64(2*index+1), "alpha")
		command.Request.Context.Repo = repo
		status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), f.peer, f.now)
		if err != nil || status.Owner == nil {
			t.Fatalf("submit cross-Matter proof target: status=%+v error=%v", status, err)
		}
		if _, err = f.s.CompleteCommand(context.Background(), status.Owner, operation.Result{
			Code:   operation.ResultSucceeded,
			Output: operation.MatterCreateOutput{ID: matterID, Title: "A title", Locator: "alpha"},
		}, matterID,
			claimTestID(100+2*index), f.now, signWith(f.key)); err != nil {
			t.Fatalf("complete cross-Matter proof target: %v", err)
		}
		if index < 2 {
			strictD121CreateStep(t, f, 40+index, 2*index+2, 140+index, 101+2*index, 10+index, matterID, repo)
		}
	}
	peerF := dependencyEnvironmentB(t, f)
	sequence, standDownSequence, allocationIndex, eventIndex := uint64(6), uint64(1), 1, 600
	m1 := acquireStrictD121ClaimAtEpoch(t, f, peerF, f.matter, repoA, 2, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
	m2 := acquireStrictD121ClaimAtEpoch(t, f, peerF, claimTestID(21), repoB, 2, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
	m3 := acquireStrictD121ClaimAtEpoch(t, f, peerF, claimTestID(22), repoC, 1, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
	foreign := strictD121ForeignDomainClaim(t, f.s, f.now)
	proof := []operation.TargetClaim{m1, m2}
	closed := []operation.TargetClaim{
		{MatterID: f.matter, ClaimID: claimTestAllocation(1, PrefixAnchor{}).ClaimID, ClaimEpoch: 1},
		{MatterID: claimTestID(21), ClaimID: claimTestAllocation(3, PrefixAnchor{}).ClaimID, ClaimEpoch: 1},
	}
	stepEndpoints := [][2]string{{claimTestID(140), claimTestID(141)}, {claimTestID(141), claimTestID(140)}}
	for _, endpoints := range stepEndpoints {
		for _, verb := range []string{"add", "remove"} {
			definition := operation.DependencyAddV2
			var input operation.Input = operation.DependencyAddV2Input{
				BlockedID: endpoints[0], BlockerID: endpoints[1], TargetClaims: proof,
			}
			if verb == "remove" {
				definition = operation.DependencyRemoveV2
				input = operation.DependencyRemoveV2Input{
					BlockedID: endpoints[0], BlockerID: endpoints[1], TargetClaims: proof,
				}
			}
			command := dependencyAuthorityCommand(f, 2500+eventIndex, sequence, definition, input, repoC, "")
			sequence++
			beforeEdges := mustDependencyProjection(t, f)
			var candidatesBefore int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=?`, domainA).Scan(&candidatesBefore); err != nil {
				t.Fatal(err)
			}
			status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 850+eventIndex)
			edgeID := dependencySuccess(t, f, command, status)
			receipt, err := readReceipt(status.Receipt)
			if err != nil || receipt.Range == nil {
				t.Fatalf("read cross-Matter Step receipt range: %+v error=%v", receipt, err)
			}
			eventID := receipt.Range.First
			if verb == "add" {
				want := append(append([]dependencyEdge(nil), beforeEdges...), dependencyEdge{
					domainA, edgeID, repoC, endpoints[0], endpoints[1], eventID, eventID, "",
				})
				sort.Slice(want, func(i, j int) bool {
					return ownerKey(want[i].domain, want[i].id) < ownerKey(want[j].domain, want[j].id)
				})
				if got := mustDependencyProjection(t, f); !reflect.DeepEqual(got, want) {
					t.Fatalf("cross-Matter Step add projection = %+v, want %+v", got, want)
				}
			} else {
				want := append([]dependencyEdge(nil), beforeEdges...)
				for index := range want {
					if want[index].id == edgeID {
						want[index].last, want[index].tombstone = eventID, eventID
					}
				}
				if got := mustDependencyProjection(t, f); !reflect.DeepEqual(got, want) {
					t.Fatalf("cross-Matter Step remove projection = %+v, want %+v", got, want)
				}
			}
			var candidatesAfter int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=?`, domainA).Scan(&candidatesAfter); err != nil || candidatesAfter != candidatesBefore {
				t.Fatalf("dependency write changed unrelated tracker candidates: before=%d after=%d error=%v", candidatesBefore, candidatesAfter, err)
			}
			eventIndex++
		}
	}
	for _, invalidation := range []struct {
		name string
		make func(int) operation.TargetClaim
	}{
		{name: "random-claim-id", make: func(endpoint int) operation.TargetClaim {
			claim := proof[endpoint]
			claim.ClaimID = claimTestID(970 + endpoint)
			return claim
		}},
		{name: "foreign-domain-claim", make: func(endpoint int) operation.TargetClaim {
			claim := proof[endpoint]
			claim.ClaimID = foreign.ClaimID
			claim.ClaimEpoch = foreign.ClaimEpoch
			return claim
		}},
		{name: "another-matter-m3-claim", make: func(endpoint int) operation.TargetClaim {
			claim := proof[endpoint]
			claim.ClaimID = m3.ClaimID
			claim.ClaimEpoch = m3.ClaimEpoch
			return claim
		}},
		{name: "closed-claim", make: func(endpoint int) operation.TargetClaim { return closed[endpoint] }},
	} {
		for endpoint := range proof {
			for _, verb := range []string{"add", "remove"} {
				badProof := append([]operation.TargetClaim(nil), proof...)
				badProof[endpoint] = invalidation.make(endpoint)
				definition := operation.DependencyAddV2
				var input operation.Input = operation.DependencyAddV2Input{BlockedID: claimTestID(140), BlockerID: claimTestID(141), TargetClaims: badProof}
				if verb == "remove" {
					definition = operation.DependencyRemoveV2
					input = operation.DependencyRemoveV2Input{BlockedID: claimTestID(140), BlockerID: claimTestID(141), TargetClaims: badProof}
				}
				command := dependencyAuthorityCommand(f, 50+eventIndex, sequence, definition, input, repoC, "")
				sequence++
				before, edges := f.anchor(t), mustDependencyProjection(t, f)
				status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 800+eventIndex)
				dependencyRefusal(t, f, command, status, "refusal.claim-fenced", before, edges)
				eventIndex++
			}
		}
	}
}

func TestStrictD121CrossMatterAsymmetricEpochProofMatrix(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	f.clone, f.worktree = claimTestID(23), claimTestID(22)
	peerF := dependencyEnvironmentB(t, f)
	sequence, standDownSequence, allocationIndex, eventIndex := uint64(4), uint64(1), 1, 500
	first := acquireStrictD121ClaimAtEpoch(t, f, peerF, f.matter, repoA, 3, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
	secondMatter := claimTestID(21)
	second := acquireStrictD121ClaimAtEpoch(t, f, peerF, secondMatter, repoB, 8, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
	if first.ClaimEpoch != 3 || second.ClaimEpoch != 8 {
		t.Fatalf("claim epochs = %d/%d, want asymmetric 3/8", first.ClaimEpoch, second.ClaimEpoch)
	}
	proof := []operation.TargetClaim{first, second}
	commandID, eventID := 2500, eventIndex
	for _, endpoints := range [][2]string{{f.matter, secondMatter}, {secondMatter, f.matter}} {
		blocked, blocker := endpoints[0], endpoints[1]
		add := dependencyAuthorityCommand(f, commandID, sequence, operation.DependencyAddV2,
			operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoA, "")
		sequence++
		commandID++
		added := completeDependencyForTest(t, f, submitDependencyForTest(t, f, add, f.peer), eventID)
		edge := dependencySuccess(t, f, add, added)
		eventID++
		remove := dependencyAuthorityCommand(f, commandID, sequence, operation.DependencyRemoveV2,
			operation.DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoB, "")
		sequence++
		commandID++
		removed := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, f.peer), eventID)
		if got := dependencySuccess(t, f, remove, removed); got != edge {
			t.Fatalf("remove in endpoint order %s -> %s tombstoned %s, want %s", blocked, blocker, got, edge)
		}
		eventID++
	}

	for _, verb := range []string{"add", "remove"} {
		for endpoint := range proof {
			for _, invalidation := range []string{"omitted", "stale-epoch"} {
				badProof := append([]operation.TargetClaim(nil), proof...)
				if invalidation == "omitted" {
					badProof = append(badProof[:endpoint], badProof[endpoint+1:]...)
				} else {
					badProof[endpoint].ClaimEpoch++
				}
				blocked, blocker := f.matter, secondMatter
				if endpoint == 1 {
					blocked, blocker = secondMatter, f.matter
				}
				definition := operation.DependencyAddV2
				var input operation.Input = operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: badProof}
				if verb == "remove" {
					definition = operation.DependencyRemoveV2
					input = operation.DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: badProof}
				}
				command := dependencyAuthorityCommand(f, commandID, sequence, definition, input, repoA, "")
				sequence++
				commandID++
				before, edges := f.anchor(t), mustDependencyProjection(t, f)
				status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), eventID)
				dependencyRefusal(t, f, command, status, "refusal.claim-fenced", before, edges)
				eventID++
			}
		}
	}
}

func TestStrictD121ForeignOwnedLiveTargetClaimIsAdmittedRefusal(t *testing.T) {
	for _, foreignIndex := range []int{0, 1} {
		for _, verb := range []string{"add", "remove"} {
			t.Run(fmt.Sprintf("%s/M%d-foreign", verb, foreignIndex+1), func(t *testing.T) {
				f := newDependencyCrossRepoHistoryFixture(t)
				defer func() { _ = f.s.Close() }()
				f.clone, f.worktree = claimTestID(23), claimTestID(22)
				m1, m2 := f.matter, claimTestID(21)
				var proof []operation.TargetClaim
				var peerF tls.ConnectionState
				var eOwned, fOwned operation.TargetClaim
				if foreignIndex == 0 {
					var foreignClaims []operation.TargetClaim
					peerF, foreignClaims = acquireStep8TargetClaims(t, f, m1)
					fOwned = foreignClaims[0]
					installed := f.anchor(t)
					allocation := claimTestAllocation(10, installed, 590, 591, 592)
					eOwned = acquireStrictD121EOwnedTargetClaim(t, f, 13, 4, m2, repoB, allocation)
				} else {
					var foreignClaims []operation.TargetClaim
					peerF, foreignClaims = acquireStep8TargetClaims(t, f, m2)
					fOwned = foreignClaims[0]
					installed := f.anchor(t)
					allocation := claimTestAllocation(10, installed, 590, 591, 592)
					eOwned = acquireStrictD121EOwnedTargetClaim(t, f, 13, 4, m1, repoA, allocation)
				}
				if foreignIndex == 0 {
					proof = []operation.TargetClaim{fOwned, eOwned}
				} else {
					proof = []operation.TargetClaim{eOwned, fOwned}
				}
				foreignOwner := envB
				if foreignIndex != 0 {
					assertStrictD121StoredTargetClaim(t, f, proof[0], envA)
					assertStrictD121StoredTargetClaim(t, f, proof[1], foreignOwner)
				} else {
					assertStrictD121StoredTargetClaim(t, f, proof[0], foreignOwner)
					assertStrictD121StoredTargetClaim(t, f, proof[1], envA)
				}
				definition := operation.DependencyAddV2
				var input operation.Input = operation.DependencyAddV2Input{BlockedID: m1, BlockerID: m2, TargetClaims: proof}
				if verb == "remove" {
					definition = operation.DependencyRemoveV2
					input = operation.DependencyRemoveV2Input{BlockedID: m1, BlockerID: m2, TargetClaims: proof}
				}
				command := dependencyAuthorityCommand(f, 14, 5, definition, input, repoA, "")
				before, edges := f.anchor(t), mustDependencyProjection(t, f)
				var membershipsBefore, candidatesBefore int
				if err := f.s.db.QueryRow(`SELECT count(*) FROM repo_memberships WHERE domain_id=?`, domainA).Scan(&membershipsBefore); err != nil {
					t.Fatal(err)
				}
				if err := f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=?`, domainA).Scan(&candidatesBefore); err != nil {
					t.Fatal(err)
				}
				status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), f.peer, f.now)
				if err != nil || status.Owner == nil {
					t.Fatalf("foreign-owner refusal did not admit: status=%+v error=%v", status, err)
				}
				status = completeDependencyForTest(t, f, status.Owner, 600)
				dependencyRefusal(t, f, command, status, "refusal.claim-fenced", before, edges)
				var state string
				if err = f.s.db.QueryRow(`SELECT state FROM submissions WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&state); err != nil || state != "terminal" {
					t.Fatalf("E-authored command against F-owned live target was not admitted terminally: state=%q error=%v", state, err)
				}
				var membershipsAfter, candidatesAfter int
				if err = f.s.db.QueryRow(`SELECT count(*) FROM repo_memberships WHERE domain_id=?`, domainA).Scan(&membershipsAfter); err != nil || membershipsAfter != membershipsBefore {
					t.Fatalf("claim-fenced refusal changed Repo memberships: before=%d after=%d error=%v", membershipsBefore, membershipsAfter, err)
				}
				if err = f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=?`, domainA).Scan(&candidatesAfter); err != nil || candidatesAfter != candidatesBefore {
					t.Fatalf("claim-fenced refusal changed tracker candidates: before=%d after=%d error=%v", candidatesBefore, candidatesAfter, err)
				}
				if len(peerF.PeerCertificates) == 0 {
					t.Fatal("foreign owner Environment was not authenticated")
				}
			})
		}
	}
}

func TestStrictD121MalformedProofAndAdmissionFailuresLeaveNoSubmissionOrReceipt(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerF, valid := acquireStep8TargetClaims(t, f, f.matter, claimTestID(21))
	duplicate := []operation.TargetClaim{valid[0], valid[0]}
	noncanonical := []operation.TargetClaim{valid[1], valid[0]}
	malformed := []operation.TargetClaim{{MatterID: "not-a-matter-id", ClaimID: claimTestID(30), ClaimEpoch: 1}, valid[1]}
	foreignCAKey := key("strict-d121-untrusted-human-ca")
	foreignCADER := caFixture(t, foreignCAKey, f.now)
	foreignHumanKey := key("strict-d121-foreign-human")
	d, _ := identity(domainA, 7)
	foreignHumanDER := leafFixture(t, foreignHumanKey, foreignCAKey, foreignCADER, domainA, envB, d.OwnerKeyID, 7, f.now, 991)
	foreignHuman := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{
		mustCert(t, foreignHumanDER), mustCert(t, foreignCADER),
	}}
	tests := []struct {
		name             string
		claims           []operation.TargetClaim
		peer             tls.ConnectionState
		wantErr          error
		mutate           func(*operation.Command)
		invalidCanonical bool
	}{
		{name: "malformed", claims: malformed, peer: peerF, invalidCanonical: true},
		{name: "duplicate", claims: duplicate, peer: peerF, invalidCanonical: true},
		{name: "noncanonical-order", claims: noncanonical, peer: peerF, invalidCanonical: true},
		{name: "wrong-authenticated-environment", claims: valid, peer: f.peer, wantErr: ErrFenced},
		{name: "foreign-human-owner-tls", claims: valid, peer: foreignHuman, wantErr: ErrFenced},
		{
			name: "wrong-domain-envelope", claims: valid, peer: peerF, wantErr: ErrNotFound,
			mutate: func(command *operation.Command) { command.AuthorityDomainID = domainB },
		},
		{
			name: "wrong-authority-epoch", claims: valid, peer: peerF, wantErr: ErrFenced,
			mutate: func(command *operation.Command) { command.ExpectedAuthorityEpoch++ },
		},
	}
	for index, test := range tests {
		command := dependencyV2Command(f, 2800+index, 3, operation.DependencyAddV2,
			operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: claimTestID(21), TargetClaims: test.claims}, repoA)
		if test.mutate != nil {
			test.mutate(&command)
		}
		hash, hashErr := command.RequestHash()
		if test.invalidCanonical {
			if hashErr == nil {
				t.Fatalf("%s proof unexpectedly has canonical request identity", test.name)
			}
		} else if hashErr != nil {
			t.Fatalf("%s request hash: %v", test.name, hashErr)
		}
		if !test.invalidCanonical {
			if _, err := f.s.SubmitCommand(context.Background(), command, hash, test.peer, f.now); !errors.Is(err, test.wantErr) {
				t.Fatalf("%s rejection = %v, want %v", test.name, err, test.wantErr)
			}
		}
		var submissions, receipts int
		if err := f.s.db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&submissions); err != nil {
			t.Fatal(err)
		}
		if err := f.s.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		if submissions != 0 || receipts != 0 {
			t.Fatalf("%s pre-admission rejection retained submissions=%d receipts=%d", test.name, submissions, receipts)
		}
	}
}

func TestStrictD121CrossMatterPendingWritesFenceM2ChangesAndCloseOnly(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		endpointRev bool
		replacement bool
	}{
		{name: "M1-blocked-M2-blocker-replacement", replacement: true},
		{name: "M2-blocked-M1-blocker-replacement", endpointRev: true, replacement: true},
		{name: "M1-blocked-M2-blocker-close-only"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newDependencyCrossRepoHistoryFixture(t)
			defer func() {
				if f.s != nil {
					_ = f.s.Close()
				}
			}()
			f.clone, f.worktree = claimTestID(23), claimTestID(22)
			peerF := dependencyEnvironmentB(t, f)
			sequence, standDownSequence, allocationIndex, eventIndex := uint64(4), uint64(1), 1, 500
			m1 := acquireStrictD121ClaimAtEpoch(t, f, peerF, f.matter, repoA, 1, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
			matter2 := claimTestID(21)
			m2 := acquireStrictD121ClaimAtEpoch(t, f, peerF, matter2, repoB, 1, &sequence, &standDownSequence, &allocationIndex, &eventIndex)
			blocked, blocker := f.matter, matter2
			if scenario.endpointRev {
				blocked, blocker = matter2, f.matter
			}
			command := dependencyAuthorityCommand(f, 2600, sequence, operation.DependencyAddV2,
				operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: []operation.TargetClaim{m1, m2}}, repoA, "")
			hash := hashCommand(t, command)
			pending := submitDependencyForTest(t, f, command, f.peer)
			sequence++
			authorizedStandDownForStrictD121(t, f, peerF, 2700, standDownSequence, 0x7a,
				claimTestAllocation(2, f.anchor(t)), m2.ClaimEpoch, claimTestID(2302), repoB, eventIndex)
			eventIndex += 2
			if scenario.replacement {
				installed := f.anchor(t)
				commandID := 2701
				raw := encodeTest(t, map[string]any{
					"schema": "wipd.command/1", "command_id": claimTestID(commandID),
					"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
					"environment": map[string]any{"id": envB, "sequence": uint64(2)},
					"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
					"causation_command_id": nil, "correlation_command_id": claimTestID(commandID),
					"operation": map[string]any{"name": "claim.acquire", "version": uint64(1)},
					"context":   map[string]any{"repo_id": repoB, "clone_id": f.clone, "worktree_id": f.worktree},
					"claim":     nil,
					"input": map[string]any{
						"matter_id": matter2, "worktree_id": f.worktree,
						"dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(2710),
					},
					"blobs": []any{},
				})
				acquireHash := digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
				acquirePending, err := f.s.SubmitClaimAcquire(context.Background(), raw, acquireHash, installed, peerF, f.now)
				if err != nil || acquirePending.Owner == nil {
					t.Fatalf("submit F replacement for M2: status=%+v error=%v", acquirePending, err)
				}
				replacement := claimTestAllocation(99, installed, eventIndex, eventIndex+1, eventIndex+2)
				if _, _, err = f.s.CompleteClaimAcquire(context.Background(), acquirePending.Owner, replacement, f.now, signWith(f.key)); err != nil {
					t.Fatalf("complete F replacement for M2: %v", err)
				}
				eventIndex += 3
			}
			before, edges := f.anchor(t), mustDependencyProjection(t, f)
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenExisting(f.root)
			if err != nil {
				t.Fatalf("reopen after M2 claim change: %v", err)
			}
			f.s = reopened
			recovered, err := f.s.RecoverCommand(context.Background(), command, hash)
			if err != nil {
				t.Fatalf("recover pending cross-Matter write: %v", err)
			}
			refused, err := f.s.CompleteCommand(context.Background(), recovered, operation.Result{Code: operation.ResultSucceeded},
				"", claimTestID(2800), f.now, signWith(f.key))
			if err != nil {
				t.Fatalf("complete stale cross-Matter write: %v", err)
			}
			dependencyRefusal(t, f, command, refused, "refusal.claim-fenced", before, edges)
			var openM1, openM2 int
			if err = f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=? AND matter_id=? AND close_command_id IS NULL AND claim_id=?`, domainA, f.matter, m1.ClaimID).Scan(&openM1); err != nil {
				t.Fatal(err)
			}
			if openM1 != 1 {
				t.Fatalf("M1 exact claim changed during the race: open claim count=%d", openM1)
			}
			if scenario.replacement {
				if err = f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=? AND matter_id=? AND owner_environment_id=? AND claim_epoch=2 AND close_command_id IS NULL`, domainA, matter2, envB).Scan(&openM2); err != nil || openM2 != 1 {
					t.Fatalf("M2 replacement claim not retained: count=%d error=%v", openM2, err)
				}
			} else if err = f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=? AND matter_id=? AND claim_id=? AND close_command_id IS NULL`, domainA, matter2, m2.ClaimID).Scan(&openM2); err != nil || openM2 != 0 {
				t.Fatalf("close-only M2 ordering retained old claim: count=%d error=%v", openM2, err)
			}
			if after := f.anchor(t); after != before {
				t.Fatalf("terminal refusal after claim change appended events: before=%+v after=%+v", before, after)
			}
			_ = pending
		})
	}
}

func TestStrictD121PendingWriteBeforeFirstClaimAcquisitionRefuses(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	claimTestBirthStep(t, f, 31, 101)
	command := dependencyAuthorityCommand(f, 12, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: claimTestID(131), BlockerID: f.matter}, repoA, "")
	owner := submitDependencyForTest(t, f, command, f.peer)
	_, acquired := acquireStep8TargetClaims(t, f, f.matter)
	if len(acquired) != 1 || acquired[0].ClaimEpoch != 1 {
		t.Fatalf("independent Environment did not acquire the first target claim: %+v", acquired)
	}
	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	status, err := f.s.CompleteCommand(context.Background(), owner, operation.Result{Code: operation.ResultSucceeded},
		"", claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete admitted no-prior-claim command after later acquire: %v", err)
	}
	dependencyRefusal(t, f, command, status, "refusal.claim-fenced", before, edges)
}

func TestDependencyAuthorityDirectionDiamondDuplicateAndMultiHopCycle(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	fourth := claimTestID(23)
	birth := matterCommand(claimTestID(13), 4, "delta")
	birth.Request.Context.Repo = repoB
	status, err := f.s.SubmitCommand(context.Background(), birth, hashCommand(t, birth), f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("fourth Matter admission: %+v %v", status, err)
	}
	if _, err = f.s.CompleteCommand(context.Background(), status.Owner, success(fourth, "delta"), fourth, claimTestID(103), f.now, signWith(f.key)); err != nil {
		t.Fatal(err)
	}
	nodes := []string{claimTestID(20), claimTestID(21), claimTestID(22), fourth}
	peerB, claims := acquireStep8TargetClaims(t, f, nodes...)
	for index, edge := range [][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}} {
		blocked, blocker := nodes[edge[0]], nodes[edge[1]]
		proof := dependencyClaimsForMatterIDs(claims, blocked, blocker)
		command := dependencyV2Command(f, 14+index, uint64(5+index), operation.DependencyAddV2,
			operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof},
			[]string{repoC, repoB, repoA, repoC}[index])
		status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, peerB), 600+index)
		edgeID := dependencySuccess(t, f, command, status)
		if !ulid.MatchString(edgeID) {
			t.Fatalf("authority assigned invalid edge identity %q", edgeID)
		}
	}
	// The diamond is acyclic in the intended blocked->blocker direction. Adding
	// D->A closes the multi-hop A->B->D path and must refuse without effects.
	before, beforeEdges := f.anchor(t), mustDependencyProjection(t, f)
	cycle := dependencyV2Command(f, 18, 9, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: fourth, BlockerID: nodes[0], TargetClaims: dependencyClaimsForMatterIDs(claims, fourth, nodes[0])}, repoB)
	cycleStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, cycle, peerB), 604)
	dependencyRefusal(t, f, cycle, cycleStatus, "refusal.dependency-cycle", before, beforeEdges)
	before, beforeEdges = f.anchor(t), mustDependencyProjection(t, f)
	duplicate := dependencyV2Command(f, 19, 10, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: nodes[0], BlockerID: nodes[1], TargetClaims: dependencyClaimsForMatterIDs(claims, nodes[0], nodes[1])}, repoA)
	duplicateStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, duplicate, peerB), 605)
	dependencyRefusal(t, f, duplicate, duplicateStatus, "refusal.dependency-exists", before, beforeEdges)
}

func TestDependencyAuthoritySelfEdgeReachesTerminalRefusal(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerB, claims := acquireStep8TargetClaims(t, f, f.matter, claimTestID(21))
	command := dependencyV2Command(f, 13, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: f.matter, TargetClaims: claims[:1]}, repoC)
	if err := operation.DependencyAddV2.ValidateRequest(command.Request); err != nil {
		t.Fatalf("self-edge must reach authority refusal: %v", err)
	}
	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, peerB), 103)
	dependencyRefusal(t, f, command, status, "refusal.dependency-cycle", before, edges)

	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyV2Command(f, 14, 4, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: claimTestID(20), BlockerID: claimTestID(21), TargetClaims: claims}, repoB)
	status = completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, peerB), 104)
	dependencyRefusal(t, f, missing, status, "refusal.dependency-missing", before, edges)
}

func TestDependencyAuthorityOppositeAddsCommitAtMostOne(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerB, claims := acquireStep8TargetClaims(t, f, claimTestID(20), claimTestID(21))
	left, right := claimTestID(20), claimTestID(21)
	proof := dependencyClaimsForMatterIDs(claims, left, right)
	first := dependencyV2Command(f, 13, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: left, BlockerID: right, TargetClaims: proof}, repoC)
	second := dependencyV2Command(f, 14, 4, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: right, BlockerID: left, TargetClaims: proof}, repoB)
	firstStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, first, peerB), 600)
	dependencySuccess(t, f, first, firstStatus)
	secondStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, second, peerB), 601)
	secondReceipt, err := readReceipt(secondStatus.Receipt)
	if err != nil || secondReceipt.Result.Code != string(operation.ResultRefused) || secondReceipt.Result.Problem == nil ||
		*secondReceipt.Result.Problem != "refusal.dependency-cycle" || secondReceipt.Range != nil || secondReceipt.Result.Output != nil {
		t.Fatalf("opposite-edge loser receipt = %+v error=%v", secondReceipt.Result, err)
	}
	var events int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND event_id IN (?,?)`, domainA, claimTestID(600), claimTestID(601)).Scan(&events); err != nil || events != 1 {
		t.Fatalf("opposite-edge event count=%d error=%v", events, err)
	}
}

func TestDependencyAuthorityForeignDomainRefusesWithoutDisclosure(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerB, claims := acquireStep8TargetClaims(t, f, f.matter)
	foreign := createForeignDependencyMatter(t)
	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	command := dependencyV2Command(f, 13, 2, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: foreign, TargetClaims: claims}, repoA)
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, peerB), 600)
	dependencyRefusal(t, f, command, status, "refusal.dependency-endpoint", before, edges)
	receipt, _ := readReceipt(status.Receipt)
	if bytes.Contains(receipt.Result.Output, []byte(foreign)) || bytes.Contains([]byte(*receipt.Result.Problem), []byte(foreign)) {
		t.Fatalf("foreign endpoint was disclosed: %+v", receipt.Result)
	}
	// A non-existent same-domain ID receives the same opaque endpoint refusal.
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyV2Command(f, 14, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: f.matter, BlockerID: claimTestID(999), TargetClaims: claims}, repoA)
	missingStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, peerB), 601)
	dependencyRefusal(t, f, missing, missingStatus, "refusal.dependency-endpoint", before, edges)
	missingReceipt, err := readReceipt(missingStatus.Receipt)
	if err != nil || missingReceipt.Result.Problem == nil || *missingReceipt.Result.Problem != *receipt.Result.Problem {
		t.Fatalf("foreign and absent endpoint refusals differ: foreign=%+v missing=%+v err=%v", receipt.Result, missingReceipt.Result, err)
	}
}

func TestDependencyAuthorityRemoveReaddReplayAndReopen(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() {
		if f.s != nil {
			_ = f.s.Close()
		}
	}()
	peerB, claims := acquireStep8TargetClaims(t, f, claimTestID(20), claimTestID(21))
	blocked, blocker := claimTestID(20), claimTestID(21)
	proof := dependencyClaimsForMatterIDs(claims, blocked, blocker)
	add := dependencyV2Command(f, 13, 3, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoC)
	addStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, add, peerB), 600)
	firstEdge := dependencySuccess(t, f, add, addStatus)
	remove := dependencyV2Command(f, 14, 4, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoB)
	removeStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, peerB), 601)
	if removedEdge := dependencySuccess(t, f, remove, removeStatus); removedEdge != firstEdge {
		t.Fatalf("remove event did not tombstone exact edge %s: got %s", firstEdge, removedEdge)
	}
	addAgain := dependencyV2Command(f, 15, 5, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoA)
	addAgainStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, addAgain, peerB), 602)
	secondEdge := dependencySuccess(t, f, addAgain, addAgainStatus)
	if secondEdge == firstEdge {
		t.Fatalf("re-add reused tombstoned edge identity %s", firstEdge)
	}

	replayed, err := f.s.SubmitCommand(context.Background(), remove, hashCommand(t, remove), peerB, f.now)
	if err != nil || replayed.Pending || !bytes.Equal(replayed.Receipt, removeStatus.Receipt) || !bytes.Equal(replayed.SignedReceipt, removeStatus.SignedReceipt) {
		t.Fatalf("remove replay = %+v error=%v", replayed, err)
	}
	projection := mustDependencyProjection(t, f)
	var activeReplacement bool
	for _, edge := range projection {
		if edge.id == secondEdge && edge.blocked == blocked && edge.blocker == blocker && edge.tombstone == "" {
			activeReplacement = true
		}
	}
	if !activeReplacement {
		t.Fatalf("replayed old removal removed replacement edge: %+v", projection)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("dependency reopen: %v", err)
	}
	for _, original := range []struct {
		command operation.Command
		status  CommandStatus
	}{{add, addStatus}, {remove, removeStatus}} {
		status, queryErr := f.s.QueryCommand(context.Background(), domainA, original.command.ID,
			hashCommand(t, original.command), 7, peerB, envB, f.now)
		if queryErr != nil || !bytes.Equal(status.Receipt, original.status.Receipt) || !bytes.Equal(status.SignedReceipt, original.status.SignedReceipt) {
			t.Fatalf("reopen terminal parity for %s: %+v %v", original.command.ID, status, queryErr)
		}
	}
	if got := mustDependencyProjection(t, f); !reflect.DeepEqual(got, projection) {
		t.Fatalf("reopened dependency projection = %+v, want %+v", got, projection)
	}
}

func TestDependencyAuthorityIgnoresTombstonedEndpointAndNeverUsesClaimJournal(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	claimTestBirthStep(t, f, 31, 101)
	step := claimTestID(131)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 103, 104, 105)
	f.acquire(t, 11, 3, anchor, allocation)
	proof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}}
	// A Step and its parent Matter share one Matter, but the dependency remains
	// authority-delivered and is not written into that Matter's claim journal.
	add := dependencyAuthorityCommand(f, 12, 4, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: step, BlockerID: f.matter, TargetClaims: proof}, repoA, "")
	addStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, add, f.peer), 106)
	edgeID := dependencySuccess(t, f, add, addStatus)
	removeStep := step12Command(f, 13, 5, operation.StepRemoveV1,
		operation.StepRemoveInput{StepID: step, Reason: "obsolete"}, allocation.ClaimID)
	completeStep12ClaimCommand(t, f, removeStep, allocation.JournalID, 1, 131, 107)
	projection := mustDependencyProjection(t, f)
	retained := false
	for _, edge := range projection {
		if edge.id == edgeID && edge.tombstone == "" {
			retained = true
		}
	}
	if !retained {
		t.Fatalf("endpoint tombstone unexpectedly rewrote retained edge history: %+v", projection)
	}
	before := f.anchor(t)
	addDead := dependencyAuthorityCommand(f, 14, 6, operation.DependencyAddV2,
		operation.DependencyAddV2Input{BlockedID: step, BlockerID: f.matter, TargetClaims: proof}, repoA, "")
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, addDead, f.peer), 108)
	dependencyRefusal(t, f, addDead, status, "refusal.dependency-endpoint", before, projection)
	if err := operation.DependencyAddV1.ValidateResult(operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.DependencyOutput{EdgeID: edgeID, BlockedID: step, BlockerID: f.matter},
	}); err != nil {
		t.Fatalf("dependency add output contract: %v", err)
	}
	before = f.anchor(t)
	removeDead := dependencyAuthorityCommand(f, 15, 7, operation.DependencyRemoveV2,
		operation.DependencyRemoveV2Input{BlockedID: step, BlockerID: f.matter, TargetClaims: proof}, repoA, "")
	removeDeadStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, removeDead, f.peer), 109)
	dependencyRefusal(t, f, removeDead, removeDeadStatus, "refusal.dependency-endpoint", before, projection)
	commandWithClaim := dependencyAuthorityCommand(f, 16, 8, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: step, BlockerID: f.matter}, repoA, "")
	commandWithClaim.Request.Claim = &operation.ClaimContext{ID: f.matter, Epoch: "1"}
	if _, err := commandWithClaim.CanonicalBytes(); err == nil {
		t.Fatal("dependency request with Matter claim context was canonicalized")
	}
}

func TestDependencyPendingAdmissionRecoversAfterReopen(t *testing.T) {
	for _, scenario := range []struct {
		name, operation string
	}{
		{name: "add-commits", operation: "add"},
		{name: "remove-commits", operation: "remove"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newDependencyCrossRepoHistoryFixture(t)
			defer func() {
				if f.s != nil {
					_ = f.s.Close()
				}
			}()
			ctx := context.Background()
			blocked, blocker := claimTestID(20), claimTestID(21)
			peerB, claims := acquireStep8TargetClaims(t, f, blocked, blocker)
			proof := dependencyClaimsForMatterIDs(claims, blocked, blocker)
			sequence := uint64(3)
			if scenario.operation == "remove" {
				add := dependencyV2Command(f, 13, sequence, operation.DependencyAddV2,
					operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}, repoC)
				addStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, add, peerB), 600)
				dependencySuccess(t, f, add, addStatus)
				sequence++
			}
			id, repo := 13, repoC
			definition := operation.DependencyAddV2
			var input operation.Input = operation.DependencyAddV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}
			if scenario.operation == "remove" {
				id, repo = 14, repoB
				definition = operation.DependencyRemoveV2
				input = operation.DependencyRemoveV2Input{BlockedID: blocked, BlockerID: blocker, TargetClaims: proof}
			}
			command := dependencyV2Command(f, id, sequence, definition, input, repo)
			hash := hashCommand(t, command)
			pending, err := f.s.SubmitCommand(ctx, command, hash, peerB, f.now)
			if err != nil || !pending.Pending || pending.Owner == nil {
				t.Fatalf("dependency admission: %+v %v", pending, err)
			}
			canonical, err := command.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			var stored []byte
			var storedHash, state string
			var events, receipts int
			if err = f.s.db.QueryRow(`SELECT command,request_hash,state,
			(SELECT count(*) FROM authority_events WHERE domain_id=s.domain_id AND command_id=s.command_id),
			(SELECT count(*) FROM terminal_receipts WHERE domain_id=s.domain_id AND command_id=s.command_id)
			FROM submissions s WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&stored, &storedHash, &state, &events, &receipts); err != nil ||
				!bytes.Equal(stored, canonical) || storedHash != hash || state != "submitted" || events != 0 || receipts != 0 {
				t.Fatalf("pending admission state: state=%q events=%d receipts=%d hash=%q error=%v", state, events, receipts, storedHash, err)
			}
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("reopen pending %s: %v", scenario.operation, err)
			}
			var reopenedBytes []byte
			if err = f.s.db.QueryRow(`SELECT command FROM submissions WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&reopenedBytes); err != nil || !bytes.Equal(reopenedBytes, canonical) {
				t.Fatalf("reopened command bytes changed: %v", err)
			}
			recovered, err := f.s.RecoverCommand(ctx, command, hash)
			if err != nil {
				t.Fatalf("recover exact pending command: %v", err)
			}
			event := 601
			terminal, err := f.s.CompleteCommand(ctx, recovered, operation.Result{Code: operation.ResultSucceeded},
				"", claimTestID(event), f.now, signWith(f.key))
			if err != nil || terminal.Pending || len(terminal.Receipt) == 0 {
				t.Fatalf("complete recovered dependency command: %+v %v", terminal, err)
			}
			dependencySuccess(t, f, command, terminal)
			replay, err := f.s.SubmitCommand(ctx, command, hash, peerB, f.now)
			if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, terminal.Receipt) || !bytes.Equal(replay.SignedReceipt, terminal.SignedReceipt) {
				t.Fatalf("terminal replay parity for recovered %s: %+v %v", scenario.operation, replay, err)
			}
			if err = f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("reopen terminal %s: %v", scenario.operation, err)
			}
			queried, err := f.s.QueryCommand(ctx, domainA, command.ID, hash, 7, peerB, envB, f.now)
			if err != nil || !bytes.Equal(queried.Receipt, terminal.Receipt) || !bytes.Equal(queried.SignedReceipt, terminal.SignedReceipt) {
				t.Fatalf("reopened receipt parity for %s: %+v %v", scenario.operation, queried, err)
			}
		})
	}
}

func TestDependencyReopenRejectsPendingSubmissionWithEvent(t *testing.T) {
	f, other := newDependencyHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	command := dependencyAuthorityCommand(f, 12, 3, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: f.matter, BlockerID: other}, repoA, "")
	owner := submitDependencyForTest(t, f, command, f.peer)
	if owner == nil {
		t.Fatal("dependency submission returned no owner")
	}
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = appendCommandEvent(context.Background(), tx, eventIdentity{
		domain: domainA, id: command.ID, hash: hashCommand(t, command), environment: envA,
		sequence: command.EnvironmentSequence, actedAt: command.ActedAt, repo: repoA,
	}, f.now, claimTestID(102), "dependency.added", f.matter,
		map[string]any{"edge": claimTestID(90), "blocker": other})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExisting(f.root)
	if store != nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("reopen accepted a pending dependency with an event: %v", err)
	}
}

func TestDependencyReopenRejectsPendingSubmissionOutsideAdmissionContract(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	command := dependencyAuthorityCommand(f, 13, 4, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: claimTestID(20), BlockerID: claimTestID(21)}, repoC, "")
	command.Request.Context.Clone = claimTestID(30)
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	identity := commandIdentity{
		domain: command.AuthorityDomainID, epoch: command.ExpectedAuthorityEpoch, environment: command.EnvironmentID,
		sequence: command.EnvironmentSequence, id: command.ID, name: command.Request.Operation.Name,
		version: uint64(command.Request.Operation.Version), repo: command.Request.Context.Repo,
		encoded: raw, hash: hashCommand(t, command), m1: &command,
	}
	if status, submitErr := f.s.submitIdentity(context.Background(), identity, f.peer, f.now, nil, nil, nil); submitErr != nil || !status.Pending {
		t.Fatalf("prepare malformed pending dependency: %+v %v", status, submitErr)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExisting(f.root)
	if store != nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("reopen accepted a dependency context forbidden at admission: %v", err)
	}
}

func mustDependencyProjection(t *testing.T, f *claimTestFixture) []dependencyEdge {
	t.Helper()
	edges, err := readDependencyProjection(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	return edges
}

func dependencyEnvironmentB(t *testing.T, f *claimTestFixture) tls.ConnectionState {
	t.Helper()
	d, owner := identity(domainA, 7)
	caKey := key("step4-ca")
	caDER := caFixture(t, caKey, f.now)
	leafKey := key("dependency-environment-b")
	leaf := leafFixture(t, leafKey, caKey, caDER, domainA, envB, d.OwnerKeyID, 7, f.now, 171)
	grant := grantFixture(t, owner, d, "environment-enroll", claimTestID(172), envB, leafKey, 171)
	if _, err := f.s.IssueEnvironmentCertificate(context.Background(), domainA, envB, grant,
		csrFixture(t, leafKey, "Dependency Environment B"), [][]byte{leaf, caDER}, f.now); err != nil {
		t.Fatal(err)
	}
	return tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
}

func createForeignDependencyMatter(t *testing.T) string {
	t.Helper()
	s, _ := fresh(t)
	defer func() { _ = s.Close() }()
	d, owner := identity(domainB, 7)
	repo := claimTestID(900)
	ctx := context.Background()
	if err := s.BootstrapDomain(ctx, d, repo); err != nil {
		t.Fatalf("bootstrap foreign domain: %v", err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	artifactKey := key("foreign-dependency-artifact")
	keyID, _ := spkiID(artifactKey.Public())
	artifactCert := signedTest(t, owner, "authority-artifact-key", "wipd.authority-artifact-key/1", domainB, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainB, "authority_epoch": uint64(7), "key_generation": uint64(1), "key_id": keyID,
		"ed25519_public_key": []byte(artifactKey.Public().(ed25519.PublicKey)), "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.RegisterArtifactKey(ctx, domainB, artifactCert, now); err != nil {
		t.Fatalf("register foreign artifact key: %v", err)
	}
	caKey := key("foreign-dependency-ca")
	caDER := caFixture(t, caKey, now)
	caID, _ := spkiID(caKey.Public())
	delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainB, d.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.environment-ca-delegation/1", "domain_id": domainB, "authority_epoch": uint64(7), "owner_key_id": d.OwnerKeyID,
		"ca_generation": uint64(1), "ca_key_id": caID, "ca_certificate_der": caDER,
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
	})
	if err := s.InstallEnvironmentCA(ctx, domainB, delegation, now); err != nil {
		t.Fatalf("install foreign environment CA: %v", err)
	}
	leafKey := key("foreign-dependency-environment")
	leaf := leafFixture(t, leafKey, caKey, caDER, domainB, envB, d.OwnerKeyID, 7, now, 173)
	grant := grantFixture(t, owner, d, "environment-enroll", claimTestID(174), envB, leafKey, 173)
	peer := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
	if _, err := s.IssueEnvironmentCertificate(ctx, domainB, envB, grant,
		csrFixture(t, leafKey, "Foreign Dependency Environment"), [][]byte{leaf, caDER}, now); err != nil {
		t.Fatalf("enroll foreign environment: %v", err)
	}
	command := operation.Command{
		ID: claimTestID(901), AuthorityDomainID: domainB, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envB, EnvironmentSequence: 1, ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: claimTestID(901),
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repo}, Input: operation.MatterCreateInput{Title: "A title", Locator: "foreign-matter"},
		},
	}
	status, err := s.SubmitCommand(ctx, command, hashCommand(t, command), peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("foreign Matter admission: %+v %v", status, err)
	}
	foreignMatter := claimTestID(902)
	if _, err = s.CompleteCommand(ctx, status.Owner, success(foreignMatter, "foreign-matter"), foreignMatter, claimTestID(100), now, signWith(artifactKey)); err != nil {
		t.Fatalf("complete foreign Matter: %v", err)
	}
	return foreignMatter
}
