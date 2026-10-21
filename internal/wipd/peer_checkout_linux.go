//go:build linux

package wipd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/procrastivity/wip/internal/remotekey"
	"golang.org/x/sys/unix"
)

// DiscoverGitCheckout snapshots Git identity from the peer's live CWD. It
// opens that CWD once, executes Git against the opened directory descriptor,
// and revalidates both the peer CWD and Git-derived identity before returning.
func (p *PeerProcess) DiscoverGitCheckout(ctx context.Context, selectedRemote string) (GitCheckout, error) {
	var empty GitCheckout
	if selectedRemote == "" {
		selectedRemote = "origin"
	}
	cwd, err := p.OpenWorkingDirectory()
	if err != nil {
		return empty, err
	}
	defer func() { _ = cwd.Close() }()
	first, err := readGitCheckout(ctx, cwd, selectedRemote)
	if err != nil {
		return empty, err
	}
	if err = p.RevalidateWorkingDirectory(cwd); err != nil {
		return empty, err
	}
	second, err := readGitCheckout(ctx, cwd, selectedRemote)
	if err != nil || first != second {
		return empty, errors.Join(ErrPeerProcessUnavailable, errors.New("git checkout identity changed during discovery"), err)
	}
	if err = p.RevalidateWorkingDirectory(cwd); err != nil {
		return empty, err
	}
	return first, nil
}

// D115CheckoutCorrelation derives an opaque, daemon-local create intent
// binding from the authenticated peer's Git common directory and worktree.
// It does not read or require a remote and never sends either path remotely.
func (p *PeerProcess) D115CheckoutCorrelation(ctx context.Context) (string, error) {
	var empty string
	cwd, err := p.OpenWorkingDirectory()
	if err != nil {
		return empty, err
	}
	defer func() { _ = cwd.Close() }()
	commonDir, err := runGitAt(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return empty, err
	}
	gitDir, err := runGitAt(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil || commonDir == "" || gitDir == "" {
		return empty, errors.Join(ErrPeerProcessUnavailable, err)
	}
	if err = p.RevalidateWorkingDirectory(cwd); err != nil {
		return empty, err
	}
	digest := sha256.Sum256([]byte("wipd/d115-checkout-binding/v1\x00" + commonDir + "\x00" + gitDir))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func readGitCheckout(ctx context.Context, cwd *os.File, selectedRemote string) (GitCheckout, error) {
	var empty GitCheckout
	commonDir, err := runGitAt(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return empty, err
	}
	gitDir, err := runGitAt(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return empty, err
	}
	if commonDir == "" || gitDir == "" {
		return empty, errors.New("wipd: peer cwd is not a Git checkout")
	}
	remotes, err := runGitAt(ctx, cwd, "remote")
	if err != nil {
		return empty, err
	}
	found := false
	for _, name := range strings.Fields(remotes) {
		if name == selectedRemote {
			found = true
			break
		}
	}
	if !found {
		return empty, fmt.Errorf("wipd: selected Git remote %q is unavailable", selectedRemote)
	}
	raw, err := runGitAt(ctx, cwd, "remote", "get-url", selectedRemote)
	if err != nil {
		return empty, err
	}
	normalized, err := remotekey.Normalize(raw)
	if err != nil {
		return empty, fmt.Errorf("wipd: selected Git remote is invalid: %w", err)
	}
	worktreeName := ""
	if commonDir != gitDir {
		worktreeName = filepath.Base(gitDir)
	}
	return GitCheckout{
		CommonDir: commonDir, GitDir: gitDir, WorktreeName: worktreeName,
		RemoteName: selectedRemote, NormalizedRemote: normalized,
	}, nil
}

func runGitAt(ctx context.Context, cwd *os.File, args ...string) (string, error) {
	if cwd == nil {
		return "", ErrPeerProcessUnavailable
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = filepath.Join("/proc", fmt.Sprint(os.Getpid()), "fd", fmt.Sprint(cwd.Fd()))
	command.Env = safeGitEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("wipd: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func safeGitEnvironment() []string {
	var environment []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			environment = append(environment, item)
		}
	}
	return append(environment, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
}

// CheckAlive uses the pidfd returned by SO_PEERPIDFD, not a fresh PID lookup.
func (p *PeerProcess) CheckAlive() error {
	return p.checkAlive()
}

func (p *PeerProcess) checkAlive() error {
	return p.checkAliveWithPoll(unix.Poll)
}

func (p *PeerProcess) checkAliveWithPoll(poll func([]unix.PollFd, int) (int, error)) error {
	if p == nil {
		return ErrPeerProcessUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pid <= 0 || p.pidfd == nil {
		return ErrPeerProcessUnavailable
	}
	fd := int(p.pidfd.Fd())
	if fd < 0 {
		return ErrPeerProcessUnavailable
	}
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	var count int
	for {
		fds[0].Revents = 0
		var err error
		count, err = poll(fds, 0)
		if !errors.Is(err, unix.EINTR) {
			if err != nil {
				return fmt.Errorf("%w: check pidfd: %v", ErrPeerProcessUnavailable, err)
			}
			break
		}
	}
	if count != 0 && fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
		return ErrPeerProcessUnavailable
	}
	return nil
}

// OpenWorkingDirectory takes the request's CWD snapshot by opening the
// kernel-authenticated peer's procfs cwd link. Callers must retain this handle
// for Git inspection and invoke RevalidateWorkingDirectory before accepting
// any resulting identity.
func (p *PeerProcess) OpenWorkingDirectory() (*os.File, error) {
	if err := p.checkAlive(); err != nil {
		return nil, err
	}
	path := filepath.Join("/proc", fmt.Sprint(p.pid), "cwd")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("wipd: open authenticated peer cwd: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wipd-peer-cwd")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrPeerProcessUnavailable
	}
	if err = verifyDirectoryFD(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = p.checkAlive(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err = p.RevalidateWorkingDirectory(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// RevalidateWorkingDirectory confirms that the live peer still has the exact
// directory opened at the request snapshot. Git callers additionally execute
// with that opened directory as their child cwd, not the procfs path string.
func (p *PeerProcess) RevalidateWorkingDirectory(snapshot *os.File) error {
	if snapshot == nil {
		return ErrPeerProcessUnavailable
	}
	if err := p.checkAlive(); err != nil {
		return err
	}
	currentFD, err := unix.Open(filepath.Join("/proc", fmt.Sprint(p.pid), "cwd"),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("wipd: re-open authenticated peer cwd: %w", err)
	}
	current := os.NewFile(uintptr(currentFD), "wipd-peer-current-cwd")
	if current == nil {
		_ = unix.Close(currentFD)
		return ErrPeerProcessUnavailable
	}
	defer func() { _ = current.Close() }()
	var before, after unix.Stat_t
	if err = unix.Fstat(int(snapshot.Fd()), &before); err != nil {
		return fmt.Errorf("wipd: inspect opened peer cwd: %w", err)
	}
	if err = unix.Fstat(int(current.Fd()), &after); err != nil {
		return fmt.Errorf("wipd: inspect current peer cwd: %w", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR || after.Mode&unix.S_IFMT != unix.S_IFDIR ||
		before.Dev != after.Dev || before.Ino != after.Ino {
		return errors.Join(ErrPeerProcessUnavailable, errors.New("peer cwd changed during discovery"))
	}
	return p.checkAlive()
}

func verifyDirectoryFD(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("wipd: inspect opened peer cwd: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrPeerProcessUnavailable
	}
	return nil
}
