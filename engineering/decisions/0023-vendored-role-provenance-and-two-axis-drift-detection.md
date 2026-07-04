# 0023 — Vendored role provenance & two-axis drift detection

- Status: accepted
- Date: 2026-07-03
- Source: BDS-58 *Detect & refresh stale vendored wip role/agent copies* / initiative `wip-orchestration-robustness`, step-02; ADR-0005, ADR-0007, ADR-0015, ADR-0020
- Amends: ADR-0015 (resolves the deferred Q-05.4 doctor render fan-in), ADR-0020 (extends the single-`cmp` vendored-drift gate to a provenance-anchored two-axis classifier)

## Context

ADR-0020 flattened the vendored orchestration install into project-scope,
self-contained agent files (`.claude/agents/wip/{orchestrator,coordinator,
researcher,builder}.md`) plus the relocated slash-commands
(`.claude/commands/wip/*.md`). Its only drift gate is `setup agents --check`
(`_wip_setup_agents_check`), which for each vendored file computes **one**
comparison:

```
render(plugin_roles@installed, backend)   vs   on-disk vendored file
        └── R_now (re-render) ─┘                └── D_now ─┘
```

A mismatch exits rc 4 (`agents-drift`) but **cannot say why**. With only two
points (`R_now`, `D_now`) and one comparison it conflates two independent
failure modes:

- **upstream advanced** — the installed plugin's `roles/` moved past what was
  vendored (a plugin-side role fix hasn't reached this consumer), vs
- **locally modified** — someone hand-edited the on-disk vendored file.

Splitting those axes needs a **third anchor**: the render captured *at vendor
time*. Nothing records it today. That anchor is the provenance stamp this ADR
adds.

Two facts pin the mechanics (both verified in-repo):

1. **"Upstream" = the installed plugin cache.** `_wip_flatten_roles_dir`
   resolves `WIP_ROLES_DIR → $root/roles → $CLAUDE_PLUGIN_ROOT/roles`. A
   `source: vendored` consumer (ADR-0020) ships no `roles/` and sets no seam, so
   `R_now` re-renders from `$CLAUDE_PLUGIN_ROOT/roles` — the globally-installed
   plugin. "Upstream advanced" therefore means *"if I re-ran `setup agents` now,
   I'd get different bytes."*
2. **Plugin version is read nowhere today.** No code reads
   `.claude-plugin/plugin.json` `.version`. Stamping it is new — a small helper
   over `$CLAUDE_PLUGIN_ROOT/.claude-plugin/plugin.json`.

**Load-bearing constraint (KV `note-duo-vendored-gitignored`).** Consuming repos
gitignore `.claude/agents/wip` + `.claude/commands/wip` **by design** —
workspace-only regenerable state, the same class as `.wip/` / `.wip.yaml`. So
the drift gate operates on the **on-disk workspace file**, never a tracked git
object; a sync's acceptance proof is **render-and-diff byte-identical**, never a
commit/SHA; and the sidecar itself is likewise gitignored workspace state (no
`git add -f`).

## Decision

### D1 — Provenance lives in a sidecar manifest, NOT frontmatter

Stamp into a single JSON sidecar at **`.claude/agents/wip/.provenance.json`**,
one entry per vendored file (the 4 agents *and* the N `.claude/commands/wip/*.md`
copies). Each entry records:

| Field | Meaning |
|---|---|
| `path` | repo-relative path of the vendored file |
| `kind` | `agent` \| `command` |
| `source_ref` | agent: `roles/<role>.md @ backend <b>`; command: `templates/setup/agents/commands/<name>.md` |
| `plugin_version` | the installed plugin `.version` at vendor time |
| `baseline_hash` | sha256 of the bytes written at vendor time (`B`) |
| `vendored_at` | ISO date the stamp was written (honors the `WIP_NOW` seam) |

Envelope: `{ "schema": 1, "files": [ <entry>, … ] }`.

Justification vs. frontmatter:

- The existing `setup agents --check` render-and-diff is a **byte** `cmp` of the
  agent/command files. A frontmatter stamp would force the renderer to
  deterministically reproduce it — impossible for a self-content-hash (circular)
  or a timestamp (non-reproducible) — and would break `--check`.
- Frontmatter is the agent's live system-prompt config that Claude Code parses;
  provenance keys pollute it.
- A sidecar keeps the agent/command bytes **pristine**, so ADR-0020's
  render-and-diff invariant survives verbatim.

The file is dot-prefixed and not `*.md`, so it is never globbed as a subagent
(unlike the retired F4 README); it is gitignored workspace state like the copies
it describes. `baseline_hash` **must** be persisted because the vendor-time
render **cannot be reconstructed later** (the plugin has since moved) — it is the
pivot both axes compare against.

### D2 — Two orthogonal axes, computed against the stamped baseline `B`

With `B = baseline_hash`, `R_now = sha256(re-render now)` (agents) /
`sha256(current template)` (commands), `D_now = sha256(on-disk file)`:

- `upstream_advanced := R_now != B` — the plugin render differs from what we
  vendored.
- `locally_modified  := D_now != B` — the on-disk file differs from what we
  vendored.

These are **independent**. The 4-quadrant table (plus three degenerate states)
and the recommended action:

The six states (`upstream_advanced` carries a **direction**, D2a):

| State | `upstream_advanced` | `locally_modified` | Meaning | Action (by direction) |
|---|---|---|---|---|
| `clean` | no | no | disk == baseline == current render | none |
| `upstream-advanced` | yes | no | plugin render differs; file untouched | **ahead** → auto-sync (re-render, overwrite, restamp); **behind**/**indeterminate** → skip-and-warn (recoverable via `--force`) |
| `locally-modified` | no | yes | plugin unchanged; file hand-edited | **warn**; sync would clobber → require `--force` (backs up `.orig`) |
| `both-diverged` | yes | yes | plugin render differs AND file hand-edited | **conflict**; `--force` takes upstream + backs up local |
| `unstamped` | — | — | no sidecar entry (legacy vendor, e.g. Duo today) | run `--sync` to **adopt-in-place** (stamp current bytes; never re-render) |
| `missing` | — | — | sidecar names a file that is gone | run `--sync` to re-vendor |

The `--status` summary has six mutually-exclusive buckets (`clean` /
`upstream_advanced` / `locally_modified` / `both_diverged` / `unstamped` /
`missing`); the per-file record additionally carries `direction`.

### D2a — Direction is a 3-value taxonomy, gated by an ordering invariant

`upstream_advanced` (`R_now != B`) means the plugin render no longer matches the
baseline — but says nothing about *which* byte-set is newer. The stamp carries
`plugin_version`; comparing it (component-wise semver, `_wip_setup_version_lt` —
pure-bash, no `sort -V`) to the freshly-read installed version yields:

- installed **>** stamped → **ahead** — the plugin genuinely moved forward.
- installed **<** stamped → **behind** — the installed plugin lags a newer stamp.
- installed **==** stamped (or either `unknown`) **but content differs** →
  **indeterminate** — the versions cannot tell us which is newer. This is the
  **Duo forward-port case**: `coordinator.md` was forward-ported from the wip
  working tree, but the working tree and the released plugin *both* label
  `0.0.17` while rendering differently, so no version delta exists (verified
  in-repo: installed cache, wip `main`, and the forward-port source are all
  `0.0.17`). The DoD's original `upstream-behind` label for this case assumed a
  version delta that does not exist; the honest classification is
  `upstream-advanced / indeterminate`.

**Ordering invariant (load-bearing).** Auto-sync REQUIRES a **proven** `ahead`
(installed > stamped). `behind` and `indeterminate` both fall to skip-and-warn,
recoverable via `--force`. Rationale: a wrong auto-sync is a **silent,
unrecoverable regression** (it overwrites a forward-port with stale bytes; the
copy is gitignored, so there is no VCS undo), whereas a wrong skip costs only one
`--force`. When the axes are ambiguous, protect the disk.

This discharges step-01's inherited open question "Plugin re-release / version
lag." A true `behind` demo (and Duo eventually classifying `clean`) is a
**documented follow-up**: bump the plugin version so the forward-port carries a
newer stamp than the released cache. It is NOT required for this step — the
`indeterminate` skip-and-warn already delivers the regression-safety.

### D3 — The update path is FLAGS on the incumbent `setup agents`

`setup agents` already owns the render engine (`wip_flatten_render`), the
vendored writer (`_wip_setup_agents_vendored`), the idempotent write helper, the
JSON ledger, and the foreign-plugin guard — and ADR-0020's `--migrate` set the
precedent that new vendored actions are **flags** here, not top-level
subcommands. So the roadmap's floated `wip vendor status`/`sync` shape lands as:

- **`setup agents --status`** — read-only two-axis report. Prints a human table
  (`PATH | KIND | STATE | STAMPED | INSTALLED | ACTION`) on stderr and a JSON
  envelope on stdout: `{ok, verb, files:[{path,kind,state,stamped_version,
  installed_version,action}], summary:{clean,upstream_advanced,locally_modified,
  both_diverged,unstamped,missing}}`. **Exit 0 always** (reporting, not gating).
- **`setup agents --sync [--force] [--dry-run]`** — the action, per (state,
  direction). `--dry-run` routes through the existing `WIP_DRY_RUN` seam. JSON:
  `{ok, verb, dry_run, synced, skipped_clean, skipped_regressive, refused_local,
  backed_up, restamped}`. Per-state:
  - `clean` → skip.
  - `upstream-advanced` **ahead** → re-render + overwrite + restamp; **behind** /
    **indeterminate** → `skipped_regressive` (distinct warning per direction);
    **`--force`** is the explicit override — take the installed render + restamp
    (no `.orig`: the file was not locally modified).
  - `locally-modified` / `both-diverged` → refuse (rc 4) without `--force`; with
    `--force` write a `<file>.orig` backup (no git undo — gitignored) then take
    upstream + restamp.
  - **`unstamped` → adopt-in-place**: stamp `baseline_hash = sha256(on-disk
    bytes)`, `plugin_version = installed`, and **write NO file bytes**. The
    on-disk bytes are the only defensible baseline for a legacy pre-step-02
    install. **Never re-render-overwrite an unstamped file** — doing so would
    clobber a forward-port (Duo's `coordinator.md`) with the stale installed
    render *before* the direction logic ever runs. (Fresh installs stamp at write
    time per C2, so `unstamped` only ever arises for legacy installs.)
  - `missing` → re-vendor + stamp.

Both flags are agents-only and, like `--check`, inert to the write-flags. The
shared engine is `_wip_setup_agents_provenance_classify <root> <td>` — **one
oracle** emitting `<state><TAB><path><TAB><direction>` per file, reused by
`--status`, `--sync`, and `doctor` (ADR-0020 D8's single-classifier principle).

"Runtime-reference instead of vendoring" is **rejected as primary** — it is
already the `source: plugin` mode a vendored consumer deliberately opted out of
(Duo hits F1: it is itself a plugin). It stays the escape hatch
(`--source plugin` / `--migrate` to the plugin end-state), not this step's
mechanism. A `wip vendor` porcelain alias is deferred sugar.

### D4 — `--check` stays the blunt CI gate

Do **not** change `--check`'s exit contract (any difference → rc 4). That keeps
`test-setup.sh` / `test-agents-commands-sync.sh` green and the CI gate simple.
The refined classification is **additive**: `--status` reports the quadrant +
direction; `--sync` acts on it; `doctor` surfaces it. Teaching `--check` to
suppress the `upstream-advanced` (behind/indeterminate) false positive is noted
as an open question, lean defer.

### D5 — Land the DEFERRED doctor fan-in, closing ADR-0015 Q-05.4

Add a `doctor` check, gated on `.features.orchestration.source == "vendored"`,
that runs the **same** classifier as `--status` (one oracle) and, on any
non-`clean` file, appends:

```json
{"kind":"orchestration","status":"vendored-drift","state":"<quadrant>",
 "fix":"<setup agents --sync | …--sync --force | upgrade the plugin>",
 "paths":[…]}
```

→ exit 4. Gating on `source: vendored` means the wip repo's own `doctor`
(`source: plugin`) is unaffected and pays **no render cost** — it fires only in a
vendored consumer, exactly where drift must surface. This is the render fan-in
ADR-0015 Q-05.4 explicitly backlogged; step-02 is chartered to close it. It is
distinct from the step-07 pure-disk legacy-footprint check (which stays as-is): a
stale *footprint* on disk and *render drift* in an installed vendored agent are
orthogonal probes, now both present in `doctor`.

### D6 — This ADR is 0023, amending 0015 + 0020

Highest ADR number on disk and across all in-flight branches
(`bds-20`/`bds-22`/`bds-60`/`setup-backends`) at authoring time was **0022**, and
the sibling step-04 (Lane backends, BDS-18) mints no ADR — so **0023** is
unclaimed and taken here. This ADR **amends ADR-0015** (resolves the deferred
Q-05.4 doctor fan-in) and **amends ADR-0020** (extends the vendored-drift gate
from a single byte-`cmp` to a provenance-anchored two-axis classifier + the
sidecar artifact).

## Consequences

- A vendored `setup agents` (and its reuse inside `--migrate`) writes
  `.claude/agents/wip/.provenance.json` **after** the files land, one entry per
  agent + command, `baseline_hash = sha256(bytes just written)`. It honors
  `WIP_DRY_RUN` (plan, write nothing) and is a **side artifact** — not counted in
  the write ledger's `wrote`/`skipped` arrays, so existing install-count asserts
  stay green. The agent/command bytes are unchanged, so `--check` still passes.
- The sidecar is the only new persistent artifact; it is out of the render path,
  so `test-flatten-render.sh` determinism is unchanged.
- `_wip_sha256` prefers `sha256sum`, falls back to `shasum -a 256` (macOS/Linux
  parity). `_wip_plugin_version` reads `$CLAUDE_PLUGIN_ROOT/.claude-plugin/
  plugin.json` `.version`, falling back to the resolved-roles-dir parent
  manifest, then the wip install manifest; it records `"unknown"` rather than
  failing the vendor if none resolves.
- Escape hatch unchanged: a repo that is itself a plugin stays `source: plugin`
  (`--source plugin` / `--migrate`), where nothing is vendored and no sidecar is
  written — the drift machinery is a vendored-only concern throughout.

## Open questions (resolved during execution)

- **`.orig` backup on `--force` (resolved: yes).** The gitignored copy has no VCS
  undo, so `--force` writes `<file>.orig` before overwriting. `.orig` is not
  `*.md` → not globbed, gitignore-safe.
- **Single sidecar vs. per-surface (resolved: single).** One
  `.claude/agents/wip/.provenance.json` referencing both surfaces by
  repo-relative path — one read/write, mirrors the unified gate; the agents dir
  is the always-present anchor.
- **`--check` false-positive suppression (deferred).** `--check` stays blunt; the
  `upstream-advanced` behind/indeterminate nuance routes through `--status`/`doctor`.
- **Plugin version bump for a true `behind` (documented follow-up).** Bumping the
  wip plugin version so a forward-port carries a newer stamp than the released
  cache would let Duo's `coordinator.md` classify a genuine `behind` (and,
  post-release, `clean`). Not required this step — `indeterminate` skip-and-warn
  already delivers the regression-safety (D2a ordering invariant).
- **Per-role `--role` selector (deferred).** `--status`/`--sync` operate on the
  whole install (parity with `--check`/`--migrate`).
