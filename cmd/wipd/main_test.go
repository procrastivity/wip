package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

type childProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stderr *bytes.Buffer
}

func TestForegroundSubprocessLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("foreground lifecycle currently has a Linux implementation only")
	}
	binary := buildDaemonBinary(t)
	var children []*childProcess
	t.Cleanup(func() {
		for _, child := range children {
			select {
			case <-child.done:
			default:
				_ = child.cmd.Process.Kill()
				select {
				case <-child.done:
				case <-time.After(3 * time.Second):
					t.Errorf("timed out reaping wipd child pid %d", child.cmd.Process.Pid)
				}
			}
		}
	})

	root := filepath.Join(shortTempRoot(t), "concurrent-profile")
	first := startDaemon(t, binary, root)
	children = append(children, first)
	second := startDaemon(t, binary, root)
	children = append(children, second)

	var loser, winner *childProcess
	select {
	case <-first.done:
		loser, winner = first, second
		if first.err == nil {
			t.Fatalf("first daemon exited successfully; expected exactly one foreground owner")
		}
	case <-second.done:
		loser, winner = second, first
		if second.err == nil {
			t.Fatalf("second daemon exited successfully; expected exactly one foreground owner")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("simultaneous daemon launches did not produce an exiting lock loser")
	}
	if !bytes.Contains(loser.stderr.Bytes(), []byte("another daemon owns this profile")) {
		t.Fatalf("loser stderr = %q, want singleton-lock refusal", loser.stderr.String())
	}
	if err := waitSocket(root, winner.done, winner); err != nil {
		t.Fatalf("winner failed to publish socket: %v; stderr=%q", err, winner.stderr.String())
	}
	socketPath := filepath.Join(root, "wipd.sock")
	lockPath := filepath.Join(root, "wipd.lock")
	winnerSocket := mustStat(t, socketPath)
	lockInode := mustStat(t, lockPath)
	if !mustStat(t, root).IsDir() || !lockInode.Mode().IsRegular() {
		t.Fatal("profile root or singleton lock has the wrong file type")
	}
	assertOwnerMode(t, root, 0o700, false)
	assertOwnerMode(t, socketPath, 0o600, true)
	assertOwnerMode(t, lockPath, 0o600, false)
	assertLockPIDDiagnostic(t, lockPath, winner.cmd.Process.Pid)
	assertInode(t, winnerSocket, mustStat(t, socketPath))
	lateLoser := startDaemon(t, binary, root)
	children = append(children, lateLoser)
	if err := waitChild(t, lateLoser, 5*time.Second); err == nil {
		t.Fatal("second contender exited successfully while a daemon owned the profile")
	}
	if !bytes.Contains(lateLoser.stderr.Bytes(), []byte("another daemon owns this profile")) {
		t.Fatalf("contender stderr = %q, want singleton-lock refusal", lateLoser.stderr.String())
	}
	assertInode(t, winnerSocket, mustStat(t, socketPath))

	if err := winner.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to winner: %v", err)
	}
	if err := waitChild(t, winner, 5*time.Second); err != nil {
		t.Fatalf("winner did not shut down cleanly on SIGTERM: %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned socket remains after SIGTERM: %v", err)
	}
	assertInode(t, lockInode, mustStat(t, lockPath))

	gracefulRestart := startDaemon(t, binary, root)
	children = append(children, gracefulRestart)
	if err := waitSocket(root, gracefulRestart.done, gracefulRestart); err != nil {
		t.Fatalf("daemon did not restart after SIGTERM: %v; stderr=%q", err, gracefulRestart.stderr.String())
	}
	assertInode(t, lockInode, mustStat(t, lockPath))
	if err := gracefulRestart.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := waitChild(t, gracefulRestart, 5*time.Second); err != nil {
		t.Fatalf("graceful restart shutdown: %v", err)
	}

	crashRoot := filepath.Join(shortTempRoot(t), "crash-profile")
	crashOwner := startDaemon(t, binary, crashRoot)
	children = append(children, crashOwner)
	if err := waitSocket(crashRoot, crashOwner.done, crashOwner); err != nil {
		t.Fatalf("crash-test daemon startup: %v; stderr=%q", err, crashOwner.stderr.String())
	}
	crashSocketPath := filepath.Join(crashRoot, "wipd.sock")
	crashLockPath := filepath.Join(crashRoot, "wipd.lock")
	staleSocket := mustStat(t, crashSocketPath)
	crashLock := mustStat(t, crashLockPath)
	if err := crashOwner.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL daemon: %v", err)
	}
	if err := waitChild(t, crashOwner, 5*time.Second); err == nil {
		t.Fatal("SIGKILL child unexpectedly exited successfully")
	}
	assertInode(t, staleSocket, mustStat(t, crashSocketPath))
	assertLockPIDDiagnostic(t, crashLockPath, crashOwner.cmd.Process.Pid)
	if _, err := net.DialTimeout("unix", crashSocketPath, time.Second); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("post-crash socket probe error = %v, want ECONNREFUSED for stale socket", err)
	}

	crashRestart := startDaemon(t, binary, crashRoot)
	children = append(children, crashRestart)
	if err := waitSocket(crashRoot, crashRestart.done, crashRestart); err != nil {
		t.Fatalf("daemon did not replace proven-stale socket: %v; stderr=%q", err, crashRestart.stderr.String())
	}
	assertInode(t, crashLock, mustStat(t, crashLockPath))
	assertLockPIDDiagnostic(t, crashLockPath, crashRestart.cmd.Process.Pid)
	connection, err := net.DialTimeout("unix", crashSocketPath, time.Second)
	if err != nil {
		t.Fatalf("restarted daemon socket is not live after stale-path replacement: %v", err)
	}
	_ = connection.Close()
	if _, err := os.Stat(filepath.Join(crashRoot, "m4-test-fixture.sqlite")); err != nil {
		t.Fatalf("fixture database missing after forced-crash restart: %v", err)
	}
	if err := crashRestart.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := waitChild(t, crashRestart, 5*time.Second); err != nil {
		t.Fatalf("crash recovery shutdown: %v", err)
	}
}

func buildDaemonBinary(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	binary := filepath.Join(directory, "wipd")
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate cmd/wipd test")
	}
	command := exec.Command("go", "build", "-o", binary, ".")
	command.Dir = filepath.Dir(source)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build disposable wipd binary: %v\n%s", err, output)
	}
	return binary
}

func startDaemon(t *testing.T, binary, root string) *childProcess {
	t.Helper()
	stderr := &bytes.Buffer{}
	command := exec.Command(binary, "--profile-root", root)
	command.Env = daemonEnvironment(t)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start wipd: %v", err)
	}
	child := &childProcess{cmd: command, done: make(chan struct{}), stderr: stderr}
	go func() {
		child.err = command.Wait()
		close(child.done)
	}()
	return child
}

func daemonEnvironment(t *testing.T) []string {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if len(entry) >= len("WIP_DB_PATH=") && entry[:len("WIP_DB_PATH=")] == "WIP_DB_PATH=" {
			continue
		}
		if len(entry) >= len("XDG_DATA_HOME=") && entry[:len("XDG_DATA_HOME=")] == "XDG_DATA_HOME=" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "WIP_DB_PATH=", "XDG_DATA_HOME="+filepath.Join(t.TempDir(), "xdg-unrelated"))
	return env
}

func shortTempRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func waitSocket(root string, childDone <-chan struct{}, child *childProcess) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		path := filepath.Join(root, "wipd.sock")
		if _, err := os.Lstat(path); err == nil {
			if connection, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
				_ = connection.Close()
				return nil
			}
		}
		select {
		case <-childDone:
			return fmt.Errorf("daemon exited before publishing socket: %w", child.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("timed out waiting for Unix socket publication")
}

func waitChild(t *testing.T, child *childProcess, timeout time.Duration) error {
	t.Helper()
	select {
	case <-child.done:
		return child.err
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for wipd pid %d", child.cmd.Process.Pid)
		return errors.New("unreachable")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}
	return info
}

func assertInode(t *testing.T, before, after os.FileInfo) {
	t.Helper()
	if !os.SameFile(before, after) {
		t.Fatalf("inode changed: before=%v after=%v", before, after)
	}
}

func assertLockPIDDiagnostic(t *testing.T, path string, pid int) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock diagnostic: %v", err)
	}
	want := fmt.Sprintf("pid=%d\n", pid)
	if string(got) != want {
		t.Fatalf("lock diagnostic = %q, want %q", got, want)
	}
}

func assertOwnerMode(t *testing.T, path string, mode os.FileMode, socket bool) {
	t.Helper()
	info := mustStat(t, path)
	if info.Mode().Perm() != mode || socket != (info.Mode()&os.ModeSocket != 0) {
		t.Fatalf("%s mode/type = %v, want mode %04o socket=%v", path, info.Mode(), mode, socket)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("%s is not owned by effective UID %d", path, os.Geteuid())
	}
}
