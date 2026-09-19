// Package refresh implements `wip plumbing refresh [locator]` — render-scratch's
// dual-purpose dispatch-open/re-render verb (step-02). With no locator it
// runs the eager path (step-05); with one, the on-demand path a sealed
// Matter needs to render at all.
package refresh

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/guards/trackedwip"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/render"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
)

// Command constructs `wip plumbing refresh [locator]`.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh [locator]",
		Short: "open or continue this worktree's dispatch, and (re-)render .wip/generated/",
		Long: "With no locator: open or continue this worktree's dispatch and re-render " +
			"every not-sealed Matter. With a locator: render that node's owning Matter " +
			"alone — the only way a sealed Matter renders. Either way the result names " +
			"the files actually written: generatedFiles in JSON (absolute paths, in " +
			"write order, beside the retained rendered locators); the human locator " +
			"form lists each file, the bare form prints \"wrote N file(s)\". Read those " +
			"files — never wip's database — for Matter content.",
		Args: cobra.MaximumNArgs(1),
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
			defer func() { _ = s.Close() }()

			cur, err := render.ResolveCurrent(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), dir)
			if err != nil {
				return err
			}

			var result render.Result
			if len(args) == 1 {
				result, err = render.Render(cmd.Context(), s, cur, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), args[0], trackedwip.RenderPrecondition)
			} else {
				result, err = render.Refresh(cmd.Context(), s, cur, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), trackedwip.RenderPrecondition)
			}
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					Dispatch       string   `json:"dispatch"`
					ScratchDir     string   `json:"scratchDir"`
					Opened         bool     `json:"opened"`
					Superseded     string   `json:"superseded,omitempty"`
					Rendered       []string `json:"rendered"`
					GeneratedFiles []string `json:"generatedFiles"`
				}{
					Dispatch: result.DispatchID, ScratchDir: result.ScratchDir,
					Opened: result.Opened, Superseded: result.Superseded, Rendered: result.Rendered,
					GeneratedFiles: nonNil(result.GeneratedFiles),
				})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}

			if result.Opened {
				if _, err := fmt.Fprintf(streams.Out, "opened dispatch %s (scratch dir %s)\n", result.DispatchID, result.ScratchDir); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(streams.Out, "continuing dispatch %s (scratch dir %s)\n", result.DispatchID, result.ScratchDir); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(streams.Out, "rendered %d matter(s)\n", len(result.Rendered)); err != nil {
				return err
			}

			// Human output diverges here (navigation contract §4.3): the
			// on-demand path (a locator was given) lists each written file;
			// the eager (bare) path prints only a summary count, so a
			// hundred-plus-file eager pass does not bury `status`. JSON
			// carries the full list on both paths, unconditionally.
			if len(args) == 1 {
				for _, path := range result.GeneratedFiles {
					if _, err := fmt.Fprintf(streams.Out, "  %s\n", path); err != nil {
						return err
					}
				}
				return nil
			}
			_, err = fmt.Fprintf(streams.Out, "wrote %d file(s)\n", len(result.GeneratedFiles))
			return err
		},
	}
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// nonNil turns a nil slice into an empty, non-nil one so it marshals as
// `[]` rather than `null` (navigation contract §4.1: generatedFiles is
// always present, never null, matching rendered's own convention).
func nonNil(paths []string) []string {
	if paths == nil {
		return []string{}
	}
	return paths
}
