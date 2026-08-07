// Package runlock provides the ephemeral, host-local advisory lock for a Run.
// The lock file is only a kernel lock handle: its presence carries no state.
package runlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Liveness states and unknown-probe causes surfaced on Run reads.
const (
	LivenessLive        = "live"
	LivenessInterrupted = "interrupted"
	LivenessUnknown     = "unknown"

	CauseUnsupported = "probe-unsupported"
	CauseDenied      = "probe-denied"
	CauseFailed      = "probe-failed"
)

// ErrHeld reports that another process holds the Run lock.
var ErrHeld = errors.New("run lock is held")

// Error reports why the operating system could not probe or acquire a lock.
type Error struct {
	Cause string
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %v", e.Cause, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Result is the read-side liveness result. Cause is empty unless State is
// unknown.
type Result struct {
	State string
	Cause string
}

// Lock is a held Run lock. Closing the file releases the kernel lock, including
// when the owning process exits. The file itself is intentionally retained.
type Lock struct {
	file *os.File
}

// Release closes the lock file, releasing the kernel lock. It is idempotent.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return f.Close()
}

// Root resolves the directory that contains Run lock files.
func Root() (string, error) {
	if root := os.Getenv("WIP_RUNTIME_DIR"); root != "" {
		return root, nil
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("runlock: resolving home directory: %w", err)
			}
			base = filepath.Join(home, ".local", "state")
		}
	}
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("runlock: resolving host key: %w", err)
	}
	if host == "" {
		return "", errors.New("runlock: this host reports an empty hostname")
	}
	return filepath.Join(base, "wip", host, "run"), nil
}

func pathFor(runID string) (string, error) {
	if runID == "" {
		return "", errors.New("runlock: empty Run identity")
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, runID+".lock"), nil
}

func open(runID string) (*os.File, error) {
	path, err := pathFor(runID)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, &Error{Cause: classify(err), Err: err}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, &Error{Cause: classify(err), Err: err}
	}
	return f, nil
}

// Acquire takes a non-blocking exclusive advisory lock for runID.
func Acquire(runID string) (*Lock, error) {
	f, err := open(runID)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrHeld
		}
		return nil, &Error{Cause: classify(err), Err: err}
	}
	return &Lock{file: f}, nil
}

// Probe derives liveness without retaining the lock.
func Probe(runID string) (Result, error) {
	f, err := open(runID)
	if err != nil {
		var le *Error
		if errors.As(err, &le) {
			return Result{State: LivenessUnknown, Cause: le.Cause}, nil
		}
		return Result{State: LivenessUnknown, Cause: CauseFailed}, err
	}
	defer func() { _ = f.Close() }()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			return Result{State: LivenessUnknown, Cause: classify(err)}, nil
		}
		return Result{State: LivenessInterrupted}, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return Result{State: LivenessLive}, nil
	}
	return Result{State: LivenessUnknown, Cause: classify(err)}, nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
		return CauseUnsupported
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
		return CauseDenied
	default:
		return CauseFailed
	}
}
