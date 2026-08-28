package manifest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ChecksumTree hashes every file under dir except the stamp itself, keyed
// by path relative to dir (slash-separated) — the same shape Drift
// compares against a Stamp's Files map. Every harness's Uninstall calls
// this today to verify the tree on disk still matches what was stamped at
// install time before removing it; install's hand-edit refusal (a later
// step of this Matter) will call it too, to detect content a human added
// or changed since the last install without going through the stamp.
func ChecksumTree(dir string) (map[string]string, error) {
	sums := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == StampFileName {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sums[relSlash] = Checksum(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("manifest: hashing installed tree %q: %w", dir, err)
	}
	return sums, nil
}
