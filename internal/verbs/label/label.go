// Package label implements the `wip label` verb: sets or renames the
// current clone's label, per the tiers Brief's "Labels and addressing"
// section.
package label

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
)

// Command constructs the `wip label <new-label>` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label <new-label>",
		Short: "set or rename the current clone's label",
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

			clone, err := tiers.SetLabel(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir, args[0])
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				}{ID: clone.ID, Label: clone.Label})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			_, err = fmt.Fprintf(streams.Out, "clone %s labeled %q\n", clone.ID, clone.Label)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
