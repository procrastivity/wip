Target kind: bundle — a roadmap-shaped LEAD doc plus N concrete child
handoffs that the porcelain explodes into one lead intake + per-child
intakes (ADR-0009 + ADR-0010). A bundle is non-terminal: you shape the
manifest; the pipeline applies the pieces.

Required shape (validator rules from intake-kinds.md §2/§3a):
- YAML front-matter MUST include:
  - `wip-kind: bundle`.
  - `lead-as: brief` or `lead-as: amendment` — what the LEAD doc is. v1
    allows only these two lead kinds.
  - `children:` — a non-empty list. Each entry is a map with:
    - `path:` — **required**, RELATIVE to this lead doc, resolving to a
      readable file. Never a glob — resolve globs to concrete paths.
    - optional hints the explode consumes: `kind` (`brief`/`amendment`;
      NEVER `bundle` — nested bundles are refused), `target`, `lane`
      (the `### Lane <name>` this child fills), `depends-on` (a sibling
      `path` or `id`; orders the explode), and one explicit amendment
      directive (`insert-after` / `replace` / `append-round` /
      `append-lane`) when the child is NOT a lane-filler.
  - `cross-cuts:` — optional but encouraged when the lead names them:
    - `shared-seams:` — a list of cross-track concerns (one string each).
    - `parallel-groups:` — a list of lane-name groups, e.g. `[[A, D]]`,
      naming tracks that run concurrently.
- Below the front-matter, the LEAD body must satisfy its `lead-as` kind:
  - `lead-as: amendment`: a `target: <slug>` + an `append-round:` directive
    in the front-matter, and a body with `## Round <N> — <title>` plus the
    main-lane prereq step(s) as `- **step-NN — <title>** — <body>` bullets.
    Put ONLY the round heading + main-lane (shared-prereq) steps in the
    body. Do NOT hand-author `### Lane` subheadings or a Cross-cuts section
    — the explode renders those deterministically from `children[].lane`
    and `cross-cuts`. Pick `<N>` as the next round after the initiative's
    current last round.
  - `lead-as: brief`: a `# <Title>` + a `## Goal` or `## Summary`, no
    `target:` referencing an existing slug.

How the explode uses this (so you shape it right):
- It strips the bundle-only keys to get the lead artifact, appends one empty
  `### Lane <name>` per distinct `children[].lane`, appends a
  `## Cross-cuts (from bundle)` section from `shared-seams`, and applies the
  lead.
- For each child it seeds an amendment whose directive is
  `insert-step-in-lane: <lane>` (when `lane` is set) or your explicit
  directive hint, then shapes + applies it. A child with neither a `lane`
  nor a directive is treated as already folded into the lead body.

Path-resolution + ASK protocol:
- Resolve every child `path:` relative to the lead doc's directory. If a
  named file does not exist but a close filename match does, ASK before
  guessing — never invent a path. If the lead names a track but you cannot
  find a concrete child doc for it, ASK which file (or whether to drop it).
- Extract dependencies from the lead's "sequence"/"dependencies" prose into
  `depends-on`, shared seams into `cross-cuts.shared-seams`, and `A ‖ D`
  parallelism into both `children[].lane` and `cross-cuts.parallel-groups`.

If `target`, `lead-as`, a child path, or the parallel structure is unclear,
return an ASK rather than guessing.
