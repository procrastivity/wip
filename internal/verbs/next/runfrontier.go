package next

// The parallel-frontier display (parallelism-decisions.md, ratified draft):
// when this Clone has an open Run whose Ready set holds more than one node,
// `wip next` shows the full frontier with the Run cap and available slots.
// It is a read: it does not engage work, move the cursor, reserve a slot, or
// promise that the first displayed node runs first.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/readsurface"
	"github.com/procrastivity/wip/internal/scheduler"
	"github.com/procrastivity/wip/internal/store"
)

// renderRunFrontier reports whether it rendered: false hands the display
// back to the ordinary outputs.
func renderRunFrontier(ctx context.Context, streams *iostreams.Streams, s *store.Store, actor store.Actor, dir string, jsonMode bool) (bool, error) {
	cur, err := readsurface.ResolveCurrent(ctx, s, actor, dir)
	if err != nil {
		return false, nil //nolint:nilerr // outside a known clone the ordinary path answers (and refuses) as it always has
	}
	runs, err := s.Runs(ctx)
	if err != nil {
		return false, err
	}
	runCap, err := scheduler.DefaultRunCap()
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if !run.Open || run.Clone != cur.Clone.ID {
			continue
		}
		fr, err := scheduler.Derive(ctx, s.View, run, runCap)
		if err != nil {
			return false, err
		}
		if len(fr.Ready) < 2 {
			continue
		}
		return true, renderFrontier(ctx, streams, s.View, fr, jsonMode)
	}
	return false, nil
}

// batchDisplay is the Batch half of a Run's full address: the name for a
// named Batch, `anonymous · <matter-locator>` for an anonymous one (F5).
func batchDisplay(ctx context.Context, v store.View, batchID string) (string, error) {
	b, err := v.Batch(ctx, batchID)
	if err != nil {
		return "", err
	}
	if b.Name != "" {
		return b.Name, nil
	}
	matter, err := v.Node(ctx, b.Matter)
	if err != nil {
		return "", err
	}
	return "anonymous · " + matter.Locator, nil
}

func renderFrontier(ctx context.Context, streams *iostreams.Streams, v store.View, fr scheduler.Frontier, jsonMode bool) error {
	batch, err := batchDisplay(ctx, v, fr.Run.Batch)
	if err != nil {
		return err
	}
	if jsonMode {
		type nodeJSON struct {
			ID      string `json:"id"`
			Address string `json:"address"`
			Kind    string `json:"kind"`
		}
		out := struct {
			Run     string     `json:"run"`
			Locator string     `json:"locator"`
			Batch   string     `json:"batch"`
			Cap     int        `json:"cap"`
			Slots   int        `json:"slots"`
			Ready   []nodeJSON `json:"ready"`
		}{Run: fr.Run.ID, Locator: fr.Run.Locator, Batch: batch, Cap: fr.Cap, Slots: fr.Slots, Ready: []nodeJSON{}}
		for _, n := range fr.Ready {
			address, _, err := readsurface.Address(ctx, v, n)
			if err != nil {
				return err
			}
			out.Ready = append(out.Ready, nodeJSON{ID: n.ID, Address: address, Kind: string(n.Kind)})
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}

	if _, err := fmt.Fprintf(streams.Out, "%-33s Run · open\n", batch+" · "+fr.Run.Locator); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(streams.Out, "  %d ready in parallel · cap %d · %d slot%s available\n",
		len(fr.Ready), fr.Cap, fr.Slots, plural(fr.Slots)); err != nil {
		return err
	}
	for _, n := range fr.Ready {
		address, _, err := readsurface.Address(ctx, v, n)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(streams.Out, "  %-31s %s · %s\n",
			address, strings.Title(string(n.Kind)), n.Lifecycle); err != nil { //nolint:staticcheck // three fixed ASCII kind tokens
			return err
		}
	}
	_, err = fmt.Fprintln(streams.Out, "  order shown is presentation-only")
	return err
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
