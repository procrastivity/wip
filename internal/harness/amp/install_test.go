package amp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/harness/amp"
	"github.com/procrastivity/wip/internal/manifest"
)

func withSkillsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(amp.SkillsDirEnv, dir)
	return dir
}

func TestInstall_WritesStampedTree(t *testing.T) {
	withSkillsDir(t)

	dir, err := amp.Install(testManifest())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing after install: %v", err)
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if !ok {
		t.Fatal("ReadStamp: no stamp found after install")
	}
	if stamp.ToolVersion != testManifest().Tool.Version {
		t.Errorf("stamp.ToolVersion = %q, want %q", stamp.ToolVersion, testManifest().Tool.Version)
	}
	if _, ok := stamp.Files["SKILL.md"]; !ok {
		t.Error("stamp.Files missing SKILL.md")
	}
}

func TestUninstall_RemovesExactlyTheStampedTree(t *testing.T) {
	skillsDir := withSkillsDir(t)

	// An unrelated skill directory that must survive uninstall untouched.
	unrelated := filepath.Join(skillsDir, "some-other-skill")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "SKILL.md"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := amp.Install(testManifest())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	removedDir, err := amp.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if removedDir != dir {
		t.Errorf("Uninstall returned %q, want %q", removedDir, dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("install dir still exists after uninstall: err=%v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated skill directory was disturbed by uninstall: %v", err)
	}
}

func TestUninstall_NothingInstalled(t *testing.T) {
	withSkillsDir(t)

	if _, err := amp.Uninstall(); err == nil {
		t.Fatal("Uninstall: want an error when nothing is installed")
	}
}

func TestUninstall_RefusesUnstampedForeignContent(t *testing.T) {
	skillsDir := withSkillsDir(t)

	dir := filepath.Join(skillsDir, "wip")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-written, not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := amp.Uninstall(); err == nil {
		t.Fatal("Uninstall: want a refusal against a target with no install stamp")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("foreign content was removed despite the refusal: %v", err)
	}
}

func TestUninstall_RefusesHandEditedInstalledContent(t *testing.T) {
	withSkillsDir(t)

	dir, err := amp.Install(testManifest())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	skillMD := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := amp.Uninstall(); err == nil {
		t.Fatal("Uninstall: want a refusal when installed content was hand-edited since install")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("hand-edited tree was removed despite the refusal: %v", err)
	}
}

func TestDrift_ReportsAddedVerbAfterInstall_ThenReinstallClearsIt(t *testing.T) {
	withSkillsDir(t)

	m1 := testManifest()
	dir, err := amp.Install(m1)
	if err != nil {
		t.Fatalf("Install(m1): %v", err)
	}
	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil || !ok {
		t.Fatalf("ReadStamp after install: ok=%v err=%v", ok, err)
	}

	m2 := testManifest()
	m2.Verbs = append(m2.Verbs, manifest.Verb{Name: "next", Kind: m1.Verbs[0].Kind, Description: "what's next"})

	files2, err := amp.Generate(m2)
	if err != nil {
		t.Fatalf("Generate(m2): %v", err)
	}
	want := manifest.ChecksumFiles(files2)

	drift := manifest.Drift(want, stamp)
	if len(drift) == 0 {
		t.Fatal("Drift: want at least one finding after adding a verb, got none")
	}

	if _, err := amp.Install(m2); err != nil {
		t.Fatalf("Install(m2): %v", err)
	}
	stamp2, ok, err := manifest.ReadStamp(dir)
	if err != nil || !ok {
		t.Fatalf("ReadStamp after re-install: ok=%v err=%v", ok, err)
	}
	if drift := manifest.Drift(want, stamp2); len(drift) != 0 {
		t.Fatalf("Drift after re-install = %+v, want none", drift)
	}
}
