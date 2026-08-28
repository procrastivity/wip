// Package devin is the Devin harness generator: it renders wip's manifest
// into a SKILL.md at Devin's own tool-owned skills path. Devin discovers
// skills from several merged locations (project-local .devin/skills/,
// ~/.config/devin/skills/, and shared ~/.agents/skills/), but wip's
// projection writes exactly one tree, targeting Devin's own
// ~/.config/devin/skills — the same choice codex, pi, and opencode each made
// for their own tool-owned directory over the shared ~/.agents/skills/ the
// arch doc leaves open. There is no plugin or TypeScript shim: Devin's skill
// mechanism is SKILL.md only, loaded on-demand via a native `skill` tool.
// Everything in the generated SKILL.md traces back to the manifest except the
// same two hand-authored blocks claudecode, codex, pi, and opencode all
// carry: this package's own judgment paragraph and vocabulary's ratified
// harness-guidance paragraph.
package devin

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
const Name = "devin"

// skillName is the directory name the generated skill is installed under.
const skillName = "wip"

// judgmentAsset is the one hand-authored prose paragraph this Matter owns,
// resolved through chassis's asset chain (assets/templates/skills/devin/).
const judgmentAsset = "templates/skills/devin/judgment.md"

// guidanceAsset is vocabulary's ratified 0444 harness-guidance paragraph,
// the same partial claudecode, codex, pi, and opencode project, seeded at the
// top level of the shipped asset tree.
const guidanceAsset = "agent-write-guidance.md"

// SkillsDirEnv is an environment variable that, when set, overrides
// SkillsDir()'s result — a test seam only (mirrors opencode's
// WIP_OPENCODE_SKILLS_DIR pattern), never read for any other purpose.
const SkillsDirEnv = "WIP_DEVIN_SKILLS_DIR"

var userHomeDir = os.UserHomeDir

// rootDir returns Devin's own root config directory under home,
// ~/.config/devin — the directory both SkillsDir and Available key off of.
func rootDir(home string) string {
	return filepath.Join(home, ".config", "devin")
}

// SkillsDir returns the directory Devin loads skills from,
// ~/.config/devin/skills — per install-target-devin's finding.
func SkillsDir() (string, error) {
	if dir := os.Getenv(SkillsDirEnv); dir != "" {
		return dir, nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("devin: locating home directory: %w", err)
	}
	return filepath.Join(rootDir(home), "skills"), nil
}

// Available reports whether this harness appears to be present on this
// host: its root config directory (~/.config/devin) exists, or — when
// SkillsDirEnv overrides SkillsDir — that override directory exists. The
// root, not the skills subdirectory, is the signal: a fresh harness install
// may not have created its skills directory yet. Presence on PATH is not
// consulted — Devin has no local binary to probe for, and in any case the
// config root is what `wip install` writes into. A home-dir lookup failure
// counts as not available rather than an error — the bare `wip install`
// run treats an unavailable harness as a skip, and a probe should never
// abort that run.
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
		return nil, fmt.Errorf("devin: resolving judgment template: %w", err)
	}
	guidance, err := asset.Resolve(guidanceAsset)
	if err != nil {
		return nil, fmt.Errorf("devin: resolving harness-guidance partial: %w", err)
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
