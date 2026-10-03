package wipdauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

func TestM6LifecycleCommandsThroughWipdProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("wipd's authenticated local socket process is Linux-only")
	}
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
	fixture.handler = fixture.server.http.Handler
	var exchangeTrace m6AuthorityHTTPTrace
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.handler, &exchangeTrace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case serveErr := <-serveDone:
			if serveErr != nil {
				t.Errorf("M6 authority server stopped with error: %v", serveErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("M6 authority server did not stop")
		}
	})

	// Keep the Unix socket path short regardless of TMPDIR or the test name.
	root, err := os.MkdirTemp("/tmp", "w6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "m6-environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step7")
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
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
	create := command(m5TwoEnvironmentID(31), 1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "M6 process lifecycle", Locator: "m6-process-lifecycle"},
	})
	created, err := client.ExecuteCommand(ctx, create)
	matter, ok := created.Output.(operation.MatterCreateOutput)
	if err != nil || created.Code != operation.ResultSucceeded || !ok || matter.ID == "" {
		t.Fatalf("Matter creation over authenticated daemon IPC = %+v (%T), %v; daemon=%s", created, created.Output, err, daemonOutput.String())
	}
	if _, err = client.ReleaseBirthClaim(ctx, matter.ID, m5TwoEnvironmentID(32), operation.Actor("human")); err != nil {
		t.Fatalf("release Matter birth claim: %v", err)
	}
	acquired, err := client.AcquireClaim(ctx, m5TwoEnvironmentID(33), matter.ID,
		m5TwoEnvironmentID(34), m5TwoEnvironmentID(35), m5TwoEnvironmentID(36), operation.Actor("human"))
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
		t.Fatalf("acquire active Matter claim over daemon IPC = %+v, %v; daemon=%s", acquired, err, daemonOutput.String())
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close M6 IPC client before inspecting journal head: %v", err)
	}
	stopDaemon()
	journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv,
		OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	birthRelease, err := journal.BirthReleaseAttempt(m5TwoEnvironmentID(32))
	if err != nil {
		t.Fatalf("read durable birth-release sequence: %v", err)
	}
	acquireAttempt, err := journal.ClaimAcquireAttempt(m5TwoEnvironmentID(33))
	if err != nil {
		t.Fatalf("read durable claim-acquire sequence: %v", err)
	}
	if create.EnvironmentSequence != 1 || birthRelease.EnvironmentSeq != 2 || acquireAttempt.EnvironmentSeq != 3 {
		t.Fatalf("expected exact serialized command sequence create/release/acquire = 1/2/3, got %d/%d/%d",
			create.EnvironmentSequence, birthRelease.EnvironmentSeq, acquireAttempt.EnvironmentSeq)
	}
	if err = journal.Close(); err != nil {
		t.Fatalf("close journal after reading serialized sequence head: %v", err)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	claim := &operation.ClaimContext{ID: acquired.Grant.ClaimID, Epoch: fmt.Sprint(acquired.Grant.ClaimEpoch)}
	commandContext := operation.Context{Repo: m5TestRepo, Clone: m5TwoEnvironmentID(34), Worktree: m5TwoEnvironmentID(35)}
	secondMatterCommand := command(m5TwoEnvironmentID(41), acquireAttempt.EnvironmentSeq+1, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "Standalone M6 start", Locator: "standalone-m6-start"},
	})
	secondMatterResult, err := client.ExecuteCommand(ctx, secondMatterCommand)
	secondMatter, ok := secondMatterResult.Output.(operation.MatterCreateOutput)
	if err != nil || secondMatterResult.Code != operation.ResultSucceeded || !ok || secondMatter.ID == "" {
		t.Fatalf("second Matter creation over authenticated daemon IPC = %+v (%T), %v; daemon=%s",
			secondMatterResult, secondMatterResult.Output, err, daemonOutput.String())
	}
	secondReleaseID, secondAcquireID := m5TwoEnvironmentID(42), m5TwoEnvironmentID(43)
	if _, err = client.ReleaseBirthClaim(ctx, secondMatter.ID, secondReleaseID, operation.Actor("human")); err != nil {
		t.Fatalf("release second Matter birth claim: %v", err)
	}
	secondAcquired, err := client.AcquireClaim(ctx, secondAcquireID, secondMatter.ID,
		m5TwoEnvironmentID(44), m5TwoEnvironmentID(45), m5TwoEnvironmentID(46), operation.Actor("human"))
	if err != nil || secondAcquired.Code != operation.ResultSucceeded || secondAcquired.Grant == nil {
		t.Fatalf("acquire second Matter claim over daemon IPC = %+v, %v; daemon=%s", secondAcquired, err, daemonOutput.String())
	}
	secondClaim := &operation.ClaimContext{ID: secondAcquired.Grant.ClaimID, Epoch: fmt.Sprint(secondAcquired.Grant.ClaimEpoch)}
	secondCommandContext := operation.Context{Repo: m5TestRepo, Clone: m5TwoEnvironmentID(44), Worktree: m5TwoEnvironmentID(45)}
	sequence := acquireAttempt.EnvironmentSeq + 4
	commands := make(map[string]operation.Command)
	submitAs := func(commandClaim *operation.ClaimContext, commandContext operation.Context, id string, operationID operation.ID, input operation.Input) operation.Result {
		t.Helper()
		request := operation.Request{Operation: operationID, Actor: "human", Context: commandContext, Claim: commandClaim, Input: input}
		item := command(id, sequence, request)
		commands[id] = item
		sequence++
		result, submitErr := client.ExecuteCommand(ctx, item)
		if submitErr != nil || result.Code != operation.ResultSucceeded {
			hash, _ := item.RequestHash()
			status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, item.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
			anchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			t.Fatalf("%s through typed daemon IPC = %+v, %v; authority status=%+v statusErr=%v anchor=%+v anchorErr=%v domain=%s profile=%+v exchanges=%v daemon stderr/stdout=%q",
				operationID, result, submitErr, status, statusErr, anchor, anchorErr, fixture.profile.domainID,
				mustLoadM6TestConfig(t, environment.profileRoot), exchangeTrace.snapshot(), daemonOutput.String())
		}
		return result
	}
	submit := func(id string, operationID operation.ID, input operation.Input) operation.Result {
		return submitAs(claim, commandContext, id, operationID, input)
	}
	standalone := submitAs(secondClaim, secondCommandContext, m5TwoEnvironmentID(47), operation.MatterStartV1.Metadata().Operation,
		operation.NodeLifecycleInput{NodeID: secondMatter.ID})
	if standalone.Output != (operation.MatterLifecycleOutput{MatterID: secondMatter.ID, State: "in-progress"}) {
		t.Fatalf("standalone Matter start output = %#v", standalone.Output)
	}
	canceled := submitAs(secondClaim, secondCommandContext, m5TwoEnvironmentID(48), operation.MatterCancelV1.Metadata().Operation,
		operation.NodeLifecycleInput{NodeID: secondMatter.ID})
	if canceled.Output != (operation.MatterLifecycleOutput{MatterID: secondMatter.ID, State: "canceled"}) {
		t.Fatalf("Matter cancel output = %#v", canceled.Output)
	}

	// This command is accepted only when the explicit M6 profile advertised the
	// M6 catalogue to the authenticated IPC client and the authority negotiation.
	stageResult := submit(m5TwoEnvironmentID(37), operation.StageCreateV1.Metadata().Operation,
		operation.StageCreateInput{MatterID: matter.ID, Title: "Process Stage"})
	if !traceContains(exchangeTrace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") ||
		!traceContains(exchangeTrace.snapshot(), "/wipd/v1/exchange request=pull.request response=pull.start,event.record") {
		t.Fatalf("Stage command did not traverse authority submit, terminal receipt, and pull: %v", exchangeTrace.snapshot())
	}
	stage, ok := stageResult.Output.(operation.StageCreateOutput)
	if !ok || stage.ID == "" || stage.MatterID != matter.ID {
		t.Fatalf("Stage creation output = %#v", stageResult.Output)
	}
	stepResult := submit(m5TwoEnvironmentID(38), operation.StepCreateV2.Metadata().Operation,
		operation.StepCreateInput{ParentID: stage.ID, Title: "Process Step"})
	step, ok := stepResult.Output.(operation.StepCreateOutput)
	if !ok || step.ID == "" || step.ParentID != stage.ID {
		t.Fatalf("Step creation output = %#v", stepResult.Output)
	}

	// The Step start is the exact Step 5 command under test. The authority emits
	// the Matter, Stage, and Step cascade as one causal event prefix.
	started := submit(m5TwoEnvironmentID(39), operation.StepStartV1.Metadata().Operation,
		operation.StepLifecycleInput{StepID: step.ID})
	if started.Output != (operation.StepLifecycleOutput{StepID: step.ID, MatterID: matter.ID, State: "in-progress"}) {
		t.Fatalf("nested Step start output = %#v", started.Output)
	}
	paused := submit(m5TwoEnvironmentID(40), operation.StepPauseV1.Metadata().Operation,
		operation.StepLifecycleInput{StepID: step.ID})
	if paused.Output != (operation.StepLifecycleOutput{StepID: step.ID, MatterID: matter.ID, State: "paused"}) {
		t.Fatalf("Step pause output = %#v", paused.Output)
	}
	resumed := submit(m5TwoEnvironmentID(49), operation.StepResumeV1.Metadata().Operation,
		operation.StepLifecycleInput{StepID: step.ID})
	if resumed.Output != (operation.StepLifecycleOutput{StepID: step.ID, MatterID: matter.ID, State: "in-progress"}) {
		t.Fatalf("Step resume output = %#v", resumed.Output)
	}
	stepCancelReason := "superseded after resume"
	stepCanceled := submit(m5TwoEnvironmentID(50), operation.StepCancelV1.Metadata().Operation,
		operation.StepCancelInput{StepID: step.ID, Reason: stepCancelReason})
	if stepCanceled.Output != (operation.StepLifecycleOutput{StepID: step.ID, MatterID: matter.ID, State: "canceled"}) {
		t.Fatalf("Step cancel output = %#v", stepCanceled.Output)
	}

	// Exercise the explicit Step 7 path on a fresh Matter. The normal birth
	// release, final Matter state, acquired release, and standalone sweep all
	// use the Environment's persisted sequence and installed release anchor.
	sweepMatterCommandID := m5TwoEnvironmentID(61)
	sweepMatterCommand := command(sweepMatterCommandID, sequence, operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.MatterCreateInput{Title: "Step 7 sweep target", Locator: "step-7-sweep-target"},
	})
	sequence++
	sweepMatterResult, err := client.ExecuteCommand(ctx, sweepMatterCommand)
	sweepMatter, ok := sweepMatterResult.Output.(operation.MatterCreateOutput)
	if err != nil || sweepMatterResult.Code != operation.ResultSucceeded || !ok || sweepMatter.ID == "" {
		t.Fatalf("Step 7 target Matter create = %+v, %v; daemon=%s", sweepMatterResult, err, daemonOutput.String())
	}
	birthCloseID := m5TwoEnvironmentID(62)
	if _, err = client.ReleaseBirthClaim(ctx, sweepMatter.ID, birthCloseID, operation.Actor("human")); err != nil {
		t.Fatalf("Step 7 target birth-claim release: %v", err)
	}
	sequence++
	acquiredSweepClaim, err := client.AcquireClaim(ctx, m5TwoEnvironmentID(63), sweepMatter.ID,
		m5TwoEnvironmentID(64), m5TwoEnvironmentID(65), m5TwoEnvironmentID(66), operation.Actor("human"))
	if err != nil || acquiredSweepClaim.Code != operation.ResultSucceeded || acquiredSweepClaim.Grant == nil {
		t.Fatalf("Step 7 target claim acquisition = %+v, %v", acquiredSweepClaim, err)
	}
	sequence++
	sweepClaim := acquiredSweepClaim.Grant
	sweepClaimContext := &operation.ClaimContext{ID: sweepClaim.ClaimID, Epoch: fmt.Sprint(sweepClaim.ClaimEpoch)}
	sweepCommandContext := operation.Context{Repo: m5TestRepo, Clone: m5TwoEnvironmentID(64), Worktree: m5TwoEnvironmentID(65)}
	sweepMatterStarted := submitAs(sweepClaimContext, sweepCommandContext, m5TwoEnvironmentID(67),
		operation.MatterStartV1.Metadata().Operation, operation.NodeLifecycleInput{NodeID: sweepMatter.ID})
	if sweepMatterStarted.Output != (operation.MatterLifecycleOutput{MatterID: sweepMatter.ID, State: "in-progress"}) {
		t.Fatalf("Step 7 target Matter start = %#v", sweepMatterStarted.Output)
	}
	unfinishedStageResult := submitAs(sweepClaimContext, sweepCommandContext, m5TwoEnvironmentID(71),
		operation.StageCreateV1.Metadata().Operation, operation.StageCreateInput{MatterID: sweepMatter.ID, Title: "Unfinished sweep child"})
	unfinishedStage, ok := unfinishedStageResult.Output.(operation.StageCreateOutput)
	if !ok || unfinishedStage.MatterID != sweepMatter.ID || unfinishedStage.State != "planned" {
		t.Fatalf("Step 7 target unfinished child = %#v", unfinishedStageResult.Output)
	}
	sweepMatterFinish := submitAs(sweepClaimContext, sweepCommandContext, m5TwoEnvironmentID(68),
		operation.MatterFinishV1.Metadata().Operation, operation.MatterFinishInput{MatterID: sweepMatter.ID})
	if sweepMatterFinish.Output != (operation.MatterFinishOutput{
		MatterID: sweepMatter.ID, State: "done", BecameSealed: true,
	}) {
		t.Fatalf("Step 7 target Matter finish = %#v", sweepMatterFinish.Output)
	}
	claimCloseID := m5TwoEnvironmentID(69)
	closedSweepClaim, err := client.ReleaseClaimJournal(ctx, claimCloseID, sweepClaim.ClaimID, sweepClaim.ClaimEpoch,
		sweepMatter.ID, sweepClaim.DispatchID, operation.Actor("human"))
	if err != nil || closedSweepClaim.Code != operation.ResultSucceeded || closedSweepClaim.CommandID != claimCloseID {
		t.Fatalf("Step 7 target acquired-claim release = %+v, %v", closedSweepClaim, err)
	}
	sweepCommandID := m5TwoEnvironmentID(70)
	sweepResult, err := client.SweepAnonymousBatch(ctx, sweepCommandID, sweepMatter.ID, sweepClaim.BatchID,
		claimCloseID, operation.Actor("human"))
	if err != nil || sweepResult.CommandID != sweepCommandID || sweepResult.Result.Code != operation.ResultSucceeded ||
		sweepResult.Result.Output != (operation.BatchSweepAnonymousOutput{Outcome: operation.BatchSweepAnonymousSwept}) {
		t.Fatalf("standalone anonymous Batch sweep = %+v, %v; daemon=%s", sweepResult, err, daemonOutput.String())
	}
	if !traceContains(exchangeTrace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") ||
		!traceContains(exchangeTrace.snapshot(), "/wipd/v1/exchange request=pull.request response=pull.start,event.record") {
		t.Fatalf("Step 7 sweep did not traverse authority submit and verified pull: %v", exchangeTrace.snapshot())
	}

	if err = client.Close(); err != nil {
		t.Fatalf("close M6 IPC client before independent journal inspection: %v", err)
	}
	stopDaemon()
	journal, err = wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1, EnvironmentID: m5TestEnv,
		OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	})
	if err != nil {
		t.Fatalf("reopen installed Environment journal after process shutdown: %v", err)
	}
	defer journal.Close()
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Anchor.EventCount == 0 {
		t.Fatalf("daemon did not pull/install the event prefix after terminal commands: %+v", installed.Anchor)
	}
	records, err := journal.EventRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != int(installed.Anchor.EventCount) {
		t.Fatalf("installed event record count=%d, anchor=%+v", len(records), installed.Anchor)
	}
	installedSweep, err := journal.Get(sweepCommandID)
	sweepReceipt, sweepReceiptOK := installed.Receipts[sweepCommandID]
	if err != nil || installedSweep.State != wipdjournal.StateAttemptPrepared || installedSweep.RequestHash != sweepResult.RequestHash ||
		!sweepReceiptOK || sweepReceipt.RequestHash != sweepResult.RequestHash || sweepReceipt.ResultCode != operation.ResultSucceeded {
		t.Fatalf("Step 7 sweep journal attempt = %+v, %v", installedSweep, err)
	}
	closeEvidence, err := journal.ReleaseInstalledClaimCloseByID(ctx, claimCloseID)
	if err != nil || closeEvidence.ClaimID != sweepClaim.ClaimID || closeEvidence.ReleaseCommandID != claimCloseID {
		t.Fatalf("installed normal claim-close evidence = %+v, %v", closeEvidence, err)
	}
	var sweepEventFound bool
	for _, record := range records {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatalf("decode installed event %s: %v", record.EventID, decodeErr)
		}
		if fields["command_id"] == sweepCommandID && fields["kind"] == "batch.swept" && fields["subject_id"] == sweepClaim.BatchID {
			sweepEventFound = true
		}
		if fields["subject_id"] == unfinishedStage.ID && fields["kind"] != "stage.created" {
			t.Fatalf("sweep completed or changed the unfinished child: %#v", fields)
		}
	}
	if !sweepEventFound {
		t.Fatalf("installed Environment tail omitted batch.swept for command %s", sweepCommandID)
	}
	var cascade []map[string]any
	var standaloneStart map[string]any
	var matterCancel map[string]any
	var stepCancel map[string]any
	for _, record := range records {
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record.Record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatalf("decode installed authority event %s: %v", record.EventID, decodeErr)
		}
		if fields["command_id"] == m5TwoEnvironmentID(39) {
			cascade = append(cascade, fields)
		}
		if fields["command_id"] == m5TwoEnvironmentID(47) {
			standaloneStart = fields
		}
		if fields["command_id"] == m5TwoEnvironmentID(48) {
			matterCancel = fields
		}
		if fields["command_id"] == m5TwoEnvironmentID(50) {
			stepCancel = fields
		}
	}
	if standaloneStart == nil || standaloneStart["kind"] != "matter.started" || standaloneStart["subject_id"] != secondMatter.ID {
		t.Fatalf("installed standalone Matter start event = %#v", standaloneStart)
	}
	if matterCancel == nil || matterCancel["kind"] != "matter.canceled" || matterCancel["subject_id"] != secondMatter.ID {
		t.Fatalf("installed Matter cancel event = %#v", matterCancel)
	}
	if stepCancel == nil || stepCancel["kind"] != "step.canceled" || stepCancel["subject_id"] != step.ID {
		t.Fatalf("installed Step cancel event = %#v", stepCancel)
	}
	stepCancelPayload, ok := stepCancel["payload"].(map[string]any)
	if !ok || stepCancelPayload["reason"] != stepCancelReason || stepCancelPayload["from"] != "in-progress" || stepCancelPayload["to"] != "canceled" {
		t.Fatalf("installed Step cancel event payload = %#v", stepCancel["payload"])
	}
	standalonePayload, ok := standaloneStart["payload"].(map[string]any)
	if !ok || standalonePayload["cascade"] != nil || standalonePayload["cause_event_id"] != nil {
		t.Fatalf("standalone Matter start unexpectedly carries cascade metadata: %#v", standaloneStart["payload"])
	}
	if len(cascade) != 3 || cascade[0]["kind"] != "matter.started" || cascade[0]["subject_id"] != matter.ID ||
		cascade[1]["kind"] != "stage.started" || cascade[1]["subject_id"] != stage.ID ||
		cascade[2]["kind"] != "step.started" || cascade[2]["subject_id"] != step.ID {
		t.Fatalf("installed nested start cascade = %#v; want Matter→Stage→Step", cascade)
	}
	for index, event := range cascade {
		payload, ok := event["payload"].(map[string]any)
		if !ok || index < 2 && payload["cascade"] != true || index == 2 && payload["cascade"] != nil ||
			index > 0 && payload["cause_event_id"] != cascade[index-1]["event_id"] {
			t.Fatalf("cascade event %d payload/cause = %#v", index, event["payload"])
		}
	}
	for _, terminal := range []struct {
		id string
	}{{m5TwoEnvironmentID(47)}, {m5TwoEnvironmentID(48)}, {m5TwoEnvironmentID(39)}, {m5TwoEnvironmentID(40)},
		{m5TwoEnvironmentID(49)}, {m5TwoEnvironmentID(50)}} {
		receipt, found := installed.Receipts[terminal.id]
		if !found || receipt.ResultCode != operation.ResultSucceeded || len(receipt.CanonicalReceipt) == 0 {
			t.Fatalf("installed terminal receipt for %s = %+v (found=%v)", terminal.id, receipt, found)
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(receipt.CanonicalReceipt,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		requestHash, hashErr := commands[terminal.id].RequestHash()
		if decodeErr != nil || hashErr != nil || fields["command_id"] != terminal.id || fields["request_hash"] != requestHash {
			t.Fatalf("installed receipt identity for %s: %#v decode=%v hash=%v", terminal.id, fields, decodeErr, hashErr)
		}
		if terminal.id == m5TwoEnvironmentID(47) {
			result, resultOK := fields["result"].(map[string]any)
			operationFields, operationOK := fields["operation"].(map[string]any)
			outputBytes, outputBytesOK := result["output"].([]byte)
			output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "matter_id", "state")
			if !resultOK || !operationOK || operationFields["name"] != "matter.start" || operationFields["version"] != uint64(1) ||
				result["code"] != string(operation.ResultSucceeded) || !outputBytesOK || outputErr != nil ||
				output["matter_id"] != secondMatter.ID || output["state"] != "in-progress" {
				t.Fatalf("installed standalone Matter start receipt operation/result = %#v", fields)
			}
		}
		if terminal.id == m5TwoEnvironmentID(48) {
			result, resultOK := fields["result"].(map[string]any)
			outputBytes, outputBytesOK := result["output"].([]byte)
			output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "matter_id", "state")
			if !resultOK || result["code"] != string(operation.ResultSucceeded) || !outputBytesOK || outputErr != nil ||
				output["matter_id"] != secondMatter.ID || output["state"] != "canceled" {
				t.Fatalf("installed Matter cancel receipt result = %#v", fields)
			}
		}
		if terminal.id == m5TwoEnvironmentID(50) {
			result, resultOK := fields["result"].(map[string]any)
			outputBytes, outputBytesOK := result["output"].([]byte)
			output, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "step_id", "matter_id", "state")
			if !resultOK || result["code"] != string(operation.ResultSucceeded) || !outputBytesOK || outputErr != nil ||
				output["step_id"] != step.ID || output["matter_id"] != matter.ID || output["state"] != "canceled" {
				t.Fatalf("installed Step cancel receipt result = %#v", fields)
			}
		}
		accepted, acceptedOK := fields["accepted_events"].(map[string]any)
		wantCount := uint64(1)
		if terminal.id == m5TwoEnvironmentID(39) {
			wantCount = 3
		}
		if !acceptedOK || accepted["event_count"] != wantCount || accepted["first_event_id"] == nil || accepted["last_event_id"] == nil {
			t.Fatalf("installed accepted event range for %s = %#v; want one exact range of %d events", terminal.id, fields["accepted_events"], wantCount)
		}
		if terminal.id == m5TwoEnvironmentID(39) &&
			(accepted["first_event_id"] != cascade[0]["event_id"] || accepted["last_event_id"] != cascade[2]["event_id"]) {
			t.Fatalf("nested cascade receipt range = %#v; cascade=%#v", accepted, cascade)
		}
	}

	// Independently compare the installed prefix with the authority's immutable
	// prefix and verify the newly submitted transition is durably terminal.
	anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || anchor.EventCount != installed.Anchor.EventCount || anchor.Digest != installed.Anchor.Digest {
		t.Fatalf("authority/client installed prefix mismatch: authority=%+v installed=%+v err=%v", anchor, installed.Anchor, err)
	}
}

type m6AuthorityHTTPTrace struct {
	mu      sync.Mutex
	entries []string
}

func (trace *m6AuthorityHTTPTrace) add(entry string) {
	trace.mu.Lock()
	trace.entries = append(trace.entries, entry)
	trace.mu.Unlock()
}

func (trace *m6AuthorityHTTPTrace) snapshot() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.entries...)
}

func traceContains(entries []string, want string) bool {
	for _, entry := range entries {
		if strings.Contains(entry, want) {
			return true
		}
	}
	return false
}

type m6TraceResponseWriter struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
}

func (writer *m6TraceResponseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *m6TraceResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	_, _ = writer.body.Write(data)
	return writer.ResponseWriter.Write(data)
}

func (writer *m6TraceResponseWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *m6TraceResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func traceM6AuthorityHTTP(next http.Handler, trace *m6AuthorityHTTPTrace) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var requestBody bytes.Buffer
		request.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.TeeReader(request.Body, &requestBody), Closer: request.Body}
		observed := &m6TraceResponseWriter{ResponseWriter: response}
		next.ServeHTTP(observed, request)
		requestFrames, _ := wipdwire.ReadFrames(requestBody.Bytes(), 8)
		responseFrames, _ := wipdwire.ReadFrames(observed.body.Bytes(), 8)
		trace.add(fmt.Sprintf("%s request=%s response=%s status=%d", request.URL.Path,
			m6FrameKinds(requestFrames), m6FrameKinds(responseFrames), observed.status))
	})
}

func m6FrameKinds(frames []wipdwire.Frame) string {
	kinds := make([]string, len(frames))
	for index, frame := range frames {
		kinds[index] = frame.Kind
		if frame.Kind == "problem" {
			fields, err := wipdwire.DecodeCanonicalMap(frame.Payload, "schema", "code", "message")
			if err == nil {
				kinds[index] += ":" + fmt.Sprint(fields["code"])
			}
		}
	}
	return strings.Join(kinds, ",")
}

func mustLoadM6TestConfig(t *testing.T, profileRoot string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(profileRoot, "connected-authority.json"))
	if err != nil {
		return fmt.Sprintf("config read error: %v", err)
	}
	var config struct {
		CommandCatalogue string `json:"command_catalogue"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return fmt.Sprintf("config decode error: %v", err)
	}
	return config.CommandCatalogue
}
