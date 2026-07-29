# store-fork — decision record (step-05)

D46 is closed. The entry below is **drafted, not appended**: it goes into
`MODEL.md` §12 verbatim when `schema` opens, per this Matter's resolved open
call and `schema` step-01.

## The entry, as it should be appended to MODEL §12

> | D61 | **D46 closed: the store is event-sourced.** The event log is the source of truth; the entity/content/edge/gate tables are projections maintained in the same transaction as the append. Chosen because invariant 1 and the §10 fidelity list are structural in this shape and re-earned per table in the other, while invariant 2 cost only a maintained projection — both spikes independently wrote the *same* indexed SELECT, so the fork's "replay-per-query vs. plain SELECT" framing did not survive contact. Spikes: winner `internal/spike/eventsourced/`, loser archived at `internal/spike/_archive/tables/`; rubric in `docs/store-fork/rubric.md`. |

### Numbering

**D61** is the next free slot: D54 is MODEL's last, and the lifecycle
resolutions claim D55–D60 (`RESOLUTIONS.md`), which `schema` step-01 also
expects. It is not fixed here — other Matters running concurrently in the
**early-design** Batch may claim intervening slots first, in which case
renumber to the next available on append. Nothing in the entry depends on the
number.

## Pointers

| What | Where |
|---|---|
| The shared scope both spikes implemented, pinned before either started | `docs/store-fork/scenario.md` |
| The harness: contract, ULID source, conformance body | `internal/spike/scenario/` |
| **Winning spike — event-sourced** (`schema`'s literal starting point) | `internal/spike/eventsourced/`, report at `report.md` |
| **Losing spike — tables + audit-log**, archived, not deleted | `internal/spike/_archive/tables/`, report at `report.md`; last live at commit `10ace2f` |
| The four-axis comparison in full | `docs/store-fork/rubric.md` |

## The one-paragraph version, for anyone who reads nothing else

Both shapes can satisfy MODEL §10's two invariants; that was known going in,
which is why the rubric had four axes and not two. The invariants split the
decision — event-sourced takes invariant 1 structurally (one write path, an
envelope that is stamped rather than constructed, D57's cascade for free),
tables+audit-log takes invariant 2 by having nothing to maintain. What broke
the tie was that **invariant 2's margin turned out to be small and invariant
1's did not**: the two spikes independently arrived at the identical
in-progress query over the identical index, because an event-sourced store that
satisfies invariant 2 maintains a projection, and once it does, it writes the
same SELECT. The event-sourced shape's price for that is 84 lines and one extra
write per event. The tables shape's price, on the other side, recurs: its
one-event-per-mutation guarantee is per-table, hand-written in SQL, roughly
twenty more triggers as MODEL §9's entity list is built out, with no way to
assert you did it — and it is one-directional even then (its own test shows the
log can be padded with events that never happened). Fidelity settled it. Under
event-sourcing the log *is* the read path, so an under-built payload fails
immediately and loudly; under tables+audit-log the log is written and never
read, so under-built payloads, drifted payloads, and fabricated rows are
indistinguishable from a healthy store — and every §10 fidelity item that
trusts the log, up to and including the export-on-seal precondition, rests on
that unread record. For a list MODEL calls un-retrofittable, that is the wrong
substrate.

## Carried forward to `schema` (findings, not decisions)

These are not part of the D-entry; they are what the spikes surfaced that
`schema` must decide. Three come from the spike that lost. Full detail in
`rubric.md` §"Findings that bind regardless of which shape won".

1. **The envelope gains `actor`, `causation` and `correlation`.** ~~Missing.~~
   **Resolved and implemented** — both spikes found the gap independently, and
   it was ratified after the fork closed. §10's own "system-driven events
   first-class" cannot be met without an actor; D57's cascade was emitting two
   events with nothing recording that they were one command. This is not a D46
   question and does not belong in the D61 entry; it is an amendment to the
   envelope `schema`'s Brief owns, and it is carried here because `schema`
   inherits the code that already implements it.

   - **`actor`** — one NOT NULL column, prefixed token: `human`,
     `role:<name>`, `system:<source>`. New roles arrive in Phase 2 with no
     migration.
   - **`causation`** — the event that entailed this one.
   - **`correlation`** — the origin event of the chain.
   - Origins **self-reference** (`a:a:a`) rather than carrying null, so
     "began its own chain" and "nobody filled this in" are never the same
     value. A chain where a causes b, b causes c, and c causes d and e reads
     `a:a:a`, `b:a:a`, `c:b:a`, `d:c:a`, `e:c:a`.
   - A cause never points forward: both fields always name an event already in
     the log. That is why the *first-emitted* event of a command is the origin.
   - In a D57 cascade, ancestors wip auto-started carry `system:wip`; the node
     the caller named carries the caller's actor. **Flagged, not settled:** this
     makes the human's own action the derived event and wip's automatic one the
     origin. See `scenario.md` §"Amendment" for the tradeoff; re-examine with
     Stage in scope, where cascades get deeper.

   **This obsoletes the eight-column envelope in `schema`'s Brief §A** — it
   becomes eleven. `workplans/schema.md` needs that amendment applied in
   `wip-reboot`; this session does not edit that repo.
2. **Make the event taxonomy a table** (from the losing spike), so the type
   column is a foreign key, unknown types are rejected rather than logged, and
   D56 is data. This is "checked in schema" satisfied literally.
3. **Column-level constraints on the log** — id length, type membership,
   per-type dimensions — which the losing spike built and the winner owes.
4. **A high-water mark at open.** Identity doubling as total order (D44, D51)
   means a fresh ULID source can mint an id below the last one on disk when a
   reopen lands in the same millisecond. Read `MAX(id)`.
5. **Two clocks.** `occurred_at` and the ULID's embedded timestamp can
   disagree, and with no `recorded_at` a back-dated timestamp is
   indistinguishable from a real one.
6. **The projection needs its own version**, distinct from the schema version,
   so the store knows when a rebuild is required; and `Rebuild` at scale wants
   a shadow-table-and-swap rather than one transaction over the whole log.
7. **Keep both correctness checks** the spikes converged on: assert a guarded
   transition affected exactly one row, and assert the projection write for an
   event touched exactly one row.

## Honest limits on this decision

The spikes are one day's throwaway code over a scope of three verbs. The scope
deliberately excluded everything that might have favoured the losing shape —
no joins across entities, no query a normalised schema makes elegant and a
projection makes awkward — so tables+audit-log's ceiling was never tested. The
migration axis compared one additive field; the harder case (a change to the
*derivation* rather than to a field, where the winner rebuilds and the loser
backfills) was argued from a tested mechanism, not run. And nothing here says
the projection stays cheap at a million events.

None of that is hidden, and none of it changes the answer: the axes that
decided it — invariant 1 and the fidelity list — are about what the shape makes
structurally true, and scale would not move them.
