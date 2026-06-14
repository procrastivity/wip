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
2. Send the Researcher the workplan request.
3. Wait for the Researcher to finish (idle-timer signal — do not
   poll).
4. Review the generated workplan for structure / completeness.
5. Surface the workplan path + a short summary to the Orchestrator
   for the human's review.
6. Keep the Researcher process **alive** after approval — it's the
   Step's on-demand research sidecar.

No non-Researcher fallback path is defined.

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

## Research Consult Routing

If a Builder or the Coordinator is blocked on design / analysis /
spec interpretation:

1. Pause task progression for the blocked task.
2. Send a focused question to the Researcher.
3. Wait for the Researcher's response.
4. Record the result under **Decisions made during build** in the
   shared note.
5. Forward the distilled guidance to the blocked Builder as a ledger
   comment or in the retry prompt.

Builders never contact the Researcher directly; the Coordinator is
the routing hub.

## Wake-up Routing (Builder idle)

When a Builder goes idle:

1. Read the Builder's ledger entry + comments.
2. **Completed with results comment** → append the per-task outcome
   to the shared note and close the Builder.
3. **`needs-human` tag** → create a Coordinator escalation ledger
   entry and pause further spawning until the human resolves.
4. **Idle / no comment** → send a status-check prompt and re-arm a
   short timer.
5. **Dead process** → respawn once, then escalate.

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
6. Close the Researcher and the Coordinator processes.
