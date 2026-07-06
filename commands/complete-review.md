---
description: Mark an In-Review node Done (the manual Tier-0 review gate).
argument-hint: "<node> [--initiative <slug>]"
allowed-tools: [Bash]
---

# /wip:complete-review — In Review → Done

Drives a node from **In Review** to **Done** in the wip ⇄ tracker lifecycle —
the manual Tier-0 review gate (ADR-0019 §A/§D; the Tier-1 equivalent is a forge
merge). Emits a `{to:done, reason:review-complete}` lifecycle intent into the
cache floor.

## Procedure

1. **Resolve `wip-plumbing`.** The plugin bundles the CLI; prefer the bundled
   copy, then an explicit override, then PATH. Run once:
   ```bash
   if [[ -n "${CLAUDE_PLUGIN_ROOT:-}" && -x "$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing" ]]; then
     WIP="$CLAUDE_PLUGIN_ROOT/bin/wip-plumbing"
   elif [[ -n "${WIP_PLUMBING_BIN:-}" && -x "${WIP_PLUMBING_BIN}" ]]; then
     WIP="$WIP_PLUMBING_BIN"
   elif WIP="$(ls -d "$HOME"/.claude/plugins/cache/*/wip/*/bin/wip-plumbing 2>/dev/null | sort -V | tail -1)" && [[ -n "$WIP" && -x "$WIP" ]]; then
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

2. **Parse `$ARGUMENTS`.** The first positional is the `<node>` (e.g.
   `step-04`); `--initiative <slug>` is optional. `<node>` is required — if it
   is missing, ask the user which node to complete (offer `/wip:review` to list
   the In-Review candidates) and stop.

3. **Run `"$WIP" review complete <node>`,** forwarding `--initiative <slug>`
   if present. Capture the JSON envelope `{ok, node, intent, was_in_review}`.

4. **Render to prose.**
   - On success, confirm: *`<node>` → Done (review complete).*
   - If `was_in_review` is `false`, add a gentle heads-up that the node was
     **not** previously In Review — the completion still applied, but the
     out-of-order transition may be worth a glance (e.g. the ship boundary
     never ran).
   - Note that the Linear/tracker write that consumes this intent is owned by
     the transport (Round 4) / BDS-20's Tier-1 path; this command records the
     intent in the cache floor.
   - If `ok: false`, surface `error.message` directly.

## Notes

- This command body is the contract; do not improvise off-script.
