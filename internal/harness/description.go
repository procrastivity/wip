package harness

import (
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/asset"
	"github.com/procrastivity/wip/internal/wiperr"
)

// skillDescriptionAsset is the SKILL.md frontmatter description: one
// sentence naming the situations this skill triggers on, not the verbs it
// exposes (those are generated, in each harness's table). Judgment prose
// by function, resolved through the asset chain like each harness's
// judgment.md (C4.4) rather than kept inline as a Go format string — it is
// the most tunable prose in the projection, since it alone decides whether
// an agent loads the skill at all (C5.1). One shared asset serves all six
// harness targets; the sentence does not vary by harness.
const skillDescriptionAsset = "templates/skills/description.txt"

// SkillDescription resolves skillDescriptionAsset and returns the value
// of SKILL.md's `description:` frontmatter key. That key holds one line,
// so an empty or multi-line override is an error rather than broken
// frontmatter. Rejected: joining the lines with spaces, which would
// install a trigger sentence the override's author never wrote. Only
// trailing whitespace, such as the newline a text file ends with, is
// trimmed.
//
// A malformed value is validation.skill-description (exit 1, C2.5): the
// shipped asset is one line, so only a user override can break it.
func SkillDescription() (string, error) {
	resolved, err := asset.Resolve(skillDescriptionAsset)
	if err != nil {
		return "", fmt.Errorf("harness: resolving skill description: %w", err)
	}
	desc := strings.TrimRight(string(resolved.Bytes()), "\n\r\t ")
	if desc == "" || strings.ContainsAny(desc, "\n\r") {
		return "", wiperr.New("validation.skill-description",
			fmt.Sprintf("skill description asset %q must be exactly one non-empty line; fix or remove the override under the wip config directory", skillDescriptionAsset))
	}
	return desc, nil
}
