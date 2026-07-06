# Reference — Duo's tiers → presets migration (what Duo is *now*)

- Status: **reference / authoritative** — the Duo & Solo facts below were verified against Duo
  source at the time of writing (see Provenance). This note is a durable, citable capture; it is
  **not** a directive. wip's own decisions live in the initiative brief + ADR-0025, not here.
- Date captured: 2026-07-05
- Initiative: `role-centric-runtime-selection` (the Duo-backend / role-centric effort)
- Source of record: Duo scratchpad `solo://proj/6/scratchpad/tiers-presets-how-du--107`
  (authored by the Duo maintainer; the "Duo/Solo facts" there are authoritative, the "wip
  recommendations" are proposals wip may adopt/adapt/reject).

> **Why this note exists.** Duo just **retired its "tier" (token-classification) runtime-selection
> model** in favor of explicit **presets**, with **no compatibility layer**. wip still carries the
> mirror-image pattern — command-first token classification in its Solo backend. This note preserves
> the authoritative "Duo is *now*" facts and the old→new translation, so the ADR and the Duo-backend
> roadmap step can cite a stable reference instead of a scratchpad that may move.

---

## 1. What Duo is now (authoritative)

Duo is a TypeScript MCP server + CLI that spawns Solo agent processes. It used to pick *which* agent
to spawn by classifying an agent tool's command string against hardcoded tiers. **That is gone.** The
current model has three concepts: **presets**, **providers**, and **extra_args**.

### 1.1 Presets (durable config)

A **preset** is a freeform label mapping to a list of **definitions**. Each definition selects one
Solo agent tool and optionally carries launch arguments and a provider label.

Config lives at `~/.config/duo/config.yaml` (resolution order: `DUO_CONFIG` env →
`$XDG_CONFIG_HOME/duo/config.yaml` → `~/.config/duo/config.yaml`):

```yaml
presets:
  builder:
    - { id: bld1a2b3, agent_tool_id: 17, extra_args: "--provider=openrouter --model=zai/glm-5.2", provider: openrouter }
    - { id: bld4c5d6, agent_tool_id: 5,  extra_args: "-m sonnet", provider: anthropic }
  reviewer:
    - { id: rev7e8f9, agent_tool_id: 4,  extra_args: "--model=gpt5.5 --effort=xhigh --agent=custom-reviewer", provider: openai }
  default:
    - { id: def0a1b2, agent_tool_id: 4, provider: openai }
```

Definition fields (Zod-`strict`):
- `id` — stable 8-char id, globally unique across all presets; the target for `remove`. Generated,
  not user-authored.
- `agent_tool_id` — integer Solo agent-tool id.
- `extra_args` — **a raw string** (not an array); tokenized at spawn time (POSIX-ish, no shell
  expansion).
- `provider` — optional freeform, filename-safe label.

Key properties:
- **Labels are freeform and unordered.** `builder`, `reviewer`, `medium` are all just names — there
  is **no** built-in ranking or capability meaning. (This is the crux of the translation problem for
  wip: you cannot ask Duo "give me a large agent.")
- **A preset may have multiple definitions.** Selection picks **one at random** among the *enabled*
  ones.
- **`default` is the only fallback.** If a requested preset has zero enabled definitions, Duo falls
  back to the `default` preset. If that is also empty → a clear error.

### 1.2 Providers (transient, lock-free runtime state)

A **provider** is a freeform label (e.g. `anthropic`, `openai`, `openrouter`) attached to
definitions. Its enabled/disabled state is **not** in config — it is one file per provider under XDG
state:

- Path: `$XDG_STATE_HOME/duo/providers/<label>` (default `~/.local/state/duo/providers/<label>`).
- Content `"0"` → disabled. File absent / unreadable / anything else → **enabled** (opt-out default).
- Lock-free atomic writes (temp file + `rename`). **Read fresh on every spawn**, never cached.

This lets you disable a rate-limited subscription mid-run without editing config, then re-enable it —
no restart, no config churn.

### 1.3 extra_args (per-launch arguments)

`extra_args` are appended to the resolved Solo agent command **without mutating the saved agent-tool
defaults**. Two sources, concatenated **preset-first, caller-second**:

```
final_args = tokenize(definition.extra_args)  ++  caller_supplied_extra_args
```

They reach Solo via `mcp__solo__spawn_process(kind="agent", agent_tool_id, extra_args: string[])`.

### 1.4 The selection algorithm (`resolve_preset`)

Given a preset name and optional `avoid_provider`:

1. Unknown preset → error.
2. Filter to definitions whose provider is **enabled** (a definition with *no* provider is always
   eligible).
3. Candidate ladder:
   - without `avoid_provider`: requested preset's enabled defs → else `default`'s enabled defs;
   - with `avoid_provider`: requested-minus-avoided → default-minus-avoided → **relent** to
     requested-with-avoided → default-with-avoided. `avoid_provider` is a **soft** preference; it
     never hard-fails on its own.
4. Pick one candidate **at random**.
5. No candidate anywhere → structured "unavailable" error naming the preset and disabled providers.

The resolved result carries diagnostics: `agent_tool_id`, `extra_args` (array), `provider?`,
`preset_requested`, `preset_used`, `fell_back_to_default`, `relented_on_avoid_provider`.

### 1.5 The MCP surface (exact, post-rename)

| Tool | Input | Result (shape) |
|---|---|---|
| `list_presets` | `{}` | `{ [preset]: { available, definitions[] } }` |
| `resolve_preset` | `{ preset, avoid_provider? }` | `ResolvedPreset` (the §1.4 diagnostics) — dry run, no spawn |
| `launch_agent` | `{ preset, name?, project_id?, avoid_provider?, extra_args? }` | `{ process_id, name, preset, agent_tool_id, extra_args[], provider|null, project_id? }` |
| `list_providers` | `{}` | `{ providers: [{ provider, enabled }] }` |
| `set_provider_enabled` | `{ provider, enabled }` | `{ provider, enabled }` |

`launch_agent`'s result **always** reports `provider` (possibly `null`) so a caller can chain "launch
the reviewer on a different provider than the builder" via `avoid_provider`.

### 1.6 The CLI surface (mirror of MCP)

```
duo config preset add <name> --agent-tool=<name|id> [--extra-arguments=<str>] [--provider=<label>]
duo config preset list [<name>]
duo config preset remove <definition-id>
duo config provider enable|disable|list <label>
duo agent list
duo agent resolve <preset> [--avoid-provider=<label>]
duo agent launch  <preset> [--name] [--project-id] [--avoid-provider] [--extra-arguments] [--prompt]
```

### 1.7 What was removed (hard break, no compat)

- `src/classifier.ts` (token→tier engine), `src/policy.ts`, `src/types/policy.ts`, and the whole
  `duo.policy.yaml` subsystem — **deleted**.
- The `small/medium/large` hardcoding and `command_tokens` / `selection` policy — **gone**.
- MCP tools **renamed** (pre-1.0 clean break): `list_agent_tiers`→`list_presets`,
  `resolve_agent_tool`→`resolve_preset`, `spawn_agent`→`launch_agent`. CLI
  `duo agent spawn <tier>`→`duo agent launch <preset>`.

> **The old Duo model, for reference:** a classifier tokenized each agent tool's `command` string and
> bucketed it into `small/medium/large`; `resolve_agent_tool(tier)` picked a tool in that bucket, with
> an optional `duo.policy.yaml` overriding the token map and a `selection.preference`. Duo's own brief
> called this "a hack that fights the point of agent tools."

---

## 2. The core mismatch (Duo now vs wip now)

| | Duo (now) | wip (now) |
|---|---|---|
| Selection unit | freeform **preset** label | fixed **tier** `small/medium/large` |
| Ordering | none (labels are unordered) | inherent capability ladder |
| Where resolution lives | explicit user **config** (presets) | **inferred** from tool inventory by token classification |
| Provider / model / extra_args | first-class | none |
| Multiples per selection | yes (random among enabled) | no (one tool per tier) |

Two consequences:

1. **Duo no longer has a home for wip's capability axis.** Any wip↔Duo bridge must translate wip's
   request into a preset *name* — Duo only knows names.
2. **wip's Solo classifier is the same pattern Duo just deleted.** It works only because the user
   maintains several Solo tools with models baked into their commands — the exact coupling Duo walked
   away from.

---

## 3. Translation layer (old → new)

**Duo MCP/CLI:**

| Old (Duo) | New (Duo) |
|---|---|
| `list_agent_tiers` | `list_presets` |
| `resolve_agent_tool(tier)` | `resolve_preset(preset, avoid_provider?)` |
| `spawn_agent(tier)` | `launch_agent(preset, …, extra_args?)` |
| `duo agent spawn <tier>` | `duo agent launch <preset>` |
| `duo.policy.yaml` (`command_tokens` + `selection`) | `presets:` in `config.yaml` + provider XDG state |

**wip concept → new home** (as resolved in ADR-0025):

| wip today | Becomes |
|---|---|
| Command-first classifier (`haiku→small`, `sonnet→medium`, `opus→large`) | **deleted**; replaced by explicit `role → agent-tool` (Solo) / `role → model` (Task) / `role → preset` (Duo) |
| Per-role tier defaults (Orch=small … Builder=medium) | per-role **assignments** in the active backend's config; role is the request |
| Escalation `medium → large` | per-role **escalation target** (a second role-scoped assignment/preset, e.g. `builder` → `builder-escalated`) |
| `.wip.yaml features.solo.agent_tier_policy.force_tier` | a fixed `default` assignment (every role → one tool) |
| `.wip.yaml … fallback_tool` | the `default` entry in the role→tool map |
| `--agent <name|id>` run pin (KV `wip/<slug>/agent-pin`) | unchanged in spirit — a per-run override of the role→runtime map |

---

## Provenance

Duo/Solo facts verified against Duo source (MCP tools in `src/server.ts` + `src/tools/`; schema in
`src/types/presets.ts` + `src/config.ts`; provider state in `src/state/`; selection in
`src/resolver.ts`; transport in `src/solo-client.ts` + `src/types/solo.ts`; CLI in
`src/cli/commands/`). Related Linear: BDS-55 (the Duo tiers→presets work, Duo-side). wip-side context:
`roles/backends/{solo,task,active}.md`, `roles/tier-policy.md`, ADR-0007 / 0012 / 0013.
