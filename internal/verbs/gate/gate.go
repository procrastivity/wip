// Package gate implements `wip gate declare`, `wip gate repair`, and `wip gate
// close`. Per
// `vocabulary` step-02, there is no bespoke `review` verb — every gate
// close, including a local review, goes through this command.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/guards/trackedwip"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/writesurface"
)

// cursorEndedJSON is the `cursorEnded` field a JSON `gate close` payload
// gains when the close ended the cursor's own work — including via
// descendant sealing at Matter scale, since readsurface.EndedCursor
// evaluates the cursor's *target* state, not whether it was this close's own
// subject. Mirrors lifecycle's own cursorEndedJSON; the two packages share
// no JSON types by convention.
type cursorEndedJSON struct {
	Reason    string `json:"reason"`
	Target    string `json:"target,omitempty"`
	Suggested string `json:"suggested,omitempty"`
}

// Command constructs the `wip gate` parent command and its verbs.
func Command(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	coordinator := tracker.NewAlignmentCoordinator(providers)
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "declare, repair, and close gates (MODEL §2.3)",
	}
	cmd.AddCommand(declareCommand(streams), repairCommand(streams), closeCommand(streams, coordinator))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func openRepo(cmd *cobra.Command) (*store.Store, store.Repo, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, store.Repo{}, err
	}
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, store.Repo{}, err
	}
	repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
	if err != nil {
		_ = s.Close()
		return nil, store.Repo{}, err
	}
	return s, repo, nil
}

func repairCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair <gate-name> <locator>",
		Short: "repair a missed prospective exemption without closing the gate",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			n, err := writesurface.RepairGateExemption(cmd.Context(), s, repo.ID, args[0], args[1])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Gate  string `json:"gate"`
					Node  string `json:"node"`
					Scale string `json:"scale"`
				}{Gate: args[0], Node: n.ID, Scale: string(n.Kind)})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "repaired %s exemption on %s\n", args[0], args[1])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func declareCommand(streams *iostreams.Streams) *cobra.Command {
	var scale string
	cmd := &cobra.Command{
		Use:   "declare <gate-name>",
		Short: "declare a gate at a scale — project config, not a domain event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			if err := writesurface.DeclareGate(cmd.Context(), s, repo.ID, args[0], store.Scale(scale)); err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Gate  string `json:"gate"`
					Scale string `json:"scale"`
				}{Gate: args[0], Scale: scale})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "declared gate %s at %s scale\n", args[0], scale)
			return err
		},
	}
	cmd.Flags().StringVar(&scale, "scale", "", "matter, stage or step")
	_ = cmd.MarkFlagRequired("scale")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func closeCommand(streams *iostreams.Streams, coordinator *tracker.AlignmentCoordinator) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <gate-name> <locator>",
		Short: "close a declared gate against a node",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir)
			if err != nil {
				return err
			}

			transition, err := writesurface.CloseGateWithEnvResult(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0], args[1])
			if err != nil {
				return err
			}
			n := transition.Node
			var report tracker.AlignmentReport
			if transition.BecameSealed {
				report = coordinator.Check(cmd.Context(), s.View, n.ID)
				if err := render.Exit(cmd.Context(), s, cur, n, trackedwip.RenderPrecondition); err != nil {
					return err
				}
			}
			var alignment *tracker.AlignmentReport
			if report.Visible() {
				alignment = &report
			}

			// Best-effort hand-off, strictly post-commit and read-only
			// (D67): a Matter-scale close can seal Done descendants,
			// including the cursor's own target, even when the cursor never
			// pointed at args[1] — readsurface.Handoff evaluates the
			// cursor's target state fresh, not this close's own subject.
			line, ce, ok := gateHandoff(cmd.Context(), s.View, cur.Clone.ID, cur.Worktree.ID)

			if flags.JSON {
				b, err := json.Marshal(struct {
					Gate        string                   `json:"gate"`
					Node        string                   `json:"node"`
					Scale       string                   `json:"scale"`
					CursorEnded *cursorEndedJSON         `json:"cursorEnded,omitempty"`
					Alignment   *tracker.AlignmentReport `json:"alignment,omitempty"`
				}{Gate: args[0], Node: n.ID, Scale: string(n.Kind), CursorEnded: ce, Alignment: alignment})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if _, err := fmt.Fprintf(streams.Out, "closed %s on %s\n", args[0], args[1]); err != nil {
				return err
			}
			for _, alignmentLine := range report.Lines() {
				if _, err := fmt.Fprintln(streams.Out, alignmentLine); err != nil {
					return err
				}
			}
			if ok {
				_, err = fmt.Fprintln(streams.Out, line)
			}
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// gateHandoff wraps readsurface.Handoff into this package's own
// cursorEndedJSON shape, the same conversion lifecycle's own handoff does.
func gateHandoff(ctx context.Context, v store.View, clone, worktree string) (line string, j *cursorEndedJSON, ok bool) {
	ended, l, targetAddr, ok := readsurface.Handoff(ctx, v, clone, worktree)
	if !ok {
		return "", nil, false
	}
	c := cursorEndedJSON{Reason: ended.Reason, Target: targetAddr}
	if ended.Suggested != nil {
		c.Suggested = ended.Suggested.Locator
	}
	return l, &c, true
}
