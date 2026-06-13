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
- `insert-after` / `replace` / `append-round` — amendment-only directives (see
  `amendment` shape rules).

Key name `wip-kind` (namespaced) was chosen over the bare `kind:` so the file can also
be consumed by other tools that may use `kind:` for their own purposes.

## 2. Kinds and minimum shapes

| Kind | Required shape (validator rules) | Destination |
|------|----------------------------------|-------------|
| `brief` | Title heading (`# <Title>`), one of `## Goal` or `## Summary`, no `target:` referencing an existing initiative slug. | `init <slug>` |
| `amendment` | `target: <initiative-slug>` in front-matter or first section; **one** of `insert-after: step-NN`, `replace: step-NN`, or `append-round: <title>`. Body sections per amendment directive (see §3). | `roadmap amend <slug>` |
| `workplan-seed` | `target: <slug>/<step-id>` in front-matter; narrative body (no required section set). Step **must** exist in the named initiative's roadmap. | `workplan init <slug> <step-id>` |
| `spec` | LDS-template conformance — heading set per `docs/specs/_template.md` if present in the consuming repo. Validator delegates to LDS when available (ADR-0006); falls back to a minimal heading check (`## Summary`, `## User stories` or `## Requirements`). | LDS seam |
| `handoff` | Parseable markdown + a title. Always coerces to `brief` or `amendment` during shaping; **never** applied as `handoff`. | (transient) |

`handoff` is the only kind without a terminal `apply` path. Its presence in the
vocabulary is deliberate: classify needs a label for "I can tell this is intended for
wip, but I cannot yet tell if it is a new thing or an edit to an existing thing."

## 3. Amendment directives

Exactly one directive must be present. Each pins the deterministic edit `roadmap amend`
performs.

- **`insert-after: step-NN`** — insert a new step immediately after `step-NN`. Body
  requires a single `### step-XX — <title>` heading (where `XX` is the new step's id;
  may be a `.5` slot per the distillation convention) and a one-or-more-paragraph body.
- **`replace: step-NN`** — replace the body of `step-NN` (keeping its id and heading
  unless the body provides a new `### step-NN — <new title>`). Body is the replacement.
- **`append-round: <title>`** — append a new round to the roadmap. Body requires a
  `## Round <N> — <title>` heading and one or more `### step-NN — <title>` entries.

Re-applying the same amendment artifact to the same roadmap is a **no-op**: `roadmap
amend` stamps a hash-of-payload comment into the roadmap at the insertion site and
detects duplicates on re-apply.

## 4. Classification heuristics

`intake classify` returns `{kind, confidence, signals[]}` with `confidence ∈ {high,
medium, low}`. Rules, applied in order; first match wins (later rules only contribute
`signals[]` for porcelain context):

| Rule | Kind | Confidence |
|------|------|------------|
| Front-matter `wip-kind:` present and valid | as stated | high |
| `target:` key + one of `insert-after` / `replace` / `append-round` | `amendment` | high |
| `target:` key matching `<slug>/<step-id>` (existing step) | `workplan-seed` | high |
| `target:` key matching an existing initiative slug, no amendment directive | `amendment` | medium (porcelain must pick directive) |
| `### step-NN — ` heading in body + no `target:` | `amendment` (likely) or `handoff` | low |
| `## User stories` or `## Requirements` heading | `spec` | medium |
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
