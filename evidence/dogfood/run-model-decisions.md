# Run and Batch lifecycle decisions

Session F closes the four lifecycle questions assigned to `run-model`. The
operative lines below are ready for the next free numbers in MODEL §12 during
Session K. The local labels F1–F4 preserve their order without allocating
D-numbers early.

| Local | Operative decision |
|---|---|
| F1 | **Run is ratified:** a Run is one Orchestrator pass over one Batch, keyed at Clone; all lifecycle events use the `run.*` family, so no existing event name changes. |
| F2 | **An anonymous Batch has no user-supplied name:** output identifies it as `anonymous · <matter-locator>`; repeated work is disambiguated on the Run, not the Batch, by the Run start time plus a short Run ID. |
| F3 | **A Run freezes its dispatch set when it starts:** Batch joins and leaves during that Run affect only later Runs; one Matter may belong to many Batches, but a live dispatch claim permits at most one Run to dispatch that Matter, and a conflict skips it with an event. Named Batches persist until explicit dismissal and may be empty; anonymous Batches are swept when their sole Run becomes terminal. |
| F4 | **An interrupted Run never resumes automatically:** it remains resumable until an explicit resume or stand-down; resume reconstructs state from events, closes any orphaned Dispatch as `reaped`, and opens a fresh Dispatch. Only the Run's Clone can resume it; when that Clone is unavailable or is not current, reads report the Run as stranded and another Clone can only stand it down, never adopt it. |

## Rationale and consequences

### F1 — ratify Run

`Run` already appears in the event vocabulary, tier model, entity diagram,
refusal list, workplans, and orchestration traces. It also names one precise
thing: one Orchestrator pass over one Batch. Renaming it would create a second
vocabulary or require a coordinated event-family rename without fixing an
ambiguity. Ratification removes D48's provisional marker. New lifecycle
events stay in the `run.*` family.

A Run is not a Session. A Run is chosen, prospective, and drives dispatch. A
Session remains derived, retrospective, and drives nothing. A Run is also not
a Batch: the Batch can persist across several Runs and membership changes.

### F2 — name the anonymous path once

The anonymous Batch is machinery for the single dispatch path, not a durable
human grouping. Giving it an editable or generated name would make hidden
machinery compete with named Batches. Its display name is derived from its
single Matter's current locator: `anonymous · <matter-locator>`. Identity and
events continue to use the Batch ULID, so a later locator change does not
change history.

Two attempts on the same Matter need separate labels, but the Batch does not
provide that distinction: one anonymous Batch can have one Run per attempt.
Human output therefore appends the Run's UTC start time and a short, unique
prefix of its opaque Run ID. The time is useful for scanning; the ID resolves
same-time and repeated attempts. The store and machine output always carry
the full Run ID.

### F3 — separate prospective membership from active execution

At `run.started`, the scheduler records the Batch membership it will consider
for that Run. A later `batch.joined` or `batch.left` event changes the Batch,
but does not mutate the active Run's dispatch set. This snapshot rule makes
churn deterministic and replayable. A user can start another Run to consume
the new membership.

Membership in several Batches remains legal because a Batch is an attention
group. Execution is exclusive: before dispatch, a Run claims the Matter for
its Clone. If another live Run already holds a claim, the contender records a
skip with the conflict as its reason and continues under D30. The check also
makes D43 enforceable when the substrate lands. A claim ends when its
Dispatch closes; membership never grants a claim by itself.

A named Batch is user-owned prospective state. It can become empty and stays
until an explicit dismissal, including when all members are sealed. This
avoids silently deleting a reusable grouping at an arbitrary boundary. An
anonymous Batch has no independent user intent and exactly one Run, so the
system sweeps it when that Run reaches a terminal outcome. Append-only events
preserve both kinds after their live rows leave normal selection.

### F4 — resume only by an explicit decision

Automatic restart could repeat side effects before the system knows why the
process stopped. After restart, an in-progress Run is therefore reported as
interrupted and resumable. `resume` is an explicit write. It reconstructs the
Run from the event log, marks an open Dispatch `reaped`, recomputes the
frontier from current durable state, and opens a new Dispatch. Completed work
is not replayed; work without a completion event becomes eligible again.

Run identity keys at Clone, so resume cannot migrate execution implicitly. A
different current Clone can inspect the host-wide row but cannot resume or
adopt it. Output calls the Run `stranded`, identifies the owning Clone, and
offers stand-down as the safe terminal action. Stand-down is permitted from a
different known Clone because it releases coordination state but performs no
work; its event records the acting and owning Clone identities. A later Run
from another Clone is new identity, not a continuation.

## Implementation contract for downstream Matters

- `run-substrate` supplies Run lifecycle, frozen Run membership, dispatch
  claims, named-Batch dismissal, anonymous-Batch sweep, resume, and
  stand-down events. Batch-subject events keep a null Repo dimension (D56).
- `surface-3` assigns capability classes to resume and stand-down. The present
  record defines behavior, not which porcelain can invoke it.
- `scheduler` consumes the frozen dispatch set, records conflict skips, and
  recomputes the frontier after explicit resume.
- Explicit Batch creation remains unavailable until `run-substrate` seals
  (NEXT-0 loose end L2). This session changes no Batch or Run rows.
