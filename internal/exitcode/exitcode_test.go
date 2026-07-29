package exitcode_test

import (
	"testing"

	"github.com/procrastivity/wip/internal/exitcode"
	"github.com/procrastivity/wip/internal/wiperr"
)

// TestFromError_ReachesEachStructuredCode covers three of the Brief's four
// exit codes (1, 3, 4) from a synthetic error of the matching kind; code 2
// (Cobra's own argument-parsing path) never reaches FromError — it's
// asserted end-to-end in internal/cli's e2e tests instead, since it depends
// on Cobra's own parse failure, not on any *wiperr.Error.
func TestFromError_ReachesEachStructuredCode(t *testing.T) {
	cases := []struct {
		name string
		code string
		want int
	}{
		{name: "plain user-facing failure", code: "validation.missing-arg", want: exitcode.UserFail},
		{name: "refusal", code: "refusal.tracked-wip-dir", want: exitcode.Refusal},
		{name: "internal/store error", code: "internal.corrupt-row", want: exitcode.Internal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := wiperr.New(tc.code, "synthetic error for exit-code mapping test")
			if got := exitcode.FromError(err); got != tc.want {
				t.Fatalf("FromError(code=%q) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}
