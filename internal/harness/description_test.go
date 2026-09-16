package harness_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/wiperr"
)

func writeDescriptionOverride(t *testing.T, content string) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	dir := filepath.Join(configHome, "wip", "templates", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "description.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillDescription_ShippedAssetIsOneLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	desc, err := harness.SkillDescription()
	if err != nil {
		t.Fatalf("SkillDescription: %v", err)
	}
	if desc == "" {
		t.Fatal("SkillDescription = empty, want the shipped one-line sentence")
	}
}

func TestSkillDescription_OverrideReplacesShippedSentence(t *testing.T) {
	writeDescriptionOverride(t, "A custom trigger sentence.\n")

	desc, err := harness.SkillDescription()
	if err != nil {
		t.Fatalf("SkillDescription: %v", err)
	}
	if desc != "A custom trigger sentence." {
		t.Errorf("SkillDescription = %q, want the override, trailing newline trimmed", desc)
	}
}

// The frontmatter key holds one line, so a malformed override is refused
// with its own code rather than installed as broken frontmatter.
func TestSkillDescription_RefusesMalformedOverride(t *testing.T) {
	for name, content := range map[string]string{
		"empty":      "\n",
		"multi-line": "line one\nline two\n",
	} {
		t.Run(name, func(t *testing.T) {
			writeDescriptionOverride(t, content)

			_, err := harness.SkillDescription()
			var werr *wiperr.Error
			if !errors.As(err, &werr) {
				t.Fatalf("SkillDescription error = %T %v, want *wiperr.Error", err, err)
			}
			if werr.Code != "validation.skill-description" {
				t.Errorf("code = %q, want %q", werr.Code, "validation.skill-description")
			}
		})
	}
}
