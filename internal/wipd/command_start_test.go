package wipd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	commandStartRepoID        = "01KZ7XHAQT1S46NYPN1PW1DX3C"
	commandStartDomainID      = "01KZ7XHAQT1S46NYPN1PW1DX3B"
	commandStartEnvironmentID = "01KZ7XHAQT1S46NYPN1PW1DX3D"
	commandStartCommandPrefix = "01KZ7XHAQT1S46NYPN1PW1DX"
)

type commandStartTrace struct {
	mu    sync.Mutex
	steps []string
}

func (trace *commandStartTrace) add(step string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.steps = append(trace.steps, step)
}

func (trace *commandStartTrace) all() []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string(nil), trace.steps...)
}

type commandStartTracedEnvironment struct {
	mu          sync.Mutex
	journal     *wipdjournal.Journal
	durable     *JournalCommandStartEnvironment
	currentID   string
	trace       *commandStartTrace
	failInstall error
}

func (environment *commandStartTracedEnvironment) Snapshot(ctx context.Context) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	currentID := environment.currentID
	environment.mu.Unlock()
	if currentID != "" {
		if _, err := environment.journal.Get(currentID); err != nil {
			return CommandStartSnapshot{}, fmt.Errorf("snapshot preceded durable command admission: %w", err)
		}
		environment.trace.add("snapshot:" + currentID)
	} else {
		environment.trace.add("stable-snapshot")
	}
	return environment.durable.Snapshot(ctx)
}

func (environment *commandStartTracedEnvironment) InstallFold(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry, fold CommandFold) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if environment.failInstall != nil {
		return CommandStartSnapshot{}, environment.failInstall
	}
	installed, err := environment.durable.InstallFold(ctx, expected, entry, fold)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.trace.add("install-fold+receipt+tail+overlay:" + entry.Command.ID)
	return installed, nil
}

func (environment *commandStartTracedEnvironment) InstallPull(ctx context.Context, expected CommandStartSnapshot, pull CommandPull) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if environment.failInstall != nil {
		return CommandStartSnapshot{}, environment.failInstall
	}
	installed, err := environment.durable.InstallPull(ctx, expected, pull)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.trace.add("install-pull+overlay")
	return installed, nil
}

func (environment *commandStartTracedEnvironment) AdmitPending(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if environment.failInstall != nil {
		return CommandStartSnapshot{}, environment.failInstall
	}
	installed, err := environment.durable.AdmitPending(ctx, expected, entry)
	if err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.trace.add("admit+overlay:" + entry.Command.ID)
	return installed, nil
}

func (environment *commandStartTracedEnvironment) commandStartJournal() *wipdjournal.Journal {
	return environment.journal
}

func (environment *commandStartTracedEnvironment) setCurrentID(commandID string) {
	environment.mu.Lock()
	environment.currentID = commandID
	environment.mu.Unlock()
}

type commandStartFakeAuthority struct {
	trace          *commandStartTrace
	returnResults  []operation.ResultCode
	returnContinue []bool
	returnIndex    int
	pullEventID    string
	pullFailure    error
}

func (authority *commandStartFakeAuthority) Return(_ context.Context, entry wipdjournal.Entry, start wipdwire.PrefixAnchor) (CommandFold, error) {
	index := authority.returnIndex
	authority.returnIndex++
	if index >= len(authority.returnResults) {
		return CommandFold{}, errors.New("unexpected authority return")
	}
	eventID := nextCommandStartEventID(start)
	record := commandStartEventRecordForEntry(eventID, entry)
	transfer, err := commandStartVerifiedTransfer(start, []wipdwire.EventRecord{{EventID: eventID, Record: record}})
	if err != nil {
		return CommandFold{}, err
	}
	end := transfer.End()
	code := authority.returnResults[index]
	continueReturn := authority.returnContinue[index]
	authority.trace.add("return:" + entry.Command.ID)
	ids := transfer.EventIDs()
	if code != operation.ResultSucceeded {
		transfer, err = commandStartVerifiedTransfer(start, nil)
		if err != nil {
			return CommandFold{}, err
		}
		end = transfer.End()
		ids = nil
	}
	receipt, err := commandStartReceipt(entry, code, ids)
	if err != nil {
		return CommandFold{}, err
	}
	return CommandFold{
		DomainID: entry.Command.AuthorityDomainID, Epoch: entry.Command.ExpectedAuthorityEpoch,
		CommandID: entry.Command.ID, RequestHash: entry.RequestHash, EnvironmentID: entry.Command.EnvironmentID,
		EnvironmentSeq: entry.EnvironmentSeq, JournalPosition: entry.JournalPosition,
		Start: start, End: end, ResultCode: code, Continue: continueReturn,
		CanonicalReceipt: receipt, Manifest: transfer.Manifest(), VerifiedTransfer: transfer,
	}, nil
}

func (authority *commandStartFakeAuthority) Pull(_ context.Context, start wipdwire.PrefixAnchor) (CommandPull, error) {
	if authority.pullFailure != nil {
		return CommandPull{}, authority.pullFailure
	}
	eventID := authority.pullEventID
	if eventID <= commandStartAnchorEventID(start) {
		eventID = nextCommandStartEventID(start)
	}
	record := commandStartEventRecord(eventID, commandStartCommandPrefix+"99", commandStartHashForTest(), commandStartEnvironmentID, 99)
	transfer, err := commandStartVerifiedTransfer(start, []wipdwire.EventRecord{{EventID: eventID, Record: record}})
	if err != nil {
		return CommandPull{}, err
	}
	end := transfer.End()
	authority.trace.add("pull:" + fmt.Sprint(start.EventCount))
	return CommandPull{
		DomainID: commandStartDomainID, Epoch: 7, Start: start, End: end,
		Manifest: transfer.Manifest(), VerifiedTransfer: transfer,
	}, nil
}

func newCommandStartFixture(t *testing.T) (*CommandStartCoordinator, *wipdjournal.Journal, *commandStartTracedEnvironment, *commandStartFakeAuthority, *commandStartTrace, *Server) {
	t.Helper()
	journal, err := wipdjournal.Open(t.TempDir()+"/journal", wipdjournal.Identity{
		RepoID: commandStartRepoID, DomainID: commandStartDomainID, AuthorityEpoch: 7, EnvironmentID: commandStartEnvironmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	trace := &commandStartTrace{}
	environment := newCommandStartTestEnvironment(t, journal, trace, commandStartCommandPrefix+"01")
	authority := &commandStartFakeAuthority{
		trace: trace, pullEventID: "01KZ7XHAQT1S46NYPN1PW1DX43",
	}
	server := newServer(operation.NewRegistry(), 4)
	coordinator, err := server.NewCommandStartCoordinator(commandStartDomainID, journal, authority, environment)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, journal, environment, authority, trace, server
}

func newCommandStartTestEnvironment(t *testing.T, journal *wipdjournal.Journal, trace *commandStartTrace, currentID string) *commandStartTracedEnvironment {
	t.Helper()
	durable, err := NewJournalCommandStartEnvironment(journal)
	if err != nil {
		t.Fatal(err)
	}
	return &commandStartTracedEnvironment{journal: journal, durable: durable, trace: trace, currentID: currentID}
}

func commandStartInput(id, locator string) wipdjournal.CommandInput {
	return wipdjournal.CommandInput{
		ID: id,
		Request: operation.Request{
			Operation: operation.MatterCreateV1.Metadata().Operation,
			Actor:     operation.Actor("human"),
			Context:   operation.Context{Repo: commandStartRepoID},
			Input:     operation.MatterCreateInput{Title: "Title " + locator, Locator: locator},
		},
	}
}

func TestConnectedCommandStartReturnsPendingPrefixBeforeTailAndWrite(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	for _, command := range []struct{ id, locator string }{
		{commandStartCommandPrefix + "11", "older-one"},
		{commandStartCommandPrefix + "12", "older-two"},
	} {
		if _, err := journal.PrepareCommand(commandStartInput(command.id, command.locator)); err != nil {
			t.Fatal(err)
		}
		admitCommandStartTestEntry(t, journal, command.id)
	}
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded, operation.ResultSucceeded}
	authority.returnContinue = []bool{true, false}
	environment.currentID = commandStartCommandPrefix + "13"
	var guardRan bool
	result, err := coordinator.RunConnected(context.Background(), commandStartInput(environment.currentID, "current"), func(_ context.Context, snapshot CommandStartSnapshot, command operation.Command) error {
		guardRan = true
		if snapshot.Revision != 7 || snapshot.Anchor.EventCount != 3 || snapshot.ManifestDigest == "" {
			return fmt.Errorf("guard received unstable post-install snapshot: %+v", snapshot)
		}
		trace.add("guard+write:" + command.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Returned || result.Entry.Command.ID != environment.currentID || !guardRan {
		t.Fatalf("command-start result=%+v guardRan=%v; want new command after sync", result, guardRan)
	}
	want := []string{
		"snapshot:" + environment.currentID,
		"return:" + commandStartCommandPrefix + "11",
		"install-fold+receipt+tail+overlay:" + commandStartCommandPrefix + "11",
		"return:" + commandStartCommandPrefix + "12",
		"install-fold+receipt+tail+overlay:" + commandStartCommandPrefix + "12",
		"pull:2", "install-pull+overlay",
		"admit+overlay:" + environment.currentID,
		"guard+write:" + environment.currentID,
	}
	if got := trace.all(); !equalCommandStartTrace(got, want) {
		t.Fatalf("pending command-start trace = %v, want %v", got, want)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if anchor := installed.Anchor; anchor.EventCount != 3 || anchor.EventID == nil || *anchor.EventID != authority.pullEventID {
		t.Fatalf("installed authority prefix = %+v, want both folds then pull tail", anchor)
	}
}

func TestConnectedCommandStartWithoutPendingPullsBeforeGuard(t *testing.T) {
	coordinator, _, environment, _, trace, _ := newCommandStartFixture(t)
	currentID := commandStartCommandPrefix + "21"
	environment.currentID = currentID
	guardRan := false
	result, err := coordinator.RunConnected(context.Background(), commandStartInput(currentID, "only"), func(_ context.Context, snapshot CommandStartSnapshot, command operation.Command) error {
		guardRan = true
		if snapshot.Revision != 3 || snapshot.Anchor.EventCount != 1 || snapshot.ManifestDigest == "" {
			return fmt.Errorf("guard received unstable post-install snapshot: %+v", snapshot)
		}
		trace.add("guard+write:" + command.ID)
		return nil
	})
	if err != nil || result.Returned || !guardRan {
		t.Fatalf("command-start result=%+v guardRan=%v err=%v", result, guardRan, err)
	}
	want := []string{
		"snapshot:" + currentID,
		"pull:0", "install-pull+overlay", "admit+overlay:" + currentID,
		"guard+write:" + currentID,
	}
	if got := trace.all(); !equalCommandStartTrace(got, want) {
		t.Fatalf("empty-prefix command-start trace = %v, want %v", got, want)
	}
}

func TestConnectedCanonicalMatterAndStepBirthReplayThroughTerminalCoordinator(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded, operation.ResultSucceeded}
	authority.returnContinue = []bool{false, false}

	matter := commandStartCanonicalCommand(commandStartCommandPrefix+"73", 1, "", commandStartCommandPrefix+"73",
		operation.MatterCreateV1.Metadata().Operation, operation.MatterCreateInput{Title: "M5 birth", Locator: "m5-birth"}, nil)
	environment.currentID = matter.ID
	matterResult, err := coordinator.RunConnectedCanonicalTerminal(context.Background(), matter, func(_ context.Context, snapshot CommandStartSnapshot, got operation.Command) error {
		if snapshot.Anchor.EventCount != 1 || got.ID != matter.ID {
			return fmt.Errorf("Matter callback preceded command-start pull: snapshot=%+v command=%s", snapshot, got.ID)
		}
		trace.add("matter-guard-after-pull")
		return nil
	})
	if err != nil || !matterResult.Returned || matterResult.SemanticResult.Code != operation.ResultSucceeded {
		t.Fatalf("Matter terminal result=%+v err=%v", matterResult, err)
	}
	matterOutput, ok := matterResult.SemanticResult.Output.(operation.MatterCreateOutput)
	if !ok || matterOutput.ID != commandStartCommandPrefix+"44" || matterOutput.Locator != "m5-birth" || matterOutput.Title != "M5 birth" {
		t.Fatalf("Matter authority output=%#v", matterResult.SemanticResult.Output)
	}

	step := commandStartCanonicalCommand(commandStartCommandPrefix+"74", 2, matter.ID, matter.ID,
		operation.StepCreateV1.Metadata().Operation, operation.StepCreateInput{ParentID: matterOutput.ID, Title: "First Step"},
		&operation.ClaimContext{ID: matterOutput.ID, Epoch: "1"})
	environment.currentID = step.ID
	stepResult, err := coordinator.RunConnectedCanonicalTerminal(context.Background(), step, func(_ context.Context, snapshot CommandStartSnapshot, got operation.Command) error {
		if snapshot.Anchor.EventCount != 3 || got.ID != step.ID {
			return fmt.Errorf("Step callback preceded command-start pull: snapshot=%+v command=%s", snapshot, got.ID)
		}
		trace.add("step-guard-after-pull")
		return nil
	})
	if err != nil || !stepResult.Returned || stepResult.SemanticResult.Code != operation.ResultSucceeded {
		t.Fatalf("Step terminal result=%+v err=%v", stepResult, err)
	}
	stepOutput, ok := stepResult.SemanticResult.Output.(operation.StepCreateOutput)
	if !ok || stepOutput.ID != commandStartCommandPrefix+"46" || stepOutput.ParentID != matterOutput.ID ||
		stepOutput.MatterID != matterOutput.ID || stepOutput.Locator != "step-01" || stepOutput.Title != "First Step" || stepOutput.SortKey != 1000 || stepOutput.State != "planned" {
		t.Fatalf("Step authority output=%#v", stepResult.SemanticResult.Output)
	}
	if matterResult.Entry.RequestHash != mustCommandStartHash(t, matter) || stepResult.Entry.RequestHash != mustCommandStartHash(t, step) {
		t.Fatalf("daemon journal changed canonical command identities: Matter=%s Step=%s", matterResult.Entry.RequestHash, stepResult.Entry.RequestHash)
	}

	stepEntry, err := journal.Get(step.ID)
	if err != nil || stepEntry.State != wipdjournal.StateReturned || stepEntry.Command.CausationCommandID != matter.ID ||
		stepEntry.Command.CorrelationCommandID != matter.ID || stepEntry.Command.Request.Claim == nil || stepEntry.Command.Request.Claim.ID != matterOutput.ID {
		t.Fatalf("durable Step command binding=%+v err=%v", stepEntry, err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil || installed.Anchor.EventCount != 4 {
		t.Fatalf("installed Matter+Step prefix=%+v err=%v", installed.Anchor, err)
	}
	stepReceipt := append([]byte(nil), stepResult.Receipt...)
	_, err = coordinator.RunConnectedCanonicalTerminal(context.Background(), step, func(context.Context, CommandStartSnapshot, operation.Command) error {
		t.Fatal("exact terminal replay reran the guard")
		return nil
	})
	if err != nil || authority.returnIndex != 2 {
		t.Fatalf("exact Step replay returned again: returnIndex=%d err=%v", authority.returnIndex, err)
	}
	replayedEntry, err := journal.InstallSnapshot(context.Background())
	if err != nil || replayedEntry.Anchor.EventCount != 5 || len(replayedEntry.Receipts) != 2 {
		t.Fatalf("exact replay duplicated a birth fold or receipt: anchor=%+v receipts=%d err=%v", replayedEntry.Anchor, len(replayedEntry.Receipts), err)
	}
	if !bytes.Equal(stepReceipt, replayedEntry.Receipts[step.ID].CanonicalReceipt) {
		t.Fatal("exact Step replay changed the installed terminal receipt")
	}
	if got := trace.all(); !containsCommandStartTrace(got, "matter-guard-after-pull") || !containsCommandStartTrace(got, "step-guard-after-pull") {
		t.Fatalf("terminal coordinator omitted post-pull birth guards: %v", got)
	}
	conflict := step
	conflict.Request.Input = operation.StepCreateInput{ParentID: matterOutput.ID, Title: "Different intent"}
	if _, err = coordinator.RunConnectedCanonicalTerminal(context.Background(), conflict, func(context.Context, CommandStartSnapshot, operation.Command) error {
		t.Fatal("conflicting Step identity reached guard")
		return nil
	}); !errors.Is(err, wipdjournal.ErrCommandIDConflict) || authority.returnIndex != 2 {
		t.Fatalf("conflicting Step replay err=%v returnIndex=%d", err, authority.returnIndex)
	}
}

func TestCommandStartPullFailureAndRestartDoNotReturnUnadmittedHead(t *testing.T) {
	root := t.TempDir() + "/journal"
	identity := wipdjournal.Identity{RepoID: commandStartRepoID, DomainID: commandStartDomainID, AuthorityEpoch: 7, EnvironmentID: commandStartEnvironmentID}
	journal, err := wipdjournal.Open(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	trace := &commandStartTrace{}
	firstID := commandStartCommandPrefix + "71"
	environment := newCommandStartTestEnvironment(t, journal, trace, firstID)
	authority := &commandStartFakeAuthority{trace: trace, pullEventID: commandStartCommandPrefix + "43", pullFailure: errors.New("injected pull failure")}
	server := newServer(operation.NewRegistry(), 4)
	coordinator, err := server.NewCommandStartCoordinator(commandStartDomainID, journal, authority, environment)
	if err != nil {
		t.Fatal(err)
	}
	firstInput := commandStartInput(firstID, "pull-fails")
	guardRan := false
	if _, err = coordinator.RunConnected(context.Background(), firstInput, func(context.Context, CommandStartSnapshot, operation.Command) error {
		guardRan = true
		return nil
	}); err == nil || guardRan {
		t.Fatalf("first command pull failure: guardRan=%v err=%v; want failure before guard", guardRan, err)
	}
	first, err := journal.Get(firstID)
	if err != nil || first.State != wipdjournal.StatePreAdmission {
		t.Fatalf("failed command disposition = %+v, %v; want durable pre-admission identity", first, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = wipdjournal.Open(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	trace = &commandStartTrace{}
	environment = newCommandStartTestEnvironment(t, journal, trace, commandStartCommandPrefix+"72")
	authority = &commandStartFakeAuthority{trace: trace, pullEventID: commandStartCommandPrefix + "43"}
	server = newServer(operation.NewRegistry(), 4)
	coordinator, err = server.NewCommandStartCoordinator(commandStartDomainID, journal, authority, environment)
	if err != nil {
		t.Fatal(err)
	}
	secondGuardRan := false
	if _, err = coordinator.RunConnected(context.Background(), commandStartInput(environment.currentID, "later"), func(context.Context, CommandStartSnapshot, operation.Command) error {
		secondGuardRan = true
		return nil
	}); !errors.Is(err, ErrCommandStartBlocked) || secondGuardRan || authority.returnIndex != 0 {
		t.Fatalf("later command crossed pre-admission head: guardRan=%v returns=%d err=%v", secondGuardRan, authority.returnIndex, err)
	}
	if got := trace.all(); !equalCommandStartTrace(got, []string{"snapshot:" + environment.currentID}) {
		t.Fatalf("later command trace = %v; want blocked before return/pull", got)
	}

	environment.currentID = firstID
	resumedGuardRan := false
	if _, err = coordinator.RunConnected(context.Background(), firstInput, func(_ context.Context, snapshot CommandStartSnapshot, _ operation.Command) error {
		resumedGuardRan = true
		if snapshot.Revision != 3 || snapshot.Anchor.EventCount != 1 {
			return fmt.Errorf("resumed command received unexpected admitted snapshot: %+v", snapshot)
		}
		return nil
	}); err != nil || !resumedGuardRan {
		t.Fatalf("exact retry could not recover pre-admission work: guardRan=%v err=%v", resumedGuardRan, err)
	}
	first, err = journal.Get(firstID)
	if err != nil || first.State != wipdjournal.StatePendingReturn {
		t.Fatalf("resumed command disposition = %+v, %v; want pending-return only after overlay commit", first, err)
	}
}

func TestConnectedCommandStartRefusalInstallsReceiptThenBlocksPull(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	pendingID := commandStartCommandPrefix + "31"
	if _, err := journal.PrepareCommand(commandStartInput(pendingID, "refused")); err != nil {
		t.Fatal(err)
	}
	admitCommandStartTestEntry(t, journal, pendingID)
	currentID := commandStartCommandPrefix + "32"
	environment.currentID = currentID
	authority.returnResults = []operation.ResultCode{operation.ResultRejected}
	authority.returnContinue = []bool{false}
	guardRan := false
	_, err := coordinator.RunConnected(context.Background(), commandStartInput(currentID, "blocked"), func(context.Context, CommandStartSnapshot, operation.Command) error {
		guardRan = true
		return nil
	})
	if !errors.Is(err, ErrCommandStartBlocked) || guardRan {
		t.Fatalf("refused-prefix result: guardRan=%v err=%v, want blocked before pull/write", guardRan, err)
	}
	want := []string{
		"snapshot:" + currentID,
		"return:" + pendingID,
		"install-fold+receipt+tail+overlay:" + pendingID,
	}
	if got := trace.all(); !equalCommandStartTrace(got, want) {
		t.Fatalf("refused pending command trace = %v, want %v", got, want)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if receipt, ok := installed.Receipts[pendingID]; !ok || receipt.ResultCode != operation.ResultRejected {
		t.Fatalf("non-success receipt was not atomically installed: %+v", installed.Receipts)
	}
}

func TestConnectedCommandStartRetryReturnsWithoutRepeatingCallback(t *testing.T) {
	coordinator, _, environment, authority, trace, _ := newCommandStartFixture(t)
	commandID := commandStartCommandPrefix + "41"
	environment.currentID = commandID
	input := commandStartInput(commandID, "retry")
	callbacks := 0
	if _, err := coordinator.RunConnected(context.Background(), input, func(context.Context, CommandStartSnapshot, operation.Command) error {
		callbacks++
		trace.add("first-guard+write")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded}
	authority.returnContinue = []bool{false}
	result, err := coordinator.RunConnected(context.Background(), input, func(context.Context, CommandStartSnapshot, operation.Command) error {
		callbacks++
		return nil
	})
	if err != nil || !result.Returned || len(result.Receipt) == 0 || callbacks != 1 {
		t.Fatalf("exact retry result=%+v callbacks=%d err=%v; want one callback and stored return", result, callbacks, err)
	}
	want := []string{
		"snapshot:" + commandID, "pull:0", "install-pull+overlay", "admit+overlay:" + commandID,
		"first-guard+write", "snapshot:" + commandID, "return:" + commandID,
		"install-fold+receipt+tail+overlay:" + commandID, "pull:2", "install-pull+overlay",
	}
	if got := trace.all(); !equalCommandStartTrace(got, want) {
		t.Fatalf("retry trace = %v, want %v", got, want)
	}
}

func TestConnectedCommandStartAtomicFoldInstallFailureStopsBeforePull(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	pendingID := commandStartCommandPrefix + "61"
	if _, err := journal.PrepareCommand(commandStartInput(pendingID, "install-fails")); err != nil {
		t.Fatal(err)
	}
	admitCommandStartTestEntry(t, journal, pendingID)
	currentID := commandStartCommandPrefix + "62"
	environment.currentID = currentID
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded}
	authority.returnContinue = []bool{false}
	wantErr := errors.New("atomic Environment fold commit failed")
	environment.failInstall = wantErr
	guardRan := false
	_, err := coordinator.RunConnected(context.Background(), commandStartInput(currentID, "not-run"), func(context.Context, CommandStartSnapshot, operation.Command) error {
		guardRan = true
		return nil
	})
	if !errors.Is(err, wantErr) || guardRan {
		t.Fatalf("failed fold install: guardRan=%v err=%v; want atomic failure before pull/write", guardRan, err)
	}
	installed, snapshotErr := journal.InstallSnapshot(context.Background())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if installed.Anchor.EventCount != 0 || len(installed.Receipts) != 0 {
		t.Fatalf("failed atomic install changed Environment state: %+v", installed)
	}
	want := []string{"snapshot:" + currentID, "return:" + pendingID}
	if got := trace.all(); !equalCommandStartTrace(got, want) {
		t.Fatalf("failed atomic install trace = %v, want %v", got, want)
	}
}

func TestConnectedCommandStartReplaysInstalledNonSuccessReceipt(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	commandID := commandStartCommandPrefix + "42"
	environment.currentID = commandID
	input := commandStartInput(commandID, "terminal-refusal")
	entry, err := journal.PrepareCommand(input)
	if err != nil {
		t.Fatal(err)
	}
	admitCommandStartTestEntry(t, journal, commandID)
	receipt, err := commandStartReceipt(entry, operation.ResultRejected, nil)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := commandStartVerifiedTransfer(installed.Anchor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.InstallFold(context.Background(), installed.Expectation(), entry, operation.ResultRejected, receipt, transfer); err != nil {
		t.Fatal(err)
	}
	guardRan := false
	result, err := coordinator.RunConnected(context.Background(), input, func(context.Context, CommandStartSnapshot, operation.Command) error {
		guardRan = true
		return nil
	})
	if err != nil || !result.Returned || result.ResultCode != operation.ResultRejected || string(result.Receipt) != string(receipt) || guardRan {
		t.Fatalf("terminal exact retry=%+v guardRan=%v err=%v; want stored refusal without execution", result, guardRan, err)
	}
	if len(authority.returnResults) != 0 || len(trace.all()) != 1 || trace.all()[0] != "snapshot:"+commandID {
		t.Fatalf("terminal retry did work after installed refusal: returns=%v trace=%v", authority.returnResults, trace.all())
	}
}

func TestConnectedCommandStartSerializesAdmissionAndPullThroughSharedDomainLane(t *testing.T) {
	coordinator, journal, environment, _, trace, server := newCommandStartFixture(t)
	currentID := commandStartCommandPrefix + "51"
	environment.currentID = currentID
	release, acquired := server.executionLanes.acquire(context.Background(), commandStartDomainID)
	if !acquired {
		t.Fatal("failed to acquire test domain lane")
	}
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.RunConnected(context.Background(), commandStartInput(currentID, "lane"), func(context.Context, CommandStartSnapshot, operation.Command) error {
			active, _ := server.executionLanes.occupancy(commandStartDomainID)
			if active != 1 {
				return fmt.Errorf("guard/write ran outside shared lane: active=%d", active)
			}
			return nil
		})
		done <- err
	}()
	waitForLaneOccupancy(t, server.executionLanes, commandStartDomainID, 1, 1)
	if _, err := journal.Get(currentID); !errors.Is(err, wipdjournal.ErrNotFound) {
		release()
		t.Fatalf("command was admitted before acquiring its domain lane: err=%v", err)
	}
	if got := trace.all(); len(got) != 0 {
		release()
		t.Fatalf("sync began while the shared domain lane was held: %v", got)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("command start after lane release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command start did not proceed after lane release")
	}
	if got := trace.all(); len(got) == 0 || got[0] != "snapshot:"+currentID {
		t.Fatalf("trace after lane release = %v, want snapshot first", got)
	}
}

func TestConnectedCommandStartStableReadUsesSharedDomainLane(t *testing.T) {
	coordinator, _, environment, _, trace, server := newCommandStartFixture(t)
	environment.currentID = ""
	if err := coordinator.WithStableReadSnapshot(context.Background(), func(_ context.Context, snapshot CommandStartSnapshot) error {
		active, _ := server.executionLanes.occupancy(commandStartDomainID)
		if active != 1 || snapshot.DomainID != commandStartDomainID || snapshot.Anchor.EventCount != 0 || snapshot.Revision != 1 {
			return fmt.Errorf("read escaped stable domain snapshot: active=%d snapshot=%+v", active, snapshot)
		}
		trace.add("read")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := trace.all(), []string{"stable-snapshot", "read"}; !equalCommandStartTrace(got, want) {
		t.Fatalf("stable-read trace = %v, want %v", got, want)
	}
}

func commandStartReceipt(entry wipdjournal.Entry, code operation.ResultCode, eventIDs []string) ([]byte, error) {
	var accepted any
	if code == operation.ResultSucceeded && len(eventIDs) > 0 {
		accepted = map[string]any{
			"first_event_id": eventIDs[0], "last_event_id": eventIDs[len(eventIDs)-1], "event_count": uint64(len(eventIDs)),
		}
	}
	var output, problem any
	if code == operation.ResultSucceeded {
		switch entry.Command.Request.Operation {
		case operation.MatterCreateV1.Metadata().Operation:
			input := entry.Command.Request.Input.(operation.MatterCreateInput)
			output, _ = wipdwire.EncodeCanonical(map[string]any{"id": eventIDs[0], "locator": input.Locator, "title": input.Title})
		case operation.StepCreateV1.Metadata().Operation:
			input := entry.Command.Request.Input.(operation.StepCreateInput)
			output, _ = wipdwire.EncodeCanonical(map[string]any{
				"id": eventIDs[0], "parent_id": input.ParentID, "matter_id": input.ParentID,
				"locator": "step-01", "title": input.Title, "sort_key": int64(1000), "state": "planned",
			})
		default:
			return nil, ErrCommandStartIdentity
		}
	} else {
		problem = "operation.invalid-request"
	}
	return wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.terminal-receipt/1", "domain_id": entry.Command.AuthorityDomainID,
		"authority_epoch": entry.Command.ExpectedAuthorityEpoch, "identity_schema": "wipd.command/1",
		"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
		"operation":       map[string]any{"name": entry.Command.Request.Operation.Name, "version": uint64(entry.Command.Request.Operation.Version)},
		"environment":     map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
		"result":          map[string]any{"code": string(code), "output": output, "problem_code": problem},
		"accepted_events": accepted,
	})
}

func commandStartManifest(domain string, epoch uint64, anchor wipdwire.PrefixAnchor) wipdwire.BlobManifest {
	digest := sha256.Sum256([]byte("wipd/blob-manifest/v1\x00"))
	return wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: domain, Epoch: epoch, AsOf: anchor,
		Entries: []wipdwire.BlobManifestEntry{}, Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
}

func commandStartHashForTest() string {
	digest := sha256.Sum256([]byte("command-start test event hash"))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func commandStartAnchorEventID(anchor wipdwire.PrefixAnchor) string {
	if anchor.EventID == nil {
		return ""
	}
	return *anchor.EventID
}

func nextCommandStartEventID(anchor wipdwire.PrefixAnchor) string {
	number := 40
	if anchor.EventID != nil {
		if parsed, err := strconv.Atoi((*anchor.EventID)[len(commandStartCommandPrefix):]); err == nil {
			number = parsed
		}
	}
	return fmt.Sprintf("%s%02d", commandStartCommandPrefix, number+1)
}

func commandStartEventRecord(eventID, commandID, requestHash, environmentID string, sequence uint64) []byte {
	encoded, _ := wipdwire.EncodeCanonical(map[string]any{
		"schema": "wipd.event/1", "event_id": eventID, "domain_id": commandStartDomainID,
		"command_id": commandID, "request_hash": requestHash,
		"environment": map[string]any{"id": environmentID, "sequence": sequence},
		"acted_at":    "2026-09-28T00:00:00Z", "occurred_at": "2026-09-28T00:00:00Z",
		"kind": "matter.created", "subject_id": eventID, "repo_id": commandStartRepoID,
		"payload": map[string]any{"id": eventID, "locator": "test:" + eventID, "title": "title " + eventID},
	})
	return encoded
}

func commandStartEventRecordForEntry(eventID string, entry wipdjournal.Entry) []byte {
	switch entry.Command.Request.Operation {
	case operation.MatterCreateV1.Metadata().Operation:
		input := entry.Command.Request.Input.(operation.MatterCreateInput)
		encoded, _ := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": commandStartDomainID,
			"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
			"environment": map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
			"acted_at":    entry.Command.ActedAt, "occurred_at": "2026-09-28T00:00:00Z",
			"kind": "matter.created", "subject_id": eventID, "repo_id": commandStartRepoID,
			"payload": map[string]any{"id": eventID, "locator": input.Locator, "title": input.Title},
		})
		return encoded
	case operation.StepCreateV1.Metadata().Operation:
		input := entry.Command.Request.Input.(operation.StepCreateInput)
		encoded, _ := wipdwire.EncodeCanonical(map[string]any{
			"schema": "wipd.event/1", "event_id": eventID, "domain_id": commandStartDomainID,
			"command_id": entry.Command.ID, "request_hash": entry.RequestHash,
			"environment": map[string]any{"id": entry.Command.EnvironmentID, "sequence": entry.EnvironmentSeq},
			"acted_at":    entry.Command.ActedAt, "occurred_at": "2026-09-28T00:00:00Z",
			"kind": "step.created", "subject_id": eventID, "repo_id": commandStartRepoID,
			"payload": map[string]any{"title": input.Title, "locator": "step-01", "parent": input.ParentID, "sort_key": int64(1000)},
		})
		return encoded
	default:
		return nil
	}
}

func commandStartCanonicalCommand(id string, sequence uint64, causationID, correlationID string, operationID operation.ID, input operation.Input, claim *operation.ClaimContext) operation.Command {
	return operation.Command{
		ID: id, AuthorityDomainID: commandStartDomainID, ExpectedAuthorityEpoch: 7,
		EnvironmentID: commandStartEnvironmentID, EnvironmentSequence: sequence,
		ActedAt: "2026-09-28T00:00:00Z", CausationCommandID: causationID,
		CorrelationCommandID: correlationID,
		Request:              operation.Request{Operation: operationID, Actor: "human", Context: operation.Context{Repo: commandStartRepoID}, Claim: claim, Input: input},
	}
}

func mustCommandStartHash(t *testing.T, command operation.Command) string {
	t.Helper()
	hash, err := command.RequestHash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func containsCommandStartTrace(trace []string, step string) bool {
	for _, item := range trace {
		if item == step {
			return true
		}
	}
	return false
}

func commandStartVerifiedTransfer(start wipdwire.PrefixAnchor, records []wipdwire.EventRecord) (wipdjournal.VerifiedTransfer, error) {
	chain, err := hex.DecodeString(start.Digest[len("sha256:"):])
	if err != nil {
		return wipdjournal.VerifiedTransfer{}, err
	}
	for _, record := range records {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(record.Record)))
		hash := sha256.New()
		_, _ = hash.Write([]byte("wipd/event-prefix-step/v1\x00"))
		_, _ = hash.Write(chain)
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(record.Record)
		chain = hash.Sum(nil)
	}
	end := wipdwire.PrefixAnchor{EventCount: start.EventCount + uint64(len(records)), Digest: "sha256:" + hex.EncodeToString(chain)}
	if len(records) == 0 {
		end.EventID = start.EventID
	} else {
		eventID := records[len(records)-1].EventID
		end.EventID = &eventID
	}
	manifest := commandStartManifest(commandStartDomainID, 7, end)
	return wipdjournal.VerifyTransfer(commandStartDomainID, 7, start, end, records, manifest)
}

func admitCommandStartTestEntry(t *testing.T, journal *wipdjournal.Journal, commandID string) {
	t.Helper()
	installed, err := journal.InstallSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AdmitPending(context.Background(), installed.Expectation(), commandID); err != nil {
		t.Fatal(err)
	}
}

func equalCommandStartTrace(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
