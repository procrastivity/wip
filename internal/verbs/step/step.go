// Package step implements write-surface's Step verbs: the birth verb
// (`create`) and the four amendment verbs (`insert`, `reorder`, `replace`,
// `remove`) — the complete set, settled by identity (D44).
package step

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

// Command constructs the `wip step` parent command and its five verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "step",
		Short: "create and amend steps",
	}
	cmd.AddCommand(
		createCommand(streams),
		insertCommand(streams),
		reorderCommand(streams),
		replaceCommand(streams),
		removeCommand(streams),
	)
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

func writeNodeJSON(streams *iostreams.Streams, n store.Node) error {
	b, err := json.Marshal(struct {
		ID      string `json:"id"`
		Locator string `json:"locator"`
		Title   string `json:"title"`
	}{ID: n.ID, Locator: n.Locator, Title: n.Title})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}

func createCommand(streams *iostreams.Streams) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "create <parent-locator>",
		Short: "birth a step under a matter or a stage, entering Planned",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			node, err := writesurface.CreateStep(cmd.Context(), s, store.ActorHuman, repo.ID, args[0], title)
			if err != nil {
				return err
			}
			if flags.JSON {
				return writeNodeJSON(streams, node)
			}
			_, err = fmt.Fprintf(streams.Out, "created step %s (%s)\n", node.Locator, node.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the step's title")
	_ = cmd.MarkFlagRequired("title")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func insertCommand(streams *iostreams.Streams) *cobra.Command {
	var title, after, before string
	cmd := &cobra.Command{
		Use:   "insert <parent-locator>",
		Short: "birth a step between siblings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			node, err := writesurface.InsertStep(cmd.Context(), s, store.ActorHuman, repo.ID, args[0], title, after, before)
			if err != nil {
				return err
			}
			if flags.JSON {
				return writeNodeJSON(streams, node)
			}
			_, err = fmt.Fprintf(streams.Out, "inserted step %s (%s)\n", node.Locator, node.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the step's title")
	_ = cmd.MarkFlagRequired("title")
	cmd.Flags().StringVar(&after, "after", "", "insert immediately after this sibling step")
	cmd.Flags().StringVar(&before, "before", "", "insert immediately before this sibling step")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func reorderCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reorder <parent-locator> <step-locator>...",
		Short: "rewrite the sibling order of a parent's live steps",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			node, err := writesurface.ReorderStep(cmd.Context(), s, store.ActorHuman, repo.ID, args[0], args[1:])
			if err != nil {
				return err
			}
			if flags.JSON {
				return writeNodeJSON(streams, node)
			}
			_, err = fmt.Fprintf(streams.Out, "reordered the steps of %s\n", args[0])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func replaceCommand(streams *iostreams.Streams) *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "replace <step-locator>",
		Short: "tombstone a step and birth its replacement in the same position",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			node, err := writesurface.ReplaceStep(cmd.Context(), s, store.ActorHuman, repo.ID, args[0], title)
			if err != nil {
				return err
			}
			if flags.JSON {
				return writeNodeJSON(streams, node)
			}
			_, err = fmt.Fprintf(streams.Out, "replaced %s with %s (%s)\n", args[0], node.Locator, node.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the replacement's title")
	_ = cmd.MarkFlagRequired("title")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func removeCommand(streams *iostreams.Streams) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "remove <step-locator>",
		Short: "tombstone a step",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			if err := writesurface.RemoveStep(cmd.Context(), s, store.ActorHuman, repo.ID, args[0], reason); err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Removed string `json:"removed"`
				}{Removed: args[0]})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "removed %s\n", args[0])
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why this step was removed")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
