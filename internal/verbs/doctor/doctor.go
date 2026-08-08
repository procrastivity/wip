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

			findings, err := guards.Run(cmd.Context(), s, dir,
				guards.CheckCycles,
				guards.CheckGateOrder,
				trackedwip.CheckTrackedWipDir,
				func(_ context.Context, _ *store.Store, _ string) ([]guards.Finding, error) {
					return guards.CheckStaleHarnessArtifact(root, build)
				},
				func(_ context.Context, _ *store.Store, _ string) ([]guards.Finding, error) {
					return guards.CheckStaleCodexHarnessArtifact(root, build)
				},
				func(_ context.Context, _ *store.Store, _ string) ([]guards.Finding, error) {
					return guards.CheckStalePiHarnessArtifact(root, build)
				},
				func(_ context.Context, _ *store.Store, _ string) ([]guards.Finding, error) {
					return guards.CheckStaleOpencodeHarnessArtifact(root, build)
				},
			)
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
					Known       bool             `json:"known"`
					Clone       string           `json:"clone,omitempty"`
					OfferRepo   string           `json:"offerRepo,omitempty"`
					OfferClones []cloneOut       `json:"offerClones,omitempty"`
					Findings    []guards.Finding `json:"findings"`
				}{
					Known: report.Known, Clone: report.Clone.ID,
					OfferRepo: report.OfferRepo.ID, OfferClones: offered,
					Findings: findings,
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

// findingsError signals doctor's own exit posture — 0 clean, 1 with one or
// more findings (chassis's "user-facing failure" code; no severity levels,
// guards.md's resolved "no severity levels; a flat findings list" call) —
// distinct from the refusal exit code (3) the render precondition uses at
// its own call site for the tracked-`.wip/` condition specifically.
func findingsError(findings []guards.Finding) error {
	if len(findings) == 0 {
		return nil
	}
	return wiperr.New("doctor.findings-present", fmt.Sprintf("%d finding(s) reported; see above", len(findings)))
}
