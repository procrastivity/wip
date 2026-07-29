# store-fork — the evaluation rubric applied (step-04)

Both spikes scored against the four axes the workplan resolved. Written so
someone who read neither spike's code can follow the comparison; every claim
below is traceable to a named test or a quoted line in
`internal/spike/eventsourced/report.md` or `internal/spike/tables/report.md`.

**How the comparison was set up.** Both spikes implement one interface
(`internal/spike/scenario`), pass one shared conformance body, share one ULID
source, and use the same SQLite driver — pinned in step-01 before either
started, so nothing below turns on one spike having interpreted the task more
conveniently. They were built independently and in parallel, neither able to
see the other's code, and each was told in the same words that a report saying
only nice things about its own shape is a failed report. Both obliged: each
spike's stated top weakness names the other shape's corresponding strength.
Where they agree against their own interest, the agreement is worth more than
either report on its own, and this document leans on that.

**Verified independently of the reports:** both packages' tests pass; the line
counts, the SQL quoted below, the index definitions, and the named tests all
exist as described.

## Scorecard

| Axis | Spike A (event-sourced) | Spike B (tables + audit-log) | Winner |
|---|---|---|---|
| 1 — invariant 1: every mutation naturally one event | Structural. One write path; the envelope is *stamped*, not constructed. | Bolted on by the shape's own admission, then made structural per-table with a FK plus two triggers. One-directional even so. | **A**, clearly |
| 2 — invariant 2: cheaply queryable | Same indexed SELECT, over a projection costing 84 lines and one extra write per event. | Same indexed SELECT, over the authoritative table. Nothing to maintain. | **B**, by less than the fork assumed |
| 3 — migration ergonomics | 9 shape-attributable lines; the derivation can always be re-derived. | 2 additive SQL statements; taxonomy-as-data made the event half free. | tie on the easy change, **A** on the hard one |
| 4 — cost of the §10 fidelity list | Most items free or structural; the log is the read path, so under-building fails loudly. | Two items free and better-built than A's; the trust-the-log items become an unverifiable standing obligation. | **A**, decisively |

## Axis 1 — invariant 1

> *Is every mutation naturally a single event, or does the shape require
> bolting the envelope on afterward?*

**Spike A: natural.** A verb never constructs an event. It returns drafts
carrying three fields — type, subject, payload — and there is no field in that
struct for an id, a timestamp, or a tier dimension, so a verb *cannot* express
an event that is missing them. One function adds the envelope, so D56's
"required dimensions are a static function of event type" is one call made
once rather than six decisions made in six verbs. One function opens
transactions. The function that moves state takes an event as its only data
argument, so there is no signature in the package by which state moves without
one. D57's cascade — one command, several verbs — cost nothing extra, because
a verb already returns a slice.

**Spike B: bolted on, and the spike is the story of bolting it on well.** Its
report opens the axis by conceding it: the primary act is an `INSERT`/`UPDATE`
that is complete and correct on its own, and the event is a second statement a
disciplined caller remembers. It then refuses to leave it at discipline, and
the result is the more interesting half of this spike. Each node row carries a
`last_event_id` that is a `NOT NULL` foreign key into the log; one trigger
requires every insert to carry an event *about that node*, another requires
every update to advance to a **strictly greater** event id. Because event ULIDs
ascend globally, only a freshly minted event satisfies the second. A
consequence neither the workplan nor I anticipated falls out for free: a single
`UPDATE` touching two rows becomes structurally impossible, since both rows
would carry one event id and an event has one subject. That is a *stronger*
statement of "exactly one" than Spike A's Go layer makes, and Spike B's proof
is stronger too — 14 bypass attempts executed as raw SQL around its own API, on
the grounds that a guarantee holding only through Go would pass every test that
goes through Go.

**Why A still wins the axis, on two grounds neither of which is elegance.**

1. **B's guarantee is one-directional and it could not be closed.** The schema
   stops a write without an event; nothing stops an event without a write.
   Spike B asserts this against itself (`TestTheHonestHole`): it appends a
   `matter.finished` for a Matter that is still in progress, the insert
   succeeds, and the tables go on cheerfully reporting the Matter as in
   progress. Closing it, its report concludes, needs a per-mutation append-only
   table of node versions — "event sourcing with extra steps." In A this class
   of disagreement cannot exist, because there is no second truth to disagree
   with.
2. **B's guarantee does not compose.** Eight triggers for *one* entity table,
   of which two are per-table and must be hand-written again for prose, gates,
   batches, backlog, tiers, cursors, archive — on the order of twenty more,
   with no way to express or assert "every mutable table is coupled," and any
   one omission silent. A pays its cost once, in one write path, for every
   entity the model will ever grow.

A's residual holes are real and smaller: its transaction function hands verbs a
live handle they could in principle write through (discipline, not structure),
and its log has no column-level `CHECK` constraints, so a malformed envelope is
caught by the harness rather than by SQLite. That second gap is exactly what
Spike B built well — see the carry-forward list.

## Axis 2 — invariant 2

> *Is "what is in progress" cheaply queryable, and at what cost — replay-per-query
> vs. a maintained projection that must itself stay correct?*

**The two spikes converged on the same query.** Not similar; the same:

```sql
-- Spike A, over the maintained projection
SELECT … FROM nodes WHERE lifecycle = 'in-progress' ORDER BY birth_event
-- Spike B, over the authoritative table
SELECT … FROM nodes WHERE lifecycle = 'in-progress' ORDER BY born_event_id
```

Both index `(lifecycle, <birth event ULID>)`; both report an index seek with no
sort step; neither touches the log. Both discovered independently that creation
order needs no sort key of its own, because the birth event's ULID already
ascends.

**This collapses the axis as the fork framed it.** D46 held the question open as
"replay-per-query vs. a plain SELECT," and that turns out not to be the choice
available. Spike A's finding is the load-bearing one: replay-on-read is not a
read-side decision at all, because *every guard is a read* — adding a Step must
confirm the parent is a Matter and count siblings to assign `step-NN`; starting
a node must confirm it is Planned and walk its ancestors. Under replay those
become whole-log folds on the **write** path, so every mutation becomes
O(events). An event-sourced store that satisfies invariant 2 therefore
maintains a projection, and once it does, it writes B's query.

So the axis does not measure query cost. It measures **maintenance cost**, and
there B wins honestly: 84 lines A does not need, one extra write statement per
event, and one obligation with no database-level enforcement — that a single
function remain the only writer of the projection. B has no projection, so it
has nothing to keep correct, nothing to rebuild, and no catch-up path.

Two things bound the margin.

- **A's own strongest objection lands here** and it is the best point B has:
  A's guards read the *derived* table, so "events are the source of truth" is
  true for durability and false for decisions. A stale projection would make
  the store refuse and permit the wrong things and then faithfully log the
  wrong events. B makes that same read against the authoritative table and has
  one fewer thing that can be stale. A's report volunteers this and says it did
  not anticipate it.
- **The failure directions are not symmetric,** which is what keeps the
  objection from being decisive. Under A a divergence yields a wrong *answer*
  over a correct log — recoverable, and A demonstrates the recovery
  (`Rebuild`, tested to reproduce the maintained projection column-for-column
  at both schema versions). Under B the corresponding failure is a correct
  answer over an incomplete *history*, and there is nothing to rebuild from.
  Both spikes state this asymmetry independently; A calls it "arguably the
  whole decision."

## Axis 3 — migration ergonomics

> *How does each shape take a schema change, under PLAN 1.2's
> versioned-migrations + backup-before-migrate posture?*

Both took the same pinned change — nodes gain an optional label, set by a new
verb, emitting a new event type, surfaced in the in-progress answer, applied
over a database already populated at v1.

| | Spike A | Spike B |
|---|---|---|
| production files touched | 3 (1 new) | 3 (1 new) |
| lines | +90, of which **9 attributable to the shape** | +102 / −6, of which the schema half is 2 statements |
| schema change | `ALTER TABLE nodes ADD COLUMN label … DEFAULT ''` | `ALTER TABLE nodes ADD COLUMN label` + one `INSERT` into the event-type table |
| required of existing data | nothing | nothing |
| new coupling/plumbing needed | none — dimensions and taxonomy handled by the existing stamp | none — existing triggers cover the new column, asserted |
| backup-before-migrate | fired, asserted; downgrade refused | fired, asserted |

**On the additive change they are a tie**, and the tie is informative: the
expensive-sounding half of a migration — the event type — was free in both,
for different reasons. A's dimensions are computed by one static function that
new types fall through automatically; B made the taxonomy a *table*, so the new
type was one `INSERT` and zero Go changes. B's approach is the better idea and
is portable to A.

**A wins the axis on the change that actually hurts.** Because A's node table is
derived, a change to the *derivation* rather than to a field — the report's
example, "in progress should exclude Matters whose every Step is Done" — is a
projection rebuild: drop, recreate, re-fold the log. In B the same change is a
data migration with a backfill over live primary data. A always has the
drop-and-rebuild option; B never does. This is scored on mechanism, not on a
second implemented migration — neither spike implemented a derivation change,
and A's rebuild path is tested, so the mechanism is evidenced even though the
specific hard migration is not. It is the axis's softest finding and is
weighted accordingly.

Two costs surfaced that belong to neither shape: backup-before-migrate is cheap
in both because the store is one file, and both spikes' read paths had to widen
their return type to surface the new field — a per-field cost that recurs
regardless of fork.

## Axis 4 — the cost of the §10 fidelity list

> *How much extra plumbing does each shape need beyond its base implementation
> to satisfy the six fidelity items?*

| Fidelity item | Spike A | Spike B |
|---|---|---|
| **append-only** | Total. Triggers, and the shape has no reason to ever update a row. | Total for the log, plus a monotonic-insert trigger. But the store holds two truths with different durability and only one is tamper-evident. |
| **identity-not-locator references** | Total and structural: at stamp time a ULID is the only thing available to reference. Locators live in payloads as facts about the write and in the table as a mutable column. | The same in practice. Both stop at a 26-character length check; both note an exact foreign key is wrong because Batch-subject events reference no node. **Identical in both shapes.** |
| **tier dimensions** | Free at write time, applied to v2's new types without a line of new code. Owes the *check*: nothing in SQLite would notice a stray dimension. | **Better than A, as built.** The taxonomy is a table; the type column is a foreign key into it, so an unknown type is rejected rather than logged; a trigger refuses any row whose dimensions disagree with its type's rule — D56's "checked in schema", literally. |
| **system-driven events first-class** | Free structurally: the write path knows nothing about who called it, so a CI webhook and a human produce identical writes. | **The shape's structural weak point.** Its coupling is written in terms of "an event about this node", and a system-driven event may change no node row at all. Such events are permitted but second-class — they get none of the schema's guarantees — and making each one first-class costs a table plus its own two triggers, per subject type. MODEL §10 is explicit that this is not optional: "a Builder closing and CI going red are events, or outer-loop roles are invisible to Session." |
| **reconstruct in-flight work** | Total, and demonstrated: rebuild-from-log-alone reproduces every projection column, tested at both schema versions. | True today, and B wrote the test that proves it — then says plainly that it holds by its author's discipline, not by structure. Nothing checks that a payload is *sufficient*; a future verb with a thin payload breaks reconstruction silently while the tables keep answering perfectly. |
| **narrate a sealed Matter** (the export-on-seal precondition) | Most of it. The log is the narration and carries from/to on every transition. Owes actor, causation, payload stability. | The same three debts, plus a fourth of its own (two clocks: a Go-written timestamp and the ULID's own, with nothing reconciling them and no recorded_at), plus the structural problem that **nothing in daily operation reads the log**, so nothing pressures it to stay sufficient. |

**The axis turns on one asymmetry, which both spikes state independently.** Under
A the log *is* the read path, so an under-built payload breaks something
immediately and loudly. Under B the log is written and never read, so
under-built payloads, drifted payloads, and outright fabricated rows are
indistinguishable from a healthy store — and every fidelity item that *trusts*
the log (reconstruct-in-flight, narrate-a-sealed-Matter, and therefore
export-on-seal) rests on that unread record. B's report calls this its top
weakness and states that in the event-sourced shape the entire class of failure
does not exist.

That matters more here than it would in most systems, because MODEL §10 puts
these items on the "cannot be retrofitted, binds from event one" list and
because export-on-seal is already the design's acknowledged unmet promise. A
shape whose fidelity is a standing per-verb obligation with no mechanism to
discharge it is the wrong substrate for a promise that must survive years of
verbs being added by people who have not read this document.

## What this comparison does not establish

- **Neither spike is production code**, and neither was written under the
  pressures — concurrency, corruption, scale, a real verb surface — that a
  store meets later. Nothing here says A's projection stays cheap at a million
  events; A's own report flags that `Rebuild` is one transaction over the whole
  log and would need a shadow-table swap and a projection version distinct from
  the schema version.
- **The hard migration was argued, not run.** See axis 3.
- **The scope excluded everything that might have favoured B**: no joins across
  entities, no query that a normalised schema makes elegant and a projection
  makes awkward. B's advantage is real and this scope did not stress-test its
  ceiling.
- **Both spikes were written by the same kind of author on the same day.** The
  convergence noted throughout is evidence, not proof.

## Findings that bind regardless of which shape won

Recorded here because `schema` needs them either way, and three came out of the
spike that lost.

1. **MODEL §10's envelope is missing two fields**, surfaced independently by
   both spikes: **actor/source** (§10's own "system-driven events first-class"
   requirement cannot be met without it) and **causation/correlation** (D57's
   cascade emits two events with nothing recording that they were one command;
   a narrator reading the log sees two unrelated starts). Neither is a fork
   question. Both are on the "cannot be retrofitted" list in spirit, and
   `schema` should decide them before the first event ships.
2. **Make the event taxonomy a table, not a Go constant** (from Spike B): the
   type column becomes a foreign key so an unknown type is rejected rather than
   silently logged, D56 becomes data instead of code, and adding event types in
   a migration costs one `INSERT`. This is D56's "checked in schema" satisfied
   literally, and it is the single most portable idea either spike produced.
3. **Stamp the envelope; never construct it** (from Spike A): verbs name a
   type, a subject, and a payload, and nothing else. No verb should ever be in
   a position to name a tier dimension or mint an id.
4. **A reopened store needs a high-water mark.** Because an event's identity
   *is* the total order (D44, D51), a fresh ULID source in a new process can
   mint an id below the last one on disk if the reopen lands in the same
   millisecond. Read `MAX(id)` at open. Cheap; easy to not notice until it
   produces an unreproducible ordering bug.
5. **Column-level constraints on the log** — id length, type membership,
   per-type dimensions — belong in the schema, as Spike B built them and Spike
   A owes them.
6. **Two clocks need a decision** (from Spike B): `occurred_at` and the ULID's
   embedded timestamp can disagree, and with no `recorded_at` a back-dated
   `occurred_at` is indistinguishable from a real one.
7. **A guarded transition statement must assert it affected exactly one row**
   (from Spike B): otherwise a zero-row update commits an event describing a
   transition that did not happen. Spike A's equivalent check turns a
   log/projection divergence into a write-time error rather than a read-time
   wrong answer. Keep both.

## Verdict

**Event-sourced.** It takes axes 1 and 4 clearly and axis 3 narrowly; it loses
axis 2, at a cost measured in 84 lines and one write statement per event
against a query that turned out to be identical in both shapes. The two axes a
fast invariant-only read would have skipped are exactly the ones that decided
it — which is what the workplan predicted when it refused to let any axis be
dropped.

The decision record is `decision-record.md`.
