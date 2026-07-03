# 0022 — `features.forge.backend` config is the primary forge selector

- Status: accepted
- Date: 2026-07-03
- Source: `setup-backends` initiative, Round 2 (step-08); Linear BDS-60; ADR-0018, ADR-0021

## Context

ADR-0018 §3 wired forge detection as a **remote-blind binary probe**:
`command -v gh` (preferred), then `command -v glab`. In a single-forge
environment that is enough — whichever CLI is installed is the right one. But in
a **mixed environment** where both `gh` and `glab` are installed, the probe is
blind to which forge the *repository* actually uses: it always resolves `gh`
first.

BDS-60 makes the failure concrete. On a GitLab repo with both CLIs present,
`_wip_forge_detect` picks `gh`; `gh pr view <branch>` returns empty (there is no
GitHub PR — the remote is GitLab); `_wip_forge_run` swallows the empty result;
and `forge observe` reports `intent: none` while `status --probe-forge` still
reports `forge_reachable: true`. The result is a **green light over a blind
observer** — the liveness probe says a forge is reachable while the observation
path silently mis-selects the wrong CLI and sees nothing. There was no way for
the user to tell wip *which* forge the repo uses, because ADR-0021 §2 declared
`setup forge` takes no backend argument (the kind was "auto-detected at
`status --probe-forge` time").

Round 2 already shipped the remedy in code: step-06 (`5acfa1d`) added the
`features.forge.backend` config layer read by `_wip_forge_detect`, and step-07
(`c048e1e`, `0dba645`) added the `setup forge [gh|glab]` optional pin and the
`features` echo. This ADR **records the demotion those steps encode**; it changes
no behavior.

## Decision

`features.forge.backend` (an explicit config pin, `gh` | `glab`) is the
**primary** forge selector. The remote-blind binary probe is **demoted to the
zero-config fallback** — it runs only when no explicit selection exists. The
`WIP_FORGE_CLI` env var stays **highest** as the explicit process pin and test
seam.

The canonical resolution order `_wip_forge_detect` implements, highest to
lowest:

1. **`WIP_FORGE_CLI` env** (highest) — explicit process pin / test seam.
   Set-but-empty forces `none` (the caller is asserting "no forge").
2. **`features.forge.backend` config pin** (**new primary** selector) — the
   repo's declared forge, written by `setup forge [gh|glab]`.
3. **Binary probe** (`command -v gh` → `command -v glab`, **gh-wins**) — the
   **demoted zero-config fallback**, used only when neither of the above
   selects. Absent both CLIs → `""` → `forge_reachable: null`, no signal
   (ADR-0018 §3, preserved).

This ADR records the demotion of the probe from *primary* to *fallback*. It does
**not** change code — the resolution order above matches the shipped
`_wip_forge_detect [configured_cli]` contract verbatim (step-06/07).

## Consequences

- A user on a mixed-forge host declares the repo's forge once
  (`setup forge glab`) and `forge observe` selects the right CLI deterministically
  — the BDS-60 mis-selection is closed by the config pin overriding the gh-wins
  probe.
- Zero-config behavior is unchanged for single-forge hosts: with no pin and no
  env, the probe still resolves the sole installed CLI exactly as before.
- The env seam (`WIP_FORGE_CLI`) remains the highest-precedence override, so
  existing tests and explicit process pins are unaffected.
- No config-schema, sentinel, or probe-transport change: `features.forge.backend`
  is an optional field under the existing `features.forge` block (ADR-0018 §5
  enablement, ADR-0021 §2 config-echo contract — both preserved).

## Supersedes / Amends

This ADR **surgically amends** two prior ADRs. Only the clauses named below
change; everything else in both ADRs stands.

**Superseded:**

- **ADR-0018 §3, first bullet** — "**Detection:** `command -v gh` (preferred),
  then `command -v glab`" **as the *primary* selector**. The binary probe is
  demoted to the zero-config fallback (rung 3 above); `features.forge.backend`
  is now primary.
- **ADR-0021 §2, the sentence** "`setup forge` takes **no backend argument**".
  `setup forge [gh|glab]` now *optionally* writes `features.forge.backend`; bare
  `setup forge` stays a pure enable flip.

**Preserved explicitly** (so the amendment stays surgical):

- **ADR-0018 §1** — observe, don't own the push.
- **ADR-0018 §3** — the two overridable env seams `WIP_FORGE_STATUS_CMD` /
  `WIP_FORGE_OBSERVE_CMD`, and the absent-CLI → `forge_reachable: null` / no-signal
  behavior.
- **ADR-0018 §4** — the observed-state → transition-intent mapping
  (`in-review` / `done` / none).
- **ADR-0018 §5** — the `features.forge` enablement gate and the Tier-0
  stand-down contract.
- **ADR-0021 §2** — the config-echo / idempotent / no-sentinel writer contract,
  and the `setup issue-tracker` **required**-backend behavior (unchanged; only
  `setup forge`'s backend argument moves from forbidden to optional).
