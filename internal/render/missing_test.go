package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/wiperr"
)

// TestReadGenerated_MissingFileRefuses is step-08's Done: a read against a
// missing expected file fails with a wip-authored refusal naming the
// missing path and directing to `wip refresh <locator>` — never a silent
// re-render, never a raw filesystem error.
func TestReadGenerated_MissingFileRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated", "matter-042.md")

	_, err := ReadGenerated(path, "matter-042")
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	werr, ok := err.(*wiperr.Error)
	if !ok {
		t.Fatalf("error %v is not a *wiperr.Error", err)
	}
	if werr.Code != "refusal.generated-missing" {
		t.Errorf("code = %q, want %q", werr.Code, "refusal.generated-missing")
	}
	if !contains(werr.Message, path) || !contains(werr.Message, "wip refresh matter-042") {
		t.Errorf("message = %q, want it to name the path and `wip refresh matter-042`", werr.Message)
	}
}

// TestReadGenerated_PresentFileReadsNormally confirms the happy path is an
// ordinary read, not a refusal.
func TestReadGenerated_PresentFileReadsNormally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matter.md")
	if err := os.WriteFile(path, []byte("hello"), 0o444); err != nil {
		t.Fatal(err)
	}
	data, err := ReadGenerated(path, "whatever")
	if err != nil {
		t.Fatalf("ReadGenerated: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want %q", data, "hello")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
