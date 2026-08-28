package claudecode_test

import (
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/harness/claudecode"
)

// The generated SKILL.md must carry the unknown-clone rule (workplan D4):
// stop, report, offer `wip init` as a write, and never read wip's source
// or another repo's .wip/ to answer instead.
func TestGenerate_SkillCarriesUnknownCloneRule(t *testing.T) {
	files, err := claudecode.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	skill := string(files["SKILL.md"])
	for _, want := range []string{
		`refuses with "this clone is unknown to wip"`,
		"is a write, so when you are in a read-only mode, ask",
		"reading wip's source, its database, or another repo's `.wip/`",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md missing unknown-clone rule text %q", want)
		}
	}
}
