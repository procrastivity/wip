# Uninstall-targets verification pass

Date: 2026-08-18

Matter: `surface-wip-uninstall-targets-the-same-way-as-install`

Verdict: **Positive**

The pass followed V1-V5 in `evidence/dogfood/verification-model-decisions.md`.
The human reviewed and accepted the change and merged it before this
pass; this pass confirms the Matter contract at the merged revision.

## Reviewed revision

The reviewed revision is `20af38963ad384f08c408eba5c41bd41ed33dd04`
("feat(install): list harness targets in bare run and --help"), on branch
`go`, clean working tree. The commit carries this Matter's share in the
same five files as its sibling.

## Contract results

| Criterion | Observed result |
|---|---|
| The harness list moved to one shared source. | Pass. `internal/harness/registry` holds `Names`; both verbs render from it. A leaf package was required: every harness subpackage imports `internal/harness` (for `Projectable`), so the list there would cycle. |
| `wip uninstall` matches `wip install` on discovery surfaces. | Pass. Bare run lists the harnesses and exits 0; `--help` carries the same line; the unknown-harness error joins the registry list with ", or ". |
| Uninstall validates before any uninstall work. | Pass. The name check now precedes flag setup and the harness switch; the error keeps code `validation.unknown-harness`. No test asserted the old wording — only exit codes — and all prior uninstall e2e paths still pass. |
| Tests mirror install's coverage. | Pass. `uninstall_test.go` adds unknown-harness, no-arg, and `--help` cases. |

## Independent rerun

From a clean test state (`go clean -testcache`, `-count=1`) at the
reviewed revision: `go vet ./...` clean; `go test -count=1 ./...` all
packages ok, including `internal/verbs/uninstall`, `internal/cli`, and
the four harness packages. Behavioral spot-checks of the built tree
confirmed both verbs' listings, `--help` lines, and refusals for an
unknown name (exit 1).
