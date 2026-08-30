// Package amp is the Amp harness generator: it renders wip's manifest into
// a SKILL.md at Amp's documented machine-global Agent Skills path,
// ~/.config/agents/skills. Amp discovers skills from several merged
// locations, but wip's projection writes exactly one tree at the preferred
// global path. There is no plugin or TypeScript shim: Amp's skill mechanism
// is SKILL.md only, loaded on demand by the native skill tool. Everything in
// the generated SKILL.md traces back to the manifest except the same two
// hand-authored blocks the other skill-only harnesses carry: this package's
// own judgment paragraph and vocabulary's ratified harness-guidance
// paragraph.
package amp

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
const Name = "amp"

// skillName is the directory name the generated skill is installed under.
const skillName = "wip"

// judgmentAsset is the one hand-authored prose paragraph this Matter owns,
// resolved through chassis's asset chain (assets/templates/skills/amp/).
const judgmentAsset = "templates/skills/amp/judgment.md"

// guidanceAsset is vocabulary's ratified 0444 harness-guidance paragraph,
// the same partial claudecode, codex, and pi project, seeded at the top
// level of the shipped asset tree.
const guidanceAsset = "agent-write-guidance.md"

// SkillsDirEnv is an environment variable that, when set, overrides
// SkillsDir()'s result — a test seam only (mirrors the other harness
// SkillsDirEnv patterns), never read for any other purpose.
const SkillsDirEnv = "WIP_AMP_SKILLS_DIR"

var userHomeDir = os.UserHomeDir

// rootDir returns Amp's own root config directory under home,
// ~/.config/amp — the directory Available keys off of. Amp's global skill
// directory is a sibling under ~/.config, not a child of this directory.
func rootDir(home string) string {
	return filepath.Join(home, ".config", "amp")
}

// SkillsDir returns the directory Amp loads machine-global skills from,
// ~/.config/agents/skills — Amp's documented --global destination.
func SkillsDir() (string, error) {
	if dir := os.Getenv(SkillsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("amp: locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", "agents", "skills"), nil
}

// Available reports whether this harness appears to be present on this
// host: its root config directory (~/.config/amp) exists, or — when
// SkillsDirEnv overrides SkillsDir — that override directory exists. The
// root, not the skills subdirectory, is the signal: a fresh harness install
// may not have created its skills directory yet. Presence on PATH is not
// consulted; the config root is what `wip install` writes into. A home-dir
// lookup failure counts as not available rather than an error — the bare
// `wip install` run treats an unavailable harness as a skip, and a probe
// should never abort that run.
func Available() bool {
	dir := os.Getenv(SkillsDirEnv)
	if dir == "" {
		home, err := userHomeDir()
		if err != nil {
			return false
		}
		dir = rootDir(home)
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
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
		return nil, fmt.Errorf("amp: resolving judgment template: %w", err)
	}
	guidance, err := asset.Resolve(guidanceAsset)
	if err != nil {
		return nil, fmt.Errorf("amp: resolving harness-guidance partial: %w", err)
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
