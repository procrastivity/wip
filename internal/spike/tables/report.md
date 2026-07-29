# Spike B — tables + audit-log

`store-fork` step-03. One-day-spike-grade throwaway code whose only product is
evidence for a decision record. Scope is exactly `docs/store-fork/scenario.md`.

**Shape.** Normalised entity tables are the source of truth. Every verb writes
the entity row as its primary act; the same transaction *additionally* appends
one row to a separate `events` table. "What is in progress" is a plain SELECT.

**Size.** 853 lines of non-test Go+SQL across five files, 516 lines of test.

| file | lines | what it is |
|---|---|---|
| `schema.go` | 165 | the whole schema, as numbered migrations (16 SQL statements in v1, 2 in v2) |
| `store.go` | 389 | the five verbs, the two reads, the transaction-scoped reads |
| `coupling.go` | 131 | the Go half of invariant 1 — `inTx`, `mutation`, `apply` |
| `migrate.go` | 111 | the migration runner + backup-before-migrate |
| `label_v2.go` | 57 | the entire Go cost of the v2 exercise |
| `store_test.go` | 516 | conformance + 14 bypass probes + refusals + migration + log-replay |

Resulting database: **4 tables, 8 triggers, 2 indexes** — for *one* entity
table. That ratio is the headline finding; see §5.

---

## 1. Axis 1 — invariant 1 ("every verb emits exactly one event; no mutation path bypasses this")

### Is it natural, or bolted on?

**Bolted on, and the spike is the story of bolting it on well enough to trust.**
In this shape the primary act is `INSERT INTO nodes` / `UPDATE nodes`. That
statement is complete and correct on its own — SQLite will happily execute it
with no event anywhere. The event is a *second* statement that a disciplined
caller remembers to write. Nothing about the shape makes forgetting it hard.
That is precisely the failure mode MODEL §10 is guarding against, so "we will
remember" was not an acceptable answer and I did not take it.

### Where the guarantee actually lives

In **two places, deliberately redundant**, one of which does not depend on the
Go code being obeyed.

**(a) Go-side containment — `coupling.go`.** Three properties, by construction:

1. No verb ever holds a `*sql.Tx`. `Store.inTx` hands the verb body a `*txn`,
   whose only write method is `apply`.
2. `apply` is the single call site in the package that executes a statement
   against `nodes`. It mints the event ULID itself, writes the audit row, then
   executes the node write with that same ULID bound to `:event`. A caller
   cannot supply, skip, reuse, or reorder the event id.
3. A `mutation` is **data, not a callback** (`sql string` + named `args`), so
   there is no seam through which a verb could receive a handle and write
   something extra. `apply` also asserts `RowsAffected() == 1`, so one event can
   never stand for zero or two node transitions.

`apply` also stamps tier dimensions from `scenario.RequiredDimensions` — D56's
static function of type — so a verb author never chooses them.

**(b) Schema-side enforcement — `schema.go`.** This is the part that makes the
guarantee structural rather than disciplinary. Two triggers on `nodes`:

```sql
CREATE TRIGGER nodes_insert_requires_event AFTER INSERT ON nodes
 WHEN NOT EXISTS (SELECT 1 FROM events WHERE id = NEW.last_event_id AND subject = NEW.id)
 BEGIN SELECT RAISE(ABORT, 'nodes INSERT must carry an event about that node'); END;

CREATE TRIGGER nodes_update_requires_event AFTER UPDATE ON nodes
 WHEN NEW.last_event_id <= OLD.last_event_id
   OR NOT EXISTS (SELECT 1 FROM events WHERE id = NEW.last_event_id AND subject = NEW.id)
 BEGIN SELECT RAISE(ABORT, 'nodes UPDATE must advance to a newer event about that node'); END;
```

`nodes.last_event_id` is a `NOT NULL REFERENCES events(id)` column: every node
row permanently names the event that last touched it. The `<=` (rather than
`<>`) in the UPDATE trigger is load-bearing — event ULIDs ascend globally, so
only an event minted *after* the row's current one can satisfy it. A stale or
replayed event id cannot.

Consequences that fall out for free and are asserted in
`TestNodeWritesCannotBypassTheLog` (14 cases, all executed as **raw SQL on the
same connection, around the Go API**, because a guarantee that only holds
through Go would pass every test that goes through Go):

- A node write with no event: refused.
- A node write reusing the row's own current event: refused.
- A node write naming an event about a different node: refused.
- **A single UPDATE touching two rows: refused.** Both rows would get the same
  `NEW.last_event_id`, and an event's `subject` can match only one of them. Bulk
  mutation is structurally impossible, which is a stronger statement of
  "exactly one" than the Go layer makes.
- `UPDATE`/`DELETE` on `events`: refused (append-only is a table property).
- An event inserted out of order: refused (`events_monotonic`).
- An event type outside the taxonomy: refused by FK to `event_types`.
- An event whose dimensions disagree with D56's rule for its type: refused.
- Changing a node's id, kind, parent or birth event: refused.
- Deleting a node: refused (removal must be a tombstone verb, D44).

The D57 cascade is the sharpest test and it passes cleanly: `Start` on a Step
under a Planned Matter loops `apply` twice, ancestor-first, producing exactly
`matter.started` then `step.started`, each with its own fresh event bound into
its own row. Nothing special was needed for the cascade — it is two mutations
in one transaction.

Refusal semantics come from the transaction boundary: `inTx` commits only if
the verb returns nil, so a refused command writes nothing.
`TestRefusedCommandsWriteNothing` checks all nine refusals.

### What could still bypass it — honestly

1. **The converse direction is wide open.** The schema stops a *write without an
   event*. It does not stop an *event without a write*. Anyone with the database
   file can `INSERT INTO events` freely, and nothing will ever notice.
   `TestTheHonestHole` asserts this explicitly: it appends a `matter.finished`
   for a Matter that is still in progress, the insert succeeds, and the tables
   go on correctly reporting the Matter as in progress. The log can be padded
   with events that never happened. I could not close this with a deferred
   foreign key, because `nodes` holds only the *latest* event per row — the
   events a node has already advanced past are legitimately unreferenced. To
   close it I would need a per-mutation append-only table of node versions,
   which is event sourcing with extra steps. **In the event-sourced shape this
   hole cannot exist**, because there is no state for the log to disagree with.

2. **The guarantee is per-table, not per-store.** `nodes` is covered because
   somebody wrote `nodes`-specific triggers. A future `prose`, `gates`,
   `batches`, `backlog`, `tiers`, or `cursors` table has *no* coupling at all
   until somebody adds a `last_event_id` column and two more triggers to it.
   Nothing warns you. There is no schema-level assertion of the form "every
   mutable table is coupled"; SQLite cannot express one.

3. **Triggers can be dropped.** `DROP TRIGGER` is not itself guarded, and
   SQLite has no way to guard it. A migration — or an operator — can remove the
   teeth. `PRAGMA foreign_keys` is per-connection and defaults *off*; the store
   sets it in its DSN, but any other connection to the file (the `sqlite3` CLI,
   a recovery script) has it off and can violate the FKs. The triggers still
   fire for that connection; the FKs do not.

4. **A `mutation` whose `sql` never binds `:event` into `last_event_id`.** Go
   would not catch it, but the triggers would — the transaction aborts. This is
   the case where redundancy (a) + (b) pays off.

5. **Two `Store` handles on one file each own a private ULID source.** They can
   mint interleaved ids. `events_monotonic` turns that into a hard ABORT rather
   than silent disorder, which is the right failure, but it means this shape as
   written cannot support two writers without a shared id source. (Not this
   spike's problem — D34/D43 refuse multi-writer — but worth recording.)

---

## 2. Axis 2 — invariant 2 ("what is in progress")

It cost **one query and one index.** There is no projection, so there is
nothing to keep correct, nothing to rebuild, and no catch-up path.

```sql
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle, COALESCE(label, '')
FROM nodes
WHERE lifecycle = 'in-progress'
ORDER BY born_event_id
```

`EXPLAIN QUERY PLAN` on a populated store:

```
SEARCH nodes USING INDEX nodes_lifecycle (lifecycle=?)
```

One index seek, **no sort step** — the index is `(lifecycle, born_event_id)`, so
the ordering comes out of the seek. Creation order needed no new column:
`born_event_id` is the node's `*.created` event ULID, and event ULIDs ascend, so
the birth event *is* the creation-order sort key. This is the one place where
having the log around made the tables cheaper rather than more expensive.

Durability across close/reopen is free — nothing is held in process memory. The
conformance run's reopen assertion passes without any code written for it.

Nothing about this answer degrades as the log grows: the query never touches
`events`. That is the whole value proposition of this shape and it is real.

---

## 3. Axis 3 — what the v2 migration actually cost

Measured, not estimated: I reconstructed a v1-only baseline from the committed
code (verified it compiles and vets), then diffed.

| file | added | removed |
|---|---|---|
| `schema.go` | +16 | −0 |
| `store.go` | +35 | −6 |
| `label_v2.go` | +51 | −0 (new file) |
| **total** | **+102** | **−6** |

**3 files touched, 1 of them new.** Of the 102 added lines, roughly 45 are
comments.

The schema half is **two SQL statements**, both additive:

```sql
ALTER TABLE nodes ADD COLUMN label TEXT;
INSERT INTO event_types (type, requires_repo, requires_clone, requires_worktree)
VALUES ('matter.labeled', 1, 0, 0), ('step.labeled', 1, 0, 0);
```

**What had to be true about existing data: nothing.** The column is nullable, so
every pre-existing row was already valid under the new schema. No backfill, no
default, no rewrite, no data-migration pass, no downtime window. The read path
`COALESCE(label, '')`s the NULLs into empty strings, so v1-born rows answer the
in-progress query with an empty label and are otherwise untouched.
`TestMigrationV1PopulatedThenV2` proves this end to end: it populates at v1 (via
`OpenAtVersion(path, env, 1)`), closes, reopens with `Open` (which migrates),
and then checks that the two v1-born nodes still answer in the right order with
locator and title intact, that the v1 event log survived byte-identical, that
the new verb works on a row born before the column existed, and that the new
event type inherits the D56 dimension rule automatically.

The Go half is `label_v2.go` — a new verb that is a pre-check plus **one**
`mutation`. It needed *zero* new coupling code: it goes through the same
`apply`, and the same two `nodes` triggers cover the new column for free (the
migration test asserts that a raw `UPDATE nodes SET label = …` is still
refused). That is the clearest single piece of evidence that the coupling
generalises across *verbs* even though it does not generalise across *tables*.

Backup-before-migrate (PLAN 1.2) is a nine-line file copy to
`<path>.bak-v<current>`, taken before the first pending migration runs, asserted
by the test. Cheap because the whole store is one file — true of either shape.

**The one cost I would not have predicted:** the read path had to become
version-aware (`inProgressSQLv1` / `inProgressSQLv2`, +3 lines of dispatch), and
the return type had to widen (`LabeledNode` wrapping `scenario.Node`). Honesty
requires noting that the version dispatch is an artifact of the *test* needing a
v1-pinned store to migrate from; production always migrates on open and would
only ever need the latest query. The type widening is real and would recur for
every future field the read path surfaces.

---

## 4. Axis 4 — the MODEL §10 fidelity list, item by item

### append-only

**Free, and genuinely enforced.** Two triggers (`events_no_update`,
`events_no_delete`) plus `events_monotonic`, which makes the log append-only *at
the end* — new events must sort after every existing one, which is what lets the
event ULID double as the total-order key with no sequence column (D44, D51).
Five lines of SQL. Asserted from raw SQL.

**Owed:** nothing for `events`. But note the asymmetry this shape is built on:
`nodes` is deliberately *not* append-only. The store therefore has two truths
with different durability properties, and only one of them is tamper-evident.

### identity-not-locator references

**Mostly free.** Everything that references anything references a ULID:
`events.subject`, `nodes.parent`, `nodes.born_event_id`, `nodes.last_event_id`.
Locators live in `nodes.locator` and appear in payloads only as facts about the
write, exactly as `scenario.md` permits:

```
step.created | subject=01KYQ…BTHJ | {"kind":"step","locator":"step-01",
                                     "parent":"01KYQ…K4Y","title":"a step"}
```

The parent is carried as a ULID, never as `"matter"`.

**Owed:** the schema's guard is `CHECK (length(subject) = 26)`, which is a weak
proxy — a 26-character locator would pass. A `DEFERRABLE INITIALLY DEFERRED`
foreign key from `events.subject` to `nodes.id` would be exact, and I
deliberately did not add it: it would be wrong the moment MODEL's Batch-subject
events arrive (batches are not nodes) and wrong for tombstoned subjects. So the
real enforcement is a length check plus code review. This is a genuine gap and
it is identical in either shape.

### tier dimensions

**Free, and checked in the schema exactly as D56 asks.** The taxonomy is a
table, not a Go constant:

```sql
CREATE TABLE event_types (type PRIMARY KEY, requires_repo, requires_clone, requires_worktree);
```

`events.type` is a foreign key to it (an unknown type is *rejected*, not
silently logged), and `events_dimensions` refuses any row whose null-pattern
disagrees with its type's rule. Go computes dimensions from
`scenario.RequiredDimensions`; the schema is the authority and aborts on
disagreement. Adding v2's two event types with their rule was one INSERT and
zero changes to the dimension logic. Batch-subject events with a null repo
already work (`requires_repo = 0`).

**Owed:** nothing structural. `internal/spike/scenario`'s Go rule and the
`event_types` table are two encodings of one fact and can drift; the drift is
loud (an ABORT) rather than silent, but a real implementation should generate
one from the other.

### system-driven events first-class

**This is where the shape is weakest, and it is a structural weakness, not an
effort one.** In this shape an event is *caused by* an entity-row write, and the
coupling trigger is written in those terms (`subject = NEW.id`). A system-driven
event — a Builder closing, CI going red — may involve no entity-row change at
all, or a change to something that is not a `nodes` row.

Mechanically they are *permitted* today: the log accepts any append (§1's honest
hole cuts both ways). But they are **second-class**, because they get none of
the schema's guarantees — nothing checks that the write they describe happened,
nothing couples them to state. And when a system event *does* drive a state
change (CI turning a gate red), making it first-class means giving its target
its own table, its own `last_event_id`, and its own two triggers: roughly 10
lines of SQL and one more thing nobody re-checks, **per subject type**.

In the event-sourced shape a system-driven event is just an event; there is no
distinction to make. That difference is not a matter of implementation effort.

### enough to reconstruct in-flight work

**Free from the tables; true-but-unguarded from the log.**

From the tables it is trivial — they *are* the in-flight state. That triviality
is exactly why this item is easy to under-build here: nobody exercises the log,
so nobody notices when it stops being sufficient.

So I wrote the test that notices. `TestLogAloneReconstructsState` rebuilds every
node from `events` and nothing else, then holds the rebuild against what the
tables report. **It passes today** — the payloads carry `kind`, `title`,
`locator`, `parent`, `from`/`to`, and `label`, and the log's own total order
supplies creation order, so a full rebuild needs nothing the log does not have.

But that is true by my discipline, not by structure. **Nothing checks that a
payload is sufficient.** A future verb that writes a column and puts nothing
useful in its payload breaks reconstruction silently, and the tables keep
answering perfectly, and only this test — which a real implementation has no
particular reason to keep — would ever fail. In the event-sourced shape
insufficient payloads break the *primary* read path immediately and loudly.

**Owed:** a standing obligation, per verb, forever, that this shape gives you no
mechanism to discharge.

### enough to narrate a sealed Matter after the fact

**Partly free, materially owed.** What the log has: a total order, a UTC
timestamp per event, the subject's identity, tier dimensions, and payloads rich
enough to reconstruct what each node was and what changed. Someone with only the
`events` table and no access to this machine could reconstruct the sequence of
this scope's work and render it as prose.

What it is missing, and this is not specific to the fork but must be paid
somewhere:

- **No actor.** Nothing says who or what performed a verb. §10's system-driven
  requirement ("outer-loop roles are invisible to Session") needs this.
- **No causation or correlation.** D57's cascade emits `matter.started` and
  `step.started` with nothing recording that they were *one command*. A narrator
  reading the log sees two independent starts. This is the single most
  narration-relevant omission in the spike and it would cost one nullable
  `caused_by` column plus one field in `apply`.
- **No reason / no prose.** Out of scope here, but a narration made of
  lifecycle transitions and titles is a state machine transcript, not a story.
- **Two clocks.** `occurred_at` is a text timestamp written by Go; the event's
  ULID carries an independent millisecond timestamp. They can disagree, and
  nothing checks them against each other. There is no `recorded_at`, so a
  back-dated `occurred_at` is indistinguishable from a real one.

Export-on-seal itself stays possible: the log is self-contained for this scope
(no payload points at anything outside the log), which is the actual
precondition. But "possible" is doing work — the *narratability* of the export
is limited by the four gaps above, and the tables shape gives no pressure to
close any of them, because nothing in daily operation reads the log.

---

## 5. Things the shape made obvious that the rubric does not ask about

**The trigger-to-table ratio is the finding.** One entity table needed
**8 triggers** to make invariant 1 structural (2 coupling, 2 append-only,
1 monotonicity, 1 dimensions, 1 immutable-birth, 1 no-delete). Four of those are
generic to the log and paid once; **two are per-entity-table and must be
rewritten for every mutable table MODEL adds** — prose, gates, batches, backlog,
tiers, cursors, archive. The guarantee does not compose; it is re-earned N times,
by hand, in SQL, with no way to assert that you did it. Extrapolating MODEL §9's
entity list, that is on the order of 20 hand-written triggers that must all stay
correct, and any one of them being missing is invisible.

**Making the event taxonomy a table was the highest-leverage six lines in the
spike.** It gave: FK rejection of unknown types, D56 as data rather than code,
and a v2 migration whose event-type half was a single INSERT with zero Go
changes. If this shape wins, keep that idea; if it loses, port it.

**Named SQL parameters shaped the design.** `insertNodeSQL` binds `:event` twice
(`born_event_id` and `last_event_id`), which is why `mutation` carries named
args rather than positional ones — which in turn is why `mutation` could be data
instead of a callback, which is the whole Go-side containment argument. A small
mechanical detail turned out to be load-bearing.

**`apply`'s `RowsAffected() == 1` check is not decoration.** The transition
statement is guarded (`WHERE id = :id AND lifecycle = :from`), so it can match
zero rows under a race. Without the check, that would commit an event describing
a transition that did not happen — invariant 1 violated in the direction the
triggers cannot see.

**Statement counts per verb** (for whatever it is worth against Spike A):
`CreateMatter` = 2 writes, 0 reads. `AddStep` = 2 reads + 2 writes. `Start` with
a D57 cascade = 2 reads + 4 writes. `Finish` / `Label` = 1 read + 2 writes. Each
node write additionally fires an indexed `EXISTS` probe on `events(id)`.

**What was awkward.** The version-dispatched read path (an artifact of needing to
manufacture a v1 store, §3). The two-clock situation (§4). The fact that the
whole §1 argument is unfalsifiable from inside Go, so the only meaningful tests
had to be raw SQL executed around the API — which is not how anyone normally
tests a store, and is a maintenance liability nobody will honour in six months.

---

## 6. Top two honest weaknesses of this shape

1. **The log's truthfulness is one-directional and cannot be made
   bidirectional.** The schema can guarantee no write happens without an event;
   it cannot guarantee no event is recorded without a write, and I could not
   find a way to make it (§1, hole 1, asserted by `TestTheHonestHole`). Every
   fidelity item that *trusts* the log — reconstruct-in-flight,
   narrate-a-sealed-Matter, and therefore export-on-seal — rests on a record
   that nothing in the system ever reads in normal operation and that nothing
   can verify. Under-built payloads, drifted payloads, and outright fabricated
   rows all look identical to a healthy store, because the store answers from
   the tables. In the event-sourced shape this entire class of failure does not
   exist.

2. **The invariant-1 guarantee is per-table and does not compose.** It cost 2
   triggers plus a column plus a redundant Go containment layer to cover *one*
   entity table, and every one of MODEL §9's remaining entity types pays the
   same price again, by hand, with no mechanism to detect that it was skipped
   (§5). System-driven events make this worse, not better, because they are
   exactly the events whose subject is *not* a node (§4). The shape's advantage
   — invariant 2 is a free indexed SELECT with no projection to maintain — is
   real, decisive on that axis, and paid for on every other axis by an
   obligation that recurs with every table and every verb the model grows.
