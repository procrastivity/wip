# Workplan — step-21 · LDS scaffold layer-6 naming

Roadmap: Round 5, effort `small`. Align the `setup lds` scaffold to the
**canonical wip LDS layer set**: rename the layer-6 scaffold directory
`features/` → `behaviors/` so the deterministic scaffold matches the
canonical naming locked by step-16 (`templates/glossary/lds.md`), xcind
ADR-0011, and the playbook DOCUMENTATION-GUIDE. Behavior is unchanged
except the layer-6 directory name; this does not land or extend a spec,
it removes a naming drift the scaffold inherited from the older upstream
LDS distribution.

Started: 2026-06-19.

## Canonical layer set (confirmed)

The canonical 7 layers, in order, with layer 6 = `behaviors/`:

| Layer | Directory |
|-------|-----------|
| 1 Decisions | `decisions/` |
| 2 Vision | `product/` |
| 3 Architecture | `architecture/` |
| 4 Specifications | `specs/` |
| 5 Reference | `reference/` |
| 6 Behaviors | `behaviors/`  ← scaffold currently ships `features/` |
| 7 Implementation | `implementation/` |

Cross-referenced and agreeing on `behaviors/`:

- `templates/glossary/lds.md:31` (step-16 canon) — `| 6 Behaviors | behaviors/ |`
- `xcind/engineering/decisions/0011-layered-documentation-system.md:22` — `| 6. Behaviors | behaviors/ |`
- `playbook/engineering/DOCUMENTATION-GUIDE.md:100` — `### Layer 6 — Behaviors (behaviors/)`

The **only** source naming the dir `features/` is the vendored upstream
`layered-documentation-system/LAYERED-DOCUMENTATION-SYSTEM.md:228`
(`| 6 | Behaviors | Gherkin, Feature Files | features/ |`). The scaffold's
`engineering/features/.gitkeep` follows that older upstream convention;
wip canon standardized on `behaviors/`. The rename closes that gap.

`appendices/` (offload dir) and `maintenance/` (workflow docs) are part of
the scaffold tree but are **not** layers; they are unaffected.

## Decisions (made here, feed later steps)

- **D1 — Rename target is `behaviors/`, replacing `features/` outright.**
  No dual-naming. The canonical set is exactly the 7 layers above with
  `behaviors/` at 6. Every wip-owned site that names the layer-6 dir
  changes from `features` to `behaviors`.
- **D2 — Exactly three wip-owned sites carry the layer-6 dir name** and
  all three change together (no dangling `features/` as a layer):
  1. `templates/setup/lds/engineering/features/.gitkeep` (the scaffold dir)
  2. `lib/wip/wip-plumbing-graduate-lib.bash:18` — `WIP_GRADUATE_LAYERS`
     allowlist (load-bearing: `graduate` exits 4 `unknown-layer` for any
     first path segment not in this set)
  3. `test/test-setup.sh:226` — the layer-dir assertion loop
- **D3 — No `.lds-manifest.yaml` `layers:` rename needed.** The seed
  manifest (`templates/setup/lds/engineering/.lds-manifest.yaml`) contains
  only `metadata` + `entries: []`; it does **not** enumerate layer dirs.
  Nothing to change there. (Resolves open Q2.)
- **D4 — No back-compat / migration shim.** `templates/setup/lds/` is the
  distribution source; fix the template, ship the new name. (Resolves
  open Q3.) Consumers re-run `setup lds`; this verb is idempotent and
  there is no live consumer install to migrate inside this repo.
- **D5 — Do NOT touch the byte-pinned maintenance docs.** The four
  `templates/setup/lds/engineering/maintenance/{audit,refine,sync,update}.md`
  files reference `features/` (e.g. `cucumber-js features/`,
  `features/{feature}.feature`) but those are the upstream cucumber
  **runner / feature-file** convention, a different concept from the wip
  LDS layer-6 doc dir. They are also **byte-pinned** to the vendored
  `layered-documentation-system/maintenance/*.md` via `assert_cmp`
  (`test/test-setup.sh:196-200`). Editing them would break the
  byte-equality guard. They stay as-is. (See open Q1 for the accepted
  residual divergence.)
- **D6 — File count stays 13.** The rename is 1:1 (one `.gitkeep` keeps
  its place), so `setup lds` still writes 13 files; the `wrote 13` /
  `skipped 13` / dry-run `13` assertions are unchanged. Doctor drift
  stays `0` because template and installed tree rename in lockstep.

## Chunks

Small step; can land as **one focused commit** (rename + the two
in-lockstep reference updates must move together to keep `make check`
green). Kept as one chunk on purpose — splitting would leave an
intermediate state with a dangling `features/` reference and a red
`make check`.

1. **Rename layer-6 scaffold dir and its two wip-owned references.**
   - `git mv templates/setup/lds/engineering/features/ templates/setup/lds/engineering/behaviors/`
     (carries the `.gitkeep`).
   - `lib/wip/wip-plumbing-graduate-lib.bash:18` — in `WIP_GRADUATE_LAYERS`
     replace the `features` token with `behaviors` (keep order; the line
     becomes `decisions product architecture specs reference behaviors implementation maintenance appendices`).
   - `test/test-setup.sh:226` — in the `for layer in …` loop replace
     `features` with `behaviors`.
   - Run `make check`; update nothing else.

## Test strategy

- **`make check` is the gate**; `test/test-setup.sh` is the load-bearing
  suite for this change.
- **Required-not-regression edits** (deliberately changed expected
  values, not flaky breakage):
  - `test/test-setup.sh:226` layer loop — assert `behaviors/.gitkeep`
    present, not `features/.gitkeep`. This is the dogfood case that
    checks the written LDS tree; the edit is the point of the step.
- **Assertions that must stay green unchanged** (prove behavior is
  otherwise identical):
  - `test/test-setup.sh:219` `[lds] wrote 13 files`, `:235` `skipped 13`,
    `:310` dry-run `13` — count unaffected by a 1:1 rename.
  - `test/test-setup.sh:196-200` maintenance byte-equality (`assert_cmp`
    vs `layered-documentation-system/maintenance/*.md`) — untouched
    because D5 leaves those files alone.
  - `test/test-setup.sh:248` `[lds] doctor drift 0` — template and
    installed tree rename together, so doctor still sees zero drift.
  - `test/test-graduate.sh` — the unknown-layer case (`:216-227`) uses a
    typo (`decisons/`), not `features/`, so the `WIP_GRADUATE_LAYERS`
    edit does not perturb it; the dogfood graduate-to-`decisions/`
    assertions are unaffected.
- No new test is required; the existing dogfood tree assertion already
  exercises the renamed dir once line 226 is updated. (Optional nicety,
  not required: an `assert_absent .../features/.gitkeep` line to prove the
  old name is gone — call out only if the reviewer wants belt-and-braces.)

## Definition of done

- `setup lds` writes `engineering/behaviors/.gitkeep` and **no**
  `engineering/features/.gitkeep`; full install still writes 13 files,
  re-run skips 13, doctor reports drift 0.
- `graduate` accepts a `graduate-to: behaviors/<file>.md` target and
  rejects `features/<file>.md` with exit 4 `unknown-layer` (the allowlist
  now lists `behaviors`, not `features`).
- `grep -rn 'features/' lib/ templates/setup/lds/ test/test-setup.sh`
  shows **no** match referring to the LDS layer-6 dir (only the
  byte-pinned maintenance-doc cucumber `features/` paths remain, per D5).
- `make check` is green.

## Open questions to resolve during execution

- **Q1 — Residual divergence: byte-pinned maintenance docs still say
  `features/`.** After the rename the scaffold ships `behaviors/` while the
  vendored maintenance docs (`update.md`, `sync.md`, `refine.md`) still
  instruct authors to write `features/{feature}.feature`.
  **Lean:** accept it for this small step — those `features/` references
  are the upstream cucumber runner-dir convention (a different concept)
  and are byte-locked to `layered-documentation-system/` by an `assert_cmp`
  guard. Reconciling them is a separate, larger "re-vendor / fork the LDS
  maintenance docs to wip-canon naming" effort, out of scope here. Note it
  in the commit body so it is a known, tracked divergence, not a silent
  one.
- **Q2 — Does the seed `.lds-manifest.yaml` need a `layers:` rename?**
  **Lean / resolved:** No — the seed manifest has only `metadata` +
  `entries: []`, no layer enumeration (see D3). Verified in
  `templates/setup/lds/engineering/.lds-manifest.yaml`.
- **Q3 — Back-compat for an existing consumer that already ran
  `setup lds`?** **Lean / resolved:** No shim (see D4). This repo is the
  distribution source; fix the template. `setup lds` is idempotent, so a
  consumer re-runs it; there is no in-repo live install to migrate.
- **Q4 — Should `WIP_GRADUATE_LAYERS` keep `features` as a back-compat
  alias alongside `behaviors`?** **Lean:** No — replace, don't augment.
  Keeping both permanently enshrines the wrong name and lets a stale
  `graduate-to: features/…` silently succeed. The allowlist is the
  canonical set; it should be exactly the 7 layers (+ `maintenance` +
  `appendices`) with `behaviors` at 6. The consumer-facing change (a
  `features/` target now errors) is deliberate and required-not-regression.
