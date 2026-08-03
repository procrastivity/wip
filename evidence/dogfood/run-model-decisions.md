# Run and Batch lifecycle decisions

Session F, 2026-08-03. This record closes the four lifecycle questions
assigned to `run-model`. The operative lines below are ready for the next
free numbers in MODEL §12 during Session K. The local labels F1–F9 preserve
their order without allocating D-numbers early. Each label is one operative
decision, so Session K allocates one D-number per row.

| Local | Operative decision |
|---|---|
| F1 | **Run is ratified:** one Orchestrator pass over one Batch, keyed at Clone; its events are the `run.*` family. |
| F2 | **A Run is `open` or `closed`;** `closed` is terminal, with reason `completed`, `stood-down`, or `reaped`. |
| F3 | **A Run freezes its dispatch set at `run.started`;** later joins and leaves affect only later Runs. |
| F4 | **A Matter has at most one open Dispatch, host-wide:** that Dispatch is the claim; contention follows D30. |
| F5 | **An anonymous Batch has no name:** one per Matter, born at its first bare dispatch; it may hold many Runs. |
| F6 | **A Run carries a per-Batch locator `run-NN`,** never reused; output shows it with the UTC start time. |
| F7 | **A named Batch lives until dismissal; an anonymous Batch is swept when its Matter seals.** Amends D18. |
| F8 | **An interrupted Run never resumes automatically:** `run resume` is an explicit write from the owning Clone. |
| F9 | **A Run read from a non-owning Clone is `stranded`** — derived, not stored; that Clone may only stand it down. |

## Rationale and consequences

### F1 — ratify Run

`Run` already appears in the entity diagram and the tier note (MODEL §9), in
§6's dispatch prose, in §8.1's provisional vocabulary entry, in D48 and D60,
and in the workplans and orchestration traces. It also names one precise
thing: one Orchestrator pass over one Batch. Renaming it would create a
second vocabulary or force a coordinated rename without fixing an ambiguity.
Ratification removes D48's provisional marker and the `**Provisional:**`
marker in §8.1.

The `run.*` event family does not exist yet: MODEL §10 lists run/roles as a
Phase 2 family and `internal/store/taxonomy.go` carries no `run.*` constant.
Ratification therefore renames nothing — it fixes the name before the family
is written, which is the whole value of deciding it now. F2 and the event
list below name the family.

A Run is not a Session. A Run is chosen, prospective, and drives dispatch. A
Session remains derived, retrospective, and drives nothing. A Run is also not
a Batch: the Batch persists across several Runs and membership changes.

### F2 — a Run's states are the Dispatch shape, not the node shape

A Run is an execution bracket one tier above a Dispatch, so it takes the
Dispatch state shape rather than the five-state node lifecycle. The node
states are for plan nodes at every scale (MODEL §2.2), and `paused` and
`canceled` would compete with stand-down.

- Stored: `state` is `open` or `closed`. A close always carries a reason,
  as D59 requires of a Dispatch.
- Reasons: `completed` (nothing further to dispatch), `stood-down` (an
  explicit decision to stop), `reaped` (closed by a sweep, not by intent).
- Terminal means `closed`. This is the trigger F7 needs.

`blocked`, `standing by` (D60), `interrupted`, and `stranded` are read-side
words derived from rows, never stored states. A Run over Matters parked at a
human-owned gate is open and standing by; it is not failed and not closed.

The `runs` table exists already (`internal/store/schema_v1.go:460-468`) with
no CHECK on `state`, deliberately, pending this decision. Its only value in
the tree is the test-only `'queued'`, which this record does not ratify: a
Run is `open` from `run.started`. `run-substrate` adds the CHECK and drops
that value.

### F3 — separate prospective membership from active execution

At `run.started`, the scheduler records the Batch membership it will consider
for that Run, in the event payload. A later `batch.joined` or `batch.left`
event changes the Batch but does not mutate the active Run's dispatch set.
This snapshot rule makes churn deterministic and replayable. To consume new
membership, start another Run.

D58 also joins a Matter to a Batch implicitly, on its first work within a
dispatch. Such a join fires during the Run that caused it and is a no-op
against the frozen set: the Matter is already in that set, and joining is
idempotent per (batch, matter).

### F4 — the claim is the open Dispatch, and it is per Matter

Membership in several Batches remains legal because a Batch is an attention
group. Execution is exclusive at the Matter, and nowhere wider: **a Matter
has at most one open Dispatch across the host.** Before dispatching, a Run
opens the Dispatch; if one is already open for that Matter, the Matter is
contended.

The claim is the open Dispatch row itself, so no separate claim event and no
claim table exist. A claim ends when its Dispatch closes. Membership never
grants a claim.

This is the strictest rule that still permits the concurrency we want. Two
Runs may execute at the same time, including two Runs on one Clone in
different Worktrees, as long as their dispatch sets do not collide on a live
claim. It also subsumes D43 — a Matter worked from one Clone at a time —
because one open Dispatch names one Worktree and therefore one Clone. That
is the checkable predicate D69 says P1 does not have.

A contended Matter is not a special case: it follows D30's configured
policy — skip and continue by default, halt or ask if configured — and the
skip event names contention as the reason.

### F5 — one anonymous Batch per Matter

The anonymous Batch is machinery for the single dispatch path, not a durable
human grouping. Giving it an editable or generated name would make hidden
machinery compete with named Batches. Output derives its display from its
single Matter's current locator: `anonymous · <matter-locator>`. Identity and
events use the Batch ULID, so a later locator change does not rewrite
history.

One anonymous Batch per Matter, not one per attempt. A second bare "work this
Matter" opens a second Run over the same anonymous Batch. Keeping several
Runs per Batch legal costs nothing here and leaves the shape open: if a
Batch turns out to want one Run, that is a later narrowing, not a
retrofit. It also gives F6's ordinal a stable domain to count in.

### F6 — a Run is identified by a locator, not by an ID prefix

Two Runs over one Batch need separate labels. The start time alone does not
give one, and neither does a short prefix of the Run ID: identities are
monotonic ULIDs whose leading characters are the millisecond timestamp
(`internal/store/ulid.go:17-27`), so a short prefix re-encodes the time and
separates nothing. Two Runs started in one millisecond share it entirely.
Nothing resolves a partial identity either — `IsIdentityShaped` requires all
26 characters.

So a Run takes a locator on the model the Steps already use: `run-NN`,
sequential within its Batch, **never reused**, counted over every prior Run
including closed ones, exactly as `NextStepLocator` counts tombstoned Steps
so that no reader of the log finds two `step-03`s.

`run-NN` is unique within its Batch and not beyond it, exactly as `step-NN`
is unique within its Matter. A Run's full address is therefore its Batch and
its locator: `anonymous · widget-cache · run-02` on the anonymous path,
`cache-work · run-02` for a named Batch, with the UTC start time beside it
for scanning. The ` · ` is the ratified separator for "and specifically this
thing within it", not a new convention. The store and machine output always
carry the full Run ULID.

### F7 — named and anonymous Batches have different owners, so different lifetimes

**This amends D18.** D18 makes a Batch persistent-but-transient, "a row
surviving restart but not the completion of its contents" (MODEL §6). That
rule is right for the anonymous path and wrong for the named one, so the
amendment splits it rather than replacing it.

A named Batch is user-owned prospective state. It may become empty and stays
until an explicit dismissal, including when all its members are sealed.
Deleting a curated grouping at an arbitrary boundary loses work the user did,
and re-creating it mints a new ULID that orphans its event history.

An anonymous Batch has no independent user intent. It is swept when its
Matter seals — not when a Run closes, since F5 lets the Matter be worked
again. The sweep closes any Run left open with reason `reaped`.

Append-only events preserve both kinds after their live rows leave normal
selection.

### F8 — resume only by an explicit decision

Automatic restart could repeat side effects before the system knows why the
process stopped. An in-progress Run therefore stays open and resumable until
somebody decides. `run resume` is an explicit write. It reconstructs the Run
from the event log, closes the orphaned Dispatch with reason `reaped`,
recomputes the frontier from current durable state, and opens a new Dispatch.
Completed work is not replayed; work with no completion event becomes
eligible again.

The verb is namespaced. Flat `resume` is already the node lifecycle verb
(Paused → In Progress), and `restart` and `continue` are both spoken for —
`restart` means process restart throughout these documents, and `continue` is
D30's skip-and-continue. `wip run resume` follows the `wip dispatch close`
precedent.

**Reads cannot yet tell an interrupted Run from a live one.** Nothing in the
store distinguishes a Run whose process died from a Run that is dispatching
right now; that is the liveness question MODEL §11 parks and Step 0 assigned
to `surface-3`. Until `surface-3` decides it, reads report an open Run as
open with liveness unknown, and never assert that it is interrupted. The
decisions above do not depend on which way that question goes — only the
wording of the read does.

### F9 — stranded, and who may stand a Run down

Run identity keys at Clone, so resume cannot migrate execution implicitly. A
different current Clone can inspect the host-wide row but cannot resume or
adopt it. Output calls such a Run `stranded` and names the owning Clone.
`stranded` is derived at read time — owning Clone is not the current Clone —
and is not a stored state and not a column.

Stand-down is permitted from a different **known** Clone: one with a
`clone.attached` event on this host. It releases coordination state and
performs no work, so it is safe from elsewhere; its event records both the
acting and the owning Clone identity. A later Run from another Clone is a new
identity, not a continuation.

## Event names

The `run.*` family, all Clone-tier, subject as noted:

| Event | Subject | Notes |
|---|---|---|
| `run.started` | Run | Payload carries the frozen dispatch set (F3) and the `run-NN` locator (F6). |
| `run.skipped` | Matter | One per skipped Matter, with the reason: contention (F4), blocked, or failed (D30). |
| `run.resumed` | Run | Explicit resume (F8); pairs with the `dispatch.closed`/`reaped` it causes. |
| `run.finished` | Run | Closes the Run with reason `completed`. |
| `run.stood-down` | Run | Closes with reason `stood-down`; records acting and owning Clone (F9). |

Two additions to the existing batch family, both Batch-subject and therefore
carrying a null Repo dimension (D56):

| Event | Subject | Notes |
|---|---|---|
| `batch.dismissed` | Batch | Explicit dismissal of a named Batch (F7). |
| `batch.swept` | Batch | System sweep of an anonymous Batch when its Matter seals (F7). |

No claim event exists, by F4: the claim is the open Dispatch row.

## What needs no schema change

- `reaped` is already a legal Dispatch close reason
  (`internal/store/schema_v1.go:475`), so F8's orphan close and F7's sweep
  need no migration.
- Batch-subject events with a null Repo dimension are already the rule
  (D56), so `batch.dismissed` and `batch.swept` fit the existing envelope.
- The `runs` table already exists; F2 adds a CHECK to a column that has
  none, and adds no table.

## Implementation contract for downstream Matters

- `run-substrate` supplies Run lifecycle and states (F2, including the
  `state` CHECK and the removal of the unratified `queued` value), the frozen
  membership snapshot, the `run-NN` locator counter, named-Batch dismissal,
  the anonymous-Batch sweep at seal, resume, and stand-down. Every write path
  is a verb emitting exactly one event.
- `surface-3` owns two things here: the capability classes for `run resume`
  and `run stand-down`, and **the liveness question F8 depends on** — how a
  read distinguishes a live Run from an interrupted one, which is the same
  probe MODEL §11's provisional refusal entry parks. Until it decides, reads
  say "open, liveness unknown".
- `scheduler` consumes the frozen dispatch set, applies D30's configured
  policy to contended Matters and emits `run.skipped`, and recomputes the
  frontier after an explicit resume.
- Explicit Batch creation remains unavailable until `run-substrate` seals
  (NEXT-0 loose end L2). This session changes no Batch or Run rows.

## For the Session K append (L4)

Beyond appending F1–F9 as new D-numbers:

1. **D48** — strike the provisional marker; Run is ratified (F1).
2. **MODEL §8.1** — remove `**Provisional:**` from the `Run` vocabulary
   entry and move it into the locked list (F1).
3. **D18** — amend: persistent-but-transient governs the anonymous Batch; a
   named Batch lives until explicit dismissal (F7).
4. **MODEL §6** — the prose gloss "a row surviving restart but not the
   completion of its contents" changes with D18 (F7).
5. **MODEL §10** — the P2 event families list gains the `run.*` names and
   the two batch additions above.
6. **D69** — the honesty debt is dischargeable once F4 lands: one open
   Dispatch per Matter is the predicate D43 lacked.
