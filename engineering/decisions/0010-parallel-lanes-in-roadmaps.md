# 0010 — Parallel lanes in roadmaps

- Status: accepted
- Date: 2026-06-15
- Source: planning session (2026-06-15); ADR-0004 (step-not-phase), ADR-0009 (intake-as-pipeline)

## Context

`wip` roadmaps are strictly linear (ADR-0004, the step/round grammar resolved in
step-08): a `## Round N` contains a flat list of `- **step-NN — <title>**` bullets;
rounds are sequential and steps within a round are sequential. The parser
(`lib/wip/wip-plumbing-roadmap-lib.bash`), the `next` ranker, the `status` reporter,
and `roadmap amend` all assume this shape. There is no first-class concept of "these
two steps run in parallel."

The recurring real case this loses is **parallel work tracks**. A roadmap-shaped lead
doc routinely schedules `Track A ‖ Track D` as concurrent, with a foundational step as
the shared prereq. Today the only way to encode that is `step-13 then step-14 then
step-15`, which linearizes a structure the input deliberately parallelized. The lost
parallelism is not cosmetic: it is the difference between "two people can pick up
independent work now" and "do these in an arbitrary forced order."

This is also a hard prereq for the deferred `bundle` intake kind (preserved in the
bundle handoff): a bundle emits a `### Lane <name>` amendment when its lead doc names
parallel tracks. Without a lane primitive the bundle would either linearize (lossy) or
hit an empty seam.

## Decision

Add **named parallel lanes** as a structural roadmap primitive. Lanes parallelize
*across*; steps *within* a lane stay sequential.

### 1. Format — `### Lane <name>` subheadings inside a round

A round contains main-lane step bullets plus zero or more `### Lane <name>`
subheadings, each owning its own step bullets:

```markdown
## Round 4 — Track expansion

- **step-12 — F1: model-profile taxonomy** — body…

### Lane A
- **step-13 — Track A part 1: core.document spine** — body…
- **step-15 — Track A part 2: ADR-010 + Hypomnema** — body…

### Lane D
- **step-14 — Track D: SPA usability v1** — body…
```

Step IDs stay **globally sequential** (`step-12, 13, 14, 15`); they do not restart per
lane. A lane is a *grouping*, not a numbering namespace. The parser tags each step with
a `lane` field — the lane name, or `null` for a main-lane step.

### 2. Prereqs by ordering, not metadata

The main-lane step *before* the first `### Lane` subheading is the prereq for every
lane in that round. There is no new `depends-on:` field on steps; dependency is
positional. This is the smallest-step interpretation; a richer cross-lane dependency
model can come later if a real case demands it.

### 3. No nested lanes

Lanes do not contain sub-lanes. A deeper heading (`#### Lane …`) inside a round is
malformed. Steps within a lane are sequential.

### 4. No cross-lane synchronization metadata

A step in Lane A cannot declare "I must finish before step-X in Lane D." Sync points are
expressed by main-lane sequencing: a prereq above the lanes, or sync steps below the
last lane (same round) / in a subsequent round.

### 5. The precise round grammar (and the rejected malformed cases)

Within a round the parser tracks a `current_lane`, `null` until the first `### Lane`.
A round's step sequence, by lane, must match `main* (lane+)? main*` — optional leading
main-lane (pre-lane prereqs), then the lane blocks, then optional trailing main-lane
(post-lane sync steps). Concretely the parser **rejects** (records into `lane_errors[]`):

- **`lane-outside-round`** — a `### Lane` heading in `## Backlog` / `## Deferred` / a
  non-round section.
- **`nested-lane`** — a `#### Lane …` (H4+) heading.
- **`duplicate-lane`** — two `### Lane <name>` headings with the same name in one round.
- **`main-step-between-lanes`** — a main-lane (`lane: null`) step that has a lane step
  *before it* **and** a lane step *after it* in the same round. This is the
  "bare bullet between two `### Lane` headings" case. Pre-lane and post-lane main steps
  are fine precisely because they are not sandwiched.

The parser never aborts: it always emits the parsed document (so `next` / `status` keep
working), and surfaces violations in a top-level `lane_errors[]` array (empty when
clean). The write path — `roadmap amend` — is where a non-empty `lane_errors[]` becomes
a refusal, so a malformed lane structure cannot be amended on top of.

### 6. `roadmap amend` directives

- **`insert-after: step-NN`** and **`replace: step-NN`** stay context-aware: the new /
  replacement step inherits whatever lane (or main lane) `step-NN` already lives in,
  because it is rendered in place. No directive change needed for main-lane targets.
- **`append-round: <title>`** body may include `### Lane` subheadings (it is emitted
  verbatim, and the parser recognizes the lanes on the next read).
- **New `append-lane: <name>`** with **`target-round: <N>`** — add a new lane to an
  existing round. The artifact body carries the lane's `### step-NN` entries; the lane
  block is appended at the end of round N (after existing lanes), idempotent via the
  same SHA-256-of-rendered-payload marker as the other directives.

`append-lane` is the **fourth** amendment directive; exactly one directive per amendment
artifact still holds (the `intake` validator's count check now ranges over four).

We deliberately do **not** add a separate `insert-step-in-lane` directive in v1: a
context-aware `insert-after: step-NN` already adds a step into the lane that hosts
`step-NN`. The explicit directive only earns its place if a real case needs to target an
*empty* lane (no anchor step) — which is exactly the bundle's emit pattern, so it is
reconsidered when the bundle kind ships.

### 7. `next` / `status` lane semantics

- **`next`** — when the active step lives in lane L, the next unshipped step in L is the
  primary forward candidate (`reason: "next-in-lane"`). Unshipped steps in *sibling*
  lanes of the same round are surfaced with **`concurrent: true`** so the porcelain can
  present "you could also work these in parallel." Ranks remain a stable total order
  (sequential, unique integers); the `concurrent` flag — not a duplicated rank integer —
  carries the parallelism signal. Across-round ranking is unchanged.
- **`status`** — discloses `lane` on the active step (it falls out of the parsed step
  record). When two or more lanes have unshipped steps in the active round, status
  reports `lanes_in_flight: [{lane, step}]` (the next actionable step per in-flight
  lane); otherwise that array is empty.

### 8. Manifest `active_step` stays scalar in v1

`.wip.yaml`'s `active_step` remains a single id. Working two lanes literally in parallel
means picking one as the manifest's `active_step`; the other lanes are tracked through
`status`'s `lanes_in_flight` only. Promotion to `active_steps[]` is deferred until a real
two-lane-at-once scenario demands it.

## Consequences

- The parsed roadmap document gains a `lane` field on every step (`null` for main-lane),
  a `lanes[]` array on every round (declared lane names, in order — supports empty
  lanes), and a top-level `lane_errors[]`. These are additive: a linear roadmap parses
  to the same rounds/steps/backlog it always did, every step `lane: null`,
  `lane_errors: []`. The distillation roadmap is the regression pin.
- Consumers touched: `parse` (lanes + errors), `roadmap amend` (`append-lane` + refuse
  on `lane_errors`), `next` (concurrent flag), `status` (`lane` + `lanes_in_flight`).
  `glossary`, `setup`, `graduate`, `extract`, `workplan init` do not read lane structure
  and are unchanged.
- A read-only `roadmap parse <file>` subcommand is added so the grammar (and the
  regression gate) is runnable from the CLI, not only from the sourced library.
- The closed amendment-directive vocabulary grows from three to four
  (`insert-after` / `replace` / `append-round` / `append-lane`); `intake-kinds.md` §3 and
  the `wip-plumbing-cli.md` `roadmap amend` section are updated to match.
- This unblocks the deferred `bundle` intake kind, which depends on lanes to express
  `A ‖ D` structurally rather than as prose.
- ADR-0004 is untouched: lanes group steps, they are not phases. ADR-0009's pipeline
  shape is untouched: `append-lane` is one more amendment directive, not a new kind.
