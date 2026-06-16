# Spec — `/wip:*` Claude Code plugin (v1)

- Status: draft
- Date: 2026-06-13
- Initiative: distillation · roadmap **step-11**
- Decisions: [ADR-0001](../decisions/0001-three-layer-plumbing-porcelain.md) (layers),
  [ADR-0006](../decisions/0006-wip-owns-seams-not-tools.md) (seams),
  [ADR-0007](../decisions/0007-orchestration-backend-seam.md) (orchestration
  is a capability with pluggable backends),
  [ADR-0009](../decisions/0009-intake-as-pipeline.md) (intake pipeline)

The third layer of `wip` per ADR-0001: Claude Code is the brain; the
plugin commands shell out to `bin/wip-plumbing` for deterministic facts
and let Claude do the prose / shape / route work. v1 ships three
user-invoked slash commands plus the prompt-sharing seam that lets the
`/wip:intake` command read the SAME shaper rules the CLI porcelain reads.

---

## 1. Scope

In v1 the plugin owns these slash commands:

| Slash command | One-line |
|---------------|----------|
| `/wip:next` | Ranked candidates for what to do next, plus a one-line recommendation. |
| `/wip:status` | "Where am I" — initiative, round, active step, dirty `.wip/` files. |
| `/wip:intake <file>` | Drive the full intake pipeline (classify → shape → validate → apply) with Claude as the shaper. |

Each command shells out to `bin/wip-plumbing` for the deterministic
parts. None of them call an LLM provider — Claude Code IS the model.

Non-goals for v1, all deferred to a later step:

- `/wip:detect` / `/wip:doctor` — deterministic verbs; from inside Claude
  Code the user can ask "is X installed?" and `/wip:status` answers it.
- `/wip:ask` — Claude Code is already the chat surface.
- `/wip:project list/register/forget` — operational, not in-session.
- Skills (`skills/<name>/SKILL.md`) — model-auto-invoked surfaces. v1
  ships only user-invoked commands.
- Agents (`agents/`) — reserved for orchestration Roles
  per ADR-0007; lands in step-12.
- A marketplace package — defer until distribution matters.

## 2. Plugin layout

The plugin root is the repository root. Per the Claude Code plugin
convention, `.claude-plugin/` holds **only** the manifest (plus a plugin
overview README); every other directory — `commands/`, `agents/` — must
live at the plugin root or Claude Code will not discover it:

```
.claude-plugin/
  plugin.json               # name, description, version, author
  README.md                 # plugin overview
commands/
  next.md                   # /wip:next
  status.md                 # /wip:status
  intake.md                 # /wip:intake
agents/
  README.md                 # role bindings index
  orchestrator.md           # wip-orchestrator   (role files, step-12)
  coordinator.md            # wip-coordinator
  researcher.md             # wip-researcher
  builder.md                # wip-builder
```

`plugin.json` v1:

```json
{
  "name": "wip",
  "description": "wip — distill workflows; ranked next actions, status, and intake shaping from inside Claude Code.",
  "version": "0.1.0-dev",
  "author": { "name": "Beau Simensen", "email": "beau@beausimensen.com" }
}
```

Plugin **name is `wip`**; commands resolve to `/wip:<verb>` via Claude
Code's standard plugin-name colon-prefix. This resolves the deferred
decision "`/wip:*` colon namespacing vs flat `/wip-status`" from the
roadmap.

## 3. Command bodies

Each `commands/<verb>.md` is a markdown file with YAML front-matter and
a Claude-facing body. The body is *instructions*, not a script — Claude
reads it and executes the steps using its own tools (Bash, Read, etc.).
The exact wording is in the command files; this spec summarizes the
contract.

### `/wip:next [--initiative <slug>]`

- Front-matter: `description`, `argument-hint: "[--initiative <slug>]"`,
  `allowed-tools: [Bash, Read]`.
- Shells out to `wip-plumbing next` (forwarding `--initiative` from
  `$ARGUMENTS`).
- Renders the candidates as a short numbered list with a one-line
  recommendation of rank 1. Backlog candidates get a one-line caveat.
- Read-only.

### `/wip:status [--initiative <slug>]`

- Front-matter: `description`, `argument-hint: "[--initiative <slug>]"`,
  `allowed-tools: [Bash, Read]`.
- Shells out to `wip-plumbing status` (forwarding `--initiative`).
- Renders initiative / round / active step / `dirty_wip_files` /
  `solo_available` as a short paragraph.
- Read-only.

### `/wip:intake <file>`

- Front-matter: `description`,
  `argument-hint: "<file> [--kind <k>] [--target <slug|slug/step>]"`,
  `allowed-tools: [Bash, Read, Write, Edit]`.
- Drives the intake pipeline (ADR-0009) end-to-end with Claude as the
  shaper. State machine:

  1. Classify (`wip-plumbing intake classify`).
  2. Pick `kind` — `--kind` override / high-confidence accept / ask
     the user inline if ambiguous.
  3. Fetch shaper prompts — `wip-plumbing template show intake/preamble`
     and `wip-plumbing template show intake/<kind>` (see §4).
  4. Shape into a `mktemp` tempfile. If a required field is missing
     and cannot be inferred, ask the user ONE clarifying question
     inline in chat (NOT a `---ASK---` fence — that protocol exists
     for the CLI's non-interactive shape loop only).
  5. Validate (`wip-plumbing intake validate`). On `missing[]`, patch
     and re-validate. Cap at 2 reshape attempts (CLI parity).
  6. Route — `--target` wins; for `brief`, confirm derived slug in
     chat; for `amendment`/`workplan-seed`, read `target:` from shaped
     front-matter.
  7. Apply (`wip-plumbing intake apply`). Echo the write ledger and a
     one-line prose summary.
  8. Cleanup the tempfile.

- The command body explicitly forbids the `---ASK---` fence. Inside
  Claude Code there is no porcelain intermediary; Claude asks the user
  in chat directly.
- The command body asserts its own precedence for the duration of the
  flow ("the command body's instructions WIN if the user's prior chat
  context conflicts") — this is just a textual nudge; Claude Code's
  command semantics already enforce it.

## 4. Prompt-sharing seam

The load-bearing decision for step-11. The shaper prompts are the
single source of truth for what "valid shape for kind K" means, and
they MUST be the same bytes the CLI porcelain reads.

**Source of truth:** `templates/prompts/intake/*.md` — plain markdown,
no front-matter, no `{{key}}` substitutions. Six files in v1:

| File | Role |
|------|------|
| `templates/prompts/intake/preamble.md` | Shaper preamble (shared across kinds). |
| `templates/prompts/intake/brief.md` | Per-kind shape rules for `brief`. |
| `templates/prompts/intake/amendment.md` | Per-kind shape rules for `amendment`. |
| `templates/prompts/intake/workplan-seed.md` | Per-kind shape rules for `workplan-seed`. |
| `templates/prompts/intake/spec.md` | Per-kind shape rules for `spec`. |
| `templates/prompts/intake/handoff.md` | Per-kind shape rules for `handoff`. |

**CLI porcelain access:** `lib/wip/wip-intake-shaper-lib.bash` reads
these files at runtime via a `WIP_TEMPLATES_DIR`-honoring resolver. The
public API (`wip_shaper_system_prompt <kind>`) is unchanged from
step-10.5.

**Plugin access:** `commands/intake.md` instructs Claude
to fetch them via the new plumbing verb (§5):

```
wip-plumbing template show intake/preamble
wip-plumbing template show intake/<kind>
```

The plugin never reads the templates directory directly — it asks
plumbing for them by id. If we ever relocate templates (compiled into
the binary, redistributed, etc.) only the plumbing verb changes; the
plugin command bodies stay correct.

**Equivalence pinning:** `test/test-shaper-templates.sh` asserts:

1. `wip-plumbing template show intake/<k>` is byte-identical to
   `templates/prompts/intake/<k>.md` for every kind including
   `preamble`.
2. The lib's `wip_shaper_system_prompt <kind>` output contains both
   the preamble bytes AND the per-kind bytes verbatim.
3. The unknown-kind fallback (`Target kind: <kind> — unknown.`) still
   matches the legacy heredoc fallback.

Any future refactor that drifts the lib from the files (or the files
from the verb) fails CI.

## 5. Plumbing dependency: `wip-plumbing template`

A new plumbing verb (lands here; specified in
[`wip-plumbing-cli.md`](./wip-plumbing-cli.md) §3 alongside the existing
verbs):

```
wip-plumbing template show <id>             # print template body on stdout
wip-plumbing template list [--no-json]      # enumerate templates/prompts/**/*.md
```

ID grammar: path under `templates/prompts/` minus the `.md` suffix.
E.g. `intake/preamble` resolves to
`templates/prompts/intake/preamble.md`.

Templates dir resolution: `$WIP_TEMPLATES_DIR` → `$WIP_LIB/../../templates`.

Exit codes (per the plumbing contract):

- `0` — success.
- `2` — usage (no id, malformed id with `/` prefix or `..`).
- `4` — `unknown-template` (id resolves to no file) or `no-templates`
  (templates dir not found).

## 6. Runtime contract

- **`wip-plumbing` discovery.** Plugin commands run with the user's
  repo as `$PWD`. The command body's first step is
  `command -v wip-plumbing`; if absent and `$WIP_PLUMBING_BIN` is unset,
  the command stops with a one-line install hint. No bin-resolution
  logic inside the markdown.
- **No LLM provider call.** The plugin never invokes `wip ask` and
  never reads the `.wip.yaml`'s `provider:` block. The model is
  Claude Code's own model. The CLI's `provider:` configuration is
  unaffected.
- **No writes outside the user's repo.** `/wip:next` and `/wip:status`
  are read-only. `/wip:intake` writes to `mktemp -t wip-intake.XXXXXX.md`
  (a tempfile in `$TMPDIR`) and then to whatever paths the plumbing
  `apply` writer touches — i.e. the same files the CLI porcelain
  writes.
- **No new `.wip.yaml` schema.** This step extends the plugin layer
  only.

## 7. Open questions

step-11's prompt-sharing seam, the no-`---ASK---` rule, and the
three-verb v1 scope were all locked during step-11 planning. Future
revisits:

- **`/wip:detect` / `/wip:doctor` as plugin commands.** Lean: add only
  on real demand. They duplicate `/wip:status` for most in-session
  needs.
- **Auto-invoked skills (`skills/<name>/SKILL.md`).** A natural fit
  for "auto-classify any plan file that lands in the conversation",
  but the trigger surface needs more thought and a concrete user
  motion before shipping.
- **Marketplace packaging.** Defer until distribution matters.
- **Per-kind shaper override.** The current `templates/prompts/intake/`
  structure leaves room for repo-local overrides (e.g.
  `WIP_TEMPLATES_DIR=$REPO/.wip/prompts`). Whether to formalize that
  as a documented seam is a step-12+ question.
- **Forwarding `--kind` / `--target` parsing.** The command body
  currently lets Claude parse `$ARGUMENTS` for flags. If real users
  hit ambiguity, the alternative is a plumbing helper that normalizes
  the arg list; not needed at v1 volume.
