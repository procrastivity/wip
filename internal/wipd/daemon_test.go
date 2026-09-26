//go:build linux

package wipd

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
