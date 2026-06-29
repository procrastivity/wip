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

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Run `wip-plumbing review list`.** Forward `--initiative <slug>` from
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
