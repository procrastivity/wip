package harness

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Install renders generate(m) into installDir() and stamps the result with
// tool.version, schemaVersion, and a per-file checksum. It always
// overwrites whatever it finds and writes the stamp unconditionally —
// refusing to overwrite a hand-edited or unstamped target is the
// `wip install` verb's job (Status plus Refusal), not this function's,
// kept out of Install so every harness stays policy-free and so a caller
// that has already decided to overwrite (the verb after --force) needs no
// second flag here.
//
// One body serves every harness, parameterized by the same three facts a
// registry row carries — name, install dir, generator. The per-harness
// packages hold only their paths, their Available probe, and their
// Generate; six byte-identical copies of this body is what this replaced.
func Install(harnessName string, installDir func() (string, error), generate func(manifest.Manifest) (map[string][]byte, error), m manifest.Manifest) (string, error) {
	files, err := generate(m)
	if err != nil {
		return "", err
	}

	dir, err := installDir()
	if err != nil {
		return "", err
	}

	for path, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", fmt.Errorf("harness %s: creating %q: %w", harnessName, filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return "", fmt.Errorf("harness %s: writing %q: %w", harnessName, full, err)
		}
	}

	stamp := manifest.Stamp{
		ToolVersion:   m.Tool.Version,
		SchemaVersion: m.SchemaVersion,
		Files:         manifest.ChecksumFiles(files),
	}
	if err := manifest.WriteStamp(dir, stamp); err != nil {
		return "", err
	}

	return dir, nil
}

// Uninstall removes exactly the stamped tree at installDir() — never more
// (C4.7). It refuses, rather than silently proceeding, on any of Status's
// three unsafe states (hand-edited, foreign, or unreadable content):
// projections are never hand-authored (arch doc §3), and this must not
// silently delete something a human put there by hand. Status is asked
// with a nil files map — uninstall has nothing to generate, so a stale
// tree (C4.6's binary-vs-stamp question) reads as Current and is removed
// like any other current tree; uninstall has no --force, so Refusal's
// remedy here always names removing the tree by hand instead.
func Uninstall(harnessName string, installDir func() (string, error)) (string, error) {
	dir, err := installDir()
	if err != nil {
		return "", err
	}

	s, err := Status(dir, nil)
	if err != nil {
		return "", err
	}
	if s == Missing {
		return "", wiperr.New("not-found.harness-not-installed",
			fmt.Sprintf("not found — no %s skill is installed at %s", harnessName, dir))
	}
	if err := Refusal(harnessName, dir, s, "remove it by hand if that was intentional"); err != nil {
		return "", err
	}

	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("harness %s: removing %q: %w", harnessName, dir, err)
	}
	return dir, nil
}
