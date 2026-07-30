// Package session implements `wip session [--idle-gap <duration>]` — a
// derived view over the event log covering contiguous working periods
// (MODEL §2.4). Never persisted, drives nothing, gates nothing (D17):
// every call recomputes it from the log. Unlike `status`/`next`, it takes no
// tier scope — it derives host-wide, always (D66).
package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
)

// Command constructs `wip session`.
func Command(streams *iostreams.Streams) *cobra.Command {
	var idleGapFlag string
	cmd := &cobra.Command{
		Use:   "session",
		Short: "working periods derived from the event log — what was done recently",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			idleGap, err := resolveIdleGap(idleGapFlag)
			if err != nil {
				return err
			}

			s, err := tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			sessions, err := readsurface.Derive(cmd.Context(), s, idleGap)
			if err != nil {
				return err
			}

			if flags.JSON {
				return renderJSON(streams, sessions)
			}
			return renderHuman(streams, sessions)
		},
	}
	cmd.Flags().StringVar(&idleGapFlag, "idle-gap", "",
		"idle-gap threshold that ends one working period and begins the next (Go duration syntax, e.g. 12h); defaults to tool config's idle_gap")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// resolveIdleGap is the flag override, first-class per invocation (MODEL
// §2.4), falling back to the configured default (read-surface step-06).
func resolveIdleGap(flag string) (time.Duration, error) {
	if flag != "" {
		d, err := time.ParseDuration(flag)
		if err != nil {
			return 0, fmt.Errorf("session: --idle-gap %q is not a valid duration: %w", flag, err)
		}
		return d, nil
	}
	return readsurface.DefaultIdleGap()
}

func renderHuman(streams *iostreams.Streams, sessions []readsurface.Session) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(streams.Out, "no events in the log yet")
		return err
	}
	for i, sess := range sessions {
		if i > 0 {
			if _, err := fmt.Fprintln(streams.Out); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(streams.Out, "%s — %s (%d events)\n",
			sess.Start.Local().Format(time.RFC3339), sess.End.Local().Format(time.RFC3339), len(sess.Events)); err != nil {
			return err
		}
	}
	return nil
}

type sessionJSON struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Events int    `json:"events"`
}

func renderJSON(streams *iostreams.Streams, sessions []readsurface.Session) error {
	out := make([]sessionJSON, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionJSON{
			Start:  sess.Start.UTC().Format(time.RFC3339),
			End:    sess.End.UTC().Format(time.RFC3339),
			Events: len(sess.Events),
		})
	}
	b, err := json.Marshal(struct {
		Sessions []sessionJSON `json:"sessions"`
	}{Sessions: out})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}
