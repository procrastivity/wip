# Step 10 verifier process-test inputs

The authenticated process test reads two exact external source inputs. It does
not use `.amp` packet files, a developer worktree, an unpinned branch, or a
verifier-side network request.

| Input | Source commit | Source path | SHA-256 |
| --- | --- | --- | --- |
| Model | `7caaf6e07f0b22d778f8431b37b2629a910b9b45` | `MODEL.md` | `77fb90f96d7a43cac041cb18fea8b27345e1b43b5b1b661b21cb04cb4ced8388` |
| Workplan | `92d3bd5dbe9d39203f3722ba916c768002a133ae` | `workplans/daemon-operation-parity-continuation.md` | `8c4a01850a07a97bd71b19ccbed97853950226d6f82c846fbeea8b5d7ed3ca71` |

GitHub Actions checks out each source by its full commit ID into separate
sparse directories, with `fetch-depth: 1` and credential persistence disabled.
The fine-grained `WIP_REBOOT_READ_TOKEN` is supplied only to those checkout
actions. The preparation script verifies each checkout's `HEAD`, path, and
content hash, rejects persisted HTTP credential headers, and copies only the
two pinned files plus a canonical source manifest into a fresh temporary
directory. That manifest records both source commits and hashes, plus the
repository, the expected external-source checkout action/ref-depth/sparse-path/
credential settings, and the verifier's denied-network configuration. Its
`ci_fetch_depth: 1` describes these two pinned external-source checkouts only.
The process test embeds this manifest in its review input, so the durable review
digest binds the actual input provenance without changing the verifier's fixed
five-blob contract.

The CI test job installs Nix and prepares the verifier flake inputs before the
external-source secret checkouts. It runs
`nix flake archive --json --no-write-lock-file .` using ordinary public network
access; `WIP_REBOOT_READ_TOKEN` is not passed to Nix. This is required because
`verifierLockedInputs` checks the local Nix database for the fixed CA of every
non-root `flake.lock` node, including nested inputs that a normal dev-shell
evaluation may not retain. The preparation step checks each exact CA record in
`/nix/var/nix/db/db.sqlite` and confirms that its store path appears in the
archive JSON. CI stops before the private checkouts if any locked input is
missing. The archive operation does not change `flake.lock` or alter verifier
assertions. The private checkouts still use depth 1 and
`persist-credentials: false`; the separate test step receives only the
prepared pinned input directory, and the verifier's checks remain offline and
isolated.

The primary checkout of this repository in the CI `test` job is different: it
uses `fetch-depth: 0` so the complete Git history, including the pinned base
`aa21e333b6000195d867b4f164fdb18159fb8c3b`, is available to the unchanged
candidate source collector. That collector compares the complete checkout tree
against this base before constructing the D106 snapshot; a depth-one checkout
does not contain the base object and fails with `fatal: bad object`. The full
history setting applies only to the primary repository checkout. It does not
change either external source checkout's shallow depth or token boundary. The
candidate manifest binds the workflow bytes and therefore this primary checkout
setting; `ci_fetch_depth` in the source provenance manifest remains `1` for the
external inputs.

## Running the test locally

`make check` runs this test, and the test needs three host conditions. The
test checks all three before it starts any process work. When one is missing,
it fails at once and names the fix:

- **Test deadline.** A full run takes about twelve minutes, so Go's default
  ten-minute package timeout always stops it. Set
  `GOFLAGS='-timeout=45m -count=1'`.
- **Pinned inputs.** Set `WIPD_STEP10_VERIFIER_INPUTS` to a prepared input
  directory.
- **Bubblewrap sandbox.** The test runs the Nix `bwrap` from the dev shell. It
  must create a user and network namespace and use `CAP_NET_ADMIN` in it. On
  Ubuntu 23.10 and later, `kernel.apparmor_restrict_unprivileged_userns=1`
  blocks this unless an AppArmor profile gives the Nix `bwrap` the `userns`
  permission. GitHub-hosted runners block it, so CI and the release
  workflow skip this one test for now.

When a local clone of `wip-reboot` already holds both pinned commits, prepare
the inputs from it, with no network access or credentials:

```sh
make step10-verifier-inputs-local          # WIP_REBOOT_DIR defaults to ../wip-reboot
GOFLAGS='-timeout=45m -count=1' \
  WIPD_STEP10_VERIFIER_INPUTS="$PWD/.step10-local/inputs" make check
```

The target clones the two commits into the ignored `.step10-local/` directory
and runs the same preparation script as CI. To leave the test out, as CI does:

```sh
GOFLAGS='-skip=^TestStep10PinnedPlanThroughAuthenticatedEnvironmentAndAuthorityProcesses$' make check
```

To use the same validation path with other checkouts, provide two explicit
detached source checkouts at the commits above and a fresh output path:

```sh
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
make prepare-step10-verifier-inputs \
  MODEL_SOURCE_DIR=/path/to/model-checkout \
  WORKPLAN_SOURCE_DIR=/path/to/workplan-checkout \
  STEP10_INPUT_DIR="$tmp/step10-inputs"
GOFLAGS='-timeout=45m -count=1' \
  WIPD_STEP10_VERIFIER_INPUTS="$tmp/step10-inputs" make test
```

The CI test step sets `GOFLAGS='-timeout=45m -count=1'` for the full suite.
This preserves every test and disables cached results; the longer timeout lets
the authenticated process acceptance complete (it can take over ten minutes)
instead of failing at Go's default ten-minute package timeout.

The preparation script itself never accesses credentials or the network. If
the private external repository cannot be checked out in a local environment,
preparation fails; do not fall back to ambient Git credentials or local
`.amp` files. The checked-in `step10-adversarial-analysis-fixture.txt` is
substantive deterministic **test input only**, not a High verdict, independent
review, or acceptance evidence. Independent High review remains a separate
gate for any changed candidate.
