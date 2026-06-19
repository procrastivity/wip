# Workplan — step-17 · `extract` extraction report

Anchors:

- **Roadmap bullet** — `.wip/initiatives/distillation/roadmap.md:64`:
  _"step-17 — `extract` extraction report (small) — Write
  `extraction-report.{md,yaml}` to disk per LDS §7 (metadata + summary +
  per-entry rows). The data already exists in the stdout ledger; serialize
  it."_ (was backlog `extract-extraction-report`.)
- **LDS §7** — `layered-documentation-system/extract.md:851-1082`
  ("Generate Extraction Report"): §7.1 contents, §7.2 YAML shape + write
  location `{ENG_DOCS_DIR}/extraction-report.yaml`, §7.3 "report even on
  partial failure", §7.4 human-readable terminal summary.
- **ADR-0006** — `engineering/decisions/0006-wip-owns-seams-not-tools.md`:
  `wip` owns the LDS seam; `extract` is the deterministic core. This step
  brings the seam into §7 conformance without reimplementing LDS analysis.
- **CLI spec** — `engineering/specs/wip-plumbing-cli.md:796-921` (extract
  verb). Line 824 today reads
  `| Extraction report file | **not written** (ledger is stdout-only) |`.
  This step closes that gap; the spec line must flip and the report shape
  documented.
- **step-15 deferral** — `step-15-graduate-extract.md:756-761`: the written
  report was deliberately deferred to this backlog item.

Started: 2026-06-17.

## The core problem (read before the Decisions)

LDS §7's report schema is **richer than what v1 `extract` computes**. The
ledger the command already builds carries:
`wrote[] / skipped_idempotent[] / wrote_forced[] / refused[] /
unsupported[] / bad_entries[] / hash_verification:"skipped-v1"`
(`wip-plumbing-cli.md:879-895`, built in `extract.bash:115-256`).

§7's YAML wants `summary.{successful,failed,skipped}`, `line_statistics`,
`layer_breakdown` line totals, `verification_results.content_hash_check`
(real hash verification), `manifest_hash`, `source_changes`. v1 **does not
track source/output line counts and explicitly skips source-hash
verification** (`extract-lib.bash:1-17`).

The roadmap mandate is _"serialize"_, not _"compute"_. So the governing
rule for this step is **faithful-subset serialization**: render the §7
structure, populate every field derivable from the existing ledger plus a
cheap filesystem stat, set genuinely-unavailable fields to `null` (or the
self-documenting `"skipped-v1"` status the ledger already uses), and
**never fabricate numbers**. This is additive — it does not touch extract's
existing stdout envelope, its extraction semantics, or its exit codes.

## Decisions (made here, feed later steps)

- **Write location is `<eng-docs>/extraction-report.{yaml,md}`** — fixed
  filenames at the eng-docs root, exactly per LDS §7.2
  (`extract.md:909`). `<eng-docs>` resolves via the existing
  `wip_extract_lds_root` (default `engineering`), the same root extract
  already uses for targets. No subdir, no configurable path in v1.
- **Both `.yaml` and `.md` are written** (the roadmap says `{md,yaml}`).
  `.yaml` is the machine-readable §7.2 structure; `.md` is the §7.4
  human-readable summary rendered to a file. The §7 spec only *mandates*
  the YAML and describes §7.4 as terminal output — we satisfy the roadmap's
  `{md,yaml}` by writing §7.4's layout to `extraction-report.md` rather
  than (or in addition to) printing it.
- **The report is always written, including on partial failure** (§7.3),
  but **honors `--dry-run`** (`WIP_DRY_RUN=1` → write nothing, consistent
  with the global flag's "touch nothing" contract). On the `ok:false`
  drift/bad-shape path the report is written **before** `exit 4`.
- **The report write is a plain overwrite, NOT three-way idempotent.** The
  report embeds a fresh `executed_at` timestamp, so it differs every run by
  construction. It must **bypass `wip_setup_write_idempotent`** — routing
  it through that helper would make every second run report a spurious
  `content-drift` on the report file itself. Extracted *targets* keep their
  three-way idempotency; only the report file is exempt.
- **The existing stdout JSON envelope and the one-line stderr summary are
  unchanged.** The report is a new side artifact, not a change to either
  stream. No new fields in the stdout ledger.
- **Ledger → §7 vocabulary mapping** (locked so the builder isn't
  re-deriving it):
  | §7 field | v1 source |
  |---|---|
  | `summary.successful` | `len(wrote) + len(wrote_forced)` |
  | `summary.failed` | `len(refused) + len(bad_entries)` |
  | `summary.skipped` | `len(skipped_idempotent)` (repurposed from §7's `--resume` meaning; document inline) |
  | `summary.unsupported` *(added field)* | `len(unsupported)` |
  | `summary.total_entries` | `entries_total` |
  | `files_created[]` | one row per `wrote`/`wrote_forced` (status `success`), `refused` (status `failed`, error `content-drift`), `bad_entries` (status `failed`, error=reason) |
  | `files_created[].status` enum | extend with `unsupported` for `unsupported[]` rows, OR keep them in a sibling `unsupported[]` block — see OQ5 lean |
  | `verification_results.content_hash_check.status` | `"skipped-v1"` (mirrors ledger `hash_verification`) |
  | `verification_results.line_count_check.status` | `"skipped-v1"` (line counts not tracked) |
  | `verification_results.file_existence_check` | computed live — stat each `wrote`/`wrote_forced` target |
  | `line_statistics` | `null` / omitted (not tracked in v1) |
  | `layer_breakdown.<layer>.files_created` | count target path prefixes (cheap); `total_lines` → `null` |
  | `metadata.manifest_file` | `$mpath` (already in ledger as `manifest`) |
  | `metadata.executed_at` | `date -u +%Y-%m-%dT%H:%M:%SZ` (pattern from `_wip_iso_now`, `registry-lib.bash:36`) |
  | `metadata.flags.force` / `.resume` | `force` / `false` (resume not implemented) |
  | `metadata.manifest_hash` | see OQ — lean compute via `shasum -a 256` |
  | `source_changes` | `{detected:false, files_changed:[], force_flag_used:<force>}` (source-hash detection is skipped-v1) |
- **Self-documenting v1 limits:** unavailable numeric fields are `null` and
  the relevant verification checks carry `status:"skipped-v1"`, so a reader
  can tell "not computed" from "computed as zero". No invented values.

## Chunks

Each chunk is one focused commit.

1. **Report renderers in `wip-plumbing-extract-lib.bash` (pure, no I/O).**
   Add two functions that take the already-computed ledger data and emit
   report text to stdout:
   - `wip_extract_report_yaml` — builds the §7.2 object with `jq` (same
     style the command already uses to build its stdout JSON), then pipes
     through `yq -P -o=yaml` to produce `extraction_report:` YAML. Inputs:
     manifest path, `entries_total`, the four target arrays, the
     `unsupported_json` / `bad_json` blobs, `force`, `executed_at`,
     `manifest_hash`, and a per-target layer/existence map. No file writes.
   - `wip_extract_report_md` — renders the §7.4 layout (FILES CREATED /
     LAYER SUMMARY / SUMMARY / VERIFICATION / `Status:` line) from the same
     inputs. The final `Status:` line is `COMPLETED` when `ok:true`,
     `COMPLETED WITH ERRORS` otherwise (matches §7.4's example).
   Keep these functions side-effect-free so they're unit-testable and so
   the command layer owns all filesystem writes.

2. **Wire report emission into `extract.bash`.** After the ledger arrays
   are finalized (after the entry loop, ~line 210) and the `ok` / `err_*`
   values are computed (~line 214-225), but in **both** the `ok:true` and
   `ok:false` branches:
   - compute `executed_at` and (per OQ) `manifest_hash`;
   - unless `WIP_DRY_RUN=1`, write `$eng/extraction-report.yaml` and
     `$eng/extraction-report.md` via a **plain overwrite** (`printf > file`
     / `mktemp` + `mv`), explicitly **not** `wip_setup_write_idempotent`;
   - place the write before the `exit 4` so §7.3 (report on partial
     failure) holds.
   The stdout JSON emit and stderr one-liner stay byte-for-byte as they are.

3. **Spec update (`engineering/specs/wip-plumbing-cli.md`).** Flip line 824
   from `**not written** (ledger is stdout-only)` to written, and add a
   short subsection documenting: the two report files + location, that the
   report is always written (honoring `--dry-run`) including on partial
   failure, the plain-overwrite (non-idempotent) behavior, and the
   field-availability caveats (`line_statistics`/`layer_breakdown.total_lines`
   = null, hash/line-count checks = `skipped-v1`). Update the **Writes:**
   bullet (line 863) to mention the report files.

4. **Tests in `test/test-extract.sh`** (own commit; see Test strategy).

## Test strategy

Extend `test/test-extract.sh` — **reuse its existing manifest fixtures**;
do not build new consumer roots. Helpers `build_lds_enabled_root`,
`write_manifest`, `assert_file`, `assert_grep`, `assert_absent`, and `yq`
are already in scope.

- **Happy path (fixture c1, verbatim+content).** After the existing run,
  assert `assert_file "$d1/engineering/extraction-report.yaml"` and
  `…/extraction-report.md`. Parse the YAML with `yq` and assert it
  reconciles with the stdout ledger:
  `summary.total_entries == 2`, `summary.successful == 2`,
  `summary.failed == 0`, `summary.skipped == 0`; that `files_created[]` has
  rows for both targets with `status: success`; that
  `verification_results.content_hash_check.status == "skipped-v1"`.
- **Unsupported (fixture c2).** Assert the two transform/summarize entries
  appear in the report's unsupported representation (per OQ5 lean) with
  their reasons, and that `summary.successful == 1`.
- **Multi-file unsupported (fixture c3).** Assert the multi-file entry is
  represented as unsupported in the report (not as a created file).
- **Idempotent re-run (fixture c7).** Run twice; assert the **report is
  regenerated, not refused** (i.e. no `content-drift` on the report file),
  `summary.skipped == 1`, `summary.successful == 0` on the second run.
  This is the regression guard for the "report bypasses idempotency"
  decision.
- **Partial failure — content drift (fixture c8).** After tampering and
  the `exit 4` drift run, assert the report file **still exists** and its
  `summary.failed >= 1` / the drifted target shows `status: failed` (§7.3).
- **Partial failure — bad shape (fixture c12).** Same: report written
  despite `exit 4`, bad entry represented as failed/error.
- **Dry-run.** Add one case invoking `bin/wip-plumbing --dry-run extract`
  against a c1-style fixture; assert **no** `extraction-report.{yaml,md}`
  is written (and no targets), consistent with the global flag.
- **Non-determinism guard.** Never assert the exact `executed_at` value —
  only that the field is present and ISO-8601-shaped. Likewise treat
  `manifest_hash` as presence-only (or null per OQ).

Deferred coverage (with reason): no assertions on `line_statistics` or
`layer_breakdown.total_lines` values — they are intentionally `null` in v1;
assert only that the keys exist and are null/absent per the chosen shape.

## Definition of done

- Running `extract` against an approved manifest writes
  `<eng-docs>/extraction-report.yaml` **and** `extraction-report.md`; the
  YAML parses and its `summary` counts reconcile with the stdout ledger
  for the same run.
- The report is written on the `exit 4` partial-failure paths
  (drift / bad-shape), and is **not** written under `--dry-run`.
- Re-running `extract` on an unchanged tree regenerates the report without
  triggering `content-drift` on the report file; extracted targets remain
  three-way idempotent.
- The stdout JSON envelope and stderr one-liner are byte-identical to
  pre-step-17 behavior (no new ledger fields).
- `engineering/specs/wip-plumbing-cli.md` no longer says the report is "not
  written" and documents the report's shape, location, dry-run behavior,
  and v1 field-availability caveats.
- `test/test-extract.sh` covers the cases above; all prior extract
  assertions and every other suite stay green.

## Open questions to resolve during execution

Each carries a lean so the builder can proceed without blocking.

1. **`manifest_hash` — compute or null?** §7.2 includes it, but v1 skips
   *source*-hash verification. Hashing the *manifest file itself* is a
   different, cheap operation. **Lean: compute via `shasum -a 256` of the
   resolved manifest file** (present on macOS + Linux; one line). If
   `shasum` is unavailable, emit `null` rather than failing. It's genuine
   audit value at near-zero cost and does not touch the source-hash
   "skipped-v1" story.

2. **Always-on vs a flag (`--report` / `--no-report` / `--report-dir`).**
   §7 treats the report as unconditional. **Lean: always-on, no new flag in
   v1** — fewer surfaces, matches the spec, and `--dry-run` already gives
   the "don't write" escape hatch. Defer `--report-dir`/`--no-report` to
   backlog if a consumer asks. (If always-on writing a new file into the
   consumer's docs tree is judged too surprising during review, the
   fallback is `--report` opt-in — but the lean is always-on.)

3. **md vs yaml content split.** **Lean: `.yaml` = full §7.2 machine
   structure; `.md` = §7.4 human-readable layout** (FILES CREATED / LAYER
   SUMMARY / SUMMARY / VERIFICATION / `Status:`), rendered to file rather
   than printed. Rationale: the roadmap says `{md,yaml}`; the spec's only
   two report representations are exactly these two; the md is the
   audit-friendly digest, the yaml is the source of truth.

4. **`--dry-run` behavior.** **Lean: write nothing** (no report, no
   targets) — consistent with the global `--dry-run` "touch nothing"
   contract and with how `wip_setup_write_idempotent` already short-circuits
   under `WIP_DRY_RUN`. The stdout ledger already reflects *would-be*
   results, so a dry-run report would be redundant and would violate the
   flag's contract. (Note: extract's own arg parser does not yet accept a
   subcommand-level `--dry-run`; the flag is global on `bin/wip-plumbing`,
   so honoring `WIP_DRY_RUN` is the correct hook — no parser change needed.)

5. **How `unsupported[]` / `bad_entries[]` appear in the report.** §7's
   `files_created[]` status enum is `success|failed`, and it has separate
   top-level `warnings[]` / `errors[]`. **Lean:** represent
   `bad_entries`/`refused` as `files_created[]` rows with `status: failed`
   + an `error` string *and* mirror them into top-level `errors[]`;
   represent `unsupported[]` in a top-level `unsupported[]` block (mirroring
   the ledger's own field name verbatim) **and** in `summary.unsupported`,
   rather than overloading `files_created[].status`. Rationale: unsupported
   ≠ failed (the run still succeeds, `ok:true`), so keeping them out of
   `files_created` and out of `errors` preserves the success semantics while
   staying legible. Keeping the ledger field name (`unsupported`) verbatim
   makes the serialize-the-ledger intent obvious.

6. **Test strategy reuse.** Resolved in Test strategy above: reuse fixtures
   c1/c2/c3/c7/c8/c12 and add one dry-run case. **Lean: assert
   reconciliation between the on-disk YAML and the stdout ledger** (counts
   and per-target rows) rather than asserting hardcoded report text — this
   keeps the tests robust to cosmetic report-format tweaks while still
   proving the serialize-the-ledger contract.

7. **`layer_breakdown` / `line_statistics` altitude.** The §7 example
   populates per-layer line totals and source/output line stats. v1 tracks
   no line counts. **Lean: include `layer_breakdown.<layer>.files_created`
   (cheap — count target path prefixes) with `total_lines: null`, and emit
   `line_statistics` with all fields `null`** (keys present, values null),
   so the report is shape-complete and self-documents what v1 omits. Do
   **not** add line-counting to the extract loop — that would change
   extract's core and exceed this "small" step's mandate. If review prefers
   zero half-populated sections, the fallback is to omit `line_statistics`
   and `layer_breakdown` entirely; the lean is keys-present-with-null.
