---
description: Reconcile wip ⇄ issue-tracker, pushing lifecycle transitions forward.
argument-hint: "[services…] [--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:sync — reconcile wip ⇄ tracker (push-forward)

Reconciles wip's lifecycle cache with the issue tracker, **push-forward only**
(ADR-0019 §6): it advances issues to match wip, never moves them backward, and
never writes wip's truth from the tracker. The bare-CLI write transport is
deferred (BDS-23), so today the **agent/MCP path** is how `pending` transitions
actually land in Linear: this command runs the plumbing to compute the plan,
then applies it via the Linear MCP tools.

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

2. **Run `"$WIP" sync`,** forwarding any `services…` positionals (e.g. `linear`)
   and `--initiative <slug>` from `$ARGUMENTS`. Capture the JSON envelope
   `{ok, initiative, backend, transport, applied, pending, skipped, observed}`.

3. **Apply the `pending` transitions via the Linear MCP connector — strictly
   forward only.** When `transport` is `mcp`, each `pending` entry
   `{node, issue, to, min_rank}` is a forward move the plumbing computed but did
   **not** write. On this path the plumbing has no live tracker read, so its
   backward guard could not run — it delegates that guard to you via `min_rank`,
   the semantic rank of the target `to` state. **You** enforce it here with a
   live re-read; do not apply blindly.

   For each `pending` row, before writing:
   1. **Re-read the issue's current state.** Fetch `<issue>` via the Linear MCP
      tools and take its current workflow state.
   2. **Map that state to a semantic rank** — the same order the plumbing uses
      (`_wip_tracker_semantic_rank`): `Todo` = 0, `In Progress` = 1,
      `In Review` = 2, `Done` = 3. Treat any state that maps to none of these as
      rank `-1` (unknown ⇒ below the floor, so the forward move applies).
   3. **Compare `current_rank` against the row's `min_rank`** and act on the
      trichotomy (mirrors the CLI guard at `sync.bash`):
      - `current_rank < min_rank` — **strictly forward.** Move `<issue>` to
        `<to>` using the Linear MCP tools (update the issue's state).
      - `current_rank == min_rank` — **already in sync.** Skip; write nothing.
      - `current_rank > min_rank` — issue is **ahead** of wip. Do **not** write.
        Surface it as an `observed` entry `{node, issue, tracker_state}` (its
        live tracker state), exactly as the plumbing's `observed` bucket does —
        it may mean work advanced outside wip; never move it backward.

   Apply **only** strictly-forward moves. This live re-read is the floor that
   keeps a stale `in-progress` cache from stamping over a `Done` issue on the
   default MCP path.

4. **Render to prose.**
   - `applied` / newly-applied via MCP: *Advanced N issue(s).*
   - `skipped`: in-sync or stateless nodes — usually silent, summarized as a
     count.
   - `observed`: issues **ahead** of wip (tracker_state) — both those the
     plumbing surfaced and any the strictly-forward re-read in step 3 held back.
     Surface these — they may indicate work completed outside wip; never moved
     backward.
   - If `ok: false`, surface `error.message` directly.

## Notes

- **Push-forward only.** Never move an issue backward; never auto-Done beyond
  wip's explicit cache (Done enters only via `/wip:complete-review` or a merge).
  Genuine conflicts are surfaced by `wip doctor --probe-linear`, not resolved
  here.
- This command body is the contract; do not improvise off-script.
