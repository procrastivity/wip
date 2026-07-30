// Package claudecode is the P1-only harness generator (H9): it renders
// wip's manifest into a Claude Code skill directory plus a
// .claude-plugin/plugin.json in the same directory, the "skills-dir as
// plugin" mechanism arch doc §4 describes — no marketplace manifest, no
// separate registry entry. Everything in the generated tree traces back to
// the manifest except two verbatim, hand-authored strings: the "when to
// reach for wip" judgment paragraph (this package's own template asset) and
// vocabulary's ratified 0444 harness-guidance paragraph.
package claudecode

import (
	"encoding/json"
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
const Name = "claude-code"

// skillName is the directory name the generated skill is installed under.
const skillName = "wip"

// judgmentAsset is the one hand-authored prose paragraph this Matter owns,
// resolved through chassis's asset chain (assets/templates/skills/claude-code/,
// per the Brief's "where per-harness prose judgment lives").
const judgmentAsset = "templates/skills/claude-code/judgment.md"

// guidanceAsset is vocabulary's ratified 0444 harness-guidance paragraph
// (vocabulary step-08/step-13), seeded at the top level of the shipped
// asset tree and projected here verbatim — the only other hand-written
// string in the generated tree.
const guidanceAsset = "agent-write-guidance.md"

// SkillsDirEnv is an environment variable that, when set, overrides
// SkillsDir()'s result — a test seam only (mirrors internal/asset's
// os.Executable indirection pattern), never read for any other purpose.
const SkillsDirEnv = "WIP_CLAUDE_SKILLS_DIR"

var userHomeDir = os.UserHomeDir

// SkillsDir returns the directory Claude Code loads skills from,
// ~/.claude/skills, per arch doc §4.
func SkillsDir() (string, error) {
	if dir := os.Getenv(SkillsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("claudecode: locating home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
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
		return nil, fmt.Errorf("claudecode: resolving judgment template: %w", err)
	}
	guidance, err := asset.Resolve(guidanceAsset)
	if err != nil {
		return nil, fmt.Errorf("claudecode: resolving harness-guidance partial: %w", err)
	}

	pluginJSON, err := renderPluginJSON(m)
	if err != nil {
		return nil, fmt.Errorf("claudecode: rendering plugin.json: %w", err)
	}

	return map[string][]byte{
		"SKILL.md":                   renderSkillMD(m, verbs, judgment.Bytes(), guidance.Bytes()),
		".claude-plugin/plugin.json": pluginJSON,
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

// pluginManifest is the minimal .claude-plugin/plugin.json shape (arch doc
// §4's skills-dir-as-plugin mechanism). Hooks/agents/MCP sections are left
// absent — no P1 need is designed for them; the mechanism costs nothing to
// leave empty, per the Brief.
type pluginManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

func renderPluginJSON(m manifest.Manifest) ([]byte, error) {
	p := pluginManifest{
		Name:        skillName,
		Description: fmt.Sprintf("%s's own generated plumbing-verb skill.", m.Tool.Name),
		Version:     m.Tool.Version,
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
