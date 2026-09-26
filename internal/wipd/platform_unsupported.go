//go:build !linux

package wipd

import (
	"net"
	"os"
)

func ensurePrivateDirectory(string) error { return ErrUnsupported }

func acquireProcessLock(string) (*os.File, error) { return nil, ErrUnsupported }

func removeProvenStaleSocket(string) error { return ErrUnsupported }

func bindPrivateSocket(string) (net.Listener, socketIdentity, error) {
	return nil, socketIdentity{}, ErrUnsupported
}

func removeOwnedSocket(string, socketIdentity) error { return ErrUnsupported }
