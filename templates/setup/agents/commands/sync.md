---
description: Reconcile wip ⇄ issue-tracker, pushing lifecycle transitions forward.
argument-hint: "[services…] [--initiative <slug>]"
allowed-tools: [Bash, Read]
---

# /wip:sync — reconcile wip ⇄ tracker (push-forward)

Reconciles wip's lifecycle cache with the issue tracker, **push-forward only**
(ADR-0019 §6): it advances issues to match wip, never moves them backward, and
never writes wip's truth from the tracker. How transitions land depends on the
backend's transport (ADR-0026), reported as `transport` in the envelope:

- **`cli`** (github/gitlab, or a Linear seam wired via `WIP_TRACKER_*` /
  `WIP_LINEAR_*`): the plumbing **already applied** every forward move in-process
  via the backend's write command. You only render what it did — there is no
  agent-side apply and no live re-read.
- **`mcp`** (Linear's default agent-side path): the bare write transport is
  absent, so the plumbing computes `pending` moves but does not write them. **You**
  apply each via the Linear MCP tools, enforcing the push-forward floor with a live
  re-read (step 3b).

## Procedure

1. **Resolve plumbing.** Run `command -v wip-plumbing`. If absent and
   `$WIP_PLUMBING_BIN` is unset, print a one-line install hint and stop:
   > `wip-plumbing` is not on PATH. Install wip first (see the project README) or set $WIP_PLUMBING_BIN.

2. **Run `wip-plumbing sync`,** forwarding any `services…` positionals (e.g. `linear`)
   and `--initiative <slug>` from `$ARGUMENTS`. Capture the JSON envelope
   `{ok, initiative, backend, transport, applied, pending, skipped, observed}`.

3. **Dispatch on `transport`.**

   **3a. `transport` is `cli` — the plumbing already applied everything.** Do
   **not** re-apply and do **not** re-read. The `applied` rows are the forward
   moves the backend's write command already made; `pending` is empty on a real
   run (non-empty only under `--dry-run`, meaning "would advance"); `observed` and
   `skipped` are as the plumbing computed. Go straight to step 4 and render.

   **3b. `transport` is `mcp` — apply the `pending` transitions via the Linear MCP
   connector, strictly forward only.** Each `pending` entry
   `{node, issue, to, min_rank}` is a forward move the plumbing computed but did
   **not** write. On this path the plumbing has no live tracker read, so its
   backward guard could not run — it delegates that guard to you via `min_rank`,
   the semantic rank of the target `to` state. **You** enforce it here with a
   live re-read; do not apply blindly.

   For each `pending` row, before writing:
   1. **Re-read the issue's current state.** Fetch `<issue>` via the Linear MCP
      tools and take its current workflow state.
   2. **Map that Linear workflow state to a semantic rank** — the same order the
      plumbing uses (`_wip_tracker_semantic_rank` ∘ the Linear provider mapping):
      `Todo` = 0, `In Progress` = 1, `In Review` = 2, `Done` = 3. Treat any state
      that maps to none of these as rank `-1` (unknown ⇒ below the floor, so the
      forward move applies). *(This map is Linear-specific and lives only on this
      MCP branch; `cli` backends never need it — their read command emits the
      semantic rank's token directly.)*
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

4. **Render to prose.** (Both transports land here.)
   - `applied`: forward moves that landed — the `cli` backend wrote them, or you
     wrote them via MCP in step 3b. *Advanced N issue(s).*
   - `skipped`: in-sync or stateless nodes — usually silent, summarized as a
     count.
   - `observed`: issues **ahead** of wip (tracker_state) — both those the
     plumbing surfaced and any the strictly-forward re-read in step 3b held back.
     Surface these — they may indicate work completed outside wip; never moved
     backward.
   - `pending`: empty on a real run; under `--dry-run` these are the moves that
     *would* advance — render them as a preview, don't apply.
   - If `ok: false`, surface `error.message` directly.

## Notes

- **Push-forward only.** Never move an issue backward; never auto-Done beyond
  wip's explicit cache (Done enters only via `/wip:complete-review` or a merge).
  Genuine conflicts are surfaced by `wip doctor --probe-tracker`, not resolved
  here.
- This command body is the contract; do not improvise off-script.
