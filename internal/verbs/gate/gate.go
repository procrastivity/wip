// Package gate implements `wip gate declare` and `wip gate close`. Per
// `vocabulary` step-02, there is no bespoke `review` verb — every gate
// close, including a local review, goes through this command.
package gate

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the `wip gate` parent command and its two verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "declare and close gates (MODEL §2.3)",
	}
	cmd.AddCommand(declareCommand(streams), closeCommand(streams))
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

func closeCommand(streams *iostreams.Streams) *cobra.Command {
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

			n, err := writesurface.CloseGateWithEnv(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0], args[1])
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
			_, err = fmt.Fprintf(streams.Out, "closed %s on %s\n", args[0], args[1])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
