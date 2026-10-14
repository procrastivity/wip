package wipdauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/schedulerhook"
	"github.com/procrastivity/wip/internal/wipdwire"
	_ "modernc.org/sqlite"
)

func TestStep10PinnedPlanThroughAuthenticatedEnvironmentAndAuthorityProcesses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("authenticated wipd process acceptance is Linux-only")
	}
	ctx := context.Background()
	policyMode := os.Getenv("WIPD_STEP10_POLICY_TEST")
	step10ProcessPreflight(t, policyMode)
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
	var hookFaults step10HookResultFaults
	fixture.server.http.Handler = interceptStep10HookResultFaults(fixture.server.http.Handler, &hookFaults)
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

	root, err := os.MkdirTemp("/tmp", "w10-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err = os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_RUNTIME_DIR", filepath.Join(root, "runtime"))
	environment, err := prepareM5ProcessEnvironment(t, fixture, root, "environment", m5TestEnv,
		fixture.environment, fixture.clientCert.Certificate[0], fixture.clientCert.Certificate[1], "m6-step10")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(environment.state.PrivateKeyPKCS8)
	planBytes := []byte("# Workplan\n- [ ] First asymmetric task\n- [ ] Second task\n- [x] Already complete\n")
	staged := stageM5ProcessBlob(t, environment, planBytes)
	sourceBytes := []byte("# Introduction\ntext\n## Edge case\n")
	sourceSum := sha256.Sum256(sourceBytes)
	expectedSourceDigest := "sha256:" + hex.EncodeToString(sourceSum[:])
	source := stageM5ProcessBlob(t, environment, sourceBytes)
	failedSource := stageM5ProcessBlob(t, environment, []byte("No headings.\n"))
	if source.Digest != expectedSourceDigest {
		t.Fatalf("staged source digest = %s, independently computed %s", source.Digest, expectedSourceDigest)
	}
	expectedOutlineBytes := []byte(fmt.Sprintf(
		`{"schema":"wipd.markdown-outline/1","source_digest":%q,"headings":[{"level":1,"line":1,"text":"Introduction"},{"level":2,"line":3,"text":"Edge case"}]}`,
		expectedSourceDigest))
	outlineSum := sha256.Sum256(expectedOutlineBytes)
	expectedOutlineDigest := "sha256:" + hex.EncodeToString(outlineSum[:])
	outline := stageM5ProcessBlob(t, environment, expectedOutlineBytes)
	invalidOutlineBytes := []byte(strings.Replace(string(expectedOutlineBytes), "Edge case", "Wrong heading", 1))
	invalidOutline := stageM5ProcessBlob(t, environment, invalidOutlineBytes)
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
		Input: operation.MatterCreateInput{Title: "Step 10 Plan process", Locator: "step-10-plan-process"},
	}))
	created, ok := createdResult.Output.(operation.MatterCreateOutput)
	if err != nil || createdResult.Code != operation.ResultSucceeded || !ok || created.ID == "" {
		t.Fatalf("create Step 10 target Matter: result=%+v error=%v daemon=%s", createdResult, err, daemonOutput.String())
	}
	if _, err = client.ReleaseBirthClaim(ctx, created.ID, m5TwoEnvironmentID(11), operation.Actor("human")); err != nil {
		t.Fatalf("release target birth claim: %v", err)
	}
	cloneID, worktreeID, dispatchID := m5TwoEnvironmentID(12), m5TwoEnvironmentID(13), m5TwoEnvironmentID(14)
	acquired, err := client.AcquireClaim(ctx, m5TwoEnvironmentID(15), created.ID,
		cloneID, worktreeID, dispatchID, operation.Actor("human"))
	if err != nil || acquired.Code != operation.ResultSucceeded || acquired.Grant == nil {
		t.Fatalf("acquire target Matter claim: result=%+v error=%v daemon=%s", acquired, err, daemonOutput.String())
	}
	anchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatalf("read authority prefix after installed claim grant: %v", err)
	}
	claim := acquired.Grant
	initialClaim := *claim
	sequence := uint64(4)
	runID, runLocator := m5TwoEnvironmentID(17), "step-10-plan-process-run"
	foreignRunLock, err := runlock.Acquire(runID)
	if err != nil {
		t.Fatalf("acquire disposable foreign Run liveness lock: %v", err)
	}
	blockedRole := command(m5TwoEnvironmentID(38), sequence, operation.Request{
		Operation: operation.RoleSpawnV1.Metadata().Operation,
		Actor:     operation.Actor("system:scheduler"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Input: operation.RoleLifecycleInput{
			RunID: runID, RunLocator: runLocator, MatterID: created.ID,
			DispatchID: claim.DispatchID, RoleID: m5TwoEnvironmentID(18), Role: "researcher",
		},
	})
	beforeHeldLock, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ExecuteCommand(ctx, blockedRole); err == nil || !strings.Contains(err.Error(), "run.lock-held") {
		t.Fatalf("independently held Run lock was not refused before Role admission: %v", err)
	}
	blockedHash, err := blockedRole.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, blockedRole.ID, blockedHash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("held-lock refusal created an authority submission")
	}
	afterHeldLock, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterHeldLock != beforeHeldLock {
		t.Fatalf("held-lock refusal changed the authority prefix: before=%+v after=%+v error=%v", beforeHeldLock, afterHeldLock, err)
	}
	if err = foreignRunLock.Release(); err != nil {
		t.Fatalf("release disposable foreign Run lock: %v", err)
	}
	spawnRole := func(commandNumber, roleNumber int, role string) {
		t.Helper()
		roleID := m5TwoEnvironmentID(roleNumber)
		roleCommand := command(m5TwoEnvironmentID(commandNumber), sequence, operation.Request{
			Operation: operation.RoleSpawnV1.Metadata().Operation,
			Actor:     operation.Actor("system:scheduler"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
			Input: operation.RoleLifecycleInput{
				RunID: runID, RunLocator: runLocator, MatterID: created.ID,
				DispatchID: claim.DispatchID, RoleID: roleID, Role: role,
			},
		})
		spawned, spawnErr := client.ExecuteCommand(ctx, roleCommand)
		sequence++
		if spawnErr != nil || spawned.Code != operation.ResultSucceeded || spawned.Output != (operation.RoleLifecycleOutput{
			RunID: m5TwoEnvironmentID(17), MatterID: created.ID, DispatchID: claim.DispatchID,
			RoleID: roleID, Role: role, State: "open",
		}) {
			requestHash, hashErr := roleCommand.RequestHash()
			status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, roleCommand.ID, requestHash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
			anchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			domain, domainErr := fixture.store.LookupDomain(ctx, m5TestDomain)
			info, statErr := os.Stat(filepath.Join(fixture.root, "authority.db"))
			t.Fatalf("authority did not spawn exact %s Role: result=%+v error=%v hash_error=%v status=%+v status_error=%v sequence=%d domain=%+v domain_error=%v prefix=%+v prefix_error=%v store_info=%v stat_error=%v daemon=%s",
				role, spawned, spawnErr, hashErr, status, statusErr, roleCommand.EnvironmentSequence,
				domain, domainErr, anchor, anchorErr, info, statErr, daemonOutput.String())
		}
	}
	closeRoleID := func(commandNumber int, roleID, role, reason string) {
		t.Helper()
		actor := operation.Actor("role:" + role)
		if reason == "reaped" {
			actor = "system:scheduler"
		}
		roleCommand := command(m5TwoEnvironmentID(commandNumber), sequence, operation.Request{
			Operation: operation.RoleCloseV1.Metadata().Operation, Actor: actor,
			Context: operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Claim:   &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
			Input: operation.RoleLifecycleInput{
				RunID: runID, RunLocator: runLocator,
				MatterID: created.ID, DispatchID: claim.DispatchID,
				RoleID: roleID, Role: role, Reason: reason,
			},
		})
		closed, closeErr := client.ExecuteCommand(ctx, roleCommand)
		sequence++
		if closeErr != nil || closed.Code != operation.ResultSucceeded || closed.Output != (operation.RoleLifecycleOutput{
			RunID: m5TwoEnvironmentID(17), MatterID: created.ID, DispatchID: claim.DispatchID,
			RoleID: roleID, Role: role, State: "closed",
		}) {
			hash, hashErr := roleCommand.RequestHash()
			status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, roleCommand.ID, hash, 1,
				fixture.peer, m5TestEnv, time.Now().UTC())
			journal, journalErr := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, m5TestEnv,
				claim.ClaimID, claim.ClaimEpoch, created.ID, claim.DispatchID)
			nextSequence, sequenceErr := step10NextEnvironmentSequence(environment.profileRoot)
			authorityDB, _ := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
			var authoritySequence uint64
			if authorityDB != nil {
				_ = authorityDB.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`, m5TestDomain, m5TestEnv).Scan(&authoritySequence)
				_ = authorityDB.Close()
			}
			t.Fatalf("authority did not close exact %s Role (%s): result=%+v error=%v sequence=%d next_sequence=%d local_sequence_error=%v authority_head=%d status=%+v status_error=%v journal=%+v journal_error=%v hash_error=%v daemon=%s",
				role, reason, closed, closeErr, roleCommand.EnvironmentSequence, nextSequence, sequenceErr, authoritySequence, status, statusErr, journal, journalErr, hashErr, daemonOutput.String())
		}
	}
	closeRole := func(commandNumber, roleNumber int, role, reason string) {
		t.Helper()
		closeRoleID(commandNumber, m5TwoEnvironmentID(roleNumber), role, reason)
	}
	resumeAfterLostAsk := func(askCommand operation.Command, handling string, openRoleIDs []string) {
		t.Helper()
		askHash, hashErr := askCommand.RequestHash()
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		hookFaults.dropped.Store(false)
		hookFaults.mode.Store(step10FaultAfterAuthorityCommit)
		if _, askErr := client.ExecuteCommand(ctx, askCommand); askErr == nil || !hookFaults.dropped.Load() {
			t.Fatalf("Ask response-loss injection did not leave an uncertain client result: error=%v dropped=%t", askErr, hookFaults.dropped.Load())
		}
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, askCommand.ID, askHash, 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		if statusErr != nil || status.Pending || len(status.Receipt) == 0 {
			t.Fatalf("lost Ask response did not retain its exact terminal authority receipt: status=%+v error=%v", status, statusErr)
		}
		claimCountBefore := step10ClaimCountForBatch(t, filepath.Join(fixture.root, "authority.db"), initialClaim.BatchID)
		roleSpawnCountBefore := step10RoleSpawnCount(t, filepath.Join(fixture.root, "authority.db"))
		hookCountBefore := step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db"))
		if err = client.Close(); err != nil {
			t.Fatalf("close Environment after committed Ask/lost response: %v", err)
		}
		stopDaemon()
		client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
		resumeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		resumed, resumeErr := client.ResumeSchedulerRun(resumeCtx, runID)
		cancel()
		wantBlocked := "policy-" + map[string]string{"halt": "halt", "skip": "skipped"}[handling]
		if resumeErr != nil || resumed.RunID != runID || resumed.Blocked != wantBlocked || resumed.Claim.ClaimID != "" ||
			resumed.RoleID != "" || resumed.RoleCommandID != "" || resumed.RoleName != "" || resumed.TargetID != "" {
			t.Fatalf("production Resume did not apply retained Ask handling=%s before fresh work: result=%+v error=%v daemon=%s",
				handling, resumed, resumeErr, daemonOutput.String())
		}
		step10AssertAskBeforeRoleClosure(t, filepath.Join(fixture.root, "authority.db"), askCommand.ID, openRoleIDs)
		askReplay, replayErr := client.ExecuteCommand(ctx, askCommand)
		askOutput, outputOK := askReplay.Output.(operation.SchedulerHookOutput)
		if replayErr != nil || askReplay.Code != operation.ResultSucceeded || !outputOK || askOutput.Handling != handling {
			t.Fatalf("exact Ask identity did not recover the original handling after owner Resume: result=%+v error=%v", askReplay, replayErr)
		}
		claimCountAfter := step10ClaimCountForBatch(t, filepath.Join(fixture.root, "authority.db"), initialClaim.BatchID)
		roleSpawnCountAfter := step10RoleSpawnCount(t, filepath.Join(fixture.root, "authority.db"))
		if claimCountAfter != claimCountBefore || roleSpawnCountAfter != roleSpawnCountBefore ||
			step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db")) != hookCountBefore {
			t.Fatalf("halt/skip Resume created fresh authority work: claims=%d→%d Roles=%d→%d hooks=%d→%d",
				claimCountBefore, claimCountAfter, roleSpawnCountBefore, roleSpawnCountAfter, hookCountBefore,
				step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db")))
		}
		unexpectedLock, lockErr := runlock.Acquire(runID)
		if !errors.Is(lockErr, runlock.ErrHeld) {
			if unexpectedLock != nil {
				_ = unexpectedLock.Release()
			}
			t.Fatalf("parked Run did not retain its owner-Environment lock: %v", lockErr)
		}
	}
	spawnRole(40, 18, "researcher")
	spawnRole(43, 36, "orchestrator")
	anchor, err = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatalf("read authority prefix after Run/researcher activation: %v", err)
	}
	openBracketPrefix := anchor
	if _, releaseErr := client.ReleaseClaimJournal(ctx, m5TwoEnvironmentID(45), claim.ClaimID, claim.ClaimEpoch,
		created.ID, claim.DispatchID, operation.Actor("system:scheduler")); releaseErr == nil {
		t.Fatal("normal release sealed a claim while Step 10 Roles were still open")
	}
	afterRefusedRelease, prefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	currentJournal, journalErr := fixture.store.GetCurrentClaimJournal(ctx, m5TestDomain, 1, m5TestEnv,
		claim.ClaimID, claim.ClaimEpoch, created.ID, claim.DispatchID)
	if prefixErr != nil || afterRefusedRelease != openBracketPrefix || journalErr != nil || currentJournal.State != "open" {
		t.Fatalf("open-Role release refusal changed or fenced authority history: before=%+v after=%+v prefix_error=%v journal=%+v journal_error=%v",
			openBracketPrefix, afterRefusedRelease, prefixErr, currentJournal, journalErr)
	}
	commandID := m5TwoEnvironmentID(16)
	prefixEventID := anchor.EventID
	hookCommand := command(commandID, sequence, operation.Request{
		Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
		Actor:     operation.Actor("role:researcher"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Blobs:     []operation.BlobInput{{Name: "workplan", Digest: staged.Digest, Size: staged.Size}},
		Input: operation.SchedulerHookInput{
			Kind: "plan", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
			RunID: m5TwoEnvironmentID(17), MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
			Role: "researcher", RoleID: m5TwoEnvironmentID(18), DispatchID: claim.DispatchID,
			Revision:        "builtin.markdown-checklist-plan@v1",
			TargetClaims:    []operation.TargetClaim{{MatterID: created.ID, ClaimID: claim.ClaimID, ClaimEpoch: claim.ClaimEpoch}},
			InstalledPrefix: operation.SchedulerPrefixAnchor{EventCount: anchor.EventCount, EventID: &prefixEventID, Digest: anchor.Digest},
		},
	})
	for index, mutate := range []func(*operation.SchedulerHookInput){
		func(input *operation.SchedulerHookInput) {
			input.InstalledPrefix.Digest = "sha256:" + strings.Repeat("a", 64)
		},
		func(input *operation.SchedulerHookInput) { input.InstalledPrefix.EventCount++ },
		func(input *operation.SchedulerHookInput) {
			wrong := m5TwoEnvironmentID(90)
			input.InstalledPrefix.EventID = &wrong
		},
	} {
		bad := hookCommand
		bad.ID = m5TwoEnvironmentID(60 + index)
		bad.CorrelationCommandID = bad.ID
		bad.ActedAt = time.Now().UTC().Format(time.RFC3339Nano)
		badInput := bad.Request.Input.(operation.SchedulerHookInput)
		mutate(&badInput)
		bad.Request.Input = badInput
		if _, badErr := client.ExecuteCommand(ctx, bad); badErr == nil {
			t.Fatalf("substituted installed-prefix component %d was admitted", index)
		}
		badHash, hashErr := bad.RequestHash()
		badStatus, queryErr := fixture.store.QueryCommand(ctx, m5TestDomain, bad.ID, badHash, 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		badPrefix, badPrefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		ledger, ledgerErr := step10HookLedgerDB(environment.profileRoot)
		if ledgerErr != nil {
			t.Fatalf("open disposable Step 10 ledger after prefix refusal: %v", ledgerErr)
		}
		var engagements int
		ledgerErr = ledger.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
			m5TestDomain, bad.ID).Scan(&engagements)
		_ = ledger.Close()
		if hashErr != nil || queryErr == nil || badStatus.Pending || len(badStatus.Receipt) != 0 ||
			badPrefixErr != nil || badPrefix != openBracketPrefix || ledgerErr != nil || engagements != 0 {
			t.Fatalf("substituted prefix %d left authority or adapter effects: status=%+v query_error=%v prefix=%+v prefix_error=%v engagements=%d ledger_error=%v hash_error=%v",
				index, badStatus, queryErr, badPrefix, badPrefixErr, engagements, ledgerErr, hashErr)
		}
	}
	if err = installStep10EnvironmentResultFault(environment.profileRoot); err != nil {
		t.Fatalf("install disposable post-engagement fault: %v", err)
	}
	if _, err = client.ExecuteCommand(ctx, hookCommand); err == nil {
		t.Fatal("post-engagement Environment result-install fault returned success")
	}
	if state, installed := step10HookLedgerState(t, environment.profileRoot, commandID); state != "completed" || installed {
		t.Fatalf("fault did not leave a committed engagement before Environment installation: state=%q installed=%t", state, installed)
	}
	requestHash, err := hookCommand.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.store.QueryCommand(ctx, m5TestDomain, commandID, requestHash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || !pending.Pending || len(pending.Receipt) != 0 {
		t.Fatalf("post-engagement fault did not retain the original authority submission: status=%+v error=%v", pending, err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC at post-engagement/pre-result crash point: %v", err)
	}
	stopDaemon()
	if err = removeStep10EnvironmentResultFault(environment.profileRoot); err != nil {
		t.Fatalf("remove disposable post-engagement fault: %v", err)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	birthSequence, err := step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next Environment sequence before production Plan continuation: %v", err)
	}
	if err = installStep10ReceiptInstallFault(environment.profileRoot, birthSequence); err != nil {
		t.Fatalf("install disposable first Plan-birth receipt fault: %v", err)
	}
	planned, err := client.ExecuteCommand(ctx, hookCommand)
	if err == nil {
		t.Fatal("production Plan continuation hid the injected first-birth install failure")
	}
	if state, installed := step10HookLedgerState(t, environment.profileRoot, commandID); state != "completed" || !installed {
		t.Fatalf("Plan result was not durable before first-birth receipt failure: state=%q installed=%t", state, installed)
	}
	pendingBirth := step10PendingBirthAtSequence(t, environment.profileRoot, birthSequence)
	if pendingBirth.CausationCommandID != hookCommand.ID || pendingBirth.CorrelationCommandID != hookCommand.ID {
		t.Fatalf("production birth lost its original Plan binding: %+v", pendingBirth)
	}
	afterFirstBirthEffect, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterFirstBirthEffect.EventCount != anchor.EventCount+2 {
		t.Fatalf("first birth effect was not committed exactly once before local receipt install: base=%+v after=%+v error=%v",
			anchor, afterFirstBirthEffect, err)
	}
	prematureClose := command(m5TwoEnvironmentID(88), birthSequence+1, operation.Request{
		Operation: operation.RoleCloseV1.Metadata().Operation,
		Actor:     operation.Actor("role:researcher"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Input: operation.RoleLifecycleInput{
			RunID: runID, RunLocator: runLocator, MatterID: created.ID,
			DispatchID: claim.DispatchID, RoleID: m5TwoEnvironmentID(18), Role: "researcher", Reason: "completed",
		},
	})
	prematureCloseHash, err := prematureClose.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, closeErr := fixture.store.SubmitCommand(ctx, prematureClose, prematureCloseHash, fixture.peer, time.Now().UTC()); !errors.Is(closeErr, authoritystore.ErrPending) {
		t.Fatalf("Researcher Role closed before the Plan's second durable birth: err=%v", closeErr)
	}
	afterPrematureClose, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterPrematureClose != afterFirstBirthEffect {
		t.Fatalf("incomplete-Plan Role close refusal changed authority history: before=%+v after=%+v error=%v",
			afterFirstBirthEffect, afterPrematureClose, err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment at production partial-Plan-birth crash point: %v", err)
	}
	stopDaemon()
	if err = removeStep10ReceiptInstallFault(environment.profileRoot); err != nil {
		t.Fatalf("remove disposable first Plan-birth receipt fault: %v", err)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	planned, err = client.ExecuteCommand(ctx, hookCommand)
	if err != nil {
		t.Fatalf("same-Environment production Plan continuation did not recover its original birth: %v daemon=%s", err, daemonOutput.String())
	}
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read Environment sequence after production Plan recovery: %v", err)
	}
	output, outputOK := planned.Output.(operation.SchedulerHookOutput)
	if err != nil || planned.Code != operation.ResultSucceeded || !outputOK || output.Kind != "plan" ||
		output.Outcome != "planned" || len(output.Steps) != 2 ||
		output.Steps[0].Title != "First asymmetric task" || output.Steps[1].Title != "Second task" {
		requestHash, hashErr := hookCommand.RequestHash()
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, commandID, requestHash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
		currentPrefix, prefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		t.Fatalf("authenticated Step 10 Plan result = %+v (%T), %v; authority pending=%t receipt_bytes=%d query_error=%v hash_error=%v current_prefix=%+v prefix_error=%v daemon=%s",
			planned, planned.Output, err, status.Pending, len(status.Receipt), statusErr, hashErr, currentPrefix, prefixErr, daemonOutput.String())
	}
	status, err := fixture.store.QueryCommand(ctx, m5TestDomain, commandID, requestHash, 1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || status.Pending || len(status.Receipt) == 0 || string(status.Receipt) == "" {
		t.Fatalf("Step 10 authority receipt = %+v, error=%v", status, err)
	}
	installedPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if installedPrefix.EventCount != anchor.EventCount+3 {
		t.Fatalf("Plan and automatic births appended %d events, want exactly three", installedPrefix.EventCount-anchor.EventCount)
	}
	if outputs := step10PlanBirthOutputs(t, environment.profileRoot, hookCommand.ID); len(outputs) != 2 ||
		outputs[0].Title != "First asymmetric task" || outputs[1].Title != "Second task" || outputs[0].ID == outputs[1].ID {
		t.Fatalf("production Plan continuation did not install both distinct ordered births: %+v", outputs)
	}

	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC before restart: %v", err)
	}
	stopDaemon()
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	defer func() {
		_ = client.Close()
		stopDaemon()
	}()
	replayed, err := client.ExecuteCommand(ctx, hookCommand)
	replayedOutput, replayedOK := replayed.Output.(operation.SchedulerHookOutput)
	if err != nil || replayed.Code != operation.ResultSucceeded || !replayedOK || !reflect.DeepEqual(replayedOutput, output) {
		t.Fatalf("same-Environment process restart did not recover the exact Plan result: result=%+v error=%v daemon=%s",
			replayed, err, daemonOutput.String())
	}
	afterReplay, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterReplay.EventCount != installedPrefix.EventCount || afterReplay.Digest != installedPrefix.Digest {
		t.Fatalf("exact Plan replay changed authority history: before=%+v after=%+v error=%v", installedPrefix, afterReplay, err)
	}
	duplicatePlan := hookCommand
	duplicatePlan.ID = m5TwoEnvironmentID(89)
	duplicatePlan.EnvironmentSequence = sequence
	duplicatePlan.ActedAt = time.Now().UTC().Format(time.RFC3339Nano)
	duplicatePlan.CorrelationCommandID = duplicatePlan.ID
	duplicateAnchorEventID := afterReplay.EventID
	duplicateInput := duplicatePlan.Request.Input.(operation.SchedulerHookInput)
	duplicateInput.InstalledPrefix = operation.SchedulerPrefixAnchor{
		EventCount: afterReplay.EventCount, EventID: &duplicateAnchorEventID, Digest: afterReplay.Digest,
	}
	duplicatePlan.Request.Input = duplicateInput
	duplicateHash, err := duplicatePlan.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.SubmitCommand(ctx, duplicatePlan, duplicateHash, fixture.peer, time.Now().UTC()); !errors.Is(err, authoritystore.ErrConflict) {
		t.Fatalf("fresh command ID re-engaged the already completed Plan unit: err=%v", err)
	}
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, duplicatePlan.ID, duplicateHash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("fresh-ID duplicate Plan unexpectedly acquired an authority submission or receipt")
	}
	duplicatePrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || duplicatePrefix != afterReplay {
		t.Fatalf("fresh-ID duplicate Plan changed authority history: before=%+v after=%+v error=%v", afterReplay, duplicatePrefix, err)
	}
	ledger, err := step10HookLedgerDB(environment.profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	var duplicateEngagements int
	err = ledger.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		m5TestDomain, duplicatePlan.ID).Scan(&duplicateEngagements)
	_ = ledger.Close()
	if err != nil || duplicateEngagements != 0 {
		t.Fatalf("fresh-ID duplicate Plan created an adapter engagement: count=%d err=%v", duplicateEngagements, err)
	}

	// The production continuation selected and installed both birth identities;
	// this test reads their durable receipts and exercises only the ordinary
	// lifecycle that consumes them.
	plannedBirths := step10PlanBirthOutputs(t, environment.profileRoot, hookCommand.ID)
	for index, createdStep := range plannedBirths {
		plannedStep := output.Steps[index]
		if createdStep.Title != plannedStep.Title || createdStep.State != "planned" || createdStep.ParentID != created.ID {
			t.Fatalf("production birth %d does not match its typed Plan unit: birth=%+v planned=%+v", index, createdStep, plannedStep)
		}
		started, startErr := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(21+index*3), sequence, operation.Request{
			Operation: operation.StepStartV1.Metadata().Operation,
			Actor:     operation.Actor("role:scheduler"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
			Input:     operation.StepLifecycleInput{StepID: createdStep.ID},
		}))
		sequence++
		if startErr != nil || started.Code != operation.ResultSucceeded || started.Output != (operation.StepLifecycleOutput{
			StepID: createdStep.ID, MatterID: created.ID, State: "in-progress",
		}) {
			t.Fatalf("authority did not start Plan step %q: result=%+v error=%v", plannedStep.Title, started, startErr)
		}
		if index != 0 {
			finished, finishErr := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(22+index*3), sequence, operation.Request{
				Operation: operation.StepFinishV1.Metadata().Operation,
				Actor:     operation.Actor("role:scheduler"),
				Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
				Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
				Input:     operation.StepLifecycleInput{StepID: createdStep.ID},
			}))
			sequence++
			if finishErr != nil || finished.Code != operation.ResultSucceeded || finished.Output != (operation.StepLifecycleOutput{
				StepID: createdStep.ID, MatterID: created.ID, State: "done",
			}) {
				t.Fatalf("authority did not finish Plan step %q: result=%+v error=%v", plannedStep.Title, finished, finishErr)
			}
		}
	}
	// Builder and Verifier use the same authenticated split phase. Builder's
	// pinned-source outline is retained as a typed evidence blob; Verifier then
	// consumes that exact outline and source under a new original command ID.
	currentAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	spawnRole(41, 33, "builder")
	currentAnchor, err = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	currentEventID := currentAnchor.EventID
	builderSource := source
	if strings.HasPrefix(policyMode, "failed-") {
		builderSource = failedSource
	}
	builderCommand := command(m5TwoEnvironmentID(30), sequence, operation.Request{
		Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
		Actor:     operation.Actor("role:builder"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Blobs:     []operation.BlobInput{{Name: "source", Digest: builderSource.Digest, Size: builderSource.Size}},
		Input: operation.SchedulerHookInput{
			Kind: "work", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
			RunID: m5TwoEnvironmentID(17), MatterID: created.ID, TargetID: plannedBirths[0].ID, TargetKind: "step",
			Role: "builder", RoleID: m5TwoEnvironmentID(33), DispatchID: claim.DispatchID,
			Revision:        "builtin.markdown-outline@v1",
			TargetClaims:    []operation.TargetClaim{{MatterID: created.ID, ClaimID: claim.ClaimID, ClaimEpoch: claim.ClaimEpoch}},
			InstalledPrefix: operation.SchedulerPrefixAnchor{EventCount: currentAnchor.EventCount, EventID: &currentEventID, Digest: currentAnchor.Digest},
		},
	})
	wrongTargetKind := builderCommand
	wrongTargetKind.ID = m5TwoEnvironmentID(92)
	wrongTargetKind.CorrelationCommandID = wrongTargetKind.ID
	wrongTargetInput := wrongTargetKind.Request.Input.(operation.SchedulerHookInput)
	wrongTargetInput.TargetKind = "stage"
	wrongTargetKind.Request.Input = wrongTargetInput
	wrongTargetHash, err := wrongTargetKind.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.SubmitCommand(ctx, wrongTargetKind, wrongTargetHash, fixture.peer, time.Now().UTC()); !errors.Is(err, authoritystore.ErrFenced) {
		t.Fatalf("authority accepted a Step ID mislabeled as a Stage target: %v", err)
	}
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, wrongTargetKind.ID, wrongTargetHash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("wrong target kind created an authority submission")
	}
	wrongTargetPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || wrongTargetPrefix != currentAnchor {
		t.Fatalf("wrong target kind changed authority history: before=%+v after=%+v error=%v", currentAnchor, wrongTargetPrefix, err)
	}
	ledger, err = step10HookLedgerDB(environment.profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	var wrongTargetEngagements int
	err = ledger.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		m5TestDomain, wrongTargetKind.ID).Scan(&wrongTargetEngagements)
	_ = ledger.Close()
	if err != nil || wrongTargetEngagements != 0 {
		t.Fatalf("wrong target kind launched an adapter engagement: count=%d error=%v", wrongTargetEngagements, err)
	}
	hookFaults.mode.Store(step10FaultBeforeResultReceiver)
	built, err := client.ExecuteCommand(ctx, builderCommand)
	if err == nil {
		t.Fatal("Environment-result-installed/authority-receiver fault returned success")
	}
	builderHash, hashErr := builderCommand.RequestHash()
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	builderStatus, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, builderCommand.ID, builderHash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if statusErr != nil || !builderStatus.Pending || len(builderStatus.Receipt) != 0 {
		t.Fatalf("pre-receiver fault unexpectedly completed authority command: status=%+v error=%v", builderStatus, statusErr)
	}
	if state, installed := step10HookLedgerState(t, environment.profileRoot, builderCommand.ID); state != "completed" || !installed {
		t.Fatalf("pre-receiver fault did not retain the installed Environment result: state=%q installed=%t", state, installed)
	}
	step10AssertReceiverRefusals(t, fixture, environment.profileRoot, builderCommand)
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC at installed-result/pre-receipt crash point: %v", err)
	}
	stopDaemon()
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	if strings.HasPrefix(policyMode, "failed-") {
		built, err = client.ExecuteCommand(ctx, builderCommand)
		builtOutput, outputOK := built.Output.(operation.SchedulerHookOutput)
		if err != nil || built.Code != operation.ResultSucceeded || !outputOK || builtOutput.Outcome != "failed" ||
			builtOutput.ProblemCode != "adapter.markdown-no-headings" || builtOutput.EvidenceRefs == nil || len(builtOutput.EvidenceRefs) != 0 {
			t.Fatalf("exact-ID restart retry did not recover the terminal failed Work receipt: result=%+v error=%v", built, err)
		}
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, builderCommand.ID, builderHash, 1,
			fixture.peer, m5TestEnv, time.Now().UTC())
		authorityDB, openErr := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		var eventCount int
		var eventRecord []byte
		eventErr := authorityDB.QueryRow(`SELECT count(*),min(record) FROM authority_events WHERE domain_id=? AND command_id=?`,
			m5TestDomain, builderCommand.ID).Scan(&eventCount, &eventRecord)
		_ = authorityDB.Close()
		if eventErr != nil || eventCount != 1 {
			t.Fatalf("failed Work command has %d authority events, want exactly one: %v", eventCount, eventErr)
		}
		eventFields, decodeErr := wipdwire.DecodeCanonicalMap(eventRecord,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil || eventFields["kind"] != "scheduler.hook-completed" || eventFields["subject_id"] != created.ID {
			t.Fatalf("failed Work command did not produce its exact hook-completed event: fields=%v error=%v", eventFields, decodeErr)
		}
		if statusErr != nil || status.Pending || len(status.Receipt) == 0 {
			t.Fatalf("failed Work did not receive one terminal successful authority receipt: status=%+v error=%v", status, statusErr)
		}
		ledger, ledgerErr := step10HookLedgerDB(environment.profileRoot)
		if ledgerErr != nil {
			t.Fatalf("open hook ledger after failed Work recovery: %v", ledgerErr)
		}
		var engagements int
		ledgerErr = ledger.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
			m5TestDomain, builderCommand.ID).Scan(&engagements)
		_ = ledger.Close()
		if ledgerErr != nil || engagements != 1 {
			t.Fatalf("failed Work exact retry re-engaged the adapter: count=%d error=%v", engagements, ledgerErr)
		}
		sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
		if err != nil {
			t.Fatalf("read same-Environment sequence before failed-Work Role closure: %v", err)
		}
		closeRoleID(82, m5TwoEnvironmentID(33), "builder", "completed")
		if policyMode == "failed-no-ask" {
			authorityPath := filepath.Join(fixture.root, "authority.db")
			claimsBefore := step10ClaimCountForBatch(t, authorityPath, initialClaim.BatchID)
			dispatchesBefore := step10DispatchCountForBatch(t, authorityPath, initialClaim.BatchID)
			rolesBefore := step10RoleSpawnCount(t, authorityPath)
			hooksBefore := step10HookSubmissionCount(t, authorityPath)
			prefixBefore, prefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			if prefixErr != nil {
				t.Fatal(prefixErr)
			}
			resumed, resumeErr := client.ResumeSchedulerRun(ctx, runID)
			prefixAfter, afterErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
			claimsAfter := step10ClaimCountForBatch(t, authorityPath, initialClaim.BatchID)
			dispatchesAfter := step10DispatchCountForBatch(t, authorityPath, initialClaim.BatchID)
			rolesAfter := step10RoleSpawnCount(t, authorityPath)
			hooksAfter := step10HookSubmissionCount(t, authorityPath)
			if resumeErr != nil || resumed.RunID != runID || resumed.MatterID != created.ID || resumed.Blocked != "policy-required" ||
				resumed.Finished || resumed.TargetID != "" || resumed.TargetKind != "" || resumed.WorkCausationID != "" ||
				claimsAfter != claimsBefore || dispatchesAfter != dispatchesBefore || rolesAfter != rolesBefore || hooksAfter != hooksBefore ||
				afterErr != nil || prefixAfter != prefixBefore {
				t.Fatalf("Resume did not park failed Builder without D30 policy before fresh work: response=%+v error=%v counts claims=%d→%d dispatches=%d→%d Roles=%d→%d hooks=%d→%d prefix=%+v→%+v prefix_error=%v daemon=%s",
					resumed, resumeErr, claimsBefore, claimsAfter, dispatchesBefore, dispatchesAfter, rolesBefore, rolesAfter,
					hooksBefore, hooksAfter, prefixBefore, prefixAfter, afterErr, daemonOutput.String())
			}
			unexpectedLock, lockErr := runlock.Acquire(runID)
			if !errors.Is(lockErr, runlock.ErrHeld) {
				if unexpectedLock != nil {
					_ = unexpectedLock.Release()
				}
				t.Fatalf("missing-policy parked Run did not retain its owner lock: %v", lockErr)
			}
			return
		}
		handling := strings.TrimPrefix(policyMode, "failed-")
		if handling != "halt" && handling != "skip" {
			t.Fatalf("unknown failed-Work policy test mode %q", policyMode)
		}
		policyAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil {
			t.Fatal(anchorErr)
		}
		policyEventID := policyAnchor.EventID
		askSequence, sequenceErr := step10NextEnvironmentSequence(environment.profileRoot)
		if sequenceErr != nil {
			t.Fatal(sequenceErr)
		}
		askCommand := command(m5TwoEnvironmentID(32), askSequence, operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
			Actor:     operation.Actor("system:scheduler"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Input: operation.SchedulerHookInput{
				Kind: "ask", Version: 1, AdapterID: schedulerhook.AdapterID, AdapterVersion: uint64(schedulerhook.AdapterVersion),
				RunID: runID, MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
				Revision: "builtin.run-policy@v1", Reason: "failed", Contention: "skip", Blocked: "skip", Failed: handling,
				TargetClaims: []operation.TargetClaim{},
				InstalledPrefix: operation.SchedulerPrefixAnchor{
					EventCount: policyAnchor.EventCount, EventID: &policyEventID, Digest: policyAnchor.Digest,
				},
			},
		})
		resumeAfterLostAsk(askCommand, handling, []string{m5TwoEnvironmentID(18), m5TwoEnvironmentID(36)})
		return
	}
	finishSequence, err := step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next Environment sequence before Work continuation: %v", err)
	}
	if err = installStep10ReceiptInstallFault(environment.profileRoot, finishSequence); err != nil {
		t.Fatalf("install disposable Work pre-finish receipt fault: %v", err)
	}
	built, err = client.ExecuteCommand(ctx, builderCommand)
	if err == nil {
		finishCommand, finishState := step10CommandAtSequence(t, environment.profileRoot, finishSequence)
		t.Fatalf("production Work continuation hid the injected pre-finish receipt failure: result=%+v finish=%s state=%s", built, finishCommand.Request.Operation, finishState)
	}
	t.Logf("Work continuation returned before pre-finish recovery: result=%+v error=%v expected-sequence=%d next-sequence=%v", built, err, finishSequence, func() any {
		next, nextErr := step10NextEnvironmentSequence(environment.profileRoot)
		return []any{next, nextErr}
	}())
	if state, installed := step10HookLedgerState(t, environment.profileRoot, builderCommand.ID); state != "completed" || !installed {
		t.Fatalf("completed Work result was not installed before pre-finish receipt failure: state=%q installed=%t", state, installed)
	}
	finishCommand, finishState := step10CommandAtSequence(t, environment.profileRoot, finishSequence)
	finishInput, finishInputOK := finishCommand.Request.Input.(operation.StepLifecycleInput)
	if finishCommand.Request.Operation != operation.StepFinishV1.Metadata().Operation || finishState != "pending-return" ||
		finishCommand.CausationCommandID != builderCommand.ID || finishCommand.CorrelationCommandID != builderCommand.ID ||
		!finishInputOK || finishInput.StepID != plannedBirths[0].ID {
		t.Fatalf("production Work pre-finish lost its exact durable binding: command=%+v state=%q", finishCommand, finishState)
	}
	finishHash, err := finishCommand.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	finishStatus, err := fixture.store.QueryCommand(ctx, m5TestDomain, finishCommand.ID, finishHash, 1,
		fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || finishStatus.Pending || len(finishStatus.Receipt) == 0 {
		t.Fatalf("Work pre-finish authority result was not durable before local install failure: status=%+v error=%v", finishStatus, err)
	}
	preFinishPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment at Work/pre-finish crash point: %v", err)
	}
	stopDaemon()
	if err = removeStep10ReceiptInstallFault(environment.profileRoot); err != nil {
		t.Fatalf("remove disposable Work pre-finish receipt fault: %v", err)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	built, err = client.ExecuteCommand(ctx, builderCommand)
	if err != nil {
		t.Fatalf("same-Environment production Work continuation did not recover its pre-finish: %v daemon=%s", err, daemonOutput.String())
	}
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read Environment sequence after Work pre-finish recovery: %v", err)
	}
	builtOutput, builtOK := built.Output.(operation.SchedulerHookOutput)
	if err != nil || built.Code != operation.ResultSucceeded || !builtOK || builtOutput.Kind != "work" ||
		builtOutput.Outcome != "completed" || len(builtOutput.EvidenceRefs) != 1 {
		t.Fatalf("authenticated Builder did not return a useful typed outline: result=%+v error=%v daemon=%s",
			built, err, daemonOutput.String())
	}
	if builtOutput.EvidenceRefs[0] != expectedOutlineDigest {
		t.Fatalf("Builder evidence digest = %s, independently expected %s", builtOutput.EvidenceRefs[0], expectedOutlineDigest)
	}
	evidenceDB, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatalf("open disposable authority store to inspect Builder artifact retention: %v", err)
	}
	var evidencePosition, completionPosition uint64
	err = evidenceDB.QueryRow(`SELECT r.first_position,e.position FROM blob_references r
		JOIN authority_events e ON e.domain_id=r.domain_id AND e.command_id=?
		WHERE r.domain_id=? AND r.digest=?`, builderCommand.ID, m5TestDomain, expectedOutlineDigest).
		Scan(&evidencePosition, &completionPosition)
	_ = evidenceDB.Close()
	if err != nil || evidencePosition != completionPosition {
		t.Fatalf("authenticated Builder output was not retained at its authority completion event: artifact_position=%d event_position=%d error=%v",
			evidencePosition, completionPosition, err)
	}
	afterPreFinishReplay, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterPreFinishReplay != preFinishPrefix {
		t.Fatalf("replaying production Work pre-finish duplicated its authority outcome: before=%+v after=%+v error=%v",
			preFinishPrefix, afterPreFinishReplay, err)
	}
	currentAnchor, err = fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	duplicateWork := builderCommand
	duplicateWork.ID = m5TwoEnvironmentID(91)
	duplicateWork.EnvironmentSequence = sequence
	duplicateWork.ActedAt = time.Now().UTC().Format(time.RFC3339Nano)
	duplicateWork.CorrelationCommandID = duplicateWork.ID
	duplicateWorkInput := duplicateWork.Request.Input.(operation.SchedulerHookInput)
	duplicateWorkEventID := currentAnchor.EventID
	duplicateWorkInput.InstalledPrefix = operation.SchedulerPrefixAnchor{
		EventCount: currentAnchor.EventCount, EventID: &duplicateWorkEventID, Digest: currentAnchor.Digest,
	}
	duplicateWork.Request.Input = duplicateWorkInput
	duplicateWorkHash, err := duplicateWork.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.SubmitCommand(ctx, duplicateWork, duplicateWorkHash, fixture.peer, time.Now().UTC()); !errors.Is(err, authoritystore.ErrConflict) {
		t.Fatalf("fresh command ID relaunched the completed Builder Work unit: %v", err)
	}
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, duplicateWork.ID, duplicateWorkHash, 1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("fresh-ID duplicate Work unexpectedly acquired an authority receipt")
	}
	duplicateWorkPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || duplicateWorkPrefix != currentAnchor {
		t.Fatalf("fresh-ID duplicate Work changed authority history: before=%+v after=%+v error=%v", currentAnchor, duplicateWorkPrefix, err)
	}
	declared, err := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(35), sequence, operation.Request{
		Operation: operation.GateDeclareV1.Metadata().Operation,
		Actor:     operation.Actor("human"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Input:     operation.GateDeclareInput{Gate: "verified", Scale: "matter"},
	}))
	sequence++
	if err != nil || declared.Code != operation.ResultSucceeded || declared.Output != (operation.GateDeclareOutput{
		Gate: "verified", Scale: "matter",
	}) {
		t.Fatalf("Repo did not install the fixed verified gate declaration: result=%+v error=%v", declared, err)
	}
	spawnRole(42, 34, "verifier")
	negativeBasePrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	verifyOutline := func(commandNumber, roleNumber int, digest string, size int64) operation.Result {
		t.Helper()
		currentAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil {
			t.Fatal(anchorErr)
		}
		currentEventID := currentAnchor.EventID
		verified, verifyErr := client.ExecuteCommand(ctx, command(m5TwoEnvironmentID(commandNumber), sequence, operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
			Actor:     operation.Actor("role:verifier"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
			Blobs: []operation.BlobInput{
				{Name: "outline", Digest: digest, Size: size},
				{Name: "source", Digest: source.Digest, Size: source.Size},
			},
			Input: operation.SchedulerHookInput{
				Kind: "work", Version: 1, AdapterID: "builtin.pinned-markdown", AdapterVersion: 1,
				RunID: m5TwoEnvironmentID(17), MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
				Role: "verifier", RoleID: m5TwoEnvironmentID(roleNumber), DispatchID: claim.DispatchID,
				Revision:        "builtin.markdown-outline-verifier@v1",
				TargetClaims:    []operation.TargetClaim{{MatterID: created.ID, ClaimID: claim.ClaimID, ClaimEpoch: claim.ClaimEpoch}},
				InstalledPrefix: operation.SchedulerPrefixAnchor{EventCount: currentAnchor.EventCount, EventID: &currentEventID, Digest: currentAnchor.Digest},
			},
		}))
		sequence++
		if verifyErr != nil {
			t.Fatalf("authenticated Verifier command: %v", verifyErr)
		}
		return verified
	}
	negative := verifyOutline(31, 34, invalidOutline.Digest, invalidOutline.Size)
	negativeOutput, negativeOK := negative.Output.(operation.SchedulerHookOutput)
	if negative.Code != operation.ResultSucceeded || !negativeOK || negativeOutput.Kind != "work" ||
		negativeOutput.Outcome != "not-verified" || negativeOutput.ProblemCode != "verification.markdown-outline-invalid" {
		t.Fatalf("invalid pinned outline should produce typed negative verification, not success: %+v daemon=%s",
			negative, daemonOutput.String())
	}
	negativePrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if negativePrefix.EventCount != negativeBasePrefix.EventCount+1 {
		t.Fatalf("negative Verifier did not append exactly its typed hook-completion event: before=%d after=%d",
			negativeBasePrefix.EventCount, negativePrefix.EventCount)
	}
	closeRole(46, 34, "verifier", "reaped")
	spawnRole(47, 37, "verifier")
	verified := verifyOutline(48, 37, outline.Digest, outline.Size)
	verifiedOutput, verifiedOK := verified.Output.(operation.SchedulerHookOutput)
	if verified.Code != operation.ResultSucceeded || !verifiedOK || verifiedOutput.Kind != "work" ||
		verifiedOutput.Outcome != "not-verified" || verifiedOutput.ProblemCode != "verification.complete-pass-unavailable" ||
		len(verifiedOutput.EvidenceRefs) != 1 || verifiedOutput.EvidenceRefs[0] != outline.Digest {
		t.Fatalf("authenticated outline comparison did not remain fail-closed for D106: result=%+v daemon=%s",
			verified, daemonOutput.String())
	}
	if policyMode == "blocked-halt" || policyMode == "blocked-skip" {
		handling := strings.TrimPrefix(policyMode, "blocked-")
		currentAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil {
			t.Fatal(anchorErr)
		}
		currentEventID = currentAnchor.EventID
		askSequence, sequenceErr := step10NextEnvironmentSequence(environment.profileRoot)
		if sequenceErr != nil {
			t.Fatal(sequenceErr)
		}
		askCommand := command(m5TwoEnvironmentID(32), askSequence, operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
			Actor:     operation.Actor("system:scheduler"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Input: operation.SchedulerHookInput{
				Kind: "ask", Version: 1, AdapterID: schedulerhook.AdapterID, AdapterVersion: uint64(schedulerhook.AdapterVersion),
				RunID: runID, MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
				Revision: "builtin.run-policy@v1", Reason: "blocked", Contention: "halt", Blocked: handling, Failed: "halt",
				TargetClaims: []operation.TargetClaim{},
				InstalledPrefix: operation.SchedulerPrefixAnchor{
					EventCount: currentAnchor.EventCount, EventID: &currentEventID, Digest: currentAnchor.Digest,
				},
			},
		})
		resumeAfterLostAsk(askCommand, handling, []string{
			m5TwoEnvironmentID(18), m5TwoEnvironmentID(33), m5TwoEnvironmentID(36), m5TwoEnvironmentID(37),
		})
		return
	}
	if policyMode != "" && policyMode != "positive" && policyMode != "live-halt" && policyMode != "live-skip" {
		t.Fatalf("unknown Step 10 policy test mode %q", policyMode)
	}
	// This no-D30-decision control retains the negative-verification correction
	// path; explicit Ask answers are exercised in the halt/skip variants above.
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC before positive Resume control: %v", err)
	}
	stopDaemon()
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read Environment sequence before Run-frontier recovery: %v", err)
	}
	hookSubmissionsBeforeResume := step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db"))
	resumeCtx, cancelResume := context.WithTimeout(ctx, 30*time.Second)
	resumed, err := client.ResumeSchedulerRun(resumeCtx, runID)
	cancelResume()
	if err != nil || resumed.RunID != runID || resumed.MatterID != created.ID || resumed.Claim.ClaimID == "" ||
		resumed.Claim.ClaimID == initialClaim.ClaimID || resumed.Claim.DispatchID == initialClaim.DispatchID ||
		resumed.RoleName != "builder" || resumed.RoleID == "" || resumed.RoleCommandID == "" || resumed.TargetKind != "step" ||
		resumed.TargetID == "" || resumed.WorkCausationID == "" {
		t.Fatalf("production owner-Environment Resume did not recover, close/release, recompute and select the correction Builder: result=%+v error=%v daemon=%s",
			resumed, err, daemonOutput.String())
	}
	claim = &resumed.Claim
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next sequence after production Resume: %v", err)
	}
	step10AssertClaimReleaseBeforeFreshAcquire(t, filepath.Join(fixture.root, "authority.db"),
		initialClaim.ClaimID, resumed.Claim.ClaimID)
	if got := step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db")); got != hookSubmissionsBeforeResume {
		t.Fatalf("production Resume relaunched completed/pending hook work instead of recovering exact identities: before=%d after=%d", hookSubmissionsBeforeResume, got)
	}
	unexpectedLock, lockErr := runlock.Acquire(runID)
	if !errors.Is(lockErr, runlock.ErrHeld) {
		if unexpectedLock != nil {
			_ = unexpectedLock.Release()
		}
		t.Fatalf("owning Environment did not retain the Run lock after Resume for fresh Dispatch work: %v", lockErr)
	}
	if got := step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db")); got != hookSubmissionsBeforeResume {
		t.Fatalf("same-Environment Resume relaunched completed hook units: before=%d after=%d", hookSubmissionsBeforeResume, got)
	}
	resumePrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	resumeReplay, err := client.ResumeSchedulerRun(ctx, runID)
	resumePrefixReplay, prefixReplayErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || resumeReplay.Claim.ClaimID != claim.ClaimID || resumeReplay.RoleID != resumed.RoleID ||
		resumeReplay.RoleCommandID != resumed.RoleCommandID || resumeReplay.TargetID != resumed.TargetID ||
		prefixReplayErr != nil || resumePrefixReplay != resumePrefix {
		t.Fatalf("same-process Resume did not return the live Role/Dispatch without duplicate transitions: replay=%+v error=%v before=%+v after=%+v prefix_error=%v",
			resumeReplay, err, resumePrefix, resumePrefixReplay, prefixReplayErr)
	}
	if policyMode == "live-halt" || policyMode == "live-skip" {
		handling := strings.TrimPrefix(policyMode, "live-")
		policyAnchor, anchorErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if anchorErr != nil {
			t.Fatal(anchorErr)
		}
		policyEventID := policyAnchor.EventID
		askSequence, sequenceErr := step10NextEnvironmentSequence(environment.profileRoot)
		if sequenceErr != nil {
			t.Fatal(sequenceErr)
		}
		askCommand := command(m5TwoEnvironmentID(32), askSequence, operation.Request{
			Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
			Actor:     operation.Actor("system:scheduler"),
			Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
			Input: operation.SchedulerHookInput{
				Kind: "ask", Version: 1, AdapterID: schedulerhook.AdapterID, AdapterVersion: uint64(schedulerhook.AdapterVersion),
				RunID: runID, MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
				Revision: "builtin.run-policy@v1", Reason: "blocked", Contention: handling, Blocked: handling, Failed: handling,
				TargetClaims: []operation.TargetClaim{},
				InstalledPrefix: operation.SchedulerPrefixAnchor{
					EventCount: policyAnchor.EventCount, EventID: &policyEventID, Digest: policyAnchor.Digest,
				},
			},
		})
		asked, askErr := client.ExecuteCommand(ctx, askCommand)
		askOutput, askOutputOK := asked.Output.(operation.SchedulerHookOutput)
		if askErr != nil || asked.Code != operation.ResultSucceeded || !askOutputOK || askOutput.Kind != "ask" || askOutput.Handling != handling {
			t.Fatalf("authenticated D30 %s Ask did not install beside the live correction Role: result=%+v error=%v", handling, asked, askErr)
		}
		authorityPath := filepath.Join(fixture.root, "authority.db")
		claimsBefore := step10ClaimCountForBatch(t, authorityPath, claim.BatchID)
		dispatchesBefore := step10DispatchCountForBatch(t, authorityPath, claim.BatchID)
		rolesBefore := step10RoleSpawnCount(t, authorityPath)
		hooksBefore := step10HookSubmissionCount(t, authorityPath)
		policyPrefix, prefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		if prefixErr != nil {
			t.Fatal(prefixErr)
		}
		parked, parkErr := client.ResumeSchedulerRun(ctx, runID)
		parkedPrefix, parkedPrefixErr := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
		claimsAfter := step10ClaimCountForBatch(t, authorityPath, claim.BatchID)
		dispatchesAfter := step10DispatchCountForBatch(t, authorityPath, claim.BatchID)
		rolesAfter := step10RoleSpawnCount(t, authorityPath)
		hooksAfter := step10HookSubmissionCount(t, authorityPath)
		if prefixErr != nil || parkedPrefixErr != nil || claimsAfter != claimsBefore || dispatchesAfter != dispatchesBefore ||
			rolesAfter != rolesBefore || hooksAfter != hooksBefore || parked.RunID != runID || parked.Finished {
			t.Fatalf("same-daemon Resume created effects beyond the selected D30 handling=%s: response=%+v error=%v counts claims=%d→%d dispatches=%d→%d Roles=%d→%d hooks=%d→%d prefix=%+v→%+v prefix_errors=%v/%v daemon=%s",
				handling, parked, parkErr, claimsBefore, claimsAfter, dispatchesBefore, dispatchesAfter, rolesBefore, rolesAfter,
				hooksBefore, hooksAfter, policyPrefix, parkedPrefix, prefixErr, parkedPrefixErr, daemonOutput.String())
		}
		authorityDB, dbErr := sql.Open("sqlite", authorityPath)
		if dbErr != nil {
			t.Fatal(dbErr)
		}
		defer func() { _ = authorityDB.Close() }()
		if handling == "halt" {
			if parked.MatterID != created.ID || parked.Blocked != "policy-halt" ||
				parked.Claim.ClaimID != claim.ClaimID || parked.Claim.DispatchID != claim.DispatchID ||
				parked.RoleID != resumed.RoleID || parked.RoleCommandID != resumed.RoleCommandID || parked.RoleName != "builder" ||
				parked.TargetID != resumed.TargetID || parked.TargetKind != "step" || parked.WorkCausationID != resumed.WorkCausationID ||
				parkedPrefix != policyPrefix {
				t.Fatalf("same-daemon Resume did not park retained halt with exact live correction identity: result=%+v error=%v expected role=%s target=%s cause=%s prefix=%+v→%+v",
					parked, parkErr, resumed.RoleID, resumed.TargetID, resumed.WorkCausationID, policyPrefix, parkedPrefix)
			}
			if closedRoles := step10AuthorityEventPositions(t, authorityDB, "role.closed", resumed.RoleID); len(closedRoles) != 0 {
				t.Fatalf("same-daemon policy halt closed the retained live Role: positions=%v", closedRoles)
			}
		} else {
			if parked.MatterID != "" || parked.Blocked != "policy-skipped" || parked.Claim.ClaimID != "" ||
				parked.RoleID != "" || parked.RoleCommandID != "" || parked.RoleName != "" || parked.TargetID != "" ||
				parked.TargetKind != "" || parked.WorkCausationID != "" || parkedPrefix.EventCount <= policyPrefix.EventCount {
				t.Fatalf("same-daemon Resume did not apply retained skip through Role-close/release/frontier: result=%+v error=%v prefix=%+v→%+v",
					parked, parkErr, policyPrefix, parkedPrefix)
			}
			if closedRoles := step10AuthorityEventPositions(t, authorityDB, "role.closed", resumed.RoleID); len(closedRoles) != 1 {
				t.Fatalf("same-daemon skip did not close its old Role exactly once: positions=%v", closedRoles)
			}
			if releasedClaims := step10AuthorityEventPositions(t, authorityDB, "claim.released", claim.ClaimID); len(releasedClaims) != 1 {
				t.Fatalf("same-daemon skip did not release its old claim exactly once: positions=%v", releasedClaims)
			}
		}
		unexpectedLock, lockErr := runlock.Acquire(runID)
		if !errors.Is(lockErr, runlock.ErrHeld) {
			if unexpectedLock != nil {
				_ = unexpectedLock.Release()
			}
			t.Fatalf("live-halt parked Run did not retain its owner lock: %v", lockErr)
		}
		return
	}
	correctionAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	correctionEventID := correctionAnchor.EventID
	correctionCommandID := m5TwoEnvironmentID(81)
	correctionCommand := command(correctionCommandID, sequence, operation.Request{
		Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
		Actor:     operation.Actor("role:builder"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Blobs:     []operation.BlobInput{{Name: "source", Digest: source.Digest, Size: source.Size}},
		Input: operation.SchedulerHookInput{
			Kind: "work", Version: 1, AdapterID: schedulerhook.AdapterID, AdapterVersion: uint64(schedulerhook.AdapterVersion),
			RunID: runID, MatterID: created.ID, TargetID: resumed.TargetID, TargetKind: resumed.TargetKind,
			Role: "builder", RoleID: resumed.RoleID, DispatchID: claim.DispatchID, Revision: "builtin.markdown-outline@v1",
			TargetClaims: []operation.TargetClaim{{MatterID: created.ID, ClaimID: claim.ClaimID, ClaimEpoch: claim.ClaimEpoch}},
			InstalledPrefix: operation.SchedulerPrefixAnchor{
				EventCount: correctionAnchor.EventCount,
				EventID:    &correctionEventID, Digest: correctionAnchor.Digest,
			},
		},
	})
	correctionCommand.CausationCommandID = resumed.WorkCausationID
	corrected, correctedErr := client.ExecuteCommand(ctx, correctionCommand)
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next sequence after correction Builder hook and Step completion: %v", err)
	}
	correctedOutput, correctedOK := corrected.Output.(operation.SchedulerHookOutput)
	if correctedErr != nil || corrected.Code != operation.ResultSucceeded || !correctedOK || correctedOutput.Outcome != "completed" ||
		len(correctedOutput.EvidenceRefs) != 1 || correctedOutput.EvidenceRefs[0] != expectedOutlineDigest {
		t.Fatalf("production Resume correction Builder did not perform useful typed work on its new Step: result=%+v error=%v", corrected, correctedErr)
	}
	closeRoleID(82, resumed.RoleID, "builder", "completed")
	verifierResume, err := client.ResumeSchedulerRun(ctx, runID)
	if err != nil || verifierResume.RoleName != "verifier" || verifierResume.TargetKind != "matter" ||
		verifierResume.TargetID != created.ID || verifierResume.RoleID == "" ||
		verifierResume.Claim.ClaimID != claim.ClaimID || verifierResume.WorkCausationID != correctionCommandID {
		t.Fatalf("production Resume did not select the Verifier after the causally bound Builder correction: result=%+v error=%v", verifierResume, err)
	}
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next sequence after production Verifier Role activation: %v", err)
	}
	if got := step10HookSubmissionCount(t, filepath.Join(fixture.root, "authority.db")); got != hookSubmissionsBeforeResume+1 {
		t.Fatalf("correction path executed an unexpected number of additional hooks: before=%d after=%d", hookSubmissionsBeforeResume, got)
	}
	if checkID, nested := step10NestedVerifierCheck(); nested {
		t.Logf("non-recursive D106 boundary inside %s: production Resume, reaped-Role close-before-release, fresh-claim correction Builder and next-Verifier selection completed; enclosing acceptance runs D106/C", checkID)
		return
	}
	verifierCommandID := m5TwoEnvironmentID(77)
	verifierAnchor, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatalf("read authority prefix before the complete Verifier pass: %v", err)
	}
	verifierAnchorEventID := verifierAnchor.EventID
	verifierInput := operation.SchedulerHookInput{
		Kind: "work", Version: 1, AdapterID: schedulerhook.AdapterID, AdapterVersion: uint64(schedulerhook.AdapterVersion),
		RunID: m5TwoEnvironmentID(17), MatterID: created.ID, TargetID: created.ID, TargetKind: "matter",
		Role: "verifier", RoleID: verifierResume.RoleID, DispatchID: claim.DispatchID,
		Revision:     operation.SchedulerStep10VerifierRevision,
		TargetClaims: []operation.TargetClaim{{MatterID: created.ID, ClaimID: claim.ClaimID, ClaimEpoch: claim.ClaimEpoch}},
		InstalledPrefix: operation.SchedulerPrefixAnchor{
			EventCount: verifierAnchor.EventCount, EventID: &verifierAnchorEventID, Digest: verifierAnchor.Digest,
		},
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC before offline Verifier input staging: %v", err)
	}
	stopDaemon()
	substitutedCommandID := m5TwoEnvironmentID(76)
	substitutedBlobs, substitutedCandidate, substitutedManifest := step10VerifierInputs(
		t, environment, verifierInput, substitutedCommandID, claim.ClaimID, claim.ClaimEpoch, true,
	)
	verifierBlobs, candidateRevision, candidateManifestDigest := step10VerifierInputs(
		t, environment, verifierInput, verifierCommandID, claim.ClaimID, claim.ClaimEpoch, false,
	)
	if substitutedCandidate != candidateRevision || substitutedManifest != candidateManifestDigest {
		t.Fatalf("baseline substitution changed candidate identity: candidate=%s/%s manifest=%s/%s",
			substitutedCandidate, candidateRevision, substitutedManifest, candidateManifestDigest)
	}
	substitutionProfile := step10CloneEnvironmentProfile(t, environment, filepath.Join(root, "baseline-substitution"))
	substitutionClient, stopSubstitutionDaemon, substitutionOutput := startWipdForBirthReleaseRecovery(
		t, binary, substitutionProfile, true, true)
	substitutedCommand := command(substitutedCommandID, sequence, operation.Request{
		Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
		Actor:     operation.Actor("role:verifier"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Blobs:     substitutedBlobs,
		Input:     verifierInput,
	})
	substitutedCommand.CausationCommandID = correctionCommandID
	substitutedRequestHash, err := substitutedCommand.RequestHash()
	if err != nil {
		t.Fatalf("hash authenticated baseline-substitution probe: %v", err)
	}
	if _, err = substitutionClient.ExecuteCommand(ctx, substitutedCommand); err == nil {
		t.Fatal("authenticated authority accepted an unlisted baseline README substitution")
	}
	if err = substitutionClient.Close(); err != nil {
		t.Fatalf("close baseline-substitution probe Environment IPC: %v", err)
	}
	stopSubstitutionDaemon()
	if _, err = fixture.store.QueryCommand(ctx, m5TestDomain, substitutedCommandID, substitutedRequestHash,
		1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
		t.Fatal("baseline-substitution refusal retained an authority command submission")
	}
	substitutedPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || substitutedPrefix != verifierAnchor {
		t.Fatalf("baseline-substitution refusal changed authority history: before=%+v after=%+v error=%v",
			verifierAnchor, substitutedPrefix, err)
	}
	ledger, err = step10HookLedgerDB(substitutionProfile)
	if err != nil {
		t.Fatalf("open cloned disposable adapter ledger after baseline-substitution refusal: %v", err)
	}
	var substitutedEngagements int
	err = ledger.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		m5TestDomain, substitutedCommandID).Scan(&substitutedEngagements)
	_ = ledger.Close()
	if err != nil || substitutedEngagements != 0 {
		t.Fatalf("authority launched or retained a baseline-substitution adapter engagement: count=%d error=%v daemon=%s",
			substitutedEngagements, err, substitutionOutput.String())
	}
	if os.Getenv("WIPD_STEP10_PROCESS_STAGE") == "baseline-substitution" {
		t.Log("authenticated authority refused the substituted baseline before submission, prefix change, or adapter engagement")
		return
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	verifierCommand := command(verifierCommandID, sequence, operation.Request{
		Operation: operation.SchedulerOrchestrateV1.Metadata().Operation,
		Actor:     operation.Actor("role:verifier"),
		Context:   operation.Context{Repo: m5TestRepo, Clone: cloneID, Worktree: worktreeID},
		Claim:     &operation.ClaimContext{ID: claim.ClaimID, Epoch: fmt.Sprint(claim.ClaimEpoch)},
		Blobs:     verifierBlobs,
		Input:     verifierInput,
	})
	verifierCommand.CausationCommandID = correctionCommandID
	verifierRequestHash, err := verifierCommand.RequestHash()
	if err != nil {
		t.Fatalf("hash exact authenticated Verifier command: %v", err)
	}
	localNextSequence, err := step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read Environment command sequence before positive D106 run: %v", err)
	}
	sequenceDB, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatal(err)
	}
	var authoritySequenceHead uint64
	err = sequenceDB.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`,
		m5TestDomain, m5TestEnv).Scan(&authoritySequenceHead)
	_ = sequenceDB.Close()
	if err != nil || sequence != localNextSequence || sequence != authoritySequenceHead+1 {
		t.Fatalf("positive D106 command sequence is not the next installed authority/local sequence: command=%d local_next=%d authority_next=%d error=%v",
			sequence, localNextSequence, authoritySequenceHead+1, err)
	}
	hookFaults.mode.Store(step10FaultBeforeResultReceiver)
	hookFaults.keepAliveQueries.Store(0)
	if _, err = client.ExecuteCommand(ctx, verifierCommand); err == nil || hookFaults.mode.Load() != 0 {
		localNextAfter, localErr := step10NextEnvironmentSequence(environment.profileRoot)
		afterSequenceDB, _ := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
		var authorityHeadAfter uint64
		if afterSequenceDB != nil {
			_ = afterSequenceDB.QueryRow(`SELECT sequence_head FROM environments WHERE domain_id=? AND environment_id=?`,
				m5TestDomain, m5TestEnv).Scan(&authorityHeadAfter)
			_ = afterSequenceDB.Close()
		}
		lastFrame, _ := hookFaults.lastFrame.Load().(string)
		t.Fatalf("post-verification/pre-authority-result fault was not exercised: error=%v mode=%d keepalives=%d command-sequence=%d local-next=%d local-error=%v authority-head=%d last-frame=%q daemon=%s",
			err, hookFaults.mode.Load(), hookFaults.keepAliveQueries.Load(), sequence, localNextAfter, localErr, authorityHeadAfter, lastFrame, daemonOutput.String())
	}
	if keepalives := hookFaults.keepAliveQueries.Load(); keepalives == 0 {
		state, installed := step10HookLedgerState(t, environment.profileRoot, verifierCommandID)
		inputDigest, checks, artifact := step10VerifierLedgerSnapshot(t, environment.profileRoot, verifierCommandID)
		var evidence step10VerifierEvidence
		if decodeErr := json.Unmarshal(artifact, &evidence); decodeErr != nil {
			t.Fatalf("long-running Verifier did not keep its exact pending authority command live; decode retained evidence: %v", decodeErr)
		}
		t.Logf("Verifier result-loss diagnostic: state=%q installed=%t input=%s outcome=%q checks=%d clean_states=%d bootstrap=%q",
			state, installed, inputDigest, evidence.Outcome, len(evidence.Checks), len(checks), evidence.Toolchain.BootstrapNetworkState)
		for _, check := range evidence.Checks {
			t.Logf("Verifier check diagnostic: id=%q required=%t skipped=%t exit=%d problem=%q stdout=%q stderr=%q",
				check.ID, check.Required, check.Skipped, check.ExitCode, check.Problem, check.StdoutTail, check.StderrTail)
		}
		t.Fatalf("long-running Verifier did not keep its exact pending authority command live")
	}
	verifierPending, err := fixture.store.QueryCommand(ctx, m5TestDomain, verifierCommandID, verifierRequestHash,
		1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || !verifierPending.Pending || len(verifierPending.Receipt) != 0 {
		t.Fatalf("injected pre-receiver fault unexpectedly completed the authority Verifier command: status=%+v error=%v",
			verifierPending, err)
	}
	if state, installed := step10HookLedgerState(t, environment.profileRoot, verifierCommandID); state != "completed" || !installed {
		t.Fatalf("Verifier checks/artifact were not durably installed before result loss: state=%q installed=%t",
			state, installed)
	}
	verifierInputDigest, verifierChecks, verifierArtifact := step10VerifierLedgerSnapshot(t,
		environment.profileRoot, verifierCommandID)
	validateStep10VerifierArtifact(t, verifierArtifact, verifierInput, verifierCommandID,
		verifierRequestHash, verifierInputDigest, candidateRevision, candidateManifestDigest, verifierBlobs)
	if len(verifierChecks) != len(step10ExpectedVerifierChecks) {
		t.Fatalf("pre-receiver durable clean-state count = %d, want every fixed check %d",
			len(verifierChecks), len(step10ExpectedVerifierChecks))
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC at complete-Verifier/pre-receipt crash point: %v", err)
	}
	if err = killStep10WipdProcess(t, binary, environment.profileRoot); err != nil {
		t.Fatalf("kill wipd after verifier effect and before authority result receipt: %v", err)
	}
	stopDaemon()
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	verified, err = client.ExecuteCommand(ctx, verifierCommand)
	sequence++
	verifiedOutput, verifiedOK = verified.Output.(operation.SchedulerHookOutput)
	if err != nil || verified.Code != operation.ResultSucceeded || !verifiedOK ||
		verifiedOutput.Kind != "work" || verifiedOutput.Version != 1 || verifiedOutput.Outcome != "verified" ||
		verifiedOutput.Revision != operation.SchedulerStep10VerifierRevision || len(verifiedOutput.EvidenceRefs) != 1 ||
		verifiedOutput.EvidenceRefs[0] != step10BlobDigest(verifierArtifact) {
		requestHash, hashErr := verifierCommand.RequestHash()
		status, statusErr := fixture.store.QueryCommand(ctx, m5TestDomain, verifierCommandID, requestHash,
			1, fixture.peer, m5TestEnv, time.Now().UTC())
		t.Fatalf("same-Environment restart did not recover the complete original Verifier result: result=%+v error=%v status=%+v status_error=%v hash_error=%v daemon=%s",
			verified, err, status, statusErr, hashErr, daemonOutput.String())
	}
	postRecoveryInputDigest, postRecoveryChecks, postRecoveryArtifact := step10VerifierLedgerSnapshot(
		t, environment.profileRoot, verifierCommandID,
	)
	if postRecoveryInputDigest != verifierInputDigest || !reflect.DeepEqual(postRecoveryChecks, verifierChecks) ||
		!bytes.Equal(postRecoveryArtifact, verifierArtifact) {
		t.Fatal("same-command recovery changed clean-state records, candidate evidence, or retained Verifier result")
	}
	step10PersistVerifierArtifact(t, candidateRevision, verifierArtifact)
	verifierStatus, err := fixture.store.QueryCommand(ctx, m5TestDomain, verifierCommandID, verifierRequestHash,
		1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || verifierStatus.Pending || len(verifierStatus.Receipt) == 0 {
		t.Fatalf("recovered positive Verifier has no exact authority terminal receipt: status=%+v error=%v",
			verifierStatus, err)
	}
	verifierEvidencePosition, verifierCompletionPosition := step10AuthorityEvidencePosition(
		t, filepath.Join(fixture.root, "authority.db"), verifierCommandID, verifiedOutput.EvidenceRefs[0],
	)
	if verifierEvidencePosition != verifierCompletionPosition {
		t.Fatalf("Verifier artifact was not durably retained at its completion event: evidence=%d completion=%d",
			verifierEvidencePosition, verifierCompletionPosition)
	}
	verifiedGatePosition := step10VerifiedGatePosition(t, filepath.Join(fixture.root, "authority.db"), created.ID)
	if uint64(verifiedGatePosition) <= verifierCompletionPosition {
		t.Fatalf("Verifier-owned verified gate was not automatically closed after its evidence event: evidence=%d gate=%d",
			verifierCompletionPosition, verifiedGatePosition)
	}
	sequence, err = step10NextEnvironmentSequence(environment.profileRoot)
	if err != nil {
		t.Fatalf("read next sequence after Verifier continuation and automatic gate closure: %v", err)
	}
	closeRoleID(79, verifierResume.RoleID, "verifier", "completed")
	settled, err := client.ResumeSchedulerRun(ctx, runID)
	if err != nil || !settled.Finished || settled.Claim.ClaimID != "" {
		t.Fatalf("production owner-Environment Resume did not finish the verified Matter, release normally, and settle the Run: result=%+v error=%v daemon=%s",
			settled, err, daemonOutput.String())
	}
	releaseID := step10ClaimReleaseCommandID(t, environment.profileRoot, claim.ClaimID)
	closed, err := client.ReleaseClaimJournal(ctx, releaseID, claim.ClaimID, claim.ClaimEpoch,
		created.ID, claim.DispatchID, operation.Actor("human"))
	if err != nil || closed.Code != operation.ResultSucceeded || closed.CommandID != releaseID || len(closed.Receipt) == 0 {
		t.Fatalf("production Resume's installed normal release could not be recovered by its original identity: result=%+v error=%v daemon=%s",
			closed, err, daemonOutput.String())
	}
	closedReplay, err := client.ReleaseClaimJournal(ctx, releaseID, claim.ClaimID, claim.ClaimEpoch,
		created.ID, claim.DispatchID, operation.Actor("human"))
	if err != nil || closedReplay.Code != closed.Code || !reflect.DeepEqual(closedReplay.Receipt, closed.Receipt) {
		t.Fatalf("same-ID Resume release did not replay the exact receipt: got=%+v error=%v", closedReplay, err)
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Environment IPC in the installed-release/pre-sweep-attempt gap: %v", err)
	}
	stopDaemon()
	authorityDB, err := sql.Open("sqlite", filepath.Join(fixture.root, "authority.db"))
	if err != nil {
		t.Fatalf("open disposable authority state for the sweep-gap assertion: %v", err)
	}
	defer func() { _ = authorityDB.Close() }()
	var sweepAttempts int
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND operation_name='batch.sweep-anonymous'`,
		m5TestDomain).Scan(&sweepAttempts); err != nil || sweepAttempts != 0 {
		t.Fatalf("claim release or shutdown created an automatic Step 7 sweep attempt: count=%d error=%v", sweepAttempts, err)
	}
	releaseGapPrefix, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true, true)
	afterReleaseRestart, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil || afterReleaseRestart != releaseGapPrefix {
		t.Fatalf("same-Environment restart changed the installed-release/pre-sweep gap: before=%+v after=%+v error=%v",
			releaseGapPrefix, afterReleaseRestart, err)
	}
	if err = authorityDB.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND operation_name='batch.sweep-anonymous'`,
		m5TestDomain).Scan(&sweepAttempts); err != nil || sweepAttempts != 0 {
		t.Fatalf("same-Environment restart created an automatic Step 7 sweep attempt: count=%d error=%v", sweepAttempts, err)
	}

	runFinishEvents := step10AuthorityEventPositions(t, authorityDB, "run.finished", runID)
	if len(runFinishEvents) != 1 {
		t.Fatalf("production Resume did not settle the completed Run exactly once: events=%d", len(runFinishEvents))
	}
	if err = client.Close(); err != nil {
		t.Fatalf("close Step 10 Environment IPC before final Step 7 sweep: %v", err)
	}
	stopDaemon()
	setM5ProcessCatalogue(t, environment.profileRoot, "m6-step7")
	client, stopDaemon, daemonOutput = startWipdForBirthReleaseRecovery(t, binary, environment.profileRoot, true)
	swept, err := client.SweepAnonymousBatch(ctx, m5TwoEnvironmentID(60), created.ID, claim.BatchID,
		closed.CommandID, operation.Actor("system:scheduler"))
	if err != nil || swept.Result.Code != operation.ResultSucceeded || swept.Result.Output != (operation.BatchSweepAnonymousOutput{
		Outcome: operation.BatchSweepAnonymousSwept,
	}) {
		t.Fatalf("Step 7 explicit sweep after installed closure and Run settlement = %+v, error=%v daemon=%s",
			swept, err, daemonOutput.String())
	}
	sweptReplay, err := client.SweepAnonymousBatch(ctx, m5TwoEnvironmentID(60), created.ID, claim.BatchID,
		closed.CommandID, operation.Actor("system:scheduler"))
	if err != nil || sweptReplay.Result.Code != swept.Result.Code || !reflect.DeepEqual(sweptReplay.Receipt, swept.Receipt) {
		t.Fatalf("same-ID Batch sweep did not replay the exact terminal receipt: result=%+v error=%v", sweptReplay, err)
	}
	beforeAlreadySwept, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	alreadySwept, err := client.SweepAnonymousBatch(ctx, m5TwoEnvironmentID(61), created.ID, claim.BatchID,
		closed.CommandID, operation.Actor("system:scheduler"))
	if err != nil || alreadySwept.Result.Code != operation.ResultSucceeded || alreadySwept.Result.Output != (operation.BatchSweepAnonymousOutput{
		Outcome: operation.BatchSweepAnonymousAlreadySwept,
	}) {
		t.Fatalf("fresh-ID explicit sweep replay should report already-swept without repeating effects: result=%+v error=%v", alreadySwept, err)
	}
	afterAlreadySwept, err := fixture.store.CurrentPrefixAnchor(ctx, m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	if afterAlreadySwept.EventCount != beforeAlreadySwept.EventCount {
		t.Fatalf("fresh-ID already-swept response appended authority events: before=%d after=%d",
			beforeAlreadySwept.EventCount, afterAlreadySwept.EventCount)
	}
}

func step10CloneEnvironmentProfile(t *testing.T, source m5ProcessEnvironment, destination string) string {
	t.Helper()
	profileRoot := filepath.Join(destination, "profile")
	clientStateRoot := filepath.Join(destination, "client-state")
	if err := copyStep10ProcessTree(source.profileRoot, profileRoot); err != nil {
		t.Fatalf("clone disposable Step 10 Environment profile: %v", err)
	}
	if err := copyStep10ProcessTree(source.clientStateRoot, clientStateRoot); err != nil {
		t.Fatalf("clone disposable Step 10 Environment client state: %v", err)
	}
	profilePath := filepath.Join(profileRoot, "connected-authority.json")
	profileBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read cloned connected Environment profile: %v", err)
	}
	var profile map[string]json.RawMessage
	if err = json.Unmarshal(profileBytes, &profile); err != nil || profile["client_state_directory"] == nil {
		t.Fatalf("decode cloned connected Environment profile: %v", err)
	}
	clientStatePath, err := json.Marshal(clientStateRoot)
	if err != nil {
		t.Fatal(err)
	}
	profile["client_state_directory"] = clientStatePath
	profileBytes, err = json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(profilePath, profileBytes, 0o600); err != nil {
		t.Fatalf("retarget cloned Environment profile to its disposable client state: %v", err)
	}
	return profileRoot
}

func copyStep10ProcessTree(source, destination string) error {
	root, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("source %q is not a real directory: %w", source, err)
	}
	if !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source %q is not a real directory", source)
	}
	if err = os.MkdirAll(destination, root.Mode().Perm()); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("profile path %q is not safely copyable: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("profile path %q is a symlink", path)
		}
		if info.IsDir() {
			if err = os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("profile path %q is not a regular file", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.WriteFile(target, content, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}

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

const (
	step10FaultBeforeResultReceiver int32 = 1
	step10FaultAfterAuthorityCommit int32 = 2
)

type step10HookResultFaults struct {
	mode             atomic.Int32
	dropped          atomic.Bool
	lastFrame        atomic.Value
	keepAliveQueries atomic.Int32
}

func interceptStep10HookResultFaults(next http.Handler, faults *step10HookResultFaults) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != labExchangePath {
			next.ServeHTTP(writer, request)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			next.ServeHTTP(writer, request)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		frame, err := wipdwire.ReadFrame(bytes.NewReader(body))
		if err == nil {
			faults.lastFrame.Store(fmt.Sprintf("%s/sequence-%d", frame.Kind, frame.Sequence))
			if frame.Kind == "receipt.query" && faults.mode.Load() == step10FaultBeforeResultReceiver {
				faults.keepAliveQueries.Add(1)
			}
		}
		if err != nil || frame.Kind != "scheduler.hook-result" {
			next.ServeHTTP(writer, request)
			return
		}
		switch faults.mode.Load() {
		case step10FaultBeforeResultReceiver:
			if faults.mode.CompareAndSwap(step10FaultBeforeResultReceiver, 0) {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case step10FaultAfterAuthorityCommit:
			if faults.mode.CompareAndSwap(step10FaultAfterAuthorityCommit, 0) {
				drop := &m5OrdinaryCommandResponseDrop{}
				drop.armed.Store(true)
				next.ServeHTTP(m5DropTerminalResponseWriter{ResponseWriter: writer, drop: drop}, request)
				faults.dropped.Store(drop.dropped.Load())
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func step10HookLedgerDB(profileRoot string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filepath.Join(profileRoot, "scheduler-hooks.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func step10HookSubmissionCount(t *testing.T, authorityPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND operation_name='scheduler.orchestrate'`, m5TestDomain).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func step10ClaimCountForBatch(t *testing.T, authorityPath, batchID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM claims WHERE domain_id=? AND batch_id=?`, m5TestDomain, batchID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func step10DispatchCountForBatch(t *testing.T, authorityPath, batchID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err = db.QueryRow(`SELECT count(DISTINCT dispatch_id) FROM claims WHERE domain_id=? AND batch_id=?`, m5TestDomain, batchID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func step10RoleSpawnCount(t *testing.T, authorityPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM submissions WHERE domain_id=? AND operation_name='role.spawn'`, m5TestDomain).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func installStep10EnvironmentResultFault(profileRoot string) error {
	db, err := step10HookLedgerDB(profileRoot)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TRIGGER fail_step10_environment_result BEFORE INSERT ON environment_hook_results
		BEGIN SELECT RAISE(ABORT,'injected post-engagement/pre-result crash'); END`)
	return err
}

func removeStep10EnvironmentResultFault(profileRoot string) error {
	db, err := step10HookLedgerDB(profileRoot)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DROP TRIGGER fail_step10_environment_result`)
	return err
}

func step10CommandJournalDB(profileRoot string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filepath.Join(profileRoot, "environment-journal", "command-journal.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func step10NextEnvironmentSequence(profileRoot string) (uint64, error) {
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var sequence uint64
	err = db.QueryRow(`SELECT next_environment_sequence FROM environment_state`).Scan(&sequence)
	return sequence, err
}

func step10AssertAskBeforeRoleClosure(t *testing.T, authorityPath, askCommandID string, roleIDs []string) {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var askPosition int64
	if err = db.QueryRow(`SELECT min(position) FROM authority_events WHERE domain_id=? AND command_id=?`,
		m5TestDomain, askCommandID).Scan(&askPosition); err != nil || askPosition <= 0 {
		t.Fatalf("read recovered Ask event position: position=%d error=%v", askPosition, err)
	}
	for _, roleID := range roleIDs {
		closePositions := step10AuthorityEventPositions(t, db, "role.closed", roleID)
		if len(closePositions) == 0 || closePositions[0] <= askPosition {
			var closePosition int64
			if len(closePositions) != 0 {
				closePosition = closePositions[0]
			}
			t.Fatalf("Resume did not install Ask result before closing Role %s: ask=%d close=%d", roleID, askPosition, closePosition)
		}
	}
}

func step10AssertClaimReleaseBeforeFreshAcquire(t *testing.T, authorityPath, oldClaimID, freshClaimID string) {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	releasedPositions := step10AuthorityEventPositions(t, db, "claim.released", oldClaimID)
	if len(releasedPositions) == 0 || releasedPositions[0] <= 0 {
		t.Fatalf("read old claim release position: positions=%v", releasedPositions)
	}
	acquiredPositions := step10AuthorityEventPositions(t, db, "claim.acquired", freshClaimID)
	if len(acquiredPositions) == 0 || acquiredPositions[0] <= releasedPositions[0] {
		t.Fatalf("fresh claim was not acquired after exact normal old-claim release: release=%v acquire=%v", releasedPositions, acquiredPositions)
	}
}

func step10VerifiedGatePosition(t *testing.T, authorityPath, matterID string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	positions := step10AuthorityEventPositions(t, db, "gate.closed", matterID)
	if len(positions) == 0 || positions[0] <= 0 {
		t.Fatalf("positive Verifier did not produce its Gate-close event: positions=%v", positions)
	}
	return positions[0]
}

func step10AuthorityEventPositions(t *testing.T, db *sql.DB, kind, subjectID string) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT position,record FROM authority_events WHERE domain_id=? ORDER BY position`, m5TestDomain)
	if err != nil {
		t.Fatalf("read authority event history: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var positions []int64
	for rows.Next() {
		var position int64
		var record []byte
		if err = rows.Scan(&position, &record); err != nil {
			t.Fatalf("scan authority event history: %v", err)
		}
		fields, decodeErr := wipdwire.DecodeCanonicalMap(record,
			"schema", "event_id", "domain_id", "command_id", "request_hash", "environment", "acted_at", "occurred_at", "kind", "subject_id", "repo_id", "payload")
		if decodeErr != nil {
			t.Fatalf("decode authority event at position %d: %v", position, decodeErr)
		}
		if fields["kind"] == kind && fields["subject_id"] == subjectID {
			positions = append(positions, position)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("iterate authority event history: %v", err)
	}
	return positions
}

func step10ClaimReleaseCommandID(t *testing.T, profileRoot, claimID string) string {
	t.Helper()
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var commandID string
	if err = db.QueryRow(`SELECT command_id FROM claim_journal_release_attempts
		WHERE claim_id=? AND state='returned' AND result_code=?`, claimID, string(operation.ResultSucceeded)).Scan(&commandID); err != nil || len(commandID) != 26 {
		t.Fatalf("read installed normal release identity for claim %s: command=%q error=%v", claimID, commandID, err)
	}
	return commandID
}

func installStep10ReceiptInstallFault(profileRoot string, sequence uint64) error {
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_step10_receipt BEFORE INSERT ON installed_receipts
		WHEN NEW.environment_sequence=%d
		BEGIN SELECT RAISE(ABORT,'injected Step birth receipt-install crash'); END`, sequence))
	if err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_step10_authority_receipt BEFORE INSERT ON authority_command_outcomes
		WHEN NEW.environment_sequence=%d
		BEGIN SELECT RAISE(ABORT,'injected authority result-install crash'); END`, sequence))
	return err
}

func removeStep10ReceiptInstallFault(profileRoot string) error {
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`DROP TRIGGER fail_step10_receipt`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`DROP TRIGGER fail_step10_authority_receipt`)
	return err
}

func step10CommandAtSequence(t *testing.T, profileRoot string, sequence uint64) (operation.Command, string) {
	t.Helper()
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var raw []byte
	var state string
	if err = db.QueryRow(`SELECT canonical_bytes,state FROM commands WHERE environment_sequence=?`, sequence).Scan(&raw, &state); err != nil {
		t.Fatalf("read production-selected Step birth at sequence %d: %v", sequence, err)
	}
	command, err := operation.DecodeCanonicalCommand(raw)
	if err != nil {
		t.Fatalf("decode production-selected Environment command at sequence %d: %v", sequence, err)
	}
	return command, state
}

func step10PendingBirthAtSequence(t *testing.T, profileRoot string, sequence uint64) operation.Command {
	t.Helper()
	command, state := step10CommandAtSequence(t, profileRoot, sequence)
	if state != "pending-return" || command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
		t.Fatalf("production-selected Step birth = operation %v state %q", command.Request.Operation, state)
	}
	return command
}

func step10PlanBirthOutputs(t *testing.T, profileRoot, planCommandID string) []operation.StepCreateOutput {
	t.Helper()
	db, err := step10CommandJournalDB(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT c.canonical_bytes,r.canonical_receipt FROM commands c
		JOIN installed_receipts r ON r.command_id=c.command_id
		WHERE c.delivery='claim' ORDER BY c.environment_sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var outputs []operation.StepCreateOutput
	for rows.Next() {
		var commandBytes, receiptBytes []byte
		if err = rows.Scan(&commandBytes, &receiptBytes); err != nil {
			t.Fatal(err)
		}
		command, decodeErr := operation.DecodeCanonicalCommand(commandBytes)
		if decodeErr != nil {
			t.Fatalf("decode Environment command journal entry: %v", decodeErr)
		}
		if command.CausationCommandID != planCommandID || command.Request.Operation != operation.StepCreateV2.Metadata().Operation {
			continue
		}
		receipt, decodeErr := wipdwire.DecodeCanonicalMap(receiptBytes,
			"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
		if decodeErr != nil {
			t.Fatalf("decode installed Step birth receipt: %v", decodeErr)
		}
		result, ok := receipt["result"].(map[string]any)
		outputBytes, outputOK := result["output"].([]byte)
		if !ok || !outputOK {
			t.Fatal("installed Step birth receipt has no typed output")
		}
		outputFields, decodeErr := wipdwire.DecodeCanonicalMap(outputBytes, "id", "parent_id", "matter_id", "locator", "title", "sort_key", "state")
		sortKey, sortOK := outputFields["sort_key"].(uint64)
		if decodeErr != nil || !sortOK {
			t.Fatalf("decode installed Step birth output: %v", decodeErr)
		}
		output := operation.StepCreateOutput{
			ID: outputFields["id"].(string), ParentID: outputFields["parent_id"].(string),
			MatterID: outputFields["matter_id"].(string), Locator: outputFields["locator"].(string),
			Title: outputFields["title"].(string), SortKey: int64(sortKey), State: outputFields["state"].(string),
		}
		outputs = append(outputs, output)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return outputs
}

func step10HookLedgerState(t *testing.T, profileRoot, commandID string) (string, bool) {
	t.Helper()
	db, err := step10HookLedgerDB(profileRoot)
	if err != nil {
		t.Fatalf("open disposable Step 10 ledger: %v", err)
	}
	defer func() { _ = db.Close() }()
	var state string
	if err = db.QueryRow(`SELECT state FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		m5TestDomain, commandID).Scan(&state); err != nil {
		t.Fatalf("read durable Step 10 engagement: %v", err)
	}
	var installed int
	if err = db.QueryRow(`SELECT count(*) FROM environment_hook_results WHERE domain_id=? AND environment_id=? AND command_id=?`,
		m5TestDomain, m5TestEnv, commandID).Scan(&installed); err != nil {
		t.Fatalf("read installed Step 10 result: %v", err)
	}
	return state, installed == 1
}

func step10AssertReceiverRefusals(t *testing.T, fixture *m5CommandFixture, profileRoot string, command operation.Command) {
	t.Helper()
	client, err := fixture.profile.HTTPClientWithCertificate(fixture.serverRoots, &fixture.clientCert)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	var connection net.Conn
	hello, err := wipdwire.EncodeCanonical(map[string]any{
		"protocol_min": []any{uint64(1), uint64(0)}, "protocol_max": []any{uint64(1), uint64(0)},
		"identity_schemas": []any{"wipd.command/1"},
		"operations": []any{map[string]any{
			"name": "scheduler.orchestrate", "versions": []any{uint64(1)},
			"identity_schemas": []any{"wipd.command/1"},
		}},
		"store_schemas": []any{"wipd.store/1"}, "features": []any{"wipd.frame/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	negotiated := m5HTTPSExchange(t, client, fixture.profile, labNegotiatePath, "client.hello", hello, &connection)
	if len(negotiated) != 2 || negotiated[0].Kind != "server.hello" || negotiated[1].Kind != "session.parameters" {
		t.Fatalf("Step 10 receiver negotiation = %+v", negotiated)
	}
	db, err := step10HookLedgerDB(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	var inputDigest, resultDigest string
	var typedBytes []byte
	if err = db.QueryRow(`SELECT input_digest,result_digest,result FROM environment_hook_results
		WHERE domain_id=? AND environment_id=? AND command_id=?`, m5TestDomain, m5TestEnv, command.ID).
		Scan(&inputDigest, &resultDigest, &typedBytes); err != nil {
		_ = db.Close()
		t.Fatalf("read exact installed Builder result for receiver refusal probes: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var typed schedulerhook.TypedResult
	if err = wipdwire.DecodeCanonical(typedBytes, &typed, "kind", "version", "work", "plan", "ask"); err != nil {
		t.Fatalf("decode installed Builder result: %v", err)
	}
	output, err := schedulerhook.OperationOutput(schedulerhook.Result{
		Key:         schedulerhook.Key{DomainID: m5TestDomain, CommandID: command.ID},
		InputDigest: inputDigest, Typed: typed, ResultDigest: resultDigest,
	})
	if err != nil {
		t.Fatalf("convert exact installed Builder result: %v", err)
	}
	sendResult := func(submitted wipdwire.SchedulerHookResult) []wipdwire.Frame {
		t.Helper()
		payload, encodeErr := wipdwire.EncodeCanonical(submitted)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "scheduler.hook-result", payload, &connection)
	}
	canonical, err := command.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := wipdwire.EncodeCanonical(output)
	if err != nil {
		t.Fatal(err)
	}
	submitted := wipdwire.SchedulerHookResult{
		Schema: "wipd.scheduler-hook-result/1", DomainID: m5TestDomain, Epoch: 1,
		EnvironmentID: m5TestEnv, CommandID: command.ID, RequestHash: requestHash,
		CanonicalCommand: canonical, Result: resultBytes,
	}
	before, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil {
		t.Fatal(err)
	}
	staleEpoch := submitted
	staleEpoch.Epoch++
	frames := sendResult(staleEpoch)
	if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "protocol.malformed-message" {
		t.Fatalf("stale-epoch authenticated hook result = %+v", frames)
	}
	substituted := command
	substituted.EnvironmentSequence++
	substitutedCanonical, err := substituted.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	substitutedHash, err := substituted.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	substitutedResult := submitted
	substitutedResult.CanonicalCommand = substitutedCanonical
	substitutedResult.RequestHash = substitutedHash
	frames = sendResult(substitutedResult)
	if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "command.id-conflict" {
		t.Fatalf("same-ID substituted-context authenticated hook result = %+v", frames)
	}
	db, err = step10HookLedgerDB(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	baseCommand, err := command.CanonicalBytes()
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	unsupportedCases := []struct {
		name  string
		field string
		value any
	}{
		{name: "adapter", field: "adapter_id", value: "unapproved.external-agent"},
		{name: "kind", field: "kind", value: "agent"},
		{name: "version", field: "adapter_version", value: uint64(2)},
	}
	for index, unsupportedCase := range unsupportedCases {
		rawCommand, decodeErr := wipdwire.DecodeCanonicalMap(baseCommand,
			"schema", "command_id", "authority", "environment", "acted_at", "actor", "causation_command_id",
			"correlation_command_id", "operation", "context", "claim", "input", "blobs")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		rawInput, ok := rawCommand["input"].(map[string]any)
		if !ok {
			t.Fatalf("raw Step 10 input has type %T", rawCommand["input"])
		}
		rawInput[unsupportedCase.field] = unsupportedCase.value
		rawCommand["input"] = rawInput
		unsupportedID, idErr := randomULID(time.Now().UTC().Add(time.Duration(index+1) * time.Second))
		if idErr != nil {
			t.Fatal(idErr)
		}
		rawCommand["command_id"] = unsupportedID
		rawCommand["correlation_command_id"] = unsupportedID
		unsupportedCanonical, encodeErr := wipdwire.EncodeCanonical(rawCommand)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		requestHashBytes := sha256.Sum256(append([]byte("wipd/request-hash/v1\x00"), unsupportedCanonical...))
		unsupportedHash := "sha256:" + hex.EncodeToString(requestHashBytes[:])
		unsupportedPayload, encodeErr := wipdwire.EncodeCanonical(wipdwire.CommandSubmit{
			Schema: "wipd.command-submit/1", CanonicalCommand: unsupportedCanonical, RequestHash: unsupportedHash,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		frames = m5HTTPSExchange(t, client, fixture.profile, labExchangePath, "command.submit", unsupportedPayload, &connection)
		if len(frames) != 1 || frames[0].Kind != "problem" || m5ProblemCode(t, frames[0]) != "protocol.malformed-message" {
			t.Fatalf("raw unsupported %s submission = %+v", unsupportedCase.name, frames)
		}
		if _, err = fixture.store.QueryCommand(context.Background(), m5TestDomain, unsupportedID, unsupportedHash,
			1, fixture.peer, m5TestEnv, time.Now().UTC()); err == nil {
			t.Fatalf("raw unsupported %s request was admitted despite receiver refusal", unsupportedCase.name)
		}
		db, err = step10HookLedgerDB(profileRoot)
		if err != nil {
			t.Fatal(err)
		}
		var engagements int
		if err = db.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
			m5TestDomain, unsupportedID).Scan(&engagements); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		if engagements != 0 {
			t.Fatalf("raw unsupported %s request launched %d adapter engagements", unsupportedCase.name, engagements)
		}
	}
	after, err := fixture.store.CurrentPrefixAnchor(context.Background(), m5TestDomain)
	if err != nil || after != before {
		t.Fatalf("receiver refusals changed authority history: before=%+v after=%+v error=%v", before, after, err)
	}
	status, err := fixture.store.QueryCommand(context.Background(), m5TestDomain, command.ID, requestHash,
		1, fixture.peer, m5TestEnv, time.Now().UTC())
	if err != nil || !status.Pending || len(status.Receipt) != 0 {
		t.Fatalf("refusal probes consumed or changed the original pending command: status=%+v error=%v", status, err)
	}
}
