# Workplan — followup · `setup lds` (the sixth `setup` verb)

Backlog item per [`roadmap.md`'s Backlog
section](../roadmap.md#backlog): **setup-lds-verb**. Round 3 closed at
step-15; this is a follow-up that unblocks the consumer-facing path for
the LDS seam shipped there. step-15's `graduate` / `extract` verbs exit
3 (`lds-not-enabled` / `lds-sentinel-missing`) on every repo whose
`features.lds.enabled` is false or whose `<eng-docs>/.lds-manifest.yaml`
is absent — the error hints both point at this item. After this verb
ships, a fresh consumer repo can run `wip-plumbing init` → `setup deps`
→ `setup lds` → `graduate <artifact>` end-to-end.

The verb is the **sixth member of the `wip setup` family** shipped in
step-14. Its contracts (three-way idempotency, JSON ledger shape,
`--force`, `--dry-run`, stderr hint, manifest flag flip, sentinel
post-check) are inherited verbatim from step-14's
[workplan](./step-14-wip-setup.md). The only new shape this verb adds is
a `--sentinel-only` flag (rationale below).

This is a **backlog follow-up, not a numbered Round 4 step.** Round 3
closed cleanly at step-15; there is no Round 4 yet. The roadmap stays
shaped as "Round 3 closed; this backlog item later shipped" — see
[Roadmap convention](#roadmap-convention) for the in-place mark.

## Decisions (made here, follow step-14's contract verbatim where applicable)

- **Sixth verb only — `setup lds`.** Slots into the existing
  `bin/wip-plumbing setup {deps,direnv,hygiene,release,agents}`
  dispatcher as `setup lds`. No aggregator, no chained variant. Same
  composition rule as the other five (`missing-manifest` if `.wip.yaml`
  is absent; no other prereq because LDS install doesn't depend on
  another `setup` verb's output).
  **Lean: one verb, family-pattern.**

- **Template tree: `templates/setup/lds/engineering/...`** Writes
  install bytes under `<root>/engineering/...` literally. The
  `engineering/` segment is part of the template path, not a parameter —
  matching the rest of the family (every other `setup` verb writes
  fixed paths into the consumer root). Consumers who want a non-default
  LDS root (`docs/`, `documentation/`, etc.) get exit 3
  `lds-already-installed-elsewhere` if the `features.lds.root` they've
  already set differs from `engineering`, with a stderr hint pointing
  at the open question below. (Lean: `engineering` is LDS's own default
  per `install.md` Step 1; v1 doesn't need configurable roots.) See
  [Open question: configurable root](#configurable-lds-root).
  **Lean: hardcoded `engineering/`; flag follow-up for later.**

- **What the template ships.** Verbatim file copies (no `{{key}}`
  substitution; same as the other five verbs):

  ```
  templates/setup/lds/
    engineering/
      .lds-manifest.yaml             # the sentinel (seed manifest, see below)
      decisions/.gitkeep
      product/.gitkeep
      architecture/.gitkeep
      specs/.gitkeep
      reference/.gitkeep
      features/.gitkeep              # Layer 6 in LDS; "features" in our layer allowlist
      implementation/.gitkeep
      appendices/.gitkeep
      maintenance/
        audit.md                     # verbatim from layered-documentation-system/maintenance/audit.md
        refine.md                    # verbatim
        sync.md                      # verbatim
        update.md                    # verbatim
  ```

  - **Nine directories.** The 7 canonical LDS layers
    (`decisions, product, architecture, specs, reference, features,
    implementation`) plus `maintenance` and `appendices` — the same
    nine-layer set step-15 locked into
    `WIP_GRADUATE_LAYERS` (`lib/wip/wip-plumbing-graduate-lib.bash:18`).
    So once `setup lds` runs, every `graduate-to:` directive in the
    allowlist lands in a directory that exists.
  - **`.gitkeep` files.** Plain empty files so empty layer dirs survive
    `git add`. The byte content is `""` (empty); they're real files on
    disk so the template walker sees them.
  - **`maintenance/` workflow copies — all four, verbatim.** Per user
    brief lean. The bytes are read from
    `layered-documentation-system/maintenance/{audit,refine,sync,update}.md`
    once at template-author time (this workplan's Chunk 3), checked in
    under `templates/setup/lds/engineering/maintenance/`, and copied
    verbatim by the verb. `audit.md` contains `{ENG_DOCS_DIR}`
    placeholders intact — they are *instruction text* read by an AI
    agent, not template substitutions the verb performs. LDS's own
    workflow that consumes them does the substitution at use time.
    Per-file `cmp` test (Chunk 7) pins byte-equivalence to the source
    files; if `layered-documentation-system/maintenance/*.md` drifts,
    the test catches it.
  - **The `audit/` subdir** under `layered-documentation-system/maintenance/`
    (`audit-migration.md`, `audit-ongoing.md`, `common-audit.md`) is
    **NOT shipped**. Those are install-time *generators* that LDS's
    `install.md` combines to produce `maintenance/audit.md`; once we're
    shipping `audit.md` directly, the generators are dead weight in the
    install. (LDS's own install workflow ships the generators because
    its install.md picks fresh-install vs migration-install templates;
    `setup lds` only does fresh install.) Cut documented for the
    follow-up `setup lds --migration` open question below.

- **Manifest seed: a tiny approved-shape manifest, not the LDS upgrade
  manifest.** The sentinel file `<root>/engineering/.lds-manifest.yaml`
  ships with a minimal shape that:
  - **Satisfies `extract`'s validator** (so a `wip-plumbing extract`
    against a fresh install exits with `manifest-empty` exit 4, not
    `incompatible-schema` — see `lib/wip/wip-plumbing-extract-lib.bash`
    `wip_extract_validate_manifest` for the three keys it checks:
    `metadata.schema_version`, `metadata.status`, `entries[]`).
  - **Satisfies `doctor`'s sentinel check** (the file just needs to
    exist; `_wip_feature_records` resolves
    `(.features.lds.root // "engineering") + "/.lds-manifest.yaml"`).
  - **Is also a valid LDS install manifest** per `install.md` §11e (so
    a consumer who later runs the LDS `upgrade` workflow finds the
    expected schema_version / install_type fields).

  Concrete shape (~25 lines):

  ```yaml
  # LDS installation manifest — generated by `wip-plumbing setup lds`.
  # Do not edit by hand; bytes are pinned for idempotency.

  metadata:
    schema_version: "1.0.0"
    status: "approved"
    install_type: "fresh"
    eng_docs_dir: "engineering"

  # Empty extraction list — `wip-plumbing extract` will exit 4
  # `manifest-empty` until entries are added by the LDS analyze
  # workflow (LDS porcelain territory, not this verb).
  entries: []
  ```

  Note: the `entries: []` is *deliberate*. step-15's `extract` treats
  an empty entries list as exit 4 `manifest-empty` (per its workplan,
  "zero entries is an authoring bug, not an idempotent no-op"). That's
  correct here: after `setup lds`, `extract` should fail until the
  consumer authors entries, which is what they want — silent success
  on an empty manifest would mask the missing-authorship state.
  **Lean: ship the empty-but-valid manifest; let `extract` complain
  loudly when run prematurely.**

- **Manifest flag flip.** Sets BOTH
  `features.lds.enabled: true` AND `features.lds.root: engineering`,
  via two `wip_setup_set_feature_flag` calls (or one call with two
  kv-pairs — the helper accepts `<feature> <key=value>...`). Same
  yq-in-place pattern the other five verbs use. The `root` key is what
  `_wip_feature_records` reads to resolve the sentinel path, so flipping
  both keeps detect/doctor consistent without a separate `wip.yaml`
  edit.
  Idempotency: re-running on a manifest that already has both keys is a
  manifest no-op (`manifest_status: "noop"`, ledger
  `manifest_updated: null`).

- **Three-way idempotency per file, identical to step-14.** Reuses
  `wip_setup_walk_template_tree` as-is. The walker already handles
  arbitrary subtree depths (`engineering/maintenance/audit.md` is two
  levels deep — same shape as `agents/.claude-plugin/commands/next.md`,
  which the walker already handles).

- **Sentinel post-check.** After all writes, the verb asserts that
  `<root>/engineering/.lds-manifest.yaml` exists. Wire `lds` into the
  per-verb sentinel map by adding one case to
  `wip_setup_sentinel_for_verb`. Failure ⇒ exit 1 `internal` (template
  bug; should never trigger).

- **`--sentinel-only` flag.** Writes ONLY
  `engineering/.lds-manifest.yaml`, skips all `.gitkeep` files and the
  `maintenance/*.md` files. Still flips both feature-flag keys.
  Rationale: a real-world adoption case is "this repo already has
  `engineering/decisions/`, `engineering/specs/` etc. authored by hand
  (or by a different tool); I just want to bind the LDS seam without
  rewriting my layout." Without this flag the verb would either:
  - refuse on every existing layer dir's `.gitkeep` (since `.gitkeep` is
    absent in their tree — actually no, the verb only refuses on
    *differing* files; absent target ⇒ writes the `.gitkeep`. So
    without the flag it would *add* `.gitkeep` to dirs that already
    have content. That's harmless but noisy.)
  - try to overwrite an existing `engineering/maintenance/audit.md` if
    the consumer already authored one — exit 4 unless `--force`.

  `--sentinel-only` keeps the bare minimum needed for `graduate` /
  `extract` to start working. Documented in spec + stderr hint.
  Implementation: dispatcher branch that bypasses `walk_template_tree`
  and calls `wip_setup_write_idempotent` for `.lds-manifest.yaml`
  directly.
  **Lean: ship the flag; one extra dispatcher branch.**

- **No `--root <dir>` flag in v1.** Per the lean above. Adding it
  requires deciding how the template path rewrites (does it ship the
  template under `engineering/` and rewrite at copy time? Or template
  the dir name? Both are larger than this follow-up wants). Captured
  as an open question; deferred to a follow-up if a real consumer asks.

- **No migration-mode in v1.** Per the cut above. LDS's `install.md`
  supports both fresh install and migration install (from legacy docs);
  the migration path is multi-session LLM-driven (analyze → review →
  extract) and belongs in porcelain, not this deterministic verb.
  **Lean: setup lds = fresh install only. Migration is a separate seam
  (the existing `wip-plumbing extract` verb already covers the
  deterministic half once the consumer has an approved manifest).**

- **No `setup` aggregator change.** step-14 deliberately rejected a
  no-arg `setup` aggregator that runs every verb in sequence; this
  follow-up doesn't reopen that decision.

- **Glossary partial: NOT in scope.** step-13's `lds.md` glossary
  partial is its own backlog item (`glossary-partial-lds`), declared
  predicate-true-but-partial-absent (graceful skip). After this verb
  flips `features.lds.enabled: true` on a consumer, their
  `glossary check` would list `lds.md` under the ledger's `skipped`
  field — *existing* behavior, not a regression caused by this verb.
  When the partial-LDS backlog item ships, the entry will start
  *including*.
  Verified by Chunk 6's "post-verb glossary-check on tempdir" test:
  ledger must show `lds.md` as skipped (not refused), exit 0.

- **`--project` forwarding.** Inherits from the dispatcher prelude
  (same as the other five verbs); no per-verb work.

- **`--dry-run`.** Per family convention: print the ledger (with
  expected `wrote` paths), touch nothing — neither files nor manifest.
  Same code path as the other verbs; no special handling.

- **Spec home: §1 verb table + §3.** One new row in the §1 table
  (between `setup agents` and `graduate`) and one new subsection in §3
  alongside the existing five `setup` verbs. The contract documentation
  is mostly inherited — only the `--sentinel-only` flag, the
  `engineering/` hardcoding, and the manifest-flag map (`features.lds.{enabled, root}`)
  need verb-specific words.

## Out of scope (explicit cuts)

- **Configurable LDS root** (`--root <dir>`): v1 hardcodes
  `engineering/`. Open question.
- **Migration install** (analyze legacy docs → extract): use the
  existing `extract` verb once an approved manifest is available;
  authoring the manifest is LLM-driven (porcelain).
- **Template selection** (MADR-minimal vs MADR-full, Lean-PRD vs
  Epic-based, etc.): LDS's `install.md` lets the user pick per-layer
  templates. This verb ships an opinionated bare-bones structure
  (`.gitkeep` only — no template files). A follow-up could ship
  per-layer `_template.md` files; for now, consumers author their first
  ADR / spec without a template scaffold.
- **`features.lds.installs[]`** (plural roots / monorepo support): per
  the manifest's existing comment "scalar single root for v1 (monorepo
  plural deferred)." This verb writes the scalar form.
- **Cliff for upgrade-mode**: LDS supports an `upgrade` workflow that
  diffs installed files against a new LDS distribution. Out of scope —
  the v1 `setup lds` is install-only.

## Chunks

1. **Author `templates/setup/lds/engineering/.lds-manifest.yaml`.** The
   ~25-line seed shown in Decisions above. Single source of truth for
   the manifest shape; pinned by Chunk 7's "yq parses cleanly + meets
   extract validator" test.

2. **Author the empty `.gitkeep` files** under
   `templates/setup/lds/engineering/{decisions,product,architecture,specs,reference,features,implementation,appendices}/`.
   Each is a zero-byte file. (Idiomatic gitkeep; some projects put a
   newline — we use zero bytes for byte-equivalence simplicity. Test
   asserts each is 0 bytes.)

3. **Copy maintenance workflow files** from
   `layered-documentation-system/maintenance/{audit,refine,sync,update}.md`
   to `templates/setup/lds/engineering/maintenance/` verbatim:

   ```bash
   cp layered-documentation-system/maintenance/{audit,refine,sync,update}.md \
      templates/setup/lds/engineering/maintenance/
   ```

   No substitution. Chunk 7's `cmp` test pins the byte-equivalence.

4. **Extend `lib/wip/wip-plumbing-subcommands/setup.bash`** with the
   `lds` case. Three edits:
   - Subcommand allowlist (line 22): add `lds`.
   - Dispatcher prelude (after `direnv`'s `missing-prereq` case, line
     ~62-68): add a `lds` case that branches on `--sentinel-only`:
     - If `--sentinel-only`: call `wip_setup_write_idempotent` directly
       on `templates/setup/lds/engineering/.lds-manifest.yaml` →
       `<root>/engineering/.lds-manifest.yaml`, feed one
       `{status, path}` line into the ledger fold, skip the walker.
     - Otherwise: fall through to the normal `walk_template_tree`.
   - Manifest-flip block (after the `agents` case, line ~108-122):
     add `lds` calling `wip_setup_set_feature_flag "$manifest" "lds"
     "enabled=true" "root=engineering"`.
   - `wip_setup_sentinel_for_verb` (line ~174-180): add
     `lds) printf 'engineering/.lds-manifest.yaml' ;;`.

   **Flag parsing.** Add `--sentinel-only` to the loop (line ~28-37);
   sets a local `sentinel_only=1`. Lives next to `--force`. Reject
   `--sentinel-only` for any subcommand other than `lds` with exit 2
   usage.

5. **Extend the stderr-hint block** (`_wip_setup_hint`):
   ```bash
   lds)
     printf 'wip-plumbing: setup lds: hint: run `wip-plumbing doctor` to verify the LDS sentinel\n' >&2
     printf 'wip-plumbing: setup lds: hint: `wip-plumbing graduate <artifact>` now works against this repo\n' >&2
     ;;
   ```

6. **Spec update: `engineering/specs/wip-plumbing-cli.md`.**
   - §1 verb table: insert one row between the existing `setup agents`
     row and the `graduate` row:
     `| `setup lds` | Write the LDS install scaffold to `engineering/`; flip `features.lds.{enabled, root: engineering}`. | step-15 follow-up |`
   - §3 `wip-plumbing setup` subsection: add a sub-block documenting
     `setup lds` specifically — what it writes, the
     `--sentinel-only` flag, the `engineering/` hardcoding, the
     verb→feature-flag map entry, the post-write sentinel
     (`engineering/.lds-manifest.yaml`).
   - Update the verb→feature-flag table (currently shows five rows) to
     add `setup lds → features.lds.{enabled, root: engineering}` and
     sentinel `engineering/.lds-manifest.yaml`.
   - Update §3's prose paragraph "Five subcommands" → "Six
     subcommands."

7. **Tests: extend `test/test-setup.sh`.** One file, no new test
   helpers. Reuses the tempdir fixture pattern from the existing five
   verbs. Coverage:
   - **`setup lds` writes the expected file set** (full mode). Fresh
     tempdir with `.wip.yaml`. Assert exit 0, ledger
     `ok:true`, `wrote` contains all 13 files (1 manifest + 8 gitkeeps
     + 4 maintenance), `skipped_idempotent` empty, `refused` empty.
   - **Sentinel post-check.** Assert `engineering/.lds-manifest.yaml`
     exists on disk after the verb. Assert `wip-plumbing doctor` on the
     tempdir reports zero LDS drift (feature now declared+active).
   - **Manifest flag flip.** Assert
     `.wip.yaml` `features.lds.enabled == true` AND
     `features.lds.root == "engineering"`. Re-running the verb is a
     manifest no-op.
   - **Idempotency on re-run.** Second run: `wrote` empty,
     `skipped_idempotent` contains all 13 paths,
     `manifest_updated: null`.
   - **`--force` overwrites drift.** Mutate one of the maintenance
     files (append a byte); re-run without `--force` ⇒ exit 4 with
     that path in `refused`; re-run with `--force` ⇒ exit 0, file
     restored.
   - **`--sentinel-only` skips layer dirs and maintenance files.**
     Fresh tempdir, run `setup lds --sentinel-only`. Ledger `wrote`
     contains exactly `[engineering/.lds-manifest.yaml]`; no
     `.gitkeep` files exist on disk; `engineering/decisions/` etc. do
     NOT exist. Manifest flags flipped as in the full-mode case.
   - **`--sentinel-only` rejected for other subcommands.** e.g.,
     `setup deps --sentinel-only` ⇒ exit 2 usage.
   - **Template fidelity dogfood (cmp).** Assert byte-equivalence
     of each maintenance file:
     ```
     cmp templates/setup/lds/engineering/maintenance/audit.md  layered-documentation-system/maintenance/audit.md
     cmp templates/setup/lds/engineering/maintenance/refine.md layered-documentation-system/maintenance/refine.md
     cmp templates/setup/lds/engineering/maintenance/sync.md   layered-documentation-system/maintenance/sync.md
     cmp templates/setup/lds/engineering/maintenance/update.md layered-documentation-system/maintenance/update.md
     ```
   - **Seed manifest is yq-parseable + passes extract-validator.**
     Parse the seed via `yq` (no error). Run an integration check by
     constructing a `wip_extract_validate_manifest` call on the seed's
     JSON — should pass the schema_version + status checks and FAIL
     on `entries` (empty list ⇒ `manifest-empty`). Pins the lean
     "extract complains loudly when no entries" decision.
   - **Composition: `--dry-run` touches nothing.** Manifest byte-equal
     pre/post; tempdir is empty of `engineering/*` post.
   - **Glossary partial graceful skip.** After the verb runs (full
     mode) on the tempdir, `wip-plumbing glossary assemble` exits 0,
     the ledger lists `lds.md` as `skipped`, no other regression.
     Pins step-13's already-existing graceful-skip behavior.

   Budget: ~10 new assertions on top of step-14's 74. Total
   test-setup.sh stays under 200 assertions.

8. **Dogfood end-to-end against `graduate`.** Inside the test (new
   case at the end of `test-setup.sh`):
   - Build tempdir with `init`.
   - Run `setup lds` (full mode).
   - Write a tiny artifact at `$tmp/scratch/foo.md` with front-matter
     `graduate-to: decisions/auto-test-graduate.md`.
   - Run `wip-plumbing graduate $tmp/scratch/foo.md`.
   - Assert exit 0 (was exit 3 `lds-not-enabled` before this verb
     existed); assert
     `$tmp/engineering/decisions/0001-test-graduate.md` exists with the
     artifact body (front-matter stripped).
   - Re-run `graduate` ⇒ idempotent skip.
   This is the "consumer can actually use `graduate` now" sentinel for
   the entire follow-up.

9. **Bump usage block.** Update `wip_usage` in
   `wip-plumbing-lib.bash` to list `setup lds` and the
   `--sentinel-only` flag.

10. **Mark the backlog row in `roadmap.md`** per the convention below.

11. **Branch + commit + merge.** Branch name:
    `followup-setup-lds-verb`. Commit body MUST include the dogfood
    transcript from Chunk 8 (tempdir round-trip exit 0 +
    `graduate` against the freshly-installed scaffold). Merge via
    `git checkout main && git merge --no-ff followup-setup-lds-verb`.

## Test strategy

Extend `test/test-setup.sh` only — no new test file. Reuses the same
tempdir + JSON-ledger fixture pattern as the existing 74 assertions.
~10 new assertions. Adds the new dogfood case at the end.

All other suites (25 prior, all green after step-15) stay untouched.
`make check` budget: small.

Gate triple stays green:
- `nix develop --command make check`
- `nix develop --command bin/wip-plumbing doctor` (zero drift; THIS
  repo's `features.lds.enabled: false` is unchanged because all the
  tests run in tempdirs)
- `nix develop --command pre-commit run --all-files`

Critical invariant per the user's brief: **do NOT flip this repo's own
`features.lds.enabled`.** Every test uses a tempdir; the dogfood case
runs against `$tmp`, not the repo. The `cmp` template-fidelity
assertions read files from `layered-documentation-system/maintenance/`
but only write to `templates/setup/lds/`, which is template authorship,
not install.

## Definition of done

- `templates/setup/lds/engineering/` populated per Chunks 1–3 (1
  manifest + 8 `.gitkeep` files + 4 maintenance copies = 13 files).
- `lib/wip/wip-plumbing-subcommands/setup.bash` extended with the
  `lds` case + `--sentinel-only` flag + sentinel-map entry.
- `bin/wip-plumbing` usage lists `setup lds` and `--sentinel-only`.
- `engineering/specs/wip-plumbing-cli.md` documents `setup lds`: one
  new row in §1 verb table; new sub-block + table-row in §3.
- `test/test-setup.sh` extended with the ~10 new assertions + the
  dogfood case; passes under `nix develop --command make check`.
- All previously-passing tests still pass.
- `nix develop --command bin/wip-plumbing doctor` reports zero drift
  (this repo's `features.lds.enabled: false` unchanged).
- `nix develop --command bin/wip-plumbing glossary check` exits 0.
- `nix develop --command pre-commit run --all-files` exits 0.
- `.wip/initiatives/distillation/roadmap.md` backlog row marked per
  the convention below.
- `.wip.yaml`'s `initiatives[0].active_step` UNCHANGED (stays at
  `step-15` — backlog items don't advance the active step).
- Branch + commit + merge into `main` (no-ff merge commit).
- Commit body includes the dogfood transcript per Chunk 8.

## Roadmap convention

Active step stays at `step-15`. The backlog bullet at the bottom of
`roadmap.md` (line 68) is replaced **in place** with a ✅ form that
preserves the auditable trace, so a future reader can still see what
the backlog item said when it was authored. Recommendation:

```markdown
- ✅ **setup-lds-verb** — Sixth `setup` verb to install the LDS scaffold
  (manifest skeleton, layer directories, `maintenance/` workflow
  copies) and flip `features.lds.enabled`. The hint in
  `lds-not-enabled` / `lds-sentinel-missing` errors from
  `graduate`/`extract` points at this item. **✅ shipped 2026-06-14**
  ([followup workplan](./workplans/followup-setup-lds.md)) — six
  `setup` verbs now; `--sentinel-only` for repos with existing
  `engineering/`; consumer end-to-end (`init` → `setup deps` → `setup
  lds` → `graduate`) verified in tempdir dogfood.
```

Alternative considered and rejected: add a `## Round 4` heading with
this as `step-16`. Wrong shape — Round 3 closed cleanly at step-15
with the explicit roadmap note "Round 3 closes here." A backlog
follow-up is not Round 4. The in-place mark is the honest record:
"Round 3 shipped the seam, this backlog unblock landed later."

A separate alternative: a `## Backlog completed` section. Rejected
for v1 because exactly one backlog item is shipping; one entry in its
own section is more bureaucratic than helpful. If a second backlog
item ships and the in-place ✅ pattern starts feeling crowded, lift
both into a dedicated section in that next follow-up.

## Open questions to resolve during execution

- **Configurable LDS root** (`--root <dir>`). Lean: **defer to a
  follow-up.** v1 hardcodes `engineering/` because that's LDS's own
  default per `install.md` Step 1; consumers who want `docs/` (the
  pre-2-track default) can edit the manifest after install (set
  `features.lds.root: docs`) and copy the `engineering/` tree to
  `docs/` themselves — clunky but rare. The clean implementation
  rewrites the template-author-time directory name at copy time (a
  `sed`-style path remap in the walker), which is bigger than this
  follow-up wants.

- **Migration mode** (`setup lds --migration <legacy-docs-dir>`).
  Lean: **never in plumbing.** Migration is LLM-driven (LDS itself
  calls it multi-session, `/clear` between phases). Belongs in
  porcelain — a future `wip lds-migrate` porcelain verb that drives
  the analyze → review → extract pipeline using the shaper seam from
  step-10.5. The seam from this verb: a consumer who runs `setup lds`
  ends up with everything needed to *receive* a migration into the
  layer dirs.

- **Per-layer `_template.md` shipping.** LDS's `install.md` ships
  layer-specific templates (MADR-minimal, Lean-PRD, etc.) when each
  layer is selected. v1 ships only `.gitkeep`. Lean: **defer.** A
  consumer authoring their first ADR will either copy an existing one
  or read LDS's docs; shipping a template means picking defaults that
  the LDS install workflow itself prompts the user for.

- **Should the seed manifest's `entries: []` block also include a
  `sources: {}` field?** LDS schema (per `install.md` §8b's manifest
  shape) allows it. Lean: **omit for v1.** extract-validator doesn't
  require it; emptier = simpler to write and to compare bytewise.
  Easy to add later if a consumer's tooling expects the key.

- **Should `setup lds` print the LDS install-time prompts that
  `install.md` Step 4 asks** (template per layer, tooling tier)? Lean:
  **no.** This is a deterministic plumbing verb; the install.md
  prompts are LLM-shaper territory. A future
  `wip lds-install` porcelain could drive them; this verb's job is to
  put down the bytes that don't need a decision.

- **`.gitkeep` vs `.keep`.** Both are common conventions. Lean:
  **`.gitkeep`** — same convention as the rest of this repo (`.envrc`
  / `.wip.yaml` / `.pre-commit-config.yaml` are dotfile-with-tool
  prefix; `.gitkeep` slots in). Sample check: `grep -r '\.gitkeep' .`
  on the existing tree finds zero hits (this repo has no empty dirs
  yet), so we set the precedent here.

- **What if the consumer's `.wip.yaml` already has
  `features.lds.enabled: true` and `root: docs` (different root)?**
  Lean: **exit 3 `lds-already-installed-elsewhere`** with a stderr
  hint pointing at the configurable-root open question.
  Implementation: read `features.lds.root` before any write; if it's
  set to something other than `engineering` (or unset), proceed; if
  it's explicitly `engineering` or unset, proceed. Test case for the
  refuse path.

- **Idempotency of `--sentinel-only` after a full install.** A
  consumer runs full `setup lds`, then later runs
  `setup lds --sentinel-only`. The sentinel file is byte-equal, so
  `skipped_idempotent`. Other files untouched. Lean: **just works,
  no special handling needed.** Worth one test assertion.
