---
description: Show where the user is in the current wip initiative.
argument-hint: "[--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:status — where am I?

Renders the answer to "where am I" using `wip-plumbing status`. Read-only,
backed entirely by deterministic plumbing.

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

2. **Run `"$WIP" status`.** Forward `--initiative <slug>` from
   `$ARGUMENTS` if present. Capture the JSON envelope.

3. **Render to prose.** A short paragraph or bulleted summary, in this
   order:
   - Initiative slug + status (`in-flight`, `shipped`, etc.).
   - Current round (number + title).
   - Active step id + title, and whether it's shipped.
   - `dirty_wip_files` count; if non-zero, list each file on its own
     line under a "Pending edits in `.wip/`:" sub-bullet.
   - `solo_available: true|false` (one-line footer).
   - If `ok: false`, surface `error.message` directly.

4. **No writes.** Read-only.

## Example envelope (shape only)

```json
{ "ok": true, "initiative": "distillation", "status": "in-flight",
  "round": { "n": 3, "title": "Porcelain, plugin & features" },
  "active_step": { "id": "step-12", "title": "Roles set", "shipped": false },
  "dirty_wip_files": [],
  "solo_available": true }
```
