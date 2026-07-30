// Package doctor implements the `wip doctor` verb. This Matter builds
// exactly one of its eventual five checks — the unknown-clone check (tiers
// Brief, "Move detection") — and `guards` (`guards ← tiers, render-scratch`)
// adds the other four to this same command later, not a new one.
package doctor

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

// Command constructs the `wip doctor` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "diagnose this location against what wip knows",
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

			report, err := tiers.CheckUnknownClone(cmd.Context(), s, store.ActorHuman, dir)
			if err != nil {
				return err
			}

			if flags.JSON {
				type cloneOut struct {
					ID    string `json:"id"`
					Label string `json:"label"`
				}
				offered := make([]cloneOut, 0, len(report.OfferClones))
				for _, c := range report.OfferClones {
					offered = append(offered, cloneOut{ID: c.ID, Label: c.Label})
				}
				b, err := json.Marshal(struct {
					Known       bool       `json:"known"`
					Clone       string     `json:"clone,omitempty"`
					OfferRepo   string     `json:"offerRepo,omitempty"`
					OfferClones []cloneOut `json:"offerClones,omitempty"`
				}{
					Known: report.Known, Clone: report.Clone.ID,
					OfferRepo: report.OfferRepo.ID, OfferClones: offered,
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			if report.Known {
				_, err := fmt.Fprintf(streams.Out, "this clone is known to wip (clone %s, repo %s)\n", report.Clone.ID, report.Repo.ID)
				return err
			}

			if _, err := fmt.Fprintf(streams.Out, "this clone is unknown, but its remote matches known repo %s\n", report.OfferRepo.ID); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(streams.Out, "  relink: wip clone relink <clone-locator>   (this clone moved)"); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(streams.Out, "  new clone: wip init                        (a separate clone of the same repo)"); err != nil {
				return err
			}
			if len(report.OfferClones) > 0 {
				if _, err := fmt.Fprintln(streams.Out, "  existing clones of this repo:"); err != nil {
					return err
				}
				for _, c := range report.OfferClones {
					if _, err := fmt.Fprintf(streams.Out, "    %-20s %s\n", c.Label, c.ID); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
