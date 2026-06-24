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

2. **Run `wip-plumbing status --probe-solo`.** Forward `--initiative <slug>` from
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
   - If `ok: false`, surface `error.message` directly.

4. **Solo unreachable → warn + offer the Task-backend fallback.** If
   `signals` contains `"solo-unreachable"` (Solo is the active orchestration
   backend but the live probe did not answer), tell the user plainly: *Solo
   is enabled but not reachable — orchestration would stall at the first
   spawn.* Then **offer** to fall back to the Task backend (native subagents,
   no Solo):
   - On an explicit **yes**, run `wip-plumbing orchestrate backend task`. It
     regenerates `roles/backends/active.md` and flips
     `features.orchestration.backend` to `task`. Echo the resulting ledger and
     tell the user to re-run their orchestration (e.g. `/wip:orchestrate`).
   - **Never switch without confirmation** — this is the *only* write this
     command can make, and only on a yes. Switching back later is
     `wip-plumbing orchestrate backend solo`.

5. **No writes** other than the confirmed fallback switch in step 4.

## Example envelope (shape only)

```json
{ "ok": true, "initiative": "distillation", "status": "in-flight",
  "round": { "n": 3, "title": "Porcelain, plugin & features" },
  "active_step": { "id": "step-12", "title": "Roles set", "shipped": false },
  "dirty_wip_files": [],
  "solo_available": true, "solo_reachable": false,
  "signals": ["solo-unreachable"] }
```
