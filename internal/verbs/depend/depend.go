// Package depend implements `wip depend add` and `wip depend remove` — the
// `blocked-by` edge verbs (MODEL §5, D28/D29).
package depend

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

// Command constructs the `wip depend` parent command and its two verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "depend",
		Short: "add and remove blocked-by edges",
	}
	cmd.AddCommand(addCommand(streams), removeCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
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

func addCommand(streams *iostreams.Streams) *cobra.Command {
	var blockedBy string
	cmd := &cobra.Command{
		Use:   "add <node>",
		Short: "add a blocked-by edge: <node> waits for --blocked-by",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			edge, err := writesurface.DependAdd(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], blockedBy)
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Edge    string `json:"edge"`
					Blocked string `json:"blocked"`
					Blocker string `json:"blocker"`
				}{Edge: edge.ID, Blocked: edge.Blocked, Blocker: edge.Blocker})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "%s is now blocked-by %s\n", args[0], blockedBy)
			return err
		},
	}
	cmd.Flags().StringVar(&blockedBy, "blocked-by", "", "the node this one waits for")
	_ = cmd.MarkFlagRequired("blocked-by")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func removeCommand(streams *iostreams.Streams) *cobra.Command {
	var blockedBy string
	cmd := &cobra.Command{
		Use:   "remove <node>",
		Short: "remove a blocked-by edge — tombstoned, not deleted (D44)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			if err := writesurface.DependRemove(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], blockedBy); err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Blocked string `json:"blocked"`
					Blocker string `json:"blocker"`
				}{Blocked: args[0], Blocker: blockedBy})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "%s is no longer blocked-by %s\n", args[0], blockedBy)
			return err
		},
	}
	cmd.Flags().StringVar(&blockedBy, "blocked-by", "", "the node this one no longer waits for")
	_ = cmd.MarkFlagRequired("blocked-by")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
