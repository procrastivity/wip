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

func referenceV2Command(f *claimTestFixture, id int, sequence uint64, definition operation.Definition,
	input operation.Input, repo string,
) operation.Command {
	command := referenceAuthorityCommand(f, id, sequence, definition, input, repo)
	command.EnvironmentID = envB
	return command
}

func submitReferenceForTest(t *testing.T, f *claimTestFixture, command operation.Command) *Execution {
	t.Helper()
	peer := f.peer
	if command.EnvironmentID == envB {
		peer = f.peerB
	}
	status, err := f.s.SubmitCommand(context.Background(), command, hashCommand(t, command), peer, f.now)
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
		var from, to string
		switch input := command.Request.Input.(type) {
		case operation.ReferenceRebindInput:
			from, to = input.From, input.To
		case operation.ReferenceRebindV2Input:
			from, to = input.From, input.To
		default:
			t.Fatalf("unexpected rebind input %T", command.Request.Input)
		}
		var payload struct {
			From  string `cbor:"from"`
			To    string `cbor:"to"`
			Level string `cbor:"tracker_push_level"`
		}
		if !step13ClosedPayload(event.payload, &payload, []string{"from", "to", "tracker_push_level"}) ||
			payload.From != from || payload.To != to || payload.Level != "off" {
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
	case operation.ReferenceBindV2Input:
		return input.MatterID
	case operation.ReferenceUnbindInput:
		return input.MatterID
	case operation.ReferenceUnbindV2Input:
		return input.MatterID
	case operation.ReferenceRebindInput:
		return input.MatterID
	case operation.ReferenceRebindV2Input:
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

func TestStrictD121ReferenceV2RequiresCurrentMatterClaimAndRefusesV1(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	claimTestBirthStep(t, f, 31, 101)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, allocation)
	proof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}}
	command := referenceAuthorityCommand(f, 12, 4, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "STRICT", TargetClaims: proof}, repoA)
	status := completeReferenceForTest(t, f, command, 105)
	assertReferenceSuccess(t, f, command, status, "reference.added", "STRICT", "")

	before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	missing := referenceAuthorityCommand(f, 13, 5, operation.ReferenceUnbindV2,
		operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: "STRICT"}, repoA)
	refused := completeReferenceForTest(t, f, missing, 106)
	assertReferenceRefusal(t, f, missing, refused, "refusal.claim-fenced", before, projection)
	if len(refused.SignedReceipt) == 0 {
		t.Fatal("semantic claim refusal did not retain one signed protocol receipt")
	}

	legacy := referenceAuthorityCommand(f, 14, 6, operation.ReferenceBindV1,
		operation.ReferenceBindInput{MatterID: f.matter, Reference: "LEGACY"}, repoA)
	before, projection = f.anchor(t), mustStep13ProjectionForTest(t, f)
	legacyStatus := completeReferenceForTest(t, f, legacy, 107)
	assertReferenceRefusal(t, f, legacy, legacyStatus, "refusal.step8-v1-claim-required", before, projection)
	replay, err := f.s.QueryCommand(context.Background(), domainA, missing.ID, hashCommand(t, missing), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, refused.Receipt) || !bytes.Equal(replay.SignedReceipt, refused.SignedReceipt) {
		t.Fatalf("terminal refusal replay changed receipt: %+v %v", replay, err)
	}
	legacyReplay, err := f.s.QueryCommand(context.Background(), domainA, legacy.ID, hashCommand(t, legacy), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(legacyReplay.Receipt, legacyStatus.Receipt) || !bytes.Equal(legacyReplay.SignedReceipt, legacyStatus.SignedReceipt) {
		t.Fatalf("fresh-v1 terminal replay changed after a current claim exists: %+v %v", legacyReplay, err)
	}
	rebind := referenceAuthorityCommand(f, 15, 7, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: "STRICT", To: "STRICT-NEW", TargetClaims: proof}, repoA)
	rebindStatus := completeReferenceForTest(t, f, rebind, 108)
	assertReferenceSuccess(t, f, rebind, rebindStatus, "reference.rebound", "STRICT-NEW", "STRICT")
	unbind := referenceAuthorityCommand(f, 16, 8, operation.ReferenceUnbindV2,
		operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: "STRICT-NEW", TargetClaims: proof}, repoA)
	unbindStatus := completeReferenceForTest(t, f, unbind, 109)
	assertReferenceSuccess(t, f, unbind, unbindStatus, "reference.removed", "STRICT-NEW", "")
	changedProof := missing
	changedProof.Request.Input = operation.ReferenceUnbindV2Input{
		MatterID: f.matter, Reference: "STRICT",
		TargetClaims: proof,
	}
	if _, err = f.s.SubmitCommand(context.Background(), changedProof, hashCommand(t, changedProof), f.peer, f.now); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-ID changed proof did not conflict with retained refusal: %v", err)
	}
}

func TestStrictD121MissingClaimRefusalReplaysAfterLaterAcquisition(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() { _ = f.s.Close() }()
	command := referenceAuthorityCommand(f, 11, 2, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "LATER"}, repoA)
	before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	status := completeReferenceForTest(t, f, command, 101)
	assertReferenceRefusal(t, f, command, status, "refusal.claim-fenced", before, projection)
	installed := f.anchor(t)
	allocation := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 12, 3, installed, allocation)
	afterAcquire := f.anchor(t)
	replay, err := f.s.QueryCommand(context.Background(), domainA, command.ID, hashCommand(t, command), 7, f.peer, envA, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, status.Receipt) || !bytes.Equal(replay.SignedReceipt, status.SignedReceipt) {
		t.Fatalf("same-ID refusal changed after later claim acquisition: %+v %v", replay, err)
	}
	if after := f.anchor(t); after != afterAcquire {
		t.Fatalf("terminal replay changed the post-acquisition event prefix: before=%+v after=%+v", afterAcquire, after)
	}
}

func TestReferenceAuthoritySharesAcrossMatterReposAndFinalUnbindHasNoCandidate(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	_, claims := acquireStep8TargetClaims(t, f, f.matter, claimTestID(21))
	ref := "  provider-neutral:ticket/17#part  "
	bind := referenceV2Command(f, 13, 3, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: ref, TargetClaims: claims[:1]}, repoA)
	first := completeReferenceForTest(t, f, bind, 600)
	assertReferenceSuccess(t, f, bind, first, "reference.added", ref, "")

	// A second distinct reference on the same Matter proves bind is additive,
	// not the legacy singleton replacement represented by reference.bound.
	otherRef := "ticket/other"
	additive := referenceV2Command(f, 14, 4, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: otherRef, TargetClaims: claims[:1]}, repoA)
	additiveStatus := completeReferenceForTest(t, f, additive, 601)
	assertReferenceSuccess(t, f, additive, additiveStatus, "reference.added", otherRef, "")

	sharedMatter := claimTestID(21)
	shared := referenceV2Command(f, 15, 5, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: sharedMatter, Reference: ref, TargetClaims: claims[1:]}, repoB)
	sharedStatus := completeReferenceForTest(t, f, shared, 602)
	assertReferenceSuccess(t, f, shared, sharedStatus, "reference.added", ref, "")
	if disposition, members, exists := referenceAggregateForTest(t, f, ref); !exists || disposition != "" || members != 2 {
		t.Fatalf("cross-Matter/cross-Repo aggregate = %q/%d exists=%t; want two planned members", disposition, members, exists)
	}
	if disposition, members, exists := referenceAggregateForTest(t, f, otherRef); !exists || disposition != "" || members != 1 {
		t.Fatalf("same-Matter additive aggregate = %q/%d exists=%t; want one member", disposition, members, exists)
	}

	unboundFirst := referenceV2Command(f, 16, 6, operation.ReferenceUnbindV2,
		operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: ref, TargetClaims: claims[:1]}, repoA)
	unboundFirstStatus := completeReferenceForTest(t, f, unboundFirst, 603)
	assertReferenceSuccess(t, f, unboundFirst, unboundFirstStatus, "reference.removed", ref, "")
	if _, members, exists := referenceAggregateForTest(t, f, ref); !exists || members != 1 {
		t.Fatalf("shared aggregate after one unbind has members=%d exists=%t; want one", members, exists)
	}
	unboundFinal := referenceV2Command(f, 17, 7, operation.ReferenceUnbindV2,
		operation.ReferenceUnbindV2Input{MatterID: sharedMatter, Reference: ref, TargetClaims: claims[1:]}, repoB)
	finalStatus := completeReferenceForTest(t, f, unboundFinal, 604)
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
	replay, err := reopened.QueryCommand(context.Background(), domainA, unboundFinal.ID, hashCommand(t, unboundFinal), 7, f.peerB, envB, f.now)
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
	_, claims := acquireStep8TargetClaims(t, f, f.matter)
	existingRef, destinationRef := "existing/ref", "destination/ref"
	bind := referenceV2Command(f, 13, 2, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: existingRef, TargetClaims: claims}, repoA)
	completeReferenceForTest(t, f, bind, 600)

	refusals := []struct {
		id, sequence, event int
		definition          operation.Definition
		input               operation.Input
		repo, problem       string
	}{
		{14, 3, 601, operation.ReferenceBindV2, operation.ReferenceBindV2Input{MatterID: f.matter, Reference: existingRef, TargetClaims: claims}, repoA, "refusal.reference-exists"},
		{15, 4, 602, operation.ReferenceUnbindV2, operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: "missing/ref", TargetClaims: claims}, repoA, "refusal.reference-missing"},
		{16, 5, 603, operation.ReferenceRebindV2, operation.ReferenceRebindV2Input{MatterID: f.matter, From: "missing/ref", To: destinationRef, TargetClaims: claims}, repoA, "refusal.reference-missing"},
	}
	for _, test := range refusals {
		command := referenceV2Command(f, test.id, uint64(test.sequence), test.definition, test.input, test.repo)
		before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
		status := completeReferenceForTest(t, f, command, test.event)
		assertReferenceRefusal(t, f, command, status, test.problem, before, projection)
	}

	addDestination := referenceV2Command(f, 17, 6, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: destinationRef, TargetClaims: claims}, repoA)
	completeReferenceForTest(t, f, addDestination, 604)
	conflict := referenceV2Command(f, 18, 7, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: existingRef, To: destinationRef, TargetClaims: claims}, repoA)
	before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	conflictStatus := completeReferenceForTest(t, f, conflict, 605)
	assertReferenceRefusal(t, f, conflict, conflictStatus, "refusal.reference-destination-exists", before, projection)

	wrongRepo := referenceV2Command(f, 19, 8, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "wrong/repo", TargetClaims: claims}, repoB)
	before, projection = f.anchor(t), mustStep13ProjectionForTest(t, f)
	wrongRepoStatus := completeReferenceForTest(t, f, wrongRepo, 606)
	assertReferenceRefusal(t, f, wrongRepo, wrongRepoStatus, "refusal.reference-matter", before, projection)

	wrongDomain := referenceV2Command(f, 20, 9, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: domainB, Reference: "wrong/domain", TargetClaims: claims}, repoA)
	before, projection = f.anchor(t), mustStep13ProjectionForTest(t, f)
	wrongDomainStatus := completeReferenceForTest(t, f, wrongDomain, 607)
	assertReferenceRefusal(t, f, wrongDomain, wrongDomainStatus, "refusal.reference-matter", before, projection)

	outsideRepo := referenceV2Command(f, 21, 10, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "outside/domain", TargetClaims: claims}, claimTestID(990))
	if _, err := f.s.SubmitCommand(context.Background(), outsideRepo, hashCommand(t, outsideRepo), f.peerB, f.now); !errors.Is(err, ErrFenced) {
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
	_, claims := acquireStep8TargetClaims(t, f, f.matter)
	from, to := "source/ref", "destination/ref"
	for _, item := range []struct {
		id, sequence, event int
		definition          operation.Definition
		input               operation.Input
	}{
		{13, 2, 600, operation.ReferenceBindV2, operation.ReferenceBindV2Input{MatterID: f.matter, Reference: from, TargetClaims: claims}},
		{14, 3, 601, operation.ReferenceBindV2, operation.ReferenceBindV2Input{MatterID: f.matter, Reference: to, TargetClaims: claims}},
		{15, 4, 602, operation.ReferenceUnbindV2, operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: to, TargetClaims: claims}},
	} {
		command := referenceV2Command(f, item.id, uint64(item.sequence), item.definition, item.input, repoA)
		completeReferenceForTest(t, f, command, item.event)
	}
	rebind := referenceV2Command(f, 16, 5, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: from, To: to, TargetClaims: claims}, repoA)
	status := completeReferenceForTest(t, f, rebind, 603)
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
	if fromRow.removed != claimTestID(603) || fromRow.last != claimTestID(603) ||
		toRow.removed != "" || toRow.birth != claimTestID(601) || toRow.last != claimTestID(603) {
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
	replay, err := reopened.QueryCommand(context.Background(), domainA, rebind.ID, hashCommand(t, rebind), 7, f.peerB, envB, f.now)
	if err != nil || !bytes.Equal(replay.Receipt, status.Receipt) || !bytes.Equal(replay.SignedReceipt, status.SignedReceipt) {
		t.Fatalf("reopened rebind receipt changed: status=%+v error=%v", replay, err)
	}
}

func TestStrictD121ReferenceRebindTargetsOnlyItsMatterAcrossAsymmetricMemberships(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerF, claims := acquireStep8TargetClaims(t, f, f.matter, claimTestID(21), claimTestID(22))
	claimFor := func(matter string) operation.TargetClaim {
		t.Helper()
		for _, claim := range claims {
			if claim.MatterID == matter {
				return claim
			}
		}
		t.Fatalf("missing claim for Matter %s", matter)
		return operation.TargetClaim{}
	}
	claimM1, claimM2, claimM3 := claimFor(f.matter), claimFor(claimTestID(21)), claimFor(claimTestID(22))

	start := step12Command(f, 800, 4, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, claimM1.ClaimID)
	start.EnvironmentID = envB
	startPending, err := f.s.SubmitCommand(context.Background(), start, hashCommand(t, start), peerF, f.now)
	if err != nil || startPending.Owner == nil {
		t.Fatalf("submit active M1 lifecycle: status=%+v error=%v", startPending, err)
	}
	started, err := f.s.CompleteConnectedLifecycle(context.Background(), startPending.Owner,
		[]string{claimTestID(900), claimTestID(901)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete active M1 lifecycle: %v", err)
	}
	claimTestAcknowledge(t, f, claimTestID(81), 1, started)

	secondStart := step12Command(f, 807, 5, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: claimTestID(22)}, claimM3.ClaimID)
	secondStart.EnvironmentID = envB
	secondStart.Request.Context.Repo = repoC
	secondStartPending, err := f.s.SubmitCommand(context.Background(), secondStart, hashCommand(t, secondStart), peerF, f.now)
	if err != nil || secondStartPending.Owner == nil {
		t.Fatalf("submit M3 lifecycle start: status=%+v error=%v", secondStartPending, err)
	}
	secondStarted, err := f.s.CompleteConnectedLifecycle(context.Background(), secondStartPending.Owner,
		[]string{claimTestID(902), claimTestID(903)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete M3 lifecycle start: %v", err)
	}
	claimTestAcknowledge(t, f, claimTestID(83), 1, secondStarted)
	referenceSequence, event := uint64(6), 904
	bind := func(id int, matter, reference string, claim operation.TargetClaim, repo string) CommandStatus {
		t.Helper()
		command := referenceV2Command(f, id, referenceSequence, operation.ReferenceBindV2,
			operation.ReferenceBindV2Input{MatterID: matter, Reference: reference, TargetClaims: []operation.TargetClaim{claim}}, repo)
		referenceSequence++
		status := completeReferenceForTest(t, f, command, event)
		event++
		assertReferenceSuccess(t, f, command, status, "reference.added", reference, "")
		return status
	}
	bind(801, f.matter, "R", claimM1, repoA)
	bind(802, claimTestID(22), "R", claimM3, repoC)
	finish := step12Command(f, 803, referenceSequence, operation.MatterFinishV1,
		operation.MatterFinishInput{MatterID: claimTestID(22)}, claimM3.ClaimID)
	finish.EnvironmentID = envB
	finish.Request.Context.Repo = repoC
	finishPending, err := f.s.SubmitCommand(context.Background(), finish, hashCommand(t, finish), peerF, f.now)
	if err != nil || finishPending.Owner == nil {
		t.Fatalf("submit sealed M3 lifecycle: status=%+v error=%v", finishPending, err)
	}
	finished, err := f.s.CompleteConnectedLifecycle(context.Background(), finishPending.Owner,
		[]string{claimTestID(event), claimTestID(event + 1)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete sealed M3 lifecycle: %v", err)
	}
	referenceSequence++
	event += 2
	claimTestReceipt(t, finished, "result.succeeded", map[string]any{
		"matter_id": claimTestID(22), "state": "done", "became_sealed": true,
	}, event-2)
	bind(804, claimTestID(21), "S", claimM2, repoB)

	beforeAnchor, beforeProjection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	invalidProof := referenceV2Command(f, 805, referenceSequence, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: "R", To: "S", TargetClaims: []operation.TargetClaim{{MatterID: f.matter, ClaimID: claimM1.ClaimID, ClaimEpoch: claimM1.ClaimEpoch + 1}}}, repoA)
	referenceSequence++
	invalid := completeReferenceForTest(t, f, invalidProof, event)
	assertReferenceRefusal(t, f, invalidProof, invalid, "refusal.claim-fenced", beforeAnchor, beforeProjection)
	if after := mustStep13ProjectionForTest(t, f); !sameStep13Projection(after, beforeProjection) {
		t.Fatalf("invalid M1 proof changed memberships/candidates: before=%+v after=%+v", beforeProjection, after)
	}

	rebind := referenceV2Command(f, 806, referenceSequence, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: "R", To: "S", TargetClaims: []operation.TargetClaim{claimM1}}, repoA)
	referenceSequence++
	rebound := completeReferenceForTest(t, f, rebind, event+1)
	assertReferenceSuccess(t, f, rebind, rebound, "reference.rebound", "S", "R")
	projection := mustStep13ProjectionForTest(t, f)
	if len(projection.references) != 4 || len(projection.candidates) != 0 {
		t.Fatalf("candidate-policy-off rebind projection = %+v; want four retained membership records and no candidates", projection)
	}
	for _, want := range []struct {
		matter, ref, removed string
	}{
		{f.matter, "R", claimTestID(event + 1)},
		{f.matter, "S", ""},
		{claimTestID(21), "S", ""},
		{claimTestID(22), "R", ""},
	} {
		var removed sql.NullString
		if err := f.s.db.QueryRow(`SELECT removed_event_id FROM m6_tracker_references WHERE domain_id=? AND matter_id=? AND ref=?`,
			domainA, want.matter, want.ref).Scan(&removed); err != nil || removed.String != want.removed || removed.Valid != (want.removed != "") {
			t.Fatalf("membership %s/%s removed=%q valid=%t, want %q: %v", want.matter, want.ref, removed.String, removed.Valid, want.removed, err)
		}
	}
	for _, want := range []struct {
		ref         string
		disposition string
		members     int64
	}{
		{"R", "completed", 1}, {"S", "active", 2},
	} {
		if disposition, members, exists := referenceAggregateForTest(t, f, want.ref); !exists || disposition != want.disposition || members != want.members {
			t.Fatalf("aggregate %s = %q/%d exists=%t, want %q/%d", want.ref, disposition, members, exists, want.disposition, want.members)
		}
	}
	if len(peerF.PeerCertificates) == 0 {
		t.Fatal("F-owned unrelated Matter claims were not established")
	}
}

func TestStrictD121ReferenceRebindEmitsAsymmetricCandidatesWithEnabledPolicy(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerF, claims := acquireStep8TargetClaims(t, f, claimTestID(21), claimTestID(22))
	f.clone, f.worktree = claimTestID(23), claimTestID(22)
	sequence, standDownSequence, allocationIndex, eventIndex := uint64(4), uint64(1), 3, 600
	claimM1 := acquireStrictD121ClaimAtEpoch(t, f, peerF, f.matter, repoA, 1,
		&sequence, &standDownSequence, &allocationIndex, &eventIndex)
	claimM2, claimM3 := claims[0], claims[1]
	startM1 := step12Command(f, 810, 5, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, claimM1.ClaimID)
	startedM1, err := f.s.SubmitCommand(context.Background(), startM1, hashCommand(t, startM1), f.peer, f.now)
	if err != nil || startedM1.Owner == nil {
		t.Fatalf("submit E-owned M1 lifecycle start: status=%+v error=%v", startedM1, err)
	}
	started, err := f.s.CompleteConnectedLifecycle(context.Background(), startedM1.Owner,
		[]string{claimTestID(603), claimTestID(604)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete E-owned M1 lifecycle start: %v", err)
	}
	claimTestAcknowledge(t, f, claimTestID(83), 1, started)
	startM3 := step12Command(f, 811, 3, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: claimTestID(22)}, claimM3.ClaimID)
	startM3.EnvironmentID = envB
	startM3.Request.Context.Repo = repoC
	startedM3, err := f.s.SubmitCommand(context.Background(), startM3, hashCommand(t, startM3), peerF, f.now)
	if err != nil || startedM3.Owner == nil {
		t.Fatalf("submit F-owned M3 lifecycle start: status=%+v error=%v", startedM3, err)
	}
	started, err = f.s.CompleteConnectedLifecycle(context.Background(), startedM3.Owner,
		[]string{claimTestID(605), claimTestID(606)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete F-owned M3 lifecycle start: %v", err)
	}
	claimTestAcknowledge(t, f, claimTestID(82), 1, started)
	refSequence, event := uint64(6), 607
	bind := func(id int, matter, reference, repo string, proof operation.TargetClaim) {
		t.Helper()
		command := referenceAuthorityCommand(f, id, refSequence, operation.ReferenceBindV2,
			operation.ReferenceBindV2Input{MatterID: matter, Reference: reference, TargetClaims: []operation.TargetClaim{proof}}, repo)
		refSequence++
		status := completeReferenceForTest(t, f, command, event)
		event++
		assertReferenceSuccess(t, f, command, status, "reference.added", reference, "")
	}
	bind(812, f.matter, "R", repoA, claimM1)
	bindM3 := referenceV2Command(f, 813, 4, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: claimTestID(22), Reference: "R", TargetClaims: []operation.TargetClaim{claimM3}}, repoC)
	boundM3 := completeReferenceForTest(t, f, bindM3, event)
	event++
	assertReferenceSuccess(t, f, bindM3, boundM3, "reference.added", "R", "")
	finishM3 := step12Command(f, 814, 5, operation.MatterFinishV1,
		operation.MatterFinishInput{MatterID: claimTestID(22)}, claimM3.ClaimID)
	finishM3.EnvironmentID = envB
	finishM3.Request.Context.Repo = repoC
	finishPending, err := f.s.SubmitCommand(context.Background(), finishM3, hashCommand(t, finishM3), peerF, f.now)
	if err != nil || finishPending.Owner == nil {
		t.Fatalf("submit F-owned M3 finish: status=%+v error=%v", finishPending, err)
	}
	finished, err := f.s.CompleteConnectedLifecycle(context.Background(), finishPending.Owner,
		[]string{claimTestID(event), claimTestID(event + 1)}, f.now, signWith(f.key))
	if err != nil {
		t.Fatalf("complete F-owned M3 finish: %v", err)
	}
	event += 2
	claimTestReceipt(t, finished, "result.succeeded", map[string]any{
		"matter_id": claimTestID(22), "state": "done", "became_sealed": true,
	}, event-2)
	bindM2 := referenceV2Command(f, 815, 6, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: claimTestID(21), Reference: "S", TargetClaims: []operation.TargetClaim{claimM2}}, repoB)
	boundM2 := completeReferenceForTest(t, f, bindM2, event)
	event++
	assertReferenceSuccess(t, f, bindM2, boundM2, "reference.added", "S", "")
	var openClaimID, ownerEnvironment string
	var claimEpoch uint64
	if err = f.s.db.QueryRow(`SELECT claim_id,claim_epoch,owner_environment_id FROM claims WHERE domain_id=? AND matter_id=? AND close_command_id IS NULL`,
		domainA, claimTestID(21)).Scan(&openClaimID, &claimEpoch, &ownerEnvironment); err != nil ||
		openClaimID != claimM2.ClaimID || claimEpoch != claimM2.ClaimEpoch || ownerEnvironment != envB {
		t.Fatalf("unrelated F-owned S claim changed: id=%s epoch=%d owner=%s error=%v", openClaimID, claimEpoch, ownerEnvironment, err)
	}
	beforeAnchor, beforeProjection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	invalidProof := referenceAuthorityCommand(f, 816, refSequence, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: "R", To: "S", TargetClaims: []operation.TargetClaim{{MatterID: f.matter, ClaimID: claimM1.ClaimID, ClaimEpoch: claimM1.ClaimEpoch + 1}}}, repoA)
	refSequence++
	invalid := completeReferenceForTest(t, f, invalidProof, event)
	assertReferenceRefusal(t, f, invalidProof, invalid, "refusal.claim-fenced", beforeAnchor, beforeProjection)
	if after := mustStep13ProjectionForTest(t, f); !sameStep13Projection(after, beforeProjection) {
		t.Fatalf("invalid E-owned M1 proof changed membership/candidates: before=%+v after=%+v", beforeProjection, after)
	}
	rebind := referenceAuthorityCommand(f, 817, refSequence, operation.ReferenceRebindV2,
		operation.ReferenceRebindV2Input{MatterID: f.matter, From: "R", To: "S", TargetClaims: []operation.TargetClaim{claimM1}}, repoA)
	rebound := completeReferenceForTest(t, f, rebind, event+1)
	assertReferenceSuccess(t, f, rebind, rebound, "reference.rebound", "S", "R")
	projection := mustStep13ProjectionForTest(t, f)
	if len(projection.references) != 4 || len(projection.candidates) != 0 {
		t.Fatalf("candidate-policy-on rebind projection has %d memberships and %d candidates: %+v", len(projection.references), len(projection.candidates), projection)
	}
	nodes, err := step13Nodes(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	events, err := step13Events(f.s.db)
	if err != nil {
		t.Fatal(err)
	}
	var candidateEventFound bool
	for index := range events {
		if events[index].id == claimTestID(event+1) {
			configEvent := step13TestEvent(int(events[index].position), "config.set", repoA,
				map[string]any{"key": "tracker.push-level", "value": "boundary"})
			configEvent.id = claimTestID(event)
			events[index].position++
			events = append(events, step13Event{})
			copy(events[index+1:], events[index:len(events)-1])
			events[index] = configEvent
			index++
			level, marshalErr := artifactEncoder.Marshal("boundary")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			events[index].payload["tracker_push_level"] = level
			candidateEventFound = true
		}
	}
	if !candidateEventFound {
		t.Fatalf("missing retained rebind event %s", claimTestID(event+1))
	}
	candidateProjection, err := deriveStep13Projection(nodes, events)
	if err != nil {
		t.Fatalf("derive policy-enabled asymmetric rebind candidates: %v", err)
	}
	wantCandidates := map[string]string{"R": `{"disposition":"completed"}`, "S": `{"disposition":"active"}`}
	if len(candidateProjection.candidates) != 2 {
		t.Fatalf("policy-enabled asymmetric rebind emitted %d candidates: %+v", len(candidateProjection.candidates), candidateProjection.candidates)
	}
	for _, candidate := range candidateProjection.candidates {
		if candidate.kind != "state" || candidate.repo != repoA || candidate.subject != f.matter || candidate.event != claimTestID(event+1) ||
			candidate.payload != wantCandidates[candidate.ref] {
			t.Fatalf("unexpected R/S candidate from asymmetric rebind: %+v", candidate)
		}
		delete(wantCandidates, candidate.ref)
	}
	if len(wantCandidates) != 0 {
		t.Fatalf("missing exact rebind candidates: %v", wantCandidates)
	}
	for _, want := range []struct{ matter, ref, removed string }{
		{f.matter, "R", claimTestID(event + 1)},
		{f.matter, "S", ""},
		{claimTestID(21), "S", ""},
		{claimTestID(22), "R", ""},
	} {
		var removed sql.NullString
		if err = f.s.db.QueryRow(`SELECT removed_event_id FROM m6_tracker_references WHERE domain_id=? AND matter_id=? AND ref=?`,
			domainA, want.matter, want.ref).Scan(&removed); err != nil || removed.String != want.removed || removed.Valid != (want.removed != "") {
			t.Fatalf("asymmetric membership %s/%s removed=%q valid=%t, want %q: %v", want.matter, want.ref, removed.String, removed.Valid, want.removed, err)
		}
	}
}

func TestStrictD121UnbindReplayPreservesForeignReplacementMembership(t *testing.T) {
	f := newClaimTestFixture(t)
	defer func() {
		if f.s != nil {
			_ = f.s.Close()
		}
	}()
	claimTestBirthStep(t, f, 31, 101)
	installed := f.anchor(t)
	oldClaim := claimTestAllocation(1, installed, 102, 103, 104)
	f.acquire(t, 11, 3, installed, oldClaim)
	oldProof := []operation.TargetClaim{{MatterID: f.matter, ClaimID: oldClaim.ClaimID, ClaimEpoch: 1}}
	bind := referenceAuthorityCommand(f, 12, 4, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "REPLACED", TargetClaims: oldProof}, repoA)
	bound := completeReferenceForTest(t, f, bind, 105)
	assertReferenceSuccess(t, f, bind, bound, "reference.added", "REPLACED", "")
	unbind := referenceAuthorityCommand(f, 13, 5, operation.ReferenceUnbindV2,
		operation.ReferenceUnbindV2Input{MatterID: f.matter, Reference: "REPLACED", TargetClaims: oldProof}, repoA)
	unbindHash := hashCommand(t, unbind)
	removed := completeReferenceForTest(t, f, unbind, 106)
	assertReferenceSuccess(t, f, unbind, removed, "reference.removed", "REPLACED", "")

	peerF := dependencyEnvironmentB(t, f)
	f.peerB = peerF
	authorizedStandDownForStrictD121(t, f, peerF, 700, 1, 0x7b, oldClaim, 1, claimTestID(51), repoA, 107)
	replacementStart := f.anchor(t)
	raw, replacementHash := f.command(t, 701, 2, "claim.acquire", nil, map[string]any{
		"matter_id": f.matter, "worktree_id": f.worktree, "dispatch_mode": "anonymous-matter",
		"requested_dispatch_id": claimTestID(751),
	}, envB)
	pending, err := f.s.SubmitClaimAcquire(context.Background(), raw, replacementHash, replacementStart, peerF, f.now)
	if err != nil || pending.Owner == nil {
		t.Fatalf("submit replacement reference claim: status=%+v error=%v", pending, err)
	}
	replacement := claimTestAllocation(2, replacementStart, 109, 110, 111)
	if _, _, err = f.s.CompleteClaimAcquire(context.Background(), pending.Owner, replacement, f.now, signWith(f.key)); err != nil {
		t.Fatalf("complete replacement reference claim: %v", err)
	}
	newBind := referenceV2Command(f, 702, 3, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "REPLACED", TargetClaims: []operation.TargetClaim{{MatterID: f.matter, ClaimID: replacement.ClaimID, ClaimEpoch: 2}}}, repoA)
	newBound := completeReferenceForTest(t, f, newBind, 112)
	assertReferenceSuccess(t, f, newBind, newBound, "reference.added", "REPLACED", "")
	before, projection := f.anchor(t), mustStep13ProjectionForTest(t, f)
	replay, err := f.s.SubmitCommand(context.Background(), unbind, unbindHash, f.peer, f.now)
	if err != nil || replay.Pending || !bytes.Equal(replay.Receipt, removed.Receipt) || !bytes.Equal(replay.SignedReceipt, removed.SignedReceipt) {
		t.Fatalf("E unbind replay changed terminal receipt/range: status=%+v error=%v", replay, err)
	}
	if after := f.anchor(t); after != before {
		t.Fatalf("E unbind replay appended events after F replacement: before=%+v after=%+v", before, after)
	}
	if after := mustStep13ProjectionForTest(t, f); !sameStep13Projection(after, projection) {
		t.Fatalf("E unbind replay changed F membership: before=%+v after=%+v", projection, after)
	}
	if len(projection.references) != 1 || projection.references[0].matter != f.matter ||
		projection.references[0].ref != "REPLACED" || projection.references[0].removed != "" || len(projection.candidates) != 0 {
		t.Fatalf("replacement membership projection = %+v", projection)
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

	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 102, 103, 104)
	f.acquire(t, 13, 2, anchor, allocation)
	claims := []operation.TargetClaim{{MatterID: f.matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}}
	bind := referenceAuthorityCommand(f, 12, 3, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: "lifecycle/ref", TargetClaims: claims}, repoA)
	bindStatus := completeReferenceForTest(t, f, bind, 105)
	if disposition, members, exists := referenceAggregateForTest(t, f, "lifecycle/ref"); !exists || disposition != "" || members != 1 {
		t.Fatalf("bound planned Matter aggregate = %q/%d exists=%t", disposition, members, exists)
	}
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
	claims := []operation.TargetClaim{{MatterID: f.matter, ClaimID: allocation.ClaimID, ClaimEpoch: 1}}
	start := step12Command(f, 12, 3, operation.MatterStartV1,
		operation.NodeLifecycleInput{NodeID: f.matter}, allocation.ClaimID)
	started := completeLifecycleCommand(t, f, start, []string{claimTestID(104)})
	claimTestAcknowledge(t, f, allocation.JournalID, 1, started)

	const reference = "active/ref"
	bind := referenceAuthorityCommand(f, 13, 4, operation.ReferenceBindV2,
		operation.ReferenceBindV2Input{MatterID: f.matter, Reference: reference, TargetClaims: claims}, repoA)
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
