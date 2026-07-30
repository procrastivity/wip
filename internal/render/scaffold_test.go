package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureLayout_CreatesBothHalvesAndExcludes is step-01's Done: on first
// render, both `.wip/generated/` and `.wip/work/` exist, and `.wip/` is
// excluded per-clone via `.git/info/exclude` — never a tracked `.gitignore`
// (D41 verbatim).
func TestEnsureLayout_CreatesBothHalvesAndExcludes(t *testing.T) {
	_, dir, cur := setup(t)

	if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	for _, want := range []string{GeneratedDir(cur.Root), WorkDir(cur.Root)} {
		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("stat %s: %v", want, err)
		}
		if !info.IsDir() {
			t.Errorf("%s exists but is not a directory", want)
		}
	}

	excludePath := filepath.Join(cur.Clone.GitCommonDir, "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read %s: %v", excludePath, err)
	}
	if !containsLine(string(data), ".wip/") {
		t.Errorf(".git/info/exclude = %q, want a %q line", data, ".wip/")
	}

	if gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
		t.Errorf(".gitignore was created (%q) — D41 says .wip/ is never excluded via a tracked .gitignore", gitignore)
	}
}

// TestEnsureLayout_IdempotentAndNeverDuplicates re-runs step-01's Done
// clause literally: a repeat call never duplicates the exclude line or
// recreates existing directories destructively.
func TestEnsureLayout_IdempotentAndNeverDuplicates(t *testing.T) {
	_, _, cur := setup(t)

	for i := 0; i < 3; i++ {
		if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
			t.Fatalf("EnsureLayout call %d: %v", i, err)
		}
	}

	excludePath := filepath.Join(cur.Clone.GitCommonDir, "info", "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read %s: %v", excludePath, err)
	}
	if n := strings.Count(string(data), ".wip/"); n != 1 {
		t.Errorf(".git/info/exclude contains %q %d times after 3 calls, want exactly 1", ".wip/", n)
	}
}

// TestEnsureLayout_PreservesExistingExcludeContent confirms the append never
// clobbers whatever was already in the file, and handles a file with no
// trailing newline.
func TestEnsureLayout_PreservesExistingExcludeContent(t *testing.T) {
	_, _, cur := setup(t)

	infoDir := filepath.Join(cur.Clone.GitCommonDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(infoDir, "exclude")
	if err := os.WriteFile(excludePath, []byte("*.log"), 0o644); err != nil { // no trailing newline
		t.Fatal(err)
	}

	if err := EnsureLayout(cur.Root, cur.Clone.GitCommonDir); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "*.log") {
		t.Errorf("existing content lost: %q", got)
	}
	if !containsLine(got, ".wip/") {
		t.Errorf("exclude line missing: %q", got)
	}
	if strings.Contains(got, "*.log.wip/") || strings.Contains(got, "*.log\n.wip/") == false {
		t.Errorf("lines fused without a newline between them: %q", got)
	}
}

func containsLine(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
