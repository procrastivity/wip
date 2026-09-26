//go:build !linux

package wipd

import (
	"net"
	"os"
)

func ensurePrivateDirectory(string) error { return ErrUnsupported }

func privateDirectoryIdentity(string) (directoryIdentity, error) {
	return directoryIdentity{}, ErrUnsupported
}

func verifySocketPath(string, string, directoryIdentity, socketIdentity) error { return ErrUnsupported }

func peerEffectiveUID(net.Conn) (uint64, error) { return 0, ErrUnsupported }

func acquireProcessLock(string) (*os.File, error) { return nil, ErrUnsupported }

func removeProvenStaleSocket(string) error { return ErrUnsupported }

func bindPrivateSocket(string) (net.Listener, socketIdentity, error) {
	return nil, socketIdentity{}, ErrUnsupported
}

func removeOwnedSocket(string, socketIdentity) error { return ErrUnsupported }
