# Shared Role Behavior

Cross-Role behavior for every wip Role: **Orchestrator**, **Coordinator**,
**Researcher**, **Builder**. Backend-agnostic — every primitive named
here is the abstract one from
[`templates/glossary/orchestration.md`](../templates/glossary/orchestration.md).
The concrete substrate (which MCP tool implements which abstract
primitive) lives in the active orchestration backend's binding under
[`backends/`](./backends/).

On activation, confirm your role via the active backend; see
[`backends/`](./backends/) for how. Tier selection for any spawn is
governed by [`tier-policy.md`](./tier-policy.md).

## Role Invariants

- **Orchestrator** and **Coordinator** are never the same agent process.
- **Coordinator** and **Researcher** are separate agent processes.
- **Builders** are ephemeral, scoped to a single Chunk/task.
- **Researcher** is long-lived for the lifetime of a Step and remains
  available for consultation during build.
- Never spawn a **Coordinator** without also spawning its **Researcher**.

## Naming Conventions

Process / agent names are role-scoped and slug-namespaced so concurrent
initiatives never collide.

| Thing | Pattern | Example |
|---|---|---|
| Orchestrator process | `orchestrator` | `orchestrator` |
| Coordinator process | `<slug>-step-NN-coordinator` | `distillation-step-12-coordinator` |
| Researcher process | `<slug>-step-NN-researcher` | `distillation-step-12-researcher` |
| Builder process | `<slug>-step-NN-builder-MM` (retries add `-r1`/`-r2`) | `distillation-step-12-builder-03` |
| Step shared note | `<slug>-step-NN-context` | `distillation-step-12-context` |
| Step ledger entry | `Step N · Task M — <one-line>` | `Step 12 · Task 3 — Backend grep test` |
| Escalation ledger entry | `[ESCALATION step-NN/builder-MM] <summary>` | `[ESCALATION step-12/builder-03] Tier resolver ambiguous` |

## Ledger Tags

Used to slice the task ledger by scope:

- `roadmap` — Roadmap-derived entries.
- `step-NN` — scoped to a single Step (combined with the initiative
  slug as needed by the backend's tag conventions).
- `task` — leaf execution units inside a Step.
- `needs-human` — blocking on a human decision; routed by the
  Orchestrator.
- `escalation` — surfaced upward from Builder → Coordinator →
  Orchestrator.
- `coordinator-context` — entries the Coordinator owns.

## Pause and Resume

Roles do not poll. Use the backend's **idle-timer** signal to pause
and resume:

- A timer's body is injected as a **fresh user turn** when it fires, so
  the next action picks up automatically — no polling loop, no burned
  context.
- Timer bodies must be **self-contained**: include the agent's
  identity, any ids the next action needs, and the action itself. The
  fresh turn has no implicit memory of what set the timer.
- Use the "fire when watched agents go idle" variant for **worker
  quiet periods** (waiting on a Builder, a Researcher, or a batch of
  Builders to finish their current task). Use a **fixed-duration**
  timer for time-based waits. Use a **port-bound** wait for service
  readiness, not for worker idle.

See [`backends/`](./backends/) for the concrete timer tool names.

### An idle edge is not a completion signal

The "fire when watched agents go idle" timer can fire on a *between-step*
idle — a watched agent momentarily quiet between tool calls, not finished.
Before routing any watched agent or task as **complete**, apply the
**liveness-and-report gate**:

1. **Liveness re-check** — re-read the backend's **liveness signal**. If
   the agent is still active, producing output, or only briefly quiet,
   treat it as a between-step lull: **re-arm the wait and take no routing
   action.**
2. **Explicit terminal signal** — require an explicit **final-report
   comment or a completed ledger entry** authored by the watched agent.

Route to "complete" / close a process only when **both** hold, **and**
the operator-engagement guard below clears. Never route on the bare idle
edge. See [`backends/`](./backends/) for the concrete liveness signal.

### The operator-engagement guard

A human operator can take over **any** spawned agent directly — pairing
with it, course-correcting, or asking a follow-up — not only through this
Role. Two actions must never land on an agent a human is actively using:
**closing it**, and **injecting into it** (a status-check prompt, a retry
prompt, or a fresh timer turn). Before either action, against any watched
agent, apply the guard:

1. **Explicit hold.** An operator may place a *hold* on a spawned agent.
   While a hold is present, take **no** routing action against it — do not
   close it, do not inject into it; re-arm the wait. Timer-delivered turns
   are also subject to this guard: a timer body that wakes while its
   delivery target or watched target is held must no-op/re-arm instead of
   continuing. A backend may additionally pause timers while a hold is
   present, but the mandatory guarantee is the guard check at action time.
   The hold is cleared only by the operator.
2. **Passive engagement re-check.** Immediately before closing or
   injecting, re-read the engagement signal. If there is fresh activity
   this Role did not cause — the agent active again, or an un-submitted
   operator draft pending — treat it as operator-engaged: back off and
   re-arm. Best-effort; the explicit hold is the guarantee.

Fold this into completion routing: close a process only when it is quiet,
carries the explicit terminal signal above, **and** is neither held nor
operator-engaged. Where the backend has no long-lived process a human can
interject into (a synchronous one-shot backend), this guard is N/A — see
[`backends/`](./backends/) for the concrete engagement signal and hold.

## Shared-Note Template

Coordinators bootstrap a Step shared note from this template at build
kickoff. Status lives in the **task ledger** (queried by
`<slug>/step-NN` tag), not here — this note carries rolling context
the ledger can't.

```markdown
# Step N — Rolling Context

**Coordinator**: <agent name and id>
**Researcher**: <agent name and id>
**Workplan**: .wip/initiatives/<slug>/workplans/step-NN-<title>.md
**Build started**: <ISO timestamp>

## Batching plan

| Batch | Tasks | Rationale |
|---|---|---|
| (filled by coordinator at setup) | | |

> **Live task status**: query the task ledger by the `<slug>/step-NN`
> tag rather than maintaining a status table here.

## Decisions made during build

(append during build)

## Escalations

(append as escalations occur)

## Per-task outcomes

(append one outcome paragraph per task completion)
```

## Tier Selection

When spawning, request a **tier** (`small` / `medium` / `large`), never
a runtime tool id. The active backend resolves the tier to whatever
runtime it has available. See [`tier-policy.md`](./tier-policy.md) for
the per-Role defaults and the escalation guardrails.
