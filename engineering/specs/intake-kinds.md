# Spec — Intake artifact kinds (v1)

- Status: draft
- Date: 2026-06-13
- Initiative: distillation · roadmap **step-07.5**
- Decisions: [ADR-0009](../decisions/0009-intake-as-pipeline.md) (intake as pipeline),
  [ADR-0001](../decisions/0001-three-layer-plumbing-porcelain.md) (layers),
  [ADR-0006](../decisions/0006-wip-owns-seams-not-tools.md) (seams)

The closed vocabulary of artifact `kind`s the intake pipeline recognizes, the minimum
shape each must satisfy, and the heuristics `intake classify` uses to guess `kind` from
an arbitrary inbound file.

This spec is consumed by `wip-plumbing intake classify` (heuristics), `wip-plumbing
intake validate` (shape rules), and `wip-plumbing intake apply` (routing). The
`wip intake` porcelain reads it to know what shaping outputs are legal.

---

## 1. Front-matter convention

The deterministic signal `classify` keys off when present:

```yaml
---
wip-kind: amendment
target: distillation
insert-after: step-06
---
```

- `wip-kind` — one of the kinds in §2. Optional but authoritative when present
  (overrides heuristics).
- `target` — initiative slug (for `amendment`, `workplan-seed`) or `<slug>/<step-id>`
  (for `workplan-seed`).
- `insert-after` / `replace` / `append-round` / `append-lane` / `insert-step-in-lane` —
  amendment-only directives (see `amendment` shape rules). `append-lane` and
  `insert-step-in-lane` additionally require `target-round: <N>`.
- `bundle`-only keys (`wip-kind: bundle`, `lead-as`, `children`, `cross-cuts`) — see the
  `bundle` row in §2 and the directives subsection in §3.

Key name `wip-kind` (namespaced) was chosen over the bare `kind:` so the file can also
be consumed by other tools that may use `kind:` for their own purposes.

## 2. Kinds and minimum shapes

| Kind | Required shape (validator rules) | Destination |
|------|----------------------------------|-------------|
| `brief` | Title heading (`# <Title>`), one of `## Goal` or `## Summary`, no `target:` referencing an existing initiative slug. | `init <slug>` |
| `amendment` | `target: <initiative-slug>` in front-matter or first section; **one** of `insert-after: step-NN`, `replace: step-NN`, `append-round: <title>`, `append-lane: <name>`, or `insert-step-in-lane: <name>` (the last two also need `target-round: <N>`). Body sections per amendment directive (see §3). | `roadmap amend <slug>` |
| `workplan-seed` | `target: <slug>/<step-id>` in front-matter; narrative body (no required section set). Step **must** exist in the named initiative's roadmap. | `workplan init <slug> <step-id>` |
| `spec` | LDS-template conformance — heading set per `docs/specs/_template.md` if present in the consuming repo. Validator delegates to LDS when available (ADR-0006); falls back to a minimal heading check (`## Summary`, `## User stories` or `## Requirements`). | LDS seam |
| `bundle` | Front-matter `wip-kind: bundle`, `lead-as: {brief\|amendment}`, and a non-empty `children:` list of readable child paths (relative to the lead). Lead body must satisfy the `lead-as` kind's rules (validated against a bundle-key-stripped copy). See §3. | (porcelain explode) |
| `handoff` | Parseable markdown + a title. Always coerces to `brief` or `amendment` during shaping; **never** applied as `handoff`. | (transient) |

`handoff` and `bundle` are the kinds without a terminal `apply` path. `handoff`'s
presence is deliberate: classify needs a label for "I can tell this is intended for
wip, but I cannot yet tell if it is a new thing or an edit to an existing thing."
`bundle` is non-terminal for a different reason: it is *structurally* a lead plus N
children, so the porcelain explodes it into one lead intake plus per-child intake calls
(each reusing the single-file pipeline) rather than applying it atomically. `apply --kind
bundle` exits 4 `not-terminal`, mirroring `handoff`.

## 3. Amendment directives

Exactly one directive must be present (the validator's count ranges over all five). Each
pins the deterministic edit `roadmap amend` performs.

- **`insert-after: step-NN`** — insert a new step immediately after `step-NN`. Body
  requires a single `### step-XX — <title>` heading (where `XX` is the new step's id;
  may be a `.5` slot per the distillation convention) and a one-or-more-paragraph body.
  Lane-aware (ADR-0010): the new step inherits `step-NN`'s lane (or main lane).
- **`replace: step-NN`** — replace the body of `step-NN` (keeping its id and heading
  unless the body provides a new `### step-NN — <new title>`). Body is the replacement.
  The replaced step stays in its existing lane.
- **`append-round: <title>`** — append a new round to the roadmap. Body requires a
  `## Round <N> — <title>` heading and one or more step entries — either `### step-NN —
  <title>` headings (amendment form) or `- **step-NN — <title>**` bullets (canonical
  roadmap form). The body may include `### Lane <name>` subheadings (ADR-0010); when it
  does, use the bullet form so the lane blocks stay contiguous (the parser ends a lane
  block at a blank line).
- **`append-lane: <name>`** with **`target-round: <N>`** — add a new lane to an existing
  round (ADR-0010). Body requires one or more `### step-NN — <title>` entries and **no**
  `## Round` heading. The lane block is appended at the end of round N, idempotent via the
  same hash-of-payload marker. Refuses if the lane name already exists in round N
  (`duplicate-lane`).
- **`insert-step-in-lane: <name>`** with **`target-round: <N>`** — append a single step to
  the end of an **already-declared** lane in round N (ADR-0010 §6, promoted when the
  `bundle` kind shipped). Body requires exactly one `### step-NN — <title>` heading + body
  (same shape as `insert-after`) and **no** `## Round` heading. Unlike `append-lane`, the
  target lane must already exist — including an **empty** lane (a `### Lane <name>` heading
  with no steps yet), which is precisely a `bundle` lead's emit pattern: the lead declares
  empty lanes via `append-round`, and each child fills its lane via `insert-step-in-lane`.
  Idempotent via the same hash-of-payload marker. Refuses if the lane is absent from round
  N (`lane-not-in-round`) or the round is absent (`round-not-in-roadmap`).

Re-applying the same amendment artifact to the same roadmap is a **no-op**: `roadmap
amend` stamps a hash-of-payload comment into the roadmap at the insertion site and
detects duplicates on re-apply.

## 3a. Bundle directives

A `bundle` is a *lead* doc plus a manifest of children. Its front-matter, not a body
section, carries the structure:

- **`wip-kind: bundle`** — authoritative; required (a bundle is never inferred to
  high confidence without it, only proposed at low confidence — see §4).
- **`lead-as: {brief | amendment}`** — what kind the *lead* doc is shaped and applied as.
  v1 restricts lead kinds to `brief` and `amendment`; `workplan-seed` / `spec` / `handoff`
  leads are out of scope.
- **`children:`** — a non-empty list. Each entry is a map:
  - `path:` — **required**, relative to the lead doc; must resolve to a readable file. The
    validator rejects a missing or unreadable child path. Globs are never present in a
    *validated* bundle (the shaper resolves them to concrete paths first).
  - optional hints consumed by the porcelain explode: `kind` (`brief`/`amendment`; never
    `bundle` — nested bundles are refused), `target`, an amendment directive
    (`insert-after` / `replace` / `append-round` / `append-lane` / `insert-step-in-lane`),
    `id`, `lane`, `depends-on` (a child path or id; orders the explode), and
    `shares-seam-with`.
- **`cross-cuts:`** — optional. `shared-seams[]` records cross-track concerns;
  `parallel-groups[]` records `[[A, D]]`-style parallelism the explode maps to lanes.

The validator enforces only the *structural* minimum (`wip-kind: bundle`, a valid
`lead-as`, a non-empty `children[]` with readable paths) plus the lead body satisfying its
`lead-as` kind's rules (checked against a copy with the bundle-only keys stripped). The
porcelain explode (ADR-0009 phase 2/4) owns everything else: materializing the lead
tempfile (injecting a `## Cross-cuts (from bundle)` section and `### Lane <name>`
subheadings from `parallel-groups`), topo-sorting children by `depends-on`, and applying
the lead + each child through the single-file pipeline. Per-child apply is **independent
and non-atomic**: partial failure is reported in the aggregate envelope, not rolled back;
re-apply is safe via the existing amendment hash markers.

## 4. Classification heuristics

`intake classify` returns `{kind, confidence, signals[]}` with `confidence ∈ {high,
medium, low}`. Rules, applied in order; first match wins (later rules only contribute
`signals[]` for porcelain context):

| Rule | Kind | Confidence |
|------|------|------------|
| Front-matter `wip-kind:` present and valid | as stated | high |
| `target:` key + one of `insert-after` / `replace` / `append-round` / `append-lane` | `amendment` | high |
| `target:` key matching `<slug>/<step-id>` (existing step) | `workplan-seed` | high |
| `target:` key matching an existing initiative slug, no amendment directive | `amendment` | medium (porcelain must pick directive) |
| `### step-NN — ` heading in body + no `target:` | `amendment` (likely) or `handoff` | low |
| `## User stories` or `## Requirements` heading | `spec` | medium |
| Roadmap-shaped: a `## Track…`/`## Roadmap`/`## Children`/`## Foundational…` heading (a single `## Tracks` section or per-track `## Track A`/`## Track D` headings both count) **and** a `## Sequence`/`## Recommended sequence` heading, no `target:`, no amendment directive | `bundle` | low |
| Title heading + `## Goal`/`## Summary`, no `target:` | `brief` | medium |
| Parseable markdown, title present, none of the above | `handoff` | low |
| Unparseable / no title | (invalid; classify exits 4) | — |

Porcelain is free to override any non-`high` classification. Plumbing never asks; it
reports its guess and the signals that led there.

## 5. Worked examples

- **Real Claude Code plan file** named `~/.claude/plans/explore-how-clast-whimsical-floyd.md`
  with body referencing `step-07.5` and `target: distillation` (added during shaping) →
  classify: `amendment` (high); validate against amendment rules; apply via
  `roadmap amend distillation --insert-after step-06`.
- **`spec-generator` output** with LDS heading set → classify: `spec` (medium); validate
  via LDS delegate or fallback; route to LDS seam.
- **Claude Desktop handoff** "here's where I left off on the auth rewrite" → classify:
  `handoff` (low); porcelain decides `brief` (new initiative) or `amendment` (existing
  `auth-rewrite` slug) and reshapes accordingly before re-validation.
- **Roadmap-shaped lead doc** `handoff-post-phase0-roadmap.md` (a `## Tracks` section
  naming Track A ‖ Track D plus a foundational F1, a `## Recommended sequence` section, no
  `target:`) → classify: `bundle` (low); porcelain confirms (low confidence), then shapes
  it into a bundle with `lead-as: amendment`, `target: typed-context`, three concrete
  `children:` (F1 in the main lane, Track A → Lane A, Track D → Lane D),
  `cross-cuts.shared-seams: [ChatRespondLoop prompt-assembly]`, and
  `cross-cuts.parallel-groups: [[track-A, track-D]]`. Validate confirms the structure +
  readable child paths + the lead body satisfies `amendment` rules. Explode (topo order
  `[lead, F1, track-A, track-D]`): the lead `append-round`s a "Track expansion" round with
  a `## Cross-cuts (from bundle)` section and empty `### Lane A` / `### Lane D`
  subheadings; F1 `insert-after`s into the main lane; Track A / Track D each
  `insert-step-in-lane` their lane. Envelope: `{ok, kind: bundle, lead, children[],
  summary}`, `ok` iff the lead and every child applied.

## 6. Open questions

1. **`handoff` as terminal kind.** v1 says `handoff` always coerces during shaping.
   Alternative: make it first-class and write to `.wip/inbox/` for later triage —
   preserves raw artifacts even after shaping. Lean: keep transient in v1; add an
   `--archive` flag to `intake apply` later if the inbox idea earns its keep.
2. **`wip-kind` key namespacing.** Confirmed `wip-kind` over `kind:` and `intake-kind:`
   in §1; revisit only if a real conflict emerges.
3. **Amendment idempotency hash.** Hash the shaped payload bytes, or a normalized
   structural digest? Bytes are simpler but break on whitespace changes; structural is
   robust but needs a parser. Lean: bytes in v1, structural if churn proves it.
