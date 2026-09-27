# M4 Step 9: integrated isolation and lifecycle evidence

Status: Linux orb and isolated GitHub Actions evidence for the M4 exit checks.
This note records existing focused tests plus the client-process restart
regression added in this step.
All fixture values below are disposable synthetic test-handler records; they
are not authority state, events, receipts, claims, journals, or canonical
matter state. The production `NewServer` remains unregistered/empty, and the
production `matter.create` adapter remains legacy-only.

## Evidence map

| Exit check | Test evidence | Result and boundary |
|---|---|---|
| Client restart retains fixture data | `internal/wipd.TestClientProcessRestartRetainsFixtureState` | A subprocess client submits the asymmetric fixture command and exits; a fresh subprocess reconnects; the daemon-owned fixture record remains exact. This covers client process replacement, not restart of a production client service. |
| Daemon kill/restart retains fixture data | `internal/wipd.TestFixtureDataSurvivesForegroundCrashAndRestart`; `cmd/wipd.TestForegroundSubprocessLifecycle`; `internal/wipd.TestLifecycleRetainsLockInodeAndFixtureData` | The foreground daemon is killed and restarted; durable fixture state, singleton-lock inode behavior, and socket lifecycle are checked. A second live daemon is refused without replacing the winner. |
| Distinct UID is denied before downstream effects | `internal/wipd.TestDistinctUIDSubprocessRejectedBeforeDownstreamEffects` | **PASS on isolated GitHub Actions `ubuntu-latest`** ([run 36312584639](https://github.com/procrastivity/wip/actions/runs/36312584639), job [108601242815](https://github.com/procrastivity/wip/actions/runs/36312584639/job/108601242815)), testing the exact Step 9 source commit `1b5a6099d3104c7a9cd622101382b23381f18273`. The job ran the test binary as root and required the explicit `--- PASS` line; its workflow fails on `SKIP`. The separate orb attempt did skip because `setpriv --reuid 65534` failed with `setresuid failed: Operation not permitted`, and is not counted as evidence. |
| Default/override/symlink/alias roots refuse; sentinel is unchanged | `internal/wipdprofile.TestResolveProfilePaths`; `TestResolveRejectsSymlinkCanceledByDotDot`; `TestResolveRefusesBeforeCreationAndLeavesLegacySentinelUnchanged`; `internal/cli.TestExperimentalProfileStatusIsLocalAndDoesNotTouchLegacyStore`; `TestActivateUnavailableDoesNotCreateProfileOrOpenLegacyStore` | Tests cover default store, `WIP_DB_PATH`, XDG, symlink root/ancestor, lexical alias, canceled `..`, pre-create refusal, and byte-for-byte normal-store sentinel preservation. `Open` re-resolves the profile before creating/opening fixture data. |
| Client direct-store prohibition | `internal/wipd.TestWipdImportGraphExcludesLegacyAndAuthorityStores`; `internal/wipd.TestProductionServerAdvertisesNoUnregisteredLegacyHandler` | Production `internal/wipd` dependency graph excludes `internal/store` and `internal/authoritystore`; `NewServer()` advertises no operation until an explicit handler registry is supplied. No client-side database opener is introduced. |
| Typed IPC/dispatcher parity, local status, and no fallback | `internal/wipd.TestClientMapsBareM1ResultAndFixtureStateMatchesDirectDispatch`; `internal/wipd.TestRegistryDispatchPersistsOnlyLocalFixtureAndReturnsTypedOutput`; `internal/wipdfixture.TestTestOnlyRegistryHandlerPersistsTypedAsymmetricFixture`; `internal/cli.TestExperimentalProfileStatusIsLocalAndDoesNotTouchLegacyStore`; `TestExplicitExperimentalProfileNeverFallsBackToLegacyMatterCreate` | Direct and IPC dispatch preserve typed success/rejection/refusal and asymmetric fixture state; status remains local-only; explicit experimental invocation does not fall back to the legacy matter.create path. Registry-backed durable handlers exist only in `_test.go` files. |
| Same-key serialization and independent-key progress | `internal/wipd.TestDomainExecutionLaneSerializesMutationsAndIsolatesOtherDomains` | A held mutation queues a same-domain mutation while a different domain completes; final fixture counters are checked. Keys are synthetic scheduling inputs, not authenticated authority identities. |
| Bounded exchanges and cancellation/uncertainty | `internal/wipd.TestExchangeConcurrencyLimitReturnsCorrelatedOverloadBeforeDispatch`; `TestDeclaredLongerOpenBodyDispatchesWithoutWaitingForEOF`; `TestStalledPartialFrameDoesNotStarveValidExchange`; `TestControlCancelBeforeLaneDispatchReturnsNoEffect`; `TestDisconnectAfterFixtureCommitReturnsOutcomeUnknownAndKeepsValue`; `TestControlCancelStopsWaitingWithoutCancellingOrRollingBackHandler` | Limits reject excess work before handler dispatch; open/stalled request bodies do not defeat bounds; pre-dispatch cancellation has no effect; post-commit disconnect returns `transport.outcome-unknown` and retains the committed fixture value. |

## Commands and limitations

Commands run on this branch:

```text
go test -count=1 ./internal/wipd ./internal/wipdprofile ./internal/wipdfixture ./internal/cli ./cmd/wipd ./internal/operation
  PASS: all six packages returned `ok` (wipd 7.536s, wipdprofile 0.015s,
  wipdfixture 0.016s, cli 46.836s, cmd/wipd 0.914s, operation 0.129s).

go test -count=1 -v ./internal/wipd -run '^TestDistinctUIDSubprocessRejectedBeforeDownstreamEffects$'
  SKIP: setpriv --reuid 65534 failed: setresuid failed: Operation not permitted.
  The test binary returned PASS with the test explicitly marked SKIP, not PASS.

GitHub Actions workflow `.github/workflows/m4-cross-uid-peer-auth.yml`
  From branch `amp/m4-step9-cross-uid-ci`, commit
  1178f0264e9fdb1b1690f324bc87011e570b21cd. PASS: run 36312584639, job
  108601242815 on isolated `ubuntu-latest`.
  Checked out exact Step 9 source 1b5a6099d3104c7a9cd622101382b23381f18273,
  built with the repository's Nix Go 1.24.10 environment, and ran the test
  binary as root. The log contains `--- PASS:
  TestDistinctUIDSubprocessRejectedBeforeDownstreamEffects (0.05s)`; the
  workflow explicitly fails if that test is skipped.

go test -race -count=1 ./internal/wipd ./internal/wipdprofile ./internal/wipdfixture ./internal/cli ./cmd/wipd ./internal/operation
  PASS: all six packages returned `ok` (wipd 11.461s, wipdprofile 1.025s,
  wipdfixture 1.035s, cli 119.992s, cmd/wipd 1.926s, operation 2.142s).

go test ./... -count=1
  PASS: all packages returned `ok` or `[no test files]`; no package failed.

git diff --check
  PASS.
```

The distinct-UID test must be run with `-v` to expose whether it passed or was
skipped; a package-level `ok` line alone does not convert the capability skip
into a pass.

The evidence was collected on Linux only. `TestStartFailsClosedOnUnsupportedPlatform`
is built only on non-Linux systems and was not executed in this orb; no claim
is made about unsupported-platform behavior beyond that source-level test.
The orb's distinct-UID capability limitation remains recorded, but it no longer
blocks this exit check: the isolated Actions run above passed the exact helper
and rejected a capability skip. The root's local Step 5 sealed status was not
used as evidence; the successful isolated run supplies the missing cross-UID
result. The non-Linux-only `TestStartFailsClosedOnUnsupportedPlatform` remains
unexecuted here and is not represented as a Linux test pass.
