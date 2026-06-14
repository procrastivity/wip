---
description: Show where the user is in the current wip initiative.
argument-hint: "[--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:status — where am I?

Renders the answer to "where am I" using `wip-plumbing status`. Read-only,
backed entirely by deterministic plumbing.

## Procedure

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Run `wip-plumbing status`.** Forward `--initiative <slug>` from
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
