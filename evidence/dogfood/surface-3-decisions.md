# Surface-3 porcelain decisions

Session H, 2026-08-03. This record closes the porcelain-split remainder
assigned to `surface-3`. The operative lines below are ready for the next
free numbers in MODEL §12 during Session K. The local labels S1–S5 preserve
their order without allocating D-numbers early. Each label is one operative
decision.

| Local | Operative decision |
|---|---|
| S1 | **Reads have porcelain parity:** every Batch, Run, Dispatch, frontier, and liveness read is deterministic `plumbing`, available from both the CLI and agent porcelain; actions do not gain parity from a related read. |
| S2 | **Run liveness comes from an ephemeral host-local ownership lock keyed by Run identity:** the Orchestrator holds the lock while it can dispatch; a read reports `live` when the lock is held, `interrupted` when an open Run's lock is free, and `unknown` only when the host cannot perform the probe. The lock is coordination, not state, and emits no event. |
| S3 | **`wip run resume` is `control-plane`:** it acquires the free ownership lock and re-enters orchestration, so it exists only on the live agent porcelain and never projects into a harness. |
| S4 | **`wip run stand-down` is guarded `plumbing`:** it acquires the free ownership lock before it deterministically closes an interrupted or stranded Run; it refuses while the lock is held and has no force option. It projects into harnesses and may act from another known Clone as F9 permits. |
| S5 | **Surface kind follows capability, not backend:** Solo and non-Solo backends use the same read parity, ownership lock, and verb classes; the coordinator owns the Run lock, never an individual worker. MODEL §11's provisional refusal therefore stands as a final refusal of every live control-plane action from the CLI. No degraded surface-3 CLI path is added. |

## Rationale and consequences

### S1 — reads are parity; actions are not

A read observes durable state or ephemeral ownership. It does not schedule
work, acquire execution authority, or change a Run. The same read therefore
has the same JSON and exit-code contract on the CLI and through the agent
porcelain. This includes the liveness probe that MODEL §11 left undecided.

Parity stops at observation. A CLI caller does not gain permission to resume
a Run because it can see that the Run is interrupted. The manifest keeps one
surface kind per verb, and D53 remains the mechanical projection rule.

### S2 — liveness is an ownership fact, not a heartbeat history

F8 needs reads to distinguish a Run that is actively dispatching from one
whose process stopped. A durable `open` row cannot make that distinction.
A stored heartbeat would also turn liveness traffic into domain mutations,
which would require events under MODEL §10 and would still leave an arbitrary
timeout.

The Orchestrator instead takes a non-blocking, host-local ownership lock for
the Run before it can dispatch and holds that lock for the execution bracket.
The lock key is the full Run ULID, not its locator. Process exit releases the
lock without a cleanup write. A read probes the same lock without retaining
it:

- held lock + open Run = `live`;
- free lock + open Run = `interrupted`;
- probe unavailable = `unknown`, with the cause in machine output;
- closed Run has no liveness label because terminal state already answers the
  question.

The ownership lock is ephemeral coordination. It can live under the
host-local runtime area and is never the source of Run state, never rendered,
and never replayed. Durable Run state and its history remain in the store.
The lock complements F4's durable open-Dispatch claim: the Dispatch excludes
another Matter worker, while the Run lock excludes another coordinator from
driving or closing the same Run.

The probe is a deterministic read. It is the degraded CLI visibility that
operators need, but it is not a degraded control-plane action and does not
amend the action refusal.

### S3 — resume belongs to the live control plane

`run resume` does more than change a stored state. Per F8 it reconstructs the
Run, reaps an orphaned Dispatch, recomputes the frontier, opens a fresh
Dispatch, and continues execution. The verb must first acquire the Run's
ownership lock and must hold it while the Orchestrator can dispatch.

Those semantics require a live coordinator, so the manifest kind is
`control-plane`. The verb is available through the agent porcelain and is
excluded from generated harness skills by D53. There is no CLI form that
performs only part of resume: splitting acquisition from execution would
create a second, weaker lifecycle with no owner.

### S4 — stand-down is the deterministic escape hatch

Stand-down performs no work. It closes an open Run with reason `stood-down`
and releases durable coordination state. Before writing, it takes the same
ownership lock non-blockingly. A held lock means the Run is live, so the verb
refuses. A free lock serializes stand-down against resume and proves that the
verb acts only on an interrupted Run.

That shape is deterministic `plumbing`: one guarded transition, stable JSON
and exit codes, and no scheduler or LLM. It remains legal from another known
Clone as F9 requires, because the acting Clone does not adopt or execute the
Run. There is no `--force`; killing or overriding a live coordinator would be
a live control-plane action from the CLI and remains refused.

The two boundary verbs therefore do not span two manifest kinds. `run
resume` is control-plane; `run stand-down` is plumbing. Their shared Run
namespace does not imply shared capability.

### S5 — the split is backend-independent

The Run's coordinator owns execution authority. Workers behind that
coordinator can be Solo, a multi-agent backend, or a later backend without
changing the porcelain contract. No worker owns the Run lock, because worker
turnover must not make one live Run appear interrupted.

A backend adapter can affect how the coordinator dispatches work, but it
cannot reclassify verbs or expose a second CLI lifecycle. This keeps D47 and
D53 true for non-Solo backends: deterministic reads and guarded state changes
are plumbing; actions that require the live coordinator are control-plane.

## Resolution of MODEL §11

The provisional entry stands and becomes final: wip refuses every live
control-plane action from the CLI. The liveness probe is admitted as a
plumbing read, and `run stand-down` is admitted as a plumbing action only
after it proves that the Run is not live. Neither is an exception to the
refusal. No surface-3 action gets a degraded CLI path.

For the Session K append:

1. Add S1–S5 at the next free D-numbers.
2. Remove the provisional parenthetical from MODEL §11 and retain the refusal
   as final.
3. Add the liveness vocabulary `live`, `interrupted`, and `unknown` as
   read-side terms; none is a stored Run state.
4. Keep D47 and D53 unchanged; S1 and S5 make their backend-independent
   consequences explicit.

## Implementation contract for downstream Matters

- `run-substrate` implements the host-local Run ownership lock, the tri-state
  liveness read, guarded stand-down, and lock acquisition for resume. It
  registers `run stand-down` as `plumbing` and the resume entry point as
  `control-plane`.
- `scheduler` holds the acquired ownership lock for as long as its
  Orchestrator can dispatch that Run. It does not transfer the lock to a
  worker backend.
- Harness generation needs no new rule. D53 already projects stand-down and
  excludes resume by their manifest kinds.
