// Package manifest implements the `wip manifest` verb: wip's own
// machine-readable declaration of itself (manifest-install Brief). It reads
// chassis's single verb-registration point directly off the built root
// command — no second registry.
package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	wipmanifest "github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tracker"
)

// Command constructs the `wip manifest` verb. root is the same *cobra.Command
// NewRootCommand is assembling — captured by reference, so by the time
// RunE executes every other verb registered on it, since Command is called
// during that same construction. providers is the tracker registry root.go
// built; its Names() land in the manifest's trackers list.
func Command(streams *iostreams.Streams, build buildinfo.Info, root *cobra.Command, providers *tracker.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "print wip's machine-readable self-description: verbs, kinds, tracker backends, and shipped assets",
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			m, err := wipmanifest.Build(root, build, wipmanifest.WithTrackers(providers.Names()))
			if err != nil {
				return err
			}

			if flags.Verbose {
				if _, err := fmt.Fprintf(streams.Err, "manifest: %d verb(s), %d asset(s), %d tracker backend(s)\n", len(m.Verbs), len(m.Assets), len(m.Trackers)); err != nil {
					return err
				}
			}

			if flags.JSON {
				b, err := json.Marshal(m)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			_, err = fmt.Fprintf(streams.Out, "%s %s — %d verb(s), %d asset(s), %d tracker(s), schema %d\n",
				m.Tool.Name, m.Tool.Version, len(m.Verbs), len(m.Assets), len(m.Trackers), m.SchemaVersion)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
