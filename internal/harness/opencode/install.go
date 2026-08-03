package opencode

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Install renders m's plumbing-verb subset into InstallDir() and stamps the
// result with tool.version, schemaVersion, and a per-file checksum. It
// always overwrites whatever it finds — regenerating a previously-stamped
// tree is the normal upgrade path; refusing on unstamped content is
// uninstall's job, since that is the one place a human-authored artifact is
// at risk of being silently destroyed.
func Install(m manifest.Manifest) (string, error) {
	files, err := Generate(m)
	if err != nil {
		return "", err
	}

	dir, err := InstallDir()
	if err != nil {
		return "", err
	}

	for path, data := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", fmt.Errorf("opencode: creating %q: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return "", fmt.Errorf("opencode: writing %q: %w", full, err)
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

// Uninstall removes exactly the stamped tree at InstallDir() — never more.
// It refuses, rather than silently proceeding, when the target has no
// stamp or its content no longer matches the stamp (hand-edited or foreign
// content): projections are never hand-authored (arch doc §3), and this
// must not silently delete something a human put there by hand.
func Uninstall() (string, error) {
	dir, err := InstallDir()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", wiperr.New("not-found.harness-not-installed",
			fmt.Sprintf("not found — no %s skill is installed at %s", Name, dir))
	} else if err != nil {
		return "", fmt.Errorf("opencode: checking install dir %q: %w", dir, err)
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", wiperr.New("refusal.unstamped-harness-target",
			fmt.Sprintf("refused — %s has no install stamp; it was not written by `wip install %s` and will not be removed automatically", dir, Name))
	}

	actual, err := checksumTree(dir)
	if err != nil {
		return "", err
	}
	if drift := manifest.Drift(actual, stamp); len(drift) > 0 {
		return "", wiperr.New("refusal.unstamped-harness-target",
			fmt.Sprintf("refused — %s no longer matches what `wip install %s` last wrote (%d file(s) changed since); remove it by hand if that was intentional", dir, Name, len(drift)))
	}

	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("opencode: removing %q: %w", dir, err)
	}
	return dir, nil
}

// checksumTree hashes every file under dir except the stamp itself, keyed
// by path relative to dir — the same shape manifest.Drift compares against
// a Stamp's Files map.
func checksumTree(dir string) (map[string]string, error) {
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
		if relSlash == manifest.StampFileName {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sums[relSlash] = manifest.Checksum(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("opencode: hashing installed tree %q: %w", dir, err)
	}
	return sums, nil
}
