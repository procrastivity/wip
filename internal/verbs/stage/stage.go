// Package stage implements `wip stage create` — write-surface's birth verb
// for a Stage under a Matter.
package stage

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the `wip stage` parent command and its `create` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stage",
		Short: "create stages under a matter",
	}
	cmd.AddCommand(createCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func createCommand(streams *iostreams.Streams) *cobra.Command {
	var title string

	cmd := &cobra.Command{
		Use:   "create <matter-locator>",
		Short: "birth a stage under a matter, entering Planned",
		Args:  cobra.ExactArgs(1),
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

			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			node, err := writesurface.CreateStage(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], title)
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ID      string `json:"id"`
					Locator string `json:"locator"`
					Title   string `json:"title"`
				}{ID: node.ID, Locator: node.Locator, Title: node.Title})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "created stage %s/%s (%s)\n", args[0], node.Locator, node.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the stage's title")
	_ = cmd.MarkFlagRequired("title")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
