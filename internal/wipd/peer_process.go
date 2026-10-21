package wipd

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
)

var (
	// ErrUnsupportedCheckoutContext means this platform cannot establish a
	// stable authenticated peer checkout context for Step 11 discovery.
	ErrUnsupportedCheckoutContext = errors.New("wipd: authenticated peer checkout context is unsupported")
	// ErrPeerProcessUnavailable means the authenticated peer exited or its
	// process-bound checkout context can no longer be verified.
	ErrPeerProcessUnavailable = errors.New("wipd: authenticated peer process is unavailable")
)

type peerProcessContextKey struct{}

// PeerProcess is the kernel-authenticated process that opened a daemon Unix
// connection. Its process handle is owned by the accepted connection.
type PeerProcess struct {
	mu    sync.Mutex
	uid   uint64
	pid   int
	pidfd *os.File
}

// GitCheckout is the immutable local discovery snapshot derived from an
// authenticated peer's opened working directory.
type GitCheckout struct {
	CommonDir        string
	GitDir           string
	WorktreeName     string
	RemoteName       string
	NormalizedRemote string
}

// Close releases the retained operating-system process handle.
func (p *PeerProcess) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pidfd == nil {
		return nil
	}
	err := p.pidfd.Close()
	p.pidfd = nil
	return err
}

func peerProcessForConnection(connection net.Conn) *PeerProcess {
	peerConnection, ok := connection.(*peerProcessConn)
	if !ok {
		return nil
	}
	return peerConnection.peer
}

// PID returns the process ID associated with the kernel-issued process handle.
// The value is diagnostic; callers must use the retained handle for liveness.
func (p *PeerProcess) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}

// PeerProcessFromContext returns the peer process bound to an accepted daemon
// connection, or nil when the context did not originate from that boundary.
func PeerProcessFromContext(ctx context.Context) *PeerProcess {
	if ctx == nil {
		return nil
	}
	peer, _ := ctx.Value(peerProcessContextKey{}).(*PeerProcess)
	return peer
}

type peerProcessConn struct {
	net.Conn
	peer *PeerProcess
	once sync.Once
	err  error
}

func (c *peerProcessConn) Close() error {
	if c == nil {
		return net.ErrClosed
	}
	c.once.Do(func() {
		if c.peer != nil {
			c.err = errors.Join(c.err, c.peer.Close())
		}
		c.err = errors.Join(c.err, c.Conn.Close())
	})
	return c.err
}
