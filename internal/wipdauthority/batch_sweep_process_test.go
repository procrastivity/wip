package wipdauthority

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestBatchSweepThroughAuthenticatedWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated local wipd IPC is Linux-only")
	}
	for _, scenario := range []string{"lost-terminal", "lost-local-response", "missing-install", "not-eligible", "target-missing", "local-step5", "local-m5", "authority-step5"} {
		t.Run(scenario, func(t *testing.T) { runBatchSweepProcess(t, scenario) })
	}
}

func runBatchSweepProcess(t *testing.T, scenario string) {
	ctx := context.Background()
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step7Registry()
	if scenario == "authority-step5" {
		registry, err = NewM6Step5Registry()
	}
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	fixture.server, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	var drop m5OrdinaryCommandResponseDrop
	var faults m5ConnectionFaults
	var loseReleaseInstall atomic.Bool
	var trace m6AuthorityHTTPTrace
	handler := discardOneOrdinaryCommandResponse(fixture.server.http.Handler, &drop)
	handler = injectM5ConnectionFaults(handler, &faults)
	handler = blockSweepProcessReleaseInstall(handler, &loseReleaseInstall, &faults)
	var holdTerminal atomic.Bool
	committed, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	next := handler
	handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		next.ServeHTTP(sweepProcessTerminalHoldWriter{writer, &holdTerminal, committed, resume}, request)
	})
	fixture.server.http.Handler = traceM6AuthorityHTTP(handler, &trace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case err := <-served:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("authority did not stop")
		}
	})
	root, err := os.MkdirTemp("/tmp", "sweep-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	catalogue := []string{"m6-step7"}
	switch scenario {
	case "local-step5":
		catalogue = []string{"m6-step5"}
	case "local-m5":
		catalogue = nil
	}
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "env", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], catalogue...)
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	if scenario == "authority-step5" {
		processCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		output, err := exec.CommandContext(processCtx, binary, "--profile-root", environment.profileRoot).CombinedOutput()
		if err == nil || processCtx.Err() != nil || !strings.Contains(string(output), "connected authority startup refused") {
			t.Fatalf("Step 7 profile did not fail closed against Step 5 authority: %v %s", err, output)
		}
		anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || anchor.EventCount != 0 || faults.commandSubmits.Load() != 0 {
			t.Fatalf("unsupported authority changed history: %+v %v", anchor, err)
		}
		return
	}
	client, stopDaemon, output := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	defer func() { _ = client.Close(); stopDaemon() }()
	restart := func() {
		_ = client.Close()
		stopDaemon()
		client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	}
	openJournal := func() *wipdjournal.Journal {
		_ = client.Close()
		stopDaemon()
		journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
			RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv,
			OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return journal
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	assertNotAdmitted := func(id string) {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, m5TestDomain, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unsupported/uninstalled sweep reached authority: count=%d err=%v", count, err)
		}
	}
	assertProblem := func(err error, code string) {
		var exchange *wipd.ExchangeError
		if !errors.As(err, &exchange) || exchange.Code != code {
			t.Fatalf("IPC problem=%v; want %s; daemon=%s", err, code, output.String())
		}
	}
	if scenario == "local-step5" || scenario == "local-m5" {
		id := repairTransportID(70)
		_, err := client.SweepAnonymousBatch(ctx, id, repairTransportID(60), repairTransportID(65), repairTransportID(67), "human")
		assertProblem(err, "protocol.unsupported-extension")
		assertNotAdmitted(id)
		journal := openJournal()
		defer func() { _ = journal.Close() }()
		if _, err := journal.Get(id); !errors.Is(err, wipdjournal.ErrNotFound) {
			t.Fatalf("unsupported capability prepared a durable sweep: %v", err)
		}
		return
	}
	command := func(id int, sequence uint64, request operation.Request) operation.Command {
		commandID := repairTransportID(id)
		return operation.Command{
			ID: commandID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, EnvironmentSequence: sequence, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationCommandID: commandID, Request: request,
		}
	}
	created, err := client.ExecuteCommand(ctx, command(60, 1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "Sweep recovery", Locator: "sweep-recovery"},
	}))
	matter, ok := created.Output.(operation.MatterCreateOutput)
	if err != nil || created.Code != operation.ResultSucceeded || !ok {
		t.Fatalf("create: %+v %v daemon=%s", created, err, output.String())
	}
	if _, err := client.ReleaseBirthClaim(ctx, matter.ID, repairTransportID(61), "human"); err != nil {
		t.Fatal(err)
	}
	clone, worktree := repairTransportID(63), repairTransportID(64)
	acquired, err := client.AcquireClaim(ctx, repairTransportID(62), matter.ID, clone, worktree, repairTransportID(65), "human")
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
		t.Fatalf("acquire: %+v %v", acquired, err)
	}
	grant := acquired.Grant
	if scenario != "not-eligible" {
		for index, definition := range []operation.Definition{operation.MatterStartV1, operation.MatterFinishV1} {
			var input operation.Input = operation.NodeLifecycleInput{NodeID: matter.ID}
			if index == 1 {
				input = operation.MatterFinishInput{MatterID: matter.ID}
			}
			got, err := client.ExecuteCommand(ctx, command(66+index, uint64(4+index), operation.Request{
				Operation: definition.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo, Clone: clone, Worktree: worktree},
				Claim: &operation.ClaimContext{ID: grant.ClaimID, Epoch: fmt.Sprint(grant.ClaimEpoch)}, Input: input,
			}))
			if err != nil || got.Code != operation.ResultSucceeded {
				t.Fatalf("%s: %+v %v", definition.Metadata().Operation, got, err)
			}
		}
	}
	closeID, sweepID := repairTransportID(68), repairTransportID(70)
	if scenario == "missing-install" {
		loseReleaseInstall.Store(true)
	}
	closed, err := client.ReleaseClaimJournal(ctx, closeID, grant.ClaimID, grant.ClaimEpoch, matter.ID, grant.DispatchID, "human")
	if scenario == "missing-install" {
		if err == nil {
			t.Fatal("release with unavailable install tail succeeded")
		}
		_, err = client.SweepAnonymousBatch(ctx, sweepID, matter.ID, grant.BatchID, closeID, "human")
		assertProblem(err, "command.sequence-blocked")
		assertNotAdmitted(sweepID)
		journal := openJournal()
		attempt, attemptErr := journal.ClaimJournalReleaseAttempt(closeID)
		if attemptErr != nil || attempt.Returned {
			t.Fatalf("uninstalled release was marked returned: %+v %v", attempt, attemptErr)
		}
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, closeID, attempt.RequestHash, 1, fixture.peer, m5TestEnv, time.Now())
		if statusErr != nil || len(status.SignedReceipt) == 0 {
			t.Fatalf("fault did not occur after authoritative close: %+v %v", status, statusErr)
		}
		if _, err := journal.ReleaseInstalledClaimCloseByID(ctx, closeID); !errors.Is(err, wipdjournal.ErrNotFound) {
			t.Fatalf("uninstalled normal release became sweep evidence: %v", err)
		}
		if _, err := journal.Get(sweepID); !errors.Is(err, wipdjournal.ErrNotFound) {
			t.Fatalf("uninstalled release prepared sweep attempt: %v", err)
		}
		_ = journal.Close()
		faults.blockPull.Store(false)
		restart()
		closed, err = client.ReleaseClaimJournal(ctx, closeID, grant.ClaimID, grant.ClaimEpoch, matter.ID, grant.DispatchID, "human")
	}
	if err != nil || closed.Code != operation.ResultSucceeded {
		t.Fatalf("normal release install: %+v %v daemon=%s", closed, err, output.String())
	}
	_, err = client.SweepAnonymousBatch(ctx, repairTransportID(69), matter.ID, grant.BatchID, repairTransportID(99), "human")
	assertProblem(err, "batch.sweep-claim-close-unavailable")
	assertNotAdmitted(repairTransportID(69))
	before, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	sweepMatter := matter.ID
	if scenario == "target-missing" {
		sweepMatter = repairTransportID(98)
	}
	if scenario == "lost-terminal" {
		drop.armed.Store(true)
		faults.blockReceiptQuery.Store(true)
	}
	var result wipd.BatchSweepAnonymousResponse
	if scenario == "lost-local-response" {
		// Cancel only the local wait after authority commit, before wipd can
		// deliver a response. The durable attempt must still install its result.
		holdTerminal.Store(true)
		waitCtx, cancelWait := context.WithCancel(ctx)
		defer cancelWait()
		finished := make(chan error, 1)
		go func() {
			_, err := client.SweepAnonymousBatch(waitCtx, sweepID, sweepMatter, grant.BatchID, closeID, "human")
			finished <- err
		}()
		select {
		case <-committed:
		case <-time.After(10 * time.Second):
			t.Fatal("sweep did not commit before local response loss")
		}
		cancelWait()
		select {
		case err := <-finished:
			var exchange *wipd.ExchangeError
			if !errors.As(err, &exchange) || !exchange.Uncertain {
				t.Fatalf("post-admission local loss was not uncertain: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("local cancelled wait did not end")
		}
		close(resume)
		localDB, err := sql.Open("sqlite", "file:"+filepath.Join(environment.profileRoot, "environment-journal", "command-journal.sqlite")+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = localDB.Close() }()
		localDB.SetMaxOpenConns(1)
		var installedReceipt []byte
		deadline := time.Now().Add(10 * time.Second)
		for {
			err = localDB.QueryRow(`SELECT canonical_receipt FROM authority_command_outcomes WHERE command_id=?`, sweepID).Scan(&installedReceipt)
			if err == nil {
				break
			}
			if !errors.Is(err, sql.ErrNoRows) || time.Now().After(deadline) {
				t.Fatalf("local response loss prevented durable installation: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		restart()
		result, err = client.SweepAnonymousBatch(ctx, sweepID, sweepMatter, grant.BatchID, closeID, "human")
		if err != nil || !bytes.Equal(result.Receipt, installedReceipt) {
			t.Fatalf("restart after local response loss changed receipt: %+v %v", result, err)
		}
	} else {
		result, err = client.SweepAnonymousBatch(ctx, sweepID, sweepMatter, grant.BatchID, closeID, "human")
	}
	if scenario == "lost-terminal" {
		if err == nil || !drop.dropped.Load() {
			t.Fatalf("terminal was not lost: %v dropped=%t", err, drop.dropped.Load())
		}
		journal := openJournal()
		entry, entryErr := journal.Get(sweepID)
		installed, installErr := journal.InstallSnapshot(ctx)
		if entryErr != nil || installErr != nil || entry.State != wipdjournal.StateAttemptPrepared || installed.Anchor.EventCount != before.EventCount {
			t.Fatalf("lost-response durable attempt/prefix: %+v %+v %v %v", entry, installed, entryErr, installErr)
		}
		if _, found := installed.Receipts[sweepID]; found {
			t.Fatal("lost terminal was installed before recovery")
		}
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, sweepID, entry.RequestHash, 1, fixture.peer, m5TestEnv, time.Now())
		if statusErr != nil || len(status.SignedReceipt) == 0 {
			t.Fatalf("lost terminal not committed: %+v %v", status, statusErr)
		}
		_ = journal.Close()
		faults.blockReceiptQuery.Store(false)
		restart()
		result, err = client.SweepAnonymousBatch(ctx, sweepID, sweepMatter, grant.BatchID, closeID, "human")
		if err != nil || !bytes.Equal(result.Receipt, status.Receipt) || result.RequestHash != entry.RequestHash {
			t.Fatalf("restart changed original terminal: %+v %v daemon=%s", result, err, output.String())
		}
		replayed, err := fixture.store.QueryCommand(ctx, m5TestDomain, sweepID, entry.RequestHash, 1, fixture.peer, m5TestEnv, time.Now())
		if err != nil || !bytes.Equal(replayed.SignedReceipt, status.SignedReceipt) {
			t.Fatalf("recovery changed signed receipt: %+v %v", replayed, err)
		}
	}
	if err != nil {
		t.Fatalf("sweep: %+v %v daemon=%s trace=%v", result, err, output.String(), trace.snapshot())
	}
	refused := scenario == "not-eligible" || scenario == "target-missing"
	if refused {
		problem := operation.ProblemBatchSweepNotEligible
		if scenario == "target-missing" {
			problem = operation.ProblemBatchSweepTargetMissing
		}
		if result.Result.Code != operation.ResultRefused || result.Result.Problem == nil || result.Result.Problem.Code != problem {
			t.Fatalf("sweep refusal: %+v", result)
		}
	} else if result.Result.Code != operation.ResultSucceeded || result.Result.Output != (operation.BatchSweepAnonymousOutput{Outcome: operation.BatchSweepAnonymousSwept}) {
		t.Fatalf("effectful sweep: %+v", result)
	}
	results := []wipd.BatchSweepAnonymousResponse{result}
	if !refused {
		already, err := client.SweepAnonymousBatch(ctx, repairTransportID(71), sweepMatter, grant.BatchID, closeID, "human")
		if err != nil || already.Result.Code != operation.ResultSucceeded || already.Result.Output != (operation.BatchSweepAnonymousOutput{Outcome: operation.BatchSweepAnonymousAlreadySwept}) {
			t.Fatalf("fresh-ID no-event sweep: %+v %v daemon=%s", already, err, output.String())
		}
		results = append(results, already)
	}
	after, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	wantEvents := before.EventCount
	if !refused {
		wantEvents++
	}
	if err != nil || after.EventCount != wantEvents {
		t.Fatalf("sweep effects: %+v want=%d err=%v", after, wantEvents, err)
	}
	journal := openJournal()
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil || installed.Anchor.EventCount != wantEvents || installed.Anchor.Digest != after.Digest {
		t.Fatalf("installed sweep prefix: %+v %v", installed, err)
	}
	for index, want := range results {
		receipt, found := installed.Receipts[want.CommandID]
		status, err := fixture.store.QueryCommand(ctx, m5TestDomain, want.CommandID, want.RequestHash, 1, fixture.peer, m5TestEnv, time.Now())
		if err != nil || !found || !bytes.Equal(receipt.CanonicalReceipt, want.Receipt) || !bytes.Equal(status.Receipt, want.Receipt) {
			t.Fatalf("installed/original sweep receipt: %+v %+v %v", receipt, status, err)
		}
		fields, err := wipdwire.DecodeCanonicalMap(want.Receipt, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if err != nil || (fields["accepted_events"] == nil) != (refused || index > 0) {
			t.Fatalf("sweep event range: %#v %v", fields, err)
		}
	}
	_ = journal.Close()
	restart()
	for _, want := range results {
		got, err := client.SweepAnonymousBatch(ctx, want.CommandID, sweepMatter, grant.BatchID, closeID, "human")
		if err != nil || got.RequestHash != want.RequestHash || got.Result.Code != want.Result.Code || got.Result.Output != want.Result.Output || !bytes.Equal(got.Receipt, want.Receipt) {
			t.Fatalf("installed exact replay: %+v %v daemon=%s", got, err, output.String())
		}
	}
	_, err = client.SweepAnonymousBatch(ctx, sweepID, sweepMatter, grant.BatchID, closeID, "role:other")
	assertProblem(err, "command.id-conflict")
	final, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || final != after {
		t.Fatalf("replay/conflict repeated effects: %+v %v", final, err)
	}
}

// Let the authority close commit, but deny the subsequent installation pull.
// Earlier readiness pulls and the release's journal ACK/seal still succeed.
func blockSweepProcessReleaseInstall(next http.Handler, armed *atomic.Bool, faults *m5ConnectionFaults) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == labExchangePath && armed.Load() {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			frame, err := wipdwire.ReadFrame(bytes.NewReader(body))
			if err == nil && frame.Kind == "claim.release" && armed.CompareAndSwap(true, false) {
				faults.blockPull.Store(true)
			}
		}
		next.ServeHTTP(writer, request)
	})
}

type sweepProcessTerminalHoldWriter struct {
	http.ResponseWriter
	armed             *atomic.Bool
	committed, resume chan struct{}
}

func (writer sweepProcessTerminalHoldWriter) Write(payload []byte) (int, error) {
	frame, err := wipdwire.ReadFrame(bytes.NewReader(payload))
	if err == nil && frame.Kind == "command.terminal" && writer.armed.CompareAndSwap(true, false) {
		close(writer.committed)
		<-writer.resume
	}
	return writer.ResponseWriter.Write(payload)
}

func (writer sweepProcessTerminalHoldWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
