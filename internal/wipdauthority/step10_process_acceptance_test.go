package wipdauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
)

func TestStep10OpenRunSweepRefusesWithoutPartialEffect(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated wipd process acceptance is Linux-only")
	}
	ctx := context.Background()
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step10Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	config.NamedBatchRead = true
	fixture.server, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler = fixture.server.http.Handler
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case serveErr := <-serveDone:
			if serveErr != nil {
				t.Errorf("Step 10 authority server stopped with error: %v", serveErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("Step 10 authority server did not stop")
		}
	})

	root, err := os.MkdirTemp("/tmp", "w10-open-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step10")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(environment.state.PrivateKeyPKCS8)
	binary := m5WipdBinary(t)
	client, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	t.Cleanup(func() {
		_ = client.Close()
		stopDaemon()
	})
	command := func(id string, sequence uint64, request operation.Request) operation.Command {
		return operation.Command{
			ID: id, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, EnvironmentSequence: sequence,
			ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: id,
			Request: request,
		}
	}
	createdResult, err := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(10), 1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "Open Run sweep guard", Locator: "open-run-sweep-guard"},
	}))
	created, ok := createdResult.Output.(operation.MatterCreateOutput)
	if err != nil || createdResult.Code != operation.ResultSucceeded || !ok {
		t.Fatalf("create open-Run sweep guard Matter: result=%+v error=%v daemon=%s", createdResult, err, daemonOutput.String())
	}
	if _, err = client.ReleaseBirthClaim(ctx, created.ID, m5TwoEnvironmentID(11), operation.Actor("human")); err != nil {
		t.Fatalf("release open-Run sweep guard birth claim: %v", err)
	}
	cloneID, worktreeID, dispatchID := m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), m5TwoEnvironmentID(14)
	claim, err := client.AcquireClaim(ctx, m5TwoEnvironmentID(15), created.ID,
		cloneID, worktreeID, dispatchID, operation.Actor("human"))
	if err != nil || claim.Code != operation.ResultSucceeded || claim.Grant == nil {
		t.Fatalf("acquire open-Run sweep guard claim: result=%+v error=%v daemon=%s", claim, err, daemonOutput.String())
	}
	sequence := uint64(4)
	execute := func(id int, request operation.Request) operation.Result {
		t.Helper()
		result, executeErr := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(id), sequence, request))
		sequence++
		if executeErr != nil || result.Code != operation.ResultSucceeded {
			t.Fatalf("Step 10 command %d failed: result=%+v error=%v daemon=%s", id, result, executeErr, daemonOutput.String())
		}
		return result
	}
	runID, roleID, runLocator := m5TwoEnvironmentID(16), m5TwoEnvironmentID(17), "open-run-sweep-guard-run"
	claimContext := &operation.ClaimContext{ID: claim.Grant.ClaimID, Epoch: fmt.Sprint(claim.Grant.ClaimEpoch)}
	execute(18, operation.Request{
		Operation: operation.RoleSpawnV1.Metadata().Operation, Actor: operation.Actor("system:scheduler"),
		Context: operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID}, Claim: claimContext,
		Input: operation.RoleLifecycleInput{
			RunID: runID, RunLocator: runLocator, MatterID: created.ID,
			DispatchID: claim.Grant.DispatchID, RoleID: roleID, Role: "researcher",
		},
	})
	execute(19, operation.Request{
		Operation: operation.MatterStartV1.Metadata().Operation, Actor: operation.Actor("human"),
		Context: operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID}, Claim: claimContext,
		Input: operation.NodeLifecycleInput{NodeID: created.ID},
	})
	execute(20, operation.Request{
		Operation: operation.RoleCloseV1.Metadata().Operation, Actor: operation.Actor("role:researcher"),
		Context: operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID}, Claim: claimContext,
		Input: operation.RoleLifecycleInput{
			RunID: runID, RunLocator: runLocator, MatterID: created.ID,
			DispatchID: claim.Grant.DispatchID, RoleID: roleID, Role: "researcher", Reason: "completed",
		},
	})
	finished, err := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(21), sequence, operation.Request{
		Operation: operation.MatterFinishV1.Metadata().Operation, Actor: operation.Actor("human"),
		Context: operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID}, Claim: claimContext,
		Input: operation.MatterFinishInput{MatterID: created.ID},
	}))
	sequence++
	if err != nil || finished.Code != operation.ResultSucceeded || finished.Output != (operation.MatterFinishOutput{
		MatterID: created.ID, State: "done", BecameSealed: true,
	}) {
		t.Fatalf("finish guard Matter while leaving its Run open: result=%+v error=%v daemon=%s", finished, err, daemonOutput.String())
	}
	closed, err := client.ReleaseClaimJournal(ctx, m5TwoEnvironmentID(22), claim.Grant.ClaimID,
		claim.Grant.ClaimEpoch, created.ID, claim.Grant.DispatchID, operation.Actor("human"))
	sequence++
	if err != nil || closed.Code != operation.ResultSucceeded || len(closed.Receipt) == 0 {
		t.Fatalf("release normally before testing the open-Run sweep guard: result=%+v error=%v daemon=%s", closed, err, daemonOutput.String())
	}
	beforeSweep, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Step 10 Environment IPC before open-Run sweep guard: %v", err)
	}
	stopDaemon()
	setM5ProcessCatalogue(t, environment.profileRoot, "m6-step7")
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	sweep, err := client.SweepAnonymousBatch(ctx, m5TwoEnvironmentID(23), created.ID, claim.Grant.BatchID,
		closed.CommandID, operation.Actor("system:scheduler"))
	if err != nil || sweep.Result.Code != operation.ResultRefused || sweep.Result.Problem == nil ||
		sweep.Result.Problem.Code != operation.ProblemBatchSweepUnsupported {
		t.Fatalf("Step 7 must refuse the open Run without partial sweep: result=%+v error=%v daemon=%s", sweep.Result, err, daemonOutput.String())
	}
	afterSweep, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterSweep.EventCount != beforeSweep.EventCount || afterSweep.Digest != beforeSweep.Digest {
		t.Fatalf("open-Run refusal partially swept or changed authority history: before=%+v after=%+v error=%v", beforeSweep, afterSweep, err)
	}
	sweepReplay, err := client.SweepAnonymousBatch(ctx, m5TwoEnvironmentID(23), created.ID, claim.Grant.BatchID,
		closed.CommandID, operation.Actor("system:scheduler"))
	if err != nil || sweepReplay.Result.Code != sweep.Result.Code || !reflect.DeepEqual(sweepReplay.Receipt, sweep.Receipt) {
		t.Fatalf("same-ID open-Run sweep refusal did not replay exactly: result=%+v error=%v", sweepReplay, err)
	}
}

func setM5ProcessCatalogue(t *testing.T, profileRoot, catalogue string) {
	t.Helper()
	path := filepath.Join(profileRoot, "connected-authority.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disposable connected profile: %v", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decode disposable connected profile: %v", err)
	}
	encodedCatalogue, err := json.Marshal(catalogue)
	if err != nil {
		t.Fatalf("encode disposable connected profile catalogue: %v", err)
	}
	fields["command_catalogue"] = encodedCatalogue
	updated, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode disposable connected profile: %v", err)
	}
	if err = os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("update disposable connected profile catalogue: %v", err)
	}
}
