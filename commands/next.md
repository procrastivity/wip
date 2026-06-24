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

1. **Resolve `wip-plumbing`.** The plugin bundles the CLI; prefer the bundled
   copy, then an explicit override, then PATH. Run once:
   ```bash
   if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -x "$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing" ]]; then
     WIP="$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing"
   elif [[ -n "${WIP_PLUMBING_BIN:-}" && -x "${WIP_PLUMBING_BIN}" ]]; then
     WIP="$WIP_PLUMBING_BIN"
   elif command -v wip-plumbing >/dev/null 2>&1; then
     WIP="wip-plumbing"
   else
     echo "wip-plumbing not found — enable the wip plugin or install it (see the project README)"
   fi
   ```
   If the resolver printed the not-found message (`$WIP` unset), stop. Use
   `"$WIP"` in place of `wip-plumbing` for every command below; re-run this
   resolver if a later step starts in a fresh shell.

2. **Run `"$WIP" next`.** If the user passed `--initiative <slug>`
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
   - If the envelope is `ok: false`, surface `error.message` directly.

4. **No writes.** This command is read-only; it must not mutate any file
   or invoke any other `wip-plumbing` verb.

## Example envelope (shape only)

```json
{ "ok": true, "initiative": "distillation",
  "candidates": [
    { "rank": 1, "source": "roadmap", "id": "step-12",
      "title": "Roles set", "reason": "first unshipped step in active round" }
  ] }
```
