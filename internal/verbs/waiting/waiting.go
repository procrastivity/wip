// Package waiting renders the shared human waiting summary used by porcelain
// status and idle next output.
package waiting

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
)

const maxCausesPerMatter = 3

// Lines renders Matter-grouped waiting causes without a section heading.
func Lines(ctx context.Context, v store.View, groups []readsurface.WaitingMatter) ([]string, error) {
	var out []string
	for _, group := range groups {
		matterAddress, _, err := readsurface.Address(ctx, v, group.Matter)
		if err != nil {
			return nil, err
		}
		out = append(out, "  "+matterAddress)
		var causes []string
		for _, gate := range group.Gates {
			subject := ""
			if gate.Subject.ID != group.Matter.ID {
				subjectLabel, err := nodeLabel(ctx, v, group.Matter, gate.Subject)
				if err != nil {
					return nil, err
				}
				subject = " on " + subjectLabel
			}
			causes = append(causes, fmt.Sprintf("gate %s%s · owner %s · %s", gate.Gate, subject, gate.Owner, gate.State))
		}
		for _, dependency := range group.Dependencies {
			blockerLabel, err := nodeLabel(ctx, v, group.Matter, dependency.Blocker)
			if err != nil {
				return nil, err
			}
			for _, node := range dependency.Held {
				if node.ID == group.Matter.ID {
					causes = append(causes, "blocked by "+blockerLabel)
					continue
				}
				heldLabel, err := nodeLabel(ctx, v, group.Matter, node)
				if err != nil {
					return nil, err
				}
				causes = append(causes, fmt.Sprintf("%s waits for %s", heldLabel, blockerLabel))
			}
		}
		shown := len(causes)
		if shown > maxCausesPerMatter {
			shown = maxCausesPerMatter
		}
		for _, cause := range causes[:shown] {
			out = append(out, "    "+cause)
		}
		if remaining := len(causes) - shown; remaining > 0 {
			out = append(out, fmt.Sprintf("    … %d more — wip status --full", remaining))
		}
	}
	return out, nil
}

func nodeLabel(ctx context.Context, v store.View, groupMatter, node store.Node) (string, error) {
	if node.Matter == groupMatter.ID {
		if node.ID == groupMatter.ID {
			return groupMatter.Locator, nil
		}
		return quotedNodeTitle(node), nil
	}
	matter, err := v.Node(ctx, node.Matter)
	if err != nil {
		return "", err
	}
	if node.ID == matter.ID {
		return matter.Locator, nil
	}
	return matter.Locator + " / " + quotedNodeTitle(node), nil
}

func quotedNodeTitle(node store.Node) string {
	if node.Title == "" {
		return node.Locator
	}
	return "“" + node.Title + "”"
}
