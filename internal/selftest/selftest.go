// Package selftest holds test-fixture commands that exist only to exercise
// chassis's own error-envelope machinery end-to-end through the built
// binary (step-11). They are wired into the root command only when
// WIP_SELFTEST=1 is set (see internal/cli.NewRootCommand) so they never
// appear in a real install or a future manifest walk.
package selftest

import (
	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/wiperr"
)

// RefusalCommand manufactures a refusal-style *wiperr.Error, unconditionally,
// so chassis's tests can assert the code-3 exit path and its --json envelope
// without borrowing a real refusal check from guards or render-scratch (which
// haven't been built yet). The code and message are vocabulary's own ratified
// tracked-.wip/ refusal (workplans/vocabulary.md step-09), reused verbatim —
// this is the proving case both Matters converged on independently.
func RefusalCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "__selftest-refusal",
		Short:  "chassis test fixture: manufactures a refusal-style error",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return wiperr.New(
				"refusal.tracked-wip-dir",
				"refused — .wip/ is tracked by git in this repo; untrack it and add it to .git/info/exclude, then re-run",
			)
		},
	}
}
