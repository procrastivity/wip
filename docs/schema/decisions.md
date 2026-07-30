# `schema` — decisions this Matter made

The design of record is the `schema` Brief (`workplans/schema.md`, external). This
file records what building it *forced* — the calls the Brief left open, the
findings `store-fork` handed over, and the places the implementation diverged
from the Brief text. Step-11 folds the divergences back into the Brief; this is
the working record it reads.

Status: **in progress.** Steps 01–08 are complete and tested; steps 09–11 remain.
See "Where this stands" at the bottom.

## D46's outcome, consumed (step-01)

`store-fork`'s decision record names the event-sourced shape. Its drafted
D-register entry was appended to `MODEL.md` §12 verbatim as **D61** — the slot
the record itself predicted, D54 being MODEL's last and the lifecycle
resolutions claiming D55–D60.

Consequences taken as settled, not re-opened:

- The `events` table is the source of truth; every other durable table is a
  projection maintained in the same transaction and rebuildable from the log.
- The winning spike, `internal/spike/eventsourced`, is this Matter's literal
  starting point. `internal/store` descends from it: the same `stamp → append →
  apply` write path, the same monotonic-identity-as-total-order rule, the same
  high-water mark at open, the same numbered-migration register.
- **The spike stays where it is.** D61 names its path, so moving it would
  falsify a just-appended MODEL entry. It is evidence, not live code, and
  nothing in `internal/store` imports it.

## Findings `store-fork` carried forward, and what was done with each

1. **`actor`, `causation`, `correlation` in the envelope.** Already resolved and
   implemented before this Matter opened; inherited unchanged, in the v1
   baseline rather than as a migration.
2. **The taxonomy as a table.** *Adopted* — `event_types` is a real table,
   `events.type` is a foreign key into it, and each type's required tier
   dimensions are a row. **This diverges from the Brief**, which says `type` is
   "an open token column ... without the store enforcing an enum that later
   phases would have to migrate." D56 says required dimensions are "checked in
   schema", and that check is not expressible without the type's rule being in
   the schema — so the MODEL-level decision wins over the Brief's prudential
   note. The cost the Brief was avoiding is one `INSERT` inside a migration a
   later phase is already shipping. `events-traces` still audits the *produced*
   streams for fidelity, which membership enforcement does not cover.
3. **Column-level constraints on the log.** Adopted from the losing spike and
   extended: identity lengths **and Crockford alphabet**, the actor's
   prefixed-token form, a fixed-width RFC3339 `occurred_at`,
   `json_type(payload) = 'object'`, plus triggers for append-only,
   strictly-ascending ids, never-points-forward causation, and the D56 dimension
   rule.

   The alphabet check was added while writing step-02's tests. Length alone is
   not identity-hood: `occurred_at` is *derived* from the id rather than read
   from a second clock (finding 5 below), so a 26-character id outside
   Crockford's alphabet would be an event no reader could date. It went into the
   v1 baseline rather than a v2 migration because v1 had not shipped —
   `schema_v1.go`'s "nothing in here may be edited once shipped" is the actual
   rule, and nothing had.

   Two things about the log are still *not* enforced by the substrate, stated
   rather than implied. Nothing ties `occurred_at` to its own id — a base32
   decode is not expressible in a CHECK, so it is the one envelope invariant
   resting on Go discipline while its neighbours all rest on the substrate; a
   test covers it. And `correlation` is not required to name a chain *origin*:
   the foreign key plus the never-forward trigger admit a correlation naming a
   mid-chain event.
4. **A high-water mark at open.** Kept, and simplified to one query:
   `MAX(events.id)` dominates every identity in the store, because an entity's
   ULID is minted inside the command that births it and no command commits
   without appending an event.
5. **Two clocks.** *Closed with a rule rather than a column.* `occurred_at` is
   **derived from the event's own ULID**, so the two clocks are one clock by
   construction and there is nothing to reconcile — no twelfth envelope column,
   no `recorded_at`. A foreign timestamp (an imported artifact's own date)
   belongs in a payload and may never be written to `occurred_at`.
6. **A projection version, distinct from the schema version.** Adopted:
   `store_meta.projection_version`, checked at open, with a rebuild when this
   binary folds a newer derivation than the store was built under. On the
   shadow-table-and-swap: *not* adopted, with a reason. One transaction over the
   whole log is already atomic — a failure rolls back and leaves the live
   projection intact — so what the swap buys is a shorter write lock, which a
   single-user single-host store with one connection (D34, D43) does not need.
7. **Keep both correctness checks.** Kept. Every projection write asserts its
   row count, and every lifecycle transition updates `WHERE lifecycle = <from>`,
   so an event describing a transition that could not have happened fails at
   write time instead of being appended to a log that then disagrees with the
   projection.

## The flagged call: actor within a D57 cascade

`store-fork` implemented but explicitly did not ratify this, and asked `schema`
to re-examine it with Stage in scope. `write-surface` defers to whatever this
Brief settles.

**The spike's posture:** the node the caller named carries the caller's actor;
ancestors wip auto-started carry `system:wip`. With Stage in scope, `wip start
step-01` on a Planned Matter writes `matter.started` (`system:wip`, and the chain
origin), `stage.started` (`system:wip`), `step.started` (`human`) — so the log
narrates the system acting first and the human following, two events out of three
attributed to wip.

**Resolved the other way, with the mechanism moved into the payload:**

- **`actor` is the requesting actor for every event in the command's chain.** A
  command is a request (D57), and the actor belongs to the request. wip
  auto-starting an ancestor is acting within the authority the command granted
  it, and an act within authority is attributed to the principal.
- **`payload.cascade: true` marks a node wip started on its own initiative.**
  The fact the alternative was protecting — the human never named this Matter —
  is recorded on the event itself rather than inferred from an actor token.

Why this way: `actor` is the attribution column, and Session reads it. Under the
spike's posture every "what did I do" view has to special-case `system:wip`, and
a Builder starting a Step three levels down gets two-thirds of its work credited
to wip. The narration reads correctly top-down, no causation pointer points
forward, and nothing is lost.

`system:<source>` therefore has **no P1 emitter**. That is a fact about P1's role
set, not a gap: MODEL §10 requires the envelope to be *able* to say a system
acted, and the first real user is a CI webhook or a watcher in a later phase.

**This needs an amendment `schema` is not sanctioned to make:**
`workplans/write-surface.md` states the spike's posture as its own in step-01 of
`birth-and-amendment` ("`system:wip` for events wip emits on its own
initiative — in this Matter, exactly the ancestors `start` auto-starts") and
again in step-03. Its step-03 already says to follow `schema`'s Brief §A rather
than re-deciding, so it is self-deferring rather than invalidated — but the two
sentences are now wrong and should be amended.

## Storage calls the Brief left to the implementation

- **One `nodes` table for Matter, Stage and Step**, discriminated by `kind`.
  The Brief presents them as three rows in an entity table; one table is what
  `blocked-by` (any node → any node) and gate state (keyed by node) require,
  since both need a single identity space. Stage's entity-hood is not weakened:
  it is a row exactly like Step, with its own ULID, sort key, gate state and
  edges.
- **`nodes.matter`** is a derived column — a node's owning Matter, and its own
  identity when it is a Matter. It is the addressing scope: `step-NN` is
  globally sequential within a Matter and a Stage grouping Steps neither renames
  nor renumbers them (D16), so locator uniqueness is scoped to the Matter and
  not to the parent.
- **Archive is a view, not a table.** The Brief's entity table says "sealing
  moves a Matter into archived state," but D55 makes *sealed* a predicate over
  lifecycle and gates rather than a state or a frame, and there is no `*.sealed`
  event to project. `archived_matters` computes it: Done, with every
  matter-scale gate its Repo declares closed against it. Finish and gate-close
  stay order-independent, which is what a predicate expresses and a state would
  not.
- **Gate declarations and project config are the one non-projection exception.**
  Declaring a gate is configuration (D4, D54) and emits no event, so there is
  nothing to fold and `Rebuild` must leave those two tables alone. The seam:
  config says how the project is set up, the log says what happened, a rebuild
  reconstructs only the latter.
- **Content rows are segments.** Create-once kinds (brief, workplan, body) are
  constrained to exactly one live segment by a partial unique index; findings
  accumulate one segment per `content.appended`, and a read concatenates them in
  identity order. Segments rather than a rewritten row is what makes an append
  an insert, keeps rebuild trivial, and avoids a read-modify-write on prose.

  Two consequences step-04 forced. `ContentWritten.Bytes` carries no
  `omitempty`: under it an empty byte slice and an absent one encode
  identically, so a zero-length content object came back off the wire looking
  like a *spilled* payload with no reference. The schema permits zero length
  (`byte_len >= 0`), so the payload has to be able to say it. And
  `insertContent` verifies in-store bytes against the payload's own `byte_len`
  and `sha256` — a row the store cannot read back is a row it should not have
  written, and checking at projection time leaves the read-side verification
  saying something about the *sidecar file*, the one fact the log does not carry.

  Note for whoever builds a verb that rewrites a Brief: **no P1 event tombstones
  content.** The `tombstone_event IS NULL` half of `content_create_once` is
  correct but unreachable through any command, which is consistent — a
  create-once kind genuinely cannot be rewritten in P1 — but it is a fact about
  P1's event set and not a property of the index.
- **The spill threshold is 1 MiB**, documented in `content.go`: comfortably
  above any Brief, Workplan or findings list, comfortably below the ingested-log
  case PLAN 1.2 names. Spilled bytes are the one projection fact not recoverable
  from the log alone, so the payload carries a length and a SHA-256 and a read
  verifies both — a missing sidecar file is reported, never silently short. A
  rolled-back command can leave an unreferenced blob, which is why reaping them
  belongs with `clean`'s crash orphans.
- **`sort_key` is a dense multiple of 1000**, with a midpoint helper for
  insertion and a whole-sibling-set `step.reordered` when a gap runs out.
  Magnitude is meaningless (D51).
- **Subjects, where the Brief left them open:** content events take the owning
  node (payload carries the kind and the segment id); dependency events take the
  *blocked* node (payload carries the edge and the blocker); `step.reordered`
  takes the parent whose children moved; `step.replaced` takes the Step being
  replaced; `cursor.moved` takes the Worktree the cursor belongs to.
- **Amendment registers `step.*` only.** The Brief's "stage equivalents where
  earned" stays unearned — but **not** because the rules are kind-agnostic, which
  is what this file claimed before step-03 tested them. It is true of
  `step.removed` (`tombstoneNode` never asks the scale) and of `step.inserted`
  (`insertNode` takes one). It is false of `step.replaced`, which *births* a row
  and now refuses a non-Step subject, and false of `step.reordered`, which
  filters `kind = 'step'`. A `stage.replaced` would need Go work and, more to the
  point, a decision about what becomes of what the Stage grouped — and no P1
  event says that. So the equivalents are not four `INSERT`s in a migration.

- **An edge is in force only while both its ends are** — a new
  `edges_in_force` view, and the second view in the schema after
  `archived_matters`, for the same reason: it is a predicate over rows and not a
  state anything writes. `BlockedBy`, `LiveEdges` and the cycle check's
  `liveEdgeGraph` all read it, so "what blocks this" and "what loops does this
  store have" cannot answer differently.

  The Brief does not decide this and step-06 surfaced it: a live edge could name
  a tombstoned node, so `wip status` would report a blocker that can never
  complete and `doctor` would report loops no repair can break. An edge is a
  relation between two nodes and a relation to a removed node is not a relation;
  removing the blocker is precisely how D44's amendment clears an obstruction, so
  the obstruction going with it is the rule rather than an exception to it. The
  edge *row* is untouched and stays tombstone-free — out of force is not removed,
  and its own `dependency.removed` would still be a legitimate event.
- **The projection is total over the P1 taxonomy.** `schema` owns a rule for
  every registered type, so an emitter in a later Matter needs no projection
  work of its own; `render.performed` is the one member that deliberately
  projects nothing, because a render is a projection of the store onto disk that
  the store never reads back (D36, D40).

## Where the implementation goes beyond the spike

- **`Tx` exposes no way to write.** The spike handed its verbs a live
  transaction handle and called "no verb writes directly" discipline rather than
  structure; here a decide function gets reads and `NewID()` and nothing else.
- **Projection guard triggers.** The spike's own report named its largest hole:
  the log had triggers and the projection could not, because `Rebuild` must be
  able to clear it. Three generated guards close most of it — a row is born
  naming exactly one event, a row may only ever advance to a strictly newer
  event, and a row may only be deleted inside a rebuild (a `store_meta`
  sentinel set for the duration of `Rebuild`'s transaction). What they do *not*
  close, stated rather than implied: nothing checks that the event a row names is
  an event *about* that row, because amendment is legitimately not
  one-event-one-row — `step.reordered` names the parent and moves its children.
- **The tier context is per-command, not per-store.** `Commit` takes a `Request`
  carrying the actor and the `Env` the command ran in, rather than the store
  holding a fixed `Env`. This is what lets `wip init` create the Repo its own
  event's `repo` dimension names, with no special case in `stamp`.

## Bugs the tests found, and what they were

Recorded because each one is a fact about the shape rather than a typo, and
because "the implementation was written before any of it ran" is the context
step-11 should read them in. Every fix was verified by mutation: the guard is
removed, the owning test fails, the guard is restored.

- **`applyReplace` never read its subject's `kind`** and hardcoded `'step'` in
  its INSERT, so `step.replaced` against a Stage succeeded — tombstoning the
  Stage, birthing a Step in its place, and leaving every Step it grouped
  parented to a tombstone.
- **`matterOf` did not filter on liveness**, so a node could be born under a
  *tombstoned* parent: a row `Children` never reaches but `MatterNodes` still
  lists.
- **`applyTransition` did not filter on liveness** either, so a `*.canceled`
  against a removed node wrote a state change no reader can reach — making the
  Brief's "removed while still Planned" a fact that does not stay put.
- **`tombstoneEdge` tombstoned whatever row the payload's edge id named**,
  checking neither the subject nor the blocker, so one node's
  `dependency.removed` could quietly retire another node's edge and the log would
  read as though it had been theirs.
- **`closeGate` never read its subject's `kind`** and took the payload's scale as
  given, so a `gate.closed` could record `reviewed-local` at *Step* scale against
  a Matter. `archived_matters` asks only whether a declared gate has a row against
  the Matter, so the mis-scaled close sealed it; and because gate state is one row
  per `(node, gate)`, that same row took the slot the real close needed — the
  Matter could then never be sealed by any later event. A gate binds to a scale
  (D12) and closes against a node *at* that scale, so the rule now reads the
  subject's kind and refuses a scale that is not it. It is `applyReplace`'s miss
  again: a scale trusted from the payload when the row could have been asked.
- **`Cycles` reported one loop per tangle, not every loop.** It was one
  depth-first sweep recording back edges; every loop contains a back edge, but
  two loops can share one, and only the loop on the current path was reported,
  after which the `done` set pruned the rest. `doctor` would have reported one
  loop out of two, the user repairs it, the next run reports the next. Each loop
  is now enumerated from its own smallest node over a walk that never steps below
  it — a loop has exactly one smallest node, so it is found exactly once and
  arrives already canonically rotated, which retired `cycleFrom` and `cycleKey`.

  **The five cases the workplan names all pass against the broken version.** That
  is the most useful thing this Matter learned about its own seal condition: the
  five are necessary and nowhere near sufficient. What caught it was a
  multi-loop case and an independent oracle (120 fixed-seed random graphs against
  a naive enumerator, plus a transitive closure computed by relaxation rather than
  by a walk) — it fails ~13 of 120 trials against the old code and none against
  the new.

## Known gaps, deliberately not closed here

Each of these is real, verified, and belongs to somebody else — recorded so the
next Matter finds them rather than rediscovering them.

- **Two Matters of one Repo may share a slug.** `nodes_locator` is
  `(matter, locator)` and a Matter is its own matter, so the constraint on a
  Matter's *own* locator is vacuous. Needs a new index and a decision about the
  scope → the addressing Matter.
- **`backlog.planned` and `batch.joined` accept any node where a Matter is
  meant** (`REFERENCES nodes(id)` with no kind check). Left to the verb layer,
  consistent with `guards` owning the cycle and gate-order preconditions.
- **`clone.attached` never checks `payload.repo == event.repo`**, though
  `insertRepo` checks the analogous thing for itself.
- **A tombstone can be undone by raw SQL.** `UPDATE edges SET tombstone_event =
  NULL, last_event = <newer>` succeeds; the `advance` trigger only catches the
  careless form. "A tombstone is final" wants a trigger, which is a migration.
- **`content.created` / `cursor.moved` / `dependency.added` / `gate.closed`
  accept a tombstoned node as their target.** D44 guarantees *prior* references
  stay valid; new ones pointing at a corpse look like `doctor`'s business.
  (`dependency.added` is mitigated: `edges_in_force` keeps such an edge out of
  every read. `gate.closed` is mitigated too: `archived_matters` excludes
  tombstoned Matters before it ever looks at a gate.)
- **A `gate.closed` may run in another Repo's tier context.** Nothing compares
  the event's `repo` dimension with the Repo of the node it closes a gate
  against, so a command running in Repo B closes a gate on a Matter of Repo A
  and seals it under A's declarations. Same family as `clone.attached` never
  checking `payload.repo == event.repo`, and the same answer: the verb layer
  resolves the tier context, and a mismatch is a caller that lied about where it
  was.
- **`InProgress` answers store-wide, with no Repo dimension.** Invariant 2's
  query is one seek into `nodes_lifecycle`, which is keyed
  `(lifecycle, birth_event)` and carries no Repo — so "what is in progress"
  spans every Repo in the store. The founding question is asked from somewhere,
  and `read-surface` composes the output; whether the filter belongs in the
  query (and therefore in the index) is its call, not this Matter's.
- **`backlog.declined` overwrites `detail` with the decline reason**, destroying a
  `deferred` entry's rationale in the projection. The log keeps both, and there is
  no second free-text column.
- **`SegmentBytes` and `Content` are on `*Store`** and read `s.db` directly while
  `ContentSegments` is on `View`, so a decide function can list segments inside
  its own transaction but not read their bytes. Nothing in P1 needs it; a verb
  appending to findings after reading them will hit it.
- **A nested `Commit` inside a decide function deadlocks** rather than erroring
  (`SetMaxOpenConns(1)`). A guard on `Tx` would be cheap.

## Where this stands

Built and passing: the v1 baseline (all tables, indexes, triggers, both views),
the taxonomy as data, the write path (`Commit`/`stamp`/`appendEvent`), the
projection rules for the whole P1 taxonomy, `Rebuild`, the migration framework
with backup-before-migrate, the read surface, the static cycle check, content
storage with spill, and Repo-tier config.

Tested: **steps 02–08.** The envelope, every entity table and its projection
rule, `Rebuild`'s column-for-column fidelity, content and spill, edges and
tombstones, the static cycle check — which discharges half the seal condition —
gate storage at all three scales, and invariant 2.

Two notes on step-08, because the claim it makes is easy to weaken later.
`TestInProgressIsAnIndexSeek` asserts SQLite's plan for the `inProgressSQL`
constant itself and not for a copy of it, so the test cannot pass while the
query the store runs drifts into a scan. And the projection's own correctness —
which is all invariant 2 rests on, since the answer is never recomputed — is
held by an oracle rather than by the named cases: the whole log folded in Go,
over one fixed pseudorandom history of creations, all five moves at all three
scales, mid-history births, removals and replacements across two Repos, compared
to the table every eighth move and again after a `Rebuild`.

Not done: **steps 09–10's tests** (migrations, round-trip) and **step-11's
reconciliation** of the Brief.
