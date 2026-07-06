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
   elif WIP="$(ls -d "$HOME"/.claude/plugins/cache/*/wip/*/bin/wip-plumbing 2>/dev/null | sort | tail -1)" && [[ -n "$WIP" && -x "$WIP" ]]; then
     : # bundled copy from the installed plugin cache (CLAUDE_PLUGIN_ROOT not exported to this shell)
   elif command -v wip-plumbing >/dev/null 2>&1; then
     WIP="wip-plumbing"
   else
     echo "wip-plumbing not found — enable the wip plugin (settings.json → enabledPlugins) or install it (see the project README)"
   fi
   ```
   If the resolver printed the not-found message (`$WIP` unset), stop. Use
   `"$WIP"` in place of `wip-plumbing` for every command below; re-run this
   resolver if a later step starts in a fresh shell.

2. **Run `"$WIP" status --probe-solo`.** Forward `--initiative <slug>` from
   `$ARGUMENTS` if present. Capture the JSON envelope. `--probe-solo` adds a
   live `solo_reachable` check (used in step 4); it shells out to the `solo`
   CLI, so drop the flag only if you explicitly want the fast, probe-free read.

3. **Render to prose.** A short paragraph or bulleted summary, in this
   order:
   - Initiative slug + status (`in-flight`, `shipped`, etc.).
   - Current round (number + title).
   - Active step id + title, and whether it's shipped.
   - `dirty_wip_files` count; if non-zero, list each file on its own
     line under a "Pending edits in `.wip/`:" sub-bullet.
   - Solo footer: `solo_available` (Solo is *declared* in config) and, when
     probed, `solo_reachable` (`true` = answering, `false` = not answering,
     `null` = not probed).
   - If `deferred` is non-empty, add a one-line **Deferred (not actionable):**
     note listing the `title`s — consciously postponed items, shown as context
     only; never a next step.
   - If `ok: false`, surface `error.message` directly.

4. **Solo unreachable → warn + offer the Task-backend fallback.** If
   `signals` contains `"solo-unreachable"` (Solo is the active orchestration
   backend but the live probe did not answer), tell the user plainly: *Solo
   is enabled but not reachable — orchestration would stall at the first
   spawn.* Then **offer** to fall back to the Task backend (native subagents,
   no Solo):
   - On an explicit **yes**, run `"$WIP" orchestrate backend task`. It
     regenerates `roles/backends/active.md` and flips
     `features.orchestration.backend` to `task`. Echo the resulting ledger and
     tell the user to re-run their orchestration (e.g. `/wip:orchestrate`).
   - **Never switch without confirmation** — this is the *only* write this
     command can make, and only on a yes. Switching back later is
     `"$WIP" orchestrate backend solo`.

5. **No writes** other than the confirmed fallback switch in step 4.

## Example envelope (shape only)

```json
{ "ok": true, "initiative": "distillation", "status": "in-flight",
  "round": { "n": 3, "title": "Porcelain, plugin & features" },
  "active_step": { "id": "step-12", "title": "Roles set", "shipped": false },
  "dirty_wip_files": [],
  "solo_available": true, "solo_reachable": false,
  "deferred": [ { "id": "doctor-probe-duo", "title": "doctor --probe-duo mirror" } ],
  "signals": ["solo-unreachable"] }
```
