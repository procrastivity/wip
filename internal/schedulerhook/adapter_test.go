package schedulerhook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdwire"
)

const (
	testDomainID         = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testRepoID           = "01ARZ3NDEKTSV4RRFFQ69G5FAS"
	testEnvID            = "01ARZ3NDEKTSV4RRFFQ69G5FAT"
	testCommand1         = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	testCommand2         = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	testCommand3         = "01ARZ3NDEKTSV4RRFFQ69G5FAY"
	testProfileEV        = "SCHEDULERHOOK_CRASH_PROFILE"
	testCrashFlag        = "SCHEDULERHOOK_CRASH_WORKER"
	testProcessFlag      = "SCHEDULERHOOK_CONCURRENT_WORKER"
	testProcessReadyEnv  = "SCHEDULERHOOK_CONCURRENT_READY"
	testProcessOutputEnv = "SCHEDULERHOOK_CONCURRENT_OUTPUT"
	testEnvironmentRoot  = "SCHEDULERHOOK_ENVIRONMENT_ROOT"
)

func testRequest(commandID string, kind Kind, role, revision string, inputs ...PinnedBlob) Request {
	request := Request{
		CommandID: commandID, RequestHash: blobDigest([]byte("command:" + commandID)),
		Kind: kind, Version: 1, AdapterID: AdapterID, AdapterVersion: AdapterVersion,
		RunID: testCommand1, MatterID: testCommand2, TargetID: testCommand2, TargetKind: "matter",
		EnvironmentID: testEnvID, Prefix: PrefixAnchor{EventCount: 1, EventID: stringPtr(testCommand3), Digest: blobDigest([]byte("prefix"))},
		Role: role, Revision: revision, Inputs: inputs,
	}
	if kind != KindAsk {
		request.RoleID = testCommand3
		request.DispatchID = testCommand1
		request.Claim = &Claim{ID: testCommand2, Epoch: 7}
		request.TargetClaims = []TargetClaim{{MatterID: testCommand2, ClaimID: testCommand2, Epoch: 7}}
	}
	if kind == KindAsk {
		request.Reason = "blocked"
		request.Blocked = HandleHalt
		request.Contention = HandleSkip
		request.Failed = HandleHalt
	}
	if kind == KindPlan || kind == KindAsk {
		request.TargetID = request.MatterID
	}
	return request
}

func stringPtr(value string) *string { return &value }

func testIdentity(domainID, environmentID string) wipdjournal.Identity {
	return wipdjournal.Identity{RepoID: testRepoID, DomainID: domainID, AuthorityEpoch: 7, EnvironmentID: environmentID}
}

func pinned(name string, content []byte) PinnedBlob {
	return PinnedBlob{Name: name, Digest: blobDigest(content), Size: int64(len(content)), Bytes: bytes.Clone(content)}
}

func privateTestProfile(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("make disposable profile private: %v", err)
	}
	return root
}

func openTestAdapter(t *testing.T, root, domainID string) *Adapter {
	t.Helper()
	adapter, err := openIdentity(root, testIdentity(domainID, testEnvID))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return adapter
}

func TestPinnedMarkdownAdapterReturnsTypedPlanWorkAndVerification(t *testing.T) {
	profile := privateTestProfile(t)
	adapter := openTestAdapter(t, profile, testDomainID)
	ctx := context.Background()

	planInput := pinned("workplan", []byte("# Work\n- [ ] First asymmetric task\n- [ ] Second task\n- [x] Already complete\n"))
	planRequest := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1", planInput)
	plan, err := adapter.StartOrGet(ctx, planRequest)
	if err != nil {
		t.Fatalf("Plan StartOrGet() error = %v", err)
	}
	if plan.Key != (Key{DomainID: testDomainID, CommandID: testCommand1}) || plan.Typed.Plan == nil ||
		plan.Typed.Plan.Outcome != PlanPlanned || len(plan.Typed.Plan.Steps) != 2 ||
		plan.Typed.Plan.Steps[0].Title != "First asymmetric task" || plan.Typed.Plan.Steps[1].Title != "Second task" {
		t.Fatalf("Plan result does not preserve typed authored order: %+v", plan)
	}

	source := pinned("source", []byte("# Introduction\ntext\n## Edge case\n"))
	workRequest := testRequest(testCommand2, KindWork, "builder", "builtin.markdown-outline@v1", source)
	work, err := adapter.StartOrGet(ctx, workRequest)
	if err != nil {
		t.Fatalf("Builder StartOrGet() error = %v", err)
	}
	if work.Typed.Work == nil || work.Typed.Work.Outcome != WorkCompleted || len(work.Typed.Work.EvidenceRefs) != 1 {
		t.Fatalf("Builder result is not useful typed work: %+v", work.Typed)
	}
	outlineBytes, err := adapter.Evidence(ctx, work.Typed.Work.EvidenceRefs[0])
	if err != nil {
		t.Fatalf("Evidence() error = %v", err)
	}
	var outline outlineDocument
	if err := json.Unmarshal(outlineBytes, &outline); err != nil {
		t.Fatalf("decode retained outline: %v", err)
	}
	if outline.SourceDigest != source.Digest || len(outline.Headings) != 2 || outline.Headings[0].Line != 1 ||
		outline.Headings[0].Text != "Introduction" || outline.Headings[1].Level != 2 || outline.Headings[1].Line != 3 {
		t.Fatalf("outline does not describe the pinned source: %+v", outline)
	}

	verifierInputs := []PinnedBlob{pinned("outline", outlineBytes), source}
	verifyRequest := testRequest(testCommand3, KindWork, "verifier", "builtin.markdown-outline-verifier@v1", verifierInputs...)
	verified, err := adapter.StartOrGet(ctx, verifyRequest)
	if err != nil {
		t.Fatalf("Verifier StartOrGet() error = %v", err)
	}
	if verified.Typed.Work == nil || verified.Typed.Work.Outcome != WorkNotVerified ||
		verified.Typed.Work.ProblemCode != "verification.complete-pass-unavailable" ||
		len(verified.Typed.Work.EvidenceRefs) != 1 || verified.Typed.Work.EvidenceRefs[0] != verifierInputs[0].Digest {
		t.Fatalf("outline equality was incorrectly promoted to a complete verification pass: %+v", verified.Typed)
	}

	badBytes := bytes.Replace(outlineBytes, []byte("Introduction"), []byte("Different"), 1)
	badVerify := testRequest("01ARZ3NDEKTSV4RRFFQ69G5FAZ", KindWork, "verifier", "builtin.markdown-outline-verifier@v1",
		[]PinnedBlob{pinned("outline", badBytes), source}...)
	negative, err := adapter.StartOrGet(ctx, badVerify)
	if err != nil {
		t.Fatalf("negative Verifier StartOrGet() error = %v", err)
	}
	if negative.Typed.Work == nil || negative.Typed.Work.Outcome != WorkNotVerified ||
		negative.Typed.Work.ProblemCode != "verification.markdown-outline-invalid" {
		t.Fatalf("tampered outline was not given a useful negative verdict: %+v", negative.Typed)
	}
}

func TestFailedWorkRoundTripPreservesEmptyEvidenceDigest(t *testing.T) {
	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	request := testRequest(testCommand1, KindWork, "builder", "builtin.markdown-outline@v1",
		pinned("source", []byte("No headings.\n")))
	result, err := adapter.StartOrGet(context.Background(), request)
	if err != nil || result.Typed.Work == nil || result.Typed.Work.Outcome != WorkFailed ||
		result.Typed.Work.EvidenceRefs == nil || len(result.Typed.Work.EvidenceRefs) != 0 {
		t.Fatalf("actual failed Builder result must carry a non-nil empty evidence list: result=%+v error=%v", result, err)
	}
	output, err := OperationOutput(result)
	if err != nil || output.EvidenceRefs == nil || len(output.EvidenceRefs) != 0 {
		t.Fatalf("operation output did not preserve the empty evidence array: output=%+v error=%v", output, err)
	}
	got, err := TypedFromOperationOutput(operation.SchedulerHookOutput{
		Kind: output.Kind, Version: output.Version, Outcome: output.Outcome,
		Revision: output.Revision, EvidenceRefs: output.EvidenceRefs, ProblemCode: output.ProblemCode,
	})
	if err != nil || got.Work == nil || got.Work.EvidenceRefs == nil || len(got.Work.EvidenceRefs) != 0 {
		t.Fatalf("typed round-trip did not preserve the empty evidence array: typed=%+v error=%v", got, err)
	}
	digest, err := ResultDigest(result.Key, result.InputDigest, got)
	if err != nil || digest != result.ResultDigest {
		t.Fatalf("failed Work result digest changed across public conversion: original=%s reconstructed=%s error=%v",
			result.ResultDigest, digest, err)
	}
}

func TestStartOrGetChangedIntentConflictsOnOriginalCommandID(t *testing.T) {
	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	first := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Alpha\n")))
	if _, err := adapter.StartOrGet(context.Background(), first); err != nil {
		t.Fatalf("first StartOrGet() error = %v", err)
	}
	changed := first
	changed.Inputs = []PinnedBlob{pinned("workplan", []byte("- [ ] Beta\n"))}
	if _, err := adapter.StartOrGet(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-intent StartOrGet() error = %v, want ErrConflict", err)
	}
	changedHash := first
	changedHash.RequestHash = blobDigest([]byte("different canonical scheduler command"))
	if _, err := adapter.StartOrGet(context.Background(), changedHash); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed canonical command hash StartOrGet() error = %v, want ErrConflict", err)
	}
	retained, err := adapter.Lookup(context.Background(), testCommand1)
	if err != nil || retained.Typed.Plan == nil || retained.Typed.Plan.Steps[0].Title != "Alpha" {
		t.Fatalf("original result was not retained after conflict: result=%+v error=%v", retained, err)
	}
}

func TestWorkTargetKindIsPartOfDurableIntent(t *testing.T) {
	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	work := testRequest(testCommand1, KindWork, "builder", "builtin.markdown-outline@v1",
		pinned("source", []byte("# A heading\n")))
	work.TargetID = testCommand3
	work.TargetKind = "step"
	if _, err := adapter.StartOrGet(context.Background(), work); err != nil {
		t.Fatalf("start Step-targeted Work: %v", err)
	}
	changed := work
	changed.TargetKind = "stage"
	if _, err := adapter.StartOrGet(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-key changed target kind error = %v, want ErrConflict", err)
	}
}

func TestSameCommandIDIsIsolatedByAuthenticatedDomain(t *testing.T) {
	profile := privateTestProfile(t)
	firstAdapter := openTestAdapter(t, profile, testDomainID)
	secondAdapter := openTestAdapter(t, profile, "01ARZ3NDEKTSV4RRFFQ69G5FAB")
	firstRequest := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] First domain\n")))
	secondRequest := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Second domain\n")))
	first, err := firstAdapter.StartOrGet(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("first domain StartOrGet() error = %v", err)
	}
	second, err := secondAdapter.StartOrGet(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second domain StartOrGet() error = %v", err)
	}
	if first.Key.CommandID != second.Key.CommandID || first.Key.DomainID == second.Key.DomainID ||
		first.Typed.Plan.Steps[0].Title != "First domain" || second.Typed.Plan.Steps[0].Title != "Second domain" {
		t.Fatalf("same command ID was not independently scoped by authenticated domain: first=%+v second=%+v", first, second)
	}

	otherEnvironment := "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	thirdAdapter, err := openIdentity(profile, testIdentity(testDomainID, otherEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = thirdAdapter.Close() }()
	thirdRequest := firstRequest
	thirdRequest.EnvironmentID = otherEnvironment
	if _, err = thirdAdapter.StartOrGet(context.Background(), thirdRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("same domain/command under a different Environment identity error = %v, want ErrConflict", err)
	}
}

func TestOpenBindsAdapterToAuthenticatedEnvironmentJournal(t *testing.T) {
	profile := privateTestProfile(t)
	identity := testIdentity(testDomainID, testEnvID)
	journal, err := wipdjournal.Open(profile, identity)
	if err != nil {
		t.Fatalf("open disposable Environment journal: %v", err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close disposable Environment journal: %v", err)
		}
	})
	adapter, err := Open(profile, journal)
	if err != nil {
		t.Fatalf("Open(authenticated journal) error = %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close adapter: %v", err)
		}
	})
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Journal-bound request\n")))
	request.EnvironmentID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	if _, err := adapter.StartOrGet(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("request with substituted Environment identity error = %v, want ErrInvalidRequest", err)
	}
	request.EnvironmentID = identity.EnvironmentID
	result, err := adapter.StartOrGet(context.Background(), request)
	if err != nil {
		t.Fatalf("journal-bound StartOrGet() error = %v", err)
	}
	if result.Key.DomainID != identity.DomainID || result.Key.CommandID != request.CommandID {
		t.Fatalf("adapter did not preserve journal domain and original command ID: %+v", result.Key)
	}
}

func TestConcurrentSameKeyStartsCoalesce(t *testing.T) {
	profile := privateTestProfile(t)
	const workers = 24
	adapters := make([]*Adapter, 4)
	for i := range adapters {
		adapters[i] = openTestAdapter(t, profile, testDomainID)
	}
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] One\n- [ ] Two\n- [ ] Three\n")))
	results := make([]Result, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := range workers {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			results[i], errs[i] = adapters[i%len(adapters)].StartOrGet(context.Background(), request)
		}(i)
	}
	close(start)
	wait.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent StartOrGet[%d] error = %v", i, err)
		}
		if results[i].Key != (Key{DomainID: testDomainID, CommandID: testCommand1}) ||
			results[i].Typed.Plan == nil || len(results[i].Typed.Plan.Steps) != 3 {
			t.Fatalf("concurrent StartOrGet[%d] returned wrong result: %+v", i, results[i])
		}
		if results[i].ResultDigest != results[0].ResultDigest {
			t.Fatalf("concurrent starts diverged: result 0 digest=%s result %d digest=%s",
				results[0].ResultDigest, i, results[i].ResultDigest)
		}
	}
	var count int
	if err := adapters[0].db.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		testDomainID, testCommand1).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable engagement count = %d, error=%v, want exactly one", count, err)
	}
}

func TestConcurrentSameKeyProcessWorker(t *testing.T) {
	if os.Getenv(testProcessFlag) != "1" {
		return
	}
	readyPath := os.Getenv(testProcessReadyEnv)
	outputPath := os.Getenv(testProcessOutputEnv)
	if readyPath == "" || outputPath == "" {
		t.Fatal("worker paths are required")
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
		t.Fatalf("await process-start barrier: %v", err)
	}
	environmentRoot := os.Getenv(testEnvironmentRoot)
	journal, err := wipdjournal.Open(environmentRoot, testIdentity(testDomainID, testEnvID))
	if err != nil {
		t.Fatalf("open process Environment journal: %v", err)
	}
	defer func() { _ = journal.Close() }()
	adapter, err := Open(os.Getenv(testProfileEV), journal)
	if err != nil {
		t.Fatalf("open process adapter: %v", err)
	}
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Process-level first\n- [ ] Process-level second\n")))
	result, err := adapter.StartOrGet(context.Background(), request)
	if err != nil {
		t.Fatalf("process StartOrGet(): %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentSameKeyStartsCoalesceAcrossProcesses(t *testing.T) {
	profile := privateTestProfile(t)
	coordinationRoot := privateTestProfile(t)
	const workers = 8
	type process struct {
		command *exec.Cmd
		input   io.WriteCloser
		ready   string
		output  string
		stdout  bytes.Buffer
		stderr  bytes.Buffer
		done    chan error
		exited  bool
	}
	processes := make([]*process, 0, workers)
	for i := range workers {
		environmentRoot := filepath.Join(coordinationRoot, fmt.Sprintf("environment-%d", i))
		if err := os.Mkdir(environmentRoot, 0o700); err != nil {
			t.Fatalf("create disposable Environment %d root: %v", i, err)
		}
		ready := filepath.Join(coordinationRoot, fmt.Sprintf("ready-%d", i))
		output := filepath.Join(coordinationRoot, fmt.Sprintf("result-%d.json", i))
		command := exec.Command(os.Args[0], "-test.run=^TestConcurrentSameKeyProcessWorker$")
		command.Env = append(os.Environ(), testProcessFlag+"=1", testProfileEV+"="+profile,
			testProcessReadyEnv+"="+ready, testProcessOutputEnv+"="+output, testEnvironmentRoot+"="+environmentRoot)
		input, err := command.StdinPipe()
		if err != nil {
			t.Fatalf("process %d stdin pipe: %v", i, err)
		}
		worker := &process{command: command, input: input, ready: ready, output: output, done: make(chan error, 1)}
		command.Stdout = &worker.stdout
		command.Stderr = &worker.stderr
		if err := command.Start(); err != nil {
			t.Fatalf("start process %d: %v", i, err)
		}
		processes = append(processes, worker)
		go func(worker *process) { worker.done <- worker.command.Wait() }(worker)
	}
	defer func() {
		for _, worker := range processes {
			if !worker.exited {
				_ = worker.command.Process.Kill()
				<-worker.done
				worker.exited = true
			}
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for _, worker := range processes {
		for {
			if _, err := os.Stat(worker.ready); err == nil {
				break
			}
			for i, candidate := range processes {
				select {
				case err := <-candidate.done:
					candidate.exited = true
					t.Fatalf("worker %d exited before barrier: %s; exit=%v; stdout=%s; stderr=%s",
						i, worker.ready, err, candidate.stdout.String(), candidate.stderr.String())
				default:
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("worker did not reach barrier: %s", worker.ready)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for i, worker := range processes {
		if _, err := worker.input.Write([]byte{1}); err != nil {
			t.Fatalf("release process %d: %v", i, err)
		}
		if err := worker.input.Close(); err != nil {
			t.Fatalf("close process %d stdin: %v", i, err)
		}
	}
	for i, worker := range processes {
		err := <-worker.done
		worker.exited = true
		if err != nil {
			t.Fatalf("process %d failed: %v; stderr=%s", i, err, worker.stderr.String())
		}
	}

	var digest string
	for i, worker := range processes {
		encoded, err := os.ReadFile(worker.output)
		if err != nil {
			t.Fatalf("read process %d result: %v", i, err)
		}
		var result Result
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatalf("decode process %d result: %v", i, err)
		}
		if result.Key != (Key{DomainID: testDomainID, CommandID: testCommand1}) || result.Typed.Plan == nil ||
			len(result.Typed.Plan.Steps) != 2 || result.Typed.Plan.Steps[0].Title != "Process-level first" {
			t.Fatalf("process %d returned wrong original-command result: %+v", i, result)
		}
		if i == 0 {
			digest = result.ResultDigest
		} else if result.ResultDigest != digest {
			t.Fatalf("process results diverged: first digest=%s process %d digest=%s", digest, i, result.ResultDigest)
		}
	}
	adapter := openTestAdapter(t, profile, testDomainID)
	var count int
	if err := adapter.db.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		testDomainID, testCommand1).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cross-process engagement count = %d, error=%v, want exactly one", count, err)
	}
}

func TestCrashWorkerAfterAdapterCommit(_ *testing.T) {
	if os.Getenv(testCrashFlag) != "1" {
		return
	}
	profile := os.Getenv(testProfileEV)
	journal, err := wipdjournal.Open(profile, testIdentity(testDomainID, testEnvID))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(20)
	}
	adapter, err := Open(profile, journal)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(21)
	}
	adapter.afterCommit = func() {
		_, _ = fmt.Fprintln(os.Stdout, "adapter-engagement-committed")
		_ = os.Stdout.Sync()
		select {}
	}
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Recover original plan\n- [ ] Do not duplicate\n")))
	if _, err := adapter.StartOrInstall(context.Background(), request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(22)
	}
	os.Exit(23)
}

func TestPostCommitCrashRecoversSameUsefulResult(t *testing.T) {
	profile := privateTestProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashWorkerAfterAdapterCommit$")
	command.Env = append(os.Environ(), testCrashFlag+"=1", testProfileEV+"="+profile)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start crash worker: %v", err)
	}
	reader := bufio.NewReader(stdout)
	marker, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(marker) != "adapter-engagement-committed" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("worker did not reach post-commit/pre-response fault point: marker=%q error=%v stderr=%s", marker, err, stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill worker after engagement commit: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("crash worker exited successfully; expected forced termination before result delivery")
	}

	journal, err := wipdjournal.Open(profile, testIdentity(testDomainID, testEnvID))
	if err != nil {
		t.Fatalf("open recovery Environment journal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	adapter, err := Open(profile, journal)
	if err != nil {
		t.Fatalf("open recovery adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Recover original plan\n- [ ] Do not duplicate\n")))
	if _, err := adapter.LookupInstalled(context.Background(), request.CommandID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Environment result should still be uninstalled after the forced crash: error=%v", err)
	}
	recovered, err := adapter.StartOrInstall(context.Background(), request)
	if err != nil {
		t.Fatalf("recover StartOrInstall() error = %v", err)
	}
	if recovered.Key != (Key{DomainID: testDomainID, CommandID: testCommand1}) || recovered.Typed.Plan == nil ||
		recovered.Typed.Plan.Outcome != PlanPlanned || len(recovered.Typed.Plan.Steps) != 2 ||
		recovered.Typed.Plan.Steps[0].Title != "Recover original plan" || recovered.Typed.Plan.Steps[1].Title != "Do not duplicate" {
		t.Fatalf("recovery did not return the original useful result: %+v", recovered)
	}
	lookedUp, err := adapter.LookupInstalled(context.Background(), testCommand1)
	if err != nil || lookedUp.ResultDigest != recovered.ResultDigest {
		t.Fatalf("LookupInstalled() did not return the recovered Environment result: result=%+v error=%v", lookedUp, err)
	}
	var count int
	if err := adapter.db.QueryRow(`SELECT count(*) FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		testDomainID, testCommand1).Scan(&count); err != nil || count != 1 {
		t.Fatalf("post-crash engagement count = %d, error=%v, want exactly one", count, err)
	}
}

func TestDurableUnresolvedEngagementBlocksReplay(t *testing.T) {
	ctx := context.Background()
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Original unresolved plan\n")))
	seed := openTestAdapter(t, privateTestProfile(t), testDomainID)
	if _, err := seed.StartOrGet(ctx, request); err != nil {
		t.Fatalf("create exact durable intent fixture: %v", err)
	}
	var intent []byte
	var inputDigest string
	if err := seed.db.QueryRow(`SELECT intent,input_digest FROM hook_engagements WHERE domain_id=? AND command_id=?`,
		testDomainID, testCommand1).Scan(&intent, &inputDigest); err != nil {
		t.Fatalf("read exact durable intent fixture: %v", err)
	}

	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	if _, err := adapter.db.Exec(`INSERT INTO hook_engagements
		(domain_id,command_id,intent,state,input_digest) VALUES(?,?,?,'running',?)`,
		testDomainID, testCommand1, intent, inputDigest); err != nil {
		t.Fatalf("seed an unresolved durable engagement: %v", err)
	}
	if _, err := adapter.StartOrInstall(ctx, request); !errors.Is(err, ErrIndeterminate) {
		t.Fatalf("exact unresolved replay error = %v, want ErrIndeterminate", err)
	}
	changed := request
	changed.RequestHash = blobDigest([]byte("changed immutable intent"))
	if _, err := adapter.StartOrInstall(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed unresolved intent error = %v, want ErrConflict", err)
	}
	var state, resultDigest string
	var resultIsNull int
	if err := adapter.db.QueryRow(`SELECT state,coalesce(result_digest,''),result IS NULL
		FROM hook_engagements WHERE domain_id=? AND command_id=?`, testDomainID, testCommand1).
		Scan(&state, &resultDigest, &resultIsNull); err != nil {
		t.Fatal(err)
	}
	if state != "running" || resultDigest != "" || resultIsNull != 1 {
		t.Fatalf("unresolved engagement was rewritten or falsely completed: state=%q digest=%q result_is_null=%d",
			state, resultDigest, resultIsNull)
	}
	if _, err := adapter.LookupInstalled(ctx, testCommand1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unresolved engagement produced an Environment result: %v", err)
	}
}

func TestEnvironmentResultMustMatchCommittedEngagement(t *testing.T) {
	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	request := testRequest(testCommand1, KindPlan, "researcher", "builtin.markdown-checklist-plan@v1",
		pinned("workplan", []byte("- [ ] Original result\n")))
	committed, err := adapter.StartOrGet(context.Background(), request)
	if err != nil {
		t.Fatalf("StartOrGet() error = %v", err)
	}
	changed := TypedResult{Kind: KindPlan, Version: 1, Plan: &PlanResult{
		Outcome: PlanPlanned, Steps: []StepSpec{{Title: "Different result"}},
	}}
	changedDigest, err := hookResultDigest(committed.Key, committed.InputDigest, changed)
	if err != nil {
		t.Fatalf("compute conflicting result digest: %v", err)
	}
	if err = adapter.installEnvironmentResult(context.Background(), Result{
		Key: committed.Key, InputDigest: committed.InputDigest, Typed: changed, ResultDigest: changedDigest,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("install changed Environment result error = %v, want ErrConflict", err)
	}
	if _, err = adapter.LookupInstalled(context.Background(), testCommand1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("conflicting install created an Environment result: error=%v", err)
	}

	if err = adapter.installEnvironmentResult(context.Background(), committed); err != nil {
		t.Fatalf("install exact committed result: %v", err)
	}
	changedBytes, err := wipdwire.EncodeCanonical(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.db.Exec(`UPDATE environment_hook_results SET result_digest=?,result=?
		WHERE domain_id=? AND environment_id=? AND command_id=?`, changedDigest, changedBytes,
		testDomainID, testEnvID, testCommand1); err != nil {
		t.Fatalf("replace local result with a self-consistent but conflicting record: %v", err)
	}
	if _, err = adapter.LookupInstalled(context.Background(), testCommand1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("LookupInstalled() error = %v, want ErrCorrupt for result that differs from committed engagement", err)
	}
}

func TestAskIsAKeyedTypedPolicyAnswer(t *testing.T) {
	adapter := openTestAdapter(t, privateTestProfile(t), testDomainID)
	request := testRequest(testCommand1, KindAsk, "", "builtin.run-policy@v1")
	result, err := adapter.StartOrGet(context.Background(), request)
	if err != nil {
		t.Fatalf("Ask StartOrGet() error = %v", err)
	}
	if result.Typed.Ask == nil || result.Typed.Ask.Handling != HandleHalt {
		t.Fatalf("Ask result = %+v, want blocked -> halt", result.Typed)
	}

	contended := testRequest(testCommand2, KindAsk, "", "builtin.run-policy@v1")
	contended.Reason = "contended"
	contended.Contention = HandleSkip
	contended.Blocked = HandleHalt
	result, err = adapter.StartOrGet(context.Background(), contended)
	if err != nil || result.Typed.Ask == nil || result.Typed.Ask.Handling != HandleSkip {
		t.Fatalf("contended Ask = %+v, %v; want the distinct contention -> skip policy", result.Typed, err)
	}
	legacySpelling := contended
	legacySpelling.CommandID = testCommand3
	legacySpelling.Reason = "contention"
	if _, err = adapter.StartOrGet(context.Background(), legacySpelling); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("non-contract Ask reason error = %v, want ErrInvalidRequest", err)
	}
}

func TestLedgerFileRemainsInsideExplicitDisposableProfile(t *testing.T) {
	profile := privateTestProfile(t)
	_ = openTestAdapter(t, profile, testDomainID)
	info, err := os.Stat(filepath.Join(profile, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("ledger permissions = %o, want owner-only", info.Mode().Perm())
	}
}
