---
description: List the ranked next actions for this wip initiative.
argument-hint: "[--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:next — what should I do next?

Surfaces the ranked candidates from `wip-plumbing next` and recommends the
top pick. Backed entirely by the deterministic plumbing layer (no
judgment lives here that the CLI doesn't also have).

## Procedure

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Run `wip-plumbing next`.** If the user passed `--initiative <slug>`
   in `$ARGUMENTS`, forward it verbatim. Capture the JSON envelope on
   stdout.

3. **Render to prose.** From the JSON:
   - Echo the resolved initiative.
   - Emit the candidates as a short numbered list — one line each:
     `rank. **<id> — <title>**  _<source>: <reason>_`.
   - Recommend rank 1 in a one-line conclusion.
   - **`source: "scaffold"`** (title `author the roadmap`, `id: null`): the
     initiative has a brief but no roadmap steps yet. Don't render it as a
     `step-NN` — render it as the action *author the roadmap at `<path>`*
     (from the candidate's `path`), and recommend authoring the roadmap from
     the `BRIEF.md` before anything else. `/wip:start` has nothing to start
     until a real step exists.
   - If a backlog candidate is in the list, add a one-line caveat that
     backlog items need an explicit go-ahead (they're not the sequential
     next step).
   - If `deferred` is non-empty, list its `title`s under a separate
     **Deferred (not actionable)** sub-heading, clearly apart from the
     candidates. These are consciously postponed items — never recommend one
     as the next step; they are context only.
   - If the envelope is `ok: false`, surface `error.message` directly.

4. **No writes.** This command is read-only; it must not mutate any file
   or invoke any other `wip-plumbing` verb.

## Example envelope (shape only)

```json
{ "ok": true, "initiative": "distillation",
  "candidates": [
    { "rank": 1, "source": "roadmap", "id": "step-12",
      "title": "Roles set", "reason": "first unshipped step in active round" }
  ],
  "deferred": [ { "id": "duo-backend", "title": "Duo backend" } ] }
```
