# Backlog prioritization — distillation

_Captured 2026-06-17, after Rounds 1–3 shipped. Lens for **importance**: how much does the
gap hurt a real consumer adopting `wip`'s LDS `extract` today? Lens for **effort**:
implementation surface in bash + tests. No P1s — the product shipped; nothing here blocks._

## P2 — promoted to Round 4 (2026-06-17)

These four became Round 4 — *Extract polish & LDS completion* (steps 16–19), ordered
quick-wins-first.

| Step | Item | Priority | Effort | Why this rank |
|------|------|----------|--------|---------------|
| step-16 | **glossary-partial-lds** | P2 | xs | Infra already done (inclusion rule declared, graceful skip wired). Just author `templates/glossary/lds.md`. Highest value/effort ratio — completes LDS glossary coverage in one file. |
| step-17 | **extract-extraction-report** | P2 | small | LDS §7 conformance gap; the data already exists in the stdout ledger — just serialize to `extraction-report.{md,yaml}`. Audit/observability win, low risk. |
| step-18 | **extract-verify-hashes** | P2 | small | Integrity: today stale sources extract silently (`hash_verification: "skipped-v1"`). Manifest already carries hash fields; add `shasum` to `setup deps` + a `--verify-hashes` flag. |
| step-19 | **extract-transform-mode** | P2 | large | A whole `extract` mode (heading_adjust / link_rewrite / markdown_format) is `unsupported[]`. High capability value but needs a small markdown engine in bash — **scope risk; spike before committing.** |

## P3 — remain in §Backlog (ship on demand)

| Item | Priority | Effort | Why this rank |
|------|----------|--------|---------------|
| **intake-apply-spec-graduate-dispatch** | P3 | medium | Removes an exit-3 stub in the intake→LDS seam, but is blocked on a porcelain prerequisite (spec-shaper must insert the `graduate-to:` directive). Sequence after that porcelain change exists. |
| **extract-summarize-mode** | P3 | medium | Explicitly "inherently LLM-driven, NEVER automatic" → belongs in porcelain, not plumbing. Deferred by design until the architectural home is decided. |
| **extract-resume-mode** | P3 | medium | Three-way idempotency already covers most re-run needs; full `--resume` is a separate contract with marginal added value today. |
| **extract-multi-file-source** | P3 | medium | Speculative — `source.files[]` + `combined_hash`. Add when a real manifest needs concatenation; no consumer demand yet. |
| **extract-templates-field-mappings** | P3 | large | Speculative + heavy — MADR/PRD-Lite templates with `field_mappings` (literal + `source:path:lines` refs). Add only when a consumer adopts templated extraction. |

## nit

| Item | Priority | Effort | Why this rank |
|------|----------|--------|---------------|
| **in-place-study-slice-fixes** | nit | xs | Fixes broken paths in gitignored reference slices — doesn't ship to users, and explicitly "needs a human call" (prtend is a deliberate counter-example). Cosmetic on internal study material. |

## Effort key

`xs` < `small` < `medium` < `large` < `xl` — rough bash-implementation + test surface, not calendar time.
