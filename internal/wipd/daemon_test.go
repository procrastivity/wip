//go:build linux

package wipd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/wipdfixture"
)

func TestLifecycleRetainsLockInodeAndFixtureData(t *testing.T) {
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	lockPath := filepath.Join(root, lockFileName)
	socketPath := filepath.Join(root, socketFileName)
	lockBefore := mustLstat(t, lockPath)
	if !mustLstat(t, root).IsDir() || !lockBefore.Mode().IsRegular() {
		t.Fatal("profile root or stable lock has the wrong file type")
	}
	assertPrivateOwnedPath(t, root, 0o700, false)
	assertPrivateOwnedPath(t, lockPath, 0o600, false)
	assertPrivateOwnedPath(t, socketPath, 0o600, true)

	want := wipdfixture.Record{ID: "fixture-record-1", Key: "unequal-key", Value: "durable-value"}
	if err := daemon.fixture.Put(context.Background(), want); err != nil {
		t.Fatalf("fixture Put() error = %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket remains after Close(): %v", err)
	}
	assertSameInode(t, lockBefore, mustLstat(t, lockPath))

	restarted, err := Start(root)
	if err != nil {
		t.Fatalf("Start() after graceful shutdown: %v", err)
	}
	got, err := restarted.fixture.Get(context.Background(), want.Key)
	if err != nil {
		t.Fatalf("fixture Get() after restart: %v", err)
	}
	if got != want {
		t.Fatalf("fixture record after restart = %+v, want %+v", got, want)
	}
	if err := restarted.Close(); err != nil {
		t.Fatalf("Close() after restart: %v", err)
	}
	assertSameInode(t, lockBefore, mustLstat(t, lockPath))
}

func TestFixtureDataSurvivesForegroundCrashAndRestart(t *testing.T) {
	root := testProfileRoot(t)
	want := wipdfixture.Record{ID: "crash-survivor", Key: "asymmetric-key", Value: "durable-after-kill"}
	seeder, err := Start(root)
	if err != nil {
		t.Fatalf("Start() fixture seeder: %v", err)
	}
	if err := seeder.fixture.Put(context.Background(), want); err != nil {
		t.Fatalf("seed daemon-owned fixture: %v", err)
	}
	if err := seeder.Close(); err != nil {
		t.Fatalf("close fixture seeder: %v", err)
	}

	binary := buildWipdBinary(t)
	first := startForegroundWipd(t, binary, root)
	waitForForegroundSocket(t, first, root)
	socketPath := filepath.Join(root, socketFileName)
	lockPath := filepath.Join(root, lockFileName)
	staleSocket := mustLstat(t, socketPath)
	lockInode := mustLstat(t, lockPath)
	if err := first.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL foreground daemon: %v", err)
	}
	if err := first.Wait(); err == nil {
		t.Fatal("SIGKILL foreground daemon exited successfully")
	}
	assertSameInode(t, staleSocket, mustLstat(t, socketPath))

	restarted := startForegroundWipd(t, binary, root)
	waitForForegroundSocket(t, restarted, root)
	assertSameInode(t, lockInode, mustLstat(t, lockPath))
	if err := restarted.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM restarted daemon: %v", err)
	}
	if err := restarted.Wait(); err != nil {
		t.Fatalf("restarted daemon shutdown: %v", err)
	}

	resumed, err := Start(root)
	if err != nil {
		t.Fatalf("Start() to verify recovered fixture: %v", err)
	}
	got, err := resumed.fixture.Get(context.Background(), want.Key)
	if err != nil {
		t.Fatalf("read fixture data after forced crash/restart: %v", err)
	}
	if got != want {
		t.Fatalf("fixture after forced crash/restart = %+v, want %+v", got, want)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("close recovered fixture daemon: %v", err)
	}
}

func TestAcceptAuthenticatesSameUIDSubprocessBeforeExposingBytes(t *testing.T) {
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = daemon.Close() })

	const request = "GET /wipd/v1/negotiate HTTP/1.1\r\nX-Peer-UID: 4294967295\r\n\r\n"
	client := exec.Command(os.Args[0], "-test.run=^TestPeerAuthClientHelper$")
	client.Env = append(os.Environ(), "WIPD_PEER_TEST_CLIENT=1", "WIPD_PEER_SOCKET="+filepath.Join(root, socketFileName), "WIPD_PEER_BYTES="+request)
	client.Stdout = io.Discard
	client.Stderr = io.Discard
	if err := client.Start(); err != nil {
		t.Fatalf("start same-UID client subprocess: %v", err)
	}
	t.Cleanup(func() {
		if client.ProcessState == nil {
			_ = client.Process.Kill()
			_ = client.Wait()
		}
	})

	type acceptResult struct {
		connection net.Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := daemon.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()
	var result acceptResult
	select {
	case result = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("same-UID subprocess did not reach the authenticated accept seam")
	}
	if result.err != nil {
		t.Fatalf("Accept() rejected same-UID subprocess: %v", result.err)
	}
	defer result.connection.Close()
	if err := result.connection.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline after authenticated Accept(): %v", err)
	}
	got := make([]byte, len(request))
	if _, err := io.ReadFull(result.connection, got); err != nil {
		t.Fatalf("read bytes after authenticated Accept(): %v", err)
	}
	if string(got) != request {
		t.Fatalf("bytes after authenticated Accept() = %q, want untouched request %q", got, request)
	}
	if err := client.Wait(); err != nil {
		t.Fatalf("same-UID client subprocess: %v", err)
	}
}

func TestPeerAuthClientHelper(t *testing.T) {
	if os.Getenv("WIPD_PEER_TEST_CLIENT") != "1" {
		return
	}
	connection, err := net.Dial("unix", os.Getenv("WIPD_PEER_SOCKET"))
	if err != nil {
		t.Fatalf("dial peer-auth test socket: %v", err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, os.Getenv("WIPD_PEER_BYTES")); err != nil {
		t.Fatalf("write peer-auth test bytes: %v", err)
	}
}

func TestPeerCredentialUnavailableFailsClosed(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := peerEffectiveUID(left); !errors.Is(err, ErrPeerCredentials) {
		t.Fatalf("peerEffectiveUID(net.Pipe()) error = %v, want ErrPeerCredentials", err)
	}
}

func TestAcceptRechecksPrivateRootAndSocketModes(t *testing.T) {
	tests := []struct {
		name    string
		path    func(string) string
		mode    os.FileMode
		restore os.FileMode
	}{
		{name: "profile root", path: func(root string) string { return root }, mode: 0o755, restore: 0o700},
		{name: "socket", path: func(root string) string { return filepath.Join(root, socketFileName) }, mode: 0o666, restore: 0o600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testProfileRoot(t)
			daemon, err := Start(root)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			t.Cleanup(func() { _ = daemon.Close() })
			path := test.path(root)
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			client, err := net.Dial("unix", filepath.Join(root, socketFileName))
			if err != nil {
				t.Fatalf("connect for path recheck: %v", err)
			}
			connection, err := daemon.Accept()
			_ = client.Close()
			if connection != nil {
				_ = connection.Close()
				t.Fatal("Accept() returned a stream after profile/socket permissions changed")
			}
			if !errors.Is(err, ErrUnsafeSocket) {
				t.Fatalf("Accept() error = %v, want ErrUnsafeSocket", err)
			}
			if err := os.Chmod(path, test.restore); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDistinctUIDSubprocessRejectedBeforeDownstreamEffects(t *testing.T) {
	setpriv, err := exec.LookPath("setpriv")
	if err != nil {
		t.Skipf("distinct-UID subprocess capability unavailable: setpriv not found: %v", err)
	}
	uid := uint32(65534)
	if uid == uint32(os.Geteuid()) {
		uid = 65533
	}
	uidText := strconv.FormatUint(uint64(uid), 10)
	probe := exec.Command(setpriv, "--reuid", uidText, "/usr/bin/id", "-u")
	probeOutput, err := probe.CombinedOutput()
	if err != nil {
		t.Skipf("distinct-UID subprocess capability unavailable: setpriv --reuid %s failed: %v (%s)", uidText, err, strings.TrimSpace(string(probeOutput)))
	}
	if strings.TrimSpace(string(probeOutput)) != uidText {
		t.Fatalf("setpriv reported UID %q, want %s", strings.TrimSpace(string(probeOutput)), uidText)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("distinct-UID subprocess capability unavailable: python3 not found: %v", err)
	}

	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	wantRecord := wipdfixture.Record{ID: "peer-auth-sentinel", Key: "fixture-key", Value: "unchanged-by-peer"}
	if err := daemon.fixture.Put(context.Background(), wantRecord); err != nil {
		t.Fatalf("seed fixture sentinel: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(root, socketFileName), 0o600)
		_ = os.Chmod(root, 0o700)
		_ = os.Chmod(filepath.Dir(root), 0o700)
		_ = daemon.Close()
	})
	if err := os.Chmod(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, socketFileName), 0o666); err != nil {
		t.Fatal(err)
	}

	const clientScript = `import socket,sys
s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
s.connect(sys.argv[1])
s.sendall(("GET / HTTP/1.1\r\nX-Peer-UID: "+sys.argv[2]+"\r\n\r\n").encode())
s.settimeout(5)
try:
    reply=s.recv(1)
except (ConnectionResetError, BrokenPipeError):
    reply=b""
sys.exit(0 if reply == b"" else 3)`
	client := exec.Command(setpriv, "--reuid", uidText, python, "-c", clientScript, filepath.Join(root, socketFileName), strconv.Itoa(os.Geteuid()))
	client.Stdout = io.Discard
	client.Stderr = io.Discard
	if err := client.Start(); err != nil {
		t.Fatalf("start distinct-UID peer client: %v", err)
	}
	t.Cleanup(func() {
		if client.ProcessState == nil {
			_ = client.Process.Kill()
			_ = client.Wait()
		}
	})

	type acceptResult struct {
		connection net.Conn
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		connection, err := daemon.Accept()
		accepted <- acceptResult{connection: connection, err: err}
	}()
	var result acceptResult
	select {
	case result = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("distinct-UID client did not reach peer authentication")
	}
	if result.connection != nil {
		_ = result.connection.Close()
		t.Fatal("distinct-UID peer received an authenticated stream")
	}
	if err := client.Wait(); err != nil {
		t.Fatalf("distinct-UID client: %v", err)
	}
	if !errors.Is(result.err, ErrPeerUIDMismatch) {
		t.Fatalf("Accept() error = %v, want ErrPeerUIDMismatch", result.err)
	}
	gotRecord, err := daemon.fixture.Get(context.Background(), wantRecord.Key)
	if err != nil {
		t.Fatalf("read fixture sentinel after rejected peer: %v", err)
	}
	if gotRecord != wantRecord {
		t.Fatalf("fixture changed after rejected peer: got %+v, want %+v", gotRecord, wantRecord)
	}
}

func TestUnsafePreexistingSocketPathsFailClosedAndReleaseLock(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) func()
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, path string) func() {
				if err := os.WriteFile(path, []byte("keep-this-file"), 0o600); err != nil {
					t.Fatal(err)
				}
				return func() {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != "keep-this-file" {
						t.Errorf("regular path changed: bytes=%q error=%v", got, err)
					}
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) func() {
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("keep-target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return func() {
					got, err := os.ReadFile(target)
					if err != nil || string(got) != "keep-target" {
						t.Errorf("symlink target changed: bytes=%q error=%v", got, err)
					}
				}
			},
		},
		{
			name: "unsafe socket mode",
			setup: func(t *testing.T, path string) func() {
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o660); err != nil {
					t.Fatal(err)
				}
				before := mustLstat(t, path)
				return func() { assertSameInode(t, before, mustLstat(t, path)) }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := testProfileRoot(t)
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(root, socketFileName)
			assertUnchanged := test.setup(t, socketPath)
			if _, err := Start(root); !errors.Is(err, ErrUnsafeSocket) {
				t.Fatalf("Start() error = %v, want ErrUnsafeSocket", err)
			}
			assertUnchanged()
			if _, err := os.Stat(filepath.Join(root, "m4-test-fixture.sqlite")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe socket path was inspected after fixture open: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, lockFileName)); err != nil {
				t.Fatalf("stable lock inode was not retained after refusal: %v", err)
			}

			if test.name == "regular file" {
				if err := os.Remove(socketPath); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "symlink" || test.name == "unsafe socket mode" {
				if err := os.Remove(socketPath); err != nil {
					t.Fatal(err)
				}
			}
			daemon, err := Start(root)
			if err != nil {
				t.Fatalf("Start() after removing refused path: %v", err)
			}
			if err := daemon.Close(); err != nil {
				t.Fatalf("Close() after recovery: %v", err)
			}
		})
	}
}

func TestLiveUnownedSocketIsNotReplaced(t *testing.T) {
	root := testProfileRoot(t)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, socketFileName)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mustLstat(t, socketPath)
	if _, err := Start(root); !errors.Is(err, ErrSocketLive) {
		t.Fatalf("Start() error = %v, want ErrSocketLive", err)
	}
	assertSameInode(t, before, mustLstat(t, socketPath))
	if _, err := os.Stat(filepath.Join(root, "m4-test-fixture.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live unowned socket was inspected after fixture open: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("Start() after live listener removal: %v", err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatalf("Close() after live listener refusal: %v", err)
	}
}

func TestCloseLeavesReplacementSocketInodeUntouched(t *testing.T) {
	root := testProfileRoot(t)
	daemon, err := Start(root)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	socketPath := filepath.Join(root, socketFileName)
	movedOriginal := filepath.Join(root, "original.sock")
	if err := os.Rename(socketPath, movedOriginal); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.SetUnlinkOnClose(false)
	replacementInfo := mustLstat(t, socketPath)
	if err := daemon.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertSameInode(t, replacementInfo, mustLstat(t, socketPath))
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(movedOriginal); err != nil {
		t.Fatal(err)
	}
}

func testProfileRoot(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "w4-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg-unrelated"))
	t.Setenv("WIP_DB_PATH", "")
	return filepath.Join(base, "profile")
}

func buildWipdBinary(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate daemon test")
	}
	commandDir := filepath.Join(filepath.Dir(source), "../../cmd/wipd")
	binary := filepath.Join(t.TempDir(), "wipd")
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = commandDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build foreground wipd: %v\n%s", err, output)
	}
	return binary
}

func startForegroundWipd(t *testing.T, binary, root string) *exec.Cmd {
	t.Helper()
	command := exec.Command(binary, "--profile-root", root)
	if err := command.Start(); err != nil {
		t.Fatalf("start foreground wipd: %v", err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return command
}

func waitForForegroundSocket(t *testing.T, command *exec.Cmd, root string) {
	t.Helper()
	path := filepath.Join(root, socketFileName)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err == nil {
			if connection, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
				_ = connection.Close()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Process.Kill()
	if err := command.Wait(); err != nil {
		t.Fatalf("foreground daemon failed before socket readiness: %v", err)
	}
	t.Fatal("timed out waiting for foreground daemon socket")
}

func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}
	return info
}

func assertSameInode(t *testing.T, before, after os.FileInfo) {
	t.Helper()
	if !os.SameFile(before, after) {
		t.Fatalf("inode changed: before=%v after=%v", before, after)
	}
}

func assertPrivateOwnedPath(t *testing.T, path string, mode os.FileMode, socket bool) {
	t.Helper()
	info := mustLstat(t, path)
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), mode)
	}
	if socket != (info.Mode()&os.ModeSocket != 0) {
		t.Fatalf("%s socket type = %v, want socket=%v", path, info.Mode(), socket)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Uid) != uint64(os.Geteuid()) {
		t.Fatalf("%s does not report the effective daemon UID owner", path)
	}
}
