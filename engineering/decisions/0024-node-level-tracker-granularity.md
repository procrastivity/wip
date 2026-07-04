# 0024 — node-level tracker granularity (initiative / round / step; lane excluded)

- Status: accepted
- Date: 2026-07-04
- Source: `wip-orchestration-robustness` initiative, Round 1, step-03 (the spike);
  Linear BDS-56; workplan D1–D7. Amends/extends ADR-0019 (resolves its §Deferred
  "Round/lane auto-transition"). Builds on ADR-0010 (lanes), ADR-0016 (closeout
  write contract), ADR-0006 (plumbing purity).

## Context

ADR-0019 fixed the wip ⇄ issue-tracker lifecycle contract — a provider-agnostic
intent `{node, to, reason}` that a **boundary command emits** into its ledger and
the local cache floor — but left one item explicitly **deferred**:

> **Round/lane auto-transition** — only step + initiative completion boundaries
> auto-fire at first; round/lane levels fall back to `sync`/manual with `doctor`
> drift flagging until their boundaries land.

Running real initiatives surfaced two concrete leaks that force the deferred
decision:

1. **Tracker granularity is silently step-only.** The three live emission
   boundaries (`workplan init --activate`, `ship`, `review complete`) are all
   step-level. The roadmap parser extracts `[tracker: ID]` on **steps** only; a
   `## Round N` heading parses without a `tracker` field and
   `_wip_tracker_map_from_roadmap` harvests `.rounds[].steps[]` exclusively. So a
   roadmap author cannot address a round or the initiative as a tracker node, and
   no boundary emits for them.
2. **No durable initiative→issue anchor.** The initiative record carries no field
   naming the source issue it came from, so the initiative-as-a-whole has no node
   to advance. (Captured in step-03 Chunk 1 — the `tracker_anchor` field — and
   contracted here.)

This ADR states the emission **model**, the node **addressing** scheme, the
**lane exclusion**, and the **anchor** contract, and fixes the scope split of
what auto-emission plumbing ships now vs. what stays deferred.

## Decision

### 1. Boundary-local emission at the lifecycle-bearing node levels — {step, round, initiative}

Each mapped node emits its **own** lifecycle intent **at its own boundary
command**, via the existing `_wip_tracker_emit_intent` mechanism. This is a
straight extension of ADR-0019 §A ("the boundary command itself emits the
intent"), not a new model — one uniform rule across every node level:

- **step** — activate / ship / review-complete (exists, unchanged).
- **round** — a round's first-step activation is its *start*; a round-closer is
  its *done*.
- **initiative** — `wip init` / intake-apply is its *start*; closeout
  (`status → shipped`) is its *done*.

**Rejected alternative — roll-up / derived state** (only steps emit; `sync`
computes a round's state from its children). Rejected because it breaks plumbing
purity (ADR-0006 — `sync` would become a stateful deriver rather than a
push-forward reconciler), `in-review` does not roll up meaningfully, and it
diverges from ADR-0019 §A's boundary-emits-intent contract steps already follow.

### 2. Lane is NOT a lifecycle-emitting node level

A lane has no independent completion boundary (ADR-0010: "a lane is a *grouping*,
not a numbering namespace" — no `depends-on`, no completion marker, no
lane-boundary command). A lane "completes" exactly when its last step ships,
already captured by those steps' own emissions. A team wanting a lane-level
tracker issue (an epic) maps it to the enclosing **round** or models it as an
out-of-band parent/epic link — **not** an auto-transitioned wip node.

This is enforced, not merely documented: the roadmap parser **ignores** a
`[tracker: ID]` key on a `### Lane` heading (strips it from the lane name, records
no mapping), so an author cannot wire a lane auto-transition by mistake.

### 3. Node addressing — `<slug>/<node-id>`

Cache keys and `tracker_map` keys are already free strings; this fixes the
convention for the node-id token:

- `step-NN` — a step node (exists).
- `round-N` (e.g. `round-1`) — a round node.
- the literal token `initiative` (→ key `"<slug>/initiative"`) — the whole
  initiative node. Chosen over a bare `"<slug>"` key so every cache key is
  uniformly `<slug>/<node>` and an initiative node is never ambiguous with a slug
  that happens to look like a node id.

### 4. Source-of-truth differs per node level

- **step & round** tracker ids are **roadmap-authored** — `[tracker: BDS-XX]` on
  the step bullet or on the `## Round N — title` heading — and mirrored into
  `.wip.yaml tracker_map` (ADR-0019 §C, unchanged). Round support = the parser
  extracts a `tracker` field on the round heading, and
  `_wip_tracker_map_from_roadmap` harvests `round-N` entries alongside `step-NN`.
- **initiative** tracker id is **intake-anchored, NOT roadmap-authored** — there
  is no initiative bullet in the roadmap. It is captured at `/wip:intake` time and
  persisted as a **new top-level field `tracker_anchor: <ID>`** on the initiative
  record (a **sibling** of `tracker_map`, never inside it). The bind plan / sync
  union `tracker_map` (steps + rounds) with the `tracker_anchor` (as node
  `initiative`). Keeping the anchor a separate field preserves ADR-0019 §C's
  "roadmap is SoT for `tracker_map`" invariant intact: the anchor has a different,
  earlier SoT (intake) and must not be clobbered by `wip tracker map --write`.

### 5. Ship addressing + initiative-START now; defer the round/initiative DONE writers

No round-closer and no initiative-closeout writer exists today: `ship` operates
step-level only (ADR-0016 scoped round/initiative closeout out of it), and nothing
writes `initiatives[].status = shipped`. Auto-emitting `done` for those nodes
therefore requires **new boundary commands** — a distinct surface. This step lands:

- node **addressing** (parser round-tracker + `tracker_anchor` field + the
  `<slug>/<node>` keys),
- the **initiative-START** emission at the existing `init` / intake-apply boundary
  (`{to:in-progress, reason:start}` under `<slug>/initiative`, gated on an anchor
  being present **and** `issue-tracker` enabled; headless, dry-run-parity with the
  step emitters),
- the `doctor` anchor check (informational; ADR-0019 §2f shape, never exit-4),
- `sync` honoring the initiative + round nodes (push-forward works whether the
  cache entry was boundary-seeded or unseeded).

It **defers** the round-closer and initiative-closeout **writers** (the `done`
auto-emitters) to a follow-on, exactly as ADR-0019 §Deferred already planned. The
load-bearing model (§§1–4) is locked; only how much auto-emission plumbing ships
here is the sizing question, and it is answered: defer the `done` writers.

### 6. `doctor` flags a missing anchor as informational, not hard drift

For an enabled, in-flight, anchor-less initiative, `doctor` emits a
`tracker-anchor` suggestion with `status:"ok"` (ADR-0019 §2f `tracker-unfiled`
shape), **never** exit-4 drift (§2d). Rationale: retrofitting anchors onto
pre-existing anchor-less initiatives (this one included) must not fail `doctor`
fleet-wide the moment the feature lands. Can harden to real drift once anchors are
backfilled.

## Consequences

- The intent mechanism, cache floor, mirror-vs-roadmap agreement check, and `sync`
  push-forward are **unchanged** — they were already node-agnostic; this ADR only
  makes rounds and the initiative addressable and adds one new start boundary.
- The `tracker-mirror-drift` gate (ADR-0019 §C / §2d) covers steps **and** rounds
  (both live in `tracker_map`). The `tracker_anchor` is deliberately **outside**
  the mirror, so it is excluded from the `rmap == mmap` equality check and unioned
  in only *after* the drift gate.
- A follow-on that builds the round-closer / initiative-closeout boundary writers
  will hang `{to:done, reason:...}` emissions on them with no model change — the
  node keys (`round-N`, `initiative`) and their cache entries already exist.

## Deferred

- **Round-closer / initiative-closeout `done` auto-emitters** — the boundary
  writers do not exist yet (ADR-0016 scoped them out of `ship`; nothing writes
  initiative `status`). Left as a documented follow-on (workplan Open Q1), NOT a
  silent skip. Until they land, round/initiative `done` falls back to
  `sync`/manual with `doctor` flagging — consistent with ADR-0019 §Deferred.
- **Hardening the missing-anchor check to exit-4 drift** — once anchors are
  backfilled fleet-wide.
- **Multi-anchor bundles** — a bundle uses a single lead/epic anchor; per-child
  issues ride step/round `[tracker:]` keys. Revisit only if a real bundle needs a
  first-class multi-anchor list.
