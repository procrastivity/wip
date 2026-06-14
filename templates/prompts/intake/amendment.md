Target kind: amendment — an edit to an existing initiative's roadmap.

Required shape (validator rules from intake-kinds.md §2/§3):
- YAML front-matter MUST include:
  - `target: <initiative-slug>` — the slug being amended.
  - exactly ONE directive: `insert-after: step-NN`, `replace: step-NN`,
    or `append-round: <Round title>`.
- A title heading (`# <Title>`) below the front-matter.
- Body content per the directive:
  - `insert-after` / `replace`: include a `### step-XX — <title>` heading
    where `XX` is the new step's id (may be a `.5` slot per the
    distillation convention) plus a one-or-more-paragraph body.
  - `append-round`: include `## Round <N> — <title>` plus at least one
    `### step-NN — <title>` entry.

If `target:`, the directive, or the step id is unknown, return an ASK.
