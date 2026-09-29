package authoritystore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func TestContentWritePromotesOnlySuccessfulTerminalFold(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 101, 102, 103)
	f.acquire(t, 11, 2, installed, allocation)

	stage := func(content []byte) string {
		t.Helper()
		digest := digestBytes(content)
		if _, err := f.s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), f.now); err != nil {
			t.Fatalf("start staged content %s: %v", digest, err)
		}
		if offset, err := f.s.StageBlobChunk(ctx, domainA, 7, digest, 0, content); err != nil || offset != uint64(len(content)) {
			t.Fatalf("stage content %s at %d: %v", digest, offset, err)
		}
		if err := f.s.FinishBlob(ctx, domainA, 7, digest); err != nil {
			t.Fatalf("verify staged content %s: %v", digest, err)
		}
		return digest
	}
	command := func(id int, sequence uint64, blobDigest string, size int64) operation.Command {
		commandID := claimTestID(id)
		return operation.Command{
			ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
			EnvironmentID: envA, EnvironmentSequence: sequence, ActedAt: "2026-09-23T11:59:00Z",
			CorrelationCommandID: commandID,
			Request: operation.Request{
				Operation: operation.ContentWriteOnceV1.Metadata().Operation, Actor: "human",
				Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
				Claim:   &operation.ClaimContext{ID: allocation.ClaimID, Epoch: "1"},
				Input:   operation.ContentWriteInput{SubjectID: f.matter, Kind: "brief"},
				Blobs:   []operation.BlobInput{{Name: "content", Digest: blobDigest, Size: size}},
			},
		}
	}
	complete := func(command operation.Command, contentID, eventID string) (CommandStatus, string) {
		t.Helper()
		hash, err := command.RequestHash()
		if err != nil {
			t.Fatal(err)
		}
		pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
		if err != nil || pending.Owner == nil || !pending.Pending {
			t.Fatalf("submit %s: status=%+v err=%v", command.ID, pending, err)
		}
		result := operation.Result{Code: operation.ResultSucceeded, Output: operation.ContentSegmentOutput{
			ID: contentID, SubjectID: f.matter, Kind: "brief", BlobDigest: command.Request.Blobs[0].Digest,
			ByteLength: command.Request.Blobs[0].Size,
		}}
		status, err := f.s.CompleteCommand(ctx, pending.Owner, result, contentID, eventID, f.now, signWith(f.key))
		if err != nil {
			t.Fatalf("complete %s: %v", command.ID, err)
		}
		return status, hash
	}

	acceptedBytes := []byte("first durable brief\n")
	acceptedDigest := stage(acceptedBytes)
	acceptedCommand := command(12, 3, acceptedDigest, int64(len(acceptedBytes)))
	accepted, acceptedHash := complete(acceptedCommand, claimTestID(130), claimTestID(104))
	claimTestReceipt(t, accepted, "result.succeeded", map[string]any{
		"id": claimTestID(130), "subject_id": f.matter, "kind": "brief",
		"blob_digest": acceptedDigest, "byte_length": uint64(len(acceptedBytes)),
	}, 104)
	var err error
	var acceptedSubmission storedSubmission
	if err = f.s.db.QueryRow(`SELECT domain_id,command_id,request_hash,environment_id,operation_name,state,command,epoch,environment_sequence,operation_version
		FROM submissions WHERE domain_id=? AND command_id=?`, domainA, acceptedCommand.ID).Scan(
		&acceptedSubmission.domain, &acceptedSubmission.id, &acceptedSubmission.hash, &acceptedSubmission.env,
		&acceptedSubmission.operation, &acceptedSubmission.state, &acceptedSubmission.command,
		&acceptedSubmission.epoch, &acceptedSubmission.seq, &acceptedSubmission.version); err != nil {
		t.Fatal(err)
	}
	var acceptedEventID string
	var acceptedEvent []byte
	if err = f.s.db.QueryRow(`SELECT event_id,record FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, acceptedCommand.ID).
		Scan(&acceptedEventID, &acceptedEvent); err != nil {
		t.Fatal(err)
	}
	var changedEvent map[string]any
	if err = artifactDecoder.Unmarshal(acceptedEvent, &changedEvent); err != nil {
		t.Fatal(err)
	}
	changedEvent["acted_at"] = "2026-09-23T11:59:01Z"
	changedRecord, err := artifactEncoder.Marshal(changedEvent)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err = validContentEventRecord(changedRecord, domainA, acceptedEventID, acceptedSubmission); err == nil {
		t.Fatal("content event with acted_at differing from its immutable command was accepted")
	}

	refusedBytes := []byte("verified but unreferenced duplicate\n")
	refusedDigest := stage(refusedBytes)
	refusedCommand := command(13, 4, refusedDigest, int64(len(refusedBytes)))
	refused, refusedHash := complete(refusedCommand, claimTestID(131), claimTestID(105))
	claimTestReceipt(t, refused, "result.refused", nil)
	refusalReceipt, err := readReceipt(refused.Receipt)
	if err != nil || refusalReceipt.Result.Problem == nil || *refusalReceipt.Result.Problem != "refusal.content-exists" || refusalReceipt.Range != nil {
		t.Fatalf("create-once refusal receipt = %+v, %v", refusalReceipt, err)
	}
	replay, err := f.s.SubmitCommand(ctx, refusedCommand, refusedHash, f.peer, f.now)
	if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, refused.Receipt) {
		t.Fatalf("exact refusal replay changed receipt: %+v, %v", replay, err)
	}
	queried, err := f.s.QueryCommand(ctx, domainA, refusedCommand.ID, refusedHash, 7, f.peer, envA, f.now)
	if err != nil || queried.Pending || !bytes.Equal(queried.Receipt, refused.Receipt) {
		t.Fatalf("refusal receipt query changed result: %+v, %v", queried, err)
	}

	var acceptedSegments, refusedSegments, acceptedReferences, refusedReferences, acceptedPosition uint64
	if err = f.s.db.QueryRow(`SELECT count(*) FROM content_segments WHERE domain_id=? AND blob_digest=?`, domainA, acceptedDigest).Scan(&acceptedSegments); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM content_segments WHERE domain_id=? AND blob_digest=?`, domainA, refusedDigest).Scan(&refusedSegments); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*),coalesce(max(first_position),0) FROM blob_references WHERE domain_id=? AND digest=?`, domainA, acceptedDigest).
		Scan(&acceptedReferences, &acceptedPosition); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM blob_references WHERE domain_id=? AND digest=?`, domainA, refusedDigest).Scan(&refusedReferences); err != nil {
		t.Fatal(err)
	}
	if acceptedSegments != 1 || acceptedReferences != 1 || acceptedPosition != 5 || refusedSegments != 0 || refusedReferences != 0 {
		t.Fatalf("content rows/references accepted=(%d,%d@%d) refused=(%d,%d)",
			acceptedSegments, acceptedReferences, acceptedPosition, refusedSegments, refusedReferences)
	}

	snapshot, err := f.s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), claimTestID(200), f.now, time.Minute)
	if err != nil || len(snapshot.Manifest.Entries) != 1 || snapshot.Manifest.Entries[0].Digest != acceptedDigest ||
		snapshot.Manifest.Entries[0].ByteLength != uint64(len(acceptedBytes)) {
		t.Fatalf("only accepted content is in the promoted manifest: %+v, %v", snapshot.Manifest, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen authority after accepted/refused content: %v", err)
	}
	status, err := f.s.QueryCommand(ctx, domainA, acceptedCommand.ID, acceptedHash, 7, f.peer, envA, f.now)
	if err != nil || status.Pending || !bytes.Equal(status.Receipt, accepted.Receipt) {
		t.Fatalf("accepted content receipt after reopen: %+v, %v", status, err)
	}
	status, err = f.s.QueryCommand(ctx, domainA, refusedCommand.ID, refusedHash, 7, f.peer, envA, f.now)
	if err != nil || status.Pending || !bytes.Equal(status.Receipt, refused.Receipt) {
		t.Fatalf("refused content receipt after reopen: %+v, %v", status, err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM blob_references WHERE domain_id=? AND digest=?`, domainA, refusedDigest).Scan(&refusedReferences); err != nil || refusedReferences != 0 {
		t.Fatalf("refused blob was promoted after reopen: references=%d err=%v", refusedReferences, err)
	}
}

func TestClaimBlobClosureIncludesContentOnDirectSubtreeStep(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, _, step := claimTestBirthStep(t, f, 31, 101)
	if step.Pending || step.Owner != nil {
		t.Fatalf("birth Step did not complete: %+v", step)
	}
	stepID := claimTestID(131)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 103, 104, 105)
	if _, _, acquired, _ := f.acquire(t, 11, 3, installed, allocation); acquired.Pending || acquired.Owner != nil {
		t.Fatalf("claim acquisition did not complete: %+v", acquired)
	}

	content := []byte("a Step finding required by the next claim\n")
	digest := digestBytes(content)
	if _, err := f.s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), f.now); err != nil {
		t.Fatalf("start staged finding: %v", err)
	}
	if offset, err := f.s.StageBlobChunk(ctx, domainA, 7, digest, 0, content); err != nil || offset != uint64(len(content)) {
		t.Fatalf("stage finding at %d: %v", offset, err)
	}
	if err := f.s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatalf("verify staged finding: %v", err)
	}
	command := operation.Command{
		ID: claimTestID(12), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 4,
		ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: claimTestID(12),
		Request: operation.Request{
			Operation: operation.FindingAppendV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: allocation.ClaimID, Epoch: "1"},
			Input:   operation.FindingAppendInput{SubjectID: stepID},
			Blobs:   []operation.BlobInput{{Name: "content", Digest: digest, Size: int64(len(content))}},
		},
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("submit Step finding: %+v %v", pending, err)
	}
	contentID := claimTestID(132)
	if _, err = f.s.CompleteCommand(ctx, pending.Owner, operation.Result{Code: operation.ResultSucceeded, Output: operation.ContentSegmentOutput{
		ID: contentID, SubjectID: stepID, Kind: "findings", BlobDigest: digest, ByteLength: int64(len(content)),
	}}, contentID, claimTestID(106), f.now, signWith(f.key)); err != nil {
		t.Fatalf("complete Step finding: %v", err)
	}
	second := command
	second.ID = claimTestID(13)
	second.EnvironmentSequence = 5
	second.CorrelationCommandID = second.ID
	secondHash, err := second.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	secondPending, err := f.s.SubmitCommand(ctx, second, secondHash, f.peer, f.now)
	if err != nil || secondPending.Owner == nil || !secondPending.Pending {
		t.Fatalf("submit repeated Step finding: %+v %v", secondPending, err)
	}
	secondContentID := claimTestID(133)
	secondCompleted, err := f.s.CompleteCommand(ctx, secondPending.Owner, operation.Result{Code: operation.ResultSucceeded, Output: operation.ContentSegmentOutput{
		ID: secondContentID, SubjectID: stepID, Kind: "findings", BlobDigest: digest, ByteLength: int64(len(content)),
	}}, secondContentID, claimTestID(107), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete repeated Step finding: %v", err)
	}
	claimTestReceipt(t, secondCompleted, "result.succeeded", map[string]any{
		"id": secondContentID, "subject_id": stepID, "kind": "findings", "blob_digest": digest,
		"byte_length": uint64(len(content)),
	}, 107)

	anchor := f.anchor(t)
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := subtreeRequiredBlobClosureTx(ctx, tx, domainA, f.matter, anchor.EventCount)
	_ = tx.Rollback()
	if err != nil || len(closure) != 1 || closure[0] != digest {
		t.Fatalf("direct Matter claim blob closure = %v, %v; want the Step finding %s", closure, err, digest)
	}
	var firstReference, firstEvent, secondEvent uint64
	if err = f.s.db.QueryRow(`SELECT first_position FROM blob_references WHERE domain_id=? AND digest=?`, domainA, digest).Scan(&firstReference); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT position FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, claimTestID(12)).Scan(&firstEvent); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT position FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, second.ID).Scan(&secondEvent); err != nil {
		t.Fatal(err)
	}
	if firstReference != firstEvent || firstReference >= secondEvent {
		t.Fatalf("reused digest reference position=%d, first content event=%d, second content event=%d", firstReference, firstEvent, secondEvent)
	}
	snapshot, err := f.s.PinSnapshot(ctx, domainA, 7, emptyAnchor(), claimTestID(201), f.now, time.Minute)
	if err != nil || len(snapshot.Manifest.Entries) != 1 || snapshot.Manifest.Entries[0].Digest != digest || snapshot.Manifest.Entries[0].ByteLength != uint64(len(content)) {
		t.Fatalf("reused digest snapshot manifest = %+v, %v", snapshot.Manifest, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen authority after repeated-digest content: %v", err)
	}
	pulled, rangeDigest, err := f.s.BlobRange(ctx, domainA, 7, snapshot.ID, snapshot.Manifest.Digest, digest, uint64(len(content)), 0, uint64(len(content)), f.now)
	if err != nil || !bytes.Equal(pulled, content) || rangeDigest != digestBytes(content) {
		t.Fatalf("pull reused digest after reopen: bytes=%q digest=%s err=%v", pulled, rangeDigest, err)
	}
}

func TestContentCommandUsesCurrentJournalGenerationAfterRepair(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 101, 102, 103)
	_, _, acquired, _ := f.acquire(t, 11, 2, installed, allocation)
	if acquired.Pending {
		t.Fatal("claim acquisition did not complete")
	}
	claim := map[string]any{"id": allocation.ClaimID, "epoch": uint64(1)}
	returned, returnedHash := f.command(t, 12, 3, "cursor.move", claim, map[string]any{"target_id": nil})
	if err := f.s.AppendClaimJournalEntry(ctx, allocation.JournalID, 1, returned, returnedHash); err != nil {
		t.Fatalf("append repairable journal head: %v", err)
	}
	repair, repairHash := f.command(t, 13, 3, "claim.journal-repair", claim, map[string]any{
		"journal_id": allocation.JournalID, "head_position": uint64(1),
		"head_command_id": claimTestID(12), "head_request_hash": returnedHash,
		"action": map[string]any{"kind": "abandon", "proof": map[string]any{
			"terminal_receipt": nil, "not_submitted_proof": "same-epoch-receipt-not-found",
		}},
	})
	pending, err := f.s.SubmitClaimLifecycle(ctx, repair, repairHash, f.peer, f.now, nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit real journal repair: %+v %v", pending, err)
	}
	newJournal := claimTestID(90)
	repaired, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, newJournal, []string{claimTestID(104)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete real journal repair: %v", err)
	}
	claimTestReceipt(t, repaired, "result.succeeded", map[string]any{
		"claim_id": allocation.ClaimID, "archived_journal_id": allocation.JournalID,
		"new_journal_id": newJournal, "action": "abandon",
	}, 104)
	var oldState, currentState string
	if err = f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, allocation.JournalID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, newJournal).Scan(&currentState); err != nil {
		t.Fatal(err)
	}
	if oldState != "quarantined" || currentState != "open" {
		t.Fatalf("repair generations: archived=%s current=%s", oldState, currentState)
	}

	content := []byte("content after journal repair\n")
	digest := digestBytes(content)
	if _, err = f.s.StartBlob(ctx, domainA, 7, digest, uint64(len(content)), f.now); err != nil {
		t.Fatal(err)
	}
	if offset, stageErr := f.s.StageBlobChunk(ctx, domainA, 7, digest, 0, content); stageErr != nil || offset != uint64(len(content)) {
		t.Fatalf("stage content after repair at %d: %v", offset, stageErr)
	}
	if err = f.s.FinishBlob(ctx, domainA, 7, digest); err != nil {
		t.Fatal(err)
	}
	commandID := claimTestID(14)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 4, ActedAt: "2026-09-23T11:59:00Z",
		CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.FindingAppendV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: allocation.ClaimID, Epoch: "1"},
			Input:   operation.FindingAppendInput{SubjectID: f.matter},
			Blobs:   []operation.BlobInput{{Name: "content", Digest: digest, Size: int64(len(content))}},
		},
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err = f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("content submission after journal repair: %+v %v", pending, err)
	}
	contentID := claimTestID(134)
	completed, err := f.s.CompleteCommand(ctx, pending.Owner, operation.Result{Code: operation.ResultSucceeded, Output: operation.ContentSegmentOutput{
		ID: contentID, SubjectID: f.matter, Kind: "findings", BlobDigest: digest, ByteLength: int64(len(content)),
	}}, contentID, claimTestID(105), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete content after journal repair: %v", err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"id": contentID, "subject_id": f.matter, "kind": "findings", "blob_digest": digest,
		"byte_length": uint64(len(content)),
	}, 105)
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen after repaired-generation content: %v", err)
	}
}
