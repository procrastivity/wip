// Package matter implements `wip matter create` — write-surface's birth verb
// for the addressable root (MODEL §1, D2).
package matter

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

// Command constructs the `wip matter` parent command and its `create` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "matter",
		Short: "create matters — the addressable root",
	}
	cmd.AddCommand(createCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func createCommand(streams *iostreams.Streams) *cobra.Command {
	var title, locator string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "birth a matter, entering Planned",
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

			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			node, err := writesurface.CreateMatter(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, title, locator)
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
			_, err = fmt.Fprintf(streams.Out, "created matter %s (%s)\n", node.Locator, node.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the matter's title")
	cmd.Flags().StringVar(&locator, "locator", "", "the matter's locator (default: derived from the title)")
	_ = cmd.MarkFlagRequired("title")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
