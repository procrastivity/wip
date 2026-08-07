// Package run implements the deterministic Run reads and guarded stand-down
// boundary. It intentionally does not expose Run creation, resume, or any
// scheduler control path.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/procrastivity/wip/internal/writesurface"
)

func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "inspect Runs and stand down an interrupted Run"}
	cmd.AddCommand(listCommand(streams), showCommand(streams), standDownCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

type runOutput struct {
	ID            string   `json:"id"`
	Clone         string   `json:"clone"`
	Batch         string   `json:"batch"`
	Locator       string   `json:"locator"`
	State         string   `json:"state"`
	CloseReason   *string  `json:"close_reason"`
	StartedAt     string   `json:"started_at"`
	ClosedAt      *string  `json:"closed_at"`
	Matters       []string `json:"matters"`
	Liveness      *string  `json:"liveness"`
	LivenessCause *string  `json:"liveness_cause"`
	Ownership     string   `json:"ownership"`
}

func output(ctx context.Context, s *store.Store, currentClone string, r store.Run) (runOutput, error) {
	matters, err := s.RunMatters(ctx, r.ID)
	if err != nil {
		return runOutput{}, err
	}
	out := runOutput{
		ID: r.ID, Clone: r.Clone, Batch: r.Batch, Locator: r.Locator,
		State: r.State, StartedAt: r.StartedAt.UTC().Format(time.RFC3339Nano),
		Matters: matters, Ownership: "stranded",
	}
	if r.Clone == currentClone {
		out.Ownership = "owned"
	}
	if r.CloseReason != "" {
		reason := string(r.CloseReason)
		out.CloseReason = &reason
	}
	if r.ClosedAt != nil {
		closed := r.ClosedAt.UTC().Format(time.RFC3339Nano)
		out.ClosedAt = &closed
	}
	if r.Open {
		probe, err := runlock.Probe(r.ID)
		if err != nil {
			probe = runlock.Result{State: runlock.LivenessUnknown, Cause: runlock.CauseFailed}
		}
		out.Liveness = stringPtr(probe.State)
		if probe.Cause != "" {
			out.LivenessCause = stringPtr(probe.Cause)
		}
	}
	return out, nil
}

func stringPtr(value string) *string { return &value }

func currentClone(ctx context.Context, s *store.Store) (store.Clone, error) {
	dir, err := os.Getwd()
	if err != nil {
		return store.Clone{}, err
	}
	clone, found, err := tiers.ResolveCurrentClone(ctx, s, store.ActorHuman, dir)
	if err != nil {
		return store.Clone{}, err
	}
	if !found {
		return store.Clone{}, wiperr.New("refusal.unknown-clone", "refused — this clone is unknown to wip; run `wip init` here first")
	}
	return clone, nil
}

func listCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "list", Short: "list Runs on this host", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, err := openCurrent()
			if err != nil {
				return err
			}
			defer s.Close()
			clone, err := currentClone(cmd.Context(), s)
			if err != nil {
				return err
			}
			runs, err := s.Runs(cmd.Context())
			if err != nil {
				return err
			}
			out := make([]runOutput, 0, len(runs))
			for _, r := range runs {
				item, err := output(cmd.Context(), s, clone.ID, r)
				if err != nil {
					return err
				}
				out = append(out, item)
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Runs []runOutput `json:"runs"`
				}{Runs: out})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			for _, item := range out {
				if _, err := fmt.Fprintf(streams.Out, "%s %-12s %-12s %s (%s, %s)\n", item.ID, item.Locator, item.State, item.LivenessDisplay(), item.Ownership, item.Batch); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (r runOutput) LivenessDisplay() string {
	if r.Liveness == nil {
		return "-"
	}
	if r.LivenessCause != nil {
		return fmt.Sprintf("%s (%s)", *r.Liveness, *r.LivenessCause)
	}
	return *r.Liveness
}

func showCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "show <run-id>", Short: "show one Run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, err := openCurrent()
			if err != nil {
				return err
			}
			defer s.Close()
			clone, err := currentClone(cmd.Context(), s)
			if err != nil {
				return err
			}
			if !store.IsIdentityShaped(args[0]) {
				return wiperr.New("validation.invalid-run-identity", fmt.Sprintf("Run %q must be a full ULID", args[0]))
			}
			r, err := s.Run(cmd.Context(), args[0])
			if err != nil {
				return wiperr.New("validation.unknown-run", fmt.Sprintf("no Run %s", args[0]))
			}
			item, err := output(cmd.Context(), s, clone.ID, r)
			if err != nil {
				return err
			}
			return printRun(streams, flags, item)
		},
	})
}

func standDownCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "stand-down <run-id>", Short: "stand down an interrupted Run", Args: cobra.ExactArgs(1),
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
			defer s.Close()
			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}
			closed, events, err := writesurface.StandDownRun(cmd.Context(), s, store.ActorHuman, cur.Env(), args[0])
			if err != nil {
				return err
			}
			reaped := make([]string, 0)
			for _, event := range events {
				if event.Type == store.TypeDispatchClosed {
					reaped = append(reaped, event.Subject)
				}
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Run              string   `json:"run"`
					State            string   `json:"state"`
					Reason           string   `json:"reason"`
					ReapedDispatches []string `json:"reaped_dispatches"`
				}{Run: closed.ID, State: closed.State, Reason: string(closed.CloseReason), ReapedDispatches: reaped})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "stood down Run %s (%s); reaped %d Dispatch(es)\n", closed.ID, closed.CloseReason, len(reaped))
			return err
		},
	})
}

func printRun(streams *iostreams.Streams, flags cliflags.Flags, item runOutput) error {
	if flags.JSON {
		b, err := json.Marshal(item)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}
	_, err := fmt.Fprintf(streams.Out, "Run %s (%s)\n  state: %s\n  liveness: %s\n  ownership: %s\n", item.ID, item.Locator, item.State, item.LivenessDisplay(), item.Ownership)
	return err
}

func openCurrent() (*store.Store, error) { return tiers.OpenStore() }

func plumbing(cmd *cobra.Command) *cobra.Command {
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
