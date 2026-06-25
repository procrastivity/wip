# Coordinator

Audience: `<slug>-step-NN-coordinator`, spawned by the Orchestrator.

Read first: [`shared.md`](./shared.md) and
[`tier-policy.md`](./tier-policy.md).

On activation, confirm your role via the active backend; see
[`backends/`](./backends/) for the binding.

## Responsibility

- Drive one Step end-to-end.
- Spawn and manage the persistent Researcher.
- Request the workplan from the Researcher.
- Orchestrate Builders during build.
- Route escalations up to the Orchestrator (and through them to the
  human).

## Phase 1: Workplan Production (Researcher-first, default)

1. Spawn the Researcher as `<slug>-step-NN-researcher` at the Tier
   from [`tier-policy.md`](./tier-policy.md).
2. Apply the operator-engagement guard to the Researcher.
3. Send the Researcher the workplan request only if it is not held or
   operator-engaged.
4. Wait for the Researcher to finish (idle-timer signal — do not
   poll).
5. Review the generated workplan for structure / completeness.
6. Surface the workplan path + a short summary to the Orchestrator
   for the human's review.
7. Keep the Researcher process **alive** after approval — it's the
   Step's on-demand research sidecar.

No non-Researcher fallback path is defined.

The workplan request is an inject into the Researcher: never inject into a
Researcher a human is holding or actively using — re-arm and wait instead.

## Phase 2: Build Orchestration

On `build/go/approved`:

1. Create the Step shared note from the template in
   [`shared.md`](./shared.md).
2. Record the Researcher's agent name + id in the shared-note header.
3. Decide batching and create per-task ledger entries (tagged with
   the `<slug>/step-NN` scope).
4. Execute the per-task loop:
   - spawn a Builder
   - send the Builder its bootstrap prompt
   - arm an idle timer
   - on wake, route the outcome: advance / retry / escalate

Any prompt sent into a Builder (bootstrap, status-check, retry) is an
inject and is subject to the operator-engagement guard
([`shared.md`](./shared.md) §Pause and Resume): never inject into a
Builder a human is holding or actively using — re-arm and wait instead.

## Research Consult Routing

If a Builder or the Coordinator is blocked on design / analysis /
spec interpretation:

1. Pause task progression for the blocked task.
2. Before sending, apply the operator-engagement guard to the Researcher.
3. Send a focused question to the Researcher only if it is not held or
   operator-engaged.
4. Wait for the Researcher's response.
5. Record the result under **Decisions made during build** in the
   shared note.
6. Forward the distilled guidance to the blocked Builder as a ledger
   comment or in the retry prompt.

Builders never contact the Researcher directly; the Coordinator is
the routing hub.

## Wake-up Routing (Builder idle)

When a Builder's watch timer fires, **first apply the
liveness-and-report gate and the operator-engagement guard**
([`shared.md`](./shared.md) §Pause and Resume): re-check the liveness
signal, require an explicit final-report comment, and confirm the Builder
is neither held nor operator-engaged — a bare idle edge routes nothing,
and a Builder a human is using is never closed or injected into.

1. Read the Builder's ledger entry + comments **and** re-check the
   liveness + engagement signals.
2. **Held / operator-engaged** → a human is using this Builder; re-arm
   the watch timer and wait. Do not close it and do not inject into it.
3. **Still active / only briefly quiet** → between-step lull; re-arm the
   watch timer and wait. Do not route.
4. **Quiet + final-report results comment, not held/engaged** → append
   the per-task outcome to the shared note and close the Builder.
5. **Quiet, no final-report comment, not held/engaged** → send a
   status-check prompt and re-arm a short timer (do **not** treat as
   done).
6. **`needs-human` tag** → create a Coordinator escalation ledger entry
   and pause further spawning until the human resolves.
7. **Dead process** (not running, no terminal signal) → respawn once,
   then escalate.

## Retry / Escalation Policy

- Up to **2** retries for fixable failures with clear error context.
- Same failure shape twice on the same Tier → escalate (or escalate
  the Tier — see [`tier-policy.md`](./tier-policy.md)).
- Ambiguity / spec conflict / scope question → escalate immediately.

## Step Boundary

1. Verify the Step's shipping criteria.
2. Run the post-build evaluation and append a retro entry.
3. Archive the workplan and the shared note under
   `.wip/initiatives/<slug>/archive/`.
4. **If this Step closes a Round** (last Step in its Round on the
   Roadmap): also archive the Round's intake artifacts referenced by
   that Round under `.wip/initiatives/<slug>/archive/`. Verify no
   orphaned references remain.
5. Post a step-shipped comment on the Step's ledger entry.
6. Before closing the Researcher or Coordinator, apply the
   operator-engagement guard to each process; if either is held or
   operator-engaged, re-arm and wait instead of closing it.
7. Close the Researcher and the Coordinator processes.
