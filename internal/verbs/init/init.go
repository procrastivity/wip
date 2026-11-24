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
	"github.com/procrastivity/wip/internal/verbs/sync"
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
			result, err := tiers.Init(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir, identityRemote)
			closeErr := s.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}

			onboarding, onboardingErr := sync.Onboard(cmd.Context(), result.Repo, cmd.InOrStdin(), streams.Err, !flags.JSON)

			if flags.Verbose {
				if _, err := fmt.Fprintf(streams.Err, "init: repo created=%v, clone created=%v, worktree=%q\n",
					result.RepoCreated, result.CloneCreated, result.Worktree.Name); err != nil {
					return err
				}
			}

			if flags.JSON {
				payload := struct {
					Repo               string                 `json:"repo"`
					RepoCreated        bool                   `json:"repoCreated"`
					RemoteURL          string                 `json:"remoteUrl,omitempty"`
					Clone              string                 `json:"clone"`
					CloneLabel         string                 `json:"cloneLabel"`
					Worktree           string                 `json:"worktree"`
					WorktreeName       string                 `json:"worktreeName,omitempty"`
					AlreadyInitialized bool                   `json:"alreadyInitialized,omitempty"`
					Onboarding         *sync.OnboardingResult `json:"onboarding,omitempty"`
				}{
					Repo: result.Repo.ID, RepoCreated: result.RepoCreated, RemoteURL: result.Repo.RemoteURL,
					Clone: result.Clone.ID, CloneLabel: result.Clone.Label,
					Worktree: result.Worktree.ID, WorktreeName: result.Worktree.Name,
					AlreadyInitialized: result.AlreadyInitialized, Onboarding: onboarding,
				}
				b, marshalErr := json.Marshal(payload)
				if marshalErr != nil {
					return marshalErr
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
			} else if result.AlreadyInitialized {
				if result.Worktree.Name != "" {
					_, err = fmt.Fprintf(streams.Out, "already initialized worktree %q of clone %q (repo %s)\n", result.Worktree.Name, result.Clone.Label, result.Repo.ID)
				} else {
					_, err = fmt.Fprintf(streams.Out, "already initialized the main worktree as clone %q (repo %s)\n", result.Clone.Label, result.Repo.ID)
				}
			} else if result.Worktree.Name != "" {
				// A linked worktree is attached to the Clone it belongs to; it
				// does not become one. The main worktree is the Clone itself.
				_, err = fmt.Fprintf(streams.Out, "attached worktree %q to clone %q (repo %s)\n", result.Worktree.Name, result.Clone.Label, result.Repo.ID)
			} else {
				_, err = fmt.Fprintf(streams.Out, "attached the main worktree as clone %q (repo %s)\n", result.Clone.Label, result.Repo.ID)
			}
			if err != nil {
				return err
			}
			if onboarding != nil && onboarding.Message != "" {
				if _, err := fmt.Fprintln(streams.Err, onboarding.Message); err != nil {
					return err
				}
			}
			return onboardingErr
		},
	}
	cmd.Flags().StringVar(&identityRemote, "identity-remote", "", "which remote's URL provides this Repo's identity (default: origin)")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
