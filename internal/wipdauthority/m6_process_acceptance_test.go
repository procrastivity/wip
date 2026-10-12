package wipdauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipd"
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
	defer func(journal *wipdjournal.Journal) { _ = journal.Close() }(journal)
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
	defer func(journal *wipdjournal.Journal) { _ = journal.Close() }(journal)
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
	}{
		{m5TwoEnvironmentID(47)},
		{m5TwoEnvironmentID(48)},
		{m5TwoEnvironmentID(39)},
		{m5TwoEnvironmentID(40)},
		{m5TwoEnvironmentID(49)},
		{m5TwoEnvironmentID(50)},
	} {
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

func TestM6Step9ANamedBatchBirthThroughAuthenticatedProcessAndEnvironmentReopen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated AF_UNIX IPC is Linux-only")
	}
	ctx := context.Background()
	root, err := os.MkdirTemp("/tmp", "s9a-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("WIP_DB_PATH", filepath.Join(root, "legacy", "wip.db"))

	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step9ARegistry()
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
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
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

	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step9a")
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = wipd.ConnectM6Step9A(ctx, environment.profileRoot)
	if err != nil {
		stopDaemon()
		t.Fatalf("connect with explicit Step 9-A capability over authenticated AF_UNIX IPC: %v; daemon=%s", err, daemonOutput.String())
	}
	t.Cleanup(func() {
		_ = client.Close()
		stopDaemon()
	})

	command := operation.Command{
		ID: m5TwoEnvironmentID(82), AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 1, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: m5TwoEnvironmentID(82),
		Request: operation.Request{
			Operation: operation.BatchCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchCreateInput{Name: "M6 release train"},
		},
	}
	commandHash := m5CommandHash(t, command)
	result, err := client.ExecuteCommand(ctx, command)
	batch, ok := result.Output.(operation.BatchCreateOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !ok || batch.ID == "" || batch.Name != "M6 release train" {
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, command.ID, commandHash, 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		_ = client.Close()
		stopDaemon()
		identity := wipdjournal.Identity{
			RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
		}
		localJournal, openErr := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), identity)
		var localState string
		if openErr == nil {
			localSnapshot, snapshotErr := localJournal.InstallSnapshot(ctx)
			entries, entriesErr := localJournal.Entries()
			localState = fmt.Sprintf("snapshot(err=%v events=%d receipts=%d named_batches=%d) entries(err=%v count=%d)",
				snapshotErr, localSnapshot.Anchor.EventCount, len(localSnapshot.Receipts), len(localSnapshot.NamedBatches), entriesErr, len(entries))
			if closeErr := localJournal.Close(); closeErr != nil {
				localState += fmt.Sprintf(" close=%v", closeErr)
			}
		}
		t.Fatalf("named Batch create through authenticated process = %+v (%T), %v; authorityReceipt=%t authorityErr=%v localJournal(open=%v %s) trace=%v daemon=%s",
			result, result.Output, err, len(status.Receipt) > 0, statusErr, openErr, localState, trace.snapshot(), daemonOutput.String())
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") {
		t.Fatalf("named Batch did not complete through authority command submission and terminal response: %v", trace.snapshot())
	}

	authorityReceipt, err := fixture.store.QueryCommand(ctx, m5TestDomain, command.ID, commandHash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || authorityReceipt.Pending || len(authorityReceipt.Receipt) == 0 || len(authorityReceipt.SignedReceipt) == 0 {
		t.Fatalf("named Batch authority receipt = %+v, %v", authorityReceipt, err)
	}
	receiptFields, err := wipdwire.DecodeCanonicalMap(authorityReceipt.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	operationFields, operationOK := receiptFields["operation"].(map[string]any)
	environmentFields, environmentOK := receiptFields["environment"].(map[string]any)
	resultFields, resultOK := receiptFields["result"].(map[string]any)
	acceptedEvents, acceptedOK := receiptFields["accepted_events"].(map[string]any)
	if err != nil || receiptFields["schema"] != "wipd.terminal-receipt/1" || receiptFields["domain_id"] != m5TestDomain ||
		receiptFields["command_id"] != command.ID || receiptFields["request_hash"] != commandHash ||
		!operationOK || operationFields["name"] != "batch.create" || operationFields["version"] != uint64(1) ||
		!environmentOK || environmentFields["id"] != m5TestEnv || environmentFields["sequence"] != uint64(1) ||
		!resultOK || resultFields["code"] != string(operation.ResultSucceeded) ||
		!acceptedOK || acceptedEvents["event_count"] != uint64(1) {
		t.Fatalf("authority receipt does not bind the exact named-Batch command: fields=%#v err=%v", receiptFields, err)
	}
	outputBytes, outputOK := resultFields["output"].([]byte)
	outputFields, outputErr := wipdwire.DecodeCanonicalMap(outputBytes, "id", "name")
	if !outputOK || outputErr != nil || outputFields["id"] != batch.ID || outputFields["name"] != batch.Name {
		t.Fatalf("authority receipt typed Batch output = %#v, decode error=%v", outputFields, outputErr)
	}

	anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || anchor.EventCount != 1 || anchor.EventID == "" {
		t.Fatalf("named Batch authority prefix = %+v, %v; want one birth event", anchor, err)
	}
	authoritySnapshot, err := fixture.store.PinSnapshot(ctx, m5TestDomain, 1, authoritystore.EmptyPrefixAnchor(),
		m5TwoEnvironmentID(83), time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("pin exact named-Batch authority event: %v", err)
	}
	if len(authoritySnapshot.Delta.Events) != 1 {
		t.Fatalf("named-Batch authority snapshot has %d events, want exactly one", len(authoritySnapshot.Delta.Events))
	}
	event := authoritySnapshot.Delta.Events[0]
	eventFields, err := wipdwire.DecodeCanonicalMap(event.Record,
		"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
	eventEnvironment, eventEnvironmentOK := eventFields["environment"].(map[string]any)
	eventPayload, eventPayloadOK := eventFields["payload"].(map[string]any)
	if err != nil || event.EventID != anchor.EventID || eventFields["event_id"] != event.EventID ||
		eventFields["schema"] != "wipd.event/1" || eventFields["domain_id"] != m5TestDomain ||
		eventFields["command_id"] != command.ID || eventFields["request_hash"] != commandHash ||
		eventFields["kind"] != "batch.created" || eventFields["subject_id"] != batch.ID || eventFields["repo_id"] != nil ||
		!eventEnvironmentOK || eventEnvironment["id"] != m5TestEnv || eventEnvironment["sequence"] != uint64(1) ||
		!eventPayloadOK || eventPayload["name"] != batch.Name ||
		acceptedEvents["first_event_id"] != event.EventID || acceptedEvents["last_event_id"] != event.EventID {
		t.Fatalf("authority event/receipt do not bind the exact null-Repo Batch birth: event=%#v receipt=%#v err=%v", eventFields, acceptedEvents, err)
	}

	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	identity := wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	}
	journalPath := filepath.Join(environment.profileRoot, "environment-journal")
	journal, err := wipdjournal.Open(journalPath, identity)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(ctx)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	wantProjection := wipdjournal.NamedBatchProjection{
		DomainID: m5TestDomain, BatchID: batch.ID, Name: batch.Name, BirthEventID: event.EventID,
	}
	installedReceipt, receiptOK := installed.Receipts[command.ID]
	if installed.Identity != identity || installed.Anchor.EventCount != 1 || installed.Anchor.EventID == nil || *installed.Anchor.EventID != event.EventID ||
		len(installed.NamedBatches) != 1 || installed.NamedBatches[0] != wantProjection ||
		len(installed.Receipts) != 1 || !receiptOK || installedReceipt.RequestHash != commandHash ||
		installedReceipt.EnvironmentSeq != 1 || installedReceipt.ResultCode != operation.ResultSucceeded ||
		!bytes.Equal(installedReceipt.CanonicalReceipt, authorityReceipt.Receipt) {
		_ = journal.Close()
		t.Fatalf("Environment install does not exactly match named-Batch authority result: snapshot=%+v receipt=%+v", installed, installedReceipt)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = wipdjournal.Open(journalPath, identity)
	if err != nil {
		t.Fatalf("reopen installed Environment journal: %v", err)
	}
	reopened, err := journal.InstallSnapshot(ctx)
	closeErr := journal.Close()
	if err != nil || closeErr != nil || reopened.Identity != identity || reopened.Anchor.EventCount != 1 ||
		reopened.Anchor.EventID == nil || *reopened.Anchor.EventID != event.EventID || len(reopened.NamedBatches) != 1 ||
		reopened.NamedBatches[0] != wantProjection || len(reopened.Receipts) != 1 ||
		reopened.Receipts[command.ID].RequestHash != commandHash ||
		!bytes.Equal(reopened.Receipts[command.ID].CanonicalReceipt, authorityReceipt.Receipt) {
		t.Fatalf("Environment named-Batch projection/receipt changed after journal reopen: snapshot=%+v err=%v close=%v", reopened, err, closeErr)
	}
}

func TestM6Step9BNamedBatchMembershipThroughAuthenticatedProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated AF_UNIX IPC is Linux-only")
	}
	ctx := context.Background()
	root, err := os.MkdirTemp("/tmp", "s9b-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("WIP_DB_PATH", filepath.Join(root, "legacy", "wip.db"))

	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step9BRegistry()
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
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case serveErr := <-serveDone:
			if serveErr != nil {
				t.Errorf("M6 S9-B authority server stopped with error: %v", serveErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("M6 S9-B authority server did not stop")
		}
	})

	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step9b")
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = wipd.ConnectM6Step9B(ctx, environment.profileRoot)
	if err != nil {
		stopDaemon()
		t.Fatalf("connect with explicit Step 9-B capability over authenticated AF_UNIX IPC: %v; daemon=%s", err, daemonOutput.String())
	}
	t.Cleanup(func() {
		_ = client.Close()
		stopDaemon()
	})

	sequence := uint64(1)
	command := func(idNumber int, request operation.Request) operation.Command {
		id := m5TwoEnvironmentID(idNumber)
		return operation.Command{
			ID: id, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
			EnvironmentID: m5TestEnv, EnvironmentSequence: sequence, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationCommandID: id, Request: request,
		}
	}
	submit := func(idNumber int, request operation.Request) (operation.Command, operation.Result) {
		t.Helper()
		item := command(idNumber, request)
		sequence++
		result, submitErr := client.ExecuteCommand(ctx, item)
		if submitErr != nil {
			hash, _ := item.RequestHash()
			status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, item.ID, hash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
			anchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			t.Fatalf("%s through authenticated Step 9-B process = %+v, %v; authorityStatus=%+v statusErr=%v anchor=%+v anchorErr=%v trace=%v daemon=%s",
				item.Request.Operation, result, submitErr, status, statusErr, anchor, anchorErr, trace.snapshot(), daemonOutput.String())
		}
		return item, result
	}
	createBatch := func(idNumber int, name string) (operation.Command, string) {
		t.Helper()
		item, result := submit(idNumber, operation.Request{
			Operation: operation.BatchCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.BatchCreateInput{Name: name},
		})
		created, ok := result.Output.(operation.BatchCreateOutput)
		if result.Code != operation.ResultSucceeded || !ok || created.ID == "" {
			t.Fatalf("create Batch %q through authenticated process = %+v", name, result)
		}
		return item, created.ID
	}
	createMatter := func(idNumber int, locator string) string {
		t.Helper()
		_, result := submit(idNumber, operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.MatterCreateInput{Title: locator, Locator: locator},
		})
		created, ok := result.Output.(operation.MatterCreateOutput)
		if result.Code != operation.ResultSucceeded || !ok || created.ID == "" {
			t.Fatalf("create Matter %q through authenticated process = %+v", locator, result)
		}
		return created.ID
	}
	join := func(idNumber int, batchID, matterID string) (operation.Command, operation.Result) {
		return submit(idNumber, operation.Request{
			Operation: operation.BatchJoinV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID},
		})
	}
	leave := func(idNumber int, batchID, matterID string) (operation.Command, operation.Result) {
		return submit(idNumber, operation.Request{
			Operation: operation.BatchLeaveV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
			Input: operation.BatchMembershipInput{BatchID: batchID, MatterID: matterID},
		})
	}
	statusFor := func(item operation.Command) authoritystore.CommandStatus {
		t.Helper()
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, item.ID, m5CommandHash(t, item), 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		if statusErr != nil || status.Pending || len(status.Receipt) == 0 || len(status.SignedReceipt) == 0 {
			t.Fatalf("authority terminal receipt for %s = %+v, %v", item.ID, status, statusErr)
		}
		return status
	}
	snapshotID := 50
	assertActive := func(want map[string]bool) {
		t.Helper()
		anchor, snapshotErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if snapshotErr != nil {
			t.Fatal(snapshotErr)
		}
		snapshot, snapshotErr := fixture.store.PinSnapshot(ctx, m5TestDomain, 1, authoritystore.EmptyPrefixAnchor(),
			m5TwoEnvironmentID(snapshotID), time.Now().UTC(), time.Minute)
		snapshotID++
		if snapshotErr != nil || snapshot.Delta.End.EventCount != anchor.EventCount {
			t.Fatalf("pin S9-B event prefix: snapshot=%+v err=%v anchor=%+v", snapshot, snapshotErr, anchor)
		}
		got := make(map[string]bool)
		for _, event := range snapshot.Delta.Events {
			fields, decodeErr := wipdwire.DecodeCanonicalMap(event.Record,
				"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
			if decodeErr != nil {
				t.Fatalf("decode S9-B event %s: %v", event.EventID, decodeErr)
			}
			kind, _ := fields["kind"].(string)
			if kind != "batch.joined" && kind != "batch.left" && kind != "batch.dismissed" {
				continue
			}
			batchID, _ := fields["subject_id"].(string)
			payload, payloadOK := fields["payload"].(map[string]any)
			if fields["repo_id"] != nil || batchID == "" || !payloadOK {
				t.Fatalf("S9-B event has wrong Batch-subject/null-Repo dimensions: %#v", fields)
			}
			if kind == "batch.dismissed" {
				if !wipdwire.ExactMapKeys(payload) {
					t.Fatalf("Batch dismissal payload is not empty: %#v", payload)
				}
				continue
			}
			matterID, _ := payload["matter_id"].(string)
			if !wipdwire.ExactMapKeys(payload, "matter_id") || matterID == "" {
				t.Fatalf("Batch membership event does not identify one exact Matter: %#v", fields)
			}
			pair := batchID + "\x00" + matterID
			if kind == "batch.joined" {
				if got[pair] {
					t.Fatalf("duplicate membership event for pair %q", pair)
				}
				got[pair] = true
			} else {
				if !got[pair] {
					t.Fatalf("leave event has no exact prior pair %q", pair)
				}
				delete(got, pair)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("event-folded named Batch membership pairs=%v; want %v", got, want)
		}
	}

	_, batchA := createBatch(70, "S9-B A")
	_, batchB := createBatch(71, "S9-B B")
	matterA := createMatter(72, "s9b-matter-a")
	matterB := createMatter(73, "s9b-matter-b")
	firstJoinCommand, result := join(74, batchA, matterA)
	if result.Code != operation.ResultSucceeded || result.Output != (operation.BatchMembershipOutput{BatchID: batchA, MatterID: matterA}) {
		t.Fatalf("join asymmetric pair A/A = %+v", result)
	}
	firstJoinStatus := statusFor(firstJoinCommand)
	beforeReplay, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	replayedResult, replayErr := client.ExecuteCommand(ctx, firstJoinCommand)
	replayedStatus := statusFor(firstJoinCommand)
	afterReplay, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if replayErr != nil || replayedResult.Code != result.Code || replayedResult.Output != result.Output || anchorErr != nil || afterReplay != beforeReplay ||
		!bytes.Equal(replayedStatus.Receipt, firstJoinStatus.Receipt) || !bytes.Equal(replayedStatus.SignedReceipt, firstJoinStatus.SignedReceipt) {
		t.Fatalf("same-command authenticated replay changed the result, receipt, or event prefix: result=%+v replay=%+v status=%+v replayStatus=%+v anchor=%+v/%v err=%v",
			result, replayedResult, firstJoinStatus, replayedStatus, afterReplay, anchorErr, replayErr)
	}
	_, result = join(75, batchB, matterB)
	if result.Code != operation.ResultSucceeded || result.Output != (operation.BatchMembershipOutput{BatchID: batchB, MatterID: matterB}) {
		t.Fatalf("join asymmetric pair B/B = %+v", result)
	}
	assertActive(map[string]bool{batchA + "\x00" + matterA: true, batchB + "\x00" + matterB: true})

	beforeDuplicate, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	duplicateCommand, duplicateResult := join(76, batchA, matterA)
	if duplicateResult.Code != operation.ResultSucceeded || duplicateResult.Output != (operation.BatchMembershipOutput{BatchID: batchA, MatterID: matterA}) {
		t.Fatalf("fresh duplicate join = %+v", duplicateResult)
	}
	duplicateStatus := statusFor(duplicateCommand)
	duplicateReceipt, err := wipdwire.DecodeCanonicalMap(duplicateStatus.Receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	duplicateEvents := duplicateReceipt["accepted_events"]
	if err != nil || duplicateEvents != nil {
		t.Fatalf("fresh duplicate join receipt unexpectedly has event effects: fields=%#v err=%v", duplicateReceipt, err)
	}
	afterDuplicate, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterDuplicate != beforeDuplicate {
		t.Fatalf("duplicate join changed authority event prefix: before=%+v after=%+v err=%v", beforeDuplicate, afterDuplicate, err)
	}

	assertActive(map[string]bool{batchA + "\x00" + matterA: true, batchB + "\x00" + matterB: true})

	_, result = leave(78, batchA, matterA)
	if result.Code != operation.ResultSucceeded || result.Output != (operation.BatchMembershipOutput{BatchID: batchA, MatterID: matterA}) {
		t.Fatalf("leave exact pair A/A = %+v", result)
	}
	assertActive(map[string]bool{batchB + "\x00" + matterB: true})
	_, result = join(79, batchA, matterA)
	if result.Code != operation.ResultSucceeded {
		t.Fatalf("rejoin exact pair after leave = %+v", result)
	}
	_, dismissed := submit(80, operation.Request{
		Operation: operation.BatchDismissV1.Metadata().Operation, Actor: "human", Context: operation.Context{Repo: m5TestRepo},
		Input: operation.BatchDismissInput{BatchID: batchA},
	})
	if dismissed.Code != operation.ResultSucceeded || dismissed.Output != (operation.BatchDismissOutput{BatchID: batchA}) {
		t.Fatalf("explicit dismissal through authenticated process = %+v", dismissed)
	}
	beforeClosedJoin, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	closedJoin, result := join(81, batchA, matterA)
	if result.Code != operation.ResultRefused || result.Problem == nil || result.Problem.Code != operation.ProblemBatchDismissed {
		t.Fatalf("join of explicitly dismissed Batch = %+v", result)
	}
	statusFor(closedJoin)
	afterClosedJoin, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterClosedJoin != beforeClosedJoin {
		t.Fatalf("dismissed Batch join changed authority event prefix: before=%+v after=%+v err=%v", beforeClosedJoin, afterClosedJoin, err)
	}
	if !traceContains(trace.snapshot(), "/wipd/v1/exchange request=command.submit response=submission.accepted,command.terminal") {
		t.Fatalf("S9-B operations did not traverse authenticated authority submit and terminal response: %v", trace.snapshot())
	}
}

func TestM6Step9CNamedBatchReadThroughAuthenticatedProcessMatchesStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated AF_UNIX IPC is Linux-only")
	}
	ctx := context.Background()
	root, err := os.MkdirTemp("/tmp", "s9c-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	t.Setenv("WIP_DB_PATH", filepath.Join(root, "legacy", "wip.db"))

	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step9CRegistry()
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
	var trace m6AuthorityHTTPTrace
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
	authorityCtx, stopAuthority := context.WithCancel(ctx)
	serveDone := make(chan error, 1)
	go func() { serveDone <- fixture.server.Serve(authorityCtx, fixture.listener) }()
	t.Cleanup(func() {
		stopAuthority()
		select {
		case serveErr := <-serveDone:
			if serveErr != nil {
				t.Errorf("M6 S9-C authority server stopped with error: %v", serveErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("M6 S9-C authority server did not stop")
		}
	})

	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step9c")
	if err != nil {
		t.Fatal(err)
	}
	binary := m5WipdBinary(t)
	client, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = wipd.ConnectM6Step9C(ctx, environment.profileRoot)
	if err != nil {
		stopDaemon()
		t.Fatalf("connect with explicit Step 9-C capability over authenticated AF_UNIX IPC: %v; daemon=%s", err, daemonOutput.String())
	}
	t.Cleanup(func() { _ = client.Close(); stopDaemon() })

	commandID := m5TwoEnvironmentID(90)
	command := operation.Command{
		ID: commandID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 1, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: commandID,
		Request: operation.Request{
			Operation: operation.BatchCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchCreateInput{Name: "S9-C process read"},
		},
	}
	result, err := client.ExecuteCommand(ctx, command)
	created, ok := result.Output.(operation.BatchCreateOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !ok || created.ID == "" {
		t.Fatalf("create named Batch through authenticated Step 9-C process: result=%+v err=%v daemon=%s", result, err, daemonOutput.String())
	}
	createMatterID := m5TwoEnvironmentID(91)
	createMatter := operation.Command{
		ID: createMatterID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 2, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: createMatterID,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.MatterCreateInput{Title: "S9-C first member", Locator: "s9c-member-b"},
		},
	}
	result, err = client.ExecuteCommand(ctx, createMatter)
	matterB, matterOK := result.Output.(operation.MatterCreateOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !matterOK || matterB.ID == "" {
		t.Fatalf("create Matter for named-Batch read: result=%+v err=%v", result, err)
	}
	createMatterAID := m5TwoEnvironmentID(96)
	createMatterA := operation.Command{
		ID: createMatterAID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 3, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: createMatterAID,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.MatterCreateInput{Title: "S9-C member in first Repo", Locator: "s9c-member-a"},
		},
	}
	result, err = client.ExecuteCommand(ctx, createMatterA)
	matterA, matterAOK := result.Output.(operation.MatterCreateOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !matterAOK || matterA.ID == "" {
		t.Fatalf("create second Matter for named-Batch read: result=%+v err=%v", result, err)
	}
	createMatterCID := m5TwoEnvironmentID(98)
	createMatterC := operation.Command{
		ID: createMatterCID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 4, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: createMatterCID,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.MatterCreateInput{Title: "S9-C third member", Locator: "s9c-member-c"},
		},
	}
	result, err = client.ExecuteCommand(ctx, createMatterC)
	matterC, matterCOK := result.Output.(operation.MatterCreateOutput)
	if err != nil || result.Code != operation.ResultSucceeded || !matterCOK || matterC.ID == "" {
		t.Fatalf("create third Matter for named-Batch read: result=%+v err=%v", result, err)
	}
	joinID := m5TwoEnvironmentID(92)
	join := operation.Command{
		ID: joinID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 5, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: joinID,
		Request: operation.Request{
			Operation: operation.BatchJoinV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchMembershipInput{BatchID: created.ID, MatterID: matterB.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, join)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("join named-Batch member: result=%+v err=%v", result, err)
	}
	joinAID := m5TwoEnvironmentID(97)
	joinA := operation.Command{
		ID: joinAID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 6, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: joinAID,
		Request: operation.Request{
			Operation: operation.BatchJoinV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchMembershipInput{BatchID: created.ID, MatterID: matterA.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, joinA)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("join second named-Batch member: result=%+v err=%v", result, err)
	}
	joinCID := m5TwoEnvironmentID(99)
	joinC := operation.Command{
		ID: joinCID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 7, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: joinCID,
		Request: operation.Request{
			Operation: operation.BatchJoinV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchMembershipInput{BatchID: created.ID, MatterID: matterC.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, joinC)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("join third named-Batch member: result=%+v err=%v", result, err)
	}
	before, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ReadNamedBatch(ctx, created.ID, 2, nil)
	if err != nil {
		t.Fatalf("read named Batch through authenticated Step 9-C IPC: %v; authority trace=%v", err, trace.snapshot())
	}
	direct, err := fixture.store.ReadNamedBatch(ctx, m5TestDomain, 1, created.ID, 2, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || len(direct.Items) != 2 || response.Snapshot.AsOf.EventCount != direct.AsOf.EventCount ||
		response.Snapshot.AsOf.Digest != direct.AsOf.Digest || response.Complete != direct.Complete ||
		response.NextPageToken == nil || response.Provenance.Source != "authority" || response.FilterHash != direct.FilterHash ||
		!bytes.Equal(response.Items[0].Value, direct.Items[0].Value) || !bytes.Equal(response.Items[1].Value, direct.Items[1].Value) {
		t.Fatalf("authenticated/direct named-Batch page mismatch: ipc=%+v direct=%+v", response, direct)
	}
	encodedResponse, err := wipdwire.EncodeCanonical(response)
	if err != nil {
		t.Fatal(err)
	}
	responseFields, err := wipdwire.DecodeCanonicalMap(encodedResponse,
		"schema", "query", "filter_hash", "snapshot", "provenance", "items", "next_page_token", "complete")
	queryFields, queryOK := responseFields["query"].(map[string]any)
	if err != nil || !queryOK || !wipdwire.ExactMapKeys(queryFields, "name", "version") ||
		responseFields["filter_hash"] != response.FilterHash || response.FilterHash != direct.FilterHash {
		t.Fatalf("authenticated M2 response query is not closed or filter is not represented by filter_hash: query=%v fields=%v err=%v",
			queryFields, responseFields, err)
	}
	var header struct {
		Kind    string `cbor:"kind"`
		BatchID string `cbor:"batch_id"`
	}
	if err = cbor.Unmarshal(response.Items[0].Value, &header); err != nil || header.Kind != "batch" || header.BatchID != created.ID {
		t.Fatalf("authenticated page has incorrect closed header: %+v err=%v", header, err)
	}
	fixedSizeFirst, err := client.ReadNamedBatch(ctx, created.ID, 1, nil)
	if err != nil || len(fixedSizeFirst.Items) != 1 || fixedSizeFirst.Complete || fixedSizeFirst.NextPageToken == nil {
		t.Fatalf("fixed-size page-one control: page=%+v err=%v", fixedSizeFirst, err)
	}
	fixedSizeToken := *fixedSizeFirst.NextPageToken
	fixedSizeNext, err := client.ReadNamedBatch(ctx, created.ID, 1, &fixedSizeToken)
	if err != nil || len(fixedSizeNext.Items) != 1 || fixedSizeNext.Complete || fixedSizeNext.NextPageToken == nil {
		t.Fatalf("fixed-size continuation control: page=%+v err=%v", fixedSizeNext, err)
	}
	fixedSizeDirect, err := fixture.store.ReadNamedBatch(ctx, m5TestDomain, 1, created.ID, 1, fixedSizeToken, time.Now().UTC())
	if err != nil || len(fixedSizeDirect.Items) != 1 || fixedSizeDirect.Complete ||
		!bytes.Equal(fixedSizeNext.Items[0].Value, fixedSizeDirect.Items[0].Value) {
		t.Fatalf("fixed-size authenticated/direct continuation control: ipc=%+v direct=%+v err=%v", fixedSizeNext, fixedSizeDirect, err)
	}
	pageToken := *response.NextPageToken
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	identity := wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	}
	journalPath := filepath.Join(environment.profileRoot, "environment-journal")
	projectionJournal, err := wipdjournal.Open(journalPath, identity)
	if err != nil {
		t.Fatalf("open installed Environment journal at joined prefix: %v", err)
	}
	joinedProjection, projectionErr := projectionJournal.InstallSnapshot(ctx)
	projectionCloseErr := projectionJournal.Close()
	if projectionErr != nil || projectionCloseErr != nil || len(joinedProjection.NamedBatches) != 1 ||
		len(joinedProjection.NamedBatchMemberships) != 3 || joinedProjection.NamedBatchMemberships[0].BatchID != created.ID ||
		joinedProjection.NamedBatchMemberships[0].MatterID != matterB.ID || joinedProjection.NamedBatchMemberships[0].RepoID != m5TestRepo ||
		joinedProjection.NamedBatchMemberships[1].BatchID != created.ID || joinedProjection.NamedBatchMemberships[1].MatterID == "" ||
		joinedProjection.NamedBatchMemberships[1].MatterID != matterA.ID || joinedProjection.NamedBatchMemberships[1].RepoID != m5TestRepo ||
		joinedProjection.NamedBatchMemberships[2].BatchID != created.ID || joinedProjection.NamedBatchMemberships[2].MatterID != matterC.ID ||
		joinedProjection.NamedBatchMemberships[2].RepoID != m5TestRepo ||
		joinedProjection.NamedBatchMemberships[0].MatterID >= joinedProjection.NamedBatchMemberships[1].MatterID ||
		joinedProjection.NamedBatchMemberships[1].MatterID >= joinedProjection.NamedBatchMemberships[2].MatterID {
		t.Fatalf("incremental pull did not install named-Batch membership projection: %+v install=%v close=%v",
			joinedProjection, projectionErr, projectionCloseErr)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = wipd.ConnectM6Step9C(ctx, environment.profileRoot)
	if err != nil {
		stopDaemon()
		t.Fatalf("reconnect to Step 9-C daemon after membership projection inspection: %v; daemon=%s", err, daemonOutput.String())
	}
	leaveID := m5TwoEnvironmentID(93)
	leave := operation.Command{
		ID: leaveID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 8, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: leaveID,
		Request: operation.Request{
			Operation: operation.BatchLeaveV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchMembershipInput{BatchID: created.ID, MatterID: matterB.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, leave)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("leave named-Batch member after pinning page: result=%+v err=%v", result, err)
	}
	rejoinID := m5TwoEnvironmentID(95)
	rejoin := operation.Command{
		ID: rejoinID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 9, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: rejoinID,
		Request: operation.Request{
			Operation: operation.BatchJoinV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchMembershipInput{BatchID: created.ID, MatterID: matterB.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, rejoin)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("rejoin named-Batch member after pinning page: result=%+v err=%v", result, err)
	}
	dismissID := m5TwoEnvironmentID(94)
	dismiss := operation.Command{
		ID: dismissID, AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 10, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: dismissID,
		Request: operation.Request{
			Operation: operation.BatchDismissV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchDismissInput{BatchID: created.ID},
		},
	}
	result, err = client.ExecuteCommand(ctx, dismiss)
	if err != nil || result.Code != operation.ResultSucceeded {
		t.Fatalf("dismiss named Batch after pinning page: result=%+v err=%v", result, err)
	}
	beforeContinuationRead, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	processContinuation, err := client.ReadNamedBatch(ctx, created.ID, 1, &pageToken)
	if err != nil {
		t.Fatalf("continue named-Batch process snapshot: %v", err)
	}
	directContinuation, err := fixture.store.ReadNamedBatch(ctx, m5TestDomain, 1, created.ID, 1, pageToken, time.Now().UTC())
	if err != nil {
		t.Fatalf("continue direct named-Batch snapshot: %v", err)
	}
	if len(processContinuation.Items) != 2 || len(directContinuation.Items) != 2 ||
		!bytes.Equal(processContinuation.Items[0].Value, directContinuation.Items[0].Value) ||
		!bytes.Equal(processContinuation.Items[1].Value, directContinuation.Items[1].Value) ||
		!processContinuation.Complete || processContinuation.NextPageToken != nil || !directContinuation.Complete ||
		processContinuation.Snapshot.AsOf.EventCount != response.Snapshot.AsOf.EventCount ||
		processContinuation.Snapshot.AsOf.Digest != response.Snapshot.AsOf.Digest || directContinuation.AsOf != before {
		t.Fatalf("decreased-size authenticated/direct continuation mismatch: process=%+v direct=%+v", processContinuation, directContinuation)
	}
	var firstPageMember struct {
		Kind     string `cbor:"kind"`
		BatchID  string `cbor:"batch_id"`
		MatterID string `cbor:"matter_id"`
		RepoID   string `cbor:"repo_id"`
	}
	if cbor.Unmarshal(response.Items[1].Value, &firstPageMember) != nil || firstPageMember.Kind != "membership" ||
		firstPageMember.BatchID != created.ID || firstPageMember.RepoID != m5TestRepo {
		t.Fatalf("initial size-two page did not contain its first sorted membership: %+v", firstPageMember)
	}
	remaining := map[string]bool{matterA.ID: true, matterB.ID: true, matterC.ID: true}
	delete(remaining, firstPageMember.MatterID)
	for index, item := range processContinuation.Items {
		var membership struct {
			Kind     string `cbor:"kind"`
			BatchID  string `cbor:"batch_id"`
			MatterID string `cbor:"matter_id"`
			RepoID   string `cbor:"repo_id"`
		}
		if cbor.Unmarshal(item.Value, &membership) != nil || membership.Kind != "membership" || membership.BatchID != created.ID ||
			membership.RepoID != m5TestRepo || !remaining[membership.MatterID] {
			t.Fatalf("decreased-size continuation item %d is not a remaining sorted membership: %+v", index, membership)
		}
		delete(remaining, membership.MatterID)
		if index > 0 {
			var previous struct {
				MatterID string `cbor:"matter_id"`
			}
			if cbor.Unmarshal(processContinuation.Items[index-1].Value, &previous) != nil || previous.MatterID >= membership.MatterID {
				t.Fatalf("remaining memberships are not ordered: previous=%s current=%s", previous.MatterID, membership.MatterID)
			}
		}
	}
	if len(remaining) != 0 {
		t.Fatalf("decreased-size continuation omitted pinned members: remaining=%v", remaining)
	}
	afterContinuationRead, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterContinuationRead != beforeContinuationRead {
		t.Fatalf("decreased-size continuation changed authority event prefix: before=%+v after=%+v err=%v",
			beforeContinuationRead, afterContinuationRead, err)
	}
	afterMutation, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterMutation.EventCount <= before.EventCount || processContinuation.Snapshot.AsOf.EventCount != before.EventCount ||
		processContinuation.Snapshot.AsOf.Digest != before.Digest || directContinuation.AsOf != before {
		t.Fatalf("continuation did not preserve its pinned prefix through mutations: before=%+v continuation=%+v current=%+v err=%v",
			before, processContinuation.Snapshot.AsOf, afterMutation, err)
	}
	beforeFreshRead, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := client.ReadNamedBatch(ctx, created.ID, 100, nil)
	if err != nil || !fresh.Complete || len(fresh.Items) != 4 {
		t.Fatalf("fresh dismissed named-Batch read: page=%+v err=%v", fresh, err)
	}
	var dismissedHeader struct {
		Kind             string  `cbor:"kind"`
		DismissedEventID *string `cbor:"dismissed_event_id"`
	}
	if err = cbor.Unmarshal(fresh.Items[0].Value, &dismissedHeader); err != nil || dismissedHeader.Kind != "batch" ||
		dismissedHeader.DismissedEventID == nil || *dismissedHeader.DismissedEventID == "" {
		t.Fatalf("fresh named-Batch read omitted explicit dismissal: header=%+v err=%v", dismissedHeader, err)
	}
	afterFreshRead, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterFreshRead != beforeFreshRead {
		t.Fatalf("fresh read mutated authority prefix: before=%+v after=%+v err=%v", beforeFreshRead, afterFreshRead, err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	for reopen := 0; reopen < 2; reopen++ {
		journal, openErr := wipdjournal.Open(journalPath, identity)
		if openErr != nil {
			t.Fatalf("open Environment journal after named-Batch read: %v", openErr)
		}
		installed, installErr := journal.InstallSnapshot(ctx)
		closeErr := journal.Close()
		if installErr != nil || closeErr != nil || installed.Anchor.EventCount != afterMutation.EventCount || installed.Anchor.Digest != afterMutation.Digest ||
			installed.Anchor.EventID == nil || *installed.Anchor.EventID != afterMutation.EventID || len(installed.NamedBatches) != 1 ||
			installed.NamedBatches[0].BatchID != created.ID || len(installed.NamedBatchMemberships) != 3 ||
			installed.NamedBatchMemberships[0].BatchID != created.ID || installed.NamedBatchMemberships[0].MatterID != matterB.ID ||
			installed.NamedBatchMemberships[0].RepoID != m5TestRepo ||
			installed.NamedBatchMemberships[1].BatchID != created.ID || installed.NamedBatchMemberships[1].MatterID != matterA.ID ||
			installed.NamedBatchMemberships[1].RepoID != m5TestRepo ||
			installed.NamedBatchMemberships[0].MatterID >= installed.NamedBatchMemberships[1].MatterID ||
			installed.NamedBatchMemberships[2].BatchID != created.ID || installed.NamedBatchMemberships[2].MatterID != matterC.ID ||
			installed.NamedBatchMemberships[2].RepoID != m5TestRepo ||
			installed.NamedBatchMemberships[1].MatterID >= installed.NamedBatchMemberships[2].MatterID ||
			len(installed.NamedBatchDismissals) != 1 || installed.NamedBatchDismissals[0].BatchID != created.ID ||
			installed.NamedBatchDismissals[0].DismissedEventID == "" {
			t.Fatalf("Environment named-Batch event projection changed across journal reopen %d: %+v install=%v close=%v", reopen, installed, installErr, closeErr)
		}
	}
}

func TestM6Step9AOlderExplicitProfileRefusesWithoutEffectsOrLegacyFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated AF_UNIX IPC is Linux-only")
	}
	ctx := context.Background()
	root, err := os.MkdirTemp("/tmp", "s9a-old-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	legacyPath := filepath.Join(root, "legacy", "wip.db")
	if err = os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacySentinel := []byte("S9-A legacy-store sentinel; unsupported profile must leave it unchanged\n")
	if err = os.WriteFile(legacyPath, legacySentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_DB_PATH", legacyPath)

	fixture := newM5CommandFixture(t)
	registry, err := NewM6Step8Registry()
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
	fixture.server.http.Handler = traceM6AuthorityHTTP(fixture.server.http.Handler, &trace)
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

	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step8")
	if err != nil {
		t.Fatal(err)
	}
	command := operation.Command{
		ID: m5TwoEnvironmentID(84), AuthorityDomainID: m5TestDomain, ExpectedAuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, EnvironmentSequence: 1, ActedAt: time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationCommandID: m5TwoEnvironmentID(84),
		Request: operation.Request{
			Operation: operation.BatchCreateV1.Metadata().Operation, Actor: "human",
			Context: operation.Context{Repo: m5TestRepo}, Input: operation.BatchCreateInput{Name: "unsupported release train"},
		},
	}
	commandHash := m5CommandHash(t, command)
	beforeAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || beforeAnchor.EventCount != 0 {
		t.Fatalf("unexpected authority prestate: anchor=%+v err=%v", beforeAnchor, err)
	}
	binary := m5WipdBinary(t)
	olderClient, stopDaemon, daemonOutput := startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	if err = olderClient.Close(); err != nil {
		t.Fatal(err)
	}
	if got := mustLoadM6TestConfig(t, environment.profileRoot); got != "m6-step8" {
		stopDaemon()
		t.Fatalf("negative fixture is not the intended older explicit profile: %v", got)
	}
	olderClient, err = wipd.ConnectM6Step8(ctx, environment.profileRoot)
	if err != nil {
		stopDaemon()
		t.Fatalf("connect to supported older Step 8 process profile: %v; daemon=%s", err, daemonOutput.String())
	}
	_, executeErr := olderClient.ExecuteCommand(ctx, command)
	if executeErr == nil || !strings.Contains(executeErr.Error(), "operation.unsupported-version") {
		_ = olderClient.Close()
		stopDaemon()
		t.Fatalf("older authenticated profile did not refuse named Batch before submission: %v", executeErr)
	}
	if err = olderClient.Close(); err != nil {
		t.Fatal(err)
	}
	connectCtx, cancelConnect := context.WithTimeout(ctx, time.Second)
	unsupportedClient, connectErr := wipd.ConnectM6Step9A(connectCtx, environment.profileRoot)
	cancelConnect()
	if connectErr == nil {
		_ = unsupportedClient.Close()
		stopDaemon()
		t.Fatal("ConnectM6Step9A accepted an older Step 8 profile")
	}
	if !errors.Is(connectErr, wipd.ErrUnavailable) || !strings.Contains(connectErr.Error(), "named Batch birth is not supported") {
		stopDaemon()
		t.Fatalf("older explicit profile refusal = %v; want fail-closed named-Batch capability refusal", connectErr)
	}
	stopDaemon()

	if traceContains(trace.snapshot(), "request=command.submit") {
		t.Fatalf("older-profile refusal unexpectedly submitted a command to authority: %v", trace.snapshot())
	}
	status, err := fixture.store.QueryCommand(ctx, m5TestDomain, command.ID, commandHash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if !errors.Is(err, authoritystore.ErrNotFound) || status.Pending || len(status.Receipt) != 0 || len(status.SignedReceipt) != 0 {
		t.Fatalf("older-profile refusal left an authority submission/receipt: status=%+v err=%v", status, err)
	}
	afterAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterAnchor != beforeAnchor {
		t.Fatalf("older-profile refusal changed authority event prefix: before=%+v after=%+v err=%v", beforeAnchor, afterAnchor, err)
	}
	journal, err := wipdjournal.Open(filepath.Join(environment.profileRoot, "environment-journal"), wipdjournal.Identity{
		RepoID: m5TestRepo, DomainID: m5TestDomain, AuthorityEpoch: 1,
		EnvironmentID: m5TestEnv, OwnerRootSPKI: fixture.profile.OwnerRootSPKI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(ctx)
	closeErr := journal.Close()
	if err != nil || closeErr != nil || installed.Anchor.EventCount != 0 || len(installed.Receipts) != 0 || len(installed.NamedBatches) != 0 {
		t.Fatalf("older-profile refusal changed Environment install/projection: snapshot=%+v err=%v close=%v", installed, err, closeErr)
	}
	gotSentinel, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(gotSentinel, legacySentinel) {
		t.Fatalf("older-profile refusal mutated the legacy-store sentinel: got=%q err=%v", gotSentinel, err)
	}
}
