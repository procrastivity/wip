# 0016 — closeout-write contract (the `ship` verb)

- Status: accepted
- Date: 2026-06-27
- Source: `closeout-write-completion` initiative, Round 1 (step-01); BRIEF.md; ADR-0001, ADR-0010

## Context

Closing out a shipped step touches **two** artifacts: the roadmap's `✅ shipped <date>`
bullet marker and the manifest's `active_step` pointer. Today both writes are manual, so
status/next can nominate already-shipped steps and the roadmap drifts from reality. The
`closeout-write-completion` initiative adds a deterministic state-writer to do both in one
invocation. step-01 is the pre-lane prereq: it locks the contract (this ADR) and scaffolds
the verb so the two downstream writer lanes — step-02 (roadmap marker) and step-03
(`active_step` clear) — build against a fixed seam in their own non-conflicting lib files.

Open questions the BRIEF left for this step: the verb name (`ship` vs `step ship` vs
`roadmap ship`), the step-vs-round boundary, and whether the writer gates on disk/git
state. They are resolved below.

## Decision

Add a top-level deterministic plumbing verb:

```
wip-plumbing ship <slug> <step-id> [--dry-run]
```

- **Top-level verb, not a sub-namespace.** `ship` spans **both** artifacts (roadmap marker
  **and** manifest pointer), so binding it under `roadmap ship` or `step ship` would
  misrepresent its scope. A flat top-level verb matches `bin/wip-plumbing`'s dispatch table,
  and the roadmap/step-04 already name it "the `ship` verb" / "one `ship` invocation".

- **Resolution mirrors `workplan init` exactly.** Resolve the initiative from `.wip.yaml`
  (`wip_find_root` + `wip_manifest_json`), resolve the `roadmap` path from the initiative
  record, `wip_roadmap_parse` it, and verify the step exists. Error codes mirror
  `workplan init`: missing `<slug>`/`<step-id>` → exit `2` (`usage`); unknown initiative →
  exit `3` (`unknown-initiative`); step not in roadmap → exit `4` (`step-not-in-roadmap`).
  Honors the global `--dry-run`. No `--date` flag in v1 (the date is a seam param, so a
  later step can add normalization control without a signature change).

- **Step-level ONLY.** `ship` operates on a single `<step-id>`: it writes that step's
  `✅ shipped <date>` bullet marker and clears `active_step` when (and only when) it points
  at that step. The Round `✅ Closed <date>` marker is **explicitly deferred** — round-level
  closeout stays a separate future write (roadmap "Deferred" + BRIEF open question).

- **Pure deterministic state-writer — no gating, idempotent.** `ship` does **not** inspect
  workplan-archived / git / disk state before writing (that drift detection is `doctor`'s
  job, a later step). It marks + clears unconditionally and idempotently: a re-run with
  nothing to change is a no-op (`changed: false`, exit `0`, identical ledger). Keep it dumb.

- **Two stub functions, in two distinct lib files (the lane seams).** Each writer lands in
  its own file so step-02 and step-03 never touch the same source:
  - **Stub A — roadmap marker writer (step-02 fills):**
    `_wip_ship_mark_roadmap_shipped <roadmap-path> <step-id> <date>` in
    **`lib/wip/wip-plumbing-ship-roadmap-lib.bash`**. step-02 reuses
    `_wip_roadmap_extract_shipped` (grammar) + `wip_amend_apply_replace` (in-place block
    rewrite) to insert/normalize the `✅ shipped <date>` marker.
  - **Stub B — `active_step` clearer (step-03 fills):**
    `_wip_ship_clear_active_step <manifest> <slug> <step-id>` in
    **`lib/wip/wip-plumbing-ship-manifest-lib.bash`**. step-03 mirrors
    `_wip_workplan_set_active_step` (same `yq -i` idiom) but **clears** the pointer, gated on
    the current value `== <step-id>`.

- **Stub return contract (the seam both lanes honor).** Each stub **prints** a status word
  to stdout and **returns 0**, or returns `1` on internal error:
  - Stub A prints `updated` | `noop` (`noop` = bullet already carries the correct
    `✅ shipped <date>`).
  - Stub B prints `updated` | `noop` | `skipped` (`noop` = `active_step` already unset;
    `skipped` = `active_step` points at a **different** step, left untouched — silently, per
    BRIEF "clears only when it points at the step being shipped"; never disturb another
    step's/initiative's pointer and never fail a closeout over it). This
    `updated`/`noop`/`skipped` vocabulary reuses the words `_wip_workplan_set_active_step`
    already prints — reused, not invented.
  - In **step-01** both stubs are inert seams: they perform **no** read and **no** write,
    return `noop` by default, and honor two test-injection env vars
    (`WIP_SHIP_FAKE_ROADMAP_STATUS`, `WIP_SHIP_FAKE_MANIFEST_STATUS`) so the harness's
    `updated`/`noop`/`skipped` aggregation is testable before the real writers land.
    step-02/03 replace the stub bodies **without changing the signature or the
    printed-status contract**.

- **JSON ledger shape (flat, mirrors `workplan init`'s envelope):**
  ```json
  {
    "ok": true,
    "slug": "<slug>",
    "step": "<step-id>",
    "shipped_date": "YYYY-MM-DD",
    "marked_shipped": "updated|noop",
    "active_step_cleared": "updated|noop|skipped",
    "changed": true,
    "dry_run": true
  }
  ```
  `marked_shipped` = stub A's printed status; `active_step_cleared` = stub B's printed
  status. `changed` = `true` iff either status is `updated`. `shipped_date` comes from
  `wip_scaffold_now` (honors `$WIP_NOW` in tests) and is the date the harness passes to
  stub A. The `dry_run` key is present **only** under `--dry-run`.

- **Sourcing/dispatch wiring lives entirely in step-01.** step-01 adds `ship` to the
  dispatch `case` in `bin/wip-plumbing` **and** sources both new ship libs there (next to
  the existing `roadmap`/`amend` source lines). This keeps every shared-file edit in step-01
  so step-02 and step-03 each touch **only** their own lib file + their own test — zero
  cross-lane file contention.

## Consequences

- Closeout becomes one deterministic invocation that keeps the roadmap marker and the
  `active_step` pointer in sync, removing the manual-write gap that let status/next nominate
  already-shipped steps.
- The two-stub/two-file seam lets step-02 and step-03 build the real writers in parallel
  without touching shared source; the printed-status contract is the only coupling, and it
  is fixed here.
- `ship` is deliberately un-gated: it never refuses based on git/disk drift, so it composes
  cleanly with a future `doctor` that owns drift detection. The cost is that `ship` will
  happily (re-)mark a step the caller named; idempotency keeps that harmless.
- Round-level `✅ Closed` closeout is out of scope and remains a future write; this ADR does
  not constrain its shape.
- New files: `lib/wip/wip-plumbing-subcommands/ship.bash`,
  `lib/wip/wip-plumbing-ship-roadmap-lib.bash`,
  `lib/wip/wip-plumbing-ship-manifest-lib.bash`, plus dispatch + source wiring in
  `bin/wip-plumbing` and `test/test-ship-skeleton.sh`.

## Implementation refinements

_Post-acceptance notes from the lanes that landed against this contract._

- **Stub A marker insertion (step-02, commit `b78ce12`) splices directly rather than via
  `wip_amend_apply_replace`.** The Decision above names `wip_amend_apply_replace` as the
  reuse helper, but in practice that helper structurally writes `<bullet>\n<marker>\n`: with
  an empty marker it injects a stray blank line, and with a real marker a
  `<!-- wip-amend: SHA -->` comment that is alien to `ship` and inconsistent with step-01's
  clean manual marking. step-02 instead splices the bullet's first line **in place** via the
  same lib family's block-boundary helpers (`_wip_amend_find_step_block_start` /
  `_wip_amend_find_step_block_end`), preserving continuation lines verbatim and injecting no
  marker line — same reuse spirit, clean output. The locked signature and printed-status
  contract (`updated`/`noop`) are unchanged; the reasoning is captured in a code comment in
  `lib/wip/wip-plumbing-ship-roadmap-lib.bash`.
- **The step-01 skeleton harness (`test/test-ship-skeleton.sh`) was retired to a
  writer-agnostic plumbing test (commit `87241d8`).** Its `WIP_SHIP_FAKE_{ROADMAP,MANIFEST}_STATUS`
  shim and inert-stub value assertions only held while both stubs were inert; the moment a
  real writer landed they broke. A shared pre-step (owned by neither lane) stripped the
  writer-coupled assertions, leaving dispatch / argparse / error-code / ledger-shape /
  dry-run coverage. Real writer behaviour, status aggregation, and idempotency now live in
  the per-lane tests (`test-ship-roadmap-writer.sh`, `test-ship-manifest-writer.sh`) and,
  for the composed end-to-end path, step-04.
