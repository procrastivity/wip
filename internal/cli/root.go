// Package cli is the one registration point for every verb: it builds the
// root Cobra command, binds the two global flags once, and maps whatever
// Execute returns to the process exit code. cmd/wip/main.go does nothing
// beyond calling into this package.
package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/selftest"
	cloneverb "github.com/procrastivity/wip/internal/verbs/clone"
	doctorverb "github.com/procrastivity/wip/internal/verbs/doctor"
	initverb "github.com/procrastivity/wip/internal/verbs/init"
	installverb "github.com/procrastivity/wip/internal/verbs/install"
	labelverb "github.com/procrastivity/wip/internal/verbs/label"
	manifestverb "github.com/procrastivity/wip/internal/verbs/manifest"
	statusverb "github.com/procrastivity/wip/internal/verbs/status"
	uninstallverb "github.com/procrastivity/wip/internal/verbs/uninstall"
	versionverb "github.com/procrastivity/wip/internal/verbs/version"
)

// NewRootCommand builds the wip root command with both global flags bound
// and every verb registered. It is the only place any verb package gets
// imported — no ad hoc init() side effects live anywhere else.
func NewRootCommand(streams *iostreams.Streams, build buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:   "wip",
		Short: "wip — a personal, agent-friendly project-management CLI",
		// We render every error ourselves (see Execute) so human and
		// --json modes come from one code path; Cobra's own printing
		// would double up or bypass the --json envelope.
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			jsonOut, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			verbose, err := cmd.Flags().GetBool("verbose")
			if err != nil {
				return err
			}
			cmd.SetContext(cliflags.WithFlags(cmd.Context(), cliflags.Flags{JSON: jsonOut, Verbose: verbose}))
			return nil
		},
	}
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)

	root.PersistentFlags().Bool("json", false, "emit the success payload as one JSON value")
	root.PersistentFlags().BoolP("verbose", "v", false, "extra diagnostic lines on stderr")

	root.AddCommand(versionverb.Command(streams, build))
	root.AddCommand(manifestverb.Command(streams, build, root))
	root.AddCommand(installverb.Command(streams, build, root))
	root.AddCommand(uninstallverb.Command(streams))
	root.AddCommand(initverb.Command(streams))
	root.AddCommand(cloneverb.Command(streams))
	root.AddCommand(labelverb.Command(streams))
	root.AddCommand(doctorverb.Command(streams))
	root.AddCommand(statusverb.Command(streams))

	// WIP_SELFTEST-gated fixture command: chassis step-11 needs an
	// end-to-end, through-the-built-binary exercise of the code-3/--json
	// refusal path, but no real refusal-raising verb exists yet (that's
	// guards'/render-scratch's territory). Gating it behind an env var
	// keeps it out of the real binary's command tree — and so out of any
	// future manifest walk — while still being reachable for that test.
	if os.Getenv("WIP_SELFTEST") == "1" {
		root.AddCommand(selftest.RefusalCommand())
	}

	return root
}
