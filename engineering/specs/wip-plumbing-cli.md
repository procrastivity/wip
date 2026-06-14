# Spec — `wip-plumbing` CLI contract (v1)

- Status: draft
- Date: 2026-06-12
- Initiative: distillation · roadmap **step-05**
- Decisions: [ADR-0001](../decisions/0001-three-layer-plumbing-porcelain.md) (layers),
  [ADR-0002](../decisions/0002-wip-yaml-manifest-and-detection.md) (manifest/detection),
  [ADR-0003](../decisions/0003-layered-opt-in-vocabulary.md) (glossary)

This is the build-ready contract for the deterministic core. Test fixtures pin these JSON
shapes; the `wip` and `/wip:*` porcelains are written against it; API changes start here.
Conventions are adopted verbatim from prtend's CLI contract so the two compose.

---

## 1. Scope

`wip-plumbing` is the **deterministic** half of `wip` (ADR-0001): it never calls an LLM
and never makes a judgment a human/porcelain should make. v1 ships the verbs below.
Intake is a pipeline rather than a single verb (ADR-0009); its plumbing surface is three
subcommands, and two new verbs cover the destinations its `apply` step routes to.

The standalone `wip` porcelain — which exposes this surface verbatim and adds the
OpenAI-compatible provider seam — is specified in [`wip-porcelain.md`](./wip-porcelain.md)
(step-10). The `/wip:*` Claude Code plugin — the third frontend, which reads the same
plumbing — is specified in [`wip-plugin.md`](./wip-plugin.md) (step-11).

| Verb | One-line | Roadmap step |
|------|----------|--------------|
| `detect` | What features/initiatives exist, per `.wip.yaml`. The mandatory first call. | step-06 |
| `doctor` | Verify the manifest against disk; report (and optionally fix) drift. | step-06 |
| `project` | `list` / `register` / `resolve` / `forget` entries in the global registry. | step-06.5 |
| `init` | Scaffold the repo manifest and/or an initiative from `templates/`. | step-07 |
| `intake validate` | Shape-check an inbound planning artifact (the v0 single-kind validator). | step-07 |
| `intake classify` | Best-guess `kind` from heuristics; never asks. | step-07.5 |
| `intake validate` (per-kind) | Per-kind shape rules from `intake-kinds.md`. | step-07.5 |
| `intake apply` | Terminal write; dispatches to `init` / `roadmap amend` / `workplan init`. | step-07.5 (gated on 08.5) |
| `roadmap amend` | Deterministic edit to an initiative's `roadmap.md` (insert / replace / append-round). | step-08.5 |
| `workplan init` | Scaffold `.wip/initiatives/<slug>/workplans/<step-id>-<slug>.md`. | step-08.5 |
| `status` | Where am I: current initiative, round, active step, dirty `.wip/`. | step-08 |
| `next` | Ranked candidates for what to do next (no choice — that's the porcelain). | step-08 |
| `template show` | Print a canonical template body by id (`intake/preamble`, …). | step-11 |
| `template list` | Enumerate `templates/prompts/**/*.md` as `{id, path}` records. | step-11 |
| `glossary assemble` | Render `core` + enabled-feature partials → effective glossary markdown. | step-13 |
| `glossary check` | Compare on-disk `.wip/GLOSSARY.md` against a fresh assemble; exit 4 on drift. | step-13 |
| `setup deps` | Write `flake.nix` + `flake.lock` (devShell pinning the bash toolchain). | step-14 |
| `setup direnv` | Write `.envrc` (nix-direnv shim); flip `features.direnv.enabled`. Requires `flake.nix`. | step-14 |
| `setup hygiene` | Write `.pre-commit-config.yaml` (local hooks mirroring `make check`). | step-14 |
| `setup release` | Write `cliff.toml` + `CHANGELOG.md`; flip `features.changelog.enabled`. | step-14 |
| `setup agents` | Vendor `.claude-plugin/` into the consumer; flip `features.orchestration.{enabled, backend: solo, source: plugin}`. | step-14 |

Non-goals for v1: `graduate`/`extract`, `orchestrate`/`spawn`, the `wip intake`
porcelain (step-10.5). They are later roadmap steps and get their own specs.

## 2. Global conventions

### Output discipline (per prtend)
- **stdout** — always JSON when the command has structured output; one JSON document per
  call. Never mixed with prose.
- **stderr** — human-readable diagnostics/progress/errors. Never machine-parsed.
- All structured commands accept `--json` (default on for `wip-plumbing`; the porcelains
  may default it off and render prose).

### Exit codes (per prtend)
| Code | Meaning |
|------|---------|
| 0 | Success, including idempotent no-ops |
| 1 | General error |
| 2 | Invalid arguments / usage |
| 3 | Missing dependency or **feature not enabled** in `.wip.yaml` |
| 4 | Data/state issue — refused operation needing a human decision (drift, slug exists, invalid artifact) |

Nothing-to-do is **0**, not 4. Exit 4 is reserved for "the data prevents me from acting
safely; you decide."

### Common flags
`-h/--help` (exit 0), `--version` (top-level, exit 0), `-v/--verbose`, `-q/--quiet`,
`--json/--no-json`, `--dry-run` (state-mutating verbs: print the write ledger, touch nothing),
`--project <id>` (operate on a registered project from outside its tree; accepts an
absolute path, dash-encoded segment, or opt-in slug — see
[`wip-plumbing-registry.md`](./wip-plumbing-registry.md)).

### Manifest discovery & env
- `.wip.yaml` is found by walking up from `$PWD` to the first match (the **repo root**).
  All relative paths in output resolve against that root.
- `WIP_LIB` — override `lib/wip/` path (dev installs).
- `WIP_ROOT` — force the repo root, skipping the walk-up.
- `WIP_REGISTRY_FILE` — override the global registry path (default
  `$XDG_STATE_HOME/wip/projects.jsonl`; see
  [`wip-plumbing-registry.md`](./wip-plumbing-registry.md)).
- `WIP_NO_REGISTRY=1` — suppress registry reads and writes for this invocation.
- `wip-plumbing` reads **no** LLM/provider env — that's porcelain-only.

### Common error envelope (stderr is prose; stdout stays valid JSON on handled errors)
```json
{ "ok": false, "error": { "code": 4, "kind": "slug-exists", "message": "initiative 'foo' already exists", "path": ".wip/initiatives/foo" } }
```

---

## 3. Verbs

### `wip-plumbing detect`
Mandatory first call. Pure read of `.wip.yaml` + sentinel existence checks (ADR-0002).

- **Reads:** `.wip.yaml`; each declared feature's sentinel path.
- **Writes:** nothing.
- **Exit:** 0 always when a manifest is found; 4 if `.wip.yaml` is absent or unparseable.
- **stdout:**
```json
{
  "ok": true,
  "root": "/abs/repo",
  "wip_yaml": ".wip.yaml",
  "current_initiative": "distillation",
  "features": [
    { "name": "solo",      "enabled": true,  "active": true,  "sentinel": null, "detail": { "force_tier": "large" } },
    { "name": "lds",       "enabled": false, "active": false, "sentinel": "engineering/.lds-manifest.yaml" },
    { "name": "changelog", "enabled": true,  "active": false, "sentinel": "CHANGELOG.md", "drift": "declared-but-missing" }
  ],
  "initiatives": [
    { "slug": "distillation", "status": "in-flight", "active_step": "step-05", "brief": ".wip/initiatives/distillation/BRIEF.md", "roadmap": ".wip/initiatives/distillation/roadmap.md" }
  ]
}
```
`active = enabled AND sentinel-exists`. A feature with no sentinel (e.g. `solo`) is
`active` when `enabled`.

### `wip-plumbing doctor [--fix]`
Verification loop over the detection contract.

- **Reads:** `.wip.yaml` + all sentinels; the initiative registry vs `.wip/initiatives/` on disk.
- **Writes:** nothing, unless `--fix` (materializes a missing stanza for a present-but-undeclared sentinel; prunes a registry entry whose directory is gone — each `--fix` action is logged and reversible-by-diff). **v1 status:** `--fix` is *advisory* — it warns and writes nothing; real autofix is a later step.
- **Exit:** 0 if healthy; **4** if any drift remains (declared-but-missing sentinel, present-but-undeclared feature, registry/disk mismatch, two features sharing a root).
- **stdout:**
```json
{
  "ok": false,
  "checks": [
    { "kind": "feature", "name": "lds", "status": "ok" },
    { "kind": "feature", "name": "changelog", "status": "declared-but-missing", "sentinel": "CHANGELOG.md", "fix": "wip setup release" },
    { "kind": "initiative", "slug": "distillation", "status": "ok" }
  ],
  "drift_count": 1
}
```

### `wip-plumbing project <list|register|resolve|forget>`
Manage the global registry at `$XDG_STATE_HOME/wip/projects.jsonl`
(ADR-0008). Deterministic; no LLM. Full storage and resolution rules in
[`wip-plumbing-registry.md`](./wip-plumbing-registry.md); summary below.

- `project list [--json] [--prune]` — enumerate registered projects;
  `--prune` first drops records whose `path` is gone or no longer has
  `.wip.yaml`.
- `project register [<path>] [--slug <slug>]` — idempotent upsert.
  `<path>` defaults to `$PWD`. Sets/updates `slug`.
- `project resolve <id>` — resolve an absolute path, dash-encoded segment,
  or slug to a record (exit 0 + JSON / exit 3 not-found / exit 4 ambiguous).
- `project forget <id>` — remove a record. Touches no project files.
- **Writes:** the registry file. Write errors are swallowed (do not fail
  the verb); with `-v` a one-line diagnostic is written to stderr.
- **stdout:** for `register`/`resolve`, `{"ok":true,"record":{...}}`; for
  `forget`, `{"ok":true,"forgot":"<id>"}`; for `list`, either a TSV-style
  table or raw JSONL with `--json`.

### `wip-plumbing init [<slug>] [--title <t>] [--intake ad-hoc|structured]`
Scaffold from `templates/`. Idempotent; protected-path model (never clobbers existing
content without `--force`).

- **No `<slug>`:** repo-level scaffold — write `.wip.yaml` (from `templates/wip.yaml.tmpl`) and the `.wip/` skeleton (`GLOSSARY.md` pointer, `backlog.md`) if absent.
- **With `<slug>`:** initiative scaffold — create `.wip/initiatives/<slug>/{brief.md,roadmap.md}` from templates and append a registry entry to `.wip.yaml`. `--intake structured` marks the initiative for a spec-generator pass (porcelain); `ad-hoc` (default) drops a `brief.md` stub.
- **Writes:** the files above (or, with `--dry-run`, just the ledger).
- **Exit:** 0 on scaffold; **4** if `<slug>` already exists (no overwrite); 2 on bad slug (must match `^[a-z0-9][a-z0-9-]*$`).
- **stdout:** a write ledger:
```json
{ "ok": true, "slug": "auth-rework", "wrote": [".wip/initiatives/auth-rework/brief.md", ".wip/initiatives/auth-rework/roadmap.md"], "skipped_protected": [], "manifest_updated": ".wip.yaml" }
```

### `wip-plumbing intake` — pipeline subcommands

Intake is a pipeline (ADR-0009). The plumbing surface is three subcommands; the
LLM-driven shaping and routing between them lives in the `wip intake` porcelain
(roadmap step-10.5). The closed kind vocabulary, per-kind shape rules, and classify
heuristics are specified in [`intake-kinds.md`](./intake-kinds.md).

All three subcommands shipped in step-07.5. As of step-08.5, `apply` is also
end-to-end for `brief` → `init`, `amendment` → `roadmap amend`, and
`workplan-seed` → `workplan init`. `spec` still exits 3 (LDS seam not yet
wired); `handoff` exits 4 (not terminal).

#### `wip-plumbing intake classify <file>`
Best-guess `kind` from front-matter + heading heuristics. Never asks; never makes a
judgment call — that's porcelain.

- **Reads:** the named file.
- **Writes:** nothing.
- **Exit:** 0 always when the file is parseable; **4** if unparseable or no title.
- **stdout:**
```json
{ "ok": true, "file": "plan.md", "kind": "amendment", "confidence": "high", "signals": ["front-matter wip-kind=amendment", "target=distillation", "insert-after=step-06"] }
```

#### `wip-plumbing intake validate <file> [--kind <k>]`
Per-kind shape check. With `--kind` omitted, uses `classify`'s best guess.

- **Reads:** the named file; for `--kind workplan-seed` or `--kind amendment`,
  consults `.wip.yaml` + the target initiative's `roadmap.md` to verify the named slug
  / step exists.
- **Writes:** nothing.
- **Exit:** 0 if valid; **4** if shape rules fail; 2 if no file given or `--kind` is not in
  the closed vocabulary.
- **stdout:**
```json
{ "ok": false, "file": "prd.md", "kind": "brief", "valid": false, "missing": ["goal-or-summary-section"] }
```

#### `wip-plumbing intake apply <file> --kind <k> [--target <slug|slug/step>]`
Terminal write. Validates first; refuses on shape failure. Dispatches to the
appropriate writer:

- `--kind brief` → `init <derived-slug>`.
- `--kind amendment` → `roadmap amend <target> <directive-from-artifact>`.
- `--kind workplan-seed` → `workplan init <slug> <step-id> --from <file>`.
- `--kind spec` → LDS seam (ADR-0006); v1 stub may refuse with exit 3 ("LDS not
  active") until the LDS verb surface lands.
- `--kind handoff` → exit 4 ("handoff is not a terminal kind; reshape first").

- **Reads:** the artifact; whatever the dispatched verb reads.
- **Writes:** whatever the dispatched verb writes.
- **Exit:** 0 on success; **4** on shape failure or routing refusal; 2 on bad args; 3 if
  routing to an inactive feature (e.g. `spec` with LDS disabled).
- **stdout:** the dispatched verb's write ledger, with an outer envelope:
```json
{ "ok": true, "kind": "amendment", "dispatched": "roadmap amend", "target": "distillation", "result": { "wrote": [".wip/initiatives/distillation/roadmap.md"], "directive": "insert-after step-06" } }
```

### `wip-plumbing roadmap amend <slug>`
Deterministic edit to `.wip/initiatives/<slug>/roadmap.md`. Reads a shaped `amendment`
artifact from stdin or `--from <file>`. Idempotent: re-applying the same artifact is a
no-op, detected via a hash-of-payload comment stamped at the insertion site.

- **Flags:** exactly one of `--insert-after <step-id>`, `--replace <step-id>`,
  `--append-round <title>`. If `--from` is given and the artifact carries a directive,
  the CLI flag must match (or be omitted) — mismatch is exit 2.
- **Reads:** the artifact + the target `roadmap.md`.
- **Writes:** the target `roadmap.md` (or just the ledger with `--dry-run`).
- **Exit:** 0 on amend or detected-duplicate no-op; **4** if target step doesn't exist,
  or the artifact fails amendment-shape validation; 2 on bad flags.
- **stdout:**
```json
{ "ok": true, "slug": "distillation", "directive": "insert-after step-06", "wrote": [".wip/initiatives/distillation/roadmap.md"], "idempotent_noop": false }
```

### `wip-plumbing workplan init <slug> <step-id> [--from <file>] [--force]`
Scaffold `.wip/initiatives/<slug>/workplans/<step-id>-<derived-slug>.md` from
`templates/workplan.md.tmpl`. Step must exist in the initiative's roadmap.

- **Reads:** `.wip.yaml`, the initiative's `roadmap.md`, the optional seed file, the
  template.
- **Writes:** the workplan file (or just the ledger with `--dry-run`).
- **Exit:** 0 on write; **4** if the file exists (without `--force`) or the step doesn't
  exist; 2 on bad args.
- **stdout:**
```json
{ "ok": true, "slug": "distillation", "step": "step-07.5", "wrote": [".wip/initiatives/distillation/workplans/step-07.5-intake-kinds.md"] }
```

### `wip-plumbing status [--initiative <slug>]`
"Where am I." Deterministic from manifest + the initiative's `roadmap.md` + git state of
`.wip/`. Defaults to `current_initiative`; `--initiative` overrides.

- **Reads:** `.wip.yaml`; `<initiative>/roadmap.md` (current round, active step, shipping criteria); `git status --porcelain -- .wip/`. *Solo augmentation:* when `features.solo.active`, the **porcelain** layers in live todos/process state — `wip-plumbing status` itself stays git+files only and notes `"solo_available": true`.
- **Writes:** nothing.
- **Exit:** 0; **3** if `--initiative` names a slug not in the registry.
- **stdout:**
```json
{
  "ok": true, "initiative": "distillation", "status": "in-flight",
  "round": { "n": 2, "title": "wip-plumbing v1" },
  "active_step": { "id": "step-05", "title": "CLI contract spec", "shipped": false },
  "dirty_wip_files": [".wip/initiatives/distillation/roadmap.md"],
  "solo_available": true
}
```

### `wip-plumbing next [--initiative <slug>]`
Ranked candidates for the next action. **Ranking, not choosing** — the porcelain picks and
justifies. Source order: active roadmap (first unshipped step) → backlog.

- **Reads:** `<initiative>/roadmap.md`; `.wip/backlog.md`.
- **Writes:** nothing.
- **Exit:** 0 (including "all steps shipped" → candidate to start the next round / close the initiative).
- **stdout:**
```json
{
  "ok": true, "initiative": "distillation",
  "candidates": [
    { "rank": 1, "source": "roadmap", "id": "step-05", "title": "CLI contract spec", "reason": "first unshipped step in active round" },
    { "rank": 2, "source": "roadmap", "id": "step-06", "title": "detect + doctor", "reason": "next sequential step" },
    { "rank": 3, "source": "backlog", "id": "slice-fixes", "title": "in-place study-slice fixes", "reason": "deferred; needs human go-ahead" }
  ]
}
```

### `wip-plumbing template <show|list>`

Print the canonical templates that ship with `wip`. Shipped in step-11 to
back the `/wip:*` plugin's prompt-sharing seam (see
[`wip-plugin.md`](./wip-plugin.md) §4); useful generally for any frontend
that needs the canonical bytes by name rather than by path.

ID grammar: path under `templates/prompts/` minus the `.md` suffix. E.g.
`intake/preamble` → `templates/prompts/intake/preamble.md`.

Templates dir resolution: `$WIP_TEMPLATES_DIR` (override) →
`$WIP_LIB/../../templates`.

#### `wip-plumbing template show <id>`

- **Reads:** the resolved template file.
- **Writes:** nothing.
- **Stdout:** the file body **verbatim** (no JSON envelope; this is one of
  the few plumbing verbs that emits raw bytes — same shape as a future
  `template render` would render, just without substitution).
- **Exit:** 0 on success; **2** if no id given, or the id is absolute (`/…`)
  or contains `..`; **4** if the templates dir is missing
  (`no-templates`), or the id resolves to no file (`unknown-template`).

```
$ wip-plumbing template show intake/preamble
You are the SHAPER stage of the `wip intake` pipeline (ADR-0009).
…
```

#### `wip-plumbing template list [--no-json]`

- **Reads:** the resolved templates dir; enumerates
  `prompts/**/*.md` (`find -type f -name '*.md'`).
- **Writes:** nothing.
- **Stdout (JSON, default):**
  ```json
  { "ok": true, "templates": [
      { "id": "intake/preamble", "path": "/abs/templates/prompts/intake/preamble.md" },
      { "id": "intake/brief",    "path": "…/intake/brief.md" }
    ] }
  ```
  Sorted by id.
- **Stdout (`--no-json`):** TSV — `<id>\t<path>` per line.
- **Exit:** 0 on success; **4** `no-templates` if the templates dir is
  missing.

### `wip-plumbing glossary <assemble|check>`

Render the effective glossary for the project by concatenating
`templates/glossary/core.md` with the partials whose feature is active in
`.wip.yaml`. Inclusion rules are declared in
[`templates/glossary/README.md`](../../templates/glossary/README.md); the
authoritative table is enforced in `lib/wip/wip-plumbing-glossary-lib.bash`
(adding a new partial is a one-row addition there). Per
[ADR-0007](../decisions/0007-orchestration-backend-seam.md), `solo.md` is
gated on `features.orchestration.backend == "solo"`, NOT on
`features.solo.enabled`.

Templates dir resolution: `$WIP_TEMPLATES_DIR` (override) →
`$WIP_LIB/../../templates` (same seam as `template`).

Each partial's leading `<!-- wip glossary partial: … -->` block is stripped
on emit (the generated header records the inclusion roster once;
duplicating it three times mid-document is noise). A partial whose
predicate is true but whose file is not on disk (e.g. `lds.md` / `diataxis.md`
before they ship) is a **graceful skip** — the assemble output omits its
body and the JSON ledger lists it under `partials_skipped[]` with
`reason: "predicate-true; partial-not-shipped"`.

#### `wip-plumbing glossary assemble [--output <path>]`

- **Reads:** `.wip.yaml`; each enabled partial under the resolved
  templates dir.
- **Writes:** nothing (stdout-only) by default. With `--output <path>`:
  the named file via atomic tmpfile + `mv`. `--dry-run` with `--output`:
  prints the ledger only.
- **Stdout (no `--output`):** the assembled markdown **verbatim** (no
  JSON envelope; same shape as `template show`). Body opens with an H1,
  a `<!-- GENERATED … -->` block recording Source paths + Driven-by
  predicates + the regen/verify recipes, and a one-paragraph intro
  blockquote; then per-partial `<!-- partial: NAME  source: PATH  reason: PREDICATE -->`
  dividers followed by the stripped partial body.
- **Stdout (with `--output`):** JSON write ledger.
  ```json
  { "ok": true, "wrote": [".wip/GLOSSARY.md"],
    "partials_included": [
      {"name":"core.md", "source_path":"…/templates/glossary/core.md", "predicate":"always"},
      {"name":"orchestration.md", "source_path":"…", "predicate":"features.orchestration.enabled"},
      {"name":"solo.md", "source_path":"…", "predicate":"features.orchestration.backend"}
    ],
    "partials_skipped": [
      {"name":"diataxis.md", "predicate":"features.diataxis.enabled",
       "reason":"predicate-true; partial-not-shipped"}
    ] }
  ```
- **Exit:** 0 on success; **2** on bad args; **4** `no-templates` if
  the templates dir is missing, `no-manifest` / `bad-manifest` as usual.

#### `wip-plumbing glossary check`

- **Reads:** `.wip.yaml`, each enabled partial, the on-disk glossary at
  the path derived from `gitignore.always_commit[]` (looking for
  `.wip/GLOSSARY.md`; falls back to literal `.wip/GLOSSARY.md`).
- **Writes:** nothing.
- **Stdout (no drift):**
  ```json
  { "ok": true, "drift": false, "expected_path": ".wip/GLOSSARY.md",
    "partials": [ {"name":"core.md", "predicate":"always", "status":"included", "reason":"always"}, … ] }
  ```
- **Stdout (drift):** error envelope, plus `expected_path`,
  `actual_path`, `byte_diff_count`, `partials`.
- **Stderr (drift):** the regen-hint line, then `diff -u` output between
  on-disk and freshly-assembled. Stderr-only — the JSON envelope does
  not embed the diff bytes.
- **Exit:** 0 on agreement; **4** on drift (`glossary-drift`).
  No `--fix` in v1 (cf. `doctor --fix`'s advisory-only stance).

Pre-commit wiring lives in `.pre-commit-config.yaml` as a `wip-glossary`
local hook that fires when `.wip.yaml`, `.wip/GLOSSARY.md`, or any
`templates/glossary/*.md` changes — the three drift modes the seam
catches: content drift, manifest drift, partial drift.

### `wip-plumbing setup <deps|direnv|hygiene|release|agents> [--force]`

Install-time deterministic scaffold writers, one per capability (step-14).
Each verb writes verbatim files from `templates/setup/<verb>/` into the
consumer repo, flips its mapped feature flag in `.wip.yaml` where
applicable, and verifies its sentinel exists post-write. No `{{key}}`
substitution — these are infrastructure files, not artifacts.

**Per-file write contract (three-way):**

- **Absent** → write the template bytes; ledger status `wrote`.
- **Present, byte-equal to template** → silent skip; ledger status
  `skipped` (under `skipped_idempotent`).
- **Present, differs** → refuse; **exit 4** `content-drift` with the
  offending path(s) in `error.paths`. The verb does NOT touch the
  manifest in this case.
- **Present, differs, `--force`** → overwrite; ledger status
  `wrote_forced`.

`flake.lock` is special-cased to **"skip if present, never compare"**:
locks evolve per consumer (`nix flake update` rolls inputs forward), so
byte-equal would refuse every consumer who's done one update. Under
`--force` the lock is still overwritten.

**Composition.** Verbs do not auto-chain. `setup direnv` requires
`flake.nix` and exits 3 `missing-prereq` with `error.path: "flake.nix"`
when absent (hint to run `setup deps` first). All verbs exit 3
`missing-manifest` when `.wip.yaml` is absent (run `init` first).

**Reads:** `templates/setup/<verb>/`; the consumer repo's existing
files at the destination paths; `.wip.yaml` (for the feature-flag flip).

**Writes:** the destination files per the contract above; `.wip.yaml`
(for the feature-flag flip; only on a real diff — re-running an
already-flipped verb is a manifest no-op).

**Exit:**

- **0** — success (including idempotent no-op and `wrote_forced`).
- **2** — bad subcommand, unknown flag, missing subcommand.
- **3** — `missing-manifest` (no `.wip.yaml`) or `missing-prereq`
  (e.g. `setup direnv` without `flake.nix`).
- **4** — `content-drift` (one or more destination files differ from
  template and `--force` was not passed).

**Verb → feature flag map:**

| Verb | Feature block flipped | Sentinel checked post-write |
|---|---|---|
| `setup deps` | (none) | (none) |
| `setup direnv` | `features.direnv.enabled: true` | `.envrc` |
| `setup hygiene` | (none — v1) | (none) |
| `setup release` | `features.changelog.enabled: true` | `CHANGELOG.md` |
| `setup agents` | `features.orchestration.{enabled, backend: solo, source: plugin}` | (none — orchestration has no sentinel; `detect` treats `enabled=true` as `active`) |

`setup agents` deliberately does NOT auto-create the `features.solo`
block (per ADR-0007, that block carries the consumer's backend-specific
`agent_tier_policy`; a default would be presumptuous). Stderr emits a
hint to configure `features.solo.agent_tier_policy` after the verb.

**stdout (success):**
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

**stdout (content drift):**
```json
{
  "ok": false,
  "verb": "setup deps",
  "error": {
    "code": 4,
    "kind": "content-drift",
    "message": "infrastructure files differ from template; re-run with --force to overwrite",
    "paths": ["flake.nix"]
  }
}
```

**stderr:** per-verb one-line hint on success (suppressed by `-q`); error
diagnostics on failure paths.

**Templates dir resolution:** `$WIP_TEMPLATES_DIR` override → `$WIP_LIB/../../templates`
(same seam as `template show` / `glossary assemble`). Each verb reads
`<templates-dir>/setup/<verb>/`.

**`--dry-run`:** prints the ledger (`wrote` lists what *would* be
written) and touches neither files nor the manifest. Sentinel
`sentinel_present` is `false` because the ledger reflects pre-write state.

**Plugin file substitution:** `templates/setup/agents/.claude-plugin/`
references `wip-plumbing` (PATH-resolved) — consumers are expected to
have `wip` installed. This repo's own `.claude-plugin/` keeps
`bin/wip-plumbing` (dogfood-local). The divergence is exactly the
one-substitution rule; the agents template tree is excluded from the
verbatim-cmp fidelity tests for that reason.

---

## 4. Open questions (resolve before/while building step-06–08)

1. **`status` git dependency** — *resolved in step-08.* When `.wip/` is gitignored
   (default), `git status --porcelain -- .wip/` reports nothing → `dirty_wip_files: []`.
   The porcelain layer may mtime-augment later; plumbing stays git-only.
2. **`roadmap.md` parsing** — *resolved in step-08.* The bullet form already in use is the
   v1 grammar: `## Round <N> — <title>` rounds (with optional trailing `✅ shipped
   <YYYY-MM-DD>`); steps as `- **step-<NN[.5]> — <title>**` bullets (with optional `✅`
   marker and `shipped <YYYY-MM-DD>` date); `## Backlog` sections parsed as
   `- **<title>** — <body>` entries. The amendment-form `### step-NN — <title>` heading
   is recognized too. No front-matter; the parser lives in
   `lib/wip/wip-plumbing-roadmap-lib.bash`.
3. **`intake` validators** — *resolved by ADR-0009 + `intake-kinds.md`*; shipped in
   step-07.5. The closed kind vocabulary and per-kind shape rules are now enforced by
   `wip-plumbing intake validate`.
4. **Amendment idempotency hash** — *resolved in step-08.5.* SHA-256 of the
   **rendered insertion payload** (bullet line or appended round block), not the source
   artifact's bytes. Identical inserts shaped from differently-framed artifacts collapse
   to the same hash. Marker line: `<!-- wip-amend: <sha256> -->` immediately after the
   inserted block.
