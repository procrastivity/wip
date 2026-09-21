package writesurface

// Tracker plumbing, step-04: operator-authored tracker proposals. `wip
// plumbing tracker propose create|comment|state` (the verb package is a later
// step) emits exactly one `tracker.adhoc-proposed`, which the fold turns into
// exactly one queued outbox candidate — a candidate a human wrote, rather than
// one a Matter's lifecycle produced.
//
// Two postures this file inherits rather than invents:
//
//   - No backend-configured precondition. A proposal with no provider behind it
//     queues exactly as BacklogDelegate's create does; absence bites at flush,
//     which is the one place that can actually tell (workplan D4). Requiring a
//     backend here would make the propose verbs refuse in the one situation
//     where recording the intent is most useful.
//   - Propose never approves. Every entry these functions return is `queued`,
//     and the human boundary stays exactly where OutboxApprove puts it
//     (workplan D15, MODEL §10).
//
// Validation is per kind and mirrors the fold's own rules (projectAdhocCandidate
// in internal/store/project.go). The duplication is intentional and one-way: the
// fold is the invariant and refuses a malformed proposal wherever it comes from,
// while these checks exist to turn the refusal into a coded, operator-facing
// wiperr before a transaction is opened. If the two ever disagree, the fold wins
// and this file is the bug.
//
// An error any of the three exported functions return after the commit has
// already succeeded — the read-back in proposeTracker failing to find the
// minted candidate — does not mean the proposal was refused: the event and
// its queued candidate are already durably committed. Only the coded
// validation refusals above guarantee nothing was written.

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// ProposeTrackerCreate proposes a new external item. It names no reference,
// because the item it proposes does not exist until a provider answers; the
// reference arrives later, on the creation confirmation.
func ProposeTrackerCreate(ctx context.Context, s *store.Store, actor store.Actor, repo, title, detail string) (store.OutboxEntry, error) {
	if title == "" {
		return store.OutboxEntry{}, wiperr.New("validation.missing-title", "a proposed tracker item needs a title")
	}
	return proposeTracker(ctx, s, actor, repo, store.TrackerAdhocProposed{
		Kind: store.AdhocCreate, Title: title, Detail: detail,
	})
}

// ProposeTrackerComment proposes free body text on an existing external item —
// prose an operator wrote, with none of the Stage-closure narration the
// lifecycle comments carry.
func ProposeTrackerComment(ctx context.Context, s *store.Store, actor store.Actor, repo, ref, body string) (store.OutboxEntry, error) {
	if ref == "" {
		return store.OutboxEntry{}, wiperr.New("validation.missing-reference", "a proposed comment needs the reference it comments on")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return store.OutboxEntry{}, wiperr.New("validation.missing-body", "a proposed comment needs a body; whitespace alone does not count")
	}
	return proposeTracker(ctx, s, actor, repo, store.TrackerAdhocProposed{
		Kind: store.AdhocComment, Ref: ref, Body: body,
	})
}

// ProposeTrackerState proposes one provider-neutral disposition on an existing
// external item. The candidate it queues is byte-identical to the one a
// Matter's own boundary would have queued, so the flush path cannot tell them
// apart and does not need to.
func ProposeTrackerState(ctx context.Context, s *store.Store, actor store.Actor, repo, ref string, disposition store.TrackerDisposition) (store.OutboxEntry, error) {
	if ref == "" {
		return store.OutboxEntry{}, wiperr.New("validation.missing-reference", "a proposed state change needs the reference it moves")
	}
	switch disposition {
	case store.TrackerActive, store.TrackerCompleted, store.TrackerCanceled:
	default:
		return store.OutboxEntry{}, wiperr.New("validation.invalid-disposition",
			fmt.Sprintf("%q is not active, completed or canceled", disposition))
	}
	return proposeTracker(ctx, s, actor, repo, store.TrackerAdhocProposed{
		Kind: store.AdhocState, Ref: ref, Disposition: disposition,
	})
}

// proposeTracker commits the one event the three verbs share and reads back the
// candidate it minted.
//
// The subject is the Repo, not a node: a proposal is Repo-tier and node-less,
// because it is not a consequence of any node's lifecycle (the fold refuses any
// other subject). That is also why the born entry has to be found by its birth
// event — a Repo-subject event mints a row whose identity is derived inside the
// fold, so there is no id for the caller to have minted in advance the way
// BacklogDelegate mints its outbox id.
func proposeTracker(ctx context.Context, s *store.Store, actor store.Actor, repo string, p store.TrackerAdhocProposed) (store.OutboxEntry, error) {
	req := store.Request{Actor: actor, Env: store.Env{Repo: repo}}
	events, err := s.Commit(ctx, req, func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeTrackerAdhocProposed, Subject: repo, Payload: p}}, nil
	})
	if err != nil {
		return store.OutboxEntry{}, err
	}
	// Commit returns one event per draft, in order, or an error (store.go:428);
	// one draft went in, so events[0] is the proposal.
	return s.OutboxEntryByBirth(ctx, repo, events[0].ID)
}
