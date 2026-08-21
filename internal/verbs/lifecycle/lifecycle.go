// Package lifecycle implements the five scale-polymorphic lifecycle verbs:
// `wip start`, `wip finish`, `wip cancel`, `wip pause`, `wip resume` — each a
// top-level root command (MODEL §2.2's lifecycle is uniform at every scale,
// so there is no per-scale verb triple).
package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/writesurface"
)

// cursorEndedJSON is the `cursorEnded` field a JSON payload gains when a
// write ends the cursor's own work (finish, cancel, gate close) — the same
// shape gate's closeCommand builds independently, since the two packages
// share no JSON types by convention (each verb owns its own wire shape).
type cursorEndedJSON struct {
	Reason    string `json:"reason"`
	Target    string `json:"target,omitempty"`
	Suggested string `json:"suggested,omitempty"`
}

// handoff computes the best-effort hand-off line and JSON fragment after a
// write that may have ended the cursor's work, strictly post-commit and
// read-only (D67: the write itself never moves the cursor). ok is false —
// and both other returns are zero — whenever there is nothing to report;
// callers render nothing in that case, human or JSON.
func handoff(ctx context.Context, v store.View, clone, worktree string) (line string, j *cursorEndedJSON, ok bool) {
	ended, l, targetAddr, ok := readsurface.Handoff(ctx, v, clone, worktree)
	if !ok {
		return "", nil, false
	}
	c := cursorEndedJSON{Reason: ended.Reason, Target: targetAddr}
	if ended.Suggested != nil {
		c.Suggested = ended.Suggested.Locator
	}
	return l, &c, true
}

func openRepo(cmd *cobra.Command) (*store.Store, store.Repo, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, store.Repo{}, err
	}
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, store.Repo{}, err
	}
	repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
	if err != nil {
		_ = s.Close()
		return nil, store.Repo{}, err
	}
	return s, repo, nil
}

// StartCommand constructs `wip start <locator>`.
func StartCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <locator>",
		Short: "move a matter, stage or step from Planned to In Progress",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			events, err := writesurface.Start(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Started int `json:"started"`
				}{Started: len(events)})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if len(events) > 1 {
				_, err = fmt.Fprintf(streams.Out, "started %s (auto-started %d ancestor(s))\n", args[0], len(events)-1)
			} else {
				_, err = fmt.Fprintf(streams.Out, "started %s\n", args[0])
			}
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// transitionCommand is the shared shape of finish/cancel/pause/resume: one
// node, one event.
func transitionCommand(use, short string, move func(context.Context, *store.Store, store.Actor, string, string) (store.Node, error), verbWord string) func(*iostreams.Streams) *cobra.Command {
	return func(streams *iostreams.Streams) *cobra.Command {
		cmd := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				flags := cliflags.FromContext(cmd.Context())
				s, repo, err := openRepo(cmd)
				if err != nil {
					return err
				}
				defer func() { _ = s.Close() }()

				node, err := move(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0])
				if err != nil {
					return err
				}
				if flags.JSON {
					b, err := json.Marshal(struct {
						ID        string `json:"id"`
						Lifecycle string `json:"lifecycle"`
					}{ID: node.ID, Lifecycle: string(node.Lifecycle)})
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(streams.Out, string(b))
					return err
				}
				_, err = fmt.Fprintf(streams.Out, "%s %s\n", verbWord, args[0])
				return err
			},
		}
		surface.Annotate(cmd, surface.Plumbing)
		return cmd
	}
}

func transitionCommandWithEnv(use, short string, move func(context.Context, *store.Store, store.Actor, store.Env, string) (writesurface.SealTransition, error), verbWord string, coordinator *tracker.AlignmentCoordinator) func(*iostreams.Streams) *cobra.Command {
	return func(streams *iostreams.Streams) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir)
			if err != nil {
				return err
			}
			transition, err := move(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0])
			if err != nil {
				return err
			}
			n := transition.Node
			var report tracker.AlignmentReport
			if transition.BecameSealed {
				report = coordinator.Check(cmd.Context(), s.View, n.ID)
			}
			var alignment *tracker.AlignmentReport
			if report.Visible() {
				alignment = &report
			}
			line, ce, ok := handoff(cmd.Context(), s.View, cur.Clone.ID, cur.Worktree.ID)
			if flags.JSON {
				b, err := json.Marshal(struct {
					ID          string                   `json:"id"`
					Lifecycle   string                   `json:"lifecycle"`
					CursorEnded *cursorEndedJSON         `json:"cursorEnded,omitempty"`
					Alignment   *tracker.AlignmentReport `json:"alignment,omitempty"`
				}{ID: n.ID, Lifecycle: string(n.Lifecycle), CursorEnded: ce, Alignment: alignment})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if _, err := fmt.Fprintf(streams.Out, "%s %s\n", verbWord, args[0]); err != nil {
				return err
			}
			for _, alignmentLine := range report.Lines() {
				if _, err := fmt.Fprintln(streams.Out, alignmentLine); err != nil {
					return err
				}
			}
			if ok {
				_, err = fmt.Fprintln(streams.Out, line)
			}
			return err
		}}
		surface.Annotate(cmd, surface.Plumbing)
		return cmd
	}
}

// FinishCommand constructs `wip finish <locator>`.
func FinishCommand(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	return transitionCommandWithEnv("finish <locator>", "move a matter, stage or step from In Progress to Done",
		writesurface.FinishWithEnvResult, "finished", tracker.NewAlignmentCoordinator(providers))(streams)
}

// CancelCommand constructs `wip cancel <locator>`. It cannot use
// transitionCommand — writesurface.Cancel carries the extra optional
// `reason` param transitionCommand's move signature has no room for — so its
// body mirrors transitionCommand's exactly, plus the flag.
func CancelCommand(streams *iostreams.Streams) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel <locator>",
		Short: "move a matter, stage or step from In Progress to Canceled",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			node, err := writesurface.Cancel(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], reason)
			if err != nil {
				return err
			}

			// Best-effort hand-off, resolved fresh here rather than threaded
			// through openRepo: cancel is the one lifecycle verb that never
			// needed a resolved Current for its own write, and a failure to
			// resolve one now (e.g. no known Worktree) means no hand-off,
			// never a cancel failure (D67: read-only, post-commit).
			var line string
			var ce *cursorEndedJSON
			var ok bool
			if dir, derr := os.Getwd(); derr == nil {
				if cur, cerr := render.ResolveCurrent(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir); cerr == nil {
					line, ce, ok = handoff(cmd.Context(), s.View, cur.Clone.ID, cur.Worktree.ID)
				}
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ID          string           `json:"id"`
					Lifecycle   string           `json:"lifecycle"`
					CursorEnded *cursorEndedJSON `json:"cursorEnded,omitempty"`
				}{ID: node.ID, Lifecycle: string(node.Lifecycle), CursorEnded: ce})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if _, err := fmt.Fprintf(streams.Out, "%s %s\n", "canceled", args[0]); err != nil {
				return err
			}
			if ok {
				_, err = fmt.Fprintln(streams.Out, line)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why this work was canceled")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// PauseCommand constructs `wip pause <locator>`.
func PauseCommand(streams *iostreams.Streams) *cobra.Command {
	return transitionCommand("pause <locator>", "move a matter, stage or step from In Progress to Paused",
		writesurface.Pause, "paused")(streams)
}

// ResumeCommand constructs `wip resume <locator>`.
func ResumeCommand(streams *iostreams.Streams) *cobra.Command {
	return transitionCommand("resume <locator>", "move a matter, stage or step from Paused to In Progress",
		writesurface.Resume, "resumed")(streams)
}
