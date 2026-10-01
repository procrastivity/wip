package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

// The protocol-1 controls are deliberately built independently of the M1
// operation catalogue. In particular, the asserted hash includes its domain
// separator and the exact canonical bytes supplied to the store.
func claimTestID(n int) string { return fmt.Sprintf("%026d", n) }

type claimTestFixture struct {
	s                       *Store
	root                    string
	peer                    tls.ConnectionState
	key                     ed25519.PrivateKey
	now                     time.Time
	matter, worktree, clone string
}

func newClaimTestFixture(t *testing.T) *claimTestFixture {
	t.Helper()
	s, root, peer, key, now := commandFixture(t)
	f := &claimTestFixture{
		s: s, root: root, peer: peer, key: key, now: now,
		matter: claimTestID(20), worktree: claimTestID(21), clone: claimTestID(22),
	}
	c := matterCommand(claimTestID(10), 1, "alpha")
	status, err := s.SubmitCommand(context.Background(), c, hashCommand(t, c), peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("create Matter submission: %+v %v", status, err)
	}
	if _, err = s.CompleteCommand(context.Background(), status.Owner, success(f.matter, "alpha"), f.matter, claimTestID(100), now, signWith(key)); err != nil {
		t.Fatalf("create Matter completion: %v", err)
	}
	return f
}

func (f *claimTestFixture) command(t *testing.T, id int, sequence uint64, name string, claim any, input any, acting ...string) ([]byte, string) {
	t.Helper()
	environment := envA
	if len(acting) != 0 {
		environment = acting[0]
	}
	var clone, worktree any
	if name != "claim.stand-down" {
		clone, worktree = f.clone, f.worktree
	}
	raw := encodeTest(t, map[string]any{
		"schema": "wipd.command/1", "command_id": claimTestID(id),
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": environment, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": claimTestID(id),
		"operation": map[string]any{"name": name, "version": uint64(1)},
		"context":   map[string]any{"repo_id": repoA, "clone_id": clone, "worktree_id": worktree},
		"claim":     claim, "input": input, "blobs": []any{},
	})
	return raw, digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
}

func (f *claimTestFixture) anchor(t *testing.T) PrefixAnchor {
	t.Helper()
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	a, err := currentAnchor(context.Background(), tx, domainA)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *claimTestFixture) acquire(t *testing.T, id int, sequence uint64, installed PrefixAnchor, allocation AcquireAllocation) ([]byte, string, CommandStatus, ClaimGrant) {
	t.Helper()
	raw, hash := f.command(t, id, sequence, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(40 + id),
	})
	status, err := f.s.SubmitClaimAcquire(context.Background(), raw, hash, installed, f.peer, f.now)
	if err != nil || !status.Pending || status.Owner == nil {
		t.Fatalf("acquire submission: %+v %v", status, err)
	}
	completed, grant, err := f.s.CompleteClaimAcquire(context.Background(), status.Owner, allocation, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("acquire completion: %v", err)
	}
	return raw, hash, completed, grant
}

func claimTestAllocation(id int, anchor PrefixAnchor, events ...int) AcquireAllocation {
	a := AcquireAllocation{
		ClaimID: claimTestID(30 + id), BatchID: claimTestID(50 + id), GrantID: claimTestID(60 + id),
		SnapshotID: claimTestID(70 + id), JournalID: claimTestID(80 + id), Installed: anchor,
	}
	for _, event := range events {
		a.EventIDs = append(a.EventIDs, claimTestID(event))
	}
	return a
}

func TestClaimAcquireChecksAnExplicitEmptyBlobClosureAssertion(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	status, err := f.s.SubmitClaimAcquire(ctx, raw, hash, installed, f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit claim acquisition: %+v, %v", status, err)
	}
	allocation := claimTestAllocation(1, installed, 101, 102, 103)
	allocation.RequiredDigests = []string{digest('f')}
	if _, _, err = f.s.CompleteClaimAcquire(ctx, status.Owner, allocation, f.now, signWith(f.key)); !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("incorrect explicit closure assertion = %v, want manifest mismatch", err)
	}
	allocation.RequiredDigests = []string{}
	completed, grant, err := f.s.CompleteClaimAcquire(ctx, status.Owner, allocation, f.now, signWith(f.key))
	if err != nil || completed.Receipt == nil || len(grant.Manifest) == 0 || len(grant.Snapshot.Manifest.Entries) != 0 {
		t.Fatalf("exact empty closure assertion: status=%+v manifest=%+v err=%v", completed, grant.Snapshot.Manifest, err)
	}
}

func restoreClaimTrigger(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	for _, object := range step5Schema {
		if object.name == name {
			if _, err := db.Exec(object.sql); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("missing schema trigger %s", name)
}

func claimTestReceipt(t *testing.T, status CommandStatus, code string, output map[string]any, ids ...int) {
	t.Helper()
	r, err := readReceipt(status.Receipt)
	if err != nil || r.Result.Code != code || len(status.SignedReceipt) == 0 {
		t.Fatalf("terminal receipt: %+v %v", r, err)
	}
	if output == nil {
		if r.Range != nil || len(r.Result.Output) != 0 {
			t.Fatalf("no-effect receipt had effects: %+v", r)
		}
	} else {
		if !bytes.Equal(r.Result.Output, encodeTest(t, output)) {
			t.Fatalf("output mismatch: %x, want %x", r.Result.Output, encodeTest(t, output))
		}
		if r.Range == nil || r.Range.First != claimTestID(ids[0]) || r.Range.Last != claimTestID(ids[len(ids)-1]) || r.Range.Count != uint64(len(ids)) {
			t.Fatalf("event range: %+v", r.Range)
		}
	}
}

func claimTestAcknowledge(t *testing.T, f *claimTestFixture, journalID string, position uint64, status CommandStatus) {
	t.Helper()
	if status.Pending || status.Owner != nil || len(status.Receipt) == 0 {
		t.Fatalf("cannot acknowledge nonterminal claim command: %+v", status)
	}
	installed, err := f.s.CurrentPrefixAnchor(context.Background(), domainA)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.AcknowledgeClaimJournalEntry(context.Background(), domainA, journalID, position, status.Receipt, installed); err != nil {
		t.Fatalf("acknowledge installed claim command at position %d: %v", position, err)
	}
}

func claimTestBirthStep(t *testing.T, f *claimTestFixture, id, event int) ([]byte, string, CommandStatus) {
	t.Helper()
	command := operation.Command{
		ID: claimTestID(id), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 2,
		ActedAt: "2026-09-23T11:59:00Z", CausationCommandID: claimTestID(10),
		CorrelationCommandID: claimTestID(10),
		Request: operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation,
			Actor:     operation.Actor("human"), Context: operation.Context{Repo: repoA},
			Claim: &operation.ClaimContext{ID: f.matter, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: f.matter, Title: "birth journal step"},
		},
	}
	raw, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.s.SubmitCommand(context.Background(), command, hash, f.peer, f.now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit birth Step: %+v %v", status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.StepCreateOutput{
		ParentID: f.matter, Title: "birth journal step",
	}}
	status, err = f.s.CompleteCommand(context.Background(), status.Owner, result, claimTestID(id+100), claimTestID(event), f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete birth Step: %v", err)
	}
	return raw, hash, status
}

func TestMatterFinishDoesNotSweepUntilChildStepCompletes(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, _, birthStep := claimTestBirthStep(t, f, 31, 101)
	if birthStep.Pending || birthStep.Owner != nil {
		t.Fatalf("birth Step did not complete: %+v", birthStep)
	}
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 104, 105, 106)
	_, _, acquired, grant := f.acquire(t, 11, 3, installed, allocation)
	if acquired.Pending || acquired.Owner != nil || grant.ID != allocation.GrantID || allocation.ClaimID == "" || allocation.BatchID == "" {
		t.Fatalf("claim acquisition did not complete: status=%+v grant=%+v", acquired, grant)
	}
	stepID := claimTestID(131)
	claim := &operation.ClaimContext{ID: allocation.ClaimID, Epoch: "1"}
	submitLifecycle := func(id int, sequence uint64, op operation.ID, input operation.Input, eventIDs ...int) CommandStatus {
		t.Helper()
		command := operation.Command{
			ID: claimTestID(id), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
			EnvironmentID: envA, EnvironmentSequence: sequence, ActedAt: "2026-09-23T11:59:00Z",
			CorrelationCommandID: claimTestID(id),
			Request: operation.Request{
				Operation: op, Actor: "human",
				Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
				Claim:   claim, Input: input,
			},
		}
		hash, err := command.RequestHash()
		if err != nil {
			t.Fatal(err)
		}
		pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
		if err != nil || pending.Owner == nil || !pending.Pending {
			t.Fatalf("submit %s: status=%+v err=%v", op.Name, pending, err)
		}
		ids := []string{claimTestID(eventIDs[0]), claimTestID(eventIDs[1])}
		completed, err := f.s.CompleteConnectedLifecycle(ctx, pending.Owner, ids, f.now, signWith(f.key))
		if err != nil || completed.Pending || completed.Owner != nil || len(completed.Receipt) == 0 {
			t.Fatalf("complete %s: status=%+v err=%v", op.Name, completed, err)
		}
		return completed
	}
	started := submitLifecycle(12, 4, operation.StepStartV1.Metadata().Operation,
		operation.StepLifecycleInput{StepID: stepID}, 107, 108)
	claimTestReceipt(t, started, "result.succeeded", map[string]any{
		"step_id": stepID, "matter_id": f.matter, "state": "in-progress",
	}, 107, 108)
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)

	finishedMatter := submitLifecycle(13, 5, operation.MatterFinishV1.Metadata().Operation,
		operation.MatterFinishInput{MatterID: f.matter}, 109, 110)
	claimTestReceipt(t, finishedMatter, "result.succeeded", map[string]any{
		"matter_id": f.matter, "state": "done", "became_sealed": false,
	}, 109)
	matterReceipt, err := readReceipt(finishedMatter.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	output, err := wipdwire.DecodeCanonicalMap(matterReceipt.Result.Output, "matter_id", "state", "became_sealed")
	if err != nil || output["became_sealed"] != false {
		t.Fatalf("incomplete subtree Matter-finish result = %+v, %v; want became_sealed=false", output, err)
	}

	finishedStep := submitLifecycle(14, 6, operation.StepFinishV1.Metadata().Operation,
		operation.StepLifecycleInput{StepID: stepID}, 111, 112)
	claimTestReceipt(t, finishedStep, "result.succeeded", map[string]any{
		"step_id": stepID, "matter_id": f.matter, "state": "done",
	}, 111)
	claimTestAcknowledge(t, f, allocation.JournalID, 2, finishedStep)
	anchor, err := f.s.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || anchor.EventCount != 9 || anchor.EventID != claimTestID(111) {
		t.Fatalf("authority prefix after incomplete-then-complete lifecycle = %+v, %v", anchor, err)
	}
	rows, err := f.s.db.QueryContext(ctx, `SELECT record FROM authority_events WHERE domain_id=? ORDER BY position`, domainA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var sweeps int
	for rows.Next() {
		var record []byte
		if err = rows.Scan(&record); err != nil {
			t.Fatal(err)
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if fields["kind"] == "batch.swept" {
			sweeps++
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if sweeps != 0 {
		t.Fatalf("incomplete Matter finish emitted %d batch.swept events, err=%v", sweeps, err)
	}
}

func claimTestConnectedStepCommand(f *claimTestFixture, id int, sequence uint64, op operation.ID, claimID, stepID string) operation.Command {
	return operation.Command{
		ID: claimTestID(id), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: sequence, ActedAt: "2026-09-23T11:59:00Z",
		CorrelationCommandID: claimTestID(id),
		Request: operation.Request{
			Operation: op, Actor: "human",
			Context: operation.Context{Repo: repoA, Clone: f.clone, Worktree: f.worktree},
			Claim:   &operation.ClaimContext{ID: claimID, Epoch: "1"},
			Input:   operation.StepLifecycleInput{StepID: stepID},
		},
	}
}

func TestConnectedStepStartRecoversAfterStoreReopen(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, _, _ = claimTestBirthStep(t, f, 31, 101)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 104, 105, 106)
	f.acquire(t, 11, 3, installed, allocation)
	stepID := claimTestID(131)
	command := claimTestConnectedStepCommand(f, 12, 4, operation.StepStartV1.Metadata().Operation, allocation.ClaimID, stepID)
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("accept Step start before authority restart: status=%+v err=%v", pending, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen accepted Step start: %v", err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	owner, err := f.s.RecoverCommand(ctx, command, hash)
	if err != nil {
		t.Fatalf("recover exact Step start: %v", err)
	}
	completed, err := f.s.CompleteConnectedLifecycle(ctx, owner,
		[]string{claimTestID(107), claimTestID(108)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete recovered Step start: %v", err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"step_id": stepID, "matter_id": f.matter, "state": "in-progress",
	}, 107, 108)
	queried, err := f.s.QueryCommand(ctx, domainA, command.ID, hash, 7, f.peer, envA, f.now)
	if err != nil || queried.Pending || !bytes.Equal(queried.Receipt, completed.Receipt) {
		t.Fatalf("recovered Step-start receipt replay: status=%+v err=%v", queried, err)
	}
	claimTestEvent(t, f, 6, 107, 12, hash, "matter.started", f.matter, 4,
		map[string]any{"from": "planned", "to": "in-progress", "cascade": true})
	claimTestEvent(t, f, 7, 108, 12, hash, "step.started", stepID, 4,
		map[string]any{"from": "planned", "to": "in-progress", "cause_event_id": claimTestID(107)})
}

func TestConnectedStepFinishRecoversAfterStoreReopen(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, _, _ = claimTestBirthStep(t, f, 31, 101)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 104, 105, 106)
	f.acquire(t, 11, 3, installed, allocation)
	stepID := claimTestID(131)
	start := claimTestConnectedStepCommand(f, 12, 4, operation.StepStartV1.Metadata().Operation, allocation.ClaimID, stepID)
	startHash, err := start.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	startPending, err := f.s.SubmitCommand(ctx, start, startHash, f.peer, f.now)
	if err != nil || startPending.Owner == nil {
		t.Fatalf("submit prerequisite Step start: status=%+v err=%v", startPending, err)
	}
	started, err := f.s.CompleteConnectedLifecycle(ctx, startPending.Owner,
		[]string{claimTestID(107), claimTestID(108)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete prerequisite Step start: %v", err)
	}
	claimTestReceipt(t, started, "result.succeeded", map[string]any{
		"step_id": stepID, "matter_id": f.matter, "state": "in-progress",
	}, 107, 108)
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)

	command := claimTestConnectedStepCommand(f, 13, 5, operation.StepFinishV1.Metadata().Operation, allocation.ClaimID, stepID)
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("accept Step finish before authority restart: status=%+v err=%v", pending, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen accepted Step finish: %v", err)
	}
	t.Cleanup(func() { _ = f.s.Close() })
	owner, err := f.s.RecoverCommand(ctx, command, hash)
	if err != nil {
		t.Fatalf("recover exact Step finish: %v", err)
	}
	completed, err := f.s.CompleteConnectedLifecycle(ctx, owner,
		[]string{claimTestID(109), claimTestID(110)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete recovered Step finish: %v", err)
	}
	claimTestReceipt(t, completed, "result.succeeded", map[string]any{
		"step_id": stepID, "matter_id": f.matter, "state": "done",
	}, 109)
	queried, err := f.s.QueryCommand(ctx, domainA, command.ID, hash, 7, f.peer, envA, f.now)
	if err != nil || queried.Pending || !bytes.Equal(queried.Receipt, completed.Receipt) {
		t.Fatalf("recovered Step-finish receipt replay: status=%+v err=%v", queried, err)
	}
	claimTestEvent(t, f, 8, 109, 13, hash, "step.finished", stepID, 5,
		map[string]any{"from": "in-progress", "to": "done"})
}

func claimTestBirthRelease(t *testing.T, f *claimTestFixture, id int, sequence uint64, barrier map[string]any) ([]byte, string) {
	t.Helper()
	raw, err := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.command/1", "command_id": claimTestID(id),
		"authority":   map[string]any{"domain_id": domainA, "expected_epoch": uint64(7)},
		"environment": map[string]any{"id": envA, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "actor": "human",
		"causation_command_id": nil, "correlation_command_id": claimTestID(id),
		"operation": map[string]any{"name": "claim.release", "version": uint64(1)},
		"context":   map[string]any{"repo_id": repoA, "clone_id": nil, "worktree_id": nil},
		"claim":     map[string]any{"id": f.matter, "epoch": uint64(1)},
		"input":     map[string]any{"barrier": barrier}, "blobs": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, digestBytes(append([]byte("wipd/request-hash/v1\x00"), raw...))
}

func claimTestBirthBarrier(t *testing.T, f *claimTestFixture, stepID string, stepHash string, stepStatus CommandStatus) (map[string]any, string) {
	t.Helper()
	var birthHash string
	if err := f.s.db.QueryRow(`SELECT request_hash FROM submissions WHERE domain_id=? AND command_id=?`, domainA, claimTestID(10)).Scan(&birthHash); err != nil {
		t.Fatal(err)
	}
	birth, err := f.s.QueryCommand(context.Background(), domainA, claimTestID(10), birthHash, 7, f.peer, envA, f.now)
	if err != nil {
		t.Fatal(err)
	}
	readRange := func(status CommandStatus) *wipdwire.JournalBarrierRange {
		receipt, readErr := readReceipt(status.Receipt)
		if readErr != nil || receipt.Range == nil {
			t.Fatalf("expected accepted event range in receipt: %+v %v", receipt, readErr)
		}
		return &wipdwire.JournalBarrierRange{First: receipt.Range.First, Last: receipt.Range.Last, Count: receipt.Range.Count}
	}
	entries := []wipdwire.JournalBarrierEntry{
		{Position: 1, CommandID: claimTestID(10), RequestHash: birthHash, ResultCode: "result.succeeded", Range: readRange(birth)},
		{Position: 2, CommandID: stepID, RequestHash: stepHash, ResultCode: "result.succeeded", Range: readRange(stepStatus)},
	}
	digest, err := wipdwire.JournalBarrierDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": f.matter,
		"claim":       map[string]any{"id": f.matter, "epoch": uint64(1)},
		"entry_count": uint64(2), "last_position": uint64(2), "terminal_receipt_count": uint64(2),
		"entries_digest": digest, "sealed": true, "unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	return barrier, digest
}

func claimTestBirthAck(t *testing.T, f *claimTestFixture, commandID, hash string, receipt []byte, anchor PrefixAnchor) wipdwire.BirthJournalAck {
	t.Helper()
	installed := wipdwire.PrefixAnchor{EventCount: anchor.EventCount, Digest: anchor.Digest}
	if anchor.EventCount > 0 {
		id := anchor.EventID
		installed.EventID = &id
	}
	return wipdwire.BirthJournalAck{
		Schema: "wipd.birth-journal-ack/1", DomainID: domainA, Epoch: 7,
		MatterID: f.matter, CommandID: commandID, RequestHash: hash,
		Receipt: bytes.Clone(receipt), Installed: installed,
	}
}

func TestImplicitBirthJournalReleaseRequiresInstalledTerminalReceiptBarrier(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	_, stepHash, stepStatus := claimTestBirthStep(t, f, 31, 101)
	barrier, barrierDigest := claimTestBirthBarrier(t, f, claimTestID(31), stepHash, stepStatus)

	// Terminal authority receipts alone are insufficient. The pending barrier
	// is refused until the Environment acknowledges the exact installed prefix.
	badRaw, badHash := claimTestBirthRelease(t, f, 32, 3, barrier)
	bad, err := f.s.SubmitClaimLifecycle(ctx, badRaw, badHash, f.peer, f.now, nil)
	if err != nil || bad.Owner == nil {
		t.Fatalf("submit incomplete release: %+v %v", bad, err)
	}
	refused, err := f.s.CompleteClaimLifecycle(ctx, bad.Owner, "", []string{claimTestID(102)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	refusedReceipt, err := readReceipt(refused.Receipt)
	if err != nil || refusedReceipt.Result.Problem == nil || *refusedReceipt.Result.Problem != "refusal.claim-release-barrier" {
		t.Fatalf("incomplete installed-receipt barrier refusal=%+v err=%v", refusedReceipt.Result, err)
	}
	var birthState string
	if err = f.s.db.QueryRow(`SELECT state FROM birth_journals WHERE domain_id=? AND matter_id=?`, domainA, f.matter).Scan(&birthState); err != nil || birthState != "open" {
		t.Fatalf("incomplete barrier changed birth-claim state=%q err=%v", birthState, err)
	}

	if err = f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(31), stepHash, stepStatus.Receipt, f.anchor(t)), f.peer, envA, f.now); !errors.Is(err, ErrPending) {
		t.Fatalf("out-of-order Step receipt acknowledgment = %v, want ErrPending", err)
	}
	var birthHash string
	if err = f.s.db.QueryRow(`SELECT request_hash FROM submissions WHERE domain_id=? AND command_id=?`, domainA, claimTestID(10)).Scan(&birthHash); err != nil {
		t.Fatal(err)
	}
	birth, err := f.s.QueryCommand(ctx, domainA, claimTestID(10), birthHash, 7, f.peer, envA, f.now)
	if err != nil {
		t.Fatal(err)
	}
	ackAnchor := f.anchor(t)
	if err = f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(10), birthHash, birth.Receipt, ackAnchor), f.peer, envA, f.now); err != nil {
		t.Fatalf("acknowledge installed Matter receipt: %v", err)
	}
	if err = f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(31), stepHash, stepStatus.Receipt, ackAnchor), f.peer, envA, f.now); err != nil {
		t.Fatalf("acknowledge installed Step receipt: %v", err)
	}

	releaseRaw, releaseHash := claimTestBirthRelease(t, f, 33, 4, barrier)
	pending, err := f.s.SubmitClaimLifecycle(ctx, releaseRaw, releaseHash, f.peer, f.now, nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit complete release: %+v %v", pending, err)
	}
	var signCalls int
	if _, err = f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(103)}, f.now, func(context.Context, []byte) ([]byte, error) {
		signCalls++
		return nil, errors.New("transient signer failure")
	}); err == nil || signCalls != 1 {
		t.Fatalf("injected signer failure = %v, calls=%d", err, signCalls)
	}
	if err = f.s.AbandonClaimLifecycleExecution(pending.Owner); err != nil {
		t.Fatalf("release failed completion lease: %v", err)
	}
	retry, err := f.s.SubmitClaimLifecycle(ctx, releaseRaw, releaseHash, f.peer, f.now, nil)
	if err != nil || !retry.Pending || retry.Owner != nil {
		t.Fatalf("exact release retry = %+v, %v; want pending without a second owner", retry, err)
	}
	recovered, err := f.s.RecoverClaimLifecycle(ctx, releaseRaw, releaseHash)
	if err != nil {
		t.Fatalf("recover release continuation: %v", err)
	}
	released, err := f.s.CompleteClaimLifecycle(ctx, recovered, "", []string{claimTestID(103)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"claim_id": f.matter, "claim_epoch": uint64(1), "dispatch_id": nil, "barrier_digest": barrierDigest}
	claimTestReceipt(t, released, "result.succeeded", out, 103)
	claimTestEvent(t, f, 3, 103, 33, releaseHash, "claim.released", f.matter, 4, out)
	if replay, replayErr := f.s.SubmitClaimLifecycle(ctx, releaseRaw, releaseHash, f.peer, f.now, nil); replayErr != nil || !bytes.Equal(replay.Receipt, released.Receipt) || replay.Owner != nil {
		t.Fatalf("exact release replay: %+v %v", replay, replayErr)
	}
	if err = f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(10), birthHash, birth.Receipt, f.anchor(t)), f.peer, envA, f.now); err != nil {
		t.Fatalf("valid later-prefix ACK replay after release: %v", err)
	}
	if err = f.s.AcknowledgeBirthJournalEntry(ctx, claimTestBirthAck(t, f, claimTestID(10), birthHash, birth.Receipt, PrefixAnchor{
		Digest: EmptyPrefixAnchor().Digest,
	}), f.peer, envA, f.now); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("ACK prefix omitting accepted range = %v, want ErrPrefixMismatch", err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen released birth journal: %v", err)
	}
	if replay, replayErr := f.s.SubmitClaimLifecycle(ctx, releaseRaw, releaseHash, f.peer, f.now, nil); replayErr != nil || !bytes.Equal(replay.Receipt, released.Receipt) || replay.Owner != nil {
		t.Fatalf("release replay after reopen: %+v %v", replay, replayErr)
	}
	step := operation.Command{
		ID: claimTestID(34), AuthorityDomainID: domainA, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envA, EnvironmentSequence: 5, ActedAt: "2026-09-23T11:59:00Z",
		CausationCommandID: claimTestID(10), CorrelationCommandID: claimTestID(10),
		Request: operation.Request{
			Operation: operation.StepCreateV1.Metadata().Operation,
			Actor:     "human", Context: operation.Context{Repo: repoA}, Claim: &operation.ClaimContext{ID: f.matter, Epoch: "1"},
			Input: operation.StepCreateInput{ParentID: f.matter, Title: "must be fenced"},
		},
	}
	stepHashAfterRelease, hashErr := step.RequestHash()
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	if _, err = f.s.SubmitCommand(ctx, step, stepHashAfterRelease, f.peer, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("post-release Step submission = %v, want authority fence", err)
	}
	if _, err = f.s.QueryCommand(ctx, domainA, step.ID, stepHashAfterRelease, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fenced Step gained a receipt: %v", err)
	}
}

func claimTestEvent(t *testing.T, f *claimTestFixture, position int, id int, command int, hash, kind, subject string, sequence uint64, payload map[string]any, acting ...string) {
	t.Helper()
	environment := envA
	if len(acting) != 0 {
		environment = acting[0]
	}
	var raw []byte
	if err := f.s.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND position=?`, domainA, position).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want := encodeTest(t, map[string]any{
		"schema": "wipd.event/1", "event_id": claimTestID(id), "domain_id": domainA,
		"command_id": claimTestID(command), "request_hash": hash,
		"environment": map[string]any{"id": environment, "sequence": sequence},
		"acted_at":    "2026-09-23T11:59:00Z", "occurred_at": f.now.Format(time.RFC3339Nano),
		"kind": kind, "subject_id": subject, "repo_id": repoA, "payload": payload,
	})
	if !bytes.Equal(raw, want) {
		t.Fatalf("event position %d %s: got %x want %x", position, kind, raw, want)
	}
}

func TestClaimAcquireGrantReopenAndContention(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	a := claimTestAllocation(1, installed, 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	forged := installed
	forged.Digest = digestBytes([]byte("wrong installed prefix"))
	if _, err := f.s.SubmitClaimAcquire(ctx, raw, hash, forged, f.peer, f.now); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("bad prefix before submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad prefix submitted: %v", err)
	}
	_, _, status, grant := f.acquire(t, 11, 2, installed, a)
	output := map[string]any{"claim": map[string]any{"id": a.ClaimID, "epoch": uint64(1)}, "matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(51)}
	claimTestReceipt(t, status, "result.succeeded", output, 101, 102, 103)
	claimTestEvent(t, f, 2, 101, 11, hash, "batch.anonymous-created", a.BatchID, 2, map[string]any{"batch_id": a.BatchID, "matter_id": f.matter})
	claimTestEvent(t, f, 3, 102, 11, hash, "claim.acquired", a.ClaimID, 2, map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(51), "owner_environment_id": envA, "worktree_id": f.worktree})
	claimTestEvent(t, f, 4, 103, 11, hash, "dispatch.opened", claimTestID(51), 2, map[string]any{"dispatch_id": claimTestID(51), "matter_id": f.matter, "batch_id": a.BatchID, "claim_id": a.ClaimID, "worktree_id": f.worktree})
	queried, product, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(queried.Receipt, status.Receipt) || product.ID != grant.ID || !bytes.Equal(product.Wrapper, grant.Wrapper) || !bytes.Equal(product.Delta, grant.Delta) || !bytes.Equal(product.Manifest, grant.Manifest) || !bytes.Equal(product.End, grant.End) {
		t.Fatalf("grant query: %v, %+v", err, product)
	}
	// The query returns retained wire products, rather than repinning a new snapshot.
	if replay, e := f.s.SubmitClaimAcquire(ctx, raw, hash, emptyAnchor(), f.peer, f.now); e != nil || !bytes.Equal(replay.Receipt, status.Receipt) || replay.Owner != nil {
		t.Fatalf("replay rechecked prefix or reexecuted: %+v %v", replay, e)
	}
	badRaw, badHash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(99)})
	if _, e := f.s.SubmitClaimAcquire(ctx, badRaw, badHash, f.anchor(t), f.peer, f.now); !errors.Is(e, ErrConflict) {
		t.Fatalf("different-hash retry: %v", e)
	}
	d, owner := identity(domainA, 7)
	ca := key("step4-ca")
	caDER := caFixture(t, ca, f.now)
	leafKey := key("claim-contention-environment")
	leaf := leafFixture(t, leafKey, ca, caDER, domainA, envB, d.OwnerKeyID, 7, f.now, 103)
	enrollment := grantFixture(t, owner, d, "environment-enroll", claimTestID(96), envB, leafKey, 2)
	if _, err = f.s.IssueEnvironmentCertificate(ctx, domainA, envB, enrollment, csrFixture(t, leafKey, "Environment B"), [][]byte{leaf, caDER}, f.now); err != nil {
		t.Fatalf("enroll second Environment: %v", err)
	}
	peerB := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
	contended := claimTestAllocation(2, f.anchor(t), 104, 105)
	contendedRaw, contendedHash := f.command(t, 12, 1, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(52),
	}, envB)
	type contentionState struct {
		anchor                                                               PrefixAnchor
		claims, batches, events, grants, matters, steps, contenderDispatch   int
		claimID, matterID, owner, worktree, batch, dispatch, acquire, closed string
		repo, locator, title, birth                                          string
	}
	readState := func() contentionState {
		t.Helper()
		var state contentionState
		var err error
		if state.anchor, err = f.s.CurrentPrefixAnchor(ctx, domainA); err != nil {
			t.Fatal(err)
		}
		if err = f.s.db.QueryRow(`SELECT
			(SELECT count(*) FROM claims WHERE domain_id=?),
			(SELECT count(*) FROM anonymous_batches WHERE domain_id=? AND matter_id=?),
			(SELECT count(*) FROM authority_events WHERE domain_id=?),
			(SELECT count(*) FROM claim_grants WHERE domain_id=?),
			(SELECT count(*) FROM matters WHERE domain_id=?),
			(SELECT count(*) FROM steps WHERE domain_id=?),
			(SELECT count(*) FROM claims WHERE domain_id=? AND dispatch_id=?)`,
			domainA, domainA, f.matter, domainA, domainA, domainA, domainA, domainA, claimTestID(52)).Scan(
			&state.claims, &state.batches, &state.events, &state.grants, &state.matters, &state.steps, &state.contenderDispatch); err != nil {
			t.Fatal(err)
		}
		if err = f.s.db.QueryRow(`SELECT claim_id,matter_id,owner_environment_id,worktree_id,batch_id,dispatch_id,acquire_command_id,COALESCE(close_command_id,'')
			FROM claims WHERE domain_id=? AND matter_id=?`, domainA, f.matter).Scan(
			&state.claimID, &state.matterID, &state.owner, &state.worktree, &state.batch, &state.dispatch, &state.acquire, &state.closed); err != nil {
			t.Fatal(err)
		}
		if err = f.s.db.QueryRow(`SELECT repo_id,locator,title,birth_event_id FROM matters WHERE domain_id=? AND matter_id=?`, domainA, f.matter).Scan(
			&state.repo, &state.locator, &state.title, &state.birth); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readState()
	if before.anchor.EventCount != 4 || before.claims != 1 || before.batches != 1 || before.events != 4 || before.grants != 1 ||
		before.matters != 1 || before.steps != 0 || before.contenderDispatch != 0 || before.claimID != a.ClaimID ||
		before.owner != envA || before.dispatch != claimTestID(51) || before.closed != "" || before.acquire != claimTestID(11) ||
		before.repo != repoA || before.matterID != f.matter {
		t.Fatalf("unexpected authority state before contention: %+v", before)
	}
	pending, err := f.s.SubmitClaimAcquire(ctx, contendedRaw, contendedHash, contended.Installed, peerB, f.now)
	if err != nil || pending.Owner == nil || !pending.Pending {
		t.Fatalf("submit acquisition from distinct Environment: %+v %v", pending, err)
	}
	refused, noGrant, err := f.s.CompleteClaimAcquire(ctx, pending.Owner, contended, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	refusedReceipt, err := readReceipt(refused.Receipt)
	if err != nil || refusedReceipt.Result.Problem == nil || *refusedReceipt.Result.Problem != "refusal.claim-contended" ||
		refusedReceipt.Environment.ID != envB || refusedReceipt.ID != claimTestID(12) || refusedReceipt.Range != nil {
		t.Fatalf("cross-Environment contention receipt = %+v, %v", refusedReceipt, err)
	}
	if len(noGrant.Wrapper) != 0 || len(noGrant.Delta) != 0 || len(noGrant.Manifest) != 0 || len(noGrant.End) != 0 {
		t.Fatal("contention manufactured a grant product")
	}
	replay, err := f.s.SubmitClaimAcquire(ctx, contendedRaw, contendedHash, emptyAnchor(), peerB, f.now)
	if err != nil || replay.Pending || replay.Owner != nil || !bytes.Equal(replay.Receipt, refused.Receipt) || !bytes.Equal(replay.SignedReceipt, refused.SignedReceipt) {
		t.Fatalf("exact refused acquisition replay changed its terminal result: %+v %v", replay, err)
	}
	refusedQuery, err := f.s.QueryCommand(ctx, domainA, claimTestID(12), contendedHash, 7, peerB, envB, f.now)
	if err != nil || refusedQuery.Pending || !bytes.Equal(refusedQuery.Receipt, refused.Receipt) || !bytes.Equal(refusedQuery.SignedReceipt, refused.SignedReceipt) {
		t.Fatalf("cross-Environment refused receipt query = %+v %v", refusedQuery, err)
	}
	after := readState()
	if after != before {
		t.Fatalf("contention changed claim/Dispatch/events/projections:\n before=%+v\n after=%+v", before, after)
	}
	if err := f.s.CollectExpired(ctx, grant.Snapshot.ExpiresAt); err != nil {
		t.Fatalf("collect expired temporary snapshot: %v", err)
	}
	var snapshots int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM snapshots WHERE snapshot_id=?`, a.SnapshotID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("temporary snapshot not collected: %d %v", snapshots, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	_, reopened, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, grant.Snapshot.ExpiresAt.Add(time.Second))
	if err != nil || reopened.ID != grant.ID || !bytes.Equal(reopened.Wrapper, grant.Wrapper) || !bytes.Equal(reopened.Start, grant.Start) || !bytes.Equal(reopened.Delta, grant.Delta) {
		t.Fatalf("grant lost on reopen/expiry: %v %+v", err, reopened)
	}
	replayAfterReopen, err := f.s.SubmitClaimAcquire(ctx, contendedRaw, contendedHash, emptyAnchor(), peerB, f.now)
	if err != nil || replayAfterReopen.Pending || replayAfterReopen.Owner != nil || !bytes.Equal(replayAfterReopen.Receipt, refused.Receipt) ||
		!bytes.Equal(replayAfterReopen.SignedReceipt, refused.SignedReceipt) {
		t.Fatalf("refused acquisition replay changed after reopen: %+v %v", replayAfterReopen, err)
	}
}

func TestClaimReleaseEmptySealedJournalReopenAndEpochFence(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	if err := f.s.SealClaimJournal(ctx, domainA, a.JournalID); err != nil {
		t.Fatalf("seal B0: %v", err)
	}
	root := sha256.Sum256([]byte("wipd/journal-barrier/v1\x00"))
	barrier := map[string]any{
		"schema": "wipd.journal-barrier/1", "journal_id": a.JournalID,
		"claim": map[string]any{"id": a.ClaimID, "epoch": uint64(1)}, "entry_count": uint64(0), "last_position": uint64(0),
		"terminal_receipt_count": uint64(0), "entries_digest": digestRawBytes(root[:]), "sealed": true,
		"unresolved_count": uint64(0), "quarantined_count": uint64(0),
	}
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	wrong := map[string]any{}
	for k, v := range barrier {
		wrong[k] = v
	}
	wrong["entries_digest"] = digestBytes([]byte("not B0"))
	badRaw, badHash := f.command(t, 12, 3, "claim.release", ref, map[string]any{"barrier": wrong})
	bad, err := f.s.SubmitClaimLifecycle(ctx, badRaw, badHash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := f.s.CompleteClaimLifecycle(ctx, bad.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	goodRaw, goodHash := f.command(t, 13, 4, "claim.release", ref, map[string]any{"barrier": barrier})
	good, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, f.peer, f.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	released, err := f.s.CompleteClaimLifecycle(ctx, good.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "barrier_digest": digestRawBytes(root[:])}
	claimTestReceipt(t, released, "result.succeeded", out, 104, 105)
	claimTestEvent(t, f, 5, 104, 13, goodHash, "dispatch.closed", claimTestID(51), 4, map[string]any{"dispatch_id": claimTestID(51), "claim_id": a.ClaimID, "claim_epoch": uint64(1)})
	claimTestEvent(t, f, 6, 105, 13, goodHash, "claim.released", a.ClaimID, 4, out)
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, f.peer, f.now, nil)
	if err != nil || !bytes.Equal(replayed.Receipt, released.Receipt) || replayed.Owner != nil {
		t.Fatalf("release replay after reopen: %+v %v", replayed, err)
	}
	oldRaw, oldHash := f.command(t, 14, 5, "claim.release", ref, map[string]any{"barrier": barrier})
	if _, err := f.s.SubmitClaimLifecycle(ctx, oldRaw, oldHash, f.peer, f.now, nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("closed claim release crossed submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(14), oldHash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed claim release has receipt or submission: %v", err)
	}
	if _, err = f.s.QueryCommand(ctx, domainA, claimTestID(12), badHash, 7, f.peer, envA, f.now); err != nil {
		t.Fatalf("mismatched barrier receipt lost: %v", err)
	}
	// A caller may allocate the three-ID upper bound without pre-reading the
	// anonymous Batch. The completion transaction omits the unused first ID
	// and retains exactly the two accepted events when reusing the Batch.
	next := claimTestAllocation(15, f.anchor(t), 105, 106, 107)
	next.BatchID = "" // must not be consulted on reuse
	_, nextHash, acquired, _ := f.acquire(t, 15, 5, next.Installed, next)
	claimTestReceipt(t, acquired, "result.succeeded", map[string]any{
		"claim":     map[string]any{"id": next.ClaimID, "epoch": uint64(2)},
		"matter_id": f.matter, "batch_id": a.BatchID, "dispatch_id": claimTestID(55),
	}, 106, 107)
	claimTestEvent(t, f, 7, 106, 15, nextHash, "claim.acquired", next.ClaimID, 5, map[string]any{
		"claim_id": next.ClaimID, "claim_epoch": uint64(2), "matter_id": f.matter,
		"batch_id": a.BatchID, "dispatch_id": claimTestID(55), "owner_environment_id": envA,
		"worktree_id": f.worktree,
	})
}

func TestOwnedCurrentClaimJournalBindingAndSeal(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, acquired, _ := f.acquire(t, 11, 2, a.Installed, a)
	current, err := f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, a.ClaimID, 1, f.matter, claimTestID(51))
	if err != nil || current.JournalID != a.JournalID || current.Generation != 1 || current.State != "open" ||
		current.MatterID != f.matter || current.DispatchID != claimTestID(51) {
		t.Fatalf("current acquired journal = %+v, %v", current, err)
	}
	if _, err = f.s.GetCurrentClaimJournal(ctx, domainA, 7, envB, a.ClaimID, 1, f.matter, claimTestID(51)); !errors.Is(err, ErrFenced) {
		t.Fatalf("foreign owner obtained current journal: %v", err)
	}
	if _, err = f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, a.ClaimID, 2, f.matter, claimTestID(51)); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong claim epoch obtained current journal: %v", err)
	}
	staleBindings := map[string]CurrentClaimJournal{
		"owner":           func() CurrentClaimJournal { value := current; value.EnvironmentID = envB; return value }(),
		"authority epoch": func() CurrentClaimJournal { value := current; value.AuthorityEpoch++; return value }(),
		"claim epoch":     func() CurrentClaimJournal { value := current; value.ClaimEpoch++; return value }(),
		"Matter":          func() CurrentClaimJournal { value := current; value.MatterID = claimTestID(52); return value }(),
		"Dispatch":        func() CurrentClaimJournal { value := current; value.DispatchID = claimTestID(52); return value }(),
		"journal ID":      func() CurrentClaimJournal { value := current; value.JournalID = claimTestID(52); return value }(),
		"generation":      func() CurrentClaimJournal { value := current; value.Generation++; return value }(),
		"domain":          func() CurrentClaimJournal { value := current; value.DomainID = domainB; return value }(),
	}
	installed := f.anchor(t)
	for label, stale := range staleBindings {
		if err = f.s.AcknowledgeOwnedClaimJournalEntry(ctx, stale, 1, acquired.Receipt, installed); !errors.Is(err, ErrFenced) {
			t.Fatalf("%s control identity acknowledged a claim-journal entry: %v", label, err)
		}
		if _, _, err = f.s.SealOwnedClaimJournal(ctx, stale); !errors.Is(err, ErrFenced) {
			t.Fatalf("%s control identity sealed a claim journal: %v", label, err)
		}
	}
	var journalState string
	var entries int
	if err = f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&journalState); err != nil || journalState != "open" {
		t.Fatalf("stale controls changed current journal state=%q err=%v", journalState, err)
	}
	if err = f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("stale controls created journal entries=%d err=%v", entries, err)
	}
	digest, count, err := f.s.SealOwnedClaimJournal(ctx, current)
	if err != nil || count != 0 || digest == "" {
		t.Fatalf("owner seal = %q/%d, %v", digest, count, err)
	}
	sealed, err := f.s.GetCurrentClaimJournal(ctx, domainA, 7, envA, a.ClaimID, 1, f.matter, claimTestID(51))
	if err != nil || sealed.JournalID != a.JournalID || sealed.State != "sealed" {
		t.Fatalf("sealed current journal = %+v, %v", sealed, err)
	}
}

func TestClaimAcquireSignerFailureRollsBackEveryEffect(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(51),
	})
	pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, a.Installed, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit: %+v %v", pending, err)
	}
	_, _, err = f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("signer unavailable")
	})
	if err == nil {
		t.Fatal("accepted unsigned acquisition")
	}
	queried, product, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !queried.Pending || product.ID != "" {
		t.Fatalf("failed signature changed pending grant: %+v %+v %v", queried, product, err)
	}
	var claims, batches, events int
	for _, item := range []struct {
		table string
		dest  *int
	}{{"claims", &claims}, {"anonymous_batches", &batches}, {"authority_events", &events}} {
		if err := f.s.db.QueryRow("SELECT count(*) FROM "+item.table+" WHERE domain_id=?", domainA).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	if claims != 0 || batches != 0 || events != 1 {
		t.Fatalf("failed signer left claim effects: claims=%d batches=%d events=%d", claims, batches, events)
	}
	completed, _, err := f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, signWith(f.key))
	if err != nil || completed.Pending {
		t.Fatalf("retry terminal after rollback: %+v %v", completed, err)
	}
}

func TestClaimNoEffectProblemNamespaceBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		code, invalid, valid string
	}{
		{"result.rejected", "refusal.claim-contended", "validation.matter-not-found"},
		{"result.refused", "validation.matter-not-found", "refusal.claim-contended"},
		{"result.failed", "refusal.claim-contended", "internal.unavailable"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newClaimTestFixture(t)
			ctx := context.Background()
			raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
				"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
			})
			pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, f.anchor(t), f.peer, f.now)
			if err != nil || pending.Owner == nil {
				t.Fatalf("submit: %+v %v", pending, err)
			}
			if _, err := f.s.CompleteClaimNoEffect(ctx, pending.Owner, tc.code, tc.invalid, f.now, signWith(f.key)); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("invalid namespace persisted: %v", err)
			}
			status, err := f.s.QueryCommand(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
			if err != nil || !status.Pending || len(status.Receipt) != 0 {
				t.Fatalf("invalid namespace terminated submission: %+v %v", status, err)
			}
			var events, claims int
			if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := f.s.db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=?`, domainA).Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if events != 1 || claims != 0 {
				t.Fatalf("invalid namespace mutated state: events=%d claims=%d", events, claims)
			}
			completed, err := f.s.CompleteClaimNoEffect(ctx, pending.Owner, tc.code, tc.valid, f.now, signWith(f.key))
			if err != nil {
				t.Fatal(err)
			}
			claimTestReceipt(t, completed, tc.code, nil)
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s, err = OpenExisting(f.root)
			if err != nil {
				t.Fatalf("valid namespace did not reopen: %v", err)
			}
		})
	}
}

func TestClaimAcquireRecoveryRetainsSubmittedAnchor(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	installed := f.anchor(t)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	if status, err := f.s.SubmitClaimAcquire(ctx, raw, hash, installed, f.peer, f.now); err != nil || status.Owner == nil {
		t.Fatalf("submission: %+v %v", status, err)
	}
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.s.RecoverClaimAcquire(ctx, raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	wrong := claimTestAllocation(1, emptyAnchor(), 101, 102, 103)
	if _, _, err = f.s.CompleteClaimAcquire(ctx, owner, wrong, f.now, signWith(f.key)); !errors.Is(err, ErrPrefixMismatch) {
		t.Fatalf("recovery accepted substituted anchor: %v", err)
	}
	var count int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM claims`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial claim after refused anchor: %d %v", count, err)
	}
	good := claimTestAllocation(1, installed, 101, 102, 103)
	if _, _, err = f.s.CompleteClaimAcquire(ctx, owner, good, f.now, signWith(f.key)); err != nil {
		t.Fatalf("exact retained anchor: %v", err)
	}
	if _, err = f.s.db.Exec(`DROP TRIGGER claim_acquire_intents_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`UPDATE claim_acquire_intents SET start_digest=? WHERE domain_id=? AND command_id=?`, emptyAnchor().Digest, domainA, claimTestID(11)); err != nil {
		t.Fatal(err)
	}
	restoreClaimTrigger(t, f.s.db, "claim_acquire_intents_immutable")
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("tampered acquire evidence reopened: %v", err)
	}
}

func TestClaimAcquireConcurrentTerminalAndGrantAreSingleProduct(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	raw, hash := f.command(t, 11, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter", "requested_dispatch_id": claimTestID(51),
	})
	pending, err := f.s.SubmitClaimAcquire(ctx, raw, hash, a.Installed, f.peer, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit: %+v %v", pending, err)
	}
	type outcome struct {
		status CommandStatus
		grant  ClaimGrant
		err    error
	}
	var wg sync.WaitGroup
	results := make(chan outcome, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, grant, err := f.s.CompleteClaimAcquire(ctx, pending.Owner, a, f.now, signWith(f.key))
			results <- outcome{status, grant, err}
		}()
	}
	wg.Wait()
	close(results)
	var winner outcome
	succeeded, fenced := 0, 0
	for got := range results {
		switch {
		case got.err == nil:
			succeeded++
			winner = got
		case errors.Is(got.err, ErrNotOwner):
			fenced++
		default:
			t.Fatalf("unexpected competing terminal: %v", got.err)
		}
	}
	if succeeded != 1 || fenced != 1 {
		t.Fatalf("competing completions: success=%d fenced=%d", succeeded, fenced)
	}
	status, grant, err := f.s.QueryClaimGrant(ctx, domainA, claimTestID(11), hash, 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(status.Receipt, winner.status.Receipt) || !bytes.Equal(grant.Wrapper, winner.grant.Wrapper) {
		t.Fatalf("race retained different product: %v", err)
	}
}

func TestClaimJournalPendingRepairProofAndGeneration(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	journalRaw, journalHash := f.command(t, 12, 3, "cursor.move", ref, map[string]any{"target_id": nil})
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrPending) {
		t.Fatalf("out of order position: %v", err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 1, journalRaw, journalHash); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrPending) {
		t.Fatalf("pending head admitted next: %v", err)
	}
	if err := f.s.SealClaimJournal(ctx, domainA, a.JournalID); !errors.Is(err, ErrPending) {
		t.Fatalf("sealed pending journal: %v", err)
	}
	input := func(proof any) map[string]any {
		return map[string]any{
			"journal_id": a.JournalID, "head_position": uint64(1),
			"head_command_id": claimTestID(12), "head_request_hash": journalHash,
			"action": map[string]any{"kind": "abandon", "proof": proof},
		}
	}
	wrongProof := map[string]any{"terminal_receipt": nil, "not_submitted_proof": "same-epoch-receipt-not-found"}
	staleRef := map[string]any{"id": a.ClaimID, "epoch": uint64(2)}
	staleRaw, staleHash := f.command(t, 14, 3, "claim.journal-repair", staleRef, input(wrongProof))
	if _, err := f.s.SubmitClaimLifecycle(ctx, staleRaw, staleHash, f.peer, f.now, nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("wrong-epoch repair crossed submission: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(14), staleHash, 7, f.peer, envA, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-epoch repair has receipt or submission: %v", err)
	}
	badRaw, badHash := f.command(t, 13, 3, "claim.journal-repair", ref, input(wrongProof))
	pending, err := f.s.SubmitClaimLifecycle(ctx, badRaw, badHash, f.peer, f.now, nil)
	if err != nil || pending.Owner == nil {
		t.Fatalf("repair submission: %+v %v", pending, err)
	}
	newJournal := claimTestID(90)
	completed, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, newJournal, []string{claimTestID(104)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("not-found proof repair: %v", err)
	}
	out := map[string]any{"claim_id": a.ClaimID, "archived_journal_id": a.JournalID, "new_journal_id": newJournal, "action": "abandon"}
	claimTestReceipt(t, completed, "result.succeeded", out, 104)
	claimTestEvent(t, f, 5, 104, 13, badHash, "claim.journal-repaired", a.ClaimID, 3, out)
	var prior, next string
	var generation uint64
	if err = f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&prior); err != nil {
		t.Fatal(err)
	}
	if err = f.s.db.QueryRow(`SELECT state,generation FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, newJournal).Scan(&next, &generation); err != nil {
		t.Fatal(err)
	}
	if prior != "quarantined" || next != "open" || generation != 2 {
		t.Fatalf("repair generation: old=%s new=%s generation=%d", prior, next, generation)
	}
	if err = f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, journalHash); !errors.Is(err, ErrFenced) {
		t.Fatalf("archived journal accepts writes: %v", err)
	}
}

func TestClaimRepairCannotTreatSubmittedPendingHeadAsNotFound(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	ref := map[string]any{"id": a.ClaimID, "epoch": uint64(1)}
	journalRaw, journalHash := f.command(t, 12, 3, "cursor.move", ref, map[string]any{"target_id": nil})
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 1, journalRaw, journalHash); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AppendClaimJournalEntry(ctx, a.JournalID, 2, journalRaw, digestBytes([]byte("wrong journal bytes"))); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("journal hash not verified: %v", err)
	}
	if err := f.s.AcknowledgeClaimJournalEntry(ctx, domainA, a.JournalID, 1, nil, f.anchor(t)); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("missing terminal receipt acknowledged: %v", err)
	}
	// Model an admitted but still pending Step 6 return. A query for a
	// terminal receipt is not-found, but that absence is not a no-submission
	// proof and must not permit repair to abandon the head.
	if _, err := f.s.db.Exec(`INSERT INTO submissions(domain_id,command_id,request_hash,command,epoch,environment_id,environment_sequence,operation_name,operation_version,state) VALUES(?,?,?,?,?,?,?,?,?,'submitted')`, domainA, claimTestID(12), journalHash, journalRaw, 7, envA, 3, "cursor.move", 1); err != nil {
		t.Fatal(err)
	}
	raw, hash := f.command(t, 13, 3, "claim.journal-repair", ref, map[string]any{
		"journal_id": a.JournalID, "head_position": uint64(1), "head_command_id": claimTestID(12),
		"head_request_hash": journalHash, "action": map[string]any{
			"kind": "abandon", "proof": map[string]any{"terminal_receipt": nil, "not_submitted_proof": "same-epoch-receipt-not-found"},
		},
	})
	// The pending return owns sequence 3. The repair must not bypass that
	// head even though it supplies a "receipt not found" assertion.
	if _, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, f.peer, f.now, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("pending return did not fence repair submission: %v", err)
	}
	queried, err := f.s.QueryCommand(ctx, domainA, claimTestID(12), journalHash, 7, f.peer, envA, f.now)
	if err != nil || !queried.Pending {
		t.Fatalf("pending return lost: %+v %v", queried, err)
	}
	var state string
	if err := f.s.db.QueryRow(`SELECT state FROM claim_journals WHERE domain_id=? AND journal_id=?`, domainA, a.JournalID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("pending return journal quarantined: %s", state)
	}
}

func TestClaimStandDownCrossEnvironmentSignedScopeAndLoss(t *testing.T) {
	f := newClaimTestFixture(t)
	ctx := context.Background()
	a := claimTestAllocation(1, f.anchor(t), 101, 102, 103)
	_, _, _, _ = f.acquire(t, 11, 2, a.Installed, a)
	d, owner := identity(domainA, 7)
	ca := key("step4-ca")
	caDER := caFixture(t, ca, f.now)
	leafKey := key("claim-second-environment")
	leaf := leafFixture(t, leafKey, ca, caDER, domainA, envB, d.OwnerKeyID, 7, f.now, 102)
	grant := grantFixture(t, owner, d, "environment-enroll", claimTestID(95), envB, leafKey, 2)
	if _, err := f.s.IssueEnvironmentCertificate(ctx, domainA, envB, grant, csrFixture(t, leafKey, "Environment B"), [][]byte{leaf, caDER}, f.now); err != nil {
		t.Fatalf("enroll second Environment: %v", err)
	}
	peerB := tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
	target := map[string]any{"claim_id": a.ClaimID, "claim_epoch": uint64(1), "owner_environment_id": envA}
	reason := "Owner accepts loss of unreturned work"
	authorize := func(id int, hash string, nonce byte, scopeClaim string) []byte {
		subject := encodeTest(t, map[string]any{
			"schema": "wipd.claim-stand-down-subject/1", "command_id": claimTestID(id), "request_hash": hash,
			"claim_id": scopeClaim, "claim_epoch": uint64(1), "owner_environment_id": envA,
			"acting_environment_id": envB, "reason_digest": digestBytes([]byte(reason)),
		})
		return signedTest(t, owner, "owner-attestation", "wipd.owner-attestation/1", domainA, d.OwnerKeyID, 7, map[string]any{
			"schema": "wipd.owner-attestation/1", "action": "claim-stand-down", "domain_id": domainA,
			"current_epoch": uint64(7), "next_epoch": nil, "subject_schema": "wipd.claim-stand-down-subject/1",
			"subject_digest": digestBytes(subject), "subject": subject, "nonce": bytes.Repeat([]byte{nonce}, 16),
			"issued_at":  f.now.Add(-time.Minute).Format(time.RFC3339Nano),
			"expires_at": f.now.Add(time.Minute).Format(time.RFC3339Nano), "loss_accepted": true,
		})
	}
	input := func(loss bool) map[string]any {
		return map[string]any{"target": target, "reason": reason, "acknowledge_unreturned_work_loss": loss}
	}
	raw, hash := f.command(t, 12, 1, "claim.stand-down", nil, input(false), envB)
	proof := authorize(12, hash, 3, a.ClaimID)
	if _, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, peerB, f.now, authorize(12, hash, 4, claimTestID(99))); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("wrong owner scope admitted: %v", err)
	}
	if _, err := f.s.QueryCommand(ctx, domainA, claimTestID(12), hash, 7, peerB, envB, f.now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-scope attestation submitted: %v", err)
	}
	pending, err := f.s.SubmitClaimLifecycle(ctx, raw, hash, peerB, f.now, proof)
	if err != nil || pending.Owner == nil {
		t.Fatalf("stand-down submission: %+v %v", pending, err)
	}
	refused, err := f.s.CompleteClaimLifecycle(ctx, pending.Owner, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	claimTestReceipt(t, refused, "result.refused", nil)
	goodRaw, goodHash := f.command(t, 13, 2, "claim.stand-down", nil, input(true), envB)
	goodProof := authorize(13, goodHash, 5, a.ClaimID)
	good, err := f.s.SubmitClaimLifecycle(ctx, goodRaw, goodHash, peerB, f.now, goodProof)
	if err != nil || good.Owner == nil {
		t.Fatalf("authorized stand-down: %+v %v", good, err)
	}
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s, err = OpenExisting(f.root)
	if err != nil {
		t.Fatalf("pending stand-down reopen: %v", err)
	}
	if _, err = f.s.RecoverClaimLifecycle(ctx, goodRaw, goodHash, []byte("different proof")); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("recovery accepted substituted owner proof: %v", err)
	}
	recovered, err := f.s.RecoverClaimLifecycle(ctx, goodRaw, goodHash)
	if err != nil {
		t.Fatalf("admitted owner proof expired after submission: %v", err)
	}
	closed, err := f.s.CompleteClaimLifecycle(ctx, recovered, "", []string{claimTestID(104), claimTestID(105)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatal(err)
	}
	reasonDigest := digestBytes([]byte(reason))
	claimTestReceipt(t, closed, "result.succeeded", map[string]any{
		"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51), "reason_digest": reasonDigest,
	}, 104, 105)
	claimTestEvent(t, f, 5, 104, 13, goodHash, "dispatch.closed", claimTestID(51), 2,
		map[string]any{"dispatch_id": claimTestID(51), "claim_id": a.ClaimID, "claim_epoch": uint64(1)}, envB)
	claimTestEvent(t, f, 6, 105, 13, goodHash, "claim.stood-down", a.ClaimID, 2,
		map[string]any{
			"claim_id": a.ClaimID, "claim_epoch": uint64(1), "dispatch_id": claimTestID(51),
			"owner_environment_id": envA, "acting_environment_id": envB, "reason_digest": reasonDigest,
			"loss_accepted": true,
		}, envB)
	if _, err = f.s.db.Exec(`DROP TRIGGER claim_stand_down_proofs_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`UPDATE claim_stand_down_proofs SET authorization=? WHERE domain_id=? AND command_id=?`, []byte("forged"), domainA, claimTestID(13)); err != nil {
		t.Fatal(err)
	}
	restoreClaimTrigger(t, f.s.db, "claim_stand_down_proofs_immutable")
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenExisting(f.root); !errors.Is(err, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("tampered owner proof reopened: %v", err)
	}
}
