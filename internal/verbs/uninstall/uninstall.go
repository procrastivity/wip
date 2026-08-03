// Package uninstall implements the `wip uninstall <harness>` verb: it
// removes exactly the stamped tree a prior `wip install <harness>` wrote,
// and refuses — rather than silently proceeding — if it finds unstamped
// content at the target path (manifest-install Brief).
package uninstall

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/harness/claudecode"
	"github.com/procrastivity/wip/internal/harness/codex"
	"github.com/procrastivity/wip/internal/harness/opencode"
	"github.com/procrastivity/wip/internal/harness/pi"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip uninstall <harness>` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall <harness>",
		Short: "remove a previously installed harness self-projection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			harnessName := args[0]

			flags := cliflags.FromContext(cmd.Context())

			var dir string
			var err error
			switch harnessName {
			case claudecode.Name:
				dir, err = claudecode.Uninstall()
			case codex.Name:
				dir, err = codex.Uninstall()
			case pi.Name:
				dir, err = pi.Uninstall()
			case opencode.Name:
				dir, err = opencode.Uninstall()
			default:
				return wiperr.New("validation.unknown-harness",
					fmt.Sprintf("unknown harness %q — only %q, %q, %q, or %q is supported", harnessName, claudecode.Name, codex.Name, pi.Name, opencode.Name))
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

			_, err = fmt.Fprintf(streams.Out, "uninstalled %s skill from %s\n", harnessName, dir)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
