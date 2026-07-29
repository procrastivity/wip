# Spike A — event-sourced. Report.

`store-fork` step-02. Evidence for a decision record, not an argument for a
winner. Where this shape is worse, it says so.

**What was built.** `internal/spike/eventsourced/`, four production files and
two test files, satisfying `scenario.Store` and passing `RunConformance`.

| file | total lines | code lines | what it is |
|---|---:|---:|---|
| `store.go` | 492 | 353 | open, the one write path, the five verbs, the reads |
| `migrations.go` | 206 | 143 | the numbered migration register + backup-before-migrate |
| `project.go` | 119 | 84 | the projection's entire definition, and `Rebuild` |
| `label.go` | 124 | 81 | the whole v2 increment |
| `store_test.go` | 258 | 217 | conformance + four shape-specific tests |
| `migration_test.go` | 162 | 129 | the v2 exercise over a v1-populated database |

Production total 661 code lines; **571 of those are v1**, 90 are v2 (axis 3).

---

## 1. Axis 1 — invariant 1: is every mutation naturally a single event?

**Yes, and the envelope was not bolted on — it is the only thing the write
path knows how to write.** Three structural facts, all in `store.go`:

1. A verb does not construct an event. It returns `[]draft`, and `draft` has
   exactly three fields — `typ`, `subject`, `payload`. There is no field for
   an id, a timestamp, or a tier dimension, so a verb *cannot* express an
   event that is missing them. `Store.stamp` adds them, in one place, for
   every event in the package. D56 ("required dimensions are a static function
   of event type") is therefore literally one call to
   `scenario.RequiredDimensions(d.typ)`, made once, rather than six decisions
   made in six verbs.
2. `Store.commit` is the only function in the package that opens a
   transaction. Every verb is `return s.commit(ctx, func(tx) ([]draft, error)
   { ...guards...; return drafts })`. Its loop is
   `stamp → appendEvent → applyEvent`, in that order, per draft.
3. `applyEvent(ctx, tx, ev)` — the projection's whole definition — takes an
   *event* as its only data argument. There is no signature in the package
   that moves state without an event in hand.

Refusals write nothing for free, not by care: the guard runs inside the
transaction that would have appended, so a returned error means a rollback of
a transaction that never got to `stamp`. `TestRefusedVerbWritesNothing`
exercises seven refusal paths and asserts both the log and the projection are
byte-identical afterwards.

D57's cascade — one command, several verbs — cost **zero** extra machinery,
because the verb already returns a *slice*. `Start` walks to the root, filters
to Planned ancestors, and emits root-first; `commit`'s loop does the rest.
`TestStartCascadeIsExactlyTwoEvents` pins it.

Append-only is enforced by the substrate, not by convention:

```sql
CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events ...
```

`TestLogIsAppendOnlyInTheSubstrate` issues a raw `UPDATE events` and a raw
`DELETE FROM events` against the handle and asserts both are refused.

### What could bypass it — four answers, honestly

- **Nothing can bypass the log's shape once an insert reaches it**, but the
  triggers guard `UPDATE`/`DELETE`, not `INSERT`. `appendEvent` is the only
  `INSERT INTO events` in the package and it takes a `scenario.Event`, of which
  `stamp` is the only real constructor — but `scenario.Event{}` is a plain
  struct any code in the package could zero-value. **Owed:** column-level
  `CHECK(length(id) = 26)`, `CHECK(type IN (...))`, and a per-type dimension
  check, so the schema refuses a malformed envelope rather than trusting Go.
  MODEL D56 says "checked in schema"; here it is checked only by the harness.
- **The projection has no such protection and cannot have any**, because
  `Rebuild` must be able to `DELETE FROM nodes`. Any code in the package with
  the `*sql.DB` could `UPDATE nodes` directly and the query would start lying.
  Note the failure *direction*, though: the log would still be right, so the
  damage is recoverable by one `Rebuild`. That asymmetry is the shape's real
  safety property, and it is the thing Spike B cannot offer — a corrupted
  primary table has nothing to be rebuilt from.
- **`commit` hands the decision function a live `*sql.Tx`**, so a verb could
  write through it directly. Discipline, not structure. Closing it means
  passing a read-only querier and giving up transactional guards; I did not,
  and it is the one place the "structurally impossible" claim weakens to
  "nobody would".
- **"Exactly one" is really "exactly one per node per transition", and that
  part lives in the guards, not the envelope.** `commit` enforces *at least
  one* (`errNoEvent` refuses a verb that decided nothing happened); it is
  `Start`'s `if node.Lifecycle != scenario.Planned { return error }` that
  stops a second `started`. Invariant 1 is not self-enforcing in either shape;
  what this shape gives you is that the enforcement has exactly one place to
  live.

---

## 2. Axis 2 — invariant 2: what did "what is in progress" cost?

### The choice: a maintained projection, written in the same transaction as the append. Not replay-on-read.

Three reasons, in decreasing order of how much they constrained the decision:

1. **MODEL §10 invariant 2 names replay as the disqualifying answer** — "a
   SELECT or a maintained projection, never ad-hoc replay". A replay-on-read
   spike would not have been the event-sourced shape passing the test; it
   would have been the event-sourced shape failing it.
2. **The write path needs current state anyway, so replay is not a read-side
   decision.** Every guard in every verb is a read: `AddStep` must know the
   parent exists and is a Matter, and must count siblings to assign `step-NN`;
   `Start` must know the node is Planned and must walk its ancestors; `Finish`
   must know the node is In Progress. Under replay-on-read those become
   whole-log folds on the *write* path, so every mutation becomes O(events).
   The choice is not "how do reads work", it is "how does anything work".
3. **Same transaction, not asynchronous.** An eventually-consistent projection
   breaks two things the conformance harness checks immediately after each
   verb: that a refused command left nothing behind, and that the answer right
   after a write is already correct.

**I wanted both, and built both — but not as two read strategies.** Replay
lives as `Store.Rebuild`: fold the whole log over an empty `nodes` table. It
is a recovery and verification *operation*, not a query path.
`TestRebuildEqualsMaintainedProjection` labels two nodes, snapshots every
column of every projection row, rebuilds, and asserts `reflect.DeepEqual` —
and separately asserts `Rebuild` did not touch the log. Because the live path
and the rebuild path call the **same** `applyEvent`, "the projection holds no
fact the log does not" is a property of the code, not a claim in a comment.

### The query, verbatim

```sql
SELECT id, kind, COALESCE(parent, ''), locator, title, lifecycle
FROM   nodes
WHERE  lifecycle = 'in-progress'
ORDER  BY birth_event
```

Backed by `CREATE INDEX nodes_lifecycle ON nodes(lifecycle, birth_event)`.
`EXPLAIN QUERY PLAN` gives, at both v1 and v2:

```
SEARCH nodes USING INDEX nodes_lifecycle (lifecycle=?)
```

— an index seek, and **no `USE TEMP B-TREE FOR ORDER BY`**, because
`birth_event` is the second index column. Creation order is free: `birth_event`
is the ULID of the `*.created` event that produced the row, so D51's
"sibling order is a sort key, presentation-only" needs no sort key of its own
in this scope.

### The cost, concretely

- **84 code lines** (`project.go`) is the entire price of invariant 2: the
  projection rules, the row-count sanity check, and `Rebuild`.
- **Two indexes** (`nodes_lifecycle`, `nodes_parent`).
- **One extra write statement per event.** Statements inside the transaction,
  per mutation:

  | verb | reads | writes | total |
  |---|---:|---:|---:|
  | `CreateMatter` | 0 | 2 (`INSERT events`, `INSERT nodes`) | 2 |
  | `AddStep` | 2 (parent lookup, sibling count) | 2 | 4 |
  | `Start`, no cascade | 1 | 2 | 3 |
  | `Start`, cascade (D57) | 2 | 4 | 6 |
  | `Finish` | 1 | 2 | 3 |
  | `Label` (v2) | 1 | 2 | 3 |

- **One obligation with no database-level enforcement:** `applyEvent` must
  remain the only writer of `nodes`. See axis 1's second bullet.
- `applyEvent`'s `exactlyOneRow` check turns a divergence between log and
  projection into an error at write time rather than a wrong answer at read
  time. Cheap; would not have occurred to me if the two tables were one.

---

## 3. Axis 3 raw material — what the v2 migration actually took

The change: nodes gain an optional `label`, set after birth by a new verb,
emitting `matter.labeled` / `step.labeled`, surfaced in the in-progress answer,
applied over a database already populated under v1.

**Production files touched: 3. Production code lines added: 90.**

| file | code lines | what |
|---|---:|---|
| `label.go` (new) | 81 | 2 event-type constants, payload struct, `Store.Label`, `labeledType`, `setLabel` (the projection rule), `LabeledNode`, `ErrNeedsV2`, `inProgressLabeledSQL`, `InProgressLabeled` |
| `migrations.go` | **7** | one `migration{version: 2, ...}` entry whose entire body is `ALTER TABLE nodes ADD COLUMN label TEXT NOT NULL DEFAULT ''` |
| `project.go` | **2** | `case TypeMatterLabeled, TypeStepLabeled: return setLabel(ctx, tx, ev)` |

Test cost: `migration_test.go` (129 code lines, new) plus ~6 lines in
`store_test.go` to make `snapshot` version-aware.

The 81 lines in `label.go` are the *feature* — a new verb and a new read path,
which any shape pays in some form. The part attributable to **the shape** is
**9 lines**: one `ALTER` and two switch cases.

### What had to be true about existing data

**Nothing.** That is the whole finding, and it is worth stating precisely
rather than triumphantly:

- **Zero events were rewritten or backfilled.** The migration does not touch
  `events` at all — the append-only triggers would have refused it, and there
  was no reason to try. Verified: `TestV2MigrationOverV1Database` captures the
  full log before closing the v1 handle and asserts `reflect.DeepEqual` against
  the log after migration.
- **Zero rows needed a value.** `NOT NULL DEFAULT ''` means rows born before
  the event type existed answer with the empty string. The test asserts the
  pre-migration in-progress answer is byte-identical under v2, and that
  pre-migration nodes come back with an empty (not null, not missing) label.
- **The new verb works on old nodes.** The test labels a Matter and a Step that
  both predate the event type, and checks the two new events carry the correct
  tier dimensions — which they do without a line of new code, because
  `scenario.RequiredDimensions`' default arm covers new types and `stamp` is
  the only place dimensions are decided.
- **Backup-before-migrate (PLAN 1.2) fired.** `migrate` copies the file to
  `<path>.v1.bak` before applying, but only when `current > 0` — a database
  being born has nothing to lose. Both are asserted.
- **Downgrades are refused**, asserted separately.

### Two honest qualifications

- **This migration was the easy kind on purpose, and the shape's advantage is
  bigger on the hard kind.** Because `nodes` is *derived*, an equally valid v2
  would have been "drop the projection, recreate it with the new column, call
  `Rebuild`" — no `ALTER`, no default, no reasoning about existing rows at all.
  That option is always available here and is never available for a primary
  table. The test proves the equivalent: after migrating and labeling, a full
  `Rebuild` still reproduces the maintained projection exactly. **A change that
  altered the *derivation* rather than adding a field — say, "in progress
  should exclude Matters whose every Step is Done" — is a projection rebuild
  in this shape and a data migration in the other one.**
- **`backup()` is spike-grade.** It checkpoints the WAL and copies the file. A
  real one should use `VACUUM INTO` or SQLite's backup API; the copy is only
  safe here because it runs at open time before any writer exists.

---

## 4. Axis 4 raw material — the §10 fidelity list, item by item

| fidelity item | free | still owed |
|---|---|---|
| **append-only** | Total. Two triggers, and the shape has no reason to ever update a row. Demonstrated by test. | Nothing *in scope*. Beyond it: corrections. D44's tombstones, amendment and remove must all be new events, and every read path must know to fold them — the projection makes that easy, but "you can never fix a bad event, only compensate" is a permanent tax (see the locator note below). |
| **identity-not-locator references** | Total, and structurally so. `subject` is the only reference field in the envelope, and at `stamp` time a ULID is the only thing available to put there. `step-NN` lives in the birth payload as a fact about the write (MODEL §10 explicitly permits this) and in the projection as a mutable column. Renaming a locator later is a future event that touches only the projection; every old event stays correct. | Nothing. This is the cleanest single result of the spike. |
| **tier dimensions** | Free at write time: one static call in `stamp`, applied to every event including v2's new types without a line of new code. | The **check**. D56 says "checked in schema"; here nothing in SQLite would notice a `clone` on a durable-write event. Owed: per-type `CHECK` constraints or an insert trigger. Also owed: the Batch-subject null-repo case, which this scope excluded. |
| **system-driven events first-class** | Free structurally — `commit` knows nothing about who called the verb, so a CI webhook and a human typing `wip start` produce identical writes. | **A gap in MODEL §10's envelope itself, not in the shape: there is no `actor`/`source` field.** If "a Builder closing and CI going red are events", the envelope must be able to say *who*, and today it cannot. `schema` should decide this. Neither shape helps. |
| **enough to reconstruct in-flight work** | Total, and this is where the shape is unambiguously ahead. `Rebuild` is ~30 lines and reproduces every column of the projection from the log alone, proved by test at both schema versions. The log also carries `from`/`to` on every transition, so the log narrates rather than merely replaying. | Nothing in scope. At scale, `Rebuild` is one transaction over the whole log — needs a shadow-table-and-swap and a **projection version** distinct from the schema version, so the store knows when a rebuild is required. |
| **narrate a sealed Matter after the fact** (export-on-seal precondition) | Most of it. The log is the narration; nothing was thrown away; `from`/`to` cost nothing and turn a replay log into a readable one. | Three concrete debts. (a) **Causation.** The D57 cascade writes `matter.started` and `step.started` with *nothing linking them* — the log cannot say they were one `wip start`. A `command_id`/causation field belongs in the envelope and is not there. (b) **Actor**, as above. (c) **Payload stability.** Payloads are Go structs marshalled ad hoc; an exporter written years later must read a v1 payload. That third debt is **not smaller in this shape than in the other one** — it is the real cost of export-on-seal and neither shape pays it for you. |

---

## 5. Things the shape made obvious that the rubric did not ask about

1. **The envelope wants to be *stamped*, not *constructed*.** Splitting the
   verb's decision (`draft`: type, subject, payload) from the envelope
   (`stamp`: id, timestamp, dimensions) is the single most transferable finding
   here, and **it is not specific to this fork** — Spike B should want the same
   split for its audit rows. If `schema` ratifies only one implementation rule
   from this Matter, make it this one: no verb ever names a tier dimension.
2. **MODEL §10's envelope is missing two fields**, and the spike surfaced both
   as soon as D57 and "system-driven events first-class" were taken seriously:
   causation/correlation, and actor. Neither is a fork question. Both should be
   decided in `schema` before the first event ships, because both are on the
   "cannot be retrofitted" list in spirit even though §10 does not name them.
3. **Reopening a store needs a high-water mark.** Because an event's identity
   *is* the total order (D44, D51), a fresh ULID source in a new process can
   mint an id below the last one on disk if the reopen lands in the same
   millisecond. `Store.loadFloor` + `Store.nextID` (~25 lines) fix it by reading
   `MAX(id)` from both tables at open. This cost lands harder on this shape
   than on a shape that can lean on a rowid, and it is easy to not notice until
   it produces an unreproducible ordering bug.
4. **A derived fact in an append-only log is permanent.** `step-NN` is computed
   at write time (a `COUNT` over siblings) and baked into the payload. That is
   sanctioned, and it makes rebuild trivial — but it also means a bug that once
   assigned `step-03` twice is reproduced faithfully by every future rebuild.
   Append-only cuts both ways and the locator is where it bites first.
5. **`WITHOUT ROWID` fits both tables** (TEXT ULID primary keys), which is a
   small real win for the log.

## 6. The two weaknesses I would lead with if I were arguing against this shape

1. **The derived table is on the write path, not just the read path.** Every
   guard — `Start` refusing an already-started node, `AddStep` refusing an
   unknown Matter, the sibling count that assigns `step-NN` — reads `nodes`,
   the *derived* table. So "events are the source of truth" is true for
   durability and false for decisions: if the projection were stale, the store
   would refuse and permit the wrong things and then faithfully write the wrong
   events into a log that can never be corrected. Spike B makes that same read
   against the authoritative table, so it has one fewer thing that can be
   stale. This is the shape's largest honest cost and it is completely
   invisible until you write the guards — I did not anticipate it.
2. **Two writes per event, with the agreement between them enforced by nothing
   but code review.** The log has triggers; the projection cannot have them
   (rebuild needs to delete). `applyEvent` being the sole writer is a property
   of *this* package on *this* day. The mirror-image risk in Spike B is real
   and worth naming for symmetry — its audit row can be forgotten while its
   primary write succeeds — but the failure modes are not equally bad in the
   same way: here you get a wrong *answer* over a correct log (recoverable by
   `Rebuild`); there you get a correct answer over an incomplete *history*
   (not recoverable at all). Which of those you fear more is arguably the whole
   decision.

## 7. Deliberate omissions

Gates, Session, tier resolution, `blocked-by`, Stage, Cancel, Pause, Batch,
dispatch, render, cursor, prose, and the CLI, per the pinned scope. Concurrency
beyond `SetMaxOpenConns(1)`. Event-type registry, payload versioning, snapshot
support, and `Rebuild` at scale. `backup()` is a checkpoint-and-copy, not
`VACUUM INTO`.
