// Package cli is the one registration point for every verb: it builds the
// root Cobra command, binds the two global flags once, and maps whatever
// Execute returns to the process exit code. cmd/wip/main.go does nothing
// beyond calling into this package.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/selftest"
	"github.com/procrastivity/wip/internal/tracker"
	githubtracker "github.com/procrastivity/wip/internal/tracker/github"
	gitlabtracker "github.com/procrastivity/wip/internal/tracker/gitlab"
	lineartracker "github.com/procrastivity/wip/internal/tracker/linear"
	doctorverb "github.com/procrastivity/wip/internal/verbs/doctor"
	initverb "github.com/procrastivity/wip/internal/verbs/init"
	installverb "github.com/procrastivity/wip/internal/verbs/install"
	manifestverb "github.com/procrastivity/wip/internal/verbs/manifest"
	nextverb "github.com/procrastivity/wip/internal/verbs/next"
	plumbingverb "github.com/procrastivity/wip/internal/verbs/plumbing"
	statusverb "github.com/procrastivity/wip/internal/verbs/status"
	uninstallverb "github.com/procrastivity/wip/internal/verbs/uninstall"
	versionverb "github.com/procrastivity/wip/internal/verbs/version"
	"github.com/procrastivity/wip/internal/wiperr"
)

// NewRootCommand builds the wip root command with both global flags bound
// and every verb registered. It is the only place any verb package gets
// imported — no ad hoc init() side effects live anywhere else.
func NewRootCommand(streams *iostreams.Streams, build buildinfo.Info) *cobra.Command {
	providers := tracker.NewRegistry()
	providers.Register("github", githubtracker.Factory(githubtracker.Options{}))
	providers.Register("gitlab", gitlabtracker.Factory(gitlabtracker.Options{}))
	providers.Register("linear", lineartracker.Factory(lineartracker.Options{}))
	return NewRootCommandWithProviders(streams, build, providers)
}

// NewRootCommandWithProviders builds the root with an injected tracker
// registry. Production uses NewRootCommand. Tests and agent porcelain can
// supply read-capable provider adapters without changing command behavior.
func NewRootCommandWithProviders(streams *iostreams.Streams, build buildinfo.Info, providers *tracker.Registry) *cobra.Command {
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
			asRole, err := cmd.Flags().GetString("as-role")
			if err != nil {
				return err
			}
			if asRole == "" {
				asRole = os.Getenv("WIP_AS_ROLE")
			}
			cmd.SetContext(cliflags.WithFlags(cmd.Context(), cliflags.Flags{JSON: jsonOut, Verbose: verbose, AsRole: asRole}))
			return nil
		},
	}
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)

	root.PersistentFlags().Bool("json", false, "emit the success payload as one JSON value")
	root.PersistentFlags().BoolP("verbose", "v", false, "extra diagnostic lines on stderr")
	root.PersistentFlags().String("as-role", "", "act as this spawned role (or set WIP_AS_ROLE); the claim must have an open `wip plumbing role spawn` behind it")

	// Help lists the porcelain in registration order (D112), not
	// alphabetically — nine entries, manifest last. Every verb that isn't
	// one of these nine lives under the plumbing namespace instead
	// (internal/verbs/plumbing), registered alphabetically there since
	// this global also disables sorting inside that group.
	cobra.EnableCommandSorting = false
	root.AddCommand(statusverb.PorcelainCommand(streams))
	root.AddCommand(nextverb.AliasCommand(streams))
	root.AddCommand(initverb.Command(streams))
	root.AddCommand(doctorverb.Command(streams, build, root))
	root.AddCommand(installverb.Command(streams, build, root, providers))
	root.AddCommand(uninstallverb.Command(streams))
	root.AddCommand(versionverb.Command(streams, build))
	plumbingCmd := plumbingverb.Command(streams, providers)
	root.AddCommand(plumbingCmd)
	// manifest stays last (D112).
	root.AddCommand(manifestverb.Command(streams, build, root, providers))

	// WIP_SELFTEST-gated fixture command: chassis step-11 needs an
	// end-to-end, through-the-built-binary exercise of the code-3/--json
	// refusal path, but no real refusal-raising verb exists yet (that's
	// guards'/render-scratch's territory). Gating it behind an env var
	// keeps it out of the real binary's command tree — and so out of any
	// future manifest walk — while still being reachable for that test.
	if os.Getenv("WIP_SELFTEST") == "1" {
		root.AddCommand(selftest.RefusalCommand())
	}

	// D112's flat-invocation seam. Cobra's own "unknown command" error
	// (legacyArgs, cobra v1.10.2 args.go) only fires when the resolved
	// command's Args field is nil; Find resolves to root itself for any
	// first token that no longer names a root-level command (every moved
	// verb, and any command that never existed). Giving root a non-nil
	// Args lets us tell those two cases apart before Cobra's own message
	// ever gets produced, and giving root a RunE keeps it Runnable so
	// Cobra reaches ValidateArgs (and so this Args func) at all — bare
	// `wip` still gets nil args and falls through to help, exit 0,
	// exactly as before.
	moved := movedVerbs(plumbingCmd)
	root.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if qualified, ok := moved[args[0]]; ok {
			return wiperr.New("validation.moved-verb", fmt.Sprintf(
				"moved — `%s` now lives under the plumbing namespace; run `wip plumbing %s` instead (`wip plumbing` lists the substrate)",
				args[0], qualified,
			))
		}
		return fmt.Errorf("unknown command %q for %q%s", args[0], cmd.CommandPath(), suggestionsBlock(cmd, args[0]))
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	}

	return root
}

// movedVerbs derives the flat-invocation moved set from the plumbing
// group's own registered children (and their aliases) rather than a
// hand-written second list, which would be exactly the kind of parallel
// registry internal/surface exists to avoid. The map's value is each verb's
// canonical (non-alias) name, for the "run `wip plumbing <name>`" remedy.
func movedVerbs(plumbingCmd *cobra.Command) map[string]string {
	moved := map[string]string{}
	for _, c := range plumbingCmd.Commands() {
		moved[c.Name()] = c.Name()
		for _, a := range c.Aliases {
			moved[a] = c.Name()
		}
	}
	return moved
}

// suggestionsBlock rebuilds Cobra's own "Did you mean this?" block
// (Command.findSuggestions is unexported; Command.SuggestionsFor is not)
// so a genuinely unknown command keeps its typo help once root grows its
// own Args func.
func suggestionsBlock(cmd *cobra.Command, arg string) string {
	if cmd.DisableSuggestions {
		return ""
	}
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	suggestions := cmd.SuggestionsFor(arg)
	if len(suggestions) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nDid you mean this?\n")
	for _, s := range suggestions {
		_, _ = fmt.Fprintf(&sb, "\t%v\n", s)
	}
	return sb.String()
}
