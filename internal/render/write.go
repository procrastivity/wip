package render

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeGenerated writes data to path and leaves it 0444 (step-04): the one
// legitimate writer is this package's own render pass, which briefly holds
// write access to regenerate content, then restores 0444 before returning.
// If path already exists 0444 from a prior render, it is reopened writable
// first — os.WriteFile's mode argument only applies at creation, so a stale
// 0444 file would otherwise refuse the very rewrite this function exists to
// perform.
func writeGenerated(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("render: preparing %s: %w", filepath.Dir(path), err)
	}
	if err := os.Chmod(path, 0o644); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("render: reopening %s for rewrite: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("render: writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		return fmt.Errorf("render: restoring 0444 on %s: %w", path, err)
	}
	return nil
}
