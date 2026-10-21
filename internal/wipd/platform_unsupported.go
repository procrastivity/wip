//go:build !linux

package wipd

import (
	"context"
	"net"
	"os"
)

func ensurePrivateDirectory(string) error { return ErrUnsupported }

func privateDirectoryIdentity(string) (directoryIdentity, error) {
	return directoryIdentity{}, ErrUnsupported
}

func verifySocketPath(string, string, directoryIdentity, socketIdentity) error { return ErrUnsupported }

func peerEffectiveUID(net.Conn) (uint64, error) { return 0, ErrUnsupported }

func authenticatedPeerProcess(net.Conn) (*PeerProcess, error) {
	return nil, ErrUnsupportedCheckoutContext
}

func (*PeerProcess) CheckAlive() error { return ErrUnsupportedCheckoutContext }

func (*PeerProcess) OpenWorkingDirectory() (*os.File, error) {
	return nil, ErrUnsupportedCheckoutContext
}

func (*PeerProcess) RevalidateWorkingDirectory(*os.File) error {
	return ErrUnsupportedCheckoutContext
}

func (*PeerProcess) DiscoverGitCheckout(_ context.Context, _ string) (GitCheckout, error) {
	return GitCheckout{}, ErrUnsupportedCheckoutContext
}

func (*PeerProcess) D115CheckoutCorrelation(context.Context) (string, error) {
	return "", ErrUnsupportedCheckoutContext
}

func acquireProcessLock(string) (*os.File, error) { return nil, ErrUnsupported }

func removeProvenStaleSocket(string) error { return ErrUnsupported }

func bindPrivateSocket(string) (net.Listener, socketIdentity, error) {
	return nil, socketIdentity{}, ErrUnsupported
}

func removeOwnedSocket(string, socketIdentity) error { return ErrUnsupported }
