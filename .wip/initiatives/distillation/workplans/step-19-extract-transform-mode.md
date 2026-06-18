# Workplan — step-19 · `extract` transform mode

Anchors: roadmap `Round 4` → step-19 (`.wip/initiatives/distillation/roadmap.md:66`,
"LDS `transform` mode … Requires a small markdown engine in bash + per-transform
options. **Spike scope before committing.**"); LDS transform spec
`layered-documentation-system/extract.md` §3.3 + `schemas/extraction-manifest.schema.yaml`
(`transform_config`, lines 301–319 / Example 5 lines 1056–1072); CLI spec
`engineering/specs/wip-plumbing-cli.md` §`extract` (line 822 — the `transform` row this
step flips); ADR-0006 (`wip` owns the LDS seam, invokes — does not reimplement — but the
deterministic Extract core lives in plumbing per step-15).

Started: 2026-06-18.

---

## ⭐ Scope recommendation (READ FIRST — human go-gate decision)

**This step is a scope spike. The roadmap explicitly says "Spike scope before committing."**
The LDS `transform` mode names four transform types (`heading_adjust`, `link_rewrite`,
`markdown_format`, `custom`). `custom` is already declared OUT OF SCOPE by LDS itself
(`extract.md` §3.3 / line 473). That leaves three to weigh.

### Recommendation: **(b) — implement a viable subset this step + defer the rest.**

> **Ship `heading_adjust` only in step-19. Defer `link_rewrite` and `markdown_format`
> to §Backlog.**

This shrinks `extract`'s `unsupported[]` by the **one transform with precise, testable
semantics and a clean bash implementation**, stays additive in the exact shape of
steps 16–18 (one new pure lib helper + a classify-routing change + tests), and leaves the
two genuinely-speculative/underspecified transforms in backlog — which is precisely the
round's closing criterion ("the round closes when `extract`'s remaining v1 `unsupported[]`
shrinks to the genuinely-speculative items", roadmap line 61).

Rejected alternatives:
- **(a) all three** — over-reaches. `link_rewrite` and `markdown_format` are high-risk and
  under/un-specified (see table); building them now imports correctness risk and a likely
  new flake dependency for little value, against the additive grain of this round.
- **(c) split into multiple steps / defer entirely** — `heading_adjust` alone is a single
  focused step (≈ the size of step-18), so a multi-step split is unwarranted; and deferring
  *everything* would leave the round's one large capability gap untouched when a clean,
  high-value subset is readily shippable.

### Per-transform complexity / risk table

| Transform | Semantics defined? | Bash feasibility | Blast radius | Existing-tool leverage | Verdict |
|---|---|---|---|---|---|
| **`heading_adjust`** (`level_offset`, `skip_first`) | **Yes — fully.** `extract.md` §3.3 defines exact behavior; Example 5 exercises it. | **High.** A ~30-line fence-aware `awk` ATX-heading level shifter. Pure, line-oriented, deterministic. | **Bounded.** Only `^#{1,6}` heading lines change; code fences preserved. | `awk` (in flake). No new dep. | ✅ **SHIP v1.** Low–medium complexity, low risk. This *is* the "small markdown engine" the roadmap envisioned. |
| **`link_rewrite`** (`base_path`) | **No — underspecified.** LDS says only "rewrite internal links… update relative paths based on `base_path`" with **no precise algorithm**. | **Low.** Markdown link syntax is rich (inline/ref-style/images/autolinks/titles, code-span exclusion, URLs containing `)`); regex/sed approaches mangle edge cases. | **High & silent.** A wrong rewrite yields a broken link that's invisible until clicked. | None robust; would need a parser. | ⛔ **DEFER.** High complexity, high risk, **spec gap** (needs `base_path` semantics tightened first). |
| **`markdown_format`** (no options) | **No — undefined.** "Format/clean/normalize markdown" has no canonical meaning; formatters (`prettier`/`mdformat`/`remark`) disagree. | **Low.** Any choice is arbitrary; easy to corrupt fences/tables; **conflicts with LDS's "deterministic/mechanical, no interpretation" principle.** | **High.** Touches every line. | **None in flake** — would need a *new* formatter dependency. | ⛔ **DEFER.** High complexity, high risk, **undefined target + new dep**. Arguably never belongs in plumbing. |

> If the human wants a token `markdown_format` at the gate anyway, the smallest defensible
> v1 is "collapse runs of ≥3 blank lines → 1 blank line + strip trailing whitespace, code
> fences excluded" — but even that mutates author-intended whitespace, so the lean is still
> **defer**.

### What ships in step-19 (scope (b))
- `transform` mode is **supported** when `transform_config.type == heading_adjust` **and**
  the source classifies as a verbatim-able single-file / simple-path source.
- `transform` with `type` ∈ {`link_rewrite`, `markdown_format`, `custom`}, **or** on a
  multi-file source, stays in `unsupported[]` (skip, don't fail) — `unsupported[]` shrinks
  but is not emptied.
- Everything else (`verbatim` / `content` / no-transform) stays **byte-identical** to step-18.

---

## Decisions (made here, feed later steps)

- **D1 — Subset, not whole.** step-19 ships `heading_adjust` only; `link_rewrite` and
  `markdown_format` are deferred to §Backlog as `extract-transform-link-rewrite` and
  `extract-transform-markdown-format` (the latter carries a standing note that it may belong
  in porcelain, never plumbing). `custom` remains permanently out of scope per LDS.
- **D2 — Pure engine helper, step-18 pattern.** The heading shifter is a standalone pure
  helper `wip_extract_heading_adjust` (stdin → stdout; args `level_offset`, `skip_first`),
  unit-testable in isolation exactly like `wip_extract_sha256` / `wip_extract_source_body`.
  The renderer `wip_extract_render_transform` = `wip_extract_attribution_lines` + blank line
  + `wip_extract_source_body | wip_extract_heading_adjust`. Attribution is **identical** to
  verbatim (transform has a real source), so the §6.3 block is reused unchanged.
- **D3 — Fence-aware ATX only.** The engine adjusts only ATX headings (`^ {0,3}#{1,6}(\s|$)`)
  outside fenced code blocks; it tracks ```` ``` ````/`~~~` fence state and leaves fenced
  content (and 4-space-indented code) untouched. Setext (`===`/`---` underline) headings are
  left unchanged in v1 (documented limitation; offset on setext is ill-defined).
- **D4 — Clamp to [1,6].** `new_level = clamp(old_level + level_offset, 1, 6)` — never emit
  invalid `#######` (7) and never drop a heading to 0. (See OQ1.)
- **D5 — Faithful defaults.** Missing `level_offset` → `0` (faithful no-op shift); missing
  `skip_first` → `false`. A transform entry is only `bad-shape` if `transform_config` is
  absent/non-map; a no-op shift still writes faithfully rather than failing.
- **D6 — Classification routing.** New action token `ok-transform`. The still-unsupported
  types route to `unsupported-transform:<type>` (parallels the existing
  `unsupported-mode:` / `unsupported-source:` shapes); the dispatcher's `unsupported-*`
  ledger branch absorbs it with reason `"<type> transform not supported in v1"`. Transform on
  a multi-file source keeps reporting `unsupported-source:multi-file`.
- **D7 — `--verify-hashes` stays verbatim-only (additive).** step-18's gate remains gated on
  `cls == ok-verbatim`; transform entries are counted in `content_hash_check.entries_no_hash`
  and never failed. The source-byte recipe is identical for transform, so extending the gate
  later is cheap — deferred to keep step-18's surface byte-stable. (See OQ4.)
- **D8 — Idempotency unchanged.** Transform output flows through `wip_setup_write_idempotent`
  like every other target; deterministic transform → stable bytes → three-way idempotent.

## Chunks

_Each chunk is one focused commit, sequenced so the suite is green after each. Mirrors the
step-17/18 cadence (spec → helper → wire → test → roadmap)._

1. **Spec: flip the `transform` row.** In `engineering/specs/wip-plumbing-cli.md` §`extract`,
   change the v1-scope `transform` row (line 822) from "skipped" to "**partial** —
   `heading_adjust` supported; `link_rewrite` / `markdown_format` / `custom` skipped", and
   add a short "Transform mode (v1)" subsection documenting `heading_adjust` semantics
   (`level_offset` clamp, `skip_first`, fence-awareness, setext limitation) + the new
   `unsupported-transform:<type>` reason string. Docs only.
2. **Engine helper.** Add the pure `wip_extract_heading_adjust` (fence-aware `awk` ATX level
   shifter) to `lib/wip/wip-plumbing-extract-lib.bash`, with a doc-comment in the house style
   of the existing helpers. No wiring yet — independently unit-testable.
3. **Renderer + classify routing.** Add `wip_extract_render_transform`; extend
   `wip_extract_classify_entry` so `transform` + `heading_adjust` + verbatim-able source →
   `ok-transform`, other types → `unsupported-transform:<type>`, multi-file →
   `unsupported-source:multi-file`, absent/bad `transform_config` → `bad-shape:…`. Wire a
   `transform) action="transform"` branch + an `unsupported-transform:*` ledger branch into
   `lib/wip/wip-plumbing-subcommands/extract.bash`. Verbatim/content paths stay byte-identical.
4. **Tests.** Extend `test/test-extract.sh`: heading shift `+1` and `-1`, `skip_first`, clamp
   at both ends, fenced-`#` untouched, indented-`#` untouched, idempotent re-run, attribution
   present; `link_rewrite` / `markdown_format` / `custom` still land in `unsupported[]`;
   transform-on-multi-file still `unsupported-source`; §7 report reconciliation (transform
   success counted in `summary.successful` / `files_created`, not `unsupported`). **Migrate
   the existing step-18 "[unsupported] transform" case** (lines 150–189): it currently uses
   `type: heading_adjust` to assert the *unsupported* path — that path is now *supported*, so
   repoint that fixture to `type: markdown_format` (or `link_rewrite`) to keep exercising the
   skip path. (See Test strategy note.)
5. **Roadmap.** Mark step-19 shipped in `roadmap.md` with the subset summary + the two new
   backlog entries; this closes Round 4.

## Test strategy

- **Engine in isolation (chunk 2/4).** Source the lib and pipe crafted markdown through
  `wip_extract_heading_adjust <offset> <skip_first>` directly, asserting on stdout — the
  step-18 pattern for `wip_extract_sha256` / `wip_extract_source_body`. Covers: offset up,
  offset down, clamp ceiling (`###### ` +1 stays `######`), clamp floor (`# ` −1 stays `# `),
  `skip_first` leaves only the first heading, a `#` inside ```` ``` ```` / `~~~` fences is
  untouched, a 4-space-indented `#` is untouched, setext underline left alone, non-heading
  `#tag` (no space) untouched.
- **End-to-end through `extract` (chunk 4).** A manifest with a `heading_adjust` entry writes
  a target with shifted headings + attribution; a second run skips it (idempotent); the §7
  report counts it as success. Separate fixtures keep `link_rewrite` / `markdown_format` /
  `custom` and transform-on-multi-file in `unsupported[]` with the right reason strings.
- **Regression guard.** All 34 suites must stay green; the verbatim/content/`--verify-hashes`
  cases prove the additive invariant byte-for-byte. **One deliberate edit:** the step-18
  transform-unsupported fixture is migrated (chunk 4) because its assertion encodes the *old*
  behavior — this is a required test migration, not a regression. Note it in the commit.
- **Deferred coverage:** `link_rewrite` / `markdown_format` *behavior* is not tested (not
  built); only their continued presence in `unsupported[]` is asserted.

## Definition of done

_Observable behavior:_

- A manifest entry `mode: transform` with `transform_config.type: heading_adjust` over a
  single-file / simple-path source **writes** a target whose ATX headings are shifted by
  `level_offset` (clamped to 1–6), with `skip_first` honored and fenced/indented code
  untouched, attribution (`<!-- Migrated from … -->` + Extraction ID) prepended.
- That target is **three-way idempotent**: an unchanged re-run reports it under
  `skipped_idempotent` and `--force` overwrites.
- `transform` with `type` ∈ {`link_rewrite`, `markdown_format`, `custom`} **or** on a
  multi-file source appears in `unsupported[]` (run still `ok:true`, other entries still
  execute) with a descriptive reason; it is **not** in `files_created` / `errors`.
- `verbatim` / `content` / no-transform output and the stdout JSON envelope shape are
  **byte-identical** to step-18 (existing suites green, minus the one migrated fixture).
- `--dry-run` / `WIP_DRY_RUN=1` writes neither targets nor report; exit codes and existing
  extraction semantics unchanged.
- The §7 `extraction-report.{yaml,md}` reconciles with the stdout ledger (transform success
  in `summary.successful`).
- All 34 test suites pass; `make check` (shellcheck/shfmt) clean.

## Open questions to resolve during execution

1. **Offset overflow/underflow** — **lean: clamp to [1,6]** (D4). Emitting `#######` is
   invalid markdown and 0 is impossible; clamping never drops or invalidates a heading.
2. **Setext headings** (`Title` + `===`/`---` underline) — **lean: leave untouched in v1**,
   document as a known limitation; converting setext→ATX to apply offset is scope creep.
3. **Malformed / missing `transform_config`** — **lean: default `level_offset:0`,
   `skip_first:false` and write faithfully** (D5); only an absent or non-map `transform_config`
   is `bad-shape`.
4. **`--verify-hashes` over transform entries** — **lean: keep step-18's `ok-verbatim`-only
   gate** (D7); transform entries go to `entries_no_hash`. Revisit if a real transform manifest
   carries `source.hash` (the source-byte recipe is identical, so extension is a one-line
   gate change later).
5. **Fence-detection fidelity** — **lean: toggle on a line whose first non-space run (≤3
   leading spaces) is ≥3 backticks or ≥3 tildes**; sufficient for real docs (info strings and
   exact fence-length matching are not needed for a heading shifter). Note the simplification
   in the doc-comment.
6. **Classification token / reason strings** (`ok-transform`, `unsupported-transform:<type>`)
   — **lean: as in D6**; cosmetic, settle in code review.
7. **`skip_first` scope** — **lean: the first ATX heading document-wide (outside fences)** is
   left unchanged; offset applies from the second heading on (matches LDS "useful for document
   titles").
