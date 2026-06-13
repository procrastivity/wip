# wip

> 🚧 Pre-1.0 — design in flight. The canonical model lives in
> [`engineering/`](./engineering/) (decisions) and
> [`templates/glossary/`](./templates/glossary/) (vocabulary).

`wip` gives in-flight planning a home and makes "what's the state / what's next"
**deterministic**. It is a small composable spine that orchestrates the tools you
already use (LDS, Diátaxis, changelog, direnv, Solo, prtend) rather than replacing them.

## Shape

Three layers (clast-style — one repo, names collapse over time):

- **`wip-plumbing`** — deterministic bash core. No LLM. JSON / exit codes. Detection, status, ranking, file writes.
- **`wip`** — standalone porcelain; talks to an OpenAI-compatible endpoint for judgment, shells out to `wip-plumbing`.
- **`/wip:*`** — Claude Code plugin porcelain; Claude Code is the brain.

`wip` owns the **seams**, not the tools: LDS, prtend, Duo, etc. each ship from their own
repos. `wip` detects them (`.wip.yaml` + a sentinel file) and invokes them.

## Repository map

| Path | What |
|------|------|
| `bin/`, `lib/wip/` | the CLI (ships) — *not built yet* |
| `templates/` | content `wip` scaffolds into consumers (glossary partials, file templates) |
| `roles/` | Solo orchestration Roles, shipped by the plugin (opt-in) |
| `.claude-plugin/` | the `/wip:*` plugin (ships) — *not built yet* |
| `engineering/` | **LDS** — the decisions & specs behind `wip` itself |
| `docs/` | **Diátaxis** — user docs for people consuming `wip` |
| `.wip.yaml`, `.wip/` | this repo dogfooding itself (manifest + in-flight work) |

## Dogfooding

This repo uses `wip` to build `wip`. The active initiative is
[`distillation`](./.wip/initiatives/distillation/) — see its
[roadmap](./.wip/initiatives/distillation/roadmap.md).
