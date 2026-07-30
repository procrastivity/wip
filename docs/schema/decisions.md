# `schema` — decisions this Matter made

The design of record is the `schema` Brief (`workplans/schema.md`, external). This
file records what building it *forced* — the calls the Brief left open, the
findings `store-fork` handed over, and the places the implementation diverged
from the Brief text. Step-11 folds the divergences back into the Brief; this is
the working record it reads.

Status: **in progress.** Steps 01–02 are built and the v1 baseline applies;
steps 03–11 are not done yet. See "Where this stands" at the bottom.

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
   extended: identity lengths, the actor's prefixed-token form, a fixed-width
   RFC3339 `occurred_at`, `json_type(payload) = 'object'`, plus triggers for
   append-only, strictly-ascending ids, never-points-forward causation, and the
   D56 dimension rule.
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
  earned" stays unearned: the projection rules are already kind-agnostic, so the
  equivalents would be four `INSERT`s in a migration and no Go change.
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

## Where this stands

Built and passing: the v1 baseline (all tables, indexes, triggers, the archive
view), the taxonomy as data, the write path (`Commit`/`stamp`/`appendEvent`),
the projection rules for the whole P1 taxonomy, `Rebuild`, the migration
framework with backup-before-migrate, the read surface, the static cycle check,
content storage with spill, and Repo-tier config.

Not done: the test bodies for steps 02–10 (only a baseline open test exists),
and step-11's reconciliation of the Brief.
