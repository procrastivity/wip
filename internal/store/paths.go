package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// appName names the store's subdirectory under $XDG_DATA_HOME.
const appName = "wip"

// hostname is os.Hostname, indirected so a test can pin the host key without
// depending on the machine it runs on.
var hostname = os.Hostname

// DataDir returns $XDG_DATA_HOME/wip/<host> — the directory that holds the
// store and everything the store owns by reference.
//
// The host key is D49: state stays host-local (D34), and the key exists only so
// a home directory shared or synced across hosts never lets two hosts collide
// on one database.
func DataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("store: resolving XDG_DATA_HOME fallback: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	host, err := hostname()
	if err != nil {
		return "", fmt.Errorf("store: resolving host key: %w", err)
	}
	if host == "" {
		return "", fmt.Errorf("store: this host reports an empty hostname; the store path needs a host key (D49)")
	}
	return filepath.Join(base, appName, host), nil
}

// DBPath returns $XDG_DATA_HOME/wip/<host>/wip.db (D35, D49) — one SQLite
// database per user per host, holding everything durable.
//
// WIP_DB_PATH, when set, overrides the computed path outright — the same
// escape-hatch pattern manifest-install's WIP_CLAUDE_SKILLS_DIR uses, so
// tests and worked examples exercise the real store through the real verbs
// without ever touching the one on the host that ran them.
func DBPath() (string, error) {
	if p := os.Getenv("WIP_DB_PATH"); p != "" {
		return p, nil
	}
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wip.db"), nil
}

// blobDirFor returns the sidecar blob directory beside a store at dbPath:
// $XDG_DATA_HOME/wip/<host>/blobs for the real store, and a sibling of the
// database for any test store. Oversized ingested content spills here and the
// content row keeps the reference — the store owns the reference either way
// (PLAN 1.2). See content.go for the threshold and the resolution path.
func blobDirFor(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "blobs")
}
