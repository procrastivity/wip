// Package wipdprofile resolves an explicitly supplied private root for the
// experimental wipd fixture. It never creates or opens the root.
package wipdprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrMissingProfile = errors.New("wipd profile: explicit root is required")
	ErrRelativeRoot   = errors.New("wipd profile: root must be absolute")
	ErrUnsafeRoot     = errors.New("wipd profile: root or an ancestor is unsafe")
	ErrStoreCollision = errors.New("wipd profile: root overlaps normal WIP storage")
	ErrAuthorityDB    = errors.New("wipd profile: root overlaps authority.db")
)

// Profile is a verified local path capability, not an opened fixture store.
type Profile struct {
	Root string
}

// Resolve accepts exactly one explicit absolute root. It canonicalizes through
// existing ancestors, rejects symlink components and unsafe directories, and
// refuses overlap with legacy-store locations without creating or opening
// anything. Environment variables are read only to identify paths that must
// not collide with the candidate; they never select or replace the root.
func Resolve(explicitRoot string) (Profile, error) {
	if strings.TrimSpace(explicitRoot) == "" {
		return Profile{}, ErrMissingProfile
	}
	if !filepath.IsAbs(explicitRoot) {
		return Profile{}, ErrRelativeRoot
	}
	clean := filepath.Clean(explicitRoot)
	canonical, err := canonicalWithoutSymlinks(clean)
	if err != nil {
		return Profile{}, err
	}
	if err := validateDirectoryChain(canonical); err != nil {
		return Profile{}, err
	}
	authorityCollision, err := containsAuthorityDB(canonical)
	if err != nil {
		return Profile{}, fmt.Errorf("wipd profile: inspect authority.db collision: %w", err)
	}
	if authorityCollision {
		return Profile{}, ErrAuthorityDB
	}
	legacyPaths, err := normalStorePaths()
	if err != nil {
		return Profile{}, fmt.Errorf("wipd profile: resolve normal-store collision paths: %w", err)
	}
	for _, legacy := range legacyPaths {
		canonicalLegacy, err := canonicalForComparison(legacy)
		if err != nil {
			return Profile{}, fmt.Errorf("wipd profile: resolve normal-store collision path: %w", err)
		}
		if pathsOverlap(canonical, canonicalLegacy) {
			return Profile{}, fmt.Errorf("%w: %s", ErrStoreCollision, canonicalLegacy)
		}
	}
	return Profile{Root: canonical}, nil
}

// canonicalWithoutSymlinks inspects each candidate path component in order.
// Once a missing component is encountered, the remaining suffix is treated as
// not-yet-created. EvalSymlinks is never allowed to silently turn an alias into
// acceptance.
func canonicalWithoutSymlinks(path string) (string, error) {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(path, current)
	components := strings.Split(relative, string(filepath.Separator))
	missing := false
	for index, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		if missing {
			continue
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%w: inspect %s: %v", ErrUnsafeRoot, current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink component %s", ErrUnsafeRoot, current)
		}
		if !info.IsDir() && index != len(components)-1 {
			return "", fmt.Errorf("%w: non-directory ancestor %s", ErrUnsafeRoot, current)
		}
	}
	return filepath.Clean(current), nil
}

func validateDirectoryChain(root string) error {
	for current := root; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%w: invalid ancestor %s", ErrUnsafeRoot, current)
			}
			if current == root && info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("%w: profile root is not owner-only", ErrUnsafeRoot)
			}
			// A sticky shared temporary ancestor (for example /tmp) is safe to
			// traverse; other group/world-writable ancestors are not.
			if info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf("%w: writable ancestor %s", ErrUnsafeRoot, current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect ancestor %s: %v", ErrUnsafeRoot, current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func containsAuthorityDB(root string) (bool, error) {
	for current := root; ; current = filepath.Dir(current) {
		if strings.EqualFold(filepath.Base(current), "authority.db") {
			return true, nil
		}
		_, err := os.Lstat(filepath.Join(current, "authority.db"))
		if err == nil {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false, nil
		}
	}
}

func normalStorePaths() ([]string, error) {
	paths := []string{}
	if override := os.Getenv("WIP_DB_PATH"); override != "" {
		overridePath, err := filepath.Abs(override)
		if err != nil {
			return nil, fmt.Errorf("resolve WIP_DB_PATH: %w", err)
		}
		overridePath = filepath.Clean(overridePath)
		paths = append(paths, overridePath, filepath.Dir(overridePath))
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		if err == nil {
			err = errors.New("empty hostname")
		}
		return nil, err
	}
	dataHome, err = filepath.Abs(dataHome)
	if err != nil {
		return nil, fmt.Errorf("resolve XDG_DATA_HOME: %w", err)
	}
	defaultDir := filepath.Join(dataHome, "wip", host)
	paths = append(paths, defaultDir, filepath.Join(defaultDir, "wip.db"))
	return paths, nil
}

// canonicalForComparison resolves the legacy target for collision checking;
// unlike candidate paths, legacy locations are permitted to contain symlinks.
func canonicalForComparison(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	missing := []string{}
	current := path
	for {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return pathWithin(left, right) || pathWithin(right, left)
}

func pathWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
