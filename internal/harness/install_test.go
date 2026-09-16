package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/manifest"
)

// The shared Install/Uninstall bodies serve every harness, so their
// behavior is proven once here against a stub target — per-harness install
// coverage lives in the e2e tests, which drive `wip install <harness>` for
// each registered name.

func stubTarget(t *testing.T) (installDir func() (string, error), dir string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "wip")
	return func() (string, error) { return dir, nil }, dir
}

func stubGenerate(m manifest.Manifest) (map[string][]byte, error) {
	body := []byte("# " + m.Tool.Name + " " + m.Tool.Version + "\n")
	for _, v := range m.Verbs {
		body = append(body, []byte(v.Name+"\n")...)
	}
	return map[string][]byte{
		"SKILL.md":        body,
		"nested/extra.md": []byte("nested content\n"),
	}, nil
}

func installManifest() manifest.Manifest {
	return manifest.Manifest{
		Tool:          manifest.Tool{Name: "wip", Version: "1.2.3"},
		SchemaVersion: manifest.SchemaVersion,
		Verbs:         []manifest.Verb{{Name: "status"}},
	}
}

func TestInstall_WritesStampedTree(t *testing.T) {
	installDir, dir := stubTarget(t)

	got, err := harness.Install("stub", installDir, stubGenerate, installManifest())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got != dir {
		t.Errorf("Install returned %q, want %q", got, dir)
	}

	for _, want := range []string{"SKILL.md", filepath.Join("nested", "extra.md")} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s missing after install: %v", want, err)
		}
	}

	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil {
		t.Fatalf("ReadStamp: %v", err)
	}
	if !ok {
		t.Fatal("ReadStamp: no stamp found after install")
	}
	if stamp.ToolVersion != "1.2.3" {
		t.Errorf("stamp.ToolVersion = %q, want %q", stamp.ToolVersion, "1.2.3")
	}
	if _, ok := stamp.Files["SKILL.md"]; !ok {
		t.Error("stamp.Files missing SKILL.md")
	}
}

func TestUninstall_RemovesExactlyTheStampedTree(t *testing.T) {
	installDir, dir := stubTarget(t)

	// An unrelated sibling directory that must survive uninstall untouched.
	unrelated := filepath.Join(filepath.Dir(dir), "some-other-skill")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "SKILL.md"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := harness.Install("stub", installDir, stubGenerate, installManifest()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	removedDir, err := harness.Uninstall("stub", installDir)
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
	installDir, _ := stubTarget(t)

	if _, err := harness.Uninstall("stub", installDir); err == nil {
		t.Fatal("Uninstall: want an error when nothing is installed")
	}
}

func TestUninstall_RefusesUnstampedForeignContent(t *testing.T) {
	installDir, dir := stubTarget(t)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-written, not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := harness.Uninstall("stub", installDir); err == nil {
		t.Fatal("Uninstall: want a refusal against a target with no install stamp")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("foreign content was removed despite the refusal: %v", err)
	}
}

func TestUninstall_RefusesHandEditedInstalledContent(t *testing.T) {
	installDir, dir := stubTarget(t)

	if _, err := harness.Install("stub", installDir, stubGenerate, installManifest()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	skillMD := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("hand-edited after install"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := harness.Uninstall("stub", installDir); err == nil {
		t.Fatal("Uninstall: want a refusal when installed content was hand-edited since install")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("hand-edited tree was removed despite the refusal: %v", err)
	}
}

func TestDrift_ReportsAddedVerbAfterInstall_ThenReinstallClearsIt(t *testing.T) {
	installDir, dir := stubTarget(t)

	m1 := installManifest()
	if _, err := harness.Install("stub", installDir, stubGenerate, m1); err != nil {
		t.Fatalf("Install(m1): %v", err)
	}
	stamp, ok, err := manifest.ReadStamp(dir)
	if err != nil || !ok {
		t.Fatalf("ReadStamp after install: ok=%v err=%v", ok, err)
	}

	m2 := installManifest()
	m2.Verbs = append(m2.Verbs, manifest.Verb{Name: "next", Description: "what's next"})

	files2, err := stubGenerate(m2)
	if err != nil {
		t.Fatalf("stubGenerate(m2): %v", err)
	}
	want := manifest.ChecksumFiles(files2)

	drift := manifest.Drift(want, stamp)
	if len(drift) == 0 {
		t.Fatal("Drift: want at least one finding after adding a verb, got none")
	}

	if _, err := harness.Install("stub", installDir, stubGenerate, m2); err != nil {
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
