package codex_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/asset"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
)

func testManifest() manifest.Manifest {
	return manifest.Manifest{
		Tool:          manifest.Tool{Name: "wip", Version: "1.2.3", Commit: "abc", Date: "2026-07-29"},
		SchemaVersion: manifest.SchemaVersion,
		Verbs: []manifest.Verb{
			{Name: "status", Kind: surface.Plumbing, Description: "show status"},
			{Name: "ask", Kind: surface.LLM, Description: "an llm-shaped verb"},
			{Name: "watch", Kind: surface.ControlPlane, Description: "an mcp-only verb"},
		},
	}
}

func TestGenerate_ProducesSkillMD(t *testing.T) {
	files, err := codex.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		t.Error("Generate: missing SKILL.md")
	}
	if _, ok := files[".claude-plugin/plugin.json"]; ok {
		t.Error("Generate: codex has no plugin.json — that mechanism is claude-code's own")
	}
}

func TestGenerate_ProjectsOnlyPlumbingVerbs(t *testing.T) {
	files, err := codex.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	skill := string(files["SKILL.md"])
	if !strings.Contains(skill, "status") {
		t.Error("SKILL.md missing the plumbing verb 'status'")
	}
	if strings.Contains(skill, "an llm-shaped verb") || strings.Contains(skill, "`wip ask`") {
		t.Error("SKILL.md includes the llm-kind verb 'ask', want it excluded (D53's harness filter)")
	}
	if strings.Contains(skill, "an mcp-only verb") || strings.Contains(skill, "`wip watch`") {
		t.Error("SKILL.md includes the control-plane-kind verb 'watch', want it excluded (D53's harness filter)")
	}
}

func TestGenerate_ProjectsHandAuthoredTextVerbatim(t *testing.T) {
	files, err := codex.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	skill := string(files["SKILL.md"])

	judgment, err := asset.Resolve("templates/skills/codex/judgment.md")
	if err != nil {
		t.Fatalf("resolving judgment asset: %v", err)
	}
	if !strings.Contains(skill, string(judgment.Bytes())) {
		t.Error("SKILL.md does not contain the judgment template verbatim")
	}

	guidance, err := asset.Resolve("agent-write-guidance.md")
	if err != nil {
		t.Fatalf("resolving harness-guidance asset: %v", err)
	}
	if !strings.Contains(skill, string(guidance.Bytes())) {
		t.Error("SKILL.md does not contain vocabulary's harness-guidance partial verbatim")
	}
}

// TestGenerate_ProjectsReadPathGuidanceParagraphVerbatim is the
// navigation contract's test 5.1: the guidance asset carries §7's
// canonical read-path paragraph verbatim. The shipped asset reflows the
// paragraph onto a single line (the contract's own cosmetic note — the
// frozen wording is unchanged, only its line breaks), so the comparison
// normalizes whitespace rather than requiring exact newlines.
func TestGenerate_ProjectsReadPathGuidanceParagraphVerbatim(t *testing.T) {
	guidance, err := asset.Resolve("agent-write-guidance.md")
	if err != nil {
		t.Fatalf("resolving harness-guidance asset: %v", err)
	}
	if !strings.Contains(normalizeWhitespace(string(guidance.Bytes())), normalizeWhitespace(wantReadPathGuidanceParagraph)) {
		t.Errorf("agent-write-guidance.md does not contain the §7 read-path paragraph verbatim (normalized whitespace):\n%s", guidance.Bytes())
	}
}

// wantReadPathGuidanceParagraph is the contract's §7 frozen wording,
// compared on normalized whitespace (see
// TestGenerate_ProjectsReadPathGuidanceParagraphVerbatim).
const wantReadPathGuidanceParagraph = "To read a Matter's own record, follow the read path `next` teaches: run " +
	"`wip plumbing next --json` and take the target's (or your chosen candidate's) " +
	"`matter` and `generatedDir` fields; run `wip plumbing refresh <matter>` (the " +
	"locator form also renders a sealed Matter); then read exactly the files " +
	"that refresh reports — `generatedFiles` in JSON, the indented list in " +
	"human output. When `next` reports no node at all, take the Matter locator " +
	"from `wip plumbing status --all` instead and refresh it the same way — " +
	"plain status hides sealed Matters older than two weeks. A printed Step " +
	"address is not an accepted locator; the `matter` field is. Never open " +
	"wip's database or event log to answer a content question, and never " +
	"read another repo's `.wip/`: the files refresh reports are the whole " +
	"sanctioned read surface for Matter content."

// normalizeWhitespace collapses any run of whitespace (including
// newlines from a hard-wrapped source paragraph) to a single space, so a
// reflow of line breaks never fails a verbatim-wording comparison.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestGenerate_EverythingElseTracesBackToTheManifest(t *testing.T) {
	m1 := testManifest()
	m2 := testManifest()
	m2.Tool.Version = "9.9.9"
	m2.Verbs = append(m2.Verbs, manifest.Verb{Name: "next", Kind: surface.Plumbing, Description: "what's next"})

	files1, err := codex.Generate(m1)
	if err != nil {
		t.Fatalf("Generate(m1): %v", err)
	}
	files2, err := codex.Generate(m2)
	if err != nil {
		t.Fatalf("Generate(m2): %v", err)
	}
	skill1, skill2 := string(files1["SKILL.md"]), string(files2["SKILL.md"])
	if skill1 == skill2 {
		t.Fatal("SKILL.md is identical across two different manifests, want the generated portions to vary")
	}

	judgment, _ := asset.Resolve("templates/skills/codex/judgment.md")
	guidance, _ := asset.Resolve("agent-write-guidance.md")
	for _, skill := range []string{skill1, skill2} {
		if !strings.Contains(skill, string(judgment.Bytes())) || !strings.Contains(skill, string(guidance.Bytes())) {
			t.Fatal("the two hand-authored blocks must appear verbatim regardless of manifest content")
		}
	}
}

func TestSkillsDir_RespectsOverride(t *testing.T) {
	t.Setenv(codex.SkillsDirEnv, "/tmp/example-skills-dir")
	dir, err := codex.SkillsDir()
	if err != nil {
		t.Fatalf("SkillsDir: %v", err)
	}
	if dir != "/tmp/example-skills-dir" {
		t.Fatalf("SkillsDir = %q, want the override value", dir)
	}
}

func TestInstallDir_IsSkillsDirSlashWip(t *testing.T) {
	t.Setenv(codex.SkillsDirEnv, "/tmp/example-skills-dir")
	dir, err := codex.InstallDir()
	if err != nil {
		t.Fatalf("InstallDir: %v", err)
	}
	if dir != "/tmp/example-skills-dir/wip" {
		t.Fatalf("InstallDir = %q, want %q", dir, "/tmp/example-skills-dir/wip")
	}
}

func TestAvailable_OverrideExisting(t *testing.T) {
	t.Setenv(codex.SkillsDirEnv, t.TempDir())
	if !codex.Available() {
		t.Error("Available() = false, want true for an existing override directory")
	}
}

func TestAvailable_OverrideMissing(t *testing.T) {
	t.Setenv(codex.SkillsDirEnv, filepath.Join(t.TempDir(), "missing"))
	if codex.Available() {
		t.Error("Available() = true, want false for a missing override directory")
	}
}
