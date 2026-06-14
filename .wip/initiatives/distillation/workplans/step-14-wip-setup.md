# Workplan — step-14 · `wip setup` family

Ship the install-time deterministic verbs that scaffold a consumer repo's
tooling per capability. step-09 hand-bootstrapped these files **for this
repo** (`.envrc`, `flake.nix`, `flake.lock`, `.pre-commit-config.yaml`,
Makefile additions); step-14 systematizes the bootstrap into five plumbing
verbs anyone can run against a fresh repo and end up with the same
toolchain shape.

These are the **w2** verbs of Round 3 ("was the bulk of w2" in the roadmap
note). Each verb:

- Writes verbatim scaffold files from `templates/setup/<capability>/`
  into the consumer repo (no `{{key}}` substitution — these are
  infrastructure files, not artifacts).
- Is **idempotent**: re-running against a byte-equal file is a silent
  skip; against a differing file refuses (exit 4) unless `--force`;
  against an absent file writes.
- Flips the matching `features.<name>.enabled` flag in `.wip.yaml`
  (creating the block if absent), where the capability maps to a tracked
  feature.
- Verifies the post-write sentinel exists per the feature's contract.
- Emits a JSON write ledger on stdout.

## Decisions (made here, feed later steps)

- **`setup` is plumbing, not porcelain.** Every verb is a deterministic
  file write — there is no LLM judgment to add. The porcelain proxies
  through `bin/wip` automatically (step-10's `exec`-through pattern).
  Spec lands in `wip-plumbing-cli.md` §1 (verb table) and §3 (per-verb
  contracts). Rejected: a `wip setup` porcelain entry — nothing for a
  shaper to do here.

- **Five subcommands; no aggregator.** Verbs:
  `setup deps`, `setup direnv`, `setup hygiene`, `setup release`,
  `setup agents`. Each is one capability; consumers pick what they need.
  Rejected for v1: a `wip-plumbing setup` no-arg form that runs all five
  in sequence — easy to add later, but commits to an order today (does
  `direnv` run before `deps`? what's the order when half are already
  installed?) without a user-driven shape. The five named verbs compose
  by hand for now.

- **Composition over chaining.** Verbs that depend on another verb's
  output **error with a hint**, not silently chain. Concretely:
  `setup direnv` exits 3 `missing-prereq` with
  `"hint: run \`wip-plumbing setup deps\` first"` when `flake.nix` is
  absent (since `.envrc` says `use flake`). Rationale: a chaining
  contract hides side effects and tangles per-verb idempotency — five
  separate idempotent verbs are easier to reason about than one verb
  that conditionally writes seven files. Open question: should `setup
  hygiene` similarly precondition on a Makefile? Lean **no** — the
  hygiene config is a self-contained file (`.pre-commit-config.yaml`)
  with no path dependency on the consumer's Makefile.

- **Idempotency: three-way write-or-skip-or-refuse.** Per destination
  file:
  - **Absent** → write the template bytes; record `wrote`.
  - **Present and byte-equal to template** → silent skip; record
    `skipped_idempotent`.
  - **Present and differs** → **exit 4** (`content-drift`) with the
    differing path in the envelope, unless `--force` is passed (then
    overwrite; record `wrote_forced`).

  This extends step-07's `wip_scaffold_write_or_skip` (which is a
  two-way absent-vs-present check; everything present is skipped, no
  content comparison). The new helper `wip_setup_write_idempotent
  <template> <dest>` lives in a new `lib/wip/wip-plumbing-setup-lib.bash`.
  Why a new helper and not extend the existing one: `init`'s
  protected-path model is *intentionally* coarse — initiative briefs
  diverge from the template the moment a human starts writing, and
  re-running `init` must not propose to overwrite them. `setup`'s files
  are *infrastructure templates* that should stay byte-equal to the
  source — different contract, different helper.

- **`--force` flag** per verb. Without it, content drift exits 4 with
  the offending path in the envelope so the consumer can `diff` it. The
  pre-commit gate on the consumer side (when they later run their own
  `setup` verbs in CI) becomes the seam that catches accidental
  edits to vendored infrastructure.

- **Feature-flag flipping.** Each verb (where applicable) flips its
  feature's `enabled` field in `.wip.yaml`. Creates the block under
  `features:` if absent; otherwise sets `enabled: true` idempotently.
  Done with `yq -i` per the existing `_wip_init_append_initiative`
  pattern in `init.bash:257`. Map:

  | Verb | Feature flag flipped | Manifest changes |
  |---|---|---|
  | `setup deps` | (none) | none — deps is foundation infrastructure, no feature block represents it. The `flake.nix`/`flake.lock` are deps' own sentinels for the verb but the manifest doesn't track it. |
  | `setup direnv` | `features.direnv.enabled: true` | flips/creates the block (sentinel: `.envrc`, already declared in `wip-plumbing-lib.bash`'s sentinel map) |
  | `setup hygiene` | (none — v1) | none. Hygiene is meta-infrastructure; the `.pre-commit-config.yaml` is its sentinel for the verb. Open question: add `features.hygiene` later — punt for v1. |
  | `setup release` | `features.changelog.enabled: true` | flips/creates the block (sentinel: `CHANGELOG.md`) |
  | `setup agents` | `features.orchestration.enabled: true` + `features.orchestration.backend: solo` | flips/creates the orchestration block per ADR-0007. The Solo binding is the default backend; consumers who need another backend edit the block after install. |

  Rationale for omitting flags on `deps` and `hygiene`: neither has a
  matching entry in `wip-plumbing-lib.bash`'s sentinel map (the
  detection ground truth), so there is nothing for `doctor` to verify.
  Adding a feature block now means inventing a sentinel and a doctor
  rule for it — premature.

- **JSON write ledger on stdout** (every verb), shape:
  ```json
  {
    "ok": true,
    "verb": "setup direnv",
    "wrote": [".envrc"],
    "skipped_idempotent": [],
    "wrote_forced": [],
    "refused": [],
    "manifest_updated": ".wip.yaml",
    "sentinel": ".envrc",
    "sentinel_present": true
  }
  ```
  Mirrors `init`'s ledger shape, with two additions: `skipped_idempotent`
  (rather than `skipped_protected` — different semantics) and the
  `sentinel` / `sentinel_present` pair so a downstream pipeline can
  branch on whether the verb actually activated the feature.

- **Sentinel verification post-write.** After all writes, the verb
  re-reads its sentinel(s) from the same `_wip_feature_records` map
  `detect`/`doctor` use. If the manifest says `features.direnv.enabled:
  true` but `.envrc` is absent after `setup direnv`, the verb exits 1
  `internal` (this is a bug; the template's sentinel doesn't match the
  template's writes). Drives the test invariant: `setup <X>` always
  results in `doctor` seeing zero drift for the X feature.

- **Templates under `templates/setup/<capability>/`.** Verbatim file
  copies. New tree:
  ```
  templates/setup/
    deps/
      flake.nix
      flake.lock
    direnv/
      .envrc
    hygiene/
      .pre-commit-config.yaml
    release/
      cliff.toml
      CHANGELOG.md
    agents/
      .claude-plugin/
        plugin.json
        commands/{next,status,intake}.md
        agents/{orchestrator,coordinator,researcher,builder}.md
        README.md
  ```
  No `{{key}}` substitution — these are infrastructure bytes. The
  walker in the subcommand iterates the template subdirectory and writes
  each file to its corresponding consumer-root path.

- **Templates are byte-derived from step-09's hand-authored files.**
  Concrete derivation rule:
  - `templates/setup/deps/flake.nix` ← `flake.nix` (verbatim)
  - `templates/setup/deps/flake.lock` ← `flake.lock` (verbatim; see
    next decision for the per-consumer drift handling)
  - `templates/setup/direnv/.envrc` ← `.envrc` (verbatim)
  - `templates/setup/hygiene/.pre-commit-config.yaml` ←
    `.pre-commit-config.yaml` (verbatim)
  - `templates/setup/release/cliff.toml` ← new (sensible git-cliff
    default; see Open questions)
  - `templates/setup/release/CHANGELOG.md` ← new (5-line
    `## [Unreleased]` stub)
  - `templates/setup/agents/.claude-plugin/**` ← `.claude-plugin/**`
    **with one substitution**: `bin/wip-plumbing` →
    `wip-plumbing` (paths in plugin command/agent files). This repo's
    own `.claude-plugin/` keeps `bin/wip-plumbing` because it's the dev
    repo, not an installed wip; consumer copies expect `wip-plumbing`
    on PATH.

- **`flake.lock` is "write if absent, never compare".** Locks evolve
  per-consumer (`nix flake update` rolls inputs forward); a byte-equal
  idempotency check on the lock would fail the moment a consumer
  updates. Special-cased in the helper: if the destination already
  exists, skip silently *regardless* of content (record under
  `skipped_idempotent` even if bytes differ). `--force` still
  overwrites, but the consumer should rarely need it. All other files
  use the strict three-way check.

- **`setup agents` vendors the full plugin into the consumer.** The
  plugin source-of-truth is `.claude-plugin/` in this repo; the verb
  copies the contents into `<consumer>/.claude-plugin/`, with the
  `bin/wip-plumbing` → `wip-plumbing` substitution above. Drift is
  acceptable for v1: a consumer who edits a vendored command file gets
  exit 4 on re-run, and decides. Rejected for v1: a centrally-installed
  `wip` plugin that consumers reference symbolically. Possible later
  (one `--source-link` flag would do it), but Claude Code's plugin
  discovery model is per-project today; vendoring matches what users
  expect.

- **`setup hygiene` writes `.pre-commit-config.yaml` only.** Not the
  Makefile. Consumers add their own `hooks` and `glossary` targets;
  the verb prints a one-line hint to stderr ("Add `make hooks` to
  install the pre-commit hooks; add `make glossary` if you use wip
  glossary"). Rejected for v1: appending to an existing Makefile —
  too clever and brittle (which lines? in what order? what if the
  user already has a `hooks` target?). Rejected for v1: writing a
  `make/wip-hygiene.mk` and asking the consumer to `include` it —
  cleaner but two-step and arguably premature.

- **`setup release` writes `cliff.toml` + `CHANGELOG.md`; does NOT
  add `git-cliff` to the flake.** Locking the release toolchain
  belongs to the consumer's `setup deps` choice. The flake template
  in `setup deps` does not include `git-cliff` (matching this repo's
  current minimal flake); a consumer who wants the cliff CLI in their
  devShell edits `flake.nix` after install. Documented in the
  `setup release` stderr hint. Symmetric to how step-09 deliberately
  left `git-cliff` out of `flake.nix` ("Lean: no, not yet").

- **Spec home: §1 verb table + §3 per-verb contracts.** Five new rows
  in the §1 table (link to roadmap step-14). One new `§3 — wip-plumbing
  setup` section between `glossary` and (existing) §4 open questions.
  Documents per verb: reads, writes, exit codes, stdout shape, the
  `--force` flag, the sentinel post-check.

- **`--project <id>` forwarding works automatically.** Dispatcher
  prelude strips `--project` from argv (`bin/wip-plumbing:74-107`); the
  subcommand reads the resolved `WIP_ROOT`. Same free inheritance as
  every other verb.

- **`--dry-run` semantics.** Per existing convention: print the ledger
  (including what *would* be written, skipped, and refused) and touch
  nothing — neither files nor manifest. Same flag, same `WIP_DRY_RUN=1`
  env path as every other verb.

- **No new env vars.** `WIP_TEMPLATES_DIR` already exists (test seam +
  install seam) and gets reused identically. The setup subcommand
  reads `wip_templates_dir` (already in the shared lib post step-13).

- **Pre-commit hook for setup-file drift**: NO new hook in v1. The
  three-way idempotency check is on the *write side* (`setup` verb);
  catching post-install hand-edits to vendored infrastructure is the
  consumer's policy. Open question lean: revisit if accidental edits
  prove a problem in practice.

## Chunks

1. **Add `lib/wip/wip-plumbing-setup-lib.bash`.** Pure functions:
   - `wip_setup_write_idempotent <template-path> <dest-path>` — the
     three-way write-or-skip-or-refuse. Returns:
     - 0 + echoes `wrote`     — destination was absent
     - 0 + echoes `skipped`   — present and byte-equal
     - 0 + echoes `wrote_forced` — present, differs, `WIP_SETUP_FORCE=1`
     - 4 + echoes `refused`   — present, differs, force off
     - 2 + echoes `error`     — I/O failure (mkdir/write)
   - `wip_setup_write_or_skip_present <template-path> <dest-path>` —
     the lock-style "skip if present, never compare" variant for
     `flake.lock`.
   - `wip_setup_set_feature_flag <manifest> <feature> <kv-pair>...` —
     idempotently set `features.<feature>.<k>: <v>` per kv-pair. Uses
     `yq -i`. Creates `features.<feature>` block if absent. Pattern
     adapted from `_wip_init_append_initiative`.
   - `wip_setup_set_orchestration_block <manifest>` — special-case
     for `setup agents`: sets `features.orchestration.enabled: true`
     and `features.orchestration.backend: solo` and `features.orchestration.source: plugin`. Mirrors this repo's `.wip.yaml` shape.
   - `wip_setup_walk_template_tree <template-root> <dest-root>` —
     find every regular file under `<template-root>`, compute its
     relative path, and call the appropriate write helper for each
     destination. Yields one `<status><TAB><relpath>` line per file
     on stdout for the subcommand to fold into the ledger.

2. **Add `lib/wip/wip-plumbing-subcommands/setup.bash`.** The
   dispatcher. Routes:
   - `setup deps`
   - `setup direnv`
   - `setup hygiene`
   - `setup release`
   - `setup agents`
   - Unknown subcommand → exit 2 `usage`.

   Each verb's body is short: resolve the template root
   (`wip_templates_dir`/`setup/<verb>`); call `walk_template_tree`;
   apply the verb's preconditions and post-checks; flip the feature
   flag (if applicable); emit the ledger.

3. **Author `templates/setup/<capability>/` files.**
   - `deps/flake.nix` ← `cp flake.nix templates/setup/deps/`
   - `deps/flake.lock` ← `cp flake.lock templates/setup/deps/`
   - `direnv/.envrc` ← `cp .envrc templates/setup/direnv/`
   - `hygiene/.pre-commit-config.yaml` ←
     `cp .pre-commit-config.yaml templates/setup/hygiene/`
   - `release/cliff.toml` ← author a sensible default (conventional
     commits, keep-a-changelog format, sections per commit type;
     ~40 lines). See Open questions for the exact cliff config shape.
   - `release/CHANGELOG.md` ← author a 5-line stub:
     ```markdown
     # Changelog

     ## [Unreleased]

     _Add your changes here._
     ```
   - `agents/.claude-plugin/**` ← `cp -R .claude-plugin templates/setup/agents/` followed by a `find … -exec sed -i 's,bin/wip-plumbing,wip-plumbing,g' {} +` pass over the command/agent markdown files. The `bin/wip-plumbing` references in the live plugin are visible in `.claude-plugin/commands/next.md:17`, `status.md`, `intake.md`. README.md and plugin.json have no such references; pass through verbatim.

4. **Wire `setup` into `bin/wip-plumbing`.**
   - Source the new lib in the dispatcher's lib-load block.
   - Add `setup` to the dispatch case (line 110).
   - Update `wip_usage` in `wip-plumbing-lib.bash` to list the verb
     with its five subcommands and the `--force` flag.

5. **Spec update: `engineering/specs/wip-plumbing-cli.md`.**
   - Add 5 rows to the §1 verb table (one per subcommand), each
     linking to step-14.
   - Add a `§3 — wip-plumbing setup` section between `glossary` and
     §4. Document the contract per verb (reads, writes, exit codes,
     stdout JSON shape) and the shared idempotency rule.
   - Update §1's "Non-goals for v1: `setup`, ..." line to remove
     `setup` (now shipping).

6. **Tests (`test/test-setup.sh`).** See *Test strategy*.

7. **Dogfood: assert the templates are byte-derived from this repo's
   files.** Inside `test-setup.sh`, `cmp` each derived template
   against the live repo file:
   - `cmp templates/setup/deps/flake.nix flake.nix`
   - `cmp templates/setup/deps/flake.lock flake.lock`
   - `cmp templates/setup/direnv/.envrc .envrc`
   - `cmp templates/setup/hygiene/.pre-commit-config.yaml .pre-commit-config.yaml`

   Skips the `agents/` tree (the `bin/wip-plumbing` → `wip-plumbing`
   substitution makes it deliberately divergent; the divergence is
   asserted positively in a separate test case).

8. **Dogfood: full-repo regeneration round-trip in a tempdir.**
   Inside `test-setup.sh`, build a fresh tempdir with
   `wip-plumbing init`, then run each `setup <verb>` against it, then
   `cmp` each written file against this repo's live file. This is the
   "delete and reinstall" property check the task brief asks for —
   without actually deleting anything in the live repo.

9. **Mark step-14 shipped + bump `active_step`.**
   - `.wip/initiatives/distillation/roadmap.md` step-14 bullet gets
     `✅ shipped <YYYY-MM-DD>` with a one-line outcome (verbs shipped,
     idempotency contract, dogfood test passing).
   - `.wip.yaml`'s `initiatives[0].active_step: step-14` → `step-15`.
   - `bin/wip-plumbing doctor` and `glossary check` both report zero
     drift; `make check` stays green.

10. **Branch + commit + merge.** Same flow as step-13: `git checkout
    -b step-14-wip-setup`, commit (body includes the dogfood
    transcript — tempdir round-trip exit 0 + `cmp` no-output for each
    derived template), `git checkout main && git merge --no-ff
    step-14-wip-setup`, leave the branch.

## Test strategy

One new file, `test/test-setup.sh`. Plain bash, sources
`test/helpers.sh`. All fixture work in tempdirs; no edits to repo
content during test execution.

Coverage targets:

- **Each verb writes the expected file set to a fresh tempdir.** For
  each of the five verbs:
  - Build a tempdir with a minimal `.wip.yaml` (from
    `templates/wip.yaml.tmpl` or hand-written stub).
  - Run `WIP_ROOT=$tmp bin/wip-plumbing setup <verb>`.
  - Assert exit 0, ledger `ok:true`, `wrote` includes the expected
    relpaths, `skipped_idempotent` is empty, `refused` is empty.
  - Assert the resulting files exist and are byte-equal to the
    template (`cmp`).

- **Each verb is idempotent (silent skip on re-run).** Run twice; on
  the second run, assert `wrote` is empty, `skipped_idempotent`
  contains every previously-written file, `refused` is empty, exit 0.

- **Content drift refuses without `--force`.** Write the verb's
  output, then mutate one file (append a byte), then re-run the verb.
  Assert exit 4, ledger `ok:false`, `error.kind == "content-drift"`,
  the mutated path is named in the envelope.

- **`--force` overwrites on drift.** Same setup, then run with
  `--force`. Assert exit 0, `wrote_forced` contains the mutated file,
  post-condition: file matches template again.

- **Feature flag flipping.** For verbs that touch the manifest:
  - `setup direnv`: assert `.wip.yaml`'s `features.direnv.enabled`
    becomes `true`; running on a manifest that already has it true
    is a no-op (ledger `manifest_updated: null`).
  - `setup release`: same for `features.changelog.enabled`.
  - `setup agents`: assert `features.orchestration.{enabled, backend,
    source}` all flip per ADR-0007.

- **Sentinel post-check passes.** For each verb that maps to a
  sentinel-bearing feature: after running, `bin/wip-plumbing doctor`
  on the tempdir reports zero drift for that feature.

- **Composition: `setup direnv` errors without `flake.nix`.** Fresh
  tempdir, no `setup deps` first; run `setup direnv`; assert exit 3,
  `error.kind == "missing-prereq"`, `error.message` mentions `setup
  deps`.

- **`flake.lock` skip-if-present.** Run `setup deps` twice; on the
  second run, mutate `flake.lock` (append a line) **between** runs;
  the second run must silently skip (record under
  `skipped_idempotent` regardless of content drift). Only `--force`
  overwrites.

- **`--dry-run` touches nothing.** For each verb, run with
  `--dry-run`; assert the ledger lists the expected `wrote` paths but
  no file on disk changed and the manifest is byte-equal pre/post.

- **Template fidelity dogfood.** Direct file-level `cmp` between
  `templates/setup/{deps,direnv,hygiene}/*` and the repo root's live
  files. If step-09's `.envrc` ever changes hand without the template
  changing, this test catches it.

- **Plugin template substitution check.** Grep
  `templates/setup/agents/.claude-plugin/commands/*.md` for the literal
  string `bin/wip-plumbing` — must be absent. Grep for `wip-plumbing` —
  must be present. The substitution rule is enforced.

- **Full-repo dogfood round-trip.** One tempdir, `init` it, run all
  five `setup` verbs in order (deps → direnv → hygiene → release →
  agents); assert each file matches its live equivalent (or, for
  `agents/`, matches the substituted form).

Existing tests stay green. `make check` budget: one new test file
(~40 assertions), one new lib (~120 lines), one new subcommand
(~150 lines), one new spec section, one new template subtree
(`templates/setup/`), one updated usage block.

## Definition of done

- `lib/wip/wip-plumbing-setup-lib.bash` committed; pure functions, no
  side effects beyond the named writes.
- `lib/wip/wip-plumbing-subcommands/setup.bash` committed; dispatches
  the five subcommands.
- `bin/wip-plumbing` sources the new lib and routes `setup`.
- `bin/wip-plumbing` usage lists `setup` with all five subcommands and
  the `--force` flag.
- `templates/setup/<capability>/` populated for all five verbs per the
  byte-derivation rule in Chunk 3.
- `engineering/specs/wip-plumbing-cli.md` documents the five subcommands
  in §1 and adds a `§3 — wip-plumbing setup` section; the §1 non-goals
  line drops `setup`.
- `test/test-setup.sh` committed and green under `nix develop --command
  make check`.
- All previously-passing tests still pass.
- `nix develop --command bin/wip-plumbing doctor` reports zero drift.
- `nix develop --command bin/wip-plumbing glossary check` exits 0.
- `nix develop --command pre-commit run --all-files` exits 0.
- `.wip/initiatives/distillation/roadmap.md` step-14 bullet marked
  `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
- `.wip.yaml`'s `initiatives[0].active_step` advanced to `step-15`.
- Branch + commit + merge into `main` (no-ff merge commit).
- Commit body includes the dogfood transcript: tempdir round-trip
  exit code + the `cmp` outputs (empty) for each derived template.

## Open questions to resolve during execution

- **Should `setup hygiene` also flip a `features.hygiene` flag?**
  Lean: **no for v1**. No matching entry in `_wip_feature_records`
  (which is the sentinel map `detect`/`doctor` rely on), so adding the
  flag means adding a sentinel rule and a feature record at the same
  time. Adds a manifest knob with one user (the verb itself). If
  consumers later want `doctor` to enforce "you have a
  `.pre-commit-config.yaml` declared but it's missing," we add the
  feature in a small follow-up.

- **Should `setup deps` flip a `features.devshell` flag?** Lean: **no
  for v1**. Same reasoning. The flake's *sentinel* is `flake.nix` but
  there's no feature in the current manifest tracking it; deps is
  infrastructure, not a capability.

- **`cliff.toml` content shape — handcraft or use git-cliff's default?**
  Lean: **handcraft a small conventional-commits config (~40 lines)**.
  git-cliff's `--init` default is verbose and assumes GitHub URLs; a
  trimmed config (`commit_parsers` for feat/fix/refactor/docs, a
  keep-a-changelog body template, no GitHub-specific link
  substitutions) is more portable. The exact cliff.toml lands in this
  step; document the choice inline in the file's comment header.

- **`setup agents` — vendor or centrally install?** Lean: **vendor in
  v1**. Per the decision above. The "central plugin" model is worth
  watching but Claude Code's plugin loading is per-project today, and
  asking users to install a global plugin adds a step the verb can't
  enforce. A future `setup agents --source-link <path>` flag would let
  power users opt into a symlink-to-central install in one line.

- **Should `setup agents` substitute `bin/wip-plumbing` →
  `wip-plumbing` at write time or at template-author time?** Lean:
  **template-author time** (Chunk 3 derives the template once via
  `sed`, and the verb just copies the bytes). Simpler verb,
  byte-equal idempotency works trivially. Rejected: a runtime
  template engine that handles substitution per write — adds
  complexity for one substitution.

- **What's the right error envelope for content drift?** Lean: shape:
  ```json
  { "ok": false, "verb": "setup direnv",
    "error": { "code": 4, "kind": "content-drift",
               "message": ".envrc differs from template; re-run with --force to overwrite",
               "path": ".envrc" } }
  ```
  Mirrors `wip_die`'s envelope shape (the standard error shape from
  `wip-plumbing-lib.bash`). The `path` field is documented in the
  spec.

- **Should `setup` verbs ever write into a subdirectory other than the
  repo root?** Lean: **all writes are root-relative for v1**. No need
  for a `--target <dir>` flag — the verb writes per the template's
  internal layout (e.g. `.claude-plugin/` becomes `<root>/.claude-plugin/`).
  If a future capability needs a non-root install path (e.g. a
  `.github/workflows/` writer), the spec adds it under that verb's
  contract; not relevant to the five v1 verbs.

- **Does `setup` need to call `init` first if `.wip.yaml` is absent?**
  Lean: **no — exit 3 with a hint to run `init` first**. Same composition
  rule as `setup direnv` → `setup deps`. `setup` mutates the manifest;
  a verb that silently scaffolds a manifest as a side effect surprises
  users. Documented exit shape: `exit 3 missing-manifest`.

- **`setup agents` + ADR-0007: do we also create `features.solo`?**
  Lean: **yes, set `features.solo.enabled: true` if the block exists,
  but DO NOT create the block if absent**. Per ADR-0007, the
  `features.solo` block is Solo backend *availability* and carries
  backend-specific config (`agent_tier_policy`). Creating it from
  scratch means picking a default `force_tier`, which is a real
  decision the consumer should make. Better: the verb flips
  `features.orchestration.{enabled, backend: solo, source: plugin}`
  and prints a stderr hint that the consumer should configure
  `features.solo.agent_tier_policy` themselves. Open question is
  worth a second pass during implementation — if the manifest schema
  *requires* `features.solo` for the orchestration binding to resolve,
  we create it with a `force_tier: medium` sane default.

- **Should `setup` verbs print a final "what's next" hint to stderr?**
  Lean: **yes, one line per verb on success**:
  - `setup deps`: `"hint: run \`direnv allow\` then \`make check\`"`
  - `setup direnv`: `"hint: run \`direnv allow\` to activate the
    devShell"`
  - `setup hygiene`: `"hint: add \`make hooks\` to install the
    pre-commit hooks"`
  - `setup release`: `"hint: edit \`cliff.toml\` then \`git cliff -o
    CHANGELOG.md\` on tag"`
  - `setup agents`: `"hint: restart Claude Code to load the wip
    plugin"`
  Matches the spec's "stderr is human-readable diagnostics" rule.
  Suppressed by `-q`.
