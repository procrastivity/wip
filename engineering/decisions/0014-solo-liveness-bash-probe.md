# 0014 — Solo liveness is a bash probe; unreachable → warn + offer fallback

- Status: accepted
- Date: 2026-06-23
- Source: `orchestration-backends` initiative, Round 1 (step-03); ADR-0007, ADR-0013

## Context

`solo_available` in `/wip:status` is a **config echo** — it reports whether
`features.solo.enabled` is active in `.wip.yaml`, not whether Solo actually answers. So if
Solo is enabled but the MCP server is unreachable, nothing warns the user: orchestration
will simply stall at the first spawn. The Solo MCP tools (`mcp__solo__whoami`,
`mcp__solo__get_process_status`) are callable only by the LLM, not from deterministic
plumbing — which suggested liveness had to be agent-driven.

It doesn't. The standalone **`solo` CLI** (`solo status --json`) returns
`{ok, data.ready, …}` and is bash-callable. `roles/backends/solo.md`'s anti-pattern
forbids `mcp-cli` *for agents that already hold direct MCP tools* — it does **not** cover
the standalone `solo` CLI used from deterministic plumbing, so a bash probe is consistent
with the existing guidance.

## Decision

Add an **opt-in** `--probe-solo` flag to `wip-plumbing status`. When set (and Solo is
declared), it shells out to `solo status --json` (overridable via the `WIP_SOLO_STATUS_CMD`
test seam) and emits a `solo_reachable` field:

- `true` — probe returned `ok` + `data.ready`.
- `false` — probe ran, Solo did not answer ready.
- `null` — not probed (no flag) or no probe available (Solo declared but the `solo` CLI
  isn't on PATH). `null` means *unknown*, never a claim of "down".

When `solo_reachable == false` **and** the active orchestration backend is `solo`, status
adds a `"solo-unreachable"` signal (the actionable case — orchestration would stall).
`/wip:status` passes `--probe-solo`, surfaces the warning, and **offers** to fall back to
the Task backend (ADR-0013) via `wip-plumbing orchestrate backend task` — **never switching
automatically**.

The probe is **opt-in** precisely because it shells out and is non-deterministic; the
default `wip-plumbing status` stays pure, fast, and unchanged (the existing test corpus is
untouched by the no-flag path).

## Consequences

- `/wip:status` gives honest feedback when Solo is configured-but-down, and a one-confirm
  path to keep working on native subagents.
- The fallback is **warn-and-offer**, not auto-switch: switching orchestration backends is
  always an explicit user choice (and reversible with `orchestrate backend solo`).
- `status` gains its first non-deterministic, opt-in branch; it is isolated behind the flag
  and the `WIP_SOLO_STATUS_CMD` seam keeps the test suite network-/Solo-free.
- The probe is liveness only — it does not diagnose *why* Solo is down, and `null` is
  deliberately distinct from `false` so an absent CLI never reads as an outage.
