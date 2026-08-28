package harness_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/wiperr"
)

// writeStamped writes files (relative path -> content) under dir and
// stamps dir with their checksums, the shape a real harness Install leaves
// behind.
func writeStamped(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	raw := make(map[string][]byte, len(files))
	for path, content := range files {
		raw[path] = []byte(content)
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", full, err)
		}
	}
	stamp := manifest.Stamp{
		ToolVersion:   "1.0.0",
		SchemaVersion: manifest.SchemaVersion,
		Files:         manifest.ChecksumFiles(raw),
	}
	if err := manifest.WriteStamp(dir, stamp); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
}

func TestRefuseHandEdited_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if err := harness.RefuseHandEdited("some-harness", dir); err != nil {
		t.Fatalf("RefuseHandEdited(missing dir) = %v, want nil", err)
	}
}

func TestRefuseHandEdited_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := harness.RefuseHandEdited("some-harness", dir); err != nil {
		t.Fatalf("RefuseHandEdited(empty dir) = %v, want nil", err)
	}
}

func TestRefuseHandEdited_MatchesStamp(t *testing.T) {
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	if err := harness.RefuseHandEdited("some-harness", dir); err != nil {
		t.Fatalf("RefuseHandEdited(matches stamp) = %v, want nil", err)
	}
}

func asRefusal(t *testing.T, err error) *wiperr.Error {
	t.Helper()
	if err == nil {
		t.Fatal("RefuseHandEdited: want a refusal error, got nil")
	}
	var got *wiperr.Error
	if !errors.As(err, &got) {
		t.Fatalf("error = %T %v, want *wiperr.Error", err, err)
	}
	if got.Code != "refusal.unstamped-harness-target" {
		t.Fatalf("error code = %q, want %q", got.Code, "refusal.unstamped-harness-target")
	}
	return got
}

func TestRefuseHandEdited_UnstampedForeignContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-written, not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	asRefusal(t, harness.RefuseHandEdited("some-harness", dir))
}

func TestRefuseHandEdited_ChangedFile(t *testing.T) {
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatal(err)
	}

	asRefusal(t, harness.RefuseHandEdited("some-harness", dir))
}

func TestRefuseHandEdited_AddedFile(t *testing.T) {
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	if err := os.WriteFile(filepath.Join(dir, "extra.md"), []byte("added by hand"), 0o644); err != nil {
		t.Fatal(err)
	}

	asRefusal(t, harness.RefuseHandEdited("some-harness", dir))
}

func TestRefuseHandEdited_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{
		"SKILL.md":                   "generated content",
		".claude-plugin/plugin.json": `{"name":"wip"}`,
	})

	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	asRefusal(t, harness.RefuseHandEdited("some-harness", dir))
}

func TestRefuseHandEdited_OnlyStampFileRemains(t *testing.T) {
	// Someone deleted every generated file by hand but left the stamp
	// behind: ChecksumTree excludes the stamp itself, so Drift reports
	// every stamped file as "removed" and install must refuse.
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	asRefusal(t, harness.RefuseHandEdited("some-harness", dir))
}
