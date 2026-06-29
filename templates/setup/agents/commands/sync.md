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

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Run `wip-plumbing sync`,** forwarding any `services…` positionals (e.g. `linear`)
   and `--initiative <slug>` from `$ARGUMENTS`. Capture the JSON envelope
   `{ok, initiative, backend, transport, applied, pending, skipped, observed}`.

3. **Apply the `pending` transitions via the Linear MCP connector.** When
   `transport` is `mcp`, each `pending` entry `{node, issue, to}` is a forward
   move the plumbing computed but did not write. For each, move the Linear issue
   `<issue>` to the state `<to>` using the Linear MCP tools (e.g. update the
   issue's state). Apply **only** what's in `pending` — the plumbing already
   filtered out in-sync, backward, and tracker-ahead nodes.

4. **Render to prose.**
   - `applied` / newly-applied via MCP: *Advanced N issue(s).*
   - `skipped`: in-sync or stateless nodes — usually silent, summarized as a
     count.
   - `observed`: issues **ahead** of wip (tracker_state). Surface these — they
     may indicate work completed outside wip; never moved backward.
   - If `ok: false`, surface `error.message` directly.

## Notes

- **Push-forward only.** Never move an issue backward; never auto-Done beyond
  wip's explicit cache (Done enters only via `/wip:complete-review` or a merge).
  Genuine conflicts are surfaced by `wip doctor --probe-linear`, not resolved
  here.
- This command body is the contract; do not improvise off-script.
