// Package outbox implements provider-neutral inspection and human lifecycle
// actions. Commands use an injected provider registry to resolve the configured
// backend. The stock binary registers the GitHub provider in that registry.
package outbox

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
	"github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the outbox command group. providers is deliberately
// injected at the CLI registration point.
func Command(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	cmd := &cobra.Command{Use: "outbox", Short: "inspect and disposition provider-neutral delivery work"}
	cmd.AddCommand(listCommand(streams), levelCommand(streams), backendCommand(streams, providers), approveCommand(streams), declineCommand(streams), retryCommand(streams), flushCommand(streams, providers))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func backendCommand(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backend [none|name]",
		Short: "read or set this repo's tracker backend",
		Args:  cobra.MaximumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()

		backend, _, err := s.Config(cmd.Context(), repo.ID, store.TrackerBackendKey)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			backend = strings.TrimSpace(args[0])
			if backend == "none" {
				backend = ""
			}
			if backend != "" {
				known := false
				for _, name := range providers.Names() {
					if name == backend {
						known = true
						break
					}
				}
				if !known {
					return fmt.Errorf("tracker: backend %q is not registered; available: %s", backend, strings.Join(providers.Names(), ", "))
				}
			}
			if err := s.SetConfig(cmd.Context(), repo.ID, store.TrackerBackendKey, backend); err != nil {
				return err
			}
		}

		if cliflags.FromContext(cmd.Context()).JSON {
			b, err := json.Marshal(struct {
				Backend string `json:"backend"`
			}{backend})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(streams.Out, string(b))
			return err
		}
		if backend == "" {
			backend = "none"
		}
		_, err = fmt.Fprintln(streams.Out, backend)
		return err
	}
	return plumbing(cmd)
}

func levelCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "level [off|boundary|narrated]",
		Short: "read or set this repo's tracker push level",
		Args:  cobra.MaximumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		var level store.TrackerPushLevel
		if len(args) == 1 {
			level, err = s.SetTrackerPushLevel(cmd.Context(), repo.ID, args[0])
		} else {
			level, err = s.EffectiveTrackerPushLevel(cmd.Context(), repo.ID)
		}
		if err != nil {
			return err
		}
		if cliflags.FromContext(cmd.Context()).JSON {
			b, err := json.Marshal(struct {
				Level store.TrackerPushLevel `json:"level"`
			}{level})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(streams.Out, string(b))
			return err
		}
		_, err = fmt.Fprintln(streams.Out, level)
		return err
	}
	return plumbing(cmd)
}

type entryOutput struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	State          string          `json:"state"`
	Subject        string          `json:"subject"`
	Reference      string          `json:"reference,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
	Reason         string          `json:"reason,omitempty"`
	Attempts       int             `json:"attempts"`
}

func output(e store.OutboxEntry) entryOutput {
	return entryOutput{
		ID: e.ID, Kind: e.Kind, State: e.State, Subject: e.Subject,
		Reference: e.Ref, IdempotencyKey: e.IdempotencyKey, Payload: e.Payload,
		Reason: e.Reason, Attempts: e.Attempts,
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

func listCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{Use: "list", Short: "list this repo's outbox — read-only, emits no event", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		entries, err := s.Outbox(cmd.Context(), repo.ID)
		if err != nil {
			return err
		}
		items := make([]entryOutput, 0, len(entries))
		for _, entry := range entries {
			items = append(items, output(entry))
		}
		if cliflags.FromContext(cmd.Context()).JSON {
			b, err := json.Marshal(struct {
				Entries []entryOutput `json:"entries"`
			}{Entries: items})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(streams.Out, string(b))
			return err
		}
		if len(items) == 0 {
			_, err := fmt.Fprintln(streams.Out, "outbox is empty")
			return err
		}
		for _, item := range items {
			if _, err := fmt.Fprintf(streams.Out, "%s %-8s %-9s attempts=%d ref=%s subject=%s key=%s reason=%s\n",
				item.ID, item.Kind, item.State, item.Attempts, item.Reference, item.Subject, item.IdempotencyKey, item.Reason); err != nil {
				return err
			}
		}
		return nil
	}
	return plumbing(cmd)
}

func approveCommand(streams *iostreams.Streams) *cobra.Command {
	return actionCommand(streams, "approve <outbox-id>", "approve one queued entry for delivery", func(cmd *cobra.Command, s *store.Store, repo store.Repo, id string) (store.OutboxEntry, error) {
		return writesurface.OutboxApprove(cmd.Context(), s, actor(cmd), repo.ID, id)
	})
}

func retryCommand(streams *iostreams.Streams) *cobra.Command {
	return actionCommand(streams, "retry <outbox-id>", "explicitly retry one failed or withheld entry", func(cmd *cobra.Command, s *store.Store, repo store.Repo, id string) (store.OutboxEntry, error) {
		return writesurface.OutboxRetry(cmd.Context(), s, actor(cmd), repo.ID, id)
	})
}

func declineCommand(streams *iostreams.Streams) *cobra.Command {
	var reason string
	cmd := actionCommand(streams, "decline <outbox-id>", "decline one entry terminally", func(cmd *cobra.Command, s *store.Store, repo store.Repo, id string) (store.OutboxEntry, error) {
		return writesurface.OutboxDecline(cmd.Context(), s, actor(cmd), repo.ID, id, reason)
	})
	cmd.Flags().StringVar(&reason, "reason", "", "why the entry is declined")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

type action func(*cobra.Command, *store.Store, store.Repo, string) (store.OutboxEntry, error)

func actionCommand(streams *iostreams.Streams, use, short string, fn action) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		entry, err := fn(cmd, s, repo, args[0])
		if err != nil {
			return err
		}
		item := output(entry)
		if cliflags.FromContext(cmd.Context()).JSON {
			b, err := json.Marshal(item)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(streams.Out, string(b))
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "%s %s is %s\n", item.Kind, item.ID, item.State)
		return err
	}
	return plumbing(cmd)
}

func flushCommand(streams *iostreams.Streams, providers *tracker.Registry) *cobra.Command {
	cmd := &cobra.Command{Use: "flush", Short: "flush approved work through the configured provider seam", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		backend, _, err := s.Config(cmd.Context(), repo.ID, store.TrackerBackendKey)
		if err != nil {
			return err
		}
		if backend == "" {
			return fmt.Errorf("tracker: no provider seam configured")
		}
		seam, err := providers.Resolve(backend, repo)
		if err != nil {
			return err
		}
		report, err := tracker.Flush(cmd.Context(), s, actor(cmd), repo.ID, seam)
		if err != nil {
			return err
		}
		if cliflags.FromContext(cmd.Context()).JSON {
			b, err := json.Marshal(report)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(streams.Out, string(b))
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "flushed %d outbox result(s)\n", len(report.Entries))
		return err
	}
	return plumbing(cmd)
}

func actor(cmd *cobra.Command) store.Actor {
	return store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole)
}

func plumbing(cmd *cobra.Command) *cobra.Command {
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
