package render

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// renderMatterTree implements step-03's rendered-depth policy: filenames and
// depth are render policy, never storage (MODEL §3.1), so this is a policy
// choice this Matter is free to make and revise without any migration.
//
// Every Matter always gets a summary file (`matter.md`) carrying its
// lifecycle, gate state, in-force blocked-by edges, body and findings —
// content that has to render somewhere regardless of earned shape. A Matter
// that has additionally earned a Brief and/or a Matter-grain Workplan also
// gets `brief.md` / `workplan.md`; one with Stages/Steps gets `roadmap.md` and
// one `workplan-<stage-locator>.md` per Stage that carries Workplan content
// — MODEL §3.1's own examples: "a bugfix Matter with no Stages renders as
// one matter.md; a Matter with a Brief, Stages, and per-Stage Workplans
// renders as brief + roadmap + per-Stage workplan files."
func renderMatterTree(ctx context.Context, s *store.Store, root string, matter store.Node) error {
	dir := filepath.Join(GeneratedDir(root), matter.Locator)

	children, err := s.Children(ctx, matter.ID)
	if err != nil {
		return err
	}

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

	for _, child := range children {
		if child.Kind != store.ScaleStage {
			continue
		}
		wp, has, err := readContentIfAny(ctx, s, child.ID, store.KindWorkplan)
		if err != nil {
			return err
		}
		if !has {
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("workplan-%s.md", child.Locator))
		if err := writeGenerated(path, wp); err != nil {
			return err
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

	gates, err := s.ClosedGates(ctx, matter.ID)
	if err != nil {
		return err
	}
	if len(gates) > 0 {
		b.WriteString("\n## Gates closed\n")
		for _, g := range gates {
			fmt.Fprintf(&b, "- %s (%s) at %s\n", g.Gate, g.Scale, g.ClosedAt.Format("2006-01-02T15:04:05Z"))
		}
	}

	if body, has, err := readContentIfAny(ctx, s, matter.ID, store.KindBody); err != nil {
		return err
	} else if has {
		b.WriteString("\n## Body\n\n")
		b.Write(body)
		b.WriteString("\n")
	}

	if findings, has, err := readContentIfAny(ctx, s, matter.ID, store.KindFindings); err != nil {
		return err
	} else if has {
		b.WriteString("\n## Findings\n\n")
		b.Write(findings)
		b.WriteString("\n")
	}

	return writeGenerated(filepath.Join(dir, "matter.md"), []byte(b.String()))
}

func renderRoadmap(ctx context.Context, s *store.Store, dir string, matter store.Node, children []store.Node) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Roadmap — %s\n\n", matter.Title)
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
