# `wip bundle` — multi-file bundle assembler (spec)

Status: draft (tracks [ADR-0011](../decisions/0011-bundle-assembler-porcelain.md)).

Porcelain verb that turns **N loose handoff files** into **one** `bundle` lead manifest,
then optionally hands it to the existing `intake --kind bundle` explode. The assembler is
LLM-driven (CLI: provider; plugin: Claude); the manifest it emits is validated and exploded
by the *existing* plumbing/porcelain — see [intake-kinds.md §3a](./intake-kinds.md) for the
bundle shape and [ADR-0009](../decisions/0009-intake-as-pipeline.md) for the explode.

## 1. CLI contract

```
wip bundle <file>... [--target <slug>] [--lead-as brief|amendment]
                     [-o <manifest>] [--intake] [--dry-run] [--yes]
```

- `<file>...` — **two or more** readable input paths (the children-to-be). One file is a
  usage error (use `wip intake <file>` directly). Globs are resolved by the shell, not us.
- `--target <slug>` — initiative the bundle amends/creates; forwarded to the lead. If
  omitted and `--lead-as amendment`, the shaper asks.
- `--lead-as brief|amendment` — what the lead doc is. If omitted, the shaper infers
  (existing initiative referenced → `amendment`; greenfield → `brief`) and confirms when
  ambiguous. v1 allows only these two (mirrors intake-kinds §3a).
- `-o <manifest>` — where to write the assembled lead manifest. Default: `bundle.md` in the
  inputs' **common parent directory** (see §3). `-` writes to stdout (implies no `--intake`).
- `--intake` — after writing, chain into `wip intake <manifest> --kind bundle` (the
  explode). Without it, emit the manifest path + ledger and stop (review-first default).
- `--dry-run` — assemble + validate, write nothing; ledger shows what *would* be written.
- `--yes` — forwarded to the chained `intake` (non-interactive).

**Stdout** (JSON envelope): `{ ok, verb:"bundle", manifest:<path>, lead_as, target,
children:[{path,lane?,depends_on?,kind?}], wrote:[...], intake?:<intake-envelope> }`.
On `--dry-run`, `wrote:[]` and `manifest` is the would-be path.

## 2. Assembly behavior (the shaper's job)

Given the input files + flags, the shaper produces a bundle lead manifest per intake-kinds
§3a. It MUST:

1. Build `children:` — one entry per input file, `path:` **relative to the manifest's
   location** (§3), `kind:` (`brief`/`amendment`, never `bundle`), and the hints it can
   justify from content: `lane`, `depends-on` (a sibling `path`), one amendment directive
   when the child is not a lane-filler.
2. Choose `lead-as` (§1) and write a lead body satisfying that kind (an `amendment` lead =
   `## Round N` + main-lane prereq steps; a `brief` lead = `# Title` + `## Goal`). It must
   NOT hand-author `### Lane` subheadings or a cross-cuts section — the explode renders
   those from `children[].lane` + `cross-cuts` (intake-kinds §3a).
3. Populate `cross-cuts.shared-seams` / `parallel-groups` when the inputs imply concurrent
   tracks.
4. **Never invent a path or a fact.** If an input is unreadable, or a lane/dependency/target
   is unclear, ASK (plugin: inline in chat; CLI: `---ASK---` fence per step-10.5) rather
   than guess.

The byte-for-byte rules live in `templates/prompts/bundle/assemble.md`, fetched by both
frontends via `wip-plumbing template show bundle/assemble` (prompt-sharing seam, step-11).

## 3. Manifest location & child paths

`children[].path` is resolved by the explode **relative to the lead doc's directory**
(intake-kinds §3a). So the assembler writes the manifest where those relative paths hold:

- Default: the **longest common parent directory** of the inputs; each child path is the
  input's path relative to that dir. Inputs that share no ancestor below `/` → the shaper
  uses absolute child paths and warns in the ledger.
- `-o <path>`: child paths are computed relative to `dirname <path>`.

## 4. Plugin parity — `/wip:bundle <files…>`

Same contract, Claude as shaper. Procedure: resolve `wip-plumbing` (the
`${CLAUDE_PLUGIN_ROOT}` step, as in the other commands) → read the input files → fetch
`template show bundle/assemble` → assemble the manifest into a tempfile/`-o` → validate via
`wip-plumbing intake validate --kind bundle` → then run the **explode** inline (apply lead,
then per-child shape+apply), echoing the aggregate ledger. Clarifications happen in chat.

## 5. Reuse & boundaries

- Validation: existing `wip-plumbing intake validate --kind bundle`. No new validator.
- Explode: existing porcelain path (`_wip_intake_explode_bundle` / the plugin intake flow).
  `wip bundle --intake` is `assemble` then the *unchanged* `intake --kind bundle`.
- No new **plumbing** verb (honors the kickoff exclusion). `wip-plumbing` stays single-file.
- Nested bundles refused (a child may not be `bundle`; ADR-0009).

## 6. Error kinds

`bundle-too-few-inputs` (exit 2, <2 files), `bundle-input-unreadable` (exit 2), plus the
chained `intake` envelope's kinds when `--intake` is set. Assembly/ASK failures reuse the
porcelain shaper kinds (`shape-failed`, `ask-without-tty`, `bad-shape-response`).

## 7. Tests

- `test/test-wip-bundle.sh` — CLI: ≥2 inputs assemble a valid manifest (mocked provider via
  `WIP_PROVIDER_CMD`); child paths resolve relative to the manifest dir; `--intake` chains
  and fans out; `<2` inputs → exit 2; nested-bundle child refused; `--dry-run` writes nothing.
- `test/test-plugin-manifest.sh` — `/wip:bundle` command present, front-matter + bundled-
  binary resolution, fetches `bundle/assemble` via the template verb.
- `test/test-template-verb.sh` — `template show bundle/assemble` byte-equals the source.
