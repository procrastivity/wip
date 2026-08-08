// Package batch implements the four named-Batch plumbing commands.
package batch

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command is the `wip batch` verb group: create, join, leave, dismiss.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{Use: "batch", Short: "manage named Batches"}
	cmd.AddCommand(createCommand(streams), joinCommand(streams), leaveCommand(streams), dismissCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

type batchJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Matter      string `json:"matter"`
	State       string `json:"state"`
	CloseReason string `json:"close_reason,omitempty"`
	Member      string `json:"member,omitempty"`
	Action      string `json:"action"`
}

func batchPayload(b store.Batch, member, action string) batchJSON {
	return batchJSON{ID: b.ID, Name: b.Name, Matter: b.Matter, State: b.State, CloseReason: string(b.CloseReason), Member: member, Action: action}
}

func current(ctxCmd *cobra.Command) (*store.Store, render.Current, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, render.Current{}, err
	}
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, render.Current{}, err
	}
	cur, err := render.ResolveCurrent(ctxCmd.Context(), s, store.ActorFor(cliflags.FromContext(ctxCmd.Context()).AsRole), dir)
	if err != nil {
		_ = s.Close()
		return nil, render.Current{}, err
	}
	return s, cur, nil
}

func displayName(b store.Batch) string {
	if b.Name != "" {
		return b.Name
	}
	return "anonymous"
}

func printBatch(streams *iostreams.Streams, flags cliflags.Flags, verb string, b store.Batch, member string) error {
	if flags.JSON {
		data, err := json.Marshal(batchPayload(b, member, verb))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(data))
		return err
	}
	if member != "" {
		_, err := fmt.Fprintf(streams.Out, "%s Batch %s (%s), Matter %s\n", verb, displayName(b), b.ID, member)
		return err
	}
	_, err := fmt.Fprintf(streams.Out, "%s Batch %s (%s)\n", verb, displayName(b), b.ID)
	return err
}

func plumbing(cmd *cobra.Command) *cobra.Command {
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func createCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "create <name>", Short: "create a named Batch", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := current(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			b, err := writesurface.CreateBatch(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0])
			if err != nil {
				return err
			}
			return printBatch(streams, flags, "created", b, "")
		},
	})
}

func joinCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "join <batch> <matter>", Short: "join a Matter to a named Batch", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := current(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			b, member, err := writesurface.JoinBatch(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0], args[1])
			if err != nil {
				return err
			}
			return printBatch(streams, flags, "joined", b, member)
		},
	})
}

func leaveCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "leave <batch> <matter>", Short: "remove a Matter from a named Batch", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := current(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			b, member, err := writesurface.LeaveBatch(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0], args[1])
			if err != nil {
				return err
			}
			return printBatch(streams, flags, "left", b, member)
		},
	})
}

func dismissCommand(streams *iostreams.Streams) *cobra.Command {
	return plumbing(&cobra.Command{
		Use: "dismiss <batch>", Short: "dismiss a named Batch", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())
			s, cur, err := current(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			b, err := writesurface.DismissBatch(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), cur.Env(), args[0])
			if err != nil {
				return err
			}
			return printBatch(streams, flags, "dismissed", b, "")
		},
	})
}
