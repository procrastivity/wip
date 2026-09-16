# Conformance run — 2026-09-16

`toolsmith check` (toolsmith at `07a5434`) against this tree with the
contract-backport Matter's changes applied (working tree on top of
`5be0507`), audited against CONTRACT.md v1.3.

Result: **no findings.** The 2026-09-10 run
(toolsmith `evidence/2026-09-10-conformance.md`) reported five findings
against wip — C7.5 (no README), C6.6 (one-stage `make hooks`), C6.5
(unpinned Actions), C3.4 (no `manifest_digest`), C3.6 (no contract
declaration). All five are fixed by contract-backport step-01.

```json
{"findings":[],"audited":["C1.1","C1.2","C1.6","C2.1","C3.1","C3.4","C3.6","C6.2","C6.3","C6.5","C6.6","C6.7","C7.5"]}
```

Checker coverage is partial by design (the [check]-marked clauses only);
the prose clauses are read, not audited — that reading pass is recorded
in `docs/contract-backport/decisions.md`.
