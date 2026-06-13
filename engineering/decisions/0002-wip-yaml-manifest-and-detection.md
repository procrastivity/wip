# 0002 — `.wip.yaml` root manifest + sentinel detection contract

- Status: accepted
- Date: 2026-06-12
- Source: findings w1 §R5, w3 §1; SYNTHESIS C2

## Context

Tooling must answer "is feature X installed, and where?" deterministically. Today that
requires `find`-ing `.lds-manifest.yaml` and guessing engineering/ vs docs/. Planning
also escapes repos entirely (`~/.claude/plans/`).

## Decision

- A single **`.wip.yaml`** at repo root — a hidden dotfile paired with `.wip/`,
  **always committed** even when `.wip/` content is gitignored. It is the only
  steady-state detection input: enabled features + locations, gitignore policy,
  initiative registry, provider config, current initiative.
- **Detection contract:** a feature is *active* iff a `.wip.yaml` stanza enables it
  **and** its declared **sentinel** file exists. Stanza-without-sentinel and
  sentinel-without-stanza are the two drift states `wip doctor` reports.
- Scalar single LDS root for v1 (monorepo plural deferred).

## Consequences

- Steady-state detection = one file read; legacy/bootstrap = one shallow `find`.
- `.wip.yaml` must live at root (not under a gitignored `.wip/`) to stay discoverable.
- Every composable feature registers a descriptor (sentinel + schema check); `wip doctor`
  is a generic loop over descriptors.
