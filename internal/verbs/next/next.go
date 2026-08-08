// Package next implements `wip next` and `wip next --set <locator>` —
// read-surface's fusion of the personal cursor with the durable unblocked
// frontier (MODEL §1), producing vocabulary's five drafted outputs plus the
// dangling-cursor case D67 makes first-class.
package next

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
)

// Command constructs `wip next`.
func Command(streams *iostreams.Streams) *cobra.Command {
	var set string
	cmd := &cobra.Command{
		Use:   "next",
		Short: "the cursor fused with the unblocked frontier — what to work on next",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			s, err := tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			if set != "" {
				node, err := readsurface.SetCursor(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir, set)
				if err != nil {
					return err
				}
				return renderSet(cmd.Context(), streams, s.View, flags.JSON, node)
			}

			actor := store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole)

			// An open Run with a parallel frontier takes the display (the
			// ratified draft in parallelism-decisions.md): the full Ready
			// set, the cap, the available slots — never a selection, which
			// is the scheduler's alone. With one Ready node or no open Run,
			// the existing outputs stand unchanged.
			if handled, err := renderRunFrontier(cmd.Context(), streams, s, actor, dir, flags.JSON); handled || err != nil {
				return err
			}

			view, err := readsurface.Next(cmd.Context(), s, actor, dir)
			if err != nil {
				return err
			}
			if flags.JSON {
				return renderJSON(cmd.Context(), streams, s.View, view)
			}
			return renderHuman(cmd.Context(), streams, s.View, view)
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "move the cursor to <locator>, emitting cursor.moved")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func renderSet(ctx context.Context, streams *iostreams.Streams, v store.View, jsonMode bool, node store.Node) error {
	address, _, err := readsurface.Address(ctx, v, node)
	if err != nil {
		return err
	}
	if jsonMode {
		b, err := json.Marshal(struct {
			Node    string `json:"node"`
			Address string `json:"address"`
		}{Node: node.ID, Address: address})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}
	_, err = fmt.Fprintf(streams.Out, "cursor set to %s\n", address)
	return err
}

// ---------------------------------------------------------------------------
// Human rendering — vocabulary's five drafted outputs, plus the dangling case.
// ---------------------------------------------------------------------------

func renderHuman(ctx context.Context, streams *iostreams.Streams, v store.View, view readsurface.View) error {
	switch view.Kind {
	case readsurface.BareMatter:
		_, err := fmt.Fprintf(streams.Out, "%-34s %s · %s\n  no plan — work it directly\n",
			view.Address, view.Node.Kind, view.Node.Lifecycle)
		return err

	case readsurface.Positioned:
		if _, err := fmt.Fprintf(streams.Out, "%-34s %s · %s\n", view.Address, view.Node.Kind, view.Node.Lifecycle); err != nil {
			return err
		}
		if view.Stage != nil {
			if _, err := fmt.Fprintf(streams.Out, "  Stage: %s (%d of %d)\n", view.Stage.StageLocator, view.Stage.Index, view.Stage.Total); err != nil {
				return err
			}
		}
		bb, err := blockedBySummary(ctx, v, view.Unmet, view.Met)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "  blocked-by: %s\n", bb)
		return err

	case readsurface.NothingUnblocked:
		if _, err := fmt.Fprintln(streams.Out, "nothing unblocked"); err != nil {
			return err
		}
		for _, b := range view.Blocked {
			addr, _, err := readsurface.Address(ctx, v, b.Node)
			if err != nil {
				return err
			}
			names, err := addresses(ctx, v, b.Blockers)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(streams.Out, "  %-18s blocked-by: %s\n", addr, joinComma(names)); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(streams.Out, "run `wip status` for the full dependency picture")
		return err

	case readsurface.NoCursor:
		if _, err := fmt.Fprintf(streams.Out, "no cursor set for this clone — %d unblocked:\n", len(view.Candidates)); err != nil {
			return err
		}
		if err := printCandidates(ctx, streams, v, view.Candidates); err != nil {
			return err
		}
		_, err := fmt.Fprintln(streams.Out, "pick one: wip next --set <locator>")
		return err

	case readsurface.InProgressNoCursor:
		if _, err := fmt.Fprintf(streams.Out, "no cursor set for this clone — nothing planned, %d in progress:\n", len(view.InProgress)); err != nil {
			return err
		}
		if err := printCandidates(ctx, streams, v, view.InProgress); err != nil {
			return err
		}
		_, err := fmt.Fprintln(streams.Out, "pick one: wip next --set <locator>")
		return err

	case readsurface.EverythingSealed:
		if _, err := fmt.Fprintln(streams.Out, "nothing in progress or planned — every Matter sealed"); err != nil {
			return err
		}
		if view.BacklogCount > 0 {
			_, err := fmt.Fprintf(streams.Out, "Backlog holds %d unprocessed entries: wip backlog list\n", view.BacklogCount)
			return err
		}
		return nil

	case readsurface.Dangling:
		reason := view.DanglingReason
		target := "a node"
		if view.Node.ID != "" {
			addr, _, err := readsurface.Address(ctx, v, view.Node)
			if err != nil {
				return err
			}
			target = addr
		}
		if _, err := fmt.Fprintf(streams.Out, "cursor points at %s, which is %s — pick a new one\n", target, reason); err != nil {
			return err
		}
		if len(view.Candidates) == 0 {
			_, err := fmt.Fprintln(streams.Out, "  nothing unblocked")
			return err
		}
		if _, err := fmt.Fprintf(streams.Out, "%d unblocked:\n", len(view.Candidates)); err != nil {
			return err
		}
		if err := printCandidates(ctx, streams, v, view.Candidates); err != nil {
			return err
		}
		_, err := fmt.Fprintln(streams.Out, "pick one: wip next --set <locator>")
		return err

	default:
		return fmt.Errorf("next: unhandled view kind %d", view.Kind)
	}
}

func printCandidates(ctx context.Context, streams *iostreams.Streams, v store.View, nodes []store.Node) error {
	for _, n := range nodes {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(streams.Out, "  %-22s %s · %s\n", addr, n.Kind, n.Lifecycle); err != nil {
			return err
		}
	}
	return nil
}

func blockedBySummary(ctx context.Context, v store.View, unmet, met []store.Node) (string, error) {
	if len(unmet) > 0 {
		names, err := addresses(ctx, v, unmet)
		if err != nil {
			return "", err
		}
		return joinComma(names), nil
	}
	if len(met) == 0 {
		return "none", nil
	}
	names, err := addresses(ctx, v, met)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("none (%s locally complete)", joinComma(names)), nil
}

func addresses(ctx context.Context, v store.View, nodes []store.Node) ([]string, error) {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		addr, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, nil
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

type nodeJSON struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Kind      string `json:"kind"`
	Lifecycle string `json:"lifecycle"`
}

func toNodeJSON(ctx context.Context, v store.View, n store.Node) (nodeJSON, error) {
	addr, _, err := readsurface.Address(ctx, v, n)
	if err != nil {
		return nodeJSON{}, err
	}
	return nodeJSON{ID: n.ID, Address: addr, Kind: string(n.Kind), Lifecycle: string(n.Lifecycle)}, nil
}

func toNodeJSONs(ctx context.Context, v store.View, nodes []store.Node) ([]nodeJSON, error) {
	out := make([]nodeJSON, 0, len(nodes))
	for _, n := range nodes {
		j, err := toNodeJSON(ctx, v, n)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func renderJSON(ctx context.Context, streams *iostreams.Streams, v store.View, view readsurface.View) error {
	kind := map[readsurface.Kind]string{
		readsurface.BareMatter:         "bare-matter",
		readsurface.Positioned:         "positioned",
		readsurface.NothingUnblocked:   "nothing-unblocked",
		readsurface.NoCursor:           "no-cursor",
		readsurface.EverythingSealed:   "everything-sealed",
		readsurface.InProgressNoCursor: "in-progress-no-cursor",
		readsurface.Dangling:           "dangling",
	}[view.Kind]

	payload := struct {
		Kind    string     `json:"kind"`
		Node    *nodeJSON  `json:"node,omitempty"`
		Stage   *stageJSON `json:"stage,omitempty"`
		Unmet   []nodeJSON `json:"unmet,omitempty"`
		Met     []nodeJSON `json:"met,omitempty"`
		Reason  string     `json:"reason,omitempty"`
		Cand    []nodeJSON `json:"candidates,omitempty"`
		Blocked []blockedJ `json:"blocked,omitempty"`
		Backlog int        `json:"backlogUnprocessed,omitempty"`
		InProg  []nodeJSON `json:"inProgress,omitempty"`
	}{Kind: kind, Reason: view.DanglingReason, Backlog: view.BacklogCount}

	if view.Kind == readsurface.BareMatter || view.Kind == readsurface.Positioned ||
		(view.Kind == readsurface.Dangling && view.Node.ID != "") {
		n, err := toNodeJSON(ctx, v, view.Node)
		if err != nil {
			return err
		}
		payload.Node = &n
	}
	if view.Stage != nil {
		payload.Stage = &stageJSON{Locator: view.Stage.StageLocator, Index: view.Stage.Index, Total: view.Stage.Total}
	}
	if view.Unmet != nil {
		u, err := toNodeJSONs(ctx, v, view.Unmet)
		if err != nil {
			return err
		}
		payload.Unmet = u
	}
	if view.Met != nil {
		m, err := toNodeJSONs(ctx, v, view.Met)
		if err != nil {
			return err
		}
		payload.Met = m
	}
	if view.Candidates != nil {
		c, err := toNodeJSONs(ctx, v, view.Candidates)
		if err != nil {
			return err
		}
		payload.Cand = c
	}
	if view.InProgress != nil {
		p, err := toNodeJSONs(ctx, v, view.InProgress)
		if err != nil {
			return err
		}
		payload.InProg = p
	}
	for _, b := range view.Blocked {
		n, err := toNodeJSON(ctx, v, b.Node)
		if err != nil {
			return err
		}
		blockers, err := toNodeJSONs(ctx, v, b.Blockers)
		if err != nil {
			return err
		}
		payload.Blocked = append(payload.Blocked, blockedJ{Node: n, BlockedBy: blockers})
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}

type stageJSON struct {
	Locator string `json:"locator"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
}

type blockedJ struct {
	Node      nodeJSON   `json:"node"`
	BlockedBy []nodeJSON `json:"blockedBy"`
}
