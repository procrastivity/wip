package doctor

import (
	"testing"

	"github.com/procrastivity/wip/internal/guards"
)

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
			name:     "existing stale harness finding still fails",
			finding:  guards.Finding{Code: "advisory.stale-harness-artifact"},
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
