// Package bind implements `wip bind` — ships inert in P1 (D25, §5): the verb
// and its event ship now, but nothing consumes the reference until Phase 3.
package bind

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

// Command constructs `wip bind <locator> <ref>`.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bind <locator> <ref>",
		Short: "bind an external reference to a node — inert until Phase 3",
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

			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			n, err := writesurface.Bind(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], args[1])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Node string `json:"node"`
					Ref  string `json:"ref"`
				}{Node: n.ID, Ref: n.ExternalRef})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "bound %s to %s\n", args[0], args[1])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
