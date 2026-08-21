package writesurface

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// OutboxApprove records the human boundary before the first seam call.
func OutboxApprove(ctx context.Context, s *store.Store, actor store.Actor, repo, id string) (store.OutboxEntry, error) {
	return mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxApproved, store.OutboxApproved{})
}

// OutboxDecline records a terminal human disposition.
func OutboxDecline(ctx context.Context, s *store.Store, actor store.Actor, repo, id, reason string) (store.OutboxEntry, error) {
	if strings.TrimSpace(reason) == "" {
		return store.OutboxEntry{}, wiperr.New("validation.missing-reason", "declining an outbox entry needs a reason")
	}
	return mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxDeclined, store.OutboxDeclined{Reason: reason})
}

// OutboxRetry explicitly makes failed or withheld work eligible for another
// seam call. It does not mint a new identity or increment Attempts.
func OutboxRetry(ctx context.Context, s *store.Store, actor store.Actor, repo, id string) (store.OutboxEntry, error) {
	return mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxRetried, store.OutboxRetried{})
}

// WithholdOutbox records a local or provider refusal. attempted is true only
// when a provider call returned the refusal. cause is the stable discriminator;
// reason remains operator-facing prose.
func WithholdOutbox(ctx context.Context, s *store.Store, actor store.Actor, repo, id, reason string, attempted bool, cause store.WithholdCause) error {
	if strings.TrimSpace(reason) == "" {
		return wiperr.New("validation.missing-reason", "withholding an outbox entry needs a reason")
	}
	if cause == "" {
		return wiperr.New("validation.missing-withhold-cause", "withholding an outbox entry needs a cause")
	}
	_, err := mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxWithheld, store.OutboxWithheld{Reason: reason, Attempted: attempted, Cause: cause})
	return err
}

// FailOutbox records a retryable seam failure and returns the entry to queued.
func FailOutbox(ctx context.Context, s *store.Store, actor store.Actor, repo, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return wiperr.New("validation.missing-reason", "a delivery failure needs a reason")
	}
	_, err := mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxDeliveryFailed, store.OutboxDeliveryFailed{Reason: reason})
	return err
}

// FlushOutboxComment records a successful provider-neutral comment delivery.
func FlushOutboxComment(ctx context.Context, s *store.Store, actor store.Actor, repo, id string) error {
	_, err := mutateOutbox(ctx, s, actor, repo, id, store.TypeOutboxFlushed, store.OutboxFlushed{})
	return err
}

// PushTrackerState records a successful conditional state delivery.
func PushTrackerState(ctx context.Context, s *store.Store, actor store.Actor, repo, id, ref string, disposition store.TrackerDisposition, lease string) error {
	if ref == "" || lease == "" {
		return wiperr.New("validation.invalid-tracker-result", "a successful state delivery needs a reference and lease")
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	_, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeTrackerStatePushed, Subject: id,
			Payload: store.TrackerStatePushed{Ref: ref, Disposition: disposition, Lease: lease},
		}}, nil
	})
	return err
}

// ObserveTrackerState records a state candidate that already held at the
// provider. The observation flushes the entry without claiming an external
// write or advancing wip's push record.
func ObserveTrackerState(ctx context.Context, s *store.Store, actor store.Actor, repo, id, ref string, disposition store.TrackerDisposition, lease string) error {
	if ref == "" || lease == "" {
		return wiperr.New("validation.invalid-tracker-result", "an observed state needs a reference and lease")
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	_, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeTrackerStateObserved, Subject: id,
			Payload: store.TrackerStateObserved{Ref: ref, Disposition: disposition, Lease: lease},
		}}, nil
	})
	return err
}

func mutateOutbox(ctx context.Context, s *store.Store, actor store.Actor, repo, id, eventType string, payload any) (store.OutboxEntry, error) {
	if !store.IsIdentityShaped(id) {
		return store.OutboxEntry{}, wiperr.New("validation.invalid-outbox-identity", fmt.Sprintf("outbox entry %q must be a full ULID", id))
	}
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	if _, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: eventType, Subject: id, Payload: payload}}, nil
	}); err != nil {
		return store.OutboxEntry{}, err
	}
	return s.OutboxEntry(ctx, repo, id)
}
