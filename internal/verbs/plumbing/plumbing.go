// Package plumbing builds the `wip plumbing` namespace command (D112): the
// audience boundary between porcelain (top-level, what bare `wip --help`
// lists) and plumbing (the deterministic substrate skills and scripts call,
// listed only by `wip plumbing --help`). It carries no logic of its own —
// every verb it groups lives in its own package, the same
// one-package-per-verb convention every porcelain verb already follows —
// and declares no RunE: a Cobra command with no Run/RunE is not Runnable,
// so invoking it bare (or with --help) falls into Cobra's own help path
// (flag.ErrHelp), which internal/cli.Execute's ExecuteC call already
// renders as the command's help text with a nil error — exit 0, without
// any special-casing here or in the chassis.
package plumbing

import (
	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/tracker"
	backlogverb "github.com/procrastivity/wip/internal/verbs/backlog"
	batchverb "github.com/procrastivity/wip/internal/verbs/batch"
	bindverb "github.com/procrastivity/wip/internal/verbs/bind"
	cleanverb "github.com/procrastivity/wip/internal/verbs/clean"
	cloneverb "github.com/procrastivity/wip/internal/verbs/clone"
	contentverb "github.com/procrastivity/wip/internal/verbs/content"
	dependverb "github.com/procrastivity/wip/internal/verbs/depend"
	dispatchverb "github.com/procrastivity/wip/internal/verbs/dispatch"
	gateverb "github.com/procrastivity/wip/internal/verbs/gate"
	labelverb "github.com/procrastivity/wip/internal/verbs/label"
	lifecycleverb "github.com/procrastivity/wip/internal/verbs/lifecycle"
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
)

// Command constructs the `wip plumbing` namespace command and registers
// every plumbing verb under it (D112). The group command itself is never
// annotated with a surface kind — it has no Runnable RunE of its own, so
// the manifest walk (internal/manifest/verbs.go's collect) recurses through
// it without requiring a kind, the same way it already treats any other
// command with children.
//
// providers is needed because three moved constructors take it
// (gateverb.Command, lifecycleverb.FinishCommand, outboxverb.Command); no
// other moved verb needs it.
//
// Registered alphabetically: cobra.EnableCommandSorting is a package
// global, so turning it off in internal/cli/root.go (to get a
// registration-ordered porcelain list) also turns it off here, and this is
// the only place left to keep the substrate list readable.
func Command(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plumbing",
		Short: "the deterministic substrate: JSON + exit codes, scripts and skills call this",
		Long: "wip plumbing is the audience boundary: everything under it is the deterministic " +
			"substrate skills and scripts call, and none of it is listed among wip's own top-level " +
			"(porcelain) verbs. A human crosses into it deliberately.",
	}
	cmd.AddCommand(backlogverb.Command(streams))
	cmd.AddCommand(batchverb.Command(streams))
	cmd.AddCommand(bindverb.Command(streams))
	cmd.AddCommand(contentverb.BodyCommand(streams))
	cmd.AddCommand(contentverb.BriefCommand(streams))
	cmd.AddCommand(lifecycleverb.CancelCommand(streams))
	cmd.AddCommand(cleanverb.Command(streams))
	cmd.AddCommand(cloneverb.Command(streams))
	cmd.AddCommand(dependverb.Command(streams))
	cmd.AddCommand(dispatchverb.Command(streams))
	cmd.AddCommand(contentverb.FindingCommand(streams))
	cmd.AddCommand(lifecycleverb.FinishCommand(streams, providers))
	cmd.AddCommand(gateverb.Command(streams, providers))
	cmd.AddCommand(labelverb.Command(streams))
	cmd.AddCommand(matterverb.Command(streams))
	cmd.AddCommand(nextverb.Command(streams))
	cmd.AddCommand(outboxverb.Command(streams, providers))
	cmd.AddCommand(lifecycleverb.PauseCommand(streams))
	cmd.AddCommand(bindverb.RebindCommand(streams))
	cmd.AddCommand(refreshverb.Command(streams))
	cmd.AddCommand(lifecycleverb.ResumeCommand(streams))
	cmd.AddCommand(roleverb.Command(streams))
	cmd.AddCommand(runverb.Command(streams))
	cmd.AddCommand(sessionverb.Command(streams))
	cmd.AddCommand(stageverb.Command(streams))
	cmd.AddCommand(lifecycleverb.StartCommand(streams))
	cmd.AddCommand(statusverb.Command(streams))
	cmd.AddCommand(stepverb.Command(streams))
	cmd.AddCommand(bindverb.UnbindCommand(streams))
	cmd.AddCommand(contentverb.WorkplanCommand(streams))
	return cmd
}
