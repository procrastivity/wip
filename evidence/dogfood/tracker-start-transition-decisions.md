# Tracker start-transition decisions

2026-08-21. This record decides when a bound Matter first proposes an active
tracker state. It closes the trigger questions assigned to
`tracker-start-transition`. The local labels A1-A8 (A for activation) preserve
their order without allocating D-numbers early. Each label is one operative
decision, so a later model append allocates one D-number per row.

Related records: `push-model-decisions.md` (T1-T10),
`reference-model-decisions.md` (R1-R2), and the Workplans for
`tracker-state-guard-read` and `tracker-gate-read`. Their one-way authority,
outbox, guard-read, reference-set, and push-level rules remain settled inputs.
This record narrows when a Planned Matter first becomes active outside wip.

## Operative decisions

| Local | Operative decision |
|---|---|
| A1 | **`matter.started` is the activation trigger for a bound Matter.** Starting the Matter directly or starting a descendant that cascades the Matter start queues the same active state candidate. Step creation, Workplan writing, first Step finish, pause, and resume do not replace or add activation triggers. |
| A2 | **An all-Planned reference has no provider-neutral state candidate.** Binding, rebinding, or recomputing a reference whose bound Matters are all Planned records the membership but queues no state entry. Planned is not active, and wip does not invent a provider-neutral Backlog disposition. |
| A3 | **Late binding stays current-state-only after a Matter leaves Planned.** Binding an In Progress Matter queues active. Binding a canceled Matter queues canceled. Binding a sealed Matter queues completed. Earlier lifecycle and narration are never replayed. |
| A4 | **A shared reference activates when any bound Matter starts.** Planned Matters do not hold an active reference backward, and they do not activate an all-Planned reference. After activation, the existing shared-reference terminal aggregation rules remain unchanged. |
| A5 | **Forge automation gets no ownership switch.** Wip always submits an eligible approved candidate through the same guarded provider seam. When a forge has already moved the tracker to the candidate or a compatible state beyond it, the guard read returns `Converged` and performs no state write. |
| A6 | **Every automatic state delivery requires a guard read.** A provider that cannot read and classify current state immediately before delivery refuses state entries. Wip does not replace the guard with an unconditional write, a remembered state, or a provider-specific exception. |
| A7 | **Never-backward remains a two-part rule.** The flush layer withholds a local regression before the provider call. The provider then reads current state and either delivers forward, reports convergence, or refuses drift. A competing or unsafe state stays visible and unchanged. |
| A8 | **Backend configuration still opts a Repo into boundary pushes unless the level is explicitly `off`.** With no backend and no explicit level, the effective level is `off`. A configured backend with no explicit level uses `boundary`. `narrated` has the same Matter activation behavior and adds only the already-defined Stage comments. Configuration changes stay lazy and non-retroactive. |

## Rationale and consequences

### A1 - Matter start is the one activation boundary

The Matter owns its tracker references, and the tracker issue mirrors the
Matter-scale lifecycle. `matter.started` is therefore the first event that can
truthfully propose active. The event exists whether the operator starts the
Matter directly or starts a descendant and lets lifecycle cascade start its
Planned ancestors.

This trigger can occur before a branch push or pull request. That difference is
intentional. Starting a Matter says that work on its outcome has begun. Writing
the Workplan and choosing Steps are part of that work, even when the forge
cannot see them yet.

Moving the trigger to the first Step start would add no useful precision. A
Matter can be the smallest node and have no Steps. A Matter can also contain
real planning work before its first Step starts. The cascade path already emits
`matter.started` when a Step is the first node started, so the existing trigger
covers that case without a second rule.

The first Step finish is too late. It would keep the tracker in Backlog after
work has produced a completed unit. Step creation and Workplan writing are also
poor triggers because they describe plan shape, not lifecycle. Pause and resume
do not activate a Planned Matter and remain tracker-silent under T3.

### A2 - Planned membership is not active work

Before this decision, reference addition computed the shared aggregate from all
nonterminal Matters. That rule treated Planned as active. At boundary level, a
bind on a Planned Matter could therefore queue an active candidate before
`matter.started`. The effective trigger was bind, despite the transition code
also queuing on Matter start.

A2 removes that contradiction. Binding still records the reference set
immediately. The projection queues no state candidate while all bound Matters
remain Planned. When one Matter starts, its `matter.started` event
recomputes the reference and queues active.

This record does not add `planned` to the provider-neutral tracker vocabulary.
Providers use different names and shapes for Backlog, triage, draft, or an
unstarted item. Wip has no need to force one of those states during binding. No
candidate is the accurate outward statement until work starts.

The rule also keeps configuration lazy. Setting a backend or changing a level
does not revisit existing Planned bindings. The next qualifying
`matter.started` event carries the effective level and creates the candidate.

### A3 - Late binding synchronizes current work, not history

T7 and R2 require a new reference to receive current state without replaying
old boundaries. A2 changes only the all-Planned case. Once the Matter leaves
Planned, its current disposition is meaningful outside wip:

- In Progress proposes active.
- Canceled proposes canceled.
- Done and sealed proposes completed.
- Done but unsealed does not propose completed until the seal event makes the
  completion boundary true.

The candidate remains subject to approval, flush composition, local
monotonicity, and the provider guard read. A late bind does not claim that wip
caused the tracker state. It proposes the current aggregate and lets the guard
classify the outside state.

### A4 - Shared references activate on the first real start

T9 gives a shared reference one monotonic aggregate across all bound Matters.
A4 refines the initial part of that aggregate. A set of only Planned Matters
has membership but no active disposition. The first Matter start changes the
aggregate to active and queues one candidate for that causal event.

Additional Planned Matters do not change an already-active reference. They do
not hold the reference in Backlog, and binding them does not queue a duplicate
active candidate. Another Matter start may queue an active candidate under the
normal event-derived rule. Flush composition and provider convergence remove
any redundant outside write.

After activation, T9 remains in force. The reference stays active while any
bound Matter is nonterminal. When all bound Matters terminate, at least one
sealed Matter proposes completed. Wip proposes cancellation only when every
Matter ends as canceled.

### A5 - Detect forge progress with the read that delivery already needs

A forge integration and wip can both observe the same boundary without needing
static ownership configuration. The provider already reads current tracker
state to guard each state delivery. That read can identify a tracker that is at
the requested state or at a compatible state beyond it.

In that case, the provider returns `Converged`. Flush terminates the outbox
entry and records an observation rather than a push. The provider performs no
state write. Wip therefore stands down when a branch push or pull request has
already advanced the issue.

A forge-owner switch would make repository setup responsible for facts that can
change outside wip. The switch could become stale when someone adds, removes,
disables, or limits an integration to some branches. A read at delivery time
answers the actual question for each candidate and each reference.

Wip does not wait for forge visibility before it queues the candidate. Waiting
would make timing depend on provider-specific branch and pull-request concepts.
The outbox preserves one timing rule: the lifecycle event creates work, a human
approves it, and flush reads before any write.

### A6 - A provider must read before it can write state

State delivery is safe only when the provider can obtain and classify the
current state immediately before a possible write. The read supplies the state
and lease information needed to distinguish these outcomes:

- The tracker is behind, and a forward write is eligible.
- The tracker is already at or beyond the candidate.
- The tracker has incompatible drift.
- The read failed, and wip can retry delivery.

A provider without that read cannot tell a useful transition from a backward
move. It returns a permanent capability refusal for automatic state entries.
Creation and comment support do not imply state support. Manual tracker action
remains available when the provider refuses automatic state delivery.

The rule permits provider-specific state mapping only inside the adapter. Wip
events, candidates, and push records continue to use active, completed, and
canceled.

### A7 - Convergence is not permission to regress

The first guard compares the candidate with wip's durable push record. It
withholds an active candidate that would follow a completed or canceled push.
No provider call occurs for that local regression.

The second guard runs in the provider. A compatible state at or beyond the
candidate returns `Converged`, with no write. An incompatible state or unsafe
lease difference returns the provider's refusal outcome. A failed read returns
a retryable failure. None of these outcomes changes the tracker.

This record does not require every terminal state to count as convergence. The
provider must distinguish a compatible state beyond the candidate from a
competing terminal result. When the provider cannot prove compatibility, it
refuses and leaves the entry visible for a person to resolve.

### A8 - Backend selection remains the coarse opt-in

Selecting a backend is an explicit request to use that provider. The existing
default to `boundary` makes the request useful without a second setup command.
An operator who wants references only as provenance can set the level to
`off`. The bind output already discloses when a reference is inert.

The default does not cause a Planned bind to advance the tracker because A2
removes that candidate. It does cause the next Matter start, cancellation, or
seal to queue the corresponding boundary entry. `narrated` adds Stage closure
comments but does not change the activation trigger.

Level and backend changes remain configuration, not history. They emit no
lifecycle event, queue no entry by themselves, replay no prior start, and do
not rewrite entries that are already queued.

## Rejected alternatives

| Alternative | Reason rejected |
|---|---|
| Keep Planned bind mapped to active. | It makes bind the effective activation trigger and moves the tracker before the Matter starts. |
| Add a provider-neutral Planned or Backlog disposition. | Provider backlog models differ, and wip does not need an outward state before activation. |
| Activate on first Step start. | Matters can have no Steps, planning after Matter start is real work, and descendant start already cascades `matter.started`. |
| Activate on first Step finish. | The tracker would remain in Backlog after work begins and until one unit is already complete. |
| Activate on branch push or pull-request creation. | Those events are forge-specific, are not wip lifecycle events, and do not exist in every workflow. |
| Add `forge-owns-started` configuration. | The value can become stale, duplicates provider capability knowledge, and cannot describe partial forge coverage reliably. |
| Skip all wip state delivery when a forge integration exists. | Wip cannot know that the integration will run for a specific reference. Repos without working automation would never advance. |
| Write first and let the forge correct later. | The write can move an already-advanced tracker backward and violates the guard-read contract. |
| Treat every terminal tracker state as convergence. | Competing completion and cancellation outcomes are not interchangeable. The provider must prove compatibility or refuse. |
| Require an explicit push level after backend selection. | Backend selection already expresses intent, while explicit `off` supplies the provenance-only mode. |

## Scenario matrix

| # | Scenario | Required result |
|---|---|---|
| 1 | No backend and no explicit level | Effective level is `off`. Bind and Matter start queue no state entry. The binding remains provenance. |
| 2 | Backend configured, level unset, Matter Planned | Effective level is `boundary`. Bind records membership and queues no state entry. |
| 3 | Backend configured, level unset, Matter starts | `matter.started` queues one active state candidate for each current reference. |
| 4 | Explicit level `off` with a backend | Bind and start queue no state entry. Changing the level later does not replay the start. |
| 5 | Explicit `boundary` or `narrated` | Both levels queue the same Matter activation candidate. Only `narrated` adds the existing Stage closure comments. |
| 6 | Descendant starts a Planned Matter | The cascade emits `matter.started`, which queues the Matter's active candidates. The child event queues no state candidate. |
| 7 | Active Matter gets a late bind | The new reference gets one active current-state candidate. Earlier events are not replayed. |
| 8 | Canceled or sealed Matter gets a late bind | The new reference gets one canceled or completed current-state candidate, subject to the normal guards. |
| 9 | Done but unsealed Matter gets a late bind | No completion is proposed until its final Matter-scale gate closes and the Matter seals. |
| 10 | Several Planned Matters share one reference | Membership is recorded, but the reference has no state candidate. |
| 11 | One Matter on a shared reference starts | The reference becomes active. Other Planned Matters do not hold it backward. |
| 12 | A second Matter starts on an active shared reference | A causal active candidate can queue. Flush composition or provider convergence prevents a redundant outside write. |
| 13 | No forge automation and tracker is behind | After approval, the provider reads, performs one forward active write, and returns `Delivered` with a new lease. |
| 14 | Forge already moved the tracker to In Progress or In Review | The provider reads a compatible active or later nonterminal state, returns `Converged`, and performs no state write. |
| 15 | Forge already moved the tracker to a compatible terminal state | The provider returns `Converged` only when it can prove that the state is beyond the candidate. It performs no state write. |
| 16 | Tracker has incompatible drift or a competing terminal state | The provider refuses the candidate. The entry stays visible, and the tracker does not change. |
| 17 | Provider cannot read current state | The provider refuses automatic state delivery. It does not perform an unconditional write. |
| 18 | Guard read fails temporarily | Delivery records a retryable failure. A later retry uses the same candidate identity. |
| 19 | Local push record is terminal and a new active candidate appears | Flush withholds the local regression before the provider call. |
| 20 | Backend is removed after an entry is approved | Flush refuses for lack of a provider seam and leaves the approved entry and attempt count unchanged. |

## Implementation obligations

- Refine shared-reference aggregation so an all-Planned bound set has
  membership but no deliverable disposition.
- Make reference addition and replacement skip state candidate creation for an
  all-Planned aggregate.
- Keep `matter.started` candidate generation for direct and cascade starts.
- Keep active, canceled, and sealed late-binding candidates. Keep earlier
  lifecycle and narration out of the new reference.
- Preserve deterministic candidate identities and event-snapshot push levels
  across reopen and rebuild.
- Preserve local regression checks before the provider call.
- Preserve `Converged` as a terminal success that records
  `tracker.state-observed`, not `tracker.state-pushed` and not a push record.
- Keep backend and level configuration lazy and Repo-tier.
- Add a provider-injected CLI test that connects bind, start, outbox approval,
  flush, guard read, provider write or convergence, durable events, and the
  push record.
- Cover all-Planned, shared-reference activation, explicit `off`, implicit
  `boundary`, no-forge delivery, forge-ahead convergence, provider refusal,
  and no-backward outcomes.

## Downstream Linear obligations

`tracker-provider-linear` consumes this record without reopening its policy.
The Linear adapter must:

- Map wip active, completed, and canceled dispositions to configured Linear
  workflow states without leaking Linear state names into wip events.
- Read the issue state immediately before every possible state write.
- Classify Linear Backlog as behind an active candidate when a forward move to
  the configured active state is safe.
- Classify In Progress, In Review, and other configured forward states as
  convergence for an active candidate when their ordering proves compatibility.
- Distinguish compatible terminal progress from a competing terminal outcome.
- Return `Converged` without a mutation when Linear or its forge integration
  already put the issue at or beyond the candidate.
- Return a refusal when it cannot classify the observed workflow state or
  cannot perform the guarded transition safely.
- Return a retryable failure when the guard read fails temporarily.
- Return a new opaque lease after a delivered state write.
- Implement no separate forge-owner flag.
- honor the provider-neutral outbox, level, composition, and never-backward
  behavior defined here and in T1-T10.

## Evidence and verification obligations

The implementation pass must prove the scenario matrix with hermetic tests.
The decision reasoning must also retain evidence from at least one real
multi-session Matter. That evidence must identify the Matter, its derived
session periods, its Matter and Step event times, and the first forge-visible
transition. A test fixture is not a substitute for that run record.

The verification artifact must identify the reviewed revision. It must map each
operative decision and scenario to an observed result. It must also list the
commands, discrepancies, resolutions, and final verdict.

## For the later model append

1. Append A1-A8 at the next free D-numbers after ratification.
2. Amend T7's late-binding rule so an all-Planned aggregate queues no state,
   while active and terminal current-state synchronization remains intact.
3. Amend T9's initial shared-reference rule so activation begins when any
   bound Matter starts, not when the first Planned Matter joins.
4. Keep T1, T3, T5, T6, and T10 in force. This record narrows their activation
   edge and does not replace their delivery, guard, or configuration rules.
