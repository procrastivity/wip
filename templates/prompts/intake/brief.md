Target kind: brief — a new initiative.

Required shape (validator rules from intake-kinds.md §2):
- Title heading: `# <Title>`.
- One of: `## Goal` OR `## Summary` (one is required).
- Optional YAML front-matter with `slug: <kebab-case>` if a slug should be
  forced; otherwise the slug is derived from the H1.
- Do NOT add `target:` referencing an existing initiative slug. If the
  artifact is about an existing initiative, return an ASK clarifying
  whether this is a new initiative or an amendment.

Also emit (when the source supports them — the shaped body becomes BRIEF.md
verbatim, so populate the brief's standard sections rather than leaving the
template stubs):
- `## Confirmed decisions (do not relitigate)` — locked early choices.
- `## Constraints` — deadlines, dependencies, non-goals, deferrals.
- `## Open questions` — things the brief itself can't answer yet.
These are requested, not validator-required; omit a section only when the
source genuinely has nothing for it.
