// Package refresh implements `wip refresh [locator]` — render-scratch's
// dual-purpose dispatch-open/re-render verb (step-02). With no locator it
// runs the eager path (step-05); with one, the on-demand path a sealed
// Matter needs to render at all.
package refresh

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

// Command constructs `wip refresh [locator]`.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh [locator]",
		Short: "open or continue this worktree's dispatch, and (re-)render .wip/generated/",
		Args:  cobra.MaximumNArgs(1),
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

			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}

			var result render.Result
			if len(args) == 1 {
				result, err = render.Render(cmd.Context(), s, cur, store.ActorHuman, args[0], render.NoPrecondition)
			} else {
				result, err = render.Refresh(cmd.Context(), s, cur, store.ActorHuman, render.NoPrecondition)
			}
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					Dispatch   string   `json:"dispatch"`
					ScratchDir string   `json:"scratchDir"`
					Opened     bool     `json:"opened"`
					Superseded string   `json:"superseded,omitempty"`
					Rendered   []string `json:"rendered"`
				}{
					Dispatch: result.DispatchID, ScratchDir: result.ScratchDir,
					Opened: result.Opened, Superseded: result.Superseded, Rendered: result.Rendered,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			if result.Opened {
				if _, err := fmt.Fprintf(streams.Out, "opened dispatch %s (scratch dir %s)\n", result.DispatchID, result.ScratchDir); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(streams.Out, "continuing dispatch %s (scratch dir %s)\n", result.DispatchID, result.ScratchDir); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(streams.Out, "rendered %d matter(s)\n", len(result.Rendered))
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
