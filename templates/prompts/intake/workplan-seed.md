Target kind: workplan-seed — input narrative for a specific step's workplan.

Required shape:
- YAML front-matter with `target: <slug>/<step-id>` (the slug AND the
  step-id, separated by `/`).
- A title heading (`# <Title>`) below the front-matter.
- Narrative body (no required section set).

If either the slug or the step-id is unclear, return an ASK.
