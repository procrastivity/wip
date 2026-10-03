package wipdauthority

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestM6Step7GateRegistryIsClosed(t *testing.T) {
	fixture := newM5CommandFixture(t)
	for _, omitted := range []string{"", "gate.declare", "gate.close", "gate.dismiss", "batch.sweep-anonymous"} {
		t.Run("omit-"+omitted, func(t *testing.T) {
			complete, err := NewM6Step7Registry()
			if err != nil {
				t.Fatal(err)
			}
			registry := operation.NewRegistry()
			for _, definition := range complete.Definitions() {
				if definition.Metadata().Operation.Name == omitted {
					continue
				}
				if err := registry.Register(definition, func(context.Context, operation.Request) operation.Result {
					return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: "placeholder"}}
				}); err != nil {
					t.Fatal(err)
				}
			}
			config := fixture.config
			config.Registry = registry
			_, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
			if (err == nil) != (omitted == "") {
				t.Fatalf("partial Step 7 set omitted=%q: %v", omitted, err)
			}
			if _, err := NewM5LabServer(fixture.profile, fixture.serverCert, config); err == nil {
				t.Fatal("old M5 authority accepted Step 7 gates")
			}
		})
	}
	step4, err := NewM6Step4Registry()
	if err != nil {
		t.Fatal(err)
	}
	step5, err := NewM6Step5Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, registry := range []*operation.Registry{step4, step5} {
		for _, definition := range registry.Definitions() {
			id := definition.Metadata().Operation
			for _, gate := range operation.GateCatalogue() {
				if id == gate.Metadata().Operation {
					t.Fatalf("old authority registry contains %s", id)
				}
			}
		}
	}
}

func TestOrdinaryGatesThroughAuthenticatedWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated local wipd IPC is Linux-only")
	}
	for _, scenario := range []string{"close-after-finish", "dismiss-after-finish", "scale-change", "dismiss-before-done", "wrong-subject", "human-role-owner", "unproved-role-owner", "old-profile"} {
		t.Run(scenario, func(t *testing.T) { runOrdinaryGateProcess(t, scenario, "") })
	}
	for _, scenario := range []string{"close-after-finish", "scale-change"} {
		for _, loss := range []string{"before", "after"} {
			t.Run(scenario+"-ack-loss-"+loss, func(t *testing.T) { runOrdinaryGateProcess(t, scenario, loss) })
		}
	}
}

func runOrdinaryGateProcess(t *testing.T, scenario, ackLoss string) {
	ctx := context.Background()
	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step7Registry()
	if err != nil {
		t.Fatal(err)
	}
	config := fixture.config
	config.Registry = registry
	fixture.server, err = NewM6LabServer(fixture.profile, fixture.serverCert, config)
	if err != nil {
		t.Fatal(err)
	}
	var trace m6AuthorityHTTPTrace
	if ackLoss != "" {
		fixture.server.http.Handler = dropFirstProcessRepairAck(fixture.server.http.Handler, ackLoss)
	}
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
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
	root, err := os.MkdirTemp("/tmp", "gate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	catalogue := "m6-step7"
	if scenario == "old-profile" {
		catalogue = "m6-step5"
	}
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "env", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], catalogue)
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, output := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	defer func() { _ = client.Close(); stopDaemon() }()
	command := func(id int, sequence uint64, request operation.Request) operation.Command {
		commandID := repairTransportID(id)
		return operation.Command{
			ID: commandID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, EnvironmentSequence: sequence,
			ActedAt: time.Now().UTC().Format(time.RFC3339Nano), CorrelationCommandID: commandID, Request: request,
		}
	}
	create := command(60, 1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "Ordinary gates", Locator: "ordinary-gates"},
	})
	created, err := client.ExecuteCommand(ctx, create)
	matter, ok := created.Output.(operation.MatterCreateOutput)
	if err != nil || created.Code != operation.ResultSucceeded || !ok {
		t.Fatalf("create: %+v %v daemon=%s", created, err, output.String())
	}
	if _, err = client.ReleaseBirthClaim(ctx, matter.ID, repairTransportID(61), "human"); err != nil {
		t.Fatal(err)
	}
	clone, worktree := repairTransportID(63), repairTransportID(64)
	acquired, err := client.AcquireClaim(ctx, repairTransportID(62), matter.ID, clone, worktree, repairTransportID(65), "human")
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
		t.Fatalf("acquire: %+v %v", acquired, err)
	}
	claim := &operation.ClaimContext{ID: acquired.Grant.ClaimID, Epoch: fmt.Sprint(acquired.Grant.ClaimEpoch)}
	request := func(definition operation.Definition, input operation.Input) operation.Request {
		return operation.Request{
			Operation: definition.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo, Clone: clone, Worktree: worktree}, Claim: claim, Input: input,
		}
	}
	gate := "reviewed-local"
	if scenario == "human-role-owner" || scenario == "unproved-role-owner" {
		gate = "verified"
	}
	declare := command(66, 4, request(operation.GateDeclareV1, operation.GateDeclareInput{Gate: gate, Scale: "matter"}))
	declared, err := client.ExecuteCommand(ctx, declare)
	if scenario == "old-profile" {
		if err == nil || !strings.Contains(err.Error(), "operation.unsupported-version") {
			t.Fatalf("Step 5 profile admitted ordinary gate: %+v %v", declared, err)
		}
		anchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil || anchor.EventCount != 5 {
			t.Fatalf("old profile changed authority history: %+v %v", anchor, anchorErr)
		}
		return
	}
	if err != nil || declared.Code != operation.ResultSucceeded || declared.Output != (operation.GateDeclareOutput{Gate: gate, Scale: "matter"}) {
		t.Fatalf("declare: %+v %v daemon=%s trace=%v", declared, err, output.String(), trace.snapshot())
	}
	commands := []operation.Command{declare}
	wantResults := []operation.Result{declared}
	noEvents := []bool{false}
	before, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	success := scenario == "close-after-finish" || scenario == "dismiss-after-finish"
	var last operation.Command
	var want operation.Result
	if success {
		identical := command(67, 5, declare.Request)
		got, err := client.ExecuteCommand(ctx, identical)
		if ackLoss != "" {
			if err == nil {
				t.Fatal("lost no-event declaration ACK unexpectedly succeeded")
			}
			_ = client.Close()
			stopDaemon()
			client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
			got, err = client.ExecuteCommand(ctx, identical)
		}
		if err != nil || got.Code != operation.ResultSucceeded || got.Output != declared.Output {
			t.Fatalf("identical no-event declaration: %+v %v daemon=%s", got, err, output.String())
		}
		after, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || after != before {
			t.Fatalf("identical declaration changed prefix: %+v %v", after, err)
		}
		commands, wantResults, noEvents = append(commands, identical), append(wantResults, got), append(noEvents, true)
		for _, step := range []struct {
			id         int
			seq        uint64
			definition operation.Definition
			input      operation.Input
		}{
			{68, 6, operation.MatterStartV1, operation.NodeLifecycleInput{NodeID: matter.ID}},
			{69, 7, operation.MatterFinishV1, operation.MatterFinishInput{MatterID: matter.ID}},
		} {
			got, err := client.ExecuteCommand(ctx, command(step.id, step.seq, request(step.definition, step.input)))
			if err != nil || got.Code != operation.ResultSucceeded {
				t.Fatalf("%s: %+v %v", step.definition.Metadata().Operation, got, err)
			}
			if step.id == 69 && got.Output != (operation.MatterFinishOutput{MatterID: matter.ID, State: "done", BecameSealed: false}) {
				t.Fatalf("finish before gate satisfaction should be Done but unsealed: %#v", got.Output)
			}
		}
		if scenario == "close-after-finish" {
			last = command(70, 8, request(operation.GateCloseV1, operation.GateCloseInput{Gate: gate, NodeID: matter.ID}))
			want = operation.Result{Code: operation.ResultSucceeded, Output: operation.GateCloseOutput{Gate: gate, NodeID: matter.ID, Scale: "matter"}}
		} else {
			last = command(70, 8, request(operation.GateDismissV1, operation.GateDismissInput{Gate: gate, NodeID: matter.ID, Reason: "Human accepts this local exception"}))
			want = operation.Result{Code: operation.ResultSucceeded, Output: operation.GateDismissOutput{Gate: gate, NodeID: matter.ID, Scale: "matter"}}
		}
	} else {
		var refused operation.Request
		var problem string
		switch scenario {
		case "scale-change":
			refused = request(operation.GateDeclareV1, operation.GateDeclareInput{Gate: gate, Scale: "step"})
			problem = "refusal.gate-scale-change"
		case "dismiss-before-done":
			refused = request(operation.GateDismissV1, operation.GateDismissInput{Gate: gate, NodeID: matter.ID, Reason: "Not yet done"})
			problem = "refusal.gate-dismissal-lifecycle"
		case "wrong-subject":
			// An existing node in the claim's Matter, but not the declared scale.
			stepCommand := command(67, 5, request(operation.StepCreateV2, operation.StepCreateInput{ParentID: matter.ID, Title: "Wrong scale"}))
			stepResult, err := client.ExecuteCommand(ctx, stepCommand)
			step, ok := stepResult.Output.(operation.StepCreateOutput)
			if err != nil || stepResult.Code != operation.ResultSucceeded || !ok {
				t.Fatalf("Step birth: %+v %v", stepResult, err)
			}
			refused = request(operation.GateCloseV1, operation.GateCloseInput{Gate: gate, NodeID: step.ID})
			problem = "refusal.gate-subject"
		case "human-role-owner", "unproved-role-owner":
			refused = request(operation.GateCloseV1, operation.GateCloseInput{Gate: gate, NodeID: matter.ID})
			if scenario == "unproved-role-owner" {
				refused.Actor = "role:verifier"
			}
			problem = "refusal.gate-owner"
		}
		sequence := uint64(5)
		if scenario == "wrong-subject" {
			sequence++
		}
		last = command(70, sequence, refused)
		want = operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{Code: operation.ProblemCode(problem)}}
		before, err = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := client.ExecuteCommand(ctx, last)
	if !success && ackLoss != "" {
		if err == nil {
			t.Fatal("lost gate refusal ACK unexpectedly succeeded")
		}
		_ = client.Close()
		stopDaemon()
		client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
		got, err = client.ExecuteCommand(ctx, last)
	}
	if err != nil || got.Code != want.Code || got.Output != want.Output || want.Problem != nil && (got.Problem == nil || got.Problem.Code != want.Problem.Code) {
		t.Fatalf("gate terminal: %+v want=%+v err=%v daemon=%s trace=%v", got, want, err, output.String(), trace.snapshot())
	}
	commands, wantResults, noEvents = append(commands, last), append(wantResults, got), append(noEvents, !success)
	if !success {
		after, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if err != nil || after != before {
			t.Fatalf("refusal changed prefix: %+v %v", after, err)
		}
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") ||
		!traceContains(trace.snapshot(), "/wipd/v1/exchange request=claim-journal.ack response=claim-journal.acked") {
		t.Fatalf("missing real submission/ACK hop: %v", trace.snapshot())
	}
	_ = client.Close()
	stopDaemon()
	journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for index, item := range commands {
		receipt, found := installed.Receipts[item.ID]
		status, err := fixture.store.QueryCommand(ctx, m5TestDomain, item.ID, m5CommandHash(t, item), 1, fixture.peer, m5TestEnv, time.Now())
		if !found || err != nil || receipt.ResultCode != wantResults[index].Code || !bytes.Equal(receipt.CanonicalReceipt, status.Receipt) || len(status.SignedReceipt) == 0 {
			t.Fatalf("installed vs original receipt for %s: %+v %+v %v", item.ID, receipt, status, err)
		}
		fields, err := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt, "schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if err != nil || (fields["accepted_events"] == nil) != noEvents[index] {
			t.Fatalf("event range for %s: %#v %v", item.ID, fields, err)
		}
		var state string
		wantState := "terminal"
		if wantResults[index].Code != operation.ResultSucceeded {
			wantState = "quarantined"
		}
		if err = db.QueryRow(`SELECT state FROM claim_journal_entries WHERE domain_id=? AND command_id=?`, m5TestDomain, item.ID).Scan(&state); err != nil || state != wantState {
			t.Fatalf("gate journal ACK %s: %s %v", item.ID, state, err)
		}
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	// Restarted replay retains all gate terminals and can re-ACK successful heads.
	client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	for index, item := range commands {
		got, err := client.ExecuteCommand(ctx, item)
		want := wantResults[index]
		if err != nil || got.Code != want.Code || got.Output != want.Output || want.Problem != nil && (got.Problem == nil || got.Problem.Code != want.Problem.Code) {
			t.Fatalf("exact replay %s: %+v %v daemon=%s", item.ID, got, err, output.String())
		}
	}
	changed := last
	changed.Request.Actor = "role:other"
	if _, err = client.ExecuteCommand(ctx, changed); err == nil || !strings.Contains(err.Error(), "command.id-conflict") {
		t.Fatalf("changed request hash replay: %v", err)
	}
	if success {
		closed, err := client.ReleaseClaimJournal(ctx, repairTransportID(71), acquired.Grant.ClaimID, acquired.Grant.ClaimEpoch, matter.ID, acquired.Grant.DispatchID, "human")
		if err != nil || closed.Code != operation.ResultSucceeded {
			t.Fatalf("normal release includes effectful/no-event gate ACKs: %+v %v daemon=%s", closed, err, output.String())
		}
		_ = client.Close()
		stopDaemon()
		client, stopDaemon, output = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
		for index, item := range commands {
			got, err := client.ExecuteCommand(ctx, item)
			if err != nil || got.Code != wantResults[index].Code || got.Output != wantResults[index].Output {
				t.Fatalf("post-release exact replay %s: %+v %v daemon=%s trace=%v", item.ID, got, err, output.String(), trace.snapshot())
			}
		}
		if _, err := client.ExecuteCommand(ctx, changed); err == nil || !strings.Contains(err.Error(), "command.id-conflict") {
			t.Fatalf("post-release changed hash did not conflict: %v", err)
		}
		fresh := command(72, 10, declare.Request)
		if _, err := client.ExecuteCommand(ctx, fresh); err == nil {
			t.Fatal("fresh gate declaration bypassed the closed-claim readiness fence")
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND command_id=?`, m5TestDomain, fresh.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("fresh gate with closed claim reached authority: %d %v", count, err)
		}
	} else {
		if _, err := client.ReleaseClaimJournal(ctx, repairTransportID(71), acquired.Grant.ClaimID, acquired.Grant.ClaimEpoch, matter.ID, acquired.Grant.DispatchID, "human"); err == nil {
			t.Fatal("quarantined gate refusal entered a normal release barrier")
		}
		current, err := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, m5TestEnv, acquired.Grant.ClaimID,
			acquired.Grant.ClaimEpoch, matter.ID, acquired.Grant.DispatchID)
		if err != nil || current.State != "open" {
			t.Fatalf("refusal unexpectedly closed claim: %+v %v", current, err)
		}
	}
}
