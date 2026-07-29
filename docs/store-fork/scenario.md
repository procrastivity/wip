# store-fork — the pinned shared scenario (step-01)

This is the contract both spikes implement, fixed before either started, so
the comparison in step-04 is between two *shapes* and not between two readings
of the task. MODEL D46 is the fork; MODEL §10 is what it is held open behind.

Nothing in this directory or under `internal/spike/` is production code.

## The scope, exactly

Three operations, and nothing more:

1. **Create a Matter.**
2. **Add a Step under it.**
3. **Query "what is in progress"** — the founding question (MODEL §1).

Two operations are in scope that the workplan's scope line does not name, and
both are there for the same reason: without them (3) has no discriminating
answer.

- **`Start`** — MODEL §2.2 is explicit that a merely-planned Matter never
  reports In Progress, so with no start verb the query either returns
  everything ever born or returns nothing. `Start` also carries D57's
  auto-start of Planned ancestors, which is the one place in this scope where
  a single command invokes several verbs — the sharpest available test of
  invariant 1's "exactly one event per verb."
- **`Finish`** — the query must be shown *excluding* Done, not merely
  including everything started.

Explicitly **out** of scope, per the workplan: gates, Session, tiers
*resolution*, `blocked-by` edges, Stage, Cancel, Pause, Batch, dispatch,
render, cursor, prose/content, the CLI. Neither spike wires a Cobra command;
both are libraries exercised by tests.

Tier *dimensions* are **in** scope even though tier resolution is out. MODEL
§10 lists them as fidelity that binds from event one and cannot be
retrofitted, so a spike that omits them would be answering an easier question
than the one the fork asks. A store is opened against a fixed `Env` of three
synthetic ULIDs and stamps them per the static per-type rule (D56); no spike
does any git probing or URL normalisation.

## The event envelope

Taken from MODEL §10 directly, not from any downstream workplan — `schema`
ratifies the envelope later and is free to differ; this is what the spikes
were compared against.

| Field | Rule |
|---|---|
| `id` | the event's own ULID, monotonic per store — doubles as the total-order key, so there is no separate sequence column (D44, D51) |
| `type` | one of the six tokens below |
| `occurred_at` | UTC timestamp |
| `repo` | Repo ULID — required on every type in this scope |
| `clone` | Clone ULID — execution events only; absent on every type in this scope |
| `worktree` | Worktree ULID — execution events only; absent on every type in this scope |
| `subject` | ULID of the entity the event is about — **identity, never a locator** |
| `payload` | JSON object of type-specific fields |

Six event types: `matter.created`, `step.created`, `matter.started`,
`step.started`, `matter.finished`, `step.finished`.

### Amendment — three fields added after the decision

*This section is a post-decision amendment. The eight-column envelope above is
what both spikes were built against and what `rubric.md` compared; it is left
standing rather than rewritten, so the comparison stays legible. The winning
spike and the harness now carry three more fields, ratified by the user after
both spikes independently reported the envelope unable to say who acted or
why.*

| Field | Rule |
|---|---|
| `actor` | who performed the verb — never empty. One column, prefixed token: `human`, `role:<name>`, `system:<source>` |
| `causation` | ULID of the event that entailed this one; **its own ULID** if it began the chain |
| `correlation` | ULID of the chain's origin event; **its own ULID** if it began the chain |

Origins self-reference rather than carrying null, so "this began its own chain"
and "nobody filled this in" are never the same value. Writing them as
`id:causation:correlation`, a chain where a causes b, b causes c, and c causes
both d and e reads:

```
a:a:a   b:a:a   c:b:a   d:c:a   e:c:a
```

Two rules follow, and the conformance body enforces both:

- **A cause never points forward.** Causation and correlation always name an
  event already in the log. This is why, in D57's cascade, the *first-emitted*
  event is the origin.
- **Same-command siblings point at the origin; a chain points at its
  predecessor.** A linear cascade (Matter → Stage → Step, once Stage exists)
  gives `a:a:a, b:a:a, c:b:a`; unrelated events from one command give
  `a:a:a, b:a:a, c:a:a`.

**Actor within a cascade.** The node the caller actually named carries the
caller's actor; ancestors that D57 auto-started carry `system:wip`, because wip
started them on its own initiative and the caller never asked. So
`wip start step-01` on a Planned Matter writes `matter.started` as
`system:wip` (the origin) and `step.started` as `human` (entailed by it).

*One consequence, flagged for `schema` rather than smoothed over:* this makes
the human's own action the **derived** event and wip's automatic one the
origin. Each fact is individually true — wip did start the Matter, and the
Matter's start is what made the Step's start legal — but a narrator reading
the chain top-down sees the system act first and the human follow. The
alternative (attributing the whole command to the caller) reads better and
claims the human started a Matter they never mentioned. Neither is free;
`schema` should re-examine it with Stage in scope, where cascades get deeper.

`step-NN` is a locator and is stored as a mutable attribute of the node. It
may appear inside a payload as a fact about the write; it may never be what an
event references.

## The harness

`internal/spike/scenario` — shared, shape-neutral, written before either spike:

- `scenario.go` — the `Store` interface (the five verbs plus `Events`), the
  `Node` and `Event` types, the taxonomy, `RequiredDimensions` (D56's static
  function of event type).
- `ulid.go` — one monotonic ULID source, shared, so neither spike can win or
  lose on its identity generator.
- `conformance.go` — `RunConformance`, the single test body both spikes are
  driven through. It scripts the scenario, then holds the result to:
  invariant 1 (exact event sequence per command, including the two-event
  ancestor cascade; refused commands write nothing), invariant 2 (the query
  answers by identity, in creation order, with locator and title populated),
  append-only (no prior event is ever rewritten), the envelope rules above,
  and durability across a close/reopen — a store that only answers from
  process memory has not answered invariant 2 at all.

Passing `RunConformance` is the floor. What each shape needs **beyond** passing
it is what step-04 scores.

## Storage substrate

SQLite via `modernc.org/sqlite` (pure Go). Not a spike decision: MODEL §3.1
fixes SQLite, and the Makefile's `CGO_ENABLED=0` posture — load-bearing for
packaging commitment 1 — rules out `mattn/go-sqlite3`. Both spikes use it, so
it is a constant in the comparison, not a variable. Pinned at `v1.34.5`, the
last line that keeps `scaffold`'s `go 1.23` directive (newer releases require
Go ≥ 1.25 and would break the Nix package's Go 1.24 toolchain).

## The migration exercise

Both spikes additionally take **one** schema change, applied as a numbered
migration against a database already populated by the scenario above, under
PLAN 1.2's versioned-migrations + backup-before-migrate posture:

> **v2:** nodes gain an optional `label` — a short mutable attribute set after
> birth by a new verb, emitting a new event type (`matter.labeled` /
> `step.labeled`), and surfaced in the "what is in progress" answer.

It is deliberately the ordinary shape of a future change: a new field, a new
event type, and a read path that must show it — including for rows that
existed before the migration. Old data must still answer the query correctly
afterwards. What that costs in each shape is rubric axis 3's raw material.

## What each spike reports

Its own `report.md`, next to its code:

1. **Axis 1 — invariant 1.** Is every mutation naturally a single event, or
   did the envelope have to be bolted on afterward? Where does the "exactly
   one" guarantee actually live in the code, and what could bypass it?
2. **Axis 2 — invariant 2.** What did "what is in progress" cost, concretely?
   For the event-sourced shape, the replay-vs-maintained-projection choice is
   the implementer's to make — **and the choice itself, with its reasoning, is
   part of the report.**
3. **Axis 3 raw material** — what the v2 migration actually took: files
   touched, lines, and what had to be true about existing data.
4. **Axis 4 raw material** — for each item on MODEL §10's fidelity list
   (append-only · identity-not-locator references · tier dimensions ·
   system-driven events first-class · enough to reconstruct in-flight work ·
   enough to narrate a sealed Matter after the fact), what this shape already
   gives for free and what it would still owe.

Plus anything the shape made obvious that the rubric does not ask about.
