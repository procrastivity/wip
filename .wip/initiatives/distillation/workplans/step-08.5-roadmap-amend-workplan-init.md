# Workplan — step-08.5 · `roadmap amend` + `workplan init`

Lands the two destination verbs that
[`engineering/specs/wip-plumbing-cli.md`](../../../../engineering/specs/wip-plumbing-cli.md)
§3 promises and that step-07.5's `intake apply` currently stubs out with exit 3.
After this step the intake pipeline is end-to-end for every terminal kind:
`brief → init`, `amendment → roadmap amend`, `workplan-seed → workplan init`.

## Decisions (made here, feed later steps)

- **Verb shape.** Top-level `roadmap` + `workplan` verbs, each with subcommands —
  `roadmap amend <slug>` and `workplan init <slug> <step-id>`. Two new
  subcommand files; the dispatcher gains them by name.
- **Layout.** New `lib/wip/wip-plumbing-amend-lib.bash` carries the rendering
  + idempotency + insertion helpers. `lib/wip/wip-plumbing-subcommands/
  roadmap.bash` and `…/workplan.bash` are thin dispatchers that own
  arg-parsing and call the lib.
- **Idempotency hash (spec §4 Q4 — bytes in v1).** The hash is computed over
  the **rendered insertion payload** (the bullet text or appended round
  block), not over the source artifact file. Identical inserts shaped from
  different artifact framings collapse to the same hash. Stamped as
  `<!-- wip-amend: <sha256> -->` on its own line, immediately after the
  inserted block. Re-apply: `grep -F` for the marker → exit 0,
  `idempotent_noop: true`, no write.
- **Amendment-body rendering rules.**
  - `insert-after step-NN`: the artifact body has a single `### step-XX —
    <title>` heading and one or more paragraphs. The bullet line is
    `- **step-XX — <title>** — <body-paragraphs joined by space>`. Inserted
    immediately after the `step-NN` bullet (and any of its continuation
    lines). The new step-id `XX` is parsed from the artifact heading, not
    from any flag.
  - `replace step-NN`: the artifact body's heading (`### step-NN — <new
    title>?`) supplies an optional new title; the rendered bullet replaces
    `step-NN`'s existing bullet line (and continuation lines). If the
    heading omits the title (`### step-NN — `), keep the existing title.
  - `append-round <title>`: the artifact body has `## Round <N> — <title>`
    + one or more `### step-NN — <title>` step headings. The full round
    block is appended at the end of the rounds region (before the first
    `## Deferred` / `## Backlog` heading, or end of file if absent). `<N>`
    is taken from the artifact; the verb does not renumber.
- **CLI flags vs. artifact directive.** Per spec, exactly one of
  `--insert-after`, `--replace`, `--append-round` is allowed. When
  `--from <file>` is given:
  - If no flag is passed, use the artifact's directive.
  - If a flag is passed and matches the artifact's directive (same kind +
    same value), accept.
  - If a flag is passed and disagrees, exit 2.
- **`roadmap amend` validates first.** Reuses
  `wip_intake_validate_kind <file> amendment`. Shape failure is exit 4
  with the validator envelope (same as `intake validate`).
- **`workplan init` slug derivation.** `--slug <override>` wins; else
  derive from the step's roadmap title via `_wip_roadmap_slugify` (already
  in roadmap-lib). Filename:
  `.wip/initiatives/<slug>/workplans/<step-id>-<derived-slug>.md`.
- **`templates/workplan.md.tmpl`.** Lands here. Sections mirror what every
  past workplan in this repo has used: Decisions / Chunks / Test strategy /
  Definition of done / Open questions. `{{slug}}`, `{{step_id}}`,
  `{{step_title}}`, `{{date}}` placeholders.
- **`--from <file>` seeding for workplan init.** When a seed is provided
  (a `workplan-seed` kind artifact), its narrative body is appended at the
  bottom under a `## Seed (from intake)` heading. The seed file is
  shape-validated via `wip_intake_validate_kind <file> workplan-seed`
  before use.
- **`intake apply` wiring.** Both `amendment` and `workplan-seed` paths
  now dispatch to the new verbs (no more exit 3). The dispatched ledger
  is wrapped in apply's standard envelope (`{ok, kind, dispatched, target,
  result}`). `target` is the resolved `<slug>` for amendment, the
  `<slug>/<step-id>` for workplan-seed.
- **Protected-path model.** Both verbs honour `--dry-run` and the
  scaffold-lib's protected-path semantics: a present workplan file aborts
  with exit 4 unless `--force`.

## Chunks

1. **template** — `templates/workplan.md.tmpl`. Update
   `templates/README.md` to drop the *(future — step-08.5)* tag.
2. **amend lib** — `lib/wip/wip-plumbing-amend-lib.bash`:
   - `wip_amend_extract_directive_from_fm <fm-json>` → emits
     `kind\tvalue` on one line (kind in `insert-after`/`replace`/`append-round`),
     empty when none.
   - `wip_amend_extract_body <file>` → emit the file's content with the
     `---`…`---` front-matter head stripped.
   - `wip_amend_render_insert_after <body>` → render `{stepid, title,
     bullet}` as a one-line JSON object. Body collapse rule: trim, join
     paragraphs with `\n  ` (markdown continuation), but the bullet's
     **first line** is `- **step-XX — Title** — <first-paragraph>`. Extra
     paragraphs become indented continuation lines under the bullet.
   - `wip_amend_render_replace <body> <existing-title>` → same as
     insert-after but reuses `<existing-title>` when the artifact heading
     omits one. Returns the bullet + continuation block.
   - `wip_amend_render_append_round <body>` → emit the literal round block
     unchanged (with a trailing blank line).
   - `wip_amend_hash <text>` → sha256 of `<text>` (no marker line).
     `shasum -a 256` (BSD) and `sha256sum` (GNU) both available; prefer
     `shasum` on macOS, fall back to `sha256sum`.
   - `wip_amend_has_marker <roadmap-path> <hash>` → 0 if marker present.
   - `wip_amend_apply_insert_after <roadmap-path> <step-id> <bullet>
     <hash>` → in-place edit: find the bullet line for `step-id`, locate
     the end of its continuation block, insert `<bullet>\n<marker>\n`.
     Bash + temp file, no `sed -i` portability traps.
   - `wip_amend_apply_replace <roadmap-path> <step-id> <bullet> <hash>` —
     analogous; replaces the existing bullet+continuation with the new
     content.
   - `wip_amend_apply_append_round <roadmap-path> <block> <hash>` —
     inserts before the first `^## Deferred` / `^## Backlog` heading or
     EOF.
3. **roadmap subcommand** —
   `lib/wip/wip-plumbing-subcommands/roadmap.bash`:
   - `wip_plumbing_cmd_roadmap` dispatches `amend` (others exit 2).
   - `_wip_roadmap_cmd_amend <slug>` parses `--from <file>` /
     `--insert-after` / `--replace` / `--append-round` (and `--dry-run`
     via global). Resolves the slug to the roadmap path through
     `.wip.yaml`. Validates artifact shape. Reconciles CLI directive vs
     artifact directive. Renders, hashes, checks idempotency, applies.
     Emits `{ok, slug, directive, wrote[], idempotent_noop}`.
4. **workplan subcommand** —
   `lib/wip/wip-plumbing-subcommands/workplan.bash`:
   - `wip_plumbing_cmd_workplan` dispatches `init`.
   - `_wip_workplan_cmd_init <slug> <step-id>` parses `--from`, `--slug`,
     `--force`. Verifies `<step-id>` exists in the initiative's roadmap
     (via `wip_roadmap_step`); else exit 4. Derives the workplan slug.
     Builds the target path. Honours `--force` against the protected-path
     rule. Renders `templates/workplan.md.tmpl`; if `--from` is set,
     validates as `workplan-seed`, appends `## Seed (from intake)\n\n<body>`
     to the rendered output. Emits
     `{ok, slug, step, wrote[]}` ledger.
5. **intake apply rewiring** — in
   `lib/wip/wip-plumbing-subcommands/intake.bash`:
   - `_wip_intake_apply_amendment` calls the new `roadmap amend` flow
     (source `roadmap.bash`, then call `wip_plumbing_cmd_roadmap amend
     <slug>` with the right flags). Wraps the ledger.
   - `_wip_intake_apply_workplan_seed` calls the new `workplan init` flow
     analogously. Slug + step-id parsed from `target:`.
   - Old `wip_die 3 not-implemented` branches go away. `spec` stays exit
     3, `handoff` stays exit 4.
6. **dispatcher wiring** — register `roadmap` + `workplan` in
   `bin/wip-plumbing`'s `case`; source `wip-plumbing-amend-lib.bash`
   alongside the other libs; update `wip_usage`.
7. **tests** —
   - `test/test-roadmap-amend.sh` — per-directive happy path; flag/artifact
     disagreement exit 2; idempotent re-apply; missing target step
     exit 4; shape failure exit 4; `--dry-run` no-writes.
   - `test/test-workplan-init.sh` — happy path; slug derivation; missing
     step exit 4; file-exists exit 4 without `--force`, success with;
     seed file appended; `--dry-run` no-writes.
   - `test/test-intake-apply.sh` — extend with two new cases: amendment
     dispatch through to `roadmap amend` ledger; workplan-seed dispatch
     through to `workplan init` ledger. Drop the two existing exit-3
     stubs.
8. **doc updates** — flip step-08.5's roadmap entry status; mark spec §4
   Q4 (idempotency hash) resolved with a one-liner; reword `intake apply`
   §3 bullets to drop the "stubbed" qualifier.

## Test strategy

Each test mints a `mktemp` repo with `.wip.yaml` + a curated `roadmap.md`
(two rounds, mixed shipped state). Tests use `WIP_NO_REGISTRY=1` +
`WIP_ROOT=<tmp>` throughout. Key cases:

**roadmap amend:**
- insert-after `step-02` writes a new bullet right after the existing
  bullet's continuation block; marker comment present; second apply is a
  no-op (`idempotent_noop: true`); third apply with whitespace-only edits
  to the artifact (changes hash) is NOT a no-op (bytes-in-v1 semantics).
- replace `step-02` swaps the bullet body; new heading text is honoured
  when the artifact provides one and preserved when it omits.
- append-round writes a new `## Round N — Title` block before the
  `## Deferred` heading; marker comment present.
- CLI flag disagrees with artifact directive → exit 2.
- Missing target step → exit 4 with `error.kind=step-not-in-roadmap`.

**workplan init:**
- `step-08.5` in fixture roadmap → writes
  `.wip/initiatives/demo/workplans/step-08.5-<slug>.md` from the template;
  ledger surfaces the path.
- `--slug short-name` overrides the derived slug.
- `--from <seed>` appends `## Seed (from intake)\n\n<body>` to the
  rendered file; seed shape-failure → exit 4.
- Pre-existing file → exit 4; with `--force` → overwrites.
- Unknown step → exit 4.

**intake apply (extended):**
- An amendment artifact targeting an existing step round-trips:
  apply ⇒ roadmap.md mutated ⇒ re-validate the modified roadmap with
  the parser ⇒ new step is parseable.
- A workplan-seed artifact round-trips: apply ⇒ workplan file written;
  seed body appears in the file.

## Definition of done

- `make check` green; all new + extended tests pass.
- `bin/wip-plumbing roadmap amend distillation --from <tmp/insert.md>`
  in dry-run prints the ledger but doesn't touch
  `.wip/initiatives/distillation/roadmap.md`. (Dogfood, dry-only — we do
  NOT mutate the real roadmap in this step.)
- `bin/wip-plumbing workplan init distillation step-08.5 --dry-run`
  prints the ledger naming
  `.wip/initiatives/distillation/workplans/step-08.5-roadmap-amend-workplan-init.md`.
  (The actual file is the one you are reading.)
- `bin/wip-plumbing intake apply` against a fixture amendment artifact
  writes through to a fixture roadmap and returns the dispatched ledger
  in the apply envelope.
- `bin/wip-plumbing intake apply` against a fixture workplan-seed artifact
  writes a workplan file and returns the dispatched ledger.
- `bin/wip-plumbing doctor` on this repo still passes.
- `bin/wip-plumbing next` on this repo now ranks step-09 first (step-08.5
  is shipped).
- Spec `wip-plumbing-cli.md` updated per "doc updates" above; the two
  step-08.5 / LDS stub notes on `intake apply` are reworded.
- Roadmap step-08.5 marked ✅ shipped.

## Open questions to resolve during execution

- **`replace`'s continuation-block boundary.** A multi-line bullet body
  ends at the next top-level bullet, the next `## ` heading, or EOF —
  whichever comes first. Lean: encode that rule once in
  `wip_amend_apply_replace`; reuse for `insert-after`'s "where does
  step-NN's block end" question. Worth a focused unit test.
- **Marker placement on `replace`.** Putting the marker after the
  replaced block matches insert-after's shape, but means a second apply
  whose artifact differs in title will both replace AND leave the prior
  marker stranded. Lean: when rewriting, strip any previous
  `<!-- wip-amend: ... -->` lines from the replaced block first.
- **`workplan-seed` and the seed-body section heading.** `## Seed (from
  intake)` is opinionated. Alternative: emit the seed as a fenced
  blockquote, or under a top-level `### Seed`. Lean: H2 section is the
  least surprising for a workplan that already uses H2 headers; keep.
