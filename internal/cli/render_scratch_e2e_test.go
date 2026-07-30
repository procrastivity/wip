// End-to-end tests for the render-scratch Matter (workplans/render-scratch.md),
// through the actual built binary (binPath, run, runIn, gitIn, newGitRepo,
// setupRepo, dbEnvPath, mustJSON, nodePayload come from e2e_test.go /
// tiers_e2e_test.go / writesurface_birth_test.go, same package).
package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type refreshPayload struct {
	Dispatch   string   `json:"dispatch"`
	ScratchDir string   `json:"scratchDir"`
	Opened     bool     `json:"opened"`
	Superseded string   `json:"superseded"`
	Rendered   []string `json:"rendered"`
}

// TestRenderScratch_DeleteWipBetweenDispatchesLosesNothing exercises D33's
// own test through the real, built binary: open a dispatch, render, write a
// Brief, close the dispatch, delete `.wip/` wholesale, then refresh again —
// every durable fact survives, because none of it lived in `.wip/`.
func TestRenderScratch_DeleteWipBetweenDispatchesLosesNothing(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Durable Work", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "brief", m.ID, "--file", writeTempFile(t, "# Brief\n\nwhy this exists\n"), "--json"); r.exitCode != 0 {
		t.Fatalf("brief: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	first := runIn(t, dir, dbEnv, "refresh", "--json")
	if first.exitCode != 0 {
		t.Fatalf("first refresh: exit=%d stderr=%q", first.exitCode, first.stderr)
	}
	firstResult := mustJSON[refreshPayload](t, first.stdout)
	if !firstResult.Opened {
		t.Error("first refresh should have opened a dispatch")
	}
	briefPath := filepath.Join(dir, ".wip", "generated", m.Locator, "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Fatalf("brief.md was not rendered: %v", err)
	}

	if r := runIn(t, dir, dbEnv, "dispatch", "close", "--json"); r.exitCode != 0 {
		t.Fatalf("dispatch close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if err := os.RemoveAll(filepath.Join(dir, ".wip")); err != nil {
		t.Fatalf("removing .wip/: %v", err)
	}

	second := runIn(t, dir, dbEnv, "refresh", "--json")
	if second.exitCode != 0 {
		t.Fatalf("second refresh: exit=%d stderr=%q", second.exitCode, second.stderr)
	}
	secondResult := mustJSON[refreshPayload](t, second.stdout)
	if !secondResult.Opened {
		t.Error("second refresh should have opened a fresh dispatch (the prior one was explicitly closed)")
	}
	if secondResult.Dispatch == firstResult.Dispatch {
		t.Error("second refresh reused the first dispatch's id")
	}

	data, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("brief.md did not survive delete-and-refresh: %v", err)
	}
	if !strings.Contains(string(data), "why this exists") {
		t.Errorf("brief.md = %q, want the durable Brief content", data)
	}
	info, err := os.Stat(briefPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("brief.md mode = %o, want 0444", info.Mode().Perm())
	}
}

// TestRenderScratch_ClusterOfMattersEagerlySkipsSealed is step-05's Done,
// through the real binary: eager `wip refresh` covers every not-sealed
// Matter and skips a sealed one, which renders only via an explicit
// `wip refresh <locator>`.
func TestRenderScratch_ClusterOfMattersEagerlySkipsSealed(t *testing.T) {
	dir, dbEnv := setupRepo(t)

	active := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Active One", "--json").stdout)
	sealed := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "matter", "create", "--title", "Sealed One", "--json").stdout)

	if r := runIn(t, dir, dbEnv, "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "start", sealed.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "finish", sealed.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "gate", "close", "reviewed-local", sealed.Locator); r.exitCode != 0 {
		t.Fatalf("gate close: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if r := runIn(t, dir, dbEnv, "refresh", "--json"); r.exitCode != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	if _, err := os.Stat(filepath.Join(dir, ".wip", "generated", active.Locator, "matter.md")); err != nil {
		t.Errorf("active matter was not rendered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wip", "generated", sealed.Locator, "matter.md")); err == nil {
		t.Error("sealed matter was rendered eagerly; it should render only on explicit refresh")
	}

	if r := runIn(t, dir, dbEnv, "refresh", sealed.Locator, "--json"); r.exitCode != 0 {
		t.Fatalf("refresh <sealed-locator>: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wip", "generated", sealed.Locator, "matter.md")); err != nil {
		t.Errorf("sealed matter was not rendered on explicit refresh: %v", err)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prose.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
