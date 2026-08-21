package scheduler

// The Orchestrator loop core (MODEL §6): one pass over one Run — the single
// dispatch path. The loop derives the frontier (frontier.go), engages Ready
// work up to the Run cap, opens the per-Matter Dispatch claim (F4), spawns
// roles into the brackets it opens, applies D30's configured policy to
// contended, blocked, and failed Matters (run.skipped, one per skip), parks
// Matters awaiting human-owned gates as the third normal outcome (D60), and
// closes the Run with run.finished only when nothing further can ever be
// dispatched.
//
// The loop is engine-only. Surface-3's record stands final (S6): no CLI verb
// starts, resumes, or drives a Run; the callers are the agent porcelain and
// tests. Every mutation below is one committed event — the engine is the
// verb, and store.Commit remains the only write path.

import (
	"context"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/runlock"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wiperr"
)

// Handling is D30's per-condition policy: what the loop does when a Matter
// is contended, blocked, or failed.
type Handling int

// The three handlings. Skip is the default everywhere (D30). Ask defers to
// the Hooks.Ask seam and degrades to Halt when no asker is wired — refusing
// to invent an answer is the only honest default for an unattended loop.
const (
	HandleSkip Handling = iota
	HandleHalt
	HandleAsk
)

// Policy is the configured handling per condition (D30: skip and continue by
// default; halting or asking configurable).
type Policy struct {
	Contention Handling
	Blocked    Handling
	Failed     Handling
}

// StepSpec is one Step the Researcher authors into an unplanned Matter.
type StepSpec struct {
	Title string
}

// Hooks are the loop's seams to intelligence. Work does one bounded piece of
// work — a Builder's turn — and its error return is the "failed" condition.
// Plan, when set, is the Researcher's just-in-time workplan flow (MODEL
// §8.1): handed an unplanned member Matter, it returns the Step list to be
// born through verbs; nil Plan (or an empty list) means the Matter is worked
// as its own smallest node (D2). Ask resolves HandleAsk; nil Ask halts.
// PostSeal runs after a Matter finish commits. The hook must re-read the
// Matter and return unless it is sealed. It is the injection point for
// provider-neutral alignment reads and performs no scheduler write.
type Hooks struct {
	Work     func(ctx context.Context, node store.Node) error
	Plan     func(ctx context.Context, matter store.Node) ([]StepSpec, error)
	Ask      func(matter store.Node, reason store.RunSkipReason) Handling
	PostSeal func(ctx context.Context, matter store.Node)
}

// State is where one pass left the Run.
type State string

// The three pass outcomes. Finished closed the Run (run.finished). StandingBy
// left it open: members are parked at human-owned gates (D60) or skipped, and
// a later pass or an explicit stand-down decides. Halted left it open because
// the configured policy said stop.
const (
	StateFinished   State = "finished"
	StateStandingBy State = "standing-by"
	StateHalted     State = "halted"
)

// Skip is one run.skipped emission.
type Skip struct {
	Matter store.Node
	Reason store.RunSkipReason
}

// Outcome reports one pass.
type Outcome struct {
	State State
	// HaltedOn is the Matter and reason that stopped the pass, when Halted.
	HaltedOn *Skip
	// Worked is every node the loop engaged and Work completed, in order.
	Worked []store.Node
	// Skipped is every run.skipped this pass emitted.
	Skipped []Skip
	// Parked is every member Done but unsealed, awaiting a human-owned gate.
	Parked []store.Node
}

// Orchestrate runs one Orchestrator pass over an open Run.
//
// driver is who invoked the pass (the spawner of the Orchestrator role);
// env is the acting tier context, whose Worktree must hold an open plain
// bracket (`wip refresh`) for the Orchestrator role to bind to (D59). The
// pass holds the Run's advisory lock for its whole duration — the
// coordinator holds it while dispatch-capable (S2) — and releases it on
// return, whatever the outcome.
func Orchestrate(ctx context.Context, s *store.Store, driver store.Actor, env store.Env, runID string, runCap int, policy Policy, hooks Hooks) (Outcome, error) {
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
		return Outcome{}, wiperr.New("refusal.run-closed", fmt.Sprintf("Run %s is closed", runID))
	}
	if run.Clone != env.Clone {
		return Outcome{}, wiperr.New("refusal.run-stranded",
			fmt.Sprintf("Run %s is owned by Clone %s; a non-owning Clone may only stand it down (F9)", runID, run.Clone))
	}
	lock, err := runlock.Acquire(runID)
	if err != nil {
		if err == runlock.ErrHeld {
			return Outcome{}, wiperr.New("refusal.run-live", fmt.Sprintf("Run %s is live under another coordinator", runID))
		}
		return Outcome{}, err
	}
	defer func() { _ = lock.Release() }()

	loop := &pass{
		s: s, driver: driver, env: env, run: run, cap: runCap,
		policy: policy, hooks: hooks,
		claims:  map[string]string{},
		skipped: map[string]bool{},
	}
	return loop.execute(ctx)
}

// pass is one Orchestrate invocation's working state.
type pass struct {
	s      *store.Store
	driver store.Actor
	env    store.Env
	run    store.Run
	cap    int
	policy Policy
	hooks  Hooks

	orchestrator string // the Orchestrator role instance
	// resume marks a Resume pass: In Progress leaves in the frozen set are
	// orphans of the interrupted coordinator (their claims were reaped, and
	// the lock proves no one else is working) and become eligible again —
	// worked in place, with no second start event (F8).
	resume bool
	// claims maps a member Matter to its open claim Dispatch this pass.
	claims map[string]string
	// skipped marks members this pass will not reconsider.
	skipped map[string]bool

	out Outcome
}

func (p *pass) execute(ctx context.Context) (Outcome, error) {
	// The Orchestrator's own instance binds to the driving worktree's plain
	// bracket — the one dispatch this pass runs *from*, as opposed to the
	// claim dispatches it runs *over*.
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

	if err := p.engage(ctx); err != nil {
		return p.out, err
	}
	if p.out.State == StateHalted {
		return p.out, nil
	}
	return p.out, p.settle(ctx)
}

// engage is the driving loop: derive, select, work, until the frontier is
// dry or the policy halts the pass.
func (p *pass) engage(ctx context.Context) error {
	for {
		fr, err := Derive(ctx, p.s.View, p.run, p.cap)
		if err != nil {
			return err
		}
		ready := make([]store.Node, 0, len(fr.Ready))
		for _, n := range fr.Ready {
			if !p.skipped[n.Matter] {
				ready = append(ready, n)
			}
		}
		// Slot accounting ignores skipped members: a failed Matter's
		// half-done node is no longer work this Run has engaged (G4) — its
		// claim was released with the skip — and letting it hold a slot
		// would starve the rest of the Batch.
		engaged := 0
		for _, n := range fr.Engaged {
			if !p.skipped[n.Matter] {
				engaged++
			}
		}
		slots := p.cap - engaged
		if slots < 0 {
			slots = 0
		}
		selected := Select(ready, slots)
		if p.resume {
			// Orphans first: they already hold their engagement, so they
			// consume no slot and re-deriving after each keeps the
			// accounting honest.
			var orphans []store.Node
			for _, n := range fr.Engaged {
				if !p.skipped[n.Matter] {
					orphans = append(orphans, n)
				}
			}
			selected = append(orphans, selected...)
		}
		if len(selected) == 0 {
			return nil
		}
		for _, node := range selected {
			progressed, err := p.engageOne(ctx, node)
			if err != nil {
				return err
			}
			if p.out.State == StateHalted {
				return nil
			}
			if progressed {
				// Work moved the graph; re-derive before the next pick so a
				// selection never acts on a frontier it has itself changed.
				break
			}
		}
	}
}

// engageOne engages one selected node: claim, plan if unplanned, start,
// work, complete. It reports whether it changed durable state (worked,
// planned, or skipped) — false only on a no-op.
func (p *pass) engageOne(ctx context.Context, node store.Node) (bool, error) {
	matter, err := p.s.Node(ctx, node.Matter)
	if err != nil {
		return false, err
	}
	claim, ok := p.claims[matter.ID]
	if !ok {
		claim, err = p.openClaim(ctx, matter)
		if err != nil {
			return false, err
		}
		if claim == "" {
			return true, nil // contended: skipped or halted inside openClaim
		}
		p.claims[matter.ID] = claim
	}

	// The Researcher's just-in-time step (MODEL §8.1): an unplanned member
	// picked for dispatch gets its Step list authored at that moment,
	// through verbs, before any Builder touches it.
	if node.ID == matter.ID && matter.Lifecycle == store.Planned && p.hooks.Plan != nil {
		planned, err := p.plan(ctx, matter, claim)
		if err != nil {
			return false, err
		}
		if planned {
			return true, nil // the frontier is now the authored Steps
		}
	}

	if node.Lifecycle == store.Planned {
		if err := p.start(ctx, node); err != nil {
			return false, err
		}
	}
	builder, err := p.spawnRole(ctx, store.RoleOrchestrator.Actor(), claim, store.RoleBuilder)
	if err != nil {
		return false, err
	}
	if workErr := p.hooks.Work(ctx, node); workErr != nil {
		// The claim bracket ends with the failure; the open Builder inside
		// it is reaped by the close (D59). The node stays In Progress — the
		// skip is about the Matter this pass, not about rewriting its state.
		if err := p.closeClaim(ctx, matter.ID); err != nil {
			return false, err
		}
		return true, p.skip(ctx, matter, store.RunSkipFailed, p.policy.Failed)
	}
	if err := p.finish(ctx, node, store.RoleBuilder.Actor()); err != nil {
		return false, err
	}
	if err := p.closeRole(ctx, builder, store.RoleBuilder); err != nil {
		return false, err
	}
	p.out.Worked = append(p.out.Worked, node)
	if node.ID == matter.ID {
		return true, p.closeClaim(ctx, matter.ID)
	}
	return true, p.completeContainers(ctx, matter)
}

// openClaim opens the Matter's claim Dispatch (F4). An existing open
// Dispatch elsewhere is contention, handled per policy; "" with nil error
// means the Matter was skipped (or the pass halted).
func (p *pass) openClaim(ctx context.Context, matter store.Node) (string, error) {
	var id string
	_, err := p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type: store.TypeDispatchOpened, Subject: id,
			Payload: store.DispatchOpened{Run: p.run.ID, Matter: matter.ID},
		}}, nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "refusal.dispatch-contention") {
			return "", p.skip(ctx, matter, store.RunSkipContention, p.policy.Contention)
		}
		return "", err
	}
	return id, nil
}

func (p *pass) closeClaim(ctx context.Context, matterID string) error {
	claim, ok := p.claims[matterID]
	if !ok {
		return nil
	}
	delete(p.claims, matterID)
	_, err := p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeDispatchClosed, Subject: claim,
			Payload: store.DispatchClosed{Reason: store.CloseCompleted},
		}}, nil
	})
	return err
}

// plan is the Researcher flow: spawn into the claim bracket, birth the Step
// list through the same event path every verb uses, close. It reports
// whether any Step was authored.
func (p *pass) plan(ctx context.Context, matter store.Node, claim string) (bool, error) {
	specs, err := p.hooks.Plan(ctx, matter)
	if err != nil {
		return false, err
	}
	if len(specs) == 0 {
		return false, nil
	}
	researcher, err := p.spawnRole(ctx, store.RoleOrchestrator.Actor(), claim, store.RoleResearcher)
	if err != nil {
		return false, err
	}
	for i, spec := range specs {
		locator := fmt.Sprintf("step-%02d", i+1)
		_, err := p.s.Commit(ctx, p.req(store.RoleResearcher.Actor()), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
			sortKey, err := tx.NextSortKey(ctx, matter.ID)
			if err != nil {
				return nil, err
			}
			return []store.Draft{{
				Type: store.TypeStepCreated, Subject: tx.NewID(),
				Payload: store.NodeBirth{Title: spec.Title, Locator: locator, Parent: matter.ID, SortKey: sortKey},
			}}, nil
		})
		if err != nil {
			return false, err
		}
	}
	return true, p.closeRole(ctx, researcher, store.RoleResearcher)
}

// start moves a node to In Progress with the D57 cascade: any Planned
// ancestor starts first, in root-first order, marked Cascade.
func (p *pass) start(ctx context.Context, node store.Node) error {
	_, err := p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(ctx context.Context, tx *store.Tx) ([]store.Draft, error) {
		fresh, err := tx.Node(ctx, node.ID)
		if err != nil {
			return nil, err
		}
		if fresh.Lifecycle != store.Planned {
			return nil, fmt.Errorf("scheduler: %s is %s, not Planned", fresh.Locator, fresh.Lifecycle)
		}
		chain, err := ancestorsRootFirst(ctx, tx.View, fresh)
		if err != nil {
			return nil, err
		}
		var drafts []store.Draft
		for _, ancestor := range chain {
			if ancestor.Lifecycle != store.Planned {
				continue
			}
			drafts = append(drafts, store.Draft{
				Type:    startEvent(ancestor.Kind),
				Subject: ancestor.ID,
				Payload: store.Transition{From: store.Planned, To: store.InProgress, Cascade: true},
			})
		}
		drafts = append(drafts, store.Draft{
			Type:    startEvent(fresh.Kind),
			Subject: fresh.ID,
			Payload: store.Transition{From: store.Planned, To: store.InProgress},
		})
		return drafts, nil
	})
	return err
}

func (p *pass) finish(ctx context.Context, node store.Node, actor store.Actor) error {
	_, err := p.s.Commit(ctx, p.req(actor), func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type:    finishEvent(node.Kind),
			Subject: node.ID,
			Payload: store.Transition{From: store.InProgress, To: store.Done},
		}}, nil
	})
	if err == nil && node.Kind == store.ScaleMatter && p.hooks.PostSeal != nil {
		p.hooks.PostSeal(ctx, node)
	}
	return err
}

// completeContainers finishes groupings whose interior is fully Done —
// deepest first, the member Matter last — and closes the Matter's claim when
// the Matter itself finishes.
func (p *pass) completeContainers(ctx context.Context, matter store.Node) error {
	nodes, err := p.s.MatterNodes(ctx, matter.ID)
	if err != nil {
		return err
	}
	hasChild := map[string]bool{}
	for _, n := range nodes {
		if n.Parent != "" {
			hasChild[n.Parent] = true
		}
	}
	// Repeatedly close the deepest completable grouping until none is left:
	// the node list is small and the loop is clearer than a topological sort.
	for {
		closed := false
		for _, n := range nodes {
			fresh, err := p.s.Node(ctx, n.ID)
			if err != nil {
				return err
			}
			if fresh.Lifecycle != store.InProgress || !hasChild[fresh.ID] {
				continue
			}
			done, err := p.interiorDone(ctx, fresh.ID)
			if err != nil {
				return err
			}
			if !done {
				continue
			}
			if err := p.finish(ctx, fresh, store.RoleOrchestrator.Actor()); err != nil {
				return err
			}
			closed = true
			if fresh.ID == matter.ID {
				if err := p.closeClaim(ctx, matter.ID); err != nil {
					return err
				}
			}
		}
		if !closed {
			return nil
		}
	}
}

func (p *pass) interiorDone(ctx context.Context, parent string) (bool, error) {
	children, err := p.s.Children(ctx, parent)
	if err != nil {
		return false, err
	}
	for _, c := range children {
		if c.Lifecycle == store.Done || c.Lifecycle == store.Canceled {
			continue
		}
		return false, nil
	}
	return len(children) > 0, nil
}

// skip emits run.skipped and applies the handling: mark and continue, halt
// the pass, or ask.
func (p *pass) skip(ctx context.Context, matter store.Node, reason store.RunSkipReason, handling Handling) error {
	if handling == HandleAsk {
		if p.hooks.Ask != nil {
			handling = p.hooks.Ask(matter, reason)
		} else {
			handling = HandleHalt
		}
	}
	_, err := p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeRunSkipped, Subject: matter.ID,
			Payload: store.RunSkipped{Run: p.run.ID, Reason: reason},
		}}, nil
	})
	if err != nil {
		return err
	}
	entry := Skip{Matter: matter, Reason: reason}
	p.out.Skipped = append(p.out.Skipped, entry)
	p.skipped[matter.ID] = true
	if handling == HandleHalt {
		p.out.State = StateHalted
		p.out.HaltedOn = &entry
	}
	return nil
}

// settle classifies the pass end: emit blocked skips for members the pass
// never reached, park Done-but-unsealed members (D60), close claims left
// open, and finish the Run only when every member is Done.
func (p *pass) settle(ctx context.Context) error {
	members, err := p.s.RunMatters(ctx, p.run.ID)
	if err != nil {
		return err
	}
	allDone := true
	for _, id := range members {
		matter, err := p.s.Node(ctx, id)
		if err != nil {
			return err
		}
		switch matter.Lifecycle {
		case store.Done:
			sealed, err := p.matterSealed(ctx, matter)
			if err != nil {
				return err
			}
			if !sealed {
				p.out.Parked = append(p.out.Parked, matter)
			}
		case store.Canceled:
			// nothing further to dispatch for it, and nothing to park.
		default:
			allDone = false
			if !p.skipped[matter.ID] {
				if err := p.skip(ctx, matter, store.RunSkipBlocked, p.policy.Blocked); err != nil {
					return err
				}
				if p.out.State == StateHalted {
					return nil
				}
			}
		}
		if err := p.closeClaim(ctx, matter.ID); err != nil {
			return err
		}
	}
	if !allDone || len(p.out.Parked) > 0 {
		// Parked members leave the Run standing by, not failed (D60); a
		// fresh dispatch after the human review re-engages. Skipped members
		// likewise leave the pass without a finished Run.
		p.out.State = StateStandingBy
		return nil
	}
	p.out.State = StateFinished
	_, err = p.s.Commit(ctx, p.req(store.RoleOrchestrator.Actor()), func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeRunFinished, Subject: p.run.ID, Payload: store.RunFinished{}}}, nil
	})
	return err
}

// matterSealed is the D55 pair at Matter scale, where sealed and locally
// complete coincide (D13).
func (p *pass) matterSealed(ctx context.Context, matter store.Node) (bool, error) {
	declared, err := p.s.GateDeclarations(ctx, matter.Repo)
	if err != nil {
		return false, err
	}
	for _, d := range declared {
		if d.Scale != store.ScaleMatter {
			continue
		}
		satisfied, err := p.s.GateSatisfied(ctx, matter.Repo, matter.ID, d.Gate)
		if err != nil {
			return false, err
		}
		if !satisfied {
			return false, nil
		}
	}
	return true, nil
}

// spawnRole and closeRole are direct drafts rather than writesurface calls:
// the writesurface pair binds to the worktree's plain bracket, and the
// loop's roles bind to the claim brackets it opens itself. The actor is the
// spawner, never the role being born: the driver spawns the Orchestrator,
// and the Orchestrator spawns everything it dispatches.
func (p *pass) spawnRole(ctx context.Context, actor store.Actor, dispatch string, name store.RoleName) (string, error) {
	var id string
	_, err := p.s.Commit(ctx, p.req(actor), func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		id = tx.NewID()
		return []store.Draft{{
			Type: store.TypeRoleSpawned, Subject: id,
			Payload: store.RoleSpawned{Dispatch: dispatch, Name: name},
		}}, nil
	})
	return id, err
}

func (p *pass) closeRole(ctx context.Context, id string, name store.RoleName) error {
	role, err := p.s.Role(ctx, id)
	if err != nil || !role.Open {
		return err // already reaped by its bracket closing is not an error
	}
	_, err = p.s.Commit(ctx, p.req(name.Actor()), func(context.Context, *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{
			Type: store.TypeRoleClosed, Subject: id,
			Payload: store.RoleClosed{Reason: store.RoleCloseCompleted},
		}}, nil
	})
	return err
}

func (p *pass) req(actor store.Actor) store.Request {
	return store.Request{Actor: actor, Env: p.env}
}

// ancestorsRootFirst walks a node's Parent chain and returns its live
// ancestors root-first — the order the D57 cascade emits in.
func ancestorsRootFirst(ctx context.Context, v store.View, n store.Node) ([]store.Node, error) {
	var chain []store.Node
	cur := n
	for cur.Parent != "" {
		parent, err := v.Node(ctx, cur.Parent)
		if err != nil {
			return nil, err
		}
		chain = append(chain, parent)
		cur = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func startEvent(kind store.Scale) string {
	switch kind {
	case store.ScaleMatter:
		return store.TypeMatterStarted
	case store.ScaleStage:
		return store.TypeStageStarted
	default:
		return store.TypeStepStarted
	}
}

func finishEvent(kind store.Scale) string {
	switch kind {
	case store.ScaleMatter:
		return store.TypeMatterFinished
	case store.ScaleStage:
		return store.TypeStageFinished
	default:
		return store.TypeStepFinished
	}
}
