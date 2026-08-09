package scheduler

// Run resume (F8, S2–S4): an explicit engine write from the owning Clone —
// never a CLI verb (S3/S6; MODEL §11's refusal is final). Resume reconstructs
// the Run from durable state, closes orphaned Dispatches as reaped (their
// roles reaped with them, D59), recomputes the frontier, opens fresh claim
// Dispatches, and re-enters the same orchestration loop under the same
// advisory lock, held across the whole resumed bracket (S2).
//
// Completed work is not replayed: a Done node has its completion event and
// stays Done. Work with no completion event — a leaf left In Progress by the
// interrupted coordinator — becomes eligible again: the resumed pass works
// it in place, without a second start event, because the start it has is
// real history.

import (
	"context"
	"fmt"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Resume resumes an interrupted open Run and drives one full pass over it.
func Resume(ctx context.Context, s *store.Store, driver store.Actor, env store.Env, runID string, runCap int, policy Policy, hooks Hooks) (Outcome, error) {
	if runCap < 1 {
		return Outcome{}, wiperr.New("validation.invalid-cap", "the Run cap is at least 1; 1 is plain sequential (D31)")
	}
	if hooks.Work == nil {
		return Outcome{}, wiperr.New("validation.no-work-hook", "the loop needs a Work hook; the engine drives, it does not think")
	}
	run, err := s.Run(ctx, runID)
	if err != nil {
		return Outcome{}, wiperr.New("validation.unknown-run", fmt.Sprintf("no Run %s", runID))
	}
	if !run.Open {
		return Outcome{}, wiperr.New("refusal.run-closed", fmt.Sprintf("Run %s is closed; a closed Run is never resumed (F2)", runID))
	}
	if run.Clone != env.Clone {
		return Outcome{}, wiperr.New("refusal.run-stranded",
			fmt.Sprintf("Run %s is owned by Clone %s; a non-owning Clone may only stand it down, never adopt it (F9)", runID, run.Clone))
	}
	lock, err := runlock.Acquire(runID)
	if err != nil {
		if err == runlock.ErrHeld {
			return Outcome{}, wiperr.New("refusal.run-live", fmt.Sprintf("Run %s is live; a live Run is not interrupted (S2)", runID))
		}
		return Outcome{}, err
	}
	defer func() { _ = lock.Release() }()

	loop := &pass{
		s: s, driver: driver, env: env, run: run, cap: runCap,
		policy: policy, hooks: hooks,
		claims:  map[string]string{},
		skipped: map[string]bool{},
		resume:  true,
	}
	return loop.executeResume(ctx)
}

func (p *pass) executeResume(ctx context.Context) (Outcome, error) {
	bracket, found, err := p.s.OpenDispatch(ctx, p.env.Worktree)
	if err != nil {
		return p.out, err
	}
	if !found {
		return p.out, wiperr.New("validation.no-open-dispatch",
			"no open dispatch on this worktree for the Orchestrator to bind to; run `wip refresh` first")
	}
	p.orchestrator, err = p.spawnRole(ctx, p.driver, bracket.ID, store.RoleOrchestrator)
	if err != nil {
		return p.out, err
	}
	defer func() { _ = p.closeRole(ctx, p.orchestrator, store.RoleOrchestrator) }()

	// run.resumed first, then the orphan reaping it causes — one command,
	// one chain, exactly the pairing F8 names.
	if err := p.reapOrphans(ctx); err != nil {
		return p.out, err
	}

	if err := p.engage(ctx); err != nil {
		return p.out, err
	}
	if p.out.State == StateHalted {
		return p.out, nil
	}
	return p.out, p.settle(ctx)
}

// reapOrphans emits run.resumed and closes every Dispatch the interrupted
// coordinator left open, reason reaped, each under its own original tier
// dimensions. Roles still open inside them reap with their brackets (D59).
func (p *pass) reapOrphans(ctx context.Context) error {
	_, err := p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		orphans, err := tx.OpenDispatchesForRun(ctx, p.run.ID)
		if err != nil {
			return nil, err
		}
		drafts := []store.Draft{{
			Type: store.TypeRunResumed, Subject: p.run.ID, Payload: store.RunResumed{},
		}}
		for _, orphan := range orphans {
			clone, err := tx.Clone(ctx, orphan.Clone)
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, store.Draft{
				Type: store.TypeDispatchClosed, Subject: orphan.ID,
				Payload: store.DispatchClosed{Reason: store.CloseReaped},
				Env:     &store.Env{Repo: clone.Repo, Clone: orphan.Clone, Worktree: orphan.Worktree},
				Cause:   0,
			})
		}
		return drafts, nil
	})
	return err
}
