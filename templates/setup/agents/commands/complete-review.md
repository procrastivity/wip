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

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Parse `$ARGUMENTS`.** The first positional is the `<node>` (e.g.
   `step-04`); `--initiative <slug>` is optional. `<node>` is required — if it
   is missing, ask the user which node to complete (offer `/wip:review` to list
   the In-Review candidates) and stop.

3. **Run `wip-plumbing review complete <node>`,** forwarding `--initiative <slug>`
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
