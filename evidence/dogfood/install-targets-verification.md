# Install-targets verification pass

Date: 2026-08-18

Matter: `surface-wip-install-targets-in-default-output-and-help`

Verdict: **Positive**

The pass followed V1-V5 in `evidence/dogfood/verification-model-decisions.md`.
The human reviewed and accepted the change and merged it before this
pass; this pass confirms the Matter contract at the merged revision.

## Reviewed revision

The reviewed revision is `20af38963ad384f08c408eba5c41bd41ed33dd04`
("feat(install): list harness targets in bare run and --help"), on branch
`go`, clean working tree. The commit changes five files: the install verb
and its tests, and — from the sibling Matter — the uninstall verb, its
tests, and the registry package.

## Contract results

| Criterion | Observed result |
|---|---|
| `wip install --help` lists the available harnesses. | Pass. The Long help renders "Available harnesses: claude-code, codex, pi, opencode." |
| Bare `wip install` surfaces the target list without a guess. | Pass. No args prints "available harnesses: …" plus a usage line on stdout and exits 0 (`MaximumNArgs(1)` replaced `ExactArgs(1)`). |
| The list renders from one source. | Pass. The package-level `harnesses` slice fed the help, the listing, and the unknown-harness error. (Superseded by the registry package in the sibling Matter, merged in the same revision.) |
| Tests cover the new outputs. | Pass. `install_test.go` adds no-arg and `--help` cases; both run in the fresh suite below. |

## Independent rerun

From a clean test state (`go clean -testcache`, `-count=1`) at the
reviewed revision: `go vet ./...` clean; `go test -count=1 ./...` all
packages ok, including `internal/verbs/install` and `internal/cli`.
Behavioral spot-checks of the built tree confirmed the listing, the
`--help` line, and the `validation.unknown-harness` refusal for an
unknown name (exit 1).
