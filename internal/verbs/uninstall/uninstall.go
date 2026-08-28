// Package uninstall implements the `wip uninstall <harness>` verb: it
// removes exactly the stamped tree a prior `wip install <harness>` wrote,
// and refuses — rather than silently proceeding — if it finds unstamped
// content at the target path (manifest-install Brief).
package uninstall

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/harness/registry"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip uninstall <harness>` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall <harness>",
		Short: "remove a previously installed harness self-projection",
		Long: "remove a previously installed harness self-projection.\n\n" +
			"Available harnesses: " + strings.Join(registry.Names, ", ") + ".",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				_, err := fmt.Fprintf(streams.Out, "available harnesses: %s\nusage: wip uninstall <harness>\n", strings.Join(registry.Names, ", "))
				return err
			}
			harnessName := args[0]
			if !slices.Contains(registry.Names, harnessName) {
				quoted := make([]string, len(registry.Names))
				for i, name := range registry.Names {
					quoted[i] = fmt.Sprintf("%q", name)
				}
				return wiperr.New("validation.unknown-harness",
					fmt.Sprintf("unknown harness %q — only %s is supported", harnessName, strings.Join(quoted, ", or ")))
			}

			flags := cliflags.FromContext(cmd.Context())

			// harnessName was already validated against registry.Names
			// above, so Lookup is guaranteed to find it here.
			h, _ := registry.Lookup(harnessName)
			dir, err := h.Uninstall()
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
