# 0019 — the wip ⇄ tracker lifecycle contract

- Status: accepted
- Date: 2026-06-28
- Source: `step-lifecycle` initiative, Round 1 (step-01); BRIEF.md; Linear BDS-20 (refinement of the `surface-latent-state` ad-hoc run); ADR-0002, ADR-0006, ADR-0014, ADR-0016, ADR-0018

## Context

wip ran an ad-hoc issue-tracker lifecycle on the `surface-latent-state`
initiative: an issue maps to a node, and the node's boundaries drive the issue
through Todo → In Progress → In Review → Done. BDS-20 refines that into a
contract — **provider-agnostic by design, Linear first**. The refined design
(brief §§1-7) is locked; what this ADR resolves is the brief's five **open
tails**, which name the seams Rounds 2-5 build against.

This initiative is **blocked by BDS-22** (the forge seam) for its Tier-1 trigger
only; the Tier-0 path is independent and ships first. It is branched on top of
the unmerged forge-surface (BDS-22) branch so the Tier-1 path can consume the
`forge` verb + transport seam (ADR-0018), and so this contract can **generalize**
the `transition` field forge-surface already added to `ship`'s ledger.

## Decision

### A. The intent contract + which boundary emits it

Plumbing emits a single provider-agnostic **lifecycle intent** — a
generalization of the `transition` field ADR-0018 added to `ship`'s ledger:

```json
{ "node": "<slug>/<node-id>", "to": "in-progress|in-review|done", "reason": "start|ship|review-complete|push|merge" }
```

- The **boundary command itself emits the intent** — into its JSON ledger **and**
  the local cache (decision C / state floor). `workplan init --activate` emits
  `{to: in-progress, reason: start}`; `ship` emits `{to: in-review, reason:
  ship}` (or stands down under a forge, ADR-0018 §5); `review complete` emits
  `{to: done, reason: review-complete}`. Tier-1 push/merge intents come from the
  forge observation (ADR-0018 §4).
- **Plumbing stays pure (ADR-0006):** it emits the intent; it never calls the
  provider. The transport (decision below) and `sync` bind an intent to a live
  call. The intent is the seam.

### B. State vocabulary + mapping key name

- **Mapping key = `tracker:`** — provider-agnostic, matching the `issue-tracker`
  feature name. **Not `linear:`**: the design is provider-agnostic and Linear is
  merely the first backend.
- **Semantic state vocabulary = `todo` / `in-progress` / `in-review` / `done`**
  (+ `canceled` passed through). These are wip's words; the transport maps them
  to the provider's concrete state names (Linear: Todo / In Progress / In Review
  / Done). No provider state names leak into plumbing.

### C. The manifest mapping mirror is writer-generated

The **roadmap node body is the source of truth** for the `tracker:` key
(hand-authored alongside the step/round bullet). The `.wip.yaml` mirror is
**writer-generated** from the roadmap by a deterministic writer (the same family
as the closeout marker writer, ADR-0016), and `doctor` checks the two agree
(the marker-vs-archive check shape, ADR-0016). The human edits **one** place;
there are never two hand-maintained copies to drift.

### D. Command naming

- **CLI = `wip review complete <node>`** — a `review` subcommand family
  (`review list`, `review complete`), mirroring `forge observe` / `glossary
  check`. **Slash = `/wip:complete-review <node>`** — slash commands read as
  verb-phrase actions. This matches the brief's command-surface table.

### E. Intake source states — operator-selected

Intake is **operator-selected, not state-gated**: `/wip:intake <issue>` draws
from whatever issue the operator points it at (as the BDS-22 / BDS-20 intakes
did). The suggestion surface (decision: Round 5) treats both **`Todo` and
`Backlog`** issues as candidates, but intake **never auto-pulls**. Explicitly
**not** "Todo-only."

## Consequences

- Rounds 2-5 build against fixed seams: feature key `issue-tracker: { enabled,
  backend }`; node key `tracker:`; vocabulary `todo|in-progress|in-review|done`;
  intent shape `{node, to, reason}`; the writer-generated `.wip.yaml` mirror +
  `doctor` agreement check; verbs `wip review list|complete`, `wip sync`,
  `doctor --probe-linear`; slash `/wip:review`, `/wip:complete-review`,
  `/wip:sync`.
- The Tier-0 lifecycle (Round 3) is fully **headless** — intents land in the
  cache with no forge and no live tracker. The transport (Round 4) is additive.
- This **generalizes, not replaces**, ADR-0018: `ship.transition` becomes one
  emitter of the lifecycle intent; the forge observation is another (Tier-1).
- Scope honesty: the CLI transport (wrap `linearis`/`lintctl`/`schpet/linear-cli`)
  is **deferred to BDS-23**; this initiative ships the agent/MCP transport first.
- ADR-0006-clean: no provider state names in plumbing, no bespoke API client, no
  tracker→wip truth writes (sync is push-forward only).

## Deferred

- **Linear CLI transport** (research existing tooling) + backend CLI config —
  BDS-23.
- **Round/lane auto-transition** — only step + initiative completion boundaries
  auto-fire at first; round/lane levels fall back to `sync`/manual with `doctor`
  drift flagging until their boundaries land.
- **Tier-1 push/merge → Done wiring** depends on BDS-22 merging; until then the
  Tier-0 `review complete` is the Done path.
