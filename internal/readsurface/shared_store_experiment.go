package readsurface

import (
	"context"
	"fmt"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// ExperimentPeriod groups source-reported evidence for one known origin.
// Its bounds are observations, not duration, causal order, or remote liveness.
type ExperimentPeriod struct {
	Origin                          string
	FirstReportedAt, LastReportedAt time.Time
	ActivityIDs                     []string
}

// ExperimentActivityPreview keeps work observations and sync diagnostics apart.
type ExperimentActivityPreview struct {
	Activities []store.ExperimentActivity
	Periods    []ExperimentPeriod
}

// PreviewExperimentActivity is opt-in; default Derive/status remain untouched.
// Unlike Derive's native-order contract, this consumes an explicit source-time
// display ordering and tracks each origin separately. Unknown origins have no
// inferred session. Abnormal administrative closes retain Derive's discount.
func PreviewExperimentActivity(ctx context.Context, s *store.Store, localOrigin string, idleGap time.Duration) (ExperimentActivityPreview, error) {
	return previewExperimentActivity(ctx, s, localOrigin, "", idleGap)
}

// PreviewExchangeActivity scopes both source observations and their periods to
// one native Repo. Other Repo work is not part of a pairing diagnostic.
func PreviewExchangeActivity(ctx context.Context, s *store.Store, localOrigin, repo string, idleGap time.Duration) (ExperimentActivityPreview, error) {
	return previewExperimentActivity(ctx, s, localOrigin, repo, idleGap)
}

func previewExperimentActivity(ctx context.Context, s *store.Store, localOrigin, repo string, idleGap time.Duration) (ExperimentActivityPreview, error) {
	if idleGap <= 0 {
		return ExperimentActivityPreview{}, fmt.Errorf("experiment: idle gap must be positive")
	}
	activities, err := s.ExchangeActivitySnapshot(ctx, localOrigin, repo)
	if err != nil {
		return ExperimentActivityPreview{}, err
	}
	preview := ExperimentActivityPreview{}
	last := map[string]int{}
	for _, a := range activities {
		preview.Activities = append(preview.Activities, a)
		if a.Origin == "" || isDiscountedClose(a.Event) {
			continue
		}
		i, ok := last[a.Origin]
		if !ok || a.Event.OccurredAt.Sub(preview.Periods[i].LastReportedAt) >= idleGap {
			i = len(preview.Periods)
			preview.Periods = append(preview.Periods, ExperimentPeriod{Origin: a.Origin, FirstReportedAt: a.Event.OccurredAt})
			last[a.Origin] = i
		}
		preview.Periods[i].LastReportedAt = a.Event.OccurredAt
		preview.Periods[i].ActivityIDs = append(preview.Periods[i].ActivityIDs, a.Event.ID)
	}
	return preview, nil
}
