# `/wip:*` — Claude Code plugin for the `wip` workflow

The third frontend in `wip`'s three-layer architecture
([ADR-0001](../engineering/decisions/0001-three-layer-plumbing-porcelain.md)):
deterministic plumbing → CLI porcelain → this plugin. Claude Code is the
brain; the plugin commands shell out to `bin/wip-plumbing` for facts and
let Claude do the prose / shape / route work.

## Commands

| Slash command | What it does |
|---------------|--------------|
| `/wip:next` | Ranked candidates for what to do next, plus a recommendation. Backed by `wip-plumbing next`. |
| `/wip:status` | "Where am I" — initiative / round / active step / dirty `.wip/` files. Backed by `wip-plumbing status`. |
| `/wip:start <step-id>` | Activate a roadmap step (`active_step`), scaffold/locate its workplan, and brief it. Offers to start; never auto-runs. Backed by `wip-plumbing workplan init … --activate`. |
| `/wip:orchestrate` | Boot orchestration for the active step: prep + gate via `wip-plumbing orchestrate prep`, then become the Orchestrator and spawn a Coordinator via the backend. The ergonomic wrapper for `/wip:start`'s on-`go` "Orchestrate" branch ([ADR-0012](../engineering/decisions/0012-orchestrate-entrypoint-is-a-plugin-command.md)). |
| `/wip:intake <file>` | Drives the full intake pipeline (classify → shape → validate → apply). Claude is the shaper; clarifications happen inline in chat. |
| `/wip:bundle <files…>` | Assembles two or more handoff files into one `bundle` lead manifest, then runs the existing intake explode inline. Claude is the shaper; clarifications happen inline in chat. |

## Prerequisites

- `wip-plumbing` on `$PATH` (or `$WIP_PLUMBING_BIN` set). The plugin only
  reaches for plumbing; the CLI porcelain (`bin/wip`) is not required.
- A `.wip.yaml` in the consumer's repo root (`/wip:next` and `/wip:status`
  exit with a useful error otherwise).

## Prompt-sharing seam (step-11)

The `/wip:intake` shape rules come from `templates/prompts/intake/*.md`,
and `/wip:bundle`'s assembly rules from `templates/prompts/bundle/assemble.md`
— the same files the CLI porcelain (`wip intake` / `wip bundle`) reads at
runtime. The plugin fetches them via `wip-plumbing template show <id>` so
command bodies say *what they want*, not *where the bytes live*. Equivalence
is pinned by `test/test-shaper-templates.sh` and `test/test-template-verb.sh`.

## What's deliberately not here

- `/wip:detect`, `/wip:doctor` — deterministic verbs; meant for CI and
  scripts. From inside Claude Code the user can ask "is X installed?"
  in chat and `/wip:status` covers the answer.
- `/wip:ask` — Claude Code IS the chat surface; there's no analog.
- `/wip:project list/register/forget` — registry verbs are operational,
  not in-session.

## See also

- Plugin contract: [`engineering/specs/wip-plugin.md`](../engineering/specs/wip-plugin.md).
- Plumbing contract: [`engineering/specs/wip-plumbing-cli.md`](../engineering/specs/wip-plumbing-cli.md).
- CLI porcelain contract: [`engineering/specs/wip-porcelain.md`](../engineering/specs/wip-porcelain.md).
- Intake pipeline rationale: [ADR-0009](../engineering/decisions/0009-intake-as-pipeline.md).
