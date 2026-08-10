# Tracker push-model decisions

2026-08-09. This record closes appendix-tracker items 1–4 — granularity,
direction, trigger set / medium / timing, and configurability — assigned to
`push-model`, the Phase 3 design record that hardens irreversibly. The
operative lines below are ready for the next free numbers in MODEL §12 at
phase close. The local labels T1–T10 (T for tracker push; F–K are used by
prior sessions, and L/P collide with the loose-end and phase namespaces)
preserve their order without allocating D-numbers early. Each label is one
operative decision, so the append allocates one D-number per row.

Related records: `run-model-decisions.md` (F1–F9), `parallelism-decisions.md`
(G1–G5), `surface-3-decisions.md` (S1–S6). Citations to T-labels elsewhere in
Phase 3 are citations to this record until the append lands.

Settled inputs, restated not reopened: reference is a nullable column on
identity, late-bound by command (D1, §5.3, D44); the outbox batches external
writes for boundary approval (D15, §5.1); delegation collapses to a stub
(D8); wip states never leak provider names inward; never move an issue
backward, never auto-complete to Done (§11).

| Local | Operative decision |
|---|---|
| T1 | **The tracker seam has no inbound synchronization path:** tracker state never changes wip state or reconciles wip history. External state writes use a provider-supported lease or conditional transition; the provider may inspect an opaque revision or current state only to guard that write. |
| T2 | **Push is Matter-boundary-only by default:** the issue's state mirrors the Matter's lifecycle. Steps never push; Stage closures are visible only at the opt-in narrated level, as comments on the Matter's issue. |
| T3 | **The boundary trigger set is started, canceled, sealed.** Paused pushes nothing. Completion proposes at sealed, never at Done. |
| T4 | **State changes are the only default medium.** A comment cannot be un-written, so no default level writes one; comments exist only at the narrated level, for Stage closures. |
| T5 | **Every external write rides the outbox — no immediate path.** At flush, entries compose per reference: state entries collapse to the newest non-regressing state; comments never collapse. |
| T6 | **Never-backward has two guards:** wip withholds a candidate that regresses its push record, and the provider atomically refuses a write when its lease no longer matches tracker state. A regression or lease mismatch stays visible with its reason — never flushed, and never dropped silently. |
| T7 | **Late binding is current-state-only:** binding queues one entry proposing the issue reflect the Matter's current state, including a completion proposal for an already-sealed Matter. History before binding is never replayed. |
| T8 | **Gate failure is invisible to the tracker:** it sees the eventual green (the seal), never the red. |
| T9 | **A shared reference derives one monotonic state from all bound Matters:** it becomes active when any bound Matter starts and stays active while any bound Matter is nonterminal. After all terminate, completion is proposed if at least one sealed; cancellation is proposed only if all were canceled. |
| T10 | **Tracker push level is per-project Repo-tier config:** `off` \| `boundary` (default once a backend is configured) \| `narrated`. A config change is lazy and non-retroactive: the current level applies when the next qualifying boundary occurs; it creates no entry, replays no history, and does not rewrite queued entries. No per-event-class matrix. |

## Rationale and consequences

### T1 — one-way authority, with a write lease

D36 already makes one-directional push structurally available: there is no
second source. A provider guard does not create one: an opaque revision or
current provider state is inspected only as a precondition of one outward
write. It never changes a Matter, reconstructs wip history, or becomes an
inbound tracker projection.

The load-bearing consequence is a force-with-lease contract. Wip records the
provider revision established when it acquires the first lease or completes a
successful write. A later write presents that revision as an atomic
precondition. Tracker-side edits invalidate the lease, so the provider refuses
the write and wip withholds the entry for human resolution. Merely reading and
then writing unconditionally is insufficient because tracker state can change
between the two operations.

Tracker-originated work still arrives — through intake into the backlog,
a human-driven path that exists regardless of this seam. Intake is not a
read path: wip imports an artifact and then owns it (D40).

### T2 — the issue tracks the Matter

The reference binds at Matter scale (the settled Matter-level address), so
the issue's single state column can mirror exactly one thing: the Matter's
lifecycle. Pushing Step or Stage transitions into that column would make the
issue flap below the grain its watchers care about.

Steps never push at any level: implementation lifecycle is what wip keeps to
itself (MODEL §1). Stage closure is the one grain between Matter and Step
with a plausible audience, and it cannot be a state change (the column is
taken), so it surfaces as a comment — and only at the narrated level,
because of T4.

The irreversibility argument sets the default. Noise in a system wip does
not own cannot be taken back, so the coarsest useful grain is the default
and finer grain is opted into per project.

### T3 — which transitions write

Started, canceled, and sealed are dispositions the tracker's audience cares
about; paused is prospective scheduling state, not a disposition, and
pushing it invites a backward move on resume. Completion proposes at sealed,
not at Done: D55 makes sealed the Matter-scale completion notion, and D60's
negative-review path then falls out of scope naturally — a red gate leaves
the Matter unsealed, so no completion entry exists to retract (T8).

### T4 — states, not comments

A tracker human can move a wrongly-advanced state back with one click; a
comment cannot be un-written. The default levels therefore write only state
changes. Comments exist at exactly one place — narrated-level Stage
closures — where a project has explicitly accepted the permanence.

### T5 — one timing rule, and flush composition

The appendix asked whether state changes might write immediately while
creation batches. Answered no: one timing rule for every external write,
because §5.1's argument is about the *boundary* — nothing leaves the system
without a human seeing a list — and that argument does not care what kind of
write is leaving. With no backend the outbox accumulates and never flushes,
a legitimate steady state (§5.1).

Flush composes per reference. A Matter started and sealed between flushes
(scenario 3) queues two state entries; pushing both is noise, so state
entries for one reference collapse to the newest non-regressing state.
Comments never collapse — each names a distinct Stage closure.

### T6 — two guards, withheld visibly

A candidate that regresses wip's own push record is withheld before the
provider call. A locally valid candidate still carries the last provider
lease; if tracker-side state changed, the provider atomically rejects the
write and the entry becomes withheld for lease mismatch. Either result stays
in the batch with its reason, so the outbox never lies by dropping it silently.

A provider that cannot supply an atomic conditional state change cannot claim
the never-backward guarantee. Its automatic state write is refused rather
than weakened to a check-then-write race. The human can act by hand in the
tracker when the proposed transition was actually intended. "Never
auto-complete to Done" remains structural: completion is always a proposed
outbox entry a human approves, like every other push.

### T7 — late binding replays nothing

Binding is a late, mutable update to a nullable column (§5.3); the events
before it predate the tracker relationship, and backfilling them would be
narration into someone else's system about a period it never watched. So
binding queues exactly one entry: make the issue reflect the Matter's
current state. For an already-sealed Matter — the link-done-work-after-the-
fact case — that entry is a completion proposal. The first guarded push
acquires or validates the provider lease; the human approving the flush sees
whether an already-Done issue makes the proposal a no-op.

### T8 — no red

Gates are wip-internal quality machinery. Under T3, no completion entry
exists until the Matter seals, so a failing gate simply delays the proposal;
the tracker sees the eventual green and never the intermediate red. A
project that wants red visible in the tracker is asking for a different
seam (CI/forge), not a leakier push model.

### T9 — the shared issue has one monotonic state

Two Matters bound to one issue (the many-nodes-to-one-reference shape the
Matter-level address implies) cannot each drive the issue's single state
column from their own lifecycle. Planned Matters do not hold an already-active
reference backward: the reference becomes active when any bound Matter starts
and stays active while any bound Matter remains nonterminal.

When every bound Matter is terminal, the outcomes combine as follows: at
least one sealed Matter proposes completion; cancellation is proposed only
when every Matter was canceled. A canceled Matter is therefore an abandoned
or superseded route to the shared outcome, not a veto over a route that
sealed. Workstreams that must all succeed belong as required children of one
Matter rather than as independent Matters sharing a reference.

Late binding cannot silently reopen a tracker item that an earlier Matter
completed: the new active proposal is a local regression against the push
record or a provider lease mismatch, and T6 withholds it. The
per-node-reference question itself belongs to `reference-model` (appendix
item 7).

### T10 — two coarse levels, applied lazily

Per-project, Repo-tier, up-front config (D4, D42, D54): `off`, `boundary`,
`narrated`. Configuring a tracker backend expresses intent to use it, so
`boundary` is the default once a backend exists; `off` stays legal so a
project can bind references purely for linkage. A per-event-class matrix is
rejected as a second configuration language — the D80 shape: when the coarse
levels don't fit, the answer is a decision amending the levels, not a matrix
that makes every project's push behavior bespoke.

Changing the level emits nothing by itself. The level in force when a
qualifying boundary occurs decides whether that boundary queues an entry.
Moving from `off` to `boundary` does not synchronize a Matter immediately or
replay boundaries already crossed; the next start, cancel, or seal is the
first opportunity. Moving to or from `narrated` does not backfill or remove
Stage comments. Entries already queued retain the level under which they were
created and remain available for approval or decline.

## The eight scenarios, traced

| # | Scenario | Trace against T1–T10 |
|---|---|---|
| 1 | No-Stage Matter | Boundary level unaffected — triggers are Matter-scale (T3). Narrated level degrades gracefully: no Stage closures exist, so it behaves as boundary. |
| 2 | Stage closure visibility | Invisible at boundary level. At narrated level, each Stage closure is one comment on the Matter's issue (T2, T4); never a state change. |
| 3 | Matter-as-smallest-node | Started and sealed may queue between two flushes; flush composition collapses them to one push of the newest state (T5). |
| 4 | Late binding | Current-state-only: one entry at bind (T7). Already-sealed Matter → completion proposal. The first write acquires or validates a provider lease; an already-Done issue can make the guarded proposal a visible no-op. No backfill, ever. |
| 5 | Gate failure | No red: completion proposes only at sealed (T3), so an open or failing gate means no entry exists yet; the tracker sees only the eventual green (T8). |
| 6 | Cancel / pause mapping | Canceled pushes at the boundary, mapped to the provider's equivalent by the provider layer (names never leak inward). Paused pushes nothing (T3). On a shared reference, one cancellation leaves the issue active while another Matter is nonterminal; all canceled proposes cancellation; any sealed after all terminate proposes completion (T9). |
| 7 | Two Matters, one issue | Any start makes the reference active; it stays active until both Matters terminate. Two sealed or one sealed plus one canceled proposes completion; two canceled proposes cancellation (T9). A late-bound active Matter cannot silently reopen an earlier completion because T6 withholds the regression or lease mismatch. |
| 8 | Tracker-side hand edits | A hand edit invalidates the provider lease (T1). The next conditional write is atomically refused and remains visible as withheld (T6); tracker state never changes wip state, and the human resolves the drift. |

## Event work

This record fixes semantics, not schema. The appendix's P3 additions —
`tracker.state-pushed` (per entry, on flush) and the reference-rebind and
delegation events completing the backlog exit set — extend the P1 envelope
unchanged; `tracker-substrate` owns their payloads, the outbox entry
lifecycle (queued, approved, declined, withheld, flushed), and the push
record's storage. Nothing here adds an event family beyond what the
appendix already names.

## Implementation contract for downstream Matters

- `reference-model` owns the reference shape this record leans on: the
  Matter-level address, many-nodes-to-one ratification (appendix item 7),
  and rebind semantics. T7 and T9 consume whatever it ratifies.
- `tracker-substrate` implements the outbox entry lifecycle, flush
  composition (T5), the withheld state and its visibility (T6), the
  per-reference push record serving both dedup (§5.4) and never-backward
  (T6), the provider lease token, lazy push-level config (T10), and
  failure/retry (appendix item 5). Every write path is a verb emitting exactly
  one event.
- `tracker-provider` owns the outward state mapping — wip dispositions to
  provider states — and the atomic conditional-write capability under the
  never-leak-inward rule, plus the stub retirement boundary (appendix item 6)
  and its interaction with T10 levels. A provider without an atomic guard
  refuses automatic state writes rather than claiming never-backward.
- `tracker-traces` re-runs these eight scenarios against the live verbs, the
  D65 way: every scenario traced per run, a clean run being an empty
  findings list.

## For the phase-close append

1. Append T1–T10 as the next free D-numbers (D93+ as of this writing).
2. MODEL §11 — the refusal list's "moving a tracker issue backward or
   auto-completing it to Done" line gains its enforcement locus: evaluated
   first against the push record, then enforced against tracker drift by an
   atomic provider lease (T1, T6).
3. MODEL §5 — the outbox bullet gains flush composition (T5) and the
   withheld state (T6).
4. appendix-tracker items 1–4 are closed by this record; items 5–7 remain
   with `tracker-substrate`, `tracker-provider`, and `reference-model` as
   assigned above.
