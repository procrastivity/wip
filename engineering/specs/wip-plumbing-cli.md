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
and never makes a judgment a human/porcelain should make. v1 ships **six verbs**:

| Verb | One-line | Roadmap step |
|------|----------|--------------|
| `detect` | What features/initiatives exist, per `.wip.yaml`. The mandatory first call. | step-06 |
| `doctor` | Verify the manifest against disk; report (and optionally fix) drift. | step-06 |
| `init` | Scaffold the repo manifest and/or an initiative from `templates/`. | step-07 |
| `intake` | Validate inbound planning artifacts (shape only). | step-07 |
| `status` | Where am I: current initiative, round, active step, dirty `.wip/`. | step-08 |
| `next` | Ranked candidates for what to do next (no choice — that's the porcelain). | step-08 |

Non-goals for v1: `setup`, `graduate`/`extract`, `orchestrate`/`spawn`, `glossary`
(assembler). They are later roadmap steps and get their own specs.

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
`--json/--no-json`, `--dry-run` (state-mutating verbs: print the write ledger, touch nothing).

### Manifest discovery & env
- `.wip.yaml` is found by walking up from `$PWD` to the first match (the **repo root**).
  All relative paths in output resolve against that root.
- `WIP_LIB` — override `lib/wip/` path (dev installs).
- `WIP_ROOT` — force the repo root, skipping the walk-up.
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
- **Writes:** nothing, unless `--fix` (materializes a missing stanza for a present-but-undeclared sentinel; prunes a registry entry whose directory is gone — each `--fix` action is logged and reversible-by-diff).
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

### `wip-plumbing intake --validate <file>...`
Deterministic **shape** check on inbound planning artifacts (PRD/handoff/proposal). It
does **not** compose a roadmap — that's the porcelain. Validates that a file is parseable
and carries the minimum a Brief/Proposal needs (title, a goal/summary section).

- **Reads:** the named files.
- **Writes:** nothing.
- **Exit:** 0 if all valid; **4** if any file fails shape validation; 2 if no files given.
- **stdout:**
```json
{ "ok": true, "results": [ { "file": "handoff.md", "valid": true, "kind": "ad-hoc-brief", "missing": [] }, { "file": "prd.md", "valid": false, "kind": "unknown", "missing": ["title","goal"] } ] }
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

---

## 4. Open questions (resolve before/while building step-06–08)

1. **`status` git dependency** — if a consumer's `.wip/` is gitignored (default), `git
   status -- .wip/` reports nothing. Fall back to mtime-based "recently touched"? Or
   accept "dirty" is empty for gitignored `.wip/`? *Lean: accept empty; dirty-tracking is
   a committed-`.wip/` nicety.*
2. **`roadmap.md` parsing** — Steps are `### step-NN — title` with a `✅` / "shipped"
   marker. Define the exact grammar `next`/`status` parse, or add lightweight front-matter
   per step? *Lean: parse the heading + a `Status:` line; avoid front-matter to keep
   roadmaps human-first.*
3. **`intake` validators** — how strict is "valid"? v1 = parseable + has a title and a
   goal/summary heading. Richer kind-detection (PRD vs handoff vs spec) can come later.
