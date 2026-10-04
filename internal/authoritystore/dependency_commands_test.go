package authoritystore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"reflect"
	"sync"
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

func dependencySuccess(t *testing.T, f *claimTestFixture, command operation.Command, status CommandStatus) string {
	t.Helper()
	receipt, err := readReceipt(status.Receipt)
	if err != nil || receipt.Result.Code != string(operation.ResultSucceeded) || receipt.Result.Problem != nil ||
		receipt.Range == nil || receipt.Range.Count != 1 || receipt.Range.First != receipt.Range.Last {
		t.Fatalf("dependency success receipt = %+v error=%v", receipt, err)
	}
	inputBlocked, inputBlocker := "", ""
	switch input := command.Request.Input.(type) {
	case operation.DependencyAddInput:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	case operation.DependencyRemoveInput:
		inputBlocked, inputBlocker = input.BlockedID, input.BlockerID
	default:
		t.Fatalf("unexpected dependency input %T", command.Request.Input)
	}
	var output dependencyReceiptOutput
	names := []string{"blocked_id", "blocker_id"}
	if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation {
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
	if command.Request.Operation == operation.DependencyRemoveV1.Metadata().Operation {
		wantKind = "dependency.removed"
	}
	if event.kind != wantKind {
		t.Fatalf("dependency event kind=%q, want %q", event.kind, wantKind)
	}
	var eventBlocker, edgeID string
	if canonicalDecode(event.payload["blocker"], &eventBlocker) != nil || canonicalDecode(event.payload["edge"], &edgeID) != nil || eventBlocker != inputBlocker {
		t.Fatalf("dependency event payload = %+v", event.payload)
	}
	if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation {
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
		if command.Request.Operation == operation.DependencyAddV1.Metadata().Operation {
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
}

func addDependency(t *testing.T, f *claimTestFixture, id int, sequence uint64, event int, blocked, blocker, repo string) (operation.Command, CommandStatus, string) {
	t.Helper()
	command := dependencyAuthorityCommand(f, id, sequence, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: blocked, BlockerID: blocker}, repo, "")
	owner := submitDependencyForTest(t, f, command, f.peer)
	status := completeDependencyForTest(t, f, owner, event)
	return command, status, dependencySuccess(t, f, command, status)
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
	for index, edge := range [][2]int{{0, 1}, {0, 2}, {1, 3}, {2, 3}} {
		_, _, edgeID := addDependency(t, f, 14+index, uint64(5+index), 104+index, nodes[edge[0]], nodes[edge[1]], []string{repoC, repoB, repoA, repoC}[index])
		if !ulid.MatchString(edgeID) {
			t.Fatalf("authority assigned invalid edge identity %q", edgeID)
		}
	}
	// The diamond is acyclic in the intended blocked->blocker direction. Adding
	// D->A closes the multi-hop A->B->D path and must refuse without effects.
	before, beforeEdges := f.anchor(t), mustDependencyProjection(t, f)
	cycle := dependencyAuthorityCommand(f, 18, 9, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: fourth, BlockerID: nodes[0]}, repoB, "")
	cycleStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, cycle, f.peer), 108)
	dependencyRefusal(t, f, cycle, cycleStatus, "refusal.dependency-cycle", before, beforeEdges)
	before, beforeEdges = f.anchor(t), mustDependencyProjection(t, f)
	duplicate := dependencyAuthorityCommand(f, 19, 10, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: nodes[0], BlockerID: nodes[1]}, repoA, "")
	duplicateStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, duplicate, f.peer), 109)
	dependencyRefusal(t, f, duplicate, duplicateStatus, "refusal.dependency-exists", before, beforeEdges)
}

func TestDependencyAuthoritySelfEdgeReachesTerminalRefusal(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	command := dependencyAuthorityCommand(f, 13, 4, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: f.matter, BlockerID: f.matter}, repoC, "")
	if err := operation.DependencyAddV1.ValidateRequest(command.Request); err != nil {
		t.Fatalf("self-edge must reach authority refusal: %v", err)
	}
	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 103)
	dependencyRefusal(t, f, command, status, "refusal.dependency-cycle", before, edges)

	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyAuthorityCommand(f, 14, 5, operation.DependencyRemoveV1,
		operation.DependencyRemoveInput{BlockedID: claimTestID(20), BlockerID: claimTestID(21)}, repoB, "")
	status = completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, f.peer), 104)
	dependencyRefusal(t, f, missing, status, "refusal.dependency-missing", before, edges)
}

func TestDependencyAuthorityConcurrentOppositeAddsCommitAtMostOne(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	peerB := dependencyEnvironmentB(t, f)
	left, right := claimTestID(20), claimTestID(21)
	first := dependencyAuthorityCommand(f, 13, 4, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: left, BlockerID: right}, repoC, envA)
	second := dependencyAuthorityCommand(f, 14, 1, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: right, BlockerID: left}, repoB, envB)
	owners := []*Execution{
		submitDependencyForTest(t, f, first, f.peer),
		submitDependencyForTest(t, f, second, peerB),
	}
	commands := []operation.Command{first, second}
	start := make(chan struct{})
	type terminal struct {
		command operation.Command
		status  CommandStatus
		err     error
	}
	results := make(chan terminal, 2)
	var wait sync.WaitGroup
	for index := range owners {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			status, err := f.s.CompleteCommand(context.Background(), owners[index], operation.Result{Code: operation.ResultSucceeded},
				"", claimTestID(103), f.now, signWith(f.key))
			results <- terminal{command: commands[index], status: status, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, refused := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent completion %s: %v", result.command.ID, result.err)
		}
		receipt, err := readReceipt(result.status.Receipt)
		if err != nil {
			t.Fatal(err)
		}
		switch receipt.Result.Code {
		case string(operation.ResultSucceeded):
			succeeded++
			dependencySuccess(t, f, result.command, result.status)
		case string(operation.ResultRefused):
			refused++
			if receipt.Result.Problem == nil || *receipt.Result.Problem != "refusal.dependency-cycle" || receipt.Range != nil || receipt.Result.Output != nil {
				t.Fatalf("opposite-edge loser receipt = %+v", receipt.Result)
			}
		default:
			t.Fatalf("unexpected concurrent result: %+v", receipt.Result)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("opposite-edge commits = succeeded %d/refused %d, want exactly 1/1", succeeded, refused)
	}
	var events int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM authority_events WHERE domain_id=? AND event_id=?`, domainA, claimTestID(103)).Scan(&events); err != nil || events != 1 {
		t.Fatalf("concurrent opposite-edge event count=%d error=%v", events, err)
	}
}

func TestDependencyAuthorityForeignDomainRefusesWithoutDisclosure(t *testing.T) {
	f := newDependencyCrossRepoHistoryFixture(t)
	defer func() { _ = f.s.Close() }()
	foreign := createForeignDependencyMatter(t)
	before, edges := f.anchor(t), mustDependencyProjection(t, f)
	command := dependencyAuthorityCommand(f, 13, 4, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: f.matter, BlockerID: foreign}, repoA, "")
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, command, f.peer), 103)
	dependencyRefusal(t, f, command, status, "refusal.dependency-endpoint", before, edges)
	receipt, _ := readReceipt(status.Receipt)
	if bytes.Contains(receipt.Result.Output, []byte(foreign)) || bytes.Contains([]byte(*receipt.Result.Problem), []byte(foreign)) {
		t.Fatalf("foreign endpoint was disclosed: %+v", receipt.Result)
	}
	// A non-existent same-domain ID receives the same opaque endpoint refusal.
	before, edges = f.anchor(t), mustDependencyProjection(t, f)
	missing := dependencyAuthorityCommand(f, 14, 5, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: f.matter, BlockerID: claimTestID(999)}, repoA, "")
	missingStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, missing, f.peer), 104)
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
	blocked, blocker := claimTestID(20), claimTestID(21)
	add, addStatus, firstEdge := addDependency(t, f, 13, 4, 103, blocked, blocker, repoC)
	remove := dependencyAuthorityCommand(f, 14, 5, operation.DependencyRemoveV1,
		operation.DependencyRemoveInput{BlockedID: blocked, BlockerID: blocker}, repoB, "")
	removeStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, remove, f.peer), 104)
	if removedEdge := dependencySuccess(t, f, remove, removeStatus); removedEdge != firstEdge {
		t.Fatalf("remove event did not tombstone exact edge %s: got %s", firstEdge, removedEdge)
	}
	_, _, secondEdge := addDependency(t, f, 15, 6, 105, blocked, blocker, repoA)
	if secondEdge == firstEdge {
		t.Fatalf("re-add reused tombstoned edge identity %s", firstEdge)
	}

	replayed, err := f.s.SubmitCommand(context.Background(), remove, hashCommand(t, remove), f.peer, f.now)
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
			hashCommand(t, original.command), 7, f.peer, envA, f.now)
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
	// A Step and its parent Matter share one Matter, but the dependency remains
	// authority-delivered and is not written into that Matter's claim journal.
	_, _, edgeID := addDependency(t, f, 12, 3, 102, step, f.matter, repoA)
	anchor := f.anchor(t)
	allocation := claimTestAllocation(1, anchor, 103, 104, 105)
	f.acquire(t, 11, 4, anchor, allocation)
	removeStep := step12Command(f, 13, 5, operation.StepRemoveV1,
		operation.StepRemoveInput{StepID: step, Reason: "obsolete"}, allocation.ClaimID)
	completeStep12ClaimCommand(t, f, removeStep, allocation.JournalID, 1, 131, 106)
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
	addDead := dependencyAuthorityCommand(f, 14, 6, operation.DependencyAddV1,
		operation.DependencyAddInput{BlockedID: step, BlockerID: f.matter}, repoA, "")
	status := completeDependencyForTest(t, f, submitDependencyForTest(t, f, addDead, f.peer), 107)
	dependencyRefusal(t, f, addDead, status, "refusal.dependency-endpoint", before, projection)
	if err := operation.DependencyAddV1.ValidateResult(operation.Result{
		Code:   operation.ResultSucceeded,
		Output: operation.DependencyOutput{EdgeID: edgeID, BlockedID: step, BlockerID: f.matter},
	}); err != nil {
		t.Fatalf("dependency add output contract: %v", err)
	}
	before = f.anchor(t)
	removeDead := dependencyAuthorityCommand(f, 15, 7, operation.DependencyRemoveV1,
		operation.DependencyRemoveInput{BlockedID: step, BlockerID: f.matter}, repoA, "")
	removeDeadStatus := completeDependencyForTest(t, f, submitDependencyForTest(t, f, removeDead, f.peer), 108)
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
		name, operation, competitor, refusal string
	}{
		{name: "add-commits", operation: "add"},
		{name: "add-refuses-cycle-after-restart", operation: "add", competitor: "opposite-add", refusal: "refusal.dependency-cycle"},
		{name: "remove-commits", operation: "remove"},
		{name: "remove-refuses-missing-after-restart", operation: "remove", competitor: "same-pair-remove", refusal: "refusal.dependency-missing"},
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
			var peerB tls.ConnectionState
			if scenario.competitor != "" {
				peerB = dependencyEnvironmentB(t, f)
			}
			if scenario.operation == "remove" {
				addDependency(t, f, 13, 4, 103, blocked, blocker, repoC)
			}
			id, sequence, repo := 13, uint64(4), repoC
			definition := operation.DependencyAddV1
			var input operation.Input = operation.DependencyAddInput{BlockedID: blocked, BlockerID: blocker}
			if scenario.operation == "remove" {
				id, sequence, repo = 14, 5, repoB
				definition = operation.DependencyRemoveV1
				input = operation.DependencyRemoveInput{BlockedID: blocked, BlockerID: blocker}
			}
			command := dependencyAuthorityCommand(f, id, sequence, definition, input, repo, "")
			hash := hashCommand(t, command)
			pending, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
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
			if scenario.competitor == "opposite-add" {
				competitor := dependencyAuthorityCommand(f, 14, 1, operation.DependencyAddV1,
					operation.DependencyAddInput{BlockedID: blocker, BlockerID: blocked}, repoB, envB)
				owner := submitDependencyForTest(t, f, competitor, peerB)
				competitorStatus := completeDependencyForTest(t, f, owner, 103)
				dependencySuccess(t, f, competitor, competitorStatus)
			}
			if scenario.competitor == "same-pair-remove" {
				competitor := dependencyAuthorityCommand(f, 15, 1, operation.DependencyRemoveV1,
					operation.DependencyRemoveInput{BlockedID: blocked, BlockerID: blocker}, repoA, envB)
				owner := submitDependencyForTest(t, f, competitor, peerB)
				competitorStatus := completeDependencyForTest(t, f, owner, 104)
				dependencySuccess(t, f, competitor, competitorStatus)
			}
			before, beforeEdges := f.anchor(t), mustDependencyProjection(t, f)
			recovered, err := f.s.RecoverCommand(ctx, command, hash)
			if err != nil {
				t.Fatalf("recover exact pending command: %v", err)
			}
			event := 103
			if scenario.operation == "remove" {
				event = 104
			}
			if scenario.competitor == "same-pair-remove" {
				event = 105
			}
			terminal, err := f.s.CompleteCommand(ctx, recovered, operation.Result{Code: operation.ResultSucceeded},
				"", claimTestID(event), f.now, signWith(f.key))
			if err != nil || terminal.Pending || len(terminal.Receipt) == 0 {
				t.Fatalf("complete recovered dependency command: %+v %v", terminal, err)
			}
			if scenario.refusal == "" {
				dependencySuccess(t, f, command, terminal)
			} else {
				dependencyRefusal(t, f, command, terminal, scenario.refusal, before, beforeEdges)
			}
			replay, err := f.s.SubmitCommand(ctx, command, hash, f.peer, f.now)
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
			queried, err := f.s.QueryCommand(ctx, domainA, command.ID, hash, 7, f.peer, envA, f.now)
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
