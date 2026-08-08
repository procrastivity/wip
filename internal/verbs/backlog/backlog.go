// Package backlog implements the `wip backlog` verb family: `add`, `list`,
// `plan`, `decline` (MODEL §4). `list` is read-only and emits no event.
package backlog

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

// Command constructs the `wip backlog` parent command and its four verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "one list, one noun, one exit set (MODEL §4)",
	}
	cmd.AddCommand(addCommand(streams), listCommand(streams), planCommand(streams), declineCommand(streams))
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

func entryJSON(e store.BacklogEntry) any {
	return struct {
		ID         string `json:"id"`
		Provenance string `json:"provenance"`
		State      string `json:"state"`
		Title      string `json:"title"`
		Detail     string `json:"detail,omitempty"`
		OriginNode string `json:"originNode,omitempty"`
		Matter     string `json:"matter,omitempty"`
	}{
		ID: e.ID, Provenance: string(e.Provenance), State: e.State, Title: e.Title,
		Detail: e.Detail, OriginNode: e.OriginNode, Matter: e.Matter,
	}
}

func addCommand(streams *iostreams.Streams) *cobra.Command {
	var title, provenance, detail, origin string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "enter a new backlog entry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entry, err := writesurface.BacklogAdd(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID,
				store.Provenance(provenance), title, detail, origin)
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(entryJSON(entry))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "entered backlog item %s (%s)\n", entry.ID, entry.Provenance)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the entry's title")
	_ = cmd.MarkFlagRequired("title")
	cmd.Flags().StringVar(&provenance, "provenance", "", "intake, found or deferred")
	_ = cmd.MarkFlagRequired("provenance")
	cmd.Flags().StringVar(&detail, "detail", "", "why (required in spirit for deferred; MODEL §4)")
	cmd.Flags().StringVar(&origin, "origin", "", "the node this entry came from or was pushed out of")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func listCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list this repo's backlog — read-only, emits no event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entries, err := s.Backlog(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}
			if flags.JSON {
				out := make([]any, 0, len(entries))
				for _, e := range entries {
					out = append(out, entryJSON(e))
				}
				b, err := json.Marshal(struct {
					Entries []any `json:"entries"`
				}{Entries: out})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			if len(entries) == 0 {
				_, err := fmt.Fprintln(streams.Out, "backlog is empty")
				return err
			}
			for _, e := range entries {
				if _, err := fmt.Fprintf(streams.Out, "%-26s %-9s %-9s %s\n", e.ID, e.Provenance, e.State, e.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func planCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan <entry-id> <matter-locator>",
		Short: "promote a backlog entry into a matter",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entry, err := writesurface.BacklogPlan(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], args[1])
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(entryJSON(entry))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "planned %s into %s\n", entry.ID, args[1])
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func declineCommand(streams *iostreams.Streams) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "decline <entry-id>",
		Short: "decline a backlog entry — the P1 decline exit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entry, err := writesurface.BacklogDecline(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], reason)
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(entryJSON(entry))
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "declined %s\n", entry.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why this entry was declined")
	_ = cmd.MarkFlagRequired("reason")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
