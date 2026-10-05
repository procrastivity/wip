# Release installer — contract review and adoption

The design of record remains the external wip-reboot repository (MODEL.md and
workplans/). This change adopts toolsmith's binary-bootstrap convention for
the existing WIP Go release line; it does not change the isolated M6 wipd
authority design or authorize a live installation/store cutover.

**Status: T37 release-installer convention officially adopted by the owner
on October 5, 2026, with a locally verified implementation based on toolsmith
v0.0.3. The v1.3–v1.5 delta review is not a full contract reconciliation or
conformance claim; C3.10 remains a separately recorded gap. No publication or
daily-install replacement is authorized by this adoption.**

## Baseline and authoritative sources

WIP's last recorded whole-contract review is
[`ce8225a`](https://github.com/procrastivity/wip/commit/ce8225acc8bc0a84a7cc49bcd7b89b8b8629c299),
September 16, 2026: `docs/contract-backport/decisions.md` records v1.3 read at
toolsmith
[`07a5434`](https://github.com/procrastivity/toolsmith/commit/07a5434f2171631632123a543ae89f7eb72cdda5).
The associated `evidence/2026-09-16-conformance.md` reports zero findings for
13 mechanical clauses and explicitly separates prose review. The later
September 21 release-driver adoption,
[`7075539`](https://github.com/procrastivity/wip/commit/7075539dc2fd4fd527e4e0f6e3ae57f18d140968),
ports `contrib/release` and protects both pushes with `--no-follow-tags`; it
does not record another whole-contract review.

The installed executable resolves through `~/.local/bin/toolsmith` to
`/home/dev/Code/toolsmith/bin/toolsmith` and reports v0.0.3, commit
[`20e9e44`](https://github.com/procrastivity/toolsmith/commit/20e9e44b2a70e37216b28a58142b5e3e17cf2259).
The runner's default noninteractive PATH omits `~/.local/bin`, so bare
`toolsmith` initially fails to resolve; prepending that existing directory
resolves the installed command without replacing it.

Authoritative inputs are the installed `toolsmith doc CONTRACT.md` (v1.5),
`toolsmith doc playbook/reconcile.md`, and the exact v0.0.3 tag's
`CONTRACT.md`, `DECISIONS.md` T37, `scripts/install.sh`,
`scripts/release_install_test.go`, release workflow and release/hygiene
playbook in `/home/dev/Code/toolsmith`. The local toolsmith-reboot handoffs
describe earlier reconciliation work, not a newer normative contract.

## Contract delta verdicts

| Clause / change | Verdict | Evidence and disposition |
| --- | --- | --- |
| v1.4 C3.9 | Holds | `internal/manifest/manifest.go` and `verbs.go` record `alias-of` only on porcelain `next`. The actual v0.3.0 release manifest has exactly `next` → `plumbing next`; `internal/cli/pairs_e2e_test.go` pins the manifest rows. |
| v1.4 C8.5 | Holds | WIP's D112 implementation is the clause's cited proof. `internal/cli/pairs_e2e_test.go` tests alias output, failures and help-path equivalence, plus status split-pair JSON equality. The existing code requires no installer change. |
| C4.3 clarification | Holds | `internal/harness/harness.go:Projectable` requires both the `plumbing ` namespace prefix and plumbing kind; `harness_test.go` tests each conjunct. No global harness installation is needed for this review. |
| v1.5 C3.10 | Diverges | Both source and actual release manifest lack `contractReconciledMinor`; toolsmith v0.0.3 check reports exactly that finding. Follow-up: add the field with a reviewed minor and corresponding manifest/digest assertions. It is not needed for binary installation and is deliberately left out of this installer-focused change. |
| C8.1–C8.4 reinspection requested by the prior review | Holds with a note | WIP now has the plumbing namespace and deterministic paired surfaces. It still has no `llm`-kind workflow/flow assets, so shape-only obligations are not applicable. No shape or projection redesign is included here. |
| T37 binary installer | Adopted by this change | Optional skeleton/distribution convention, not a new normative C6.9 clause or contract v1.6. The user's explicit goal is release-installed WIP independent of checkout builds. |
| C4.1 / C6.4 | Holds with a publication limitation | Bootstrap stays separate from `wip install`; the workflow explicitly packages `wip-install.sh` alongside the two binaries, checksum file and changelog. The new installer asset is not yet published. |

This is a delta review, not a fresh audit of every prose clause or the
experimental daemon. The checker remains partial; its one finding is not
suppressed or converted into a clean conformance claim.

## Installation decisions

The owner's decision, "Let's officially adopt it and commit all of the
appropriate files," ratifies T37 for WIP's Go release line and authorizes
the local adoption commit. It does not authorize a push, release, or change
to the live installation. C3.10's disposition remains the follow-up recorded
above; adopting the installer does not close that contract gap.

- Copy toolsmith's POSIX-shell bootstrap into `scripts/install.sh`, adapting
  repository, environment variables and asset names to WIP. Do not introduce
  `bin-install`, `update`, or `bin-uninstall` CLI verbs: those are not v0.0.3
  APIs, and WIP's existing install/uninstall verbs own harness projection.
- Match the existing release matrix: Linux amd64 and macOS arm64, `wip` only.
  Leave `wipd` builds, daemon activation, Nix subPackages and store routing
  unchanged. A native macOS run remains unverified on this Ubuntu runner.
- Default to `~/.local/bin`, with `WIP_INSTALL_DIR` for other destinations;
  select latest assets unless `WIP_VERSION` pins a tag. A missing pinned
  release fails rather than falling back. `WIP_BASE_URL` supports trusted
  forks with the same release layout.
- Download both binary and SHA256SUMS, verify before modifying a destination,
  then copy into a fresh file on its filesystem and rename into place.
  Direct copying to `~/.local/bin/wip` would follow the user's development
  symlink and overwrite `bin/wip`; rename replaces the symlink, leaving its
  target unchanged. Directory destinations are refused; failures preserve
  existing binaries/symlinks and clean installer temporary files.
- Keep SHA256SUMS restricted to binary assets, matching toolsmith. The script
  and checksum file are trusted GitHub/HTTPS inputs; checksums detect transfer
  corruption and do not authenticate a compromised release/bootstrap script.
- Put the installer under ShellCheck and the existing `go test ./...` gate.
  Exercise the release workflow's installer/checksum/publish commands using
  fixture binaries and a fake `gh`, never a real publish operation.

## Where this stands

The local installer works now against existing Go release v0.3.0 assets, so
`WIP_VERSION=v0.3.0 sh scripts/install.sh` can deliberately replace the daily
symlink without a checkout build. Validation used disposable destinations
only. The future standalone bootstrap URL needs a new, separately authorized
release carrying `wip-install.sh`; existing v0.3.0 does not have that asset.
Verification commands and results are recorded in
`evidence/2026-10-05-release-installer.md`. No tags, pushes, published releases,
system-installed binaries, harness projections or live stores were changed.
