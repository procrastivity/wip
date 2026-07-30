package manifest_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/manifest"
)

func TestChecksumFiles(t *testing.T) {
	files := map[string][]byte{
		"a.txt": []byte("hello"),
		"b.txt": []byte("world"),
	}
	sums := manifest.ChecksumFiles(files)
	if len(sums) != 2 {
		t.Fatalf("ChecksumFiles returned %d entries, want 2", len(sums))
	}
	if sums["a.txt"] != manifest.Checksum([]byte("hello")) {
		t.Errorf("a.txt checksum = %q, want the sha256 of its own content", sums["a.txt"])
	}
	if sums["a.txt"] == sums["b.txt"] {
		t.Error("a.txt and b.txt have different content but the same checksum")
	}
}

func TestWriteReadStamp_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := manifest.Stamp{
		ToolVersion:   "1.0.0",
		SchemaVersion: 1,
		Files:         map[string]string{"SKILL.md": "deadbeef"},
	}
	if err := manifest.WriteStamp(dir, want); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}

	got, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if !ok {
		t.Fatal("ReadStamp: ok = false, want true after WriteStamp")
	}
	if got.ToolVersion != want.ToolVersion || got.SchemaVersion != want.SchemaVersion {
		t.Errorf("ReadStamp = %+v, want %+v", got, want)
	}
	if got.Files["SKILL.md"] != "deadbeef" {
		t.Errorf("Files[SKILL.md] = %q, want %q", got.Files["SKILL.md"], "deadbeef")
	}
}

func TestReadStamp_AbsentReportsNotOK(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if ok {
		t.Fatal("ReadStamp: ok = true for a directory with no stamp file")
	}
}
