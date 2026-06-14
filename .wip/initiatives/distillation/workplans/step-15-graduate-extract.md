# Workplan — step-15 · `graduate` / `extract`

Ship the LDS seam. Per ADR-0006 ("wip owns the seams, not the tools"), this
step adds two top-level plumbing verbs that drive **deterministic** LDS
operations on a consumer repo. The LLM-judgment phases of LDS (analyze, review)
stay in the porcelain layer.

The complication that shapes scope: LDS's "verbs" are **markdown workflow
documents** (`layered-documentation-system/{analyze,review,extract}.md`), not a
callable CLI. There is no `lds extract` binary for wip to shell out to. So
wip-plumbing has to **implement the deterministic core** of those workflows
itself — which is fine, because the deterministic core *is* the seam: it's
what gets reduced from "read this 1200-line markdown and apply it" to "run
this verb."

The full LDS Extract phase (per `extract.md`) is enormous: SHA-256 source
hashes, multi-file source concatenation, four extraction modes
(verbatim/content/transform/summarize), template variable substitution with
`field_mappings`, multi-layer manifests, status workflow (pending/reviewed/
approved), resume mode, extraction reports. Reimplementing all of it is as
large as Round 3 itself.

**v1 scope is the minimum viable LDS seam.** Two top-level verbs:

- **`graduate <artifact>`** — promote one wip-internal planning artifact (a
  workplan decision block, a spec stub, a scratch draft) to its canonical LDS
  slot (`<eng_docs_dir>/decisions/<NNNN>-<slug>.md` or
  `<eng_docs_dir>/specs/<name>.md`, etc.). Single-artifact shortcut; no
  manifest required. Pure deterministic file write.
- **`extract`** — run the deterministic Extract phase against an on-disk LDS
  extraction manifest (`<eng_docs_dir>/.lds-manifest.yaml`). v1 supports the
  `verbatim` and `content` modes only; `transform` and `summarize` are
  recognized but skipped with an `unsupported-mode` ledger entry.

The bulk of LDS's complexity (analyze, review, transform/summarize modes,
hash verification, templates) is **deferred to backlog** with reasoned cut
lines. See [Out of scope](#out-of-scope-explicit-cuts).

## Decisions (made here, feed later steps)

- **Top-level verbs, not subcommands of an `lds` namespace.** Both verbs
  live at the top level (`wip-plumbing graduate ...`, `wip-plumbing
  extract`) rather than under `wip-plumbing lds {graduate, extract}`. Two
  reasons: (a) the roadmap explicitly closes Round 3 at step-15 with no
  additional LDS verbs planned, so the namespace would have one occupant for
  a long time, and (b) Beau-facing intent reads better at the top level —
  "graduate this decision" is a verb a user types directly, not an LDS
  subcommand. The verbs *refuse* with exit 3 `lds-not-enabled` when LDS isn't
  active, so the seam binding is enforced at runtime even though the names
  aren't namespaced. Rejected: top-level `lds` with subcommands — feels
  premature for the verb count and obscures the action.
  **Lean: top-level.**

- **`graduate` is single-artifact, `extract` is bulk-from-manifest.** They're
  different shapes of the same operation ("promote content to its canonical
  LDS slot") at different scales: `graduate` operates on one
  file with a known target; `extract` operates on an approved manifest with
  many entries. The two-verb split avoids cramming "manifest mode" and "ad-hoc
  mode" into one verb with a `--manifest` flag, and matches LDS's own mental
  model (extract.md is the bulk workflow; graduating a single decision is a
  natural single-step operation).
  **Lean: two verbs, distinct contracts.**

- **`graduate` inputs: artifact + target slot.** Two ways to specify the
  target, in priority order:
  1. **Front-matter directive** in the artifact:
     ```yaml
     ---
     graduate-to: decisions/0010-graduate-seam.md
     ---
     ```
     The slot is relative to `<eng_docs_dir>` (default `engineering/`).
  2. **`--to <relative-path>` CLI flag** — overrides front-matter.

  Front-matter parsing reuses `wip_intake_extract_frontmatter` from
  `wip-plumbing-intake-lib.bash` (already shipped, three callers post-step-15).
  The directive name is `graduate-to` (mirrors the dash-joined namespacing
  of `wip-kind` in intake) so the same artifact can carry an intake `wip-kind`
  and a `graduate-to` without colliding.

  Auto-numbering: `graduate-to: decisions/auto-<slug>.md` resolves to
  `decisions/<next-NNNN>-<slug>.md` where `next-NNNN` is one above the
  highest existing 4-digit prefix in `<eng_docs_dir>/decisions/`. Falls back
  to `0001` if the directory is empty. **Decisions only** — `specs/` and
  other layers do not auto-number; the user names the file.
  Rejected for v1: auto-numbering for all layers (specs have no
  numerical-prefix convention in LDS). Rejected: front-matter
  `graduate-to: auto` (too magic — requires also reading `target-layer`
  separately to pick the directory).
  **Lean: explicit slot; `decisions/auto-<slug>.md` is the one shortcut.**

- **`graduate` write contract: three-way idempotency (same as setup).**
  Re-use the `wip_setup_write_idempotent` helper from
  `wip-plumbing-setup-lib.bash`:
  - **absent** → write artifact body to target (front-matter stripped);
    status `wrote`.
  - **byte-equal** → silent skip; status `skipped_idempotent`.
  - **differs** → exit 4 `content-drift` unless `--force`; then
    `wrote_forced`.

  This means a graduated artifact stays graduated: a re-run is a no-op, an
  edited canon file refuses to be overwritten silently. Matches the LDS
  principle that the same manifest produces identical output every time.
  Rejected: writing always with `--force` semantics — silently overwriting
  hand-edits to ADRs (which are immutable per LDS) is exactly the bug LDS's
  authority model is designed to prevent.
  **Lean: three-way + `--force`.**

- **`graduate` body transform: strip the `graduate-to` directive only.** The
  artifact body lands verbatim in the canon slot, minus the front-matter
  directive that pointed it there. No template application, no field
  substitution, no heading-level rewrite. The artifact is *already* shaped
  for its destination — that's the user's job during shaping (porcelain
  territory). If the artifact has additional front-matter keys (e.g.
  `wip-kind`, `target`), they pass through unchanged.

  Why not strip *all* front-matter? Because LDS templates allow front-matter
  themselves (cf. `templates/adr-madr-minimal.md`). The graduate verb's job
  is to remove the *wip-internal* routing key (`graduate-to`) and pass the
  rest through. Rejected for v1: full front-matter strip — would force the
  porcelain to re-add status/date/etc. that the user wrote intentionally.
  **Lean: strip `graduate-to` (and `--to` is plumbing-internal so it never
  reaches the file), preserve everything else.**

- **`extract` inputs: manifest path (default `<eng_docs_dir>/.lds-manifest.yaml`).**
  Manifest discovery is exactly the LDS sentinel — same path the doctor uses
  for `features.lds.active`. CLI override: `--manifest <path>` (test seam +
  consumer override). Reads the file via `yq`, converts to JSON once, then
  walks `entries[]` with `jq`. Same shape as `wip_manifest_json` in the
  shared lib.
  **Lean: default to sentinel; flag override.**

- **`extract` mode coverage: verbatim + content. Transform/summarize:
  recognized, skipped with `unsupported-mode`.** Per the LDS extract
  workflow §3.5:

  | Mode | v1 status | Why |
  |---|---|---|
  | `verbatim` | **supported** | Pure file/line-range read + write; deterministic by construction. |
  | `content` | **supported** | Even simpler — write `inline_content` verbatim. |
  | `transform` | skipped (ledger: `unsupported_mode`) | Built-in transforms (heading_adjust, link_rewrite, markdown_format) require shipping a small markdown engine; out of scope for v1. |
  | `summarize` | skipped (ledger: `unsupported_mode`) | Inherently LLM-driven per LDS itself ("NEVER automatic"). Belongs in porcelain. |

  A skipped entry is logged with status `unsupported_mode` (and the mode
  name in `error_kind`) but does NOT fail the run. Other supported entries
  in the same manifest still execute. The ledger records every skip so the
  consumer can see what didn't land.
  Rejected for v1: refuse the whole run on any unsupported mode — penalizes
  consumers who have a mixed manifest. Rejected: ship transform/summarize
  fully — see Out of scope below for the cost analysis.
  **Lean: partial support with explicit skip ledger.**

- **`extract` source spec coverage: simple path + single-file-with-range.
  Multi-file: refuse.** Per the LDS schema, sources can be (a) a simple
  string path, (b) `{file, start_line?, end_line?, hash?}`, or (c)
  multi-file `{files[], separator, combined_hash}`. v1 supports (a) and
  (b); (c) is skipped with `unsupported_source`. The multi-file
  concatenation engine adds bash complexity disproportionate to its likely
  use in early LDS adoption.
  Rejected: full multi-file support — defer to a follow-up step when a real
  consumer requests it.
  **Lean: simple + single-file-with-range.**

- **`extract` hash verification: NOT performed in v1.** The LDS extract
  workflow §2 requires SHA-256 hash verification of source files against
  the manifest's stored hashes before any write. v1 *parses* hash fields if
  present (does not error on them) but does **not** compute or compare. The
  ledger records `hash_verification: "skipped-v1"` so the consumer knows
  the manifest's drift-detection wasn't enforced.

  Reasoning: hash generation belongs to the `analyze` phase, which is
  LLM-driven and porcelain territory in this seam. A consumer who runs the
  full LDS workflow under Claude Code via the markdown documents will use
  LDS's own hash machinery there; once they call `wip-plumbing extract`
  against the resulting manifest, the hashes are *informational metadata*
  the deterministic verb doesn't need to re-verify in v1. The seam is honest
  about the limitation. Rejected: implement SHA-256 verify in bash — ~80
  lines, requires `sha256sum`/`shasum` (not in the minimal flake
  toolchain).
  **Lean: skip; ledger flag; add to backlog with a clear seam point.**

- **`extract` manifest validation: required-fields only.** v1 validates:
  - `metadata.schema_version` matches `1.x.x` (any 1.x); else exit 4
    `incompatible-schema`.
  - `metadata.status == "approved"`; else exit 4 `manifest-not-approved`.
  - `entries` is a non-empty list; else exit 4 `manifest-empty` (zero
    entries is an authoring bug, not an idempotent no-op).
  - Each entry has `id` (unique), `target`, `mode`, and (for non-`content`
    modes) `source`. Missing → entry-level ledger error
    `bad-entry-shape`; the run completes other entries but exits 4 at the
    end.

  Reuses `yq` + `jq` for parsing — no `ajv` / JSON schema library. The
  manifest schema in `layered-documentation-system/schemas/extraction-manifest.schema.yaml`
  is documentation, not enforced (per its own comment "This schema is
  documentation-only").
  **Lean: minimal required-fields validation; lean on yq/jq.**

- **`extract` target write contract: three-way idempotency per entry.**
  Same `wip_setup_write_idempotent` as `graduate`. An entry's target either
  writes (absent), silently skips (byte-equal), or refuses (differs, no
  --force). The whole-run exit code is 0 if every entry succeeds or
  skips, 4 if any entry refuses or fails validation. `--force` overwrites
  all drifted entries.
  **Lean: per-entry; whole-run exit is the worst-status promotion.**

- **`extract` source attribution comments: yes, per LDS §6.3.** Each
  written target starts with two HTML comments:
  ```html
  <!-- Migrated from <source-file>:<start>-<end> -->
  <!-- Extraction ID: <entry-id> -->
  ```
  For `content` mode entries (no source):
  ```html
  <!-- Generated content - no source file -->
  <!-- Extraction ID: <entry-id> -->
  ```
  These are part of the bytes written, so the idempotency check correctly
  matches against the attribution. The format is fixed (no template).
  Rejected: omit attribution for simplicity — LDS treats it as a hard
  invariant; honoring it makes the produced files round-trippable via LDS's
  own audit/sync workflows.
  **Lean: attribution mandatory; format from LDS §6.3 verbatim.**

- **LDS sentinel + feature-flag detection.** Both verbs first call
  `wip_features_json` (already in `wip-plumbing-lib.bash`) and read the
  `lds` entry:
  - `features.lds.enabled: false` → exit 3 `lds-not-enabled` with hint
    `"enable features.lds in .wip.yaml; LDS install is not in this step's
    scope (backlog: setup lds)"`.
  - `enabled: true` but `sentinel_exists: false` → exit 3
    `lds-sentinel-missing` with hint `"run the LDS install workflow to
    create <root>/.lds-manifest.yaml; LDS install is not in this step's
    scope (backlog: setup lds)"`.

  The sentinel path is the LDS root's `.lds-manifest.yaml` per the existing
  `_wip_feature_records` map (`wip-plumbing-lib.bash:138`). No new sentinel
  to add — step-15 reuses the one step-06 already declared.

  Special case for `graduate`: technically the LDS install must be present
  for the canon directory structure (`<root>/decisions/`, etc.) to exist;
  if `features.lds.enabled: true` and the sentinel is missing,
  graduate also refuses. If `enabled: true` and sentinel exists but the
  target's parent directory (e.g. `<root>/decisions/`) doesn't exist, the
  verb creates it with `mkdir -p` (same `_wip_setup_copy_atomic` helper
  used by setup).
  **Lean: reuse existing sentinel + feature record map; add no new
  sentinel.**

- **`<eng_docs_dir>` resolution.** From the manifest:
  `features.lds.root` (set when `setup lds` lands) → falls back to
  `features.lds.installs[0].root` (per the existing
  `_wip_feature_records:138` already supports both shapes) → falls back to
  `"engineering"`. This matches what `_wip_feature_records` computes today
  for the sentinel path; the verbs use the same resolver via a new
  `wip_lds_root <manifest-json>` helper in the new lib.
  **Lean: one helper, identical to sentinel resolution.**

- **No `lds` block in `_wip_feature_records` changes.** The sentinel
  rule for `lds` already exists. No detect/doctor changes in this step.
  **Lean: zero changes to step-06's detect/doctor surface.**

- **No new sentinel post-check.** Unlike `setup`, these verbs don't activate
  a feature (LDS is already enabled before the verb runs). Sentinel
  presence is a *precondition* check, not a post-check. So we drop the
  `sentinel`/`sentinel_present` pair from the ledger; the verbs emit a
  simpler shape.
  **Lean: precondition-only; no post-check.**

- **Ledger shapes.** Two JSON envelopes, both mirror the `setup` style:

  `graduate` success:
  ```json
  {
    "ok": true,
    "verb": "graduate",
    "artifact": ".wip/initiatives/distillation/scratch/foo.md",
    "target": "engineering/decisions/0010-graduate-seam.md",
    "wrote": ["engineering/decisions/0010-graduate-seam.md"],
    "skipped_idempotent": [],
    "wrote_forced": [],
    "refused": []
  }
  ```

  `extract` success:
  ```json
  {
    "ok": true,
    "verb": "extract",
    "manifest": "engineering/.lds-manifest.yaml",
    "entries_total": 3,
    "wrote": ["engineering/decisions/0001-foo.md"],
    "skipped_idempotent": ["engineering/specs/bar.md"],
    "wrote_forced": [],
    "refused": [],
    "unsupported": [
      {"id": "spec-with-transform", "mode": "transform", "reason": "transform mode not supported in v1"}
    ],
    "hash_verification": "skipped-v1"
  }
  ```

  Refused/unsupported entries do **not** mark the whole run `ok: false`
  unless `refused` is non-empty (drift means human decision required).

  Drift error (one or more entries refused):
  ```json
  {
    "ok": false,
    "verb": "extract",
    "error": {
      "code": 4,
      "kind": "content-drift",
      "message": "extracted targets differ from manifest output; re-run with --force to overwrite",
      "paths": ["engineering/decisions/0001-foo.md"]
    }
  }
  ```
  **Lean: setup-shaped ledger; manifest-not-approved is a top-level
  error envelope, content drift is also top-level error envelope (per-entry
  refused list is the supporting detail).**

- **Spec home: §1 verb table + §3 per-verb contracts.** Two new rows in §1
  (link to step-15). Two new `§3` sections after `setup`. Drops the
  `graduate`/`extract` entry from the §1 "Non-goals for v1" list. Same
  pattern as step-14's spec update.
  **Lean: spec discipline.**

- **No glossary partial added in v1.** The existing `glossary` rule table
  in `lib/wip/wip-plumbing-glossary-lib.bash` already declares `lds.md` as
  a partial-rule (predicate `features.lds.enabled == true`), with a
  graceful-skip on partial-not-shipped. Authoring the `lds.md` partial is
  a separate concern that doesn't block the verbs; skip it for now and add
  to backlog. Step-13's open question is "lds.md / diataxis.md partials
  land as one-row additions to the rule table with zero code change" — the
  rule rows are already there; just the partial files aren't.
  **Lean: defer the partial; verbs don't depend on it.**

- **`--project <id>` forwarding works automatically.** Same as every other
  verb — the dispatcher prelude strips `--project` from argv; the
  subcommand reads the resolved `WIP_ROOT`. Free inheritance.

- **`--dry-run` semantics.** Per existing convention: print the ledger
  reflecting what *would* be written/skipped/refused; touch neither files
  nor the manifest. Same `WIP_DRY_RUN=1` env path.

- **Naming convention for `graduate`'s artifact-relative paths.** The
  ledger's `artifact` field is repo-root-relative (e.g.
  `.wip/initiatives/distillation/scratch/foo.md`), not absolute. Matches
  every other verb's path-reporting style. Targets are
  `<eng_docs_dir>`-relative inside `graduate-to` but root-relative in the
  ledger.

- **`graduate` requires an LDS-active repo.** Since this very repo has
  `features.lds.enabled: false`, dogfooding `graduate` here requires either
  a tempdir or a temporary flag flip. Per the brief: tempdir. Same approach
  step-14 used. The tempdir gets a minimal `.lds-manifest.yaml` to satisfy
  the sentinel check (one line: `metadata: {schema_version: "1.0.0",
  status: "approved", entries: []}`); `graduate` doesn't read the manifest,
  but the sentinel-exists check needs the file to be present.

- **`extract` with empty entries: exit 4.** A manifest with `entries: []`
  is a v0 authoring bug, not an idempotent no-op. Exit 4
  `manifest-empty` so the user fixes the manifest. (LDS itself permits
  empty manifests in some workflow stages, but for the `extract` verb
  contract — "execute the manifest" — empty is a refusal.)

- **Out-of-band file moves vs writes.** LDS extract is a *write* operation —
  it never *moves* the source file (source stays in `legacy-docs/` for the
  audit trail; the target gets a copy with attribution). v1 follows the
  same rule. `graduate`, by contrast, is a `wip`-native verb on
  `wip`-native artifacts (`.wip/initiatives/...`), and the source artifact
  is the user's working draft — the verb does not delete or move it. The
  user decides what to do with the draft after graduating.
  **Lean: both verbs write copies; neither moves the source.**

## Out of scope (explicit cuts)

These are deferred to backlog with a one-line rationale each. The aim is
"minimum viable LDS seam" — wip-plumbing carries enough to make the seam
real and useful while keeping the surface bounded.

- **`analyze` and `review` LDS phases.** Both are LLM-driven by the LDS
  spec itself; they belong in porcelain (via the `/wip:*` plugin reading
  the LDS markdown workflows). Out of scope for plumbing.
- **`transform` mode (heading_adjust / link_rewrite / markdown_format).**
  Requires a small markdown engine and per-transform option handling.
  Backlog item: `step-15-followup-transform-mode` when a consumer needs it.
- **`summarize` mode.** Inherently LLM-driven per LDS itself; porcelain
  territory.
- **SHA-256 source hash verification.** v1 parses hash fields but does not
  verify. Backlog: `extract --verify-hashes` flag (needs `sha256sum` or
  `shasum` in the flake; add to `setup deps` template).
- **Multi-file sources (`source.files[]`).** v1 supports single-file
  sources (simple path + `{file, start_line, end_line}`). Backlog when a
  real manifest needs concatenation.
- **Template variable substitution (`field_mappings`).** LDS extract §4
  defines a `source:<path>:<lines>` reference syntax + literal value
  substitution. v1 skips templated entries with `unsupported_template`.
  Backlog when a consumer adopts MADR/PRD-Lite templates with field maps.
- **`--resume` mode for extract.** Skip-if-exists semantics on partial
  failures. Backlog — the three-way idempotency already gives a re-run a
  reasonable shape; full resume is a bigger contract change.
- **Extraction report file.** LDS §7 specifies a written
  `extraction-report.{md,yaml}` with metadata + summary + per-entry rows.
  v1 emits the ledger to stdout only; no file write. Backlog item.
- **`setup lds` verb.** step-14's setup family did not include LDS;
  consumers must hand-author `.wip.yaml`'s `features.lds` block and run
  the LDS markdown install workflow themselves. Backlog item:
  `setup lds` as a sixth setup verb that writes the install scaffold
  (`engineering/.lds-manifest.yaml`, layer directories, the
  `maintenance/` workflows). The hint in `lds-not-installed` /
  `lds-not-enabled` errors points at this backlog item.
- **`lds.md` glossary partial.** Step-13 declared the inclusion rule for it
  (predicate true → include `templates/glossary/lds.md`); the partial file
  itself was punted. Authoring it is a one-row addition to the rule table
  and ~30 lines of glossary content; backlog item:
  `glossary-partial-lds`.

Backlog rows for each go into `.wip/backlog.md` as part of this step's
commit.

## Chunks

1. **Add `lib/wip/wip-plumbing-graduate-lib.bash`.** Pure functions:
   - `wip_graduate_extract_to_directive <artifact-path>` — read
     front-matter, return `graduate-to:` value (or empty). Reuses
     `wip_intake_extract_frontmatter` from `wip-plumbing-intake-lib.bash`
     (don't re-implement YAML parsing).
   - `wip_graduate_resolve_target <eng_docs_dir> <directive>` — return the
     repo-root-relative target path. Handles
     `decisions/auto-<slug>.md` → `decisions/<next-NNNN>-<slug>.md` (scans
     `<eng_docs_dir>/decisions/` for existing 4-digit prefixes via
     `find` + `LC_ALL=C sort`).
   - `wip_graduate_strip_directive <artifact-path>` — emit the artifact
     body with the `graduate-to:` front-matter key removed. Preserves all
     other front-matter keys. Outputs to stdout.
   - `wip_graduate_next_adr_number <decisions-dir>` — scan, find max NNNN,
     return next. Empty dir → `0001`.

2. **Add `lib/wip/wip-plumbing-extract-lib.bash`.** Pure functions:
   - `wip_extract_load_manifest <path>` — `yq -o=json` to JSON; validate
     `metadata.schema_version` (1.x.x), `metadata.status` (approved),
     `entries` (non-empty list). Returns the JSON on stdout; exits with a
     status code matching the validation failure (4 + a kind string on
     stderr for the dispatcher to consume).
   - `wip_extract_entry_target <entry-json> <eng_docs_dir>` — compute the
     `<eng_docs_dir>/<target>` path.
   - `wip_extract_render_entry <entry-json> <eng_docs_dir>` — emit the
     bytes to write to the target. Dispatches on `mode`:
     - `verbatim`: read source file lines per `start_line`/`end_line`,
       prepend attribution header.
     - `content`: emit `inline_content` + attribution header.
     - `transform`/`summarize`: return non-zero with a `mode-unsupported`
       string; caller handles the skip.
   - `wip_extract_attribution <entry-json>` — emit the two HTML comment
     lines per LDS §6.3, in the right shape for the source spec
     (single-file vs content-mode vs multi-file refused).
   - `wip_extract_lds_root <manifest-json>` — same resolution rule as
     `_wip_feature_records` for the `lds` sentinel. Returns `engineering`
     when unset.

3. **Add `lib/wip/wip-plumbing-subcommands/graduate.bash`.** Dispatcher:
   - Parse `<artifact-path>` + optional `--to <relative-path>` + `--force`.
   - Validate artifact exists and is readable.
   - Detect LDS: read `wip_features_json`; require
     `features.lds.{enabled, sentinel_exists}`. Exit 3 with the right kind
     otherwise.
   - Resolve target (CLI `--to` > front-matter `graduate-to:` > exit 4
     `no-target`).
   - Resolve auto-NNNN if applicable.
   - Render the body (strip `graduate-to`).
   - Write a temp file with the rendered body; call
     `wip_setup_write_idempotent <tmp> <target>`.
   - Emit the JSON ledger.

4. **Add `lib/wip/wip-plumbing-subcommands/extract.bash`.** Dispatcher:
   - Parse `[--manifest <path>] [--force] [--dry-run]`.
   - Detect LDS (same as graduate).
   - Default manifest path: `<eng_docs_dir>/.lds-manifest.yaml`.
   - Validate manifest (calls `wip_extract_load_manifest`); exit 4 with the
     right kind on failure.
   - For each entry: render → write via `wip_setup_write_idempotent` →
     accumulate ledger arrays. Skip unsupported modes/sources with
     `unsupported[]` entries. Bad-shape entries skip with `error[]`.
   - Emit the JSON ledger; exit 0 unless `refused[]` is non-empty (then
     exit 4 with the content-drift envelope) or any entry had a bad shape
     (also exit 4).

5. **Wire both verbs into `bin/wip-plumbing`.**
   - Source the new libs in the dispatcher's lib-load block.
   - Add `graduate` and `extract` to the dispatch case
     (`bin/wip-plumbing:110`).
   - Update `wip_usage` in `wip-plumbing-lib.bash` to list both verbs.

6. **Spec update: `engineering/specs/wip-plumbing-cli.md`.**
   - Add 2 rows to the §1 verb table (one per verb), each linking to
     step-15.
   - Add `§3 — wip-plumbing graduate` and `§3 — wip-plumbing extract`
     sections after `setup`. Document reads/writes/exit codes/stdout
     shape/--force/--dry-run per verb. Note the "v1 cuts" (modes /
     multi-file / hashes / templates) as a sub-bullet under `extract`.
   - Update §1's "Non-goals for v1: `graduate`/`extract`, …" line — remove
     them from the list (now shipping).
   - Update §1.4 intake apply: `--kind spec → LDS seam (ADR-0006); …` —
     the comment about "v1 stub may refuse with exit 3 until the LDS verb
     surface lands" is now stale; refine the language to say "the LDS verb
     surface ships in step-15 as `graduate`/`extract` (top-level verbs);
     intake apply routes `spec` to `graduate` when the artifact carries a
     `graduate-to:` directive, else exits 3 `spec-without-graduate-to`
     for the user to supply one." **OR** leave intake-apply's `spec`
     branch unchanged and just refine the spec wording — intake/spec
     routing is a separate concern. Lean: refine wording only; do not
     change intake-apply's behavior in this step.

7. **Tests (`test/test-graduate.sh`).** New file. Coverage:
   - **`graduate` happy path.** Tempdir with `features.lds.enabled: true`
     + a stub `.lds-manifest.yaml`. Author an artifact with
     `graduate-to: decisions/0001-foo.md`. Run `graduate <artifact>`.
     Assert exit 0, ledger `ok:true`, target file created at the right
     path, target body equals artifact body minus the front-matter
     directive.
   - **`graduate` LDS disabled.** Tempdir with
     `features.lds.enabled: false`. Run `graduate` → exit 3
     `lds-not-enabled`.
   - **`graduate` LDS enabled but no sentinel.** Tempdir with
     `enabled: true` but no `.lds-manifest.yaml`. Run `graduate` → exit 3
     `lds-sentinel-missing`.
   - **`graduate` no target directive.** Artifact without `graduate-to:`
     and no `--to`. Run `graduate` → exit 4 `no-target`.
   - **`graduate --to` overrides front-matter.** Artifact with
     `graduate-to: decisions/0001-a.md`, run with
     `--to decisions/0002-b.md`; assert target is the `--to` path.
   - **`graduate` auto-numbering.** Tempdir with
     `engineering/decisions/0001-foo.md` and `0002-bar.md` pre-existing.
     Artifact with `graduate-to: decisions/auto-baz.md`. Assert target is
     `engineering/decisions/0003-baz.md`.
   - **`graduate` idempotent.** Run twice; second run is `skipped`, exit 0.
   - **`graduate` content drift.** Mutate the target, re-run; exit 4
     `content-drift`. Then `--force`; exit 0, `wrote_forced`.
   - **`graduate --dry-run`.** Ledger lists the would-be target; no file
     created.
   - **`graduate` strips only the `graduate-to:` key.** Artifact with
     front-matter `{graduate-to, status, date}`; target file has
     `{status, date}` preserved.

8. **Tests (`test/test-extract.sh`).** New file. Coverage:
   - **`extract` happy path with verbatim + content modes.** Tempdir,
     stub manifest with two entries (one verbatim from a `legacy/foo.md`
     fixture, one content with inline_content). Assert both targets
     written, ledger names them, attribution comments present, exit 0.
   - **`extract` skips unsupported modes.** Manifest with one verbatim +
     one transform + one summarize. Assert verbatim written; transform +
     summarize land in `unsupported[]`; exit 0; the supported entry's file
     exists.
   - **`extract` skips multi-file source.** Manifest with one entry whose
     source is `files: [...]`. Assert `unsupported[]` entry with reason
     `multi-file-source not supported in v1`.
   - **`extract` rejects unapproved manifest.** Manifest with
     `metadata.status: pending`. Exit 4 `manifest-not-approved`.
   - **`extract` rejects incompatible schema.** Manifest with
     `metadata.schema_version: "2.0.0"`. Exit 4
     `incompatible-schema`.
   - **`extract` rejects empty entries.** Manifest with `entries: []`.
     Exit 4 `manifest-empty`.
   - **`extract` idempotent.** Run twice; second run is all
     `skipped_idempotent`, exit 0.
   - **`extract` content drift.** Mutate one target, re-run; exit 4
     `content-drift`. Then `--force`; exit 0.
   - **`extract --manifest <override>`.** Point at a non-default path;
     assert it works.
   - **`extract` LDS disabled / sentinel missing.** Same exit-3 shapes as
     graduate.
   - **`extract --dry-run`.** Ledger lists the entries; no files written.
   - **`extract` attribution shape.** Verify both verbatim and content
     attribution headers match LDS §6.3 verbatim.

9. **Dogfood (in commit body, not the test suite).** Pick one realistic
   candidate from this repo's `.wip/initiatives/distillation/`:
   - **Candidate**: a small decision block from step-13's workplan, e.g.
     "Verb shape: `glossary {assemble, check}` (two subcommands)". Build a
     tempdir consumer with `features.lds.enabled: true` and a stub
     `.lds-manifest.yaml`. Author a one-shot artifact in the tempdir's
     `.wip/scratch/` with `graduate-to: decisions/auto-glossary-verb-shape.md`
     and the decision block as the body. Run
     `WIP_ROOT=$tmp wip-plumbing graduate <artifact>`. Assert the file lands
     at `engineering/decisions/0001-glossary-verb-shape.md` (since the
     tempdir's `engineering/decisions/` is empty) with the right shape.
     Then `wip-plumbing doctor` against the tempdir — should report zero
     drift (graduate didn't change the manifest; LDS sentinel still
     present). Roll back via tempdir teardown.

10. **Mark step-15 shipped + close Round 3.**
    - `.wip/initiatives/distillation/roadmap.md` step-15 bullet gets
      `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
    - `.wip.yaml`'s `initiatives[0].active_step: step-15` —
      see *Open questions* below for what to set this to (likely a
      sentinel value indicating Round 3 is closed; or advance to a
      `step-16` placeholder if Round 4 has a planned first step).
    - Add Round 3 a `## Round 3 — Porcelain, plugin & features` heading
      ✅-marker on the line if the convention exists, mirroring Round 1
      and Round 2's "✅ shipped <date>" headings.
    - Add backlog rows in `.wip/backlog.md` (or the roadmap's
      `## Backlog` section) for each Out-of-scope item above:
      - `extract-transform-mode` — LDS transform mode (markdown_format,
        heading_adjust, link_rewrite).
      - `extract-summarize-mode` — LDS summarize mode (LLM-driven; porcelain).
      - `extract-verify-hashes` — `--verify-hashes` flag + sha256 dep.
      - `extract-multi-file-source` — multi-file source concatenation.
      - `extract-templates-field-mappings` — template + field_mappings support.
      - `extract-resume-mode` — `--resume` for partial-failure recovery.
      - `extract-extraction-report` — write extraction-report.md/yaml per LDS §7.
      - `setup-lds-verb` — sixth `setup` verb; install LDS scaffold + flip
        `features.lds.enabled`.
      - `glossary-partial-lds` — author `templates/glossary/lds.md`.
    - `bin/wip-plumbing doctor` and `glossary check` both report zero
      drift; `make check` stays green; `pre-commit run --all-files` green.

11. **Branch + commit + merge.** Same flow as step-14: branch
    `step-15-graduate-extract`, commit (body includes dogfood transcript),
    merge no-ff into main, leave the branch.

## Test strategy

Two new test files, plain bash, sourcing `test/helpers.sh`. All fixture
work in tempdirs; no edits to repo content during test execution. The
expected line count is similar to `test-setup.sh` (~9KB, ~25 cases) —
graduate has narrower surface (~10 cases), extract has more (~12 cases).

Tempdir fixture shape (shared via a helper in each test):
```
$tmp/
  .wip.yaml                    # features.lds.enabled: true, root: engineering
  engineering/
    .lds-manifest.yaml         # stub: metadata + empty/non-empty entries
    decisions/                 # may pre-populate to test auto-numbering
    specs/                     # for spec graduations
  legacy/                      # source files for verbatim extract entries
    foo.md                     # known content for line-range extraction
  .wip/scratch/                # artifacts for graduate
```

Stub `.wip.yaml` for the LDS-enabled tempdir:
```yaml
version: 1
features:
  lds:
    enabled: true
    root: engineering
current_initiative: test
initiatives:
  - slug: test
    title: Test
    status: in-flight
    brief: .wip/initiatives/test/BRIEF.md
    roadmap: .wip/initiatives/test/roadmap.md
```

Stub `engineering/.lds-manifest.yaml` (always-approved):
```yaml
metadata:
  schema_version: "1.0.0"
  status: approved
  eng_docs_dir: engineering
entries: []  # tests override with real entries
```

Each test case is one-tempdir-per-case for isolation (matches
`test-setup.sh`). Existing tests stay green. Budget for `make check`:
two new test files (~80 assertions total), two new libs (~150 lines
each), two new subcommands (~120 lines each), one new spec section,
updated usage block.

## Definition of done

- `lib/wip/wip-plumbing-graduate-lib.bash` committed; pure functions, no
  side effects beyond the named writes.
- `lib/wip/wip-plumbing-extract-lib.bash` committed; pure functions, no
  side effects beyond the named writes.
- `lib/wip/wip-plumbing-subcommands/graduate.bash` committed.
- `lib/wip/wip-plumbing-subcommands/extract.bash` committed.
- `bin/wip-plumbing` sources both new libs and dispatches both verbs.
- `bin/wip-plumbing` usage lists `graduate` and `extract`.
- `engineering/specs/wip-plumbing-cli.md` documents both verbs in §1 and
  adds two `§3` sections; the §1 non-goals line drops
  `graduate`/`extract`.
- `test/test-graduate.sh` and `test/test-extract.sh` committed and green
  under `nix develop --command make check`.
- All previously-passing tests still pass.
- `nix develop --command bin/wip-plumbing doctor` reports zero drift.
- `nix develop --command bin/wip-plumbing glossary check` exits 0.
- `nix develop --command pre-commit run --all-files` exits 0.
- `.wip/initiatives/distillation/roadmap.md` step-15 bullet marked
  `✅ shipped <YYYY-MM-DD>` with a one-line outcome.
- Round 3 closed: the `## Round 3 — Porcelain, plugin & features` heading
  is marked shipped (matching Round 1's `✅ shipped 2026-06-12` style)
  with a one-line round summary, OR a `Round 3 complete` note added under
  the round; whichever the orchestrator prefers (see Open questions).
- Backlog rows added for every Out-of-scope item.
- `.wip.yaml`'s `initiatives[0].active_step` advanced to either `step-16`
  (if Round 4 has a planned first step) or set to a "round-3-complete"
  sentinel (see Open questions).
- Branch + commit + merge into `main` (no-ff merge commit).
- Commit body includes the dogfood transcript: tempdir round-trip exit
  code + the graduate ledger + the resulting file path.

## Open questions to resolve during execution

- **Verb naming: `graduate` vs `promote`.** LDS uses neither term in its
  own docs; ADR-0006 uses "graduate" once ("wip graduate calls LDS's
  existing analyze/review/extract verbs"). The intake spec uses "graduate"
  too. **Lean: `graduate`** — it's the term already in flight in this
  repo's vocabulary, and `promote` is overloaded in CI/CD culture
  (promote-to-stage, promote-to-prod) in ways that don't match.

- **Front-matter directive name: `graduate-to:` vs `lds-target:` vs
  `extract-to:`.** All three are reasonable. **Lean: `graduate-to:`** —
  matches the verb name, and "extract-to" collides with the other verb's
  semantic in a confusing way. (`lds-target:` is more LDS-faithful but
  also more verbose and ties the directive name to LDS forever — when
  the future "extract to a Diátaxis slot" verb shows up, `graduate-to:`
  generalizes.)

- **Should `graduate` infer the layer from the target filename
  convention?** E.g. `graduate-to: 0010-foo.md` auto-resolves to
  `decisions/` because of the `NNNN-` prefix. **Lean: no** — too magic;
  explicit `decisions/0010-foo.md` is the contract. The one shortcut is
  `decisions/auto-<slug>.md` for auto-numbering — clear opt-in.

- **Should `graduate`-without-a-directive offer to write the directive
  back into the artifact?** Nice ergonomic but stretches the "plumbing
  is deterministic" rule (modifying the source artifact based on a CLI
  flag is a non-trivial mutation). **Lean: no, exit 4 with a hint** — let
  the porcelain layer offer to insert the directive interactively if it
  wants.

- **Should `graduate` validate that the target is in a recognized LDS
  layer?** E.g. refuse `graduate-to: random/foo.md` because `random/`
  isn't one of the seven LDS layers. **Lean: yes, but with a soft list
  matching LDS's canonical seven (`decisions`, `product`,
  `architecture`, `specs`, `reference`, `features`, `implementation`) +
  permission for `maintenance` and `appendices`.** Anything else exits
  4 `unknown-layer` with the recognized list in the hint. This catches
  typos like `decisons/foo.md`. Rejected for v1: arbitrary target
  freedom — too loose. Rejected: a manifest-configurable allowlist —
  premature.

- **`extract` per-entry exit code semantics.** Today: 0 if all entries
  succeed-or-skip-or-unsupported, 4 if any entry refuses (drift) or has
  a bad shape. Alternative: 4 only on bad-shape; treat refused-by-drift
  as a `--force`-able warning. **Lean: drift is exit 4** — matches
  `setup`'s contract verbatim. The whole point of three-way idempotency
  is "the data prevents me from acting safely; you decide."

- **Should `extract` write a JSON ledger file to disk too (mirroring
  `manifest_updated: .wip.yaml` in setup's ledger)?** LDS §7 specifies a
  written extraction report. **Lean: no for v1** — stdout-only ledger
  matches the prtend contract (stdout is JSON, stderr is prose). Adding
  a written report file is a separate concern; in backlog as
  `extract-extraction-report`.

- **Should `extract` validate entry `id` uniqueness?** Schema says
  required: "Must be unique across all entries in the manifest." **Lean:
  yes** — `jq` one-liner; if duplicates exist, exit 4
  `duplicate-entry-id` with the offender. Cheap and prevents a class of
  bugs.

- **Round 3 close — set `active_step` to what?** Options:
  1. `step-15` stays (with the shipped marker on the line) — `next`
     would presumably return "all steps shipped → propose Round 4."
  2. Set to a sentinel like `step-r3-complete` — explicit close marker;
     `next` could recognize it.
  3. Advance to `step-16` only if Round 4 has a planned first step in
     the roadmap (it currently doesn't).
  **Lean: option 1 (`step-15` with ✅ shipped on the line)**, since the
  status/next verbs (step-08) already handle "all steps shipped"
  gracefully by ranking next-round candidates. The roadmap's Round 3
  heading gets `✅ shipped <date>` to mirror Round 1.

- **Does intake-apply's `spec` branch route to `graduate` now?** Today
  intake-apply for `--kind spec` exits 3 with an LDS-not-active hint
  (per step-07.5). Now that `graduate` exists, should `intake apply
  --kind spec` dispatch through to `graduate` (analogous to how
  `--kind amendment` dispatches through to `roadmap amend`)? **Lean: no
  for v1, defer**, because (a) the routing requires the spec artifact
  to carry a `graduate-to:` directive, and the shaper layer for `spec`
  doesn't insert one yet (it's still a stub), and (b) the change has
  porcelain implications (the `wip intake` shaper for spec kinds would
  need to ask the user where the spec lands). Backlog item:
  `intake-apply-spec-graduate-dispatch`. Document in the spec that
  `intake apply --kind spec` still exits 3 in v1.

- **Should `graduate` accept stdin instead of a file path?** Useful for
  the porcelain piping a shaped artifact through `wip intake apply` →
  `wip graduate`. **Lean: no for v1** — every other plumbing verb takes
  a file path (with `-` reserved for future stdin if it becomes useful);
  porcelain can use `mktemp` if it needs to. The contract stays
  uniform.

- **Should `graduate` and `extract` participate in the
  `wip-plumbing template show` shape?** I.e., should there be a
  `templates/prompts/graduate/...` directory the porcelain reads via
  the template-show seam? **Lean: no for v1** — neither verb has an
  LLM-shaper analogue in this step (graduate is plumbing-only; extract
  is plumbing-only). If a future "graduate shaper" lands (a porcelain
  step that uses the LLM to insert `graduate-to:` directives into raw
  draft artifacts), it would author prompts at that point. No work in
  this step.
