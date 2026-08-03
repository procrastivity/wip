// Package pi is the Pi harness generator: it renders wip's manifest into a
// SKILL.md at Pi's native Agent Skills path — the arch doc §4 mechanism
// ("Pi supports Agent Skills natively, so for the read path this needs zero
// Pi-specific code — the skill is the wrapper, and the agent's bash tool is
// the transport"). There is no TS shim (install-target-pi/step-01's
// finding): Pi's extension layer exists only for lifecycle hooks or MCP
// bridges, and wip has no lifecycle hook to carry today. Everything in the
// generated SKILL.md traces back to the manifest except the same two
// hand-authored blocks claudecode and codex carry: this package's own
// judgment paragraph and vocabulary's ratified harness-guidance paragraph.
package pi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/procrastivity/wip/internal/asset"
	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
)

// Name is this harness's install-target name, as passed to `wip install
// <harness>` / `wip uninstall <harness>`. It is also the generated skill's
// own directory name under SkillsDir().
const Name = "pi"

// skillName is the directory name the generated skill is installed under.
const skillName = "wip"

// judgmentAsset is the one hand-authored prose paragraph this Matter owns,
// resolved through chassis's asset chain (assets/templates/skills/pi/).
const judgmentAsset = "templates/skills/pi/judgment.md"

// guidanceAsset is vocabulary's ratified 0444 harness-guidance paragraph,
// the same partial claudecode and codex project, seeded at the top level of
// the shipped asset tree.
const guidanceAsset = "agent-write-guidance.md"

// SkillsDirEnv is an environment variable that, when set, overrides
// SkillsDir()'s result — a test seam only (mirrors codex's
// WIP_CODEX_SKILLS_DIR pattern), never read for any other purpose.
const SkillsDirEnv = "WIP_PI_SKILLS_DIR"

var userHomeDir = os.UserHomeDir

// SkillsDir returns the directory Pi loads Agent Skills from,
// ~/.pi/agent/skills, per arch doc §4 and install-target-pi/step-01's
// finding.
func SkillsDir() (string, error) {
	if dir := os.Getenv(SkillsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("pi: locating home directory: %w", err)
	}
	return filepath.Join(home, ".pi", "agent", "skills"), nil
}

// InstallDir returns the directory the generated skill is written to and
// read back from: SkillsDir()/wip.
func InstallDir() (string, error) {
	dir, err := SkillsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, skillName), nil
}

// Generate renders m into the generated tree's content, keyed by path
// relative to InstallDir(). It filters m's verbs to the plumbing subset
// (harness.Projectable, D53) before rendering — the manifest's own full
// verb list is never projected as-is.
func Generate(m manifest.Manifest) (map[string][]byte, error) {
	verbs := harness.Projectable(m.Verbs)

	judgment, err := asset.Resolve(judgmentAsset)
	if err != nil {
		return nil, fmt.Errorf("pi: resolving judgment template: %w", err)
	}
	guidance, err := asset.Resolve(guidanceAsset)
	if err != nil {
		return nil, fmt.Errorf("pi: resolving harness-guidance partial: %w", err)
	}

	return map[string][]byte{
		"SKILL.md": renderSkillMD(m, verbs, judgment.Bytes(), guidance.Bytes()),
	}, nil
}

func renderSkillMD(m manifest.Manifest, verbs []manifest.Verb, judgment, guidance []byte) []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "name: %s\n", skillName)
	fmt.Fprintf(&b, "description: Track and drive %s work — Matters, Stages, Steps — through its verb surface.\n", m.Tool.Name)
	fmt.Fprintf(&b, "---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", m.Tool.Name)
	fmt.Fprintf(&b, "Generated from %s %s (schema %d) — never hand-edit; re-run `%s install %s` after upgrading.\n\n",
		m.Tool.Name, m.Tool.Version, m.SchemaVersion, m.Tool.Name, Name)

	b.Write(judgment)
	fmt.Fprintf(&b, "\n\n")

	fmt.Fprintf(&b, "## Verbs\n\n")
	if len(verbs) == 0 {
		fmt.Fprintf(&b, "(none registered yet)\n\n")
	} else {
		fmt.Fprintf(&b, "| Verb | Description |\n|---|---|\n")
		for _, v := range verbs {
			desc := v.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "| `%s %s` | %s |\n", m.Tool.Name, v.Name, desc)
		}
		fmt.Fprintf(&b, "\n")
	}

	b.Write(guidance)
	fmt.Fprintf(&b, "\n")

	return []byte(b.String())
}
