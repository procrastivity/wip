// Step-06 of the generated-content-navigation Matter: binary cold-start
// proof that the whole read path works from command output alone. Every
// test here drives the path the frozen contract teaches (§7) and nothing
// else — run `next` (or `status --all` for §3.6's three recorded
// exclusions), take the Matter locator and generated directory out of the
// payload, run `wip plumbing refresh <matter>`, and read exactly the files
// refresh reports. No test reads wip's database, its event log, or its
// source to learn where a Matter's record lives, and none discovers a file
// by globbing.
//
// binPath, run, runIn, gitIn, newGitRepo, setupRepo, openTestStore,
// dbEnvPath, mustJSON, nodePayload and refreshPayload come from e2e_test.go
// / tiers_e2e_test.go / writesurface_birth_test.go /
// render_scratch_e2e_test.go, same package.
package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

// navNode mirrors the contract's §2 nodeJSON exactly — same fields, same
// tags, same declaration order — so re-marshalling a decoded entry
// reproduces the bytes production emitted. wantNodeVerbatim uses that to
// pin additive compatibility: the pre-existing id/address/kind/lifecycle
// keep their names, values and positions, and the two navigation fields
// are appended after them, never interleaved.
type navNode struct {
	ID           string `json:"id"`
	Address      string `json:"address"`
	Kind         string `json:"kind"`
	Lifecycle    string `json:"lifecycle"`
	Matter       string `json:"matter,omitempty"`
	GeneratedDir string `json:"generatedDir,omitempty"`
}

type navBlocked struct {
	Node      navNode   `json:"node"`
	BlockedBy []navNode `json:"blockedBy"`
}

// navPayload is the ordinary `next` payload as a cold-start reader sees it.
type navPayload struct {
	Kind       string       `json:"kind"`
	Node       *navNode     `json:"node"`
	Reason     string       `json:"reason"`
	Unmet      []navNode    `json:"unmet"`
	Met        []navNode    `json:"met"`
	Candidates []navNode    `json:"candidates"`
	InProgress []navNode    `json:"inProgress"`
	Blocked    []navBlocked `json:"blocked"`
}

// navFrontier is the Run-frontier payload (§3.2), whose ready[] entries use
// the same shared node shape.
type navFrontier struct {
	Kind    string    `json:"kind"`
	Run     string    `json:"run"`
	Locator string    `json:"locator"`
	Batch   string    `json:"batch"`
	Cap     int       `json:"cap"`
	Slots   int       `json:"slots"`
	Ready   []navNode `json:"ready"`
}

// nextJSON runs one `next` invocation and returns both the decoded ordinary
// payload and the raw bytes (the raw form is what the byte-exactness pins
// are asserted against). spelling is the argv prefix — "plumbing next" or
// the porcelain "next".
func nextJSON(t *testing.T, dir string, dbEnv []string, spelling ...string) (navPayload, string) {
	t.Helper()
	r := runIn(t, dir, dbEnv, append(spelling, "--json")...)
	if r.exitCode != 0 {
		t.Fatalf("%v --json: exit=%d stderr=%q", spelling, r.exitCode, r.stderr)
	}
	return mustJSON[navPayload](t, r.stdout), strings.TrimRight(r.stdout, "\n")
}

// wantNodeVerbatim pins one node object byte-for-byte inside raw.
func wantNodeVerbatim(t *testing.T, raw string, n navNode) {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, string(b)) {
		t.Errorf("payload does not carry the node object verbatim (field names, values and order are the contract's §2):\nwant %s\n raw %s", b, raw)
	}
}

// coldStartRead is the taught read path's second half, driven only by the
// matter locator and generatedDir a command just printed: refresh that
// Matter, then read exactly the files refresh reports. It asserts each
// reported path exists as a regular 0444 file directly under the
// generatedDir the reader was handed, that reading it needs nothing but the
// path, and — as a completeness audit distinct from the read itself — that
// the directory holds exactly the reported set, so nothing is discoverable
// there that refresh did not name.
//
// generatedDir is empty on §3.6's excluded variants, where no command
// printed one: it is then derived from the reported paths themselves (still
// command output, never git or the store) and checked for the shape §0
// freezes.
func coldStartRead(t *testing.T, dir string, dbEnv []string, matter, generatedDir string) []string {
	t.Helper()
	if matter == "" {
		t.Fatalf("command output named no Matter to navigate to")
	}

	r := runIn(t, dir, dbEnv, "plumbing", "refresh", matter, "--json")
	if r.exitCode != 0 {
		t.Fatalf("refresh %s: exit=%d stderr=%q", matter, r.exitCode, r.stderr)
	}
	renderedAt := strings.Index(r.stdout, `"rendered"`)
	filesAt := strings.Index(r.stdout, `"generatedFiles"`)
	if renderedAt < 0 || filesAt < 0 || filesAt < renderedAt {
		t.Fatalf("refresh payload must retain rendered and add generatedFiles after it: %q", r.stdout)
	}
	payload := mustJSON[refreshPayload](t, r.stdout)
	if len(payload.Rendered) != 1 || payload.Rendered[0] != matter {
		t.Fatalf("rendered = %v, want [%s] — the locator next printed is the locator refresh accepts", payload.Rendered, matter)
	}
	if len(payload.GeneratedFiles) == 0 {
		t.Fatalf("refresh %s reported no files, so the read path names nothing to read", matter)
	}
	if generatedDir == "" {
		generatedDir = filepath.Dir(payload.GeneratedFiles[0])
		if want := filepath.Join(".wip", "generated", matter); !strings.HasSuffix(generatedDir, string(filepath.Separator)+want) {
			t.Fatalf("refresh %s reported files in %q, want a directory ending in %q (§0)", matter, generatedDir, want)
		}
	}

	reported := map[string]bool{}
	for _, p := range payload.GeneratedFiles {
		if !filepath.IsAbs(p) {
			t.Errorf("reported file %q is not absolute (§1)", p)
			continue
		}
		if got := filepath.Dir(p); got != generatedDir {
			t.Errorf("reported file %q sits in %q, not the generatedDir %q the read path handed out", p, got, generatedDir)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("reported file %q does not exist: %v", p, err)
			continue
		}
		if !info.Mode().IsRegular() {
			t.Errorf("reported file %q is not a regular file (mode %v)", p, info.Mode())
		}
		if info.Mode().Perm() != 0o444 {
			t.Errorf("reported file %q has mode %v, want 0444", p, info.Mode().Perm())
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("reading reported file %q needed more than its path: %v", p, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("reported file %q is empty", p)
		}
		reported[filepath.Base(p)] = true
	}

	entries, err := os.ReadDir(generatedDir)
	if err != nil {
		t.Fatalf("read back %s: %v", generatedDir, err)
	}
	for _, e := range entries {
		if !reported[e.Name()] {
			t.Errorf("%s holds %q, which refresh did not report — the reported set must be the whole read surface", generatedDir, e.Name())
		}
	}
	if len(entries) != len(reported) {
		t.Errorf("%s holds %d entries, but refresh reported %d distinct files", generatedDir, len(entries), len(reported))
	}

	// The record itself is legible from the reported file alone: no store,
	// no event log, no wip source.
	record, err := os.ReadFile(filepath.Join(generatedDir, "matter.md"))
	if err != nil {
		t.Fatalf("read the Matter record refresh reported: %v", err)
	}
	if !strings.Contains(string(record), "locator: "+matter+"\n") {
		t.Errorf("matter.md does not identify %s from its own content:\n%s", matter, record)
	}
	return payload.GeneratedFiles
}

// matterLocatorsFromStatus reads Matter locators out of `wip plumbing status`
// output — the alternative route §3.6 sanctions for the three excluded
// variants. No parsing beyond taking a printed Matter locator is involved.
func matterLocatorsFromStatus(t *testing.T, stdout string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "matter" && !strings.Contains(fields[0], "·") {
			out = append(out, fields[0])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Ordinary shapes — one cold start per shape that names a node.
// ---------------------------------------------------------------------------

// TestColdStart_BareMatter drives the whole path through the porcelain
// `wip next` spelling, as the step-06 block requires of one case.
func TestColdStart_BareMatter(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start bare", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "next")
	if payload.Kind != "bare-matter" || payload.Node == nil {
		t.Fatalf("payload = %+v, want a bare-matter node", payload)
	}
	wantNodeVerbatim(t, raw, *payload.Node)
	coldStartRead(t, dir, dbEnv, payload.Node.Matter, payload.Node.GeneratedDir)

	// The human surface carries the same two facts on this shape (§3.3), so
	// a reader who never asked for JSON is not stranded.
	human := runIn(t, dir, dbEnv, "next")
	if human.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	wantLine := "  generated: " + payload.Node.GeneratedDir + " (refresh: wip plumbing refresh " + payload.Node.Matter + ")\n"
	if !strings.HasSuffix(human.stdout, wantLine) {
		t.Errorf("next human output = %q, want it to end with %q", human.stdout, wantLine)
	}
}

// TestColdStart_Positioned covers both positioned variants: a Matter-direct
// Step, and a Stage-grouped Step whose unmet blocker belongs to a second
// Matter — so the cold start is proven from a target node and from a
// blocker entry in the same result.
func TestColdStart_Positioned(t *testing.T) {
	t.Run("matter-direct step", func(t *testing.T) {
		dir, dbEnv := setupRepo(t)
		m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start direct", "--json").stdout)
		step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Only step", "--json").stdout)
		if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator+"/"+step.Locator); r.exitCode != 0 {
			t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator+"/"+step.Locator); r.exitCode != 0 {
			t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
		}

		payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
		if payload.Kind != "positioned" || payload.Node == nil {
			t.Fatalf("payload = %+v, want a positioned node", payload)
		}
		// The printed Step address is display-only; the matter field is what
		// refresh accepts, and this is the round trip the Matter exists to fix.
		if !strings.Contains(payload.Node.Address, " · ") {
			t.Fatalf("address = %q, want the display-only Step address form", payload.Node.Address)
		}
		if refused := runIn(t, dir, dbEnv, "plumbing", "refresh", payload.Node.Address); refused.exitCode == 0 {
			t.Errorf("refresh accepted the printed Step address %q, which §0 says it must not", payload.Node.Address)
		}
		wantNodeVerbatim(t, raw, *payload.Node)
		coldStartRead(t, dir, dbEnv, payload.Node.Matter, payload.Node.GeneratedDir)
	})

	t.Run("stage-grouped step with a cross-matter blocker", func(t *testing.T) {
		dir, dbEnv := setupRepo(t)
		blocker := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start blocker", "--json").stdout)
		if r := runIn(t, dir, dbEnv, "plumbing", "start", blocker.Locator); r.exitCode != 0 {
			t.Fatalf("start blocker: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start grouped", "--json").stdout)
		stage := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "stage", "create", m.Locator, "--title", "Build it", "--json").stdout)
		step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", stage.ID, "--title", "Grouped step", "--json").stdout)
		if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", m.Locator+"/"+step.Locator, "--blocked-by", blocker.Locator); r.exitCode != 0 {
			t.Fatalf("depend add: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator+"/"+step.Locator); r.exitCode != 0 {
			t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
		}
		if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator+"/"+step.Locator); r.exitCode != 0 {
			t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
		}

		payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
		if payload.Kind != "positioned" || payload.Node == nil {
			t.Fatalf("payload = %+v, want a positioned node", payload)
		}
		if len(payload.Unmet) != 1 {
			t.Fatalf("unmet = %+v, want the one cross-Matter blocker", payload.Unmet)
		}
		wantNodeVerbatim(t, raw, *payload.Node)
		wantNodeVerbatim(t, raw, payload.Unmet[0])
		if payload.Unmet[0].Matter == payload.Node.Matter {
			t.Errorf("the blocker entry reports the target's own Matter %q; each node carries its OWN owning Matter", payload.Node.Matter)
		}
		coldStartRead(t, dir, dbEnv, payload.Node.Matter, payload.Node.GeneratedDir)
		coldStartRead(t, dir, dbEnv, payload.Unmet[0].Matter, payload.Unmet[0].GeneratedDir)
	})
}

// TestColdStart_NoCursorCandidates proves the list shapes: with no cursor
// and candidates spanning two Matters, each entry carries its own owning
// Matter and each one navigates to its own record.
func TestColdStart_NoCursorCandidates(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	alpha := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start alpha", "--json").stdout)
	beta := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start beta", "--json").stdout)

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "no-cursor" || len(payload.Candidates) != 2 {
		t.Fatalf("payload = %+v, want no-cursor with two candidates", payload)
	}
	dirs := map[string]bool{}
	for _, c := range payload.Candidates {
		wantNodeVerbatim(t, raw, c)
		dirs[c.GeneratedDir] = true
		coldStartRead(t, dir, dbEnv, c.Matter, c.GeneratedDir)
	}
	if len(dirs) != 2 {
		t.Errorf("candidates share a generatedDir %v, want one per owning Matter (%s, %s)", dirs, alpha.Locator, beta.Locator)
	}
	wantNoGeneratedLine(t, dir, dbEnv)
}

// wantNoGeneratedLine holds §3.3's list-shape rule from the cold-start side:
// a list result offers no human generated: line, so JSON is the surface the
// read path rests on for these shapes.
func wantNoGeneratedLine(t *testing.T, dir string, dbEnv []string) {
	t.Helper()
	human := runIn(t, dir, dbEnv, "plumbing", "next")
	if human.exitCode != 0 {
		t.Fatalf("next: exit=%d stderr=%q", human.exitCode, human.stderr)
	}
	if strings.Contains(human.stdout, "generated:") {
		t.Errorf("a list shape printed a generated: line, which §3.3 withholds:\n%s", human.stdout)
	}
}

// TestColdStart_InProgressNoCursor: work under way with no cursor set.
func TestColdStart_InProgressNoCursor(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start under way", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "in-progress-no-cursor" || len(payload.InProgress) != 1 {
		t.Fatalf("payload = %+v, want in-progress-no-cursor with one entry", payload)
	}
	wantNodeVerbatim(t, raw, payload.InProgress[0])
	coldStartRead(t, dir, dbEnv, payload.InProgress[0].Matter, payload.InProgress[0].GeneratedDir)
	wantNoGeneratedLine(t, dir, dbEnv)
}

// TestColdStart_NothingUnblockedBlockedVariant: the blocked variant's lists
// are the only node objects the shape has, and both halves of an edge
// navigate.
func TestColdStart_NothingUnblockedBlockedVariant(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	upstream := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start upstream", "--json").stdout)
	downstream := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start downstream", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "depend", "add", downstream.Locator, "--blocked-by", upstream.Locator); r.exitCode != 0 {
		t.Fatalf("depend add: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "start", upstream.Locator); r.exitCode != 0 {
		t.Fatalf("start upstream: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "nothing-unblocked" || len(payload.Blocked) != 1 || len(payload.Blocked[0].BlockedBy) != 1 {
		t.Fatalf("payload = %+v, want nothing-unblocked with one blocked edge", payload)
	}
	blocked, by := payload.Blocked[0].Node, payload.Blocked[0].BlockedBy[0]
	wantNodeVerbatim(t, raw, blocked)
	wantNodeVerbatim(t, raw, by)
	coldStartRead(t, dir, dbEnv, blocked.Matter, blocked.GeneratedDir)
	coldStartRead(t, dir, dbEnv, by.Matter, by.GeneratedDir)
}

// TestColdStart_ChooseNextSealed is the sealed chase the step-06 block names:
// the target sealed, so the bare refresh skips its Matter entirely, and the
// locator form next just printed is the only way its record renders.
func TestColdStart_ChooseNextSealed(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start sealed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "choose-next" || payload.Reason != "sealed" || payload.Node == nil {
		t.Fatalf("payload = %+v, want choose-next/sealed with a node", payload)
	}
	wantNodeVerbatim(t, raw, *payload.Node)

	// The bare form is session-start hygiene and skips sealed Matters, so a
	// reader who never took the locator out of next would find nothing.
	bare := runIn(t, dir, dbEnv, "plumbing", "refresh", "--json")
	if bare.exitCode != 0 {
		t.Fatalf("bare refresh: exit=%d stderr=%q", bare.exitCode, bare.stderr)
	}
	barePayload := mustJSON[refreshPayload](t, bare.stdout)
	for _, locator := range barePayload.Rendered {
		if locator == payload.Node.Matter {
			t.Fatalf("bare refresh rendered the sealed Matter %s; the locator form is what the chase needs", locator)
		}
	}
	for _, p := range barePayload.GeneratedFiles {
		if strings.HasPrefix(p, payload.Node.GeneratedDir+string(filepath.Separator)) {
			t.Errorf("bare refresh reported %q under the sealed Matter's directory", p)
		}
	}

	coldStartRead(t, dir, dbEnv, payload.Node.Matter, payload.Node.GeneratedDir)
}

// TestColdStart_ChooseNextCanceled: same shape, canceled reason.
func TestColdStart_ChooseNextCanceled(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start canceled", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "cancel", m.Locator, "--reason", "superseded"); r.exitCode != 0 {
		t.Fatalf("cancel: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "choose-next" || payload.Reason != "canceled" || payload.Node == nil {
		t.Fatalf("payload = %+v, want choose-next/canceled with a node", payload)
	}
	wantNodeVerbatim(t, raw, *payload.Node)
	coldStartRead(t, dir, dbEnv, payload.Node.Matter, payload.Node.GeneratedDir)
}

// TestColdStart_ChooseNextRemovedWithLists: a tombstoned target resolves to
// no node at all, so the read path runs off a list entry instead (§3.1's
// removed row, lists non-empty).
func TestColdStart_ChooseNextRemovedWithLists(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start removed", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Doomed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "step", "remove", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("step remove: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	payload, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "choose-next" || payload.Reason != "removed" {
		t.Fatalf("payload = %+v, want choose-next/removed", payload)
	}
	if payload.Node != nil {
		t.Errorf("node = %+v, want none — a tombstoned target resolves to nothing", payload.Node)
	}
	if len(payload.InProgress) != 1 {
		t.Fatalf("inProgress = %+v, want the still-running Matter", payload.InProgress)
	}
	wantNodeVerbatim(t, raw, payload.InProgress[0])
	coldStartRead(t, dir, dbEnv, payload.InProgress[0].Matter, payload.InProgress[0].GeneratedDir)
}

// TestColdStart_RunFrontier drives the read path off the Run-frontier
// payload, which replaces the ordinary one when a Run is open. The Run is
// seeded through the store because starting one is control-plane and has no
// CLI verb (S6); the read path itself is command output only.
func TestColdStart_RunFrontier(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	dbPath := dbEnvPath(dbEnv)

	alpha := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start frontier alpha", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", alpha.Locator, "--title", "one", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", alpha.Locator); r.exitCode != 0 {
		t.Fatalf("start alpha: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	beta := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start frontier beta", "--json").stdout)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", beta.Locator, "--title", "one", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", beta.Locator); r.exitCode != 0 {
		t.Fatalf("start beta: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	s := openTestStore(t, dbPath)
	ctx := context.Background()
	repos, _ := s.Repos(ctx)
	clones, _ := s.ClonesOfRepo(ctx, repos[0].ID)
	worktrees, _ := s.WorktreesOfClone(ctx, clones[0].ID)
	env := store.Env{Repo: repos[0].ID, Clone: clones[0].ID, Worktree: worktrees[0].ID}
	batch, run := s.NewID(), s.NewID()
	if _, err := s.Commit(ctx, store.Request{Actor: store.ActorHuman, Env: env}, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{
			{Type: store.TypeBatchCreated, Subject: batch, Payload: store.BatchCreated{Name: "cold-start-run"}},
			{Type: store.TypeRunStarted, Subject: run, Payload: store.RunStarted{Batch: batch, Locator: "run-06", Matters: []string{alpha.ID, beta.ID}}, Cause: 0},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	j := runIn(t, dir, dbEnv, "plumbing", "next", "--json")
	if j.exitCode != 0 {
		t.Fatalf("next --json: exit=%d stderr=%q", j.exitCode, j.stderr)
	}
	raw := strings.TrimRight(j.stdout, "\n")
	frontier := mustJSON[navFrontier](t, raw)
	if frontier.Kind != "run-frontier" || len(frontier.Ready) != 2 {
		t.Fatalf("frontier = %+v, want the run-frontier shape with two ready nodes", frontier)
	}
	// Additive compatibility: the pre-existing frontier fields keep their
	// names, values and order behind the new discriminator.
	wantHead, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Run     string `json:"run"`
		Locator string `json:"locator"`
		Batch   string `json:"batch"`
		Cap     int    `json:"cap"`
		Slots   int    `json:"slots"`
	}{"run-frontier", run, "run-06", "cold-start-run", frontier.Cap, frontier.Slots})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, strings.TrimSuffix(string(wantHead), "}")+`,"ready":[`) {
		t.Errorf("frontier payload head = %q, want the frozen field order %s", raw, wantHead)
	}

	dirs := map[string]bool{}
	for _, node := range frontier.Ready {
		wantNodeVerbatim(t, raw, node)
		dirs[node.GeneratedDir] = true
		coldStartRead(t, dir, dbEnv, node.Matter, node.GeneratedDir)
	}
	if len(dirs) != 2 {
		t.Errorf("ready nodes share a generatedDir %v, want one per owning Matter", dirs)
	}
}

// TestColdStart_SetCarriesTheReadPath: `--set` names a concrete node and the
// next act is to work it, so its JSON carries the same two fields flat
// (§3.5) — enough to reach the record without a second `next`.
func TestColdStart_SetCarriesTheReadPath(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	_ = mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start set", "--json").stdout)

	payload, _ := nextJSON(t, dir, dbEnv, "plumbing", "next")
	if payload.Kind != "no-cursor" || len(payload.Candidates) != 1 {
		t.Fatalf("payload = %+v, want one candidate to set the cursor to", payload)
	}

	setResult := runIn(t, dir, dbEnv, "plumbing", "next", "--set", payload.Candidates[0].Matter, "--json")
	if setResult.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", setResult.exitCode, setResult.stderr)
	}
	set := mustJSON[struct {
		Node         string `json:"node"`
		Address      string `json:"address"`
		Matter       string `json:"matter"`
		GeneratedDir string `json:"generatedDir"`
	}](t, setResult.stdout)
	if set.Matter != payload.Candidates[0].Matter || set.GeneratedDir != payload.Candidates[0].GeneratedDir {
		t.Errorf("--set payload = %+v, want the same matter/generatedDir the candidate carried", set)
	}
	coldStartRead(t, dir, dbEnv, set.Matter, set.GeneratedDir)
}

// ---------------------------------------------------------------------------
// §3.6's three recorded exclusions — no node object anywhere, so the read
// path runs through `wip plumbing status --all` instead.
// ---------------------------------------------------------------------------

// wantNoNavigationMetadata asserts a payload carries neither navigation
// field anywhere, and pins the whole payload byte-exactly — these three
// shapes are byte-identical to their pre-change form.
func wantNoNavigationMetadata(t *testing.T, raw, want string) {
	t.Helper()
	if raw != want {
		t.Fatalf("payload = %q, want %q byte-for-byte (§3.6: no node objects, no metadata)", raw, want)
	}
	for _, field := range []string{`"matter"`, `"generatedDir"`} {
		if strings.Contains(raw, field) {
			t.Errorf("payload %q carries %s, which the exclusion forbids", raw, field)
		}
	}
}

// followStatusRoute is the taught fallback: with no node in the payload,
// take a Matter locator from `wip plumbing status --all` and refresh it.
func followStatusRoute(t *testing.T, dir string, dbEnv []string, wantLocator string) {
	t.Helper()
	all := runIn(t, dir, dbEnv, "plumbing", "status", "--all")
	if all.exitCode != 0 {
		t.Fatalf("status --all: exit=%d stderr=%q", all.exitCode, all.stderr)
	}
	locators := matterLocatorsFromStatus(t, all.stdout)
	found := ""
	for _, l := range locators {
		if l == wantLocator {
			found = l
		}
	}
	if found == "" {
		t.Fatalf("status --all named %v, want the Matter %s the reader must reach:\n%s", locators, wantLocator, all.stdout)
	}
	// No generatedDir was printed on these shapes; refresh's own reported
	// paths are the whole read surface.
	coldStartRead(t, dir, dbEnv, found, "")
}

// TestColdStart_Excluded_IdleNothingUnblocked is §3.6 item 1: the ordinary
// resting state of a repo whose work is Done with an open gate.
func TestColdStart_Excluded_IdleNothingUnblocked(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start idle", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	_, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	wantNoNavigationMetadata(t, raw, `{"kind":"nothing-unblocked"}`)
	followStatusRoute(t, dir, dbEnv, m.Locator)
}

// TestColdStart_Excluded_ChooseNextRemovedEmptyLists is §3.6 item 2.
func TestColdStart_Excluded_ChooseNextRemovedEmptyLists(t *testing.T) {
	dir, dbEnv := setupRepo(t)
	if r := runIn(t, dir, dbEnv, "plumbing", "gate", "declare", "reviewed-local", "--scale", "matter"); r.exitCode != 0 {
		t.Fatalf("gate declare: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	m := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "matter", "create", "--title", "Cold start orphan", "--json").stdout)
	step := mustJSON[nodePayload](t, runIn(t, dir, dbEnv, "plumbing", "step", "create", m.Locator, "--title", "Doomed", "--json").stdout)
	if r := runIn(t, dir, dbEnv, "plumbing", "start", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("start: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "next", "--set", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("next --set: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "step", "remove", m.Locator+"/"+step.Locator); r.exitCode != 0 {
		t.Fatalf("step remove: exit=%d stderr=%q", r.exitCode, r.stderr)
	}
	if r := runIn(t, dir, dbEnv, "plumbing", "finish", m.Locator); r.exitCode != 0 {
		t.Fatalf("finish: exit=%d stderr=%q", r.exitCode, r.stderr)
	}

	_, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	wantNoNavigationMetadata(t, raw, `{"kind":"choose-next","reason":"removed"}`)
	followStatusRoute(t, dir, dbEnv, m.Locator)
}

// agedSealedRepo seeds a repo whose every event — the tier bootstrap
// included — was minted thirty days ago, leaving one sealed Matter older
// than status's 14-day recent-sealed window.
//
// The CLI environment exposes no clock seam (it carries WIP_DB_PATH and
// nothing else, and §6/§8 forbid adding one), so the fixture is minted
// in-process through store.OpenWithClock. It has to be the WHOLE fixture,
// not just the sealing finish: store.NewID mints strictly above every
// identity the store has issued or read, so a backdated write over
// present-dated bootstrap events spins forever waiting for the clock to
// clear that floor. Seeding everything under one backdated clock keeps the
// floor behind wall-clock time, so the real-clock read commands that follow
// still append normally.
func agedSealedRepo(t *testing.T, title string) (dir string, dbEnv []string, locator string) {
	t.Helper()
	dir = newGitRepo(t, "aged")
	dbPath := filepath.Join(t.TempDir(), "wip.db")
	dbEnv = []string{"WIP_DB_PATH=" + dbPath}

	backdated := time.Now().Add(-30 * 24 * time.Hour)
	aged, err := store.OpenWithClock(dbPath, func() time.Time { return backdated })
	if err != nil {
		t.Fatalf("OpenWithClock: %v", err)
	}
	defer func() { _ = aged.Close() }()

	ctx := context.Background()
	if _, err := tiers.Init(ctx, aged, store.ActorHuman, dir, ""); err != nil {
		t.Fatalf("backdated init: %v", err)
	}
	repos, err := aged.Repos(ctx)
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %v, err=%v", repos, err)
	}
	repo := repos[0].ID
	m, err := writesurface.CreateMatter(ctx, aged, store.ActorHuman, repo, title, "")
	if err != nil {
		t.Fatalf("backdated matter create: %v", err)
	}
	if _, err := writesurface.Start(ctx, aged, store.ActorHuman, repo, m.Locator); err != nil {
		t.Fatalf("backdated start: %v", err)
	}
	if _, err := writesurface.Finish(ctx, aged, store.ActorHuman, repo, m.Locator); err != nil {
		t.Fatalf("backdated finish: %v", err)
	}
	return dir, dbEnv, m.Locator
}

// TestColdStart_Excluded_EverythingSealed is §3.6 item 3, with the seal
// backdated past the 14-day recent-sealed window so plain `status` hides the
// Matter behind its count line and only `--all` yields a locator. Everything
// the read path itself does is command output only.
func TestColdStart_Excluded_EverythingSealed(t *testing.T) {
	dir, dbEnv, locator := agedSealedRepo(t, "Cold start aged")

	_, raw := nextJSON(t, dir, dbEnv, "plumbing", "next")
	wantNoNavigationMetadata(t, raw, `{"kind":"everything-sealed"}`)

	// The plain form is the dead end the amendment closed: the Matter is
	// sealed and older than the window, so status prints a count, not a
	// locator.
	plain := runIn(t, dir, dbEnv, "plumbing", "status")
	if plain.exitCode != 0 {
		t.Fatalf("status: exit=%d stderr=%q", plain.exitCode, plain.stderr)
	}
	if strings.Contains(plain.stdout, locator) {
		t.Fatalf("plain status still names %s, so the --all half of the route is unproven:\n%s", locator, plain.stdout)
	}
	if !strings.Contains(plain.stdout, "more sealed matter(s)") {
		t.Fatalf("plain status printed no hidden-sealed pointer line:\n%s", plain.stdout)
	}

	followStatusRoute(t, dir, dbEnv, locator)
}
