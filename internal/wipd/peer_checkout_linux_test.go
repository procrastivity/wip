//go:build linux

package wipd

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPeerProcessCheckAliveRetriesInterruptedPoll(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = reader.Close()
		_ = writer.Close()
	}()
	peer := &PeerProcess{pid: os.Getpid(), pidfd: reader}
	var calls int
	err = peer.checkAliveWithPoll(func(fds []unix.PollFd, timeout int) (int, error) {
		calls++
		if timeout != 0 || len(fds) != 1 || int(fds[0].Fd) != int(reader.Fd()) || fds[0].Events != unix.POLLIN {
			t.Fatalf("poll call %d received fds=%+v timeout=%d", calls, fds, timeout)
		}
		if calls == 1 {
			return 0, unix.EINTR
		}
		return 0, nil
	})
	if err != nil {
		t.Fatalf("pidfd liveness check after interrupted poll: %v", err)
	}
	if calls != 2 {
		t.Fatalf("poll calls = %d, want one EINTR retry", calls)
	}

	err = peer.checkAliveWithPoll(func([]unix.PollFd, int) (int, error) {
		return 0, errors.New("unexpected poll failure")
	})
	if !errors.Is(err, ErrPeerProcessUnavailable) {
		t.Fatalf("non-EINTR poll error = %v, want ErrPeerProcessUnavailable", err)
	}
}
