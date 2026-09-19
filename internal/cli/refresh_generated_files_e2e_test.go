// End-to-end tests for the navigation contract's §4 (generatedFiles), step-04
// rows 4.7-4.10: the refresh JSON and human surfaces report the files a
// render pass actually wrote (binPath, run, runIn, gitIn, newGitRepo,
// setupRepo, dbEnvPath, mustJSON, nodePayload, refreshPayload come from
// e2e_test.go / tiers_e2e_test.go / writesurface_birth_test.go /
// render_scratch_e2e_test.go, same package).
package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRefreshJSON_LocatorForm_GeneratedFilesAfterRendered is §9 4.7: the
// on-demand (locator) path's JSON carries generatedFiles after the
// byte-unchanged rendered field, and a sealed Matter renders and reports
// its files.
func TestRefreshJSON_LocatorForm_GeneratedFilesAfterRendered(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Locator form", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "brief", m.ID, "--file", writeTempFile(t, "# Brief\n"), "--json"); r.exitCode != 0 {
		t.Fatalf("brief: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "close", "reviewed-local", m.Locator); r.exitCode != 0 {
		t.Fatalf("gate close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "plumbing", "refresh", m.Locator, "--json")
	if r.exitCode != 0 {
		t.Fatalf("refresh <locator>: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	renderedAt := strings.Index(r.stdout, `"rendered"`)
	filesAt := strings.Index(r.stdout, `"generatedFiles"`)
	if renderedAt < 0 || filesAt < 0 || filesAt < renderedAt {
		t.Fatalf("generatedFiles does not follow rendered in %q", r.stdout)
	}

	payload := mustJSON[refreshPayload](t, r.stdout)
	if len(payload.Rendered) != 1 || payload.Rendered[0] != m.Locator {
		t.Errorf("rendered = %v, want [%s] (sealed Matter must still render on the locator path)", payload.Rendered, m.Locator)
	}
	want := []string{
		filepath.Join(dir, ".wip", "generated", m.Locator, "matter.md"),
		filepath.Join(dir, ".wip", "generated", m.Locator, "brief.md"),
	}
	if len(payload.GeneratedFiles) != len(want) {
		t.Fatalf("generatedFiles = %v, want %v", payload.GeneratedFiles, want)
	}
	for i, p := range want {
		if payload.GeneratedFiles[i] != p {
			t.Errorf("generatedFiles[%d] = %q, want %q", i, payload.GeneratedFiles[i], p)
		}
	}
}

// TestRefreshJSON_BareForm_FullListAndEmptyWhenNothingRendered is §9 4.8.
func TestRefreshJSON_BareForm_FullListAndEmptyWhenNothingRendered(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	empty := runIn(t, dir, dbEnv, "plumbing", "refresh", "--json")
	if empty.exitCode != 0 {
		t.Fatalf("refresh (nothing to render): exit=%d stderr=%q", empty.exitCode, empty.stderr)
	}
	emptyPayload := mustJSON[refreshPayload](t, empty.stdout)
	if emptyPayload.GeneratedFiles == nil || len(emptyPayload.GeneratedFiles) != 0 {
		t.Errorf("generatedFiles = %v, want present and empty", emptyPayload.GeneratedFiles)
	}
	if !strings.Contains(empty.stdout, `"generatedFiles":[]`) {
		t.Errorf("stdout = %q, want the literal empty-array form (not omitted, not null)", empty.stdout)
	}

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Bare form", "--json").stdout)

	r := runIn(t, dir, dbEnv, "plumbing", "refresh", "--json")
	if r.exitCode != 0 {
		t.Fatalf("refresh --json: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	payload := mustJSON[refreshPayload](t, r.stdout)
	want := filepath.Join(dir, ".wip", "generated", m.Locator, "matter.md")
	if len(payload.GeneratedFiles) != 1 || payload.GeneratedFiles[0] != want {
		t.Errorf("generatedFiles = %v, want [%s]", payload.GeneratedFiles, want)
	}
}

// TestRefreshHuman_LocatorForm_ListsFilesIndented is §9 4.9.
func TestRefreshHuman_LocatorForm_ListsFilesIndented(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Human locator form", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "brief", m.ID, "--file", writeTempFile(t, "# Brief\n"), "--json"); r.exitCode != 0 {
		t.Fatalf("brief: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "plumbing", "refresh", m.Locator)
	if r.exitCode != 0 {
		t.Fatalf("refresh <locator>: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	matterFile := filepath.Join(dir, ".wip", "generated", m.Locator, "matter.md")
	briefFile := filepath.Join(dir, ".wip", "generated", m.Locator, "brief.md")
	want := "rendered 1 matter(s)\n" +
		"  " + matterFile + "\n" +
		"  " + briefFile + "\n"
	if !strings.HasSuffix(r.stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", r.stdout, want)
	}
	if strings.Contains(r.stdout, "wrote ") {
		t.Errorf("locator-form output must not print the bare form's summary line:\n%s", r.stdout)
	}
}

// TestRefreshHuman_BareForm_PrintsOnlyCount is §9 4.10.
func TestRefreshHuman_BareForm_PrintsOnlyCount(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Human bare form", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "brief", m.ID, "--file", writeTempFile(t, "# Brief\n"), "--json"); r.exitCode != 0 {
		t.Fatalf("brief: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	r := runIn(t, dir, dbEnv, "plumbing", "refresh")
	if r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if !strings.HasSuffix(r.stdout, "rendered 1 matter(s)\nwrote 2 file(s)\n") {
		t.Errorf("stdout = %q, want it to end with the bare-form summary line and no per-file lines", r.stdout)
	}
	if strings.Contains(r.stdout, "matter.md") || strings.Contains(r.stdout, "brief.md") {
		t.Errorf("bare-form output must not list individual files:\n%s", r.stdout)
	}
}
