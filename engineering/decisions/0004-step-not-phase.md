# 0004 — "Step", not "Phase"

- Status: accepted
- Date: 2026-06-12
- Source: findings w1 §R1; SYNTHESIS (locked)

## Context

The collected workflows used two words for the same unit: "Step"
(`workflow-portable-stub`, symfony `playbook/`) and "Phase" (bizapps `.wip/`).

## Decision

The atomic unit of planned work is a **Step** (`step-NN`). "Phase" survives only as a
legacy alias in migration tooling, never in new docs.

Rationale: the Round → Step → Workplan → Chunk hierarchy is already four levels deep and
consistent; "Phase" overloads with SDLC phases (which are *across*-Step); `step-NN` is
numeric/orderable and extends to nested chunks; the symfony playbook already settled on
`step-NN` (bizapps was the outlier).

## Consequences

- All naming conventions, todo tags, and scratchpad templates use `step-NN`.
- Migration tooling maps `phase-a/b/c` → `step-NN`.
