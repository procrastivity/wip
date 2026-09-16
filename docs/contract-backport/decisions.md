# `contract-backport` — decisions this Matter made

The design of record is the wip-reboot sidecar repository (MODEL.md and
`workplans/`), external on purpose and never copied into this repo. The
planning input for this Matter is the standing punch list at
`~/Code/toolsmith/backport/wip.md` (audited 2026-09-10 at `5be0507`),
plus toolsmith CONTRACT.md v1.3. This file records what building the
Matter forced that neither of those says (C7.1).

**Status: reconciliation pass complete, read against CONTRACT.md v1.3
at toolsmith `07a5434` — the first reading pass this repo has recorded,
so it read the whole document, not a delta. Last-reconciled minor: v1.3
(T25).**

---

## 1. Decisions

### 1.1 Install/Uninstall are registry-row methods, not fields (C4.2)

C4.2's field list names `{Name, InstallDir, Available, Generate,
Install, Uninstall}`. wip's six per-harness `install.go` files were
byte-identical once the harness name was normalized, so the shared
bodies moved to `internal/harness.Install/Uninstall`, parameterized by
exactly the three facts a row carries, and the row's `Install`/
`Uninstall` became methods deriving from those fields. The clause's
substance — one table, many readers, no per-harness disagreement —
holds; the two operations are no longer per-row data because there is
no per-row variation to record. Call sites did not change shape.

### 1.2 The skill description asset is shared, not per-harness (C4.4)

toolsmith keeps `description.txt` under its one harness's template
directory. wip has six targets emitting one identical sentence, so the
asset lives at `templates/skills/description.txt` (above the
per-harness directories) with one resolver,
`internal/harness.SkillDescription`, carrying the one-non-empty-line
validation (`validation.skill-description`). The per-harness
`judgment.md` files stay per-harness — that prose does vary.

### 1.3 Doctor's failing rule is the code prefix (C4.7)

`doctor` previously failed on every finding except the inert-tracker
advisory, by listing that code. It now fails on any code without the
`advisory.` prefix, which is C4.7's own rule. One deliberate behavior
change rides this: a stale harness artifact (an `advisory.` code) no
longer fails doctor. The refusal-coded findings — cycles, gate order,
tracked `.wip/`, incompatible harness stamp — still fail it.

### 1.4 The three open postures (punch items 8–10)

- **OutputSchema: kept.** A reserved slot awaiting the first verb with
  a JSON payload worth describing (C3.7). The skeleton chassis ships
  the same slot; dropping it here would diverge the reference
  implementation from the chassis extracted from it.
- **`.wip/` ignore: committed.** `/.wip/` moved from
  `.git/info/exclude` into the checked-in `.gitignore` with the C6.8
  rationale beside it; the per-machine exclude line is gone.
- **`docs/install-target-devin.md`: deleted.** A completed handoff for
  the shipped Devin target; the genre lives in toolsmith's
  new-harness-target playbook and the Devin path facts live in
  `internal/harness/devin`.

## 2. The reconciliation pass (v1.3)

The checker's [check] clauses are clean
(`evidence/2026-09-16-conformance.md`). The prose clauses were read
against this tree:

- **Hold:** C1.3–C1.5, C1.7 (nix/tag divergence documented in
  flake.nix, deliberately unreconciled), C2.2–C2.7 (hermetic e2e seams
  confirmed in `internal/cli/e2e_test.go` TestMain; `WIP_*_SKILLS_DIR`
  are commented test seams), C3.2–C3.3, C3.5, C3.7 (§1.4), C3.8
  (step-02), C4.1, C4.3 (stronger than before: per-harness packages now
  hold only paths, probe, and generator), C4.4 (hand-written prose is
  `description.txt`, six `judgment.md`, `agent-write-guidance.md`, all
  through the asset chain), C4.5–C4.7 (step-04), C5.1–C5.3, C6.1,
  C6.4, C6.8 (§1.4), C7.1 (this file), C7.4–C7.5.
- **Hold, with a note:** C4.2 (§1.1); C2.5 — wip's one code outside the
  five prefixes is `doctor.findings-present`, exactly the form v1.2
  sanctions (T26), so nothing renames it.
- **Not exercised:** C4.8–C4.10 (no splice/emit-only/variant targets,
  no data dir, no hooks), C5.4 (no encumbered assets), C7.2 (no port),
  C7.3 (first evidence record lands with this Matter).
- **Conform with no change:** C8.1–C8.4 — wip ships no shapes, and the
  `plumbing` namespace binds only tools that do (T31). Adopting the
  namespace in wip's flat surface is deliberately its own decision:
  the `plumbing-namespace` Matter, which this Matter blocks.

## Where this stands

wip measures clean against every clause the v1.3 checker reaches, the
prose clauses read as holding, and the last-reconciled minor is
recorded above. The next reading pass owes only the clauses the
contract adds after v1.3, plus C8 again if `plumbing-namespace` ships
shapes or the namespace.
