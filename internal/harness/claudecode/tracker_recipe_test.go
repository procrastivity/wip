package claudecode_test

import (
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/harness/claudecode"
)

// The generated SKILL.md must let an agent configure a tracker without
// reading wip's source: the recipe heading, every registered backend
// name, the Linear-only target rule, and the credential sources.
func TestGenerate_SkillCarriesTrackerRecipe(t *testing.T) {
	files, err := claudecode.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	skill := string(files["SKILL.md"])
	for _, want := range []string{
		"Configuring a tracker takes five commands",
		"`wip outbox backend <name>`",
		"`github`, `gitlab` and `linear`",
		"is for Linear only; GitHub and GitLab",
		"`WIP_GITLAB_TOKEN`, `GITLAB_TOKEN`, then",
		"`WIP_GITHUB_TOKEN`, `GH_TOKEN`,",
		"`WIP_LINEAR_TOKEN`, `LINEAR_API_KEY`",
		"`wip outbox approve` and\n`wip outbox flush`",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("SKILL.md missing tracker-recipe text %q", want)
		}
	}
}
