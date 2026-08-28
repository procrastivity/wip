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
	"github.com/procrastivity/wip/internal/tracker"
	githubtracker "github.com/procrastivity/wip/internal/tracker/github"
	gitlabtracker "github.com/procrastivity/wip/internal/tracker/gitlab"
	lineartracker "github.com/procrastivity/wip/internal/tracker/linear"
	backlogverb "github.com/procrastivity/wip/internal/verbs/backlog"
	batchverb "github.com/procrastivity/wip/internal/verbs/batch"
	bindverb "github.com/procrastivity/wip/internal/verbs/bind"
	cleanverb "github.com/procrastivity/wip/internal/verbs/clean"
	cloneverb "github.com/procrastivity/wip/internal/verbs/clone"
	contentverb "github.com/procrastivity/wip/internal/verbs/content"
	dependverb "github.com/procrastivity/wip/internal/verbs/depend"
	dispatchverb "github.com/procrastivity/wip/internal/verbs/dispatch"
	doctorverb "github.com/procrastivity/wip/internal/verbs/doctor"
	gateverb "github.com/procrastivity/wip/internal/verbs/gate"
	initverb "github.com/procrastivity/wip/internal/verbs/init"
	installverb "github.com/procrastivity/wip/internal/verbs/install"
	labelverb "github.com/procrastivity/wip/internal/verbs/label"
	lifecycleverb "github.com/procrastivity/wip/internal/verbs/lifecycle"
	manifestverb "github.com/procrastivity/wip/internal/verbs/manifest"
	matterverb "github.com/procrastivity/wip/internal/verbs/matter"
	nextverb "github.com/procrastivity/wip/internal/verbs/next"
	outboxverb "github.com/procrastivity/wip/internal/verbs/outbox"
	refreshverb "github.com/procrastivity/wip/internal/verbs/refresh"
	roleverb "github.com/procrastivity/wip/internal/verbs/role"
	runverb "github.com/procrastivity/wip/internal/verbs/run"
	sessionverb "github.com/procrastivity/wip/internal/verbs/session"
	stageverb "github.com/procrastivity/wip/internal/verbs/stage"
	statusverb "github.com/procrastivity/wip/internal/verbs/status"
	stepverb "github.com/procrastivity/wip/internal/verbs/step"
	uninstallverb "github.com/procrastivity/wip/internal/verbs/uninstall"
	versionverb "github.com/procrastivity/wip/internal/verbs/version"
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
	root.PersistentFlags().String("as-role", "", "act as this spawned role (or set WIP_AS_ROLE); the claim must have an open `wip role spawn` behind it")

	root.AddCommand(versionverb.Command(streams, build))
	root.AddCommand(manifestverb.Command(streams, build, root, providers))
	root.AddCommand(installverb.Command(streams, build, root, providers))
	root.AddCommand(uninstallverb.Command(streams))
	root.AddCommand(initverb.Command(streams))
	root.AddCommand(cloneverb.Command(streams))
	root.AddCommand(labelverb.Command(streams))
	root.AddCommand(doctorverb.Command(streams, build, root))
	root.AddCommand(statusverb.Command(streams))

	// read-surface: status's founding-question content lives inside
	// statusverb itself (it extends tiers/tier-verbs step-08's stub in
	// place); next and session are this Matter's two new verbs.
	root.AddCommand(nextverb.Command(streams))
	root.AddCommand(sessionverb.Command(streams))

	// write-surface: birth-and-amendment.
	root.AddCommand(matterverb.Command(streams))
	root.AddCommand(stageverb.Command(streams))
	root.AddCommand(stepverb.Command(streams))
	root.AddCommand(lifecycleverb.StartCommand(streams))
	root.AddCommand(lifecycleverb.FinishCommand(streams, providers))
	root.AddCommand(lifecycleverb.CancelCommand(streams))
	root.AddCommand(lifecycleverb.PauseCommand(streams))
	root.AddCommand(lifecycleverb.ResumeCommand(streams))
	root.AddCommand(backlogverb.Command(streams))
	root.AddCommand(outboxverb.Command(streams, providers))
	root.AddCommand(batchverb.Command(streams))
	root.AddCommand(runverb.Command(streams))
	root.AddCommand(roleverb.Command(streams))

	// write-surface: content-prose.
	root.AddCommand(contentverb.BriefCommand(streams))
	root.AddCommand(contentverb.WorkplanCommand(streams))
	root.AddCommand(contentverb.BodyCommand(streams))
	root.AddCommand(contentverb.FindingCommand(streams))

	// write-surface: gates-and-dependencies.
	root.AddCommand(gateverb.Command(streams, providers))
	root.AddCommand(dependverb.Command(streams))
	root.AddCommand(bindverb.Command(streams))
	root.AddCommand(bindverb.UnbindCommand(streams))
	root.AddCommand(bindverb.RebindCommand(streams))

	// render-scratch: dispatch-open/refresh, explicit dispatch close, clean.
	root.AddCommand(refreshverb.Command(streams))
	root.AddCommand(dispatchverb.Command(streams))
	root.AddCommand(cleanverb.Command(streams))

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
