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

## Develop

Three commands from a fresh clone:

```sh
direnv allow      # loads the nix devShell (pinned in flake.nix)
make hooks        # installs the pre-commit gate
make check        # lint + tests (the same gate pre-commit runs)
```

Without nix: install `bash`, `jq`, `yq-go`, `shellcheck`, and `shfmt`
yourself, then `make check` works the same.

## Porcelain

`bin/wip` is the standalone porcelain over `bin/wip-plumbing`. It exposes every
deterministic verb of the plumbing transparently and adds two LLM-aware verbs —
`ask` and `provider show` — that talk to any OpenAI-compatible endpoint. The
endpoint is configured by env-var pointers in `.wip.yaml`'s `provider:` block.

```sh
# point at your provider (env names come from .wip.yaml's provider: block)
export WIP_LLM_BASE_URL=https://api.openai.com
export WIP_LLM_API_KEY=sk-...
export WIP_LLM_MODEL=gpt-4o-mini

bin/wip provider show          # diagnostic; never prints the api_key
echo "hello"  | bin/wip ask    # one-shot chat completion
bin/wip ask "what is wip?"
```

Spec: [`engineering/specs/wip-porcelain.md`](./engineering/specs/wip-porcelain.md).

## Dogfooding

This repo uses `wip` to build `wip`. The active initiative is
[`distillation`](./.wip/initiatives/distillation/) — see its
[roadmap](./.wip/initiatives/distillation/roadmap.md).
