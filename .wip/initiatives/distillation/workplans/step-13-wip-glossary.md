# Workplan — step-13 · `wip glossary` assembler

Ship the deterministic plumbing verb that produces a project's effective
`.wip/GLOSSARY.md` by concatenating `templates/glossary/core.md` with the
partials whose feature is active in `.wip.yaml`.

The current `.wip/GLOSSARY.md` is a hand-authored bootstrap placeholder
pointing at its inputs (it explicitly notes "the `wip glossary` assembler
does not exist yet — until it ships, this file points at its inputs rather
than duplicating them"). Step-13 replaces that placeholder with the real
generated artifact, on this repo and any consumer's. Drift between manifest
and on-disk glossary becomes a hard gate.

The inclusion contract is already locked by `templates/glossary/README.md`
(the README's inclusion-rules table is the spec). Step-13 just makes it
executable.

## Decisions (made here, feed later steps)

- **Verb shape: `glossary {assemble, check}` (two subcommands).** Matches
  the multi-subcommand idiom already used by `intake`, `roadmap`,
  `workplan`, `template`, `project`. Splits the read+emit path from the
  drift-detection path with no flag-overloading. Future extensions
  (`glossary partials` to list the resolved roster, `glossary explain`
  to dump the inclusion ledger) drop in cleanly as new subcommands.
  Rejected: one-verb `glossary` with `--check` flag — works, but mixes
  two distinct exit-code contracts (assemble is 0 always on success;
  check is 0/4 on agreement/drift), and the porcelain/pre-commit wrappers
  read better calling `glossary check` than `glossary --check`.

- **Default output target for `assemble`: stdout.** Plumbing is stdout-first
  by design (cf. `template show` which already emits raw bytes); a verb
  that silently writes a tracked file on every invocation breaks that
  rule and surprises users running `assemble` to inspect. The regen
  recipe is `wip-plumbing glossary assemble > .wip/GLOSSARY.md` (or
  `--output .wip/GLOSSARY.md` for an atomic write that respects
  `--dry-run` and emits a JSON write ledger). `make glossary` and any
  porcelain wrapper compose either form. Rejected: writing the file by
  default — convenient for one workflow, lossy for every other.

- **`check` mode: yes, exit 4 on drift.** This is the seam that catches
  the two failure modes the bootstrap placeholder warns about: (a) a
  feature flag flipped in `.wip.yaml` without regenerating the
  glossary, (b) a hand-edit to the generated artifact. `check`
  compares the on-disk `.wip/GLOSSARY.md` (path resolved from the
  manifest's `gitignore.always_commit` row, falling back to literal
  `.wip/GLOSSARY.md`) against a fresh assemble; equal → exit 0;
  different → exit 4 with a JSON envelope naming `expected_path` and
  a unified `diff -u` body on stderr. Wired into `.pre-commit-config.yaml`
  as a new local hook so a stale glossary fails CI.

- **Inclusion-rule data lives in one bash table.** Adding `lds.md` /
  `diataxis.md` later is a one-row addition to that table — no code
  change beyond a flag-resolution helper if the predicate is novel. The
  rule rows declared in v1 (in `templates/glossary/README.md`'s
  declaration order, which is also the emit order):

  | partial file | inclusion predicate |
  |---|---|
  | `core.md` | always |
  | `orchestration.md` | `.features.orchestration.enabled == true` |
  | `solo.md` | `.features.orchestration.backend == "solo"` |
  | `lds.md` | `.features.lds.enabled == true` |
  | `diataxis.md` | `.features.diataxis.enabled == true` |

  Predicates are evaluated with `jq` against the manifest JSON (same
  `wip_manifest_json` helper `detect` uses). Order matters and is the
  table order. **`solo.md` is gated on
  `features.orchestration.backend == "solo"`, NOT on
  `features.solo.enabled`** — per ADR-0007, the orchestration backend is
  the *active binding* (a single choice); `features.solo.enabled` is
  the Solo backend's *availability flag* (toolchain installed). The
  partial follows the binding.

- **Partial-not-on-disk is a graceful skip, not an error.** If a row's
  predicate is true but `templates/glossary/<name>.md` doesn't exist
  (current state for `lds.md` and `diataxis.md`), the assembler skips
  it silently and notes the skip in the generated header's inclusion
  ledger with `status: "predicate-true; partial-not-shipped"`. This is
  the seam that makes the inclusion table future-proof: the row can
  ship before the partial does without breaking detection. A `-v` flag
  forwards the skip to stderr.

  Predicate-false rows are simply omitted (no ledger entry needed —
  inclusion ledger lists what *was included*, with one extra row for
  rule-fired-but-partial-missing because that case is opaque otherwise).

- **HTML comment markers in each partial are stripped on emit.** Each
  partial begins with a `<!-- wip glossary partial: NAME. … -->`
  block that's load-bearing for a *maintainer* of `templates/glossary/`
  (it documents the inclusion rule inline) but pure noise for a
  *consumer* reading `.wip/GLOSSARY.md` (which doesn't need to be told
  three times that solo is included only when the backend is solo —
  the generated header already says so once, with the manifest flag
  that drove it).

  Strip rule: drop the first contiguous run of lines that starts with
  `<!--` and ends with `-->` (a single multi-line HTML comment block at
  the very top, with whitespace allowed after it). Leave other comment
  blocks alone (none in current partials; future partials may have
  inline notes that should survive). The assembler then emits a single
  generated divider — `<!-- partial: <name>  source: templates/glossary/<name>.md  reason: <predicate-summary> -->` — before each partial body, so the
  document remains scannable for "where does this term come from."

- **Generated-artifact header.** First two lines of every assemble
  output:

  ```markdown
  # wip — Effective Glossary (this project)

  <!-- GENERATED by `wip-plumbing glossary assemble`. Do not hand-edit.
       Source: templates/glossary/{core,orchestration,solo}.md
       Driven by: features.orchestration.enabled=true,
                  features.orchestration.backend=solo
       Regenerate: wip-plumbing glossary assemble > .wip/GLOSSARY.md
       Verify:     wip-plumbing glossary check -->
  ```

  The two trailing lines (`Source:` + `Driven by:`) are derived from the
  inclusion ledger and update automatically when the manifest changes.
  This makes the file self-documenting: a reviewer can read it and know
  exactly which manifest field would have to change to alter the
  output. A short intro blockquote (one paragraph paraphrasing the
  README's "this is your project's effective glossary" frame) follows
  the header, then the partial bodies separated by per-partial dividers.

- **Templates dir resolution: reuse step-11's `_wip_template_dir` seam.**
  The glossary subcommand source-includes `template.bash`'s helper
  rather than duplicating it (or hoists the helper to `wip-plumbing-lib.bash`
  if its visibility needs to widen). `$WIP_TEMPLATES_DIR` override
  works identically for glossary — that's the test seam.

  Implementation note: easiest path is to lift `_wip_template_dir`
  into the shared `wip-plumbing-lib.bash`, renaming to
  `wip_templates_dir` (drop the leading underscore now that it's
  shared), and have `template.bash` call the renamed helper. Mechanical
  refactor; tests already cover the override path via
  `test-template-verb.sh`.

- **Verb is JSON-emitting only when it has structured output.**
  `assemble` (without `--output`) emits markdown bytes on stdout (no
  envelope), same shape as `template show`. `assemble --output <path>`
  emits a `{ok, wrote, partials_included[], partials_skipped[]}` JSON
  ledger. `check` emits a `{ok, drift, expected_path, partials[]}`
  JSON envelope. This keeps the contract aligned with the rest of
  plumbing (raw-bytes verbs are flagged in the spec).

- **No `/wip:glossary` slash command in v1.** The assembler is pure
  determinism; there's nothing for an LLM porcelain to add. A future
  `/wip:setup` or `make glossary` target can invoke `wip-plumbing
  glossary assemble --output .wip/GLOSSARY.md`. Documenting the verb
  in `engineering/specs/wip-plumbing-cli.md` makes it discoverable to
  both porcelains without committing to a plugin command surface that
  pays for itself.

- **Pre-commit hook for `glossary check`.** New local hook:
  ```yaml
  - id: wip-glossary
    name: wip-plumbing glossary check
    entry: bin/wip-plumbing glossary check
    language: system
    pass_filenames: false
    files: '^(\.wip\.yaml|\.wip/GLOSSARY\.md|templates/glossary/.*\.md)$'
  ```
  Runs whenever the manifest, the assembled glossary, or any partial
  changes. Exits 4 on drift. The hook only matters when the verb
  succeeds; in a worktree without `.wip.yaml` it short-circuits via the
  `wip_find_root` failure path (existing convention).

- **No `lds.md` / `diataxis.md` partials authored here.** Out of scope
  per the instructions. The assembler will pick them up automatically
  when they ship.

- **No schema changes to `.wip.yaml`.** `gitignore.always_commit`
  already includes `.wip/GLOSSARY.md`, which means the generated
  artifact stays committed even on a default-gitignored `.wip/`. No
  manifest knob is added for glossary configuration in v1.

## Chunks

1. **Lift `_wip_template_dir` → `wip_templates_dir` in
   `wip-plumbing-lib.bash`.** Drop the leading underscore, keep
   identical semantics. Update `template.bash` to call the new name.
   Existing `test-template-verb.sh` exercises both the default and
   `$WIP_TEMPLATES_DIR`-override paths; both must stay green
   unchanged.

2. **Add `lib/wip/wip-plumbing-glossary-lib.bash`.** Pure functions, no
   side effects beyond stdout/stderr:
   - `wip_glossary_rules` — emit the inclusion-rule table (one row per
     line, tab-separated: `partial<TAB>predicate-key<TAB>predicate-jq`),
     in declaration order. Single source of truth for rule data.
   - `wip_glossary_resolve <manifest-json>` — emit a JSON array of
     included partials: `[{name, source_path, reason, body_present},
     …]`. Includes rows where `body_present == false` (predicate-true
     but partial-not-shipped) so the caller can decide ledger-vs-skip.
   - `wip_glossary_strip_header <file>` — print file body with the
     top HTML comment block removed (strip from BOF; tolerant of
     leading blank lines). Implementation: `awk` state machine, BSD/GNU
     portable.
   - `wip_glossary_render <root> <manifest-json>` — print the full
     assembled markdown to stdout. Composes the generated header, the
     per-partial dividers, the stripped partial bodies.

3. **Add `lib/wip/wip-plumbing-subcommands/glossary.bash`.** The
   subcommand entry point. Subcommands:
   - `assemble [--output <path>]` — calls `wip_glossary_render`.
     With `--output`, atomic write (tmpfile + `mv`) and emit JSON
     ledger; `--dry-run` prints the ledger only. Without `--output`,
     emits markdown on stdout (raw, no envelope).
   - `check` — assemble in-memory, read on-disk `.wip/GLOSSARY.md`
     (target path derived from `gitignore.always_commit[]` or default
     `.wip/GLOSSARY.md`); compare. Equal → `{ok:true, drift:false,
     expected_path: …, partials: [{name, included|skipped, reason}, …]}`
     exit 0. Different → `{ok:false, drift:true, …}` exit 4, with
     `diff -u` to stderr.

4. **Wire `glossary` into `bin/wip-plumbing`.**
   - Source the new lib in the dispatcher's lib-load block.
   - Add `glossary` to the dispatch case.
   - Update `wip_usage` in `wip-plumbing-lib.bash` to list the verb.

5. **Spec update: `engineering/specs/wip-plumbing-cli.md`.**
   - Add `glossary assemble` and `glossary check` rows to the §1
     verb table (with the step-13 roadmap link).
   - Add a `§3 — wip-plumbing glossary` section between `template`
     and the (existing) §4 open questions. Document: reads, writes,
     exit codes, stdout shape per subcommand, inclusion-rule table,
     `$WIP_TEMPLATES_DIR` override, the strip-comment-header rule,
     the generated-header format.
   - Update the §1 non-goals list to remove `glossary` (now shipped).

6. **Regenerate this repo's `.wip/GLOSSARY.md` via the new verb.**
   `bin/wip-plumbing glossary assemble > .wip/GLOSSARY.md`. The
   bootstrap pointer-list content goes away; the file becomes the real
   concatenation (core + orchestration + solo, with the generated
   header). Diff captured in the commit body.

7. **Pre-commit hook addition.** Append the `wip-glossary` local hook
   to `.pre-commit-config.yaml`. Verify `nix develop --command
   pre-commit run --all-files` is still green.

8. **Tests (`test/test-glossary.sh`).** See *Test strategy*.

9. **Mark step-13 shipped + bump `active_step`.**
   - `.wip/initiatives/distillation/roadmap.md` step-13 bullet gets
     `✅ shipped <YYYY-MM-DD>` + one-line outcome (mention the strip
     decision, the `check` mode, the pre-commit wiring).
   - `.wip.yaml`'s `initiatives[0].active_step: step-13` → `step-14`.
   - `nix develop --command bin/wip-plumbing doctor` and `glossary check`
     both report zero drift; `make check` still green.

10. **Branch + commit + merge.** Same flow as step-12: `git checkout
    -b step-13-wip-glossary`, commit (body includes the assembled-file
    diff and a transcript of `wip-plumbing glossary check` exit 0),
    `git checkout main && git merge --no-ff step-13-wip-glossary`,
    leave the branch around.

## Test strategy

One new file, `test/test-glossary.sh`. Plain bash, sourcing
`test/helpers.sh`. All fixture work uses tempdirs; no edits to repo
content during test execution. Coverage targets:

- **Inclusion-rule data is the table.** `wip-plumbing` source-loaded;
  call `wip_glossary_rules` directly; assert the emitted rows match the
  documented declaration-order table verbatim. Adding a new partial
  requires updating this assertion — that's the contract reminder.

- **Assemble produces byte-equal output for this repo's manifest.**
  Test fixture: build a tempdir with a manifest enabling
  `orchestration` with `backend: solo` and a `templates/` dir
  containing the three current partials (or pointed at the real one
  via `WIP_TEMPLATES_DIR`). Run `wip-plumbing glossary assemble`;
  assert:
  - Output starts with the H1 + generated header (regex).
  - Includes (in this order) the core dividers naming `core`,
    `orchestration`, `solo` — the divider's `partial:` field is the
    cheap order-check; the `reason:` field includes the predicate
    name.
  - Strips the leading HTML comment block from each partial (the
    `wip glossary partial: CORE.` marker MUST NOT appear in stdout).
  - Preserves the partial body content (assert a unique sentinel
    phrase from each — e.g. "Layer rule:" from core, "Roles" h2 from
    orchestration, "Solo backend (orchestration binding)" h2 from
    solo).
  - No solo content when `orchestration.backend != "solo"`.

- **Predicate flips drop / include the right partial.** Build three
  manifests in tempdirs: (a) base; (b) `orchestration.enabled: false`;
  (c) `orchestration.backend: <not-solo>` (or absent).
  Assert assemble output for each contains/excludes the expected
  partial set by sentinel-phrase grep + ledger inspection.

- **Future-row graceful skip.** Tempdir manifest with
  `features.lds.enabled: true`. The partial `lds.md` doesn't exist on
  disk. Assert: `assemble` succeeds (exit 0), output omits any
  lds content, and `--output <path>` ledger lists `lds` under
  `partials_skipped: [{name: "lds", reason: "predicate-true;
  partial-not-shipped"}]`. With `-v`, the skip surfaces on stderr.

- **`check` happy path.** In a tempdir with a freshly-written
  `.wip/GLOSSARY.md` byte-equal to the assemble output, `check` exits
  0 with `drift: false`.

- **`check` drift detection.** Mutate the on-disk file by appending
  one line; `check` exits 4, JSON envelope has `drift: true,
  expected_path: …`; stderr contains `diff -u` output. Then *remove*
  one partial body; same exit. Then flip a feature flag (mutate
  manifest); same exit. Three drift modes — content drift, missing
  content, manifest drift — all caught.

- **`--output` ledger and atomicity.** `assemble --output <path>`
  writes the file and emits a JSON ledger with `wrote:[<path>]` and
  the partial roster. With `--dry-run`: emits the ledger only; the
  file is not written. (Atomicity test: write a tmpfile, mv it; assert
  no half-written state survives a forced exit — practical version:
  assert the verb uses `mv` not `cat >` by inspecting the source.)

- **Strip rule covers only the top block.** Add a partial fixture with
  *two* HTML comment blocks separated by a blank line and a paragraph.
  Assert: first block stripped, second block survives. Catches a
  too-aggressive `grep -v` implementation.

- **No-templates / unknown-partial graceful behavior.** Unset templates
  dir → exit 4 `no-templates`. Unparseable `.wip.yaml` → exit 4
  `bad-manifest` (reuses the standard `wip_die` path).

- **Pre-commit hook regression guard.** `test-glossary.sh` invokes
  `bin/wip-plumbing glossary check` against the repo's current state
  (no tempdir setup) and asserts exit 0 — same property the pre-commit
  hook enforces. This is the dogfood seam: if step-13's commit accidentally
  lands an out-of-sync `.wip/GLOSSARY.md`, the test fails.

Existing tests stay green. `make check` budget: one new test file
(~30 assertions), one new lib (~120 lines), one new subcommand (~80
lines), one mechanical rename in `template.bash`, one new pre-commit
hook entry, one spec section, one regenerated artifact.

## Definition of done

- `lib/wip/wip-plumbing-glossary-lib.bash` committed; pure functions,
  no side effects.
- `lib/wip/wip-plumbing-subcommands/glossary.bash` committed; dispatches
  `assemble` / `check`.
- `bin/wip-plumbing` sources the new lib and routes `glossary`.
- `lib/wip/wip-plumbing-lib.bash` exposes `wip_templates_dir` (formerly
  `_wip_template_dir` in `template.bash`); `template.bash` calls the
  renamed helper.
- `engineering/specs/wip-plumbing-cli.md` documents
  `glossary assemble` and `glossary check` in §1 + §3, and removes
  `glossary` from the §1 non-goals list.
- `templates/glossary/README.md` gets a one-line addition pointing at
  `wip-plumbing glossary assemble` as the canonical regen invocation
  (replaces the parenthetical "(eventual `wip glossary` / `wip-plumbing`
  verb)").
- `.wip/GLOSSARY.md` regenerated via the new verb; the bootstrap
  pointer-list content is replaced with the real concatenated glossary
  + generated header.
- `.pre-commit-config.yaml` gains the `wip-glossary` local hook;
  `nix develop --command pre-commit run --all-files` exits 0.
- `test/test-glossary.sh` committed and green under `nix develop
  --command make check`.
- All previously-passing tests still pass (no regressions). Existing
  `test-template-verb.sh` covers the helper rename.
- `nix develop --command bin/wip-plumbing doctor` reports zero drift.
- `nix develop --command bin/wip-plumbing glossary check` exits 0
  on the committed tree.
- `.wip/initiatives/distillation/roadmap.md` step-13 bullet marked
  `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
- `.wip.yaml`'s `initiatives[0].active_step: step-13` → `step-14`.
- Branch + commit + merge into `main` (no-ff merge commit, matching
  the pattern step-09 / step-10 / step-10.5 / step-11 / step-12 used).
- Commit body includes:
  - A `git diff` snippet showing the old bootstrap placeholder content
    replaced by the generated artifact.
  - A transcript of `bin/wip-plumbing glossary check` exiting 0 against
    the committed tree (dogfood proof).

## Open questions to resolve during execution

- **Should `assemble` ever default to writing `.wip/GLOSSARY.md`?**
  Lean: **no** (decision above). But a developer-ergonomics counter is
  real: nine out of ten invocations *will* be regen-to-disk. Mitigation:
  add `make glossary` target whose body is the redirected command, so
  the common case is one short word. If the friction proves real in
  practice, flipping the default is one-line backwards-incompatible
  later (`--stdout` becomes the explicit opt-in).

- **`check`: should it offer `--fix` to write the on-disk file?** Lean:
  **no for v1**. `check` is a verification verb; mixing it with a
  fix-path conflates the contract (cf. `doctor --fix` which we
  explicitly kept advisory in v1). The fix invocation is one line
  (`assemble --output .wip/GLOSSARY.md`); pre-commit could even
  surface it in the failure message. Worth revisiting if the hook
  ends up causing frequent developer friction.

- **Where does the assembler's intro blockquote live?** Either inline in
  the renderer (one heredoc in `wip_glossary_render`) or as a template
  file `templates/glossary/_intro.md`. Lean: **inline heredoc** for
  v1. The intro is two sentences and is conceptually metadata about
  the assembly process, not glossary content. Promoting it to a
  template adds a file with one user (the assembler) and zero
  flexibility. Revisit if a consumer needs to customize.

- **`gitignore.always_commit` lookup for the on-disk path in `check`.**
  Lean: **read from manifest, fall back to literal `.wip/GLOSSARY.md`
  if the row is absent**. The manifest schema doesn't formally promise
  `.wip/GLOSSARY.md` will always be in `always_commit`, but every
  template and existing example puts it there. A future schema change
  to make the glossary path configurable would land naturally on the
  manifest read.

- **Should the generated-header `Source:` line list partial files
  *with paths* or *with names*?** Lean: **paths**
  (`templates/glossary/{core,orchestration,solo}.md`) with brace
  expansion when there's a common prefix (i.e. always, for now). Names
  alone require the reader to know the partials directory; paths are
  copy-paste-able into a `cat` invocation. The brace-expanded form
  collapses cleanly even when six partials land.

- **Is the strip rule on the partial's leading HTML comment too clever?**
  Lean: **emit + strip is right for v1; revisit if a partial author
  wants a comment block to survive into the output**. The alternative
  (preserve markers) means the generated glossary contains three
  comment blocks each saying "wip glossary partial: NAME. Included
  ONLY when … " — accurate but redundant given the generated header
  already records the inclusion rule once. If a future partial author
  needs an above-fold comment that survives, the strip rule can change
  to "strip only blocks matching `<!-- wip glossary partial:.*-->`"
  — a targeted regex rather than a positional rule.

- **`check` diff output: include in JSON envelope or stderr-only?**
  Lean: **stderr-only diff body, JSON envelope holds `expected_path` +
  `actual_path` (= the on-disk file) + a `byte_diff_count` for cheap
  shell-side branching**. Embedding the diff in JSON requires escaping
  and bloats the envelope for the success path. Pre-commit's failure
  output is the diff on stderr — a familiar shape.

- **Does the new verb need a `--project <id>` arg-prelude pass?**
  Lean: **inherits the existing one for free**. The dispatcher strips
  `--project` from argv before the verb sees it (cf. lines 74–107 of
  `bin/wip-plumbing`); `glossary` reads the resolved `WIP_ROOT` like
  every other verb. No code in `glossary.bash` references the flag.

- **Should `wip_templates_dir` move up to `wip-plumbing-lib.bash`, or
  stay in `template.bash` and be source-included by `glossary.bash`?**
  Lean: **move up**. Two consumers (template, glossary) and a likely
  third when LDS lands. The helper is six lines of pure resolution —
  the right home for it is the shared lib. Refactor is mechanical and
  pinned by the existing `test-template-verb.sh` override case.
