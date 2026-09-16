// Package doctor implements the `wip doctor` verb. `tiers` step-04 built
// this command with its one check (unknown clone); `guards` extends it here
// with the other four — dependency cycles, gate-order monotonicity, tracked
// `.wip/`, stale generated harness artifacts — through a pluggable check
// registry (internal/guards.Run), so `doctor` grows by registration rather
// than by a second command or a second output path.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/guards"
	"github.com/procrastivity/wip/internal/guards/trackedwip"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip doctor` verb. root is the *cobra.Command
// NewRootCommand is assembling, captured by reference — the same pattern
// `wip manifest`/`wip install` already use — so the stale-harness-artifact
// check (step-08) reads the manifest every verb ultimately registered on it.
func Command(streams *iostreams.Streams, build buildinfo.Info, root *cobra.Command) *cobra.Command {
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

			// tiers step-04's own check keeps its own call site, ahead of
			// the findings registry below: unlike the other four checks,
			// it can hard-fail (MODEL §11's unknown-clone refusal, exit 3)
			// rather than ever contribute a finding, so it is deliberately
			// not folded into the flat, no-severity findings list
			// guards.md's "Doctor output shape" resolves for the rest.
			report, err := tiers.CheckUnknownClone(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir)
			if err != nil {
				return err
			}

			// HarnessTargets returns both findings and per-target state
			// (C4.5), so it runs beside the registry rather than inside
			// it — a future check with no per-target state would run
			// through guards.Run as before.
			harnessFindings, targets, err := guards.HarnessTargets(root, build)
			if err != nil {
				return err
			}

			findings, err := guards.Run(cmd.Context(), s, dir,
				func(ctx context.Context, s *store.Store, _ string) ([]guards.Finding, error) {
					repo := report.Repo
					if !report.Known {
						repo = report.OfferRepo
					}
					return guards.CheckInertTrackerBindings(ctx, s, repo.ID)
				},
				guards.CheckCycles,
				guards.CheckGateOrder,
				trackedwip.CheckTrackedWipDir,
			)
			if err != nil {
				return err
			}
			findings = append(findings, harnessFindings...)

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
					Known       bool                 `json:"known"`
					Clone       string               `json:"clone,omitempty"`
					OfferRepo   string               `json:"offerRepo,omitempty"`
					OfferClones []cloneOut           `json:"offerClones,omitempty"`
					Findings    []guards.Finding     `json:"findings"`
					Targets     []guards.TargetState `json:"targets"`
				}{
					Known: report.Known, Clone: report.Clone.ID,
					OfferRepo: report.OfferRepo.ID, OfferClones: offered,
					Findings: findings, Targets: targets,
				})
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(streams.Out, string(b)); err != nil {
					return err
				}
				return findingsError(findings)
			}

			if report.Known {
				if _, err := fmt.Fprintf(streams.Out, "this clone is known to wip (clone %s, repo %s)\n", report.Clone.ID, report.Repo.ID); err != nil {
					return err
				}
			} else {
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
			}

			// Each registered target's drift state comes ahead of the
			// findings (C4.5) — per-target fact first, problems after.
			for _, t := range targets {
				if _, err := fmt.Fprintf(streams.Out, "%s: %s at %s\n", t.Harness, t.State, t.Dir); err != nil {
					return err
				}
			}

			if len(findings) == 0 {
				_, err := fmt.Fprintln(streams.Out, "no issues found")
				return err
			}
			for _, f := range findings {
				if _, err := fmt.Fprintf(streams.Out, "%s: %s\n", f.Code, f.Message); err != nil {
					return err
				}
			}
			return findingsError(findings)
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// findingsError signals doctor's own exit posture — 0 with no failing
// findings, 1 with one or more failing findings (chassis's "user-facing
// failure" code). A finding whose code carries the "advisory." prefix
// never fails the run (C4.7): the inert-tracker-binding advisory, the
// per-file stale-harness-artifact findings, and the modified/unowned
// harness-target advisories all stay in the same flat output list without
// failing doctor. This is distinct from the refusal exit code (3) the
// render precondition uses at its own call site for the tracked-`.wip/`
// condition specifically.
func findingsError(findings []guards.Finding) error {
	failing := 0
	for _, finding := range findings {
		if !strings.HasPrefix(finding.Code, "advisory.") {
			failing++
		}
	}
	if failing == 0 {
		return nil
	}
	return wiperr.New("doctor.findings-present", fmt.Sprintf("%d finding(s) reported; see above", failing))
}
