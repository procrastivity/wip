# w2 — Project-type-aware bootstrap tooling (`wip setup *`)

## TL;DR

- **The canonical baseline is xcind**, end-to-end: 3-line `CLAUDE.md` → `AGENTS.md`
  pointer; `.envrc` = pinned `nix-direnv` shim + `use flake`; `flake.nix` exposing a
  `devShells.default` whose `shellHook` *idempotently* installs `pre-commit`
  hooks; `Makefile` as the verb surface (`format/lint/test/check/changelog-preview`);
  `.pre-commit-config.yaml` ordered `secrets → formatters → manifest checks → linters`;
  `changelog-portable-stub` (cliff.toml + Makefile snippet + releasing doc) for
  releases. Everything else in the corpus is a partial or drifted copy.
- **The drift-to-prevent is `prtend`-shaped**: `CLAUDE.md` and `AGENTS.md` are
  byte-identical (`prtend/CLAUDE.md:1`, `prtend/AGENTS.md:1`) instead of pointer→
  canonical, and `prtend/cliff.toml` is a hand-edited *earlier* generation than the
  stub's templated version (no `[remote.github]`, different parser keywords). The
  goal of `wip setup` is to make the xcind shape the path of least resistance and
  the prtend shape impossible to land by accident.
- **Detection is overwhelmingly deterministic.** Project-type sniffing reduces to
  presence-of-file checks: `pyproject.toml`/`uv.lock` → python-uv;
  `composer.json` → PHP; `package.json` (no framework markers) → Node;
  `Cargo.toml` → Rust; `*.sh|bin/*` no other markers → shell; `Dockerfile` →
  layer hadolint on top of whatever else matched; `flake.nix` present → "already
  bootstrapped, offer migrate not init". Only `agents` (writing prose) and the
  *release-notes wiring* need an agent; everything else is a script.
- **Recommended command family** (dual CLI + slash command): `wip setup direnv`,
  `wip setup flake`, `wip setup hygiene`, `wip setup release`, `wip setup agents`,
  `wip setup dependabot`, plus an umbrella `wip setup all` that runs the project
  through every applicable subcommand in dependency order and a `wip setup doctor`
  that reports drift against the baseline without writing.
- **Fold-in plan for existing installers**: `wip setup release` *wraps*
  `changelog-portable-stub/scripts/install-changelog-stub` (don't reimplement —
  it already has the protected-paths/token-replace model we want everywhere).
  `direnv-session-loader` is a *Claude Code plugin*, orthogonal to project
  bootstrap; `wip setup direnv` should not touch it but `wip doctor` should
  detect+recommend it. Adopt the `install-changelog-stub` protected-paths idiom as
  the **idempotency contract for every `wip setup` subcommand**.

## Recommendations

### 1. The canonical baseline (what "good" looks like)

A repo is "wip-baselined" when all of the following are true. Each row cites the
authoritative file in the corpus and notes the drift to detect.

| Concern | Canonical artifact (source-of-truth) | Drift / anti-pattern to flag |
|---|---|---|
| Agent instructions | `xcind/CLAUDE.md` is a 3-line pointer; `xcind/AGENTS.md` is canonical (xcind/CLAUDE.md:1, xcind/AGENTS.md:1) | `prtend` ships them byte-identical (prtend/CLAUDE.md, prtend/AGENTS.md) — duplication, will silently diverge |
| direnv | Pinned `nix-direnv` source_url + `use flake` (xcind/.envrc:1) | Bare `use flake` with no version pin (prtend/.envrc:1) — works, but loses reproducibility & the explicit version contract |
| dev shell | `flake.nix` `devShells.default` with `shellHook` that runs `pre-commit install --allow-missing-config` and `PATH_add bin` (xcind/flake.nix shellHook, prtend/.envrc:2) | bizapps-symfony-bot mixes `export`s into `buildInputs` (broken Nix — see bizapps-symfony-bot/flake.nix:21–27); shellHook does package install inline (bizapps-symfony-bot/flake.nix:36–53) which is OK for impure python but should be a *recipe variant*, not the default |
| Verb surface | `Makefile` with `format / lint / test / check / changelog-preview` (xcind/Makefile) — `check` = `lint test`, "REQUIRED before completing code tasks" promise wired in AGENTS.md (xcind/AGENTS.md:6) | bizapps-symfony-bot Makefile has an `install` target that mutates the venv outside Nix (bizapps-symfony-bot/Makefile:30) — fine for app repos, but the xcind contract is what `wip setup hygiene` should write |
| pre-commit | Ordering `secrets → eof/trailing → formatters → manifest checks → linters` with `exclude: ^\.envrc$` on shellcheck (xcind/.pre-commit-config.yaml) | prtend uses bare `entry: shellcheck` `language: system` (prtend/.pre-commit-config.yaml:3–7) — pinned, project-installed binary wins |
| Changelog | `cliff.toml` from the stub with `{{GH_OWNER}}/{{GH_REPO}}` token replacement + `[remote.github]` block + `Makefile.snippet`'s `changelog-preview` target + `docs/releasing-changelog.md` (changelog-portable-stub/stub/) | prtend/cliff.toml is an *earlier hand-rolled* version: no `[remote.github]`, different parsers (prtend/cliff.toml:30+); xcind/cliff.toml exists but is also drift |
| Dockerfile (if present) | hadolint in `.pre-commit-config.yaml` ordered after shellcheck | No project in the corpus has hadolint wired — **gap** (xcind has a Dockerfile, no hadolint hook) |
| Dependabot | **No project in the corpus has `.github/dependabot.yml`** — gap; wip should default to weekly `pip`/`npm`/`composer`/`github-actions`/`docker` ecosystems based on detection |
| Auto-trigger skills | xcind ships `.claude/skills/` for `add-installed-file`, `pre-commit-check` (referenced from xcind/AGENTS.md:17, xcind/AGENTS.md:43) | nobody else does — this is the *next-tier* convention to spread |

**North-star order of operations** (this is what `wip setup all` should follow):

1. `flake` (write `flake.nix` + `flake.lock` strategy)
2. `direnv` (write pinned `.envrc`; runs `direnv allow` only if user opts in)
3. `hygiene` (pre-commit + Makefile verbs)
4. `agents` (AGENTS.md canonical, CLAUDE.md pointer)
5. `release` (cliff + Makefile snippet + releasing doc)
6. `dependabot` (last — purely additive, never blocks)

### 2. The `wip setup` command family

All subcommands share a contract:

- **Detection-first.** Each subcommand has a pure `detect()` that returns a project
  profile (no questions, no writes). Used by `wip doctor` and by every other
  subcommand to decide defaults.
- **Protected-paths idempotency.** Adopt the model from
  `changelog-portable-stub/scripts/install-changelog-stub:35–43` verbatim: each
  subcommand declares a set of files it will never overwrite without `--force`,
  always overwrites a set of "snippets/recipes" the user is expected to merge by
  hand, and reports `write/skip-protected/replace` per file. Rerunning a
  subcommand is **always safe**.
- **Dry-run.** Every subcommand supports `--dry-run` → prints the same
  `write/skip` ledger without touching disk. `wip doctor` is `wip setup all
  --dry-run` with a different summarizer.
- **One question per ambiguity, never per file.** Use `AskUserQuestion`-style
  prompts only when detection can't decide; persist answers to `.wip/wip.yaml`
  so the next subcommand reads them instead of re-asking.

#### `wip setup direnv`

| | |
|---|---|
| Detects | `flake.nix?`, project type (see §3), existing `.envrc` content (`use flake` already? pinned? other directives?) |
| Asks | Only if `.envrc` exists and diverges from canonical: *"Existing `.envrc` is unpinned. Migrate to pinned `nix-direnv` shim?"* |
| Writes | `.envrc` from a project-type-specific template: shell/bash projects get `use flake\nPATH_add bin`; python (uv) gets the same + `dotenv_if_exists`; Node + `PATH_add node_modules/.bin`; PHP + `PATH_add vendor/bin` |
| Idempotent | `.envrc` is protected; rerun = no-op unless `--force` or `--migrate` |
| Deterministic? | **Yes — pure script.** No judgment needed past detection. |

#### `wip setup flake`

| | |
|---|---|
| Detects | Project type by file markers (table below); existing `flake.nix` parsed for `devShells.default` presence |
| Asks | If multiple project markers found (e.g. `pyproject.toml` + `package.json`): *"Primary language?"* |
| Writes | `flake.nix` from one of: `shell` (xcind shape), `python-uv` (bizapps shape, sanitized — no `export`s in buildInputs), `node`, `php`, `rust`. `flake.lock` is left to `nix flake update` |
| Idempotent | `flake.nix` protected; emits `flake.nix.proposed` next to existing for diff/merge |
| Deterministic? | **Yes — pure script** (templates are static; no LLM judgment for the standard stack) |

Project-type detection table (deterministic file-sniff, in priority order so we
pick the *primary* marker):

| Marker file(s) | Project type |
|---|---|
| `pyproject.toml` or `uv.lock` or `python-requirements.txt` | `python-uv` |
| `composer.json` | `php` |
| `Cargo.toml` | `rust` |
| `package.json` + `next.config.*` / `astro.config.*` / `vite.config.*` | `node-frontend` |
| `package.json` (no framework) | `node` |
| `go.mod` | `go` |
| `bin/*` + `*.sh` + no other markers | `shell` (xcind shape) |
| `Dockerfile` (modifier, not a primary type) | adds hadolint hook |
| `compose.yaml` / `docker-compose.yaml` | hint to suggest `xcind` integration |

#### `wip setup hygiene`

| | |
|---|---|
| Detects | Project type + file extensions actually present (`find . -name '*.py' -o -name '*.sh' -o -name 'Dockerfile' ...`) |
| Asks | Only if `.pre-commit-config.yaml` exists with hooks we don't recognize: *"Merge or replace?"* |
| Writes | `.pre-commit-config.yaml` assembled from a per-file-type hook table (shell→shfmt+shellcheck; python→ruff+ruff-format; Dockerfile→hadolint; always→gitleaks + eof-fixer + trailing-whitespace); `Makefile` *snippet* (never overwrites Makefile) with `format/lint/test/check` targets parameterized by project type |
| Idempotent | pre-commit config protected after first write; snippet always rewritten |
| Deterministic? | **Yes — pure script.** The hook→file-extension mapping is a table. |

#### `wip setup release`

| | |
|---|---|
| Detects | git remote (`gh-owner`/`gh-repo`); existing `cliff.toml`/`CHANGELOG.md` |
| Asks | *"Wire a git-cliff changelog?"* (default yes if any `.git/`). If remote can't be inferred: *"GH owner?"*, *"GH repo?"* |
| Writes | **Delegates to** `changelog-portable-stub/scripts/install-changelog-stub --target $PWD --gh-owner X --gh-repo Y`. Adds nothing of its own except prepending the Makefile snippet if the user opts in |
| Idempotent | Inherits the stub's `PROTECTED_PATHS` model (cliff.toml, docs/releasing-changelog.md preserved) |
| Deterministic? | **Yes — pure script.** Wraps an existing deterministic installer. |

#### `wip setup agents`

| | |
|---|---|
| Detects | `CLAUDE.md?` `AGENTS.md?` — and whether they're byte-identical (the prtend drift signature) |
| Asks | *Only* if AGENTS.md exists with substantive content: *"Adopt CLAUDE.md→AGENTS.md pointer? (writes 3-line CLAUDE.md, preserves your AGENTS.md)"*. If neither file exists: *"Generate AGENTS.md skeleton?"* and then **delegate to an agent** to draft the skeleton (this is the one subcommand where script can't do the judgment well — AGENTS.md needs prose tuned to the project) |
| Writes | `CLAUDE.md` = 3-line xcind pointer (always overwritable — it's a 3-line file); `AGENTS.md` only if missing (protected once written) |
| Idempotent | Yes; safe to rerun |
| Deterministic? | **Mostly script.** CLAUDE.md pointer is deterministic. AGENTS.md draft *needs an agent* — call out clearly in the spec. |

#### `wip setup dependabot`

| | |
|---|---|
| Detects | Ecosystem files: `pyproject.toml` → pip; `package.json` → npm; `composer.json` → composer; `Cargo.toml` → cargo; `.github/workflows/*` → github-actions; `Dockerfile` → docker |
| Asks | *"Weekly or daily updates?"* (default weekly); *"Group all minor+patch?"* (default yes) |
| Writes | `.github/dependabot.yml` with one `updates:` block per detected ecosystem |
| Idempotent | Protected; rerun proposes a diff |
| Deterministic? | **Yes — pure script.** Pure file generation from a detection table. |

#### `wip setup doctor`

Runs every subcommand's `detect()` and reports per-concern status:
`baselined / drifted / missing`, with specific drift messages
("`CLAUDE.md` matches `AGENTS.md` byte-for-byte — adopt pointer", "`.envrc` uses
`use flake` without pinned nix-direnv shim"). Non-writing. This is the
discoverability surface — answers "is this repo baselined?" deterministically,
which is the meta-goal in the BRIEF.

#### `wip setup all`

Runs subcommands in the order above. Each subcommand short-circuits if its
`detect()` reports already-baselined. Top-level prompt: *"Found a python-uv
project with Dockerfile. Bootstrap flake, direnv, hygiene (incl. hadolint),
release, agents, dependabot? [y/N/select]"*

### 3. Dual surface: CLI vs slash command

Every subcommand is both:

- `wip setup <x>` — invocable from any shell; the CLI is the source of truth for
  detection and file generation.
- `/wip:setup <x>` — a Claude Code slash command that essentially runs
  `wip setup <x>` and then narrates the result. The slash command also handles
  the **questions interactively** via `AskUserQuestion` instead of TTY prompts,
  which is the affordance the CLI doesn't have in a Claude Code session.

**Recommended split (script vs agent):**

| Subcommand | Script | Agent | Why |
|---|---|---|---|
| `direnv` | ✅ | — | Pure file generation |
| `flake` | ✅ | — | Templates only; no judgment for standard stacks |
| `hygiene` | ✅ | — | Hook table → config |
| `release` | ✅ | — | Wraps deterministic installer |
| `agents` | ✅ (pointer) | ✅ (AGENTS.md draft) | Pointer is deterministic; first-draft AGENTS.md needs prose tuned to the repo's commands |
| `dependabot` | ✅ | — | Pure file generation |
| `doctor` | ✅ | — | Reporting only |
| `all` | ✅ | calls `agents` agent if invoked | Orchestrator |

The agent is *only* invoked by `setup agents` and only when an AGENTS.md needs to
be generated from scratch. Everything else stays deterministic — which means
`wip setup` can run unattended (in CI, in a workflow step, in scripts) without
needing an LLM in the loop.

### 4. How the existing portable stubs fold in

**`changelog-portable-stub`** — **wrap, don't replace.** It already has:

- The protected-paths idempotency model we want everywhere
  (changelog-portable-stub/scripts/install-changelog-stub:35)
- Token replacement for `{{GH_OWNER}}/{{GH_REPO}}` (install-changelog-stub:154–167)
- A `Makefile.snippet` separation that solves the "don't touch user's Makefile"
  problem (install-changelog-stub:24–27)
- A `releasing-changelog.md` doc that itself is the spec for the release flow

`wip setup release` should literally `exec` the script (or call its installer
function) and pass `--target $PWD --gh-owner X --gh-repo Y`. **Vendor it as a git
submodule or copy under `wip/vendor/` and keep the upstream installer as the
source of truth.** Do not reimplement.

Beyond that, generalize the protected-paths pattern: `wip setup` should expose
the same primitives (`is_protected`, `copy_file`, `write/skip-protected/replace`
log lines) as a tiny shared library every subcommand uses.

**`direnv-session-loader`** — **orthogonal; do not wrap.** This is a *Claude
Code plugin* (`direnv-session-loader/hooks/hooks.json:3` registers
`SessionStart`) that solves a *Claude-Code-shell* problem
(direnv-session-loader/README.md:7-12), not a project-bootstrap problem. A
project's `.envrc` works without it; it's the agent runtime that needs it.

Recommendation: `wip setup direnv` does **not** install the plugin. `wip doctor`
*does* check whether the user has it installed (via `claude plugin list` or
equivalent) and recommend it if `.envrc` exists. This keeps the project
bootstrap (per-repo, deterministic) cleanly separated from the agent runtime
config (per-user, optional).

### 5. The `wip.yaml` manifest contract

Per the BRIEF, each feature should record its presence for deterministic
detection (BRIEF.md:23-26). Recommend `wip.yaml` keys:

```yaml
features:
  flake: { type: python-uv, version: 1 }
  direnv: { pinned: true, version: 1 }
  hygiene: { hooks: [shfmt, shellcheck, gitleaks], version: 1 }
  release: { tool: git-cliff, version: 1 }
  agents: { canonical: AGENTS.md, version: 1 }
  dependabot: { ecosystems: [pip, github-actions, docker], version: 1 }
```

Each subcommand updates its own block on write. `wip doctor` reads this *and*
re-runs detection; mismatch = stale manifest = warning.

## Evidence

- xcind canonical pointer pattern: xcind/CLAUDE.md:1-5, xcind/AGENTS.md:1-50
- xcind pinned nix-direnv shim: xcind/.envrc:1-4
- xcind shellHook idempotent pre-commit install: xcind/flake.nix (shellHook block, "Let .pre-commit-config.yaml be the single source of truth")
- xcind Makefile verb surface: xcind/Makefile (`format/lint/test/check/changelog-preview` targets)
- xcind pre-commit ordering: xcind/.pre-commit-config.yaml (secrets → formatters → manifest → linters; `exclude: ^\.envrc$`)
- xcind auto-trigger skills referenced from AGENTS.md: xcind/AGENTS.md:17, xcind/AGENTS.md:43
- prtend drift (CLAUDE.md ≡ AGENTS.md): prtend/CLAUDE.md vs prtend/AGENTS.md (byte-identical except H1)
- prtend bare `use flake` (unpinned): prtend/.envrc:1
- prtend hand-rolled cliff.toml (no `[remote.github]`, divergent parsers): prtend/cliff.toml:30+
- changelog-portable-stub protected-paths model: changelog-portable-stub/scripts/install-changelog-stub:35-43, :84-97
- changelog-portable-stub token replacement: install-changelog-stub:154-167
- changelog-portable-stub Makefile.snippet pattern: install-changelog-stub:24-27, changelog-portable-stub/stub/Makefile.snippet
- direnv-session-loader is a Claude Code plugin (not project bootstrap): direnv-session-loader/hooks/hooks.json:1-13, direnv-session-loader/README.md:7-12
- bizapps-symfony-bot drift: malformed `flake.nix` mixing `export`s into `buildInputs` (bizapps-symfony-bot/flake.nix:21-27); shellHook does package install inline (bizapps-symfony-bot/flake.nix:36-53)
- No `.github/dependabot.yml` in the corpus; no project has a hadolint hook despite xcind having a Dockerfile (xcind/Dockerfile exists; xcind/.pre-commit-config.yaml has no hadolint entry)

## Open questions / escalations for the human

1. **`flake.nix` strategy for python-uv**: bizapps uses an *impure* shellHook
   that activates a venv and installs packages on every shell entry
   (bizapps-symfony-bot/flake.nix:33-53). The xcind shape (pure devShell, no
   side effects) doesn't fit python-uv. **Decision needed**: should the python
   template adopt the bizapps shellHook pattern as-is (it works, but is
   side-effectful), or should we move venv creation to an explicit
   `make install`?
2. **Where does `wip setup` live, binary-wise?** If it's a Rust binary, it
   can't itself need a `flake.nix` to install (chicken/egg). Recommend either:
   shell script with `nix run github:.../wip` as the public install path, or
   shipping prebuilt binaries via GitHub releases.
3. **Renovate vs Dependabot.** The BRIEF says dependabot; some Beau projects
   may prefer Renovate. Should `wip setup dependabot` be `wip setup deps` with
   a `--tool` flag?
4. **Auto-trigger skill propagation.** xcind ships `add-installed-file` and
   `pre-commit-check` as project-local Claude skills. Is propagating this
   pattern (`.claude/skills/`) part of `wip setup hygiene`, or its own subcommand
   `wip setup skills`?
5. **Plugin recommendations from `wip doctor`.** Should `wip doctor` shell out
   to `claude plugin list`, or stay agent-runtime-agnostic and just print
   "install direnv-session-loader" as a suggestion?

## Dependencies on other workstreams

- **w1 (vocabulary):** Need the canonical word for **the feature manifest** —
  this file recommends `wip.yaml` per the BRIEF; w1 should ratify or rename. The
  per-feature `version: N` schema field also wants w1's blessing.
- **w1 (subcommand grammar):** `wip setup <x>` vs `wip <x> setup` vs `wip init
  <x>`. This file uses `wip setup <x>` per the prompt; w1 owns final naming.
- **w3 (LDS):** `wip setup` writes `AGENTS.md` (and a pointer-style CLAUDE.md).
  w3 owns the AGENTS.md / LDS-glossary relationship — `wip setup agents` must
  not collide with LDS install location decisions. If LDS is installed,
  `wip setup agents` should link AGENTS.md to LDS entry points rather than
  drafting from scratch.
- **w4 (Solo/Duo orchestration):** `wip setup agents`'s "draft AGENTS.md skeleton
  from scratch" is the one agent-needed step. w4 should specify which Solo tier
  / Duo agent_tool_id this dispatches to (BRIEF.md:26 pins `agent_tool_id=3` for
  Opus).
- **w5 (CLI / slash-command grammar):** This file proposes the per-subcommand
  detect/ask/write/idempotent contract. w5 should ratify and ensure other
  command families (e.g. `wip plan`, `wip lds`) inherit the same contract so
  users see a consistent shape.
