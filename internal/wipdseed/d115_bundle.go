package wipdseed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/procrastivity/wip/internal/d115"
	"golang.org/x/sys/unix"
)

const d115BundleName = "d115-operator-trust.cbor"

// ErrD115BundleDurabilityUnknown reports that rename succeeded but directory
// durability could not be acknowledged; an exact retry may complete the sync.
var ErrD115BundleDurabilityUnknown = errors.New("trust-bundle.install-durability-unknown")

// D115BundleDigest returns the exact displayed SHA-256 used in the owner's
// authenticated out-of-band handoff.
func D115BundleDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// InstallD115OperatorTrustBundle installs a validated public snapshot after
// the caller has performed controlling-TTY confirmation of realm and digest.
// The selected bundle-store must already exist and is never created here.
func InstallD115OperatorTrustBundle(directory string, incoming []byte, confirmedRealm, confirmedDigest string) error {
	return installD115OperatorTrustBundle(directory, incoming, confirmedRealm, confirmedDigest, d115BundleInstallOps{
		writeTemp:     writeD115BundleTemp,
		rename:        os.Rename,
		syncDirectory: syncD115BundleDirectory,
	})
}

type d115BundleInstallOps struct {
	writeTemp     func(string, []byte) (string, error)
	rename        func(string, string) error
	syncDirectory func(string) error
}

func installD115OperatorTrustBundle(directory string, incoming []byte, confirmedRealm, confirmedDigest string, ops d115BundleInstallOps) error {
	if len(incoming) == 0 || len(incoming) > 1<<20 || confirmedDigest != D115BundleDigest(incoming) {
		return errors.New("trust-bundle.confirmation-mismatch")
	}
	bundle, err := d115.DecodeOperatorTrustBundle(incoming)
	if err != nil || bundle.RealmID != confirmedRealm {
		return errors.New("trust-bundle.invalid")
	}
	if err = validateBundleDirectory(directory); err != nil {
		return err
	}
	lockPath := filepath.Join(directory, ".d115-operator-trust.lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	if err = validatePrivateRegular(lockPath, 0o600); err != nil {
		return err
	}
	installedPath := filepath.Join(directory, d115BundleName)
	current, currentErr := readPrivateRegular(installedPath, 1<<20, 0o600)
	switch {
	case currentErr == nil && bytes.Equal(current, incoming):
		if err = syncInstalledBundle(installedPath, directory); err != nil {
			return fmt.Errorf("%w: %v", ErrD115BundleDurabilityUnknown, err)
		}
		return nil
	case currentErr == nil:
		previous, decodeErr := d115.DecodeOperatorTrustBundle(current)
		if decodeErr != nil {
			return errors.New("trust-bundle.installed-invalid")
		}
		if previous.RealmID != bundle.RealmID || bundle.Generation <= previous.Generation {
			return errors.New("trust-bundle.generation-conflict")
		}
	case errors.Is(currentErr, os.ErrNotExist):
	case currentErr != nil:
		return currentErr
	}
	tempPath, err := ops.writeTemp(directory, incoming)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err = ops.rename(tempPath, installedPath); err != nil {
		return err
	}
	if err = ops.syncDirectory(directory); err != nil {
		return fmt.Errorf("%w: %v", ErrD115BundleDurabilityUnknown, err)
	}
	return nil
}

func writeD115BundleTemp(directory string, data []byte) (string, error) {
	temp, err := os.CreateTemp(directory, ".d115-bundle-*")
	if err != nil {
		return "", err
	}
	path := temp.Name()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func syncD115BundleDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func syncInstalledBundle(path, directory string) error {
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	err = errors.Join(err, file.Close())
	return errors.Join(err, syncD115BundleDirectory(directory))
}

// LoadD115OperatorTrustBundle safely reads the selected owner's public bundle.
func LoadD115OperatorTrustBundle(directory string) ([]byte, d115.OperatorTrustBundle, error) {
	if err := validateBundleDirectory(directory); err != nil {
		return nil, d115.OperatorTrustBundle{}, err
	}
	data, err := readPrivateRegular(filepath.Join(directory, d115BundleName), 1<<20, 0o600)
	if err != nil {
		return nil, d115.OperatorTrustBundle{}, err
	}
	bundle, err := d115.DecodeOperatorTrustBundle(data)
	if err != nil {
		clear(data)
		return nil, d115.OperatorTrustBundle{}, err
	}
	return data, bundle, nil
}

func validateBundleDirectory(directory string) error {
	if directory == "" {
		return ErrInvalidClientState
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	for _, part := range splitPath(absolute) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidClientState
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return ErrInvalidClientState
	}
	return nil
}

func splitPath(path string) []string {
	volume := filepath.VolumeName(path)
	rest := bytes.TrimLeft([]byte(path[len(volume):]), string(filepath.Separator))
	if len(rest) == 0 {
		return nil
	}
	return splitPathParts(string(rest))
}

func splitPathParts(path string) []string {
	parts := []string{}
	for _, part := range bytes.Split([]byte(path), []byte{byte(filepath.Separator)}) {
		if len(part) > 0 {
			parts = append(parts, string(part))
		}
	}
	return parts
}

func validatePrivateRegular(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode || !ownedByEffectiveUID(info) {
		return ErrInvalidClientState
	}
	return nil
}

func readPrivateRegular(path string, limit int64, mode os.FileMode) ([]byte, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	} else if err != nil {
		return nil, err
	}
	if err := validatePrivateRegular(path, mode); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode || !ownedByEffectiveUID(info) {
		return nil, ErrInvalidClientState
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, ErrInvalidClientState
	}
	return data, nil
}

// ReadD115OwnerFile reads a bounded mode-0600 owner file without following a symlink.
func ReadD115OwnerFile(path string, limit int64) ([]byte, error) {
	return readPrivateRegular(path, limit, 0o600)
}

// WriteD115OwnerFile atomically creates a new mode-0600 file in an existing owner-private directory.
func WriteD115OwnerFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ownedByEffectiveUID(info) {
		return ErrInvalidClientState
	}
	existing, readErr := readPrivateRegular(path, int64(len(data)), 0o600)
	if readErr == nil {
		exact := bytes.Equal(existing, data)
		clear(existing)
		if exact {
			return nil
		}
		return errors.New("owner-file.output-conflict")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	temp, err := os.CreateTemp(directory, ".d115-owner-output-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(name, path); err != nil {
		existing, readErr := readPrivateRegular(path, int64(len(data)), 0o600)
		if readErr == nil {
			exact := bytes.Equal(existing, data)
			clear(existing)
			if exact {
				err = nil
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if err != nil {
			return err
		}
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func ownedByEffectiveUID(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint32(os.Geteuid()) == stat.Uid
}
