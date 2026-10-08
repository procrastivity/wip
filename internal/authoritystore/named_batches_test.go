package authoritystore

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func namedBatchCommand(id, domain, environment, repo string, sequence uint64, name string) operation.Command {
	return operation.Command{
		ID: id, AuthorityDomainID: domain, ExpectedAuthorityEpoch: 7,
		EnvironmentID: environment, EnvironmentSequence: sequence,
		ActedAt: "2026-09-23T11:59:00Z", CorrelationCommandID: id,
		Request: operation.Request{
			Operation: operation.BatchCreateV1.Metadata().Operation,
			Actor:     "human", Context: operation.Context{Repo: repo},
			Input: operation.BatchCreateInput{Name: name},
		},
	}
}

func enrollNamedBatchEnvironment(t *testing.T, store *Store, domainID, environmentID string, domain Domain, owner ed25519.PrivateKey, now time.Time, suffix int) tls.ConnectionState {
	t.Helper()
	ctx := context.Background()
	caKey := key("step4-ca")
	if domainID != domainA {
		caKey = key("named-batch-domain-b-ca")
	}
	caDER := caFixture(t, caKey, now)
	if domainID != domainA {
		caID, _ := spkiID(caKey.Public())
		delegation := signedTest(t, owner, "environment-ca-delegation", "wipd.environment-ca-delegation/1", domainID, domain.OwnerKeyID, 7, map[string]any{
			"schema": "wipd.environment-ca-delegation/1", "domain_id": domainID, "authority_epoch": uint64(7),
			"owner_key_id": domain.OwnerKeyID, "ca_generation": uint64(1), "ca_key_id": caID,
			"ca_certificate_der": caDER, "not_before": now.Add(-time.Hour).Format(time.RFC3339Nano),
			"not_after": now.Add(7 * 24 * time.Hour).Format(time.RFC3339Nano),
		})
		if err := store.InstallEnvironmentCA(ctx, domainID, delegation, now); err != nil {
			t.Fatalf("install named-Batch environment CA: %v", err)
		}
	}
	leafKey := key("named-batch-environment-" + environmentID)
	leaf := leafFixture(t, leafKey, caKey, caDER, domainID, environmentID, domain.OwnerKeyID, 7, now, int64(500+suffix))
	grant := grantFixture(t, owner, domain, "environment-enroll", claimTestID(800+suffix), environmentID, leafKey, byte(20+suffix))
	if _, err := store.IssueEnvironmentCertificate(ctx, domainID, environmentID, grant,
		csrFixture(t, leafKey, "Named Batch Environment"), [][]byte{leaf, caDER}, now); err != nil {
		t.Fatalf("enroll named-Batch Environment %s: %v", environmentID, err)
	}
	return tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{mustCert(t, leaf), mustCert(t, caDER)}}
}

func completeNamedBatchForTest(t *testing.T, store *Store, command operation.Command, peer tls.ConnectionState,
	artifactKey ed25519.PrivateKey, now time.Time, subject, event string,
) CommandStatus {
	t.Helper()
	hash := hashCommand(t, command)
	status, err := store.SubmitCommand(context.Background(), command, hash, peer, now)
	if err != nil || status.Owner == nil || !status.Pending {
		t.Fatalf("submit named Batch %q: status=%+v error=%v", command.Request.Input.(operation.BatchCreateInput).Name, status, err)
	}
	result := operation.Result{Code: operation.ResultSucceeded, Output: operation.BatchCreateOutput{
		ID: subject, Name: command.Request.Input.(operation.BatchCreateInput).Name,
	}}
	status, err = store.CompleteCommand(context.Background(), status.Owner, result, subject, event, now, signWith(artifactKey))
	if err != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("complete named Batch %q: status=%+v error=%v", command.Request.Input.(operation.BatchCreateInput).Name, status, err)
	}
	return status
}

func TestNamedBatchDomainUniquenessNullRepoAndReplayAfterReopen(t *testing.T) {
	store, root, peerA, artifactA, now := commandFixture(t)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	ctx := context.Background()
	if err := store.AttachRepo(ctx, domainA, repoB); err != nil {
		t.Fatal(err)
	}
	domainAInfo, ownerA := identity(domainA, 7)
	peerB := enrollNamedBatchEnvironment(t, store, domainA, envB, domainAInfo, ownerA, now, 1)

	name := "shared release"
	first := namedBatchCommand(claimTestID(701), domainA, envA, repoA, 1, name)
	firstHash := hashCommand(t, first)
	firstReceipt := completeNamedBatchForTest(t, store, first, peerA, artifactA, now, claimTestID(702), claimTestID(703))
	anchorBeforeReplay, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || anchorBeforeReplay.EventCount != 1 {
		t.Fatalf("first named-Batch event prefix: %+v %v", anchorBeforeReplay, err)
	}
	var eventRecord []byte
	if err = store.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, claimTestID(703)).Scan(&eventRecord); err != nil {
		t.Fatal(err)
	}
	eventFields, err := wipdwire.DecodeCanonicalMap(eventRecord,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	if err != nil || eventFields["kind"] != "batch.created" || eventFields["repo_id"] != nil || eventFields["subject_id"] != claimTestID(702) {
		t.Fatalf("named-Batch event must have typed identity and null Repo: fields=%v error=%v", eventFields, err)
	}
	if stats := store.db.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("named-Batch fold test requires the authority's single-connection boundary: %+v", stats)
	}
	waitCountBeforeFold := store.db.Stats().WaitCount
	foldCtx, cancelFold := context.WithTimeout(ctx, 5*time.Second)
	folded, foldErr := deriveNamedBatches(foldCtx, store.db)
	cancelFold()
	if foldErr != nil || len(folded) != 1 || folded[0].domain != domainA || folded[0].id != claimTestID(702) ||
		folded[0].name != name || folded[0].birth != claimTestID(703) {
		t.Fatalf("named-Batch event/submission folds did not complete with distinct command and subject IDs: %+v %v", folded, foldErr)
	}
	if waitCount := store.db.Stats().WaitCount; waitCount != waitCountBeforeFold {
		t.Fatalf("named-Batch fold waited for a connection while processing an open cursor: before=%d after=%d", waitCountBeforeFold, waitCount)
	}

	// Treat the successful response as lost, reopen the durable authority, then
	// replay the identical request and verify the exact immutable receipt.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen after lost named-Batch response: %v", err)
	}
	replayed, err := store.SubmitCommand(ctx, first, firstHash, peerA, now)
	if err != nil || replayed.Owner != nil || replayed.Pending || string(replayed.Receipt) != string(firstReceipt.Receipt) ||
		string(replayed.SignedReceipt) != string(firstReceipt.SignedReceipt) {
		t.Fatalf("exact named-Batch replay differs from retained receipt: %+v %v", replayed, err)
	}
	changed := namedBatchCommand(first.ID, domainA, envA, repoA, 1, "changed release")
	if _, err = store.SubmitCommand(ctx, changed, hashCommand(t, changed), peerA, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-hash command ID was not refused: %v", err)
	}

	beforeDuplicate, err := store.CurrentPrefixAnchor(ctx, domainA)
	if err != nil || beforeDuplicate != anchorBeforeReplay {
		t.Fatalf("replay or changed-hash conflict changed authority prefix: before=%+v after=%+v error=%v", anchorBeforeReplay, beforeDuplicate, err)
	}
	var projectionBefore int
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=?`, domainA).Scan(&projectionBefore); err != nil || projectionBefore != 1 {
		t.Fatalf("named-Batch projection after replay: count=%d error=%v", projectionBefore, err)
	}

	// A different Repo in the same domain cannot reuse a live name; the
	// otherwise-valid second Environment and sequence isolate that guard.
	duplicate := namedBatchCommand(claimTestID(704), domainA, envB, repoB, 1, name)
	duplicateHash := hashCommand(t, duplicate)
	status, err := store.SubmitCommand(ctx, duplicate, duplicateHash, peerB, now)
	if err != nil || status.Owner == nil {
		t.Fatalf("admit cross-Repo duplicate candidate: %+v %v", status, err)
	}
	status, err = store.CompleteCommand(ctx, status.Owner,
		operation.Result{Code: operation.ResultSucceeded, Output: operation.BatchCreateOutput{ID: claimTestID(705), Name: name}},
		claimTestID(705), claimTestID(706), now, signWith(artifactA))
	if err != nil || status.Pending {
		t.Fatalf("complete cross-Repo duplicate candidate: %+v %v", status, err)
	}
	duplicateReceipt, err := readReceipt(status.Receipt)
	if err != nil || duplicateReceipt.Result.Code != string(operation.ResultRefused) || duplicateReceipt.Result.Problem == nil ||
		*duplicateReceipt.Result.Problem != "refusal.batch-name-exists" || duplicateReceipt.Range != nil || duplicateReceipt.Result.Output != nil {
		t.Fatalf("cross-Repo duplicate was not a terminal no-effect refusal: %+v %v", duplicateReceipt, err)
	}
	var duplicateEvents, projections, receipts int
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=?`, domainA).Scan(&duplicateEvents); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=?`, domainA).Scan(&projections); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, first.ID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if duplicateEvents != 1 || projections != 1 || receipts != 1 {
		t.Fatalf("duplicate changed prior domain state: events=%d projections=%d originalReceipts=%d", duplicateEvents, projections, receipts)
	}

	// The same name is legal in another domain within this very same store.
	domainB, ownerB := identity(domainB, 7)
	if err = store.BootstrapDomain(ctx, domainB, repoC); err != nil {
		t.Fatalf("bootstrap distinct named-Batch domain: %v", err)
	}
	artifactB := key("named-batch-domain-b-artifact")
	keyID, _ := spkiID(artifactB.Public())
	artifactCertificate := signedTest(t, ownerB, "authority-artifact-key", "wipd.authority-artifact-key/1", domainB.ID, domainB.OwnerKeyID, 7, map[string]any{
		"schema": "wipd.authority-artifact-key/1", "domain_id": domainB.ID, "authority_epoch": uint64(7),
		"key_generation": uint64(1), "key_id": keyID, "ed25519_public_key": []byte(artifactB.Public().(ed25519.PublicKey)),
		"not_before": now.Add(-time.Hour).Format(time.RFC3339Nano), "not_after": now.Add(time.Hour).Format(time.RFC3339Nano),
	})
	if err = store.RegisterArtifactKey(ctx, domainB.ID, artifactCertificate, now); err != nil {
		t.Fatal(err)
	}
	peerDomainB := enrollNamedBatchEnvironment(t, store, domainB.ID, envB, domainB, ownerB, now, 2)
	otherDomain := namedBatchCommand(claimTestID(707), domainB.ID, envB, repoC, 1, name)
	completeNamedBatchForTest(t, store, otherDomain, peerDomainB, artifactB, now, claimTestID(708), claimTestID(709))
	for _, item := range []struct {
		domain string
		want   int
	}{{domainA, 1}, {domainB.ID, 1}} {
		var count int
		if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND name=?`, item.domain, name).Scan(&count); err != nil || count != item.want {
			t.Fatalf("domain-scoped name %q in %s: count=%d error=%v", name, item.domain, count, err)
		}
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("final named-Batch authority reopen: %v", err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches`).Scan(&projections); err != nil || projections != 2 {
		t.Fatalf("reopened domain-scoped projection: count=%d error=%v", projections, err)
	}
}

func TestNamedBatchPendingAdmissionReopenRecoverAndComplete(t *testing.T) {
	store, root, peer, artifact, now := commandFixture(t)
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	ctx := context.Background()
	command := namedBatchCommand(claimTestID(951), domainA, envA, repoA, 1, "admitted before crash")
	hash := hashCommand(t, command)
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.SubmitCommand(ctx, command, hash, peer, now)
	if err != nil || !status.Pending || status.Owner == nil || len(status.Receipt) != 0 || len(status.SignedReceipt) != 0 {
		t.Fatalf("valid pending named-Batch admission: status=%+v error=%v", status, err)
	}
	assertPendingState := func(label string) {
		t.Helper()
		var submissions, events, receipts, projections, head int
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=? AND request_hash=? AND command=? AND state='submitted'`,
			domainA, command.ID, hash, canonical).Scan(&submissions); queryErr != nil {
			t.Fatalf("%s pending submission query: %v", label, queryErr)
		}
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&events); queryErr != nil {
			t.Fatalf("%s pending event query: %v", label, queryErr)
		}
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&receipts); queryErr != nil {
			t.Fatalf("%s pending receipt query: %v", label, queryErr)
		}
		if queryErr := store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id=?`, domainA, claimTestID(952)).Scan(&projections); queryErr != nil {
			t.Fatalf("%s pending projection query: %v", label, queryErr)
		}
		if queryErr := store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&head); queryErr != nil {
			t.Fatalf("%s pending sequence query: %v", label, queryErr)
		}
		if submissions != 1 || events != 0 || receipts != 0 || projections != 0 || head != 0 {
			t.Fatalf("%s pending durable state: submissions=%d events=%d receipts=%d projections=%d sequence_head=%d",
				label, submissions, events, receipts, projections, head)
		}
	}
	assertPendingState("after admission")
	if err = checkNamedBatchSubmissions(ctx, store.db, map[string]bool{ownerKey(domainA, command.ID): true}); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("pending named-Batch submission paired with a birth event was accepted: %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen after pending named-Batch admission: %v", err)
	}
	assertPendingState("after reopen")

	replayed, err := store.SubmitCommand(ctx, command, hash, peer, now)
	if err != nil || !replayed.Pending || replayed.Owner != nil || len(replayed.Receipt) != 0 || len(replayed.SignedReceipt) != 0 {
		t.Fatalf("exact pending retry did not coalesce without an owner: status=%+v error=%v", replayed, err)
	}
	assertPendingState("after coalesced retry")
	changed := namedBatchCommand(command.ID, domainA, envA, repoA, 1, "changed after admission")
	if _, err = store.SubmitCommand(ctx, changed, hashCommand(t, changed), peer, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-hash pending retry was not refused: %v", err)
	}
	if _, err = store.RecoverCommand(ctx, changed, hashCommand(t, changed)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-hash pending recovery was not refused: %v", err)
	}
	assertPendingState("after changed-hash refusals")

	owner, err := store.RecoverCommand(ctx, command, hash)
	if err != nil || owner == nil {
		t.Fatalf("recover exact pending named-Batch command: owner=%v error=%v", owner, err)
	}
	status, err = store.CompleteCommand(ctx, owner,
		operation.Result{Code: operation.ResultSucceeded, Output: operation.BatchCreateOutput{ID: claimTestID(952), Name: "admitted before crash"}},
		claimTestID(952), claimTestID(953), now, signWith(artifact))
	if err != nil || status.Pending || status.Owner != nil || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
		t.Fatalf("complete recovered named-Batch command: status=%+v error=%v", status, err)
	}
	wantOutput, err := artifactEncoder.Marshal(map[string]any{"id": claimTestID(952), "name": "admitted before crash"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Domain != domainA || receipt.ID != command.ID || receipt.Hash != hash ||
		receipt.Operation.Name != operation.BatchCreateV1.Metadata().Operation.Name || receipt.Operation.Version != 1 ||
		receipt.Environment.ID != envA || receipt.Environment.Sequence != 1 ||
		receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil || string(receipt.Result.Output) != string(wantOutput) ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != claimTestID(953) || receipt.Range.Last != claimTestID(953) {
		t.Fatalf("recovered named-Batch terminal receipt: receipt=%+v error=%v", receipt, err)
	}
	var eventCount, projectionCount, receiptCount, terminalCount, head int
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id=? AND name=? AND birth_event_id=?`,
		domainA, claimTestID(952), "admitted before crash", claimTestID(953)).Scan(&projectionCount); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=? AND state='terminal'`, domainA, command.ID).Scan(&terminalCount); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, domainA, envA).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || projectionCount != 1 || receiptCount != 1 || terminalCount != 1 || head != 1 {
		t.Fatalf("recovered named-Batch terminal state: events=%d projections=%d receipts=%d terminal_submissions=%d sequence_head=%d",
			eventCount, projectionCount, receiptCount, terminalCount, head)
	}
	var eventRecord []byte
	if err = store.db.QueryRow(`SELECT record FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&eventRecord); err != nil {
		t.Fatal(err)
	}
	event, err := parseStep12Event(eventRecord, domainA, 1, claimTestID(953), command.ID)
	var eventName string
	if err != nil || event.hash != hash || event.environment != envA || event.sequence != 1 || event.kind != "batch.created" ||
		event.subject != claimTestID(952) || event.repo != "" || event.acted != command.ActedAt ||
		artifactDecoder.Unmarshal(event.payload["name"], &eventName) != nil || eventName != "admitted before crash" {
		t.Fatalf("recovered named-Batch event: event=%+v error=%v", event, err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenExisting(root)
	if err != nil {
		t.Fatalf("reopen after recovered named-Batch completion: %v", err)
	}
	var reopenedEvents, reopenedProjections, reopenedReceipts int
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&reopenedEvents); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id=? AND name=? AND birth_event_id=?`,
		domainA, claimTestID(952), "admitted before crash", claimTestID(953)).Scan(&reopenedProjections); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&reopenedReceipts); err != nil {
		t.Fatal(err)
	}
	if reopenedEvents != 1 || reopenedProjections != 1 || reopenedReceipts != 1 {
		t.Fatalf("reopened named-Batch terminal state: events=%d projections=%d receipts=%d", reopenedEvents, reopenedProjections, reopenedReceipts)
	}
	terminalReplay, err := store.SubmitCommand(ctx, command, hash, peer, now)
	if err != nil || terminalReplay.Pending || terminalReplay.Owner != nil ||
		string(terminalReplay.Receipt) != string(status.Receipt) || string(terminalReplay.SignedReceipt) != string(status.SignedReceipt) {
		t.Fatalf("exact post-reopen named-Batch replay changed terminal receipt: status=%+v error=%v", terminalReplay, err)
	}
	if _, err = store.RecoverCommand(ctx, command, hash); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("terminal named-Batch command was recoverable: %v", err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&reopenedEvents); err != nil || reopenedEvents != 1 {
		t.Fatalf("post-reopen replay changed named-Batch event count: count=%d error=%v", reopenedEvents, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM m6_named_batches WHERE domain_id=? AND batch_id=? AND name=? AND birth_event_id=?`,
		domainA, claimTestID(952), "admitted before crash", claimTestID(953)).Scan(&reopenedProjections); err != nil || reopenedProjections != 1 {
		t.Fatalf("post-reopen replay changed named-Batch projection count: count=%d error=%v", reopenedProjections, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM terminal_receipts WHERE domain_id=? AND command_id=?`, domainA, command.ID).Scan(&reopenedReceipts); err != nil || reopenedReceipts != 1 {
		t.Fatalf("post-reopen replay changed named-Batch receipt count: count=%d error=%v", reopenedReceipts, err)
	}
}
