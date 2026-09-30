# M5 Step 20 sanitized runtime acceptance

- Matter: `online-authority-proof` (`01M3HJ58D8C42C79SQ0MVVH90W`)
- Step: `20` (`01M3HJ9KMJ6ATZ5EV1G46VYPXT`)
- Run: 2026-09-30, against Step 19 base `65d7faf2359012cf901bf3385568eb5c0868a58f`
- Command: `WIP_AUTHORITY_LAB_PROJECT=wip-authority-proof-step20-final6 make authority-lab-runtime-test`
- Runtime: Docker Engine 29.8.1 (Debian GNU/Linux 12), Compose 5.5.1, Go 1.24.10
- Image: pinned `alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1`

## Outcomes

- Static Compose policy and ShellCheck: PASS.
- Runtime project: `wip-authority-proof-step20-final6`.
- `authority-env`: container `2087f90bbc6e`; volume `wip-authority-proof-step20-final6_authority-state`.
- `client-env`: container `def6d680b4a6`; volume `wip-authority-proof-step20-final6_client-state`.
- Internal network: `wip-authority-proof-step20-final6_lab`.
- Live container assertions: pinned image, read-only root, dropped capabilities,
  no added capabilities, no-new-privileges, no host PID/IPC namespaces, no host
  bind/device/port mounts, no host WIP paths or Docker socket, no secret-like
  environment keys, and exactly one distinct writable named volume per service:
  PASS.
- Bidirectional volume visibility probes: PASS; each service could write its
  own marker and could not see the other service's marker.
- In each service, with temporary profile/journal state rooted on its initially
  empty named volume: `TestM5AuthorityBackedMatterAndStepBirthThroughWipdProcess`
  and `TestM5TwoEnvironmentClaimCloseAndFinalPullSpine` (all ten scenarios): PASS.
  Covered injected boundaries include pre-submit pull unavailability, lost
  post-commit terminal/receipt-query response with exact-identity recovery, and
  acquired claim-journal close recovery. Temporary profile/journal directories
  were absent after each test suite.
- Command-start pull-ordering regressions for pending and no-pending concurrent
  authority advancement: PASS.
- Repository verification: `make check`, `go vet ./...`, and `git diff --check`:
  PASS. Race commands also passed:
  `go test -race ./internal/wipdauthority -run '^(TestM5AuthorityBackedMatterAndStepBirthThroughWipdProcess|TestM5TwoEnvironmentClaimCloseAndFinalPullSpine)$' -count=1`
  and
  `go test -race ./internal/wipd -run '^TestConnectedCommandStart(ReturnsPendingPrefixBeforeTailAndWrite|WithoutPendingPullsBeforeGuard|PullFailureAndRestartDoNotReturnUnadmittedHead|SerializesAdmissionAndPullThroughSharedDomainLane)$' -count=1`.
- Teardown removed the per-run containers, network, and volumes; independent
  Docker inspection confirmed none of those project resources remained: PASS.
- Sanitization: this record contains only matter/step, project, container,
  network, volume, test, and outcome identifiers. No test payloads, credentials,
  tokens, or private keys are retained.

## Scope note

The in-container test fixtures bind their test authority to loopback and run
separately inside each Compose service. This proves the real process tests and
fresh durable test state under the Compose runtime, alongside live container
and volume isolation. It does not claim authority traffic between the two
Compose containers; the command-start concurrency tests use the repository's
deterministic test authority on the Go host harness.
