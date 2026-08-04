# Surface-3 porcelain decisions

Session H, 2026-08-03. This record closes the porcelain-split remainder
assigned to `surface-3`. The operative lines below are ready for the next
free numbers in MODEL §12 during Session K. The local labels S1–S6 preserve
their order without allocating D-numbers early. Each label is one operative
decision, so Session K allocates one D-number per row.

Related records: `run-model-decisions.md` (labels F1–F9, Session F) and
`parallelism-decisions.md` (labels G1–G5, Session G). Citations to F-labels
below are citations to that record, not to MODEL §12.

| Local | Operative decision |
|---|---|
| S1 | **Reads have porcelain parity:** every Batch, Run, Dispatch, frontier, and liveness read is `plumbing`, available from both the CLI and agent porcelain; actions do not gain parity from a related read. |
| S2 | **A Run has one ephemeral, host-local ownership lock keyed by its full ULID:** the lock is an OS-level advisory lock that the kernel releases on process exit; the Orchestrator holds it for its dispatch bracket. The lock is coordination, not state: it is never stored, rendered, or replayed, and it emits no event. |
| S3 | **Liveness is a read-side derivation from that lock, orthogonal to ownership:** a read reports `live` when the lock is held, `interrupted` when an open Run's lock is free, and `unknown` when the probe cannot run. F9's `stranded` remains a separate ownership axis; the two axes are reported as two fields. |
| S4 | **`wip run resume` is `control-plane`:** it acquires the free lock of a Run its own Clone owns, then retains that lock and re-enters orchestration, so it exists only on the live agent porcelain and never projects into a harness. |
| S5 | **`wip run stand-down` is guarded `plumbing`:** it takes the free lock, closes an interrupted Run, reaps its orphaned Dispatch, and releases the lock before it returns; it refuses on a held lock and has no force option. Verb kind follows authority *retention*, not authority acquisition. |
| S6 | **Surface kind follows capability, not backend:** Solo and non-Solo backends use the same read parity, ownership lock, and verb classes; the coordinator owns the Run lock, never an individual worker. MODEL §11's provisional refusal therefore stands as a final refusal of every live control-plane action from the CLI. No degraded surface-3 CLI path is added. |

## Rationale and consequences

### S1 — reads are parity; actions are not

A read observes durable state or ephemeral ownership. It does not schedule
work, acquire execution authority, or change a Run. The same read therefore
has the same JSON and exit-code contract on the CLI and through the agent
porcelain. This includes the liveness probe that MODEL §11 left undecided.

Parity stops at observation. A CLI caller does not gain permission to resume
a Run because it can see that the Run is interrupted. The manifest keeps one
surface kind per verb, and D53 remains the mechanical projection rule.

D47 calls the plumbing surface deterministic. That word constrains the
*output contract* — stable JSON shape, stable exit codes, no LLM — not the
answer. A liveness probe reads ephemeral state, so two probes one second
apart may disagree. Both are deterministic reads under D47.

### S2 — one lock, held by the OS, not by a cleanup path

F8 needs reads to distinguish a Run that is actively dispatching from one
whose process stopped. A durable `open` row cannot make that distinction.
A stored heartbeat would also turn liveness traffic into domain mutations,
which would require events under MODEL §10 and would still leave an arbitrary
timeout.

The Orchestrator instead takes a non-blocking ownership lock for the Run
before it can dispatch, and holds that lock for the execution bracket. The
lock key is the full Run ULID, not its locator, so two Batches never collide.

The lock must be an OS-level advisory lock — `flock` or `fcntl` on the lock
file, held open by the coordinator process. This is normative, not an
implementation hint. The kernel releases such a lock when the process exits,
including on `SIGKILL`, on panic, and on a lost terminal. A lock file that
records a PID, or any scheme that needs a cleanup write, is refused: a killed
coordinator would leave a lock that resume cannot take and that stand-down
must refuse, and S5 gives that Run no force path. The whole escape hatch
depends on the kernel doing the release.

The lock file lives under the host runtime area, at
`$XDG_RUNTIME_DIR/wip/<store-id>/run/<run-ulid>.lock`, with mode `0700` on
the directories. When `XDG_RUNTIME_DIR` is unset, the fallback is
`/tmp/wip-<uid>/<store-id>/run/<run-ulid>.lock`. The store scope keeps two
stores on one host from reading each other's locks. Both locations clear on
reboot, which matches the lock's ephemeral nature. Presence of the file means
nothing; only the held advisory lock does.

The ownership lock is never the source of Run state, never rendered, and
never replayed. Durable Run state and its history remain in the store. The
lock complements F4's durable open-Dispatch claim: the Dispatch excludes
another Matter worker, while the Run lock excludes another coordinator from
driving or closing the same Run.

### S3 — liveness and ownership are two axes

A read probes the lock without retaining it, and derives ownership by
comparing the reading Clone to the Run's owning Clone. Ownership needs no
probe; liveness needs no store read. Two sources, two fields:

| | lock held | lock free |
|---|---|---|
| **owned** | `live` | `interrupted` |
| **stranded** (F9) | `live` | `interrupted` |

Collapsing `stranded` into the liveness enum would erase the top-right cell,
which is the one both verbs need: a stranded Run whose coordinator is alive.
S5 refuses there, and that refusal is what stops a CLI caller from killing
another Clone's live coordinator.

The read contract is therefore:

- `liveness` — `live`, `interrupted`, `unknown`, or `null`. It is `null` on a
  closed Run, because terminal state already answers the question. The field
  is always present, so machine consumers need no shape branch.
- `liveness_cause` — `null` unless `liveness` is `unknown`, and otherwise one
  of `probe-unsupported` (the filesystem or platform offers no advisory
  lock), `probe-denied` (permission), or `probe-failed` (any other I/O
  error).
- `ownership` — `owned` or `stranded`, derived per F9, present on open and
  closed Runs alike.

An `unknown` liveness exits `0`. The read succeeded; the fact it reports is
unknown. Nonzero exit stays reserved for a failed read, such as an unresolved
Run or an unreadable store. This keeps S1's parity contract one contract.

The liveness fields ride on the existing Run reads — `wip run show` and `wip
run list`. No new verb is introduced for the probe.

The probe is the degraded CLI visibility that operators need, but it is not a
degraded control-plane action and does not amend the action refusal.

### S4 — resume belongs to the live control plane

`run resume` does more than change a stored state. Per F8 it reconstructs the
Run, reaps an orphaned Dispatch, recomputes the frontier, opens a fresh
Dispatch, and continues execution. The verb takes the Run's ownership lock
and then keeps it while the Orchestrator can dispatch.

Resume is the only verb that reads both axes: it requires a free lock and
`owned` ownership, which is F8's "explicit write from the owning Clone".

Those semantics require a live coordinator, so the manifest kind is
`control-plane`. The verb is available through the agent porcelain and is
excluded from generated harness skills by D53. There is no CLI form that
performs only part of resume: splitting acquisition from execution would
create a second, weaker lifecycle with no owner.

### S5 — stand-down is the deterministic escape hatch

Stand-down performs no work. It closes an open Run with reason `stood-down`
(F2), and it closes that Run's orphaned Dispatch with reason `reaped` (D59).
The Dispatch close is not optional. Under F4 the open Dispatch *is* the
Matter's host-wide claim, so a stand-down that left it open would close the
Run and still leave the Matter unable to accept work. The escape hatch must
clear the claim it found.

Before writing, the verb takes the same ownership lock non-blockingly. A held
lock means the Run is live, so the verb refuses — on an owned Run and on a
stranded one alike. A free lock serializes stand-down against resume and
proves that the verb acts only on an interrupted Run. The verb releases the
lock before it returns.

That shape is `plumbing`: one guarded transition, stable JSON and exit codes,
and no scheduler or LLM. It remains legal from another known Clone as F9
requires, because the acting Clone does not adopt or execute the Run. There
is no `--force`; killing or overriding a live coordinator would be a live
control-plane action from the CLI and remains refused.

**The classification rule.** Both boundary verbs acquire the lock, so
acquisition classifies nothing. A verb is `control-plane` when it *retains*
execution authority — it holds the lock past its own return and can dispatch
work while holding it. A verb is `plumbing` when it holds the lock only
across one deterministic transition and releases it before returning, and
never dispatches while holding it. Later boundary verbs are classified by
this rule.

The two boundary verbs therefore do not span two manifest kinds. `run
resume` is control-plane; `run stand-down` is plumbing. Their shared Run
namespace does not imply shared capability.

**On the name.** The `agent-path` workplan uses "stand-down" as prose for the
explicit *Dispatch* close with reason `completed`. That prose predates this
record and needs correction to "dispatch close". Unqualified, "stand-down"
names the Run verb defined here.

### S6 — the split is backend-independent

The Run's coordinator owns execution authority. Workers behind that
coordinator can be Solo, a multi-agent backend, or a later backend without
changing the porcelain contract. No worker owns the Run lock, because worker
turnover must not make one live Run appear interrupted.

A backend adapter can affect how the coordinator dispatches work, but it
cannot reclassify verbs or expose a second CLI lifecycle. This keeps D47 and
D53 true for non-Solo backends: reads and guarded state changes are plumbing;
actions that retain execution authority are control-plane.

## Resolution of MODEL §11

The provisional entry stands and becomes final: wip refuses every live
control-plane action from the CLI. The liveness probe is admitted as a
plumbing read, and `run stand-down` is admitted as a plumbing action only
after it proves that the Run is not live. Neither is an exception to the
refusal. No surface-3 action gets a degraded CLI path.

For the Session K append:

1. Add S1–S6 at the next free D-numbers.
2. Remove the provisional parenthetical from MODEL §11 and retain the refusal
   as final.
3. Add the liveness vocabulary `live`, `interrupted`, and `unknown` as
   read-side terms; none is a stored Run state. Record that liveness and F9's
   `stranded` ownership are two orthogonal read-side axes.
4. Keep D47 and D53 unchanged; S1 and S6 make their backend-independent
   consequences explicit. Note under D47 that "deterministic" constrains the
   output contract, not the answer.
5. Rewrite this record's F4, F8, and F9 citations to the D-numbers those
   labels receive, in whichever session allocates them.

## Implementation contract for downstream Matters

- `run-substrate` implements the OS-level advisory Run lock at the path in
  S2, the liveness and ownership read fields in S3, guarded stand-down with
  its Dispatch reap, and lock acquisition for resume. It registers `run
  stand-down` as `plumbing` and the resume entry point as `control-plane`.
- `scheduler` holds the acquired ownership lock for as long as its
  Orchestrator can dispatch that Run. It does not transfer the lock to a
  worker backend.
- Harness generation needs no new rule. D53 already projects stand-down and
  excludes resume by their manifest kinds.
