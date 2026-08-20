package guards

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
)

// InertTrackerBindingCode identifies the one doctor finding that is purely
// informational and does not affect doctor's exit status.
const InertTrackerBindingCode = "advisory.inert-tracker-binding"

// CheckInertTrackerBindings reports every active tracker reference in repo
// when its effective push level is off. The reference remains useful as
// provenance, but no tracker update can be queued from it at that level.
func CheckInertTrackerBindings(ctx context.Context, s *store.Store, repo string) ([]Finding, error) {
	level, err := s.EffectiveTrackerPushLevel(ctx, repo)
	if err != nil {
		return nil, err
	}
	if level != store.TrackerPushOff {
		return nil, nil
	}

	matters, err := s.Matters(ctx, repo)
	if err != nil {
		return nil, err
	}
	findings := make([]Finding, 0)
	for _, matter := range matters {
		refs, err := s.TrackerReferences(ctx, matter.ID)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			findings = append(findings, Finding{
				Code: InertTrackerBindingCode,
				Message: fmt.Sprintf("tracker reference %s on matter %s is recorded as provenance only; wip will not push tracker updates",
					ref, matter.Locator),
			})
		}
	}
	return findings, nil
}
