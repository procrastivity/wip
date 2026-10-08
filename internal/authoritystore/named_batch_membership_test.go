package authoritystore

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func namedBatchMembershipCommand(id, domain, environment, repo string, sequence uint64, operationID operation.ID, input operation.Input) operation.Command {
	return operation.Command{
		ID: id, AuthorityDomainID: domain, ExpectedAuthorityEpoch: 7,
		EnvironmentID: environment, EnvironmentSequence: sequence,
		ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: id,
		Request: operation.Request{
			Operation: operationID, Actor: "human", Context: operation.Context{Repo: repo}, Input: input,
		},
	}
}

func completeNamedBatchMembershipForTest(t *testing.T, store *Store, command operation.Command, peer tls.ConnectionState,
	artifactKey ed25519.PrivateKey, now time.Time, eventNumber int,
) CommandStatus {
	t.Helper()
	status, err := store.SubmitCommand(context.Background(), command, hashCommand(t, command), peer, now)
	if err != nil || status.Owner == nil || !status.Pending {
		t.Fatalf("submit %s: status=%+v error=%v", command.Request.Operation, status, err)
	}
	var subject string
	var output operation.Output
	switch input := command.Request.Input.(type) {
	case operation.BatchMembershipInput:
		subject = input.BatchID
		output = operation.BatchMembershipOutput(input)
	case operation.BatchDismissInput:
		subject = input.BatchID
		output = operation.BatchDismissOutput(input)
	default:
		t.Fatalf("unexpected named Batch membership input %T", command.Request.Input)
	}
	status, err = store.CompleteCommand(context.Background(), status.Owner,
		operation.Result{Code: operation.ResultSucceeded, Output: output}, subject, claimTestID(eventNumber), now, signWith(artifactKey))
	if err != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("complete %s: status=%+v error=%v", command.Request.Operation, status, err)
	}
	return status
}

func completeNamedBatchMatterForTest(t *testing.T, store *Store, command operation.Command, peer tls.ConnectionState,
	artifactKey ed25519.PrivateKey, now time.Time, subjectNumber, eventNumber int,
) {
	t.Helper()
	input := command.Request.Input.(operation.MatterCreateInput)
	status, err := store.SubmitCommand(context.Background(), command, hashCommand(t, command), peer, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("submit Matter birth: status=%+v error=%v", status, err)
	}
	subject := claimTestID(subjectNumber)
	_, err = store.CompleteCommand(context.Background(), status.Owner, operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.MatterCreateOutput{ID: subject, Locator: input.Locator, Title: input.Title},
	}, subject, claimTestID(eventNumber), now, signWith(artifactKey))
	if err != nil {
		t.Fatalf("complete Matter birth: %v", err)
	}
}

func requireNamedBatchRefusal(t *testing.T, status CommandStatus, want operation.ProblemCode) {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Result.Code != string(operation.ResultRefused) || receipt.Result.Problem == nil ||
		*receipt.Result.Problem != string(want) || receipt.Range != nil || receipt.Result.Output != nil {
		t.Fatalf("named Batch refusal = %+v, %v; want %s with no event/output", receipt.Result, err, want)
	}
}

func assertActiveNamedBatchPairs(t *testing.T, store *Store, want map[string]bool) {
	t.Helper()
	rows, err := store.db.Query(`SELECT batch_id,matter_id FROM m6_named_batch_memberships WHERE domain_id=? AND left_event_id IS NULL`, domainA)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for rows.Next() {
		var batchID, matterID string
		if err = rows.Scan(&batchID, &matterID); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		got[batchID+"\x00"+matterID] = true
	}
	if err = rows.Err(); err == nil {
		err = rows.Close()
	}
	if err != nil || len(got) != len(want) {
		t.Fatalf("active named Batch pairs=%v, err=%v; want %v", got, err, want)
	}
	for pair := range want {
		if !got[pair] {
			t.Fatalf("active named Batch pair %q absent; got %v", pair, got)
		}
	}
}

func TestNamedBatchMembershipExactPairsReplayRefusalAndDurableLifetime(t *testing.T) {
	fixture := newSweepFixture(t, false, false, false)
	store, now := fixture.f.s, fixture.f.now
	ctx := context.Background()
	var sequence uint64
	if err := store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	sequence++

	sealedBatchID, emptyBatchID, secondBatchID := claimTestID(1301), claimTestID(1304), claimTestID(1307)
	sealedBatch := namedBatchCommand(claimTestID(1300), domainA, envA, repoA, sequence, "sealed Matter group")
	completeNamedBatchForTest(t, store, sealedBatch, fixture.f.peer, fixture.f.key, now, sealedBatchID, claimTestID(1302))
	sequence++
	emptyBatch := namedBatchCommand(claimTestID(1303), domainA, envA, repoA, sequence, "empty group")
	completeNamedBatchForTest(t, store, emptyBatch, fixture.f.peer, fixture.f.key, now, emptyBatchID, claimTestID(1305))
	sequence++
	secondBatch := namedBatchCommand(claimTestID(1306), domainA, envA, repoA, sequence, "second group")
	completeNamedBatchForTest(t, store, secondBatch, fixture.f.peer, fixture.f.key, now, secondBatchID, claimTestID(1308))
	sequence++
	secondMatterCommand := matterCommand(claimTestID(1309), sequence, "s9b-second-matter")
	completeNamedBatchMatterForTest(t, store, secondMatterCommand, fixture.f.peer, fixture.f.key, now, 1310, 1311)
	sequence++
	sealedMatterID, secondMatterID := fixture.f.matter, claimTestID(1310)
	join := func(commandNumber int, batchID, matterID string) (operation.Command, CommandStatus) {
		t.Helper()
		command := namedBatchMembershipCommand(claimTestID(commandNumber), domainA, envA, repoA, sequence,
			operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID})
		sequence++
		return command, completeNamedBatchMembershipForTest(t, store, command, fixture.f.peer, fixture.f.key, now, commandNumber+100)
	}
	leave := func(commandNumber int, batchID, matterID string) (operation.Command, CommandStatus) {
		t.Helper()
		command := namedBatchMembershipCommand(claimTestID(commandNumber), domainA, envA, repoA, sequence,
			operation.BatchLeaveV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID})
		sequence++
		return command, completeNamedBatchMembershipForTest(t, store, command, fixture.f.peer, fixture.f.key, now, commandNumber+100)
	}
	_, sealedJoin := join(1312, sealedBatchID, sealedMatterID)
	if receipt, err := readReceipt(sealedJoin.Receipt); err != nil || receipt.Range == nil || receipt.Range.Count != 1 {
		t.Fatalf("joining a sealed Matter must be a durable membership event: receipt=%+v err=%v", receipt, err)
	} else {
		var eventRecord []byte
		if err = store.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, receipt.Range.First).Scan(&eventRecord); err != nil {
			t.Fatal(err)
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(eventRecord,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		payload, payloadOK := fields["payload"].(map[string]any)
		if decodeErr != nil || fields["kind"] != "batch.joined" || fields["subject_id"] != sealedBatchID || fields["repo_id"] != nil ||
			!payloadOK || payload["matter_id"] != sealedMatterID {
			t.Fatalf("membership event is not Batch-subject/null-Repo exact pair: fields=%v err=%v", fields, decodeErr)
		}
	}
	firstJoin, firstJoinStatus := join(1313, sealedBatchID, secondMatterID)
	join(1314, secondBatchID, secondMatterID)
	beforeDuplicate, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil {
		t.Fatal(err)
	}
	_, duplicateJoin := join(1315, secondBatchID, secondMatterID)
	if receipt, err := readReceipt(duplicateJoin.Receipt); err != nil || receipt.Result.Code != string(operation.ResultSucceeded) ||
		receipt.Range != nil || receipt.Result.Output == nil {
		t.Fatalf("fresh duplicate join must succeed without a second event: receipt=%+v err=%v", receipt, err)
	}
	afterDuplicate, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || afterDuplicate != beforeDuplicate {
		t.Fatalf("fresh duplicate join emitted a semantic effect: before=%+v after=%+v err=%v", beforeDuplicate, afterDuplicate, err)
	}
	var membershipEpisodes int
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batch_memberships WHERE domain_id=?`, domainA).Scan(&membershipEpisodes); err != nil || membershipEpisodes != 3 {
		t.Fatalf("asymmetric named-Batch membership episodes=%d err=%v; want 3", membershipEpisodes, err)
	}
	pairKey := func(batchID, matterID string) string { return batchID + "\x00" + matterID }
	assertActiveNamedBatchPairs(t, store, map[string]bool{
		pairKey(sealedBatchID, sealedMatterID): true,
		pairKey(sealedBatchID, secondMatterID): true,
		pairKey(secondBatchID, secondMatterID): true,
	})
	_, missingLeave := leave(1316, secondBatchID, sealedMatterID)
	requireNamedBatchRefusal(t, missingLeave, operation.ProblemBatchMembershipMissing)
	afterMissingLeave, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || afterMissingLeave != beforeDuplicate {
		t.Fatalf("missing-pair leave changed event prefix: before=%+v after=%+v err=%v", beforeDuplicate, afterMissingLeave, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batch_memberships WHERE domain_id=?`, domainA).Scan(&membershipEpisodes); err != nil || membershipEpisodes != 3 {
		t.Fatalf("missing-pair leave changed membership projection: episodes=%d err=%v", membershipEpisodes, err)
	}

	var anonymousBatchID string
	if err = store.db.QueryRow(`SELECT batch_id FROM anonymous_batches WHERE domain_id=? AND matter_id=?`, domainA, sealedMatterID).Scan(&anonymousBatchID); err != nil {
		t.Fatalf("find disposable anonymous Batch target: %v", err)
	}
	_, anonymousTarget := join(1317, anonymousBatchID, sealedMatterID)
	requireNamedBatchRefusal(t, anonymousTarget, operation.ProblemBatchTargetMissing)
	_, leaveExisting := leave(1318, sealedBatchID, secondMatterID)
	if receipt, err := readReceipt(leaveExisting.Receipt); err != nil || receipt.Range == nil || receipt.Range.Count != 1 {
		t.Fatalf("leave of exact active pair did not emit one event: receipt=%+v err=%v", receipt, err)
	}
	join(1319, sealedBatchID, secondMatterID)
	assertActiveNamedBatchPairs(t, store, map[string]bool{
		pairKey(sealedBatchID, sealedMatterID): true,
		pairKey(sealedBatchID, secondMatterID): true,
		pairKey(secondBatchID, secondMatterID): true,
	})
	leave(1331, sealedBatchID, secondMatterID)
	assertActiveNamedBatchPairs(t, store, map[string]bool{
		pairKey(sealedBatchID, sealedMatterID): true,
		pairKey(secondBatchID, secondMatterID): true,
	})
	leave(1332, secondBatchID, secondMatterID)
	join(1333, secondBatchID, secondMatterID)
	leave(1334, secondBatchID, secondMatterID)
	assertActiveNamedBatchPairs(t, store, map[string]bool{
		pairKey(sealedBatchID, sealedMatterID): true,
	})

	// A separate domain owns both targets below. Same-store target probing must
	// refuse them as cross-domain rather than treating their IDs as local.
	domainBInfo, ownerB := identity(domainB, 7)
	if err = store.BootstrapDomain(ctx, domainBInfo, repoC); err != nil {
		t.Fatalf("bootstrap second Batch domain: %v", err)
	}
	artifactB := key("named-batch-membership-domain-b-artifact")
	artifactBID, _ := spkiID(artifactB.Public())
	artifactBCert := signedTest(t, ownerB, "authority-artifact-key", "wipd.authority-artifact-key/1", domainB, domainBInfo.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainB, "authority_epoch": uint64(7),
		"key_generation": uint64(1), "key_id": artifactBID, "ed25519_public_key": []byte(artifactB.Public().(ed25519.PublicKey)),
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err = store.RegisterArtifactKey(ctx, domainB, artifactBCert, now); err != nil {
		t.Fatal(err)
	}
	peerB := enrollNamedBatchEnvironment(t, store, domainB, envB, domainBInfo, ownerB, now, 3)
	foreignBatchID, foreignMatterID := claimTestID(1321), claimTestID(1324)
	foreignBatch := namedBatchCommand(claimTestID(1320), domainB, envB, repoC, 1, "foreign Batch")
	completeNamedBatchForTest(t, store, foreignBatch, peerB, artifactB, now, foreignBatchID, claimTestID(1322))
	foreignMatter := operation.Command{
		ID: claimTestID(1323), AuthorityDomainID: domainB, ExpectedAuthorityEpoch: 7,
		EnvironmentID: envB, EnvironmentSequence: 2, ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: claimTestID(1323),
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: repoC}, Input: operation.MatterCreateInput{Title: "Foreign", Locator: "foreign-matter"},
		},
	}
	completeNamedBatchMatterForTest(t, store, foreignMatter, peerB, artifactB, now, 1324, 1325)
	_, crossDomainBatch := join(1326, foreignBatchID, sealedMatterID)
	requireNamedBatchRefusal(t, crossDomainBatch, operation.ProblemBatchCrossDomain)
	_, crossDomainMatter := join(1327, sealedBatchID, foreignMatterID)
	requireNamedBatchRefusal(t, crossDomainMatter, operation.ProblemBatchCrossDomain)
	if err = checkNamedBatchMembershipState(store.db); err != nil {
		t.Fatalf("live named Batch membership fold: %v", err)
	}
	if err = checkSchema(store.db); err != nil {
		t.Fatalf("live authority schema validation: %v", err)
	}

	// The empty Batch and the Batch whose only active member is sealed both
	// survive a close/reopen. Only explicit dismissal closes either identity.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(fixture.f.root)
	if err != nil {
		t.Fatalf("reopen durable named Batch lifetimes: %v", err)
	}
	fixture.f.s = store
	t.Cleanup(func() { _ = store.Close() })
	replayed, err := store.SubmitCommand(ctx, firstJoin, hashCommand(t, firstJoin), fixture.f.peer, now)
	if err != nil || replayed.Owner != nil || replayed.Pending || string(replayed.Receipt) != string(firstJoinStatus.Receipt) ||
		string(replayed.SignedReceipt) != string(firstJoinStatus.SignedReceipt) {
		t.Fatalf("same-ID/hash named Batch join replay changed after reopen: status=%+v err=%v", replayed, err)
	}
	var retainedBatch, retainedActive int
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id IN (?,?)`, domainA, sealedBatchID, emptyBatchID).Scan(&retainedBatch); err != nil || retainedBatch != 2 {
		t.Fatalf("all-sealed/empty Batch identities were not retained after reopen: count=%d err=%v", retainedBatch, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batch_memberships WHERE domain_id=? AND batch_id=? AND matter_id=? AND left_event_id IS NULL`,
		domainA, sealedBatchID, sealedMatterID).Scan(&retainedActive); err != nil || retainedActive != 1 {
		t.Fatalf("all-sealed Batch active exact pair after reopen=%d err=%v; want its sole sealed Matter", retainedActive, err)
	}
	if err = store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	sequence++
	dismiss := func(commandNumber int, batchID string) CommandStatus {
		t.Helper()
		command := namedBatchMembershipCommand(claimTestID(commandNumber), domainA, envA, repoA, sequence,
			operation.BatchDismissV1.Metadata().Operation, operation.BatchDismissInput{BatchID: batchID})
		sequence++
		return completeNamedBatchMembershipForTest(t, store, command, fixture.f.peer, fixture.f.key, now, commandNumber+100)
	}
	dismissed := dismiss(1400, sealedBatchID)
	dismiss(1401, emptyBatchID)
	if receipt, err := readReceipt(dismissed.Receipt); err != nil || receipt.Range == nil || receipt.Range.Count != 1 {
		t.Fatalf("explicit Batch dismissal receipt=%+v err=%v", receipt, err)
	} else {
		var eventRecord []byte
		if err = store.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, receipt.Range.First).Scan(&eventRecord); err != nil {
			t.Fatal(err)
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(eventRecord,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		payload, payloadOK := fields["payload"].(map[string]any)
		if decodeErr != nil || fields["kind"] != "batch.dismissed" || fields["subject_id"] != sealedBatchID || fields["repo_id"] != nil ||
			!payloadOK || len(payload) != 0 {
			t.Fatalf("dismissal event is not Batch-subject/null-Repo: fields=%v err=%v", fields, decodeErr)
		}
	}
	_, dismissedTarget := func() (operation.Command, CommandStatus) {
		command := namedBatchMembershipCommand(claimTestID(1330), domainA, envA, repoA, sequence,
			operation.BatchJoinV1.Metadata().Operation, operation.BatchMembershipInput{BatchID: emptyBatchID, MatterID: secondMatterID})
		sequence++
		return command, completeNamedBatchMembershipForTest(t, store, command, fixture.f.peer, fixture.f.key, now, 1430)
	}()
	requireNamedBatchRefusal(t, dismissedTarget, operation.ProblemBatchDismissed)
	var dismissedRows int
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id IN (?,?) AND dismissed_event_id IS NOT NULL`,
		domainA, sealedBatchID, emptyBatchID).Scan(&dismissedRows); err != nil || dismissedRows != 2 {
		t.Fatalf("explicit dismissal did not retain both terminal Batch identities: rows=%d err=%v", dismissedRows, err)
	}
}
