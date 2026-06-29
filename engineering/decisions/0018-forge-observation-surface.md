# 0018 — the forge observation surface

- Status: accepted
- Date: 2026-06-28
- Source: `forge-surface` initiative, Round 1 (step-01); BRIEF.md; Linear BDS-22 (refinement of BDS-20); ADR-0002, ADR-0006, ADR-0014, ADR-0016

## Context

wip has no concept of a forge or a push/PR/merge event. BDS-20's desired Tier-1
lifecycle triggers — a real `git push` → **In Review**, a merge → **Done** —
need wip to *observe* forge events. BDS-20's Tier-0 path (the `wip ship`
closeout-write, ADR-0016, plus a manual `review complete`) already drives the
lifecycle with zero forge dependency; this initiative is the Tier-1 upgrade that
*owns* those transitions once a forge is wired, and makes the Tier-0 boundary
stand down so nothing double-fires.

The BRIEF left three open questions for this step, resolved below:

1. **What surface observes the push?** A `wip push` verb that owns the push; a
   `pre-push` git hook; or `sync`/`doctor` reconciling against the forge.
2. **Is "forge" a third `sync` service** (`sync forge` alongside `sync solo` /
   `sync linear`)?
3. **How does merge → Done** map onto BDS-20's `review complete` transition?

Two facts constrain the answer. First, ADR-0006: wip owns the **seams** between
tools, not the tools — it wraps existing CLIs, it does not reimplement or own
their actions. Second, BDS-27 (deferred) owns *forge workflow policy* — when a
branch is created, commit/push cadence, and MR/PR aggregation/stacking. This
step must not pre-empt either: it builds the **observation seam only**.

## Decision

### 1. Observe, don't own the push

wip **observes** forge state by shelling out to `gh`/`glab` and asking "is this
branch pushed? is there an open PR/MR? is it merged?" It does **not** add a
`wip push` verb that wraps `git push`. Owning the push action would (a) violate
ADR-0006 (wip owns seams, not the tools' actions) and (b) pre-empt BDS-27, which
owns *when and how* pushes happen. Observation reacts to the world the user (or
BDS-27's future policy) already created; it never decides push cadence.

A `pre-push` git hook that calls into wip is a **deferred, opt-in real-time
accelerator** — it would merely trigger the same observation at push time rather
than at the next reconcile. It is not required for v1 and is recorded under
Deferred; the observe path is correct with or without it.

### 2. Forge is its own verb home — no `sync` family yet

There is **no `sync` parent verb in the codebase today**; `sync solo` /
`sync linear` are aspirational. Creating a `sync` parent now, with `forge` as
its only child, is speculative scaffolding. Instead:

- **Liveness mirrors `--probe-solo` exactly (ADR-0014):** `status --probe-forge`
  sets a tri-state `forge_reachable` (`true`/`false`/`null` = not-probed) and
  appends a `forge-unreachable` signal **only** when unreachable **and** a forge
  is the active transition owner (the "non-actionable → no signal" discipline).
- **Event observation gets one new top-level plumbing verb, `forge`** — one
  subcommand-file (`lib/wip/wip-plumbing-subcommands/forge.bash`, one
  `wip_plumbing_cmd_forge` function) registered in the `bin/wip-plumbing`
  dispatch table, matching the house convention (ADR-0001).

The `sync forge | solo | linear` consolidation is **deferred** until a second
`sync` consumer actually exists; if it lands, `forge` graduates under it without
changing its observation contract.

### 3. Transport seam — wrap `gh`/`glab`, with injectable test seams

The forge transport mirrors the solo-CLI probe shape (ADR-0014):

- **Detection:** `command -v gh` (preferred), then `command -v glab`. Absent →
  `forge_reachable: null`, no signal, no failure.
- **Two overridable shell-out seams** so tests inject fakes and never touch the
  network:
  - `WIP_FORGE_STATUS_CMD` — liveness (e.g. `gh auth status`).
  - `WIP_FORGE_OBSERVE_CMD` — PR/merge state (e.g.
    `gh pr view --json state,mergedAt`).

### 4. Observed state → transition **intent** (the merge → Done mapping)

The `forge` verb maps observed forge state to a lifecycle **transition intent**,
not a Linear write (the write stays BDS-20's; that machinery is greenfield):

| Observed forge state                     | Transition intent | Tier-0 equivalent it supersedes |
|------------------------------------------|-------------------|---------------------------------|
| branch pushed **and** PR/MR open         | `in-review`       | `wip ship`'s In-Review drive    |
| PR/MR **merged**                         | `done`            | manual `review complete`        |
| PR/MR closed-unmerged                    | none (signal)     | —                               |
| no push / no PR                          | none              | —                               |

So **merge → Done** is the Tier-1 equivalent of the manual `review complete`:
when the forge reports the PR/MR merged, the emitted intent is `done`.

### 5. Stand-down contract (no double-fire)

When a forge is **enabled and reachable** for the initiative — gated by a
`features.forge` enablement in `.wip.yaml`, mirroring ADR-0002 feature
detection — the forge observation is the **single writer** of the transition
intent, and the Tier-0 boundaries stand down:

- `wip ship` **still performs its disk writes** (roadmap `✅ shipped` marker +
  `active_step` clear, ADR-0016 — unchanged, un-gated) but emits
  `transition: stood-down` instead of driving In Review.
- The manual `review complete` remains available (Tier-0 fallback) but the forge
  owns Done while it is the active owner.

This keeps exactly one transition writer at a time, so the Tier-0 and Tier-1
paths never both fire.

## Consequences

- The Round 2 lanes build against fixed seam names: verb `forge` +
  `lib/wip/wip-plumbing-subcommands/forge.bash`; flag `status --probe-forge`;
  field `forge_reachable`; signal `forge-unreachable`; env seams
  `WIP_FORGE_STATUS_CMD` / `WIP_FORGE_OBSERVE_CMD`; manifest key
  `features.forge`; intents `in-review` / `done` / none; the `transition:
  stood-down` marker on the Tier-0 `ship` path.
- Lane A (liveness, `status`/`doctor`) and Lane B (events, `forge` verb + `ship`
  stand-down) touch disjoint files and share only step-02's transport seam.
- wip stays small and ADR-0006-clean: no `git push` ownership, no bespoke forge
  API client, no speculative `sync` parent.
- Scope stays honest: forge-surface emits a transition **intent**; the Linear
  status write that consumes it is BDS-20's. Workflow policy (branch/commit/push
  cadence, MR/PR aggregation, stacking) is BDS-27's and is untouched here.
- New (Round 2): `lib/wip/wip-plumbing-subcommands/forge.bash` + dispatch wiring,
  a forge transport lib, `--probe-forge` in `status.bash`/`doctor.bash`, a
  `features.forge` detector, and the `ship` stand-down branch — each in its lane.

## Deferred

- **`pre-push` git hook** — real-time observation at push time; optional opt-in
  accelerator over the reconcile path. Revisit once the observe path ships.
- **`sync` family consolidation** (`sync forge | solo | linear`) — only once a
  second `sync` consumer exists.
- **Forge workflow policy** (branch/commit/push cadence, MR/PR aggregation,
  stacking) — owned by Linear BDS-27, explicitly out of scope here.
