package doctor

import (
	"testing"

	"github.com/procrastivity/wip/internal/guards"
)

// An "advisory."-prefixed code never fails the run (C4.7,
// contract-backport step-04); every other code still does.
func TestFindingsErrorPreservesExistingFailureSemantics(t *testing.T) {
	tests := []struct {
		name     string
		finding  guards.Finding
		wantFail bool
	}{
		{
			name:    "inert tracker binding is advisory",
			finding: guards.Finding{Code: guards.InertTrackerBindingCode},
		},
		{
			name:    "stale harness finding is advisory and no longer fails",
			finding: guards.Finding{Code: "advisory.stale-harness-artifact"},
		},
		{
			name:    "modified harness target is advisory",
			finding: guards.Finding{Code: "advisory.modified-harness-target"},
		},
		{
			name:     "incompatible harness target fails",
			finding:  guards.Finding{Code: "refusal.incompatible-harness-target"},
			wantFail: true,
		},
		{
			name:     "existing refusal finding still fails",
			finding:  guards.Finding{Code: "refusal.gate-order-violation"},
			wantFail: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := findingsError([]guards.Finding{test.finding})
			if got := err != nil; got != test.wantFail {
				t.Fatalf("findingsError() failure = %v, want %v (err=%v)", got, test.wantFail, err)
			}
		})
	}
}
