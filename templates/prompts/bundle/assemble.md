Task: ASSEMBLE N loose handoff files into ONE `bundle` lead manifest
(ADR-0011 + intake-kinds.md §3a). You are given the input files (each with
the path it will carry in `children[]`, already RELATIVE to the manifest's
location) plus optional `--target` / `--lead-as` hints. Emit a single bundle
manifest; the deterministic `wip-plumbing intake validate --kind bundle` gate
accepts it and the existing porcelain explode fans it out.

Do NOT re-derive the child paths — use EXACTLY the relative path given for
each input. The manifest is written at a location chosen so those paths
resolve; inventing or rewriting them breaks the explode.

Produce a manifest with:

1. YAML front-matter:
   - `wip-kind: bundle`.
   - `lead-as: brief` or `lead-as: amendment` — what the LEAD doc is. Use the
     `--lead-as` hint when given. If absent, INFER: an existing initiative is
     referenced (a `--target` slug or the inputs amend known work) →
     `amendment`; greenfield → `brief`. ASK when genuinely ambiguous. v1
     allows only these two lead kinds.
   - `children:` — one entry per input file, a map with:
     - `path:` — **required**, the exact relative path given for that input.
       Never a glob, never rewritten.
     - optional hints the explode consumes, only when the content justifies
       them: `kind` (`brief`/`amendment`; NEVER `bundle` — nested bundles are
       refused), `target`, `lane` (the `### Lane <name>` this child fills),
       `depends-on` (a sibling `path`; orders the explode), and one explicit
       amendment directive (`insert-after` / `replace` / `append-round` /
       `append-lane` / `insert-step-in-lane`) when the child is NOT a
       lane-filler.
   - `cross-cuts:` — optional but encouraged when the inputs imply concurrent
     tracks:
     - `shared-seams:` — a list of cross-track concerns (one string each).
     - `parallel-groups:` — a list of lane-name groups, e.g. `[[A, D]]`,
       naming tracks that run concurrently.
   - For `lead-as: amendment`: also include `target: <slug>` and an
     `append-round: <title>` directive (the lead opens a new round the
     children fill). Use the `--target` hint when given; ASK for the slug if
     it is needed and unclear.

2. A LEAD body satisfying its `lead-as` kind:
   - `lead-as: amendment`: a `## Round <N> — <title>` heading plus the
     main-lane (shared-prereq) step(s) as `- **step-NN — <title>** — <body>`
     bullets. Put ONLY the round heading + main-lane steps in the body.
   - `lead-as: brief`: a `# <Title>` + a `## Goal` or `## Summary`, no
     `target:` referencing an existing slug.

   Do NOT hand-author `### Lane <name>` subheadings or a `## Cross-cuts`
   section — the explode renders those deterministically from
   `children[].lane` and `cross-cuts`.

How the explode uses this (so you shape it right): it strips the bundle-only
keys to get the lead artifact, appends one empty `### Lane <name>` per distinct
`children[].lane`, appends a `## Cross-cuts (from bundle)` section from
`shared-seams`, applies the lead, then for each child seeds an amendment whose
directive is `insert-step-in-lane: <lane>` (when `lane` is set) or your
explicit directive hint, and shapes + applies it. A child with neither a
`lane` nor a directive is treated as folded into the lead body.

Hard rules:
- Never invent a path or a fact. Use only the relative paths handed to you and
  facts present in the input files.
- If `--target`, `lead-as`, a child's lane/dependency, or the parallel
  structure is unclear and you cannot justify it from the inputs, return an
  ASK rather than guessing.
- A child's `kind` is `brief` or `amendment` — never `bundle`.
