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
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

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

			events, err := writesurface.Start(cmd.Context(), s, store.ActorHuman, repo.ID, args[0])
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

				node, err := move(cmd.Context(), s, store.ActorHuman, repo.ID, args[0])
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

func transitionCommandWithEnv(use, short string, move func(context.Context, *store.Store, store.Actor, store.Env, string) (store.Node, error), verbWord string) func(*iostreams.Streams) *cobra.Command {
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
			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}
			n, err := move(cmd.Context(), s, store.ActorHuman, cur.Env(), args[0])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					ID        string `json:"id"`
					Lifecycle string `json:"lifecycle"`
				}{n.ID, string(n.Lifecycle)})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "%s %s\n", verbWord, args[0])
			return err
		}}
		surface.Annotate(cmd, surface.Plumbing)
		return cmd
	}
}

// FinishCommand constructs `wip finish <locator>`.
func FinishCommand(streams *iostreams.Streams) *cobra.Command {
	return transitionCommandWithEnv("finish <locator>", "move a matter, stage or step from In Progress to Done",
		writesurface.FinishWithEnv, "finished")(streams)
}

// CancelCommand constructs `wip cancel <locator>`.
func CancelCommand(streams *iostreams.Streams) *cobra.Command {
	return transitionCommand("cancel <locator>", "move a matter, stage or step from In Progress to Canceled",
		writesurface.Cancel, "canceled")(streams)
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
