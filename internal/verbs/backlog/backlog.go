// Package backlog implements the `wip plumbing backlog` verb family: `add`, `list`,
// `plan`, `decline`, `delegate` (MODEL §4), plus the read-only porcelain
// `wip backlog` command.
package backlog

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the `wip plumbing backlog` parent command and its verbs.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "one list, one noun, one exit set (MODEL §4)",
	}
	cmd.AddCommand(addCommand(streams), listCommand(streams), planCommand(streams), declineCommand(streams), delegateCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// PorcelainCommand constructs the human-facing `wip backlog` member. The
// compact and full human renders are distinct from the plumbing list, while
// --json deliberately shares the plumbing command's exact renderer.
func PorcelainCommand(streams *iostreams.Streams) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "review open local backlog entries",
		Args:  porcelainArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entries, err := s.ActiveBacklog(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}
			if flags.JSON {
				return renderJSON(streams, entries)
			}
			if len(entries) == 0 {
				_, err := fmt.Fprintln(streams.Out, "backlog is empty")
				return err
			}
			if full {
				return renderFull(streams, entries)
			}
			return renderCompact(streams, entries)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "show IDs, provenance, state, and supporting details")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func porcelainArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "add", "list", "plan", "decline", "delegate":
		invocation := strings.Join(args, " ")
		return wiperr.New("validation.moved-verb", fmt.Sprintf(
			"moved — `backlog %s` now lives under the plumbing namespace; run `wip plumbing backlog %s` instead",
			invocation, invocation,
		))
	default:
		return cobra.NoArgs(cmd, args)
	}
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
		ID            string `json:"id"`
		Provenance    string `json:"provenance"`
		State         string `json:"state"`
		Title         string `json:"title"`
		Detail        string `json:"detail,omitempty"`
		DeclineReason string `json:"declineReason,omitempty"`
		OriginNode    string `json:"originNode,omitempty"`
		Matter        string `json:"matter,omitempty"`
		Outbox        string `json:"outbox,omitempty"`
	}{
		ID: e.ID, Provenance: string(e.Provenance), State: e.State, Title: e.Title,
		Detail: e.Detail, DeclineReason: e.DeclineReason, OriginNode: e.OriginNode,
		Matter: e.Matter, Outbox: e.Outbox,
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
			if entry.State == "delegated" {
				_, err = fmt.Fprintf(streams.Out, "entered backlog item %s (%s); delegated — outbox %s queued, awaiting approve\n", entry.ID, entry.Provenance, entry.Outbox)
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

			entries, err := s.ActiveBacklog(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}
			if flags.JSON {
				return renderJSON(streams, entries)
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

func renderJSON(streams *iostreams.Streams, entries []store.BacklogEntry) error {
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

func renderCompact(streams *iostreams.Streams, entries []store.BacklogEntry) error {
	if _, err := fmt.Fprintf(streams.Out, "%s:\n", backlogCount(len(entries))); err != nil {
		return err
	}
	for _, e := range entries {
		suffix := ""
		if e.State == "delegated" {
			suffix = " · delegated"
		}
		if _, err := fmt.Fprintf(streams.Out, "  %-26s %s%s\n", e.ID, e.Title, suffix); err != nil {
			return err
		}
	}
	return nil
}

func renderFull(streams *iostreams.Streams, entries []store.BacklogEntry) error {
	if _, err := fmt.Fprintf(streams.Out, "%s:\n", backlogCount(len(entries))); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(streams.Out, "\n%s\n  id: %s\n  provenance: %s\n  state: %s\n", e.Title, e.ID, e.Provenance, e.State); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"detail", e.Detail},
			{"origin", e.OriginNode},
			{"matter", e.Matter},
			{"outbox", e.Outbox},
		} {
			if field.value == "" {
				continue
			}
			if _, err := fmt.Fprintf(streams.Out, "  %s: %s\n", field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func backlogCount(n int) string {
	if n == 1 {
		return "1 backlog entry"
	}
	return fmt.Sprintf("%d backlog entries", n)
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

func delegateCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delegate <entry-id>",
		Short: "delegate a backlog entry through the tracker outbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			entry, err := writesurface.BacklogDelegate(cmd.Context(), s, store.ActorFor(flags.AsRole), repo.ID, args[0])
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
			_, err = fmt.Fprintf(streams.Out, "delegated %s through outbox %s\n", entry.ID, entry.Outbox)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
