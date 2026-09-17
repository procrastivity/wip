// Package gate implements `wip plumbing gate list`, `wip plumbing gate status`,
// `wip plumbing gate declare`, `wip plumbing gate repair`, and `wip plumbing gate
// close`. Per
// `vocabulary` step-02, there is no bespoke `review` verb — every gate
// close, including a local review, goes through this command.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/procrastivity/wip/internal/wiperr"
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

// Command constructs the `wip plumbing gate` parent command and its verbs.
func Command(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	coordinator := tracker.NewAlignmentCoordinator(providers)
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "declare, repair, and close gates (MODEL §2.3)",
	}
	cmd.AddCommand(listCommand(streams), statusCommand(streams), declareCommand(streams), repairCommand(streams), closeCommand(streams, coordinator))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

const gateTimestampLayout = "2006-01-02T15:04:05.000Z"

type gateListJSON struct {
	Repo  string          `json:"repo"`
	Gates []gateEntryJSON `json:"gates"`
}

type gateEntryJSON struct {
	Name  string `json:"name"`
	Scale string `json:"scale"`
	Owner string `json:"owner"`
}

type gateStatusJSON struct {
	Node            gateNodeJSON          `json:"node"`
	LocallyComplete bool                  `json:"locallyComplete"`
	Sealed          bool                  `json:"sealed"`
	Requirements    []gateRequirementJSON `json:"requirements"`
}

type gateNodeJSON struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Kind      string `json:"kind"`
	Lifecycle string `json:"lifecycle"`
}

type gateRequirementJSON struct {
	Name         string          `json:"name"`
	Scale        string          `json:"scale"`
	Owner        string          `json:"owner"`
	Relationship string          `json:"relationship"`
	Subject      gateSubjectJSON `json:"subject"`
	State        string          `json:"state"`
	ClosedBy     string          `json:"closedBy,omitempty"`
	ClosedAt     string          `json:"closedAt,omitempty"`
}

type gateSubjectJSON struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Kind    string `json:"kind"`
}

func listCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list this repo's gate declarations - read-only, emits no event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			declarations, err := s.GateDeclarations(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}
			gates := make([]gateEntryJSON, 0, len(declarations))
			for _, declaration := range declarations {
				gates = append(gates, gateEntryJSON{
					Name:  declaration.Gate,
					Scale: string(declaration.Scale),
					Owner: gateOwner(declaration.Gate),
				})
			}
			if flags.JSON {
				return writeJSON(streams.Out, gateListJSON{Repo: repo.ID, Gates: gates})
			}
			if len(gates) == 0 {
				_, err = fmt.Fprintln(streams.Out, "no gates declared")
				return err
			}
			for _, gate := range gates {
				if _, err := fmt.Fprintf(streams.Out, "%-16s %-7s %s\n", gate.Name, gate.Scale, gate.Owner); err != nil {
					return err
				}
			}
			return nil
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func statusCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <locator>",
		Short: "show one live node's effective gate requirements - read-only, emits no event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			n, err := resolveStatusNode(cmd.Context(), s.View, repo.ID, args[0])
			if err != nil {
				return err
			}
			completion, err := s.NodeCompletion(cmd.Context(), n)
			if err != nil {
				return err
			}
			address, _, err := readsurface.Address(cmd.Context(), s.View, n)
			if err != nil {
				return err
			}
			if flags.JSON {
				payload, err := gateStatusPayload(cmd.Context(), s.View, n, address, completion)
				if err != nil {
					return err
				}
				return writeJSON(streams.Out, payload)
			}
			return writeGateStatusHuman(cmd.Context(), streams.Out, s.View, n, address, completion)
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func gateOwner(gate string) string {
	if owner, ok := store.GateOwner(gate); ok {
		return string(owner.Actor())
	}
	return string(store.ActorHuman)
}

func resolveStatusNode(ctx context.Context, v store.View, repo, locator string) (store.Node, error) {
	n, err := writesurface.ResolveNode(ctx, v, repo, locator)
	if err != nil || n.Repo != repo {
		if store.IsIdentityShaped(locator) {
			return store.Node{}, wiperr.New("validation.unknown-locator", fmt.Sprintf("no node %s in this repo", locator))
		}
		return store.Node{}, err
	}
	return n, nil
}

func gateStatusPayload(ctx context.Context, v store.View, n store.Node, address string, completion store.NodeCompletion) (gateStatusJSON, error) {
	requirements := make([]gateRequirementJSON, 0, len(completion.Requirements))
	for _, requirement := range completion.Requirements {
		subjectAddress, _, err := readsurface.Address(ctx, v, requirement.Subject)
		if err != nil {
			return gateStatusJSON{}, err
		}
		out := gateRequirementJSON{
			Name:         requirement.Gate,
			Scale:        string(requirement.Scale),
			Owner:        string(requirement.Owner),
			Relationship: string(requirement.Relationship),
			Subject: gateSubjectJSON{
				ID:      requirement.Subject.ID,
				Address: subjectAddress,
				Kind:    string(requirement.Subject.Kind),
			},
			State: string(requirement.State),
		}
		if requirement.State == store.GateRequirementClosed {
			out.ClosedBy = string(requirement.ClosedBy)
			if requirement.ClosedAt != nil {
				out.ClosedAt = requirement.ClosedAt.UTC().Format(gateTimestampLayout)
			}
		}
		requirements = append(requirements, out)
	}
	return gateStatusJSON{
		Node: gateNodeJSON{
			ID:        n.ID,
			Address:   address,
			Kind:      string(n.Kind),
			Lifecycle: string(n.Lifecycle),
		},
		LocallyComplete: completion.LocallyComplete,
		Sealed:          completion.Sealed,
		Requirements:    requirements,
	}, nil
}

func writeGateStatusHuman(ctx context.Context, out io.Writer, v store.View, n store.Node, address string, completion store.NodeCompletion) error {
	if _, err := fmt.Fprintf(out, "%s  %s · %s\n", address, n.Kind, n.Lifecycle); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "locally-complete: %s\n", yesNo(completion.LocallyComplete)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "sealed: %s\n", yesNo(completion.Sealed)); err != nil {
		return err
	}
	if len(completion.Requirements) == 0 {
		_, err := fmt.Fprintln(out, "requirements: none")
		return err
	}
	if _, err := fmt.Fprintln(out, "requirements:"); err != nil {
		return err
	}
	for _, requirement := range completion.Requirements {
		subjectAddress, _, err := readsurface.Address(ctx, v, requirement.Subject)
		if err != nil {
			return err
		}
		state := string(requirement.State)
		if requirement.State == store.GateRequirementClosed {
			state = fmt.Sprintf("closed by %s", requirement.ClosedBy)
			if requirement.ClosedAt != nil {
				state += " at " + requirement.ClosedAt.UTC().Format(gateTimestampLayout)
			}
		}
		if _, err := fmt.Fprintf(out, "  %s [%s, %s, %s, owner %s]: %s\n",
			requirement.Gate, requirement.Scale, requirement.Relationship, subjectAddress, requirement.Owner, state); err != nil {
			return err
		}
	}
	return nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func writeJSON(out io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
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
