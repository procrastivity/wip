# 0021 — guided `setup` porcelain for the backend features

- Status: accepted (§2 no-backend-arg superseded in part by ADR-0022)
- Date: 2026-07-03
- Source: `setup-backends` initiative, Round 1 (step-01); BRIEF.md; ADR-0002, ADR-0007, ADR-0014, ADR-0015, ADR-0018, ADR-0019

## Context

The `setup` verb family writes consumer config for `agents`, `deps`, `direnv`,
`hygiene`, `lds`, and `release`. Three backends have **no writer**: Solo (the
control plane, `features.solo` — ADR-0014), the forge (`features.forge` —
ADR-0018), and the issue-tracker (`features.issue-tracker` — ADR-0019). Those
stanzas are hand-authored today, so a user (or an agent) who wants to wire up
Solo / GitHub / Linear has nothing to call and reverse-engineers the stanza
shapes from the ADRs and the bash source before hand-editing `.wip.yaml`. A live
incident made the cost concrete: an agent spent ~3 minutes reconstructing the
three stanzas by hand because the guided path every other `setup` domain
provides did not exist.

This step adds `setup solo`, `setup forge`, and `setup issue-tracker` as
first-class verbs. It is a **porcelain gap**, not a config-model change: the
stanzas these verbs write are byte-for-byte the ones a hand-author lands today.
The BRIEF left three open questions for this step, resolved below:

1. **Verb names** — short nouns (`setup solo`/`forge`/`tracker`) or the exact
   `.wip.yaml` feature keys (`setup solo`/`forge`/`issue-tracker`)?
2. **Tier policy surface** — how does `setup solo` expose the optional
   `agent_tier_policy` (`force_tier` / `fallback_tool`)?
3. **Setup-time liveness probe** — do these writers probe the backend CLI at
   write time, or stay pure config writers?

Two constraints frame the answers. ADR-0007 keeps `setup agents` from
auto-writing `features.solo` because *defaulting* the tier policy is
presumptuous — an objection about defaulting, not about guiding. ADR-0002's
sentinel model is unchanged: all three features are config-echo (no sentinel
file), so `active == enabled` and a fresh write is `doctor`-drift-clean with
nothing else on disk.

## Decision

### 1. Verb name == feature key

The three verbs are **`setup solo`**, **`setup forge`**, and
**`setup issue-tracker`** — each verb name is the `.wip.yaml` feature key it
writes, verbatim. `setup issue-tracker` is the only hyphenated verb in the
family; that literal key-to-verb correspondence is worth more than matching the
short-noun house style, because the verb, the config key, and the `doctor`
feature row all read identically, with no `tracker`→`issue-tracker` translation
for a caller to remember.

### 2. Config writers only — no new config model

Each verb writes exactly the stanza a hand-author lands today, into the
always-committed `.wip.yaml`, and nothing else:

- `setup solo` → `solo: { enabled: true }`
- `setup forge` → `forge: { enabled: true }`
- `setup issue-tracker` → `issue-tracker: { enabled: true, backend: <linear|github> }`

All three are config-echo features (no sentinel — ADR-0002), so the write alone
makes the feature `active`. `setup forge` takes **no backend argument**: the
forge kind (gh vs glab) is auto-detected at `status --probe-forge` time
(ADR-0018), so there is no field to set. (Superseded in part by ADR-0022:
`setup forge [gh|glab]` now *optionally* writes `features.forge.backend` — the
primary forge selector — while bare `setup forge` stays a pure enable flip.)
`setup issue-tracker` **requires** a
backend argument (`linear` | `github`) and rejects an unknown or missing value,
because the tracker backend *is* stored in config (ADR-0019).

### 3. `setup solo` surfaces the tier policy via optional flags, never defaults

`setup solo` writes the bare `solo: { enabled: true }` by default. The optional
`agent_tier_policy` is offered through two flags — `--force-tier <tier>` and
`--fallback-tool <name>` — which, when supplied, add the `agent_tier_policy`
block. When they are omitted, the verb writes only `{ enabled: true }` and emits
a stderr hint naming the two keys so the user knows the knob exists.

This honors ADR-0007 exactly: the policy is never *defaulted* (no value is
invented), only *guided* (the flags and the hint make it discoverable). Flags —
not an interactive prompt — keep the verb usable by non-interactive and agent
callers, consistent with the flag idiom the rest of the `setup` family already
uses (`--source`, `--check`, `--dry-run`, …).

### 4. No setup-time liveness probe

The verbs are pure config writers, like every other `setup` verb — they never
shell out to `gh`, `glab`, or `solo`. Writing config is decoupled from CLI
presence: a user can declare a backend before the tool is installed or
authenticated. Each verb's stderr hint points at the existing liveness path
(`status --probe-solo`, `status --probe-forge`) so verifying reachability stays
a deliberate, separate action — the probe transports (ADR-0014 / ADR-0018) keep
their single home in `status`.

### 5. Mirror the established `setup` verb contract (ADR-0015 pattern)

Each verb is idempotent (re-running writes nothing and reports
`skipped_idempotent`), emits a JSON envelope on stdout and a human hint on
stderr, honors `--dry-run`, and writes only `.wip.yaml`. `setup solo` is
distinct from `features.orchestration.backend: solo` (which `setup agents`
already flips): `setup agents` picks *which backend renders the agents*, while
`setup solo` wires the *control-plane* `features.solo` block (liveness + tier
policy). The verbs' copy must make that distinction explicit so neither is
mistaken for the other.

## Consequences

- Three small, self-contained writers land in `lib/wip/wip-plumbing-subcommands/setup.bash`
  with coverage in `test/test-setup.sh`, sharing a common config-echo enable
  helper (step-02).
- `setup issue-tracker` is the family's first hyphenated verb; the dispatcher and
  help text must accept it.
- The `wip-plumbing-cli.md` setup table and feature/sentinel table gain three
  rows; `doctor` already resolves these features generically (ADR-0002), so a
  fresh write is drift-clean with no doctor change required.
- No change to the config schema, the probe transports, or the sentinel model —
  this is purely the guided writer for backends that already exist.

## Deferred

- **Setup-time reachability warnings.** Running `--probe-*` at write time to warn
  on an absent/unauthenticated CLI (still writing config, exit 0) is a plausible
  future nicety; deferred to keep the writers decoupled from probe transport.
- **Linear CLI write transport.** Out of scope here; the tracker remains
  config-echo + MCP-driven.
- **A broader `setup orchestration` umbrella** (wiring `features.orchestration` +
  `features.solo` together) is not introduced; `setup agents` and `setup solo`
  stay separate per §5.
