# 0009 — Intake is a pipeline, not a verb

- Status: accepted
- Date: 2026-06-13
- Source: planning session (2026-06-13); ADR-0001, ADR-0002, ADR-0006

## Context

The step-05 CLI contract gave `wip-plumbing` a single `intake --validate <file>` verb that
shape-checks an inbound planning artifact. That phrasing silently elides what actually
arrives at wip's front door:

- Claude Code plan files (sometimes a fresh initiative, sometimes an amendment to an
  in-flight one).
- Claude Desktop handoffs — loose markdown, "here's where I left off."
- Structured `spec-generator` output conforming to an LDS feature-spec template.
- Human PRDs, slack pastes, bug-report-turned-proposal docs.

Each needs to be **classified** (what kind?), **shaped** (rewritten into the canonical
form for that kind), and **routed** (new-initiative `init`, roadmap amendment, workplan
seed, or rejection). `intake --validate` is only the *last gate* — there is no shaper, no
router, and no destination verb for the "amend an in-flight roadmap" case (today there is
only `init`, which assumes greenfield).

## Decision

Two coupled decisions.

**1. Intake is a pipeline, not a verb.** It spans both layers (ADR-0001) and has five
phases:

1. **classify** — deterministic best-guess of `kind` from front-matter + heading
   heuristics (plumbing).
2. **shape** — rewrite the artifact into the canonical form for its kind; may ask the
   user for missing facts (porcelain; LLM-driven).
3. **validate** — deterministic shape check against per-kind rules (plumbing).
4. **route** — pick a destination (`init` / `roadmap amend` / `workplan init`) and a
   target (initiative slug, step id) (porcelain; may ask).
5. **apply** — the terminal write to disk; refuses on shape failure (plumbing).

The plumbing layer (`wip-plumbing intake classify/validate/apply`) never asks a question
and never makes a judgment call. The porcelain (`wip intake`) drives the whole pipeline
and is allowed to ask the user. This preserves ADR-0001's seam.

**2. Artifact kinds are a closed vocabulary.** v1 kinds:

- `brief` — a new initiative; consumed by `init`.
- `amendment` — a roadmap edit on an existing initiative (insert step, replace step,
  append round); consumed by `roadmap amend`.
- `workplan-seed` — input to a specific step's workplan; consumed by `workplan init`.
- `spec` — an LDS-shaped feature spec; consumed by the LDS seam (ADR-0006).
- `handoff` — loose narrative; always coerced to `brief` or `amendment` during shaping,
  never applied directly.

Each kind has a minimum shape the plumbing validator enforces. The canonical reference
for kinds, shapes, and classification heuristics is
[`engineering/specs/intake-kinds.md`](../specs/intake-kinds.md).

## Consequences

- The step-05 spec (`wip-plumbing-cli.md`) is updated: `intake` becomes a verb with
  subcommands (`classify`, `validate`, `apply`); two new top-level verbs (`roadmap amend`,
  `workplan init`) cover the destinations `apply` dispatches to.
- The distillation roadmap gains three steps (07.5, 08.5, 10.5) — the existing step-07
  ships only "intake validate v0" (today's spec), and the generalization slots between
  step-07 and step-08.
- New artifact kinds can be added later without re-litigating the pipeline shape; the
  vocabulary is closed in v1 to keep the validator deterministic, but the spec is where
  additions land.
- ADR-0006 (wip owns seams, not tools) is preserved: `spec` validation delegates to LDS's
  own validator when available; intake itself never re-implements an LDS check.
- An LLM-driven shaper is now an explicit, named layer — not a hidden implication of
  "intake." This makes the determinism boundary auditable.
