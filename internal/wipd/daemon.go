// Package wipd owns the foreground daemon lifecycle for an explicit private
// profile. The local socket is not authenticated until a later M4 step.
package wipd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/procrastivity/wip/internal/wipdfixture"
	"github.com/procrastivity/wip/internal/wipdprofile"
)

const (
	lockFileName   = "wipd.lock"
	socketFileName = "wipd.sock"
)

var (
	ErrAlreadyRunning   = errors.New("wipd: another daemon owns this profile")
	ErrUnsafeSocket     = errors.New("wipd: existing socket path is unsafe or ambiguous")
	ErrSocketLive       = errors.New("wipd: another listener is active at the socket path")
	ErrUnsupported      = errors.New("wipd: daemon lifecycle is unsupported on this platform")
	ErrUnsupportedOwner = errors.New("wipd: cannot verify effective file ownership")
)

// Daemon owns the profile lock, fixture handle, and bound local listener.
// Closing it removes only the socket inode created by this instance.
type Daemon struct {
	listener net.Listener
	fixture  *wipdfixture.Store
	lock     *os.File
	socket   string
	socketID socketIdentity

	closeOnce sync.Once
	closeErr  error
}

type socketIdentity struct {
	device uint64
	inode  uint64
	owner  uint64
}

// Start resolves and initializes one explicitly named profile. The singleton
// kernel lock is held before fixture state is opened or the socket is touched.
func Start(explicitProfileRoot string) (*Daemon, error) {
	profile, err := wipdprofile.Resolve(explicitProfileRoot)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateDirectory(profile.Root); err != nil {
		return nil, err
	}

	lock, err := acquireProcessLock(filepath.Join(profile.Root, lockFileName))
	if err != nil {
		return nil, err
	}
	d := &Daemon{lock: lock}
	fail := func(err error) (*Daemon, error) {
		if closeErr := d.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}
	if err := writeLockDiagnostic(lock); err != nil {
		return fail(fmt.Errorf("wipd: write singleton-lock diagnostic: %w", err))
	}

	d.socket = filepath.Join(profile.Root, socketFileName)
	if err := removeProvenStaleSocket(d.socket); err != nil {
		return fail(err)
	}

	d.fixture, err = wipdfixture.Open(profile)
	if err != nil {
		return fail(fmt.Errorf("wipd: open isolated fixture: %w", err))
	}
	d.listener, d.socketID, err = bindPrivateSocket(d.socket)
	if err != nil {
		return fail(err)
	}
	return d, nil
}

func writeLockDiagnostic(file *os.File) error {
	// This text is informational only; Flock is the singleton authority.
	if err := file.Truncate(0); err != nil {
		return err
	}
	diagnostic := []byte(fmt.Sprintf("pid=%d\n", os.Getpid()))
	written, err := file.WriteAt(diagnostic, 0)
	if err != nil {
		return err
	}
	if written != len(diagnostic) {
		return errors.New("short write")
	}
	return nil
}

// Listener exposes the bound stream only as a lifecycle seam for the later
// transport step. It performs no peer authentication or request handling.
func (d *Daemon) Listener() net.Listener {
	if d == nil {
		return nil
	}
	return d.listener
}

// Close releases resources in reverse startup order. The lock file is retained
// permanently so every daemon contends on the same inode after restart.
func (d *Daemon) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		var errs []error
		if d.listener != nil {
			if err := d.listener.Close(); err != nil {
				errs = append(errs, fmt.Errorf("wipd: close listener: %w", err))
			}
			if err := removeOwnedSocket(d.socket, d.socketID); err != nil {
				errs = append(errs, err)
			}
		}
		if d.fixture != nil {
			if err := d.fixture.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if d.lock != nil {
			if err := d.lock.Close(); err != nil {
				errs = append(errs, fmt.Errorf("wipd: release singleton lock: %w", err))
			}
		}
		d.closeErr = errors.Join(errs...)
	})
	return d.closeErr
}
