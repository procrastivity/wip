package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// TestDepthPolicy_NoStagesRendersOneMatterFile is MODEL §3.1's first example:
// "a bugfix Matter with no Stages renders as one matter.md".
func TestDepthPolicy_NoStagesRendersOneMatterFile(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "matter.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("generated files = %v, want exactly [matter.md]", names)
	}
	md, err := os.ReadFile(filepath.Join(dir, "matter.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(md), "\n## Findings\n") {
		t.Error("a Matter with no findings rendered a Findings section")
	}
}

func appendFinding(t *testing.T, s *store.Store, repo, locator string, payload []byte) {
	t.Helper()
	if _, err := writesurface.AppendFinding(ctx, s, store.ActorHuman, repo, locator, payload); err != nil {
		t.Fatalf("AppendFinding: %v", err)
	}
}

func matterMarkdown(t *testing.T, cur Current, locator string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(GeneratedDir(cur.Root), locator, "matter.md"))
	if err != nil {
		t.Fatalf("read matter.md: %v", err)
	}
	return string(data)
}

func findingsSuffix(t *testing.T, cur Current, locator string) string {
	t.Helper()
	md := matterMarkdown(t, cur, locator)
	const marker = "\n## Findings\n\n"
	start := strings.Index(md, marker)
	if start < 0 {
		t.Fatalf("matter.md has no Findings section:\n%s", md)
	}
	return md[start:]
}

func findingTimestamp(seg store.ContentSegment) string {
	return seg.OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z")
}

func TestMatterFindingsRenderExactBoundaryPadding(t *testing.T) {
	current := time.Date(2026, 9, 17, 9, 8, 7, 0, time.UTC)
	s, _, cur := setupWithClock(t, func() time.Time { return current })
	locator := matter(t, s, cur.Repo.ID, "Finding boundaries")
	payloads := [][]byte{[]byte("zero"), []byte("one\n"), []byte("many\n\n\n")}
	for i, payload := range payloads {
		current = time.Date(2026, 9, 17, 9, 8, 7, (i+1)*int(time.Millisecond), time.UTC)
		appendFinding(t, s, cur.Repo.ID, locator, payload)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := s.ContentSegments(ctx, node.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n## Findings\n\n" +
		fmt.Sprintf("### %s\n\nzero\n\n", findingTimestamp(segments[0])) +
		fmt.Sprintf("### %s\n\none\n\n", findingTimestamp(segments[1])) +
		fmt.Sprintf("### %s\n\nmany\n\n\n", findingTimestamp(segments[2]))
	if got := findingsSuffix(t, cur, locator); got != want {
		t.Errorf("Findings suffix = %q, want %q", got, want)
	}
}

func TestMatterFindingPreservesMultilineMarkdownExactly(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Multiline finding")
	payload := []byte("Paragraph.\n\n#### Detail\n\n- first\n  - child\n\n```go\n# code, not a generated heading\nfmt.Println(\"x\")\n```\n")
	appendFinding(t, s, cur.Repo.ID, locator, payload)
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := s.ContentSegments(ctx, node.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n## Findings\n\n" + fmt.Sprintf("### %s\n\n", findingTimestamp(segments[0])) + string(payload)
	if got := findingsSuffix(t, cur, locator); got != want {
		t.Errorf("multiline Findings suffix = %q, want %q", got, want)
	}
}

func TestMatterEmptyFindingsHaveOnlyTimestampHeadings(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Empty finding")
	appendFinding(t, s, cur.Repo.ID, locator, nil)
	appendFinding(t, s, cur.Repo.ID, locator, []byte("next"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := s.ContentSegments(ctx, node.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n## Findings\n\n" +
		fmt.Sprintf("### %s\n\n", findingTimestamp(segments[0])) +
		fmt.Sprintf("### %s\n\nnext\n", findingTimestamp(segments[1]))
	if got := findingsSuffix(t, cur, locator); got != want {
		t.Errorf("empty Findings suffix = %q, want %q", got, want)
	}

	finalEmpty := matter(t, s, cur.Repo.ID, "Final empty finding")
	appendFinding(t, s, cur.Repo.ID, finalEmpty, []byte{})
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh final empty: %v", err)
	}
	node, err = writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, finalEmpty)
	if err != nil {
		t.Fatal(err)
	}
	segments, err = s.ContentSegments(ctx, node.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	want = "\n## Findings\n\n" + fmt.Sprintf("### %s\n\n", findingTimestamp(segments[0]))
	if got := findingsSuffix(t, cur, finalEmpty); got != want {
		t.Errorf("final empty Findings suffix = %q, want %q", got, want)
	}
}

func TestMatterFindingsPreserveAppendOrderForEqualTimestamps(t *testing.T) {
	pinned := time.Date(2026, 9, 17, 9, 8, 7, 6*int(time.Millisecond), time.UTC)
	s, _, cur := setupWithClock(t, func() time.Time { return pinned })
	locator := matter(t, s, cur.Repo.ID, "Equal timestamps")
	appendFinding(t, s, cur.Repo.ID, locator, []byte("first"))
	appendFinding(t, s, cur.Repo.ID, locator, []byte("second"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	node, err := writesurface.ResolveNode(ctx, s.View, cur.Repo.ID, locator)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := s.ContentSegments(ctx, node.ID, store.KindFindings)
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].OccurredAt != segments[1].OccurredAt {
		t.Fatalf("finding timestamps differ: %s and %s", segments[0].OccurredAt, segments[1].OccurredAt)
	}
	timestamp := findingTimestamp(segments[0])
	want := "\n## Findings\n\n" + "### " + timestamp + "\n\nfirst\n\n" + "### " + timestamp + "\n\nsecond\n"
	if got := findingsSuffix(t, cur, locator); got != want {
		t.Errorf("equal-timestamp Findings suffix = %q, want %q", got, want)
	}
}

func TestMatterBodyRenderingRemainsByteExact(t *testing.T) {
	for _, body := range [][]byte{[]byte("body"), []byte("body\n"), []byte("body\n\n")} {
		t.Run(fmt.Sprintf("trailing-%d", len(body)-len(bytesTrimRightLF(body))), func(t *testing.T) {
			s, _, cur := setup(t)
			locator := matter(t, s, cur.Repo.ID, "Body regression")
			writeOnce(t, s, cur.Repo.ID, locator, store.KindBody, body)
			if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			md := matterMarkdown(t, cur, locator)
			start := strings.Index(md, "\n## Body\n\n")
			if start < 0 {
				t.Fatalf("matter.md has no Body section:\n%s", md)
			}
			want := "\n## Body\n\n" + string(body) + "\n"
			if got := md[start:]; got != want {
				t.Errorf("Body suffix = %q, want %q", got, want)
			}
		})
	}
}

func bytesTrimRightLF(data []byte) []byte {
	for len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	return data
}

func TestMatterFindingsOnlyChangeMatterProjectionAndKeep0444(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Finding projection")
	appendFinding(t, s, cur.Repo.ID, locator, []byte("finding"))
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "matter.md" {
		t.Errorf("generated files = %v, want only matter.md", entries)
	}
	path := filepath.Join(dir, "matter.md")
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("matter.md mode = %o, want 0444", mode)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("matter.md mode after refresh = %o, want 0444", mode)
	}
}

// TestDepthPolicy_BriefStagesAndWorkplansRenderSeparateFiles is MODEL §3.1's
// second example: "a Matter with a Brief, Stages, and per-Stage Workplans
// renders as brief + roadmap + per-Stage workplan files."
func TestDepthPolicy_BriefStagesAndWorkplansRenderSeparateFiles(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "A Feature")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBrief, []byte("# Brief\n\nwhy this exists\n"))
	stageLocator := stage(t, s, cur.Repo.ID, locator, "First Stage")
	writeOnce(t, s, cur.Repo.ID, locator+"/"+stageLocator, store.KindWorkplan, []byte("# Workplan\n\nsteps go here\n"))

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	dir := filepath.Join(GeneratedDir(cur.Root), locator)
	for _, want := range []string{"matter.md", "brief.md", "roadmap.md", "workplan-" + stageLocator + ".md"} {
		if !fileExists(filepath.Join(dir, want)) {
			t.Errorf("expected generated file %s not found", want)
		}
	}

	brief, err := os.ReadFile(filepath.Join(dir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(brief) != "# Brief\n\nwhy this exists\n" {
		t.Errorf("brief.md = %q, want the stored Brief content verbatim", brief)
	}
}

// TestDepthPolicy_MatterGrainWorkplanRendersAsWorkplanFile covers the
// steps-only shape with store-resident prose: a Matter whose Workplan lives
// on the Matter itself (no Stages) renders it as `workplan.md`, verbatim,
// beside `matter.md`.
func TestDepthPolicy_MatterGrainWorkplanRendersAsWorkplanFile(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Steps-only Matter")
	writeOnce(t, s, cur.Repo.ID, locator, store.KindWorkplan, []byte("# Workplan\n\nintent, shape, seal condition\n"))

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	path := filepath.Join(GeneratedDir(cur.Root), locator, "workplan.md")
	wp, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the Matter-grain workplan did not render: %v", err)
	}
	if string(wp) != "# Workplan\n\nintent, shape, seal condition\n" {
		t.Errorf("workplan.md = %q, want the stored Workplan content verbatim", wp)
	}
}

// TestMatterSummaryListsInForceBlockedByEdges: matter.md carries the node's
// in-force blocked-by edges as locators, so orientation from the projection
// sees the ordering without querying the store.
func TestMatterSummaryListsInForceBlockedByEdges(t *testing.T) {
	s, _, cur := setup(t)
	blocker := matter(t, s, cur.Repo.ID, "The Blocker")
	blocked := matter(t, s, cur.Repo.ID, "The Blocked")
	if _, err := writesurface.DependAdd(ctx, s, store.ActorHuman, cur.Repo.ID, blocked, blocker); err != nil {
		t.Fatalf("DependAdd: %v", err)
	}

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(GeneratedDir(cur.Root), blocked, "matter.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "blocked-by: " + blocker + "\n"
	if !strings.Contains(string(md), want) {
		t.Errorf("matter.md for %s does not carry %q:\n%s", blocked, want, md)
	}

	blockerMd, err := os.ReadFile(filepath.Join(GeneratedDir(cur.Root), blocker, "matter.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blockerMd), "blocked-by:") {
		t.Errorf("matter.md for unblocked %s carries a blocked-by line:\n%s", blocker, blockerMd)
	}
}

// TestMatterSummaryCarriesSnapshotStamp is the orientation contract for
// wip-authored generated files: matter.md names when it was rendered and
// tells a reader that disagrees with `wip status` to run a named
// `wip plumbing refresh`.
func TestMatterSummaryCarriesSnapshotStamp(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(GeneratedDir(cur.Root), locator, "matter.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(md)
	if !strings.Contains(got, "rendered-at: ") {
		t.Errorf("matter.md missing rendered-at:\n%s", got)
	}
	want := "run `wip plumbing refresh " + locator + "` and re-read"
	if !strings.Contains(got, want) {
		t.Errorf("matter.md missing named refresh instruction %q:\n%s", want, got)
	}
	if !strings.Contains(got, "snapshot from the last `wip plumbing refresh`") {
		t.Errorf("matter.md missing snapshot notice:\n%s", got)
	}
}

func TestMatterSummaryRendersAllGateStatesInOrder(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Gate states")
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "approved", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "verified", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.CloseGate(ctx, s, store.ActorHuman, cur.Repo.ID, "approved", locator); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
		t.Fatal(err)
	}
	openMD := matterMarkdown(t, cur, locator)
	if !strings.Contains(openMD, "- reviewed-local (matter, own): open\n") ||
		!strings.Contains(openMD, "- verified (matter, own): open\n") {
		t.Errorf("open Gates section = %q", openMD)
	}
	if _, err := writesurface.CloseGate(ctx, s, store.ActorHuman, cur.Repo.ID, "reviewed-local", locator); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Finish(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.RepairGateExemption(ctx, s, cur.Repo.ID, "verified", locator); err != nil {
		t.Fatal(err)
	}
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "zeta", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
		t.Fatal(err)
	}

	md := matterMarkdown(t, cur, locator)
	start := strings.Index(md, "\n## Gates\n\n")
	if start < 0 {
		t.Fatalf("matter.md has no Gates section:\n%s", md)
	}
	end := strings.Index(md[start+1:], "\n## ")
	if end < 0 {
		end = len(md) - start - 1
	}
	section := md[start+1 : start+1+end]
	if !strings.Contains(section, "## Gates\n\n- approved (matter, own): closed by human at ") ||
		!strings.Contains(section, "- reviewed-local (matter, own): closed by human at ") ||
		!strings.Contains(section, "- verified (matter, own): exempt\n") ||
		!strings.Contains(section, "- zeta (matter, own): exempt\n") {
		t.Errorf("Gates section = %q", section)
	}
	if strings.Index(section, "approved") > strings.Index(section, "reviewed-local") ||
		strings.Index(section, "reviewed-local") > strings.Index(section, "verified") ||
		strings.Index(section, "verified") > strings.Index(section, "zeta") {
		t.Errorf("Gates section is not lexical: %q", section)
	}
}

func TestMatterSummaryRendersDismissalMetadataDistinctFromClose(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Dismissed gate")
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "reviewed-local", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Start(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	if _, err := writesurface.Finish(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	reason := "the designated verifier is unavailable during the incident"
	if _, err := writesurface.DismissGateWithEnvResult(ctx, s, store.ActorHuman, cur.Env(), "reviewed-local", locator, reason); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
		t.Fatal(err)
	}

	md := matterMarkdown(t, cur, locator)
	if !strings.Contains(md, "- reviewed-local (matter, own): dismissed by human at ") ||
		!strings.Contains(md, ": "+reason+"\n") {
		t.Errorf("dismissed gate rendering = %q, want actor, timestamp and immutable reason", md)
	}
	if strings.Contains(md, "reviewed-local (matter, own): closed by") {
		t.Errorf("dismissed gate was rendered as a normal close:\n%s", md)
	}
}

func TestMatterSummaryRendersEmptyGatesSection(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "No gates")
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatal(err)
	}
	md := matterMarkdown(t, cur, locator)
	if !strings.Contains(md, "\n## Gates\n\nNone.\n") {
		t.Errorf("matter.md missing empty Gates section:\n%s", md)
	}
}

func TestMatterSummaryPlacesExactGateSectionBeforeContent(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Gate placement")
	if err := writesurface.DeclareGate(ctx, s, cur.Repo.ID, "approved", store.ScaleMatter); err != nil {
		t.Fatal(err)
	}
	writeOnce(t, s, cur.Repo.ID, locator, store.KindBody, []byte("body"))
	appendFinding(t, s, cur.Repo.ID, locator, []byte("finding"))
	if _, err := writesurface.Start(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	_, err := writesurface.CloseGate(ctx, s, store.ActorHuman, cur.Repo.ID, "approved", locator)
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.MatterByLocator(ctx, cur.Repo.ID, locator)
	if err != nil {
		t.Fatalf("resolve Matter: %v", err)
	}
	events, err := s.EventsOfSubject(ctx, node.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("read gate close event: %v", err)
	}
	closedAt := events[len(events)-1].OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := writesurface.Finish(ctx, s, store.ActorHuman, cur.Repo.ID, locator); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(ctx, s, cur, store.ActorHuman, locator, NoPrecondition); err != nil {
		t.Fatal(err)
	}

	md := matterMarkdown(t, cur, locator)
	gates := strings.Index(md, "\n## Gates\n\n")
	body := strings.Index(md, "\n## Body\n\n")
	findings := strings.Index(md, "\n## Findings\n\n")
	if gates < 0 || body < 0 || findings < 0 || gates >= body || body >= findings {
		t.Fatalf("section order = gates %d, body %d, findings %d:\n%s", gates, body, findings, md)
	}
	section := md[gates:body]
	if !strings.Contains(section, "- approved (matter, own): closed by human at "+closedAt) {
		t.Errorf("Gates section = %q, want exact closure metadata", section)
	}
	if strings.Contains(md, "## Gates closed") {
		t.Errorf("matter.md uses obsolete Gates closed heading:\n%s", md)
	}
}

// TestRoadmapCarriesSnapshotNotice stamps the same recovery line on
// roadmap.md, which is wip-authored. Brief and Workplan files stay
// verbatim store content (see TestDepthPolicy_MatterGrainWorkplanRendersAsWorkplanFile).
func TestRoadmapCarriesSnapshotNotice(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "A Feature")
	stage(t, s, cur.Repo.ID, locator, "First Stage")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(GeneratedDir(cur.Root), locator, "roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(md)
	want := "run `wip plumbing refresh " + locator + "` and re-read"
	if !strings.Contains(got, want) {
		t.Errorf("roadmap.md missing named refresh instruction %q:\n%s", want, got)
	}
}

// TestWrite_GeneratedFilesAre0444 is step-04's Done: every file under
// `.wip/generated/` is 0444 immediately after render, and a direct write
// attempt against one fails at the OS level.
func TestWrite_GeneratedFilesAre0444(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	path := filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("mode of %s = %o, want 0444", path, mode)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err == nil {
		t.Errorf("a direct write against %s succeeded; want an OS-level permission refusal", path)
	} else if !os.IsPermission(err) {
		t.Errorf("write against %s failed with %v, want a permission error", path, err)
	}
}

// TestWrite_RerenderReopensAndRestores0444 confirms this package's own
// render pass — the one legitimate writer — can still regenerate a file it
// previously wrote 0444, restoring 0444 afterward.
func TestWrite_RerenderReopensAndRestores0444(t *testing.T) {
	s, _, cur := setup(t)
	locator := matter(t, s, cur.Repo.ID, "Fix the thing")

	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if _, err := Refresh(ctx, s, cur, store.ActorHuman, NoPrecondition); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	path := filepath.Join(GeneratedDir(cur.Root), locator, "matter.md")
	if mode := fileMode(t, path); mode != 0o444 {
		t.Errorf("mode of %s after re-render = %o, want 0444", path, mode)
	}
}
