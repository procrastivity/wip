//go:build linux

package wipd

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdfixture"
	"github.com/procrastivity/wip/internal/wipdprofile"
)

func TestClientMapsBareM1ResultAndFixtureStateMatchesDirectDispatch(t *testing.T) {
	tests := []struct {
		name string
		code operation.ResultCode
	}{
		{name: "success", code: operation.ResultSucceeded},
		{name: "rejection", code: operation.ResultRejected},
		{name: "refusal", code: operation.ResultRefused},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directProfile, err := wipdprofile.Resolve(testProfileRoot(t))
			if err != nil {
				t.Fatal(err)
			}
			directStore, err := wipdfixture.Open(directProfile)
			if err != nil {
				t.Fatalf("open direct test-only fixture: %v", err)
			}
			t.Cleanup(func() { _ = directStore.Close() })

			var socketDaemon *Daemon
			socketRegistry := operation.NewRegistry()
			if err := registerFixtureHandler(socketRegistry, test.code, func(ctx context.Context, record wipdfixture.Record) error {
				return socketDaemon.fixture.Put(ctx, record)
			}); err != nil {
				t.Fatal(err)
			}
			socketRoot, daemon := startLocalIPCServer(t, newServer(socketRegistry, 2))
			socketDaemon = daemon

			directRegistry := operation.NewRegistry()
			if err := registerFixtureHandler(directRegistry, test.code, directStore.Put); err != nil {
				t.Fatal(err)
			}
			canonical, _ := canonicalFixtureCommand(t)
			command, err := operation.DecodeCanonicalCommand(canonical)
			if err != nil {
				t.Fatalf("decode asymmetric M1 command: %v", err)
			}
			directResult := directRegistry.Dispatch(context.Background(), command.Request)

			client, err := Connect(context.Background(), socketRoot)
			if err != nil {
				t.Fatalf("authenticated AF_UNIX HTTP/2 prior-knowledge connect: %v", err)
			}
			defer client.Close()
			socketResult, err := client.ExecuteCommand(context.Background(), command)
			if err != nil {
				t.Fatalf("execute local fixture command over IPC: %v", err)
			}
			assertSemanticResultsEqual(t, socketResult, directResult)

			wantRecord := wipdfixture.Record{ID: "m4-fixture-handler-record", Key: "fixture-17", Value: "M4 Fixture"}
			if test.code == operation.ResultSucceeded {
				directRecord, err := directStore.Get(context.Background(), wantRecord.Key)
				if err != nil || directRecord != wantRecord {
					t.Fatalf("direct fixture state = %+v, err %v; want %+v", directRecord, err, wantRecord)
				}
				socketRecord, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
				if err != nil || socketRecord != wantRecord {
					t.Fatalf("socket fixture state = %+v, err %v; want %+v", socketRecord, err, wantRecord)
				}
			} else {
				if _, err := directStore.Get(context.Background(), wantRecord.Key); err == nil {
					t.Fatal("direct non-success unexpectedly changed fixture state")
				}
				if _, err := daemon.fixture.Get(context.Background(), wantRecord.Key); err == nil {
					t.Fatal("socket non-success unexpectedly changed fixture state")
				}
			}
		})
	}
}

func registerFixtureHandler(registry *operation.Registry, code operation.ResultCode, persist func(context.Context, wipdfixture.Record) error) error {
	return registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		input, ok := request.Input.(operation.MatterCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: "unexpected fixture input type"}}
		}
		switch code {
		case operation.ResultSucceeded:
			record := wipdfixture.Record{ID: "m4-fixture-handler-record", Key: input.Locator, Value: input.Title}
			if err := persist(ctx, record); err != nil {
				return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: err.Error()}}
			}
			return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
				ID: "01M4F1XT4R3E00000000000001", Locator: input.Locator, Title: input.Title,
			}}
		case operation.ResultRejected:
			return operation.Result{Code: operation.ResultRejected, Problem: &operation.Problem{
				Code: operation.ProblemInvalidTitle, Message: "fixture rejected this title",
			}}
		case operation.ResultRefused:
			return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
				Code: operation.ProblemUnknownClone, Message: "fixture refused this clone",
			}}
		default:
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{Code: operation.ProblemExecutionFailed, Message: "unsupported test disposition"}}
		}
	})
}

func TestActivateConvergesConcurrentClientsAndStatusTracksStop(t *testing.T) {
	root := testProfileRoot(t)
	before := ProbeStatus(context.Background(), root)
	wantBefore := LocalStatus{ProfileVerified: true, StatusCode: "transport.unavailable"}
	if before != wantBefore {
		t.Fatalf("pre-activation local status = %+v, want %+v", before, wantBefore)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readiness probe created profile root: %v", err)
	}

	binary := buildWipdBinary(t)
	const count = 8
	results := make(chan struct {
		client *Client
		err    error
	}, count)
	var callers sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	for range count {
		callers.Add(1)
		go func() {
			defer callers.Done()
			client, err := Activate(ctx, root, binary)
			results <- struct {
				client *Client
				err    error
			}{client: client, err: err}
		}()
	}
	go func() {
		callers.Wait()
		close(results)
	}()

	clients := make([]*Client, 0, count)
	started := 0
	var owner *Client
	for result := range results {
		if result.err != nil {
			t.Errorf("concurrent activation: %v", result.err)
			continue
		}
		clients = append(clients, result.client)
		if status := result.client.Status(); status != readyStatus() {
			t.Errorf("activated client status = %+v, want local ready status", status)
		}
		if result.client.StartedDaemon() {
			started++
			owner = result.client
		}
	}
	t.Cleanup(func() {
		for _, client := range clients {
			_ = client.Close()
		}
		stopForegroundDaemonForTest(t, root)
	})
	if len(clients) != count {
		t.Fatalf("successful activated clients = %d, want all %d callers to converge on the winner", len(clients), count)
	}
	if started != 1 {
		t.Fatalf("successful foreground activations = %d, want exactly one winner", started)
	}
	if status := ProbeStatus(context.Background(), root); status != readyStatus() {
		t.Fatalf("probed status after activation = %+v, want local ready status", status)
	}

	for _, client := range clients {
		_ = client.Close()
	}
	if err := owner.candidate.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop foreground daemon: %v", err)
	}
	select {
	case <-owner.candidate.done:
	case <-time.After(5 * time.Second):
		t.Fatal("foreground daemon did not stop")
	}
	owner = nil

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := ProbeStatus(context.Background(), root)
		if status == wantBefore {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status after foreground daemon stop = %+v, want %+v", ProbeStatus(context.Background(), root), wantBefore)
}

func TestActivateUnavailableDoesNotCreateProfileOrOpenLegacyStore(t *testing.T) {
	root := testProfileRoot(t)
	legacyPath := filepath.Join(t.TempDir(), "legacy-wip.db")
	sentinel := []byte("must remain byte-for-byte unchanged")
	if err := os.WriteFile(legacyPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_DB_PATH", legacyPath)
	_, err := Activate(context.Background(), root, filepath.Join(t.TempDir(), "missing-wipd"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed activation error = %v, want transport.unavailable", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed client activation created profile root: %v", err)
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil || string(got) != string(sentinel) {
		t.Fatalf("legacy database sentinel = %q, err %v; want unchanged %q", got, err, sentinel)
	}
}

func TestWipdImportGraphExcludesLegacyAndAuthorityStores(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect production wipd import graph: %v\n%s", err, output)
	}
	for _, importPath := range strings.Fields(string(output)) {
		if strings.HasPrefix(importPath, "github.com/procrastivity/wip/internal/store") ||
			strings.HasPrefix(importPath, "github.com/procrastivity/wip/internal/authoritystore") {
			t.Fatalf("wipd client/server dependency reaches forbidden persistence package %q", importPath)
		}
	}
}

func startLocalIPCServer(t *testing.T, server *Server) (string, *Daemon) {
	t.Helper()
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("start fixture IPC daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, daemon) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("fixture IPC server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("fixture IPC server did not stop after cancellation")
		}
	})
	return root, daemon
}

func stopForegroundDaemonForTest(t *testing.T, profileRoot string) {
	t.Helper()
	socketPath := filepath.Join(profileRoot, socketFileName)
	if _, err := os.Lstat(socketPath); errors.Is(err, os.ErrNotExist) {
		return
	}
	lock, err := os.ReadFile(filepath.Join(profileRoot, lockFileName))
	if err != nil {
		t.Errorf("read isolated test daemon PID: %v", err)
		return
	}
	pidText, ok := strings.CutPrefix(strings.TrimSpace(string(lock)), "pid=")
	if !ok {
		t.Errorf("invalid isolated test daemon PID file %q", lock)
		return
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Errorf("parse isolated test daemon PID %q: %v", pidText, err)
		return
	}
	commandLine, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || !strings.Contains(string(commandLine), "--profile-root\x00"+profileRoot) {
		t.Errorf("refusing to stop unrelated process for isolated test profile: cmdline %q, err %v", commandLine, err)
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Errorf("find isolated test daemon: %v", err)
		return
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("stop isolated test daemon: %v", err)
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(socketPath); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("isolated test daemon did not remove its socket after SIGTERM")
}

func TestActivateDeadlineIsBoundedWhenSocketIsLiveButUnresponsive(t *testing.T) {
	root := testProfileRoot(t)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, socketFileName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	started := time.Now()
	_, err = Activate(context.Background(), root, filepath.Join(t.TempDir(), "must-not-start"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unresponsive active socket error = %v, want transport.unavailable", err)
	}
	if elapsed := time.Since(started); elapsed > activationTimeout+time.Second {
		t.Fatalf("readiness failure took %s, want bounded timeout near %s", elapsed, activationTimeout)
	}
	if _, err := os.Stat(filepath.Join(root, "m4-test-fixture.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client activated over an unresponsive live socket: fixture DB stat error = %v", err)
	}
}
