// Package status implements the `wip status` verb — the tier-scoped stub
// (tiers Brief, "Read scope"): repo-wide with the current Clone/Worktree
// marked inside a known Clone, host-wide outside one. `read-surface`
// extends this same verb with the founding "what is in progress" content on
// top of this Step's output; it does not redo the tier scoping.
package status

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
)

// Command constructs the `wip status` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "show the repo (or host) wip currently sees",
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

			view, err := tiers.Status(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}

			if flags.JSON {
				return renderJSON(streams, view)
			}
			return renderHuman(streams, view)
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

type cloneJSON struct {
	ID        string      `json:"id"`
	Label     string      `json:"label"`
	Current   bool        `json:"current"`
	Worktrees []worktreeJ `json:"worktrees,omitempty"`
}

type worktreeJ struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

type repoJSON struct {
	ID     string      `json:"id"`
	Header string      `json:"header"`
	Clones []cloneJSON `json:"clones"`
}

func toRepoJSON(rs tiers.RepoStatus) repoJSON {
	out := repoJSON{ID: rs.Repo.ID, Header: tiers.RepoHeader(rs.Repo)}
	for _, c := range rs.Clones {
		cj := cloneJSON{ID: c.Clone.ID, Label: c.Clone.Label, Current: c.Current}
		for _, w := range c.Worktrees {
			cj.Worktrees = append(cj.Worktrees, worktreeJ{ID: w.Worktree.ID, Name: w.Worktree.Name, Current: w.Current})
		}
		out.Clones = append(out.Clones, cj)
	}
	return out
}

func renderJSON(streams *iostreams.Streams, view tiers.StatusView) error {
	if view.HostWide {
		repos := make([]repoJSON, 0, len(view.Repos))
		for _, rs := range view.Repos {
			repos = append(repos, toRepoJSON(rs))
		}
		b, err := json.Marshal(struct {
			HostWide bool       `json:"hostWide"`
			Repos    []repoJSON `json:"repos"`
		}{HostWide: true, Repos: repos})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}
	b, err := json.Marshal(struct {
		HostWide bool     `json:"hostWide"`
		Repo     repoJSON `json:"repo"`
	}{HostWide: false, Repo: toRepoJSON(view.Repo)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}

func renderHuman(streams *iostreams.Streams, view tiers.StatusView) error {
	if view.HostWide {
		if len(view.Repos) == 0 {
			_, err := fmt.Fprintln(streams.Out, "no repos known to wip on this host — run `wip init` in a clone")
			return err
		}
		for i, rs := range view.Repos {
			if i > 0 {
				if _, err := fmt.Fprintln(streams.Out); err != nil {
					return err
				}
			}
			if err := renderRepo(streams, rs); err != nil {
				return err
			}
		}
		return nil
	}
	return renderRepo(streams, view.Repo)
}

func renderRepo(streams *iostreams.Streams, rs tiers.RepoStatus) error {
	if _, err := fmt.Fprintln(streams.Out, tiers.RepoHeader(rs.Repo)); err != nil {
		return err
	}
	for _, c := range rs.Clones {
		line := fmt.Sprintf("  %-18s Clone", c.Clone.Label)
		if c.Current {
			line += " · current"
		}
		if _, err := fmt.Fprintln(streams.Out, line); err != nil {
			return err
		}
		for _, w := range c.Worktrees {
			wline := fmt.Sprintf("  %-18s Worktree (of %s)", w.Worktree.Name, c.Clone.Label)
			if w.Current {
				wline += " · current"
			}
			if _, err := fmt.Fprintln(streams.Out, wline); err != nil {
				return err
			}
		}
	}
	return nil
}
