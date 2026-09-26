//go:build linux

package wipd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/procrastivity/wip/internal/wipdprofile"
	"golang.org/x/sys/unix"
)

func ensurePrivateDirectory(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("wipd: create private profile directory: %w", err)
	}
	pathInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("wipd: inspect private profile directory: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return errors.New("wipd: profile root is not a nonsymlink directory")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("wipd: open private profile directory: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("wipd: verify private profile directory: %w", err)
	}
	if uint64(stat.Uid) != uint64(os.Geteuid()) {
		return errors.New("wipd: profile directory is not owned by the effective daemon UID")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("wipd: profile root is not a directory")
	}
	pathStat, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(pathStat.Dev) != uint64(stat.Dev) || pathStat.Ino != stat.Ino {
		return errors.New("wipd: profile directory changed during verification")
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return fmt.Errorf("wipd: secure private profile directory: %w", err)
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("wipd: verify secured profile directory: %w", err)
	}
	if stat.Mode&0o7777 != 0o700 || uint64(stat.Uid) != uint64(os.Geteuid()) {
		return errors.New("wipd: profile directory is not owned and mode 0700")
	}
	pathInfo, err = os.Lstat(root)
	if err != nil {
		return fmt.Errorf("wipd: recheck secured profile directory: %w", err)
	}
	pathStat, ok = pathInfo.Sys().(*syscall.Stat_t)
	if !ok || pathInfo.Mode()&os.ModeSymlink != 0 || uint64(pathStat.Dev) != uint64(stat.Dev) || pathStat.Ino != stat.Ino {
		return errors.New("wipd: profile directory changed during securing")
	}
	return nil
}

func privateDirectoryIdentity(root string) (directoryIdentity, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return directoryIdentity{}, fmt.Errorf("wipd: inspect private profile: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return directoryIdentity{}, fmt.Errorf("%w: profile root is not a mode-0700 directory", ErrUnsafeSocket)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return directoryIdentity{}, fmt.Errorf("wipd: open private profile: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return directoryIdentity{}, fmt.Errorf("wipd: inspect open private profile: %w", err)
	}
	pathStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || uint64(stat.Uid) != uint64(os.Geteuid()) || uint64(pathStat.Dev) != uint64(stat.Dev) || pathStat.Ino != stat.Ino {
		return directoryIdentity{}, fmt.Errorf("%w: profile directory identity or owner changed", ErrUnsafeSocket)
	}
	return directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: uint64(stat.Uid)}, nil
}

func verifySocketPath(root, socket string, expectedRoot directoryIdentity, expectedSocket socketIdentity) error {
	profile, err := wipdprofile.Resolve(root)
	if err != nil || profile.Root != root {
		return fmt.Errorf("%w: profile parent chain is no longer trusted", ErrUnsafeSocket)
	}
	if filepath.Dir(socket) != root || filepath.Base(socket) != socketFileName {
		return fmt.Errorf("%w: socket is outside the verified profile root", ErrUnsafeSocket)
	}
	actualRoot, err := privateDirectoryIdentity(root)
	if err != nil {
		return err
	}
	if actualRoot != expectedRoot {
		return fmt.Errorf("%w: profile directory inode changed", ErrUnsafeSocket)
	}
	info, err := os.Lstat(socket)
	if err != nil {
		return fmt.Errorf("%w: inspect listener path: %v", ErrUnsafeSocket, err)
	}
	actualSocket, err := checkedSocketIdentity(info)
	if err != nil {
		return err
	}
	if actualSocket != expectedSocket {
		return fmt.Errorf("%w: listener socket inode changed", ErrUnsafeSocket)
	}
	return nil
}

func peerEffectiveUID(connection net.Conn) (uint64, error) {
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, ErrPeerCredentials
	}
	rawConnection, err := unixConnection.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrPeerCredentials, err)
	}
	var peerUID uint32
	var credentialErr error
	if err := rawConnection.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credentialErr = err
			return
		}
		peerUID = credentials.Uid
	}); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrPeerCredentials, err)
	}
	if credentialErr != nil {
		return 0, fmt.Errorf("%w: %v", ErrPeerCredentials, credentialErr)
	}
	return uint64(peerUID), nil
}

func acquireProcessLock(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wipd: open stable singleton lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("wipd: inspect singleton lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || uint64(stat.Uid) != uint64(os.Geteuid()) {
		_ = file.Close()
		return nil, errors.New("wipd: singleton lock is not a singly-linked owned regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("wipd: acquire singleton kernel lock: %w", err)
	}

	fileStat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("wipd: recheck opened singleton lock: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileStat, pathInfo) {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("wipd: recheck singleton lock path: %w", err)
		}
		return nil, errors.New("wipd: singleton lock path changed during acquisition")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("wipd: secure singleton lock: %w", err)
	}
	pathInfo, err = os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("wipd: verify stable singleton lock path: %w", err)
	}
	fileStat, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("wipd: verify stable singleton lock handle: %w", err)
	}
	pathStat, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileStat, pathInfo) || pathStat.Mode&0o7777 != 0o600 {
		_ = file.Close()
		return nil, errors.New("wipd: singleton lock changed while securing it")
	}
	return file, nil
}

func removeProvenStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect socket path: %v", ErrUnsafeSocket, err)
	}
	identity, err := checkedSocketIdentity(info)
	if err != nil {
		return err
	}
	connection, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return ErrSocketLive
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: socket probe was inconclusive: %v", ErrUnsafeSocket, err)
	}

	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: recheck stale socket path: %v", ErrUnsafeSocket, err)
	}
	currentIdentity, identityErr := checkedSocketIdentity(current)
	if identityErr != nil {
		return identityErr
	}
	if currentIdentity != identity {
		return fmt.Errorf("%w: socket inode changed during stale probe", ErrUnsafeSocket)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("wipd: remove proven-stale socket: %w", err)
	}
	return nil
}

func checkedSocketIdentity(info os.FileInfo) (socketIdentity, error) {
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		return socketIdentity{}, fmt.Errorf("%w: path is not a mode-0600 Unix socket", ErrUnsafeSocket)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketIdentity{}, ErrUnsupportedOwner
	}
	if uint64(stat.Uid) != uint64(os.Geteuid()) || stat.Mode&0o7777 != 0o600 {
		return socketIdentity{}, fmt.Errorf("%w: socket is not owned by the effective daemon UID", ErrUnsafeSocket)
	}
	return socketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: uint64(stat.Uid)}, nil
}

func bindPrivateSocket(path string) (net.Listener, socketIdentity, error) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, socketIdentity{}, fmt.Errorf("wipd: create Unix socket: %w", err)
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		return nil, socketIdentity{}, fmt.Errorf("wipd: bind Unix socket: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, socketIdentity{}, fmt.Errorf("wipd: inspect bound Unix socket: %w", err)
	}
	identity, err := ownedSocketIdentity(info)
	if err != nil {
		return nil, socketIdentity{}, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = removeOwnedSocket(path, identity)
		return nil, socketIdentity{}, fmt.Errorf("wipd: secure Unix socket: %w", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, socketIdentity{}, fmt.Errorf("wipd: verify secured Unix socket: %w", err)
	}
	pathStat, ok := info.Sys().(*syscall.Stat_t)
	if !sameSocketIdentity(identity, info) || !ok || pathStat.Mode&0o7777 != 0o600 {
		_ = removeOwnedSocket(path, identity)
		return nil, socketIdentity{}, fmt.Errorf("%w: bound socket changed while securing it", ErrUnsafeSocket)
	}
	if err := unix.Listen(fd, 16); err != nil {
		_ = removeOwnedSocket(path, identity)
		return nil, socketIdentity{}, fmt.Errorf("wipd: listen on Unix socket: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	listener, err := net.FileListener(file)
	_ = file.Close()
	fd = -1
	if err != nil {
		_ = removeOwnedSocket(path, identity)
		return nil, socketIdentity{}, fmt.Errorf("wipd: wrap Unix listener: %w", err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		_ = removeOwnedSocket(path, identity)
		return nil, socketIdentity{}, errors.New("wipd: operating system returned a non-Unix listener")
	}
	unixListener.SetUnlinkOnClose(false)
	return unixListener, identity, nil
}

func ownedSocketIdentity(info os.FileInfo) (socketIdentity, error) {
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return socketIdentity{}, fmt.Errorf("%w: bound path is not a Unix socket", ErrUnsafeSocket)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketIdentity{}, ErrUnsupportedOwner
	}
	if uint64(stat.Uid) != uint64(os.Geteuid()) {
		return socketIdentity{}, fmt.Errorf("%w: bound socket is not owned by the effective daemon UID", ErrUnsafeSocket)
	}
	return socketIdentity{device: uint64(stat.Dev), inode: stat.Ino, owner: uint64(stat.Uid)}, nil
}

func sameSocketIdentity(identity socketIdentity, info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Dev) == identity.device && stat.Ino == identity.inode && uint64(stat.Uid) == identity.owner
}

func removeOwnedSocket(path string, identity socketIdentity) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wipd: inspect socket during cleanup: %w", err)
	}
	if !sameSocketIdentity(identity, info) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("wipd: remove owned socket: %w", err)
	}
	return nil
}
