// Package init implements the `wip init` verb: attaches the current
// directory's git clone (and worktree, and Repo if none exists yet) to the
// store, per the tiers Brief's "Move detection" and "Multi-remote rule"
// sections.
package init

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

// Command constructs the `wip init` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	var identityRemote string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "attach the current git clone (and its worktree) to wip",
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

			result, err := tiers.Init(cmd.Context(), s, store.ActorHuman, dir, identityRemote)
			if err != nil {
				return err
			}

			if flags.Verbose {
				if _, err := fmt.Fprintf(streams.Err, "init: repo created=%v, clone created=%v, worktree=%q\n",
					result.RepoCreated, result.CloneCreated, result.Worktree.Name); err != nil {
					return err
				}
			}

			if flags.JSON {
				payload := struct {
					Repo         string `json:"repo"`
					RepoCreated  bool   `json:"repoCreated"`
					RemoteURL    string `json:"remoteUrl,omitempty"`
					Clone        string `json:"clone"`
					CloneLabel   string `json:"cloneLabel"`
					Worktree     string `json:"worktree"`
					WorktreeName string `json:"worktreeName,omitempty"`
				}{
					Repo: result.Repo.ID, RepoCreated: result.RepoCreated, RemoteURL: result.Repo.RemoteURL,
					Clone: result.Clone.ID, CloneLabel: result.Clone.Label,
					Worktree: result.Worktree.ID, WorktreeName: result.Worktree.Name,
				}
				b, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			where := "the main worktree"
			if result.Worktree.Name != "" {
				where = fmt.Sprintf("worktree %q", result.Worktree.Name)
			}
			_, err = fmt.Fprintf(streams.Out, "attached %s as clone %q (repo %s)\n", where, result.Clone.Label, result.Repo.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&identityRemote, "identity-remote", "", "which remote's URL provides this Repo's identity (default: origin)")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
