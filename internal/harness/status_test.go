package harness

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/manifest"
)

func writeFile(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %q: %v", full, err)
	}
}

// TestStatus_States covers one representative tree per drift state (C4.5).
func TestStatus_States(t *testing.T) {
	generated := map[string][]byte{"a.txt": []byte("generated content")}

	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		files map[string][]byte
		want  State
	}{
		{
			name:  "missing directory absent",
			setup: func(*testing.T, string) {},
			files: generated,
			want:  Missing,
		},
		{
			name: "missing empty directory no stamp",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
			files: generated,
			want:  Missing,
		},
		{
			name: "unowned_conflict files with no stamp",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "a.txt", []byte("foreign content"))
			},
			files: generated,
			want:  UnownedConflict,
		},
		{
			name: "current disk and binary both match stamp",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "a.txt", generated["a.txt"])
				mustWriteStamp(t, dir, manifest.Stamp{
					SchemaVersion: manifest.SchemaVersion,
					Files:         manifest.ChecksumFiles(generated),
				})
			},
			files: generated,
			want:  Current,
		},
		{
			name: "stale disk matches stamp but binary would differ",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "a.txt", generated["a.txt"])
				mustWriteStamp(t, dir, manifest.Stamp{
					SchemaVersion: manifest.SchemaVersion,
					Files:         manifest.ChecksumFiles(generated),
				})
			},
			files: map[string][]byte{"a.txt": []byte("a newer verb added this")},
			want:  Stale,
		},
		{
			name: "modified disk no longer matches stamp",
			setup: func(t *testing.T, dir string) {
				writeFile(t, dir, "a.txt", []byte("edited by hand"))
				mustWriteStamp(t, dir, manifest.Stamp{
					SchemaVersion: manifest.SchemaVersion,
					Files:         manifest.ChecksumFiles(generated),
				})
			},
			files: generated,
			want:  Modified,
		},
		{
			name: "incompatible unparseable stamp",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				writeFile(t, dir, manifest.StampFileName, []byte("not json"))
			},
			files: generated,
			want:  Incompatible,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "target")
			c.setup(t, dir)

			got, err := Status(dir, c.files)
			if err != nil {
				t.Fatalf("Status: unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("Status() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestStatus_ModifiedAndStale asserts that a tree which is both modified
// (disk vs stamp) and stale (binary vs stamp) reports Modified: the order
// in which Status checks conditions puts the unsafe state first, because
// --force on a modified tree destroys the human edit (C4.5).
func TestStatus_ModifiedAndStale(t *testing.T) {
	dir := t.TempDir()
	stamped := map[string][]byte{"a.txt": []byte("installed content")}
	writeFile(t, dir, "a.txt", []byte("edited by hand"))
	mustWriteStamp(t, dir, manifest.Stamp{
		SchemaVersion: manifest.SchemaVersion,
		Files:         manifest.ChecksumFiles(stamped),
	})

	got, err := Status(dir, map[string][]byte{"a.txt": []byte("a newer verb's output")})
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if got != Modified {
		t.Errorf("Status() = %q, want %q", got, Modified)
	}
}

// TestStatus_SchemaVersionZero asserts that a stamp decoded without a
// schemaVersion field (the zero value) is Incompatible, because it differs
// from manifest.SchemaVersion just as any other mismatch would.
func TestStatus_SchemaVersionZero(t *testing.T) {
	dir := t.TempDir()
	mustWriteStamp(t, dir, manifest.Stamp{
		SchemaVersion: 0,
		Files:         map[string]string{},
	})

	got, err := Status(dir, nil)
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if got != Incompatible {
		t.Errorf("Status() = %q, want %q", got, Incompatible)
	}
}

// TestStatus_NilStampFilesMap covers a stamp that parses, carries the
// binary's schemaVersion, and has no Files map: not Incompatible on its
// own. The disk/binary comparisons then decide — an empty tree is Stale
// against a non-empty binary, and Current when files is nil (condition 4
// skipped).
func TestStatus_NilStampFilesMap(t *testing.T) {
	dir := t.TempDir()
	mustWriteStamp(t, dir, manifest.Stamp{SchemaVersion: manifest.SchemaVersion})

	got, err := Status(dir, map[string][]byte{"a.txt": []byte("content")})
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if got != Stale {
		t.Errorf("Status() with non-nil files = %q, want %q", got, Stale)
	}

	got, err = Status(dir, nil)
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if got != Current {
		t.Errorf("Status() with nil files = %q, want %q", got, Current)
	}
}

// TestStatus_NilFilesOnStaleTree asserts that files == nil skips condition
// 4 entirely, so a tree that would otherwise be Stale reads as Current —
// uninstall has no reason to build a manifest to ask this question.
func TestStatus_NilFilesOnStaleTree(t *testing.T) {
	dir := t.TempDir()
	stamped := map[string][]byte{"a.txt": []byte("installed content")}
	writeFile(t, dir, "a.txt", stamped["a.txt"])
	mustWriteStamp(t, dir, manifest.Stamp{
		SchemaVersion: manifest.SchemaVersion,
		Files:         manifest.ChecksumFiles(stamped),
	})

	got, err := Status(dir, nil)
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if got != Current {
		t.Errorf("Status() = %q, want %q", got, Current)
	}
}

// TestStatus_StampReadErrorIsNotAState asserts that a stamp read failure
// other than an unparseable JSON body (here: the stamp path is itself a
// directory) is returned as a plain error, never folded into a state —
// it is not one of C4.5's six facts about drift.
func TestStatus_StampReadErrorIsNotAState(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, manifest.StampFileName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := Status(dir, nil)
	if err == nil {
		t.Fatalf("Status: expected an error, got state %q", got)
	}
	if errors.Is(err, manifest.ErrStampUnparseable) {
		t.Errorf("Status: error unexpectedly matches ErrStampUnparseable: %v", err)
	}
}

func mustWriteStamp(t *testing.T, dir string, stamp manifest.Stamp) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := manifest.WriteStamp(dir, stamp); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
}
