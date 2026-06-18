# Workplan — step-16 · `glossary` LDS partial

Ship `templates/glossary/lds.md`, the LDS glossary partial — the last
unshipped "future row" in the layered-vocabulary system from
[ADR-0003](../../../engineering/decisions/0003-layered-opt-in-vocabulary.md).
Roadmap Round 4 ("Extract polish & LDS completion"), step-16 (xs).

The inclusion machinery already exists: `wip_glossary_rules` in
`lib/wip/wip-plumbing-glossary-lib.bash:19` declares the row
`lds.md  features.lds.enabled  .features.lds.enabled == true`, and the
graceful-skip path (predicate true but body not on disk →
`partials_skipped[]`, no divider emitted) is built and tested. This step
ships **only the partial's bytes**. No change to the glossary lib, the
glossary subcommand, or the rule table is expected.

The partial owns the vocabulary the README assigns it
(`templates/glossary/README.md:11`): *"LDS terms + the LDS graduation
mechanism."* It must read as a sibling of `core.md` / `orchestration.md`
/ `solo.md` and assemble cleanly behind `solo.md` in declaration order.

Started: 2026-06-17.

## Decisions (made here, feed later steps)

- **Scope = vocabulary, not the LDS manual.** `lds.md` defines *terms*
  in glossary house style (term tables + short prose), exactly as the
  sibling partials do. Exhaustive layer mechanics, classification trees,
  and rationale stay in the LDS docs (`DOCUMENTATION-GUIDE.md`) and the
  LDS ADR — the partial may name them but does not reproduce them. This
  keeps the step "xs."

- **Canonical layer set = the seven LDS layers.** Authoritative wip-owned
  source is the dogfooded playbook install (`playbook/engineering/
  DOCUMENTATION-GUIDE.md`, "LDS v3.0") and xcind ADR-0011, which agree:
  1 Decisions (`decisions/`), 2 Vision (`product/`), 3 Architecture
  (`architecture/`), 4 Specifications (`specs/`), 5 Reference
  (`reference/`), 6 Behaviors (`behaviors/`), 7 Implementation
  (`implementation/`). The `features/` directory present in
  `templates/setup/lds/engineering/` (in place of `behaviors/`) is a
  scaffold divergence, **not** canonical layer naming — do not let it
  drive the glossary. See Open question 1.

- **Terms the partial owns** (no duplication of `core.md`, which already
  defines Feature / Sentinel / Detection contract / Graduation-concept):
  - **Layered Documentation System (LDS)** — framework that organizes a
    project's engineering docs into ordered layers.
  - **Layer** — a documentation category with a defined purpose,
    stability, and audience; LDS defines seven (table above).
  - **eng docs root** (`ENG_DOCS_DIR` / `DOCS_DIR`) — the single root the
    LDS tree lives under (`engineering/` or `docs/`); scalar single root
    in v1 (monorepo-plural deferred, per `.wip.yaml` comments).
  - **`.lds-manifest.yaml`** — the LDS **Sentinel** (ties to core's
    Detection contract): its existence at `{root}/.lds-manifest.yaml`
    proves LDS is installed; it also pins the extraction list.
  - **ADR** — Architecture/Architectural Decision Record; an immutable
    Layer 1 document (context / decision / consequences).
  - **Appendix** — large content offloaded to `{layer}/appendices/
    {topic}/`, keeping main docs scannable.
  - **Drift** (LDS sense) — documentation and implementation out of sync;
    distinct from core's stanza/sentinel detection-drift. Worth one
    clarifying clause so the two "drift" senses don't collide.

- **The "LDS graduation mechanism" = Extract.** `lds.md` binds core's
  abstract **Graduation** verb to its LDS-specific realization:
  `wip-plumbing extract` runs the deterministic LDS Extract phase against
  an approved **extraction manifest** (`.lds-manifest.yaml`'s `entries[]`),
  promoting durable knowledge from `.wip/` into the LDS layer tree. Per
  ADR-0006 the deterministic extract is plumbing; the analyze/review
  phases that author the manifest are porcelain. This is the partial's
  second section and satisfies the README's "graduation mechanism" charge.

- **House style (verified against the three shipped partials).** The
  Builder MUST match these exactly:
  - First bytes are a leading HTML comment block — the assembler strips
    it (`wip_glossary_strip_header`: skip leading blanks → first
    contiguous `<!-- … -->` block → trailing blanks → emit rest). Format
    mirrors siblings:
    `<!-- wip glossary partial: LDS (Layered Documentation System). Included ONLY when features.lds.enabled is true in .wip.yaml. … A consumer not using LDS never sees these terms. -->`
  - **No H1** in the partial. Top-level sections are `##` (the assembler
    emits the document H1). Use two `##` sections: the LDS terms section
    and the Graduation/Extract section.
  - Term definitions are markdown tables: `| Term | Definition |` with a
    `|------|------------|` rule, terms in `**bold**`. A layer-listing
    table (`| Layer | Directory | Purpose |` or similar) is appropriate
    for the seven layers, matching `core.md`'s Collections/verbs tables.
  - Close by tying back to `core.md` (as `core.md` and `solo.md` do with
    a `>` blockquote or bold-lead paragraph), e.g. noting Graduation is
    the core concept and Extract is its LDS binding.
  - Cross-reference siblings/ADRs in prose, not by copying their content.

- **No lib/test-of-lib changes; no GLOSSARY.md regen in this repo.** This
  repo sets `features.lds.enabled: false` (it is the LDS *distribution
  source*, not an install — `.wip.yaml`), so the predicate is false here
  and `lds.md` is never assembled into this repo's `.wip/GLOSSARY.md`.
  Shipping the file therefore does not change `.wip/GLOSSARY.md` and does
  not trip the pre-commit drift guard (`glossary check`). Verification of
  *inclusion* must happen in a synthetic repo with `lds.enabled: true`
  and `lds.md` copied in (see Test strategy).

## Chunks

Single chunk — this is an xs, single-file content step.

1. **Author `templates/glossary/lds.md`** and add an inclusion test case.
   - Write the partial per the Decisions above: leading stripped comment
     header, `## Layered Documentation System (LDS)` (LDS / Layer /
     seven-layer table / eng docs root / `.lds-manifest.yaml` sentinel /
     ADR / Appendix / Drift), `## Graduation (LDS mechanism)` (Extract +
     extraction manifest, binding core's Graduation), closing tie-back.
   - Add a positive-inclusion test to `test/test-glossary.sh` (see Test
     strategy) so the new partial is actually exercised — the existing
     suite only ships core/orchestration/solo into its tmp repos.
   - Run `make check` (or the repo's shell gates: shfmt/shellcheck/
     `bin/test`) and confirm green before completing.

## Test strategy

- **Existing suite stays green, unchanged in spirit.** `make_tmp_repo` in
  `test/test-glossary.sh` copies only core/orchestration/solo into its
  tmp templates dir, so existing cases are unaffected by a new
  `templates/glossary/lds.md`. The future-row graceful-skip case
  (section 5) deliberately uses `diataxis` (still unshipped) — leave it;
  do not repoint it at `lds`.
- **Add a positive lds-inclusion case** modeled on the assemble + flip
  cases (sections 3–4). It must: copy `lds.md` into the tmp templates
  dir, set `lds_enabled=true` in the manifest, run
  `bin/wip-plumbing glossary assemble`, then assert:
  - the `lds.md` body sentinel section heading appears
    (`^## Layered Documentation System`), and the Graduation/Extract
    section appears;
  - the partial divider `^<!-- partial: lds\.md ` is emitted;
  - **ordering**: the lds divider line number is greater than the solo
    divider's (declaration order core < orchestration < solo < lds);
  - the partial-author comment header is **stripped** (the
    `wip glossary partial: LDS` marker MUST NOT appear in output);
  - `Driven by:` / `Source:` reflect lds when present (optional, nice).
  - Consider extending `make_tmp_repo` to copy `lds.md` (and accept it via
    the existing `lds_enabled` arg) rather than copying ad hoc — Builder's
    call; either is fine.
- **No new lib unit tests** — `wip_glossary_rules`/`_resolve`/`_render`
  are unchanged; the rule-table assertion (section 1) already lists the
  lds row.
- **Manual smoke (optional, fast):** in a scratch dir with
  `lds.enabled: true` + the new partial, `glossary assemble` then
  `glossary check` should report `ok:true, drift:false`.

## Definition of done

- `templates/glossary/lds.md` exists, opens with a strippable
  `<!-- wip glossary partial: LDS … -->` header, has no H1, uses `##`
  sections, and defines the LDS vocabulary + the Extract graduation
  mechanism in glossary house style.
- With `features.lds.enabled: true` and the partial on disk,
  `wip-plumbing glossary assemble` emits the `lds.md` divider and body
  **after** solo, the author comment header is stripped, and
  `glossary check` reports `ok:true, drift:false`.
- A test in `test/test-glossary.sh` asserts the above inclusion +
  ordering + strip behavior and passes.
- `bin/wip-plumbing glossary check` against **this** repo still exits 0
  with `drift:false` (lds disabled here → output unchanged; pre-commit
  guard green).
- Shell gates pass (`make check` / shfmt / shellcheck / full test suite).
- The glossary lib (`wip-plumbing-glossary-lib.bash`) and subcommand are
  **untouched**.

## Open questions to resolve during execution

- **Q1 — Layer 6 naming: Behaviors vs Features.** The dogfooded playbook
  install and xcind ADR-0011 say **Behaviors** (`behaviors/`); the
  `templates/setup/lds/engineering/` scaffold ships `features/` instead.
  *Lean:* use the canonical **seven layers with Behaviors at 6** — the two
  authoritative LDS sources agree, and the glossary defines canonical
  vocabulary, not the scaffold's current dir list. Flag the `features/`
  scaffold divergence to the Coordinator as a possible separate cleanup;
  do **not** resolve it inside this partial.

- **Q2 — How much of the Extract mechanism to surface.** Extract has v1
  limits (verbatim/content modes only; transform/summarize + multi-file
  land in `unsupported[]`; hash verification skipped — see
  `lib/wip/wip-plumbing-subcommands/extract.bash`). *Lean:* the glossary
  defines the *term* and the graduation relationship (core Graduation →
  LDS Extract against an approved extraction manifest), not the v1 status
  matrix. Name `wip extract` and the extraction manifest; defer v1 caveats
  to the extract spec/ADR-0006.

- **Q3 — Include `Drift` given core has its own drift sense?** Core's
  Detection contract defines stanza/sentinel drift; LDS drift is
  docs-vs-implementation. *Lean:* include LDS **Drift** with a half-clause
  distinguishing it from detection-drift, so a reader assembling both
  partials isn't confused. Drop it only if it reads redundant.

- **Q4 — Enumerate all seven layers, or name-and-defer?** *Lean:* a
  compact `| Layer | Directory | Purpose |` table for all seven (one
  terse purpose phrase each), matching `core.md`'s table density; defer
  stability/audience/classification detail to the LDS DOCUMENTATION-GUIDE.
  The table is the vocabulary; the guide is the manual.
