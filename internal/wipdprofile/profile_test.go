package wipdprofile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("make test temp directory private: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat private test temp directory: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("test temp directory mode = %04o, want owner-only", info.Mode().Perm())
	}
	return dir
}

func TestResolveProfilePaths(t *testing.T) {
	base := privateTempDir(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg"))
	t.Setenv("WIP_DB_PATH", "")

	tests := []struct {
		name    string
		prepare func(*testing.T) string
		wantErr error
	}{
		{name: "missing profile", prepare: func(*testing.T) string { return "" }, wantErr: ErrMissingProfile},
		{name: "relative root", prepare: func(*testing.T) string { return "relative/root" }, wantErr: ErrRelativeRoot},
		{name: "symlink root", prepare: func(t *testing.T) string {
			target := filepath.Join(privateTempDir(t), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(privateTempDir(t), "root-link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}, wantErr: ErrUnsafeRoot},
		{name: "symlink ancestor", prepare: func(t *testing.T) string {
			target := filepath.Join(privateTempDir(t), "target")
			child := filepath.Join(target, "existing-child")
			if err := os.MkdirAll(child, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(privateTempDir(t), "ancestor-link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "existing-child")
		}, wantErr: ErrUnsafeRoot},
		{name: "unsafe writable ancestor", prepare: func(t *testing.T) string {
			ancestor := filepath.Join(privateTempDir(t), "unsafe")
			if err := os.Mkdir(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(ancestor, 0o777); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(ancestor, "profile")
		}, wantErr: ErrUnsafeRoot},
		{name: "non-private profile root", prepare: func(t *testing.T) string {
			root := filepath.Join(privateTempDir(t), "public-profile")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			return root
		}, wantErr: ErrUnsafeRoot},
		{name: "alias and canonical overlap", prepare: func(t *testing.T) string {
			target := filepath.Join(privateTempDir(t), "canonical")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(privateTempDir(t), "alias")
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		}, wantErr: ErrUnsafeRoot},
		{name: "normal default path", prepare: func(t *testing.T) string {
			defaultDir, err := store.DataDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(defaultDir, 0o700); err != nil {
				t.Fatal(err)
			}
			return defaultDir
		}, wantErr: ErrStoreCollision},
		{name: "WIP_DB_PATH override", prepare: func(t *testing.T) string {
			dir := filepath.Join(privateTempDir(t), "override")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			dbPath := filepath.Join(dir, "legacy.db")
			if err := os.WriteFile(dbPath, []byte("legacy sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WIP_DB_PATH", dbPath)
			return dir
		}, wantErr: ErrStoreCollision},
		{name: "XDG_DATA_HOME override", prepare: func(t *testing.T) string {
			dir := filepath.Join(privateTempDir(t), "xdg-override", "wip")
			host, err := os.Hostname()
			if err != nil {
				t.Fatal(err)
			}
			dir = filepath.Join(dir, host)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// Set the exact XDG root corresponding to <root>/wip/<host>.
			t.Setenv("XDG_DATA_HOME", filepath.Dir(filepath.Dir(dir)))
			return dir
		}, wantErr: ErrStoreCollision},
		{name: "authority.db overlap", prepare: func(t *testing.T) string {
			dir := filepath.Join(privateTempDir(t), "fixture")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "authority.db"), []byte("authority sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}, wantErr: ErrAuthorityDB},
		{name: "safe explicit root", prepare: func(t *testing.T) string {
			return filepath.Join(privateTempDir(t), "explicit-profile")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg"))
			t.Setenv("WIP_DB_PATH", "")
			root := test.prepare(t)
			got, err := Resolve(root)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Resolve(%q) error = %v, want %v", root, err, test.wantErr)
				}
				if got.Root != "" {
					t.Fatalf("refused profile returned root %q", got.Root)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", root, err)
			}
			want := filepath.Clean(root)
			if got.Root != want {
				t.Fatalf("resolved root = %q, want canonical %q", got.Root, want)
			}
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Resolve created safe candidate root: %v", err)
			}
		})
	}
}

func TestResolveRejectsSymlinkCanceledByDotDot(t *testing.T) {
	base := privateTempDir(t)
	linkTarget := privateTempDir(t)
	link := filepath.Join(base, "link")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(base, "profile")
	input := base + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + "profile"
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg"))
	t.Setenv("WIP_DB_PATH", "")
	if _, err := Resolve(input); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("Resolve(%q) error = %v, want unsafe path refusal", input, err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dotdot-canceled candidate was created or cannot be inspected: %v", err)
	}
}

func TestResolveRejectsUntrustedOwnerOfStickyWritableAncestor(t *testing.T) {
	base := privateTempDir(t)
	ancestor := filepath.Join(base, "untrusted-sticky")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ancestor)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Fatalf("sticky ancestor mode = %v, want sticky 0777", info.Mode())
	}
	currentOwnerLookup := fileOwnerUID
	ownerLookupReached := false
	fileOwnerUID = func(info os.FileInfo) (uint64, bool) {
		if info.Name() == "untrusted-sticky" {
			ownerLookupReached = true
			return 1<<32 - 1, true
		}
		return currentOwnerLookup(info)
	}
	t.Cleanup(func() { fileOwnerUID = currentOwnerLookup })
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg"))
	t.Setenv("WIP_DB_PATH", "")
	if _, err := Resolve(filepath.Join(ancestor, "profile")); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("Resolve() error = %v, want untrusted sticky-ancestor refusal", err)
	}
	if !ownerLookupReached {
		t.Fatal("untrusted sticky-ancestor owner lookup was not reached")
	}
}

func TestResolveRejectsStickyAncestorWhenOwnerUIDUnavailable(t *testing.T) {
	base := privateTempDir(t)
	ancestor := filepath.Join(base, "unknown-owner-sticky")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ancestor)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Fatalf("sticky ancestor mode = %v, want sticky 0777", info.Mode())
	}
	currentOwnerLookup := fileOwnerUID
	ownerLookupReached := false
	fileOwnerUID = func(info os.FileInfo) (uint64, bool) {
		if info.Name() == "unknown-owner-sticky" {
			ownerLookupReached = true
			return 0, false
		}
		return currentOwnerLookup(info)
	}
	t.Cleanup(func() { fileOwnerUID = currentOwnerLookup })
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg"))
	t.Setenv("WIP_DB_PATH", "")
	if _, err := Resolve(filepath.Join(ancestor, "profile")); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("Resolve() error = %v, want unavailable-owner refusal", err)
	}
	if !ownerLookupReached {
		t.Fatal("unavailable sticky-ancestor owner lookup was not reached")
	}
}

func TestResolveRefusesBeforeCreationAndLeavesLegacySentinelUnchanged(t *testing.T) {
	base := privateTempDir(t)
	legacyDir := filepath.Join(base, "legacy")
	if err := os.Mkdir(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("legacy store sentinel bytes")
	dbPath := filepath.Join(legacyDir, "wip.db")
	if err := os.WriteFile(dbPath, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIP_DB_PATH", dbPath)
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "other-xdg"))
	candidate := filepath.Join(legacyDir, "new-profile")
	if _, err := Resolve(candidate); !errors.Is(err, ErrStoreCollision) {
		t.Fatalf("Resolve(%q) error = %v, want normal-store collision", candidate, err)
	}
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused candidate exists or cannot be inspected: %v", err)
	}
	got, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("legacy sentinel changed: got %q, want %q", got, sentinel)
	}
}
