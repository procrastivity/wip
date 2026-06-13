# 0001 — Three-layer architecture: plumbing / porcelain / plugin

- Status: accepted
- Date: 2026-06-12
- Source: `.wip/initiatives/distillation/findings/w5-cli-grammar.md`, SYNTHESIS §1

## Context

`wip` needs to be usable as a deterministic CLI (CI, scripts, no LLM), from a plain
shell with LLM judgment, and from inside Claude Code. The clast project already models
this (deterministic `clast` core + judgment porcelain `clast-wake`/`clast-brief`, planned
rename to `clast-plumbing`).

## Decision

Ship three layers in one repo:

1. **`wip-plumbing`** — deterministic bash core. Never calls an LLM. JSON on stdout,
   prose on stderr, exit codes 0–4 (the prtend/clast contract). Owns detection, status,
   ranking, file writes, staging, atomic moves.
2. **`wip`** — standalone porcelain configured to an OpenAI-compatible endpoint; shells
   out to `wip-plumbing` for facts and writes.
3. **`/wip:*`** — Claude Code plugin porcelain; Claude Code is the brain, shells out to
   `wip-plumbing`.

**Layer rule:** pure function of files+git → plumbing; needs prose/choice/composition →
porcelain.

## Consequences

- One deterministic core, two judgment frontends; no logic duplicated across them.
- bash, not a compiled binary, for v1 (avoids a flake bootstrap chicken-and-egg; matches
  clast/prtend/xcind). A compiled core is a later optimization.
- Distribution mirrors clast: `install.sh` + Nix flake + npm + Claude Code plugin.
