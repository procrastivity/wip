# Workplan — step-22 · agent-tool resolution fallback

Fixes the tier→tool resolution gap that forced manual `agent_tool_id=3` pinning
on every spawn through Rounds 4–5. Roadmap entry: **step-22 — agent-tool
resolution fallback** (large; Phase-1 spike to right-size; LAST step of Round 5).
Extends `roles/backends/solo.md` (Tier resolver), `roles/tier-policy.md`
(manifest override), the `.wip.yaml` schema/comments, and the `/wip:orchestrate`
+ `/wip:start` command bodies. Backend seam (ADR-0007) and the
plugin-command-not-CLI rule (ADR-0012) stay intact.

Started: 2026-06-19.

## The confirmed defect (root cause)

`mcp__solo__list_agent_tools()` on this repo returns exactly:

| id | name | command | tool_type | classifies as |
|---|---|---|---|---|
| 3 | `Claude` | `claude` | `claude` | **nothing** — no model token in `command`, no tier token in `name` |
| 4 | `Codex` | `codex` | `codex` | **nothing** |
| 17 | `Codex • GPT 5.5` | `direnv exec . codex --model gpt-5.5` | `generic` | `large` (`gpt-5.5` token) |

With `features.solo.agent_tier_policy.force_tier: large`, the command-first
resolver (`roles/backends/solo.md` §Tier resolver) finds **only id 17** for
`large` — so Solo-alone silently routes every spawn to the codex runtime, never
to the intended Claude/Opus runtime (id 3). We worked around it all session by
hand-pinning `agent_tool_id=3`. This step makes Solo-alone usable without that
workaround.

## Decisions (made here, feed later steps)

### Scope recommendation (the headline: ship-vs-defer)

**SHIP in step-22 — the full Solo-alone fallback "resolution ladder" (a + b +
c-read).** They are not three independent features; they are precedence rungs of
one resolver behavior, and shipping a subset re-creates the exact pain we hit:

1. **`--agent <name|id>` request override (b)** — highest precedence; per-invocation escape hatch. Deterministic, testable.
2. **session pin** — a tool chosen once (by flag or by ask) applies to ALL spawns for the rest of the run. Durable runtime state.
3. **`.wip.yaml` `fallback_tool` (c-read)** — per-project default the resolver honors when tier classification is non-confident. Deterministic, testable, and the most ergonomic permanent fix (set once, stop thinking).
4. **ask the human, then pin (a)** — interactive last resort when nothing above resolves AND a human is present. Doc-only to specify; cheap to ship; this is the rung that makes Solo-alone usable with **zero** pre-configuration.
5. **hard-fail (existing)** — unchanged, but now only reached when non-interactive AND no flag/config/pin. The failure message gains the new escape hatches.

Why ship (a) despite being non-unit-testable: it is **doc/spec text only** (no
plumbing, no new tool) and is the actual "Solo-alone stays usable WITHOUT Duo or
pre-config" guarantee. Dropping it leaves a fresh, unconfigured session
hard-failing — the precise gap that started this step.

**DEFER (out of step-22):**

- **Duo agent-tier-selection improvement** — the human's stated *correct
  long-term fix*. Tiers are Duo's native concept; Solo has none. Duo lives in a
  separate repo/track. → **new backlog item / separate initiative**, not
  shippable here. step-22's fallback is explicitly the Solo-alone bridge until
  that lands. The fallback's "if Duo is not in use" guard is satisfied by
  definition under `features.orchestration.backend: solo` (no Duo backend bound).
- **Hardened persist-to-`.wip.yaml` plumbing verb** — writing the chosen tool
  back to the manifest as a dedicated `wip-plumbing` verb. The *offer* to persist
  ships (the Orchestrator may, on the human's "always use this", perform the
  manifest edit via the existing yq idiom — the same machinery `workplan init
  --activate` uses). A dedicated, fully-tested write-verb is deferred as a
  fast-follow if the inline edit proves insufficient.
- **Programmatic tool classification/resolution inside plumbing** — stays in
  `roles/backends/solo.md`, executed by the live agent (ADR-0007/0012). Plumbing
  only *echoes config*, never *resolves tools*.

### Locked design choices

- **D1 — `.wip.yaml` key = `fallback_tool`, holding a tool NAME.** Sits beside
  `force_tier` under `features.solo.agent_tier_policy`; "fallback" precisely names
  its role (consulted only when tier resolution is non-confident). It holds a
  tool **name** (matched against `list_agent_tools[].name`/`command`), never an
  `id` — `solo.md` already forbids static id mappings ("ids are operational
  details and change"). Chosen over `default_agent` (too generic; conflates with
  always-on default rather than a resolution fallback).

- **D2 — `--agent <name|id>` accepts BOTH a name and an id.** Ergonomic: an
  operator often has the id from a just-run `list_agent_tools`. Resolution: an
  all-digits value is treated as an id, else a name; the resolved tool is what
  gets pinned. The *persisted* config (D1) is always a name.

- **D3 — the session pin propagates via the backend KV store, namespaced by
  initiative slug.** Key (named only in `solo.md`): `wip/<slug>/agent-pin`. The
  Orchestrator writes it once (from `--agent` or from the ask); the Coordinator
  and every Builder spawn reads it *before* tier resolution and, if set, uses it
  verbatim. KV is the substrate's "shared state" primitive (already bound in
  `solo.md`), durable across context loss — superior to prompt-threading the pin
  Orchestrator→Coordinator→Builder. Cleared/overwritten at the operator's
  discretion; lean: no TTL.

- **D4 — the interactive ASK is performed by the live human-facing agent (the
  Orchestrator), not "by a Roles doc."** A Roles doc is inert text; the agent
  following it prompts. The DECISION RULE (when to ask, the precedence ladder)
  is specified abstractly in `tier-policy.md` and concretely in `solo.md`; the
  ACTUAL prompt happens in the Orchestrator flow booted by `/wip:orchestrate` /
  `/wip:start`. Command bodies own `--agent` parsing + handing the value into the
  role flow; they name **no** backend MCP tools (ADR-0007/0012 + the seam test).

- **D5 — resolution stays in the Roles/command layer; plumbing adds only a thin,
  testable config echo.** `roles/backends/solo.md` (executed by the live agent)
  owns classification, ladder, ask, pin, spawn. Plumbing's only new behavior:
  `wip-plumbing detect` surfaces the `agent_tier_policy` block (`force_tier` +
  `fallback_tool`) as per-feature `detail` — the spec'd-but-unimplemented field
  (`engineering/specs/wip-plumbing-cli.md:125`). This is a pure config read
  (additive jq + one assert; permissive yq/jq parsing → zero-risk,
  backward-compatible) and is the **one automated-test seam** for the config rung.
  `orchestrate prep` is **not** touched — its contract is pinned to emit no
  tier/tool info (`test/test-orchestrate-prep.sh`).

- **D6 — this repo's `.wip.yaml` gets `fallback_tool: Claude`** so the very
  workaround that motivated the step disappears: the next Solo-alone run resolves
  `large` → Claude (id 3) with no manual pin. (Dogfood proof of the fix.)

## Chunks (decomposed BY LAYER — each ~one focused commit)

1. **`roles/backends/solo.md` — resolver fallback ladder (concrete).** In the
   Tier resolver section, after "Resolution order" / "Failure modes", add the
   fallback ladder: detect non-confident classification → consult, in precedence
   order, (1) request override (`--agent`, resolved name|id), (2) session pin
   (`kv_get wip/<slug>/agent-pin`), (3) `.wip.yaml fallback_tool` (name match
   against `list_agent_tools`), (4) ask the human and `kv_set` the choice as the
   session pin (+ offer to persist `fallback_tool` to `.wip.yaml`), (5) the
   existing hard-fail — only when non-interactive and nothing resolved. Define
   the "Duo not in use" guard as "active backend is `solo`". Update the failure
   message to list the new escape hatches. Names Solo tools freely (allowed here).

2. **`roles/tier-policy.md` — abstract decision rule (backend-agnostic).** Extend
   the "Manifest override" section: document `fallback_tool` as a sibling knob to
   `force_tier`, and state the abstract ladder (request-pin > session-pin >
   configured fallback > ask-the-human-and-pin > fail) WITHOUT naming any backend
   MCP tool (must pass `test-roles-backend-seam.sh`'s forbidden-token grep — note
   `fallback_tool`/`force_tier` are config keys, not forbidden tokens; `kv_*`,
   `agent_tool_id`, `list_agent_tools` ARE forbidden here).

3. **`.wip.yaml` — schema/comments + live value.** Add `fallback_tool` under
   `features.solo.agent_tier_policy` with a schema comment explaining it (name,
   consulted only when tier classification is non-confident, Duo-less Solo
   bridge), and set `fallback_tool: Claude` for this repo (D6). Update the
   inline `force_tier` comment to cross-reference it.

4. **`commands/orchestrate.md` + `commands/start.md` — `--agent` surface +
   propagation.** Add `--agent <name|id>` to the argument-hint + `$ARGUMENTS`
   parsing; on parse, hand the value into the Orchestrator flow as the session
   spawn pin (the role flow writes the KV pin per chunk 1 — the command body
   names no MCP tool). Document that the pin governs Coordinator→Builder spawns
   for the rest of the run. Keep both bodies "contract, do not improvise".

5. **plumbing — `wip-plumbing detect` config echo + test (thin).** Add a jq
   extraction so `detect` emits the `agent_tier_policy` block (`force_tier` +
   `fallback_tool`) as per-feature `detail` for the `solo` feature
   (`lib/wip/...detect.bash`); add a `test/test-detect.sh` case asserting the
   echoed values, following the existing heredoc-`.wip.yaml` + `jq` + `assert_eq`
   pattern. No resolution logic; pure config surface.

> Suggested batching: chunks 1–4 are doc/manifest/command edits (parallelizable,
> low-risk); chunk 5 is the only code+test change. Chunk 2 must not regress the
> seam test — run `test/test-roles-backend-seam.sh` after it.

## Test strategy

**Covered (deterministic seams):**
- `test/test-detect.sh` — new case: `.wip.yaml` with `agent_tier_policy.{force_tier, fallback_tool}` → `detect` echoes both under the `solo` feature detail. (chunk 5)
- `test/test-plugin-manifest.sh` — new assertions: `/wip:orchestrate` + `/wip:start` advertise `--agent <name|id>` in argument-hint/body; bodies still name no `mcp__solo__` tool. (chunk 4)
- `test/test-roles-backend-seam.sh` — must STAY green: chunk 2's new `tier-policy.md` wording introduces no forbidden Solo token; chunk 1's tokens are confined to `solo.md`. (chunks 1–2)
- `test/test-orchestrate-prep.sh` — must STAY green: prep contract untouched (no tier/tool fields added). (regression guard)

**Deferred coverage (with reason):**
- The interactive ask (rung a) and the live KV session-pin propagation
  (Orchestrator→Coordinator→Builder) are **integration/dogfood-only** — they
  require a live Solo runtime and a human prompt; not unit-testable in the bash
  harness. The *decision rule* (doc) and the *config read* (detect) ARE tested;
  the runtime ask/pin is verified by dogfooding step-22 itself (D6: the next
  spawn resolves `large` → Claude with no manual pin).

## Definition of done

- A Solo-alone run hitting an unresolvable `large` tier no longer silently routes
  to codex (id 17) nor hard-fails blindly: with `fallback_tool` set it resolves
  to the named tool; with `--agent` it uses that; with neither + a human present
  it asks and pins for the session.
- This repo's `.wip.yaml` carries `fallback_tool: Claude`; a fresh spawn resolves
  `large` → Claude (id 3) with **no** manual `agent_tool_id` pin (dogfood proof).
- `roles/backends/solo.md`, `roles/tier-policy.md`, both command bodies, and
  `.wip.yaml` reflect the ladder; `detect` echoes `agent_tier_policy`.
- All test suites green, incl. the unchanged seam (`test-roles-backend-seam.sh`)
  and prep (`test-orchestrate-prep.sh`) contracts; lint clean.
- The deferred Duo tier-selection track is captured as a backlog item, and Round
  5 closes (step-22 is its last step).

## Open questions to resolve during execution (each with a lean)

- **Q-A (key name).** `fallback_tool` vs `default_agent`? **Lean: `fallback_tool`**
  (sibling to `force_tier`; names the resolution-failure role precisely).
- **Q-B (name vs id in config).** **Lean: NAME only** in `.wip.yaml` (ids are
  operational per `solo.md`); `--agent` flag accepts both for ergonomics.
- **Q-C (pin propagation mechanism).** KV vs prompt-embedded vs scratchpad?
  **Lean: KV**, key `wip/<slug>/agent-pin`, no TTL (D3) — durable, slug-namespaced,
  survives context loss.
- **Q-D (where the ask lives).** **Lean: live Orchestrator agent performs it**;
  rule specified in `tier-policy.md` (abstract) + `solo.md` (concrete); command
  bodies only parse `--agent` (D4).
- **Q-E (plumbing helper?).** **Lean: NO resolver in plumbing**; only a thin
  `detect` config echo for the testable seam (D5). Resolution stays in solo.md.
- **Q-F (persist-to-`.wip.yaml`).** Ship the *offer* + inline yq edit; **defer**
  a dedicated hardened write-verb. Confirm during execution whether the inline
  edit is robust enough or the verb must be pulled in.
- **Q-G (Duo scope).** **Lean: fully defer** to a separate track/initiative;
  open a backlog item. step-22 ships only the Solo-alone bridge.
- **Q-H ("Duo not in use" detection).** **Lean: ≡ `features.orchestration.backend
  == solo`** — true by definition this round; revisit when a Duo backend exists.
