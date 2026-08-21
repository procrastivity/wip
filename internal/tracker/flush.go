// Package tracker contains the provider-neutral delivery boundary. It owns no
// provider code, credentials, network client, or provider state vocabulary.
package tracker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Outcome classifies one returned provider result without exposing a provider
// state name to the substrate.
type Outcome string

// Delivery outcomes distinguish write success, observed convergence, retryable
// failure, lease drift, and permanent refusal without exposing
// provider-specific state.
const (
	Delivered        Outcome = "delivered"
	Converged        Outcome = "converged"
	RetryableFailure Outcome = "retryable-failure"
	LeaseMismatch    Outcome = "lease-mismatch"
	PermanentRefusal Outcome = "permanent-refusal"
)

// Result is the provider-neutral result of one seam call. Ref is required for
// creation success. Lease is required for state success.
type Result struct {
	Outcome Outcome
	Ref     string
	Lease   string
	Reason  string
}

// Seam is the complete external boundary. Implementations map the entry's
// provider-neutral payload to a concrete tracker and return one classified
// result. The stable idempotency key and prior lease travel in the entry.
type Seam interface {
	Deliver(context.Context, store.OutboxEntry) (Result, error)
}

// EntryResult reports the durable disposition produced for one entry.
type EntryResult struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Report describes one flush without replacing the durable outbox read.
type Report struct {
	Entries []EntryResult `json:"entries"`
}

type stateCandidate struct {
	entry       store.OutboxEntry
	disposition store.TrackerDisposition
}

// Flush composes approved work, applies the local monotonicity guard, invokes
// only seam, and records exactly one durable result for each returned result.
// Independent entries continue after retryable or permanent provider results.
func Flush(ctx context.Context, s *store.Store, actor store.Actor, repo string, seam Seam) (Report, error) {
	if seam == nil {
		return Report{}, fmt.Errorf("tracker: no provider seam configured")
	}
	entries, err := s.Outbox(ctx, repo)
	if err != nil {
		return Report{}, err
	}

	selected := make(map[string]bool)
	groups := make(map[string][]stateCandidate)
	var report Report
	for _, entry := range entries {
		if entry.State != "approved" {
			continue
		}
		if entry.Kind != "state" {
			selected[entry.ID] = true
			continue
		}
		disposition, parseErr := stateDisposition(entry.Payload)
		if parseErr != nil || entry.Ref == "" {
			reason := "malformed state candidate"
			if parseErr != nil {
				reason += ": " + parseErr.Error()
			}
			if err := writesurface.WithholdOutbox(ctx, s, actor, repo, entry.ID, reason, false, store.CauseMalformedCandidate); err != nil {
				return report, err
			}
			report.Entries = append(report.Entries, EntryResult{ID: entry.ID, State: "withheld", Reason: reason})
			continue
		}
		record, found, err := s.FindTrackerPushRecord(ctx, entry.Ref)
		if err != nil {
			return report, err
		}
		if found && regresses(record.Disposition, disposition) {
			reason := fmt.Sprintf("local regression: %s cannot follow %s", disposition, record.Disposition)
			if err := writesurface.WithholdOutbox(ctx, s, actor, repo, entry.ID, reason, false, store.CauseLocalRegression); err != nil {
				return report, err
			}
			report.Entries = append(report.Entries, EntryResult{ID: entry.ID, State: "withheld", Reason: reason})
			continue
		}
		groups[entry.Ref] = append(groups[entry.Ref], stateCandidate{entry: entry, disposition: disposition})
	}

	for _, candidates := range groups {
		newest := candidates[len(candidates)-1]
		selected[newest.entry.ID] = true
		for _, candidate := range candidates[:len(candidates)-1] {
			reason := "superseded by newer approved state entry " + newest.entry.ID
			if err := writesurface.WithholdOutbox(ctx, s, actor, repo, candidate.entry.ID, reason, false, store.CauseSuperseded); err != nil {
				return report, err
			}
			report.Entries = append(report.Entries, EntryResult{ID: candidate.entry.ID, State: "withheld", Reason: reason})
		}
	}

	for _, entry := range entries {
		if !selected[entry.ID] {
			continue
		}
		result, callErr := seam.Deliver(ctx, entry)
		if callErr != nil {
			result = Result{Outcome: RetryableFailure, Reason: callErr.Error()}
		}
		state, reason, err := recordResult(ctx, s, actor, repo, entry, result)
		if err != nil {
			return report, err
		}
		report.Entries = append(report.Entries, EntryResult{ID: entry.ID, State: state, Reason: reason})
	}
	return report, nil
}

func stateDisposition(payload json.RawMessage) (store.TrackerDisposition, error) {
	var p struct {
		Disposition store.TrackerDisposition `json:"disposition"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	switch p.Disposition {
	case store.TrackerActive, store.TrackerCompleted, store.TrackerCanceled:
		return p.Disposition, nil
	default:
		return "", fmt.Errorf("invalid disposition %q", p.Disposition)
	}
}

func regresses(previous, candidate store.TrackerDisposition) bool {
	if previous == store.TrackerActive {
		return false
	}
	return candidate != previous
}

func recordResult(ctx context.Context, s *store.Store, actor store.Actor, repo string, entry store.OutboxEntry, result Result) (string, string, error) {
	reason := result.Reason
	switch result.Outcome {
	case Delivered, Converged:
		entryState := "flushed"
		if result.Outcome == Converged {
			entryState = "converged"
		}
		switch entry.Kind {
		case "create":
			if result.Ref == "" {
				reason = "provider returned creation success without a reference"
				break
			}
			if err := writesurface.ConfirmBacklogDelegation(ctx, s, actor, repo, entry.ID, result.Ref); err != nil {
				return "", "", err
			}
			return entryState, "", nil
		case "state":
			disposition, err := stateDisposition(entry.Payload)
			if err != nil || result.Lease == "" || (result.Ref != "" && result.Ref != entry.Ref) {
				reason = "provider returned malformed state success"
				break
			}
			if result.Outcome == Converged {
				if err := writesurface.ObserveTrackerState(ctx, s, actor, repo, entry.ID, entry.Ref, disposition, result.Lease); err != nil {
					return "", "", err
				}
			} else {
				if err := writesurface.PushTrackerState(ctx, s, actor, repo, entry.ID, entry.Ref, disposition, result.Lease); err != nil {
					return "", "", err
				}
			}
			return entryState, "", nil
		case "comment":
			if err := writesurface.FlushOutboxComment(ctx, s, actor, repo, entry.ID); err != nil {
				return "", "", err
			}
			return entryState, "", nil
		default:
			reason = "provider returned success for an unknown outbox kind"
		}
		if err := writesurface.WithholdOutbox(ctx, s, actor, repo, entry.ID, reason, true, store.CauseMalformedProviderSuccess); err != nil {
			return "", "", err
		}
		return "withheld", reason, nil
	case RetryableFailure:
		if reason == "" {
			reason = "provider reported a retryable failure"
		}
		if err := writesurface.FailOutbox(ctx, s, actor, repo, entry.ID, reason); err != nil {
			return "", "", err
		}
		return "queued", reason, nil
	case LeaseMismatch, PermanentRefusal:
		if reason == "" {
			reason = "provider refused the delivery"
		}
		cause := store.CauseLeaseMismatch
		if result.Outcome == PermanentRefusal {
			cause = store.CausePermanentRefusal
		}
		if err := writesurface.WithholdOutbox(ctx, s, actor, repo, entry.ID, reason, true, cause); err != nil {
			return "", "", err
		}
		return "withheld", reason, nil
	default:
		reason = "provider returned an unknown outcome"
		if err := writesurface.WithholdOutbox(ctx, s, actor, repo, entry.ID, reason, true, store.CauseUnknownOutcome); err != nil {
			return "", "", err
		}
		return "withheld", reason, nil
	}
}
