# Workplan — step-07 · `init` + `intake validate` v0

Implements the `init` and v0 `intake validate` verbs from
[`engineering/specs/wip-plumbing-cli.md`](../../../../engineering/specs/wip-plumbing-cli.md)
on top of the step-06 / step-06.5 plumbing core. The generalized intake pipeline
(`classify` / per-kind `validate` / `apply`) slots in at step-07.5 and is gated on
step-08.5; this step ships only the **single-kind** v0 validator (parseable +
title + goal/summary), plus the `init` scaffold the eventual `intake apply` will
dispatch to.

## Decisions (made here, feed later steps)

- **Layout:** new `lib/wip/wip-plumbing-subcommands/init.bash`
  (`wip_plumbing_cmd_init`) and `lib/wip/wip-plumbing-subcommands/intake.bash`
  (`wip_plumbing_cmd_intake` dispatching to a `validate` subcommand). Shared
  scaffold helpers (template render + protected-path write) go in a new
  `lib/wip/wip-plumbing-scaffold-lib.bash` sourced by the dispatcher; reused by
  step-08.5's `workplan init`.
- **Templates:** `templates/wip.yaml.tmpl`, `templates/brief.md.tmpl`,
  `templates/roadmap.md.tmpl` ship here. `templates/workplan.md.tmpl` is
  deferred to step-08.5 (where `workplan init` lands). Templates use bracketed
  `{{slug}}`/`{{title}}`/`{{date}}` placeholders; rendering is plain `sed`
  substitution — no template engine.
- **Date stamping:** `init` writes the current `YYYY-MM-DD` (system date) into
  `brief.md` and `roadmap.md` front-matter / first-line. Tests pin the date via
  `WIP_NOW=YYYY-MM-DD` to keep fixtures stable.
- **Slug rule:** `^[a-z0-9][a-z0-9-]*$` per spec §3 (`init`). Reject anything
  else with exit 2.
- **Protected-path model:** any pre-existing file under the scaffold target is
  added to `skipped_protected[]` and **not overwritten**; `--force` (later) can
  flip this. v1 of `init` does **not** ship `--force` — if a target file exists,
  the whole call exits **4** (slug-exists) with a precise error envelope. This
  matches the spec's "no overwrite without --force" rule and keeps the
  destructive branch out of v0.
- **Manifest update:** `init <slug>` appends an `initiatives:` entry to the
  existing `.wip.yaml` via `yq` in-place; if `.wip.yaml` is absent, the repo-
  level scaffold path runs first (creates `.wip.yaml` from
  `wip.yaml.tmpl`). `current_initiative` is set only when the manifest had no
  prior initiatives.
- **Intake v0 scope:** `intake validate <file>` ships a **single, kind-less**
  shape check — parseable markdown, an H1 title, and either an `## Goal` or
  `## Summary` heading. No front-matter parsing, no per-kind dispatch, no
  classify. step-07.5 generalises and lands ADR-0009's full surface.

## Chunks

1. **scaffold lib** — `lib/wip/wip-plumbing-scaffold-lib.bash`:
   `wip_scaffold_render <tmpl> <dest> <key=val>...` (sed substitution, idempotent
   when dest already matches), `wip_scaffold_write_or_skip <dest> <content>`
   (returns 0 wrote / 1 skipped-protected), `wip_scaffold_now` (echoes
   `${WIP_NOW:-$(date +%F)}`). Pure helpers, exit-code driven, no jq.
2. **templates** — author `templates/wip.yaml.tmpl`, `templates/brief.md.tmpl`,
   `templates/roadmap.md.tmpl`. `wip.yaml.tmpl` is the minimal manifest from the
   current `.wip.yaml` (version, features stub, empty initiatives, no
   provider block — porcelain config is opt-in). `brief.md.tmpl` has H1, Goal,
   Confirmed decisions, Constraints sections. `roadmap.md.tmpl` has the
   one-growing-file layout (per ADR/scratchpad D2 = single file). Update
   `templates/README.md` to drop the *(future)* tag.
3. **`init` subcommand** — `lib/wip/wip-plumbing-subcommands/init.bash`:
   - no `<slug>` → repo-level scaffold (write `.wip.yaml` + `.wip/GLOSSARY.md`
     pointer + `.wip/backlog.md`); skip any present file (protected).
   - with `<slug>` → validate slug, ensure `.wip.yaml` exists (run repo-level
     scaffold first if not), create
     `.wip/initiatives/<slug>/{brief.md,roadmap.md}` via the scaffold lib,
     append to `initiatives:` via `yq -i`. Set `current_initiative` only if
     none was set. Exit 4 if `.wip/initiatives/<slug>/` already exists.
   - `--title <t>` overrides the templated title (defaults to a humanized
     slug). `--intake ad-hoc|structured` writes `intake:` on the new registry
     entry; v1 does no extra work for `structured` (porcelain handles it).
   - Emit the write ledger per spec §3 `init`. Honor `--dry-run` by collecting
     the same ledger but skipping writes.
4. **`intake` dispatcher + v0 `validate`** —
   `lib/wip/wip-plumbing-subcommands/intake.bash`:
   - `wip_plumbing_cmd_intake` dispatches `validate` → `_wip_intake_validate_v0`
     and rejects unknown subcommands with exit 2 (the message points at
     step-07.5 for `classify`/`apply`).
   - `_wip_intake_validate_v0 <file>`: file must be a regular readable file
     (else exit 2); read it once; parse for an H1 (`^# `) and a `## Goal` /
     `## Summary` heading using awk; emit
     `{ok:bool, file, kind:null, valid, missing:[...]}` on stdout. Exit 0 when
     valid; exit **4** when not (`missing` lists `title` / `goal-or-summary`).
   - No `--kind` flag yet (rejected with exit 2 pointing at step-07.5).
5. **dispatcher wiring** — register `init` + `intake` in `bin/wip-plumbing`'s
   `case` block; update `wip_usage` so they no longer read *(later step)*. Add
   `--intake` + `--title` and `validate` to the inline help.
6. **tests** — `test/test-init.sh`, `test/test-intake-validate.sh`. Reuse the
   plain-bash harness from step-06. Each test sets `WIP_ROOT=<tmp>`,
   `WIP_NOW=2026-06-13`, and asserts via `jq`.
7. **doc updates** — flip step-07's roadmap entry status when this lands; add a
   "v0 single-kind shape" note in spec §3 `intake validate` cross-linking
   `intake-kinds.md` (the per-kind rules step-07.5 will turn on).

## Test strategy

`WIP_ROOT=<tmp>` + `WIP_NOW=2026-06-13` for stable fixtures. Cover:

- `init` repo-level on an empty dir writes `.wip.yaml`, `.wip/GLOSSARY.md`,
  `.wip/backlog.md`; second run is an idempotent 0 (everything in
  `skipped_protected`).
- `init auth-rework --title "Auth Rework"` writes
  `.wip/initiatives/auth-rework/{brief.md,roadmap.md}`, appends an
  `initiatives:` entry, and sets `current_initiative` when none was set.
- A second `init auth-rework` exits **4** with `kind: slug-exists`.
- Bad slugs (`Auth`, `-foo`, `foo_bar`, empty) exit **2**.
- `--dry-run` emits the same `wrote[]` ledger but the disk is untouched.
- `intake validate file.md` on a valid file (`# Title` + `## Goal`) exits 0;
  missing title and missing goal/summary each emit the right `missing[]` and
  exit 4; non-existent file exits 2.
- `intake classify` / `intake apply` exit 2 with a message pointing at
  step-07.5.

## Definition of done

- `make check` green; new `test/test-init.sh` and `test/test-intake-validate.sh`
  pass.
- `bin/wip-plumbing init scratch-init` in a `mktemp` dir produces a
  `detect`-clean repo (i.e. `bin/wip-plumbing -- detect` against that dir is
  exit 0 with valid JSON and the new initiative listed).
- `bin/wip-plumbing intake validate .wip/initiatives/distillation/BRIEF.md`
  against this repo exits 0 (dogfood: BRIEF has H1 + Goal-like section). If
  it fails the title/goal check, *fix the validator*, not BRIEF.md — the brief
  is the source of truth.
- `bin/wip-plumbing doctor` on this repo still passes — no drift introduced.
- Spec `wip-plumbing-cli.md` updated per "doc updates" above.
- Roadmap entry for step-07 marked ✅ shipped with a date.

## Open questions to resolve during execution

- **`init`'s `.wip/GLOSSARY.md` content.** Stub pointer-comment for v0 (the
  real assembler is step-13)? Or generate the assembled glossary now from
  `templates/glossary/{core,solo}.md` (since this repo has `solo`)? Lean:
  pointer-comment in v0 — the assembler is its own step for a reason; doing
  it here drags step-13's scope in.
- **Manifest stub `features:`.** `init` (no slug) writes a manifest with which
  features stubbed `enabled: false`? Lean: write only `wip: {enabled: true,
  root: .wip}` — every other feature is opt-in via the `setup` family
  (step-14). Keeps `init`'s scope tight.
- **Goal-or-summary heading regex.** Strict (`^## (Goal|Summary)\b`) or
  permissive (`^##\s+(Goal|Summary|Overview)`)? Lean: strict in v0 (Goal or
  Summary, exact); revisit at step-07.5 when the per-kind rules land.
