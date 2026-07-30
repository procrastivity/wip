// Package clean implements `wip clean` — step-07's sweep for crash orphans:
// a scratch directory whose dispatch never got explicitly closed or
// superseded, and D68's orphaned blobs, both reaped past a documented
// staleness bound.
package clean

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
)

// Command constructs `wip clean`.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "reap crash-orphaned dispatch scratch directories and orphaned blobs",
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

			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}
			result, err := render.Clean(cmd.Context(), s, cur, store.ActorHuman, time.Now(), render.DefaultStaleBound)
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ReapedDispatches []string `json:"reapedDispatches"`
					SweptDirectories []string `json:"sweptDirectories"`
					ReapedBlobs      []string `json:"reapedBlobs"`
				}{
					ReapedDispatches: result.ReapedDispatches,
					SweptDirectories: result.SweptDirectories,
					ReapedBlobs:      result.ReapedBlobs,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "reaped %d dispatch(es), swept %d directory(ies), removed %d orphan blob(s)\n",
				len(result.ReapedDispatches), len(result.SweptDirectories), len(result.ReapedBlobs))
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
