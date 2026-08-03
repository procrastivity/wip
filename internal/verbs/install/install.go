// Package install implements the `wip install <harness>` verb: it runs the
// manifest pipeline, filters to plumbing verbs, and writes the result to
// the target harness's install path, stamped (manifest-install Brief,
// "Claude-code install target"). claude-code, codex, pi, and opencode are
// the recognized targets; an unrecognized harness name fails validation
// rather than silently no-op'ing.
package install

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip install <harness>` verb. root is the
// *cobra.Command NewRootCommand is assembling, captured by reference so the
// manifest it builds at RunE time reflects every verb ultimately
// registered on it (see internal/verbs/manifest for the same pattern).
func Command(streams *iostreams.Streams, build buildinfo.Info, root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install <harness>",
		Short: "render and install wip's self-projection into an agent harness",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			harnessName := args[0]
			if harnessName != claudecode.Name && harnessName != codex.Name && harnessName != pi.Name && harnessName != opencode.Name {
				return wiperr.New("validation.unknown-harness",
					fmt.Sprintf("unknown harness %q — only %q, %q, %q, or %q is supported", harnessName, claudecode.Name, codex.Name, pi.Name, opencode.Name))
			}

			flags := cliflags.FromContext(cmd.Context())

			m, err := manifest.Build(root, build)
			if err != nil {
				return err
			}

			var dir string
			switch harnessName {
			case claudecode.Name:
				dir, err = claudecode.Install(m)
			case codex.Name:
				dir, err = codex.Install(m)
			case pi.Name:
				dir, err = pi.Install(m)
			case opencode.Name:
				dir, err = opencode.Install(m)
			}
			if err != nil {
				return err
			}

			if flags.JSON {
				payload := struct {
					Harness string `json:"harness"`
					Dir     string `json:"dir"`
				}{Harness: harnessName, Dir: dir}
				b, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			_, err = fmt.Fprintf(streams.Out, "installed %s skill at %s\n", harnessName, dir)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
