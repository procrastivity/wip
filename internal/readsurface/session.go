package readsurface

// Step-05: `wip session` — a derived view over the event log covering a
// contiguous working period (MODEL §2.4), the temporal tense the founding
// questions add: what was done recently. Never persisted, creates or closes
// nothing, drives no behaviour, and gates nothing (D17): every call recomputes
// it from the log, and this file writes no row and no event. Unlike `status`,
// Session takes no tier scope — it derives host-wide, always (D66): a working
// period is a fact about the person, and Batch-subject events (null repo)
// belong to sessions too.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/procrastivity/wip/internal/store"
)

// Session is one contiguous working period: every event from Start to End,
// inclusive, with the idle-gap threshold that closed it on either side (the
// threshold itself lives in the caller's argument, not in this struct — a
// Session is a fact about one derivation, not about the parameter that
// produced it).
type Session struct {
	Start, End time.Time
	// Events is every event — including discounted dispatch closes — whose
	// OccurredAt falls within [Start, End]. A discounted close can still
	// fall *inside* a session when evidence surrounds it; it just never
	// supplies the boundary (see Derive).
	Events []store.Event
}

// Derive groups the whole event log into working periods, splitting wherever
// the inter-event idle gap meets or exceeds idleGap (MODEL §2.4's query
// parameter). Abnormal dispatch closes are discounted (D59): a
// dispatch.closed event whose payload.reason is superseded or reaped carries
// an administrative occurred_at, not evidence of work, and neither extends a
// working period nor bridges an idle gap — so the boundaries below are
// computed over every event *except* those, and only afterward is the full
// log (discounted events included) partitioned back into the sessions that
// resulted, so an administrative close that happens to fall inside a real
// working window is still shown as part of it.
func Derive(ctx context.Context, s *store.Store, idleGap time.Duration) ([]Session, error) {
	events, err := s.Events(ctx)
	if err != nil {
		return nil, err
	}

	evidence := make([]store.Event, 0, len(events))
	for _, ev := range events {
		if isDiscountedClose(ev) {
			continue
		}
		evidence = append(evidence, ev)
	}
	if len(evidence) == 0 {
		return nil, nil
	}

	var bounds []Session
	cur := Session{Start: evidence[0].OccurredAt, End: evidence[0].OccurredAt}
	for i := 1; i < len(evidence); i++ {
		gap := evidence[i].OccurredAt.Sub(evidence[i-1].OccurredAt)
		if gap >= idleGap {
			bounds = append(bounds, cur)
			cur = Session{Start: evidence[i].OccurredAt, End: evidence[i].OccurredAt}
			continue
		}
		cur.End = evidence[i].OccurredAt
	}
	bounds = append(bounds, cur)

	// Second pass: attach every event (evidence and discounted alike) whose
	// occurred_at falls inside a session's window. events is already in
	// total (= chronological) order, so a single pointer walked forward
	// across both slices is enough — no event is ever attached to more than
	// one session, since the windows are disjoint by construction.
	idx := 0
	for i := range bounds {
		for idx < len(events) && events[idx].OccurredAt.Before(bounds[i].Start) {
			idx++ // belongs to no session: cannot happen (bounds start at an
			// evidence event) but guards against a malformed idleGap of 0
			// producing a window narrower than its own boundary event.
		}
		for idx < len(events) && !events[idx].OccurredAt.After(bounds[i].End) {
			bounds[i].Events = append(bounds[i].Events, events[idx])
			idx++
		}
	}
	return bounds, nil
}

func isDiscountedClose(ev store.Event) bool {
	if ev.Type != store.TypeDispatchClosed {
		return false
	}
	var p store.DispatchClosed
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return false
	}
	return p.Reason == store.CloseSuperseded || p.Reason == store.CloseReaped
}
