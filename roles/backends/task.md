# Task backend binding

This file binds the Roles capability to the **Task** orchestration
backend: the built-in Task tool and native Claude Code subagents, with
all coordination state on disk. The Role behavior in the sibling files
([`orchestrator.md`](../orchestrator.md), [`coordinator.md`](../coordinator.md),
[`researcher.md`](../researcher.md), [`builder.md`](../builder.md),
[`shared.md`](../shared.md), [`tier-policy.md`](../tier-policy.md)) is
backend-agnostic; **this is the only file that names Task-backend
primitives.** It names no MCP server — there is no external control plane.

It supplies the same surface as any backend (identity, substrate
bindings, role resolver, tag glossary, anti-patterns) bound to its own
primitives — and nothing else moves (ADR-0007, ADR-0013).

Active when `.wip.yaml` has `features.orchestration.backend: task`.

## The synchronous-spawn model (read this first)

Native subagents are **synchronous, one-shot calls**. The parent invokes
the Task tool, the call **blocks**, and it returns **exactly once** — when
the subagent has finished and produced its final result. There is no
long-lived child process, no polling, and no out-of-band liveness to
inspect. This single fact reshapes the backend:

- The returned result **is** the completion signal. The
  liveness-and-report gate ([`shared.md`](../shared.md) §Pause and Resume)
  exists to disambiguate an asynchronous backend's *bare idle edge* from
  real completion; here there is no idle edge, so the gate is **N/A** —
  see [Liveness signal](#liveness-signal-na) below.
- Because subagents do not persist between calls, **all cross-call state
  must live in files** — the ledger, the shared note, and anything a
  re-invoked Researcher needs. A subagent's memory evaporates on return.
- The parent of a spawn is its single writer for shared state: a Builder
  returns its outcome **as its Task result**, and the **Coordinator**
  records that outcome into the ledger file. One writer per file → **no
  locks** (see Substrate bindings).

## Identity & process naming

There is no `whoami` and no process rename — a native subagent has no
durable identity to query. Instead:

1. A Role's identity **is** the `subagent_type` it was invoked as
   (`wip-orchestrator`, `wip-coordinator`, `wip-researcher`,
   `wip-builder`) plus the scope handed to it in its spawn prompt.
2. The role-scoped **names** from [`shared.md`](../shared.md) (e.g.
   `<slug>-step-NN-coordinator`, `<slug>-step-NN-builder-MM`) are still
   used — as **labels in the ledger and shared-note files** and in the
   spawn prompt — so humans reading the on-disk state can tell entries
   apart. They name a *unit of work*, not a live process.

The Orchestrator is the top-level session itself; it is not spawned.

## Substrate bindings

Each abstract primitive from
[`templates/glossary/orchestration.md`](../../templates/glossary/orchestration.md)
binds to a concrete Task-backend primitive. (This mirrors the table in
[`templates/glossary/task.md`](../../templates/glossary/task.md) — the
glossary partial is the vocabulary, this row-set is the behavior binding.)

Live orchestration state lives under
`.wip/initiatives/<slug>/orchestration/` — gitignored, ephemeral, the
**live execution mirror** (the Roadmap remains the git-tracked plan of
record). Paths below are the canonical defaults.

| Abstract primitive | Task-backend primitive | How |
|---|---|---|
| **Agent process** | A native subagent invocation | Call the **Task tool** with `subagent_type: wip-<role>` and a self-contained prompt. The call blocks and returns the subagent's final result. No persistent process. |
| **Task ledger** | A markdown file: `.wip/initiatives/<slug>/orchestration/step-NN-ledger.md` | One `### ` section per entry (`Step N · Task M — <one-line>`), with status / owner-label / tags bullets and a `#### Comments` subsection. The **Coordinator is the sole writer**; Builders return outcomes as their Task result and the Coordinator appends them. |
| **Shared note** (rolling context) | A markdown file: `.wip/initiatives/<slug>/orchestration/step-NN-context.md` | Bootstrapped from the [`shared.md`](../shared.md) Shared-Note Template at build kickoff. Rolling context only; status lives in the ledger file. |
| **Idle timer** (pause/resume) | **N/A** | Spawns are synchronous; "wait for X to finish" *is* awaiting the Task call's return. There is nothing to pause/resume and no timer to arm. |
| **Service readiness** wait | A `Bash` wait | When a Builder needs a service up, it waits inside its own turn with an ordinary `Bash` poll (e.g. curl-until-ready); there is no backend wait primitive. |
| **Shared state** | The ledger + shared-note files | No separate key/value store. Cross-call values (a chosen model, a pin) live in the shared-note file's front matter when needed; most are unnecessary because the parent threads them into each spawn prompt directly. |

Use the **ledger file** as the primary durable coordination surface; use
the **shared-note file** for rolling context, not as a status store.

## Spawn behavior

- The parent calls the Task tool and **awaits** the result. The result is
  the worker's final report; record the salient outcome into the ledger
  file.
- **Parallel fan-out**: where the Roadmap marks independent lanes
  (ADR-0010) or a Chunk holds independent tasks, issue **multiple Task
  calls in a single message** so the subagents run concurrently; the
  parent resumes when all have returned. Where work is sequential, issue
  one call, await, record, then issue the next.
- **The Researcher is re-invoked, not kept alive.** [`shared.md`](../shared.md)
  calls the Researcher "long-lived for the lifetime of a Step"; under this
  backend that durability is provided by **files, not a process**. Phase-1
  planning is one `wip-researcher` Task call that writes the Workplan to
  disk. Each later *consult* during build is a **fresh** `wip-researcher`
  Task call whose prompt points it at the Workplan, the ledger file, and
  the shared-note file — it re-hydrates from those, answers, and returns.
  "Keep the Researcher available" maps to "those files persist and the
  Coordinator can re-spawn the Researcher against them at any time."
- The invariant "never spawn a Coordinator without a Researcher"
  ([`shared.md`](../shared.md)) still holds: the Coordinator's first act
  is the Phase-1 Researcher call that produces the Workplan.

## Liveness signal (N/A) {#liveness-signal-na}

There is no liveness signal to read and no liveness-and-report gate to
apply. A Task call cannot report "complete" prematurely — it returns only
when the subagent is done — so the "still active / between-step lull /
re-arm" branches of [`shared.md`](../shared.md) §Pause and Resume and of
the Coordinator's Wake-up Routing **cannot occur** under this backend. The
worker's returned result is the explicit terminal signal those branches
exist to wait for; record it and route directly.

## Operator-engagement guard (N/A) {#operator-engagement-guard-na}

There is no hold to place and no engagement signal to read. A native Task
subagent is a synchronous one-shot call with no long-lived process a human
can interject into mid-stream — the parent's turn is blocked until the
subagent returns — so the operator-engagement guard of
[`shared.md`](../shared.md) §Pause and Resume (hold + passive re-check
before close/inject) **cannot apply** under this backend. There is no
between-call window in which a human takes over the worker.

## Role resolver

A native subagent runs on the **session's model** by default. There is no
tool inventory to resolve against, so the Solo-style config-map-plus-
fallback-ladder is **N/A** — model selection here is a single optional
config lookup, and [`tier-policy.md`](../tier-policy.md) already permits a
backend to own selection end-to-end and skip the ladder.

`features.task.models` (`.wip.yaml`) maps a **Role** (or a `<role>-escalated`
target) to a model, applied via the Task tool's per-call `model` override:

```yaml
features:
  task:
    models:
      default: sonnet         # fallback model for any Role
      researcher: opus        # workplan production
      builder: sonnet
      builder-escalated: opus # stronger model on escalation
```

Resolution: look up `models[<role>]`; if the Role has no explicit entry,
fall through to `models.default`; if neither is set, spawn on the
**session's model** (no `model` override passed to the Task tool). Role is
the only selection signal (ADR-0025) — the caller decides which Role key to
request, including a `-escalated` target on repeated same-shape failure or
load-bearing work; the escalation policy lives in
[`tier-policy.md`](../tier-policy.md).

This graduates ADR-0013's deferred `tier → model` map to a `role → model`
map. When `features.task.models` is absent the backend behaves exactly as
before: every Role runs on the session's model, and the Role is recorded for
audit with no effect on selection. Unlike the Solo backend there is no
`detect`/`setup` plumbing for this map — the Task-backend parent reads
`features.task.models` from `.wip.yaml` directly.

## Tag glossary

The ledger file reuses the same tag vocabulary as any backend (see
[`shared.md`](../shared.md) §Ledger Tags), written as a `Tags:` bullet on
each `### ` entry:

- `roadmap`
- `step-NN` (scoped: prefix with the initiative slug)
- `task`
- `needs-human`
- `escalation`
- `coordinator-context`

## Anti-pattern — do not treat a subagent as a background process

❌ **Wrong**: spawning a subagent and then polling, busy-waiting, or
arming a timer to "check on it later".

```
# WRONG — there is nothing to poll; the call has not returned yet
spawn wip-builder ...
loop: check if builder is done?   # it can't be — you're still in the spawn call
```

✅ **Right**: the Task call **is** the wait. Issue it, let it return, then
act on the result.

```
result = Task(subagent_type="wip-builder", prompt=<self-contained>)
# control returns here only when the builder has finished
record result into step-NN-ledger.md
```

**Why**: native subagents are synchronous. There is no out-of-band handle
to inspect and no idle edge to misread. The two failure modes to avoid:

- **Phantom polling** — trying to observe a spawn that hasn't returned.
  Await it instead.
- **State in memory** — relying on a subagent "remembering" prior context.
  It does not persist; thread context through the spawn prompt and the
  on-disk ledger / shared-note files every time.

## Task control-plane terminology

- **subagent** — a native Claude Code agent invoked via the Task tool; it
  runs one turn-set and returns a final result. Plays a Role.
- **subagent_type** — the agent definition a subagent runs as
  (`wip-coordinator`, `wip-researcher`, `wip-builder`); also its identity.
- **spawn** — issue a Task-tool call for a `subagent_type`. Synchronous:
  the caller blocks until the subagent returns.
- **ledger file / shared-note file** — the on-disk markdown files under
  `.wip/initiatives/<slug>/orchestration/` that hold, respectively, the
  durable task ledger and the rolling shared note.

When in doubt, refer to a unit of work as a **subagent** and to durable
state as a **file** — there is no process and no server to qualify.
