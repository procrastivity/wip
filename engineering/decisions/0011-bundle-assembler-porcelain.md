# 0011 — Bundle assembler is a porcelain verb

- Status: accepted
- Date: 2026-06-16
- Source: planning session (2026-06-16); ADR-0001, ADR-0009, ADR-0010; `.wip/scratch/kickoff-intake-bundle-kind.md` §"Hard exclusions"

## Context

`bundle` (ADR-0009) is a non-terminal intake kind: a single **lead** manifest whose
front-matter carries a `children:` list of concrete child handoff paths (relative to the
lead). Plumbing `apply --kind bundle` exits 4; the porcelain *explodes* it into one lead
intake plus per-child intakes ([ADR-0010](0010-parallel-lanes-in-roadmaps.md) lanes).

The single-file shape was deliberate. The bundle kickoff listed as a **hard exclusion**:
"No multi-file CLI (`wip intake a.md b.md`); no new plumbing applier verb." So `wip intake`
takes exactly one positional file, and the "multiple files" are expressed *inside* one
lead doc.

But the natural author UX is the opposite: point at N loose handoff files and get a
bundle. Building the lead manifest from N inputs — choosing `lead-as`, inferring each
child's `lane` / `depends-on`, extracting `cross-cuts`, writing the lead body — is
**judgment**. Per ADR-0001 that judgment cannot live in the deterministic plumbing layer.
And it is needed from **two** frontends: the CLI porcelain (`wip`) and the plugin
(`/wip:*`).

## Decision

Add a **porcelain-only** `bundle` verb that *assembles* N input files into one bundle lead
manifest, then hands off to the **existing** `intake --kind bundle` path. No new plumbing;
the single-file plumbing primitive and the "no plumbing applier verb" exclusion both stand.

- **CLI:** `wip bundle <f1> <f2> … [--target <slug>] [--lead-as brief|amendment]
  [-o <manifest>] [--intake] [--dry-run]`. The provider-backed shaper writes the manifest;
  `--intake` chains into `wip intake <manifest> --kind bundle`. Without `--intake` it emits
  the manifest (path + ledger) for review.
- **Plugin:** `/wip:bundle <files…>` — Claude *is* the shaper: it assembles the manifest
  inline (asking clarifying questions in chat), then runs the same explode flow
  `/wip:intake` drives.
- **Shared prompt:** the assembly rules live in `templates/prompts/bundle/assemble.md`,
  read by both frontends via `wip-plumbing template show` — the same prompt-sharing seam as
  the intake shapers (step-11). Command bodies say *what* to assemble, not *how*.
- **Reuse, not duplicate:** the assembled manifest is validated by existing plumbing
  (`intake validate --kind bundle`) and exploded by the existing porcelain path. The new
  verb only adds the multi-file → one-manifest front-end.
- **Child-path resolution:** the manifest is written so each `children[].path` is relative
  to the manifest's own location — default the inputs' common parent dir, or `-o <path>` —
  so the existing explode resolves the children unchanged.

## Consequences

- This narrowly **revisits** the kickoff's "no multi-file CLI" exclusion — but only at the
  porcelain layer. Plumbing stays single-file and deterministic; ADR-0001's seam holds.
- New porcelain verb (`lib/wip/wip-subcommands/bundle.bash`) + plugin command
  (`commands/bundle.md`) + shared prompt; the contract lands in
  [`engineering/specs/wip-bundle.md`](../specs/wip-bundle.md).
- Nested bundles stay refused — a child may not itself be a `bundle` (ADR-0009).
- The assembler is purely additive: it produces an artifact the existing validate/explode
  already accept, so bundle behavior stays single-sourced.
- A roadmap step tracks the build; tests add `test-wip-bundle.sh` (CLI), a `/wip:bundle`
  case in `test-plugin-manifest.sh`, and a `bundle/assemble` case in `test-template-verb.sh`.
- On build completion this ADR flips `proposed → accepted`.
