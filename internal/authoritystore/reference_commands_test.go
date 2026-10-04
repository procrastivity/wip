package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/operation"
)

func referenceAuthorityCommand(f *claimTestFixture, id int, sequence uint64, definition operation.Definition,
	input operation.Input, repo string,
) operation.Command {
	command := step12Command(f, id, sequence, definition, input, "")
	command.Request.Context.Repo = repo
	return command
}

func submitReferenceForTest(t *testing.T, f *claimTestFixture, command operation.Command) *Execution {
	t.Helper()
	status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), f.peer, f.now)
	if err != nil || !status.Pending || status.Owner == nil {
		t.Fatalf("submit reference command: status=%+v error=%v", status, err)
	}
	var entries int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`,
		command.AuthorityDomainID, command.ID).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("reference command entered a claim journal: entries=%d error=%v", entries, err)
	}
	return status.Owner
}

func completeReferenceForTest(t *testing.T, f *claimTestFixture, command operation.Command, event int) CommandStatus {
	t.Helper()
	status, err := f.s.CompleteCommand(context.Background(), submitReferenceForTest(t, f, command),
		operation.Result{Code: operation.ResultSucceeded}, "", claimTestID(event), f.now, signWith(f.key))
	if err != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("complete reference command: status=%+v error=%v", status, err)
	}
	return status
}

func assertReferenceSuccess(t *testing.T, f *claimTestFixture, command operation.Command, status CommandStatus,
	wantKind, wantReference, wantPrevious string,
) {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil {
		t.Fatalf("read reference success receipt: %v", err)
	}
	assertReferenceReceiptBinding(t, receipt, command)
	if receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != receipt.Range.Last {
		t.Fatalf("reference success receipt = %+v error=%v", receipt, err)
	}
	wantOutput := map[string]any{"matter_id": referenceMatter(command), "reference": wantReference}
	if wantPrevious != "" {
		wantOutput["previous_reference"] = wantPrevious
	}
	wantOutputBytes, err := artifactEncoder.Marshal(wantOutput)
	if err != nil || !bytes.Equal(receipt.Result.Output, wantOutputBytes) {
		t.Fatalf("reference output bytes = %x error=%v, want %v", receipt.Result.Output, err, wantOutput)
	}
	var position uint64
	var raw []byte
	if err = f.s.db.QueryRow(`SELECT position,record FROM authority_events WHERE domain_id=? AND event_id=? AND command_id=?`,
		command.AuthorityDomainID, receipt.Range.First, command.ID).Scan(&position, &raw); err != nil {
		t.Fatalf("read reference event: %v", err)
	}
	event, err := parseStep12Event(raw, command.AuthorityDomainID, position, receipt.Range.First, command.ID)
	if err != nil || event.kind != wantKind || event.subject != referenceMatter(command) || event.repo != command.Request.Context.Repo ||
		event.hash != hashCommand(t, command) || event.environment != command.EnvironmentID ||
		event.sequence != command.EnvironmentSequence || event.acted != command.ActedAt {
		t.Fatalf("reference event does not bind command/Repo/input: %+v error=%v", event, err)
	}
	if wantKind == "reference.rebound" {
		input := command.Request.Input.(operation.ReferenceRebindInput)
		var payload struct {
			From  string `cbor:"from"`
			To    string `cbor:"to"`
			Level string `cbor:"tracker_push_level"`
		}
		if !step13ClosedPayload(event.payload, &payload, []string{"from", "to", "tracker_push_level"}) ||
			payload.From != input.From || payload.To != input.To || payload.Level != "off" {
			t.Fatalf("rebound event payload = %+v", payload)
		}
	} else {
		var payload struct {
			Ref   string `cbor:"ref"`
			Level string `cbor:"tracker_push_level"`
		}
		if !step13ClosedPayload(event.payload, &payload, []string{"ref", "tracker_push_level"}) ||
			payload.Ref != wantReference || payload.Level != "off" {
			t.Fatalf("reference event payload = %+v", payload)
		}
	}
}

func assertReferenceRefusal(t *testing.T, f *claimTestFixture, command operation.Command, status CommandStatus,
	wantProblem string, before PrefixAnchor, projection step13Projection,
) {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil {
		t.Fatalf("read reference refusal receipt: %v", err)
	}
	assertReferenceReceiptBinding(t, receipt, command)
	if receipt.Result.Code != string(operation.ResultRefused) || receipt.Result.Problem == nil ||
		string(*receipt.Result.Problem) != wantProblem || receipt.Result.Output != nil || receipt.Range != nil {
		t.Fatalf("reference refusal receipt = %+v error=%v, want %s without output/events", receipt.Result, err, wantProblem)
	}
	if after := f.anchor(t); after != before {
		t.Fatalf("reference refusal changed event prefix: before=%+v after=%+v", before, after)
	}
	afterProjection, err := readStep13Projection(f.s.db)
	if err != nil || !sameStep13Projection(afterProjection, projection) {
		t.Fatalf("reference refusal changed projection: before=%+v after=%+v error=%v", projection, afterProjection, err)
	}
	var events int
	if err = f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`,
		command.AuthorityDomainID, command.ID).Scan(&events); err != nil || events != 0 {
		t.Fatalf("reference refusal retained %d events: %v", events, err)
	}
}

func assertReferenceReceiptBinding(t *testing.T, receipt receiptRecord, command operation.Command) {
	t.Helper()
	if receipt.ID != command.ID || receipt.Domain != command.AuthorityDomainID || receipt.Epoch != command.ExpectedAuthorityEpoch ||
		receipt.Hash != hashCommand(t, command) || receipt.Operation.Name != command.Request.Operation.Name ||
		receipt.Operation.Version != uint64(command.Request.Operation.Version) || receipt.Environment.ID != command.EnvironmentID ||
		receipt.Environment.Sequence != command.EnvironmentSequence {
		t.Fatalf("reference receipt identity does not bind exact command: %+v", receipt)
	}
}

func referenceMatter(command operation.Command) string {
	switch input := command.Request.Input.(type) {
	case operation.ReferenceBindInput:
		return input.MatterID
	case operation.ReferenceUnbindInput:
		return input.MatterID
	case operation.ReferenceRebindInput:
		return input.MatterID
	default:
		return ""
	}
}

func referenceAggregateForTest(t *testing.T, f *claimTestFixture, ref string) (string, int64, bool) {
	t.Helper()
	var disposition sql.NullString
	var members int64
	err := f.s.db.QueryRow(`SELECT disposition,member_count FROM m6_tracker_aggregates WHERE domain_id=? AND ref=?`, domainA, ref).
		Scan(&disposition, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return disposition.String, members, true
}

func TestReferenceAuthoritySharesAcrossMatterReposAndFinalUnbindHasNoCandidate(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	ref := "  provider-neutral:ticket/17#part  "
	bind := referenceAuthorityCommand(f, 13, 4, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: ref}, repoA)
	first := completeReferenceForTest(t, f, bind, 103)
	assertReferenceSuccess(t, f, bind, first, "reference.added", ref, "")

	// A second distinct reference on the same Matter proves bind is additive,
	// not the legacy singleton replacement represented by reference.bound.
	otherRef := "ticket/other"
	additive := referenceAuthorityCommand(f, 14, 5, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: otherRef}, repoA)
	additiveStatus := completeReferenceForTest(t, f, additive, 104)
	assertReferenceSuccess(t, f, additive, additiveStatus, "reference.added", otherRef, "")

	sharedMatter := claimTestID(21)
	shared := referenceAuthorityCommand(f, 15, 6, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: sharedMatter, Reference: ref}, repoB)
	sharedStatus := completeReferenceForTest(t, f, shared, 105)
	assertReferenceSuccess(t, f, shared, sharedStatus, "reference.added", ref, "")
	if disposition, members, exists := referenceAggregateForTest(t, f, ref); !exists || disposition != "" || members != 2 {
		t.Fatalf("cross-Matter/cross-Repo aggregate = %q/%d exists=%t; want two planned members", disposition, members, exists)
	}
	if disposition, members, exists := referenceAggregateForTest(t, f, otherRef); !exists || disposition != "" || members != 1 {
		t.Fatalf("same-Matter additive aggregate = %q/%d exists=%t; want one member", disposition, members, exists)
	}

	unboundFirst := referenceAuthorityCommand(f, 16, 7, operation.ReferenceUnbindV1,
		operation.ReferenceUnbindInput{MatterID: f.matter, Reference: ref}, repoA)
	unboundFirstStatus := completeReferenceForTest(t, f, unboundFirst, 106)
	assertReferenceSuccess(t, f, unboundFirst, unboundFirstStatus, "reference.removed", ref, "")
	if _, members, exists := referenceAggregateForTest(t, f, ref); !exists || members != 1 {
		t.Fatalf("shared aggregate after one unbind has members=%d exists=%t; want one", members, exists)
	}
	unboundFinal := referenceAuthorityCommand(f, 17, 8, operation.ReferenceUnbindV1,
		operation.ReferenceUnbindInput{MatterID: sharedMatter, Reference: ref}, repoB)
	finalStatus := completeReferenceForTest(t, f, unboundFinal, 107)
	assertReferenceSuccess(t, f, unboundFinal, finalStatus, "reference.removed", ref, "")
	if _, _, exists := referenceAggregateForTest(t, f, ref); exists {
		t.Fatal("final unbind retained a shared-reference aggregate")
	}
	var candidates int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM m6_tracker_candidates WHERE domain_id=? AND ref=?`, domainA, ref).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("final unbind emitted cleanup candidates: count=%d error=%v", candidates, err)
	}

	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen reference history: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
	replay, err := reopened.QueryCommand(context.Background(), domainA, unboundFinal.ID, hashCommand(t, unboundFinal), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, finalStatus.Receipt) || !bytes.Equal(replay.SignedReceipt, finalStatus.SignedReceipt) {
		t.Fatalf("reopened reference receipt replay changed: status=%+v error=%v", replay, err)
	}
	if _, _, exists := referenceAggregateForTest(t, f, ref); exists {
		t.Fatal("reopened final unbind recreated the aggregate")
	}
}

func TestReferenceAuthorityRefusalsAreAtomicAndContextRepoIsDomainMember(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	existingRef, destinationRef := "existing/ref", "destination/ref"
	bind := referenceAuthorityCommand(f, 13, 4, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: existingRef}, repoA)
	completeReferenceForTest(t, f, bind, 103)

	refusals := []struct {
		id, sequence, event int
		definition          operation.Definition
		input               operation.Input
		repo, problem       string
	}{
		{14, 5, 104, operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: f.matter, Reference: existingRef}, repoA, "refusal.reference-exists"},
		{15, 6, 105, operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: f.matter, Reference: "missing/ref"}, repoA, "refusal.reference-missing"},
		{16, 7, 106, operation.ReferenceRebindV1, operation.ReferenceRebindInput{MatterID: f.matter, From: "missing/ref", To: destinationRef}, repoA, "refusal.reference-missing"},
	}
	for _, test := range refusals {
		command := referenceAuthorityCommand(f, test.id, uint64(test.sequence), test.definition, test.input, test.repo)
		before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
		status := completeReferenceForTest(t, f, command, test.event)
		assertReferenceRefusal(t, f, command, status, test.problem, before, projection)
	}

	addDestination := referenceAuthorityCommand(f, 17, 8, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: destinationRef}, repoA)
	completeReferenceForTest(t, f, addDestination, 107)
	conflict := referenceAuthorityCommand(f, 18, 9, operation.ReferenceRebindV1,
		operation.ReferenceRebindInput{MatterID: f.matter, From: existingRef, To: destinationRef}, repoA)
	before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	conflictStatus := completeReferenceForTest(t, f, conflict, 108)
	assertReferenceRefusal(t, f, conflict, conflictStatus, "refusal.reference-destination-exists", before, projection)

	wrongRepo := referenceAuthorityCommand(f, 19, 10, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: "wrong/repo"}, repoB)
	before, projection = f.anchor(t), mustStep13ProjectionForTest(t, f)
	wrongRepoStatus := completeReferenceForTest(t, f, wrongRepo, 109)
	assertReferenceRefusal(t, f, wrongRepo, wrongRepoStatus, "refusal.reference-matter", before, projection)

	wrongDomain := referenceAuthorityCommand(f, 20, 11, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: domainB, Reference: "wrong/domain"}, repoA)
	before, projection = f.anchor(t), mustStep13ProjectionForTest(t, f)
	wrongDomainStatus := completeReferenceForTest(t, f, wrongDomain, 110)
	assertReferenceRefusal(t, f, wrongDomain, wrongDomainStatus, "refusal.reference-matter", before, projection)

	outsideRepo := referenceAuthorityCommand(f, 21, 12, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: "outside/domain"}, claimTestID(990))
	if _, err := f.s.SubmitCommand(context.Background(), outsideRepo, hashCommand(t, outsideRepo), f.peer, f.now); !errors.Is(err, ErrFenced) {
		t.Fatalf("non-member Context.Repo admitted: %v", err)
	}
	var submissions int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, domainA, outsideRepo.ID).Scan(&submissions); err != nil || submissions != 0 {
		t.Fatalf("non-member Context.Repo created %d submissions: %v", submissions, err)
	}
}

func TestReferenceRebindIsOneFoldAndReactivationKeepsBirthIdentity(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	from, to := "source/ref", "destination/ref"
	for _, item := range []struct {
		id, sequence, event int
		definition          operation.Definition
		input               operation.Input
	}{
		{13, 4, 103, operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: f.matter, Reference: from}},
		{14, 5, 104, operation.ReferenceBindV1, operation.ReferenceBindInput{MatterID: f.matter, Reference: to}},
		{15, 6, 105, operation.ReferenceUnbindV1, operation.ReferenceUnbindInput{MatterID: f.matter, Reference: to}},
	} {
		command := referenceAuthorityCommand(f, item.id, uint64(item.sequence), item.definition, item.input, repoA)
		completeReferenceForTest(t, f, command, item.event)
	}
	rebind := referenceAuthorityCommand(f, 16, 7, operation.ReferenceRebindV1,
		operation.ReferenceRebindInput{MatterID: f.matter, From: from, To: to}, repoA)
	status := completeReferenceForTest(t, f, rebind, 106)
	assertReferenceSuccess(t, f, rebind, status, "reference.rebound", to, from)
	var fromRow, toRow step13Reference
	if err := f.s.db.QueryRow(`SELECT domain_id,matter_id,ref,coalesce(removed_event_id,''),birth_event_id,last_event_id FROM m6_tracker_references WHERE domain_id=? AND matter_id=? AND ref=?`,
		domainA, f.matter, from).Scan(&fromRow.domain, &fromRow.matter, &fromRow.ref, &fromRow.removed, &fromRow.birth, &fromRow.last); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow(`SELECT domain_id,matter_id,ref,coalesce(removed_event_id,''),birth_event_id,last_event_id FROM m6_tracker_references WHERE domain_id=? AND matter_id=? AND ref=?`,
		domainA, f.matter, to).Scan(&toRow.domain, &toRow.matter, &toRow.ref, &toRow.removed, &toRow.birth, &toRow.last); err != nil {
		t.Fatal(err)
	}
	if fromRow.removed != claimTestID(106) || fromRow.last != claimTestID(106) ||
		toRow.removed != "" || toRow.birth != claimTestID(104) || toRow.last != claimTestID(106) {
		t.Fatalf("rebind/re-activation identity: source=%+v destination=%+v", fromRow, toRow)
	}
	if _, _, exists := referenceAggregateForTest(t, f, from); exists {
		t.Fatal("rebind retained the now-unreferenced source aggregate")
	}
	if _, members, exists := referenceAggregateForTest(t, f, to); !exists || members != 1 {
		t.Fatalf("rebind destination aggregate has members=%d exists=%t", members, exists)
	}
	var eventCount int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, rebind.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("rebind appended %d events: %v", eventCount, err)
	}

	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen atomic reference rebind: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
	replay, err := reopened.QueryCommand(context.Background(), domainA, rebind.ID, hashCommand(t, rebind), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, status.Receipt) || !bytes.Equal(replay.SignedReceipt, status.SignedReceipt) {
		t.Fatalf("reopened rebind receipt changed: status=%+v error=%v", replay, err)
	}
}

func TestReferenceAdmissionIsClaimNoneAndLifecycleRefreshReopens(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	invalid := referenceAuthorityCommand(f, 11, 2, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: "claim/forbidden"}, repoA)
	invalid.Request.Claim = &operation.ClaimContext{ID: claimTestID(30), Epoch: "1"}
	if err := operation.ReferenceBindV1.ValidateRequest(invalid.Request); err == nil {
		t.Fatal("reference definition accepted Claim context")
	}
	if _, err := f.s.SubmitCommand(context.Background(), invalid, "", f.peer, f.now); err == nil {
		t.Fatalf("reference command accepted Claim context: %v", err)
	}
	var submissions, journalEntries int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, domainA, invalid.ID).Scan(&submissions); err != nil || submissions != 0 {
		t.Fatalf("ClaimNone rejection retained %d submissions: %v", submissions, err)
	}
	if err := f.s.db.QueryRow(`SELECT count(*) FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, domainA, invalid.ID).Scan(&journalEntries); err != nil || journalEntries != 0 {
		t.Fatalf("ClaimNone rejection wrote %d journal entries: %v", journalEntries, err)
	}

	bind := referenceAuthorityCommand(f, 12, 2, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: "lifecycle/ref"}, repoA)
	bindStatus := completeReferenceForTest(t, f, bind, 101)
	if disposition, members, exists := referenceAggregateForTest(t, f, "lifecycle/ref"); !exists || disposition != "" || members != 1 {
		t.Fatalf("bound planned Matter aggregate = %q/%d exists=%t", disposition, members, exists)
	}
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 102, 103, 104)
	f.acquire(t, 13, 3, anchor, allocation)
	start := step12Command(f, 14, 4, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, start, []string{claimTestID(107)})
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)
	if disposition, members, exists := referenceAggregateForTest(t, f, "lifecycle/ref"); !exists || disposition != "active" || members != 1 {
		t.Fatalf("started Matter aggregate = %q/%d exists=%t", disposition, members, exists)
	}
	cancel := step12Command(f, 15, 5, operation.MatterCancelV1,
		operation.NodeLifecycleInput{NodeID: f.matter, Reason: "scope changed"}, allocation.ClaimID)
	canceled := completeLifecycleCommand(t, f, cancel, []string{claimTestID(108)})
	claimTestAcknowledge(t, f, allocation.JournalID, 2, canceled)
	if disposition, members, exists := referenceAggregateForTest(t, f, "lifecycle/ref"); !exists || disposition != "canceled" || members != 1 {
		t.Fatalf("canceled Matter aggregate = %q/%d exists=%t", disposition, members, exists)
	}

	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExisting(f.root)
	if err != nil {
		t.Fatalf("reopen bind/start/cancel projection history: %v", err)
	}
	f.s = reopened
	t.Cleanup(func() { _ = f.s.Close() })
	if disposition, members, exists := referenceAggregateForTest(t, f, "lifecycle/ref"); !exists || disposition != "canceled" || members != 1 {
		t.Fatalf("reopened lifecycle aggregate = %q/%d exists=%t", disposition, members, exists)
	}
	replay, err := reopened.QueryCommand(context.Background(), domainA, bind.ID, hashCommand(t, bind), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, bindStatus.Receipt) {
		t.Fatalf("reopened bind receipt differs: status=%+v error=%v", replay, err)
	}
}

func mustStep13ProjectionForTest(t *testing.T, f *claimTestFixture) step13Projection {
	t.Helper()
	projection, err := readStep13Projection(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestReferencePushLevelPolicyUsesStep7Fallbacks(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	tx, err := f.s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	assertLevel := func(want string) {
		t.Helper()
		got, levelErr := gatePushLevelTx(context.Background(), tx, domainA, repoA)
		if levelErr != nil || got != want {
			t.Fatalf("effective push level = %q, %v; want %q", got, levelErr, want)
		}
	}
	assertLevel("off")
	if _, err = tx.Exec(`INSERT INTO m6_repo_config VALUES(?,?,?,?,?)`, domainA, repoA, "tracker.backend", "provider", claimTestID(100)); err != nil {
		t.Fatal(err)
	}
	assertLevel("boundary")
	if _, err = tx.Exec(`INSERT INTO m6_repo_config VALUES(?,?,?,?,?)`, domainA, repoA, "tracker.push-level", "narrated", claimTestID(100)); err != nil {
		t.Fatal(err)
	}
	assertLevel("narrated")
	if _, err = tx.Exec(`UPDATE m6_repo_config SET value='off' WHERE domain_id=? AND repo_id=? AND config_key='tracker.push-level'`, domainA, repoA); err != nil {
		t.Fatal(err)
	}
	assertLevel("off")
}

func TestStep13ReferenceSnapshotUsesEffectiveConfigAtEventPosition(t *testing.T) {
	matter := claimTestID(20)
	node := step12Node{domain: domainA, id: matter, kind: "matter", repo: repoA, matter: matter, birth: claimTestID(301), last: claimTestID(301)}
	nodes := map[string]step13Node{ownerKey(domainA, matter): {node: node, birthPos: 1}}
	events := []step13Event{
		step13TestEvent(2, "config.set", repoA, map[string]any{"key": "tracker.backend", "value": "provider"}),
		step13TestEvent(3, "reference.added", matter, map[string]any{"ref": "backend/ref", "tracker_push_level": "boundary"}),
		step13TestEvent(4, "config.set", repoA, map[string]any{"key": "tracker.push-level", "value": "off"}),
		step13TestEvent(5, "reference.added", matter, map[string]any{"ref": "explicit/ref", "tracker_push_level": "off"}),
		step13TestEvent(6, "config.set", repoA, map[string]any{"key": "tracker.push-level", "value": "narrated"}),
		step13TestEvent(7, "reference.bound", matter, map[string]any{"ref": "legacy/ref"}),
	}
	if _, err := deriveStep13Projection(nodes, events); err != nil {
		t.Fatalf("valid historical snapshots or legacy reference.bound rejected: %v", err)
	}

	for _, test := range []struct {
		name, ref, level string
		position         int
	}{
		{name: "backend fallback", ref: "backend/ref", level: "off", position: 3},
		{name: "explicit level wins and later changes do not rewrite history", ref: "explicit/ref", level: "boundary", position: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			badEvents := append([]step13Event(nil), events...)
			badEvents[test.position-2] = step13TestEvent(test.position, "reference.added", matter,
				map[string]any{"ref": test.ref, "tracker_push_level": test.level})
			if _, err := deriveStep13Projection(nodes, badEvents); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("enum-valid snapshot %q inconsistent with historical config was accepted: %v", test.level, err)
			}
		})
	}
}

func TestOpenExistingRejectsReferenceSnapshotSubstitutionWithRebuiltCandidateOverlay(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 101, 102, 103)
	f.acquire(t, 11, 2, anchor, allocation)
	start := step12Command(f, 12, 3, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, start, []string{claimTestID(104)})
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)

	const reference = "active/ref"
	bind := referenceAuthorityCommand(f, 13, 4, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: reference}, repoA)
	status := completeReferenceForTest(t, f, bind, 105)
	assertReferenceSuccess(t, f, bind, status, "reference.added", reference, "")
	if disposition, members, exists := referenceAggregateForTest(t, f, reference); !exists || disposition != "active" || members != 1 {
		t.Fatalf("pre-tamper aggregate = %q/%d exists=%t; want one active member", disposition, members, exists)
	}
	beforeProjection := mustStep13ProjectionForTest(t, f)
	if len(beforeProjection.candidates) != 0 {
		t.Fatalf("off snapshot unexpectedly queued candidates before tamper: %+v", beforeProjection.candidates)
	}
	var receiptBefore, wrapperBefore []byte
	if err := f.s.db.QueryRow(`SELECT receipt,wrapper FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, bind.ID).
		Scan(&receiptBefore, &wrapperBefore); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(receiptBefore, status.Receipt) || !bytes.Equal(wrapperBefore, status.SignedReceipt) {
		t.Fatal("reference command did not retain its exact successful receipt")
	}

	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Range == nil {
		t.Fatalf("read genuine signed reference receipt: %+v, %v", receipt, err)
	}
	event := rewriteReferencePushSnapshot(t, f, receipt.Range.First, "boundary")
	if event.kind != "reference.added" || event.repo != repoA || event.subject != f.matter || event.command != bind.ID {
		t.Fatalf("rewritten event lost its command binding: %+v", event)
	}

	// A boundary snapshot on this active Matter queues a state candidate. Rebuild
	// that candidate overlay while leaving references, aggregates, and config
	// projections unchanged; reopen must reject the historical mismatch itself,
	// not merely a stale derived table.
	disposition, _, _ := referenceAggregateForTest(t, f, reference)
	var forgedCandidates []step13Candidate
	if err = addStep13Candidate(&forgedCandidates, step13Event{step12Event: event}, repoA, "state", f.matter,
		reference, map[string]string{"disposition": disposition}, ""); err != nil {
		t.Fatal(err)
	}
	if len(forgedCandidates) != 1 {
		t.Fatalf("forged boundary snapshot produced %d candidate rows, want 1", len(forgedCandidates))
	}
	for _, candidate := range forgedCandidates {
		if _, err = f.s.db.Exec(`INSERT INTO m6_tracker_candidates VALUES(?,?,?,?,?,?,?,?,?)`, candidate.domain, candidate.id,
			candidate.repo, candidate.kind, candidate.subject, candidate.ref, candidate.key, candidate.payload, candidate.event); err != nil {
			t.Fatalf("rebuild forged candidate overlay: %v", err)
		}
	}
	wantProjection := beforeProjection
	wantProjection.candidates = append(wantProjection.candidates, forgedCandidates...)
	sortStep13Projection(&wantProjection)
	if got := mustStep13ProjectionForTest(t, f); !sameStep13Projection(got, wantProjection) {
		t.Fatalf("forged event and candidate overlay do not agree: got=%+v want=%+v", got, wantProjection)
	}
	if err = checkStep4State(f.s.db); err != nil {
		t.Fatalf("tampered event should retain valid request/result receipt binding and prefix: %v", err)
	}
	if err = checkStep13State(f.s.db); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("historically substituted reference snapshot accepted: %v", err)
	}
	var receiptAfter, wrapperAfter []byte
	if err = f.s.db.QueryRow(`SELECT receipt,wrapper FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, bind.ID).
		Scan(&receiptAfter, &wrapperAfter); err != nil || !bytes.Equal(receiptAfter, receiptBefore) || !bytes.Equal(wrapperAfter, wrapperBefore) {
		t.Fatalf("snapshot substitution changed the genuine signed receipt: %v", err)
	}

	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, openErr := OpenExisting(f.root); !errors.Is(openErr, ErrInvalidStore) {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("OpenExisting accepted enum-valid substituted snapshot with matching forged overlay: %v", openErr)
	}
}

func rewriteReferencePushSnapshot(t *testing.T, f *claimTestFixture, eventID, level string) step12Event {
	t.Helper()
	var domain, commandID string
	var position uint64
	var record []byte
	if err := f.s.db.QueryRow(`SELECT domain_id,position,record,command_id FROM authority_events WHERE event_id=?`, eventID).
		Scan(&domain, &position, &record, &commandID); err != nil {
		t.Fatal(err)
	}
	var maxPosition uint64
	if err := f.s.db.QueryRow(`SELECT max(position) FROM authority_events WHERE domain_id=?`, domain).Scan(&maxPosition); err != nil || position != maxPosition {
		t.Fatalf("reference event at position %d is not the history tip %d: %v", position, maxPosition, err)
	}
	var fields map[string]cbor.RawMessage
	if err := canonicalDecode(record, &fields); err != nil {
		t.Fatal(err)
	}
	var payload map[string]cbor.RawMessage
	if err := canonicalDecode(fields["payload"], &payload); err != nil {
		t.Fatal(err)
	}
	payload["tracker_push_level"] = cbor.RawMessage(encodeTest(t, level))
	fields["payload"] = cbor.RawMessage(encodeTest(t, payload))
	var err error
	record, err = artifactEncoder.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	if err = f.s.db.QueryRow(`SELECT prefix_digest FROM authority_events WHERE domain_id=? AND position=?`, domain, position-1).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	previousBytes, err := digestRaw(previous)
	if err != nil {
		t.Fatal(err)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(record)))
	h := sha256.New()
	_, _ = h.Write([]byte("wipd/event-prefix-step/v1\x00"))
	_, _ = h.Write(previousBytes)
	_, _ = h.Write(length[:])
	_, _ = h.Write(record)
	if _, err = f.s.db.Exec(`DROP TRIGGER authority_events_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.db.Exec(`UPDATE authority_events SET record=?,prefix_digest=? WHERE domain_id=? AND position=?`,
		record, digestRawBytes(h.Sum(nil)), domain, position); err != nil {
		t.Fatal(err)
	}
	event, err := parseStep12Event(record, domain, position, eventID, commandID)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
