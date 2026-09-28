package wipd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

type commandStartMemoryEnvironment struct {
	mu          sync.Mutex
	snapshot    CommandStartSnapshot
	journal     *wipdjournal.Journal
	currentID   string
	trace       *commandStartTrace
	failInstall error
}

func (environment *commandStartMemoryEnvironment) Snapshot(ctx context.Context) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	if environment.currentID != "" {
		if _, err := environment.journal.Get(environment.currentID); err != nil {
			return CommandStartSnapshot{}, fmt.Errorf("snapshot preceded durable command admission: %w", err)
		}
		environment.trace.add("snapshot:" + environment.currentID)
	} else {
		environment.trace.add("stable-snapshot")
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return cloneCommandStartSnapshot(environment.snapshot), nil
}

func (environment *commandStartMemoryEnvironment) InstallFold(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry, fold CommandFold) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !sameCommandStartSnapshot(expected, environment.snapshot) {
		return CommandStartSnapshot{}, ErrCommandStartIdentity
	}
	if environment.failInstall != nil {
		return CommandStartSnapshot{}, environment.failInstall
	}
	// This fake models one SQLite transaction: construct the new receipt,
	// prefix, and overlay revision before publishing any of them.
	next := cloneCommandStartSnapshot(environment.snapshot)
	next.Anchor = fold.End
	next.ManifestDigest = fold.Manifest.Digest
	next.Revision++
	if next.Receipts == nil {
		next.Receipts = make(map[string]InstalledCommandReceipt)
	}
	next.Receipts[entry.Command.ID] = InstalledCommandReceipt{
		RequestHash: entry.RequestHash, EnvironmentSeq: entry.EnvironmentSeq,
		JournalPosition: entry.JournalPosition, ResultCode: fold.ResultCode,
		CanonicalReceipt: append([]byte(nil), fold.CanonicalReceipt...),
	}
	environment.snapshot = next
	environment.trace.add("install-fold+receipt+tail+overlay:" + entry.Command.ID)
	return cloneCommandStartSnapshot(next), nil
}

func (environment *commandStartMemoryEnvironment) InstallPull(ctx context.Context, expected CommandStartSnapshot, pull CommandPull) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !sameCommandStartSnapshot(expected, environment.snapshot) {
		return CommandStartSnapshot{}, ErrCommandStartIdentity
	}
	next := cloneCommandStartSnapshot(environment.snapshot)
	next.Anchor = pull.End
	next.ManifestDigest = pull.Manifest.Digest
	next.Revision++
	environment.snapshot = next
	environment.trace.add("install-pull+overlay")
	return cloneCommandStartSnapshot(next), nil
}

func (environment *commandStartMemoryEnvironment) AdmitPending(ctx context.Context, expected CommandStartSnapshot, entry wipdjournal.Entry) (CommandStartSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CommandStartSnapshot{}, err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !sameCommandStartSnapshot(expected, environment.snapshot) {
		return CommandStartSnapshot{}, ErrCommandStartIdentity
	}
	next := cloneCommandStartSnapshot(environment.snapshot)
	next.Revision++
	environment.snapshot = next
	environment.trace.add("admit+overlay:" + entry.Command.ID)
	return cloneCommandStartSnapshot(next), nil
}

type commandStartFakeAuthority struct {
	trace          *commandStartTrace
	returnResults  []operation.ResultCode
	returnContinue []bool
	returnIndex    int
	pullEventID    string
}

func (authority *commandStartFakeAuthority) Return(_ context.Context, entry wipdjournal.Entry, start wipdwire.PrefixAnchor) (CommandFold, error) {
	index := authority.returnIndex
	authority.returnIndex++
	if index >= len(authority.returnResults) {
		return CommandFold{}, errors.New("unexpected authority return")
	}
	eventID := fmt.Sprintf("01KZ7XHAQT1S46NYPN1PW1DX%02d", index+41)
	end := commandStartAnchor(start.EventCount+1, eventID)
	code := authority.returnResults[index]
	continueReturn := authority.returnContinue[index]
	authority.trace.add("return:" + entry.Command.ID)
	manifest := commandStartManifest(entry.Command.AuthorityDomainID, entry.Command.ExpectedAuthorityEpoch, end)
	ids := []string(nil)
	if code == operation.ResultSucceeded {
		ids = []string{eventID}
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
		CanonicalReceipt: receipt, AcceptedEventIDs: ids, Manifest: manifest, VerifiedTransfer: struct{}{},
	}, nil
}

func (authority *commandStartFakeAuthority) Pull(_ context.Context, start wipdwire.PrefixAnchor) (CommandPull, error) {
	end := commandStartAnchor(start.EventCount+1, authority.pullEventID)
	authority.trace.add("pull:" + fmt.Sprint(start.EventCount))
	return CommandPull{
		DomainID: commandStartDomainID, Epoch: 7, Start: start, End: end,
		Manifest: commandStartManifest(commandStartDomainID, 7, end), VerifiedTransfer: struct{}{},
	}, nil
}

func newCommandStartFixture(t *testing.T) (*CommandStartCoordinator, *wipdjournal.Journal, *commandStartMemoryEnvironment, *commandStartFakeAuthority, *commandStartTrace, *Server) {
	t.Helper()
	journal, err := wipdjournal.Open(t.TempDir()+"/journal", wipdjournal.Identity{
		RepoID: commandStartRepoID, DomainID: commandStartDomainID, AuthorityEpoch: 7, EnvironmentID: commandStartEnvironmentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	trace := &commandStartTrace{}
	emptyAnchor := commandStartEmptyAnchor()
	emptyManifest := commandStartManifest(commandStartDomainID, 7, emptyAnchor)
	environment := &commandStartMemoryEnvironment{
		journal: journal, trace: trace, currentID: commandStartCommandPrefix + "01",
		snapshot: CommandStartSnapshot{
			DomainID: commandStartDomainID, Epoch: 7, EnvironmentID: commandStartEnvironmentID,
			Revision: 1, Anchor: emptyAnchor, ManifestDigest: emptyManifest.Digest,
			Receipts: make(map[string]InstalledCommandReceipt),
		},
	}
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
	}
	authority.returnResults = []operation.ResultCode{operation.ResultSucceeded, operation.ResultSucceeded}
	authority.returnContinue = []bool{true, false}
	environment.currentID = commandStartCommandPrefix + "13"
	var guardRan bool
	result, err := coordinator.RunConnected(context.Background(), commandStartInput(environment.currentID, "current"), func(_ context.Context, snapshot CommandStartSnapshot, command operation.Command) error {
		guardRan = true
		if snapshot.Revision != 5 || snapshot.Anchor.EventCount != 3 || snapshot.ManifestDigest == "" {
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
	if anchor := environment.snapshot.Anchor; anchor.EventCount != 3 || anchor.EventID == nil || *anchor.EventID != authority.pullEventID {
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

func TestConnectedCommandStartRefusalInstallsReceiptThenBlocksPull(t *testing.T) {
	coordinator, journal, environment, authority, trace, _ := newCommandStartFixture(t)
	pendingID := commandStartCommandPrefix + "31"
	if _, err := journal.PrepareCommand(commandStartInput(pendingID, "refused")); err != nil {
		t.Fatal(err)
	}
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
	if receipt, ok := environment.snapshot.Receipts[pendingID]; !ok || receipt.ResultCode != operation.ResultRejected {
		t.Fatalf("non-success receipt was not atomically installed: %+v", environment.snapshot.Receipts)
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
	if environment.snapshot.Anchor.EventCount != 0 || len(environment.snapshot.Receipts) != 0 {
		t.Fatalf("failed atomic install changed Environment state: %+v", environment.snapshot)
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
	receipt, err := commandStartReceipt(entry, operation.ResultRejected, nil)
	if err != nil {
		t.Fatal(err)
	}
	environment.snapshot.Receipts[commandID] = InstalledCommandReceipt{
		RequestHash: entry.RequestHash, EnvironmentSeq: entry.EnvironmentSeq,
		JournalPosition: entry.JournalPosition, ResultCode: operation.ResultRejected,
		CanonicalReceipt: receipt,
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
		output = []byte{0xa0}
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
	digest := sha256.Sum256([]byte("manifest:" + fmt.Sprint(anchor.EventCount)))
	return wipdwire.BlobManifest{
		Schema: "wipd.blob-manifest/1", DomainID: domain, Epoch: epoch, AsOf: anchor,
		Entries: []wipdwire.BlobManifestEntry{}, Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
}

func commandStartEmptyAnchor() wipdwire.PrefixAnchor {
	digest := sha256.Sum256([]byte("wipd/event-prefix/v1\x00"))
	return wipdwire.PrefixAnchor{Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func commandStartAnchor(count uint64, eventID string) wipdwire.PrefixAnchor {
	digest := sha256.Sum256([]byte(fmt.Sprintf("authority-prefix-%d-%s", count, eventID)))
	return wipdwire.PrefixAnchor{EventCount: count, EventID: &eventID, Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func sameCommandStartSnapshot(left, right CommandStartSnapshot) bool {
	return left.DomainID == right.DomainID && left.Epoch == right.Epoch && left.EnvironmentID == right.EnvironmentID &&
		left.Revision == right.Revision && left.ManifestDigest == right.ManifestDigest && sameCommandStartAnchor(left.Anchor, right.Anchor)
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
