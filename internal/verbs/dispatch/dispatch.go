// Package dispatch implements `wip dispatch close` — D59's path (a): the
// explicit, deliberate close, `reason = completed`. The agent-porcelain
// contract makes this the agent's last act (agent-path).
package dispatch

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
)

// Command constructs the `wip dispatch` parent command and its one verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "close this worktree's open dispatch (D59)",
	}
	cmd.AddCommand(closeCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func closeCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close",
		Short: "close this worktree's open dispatch with reason completed, and sweep its scratch dir",
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

			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir)
			if err != nil {
				return err
			}
			closed, err := render.CloseExplicit(cmd.Context(), s, cur, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole))
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					Dispatch string `json:"dispatch"`
					Reason   string `json:"reason"`
				}{Dispatch: closed.ID, Reason: string(closed.CloseReason)})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "closed dispatch %s (%s)\n", closed.ID, closed.CloseReason)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
