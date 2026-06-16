Target kind: amendment — an edit to an existing initiative's roadmap.

Required shape (validator rules from intake-kinds.md §2/§3):
- YAML front-matter MUST include:
  - `target: <initiative-slug>` — the slug being amended.
  - exactly ONE directive: `insert-after: step-NN`, `replace: step-NN`,
    `append-round: <Round title>`, `append-lane: <name>`, or
    `insert-step-in-lane: <name>`. `append-lane` / `insert-step-in-lane`
    also require `target-round: <N>`.
- **Preserve any directive already present in the front-matter** — if the
  inbound artifact already carries one (including the lane directives
  `append-lane` / `insert-step-in-lane` and its `target-round`), keep it
  verbatim; do not "correct" it to a different directive. You are only
  reshaping the body into step form.
- A title heading (`# <Title>`) below the front-matter.
- Body content per the directive:
  - `insert-after` / `replace` / `insert-step-in-lane`: include a single
    `### step-XX — <title>` heading where `XX` is the new step's id (may be
    a `.5` slot per the distillation convention) plus a one-or-more-paragraph
    body.
  - `append-round`: include `## Round <N> — <title>` plus at least one
    `### step-NN — <title>` entry (or `- **step-NN — <title>**` bullets when
    the round carries `### Lane` subheadings).
  - `append-lane`: one or more `### step-NN — <title>` entries and NO
    `## Round` heading.

If `target:`, the directive, or the step id is unknown, return an ASK.
