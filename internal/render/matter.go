package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// snapshotNotice is the orientation line stamped on wip-authored generated
// files (matter.md, roadmap.md). Brief and Workplan files stay verbatim
// store content — a banner there would be a silent rewrite of prose.
func snapshotNotice(locator string) string {
	return fmt.Sprintf("This file is a snapshot from the last `wip plumbing refresh`, not live state. If it disagrees with `wip plumbing status`, run `wip plumbing refresh %s` and re-read.\n", locator)
}

// renderMatterTree implements step-03's rendered-depth policy: filenames and
// depth are render policy, never storage (MODEL §3.1), so this is a policy
// choice this Matter is free to make and revise without any migration.
//
// Every Matter always gets a summary file (`matter.md`) carrying its
// lifecycle, gate state, in-force blocked-by edges, body and findings —
// content that has to render somewhere regardless of earned shape. A Matter
// that has additionally earned a Brief and/or a Matter-grain Workplan also
// gets `brief.md` / `workplan.md`; one with Stages/Steps gets `roadmap.md`,
// one `workplan-<stage-locator>.md` per Stage with Workplan content, and one
// `workplan-<step-locator>.md` per Step with Workplan content.
func renderMatterTree(ctx context.Context, s *store.Store, root string, matter store.Node) error {
	dir := filepath.Join(GeneratedDir(root), matter.Locator)

	children, err := s.Children(ctx, matter.ID)
	if err != nil {
		return err
	}
	nodes, err := s.MatterNodes(ctx, matter.ID)
	if err != nil {
		return err
	}
	expectedStages := make(map[string]struct{})
	expectedSteps := make(map[string]struct{})

	if err := renderMatterSummary(ctx, s, dir, matter); err != nil {
		return err
	}

	if brief, has, err := readContentIfAny(ctx, s, matter.ID, store.KindBrief); err != nil {
		return err
	} else if has {
		if err := writeGenerated(filepath.Join(dir, "brief.md"), brief); err != nil {
			return err
		}
	}

	if wp, has, err := readContentIfAny(ctx, s, matter.ID, store.KindWorkplan); err != nil {
		return err
	} else if has {
		if err := writeGenerated(filepath.Join(dir, "workplan.md"), wp); err != nil {
			return err
		}
	}

	if len(children) > 0 {
		if err := renderRoadmap(ctx, s, dir, matter, children); err != nil {
			return err
		}
	}

	for _, node := range nodes {
		if node.Kind != store.ScaleStage && node.Kind != store.ScaleStep {
			continue
		}
		wp, has, err := readContentIfAny(ctx, s, node.ID, store.KindWorkplan)
		if err != nil {
			return err
		}
		if !has {
			continue
		}
		basename := fmt.Sprintf("workplan-%s.md", node.Locator)
		path := filepath.Join(dir, basename)
		if err := writeGenerated(path, wp); err != nil {
			return err
		}
		if node.Kind == store.ScaleStage {
			expectedStages[basename] = struct{}{}
		} else {
			expectedSteps[basename] = struct{}{}
		}
	}

	return pruneStepWorkplans(dir, expectedStages, expectedSteps)
}

var canonicalStepWorkplan = regexp.MustCompile(`^workplan-step-(0[1-9]|[1-9][0-9]+)\.md$`)

// pruneStepWorkplans removes only regular files in the renderer-owned Step
// namespace. Stage paths share the namespace when a Stage locator has the
// canonical Step shape, so expectedStages is the collision carve-out.
func pruneStepWorkplans(dir string, expectedStages, expectedSteps map[string]struct{}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("render: scanning generated Matter directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !canonicalStepWorkplan.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("render: inspecting generated path %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if _, ok := expectedStages[entry.Name()]; ok {
			continue
		}
		if _, ok := expectedSteps[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("render: removing stale generated path %s: %w", path, err)
		}
	}
	return nil
}

func readContentIfAny(ctx context.Context, s *store.Store, node string, kind store.ContentKind) ([]byte, bool, error) {
	segs, err := s.ContentSegments(ctx, node, kind)
	if err != nil {
		return nil, false, err
	}
	if len(segs) == 0 {
		return nil, false, nil
	}
	data, err := s.Content(ctx, node, kind)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func renderMatterSummary(ctx context.Context, s *store.Store, dir string, matter store.Node) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", matter.Title)
	fmt.Fprintf(&b, "locator: %s\n", matter.Locator)
	fmt.Fprintf(&b, "lifecycle: %s\n", matter.Lifecycle)
	fmt.Fprintf(&b, "rendered-at: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"))

	edges, err := s.BlockedBy(ctx, matter.ID)
	if err != nil {
		return err
	}
	if len(edges) > 0 {
		blockers := make([]string, 0, len(edges))
		for _, e := range edges {
			blocker, err := s.Node(ctx, e.Blocker)
			if err != nil {
				return err
			}
			blockers = append(blockers, blocker.Locator)
		}
		fmt.Fprintf(&b, "blocked-by: %s\n", strings.Join(blockers, ", "))
	}

	b.WriteByte('\n')
	b.WriteString(snapshotNotice(matter.Locator))

	requirements, err := s.EffectiveGateRequirements(ctx, matter)
	if err != nil {
		return err
	}
	b.WriteString("\n## Gates\n\n")
	if len(requirements) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, requirement := range requirements {
			state := string(requirement.State)
			switch requirement.State {
			case store.GateRequirementClosed:
				state = fmt.Sprintf("closed by %s at %s", requirement.ClosedBy,
					requirement.ClosedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
			case store.GateRequirementDismissed:
				state = fmt.Sprintf("dismissed by %s at %s: %s", requirement.DismissedBy,
					requirement.DismissedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
					requirement.DismissalReason)
			}
			fmt.Fprintf(&b, "- %s (%s, %s): %s\n", requirement.Gate, requirement.Scale,
				requirement.Relationship, state)
		}
	}

	if body, has, err := readContentIfAny(ctx, s, matter.ID, store.KindBody); err != nil {
		return err
	} else if has {
		b.WriteString("\n## Body\n\n")
		b.Write(body)
		b.WriteString("\n")
	}

	if err := renderFindingEntries(ctx, s, matter.ID, &b); err != nil {
		return err
	}

	return writeGenerated(filepath.Join(dir, "matter.md"), []byte(b.String()))
}

func renderFindingEntries(ctx context.Context, s *store.Store, node string, b *strings.Builder) error {
	segments, err := s.ContentSegments(ctx, node, store.KindFindings)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return nil
	}
	b.WriteString("\n## Findings\n\n")
	for i, segment := range segments {
		fmt.Fprintf(b, "### %s\n\n", segment.OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z"))
		payload, err := s.SegmentBytes(ctx, segment.ID)
		if err != nil {
			return err
		}
		b.Write(payload)
		if len(payload) == 0 {
			continue
		}
		trailing := 0
		for trailing < len(payload) && payload[len(payload)-1-trailing] == '\n' {
			trailing++
		}
		want := 1
		if i < len(segments)-1 {
			want = 2
		}
		for trailing < want {
			b.WriteByte('\n')
			trailing++
		}
	}
	return nil
}

func renderRoadmap(ctx context.Context, s *store.Store, dir string, matter store.Node, children []store.Node) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Roadmap — %s\n\n", matter.Title)
	b.WriteString(snapshotNotice(matter.Locator))
	b.WriteByte('\n')
	for _, child := range children {
		if err := renderRoadmapEntry(ctx, s, &b, child, 0); err != nil {
			return err
		}
	}
	return writeGenerated(filepath.Join(dir, "roadmap.md"), []byte(b.String()))
}

func renderRoadmapEntry(ctx context.Context, s *store.Store, b *strings.Builder, node store.Node, depth int) error {
	fmt.Fprintf(b, "%s- [%s] %s — %s (%s)\n", strings.Repeat("  ", depth), node.Lifecycle, node.Locator, node.Title, node.Kind)
	if node.Kind != store.ScaleStage {
		return nil
	}
	children, err := s.Children(ctx, node.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := renderRoadmapEntry(ctx, s, b, child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
