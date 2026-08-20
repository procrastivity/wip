// Package bind implements Matter-owned tracker-reference membership verbs.
package bind

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs `wip bind <matter> <ref>`.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bind <locator> <ref>",
		Short: "add a tracker reference to a Matter",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			n, err := writesurface.Bind(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], args[1])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Node string `json:"node"`
					Ref  string `json:"ref"`
				}{Node: n.ID, Ref: args[1]})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if level, err := s.EffectiveTrackerPushLevel(cmd.Context(), repo.ID); err != nil {
				return err
			} else if level == store.TrackerPushOff {
				_, err = fmt.Fprintf(streams.Out, "bound %s to %s; reference recorded and inert: no tracker update will be sent\n", args[0], args[1])
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "bound %s to %s\n", args[0], args[1])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// UnbindCommand constructs `wip unbind <matter> <ref>`.
func UnbindCommand(streams *iostreams.Streams) *cobra.Command {
	return membershipCommand(streams, "unbind <matter> <ref>", "remove a tracker reference from a Matter", 2,
		func(ctx *cobra.Command, s *store.Store, actor store.Actor, repo string, args []string) (store.Node, error) {
			return writesurface.Unbind(ctx.Context(), s, actor, repo, args[0], args[1])
		}, func(args []string) string { return fmt.Sprintf("unbound %s from %s", args[1], args[0]) })
}

// RebindCommand constructs `wip rebind <matter> <old-ref> <new-ref>`.
func RebindCommand(streams *iostreams.Streams) *cobra.Command {
	return membershipCommand(streams, "rebind <matter> <old-ref> <new-ref>", "replace a Matter tracker reference atomically", 3,
		func(ctx *cobra.Command, s *store.Store, actor store.Actor, repo string, args []string) (store.Node, error) {
			return writesurface.Rebind(ctx.Context(), s, actor, repo, args[0], args[1], args[2])
		}, func(args []string) string { return fmt.Sprintf("rebound %s from %s to %s", args[0], args[1], args[2]) })
}

type membershipWrite func(*cobra.Command, *store.Store, store.Actor, string, []string) (store.Node, error)

func membershipCommand(streams *iostreams.Streams, use, short string, argc int, write membershipWrite, message func([]string) string) *cobra.Command {
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(argc),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			s, err := tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}
			n, err := write(cmd, s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args)
			if err != nil {
				return err
			}
			if cliflags.FromContext(cmd.Context()).JSON {
				b, err := json.Marshal(struct {
					Node string `json:"node"`
				}{Node: n.ID})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintln(streams.Out, message(args))
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
