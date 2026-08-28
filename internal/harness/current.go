package harness

import (
	"fmt"
	"os"

	"github.com/procrastivity/wip/internal/manifest"
)

// IsCurrent reports whether the tree at dir already is exactly what the
// current binary would install: dir carries a stamp, the checksums of
// files (the current binary's generated output) match that stamp, and the
// tree on disk matches it too. All three must hold — a missing stamp, a
// binary that would generate something different (a verb added since), or
// a file changed on disk all make the tree not current. It is the
// `wip install` verb's "nothing to write" test, run after RefuseHandEdited
// has cleared the tree; doctor's stale-artifact check asks the middle
// question alone (binary vs stamp) and reports drift, where this asks
// whether a rewrite would change anything at all.
func IsCurrent(dir string, files map[string][]byte) (bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("harness: checking install dir %q: %w", dir, err)
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	// The stamp's toolVersion is deliberately not compared here: a version
	// bump whose generated content is byte-identical is still current
	// (Matter decision) — only the checksums below decide.
	if drift := manifest.Drift(manifest.ChecksumFiles(files), stamp); len(drift) > 0 {
		return false, nil
	}

	actual, err := manifest.ChecksumTree(dir)
	if err != nil {
		return false, err
	}
	if drift := manifest.Drift(actual, stamp); len(drift) > 0 {
		return false, nil
	}

	return true, nil
}
