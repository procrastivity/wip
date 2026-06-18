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
| `roadmap amend` | Deterministic edit to an initiative's `roadmap.md` (insert / replace / append-round / append-lane). | step-08.5 |
| `roadmap parse` | Read-only: emit the parsed roadmap JSON document (rounds, steps with `lane`, `lanes[]`, `lane_errors[]`, backlog). | step-08 (lanes) |
| `workplan init` | Scaffold `.wip/initiatives/<slug>/workplans/<step-id>-<slug>.md`. | step-08.5 |
| `orchestrate prep` | Deterministic readiness + brief for booting orchestration of the active step (never spawns — that is `/wip:orchestrate`). The plumbing half of [ADR-0012](../decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md). | orchestrate |
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
| `setup lds` | Write the LDS install scaffold to `engineering/` (manifest + nine layer dirs + maintenance copies); flip `features.lds.{enabled, root: engineering}`. | step-15 follow-up |
| `graduate` | Promote a single planning artifact to its LDS canon slot (`<eng-docs>/<layer>/<file>`). The LDS seam per ADR-0006. | step-15 |
| `extract` | Run the deterministic LDS Extract phase against an approved manifest. v1: verbatim+content modes only. | step-15 |

Non-goals for v1: a `orchestrate`/`spawn` **fan-out** verb (impossible at this layer —
spawning Claude agents needs MCP, only reachable from the plugin; see
[ADR-0012](../decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md), which adds the
deterministic `orchestrate prep` readiness verb but routes the actual boot through
`/wip:orchestrate`), and the `wip intake` porcelain (step-10.5). They get their own specs.

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
- `--kind spec` → LDS seam (ADR-0006). The LDS verb surface ships in step-15
  as the top-level `graduate` and `extract` verbs. v1 of intake-apply still
  exits 3 here; routing `spec` artifacts through to `graduate` requires the
  shaper layer to insert a `graduate-to:` directive, which is a separate
  porcelain change (backlog: `intake-apply-spec-graduate-dispatch`).
- `--kind handoff` → exit 4 ("handoff is not a terminal kind; reshape first").

- **Reads:** the artifact; whatever the dispatched verb reads.
- **Writes:** whatever the dispatched verb writes.
- **Exit:** 0 on success; **4** on shape failure or routing refusal; 2 on bad args; 3 if
  routing to an inactive feature (e.g. `spec` with LDS disabled).
- **stdout:** the dispatched verb's write ledger, with an outer envelope:
```json
{ "ok": true, "kind": "amendment", "dispatched": "roadmap amend", "target": "distillation", "result": { "wrote": [".wip/initiatives/distillation/roadmap.md"], "directive": "insert-after step-06" } }
```

### `wip-plumbing roadmap parse <file>`
Read-only emitter for the parsed roadmap document — the same JSON the library produces
for `status` / `next`. Exists so the roadmap grammar (including lanes, ADR-0010) is
runnable from the CLI and the parse-regression gate is a real command, not only a
sourced-library call.

- **Reads:** the named roadmap file. A missing file yields an empty document
  (`{rounds:[], backlog:[], lane_errors:[]}`).
- **Writes:** nothing.
- **Exit:** 0 always (read-only); 2 if no file argument.
- **stdout:** the parsed document. Every step carries `lane` (the `### Lane <name>`
  it lives under, or `null` for a main-lane step); every round carries `lanes` (declared
  lane names in order); the document carries `lane_errors` (empty when the lane structure
  is well-formed — see ADR-0010 §5 for the malformed cases).
```json
{
  "rounds": [
    { "n": 4, "title": "Track expansion", "shipped": false, "shipped_date": null,
      "lanes": ["A", "D"],
      "steps": [
        { "id": "step-12", "title": "F1: model-profile taxonomy", "shipped": false, "shipped_date": null, "lane": null },
        { "id": "step-13", "title": "Track A part 1", "shipped": false, "shipped_date": null, "lane": "A" },
        { "id": "step-14", "title": "Track D", "shipped": false, "shipped_date": null, "lane": "D" }
      ] }
  ],
  "backlog": [],
  "lane_errors": []
}
```

### `wip-plumbing roadmap amend <slug>`
Deterministic edit to `.wip/initiatives/<slug>/roadmap.md`. Reads a shaped `amendment`
artifact from stdin or `--from <file>`. Idempotent: re-applying the same artifact is a
no-op, detected via a hash-of-payload comment stamped at the insertion site.

- **Flags:** exactly one of `--insert-after <step-id>`, `--replace <step-id>`,
  `--append-round <title>`, `--append-lane <name>`, `--insert-step-in-lane <name>`.
  `--append-lane` and `--insert-step-in-lane` also require `--target-round <N>` (or
  `target-round: <N>` in the artifact front-matter). If `--from` is given and the artifact
  carries a directive, the CLI flag must match (or be omitted) — mismatch is exit 2.
- **Lane awareness (ADR-0010):** `insert-after` / `replace` preserve the host step's lane
  because the step is rendered in place. `append-round` bodies may include `### Lane`
  subheadings. `append-lane` appends a new `### Lane <name>` block (the artifact body's
  `### step-NN` entries) at the end of round N; it refuses (**exit 4** `duplicate-lane`)
  when that lane name already exists in round N. `insert-step-in-lane` appends a single
  step bullet (one `### step-NN` body, like `insert-after`) to the end of an
  **already-declared** lane in round N — including an *empty* lane, which `append-lane`
  cannot target. It refuses with **exit 4** `round-not-in-roadmap` (round absent) or
  `lane-not-in-round` (lane absent from the round). This is the directive ADR-0010 §6
  deferred and the `bundle` kind promoted: a bundle lead declares empty lanes via
  `append-round`, then each child fills its lane via `insert-step-in-lane`. The amend
  refuses (**exit 4** `lane-malformed`) when the target roadmap already carries a non-empty
  `lane_errors[]`, so a broken lane structure cannot be amended on top of.
- **Reads:** the artifact + the target `roadmap.md`.
- **Writes:** the target `roadmap.md` (or just the ledger with `--dry-run`).
- **Exit:** 0 on amend or detected-duplicate no-op; **4** if target step / round doesn't
  exist, the artifact fails amendment-shape validation, or the roadmap's lane structure
  is malformed; 2 on bad flags.
- **stdout:**
```json
{ "ok": true, "slug": "distillation", "directive": "insert-after step-06", "wrote": [".wip/initiatives/distillation/roadmap.md"], "idempotent_noop": false }
```
For `append-lane` / `insert-step-in-lane` the `directive` reads e.g. `"append-lane A
(round 4)"` / `"insert-step-in-lane A (round 4)"`.

### `wip-plumbing workplan init <slug> <step-id> [--from <file>] [--slug <s>] [--force] [--activate]`
Scaffold `.wip/initiatives/<slug>/workplans/<step-id>-<derived-slug>.md` from
`templates/workplan.md.tmpl`. Step must exist in the initiative's roadmap.

- `--activate` — after the workplan is in place, set `initiatives.<slug>.active_step` in
  `.wip.yaml` (deterministic yq manifest edit, the same key `detect`/`status` read).
  **Idempotent with an existing workplan:** without `--activate` an existing workplan is
  still `file-exists` (exit 4); *with* `--activate` an existing workplan is **not** an
  error — the write is skipped (ledger lists it under `skipped`), `active_step` is still
  set, so "start" is re-runnable. The ledger gains `active_step: <step-id>`;
  `manifest_updated` is present only when the key actually changed. `--dry-run --activate`
  reports the would-be activation and touches neither the file nor the manifest.
- **Reads:** `.wip.yaml`, the initiative's `roadmap.md`, the optional seed file, the
  template.
- **Writes:** the workplan file and (with `--activate`) `.wip.yaml`'s `active_step` (or
  just the ledger with `--dry-run`).
- **Exit:** 0 on write; **4** if the file exists (without `--force` and without
  `--activate`) or the step doesn't exist (`step-not-in-roadmap`); **3** if the initiative
  is not in the manifest (`unknown-initiative`); 2 on bad args.
- **stdout:**
```json
{ "ok": true, "slug": "distillation", "step": "step-07.5", "wrote": [".wip/initiatives/distillation/workplans/step-07.5-intake-kinds.md"] }
```
With `--activate` (existing workplan kept):
```json
{ "ok": true, "slug": "distillation", "step": "step-07.5", "wrote": [], "skipped": [".wip/initiatives/distillation/workplans/step-07.5-intake-kinds.md"], "active_step": "step-07.5" }
```

### `wip-plumbing orchestrate prep [--initiative <slug>]`
Deterministic readiness check + "what to orchestrate" brief for the active step. The
plumbing half of [ADR-0012](../decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md):
`/wip:orchestrate` calls this for facts + gating, then becomes the Orchestrator and spawns a
Coordinator via the backend. **This verb never spawns and never names a backend tool** — it
emits the *facts* about the work (initiative / step / workplan), not the *staffing* (Tier,
process names, `agent_tool_id` stay in the Roles + backend binding, ADR-0007).

- Resolves the initiative like `status` (default `current_initiative`; `--initiative`
  overrides), then resolves that initiative's `active_step`.
- A **missing workplan is not an error**: it reports `workplan.exists: false` and exits 0,
  because the Coordinator's Researcher produces the workplan in Phase 1. The emitted
  `workplan.path` is the existing file if present (glob `<step-id>-*.md`), else the canonical
  path derived from the step title the same way `workplan init` derives it.
- **Reads:** `.wip.yaml`; the initiative's `roadmap.md`; the `workplans/` directory listing.
- **Writes:** nothing.
- **Exit:** 0 on a ready brief; **3** if `features.orchestration.enabled` is not true
  (`orchestration-not-enabled`), if there is no `current_initiative` and no `--initiative`
  (`no-initiative`), or if `--initiative` names an unknown slug (`unknown-initiative`); **4**
  if the initiative has no `active_step` (`no-active-step` — run `/wip:start` first) or the
  `active_step` is not in the roadmap (`step-not-in-roadmap`); 2 on bad args.
- **Signals (advisory, non-fatal):** `active-step-shipped` when the active step is already
  marked shipped (mirrors `status`' divergence reporting — surfaced, not refused).
- **stdout:**
```json
{
  "ok": true,
  "initiative": "distillation",
  "orchestration": { "enabled": true, "backend": "solo" },
  "round": { "n": 4, "title": "Track expansion" },
  "active_step": { "id": "step-16", "title": "Orchestrate verb", "shipped": false, "lane": null },
  "workplan": { "path": ".wip/initiatives/distillation/workplans/step-16-orchestrate-verb.md", "exists": true },
  "roadmap": ".wip/initiatives/distillation/roadmap.md",
  "signals": []
}
```

### `wip-plumbing status [--initiative <slug>]`
"Where am I." Deterministic from manifest + the initiative's `roadmap.md` + git state of
`.wip/`. Defaults to `current_initiative`; `--initiative` overrides.

- **Reads:** `.wip.yaml`; `<initiative>/roadmap.md` (current round, active step, shipping criteria); `git status --porcelain -- .wip/`. *Solo augmentation:* when `features.solo.active`, the **porcelain** layers in live todos/process state — `wip-plumbing status` itself stays git+files only and notes `"solo_available": true`.
- **Writes:** nothing.
- **Exit:** 0; **3** if `--initiative` names a slug not in the registry.
- **Lane awareness (ADR-0010):** the `active_step` object carries a `lane` field (the
  `### Lane <name>` it lives under, or `null` for a main-lane step). When two or more
  lanes have unshipped steps in the active round, `lanes_in_flight` lists the next
  actionable step per in-flight lane; otherwise it is `[]`.
- **stdout:**
```json
{
  "ok": true, "initiative": "distillation", "status": "in-flight",
  "round": { "n": 4, "title": "Track expansion" },
  "active_step": { "id": "step-13", "title": "Track A part 1", "shipped": false, "lane": "A" },
  "lanes_in_flight": [ { "lane": "A", "step": "step-13" }, { "lane": "D", "step": "step-14" } ],
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
- **Lane awareness (ADR-0010):** when the active step lives in a lane, the next unshipped
  step in that lane is the primary forward candidate (`reason: "next-in-lane"`), and
  unshipped steps in *sibling* lanes of the same round carry `concurrent: true`. Ranks
  stay a stable total order; the `concurrent` flag — not a duplicated rank — signals
  "could be worked in parallel." Across-round ranking is unchanged.
- **stdout:**
```json
{
  "ok": true, "initiative": "distillation",
  "candidates": [
    { "rank": 1, "source": "roadmap", "id": "step-13", "title": "Track A part 1", "reason": "manifest active step" },
    { "rank": 2, "source": "roadmap", "id": "step-15", "title": "Track A part 2", "reason": "next-in-lane" },
    { "rank": 3, "source": "roadmap", "id": "step-14", "title": "Track D", "reason": "concurrent lane D", "concurrent": true }
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

### `wip-plumbing setup <deps|direnv|hygiene|release|agents|lds> [--force]`

Install-time deterministic scaffold writers, one per capability (step-14
shipped the first five; `setup lds` is the step-15 follow-up — the
sixth verb, see [Sub-section below](#setup-lds-the-sixth-verb-the-lds-scaffold)).
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

The `setup deps` devShell already lists **`coreutils`**, which provides
**`sha256sum`** — so `extract --verify-hashes` works in the pure nix dev
shell with **no new package**. (`shasum` is the perl tool, not a flake
package; the hasher leads with `sha256sum` and falls back to
`shasum -a 256` outside nix.)

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
| `setup lds` | `features.lds.{enabled, root: engineering}` | `engineering/.lds-manifest.yaml` |

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

#### `setup lds` — the sixth verb (the LDS scaffold)

Step-15 follow-up. Writes the LDS install scaffold into the consumer
repo so `graduate` / `extract` (the LDS seam verbs) have a place to
land. Inherits every shared `setup` contract above (three-way
idempotency, `--force`, `--dry-run`, JSON ledger, manifest flag flip,
sentinel post-check, `--project` forwarding). Verb-specific shape
below.

**Writes (full mode — 13 files):**

- `engineering/.lds-manifest.yaml` — the sentinel; ships as an
  approved-shape manifest with `entries: []` so `extract` exits 4
  `manifest-empty` (correctly) until the consumer authors entries.
- `engineering/{decisions,product,architecture,specs,reference,features,implementation,appendices}/.gitkeep`
  — 8 zero-byte files so the canonical LDS layer dirs survive
  `git add`. Matches step-15's `WIP_GRADUATE_LAYERS` allowlist.
- `engineering/maintenance/{audit,refine,sync,update}.md` — verbatim
  copies of the LDS maintenance workflow files; the `{ENG_DOCS_DIR}`
  placeholders in `audit.md` are instruction text read by an AI agent
  at LDS-workflow time, not template substitutions this verb performs.

**Hardcoded LDS root.** v1 always writes to `engineering/`. If the
consumer's `.wip.yaml` already has `features.lds.root` set to
something other than `engineering` (or unset), the verb exits 3
`lds-already-installed-elsewhere` with `error.path` carrying the
existing root. Configurable `--root <dir>` is a follow-up.

**`--sentinel-only` flag (lds only).** Writes ONLY
`engineering/.lds-manifest.yaml`, skips the `.gitkeep` files and the
`maintenance/*.md` copies. Still flips both feature-flag keys. Use
when adopting LDS in a repo whose `engineering/` tree already has
hand-authored content (or content from another tool). Passing
`--sentinel-only` to any other `setup` subcommand exits 2 `usage`.

**Manifest flag flip.** Sets both
`features.lds.enabled: true` AND `features.lds.root: engineering` in
the same yq-in-place update. The `root` key is what
`_wip_feature_records` reads to compute the sentinel path, so flipping
both keeps `detect` / `doctor` consistent without a separate edit.

**Post-write invariant.** `engineering/.lds-manifest.yaml` exists and
`wip-plumbing doctor` reports zero LDS drift. Pinned by
`test/test-setup.sh`'s sentinel + doctor checks.

**Out of scope for v1.** No migration mode (multi-session LLM workflow —
porcelain territory); no per-layer template files
(`_template.md` / `0000-template.md`); no plural LDS installs
(`features.lds.installs[]`); no upgrade workflow. The 13 files are the
fresh-install minimum that unblocks `graduate` / `extract`.

### `wip-plumbing graduate <artifact> [--to <slot>] [--force]`

Promote one wip-internal planning artifact to its LDS canon slot
(step-15). The single-artifact LDS seam per
[ADR-0006](../decisions/0006-wip-owns-seams-not-tools.md): the verb
*invokes the deterministic core* of LDS's extract workflow for the
one-artifact case and never re-implements the LLM-driven `analyze` /
`review` phases (those stay in the porcelain).

The target slot comes from the artifact's `graduate-to:` front-matter
directive (relative to the LDS root, e.g. `decisions/0010-foo.md`),
overridden by `--to <slot>` when present. The directive itself is
**stripped** on write; all other front-matter keys (status, date, etc.)
pass through unchanged.

**Shorthand: `decisions/auto-<slug>.md`.** Resolves to
`decisions/<next-NNNN>-<slug>.md` where `next-NNNN` is one above the
highest existing 4-digit prefix in `<eng-docs>/decisions/`. Empty
directory → `0001`. Auto-numbering is **decisions-only**; using
`auto-*` outside `decisions/` exits 4 `bad-auto-slot`.

**Layer allowlist.** The first path segment of the target must be one of
the canonical LDS layers (`decisions`, `product`, `architecture`,
`specs`, `reference`, `features`, `implementation`) plus `maintenance`
and `appendices`. Anything else exits 4 `unknown-layer`.

**Write contract: three-way idempotency** (same as `setup`):

- **absent** → write rendered body; status `wrote`.
- **byte-equal** → silent skip; status `skipped_idempotent`.
- **differs** → exit 4 `content-drift` with the path in `error.path`;
  `--force` overwrites and records `wrote_forced`.

**LDS preconditions.**

- `features.lds.enabled: false` → exit 3 `lds-not-enabled`.
- `enabled: true` but `<eng-docs>/.lds-manifest.yaml` missing → exit 3
  `lds-sentinel-missing` (hint: install LDS scaffold; `setup lds` is
  backlog).

**Reads:** the artifact file; `.wip.yaml` (for the LDS root +
sentinel); `<eng-docs>/decisions/` (for auto-NNNN scanning).

**Writes:** the rendered target file (or just the ledger with
`--dry-run`).

**Exit:**

- **0** — success (including idempotent skip and `wrote_forced`).
- **2** — bad args (unknown flag, missing artifact arg, unexpected arg).
- **3** — `lds-not-enabled` / `lds-sentinel-missing` /
  `missing-manifest`.
- **4** — `bad-artifact` (file missing), `no-target` (no directive and
  no `--to`), `bad-target` (absolute / `..` / not `<layer>/<file>`),
  `unknown-layer`, `bad-auto-slot`, `content-drift`.

**stdout (success):**

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

**stdout (content drift):**

```json
{
  "ok": false,
  "verb": "graduate",
  "error": {
    "code": 4,
    "kind": "content-drift",
    "message": "target differs from artifact; re-run with --force to overwrite",
    "path": "engineering/decisions/0010-graduate-seam.md"
  }
}
```

### `wip-plumbing extract [--manifest <path>] [--force] [--verify-hashes]`

Run the deterministic LDS Extract phase against an approved extraction
manifest (step-15). The bulk-from-manifest LDS seam: reads a
manifest, walks `entries[]`, writes each target. The LLM-driven
`analyze` / `review` phases (which generate / approve the manifest)
stay in the porcelain.

**Manifest discovery.** Default: `<eng-docs>/.lds-manifest.yaml` (the
same path the `lds` feature's sentinel rule points at, so a single
source of truth for detect / doctor / extract). Override:
`--manifest <path>`.

**v1 scope (the minimum viable LDS seam — see workplan step-15 for
deferral rationale):**

| Aspect | v1 status |
|---|---|
| `verbatim` mode | **supported** |
| `content` mode | **supported** |
| `transform` mode | **skipped** (`unsupported[]`) |
| `summarize` mode | **skipped** (`unsupported[]`) |
| Simple-path source (string) | **supported** |
| Single-file with line range | **supported** |
| Multi-file source (`source.files[]`) | **skipped** |
| SHA-256 source hash verification | **opt-in** via `--verify-hashes` (default skipped-v1; see below) |
| Templates + `field_mappings` | **skipped** (`unsupported[]`) |
| `--resume` mode | **not implemented** |
| Extraction report file | **written** (`extraction-report.{yaml,md}`; see below) |

Skipped (unsupported) entries do **not** fail the run; other entries
in the same manifest still execute. The ledger names every skip so the
consumer can see what didn't land.

**Extraction report (LDS §7).** Every run serializes the stdout ledger to
two files at the eng-docs root:

- `<eng-docs>/extraction-report.yaml` — the §7.2 machine-readable
  structure (`extraction_report:` with `metadata` / `summary` /
  `files_created[]` / `unsupported[]` / `verification_results` / `errors[]`
  / `source_changes`).
- `<eng-docs>/extraction-report.md` — the §7.4 human-readable summary
  (FILES CREATED / LAYER SUMMARY / SUMMARY / VERIFICATION / `Status:` line,
  where `Status:` is `COMPLETED` or `COMPLETED WITH ERRORS`).

The report is **always written, including on partial failure** — on the
`exit 4` drift / bad-shape paths it is written **before** the exit, so a
failed run still leaves a report (§7.3). It honors `--dry-run`: under
`--dry-run` (`WIP_DRY_RUN=1`) **nothing** is written (no report, no
targets), consistent with the global flag's touch-nothing contract.

The report write is a **plain overwrite — it is *not* three-way
idempotent** and bypasses the idempotency helper that guards extracted
targets. The report embeds a fresh `executed_at` timestamp, so it differs
every run by construction; routing it through idempotency would make every
second run report spurious `content-drift` on the report file itself.
Re-running `extract` on an unchanged tree therefore regenerates the report
while the extracted *targets* stay idempotent.

**v1 field-availability caveats.** The report is a faithful subset of what
v1 tracks — it never fabricates numbers. Genuinely-unavailable fields are
self-documenting:

- `line_statistics` fields and `layer_breakdown.<layer>.total_lines` are
  `null` (v1 does not count source/output lines).
- `verification_results.line_count_check.status` is `"skipped-v1"`.
  `content_hash_check.status` is `"skipped-v1"` by default and becomes a
  real `"pass"` / `"fail"` verdict under `--verify-hashes` (see
  **Source-hash verification** below); `file_existence_check` is always
  computed live.
- `metadata.manifest_hash` is a SHA-256 of the manifest file (`shasum -a
  256`), or `null` if `shasum` is unavailable.

**Source-hash verification (`--verify-hashes`).** Off by default; when
set, `extract` verifies declared source hashes as a **pre-write gate**
before any target is written (LDS `source_hash_mismatch_handling`:
verify at the start, *"never silently proceed when source has changed"*).

- **What is verified.** Only `entries[].source.hash` on **single-file**
  sources that classify `ok-verbatim`. Simple-path (string) sources,
  single-file entries with no `hash`, `content`-mode entries, and anything
  already routed to `unsupported[]` / `bad_entries[]` are **not verifiable**
  and are counted in `content_hash_check.entries_no_hash` — never failed.
  The top-level whole-file `sources.<path>.hash` registry and multi-file
  `combined_hash` are **out of v1 scope** (multi-file never reaches the
  write path).
- **Hash recipe (locked).** The digest covers the **extracted source-range
  body bytes exactly as written to the target, minus the attribution block**
  — i.e. the `cat` / `awk` output for the entry's source range. For a line
  range this includes the **trailing newline** the extractor emits (range
  bytes are `awk 'NR>=start && NR<=end'`); for a whole-file source it is the
  raw file bytes (`cat`). The reference computation is
  `awk 'NR>=S && NR<=E' <src> | sha256sum` (range) or `sha256sum <src>`
  (whole file). The hasher is `sha256sum` (from `coreutils`), falling back
  to `shasum -a 256`. A manifest producer that wants its hashes to verify
  must hash these same bytes.
- **Pre-write gate.** If every declared hash matches, the write loop runs
  unchanged and the run exits 0 with `hash_verification: "verified"`. If any
  hash mismatches — or a hashed source file is **missing** — the run writes
  **no target at all** (no partial extraction), sets
  `error.kind: "hash-mismatch"`, and `exit 4`. The §7 report is still
  written **first** (with `content_hash_check.status: "fail"`), before the
  exit.
- **No-op signal.** A `--verify-hashes` run where *no* entry carries a
  verifiable hash exits 0 with `hash_verification: "no-hashes"` and a
  report `warnings[]` entry (`type: "no-verifiable-hashes"`) so the consumer
  knows the flag did nothing.
- **`--dry-run` interaction.** Verification still runs (it is read-only) and
  a mismatch still yields `exit 4 hash-mismatch` in the ledger — but, like
  every dry-run path, **neither targets nor the report are written**.

The populated `content_hash_check` object (in `verification_results`):

```yaml
content_hash_check:
  status: skipped-v1 | pass | fail   # skipped-v1 when the flag is off
  entries_checked: 2                 # entries with a verifiable hash
  entries_matched: 2
  entries_no_hash: 1                 # verifiable-shape entries lacking a hash + non-verifiable entries
  mismatches:                        # [] unless status == fail
    - { id, source, expected_hash, actual_hash, status }  # status: mismatch | missing
```

The `.md` report's `Content hash check:` line renders this dynamically:
`skipped-v1`, `pass (2/2 entries matched)`, or `fail (1/2 matched, 1 mismatch)`.

**Per-entry write contract: three-way idempotency** (same as
`graduate` and `setup`). A bytes-equal target is silently skipped; a
drifted target is refused with `exit 4 content-drift` (paths in
`error.paths`); `--force` overwrites.

**Source attribution comments** (LDS §6.3) are prepended to every
written target — exact format:

```html
<!-- Migrated from legacy/foo.md:45-120 -->
<!-- Extraction ID: vision-main -->
```

For content mode: `<!-- Generated content - no source file -->` plus
the Extraction ID line. The attribution is part of the bytes
idempotency compares.

**Manifest validation** (required-fields only):

- `metadata.schema_version` must match `1.x.x` (any 1.x).
- `metadata.status == "approved"`.
- `entries` non-empty.
- Entry ids unique.
- Each entry has `id`, `target`, `mode` (one of
  verbatim/content/transform/summarize), and a `source` when
  `mode != content`.

**LDS preconditions.** Same as `graduate`:
`lds-not-enabled` (exit 3) / `lds-sentinel-missing` (exit 3).

**Reads:** `.wip.yaml`; the manifest at the default or `--manifest`
path; every entry's source file (for `verbatim` mode).

**Writes:** one target per supported entry, plus the extraction report
files `<eng-docs>/extraction-report.{yaml,md}` (always, including on
partial failure); or, with `--dry-run`, just the ledger (no targets, no
report).

**Exit:**

- **0** — success (every entry wrote, skipped, or was logged as
  unsupported / bad-shape *without any drift*).
- **2** — bad args (unknown flag, value missing).
- **3** — `lds-not-enabled` / `lds-sentinel-missing` /
  `missing-manifest`.
- **4** — `manifest-missing`, `manifest-unparseable`,
  `manifest-not-approved`, `incompatible-schema`, `manifest-empty`,
  `duplicate-entry-id`, `bad-entry-shape`, `content-drift`,
  `hash-mismatch` (only with `--verify-hashes`).

**stdout (success):**

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
  "bad_entries": [],
  "hash_verification": "skipped-v1"
}
```

`hash_verification` is `"skipped-v1"` by default, `"verified"` when
`--verify-hashes` checked ≥1 hash and all matched, or `"no-hashes"` when
`--verify-hashes` found zero verifiable hashes.

**stdout (content drift):**

```json
{
  "ok": false,
  "verb": "extract",
  "manifest": "engineering/.lds-manifest.yaml",
  "error": {
    "code": 4,
    "kind": "content-drift",
    "message": "extracted targets differ from manifest output; re-run with --force to overwrite",
    "paths": ["engineering/decisions/0001-foo.md"]
  }
}
```

**stdout (hash mismatch — only with `--verify-hashes`):**

```json
{
  "ok": false,
  "verb": "extract",
  "manifest": "engineering/.lds-manifest.yaml",
  "error": {
    "code": 4,
    "kind": "hash-mismatch",
    "message": "source hash verification failed; no targets written (run with the correct source or regenerate the manifest hashes)",
    "paths": ["legacy/foo.md"],
    "mismatches": [
      {"id": "vision-main", "source": "legacy/foo.md", "expected_hash": "abc…", "actual_hash": "def…", "status": "mismatch"}
    ]
  }
}
```

A `status` of `"missing"` (with `actual_hash: null`) marks a hashed source
file that does not exist. No target is written on this path; the §7 report
is written first (with `content_hash_check.status: "fail"`).

**Templates dir resolution:** not relevant — `extract` does not read
the `wip` templates dir (the LDS templates referenced in
`field_mappings` are in the consumer's own LDS install, and v1 skips
templated entries anyway).

**`--dry-run`:** computes the ledger reflecting what *would* be
written / skipped / refused; touches neither files nor manifest.

---

## 4. Open questions (resolve before/while building step-06–08)

1. **`status` git dependency** — *resolved in step-08.* When `.wip/` is gitignored
   (default), `git status --porcelain -- .wip/` reports nothing → `dirty_wip_files: []`.
   The porcelain layer may mtime-augment later; plumbing stays git-only.
2. **`roadmap.md` parsing** — *resolved in step-08; extended for lanes by ADR-0010.* The
   bullet form already in use is the v1 grammar: `## Round <N> — <title>` rounds (with
   optional trailing `✅ shipped <YYYY-MM-DD>`); steps as `- **step-<NN[.5]> — <title>**`
   bullets (with optional `✅` marker and `shipped <YYYY-MM-DD>` date); `## Backlog`
   sections parsed as `- **<title>** — <body>` entries. The amendment-form
   `### step-NN — <title>` heading is recognized too. **Lanes (ADR-0010):** a round may
   contain `### Lane <name>` subheadings; every step parses with a `lane` field
   (`null` for main-lane), every round with a `lanes[]` array, and the document with a
   `lane_errors[]` array (empty when well-formed; populated for `lane-outside-round`,
   `nested-lane`, `duplicate-lane`, `main-step-between-lanes`). The grammar within a round
   is `main* (lane+)? main*` — pre-lane prereqs, lane blocks, post-lane sync steps. No
   front-matter; the parser lives in `lib/wip/wip-plumbing-roadmap-lib.bash`.
3. **`intake` validators** — *resolved by ADR-0009 + `intake-kinds.md`*; shipped in
   step-07.5. The closed kind vocabulary and per-kind shape rules are now enforced by
   `wip-plumbing intake validate`.
4. **Amendment idempotency hash** — *resolved in step-08.5.* SHA-256 of the
   **rendered insertion payload** (bullet line or appended round block), not the source
   artifact's bytes. Identical inserts shaped from differently-framed artifacts collapse
   to the same hash. Marker line: `<!-- wip-amend: <sha256> -->` immediately after the
   inserted block.
