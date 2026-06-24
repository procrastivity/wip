# `/wip:*` — Claude Code plugin for the `wip` workflow

The third frontend in `wip`'s three-layer architecture
([ADR-0001](../engineering/decisions/0001-three-layer-plumbing-porcelain.md)):
deterministic plumbing → CLI porcelain → this plugin. Claude Code is the
brain; the plugin commands shell out to `wip-plumbing` for facts and
let Claude do the prose / shape / route work.

## Commands

| Slash command | What it does |
|---------------|--------------|
| `/wip:next` | Ranked candidates for what to do next, plus a recommendation. Backed by `wip-plumbing next`. |
| `/wip:status` | "Where am I" — initiative / round / active step / dirty `.wip/` files. Backed by `wip-plumbing status`. |
| `/wip:intake <file>` | Drives the full intake pipeline (classify → shape → validate → apply, or explode for a bundle). Claude is the shaper; clarifications happen inline in chat. |
| `/wip:start <step-id>` | Activate a roadmap step (`workplan init … --activate`) and brief it, then hand off solo or to orchestration. |
| `/wip:orchestrate` | Hand the active step to the Roles backend — become the Orchestrator and spawn its Coordinator. |
| `/wip:bundle <file>…` | Assemble N handoff files into one bundle lead manifest, then explode it through intake. |

The command bodies in `commands/` are **generated** from the canonical plugin
commands (`commands/*.md` in the wip repo) by `contrib/sync-agents-commands` —
the only transform is the binary resolver (`$CLAUDE_PLUGIN_ROOT` → PATH) and
`"$WIP"` → `wip-plumbing`. See [ADR-0015](../engineering/decisions/0015-setup-agents-commands-generated-from-plugin.md).

## Prerequisites

- `wip-plumbing` on `$PATH` (or `$WIP_PLUMBING_BIN` set). The plugin only
  reaches for plumbing; the CLI porcelain (`bin/wip`) is not required.
- A `.wip.yaml` in the consumer's repo root (`/wip:next` and `/wip:status`
  exit with a useful error otherwise).

## Prompt-sharing seam (step-11)

The `/wip:intake` shape rules come from `templates/prompts/intake/*.md`,
the same files the CLI porcelain's shaper reads at runtime. The plugin
fetches them via `wip-plumbing template show intake/<id>` so command
bodies say *what they want*, not *where the bytes live*. Equivalence is
pinned by `test/test-shaper-templates.sh`.

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
