Target kind: brief — a new initiative.

Required shape (validator rules from intake-kinds.md §2):
- Title heading: `# <Title>`.
- One of: `## Goal` OR `## Summary` (one is required).
- Optional YAML front-matter with `slug: <kebab-case>` if a slug should be
  forced; otherwise the slug is derived from the H1.
- Do NOT add `target:` referencing an existing initiative slug. If the
  artifact is about an existing initiative, return an ASK clarifying
  whether this is a new initiative or an amendment.
