# Workplan — step-18 · `extract --verify-hashes`

Anchors:

- **Roadmap bullet** — `.wip/initiatives/distillation/roadmap.md:65`:
  _"step-18 — `extract --verify-hashes` (small) — `--verify-hashes` flag
  enabling SHA-256 source hash verification (v1 ledger advertises
  `hash_verification: "skipped-v1"`). Requires `sha256sum`/`shasum` in the
  flake (add to `setup deps` template)."_ (was backlog
  `extract-verify-hashes`.) Third step of Round 4 ("Extract polish & LDS
  completion"); ordered after step-17's report so the report has a real
  `content_hash_check` to populate.
- **step-15 deferral** — `step-15-graduate-extract.md:164-180`: hash
  verification was explicitly cut from v1 (_"v1 parses hash fields … but does
  NOT compute or compare"_) with the seam point named: _"add to backlog with
  a clear seam point … `extract --verify-hashes` flag + sha256 dep"_
  (`:392-393`, `:609`). This step lands that seam.
- **LDS schema — source hashes** —
  `layered-documentation-system/schemas/extraction-manifest.schema.yaml`:
  - `source_spec.single_file_with_range.hash` (`:394-405`) — the per-entry
    `entries[].source.hash`, _"SHA-256 hash of the specified line range
    content."_
  - `source_hash_mismatch_handling.what_gets_hashed` (`:547-596`) — the three
    hash flavors (whole-file `sources.<path>.hash`, line-range
    `entries[].source.hash`, multi-file `combined_hash`) and how each is
    computed.
  - `source_hash_mismatch_handling.mismatch_detection` (`:620-696`) — verify
    **at the start, BEFORE any files are written**; missing file counts as a
    mismatch; _"CRITICAL PRINCIPLE: Never silently proceed when source has
    changed."_
- **CLI spec** — `engineering/specs/wip-plumbing-cli.md:796-958` (extract
  verb). Line 821 today reads
  `| SHA-256 source hash verification | **skipped-v1** (ledger flag) |`; the
  exit-4 kind list (`:912-914`) and the success-stdout `hash_verification`
  field (`:932`) must grow to cover the new flag.
- **ADR-0006** — `engineering/decisions/0006-wip-owns-seams-not-tools.md`:
  `wip` owns the LDS seam; `extract` is the deterministic core. Hash
  verification is deterministic (compute + compare), so it belongs in this
  verb — the LLM-driven hash *generation* (analyze phase) stays porcelain.
- **step-17 report renderers** — `wip-plumbing-extract-lib.bash:260-431`
  (pure `wip_extract_report_{yaml,md}`); emission wiring
  `extract.bash:259-314`. The report's
  `verification_results.content_hash_check` is hardcoded
  `{status:"skipped-v1"}` today (`extract-lib.bash:345`, `:427`) — this step
  makes it real when the flag is set.

Started: 2026-06-18.

## The core problem (read before the Decisions)

v1 `extract` already *parses* the manifest's source-hash fields but
deliberately neither computes nor compares them; every run advertises
`hash_verification: "skipped-v1"` (`extract.bash:243`) and the report's
`content_hash_check.status` is `"skipped-v1"` (`extract-lib.bash:345`). This
step makes the check **real, opt-in, and a pre-write gate** without disturbing
the default (no-flag) behavior, which must stay byte-identical to step-17.

Two facts shape every decision below:

1. **Only one hash flavor is reachable in v1.** The manifest declares hashes
   in three places (LDS schema §what_gets_hashed): the top-level
   `sources.<path>.hash` whole-file registry, the per-entry
   `entries[].source.hash` line-range hash, and the multi-file
   `entries[].source.combined_hash`. But v1 `extract` only *writes*
   `ok-verbatim` entries whose source is **simple-path (a string)** or
   **single-file (`{file, start_line?, end_line?, hash?}`)** — multi-file is
   already routed to `unsupported[]` (`extract-lib.bash:248`), and simple-path
   string sources have no slot to carry a hash. So the only verifiable,
   in-scope hash is **`entries[].source.hash` on single-file sources**.
   Whole-file `sources` registry and multi-file `combined_hash` are
   out of v1 scope (see OQ2).

2. **LDS mandates a pre-write gate, not a per-entry inline check.** The schema
   is emphatic (`:621-680`): hash verification happens at the *start* of the
   phase, *before any file is written*, and the tool must *never silently
   proceed*. So `--verify-hashes` adds a verification **pre-pass**: if every
   declared hash matches, the existing write loop runs untouched; if any
   mismatches (or a hashed source is missing), the run fails **before writing
   any target** with `exit 4 hash-mismatch` — but still writes the §7 report
   first (§7.3). This keeps the change surgical: the write loop is not
   modified at all.

The governing rule mirrors step-17's: **additive and honest**. No-flag runs
are unchanged; the flag turns `"skipped-v1"` into a genuine result and never
fabricates a verdict for an entry that carries no hash.

## Decisions (made here, feed later steps)

- **Opt-in `--verify-hashes` flag, parsed per-verb like `--force`.** Added to
  `extract.bash`'s arg loop; default off. With the flag off, the ledger keeps
  `hash_verification: "skipped-v1"` and the report keeps
  `content_hash_check.status: "skipped-v1"` — step-17 behavior, unchanged.
  Matches the advertised "skipped-v1" default and ADR-0006's "deterministic
  core" framing (the check is deterministic; only its *invocation* is
  opt-in).

- **Verified hash = `entries[].source.hash` on single-file sources only.**
  An entry is *verifiable* iff it classifies `ok-verbatim` **and** its
  `source` is the single-file object form **and** that object carries a
  non-empty `hash`. Everything else (simple-path string sources, single-file
  without `hash`, `content` mode, anything already in `unsupported[]`/
  `bad_entries[]`) is **not verifiable** and is recorded as `entries_no_hash`,
  never failed. The top-level `sources.<path>.hash` whole-file registry and
  multi-file `combined_hash` are **deferred** (OQ2) — out of scope for this
  small step.

- **What gets hashed: the extracted source-range bytes, excluding
  attribution.** The hash covers exactly the bytes `wip_extract_render_verbatim`
  emits as its *body* — the `cat`/`awk` output of the source range — **without**
  the two-line attribution comment block or the blank line that follows it.
  Rationale: the manifest hash certifies that the *source* didn't drift, not
  that the rendered target matches. To keep impl and the (future, porcelain)
  analyze phase aligned to one recipe, factor a pure
  `wip_extract_source_body <entry> <root>` helper that emits just that body,
  and have `wip_extract_render_verbatim` call it (so the render output stays
  byte-identical). The exact newline normalization is locked in OQ1.

- **`--verify-hashes` is a pre-write gate (LDS mismatch_detection).** When the
  flag is set, a verification pass runs **after manifest validation but before
  the entry write loop**. All declared hashes match → the write loop proceeds
  unchanged. Any mismatch or missing hashed source → the run does **not write
  any target**, sets `ok:false` / `error.kind:"hash-mismatch"`, writes the §7
  report (before the exit, §7.3), and `exit 4`. No partial extraction, per the
  LDS "never silently proceed" principle.

- **Mismatch surfaces as `exit 4`, kind `hash-mismatch`.** Consistent with
  every other `extract` manifest-integrity failure (incompatible-schema,
  not-approved, empty, dup-id, bad-shape, content-drift are all exit 4). No new
  exit code. The `ok:false` envelope carries
  `error: {code:4, kind:"hash-mismatch", message, paths:[…source files…],
  mismatches:[{id, source, expected_hash, actual_hash, status}]}` where
  `status` is `mismatch` or `missing`.

- **Ledger `hash_verification` gains a small enum.** On the **success**
  envelope (`ok:true`): `"skipped-v1"` (flag off, default — unchanged) |
  `"verified"` (flag on, ≥1 hash checked, all matched) | `"no-hashes"` (flag
  on, but zero entries carried a verifiable hash — the flag was a no-op; also
  emits a report warning). On the mismatch path the status lives in
  `error.kind` instead (the `ok:false` envelope has no `hash_verification`
  field today and keeps that shape).

- **Report `content_hash_check` is threaded as a computed arg, mirroring
  `file_existence_check`.** Add a 14th positional arg
  (`content_hash_json`) to **both** `wip_extract_report_yaml` and
  `wip_extract_report_md`, exactly as `existence_json` is threaded today. The
  command layer computes it; the renderers stay pure. Shape:
  ```yaml
  content_hash_check:
    status: skipped-v1 | pass | fail   # skipped-v1 when flag off
    entries_checked: <int>             # entries with a verifiable hash
    entries_matched: <int>
    entries_no_hash: <int>             # verifiable-shape entries lacking a hash + non-verifiable entries
    mismatches:                        # [] unless status == fail
      - { id, source, expected_hash, actual_hash, status }  # status: mismatch | missing
  ```
  When the flag is off the command passes the literal
  `{status:"skipped-v1"}` (default) so the renderers need no flag awareness.
  The `.md` VERIFICATION line `Content hash check:   skipped-v1`
  (`extract-lib.bash:427`) becomes dynamic — e.g.
  `Content hash check:   pass (2/2 entries matched)` or
  `fail (1/2 matched, 1 mismatch)` or `skipped-v1`, mirroring the existing
  `File existence check:` line.

- **Hasher helper prefers `sha256sum` (already a dep via `coreutils`), falls
  back to `shasum -a 256`.** Add `wip_extract_sha256` (reads stdin or a file
  path, echoes the hex digest, or non-zero + stderr if no hasher found). **No
  flake change is strictly required** — see OQ6 / the finding below: both
  `flake.nix` and `templates/setup/deps/flake.nix` already list `coreutils`,
  which provides `sha256sum`. (`shasum` is the perl tool and is *not* a flake
  package; step-17's `manifest_hash` uses `shasum -a 256` directly and
  degrades to `null` if absent — for step-18 we lead with `sha256sum` so the
  flag works in the pure nix dev shell.)

- **`--verify-hashes` runs even under `--dry-run`** (it is read-only). A
  mismatch under `--dry-run` still yields `ok:false` / `exit 4`
  `hash-mismatch` (the gate's whole purpose is to report "this would not
  proceed"), but — like every dry-run path — **writes no targets and no
  report**. See OQ5.

## Chunks

Each chunk is one focused commit.

1. **Pure hash helpers in `wip-plumbing-extract-lib.bash` (no I/O).**
   - `wip_extract_sha256` — echo the SHA-256 hex of stdin (or `$1` file path);
     try `sha256sum` then `shasum -a 256`; return non-zero + a stderr
     diagnostic if neither exists.
   - `wip_extract_source_body <entry-json> <root>` — emit just the extracted
     source-range bytes (the `cat`/`awk` body, **no** attribution), factored
     out of `wip_extract_render_verbatim`. Refactor `render_verbatim` to call
     it so its output stays byte-identical (regression-guarded by the existing
     happy-path asserts).
   - `wip_extract_verify_hashes <manifest-json> <root>` — pure verification
     pass: walk entries, select the verifiable ones (ok-verbatim +
     single-file + non-empty `source.hash`), compute the body hash, compare,
     and **echo the `content_hash_check` JSON object** (status pass/fail +
     counts + `mismatches[]`). Missing source on a hashed entry → a `missing`
     mismatch. Side-effect-free and unit-testable.

2. **Thread `content_hash_check` through the report renderers.** Add the 14th
   positional arg to `wip_extract_report_yaml` and `wip_extract_report_md`;
   replace the hardcoded `content_hash_check: { status: "skipped-v1" }`
   (`:345`) with the passed object, and make the `.md` `Content hash check:`
   line render from it. Default callers (step-17 wiring) pass
   `{status:"skipped-v1"}`, so with the flag off the output is byte-identical.

3. **Wire the flag + pre-pass gate into `extract.bash`.**
   - Parse `--verify-hashes` in the arg loop (set `verify_hashes=1`).
   - After manifest validation, **before** the entry write loop: if
     `verify_hashes`, call `wip_extract_verify_hashes` → `content_hash_json`;
     else `content_hash_json='{"status":"skipped-v1"}'`.
   - If verify found mismatches: set `ok=false`, `err_kind=hash-mismatch`,
     `err_paths_json` = mismatched source files, attach `mismatches[]`; **skip
     the write loop** (write no targets).
   - Set the stdout `hash_verification` value per the enum (`skipped-v1` /
     `verified` / `no-hashes`) on the `ok:true` branch; emit `hash-mismatch`
     in the `ok:false` error envelope.
   - Pass `content_hash_json` to both report renderers (it's written before
     the `exit 4`, §7.3). Honor `--dry-run` exactly as today (no report when
     `WIP_DRY_RUN=1`).
   The existing stdout envelope shape, stderr one-liner, and the entire write
   loop are otherwise untouched.

4. **Spec update (`engineering/specs/wip-plumbing-cli.md`).**
   - Usage signature → `extract [--manifest <path>] [--force] [--verify-hashes]`.
   - Flip the v1-scope table row 821 from `**skipped-v1** (ledger flag)` to
     describe the opt-in flag (default skipped; `--verify-hashes` enables it).
   - Add a `--verify-hashes` subsection: what's verified
     (`entries[].source.hash`, single-file sources only), the pre-write-gate
     semantics, `exit 4 hash-mismatch`, the `content_hash_check` populated
     shape, the `no-hashes` no-op-warning case, and the dry-run interaction.
   - Add `hash-mismatch` to the exit-4 kind list (`:912-914`); note the new
     `hash_verification` enum values and the `error.mismatches[]` field.
   - Update the `setup deps` flake note: `sha256sum` is provided by
     `coreutils` (already listed) — no new package required.

5. **Tests in `test/test-extract.sh`** (own commit; see Test strategy).

(No standalone flake-edit chunk — the dep is already satisfied; see OQ6. If
review wants the dependency made *explicit*, the fallback is a one-line
comment in both flakes, but the lean is no change.)

## Test strategy

Extend `test/test-extract.sh` — reuse `build_lds_enabled_root` /
`write_manifest` / `assert_*` / `yq`, same as step-17. **The test computes
each expected hash itself** with the same recipe the impl uses (there is no
analyze phase to produce real hashes, and hardcoding a magic digest would be
brittle). Lock the recipe so test and impl agree (OQ1): for a ranged
single-file source, `expected=$(awk 'NR>=S && NR<=E' src | sha256sum | awk '{print $1}')`;
for whole-file, `cat src | sha256sum`.

New cases (all opt-in via `--verify-hashes`):

- **Match → pass.** Single-file ranged entry whose stored `source.hash` is the
  correct digest. Assert `ok:true`, `hash_verification == "verified"`, target
  written, and report `content_hash_check.status == "pass"` with
  `entries_checked == entries_matched == 1`.
- **Mismatch → exit 4 gate.** Same fixture but the stored hash is wrong (or
  mutate the source after computing). Assert `exit 4`,
  `error.kind == "hash-mismatch"`, `error.paths` lists the source, and that
  **no target was written** (`assert_absent`). Assert the report **was** written
  (§7.3) with `content_hash_check.status == "fail"` and one `mismatches[]` row.
- **Missing hashed source → missing mismatch.** Hashed entry whose source file
  doesn't exist. Assert `exit 4 hash-mismatch`, no target, report mismatch row
  `status == "missing"`.
- **No declared hash → skipped, not failed.** Simple-path / single-file
  without `hash`. Assert `ok:true`, `hash_verification == "no-hashes"`,
  `content_hash_check.entries_no_hash >= 1`, a report `warnings[]` entry, and
  the targets still wrote.
- **Mixed manifest.** One hashed-matching entry + one no-hash entry → `ok:true`,
  `hash_verification == "verified"`, both targets written,
  `entries_checked == 1`, `entries_no_hash == 1`.
- **Flag off (regression).** Re-assert an existing happy-path fixture without
  the flag: `hash_verification == "skipped-v1"` and
  `content_hash_check.status == "skipped-v1"` (step-17 behavior unchanged).
- **dry-run + verify.** `--dry-run --verify-hashes` on a matching fixture →
  `ok:true`, no target, no report. On a mismatching fixture → `exit 4
  hash-mismatch`, no target, no report.

Deferred coverage (with reason): no test for the top-level `sources.<path>.hash`
registry or multi-file `combined_hash` — both are out of v1 scope (OQ2).

## Definition of done

- `extract --verify-hashes` against a manifest whose single-file
  `source.hash` values all match writes every target as before, exits 0, and
  the stdout ledger reports `hash_verification: "verified"`; the report's
  `content_hash_check.status` is `"pass"` with matching `entries_checked` /
  `entries_matched`.
- A single mismatched (or missing) hashed source makes the run **write no
  target**, exit 4 with `error.kind: "hash-mismatch"` (source paths in
  `error.paths`, detail in `error.mismatches[]`), while still writing the §7
  report (with `content_hash_check.status: "fail"`) before the exit.
- An entry carrying no verifiable hash is recorded in
  `content_hash_check.entries_no_hash` and never fails the run; a
  `--verify-hashes` run where *no* entry has a hash exits 0 with
  `hash_verification: "no-hashes"` and a report warning.
- **Without** `--verify-hashes`, the stdout envelope, stderr one-liner, and
  report are byte-identical to step-17 (`hash_verification: "skipped-v1"`,
  `content_hash_check.status: "skipped-v1"`).
- `--dry-run --verify-hashes` performs the check, reflects it in stdout, exits
  4 on mismatch, and writes neither targets nor report.
- `sha256sum` resolves in the pure nix dev shell (via `coreutils`); the
  helper also works where only `shasum` exists.
- `engineering/specs/wip-plumbing-cli.md` documents the flag, the
  `hash-mismatch` exit-4 kind, the `content_hash_check` populated shape, and
  the no-op/dry-run cases; the v1-scope table no longer calls hash
  verification flatly "skipped-v1".
- `test/test-extract.sh` covers the cases above; all extract assertions and
  every other suite stay green.

## Open questions to resolve during execution

Each carries a lean so the builder can proceed without blocking.

1. **Hash recipe / newline normalization — the load-bearing one.** The LDS
   schema's `line_range` calculation (`:560-577`) says _"extract lines, join
   with newlines, encode UTF-8, sha256"_ — i.e. **no trailing newline**.
   But `awk 'NR>=s&&NR<=e'` (the body extractor) emits a **trailing newline**.
   So the digest of the body bytes ≠ the digest of a join-without-trailing-nl.
   **Lean: hash the extracted body bytes exactly as `wip_extract_source_body`
   produces them** (trailing newline included for ranges; raw `cat` bytes for
   whole-file). Rationale: (a) it is the literal reading of "compute SHA-256 of
   each entry's source" for *our* extractor; (b) it is trivially
   self-consistent with a test that computes the same; (c) there is **no
   analyze phase in this repo** producing real hashes to be incompatible with,
   so we get to define the deterministic contract — and we document it in the
   spec. If a future real consumer's analyze output uses the no-trailing-nl
   join, reconcile then (a follow-up, not a v1 blocker). **Escalate to
   Coordinator if** a concrete external manifest with `source.hash` values
   surfaces during build — the recipe must match its producer.

2. **Whole-file `sources` registry + multi-file `combined_hash` — verify too?**
   The schema defines a top-level `sources.<path>.hash` whole-file registry and
   a multi-file `combined_hash`. **Lean: no — v1 verifies only
   `entries[].source.hash` on single-file sources.** Multi-file is already
   `unsupported[]` (can't reach the write path), and the roadmap goal says
   _"each entry's source"_ (entry-level, not a separate registry pass). Defer
   both to a backlog `extract-verify-sources-registry` item if a consumer asks.

3. **Mismatch → exit code & blast radius.** **Lean: `exit 4`, new kind
   `hash-mismatch`, full pre-write gate (write zero targets on any mismatch).**
   Matches LDS "never partial / verify before write" and keeps every
   manifest-integrity failure on exit 4. Rejected: a *new* exit code (5) — no
   other extract failure justifies a distinct code, and the spec's exit table
   keeps integrity failures unified at 4. Rejected: per-entry skip-and-continue
   (write the matching entries, skip the mismatched) — violates LDS's
   no-partial-extraction principle.

4. **Hash absent in the manifest — skip vs warn.** **Lean: skip per entry
   (count in `entries_no_hash`, never fail); warn only at the aggregate when a
   `--verify-hashes` run finds *zero* verifiable hashes** (`hash_verification:
   "no-hashes"` + a report `warnings[]` line so the consumer knows the flag was
   a no-op). Hashes are `required: false` in the schema, so a missing hash is
   not an error — but a flag that silently does nothing deserves one visible
   signal.

5. **`--verify-hashes` × `--dry-run`.** **Lean: verification runs (it's
   read-only), the result shows in stdout, a mismatch still produces `ok:false`
   / `exit 4 hash-mismatch`, and — like all dry-run paths — no targets and no
   report are written.** Rationale: the gate's purpose is to answer "would this
   proceed?"; masking a mismatch under dry-run would make the dry-run lie.
   (Note: `--dry-run` is the **global** flag on `bin/wip-plumbing`
   (`bin/wip-plumbing:47` → `WIP_DRY_RUN`); no per-verb parsing is needed —
   verify just reads `WIP_DRY_RUN` for the write decisions, same hook step-17
   uses.)

6. **Tooling fallback + flake dependency.** **Lean: `wip_extract_sha256`
   tries `sha256sum` first, then `shasum -a 256`; add nothing to the flakes.**
   **Verified finding:** both `flake.nix` and `templates/setup/deps/flake.nix`
   already list `coreutils`, which provides `sha256sum` — so the roadmap
   bullet's _"add `sha256sum`/`shasum` to the flake"_ is **already satisfied**
   for `sha256sum`. `shasum` (perl) is *not* a flake package, so leading with
   `sha256sum` is what makes the flag work in the pure dev shell. If the
   Coordinator/human prefers the dependency be made *explicit* anyway, the
   fallback is a clarifying comment in both flakes — but the lean is no change.
   **Flagged to Coordinator** because it diverges from the roadmap bullet's
   stated expectation.

7. **Test strategy — fixtures & expected hashes.** **Lean: reuse the existing
   `test-extract.sh` fixture style; the test computes each expected
   `source.hash` itself** with the OQ1 recipe (`awk` range | `sha256sum`),
   writes it into the manifest, then asserts the match path; for the mismatch
   path either store a deliberately-wrong hash or mutate the source after
   computing. This keeps the suite self-consistent and free of magic digests
   while still proving both the pass gate and the fail gate.
