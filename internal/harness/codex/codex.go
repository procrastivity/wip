// Package codex is the Codex CLI harness generator: it renders wip's
// manifest into a SKILL.md at Codex's filesystem skill path — the arch
// doc §4 mechanism ("Codex reads SKILL.md from its filesystem skill path;
// it does not consume skill:// over MCP"). Unlike claude-code, there is no
// plugin.json: that file is claude-code's own "skills-dir as plugin"
// mechanism (H9), not something Codex reads. Everything in the generated
// SKILL.md traces back to the manifest except the same two hand-authored
// blocks claudecode carries: this package's own judgment paragraph and
// vocabulary's ratified harness-guidance paragraph.
package codex

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
const Name = "codex"

// skillName is the directory name the generated skill is installed under.
const skillName = "wip"

// judgmentAsset is the one hand-authored prose paragraph this Matter owns,
// resolved through chassis's asset chain (assets/templates/skills/codex/).
const judgmentAsset = "templates/skills/codex/judgment.md"

// guidanceAsset is vocabulary's ratified 0444 harness-guidance paragraph,
// the same partial claudecode projects, seeded at the top level of the
// shipped asset tree.
const guidanceAsset = "agent-write-guidance.md"

// SkillsDirEnv is an environment variable that, when set, overrides
// SkillsDir()'s result — a test seam only (mirrors claudecode's
// WIP_CLAUDE_SKILLS_DIR pattern), never read for any other purpose.
const SkillsDirEnv = "WIP_CODEX_SKILLS_DIR"

var userHomeDir = os.UserHomeDir

// SkillsDir returns the directory Codex loads skills from, ~/.codex/skills
// — a tool-owned directory mirroring claude-code's own ~/.claude/skills,
// per install-target-codex/step-01's finding (not the shared
// ~/.agents/skills/ the arch doc leaves open).
func SkillsDir() (string, error) {
	if dir := os.Getenv(SkillsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("codex: locating home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "skills"), nil
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
		return nil, fmt.Errorf("codex: resolving judgment template: %w", err)
	}
	guidance, err := asset.Resolve(guidanceAsset)
	if err != nil {
		return nil, fmt.Errorf("codex: resolving harness-guidance partial: %w", err)
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
