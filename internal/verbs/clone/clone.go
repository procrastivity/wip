// Package clone implements the `wip clone` verb family: `list` (the
// read-only companion `relink`/`label` act on) and `relink` (the accepted
// half of doctor's move-detection offer).
package clone

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip clone` parent command and its subcommands.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone",
		Short: "inspect and repair this host's known clones",
	}
	cmd.AddCommand(listCommand(streams), relinkCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func listCommand(streams *iostreams.Streams) *cobra.Command {
	var repoLocator string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "list a repo's known clones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			repo, err := resolveTargetRepo(cmd.Context(), s, dir, repoLocator)
			if err != nil {
				return err
			}
			clones, err := s.ClonesOfRepo(cmd.Context(), repo.ID)
			if err != nil {
				return err
			}

			if flags.JSON {
				type cloneOut struct {
					ID           string `json:"id"`
					Label        string `json:"label"`
					GitCommonDir string `json:"gitCommonDir"`
				}
				out := make([]cloneOut, 0, len(clones))
				for _, c := range clones {
					out = append(out, cloneOut{ID: c.ID, Label: c.Label, GitCommonDir: c.GitCommonDir})
				}
				b, err := json.Marshal(struct {
					Repo   string     `json:"repo"`
					Clones []cloneOut `json:"clones"`
				}{Repo: repo.ID, Clones: out})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			if len(clones) == 0 {
				_, err := fmt.Fprintf(streams.Out, "repo %s has no known clones\n", repo.ID)
				return err
			}
			for _, c := range clones {
				if _, err := fmt.Fprintf(streams.Out, "%-20s %-40s %s\n", c.Label, c.GitCommonDir, c.ID); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoLocator, "repo", "", "the repo to list clones of (default: the current repo)")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// resolveTargetRepo resolves --repo when given, else the current directory's
// own repo — refusing per MODEL §11 if neither is available.
func resolveTargetRepo(ctx context.Context, s *store.Store, dir, repoLocator string) (store.Repo, error) {
	if repoLocator != "" {
		return tiers.ResolveRepo(ctx, s.View, repoLocator)
	}
	clone, found, err := tiers.ResolveCurrentClone(ctx, s, store.ActorFor(cliflags.FromContext(ctx).AsRole), dir)
	if err != nil {
		return store.Repo{}, err
	}
	if !found {
		return store.Repo{}, wiperr.New("refusal.unknown-clone",
			"refused — this clone is unknown to wip; pass --repo, or run `wip init` here first (a write; if you cannot run it, ask the user)")
	}
	return s.Repo(ctx, clone.Repo)
}

func relinkCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relink <clone-locator>",
		Short: "point a known clone's row at this directory (the clone moved)",
		Args:  cobra.ExactArgs(1),
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

			clone, err := tiers.Relink(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir, args[0])
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ID           string `json:"id"`
					Label        string `json:"label"`
					GitCommonDir string `json:"gitCommonDir"`
				}{ID: clone.ID, Label: clone.Label, GitCommonDir: clone.GitCommonDir})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			_, err = fmt.Fprintf(streams.Out, "relinked clone %q to %s\n", clone.Label, clone.GitCommonDir)
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
