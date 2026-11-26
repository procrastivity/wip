// Package localtransfer implements the local-file workflow around portable
// snapshots. It never loads tool configuration or calls a transport.
package localtransfer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// Capture requires a frozen DB/blob set. It copies files before making any
// SQLite connection, including the committed WAL, and normalizes only that
// copy. Hashing before and after detects writers rather than ignoring WAL.
func Capture(ctx context.Context, source, destination string) (fingerprint string, err error) {
	source, err = physicalDB(source)
	if err != nil {
		return "", err
	}
	before, err := Fingerprint(source)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destination)
		}
	}()
	raw := filepath.Join(destination, "capture.db")
	for _, suffix := range []string{"", "-wal"} {
		if err := copyFile(source+suffix, raw+suffix); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			return "", err
		}
	}
	blobDir := filepath.Join(filepath.Dir(source), "blobs")
	if err := copyBlobs(blobDir, filepath.Join(destination, "blobs")); err != nil {
		return "", err
	}
	after, err := Fingerprint(source)
	if err != nil {
		return "", err
	}
	if before != after {
		return "", fmt.Errorf("transfer: store changed during capture; stop writers and retry on a frozen copy")
	}
	// SQLite may create sidecars here, but this is already a private copy.
	u := &url.URL{Scheme: "file", Path: raw}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return "", fmt.Errorf("transfer: capture integrity: %w", err)
	}
	if integrity != "ok" {
		return "", fmt.Errorf("transfer: capture integrity: %s", integrity)
	}
	dest := filepath.Join(destination, "wip.db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return "", fmt.Errorf("transfer: normalize capture: %w", err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return "", err
	}
	complete = true
	return before, nil
}

// Inspect opens only a disposable normalized copy. cleanup must always run.
func Inspect(ctx context.Context, path string) (s *store.Store, fingerprint string, cleanup func(), err error) {
	root, err := os.MkdirTemp("", "wip-transfer-")
	if err != nil {
		return nil, "", nil, err
	}
	remove := func() { _ = os.RemoveAll(root) }
	fingerprint, err = Capture(ctx, path, filepath.Join(root, "store"))
	if err == nil {
		s, err = store.Open(filepath.Join(root, "store", "wip.db"))
	}
	if err != nil {
		remove()
		return nil, "", nil, err
	}
	return s, fingerprint, func() { _ = s.Close(); remove() }, nil
}

func physicalDB(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("transfer: an explicit existing database path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("transfer: database is not a regular file")
	}
	return path, nil
}

// Fingerprint deliberately includes physical DB/WAL and every sibling blob.
// It does not open SQLite or inspect config/credential files.
func Fingerprint(path string) (string, error) {
	path, err := physicalDB(path)
	if err != nil {
		return "", err
	}
	files := map[string]string{"database": path}
	if _, err := os.Lstat(path + "-wal"); err == nil {
		files["wal"] = path + "-wal"
	} else if !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "blobs")
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("transfer: blobs must be a real directory, not a symlink")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			files["blobs/"+entry.Name()] = filepath.Join(dir, entry.Name())
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		file := files[name]
		info, err := os.Lstat(file)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("transfer: %s must be a regular file, not a link or directory", name)
		}
		in, err := os.Open(file)
		if err != nil {
			return "", err
		}
		_, headerErr := fmt.Fprintf(sum, "%s\x00%d\x00", name, info.Size())
		_, copyErr := io.Copy(sum, in)
		closeErr := in.Close()
		if headerErr != nil {
			return "", headerErr
		}
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func copyFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("transfer: refusing non-regular file %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func copyBlobs(source, destination string) error {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("transfer: refusing linked/non-directory blobs")
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.ContainsAny(entry.Name(), `/\\`) {
			return fmt.Errorf("transfer: invalid blob filename")
		}
		if err := copyFile(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func capturedSchema(ctx context.Context, normalized string) (int, error) {
	u := &url.URL{Scheme: "file", Path: filepath.Join(filepath.Dir(normalized), "capture.db")}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var version int
	err = db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&version)
	return version, err
}
