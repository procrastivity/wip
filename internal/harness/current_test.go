package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
)

func TestIsCurrent_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := harness.IsCurrent(dir, map[string][]byte{"SKILL.md": []byte("generated content")})
	if err != nil {
		t.Fatalf("IsCurrent(missing dir) error = %v, want nil", err)
	}
	if got {
		t.Fatal("IsCurrent(missing dir) = true, want false")
	}
}

func TestIsCurrent_StampedTreeMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{"SKILL.md": []byte("generated content")}
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	got, err := harness.IsCurrent(dir, files)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if !got {
		t.Fatal("IsCurrent(stamped tree matching files) = false, want true")
	}
}

func TestIsCurrent_BinaryWouldGenerateDifferentContent(t *testing.T) {
	dir := t.TempDir()
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	// The current binary would generate different content than what was
	// stamped (e.g. a verb added since install) — the disk still matches
	// the stamp, but the binary has drifted from it.
	files := map[string][]byte{"SKILL.md": []byte("new generated content")}

	got, err := harness.IsCurrent(dir, files)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if got {
		t.Fatal("IsCurrent(binary drift) = true, want false")
	}
}

func TestIsCurrent_DiskEditedSinceStamp(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{"SKILL.md": []byte("generated content")}
	writeStamped(t, dir, map[string]string{"SKILL.md": "generated content"})

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := harness.IsCurrent(dir, files)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if got {
		t.Fatal("IsCurrent(disk edited since stamp) = true, want false")
	}
}

func TestIsCurrent_NoStamp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("generated content"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := harness.IsCurrent(dir, map[string][]byte{"SKILL.md": []byte("generated content")})
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if got {
		t.Fatal("IsCurrent(no stamp) = true, want false")
	}
}

func TestIsCurrent_DifferentToolVersionSameChecksums(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{"SKILL.md": []byte("generated content")}

	raw := map[string][]byte{"SKILL.md": []byte("generated content")}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), raw["SKILL.md"], 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := manifest.Stamp{
		ToolVersion:   "0.9.0",
		SchemaVersion: manifest.SchemaVersion,
		Files:         manifest.ChecksumFiles(raw),
	}
	if err := manifest.WriteStamp(dir, stamp); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}

	got, err := harness.IsCurrent(dir, files)
	if err != nil {
		t.Fatalf("IsCurrent: %v", err)
	}
	if !got {
		t.Fatal("IsCurrent(different ToolVersion, same checksums) = false, want true")
	}
}
