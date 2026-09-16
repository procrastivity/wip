// Package install implements the `wip install [harness]` verb: it runs the
// manifest pipeline, filters to plumbing verbs, and writes the result to
// the target harness's install path, stamped (manifest-install Brief,
// "Claude-code install target"). Given a harness name, it installs (or
// reinstalls) into just that target; the registry's harnesses
// (internal/harness/registry) are the recognized targets, and an
// unrecognized name fails validation rather than silently no-op'ing. Run
// bare, it instead detects every harness available on this host and
// installs into each one in turn, reporting a per-harness result rather
// than aborting the whole run for one harness's refusal. Before writing to
// any target, it asks internal/harness.Status and refuses
// (internal/harness.Refusal) on any of its three unsafe states — a target
// a human edited, one holding foreign unstamped content, or one whose
// stamp this binary cannot use; a Current target is reported current
// rather than rewritten; --force skips Status and overwrites
// unconditionally, in both modes.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/harness"
	"github.com/procrastivity/wip/internal/harness/registry"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Command constructs the `wip install <harness>` verb. root is the
// *cobra.Command NewRootCommand is assembling, captured by reference so the
// manifest it builds at RunE time reflects every verb ultimately
// registered on it (see internal/verbs/manifest for the same pattern).
func Command(streams *iostreams.Streams, build buildinfo.Info, root *cobra.Command, providers *tracker.Registry) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install <harness>",
		Short: "render and install wip's self-projection into an agent harness",
		Long: "render and install wip's self-projection into an agent harness.\n\n" +
			"With no harness name, wip install detects every harness available on this host and installs into each one, reporting a result per harness. Naming a harness installs (or reinstalls) only that one.\n\n" +
			"Available harnesses: " + strings.Join(registry.Names, ", ") + ".\n\n" +
			"Refuses to overwrite a target that was hand-edited, holds unstamped content, or carries a stamp this binary cannot use; pass --force to overwrite it anyway, in either mode. A tree that already matches what this binary would write is reported as current and left untouched; --force rewrites it anyway.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())

			if len(args) == 0 {
				m, err := manifest.Build(root, build, manifest.WithTrackers(providers.Names()))
				if err != nil {
					return err
				}
				return installAll(streams, flags, m, force)
			}

			harnessName := args[0]
			if !slices.Contains(registry.Names, harnessName) {
				quoted := make([]string, len(registry.Names))
				for i, name := range registry.Names {
					quoted[i] = fmt.Sprintf("%q", name)
				}
				return wiperr.New("validation.unknown-harness",
					fmt.Sprintf("unknown harness %q — only %s is supported", harnessName, strings.Join(quoted, ", or ")))
			}

			m, err := manifest.Build(root, build, manifest.WithTrackers(providers.Names()))
			if err != nil {
				return err
			}

			// harnessName was already validated against registry.Names
			// above, so Lookup is guaranteed to find it here.
			h, _ := registry.Lookup(harnessName)

			if !force {
				installDir, err := h.InstallDir()
				if err != nil {
					return err
				}
				files, err := h.Generate(m)
				if err != nil {
					return err
				}
				s, err := harness.Status(installDir, files)
				if err != nil {
					return err
				}
				if err := harness.Refusal(harnessName, installDir, s, harness.ForceRemedy(harnessName, s)); err != nil {
					return err
				}
				if s == harness.Current {
					return writeTargetedResult(streams, flags, harnessName, installDir, "current")
				}
				// Missing or Stale: fall through and install below.
			}

			dir, err := h.Install(m)
			if err != nil {
				return err
			}

			return writeTargetedResult(streams, flags, harnessName, dir, "installed")
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite a target tree even if it was hand-edited or not written by wip install")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// writeTargetedResult renders the targeted (single-harness) mode's outcome
// to streams.Out: under --json, {"harness","dir","status"}; in human mode,
// "installed <name> skill at <dir>" when status is "installed", or "<name>
// skill at <dir> is already current" when status is "current".
func writeTargetedResult(streams *iostreams.Streams, flags cliflags.Flags, harnessName, dir, status string) error {
	if flags.JSON {
		payload := struct {
			Harness string `json:"harness"`
			Dir     string `json:"dir"`
			Status  string `json:"status"`
		}{Harness: harnessName, Dir: dir, Status: status}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}

	var line string
	if status == "current" {
		line = fmt.Sprintf("%s skill at %s is already current", harnessName, dir)
	} else {
		line = fmt.Sprintf("installed %s skill at %s", harnessName, dir)
	}
	_, err := fmt.Fprintln(streams.Out, line)
	return err
}

// harnessResult is one row of the bare-invocation report: what happened
// when installAll considered a single harness. Exactly one of Dir, Reason,
// or Err is populated, matching Status.
type harnessResult struct {
	Harness string              `json:"harness"`
	Status  string              `json:"status"`
	Dir     string              `json:"dir,omitempty"`
	Reason  string              `json:"reason,omitempty"`
	Error   *harnessResultError `json:"error,omitempty"`
}

// harnessResultError is a refusal's code and message, carried inside a
// harnessResult exactly as wiperr.Error would render them under --json.
type harnessResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// installAll is the bare `wip install` (no harness argument) path: it
// walks registry.All in order, installing into every harness Available
// reports present on this host and recording one result per harness —
// installed, current (the tree on disk already matches what this binary
// would generate, so nothing is written), skipped (not detected), or
// refused (Status found one of its three unsafe states, without --force;
// each carries its own code, per harness.Refusal). A refusal on one
// harness does not stop the run; any other error (an I/O failure reading
// or writing a harness's install dir) does, since that is not a policy
// decision this loop can route around. After printing every result, it
// returns a single refusal.harness-targets-refused error naming every
// refused harness so the process still exits non-zero, unless nothing was
// refused.
func installAll(streams *iostreams.Streams, flags cliflags.Flags, m manifest.Manifest, force bool) error {
	results := make([]harnessResult, 0, len(registry.All))
	var refused []string

	for _, h := range registry.All {
		if !h.Available() {
			results = append(results, harnessResult{Harness: h.Name, Status: "skipped", Reason: "not detected"})
			continue
		}

		if !force {
			installDir, err := h.InstallDir()
			if err != nil {
				return err
			}
			files, err := h.Generate(m)
			if err != nil {
				return err
			}
			s, err := harness.Status(installDir, files)
			if err != nil {
				return err
			}
			if err := harness.Refusal(h.Name, installDir, s, harness.ForceRemedy(h.Name, s)); err != nil {
				var werr *wiperr.Error
				if errors.As(err, &werr) {
					results = append(results, harnessResult{
						Harness: h.Name,
						Status:  "refused",
						Error:   &harnessResultError{Code: werr.Code, Message: werr.Message},
					})
					refused = append(refused, h.Name)
					continue
				}
				return err
			}
			if s == harness.Current {
				results = append(results, harnessResult{Harness: h.Name, Status: "current", Dir: installDir})
				continue
			}
			// Missing or Stale: fall through and install below.
		}

		dir, err := h.Install(m)
		if err != nil {
			return err
		}
		results = append(results, harnessResult{Harness: h.Name, Status: "installed", Dir: dir})
	}

	if err := writeInstallAllResults(streams, flags, results); err != nil {
		return err
	}

	if len(refused) > 0 {
		return wiperr.New("refusal.harness-targets-refused",
			fmt.Sprintf("refused — %d harness target(s) hold hand-edited, unstamped, or incompatible content (%s); re-run `wip install <harness> --force` for each to overwrite",
				len(refused), strings.Join(refused, ", ")))
	}
	return nil
}

// writeInstallAllResults renders installAll's per-harness results to
// streams.Out: one JSON value under --json, or one line per harness in
// human mode, followed by a hint when every harness was skipped.
func writeInstallAllResults(streams *iostreams.Streams, flags cliflags.Flags, results []harnessResult) error {
	if flags.JSON {
		payload := struct {
			Results []harnessResult `json:"results"`
		}{Results: results}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(streams.Out, string(b))
		return err
	}

	anyDetected := false
	for _, r := range results {
		var line string
		switch r.Status {
		case "installed":
			anyDetected = true
			line = fmt.Sprintf("installed %s skill at %s", r.Harness, r.Dir)
		case "current":
			anyDetected = true
			line = fmt.Sprintf("current %s skill at %s", r.Harness, r.Dir)
		case "refused":
			anyDetected = true
			// The refusal message already opens with "refused — "; the
			// line's own leading "refused <harness>" carries that word.
			line = fmt.Sprintf("refused %s — %s", r.Harness, strings.TrimPrefix(r.Error.Message, "refused — "))
		default: // "skipped"
			line = fmt.Sprintf("skipped %s — %s", r.Harness, r.Reason)
		}
		if _, err := fmt.Fprintln(streams.Out, line); err != nil {
			return err
		}
	}

	if !anyDetected {
		if _, err := fmt.Fprintln(streams.Out, "no harness detected on this host; install one explicitly: wip install <harness>"); err != nil {
			return err
		}
	}
	return nil
}
