//go:build !linux

package wipd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStartFailsClosedOnUnsupportedPlatform(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "profile")
	if _, err := Start(root); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Start() error = %v, want ErrUnsupported", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported Start() created the profile root: %v", err)
	}
}
