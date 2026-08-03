package pi_test

import (
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/asset"
	"github.com/procrastivity/wip/internal/harness/pi"
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
	files, err := pi.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		t.Error("Generate: missing SKILL.md")
	}
	if _, ok := files[".claude-plugin/plugin.json"]; ok {
		t.Error("Generate: pi has no plugin.json — that mechanism is claude-code's own")
	}
}

func TestGenerate_ProjectsOnlyPlumbingVerbs(t *testing.T) {
	files, err := pi.Generate(testManifest())
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
	files, err := pi.Generate(testManifest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	skill := string(files["SKILL.md"])

	judgment, err := asset.Resolve("templates/skills/pi/judgment.md")
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

func TestGenerate_EverythingElseTracesBackToTheManifest(t *testing.T) {
	m1 := testManifest()
	m2 := testManifest()
	m2.Tool.Version = "9.9.9"
	m2.Verbs = append(m2.Verbs, manifest.Verb{Name: "next", Kind: surface.Plumbing, Description: "what's next"})

	files1, err := pi.Generate(m1)
	if err != nil {
		t.Fatalf("Generate(m1): %v", err)
	}
	files2, err := pi.Generate(m2)
	if err != nil {
		t.Fatalf("Generate(m2): %v", err)
	}
	skill1, skill2 := string(files1["SKILL.md"]), string(files2["SKILL.md"])
	if skill1 == skill2 {
		t.Fatal("SKILL.md is identical across two different manifests, want the generated portions to vary")
	}

	judgment, _ := asset.Resolve("templates/skills/pi/judgment.md")
	guidance, _ := asset.Resolve("agent-write-guidance.md")
	for _, skill := range []string{skill1, skill2} {
		if !strings.Contains(skill, string(judgment.Bytes())) || !strings.Contains(skill, string(guidance.Bytes())) {
			t.Fatal("the two hand-authored blocks must appear verbatim regardless of manifest content")
		}
	}
}

func TestSkillsDir_RespectsOverride(t *testing.T) {
	t.Setenv(pi.SkillsDirEnv, "/tmp/example-skills-dir")
	dir, err := pi.SkillsDir()
	if err != nil {
		t.Fatalf("SkillsDir: %v", err)
	}
	if dir != "/tmp/example-skills-dir" {
		t.Fatalf("SkillsDir = %q, want the override value", dir)
	}
}

func TestInstallDir_IsSkillsDirSlashWip(t *testing.T) {
	t.Setenv(pi.SkillsDirEnv, "/tmp/example-skills-dir")
	dir, err := pi.InstallDir()
	if err != nil {
		t.Fatalf("InstallDir: %v", err)
	}
	if dir != "/tmp/example-skills-dir/wip" {
		t.Fatalf("InstallDir = %q, want %q", dir, "/tmp/example-skills-dir/wip")
	}
}
