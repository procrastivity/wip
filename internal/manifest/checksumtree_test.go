package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/manifest"
)

func TestChecksumTree(t *testing.T) {
	dir := t.TempDir()

	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	topContent := []byte("top-level content")
	nestedContent := []byte("nested content")
	if err := os.WriteFile(filepath.Join(dir, "top.txt"), topContent, 0o644); err != nil {
		t.Fatalf("WriteFile top.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "leaf.txt"), nestedContent, 0o644); err != nil {
		t.Fatalf("WriteFile nested/leaf.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.StampFileName), []byte(`{"toolVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("WriteFile stamp: %v", err)
	}

	sums, err := manifest.ChecksumTree(dir)
	if err != nil {
		t.Fatalf("ChecksumTree: %v", err)
	}

	if _, ok := sums[manifest.StampFileName]; ok {
		t.Errorf("ChecksumTree included the stamp file %q, want it excluded", manifest.StampFileName)
	}

	if len(sums) != 2 {
		t.Fatalf("ChecksumTree returned %d entries, want 2 (got %v)", len(sums), sums)
	}

	if got, want := sums["top.txt"], manifest.Checksum(topContent); got != want {
		t.Errorf("sums[top.txt] = %q, want %q", got, want)
	}
	if got, want := sums["nested/leaf.txt"], manifest.Checksum(nestedContent); got != want {
		t.Errorf("sums[nested/leaf.txt] = %q, want %q", got, want)
	}
	for path := range sums {
		if filepath.ToSlash(path) != path {
			t.Errorf("key %q is not slash-separated", path)
		}
	}
}

func TestChecksumTree_MissingDirReturnsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := manifest.ChecksumTree(dir); err == nil {
		t.Fatal("ChecksumTree: err = nil, want an error for a missing directory")
	}
}
