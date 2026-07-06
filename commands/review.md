---
description: List the nodes currently In Review in the wip lifecycle.
argument-hint: "[--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:review — what's In Review?

Renders the nodes currently **In Review** in the wip ⇄ tracker lifecycle
(ADR-0019), backed entirely by deterministic plumbing reading the local
lifecycle cache floor. Read-only.

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

2. **Run `"$WIP" review list`.** Forward `--initiative <slug>` from
   `$ARGUMENTS` if present. Capture the JSON envelope `{ok, initiative,
   in_review:[{node, state, reason, updated}]}`.

3. **Render to prose.**
   - If `in_review` is empty, say plainly: *Nothing is In Review in
     `<initiative>`.*
   - Otherwise list each node on its own line: the `node` id, and its
     `reason` + `updated` date as context (e.g. `demo/step-02 — In Review
     since 2026-06-28 (ship)`).
   - Remind the user that `/wip:complete-review <node>` is the manual Done
     gate for any of them.
   - If `ok: false`, surface `error.message` directly.

## Notes

- This reads the **cache floor** — wip's durable, headless view of lifecycle
  state. A live tracker read (Round 4 transport) refreshes the cache; when the
  transport is down, this still answers from the cache.
- This command body is the contract; do not improvise off-script.
