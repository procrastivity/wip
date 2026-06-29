# Changelog

All notable changes to this project are documented here.

## [0.0.15] - 2026-06-29

### Added

- Step-lifecycle step-01: ADR-0019 — the wip ⇄ tracker lifecycle contract
- Step-lifecycle step-02: issue-tracker feature + tracker: node mapping
- Step-lifecycle step-03: cache-as-floor lifecycle-state lib
- Step-lifecycle step-04: Tier-0 lifecycle intent emission at boundaries
- Step-lifecycle step-05: review command surface (list + complete + slashes)
- Step-lifecycle step-06: tracker transport adapter (bind plan + state map)
- Step-lifecycle step-07: doctor --probe-linear (Lane A, read-only drift)
- Step-lifecycle step-08: wip sync push-forward reconciler (Lane B)
- Step-lifecycle step-09: surface unfiled deferred/backlog items

### Other

- Step 1: add test/run parallel runner with fail-aggregation + --serial
- Step 1: WIP_TEST_TIMING slowest-N timing report
- Step 1: repoint make test at parallel runner + lint test/run
- Step 1: add test/run regression + baseline/guardrail proof
- Step 2: add CI workflow (Nix + make check) on push/PR
- Step 3: add fixture builders to test/helpers.sh
- Step 3: migrate test-next.sh to fixture builders (count 52 unchanged)
- Step 3: make wip_mktemp subshell-safe (tracking file + source-time trap)
- Step 3: migrate test-status.sh to fixture builders (count 43 unchanged)
- Step 3: migrate test-doctor-closeout.sh to fixture builders (count 22 unchanged)
- Step 3: migrate test-orchestrate-prep.sh to fixture builders (count 50 unchanged)
- Step 3: migrate test-graduate.sh to wip_mktemp (count 37 unchanged)
- Step 4 (R1): port folded-into-lead case into test-wip-intake-bundle (count 43->52)
- Step 4 (R1): remove test-wip-intake-bundle-roundtrip; coverage merged into test-wip-intake-bundle (suite 43->42 files, intentional)
- Step 5 · Task 1: test-extract jq-collapse (C1) — count 175 unchanged
- Step 5 · Task 2: test-wip-intake-bundle jq-collapse (C3) — count 52 unchanged
- Step 5 · Task 3: test-setup jq/yq-collapse (C4) — count 133 unchanged
- Step 5 · Task 4: test-roadmap-amend jq-collapse (C5) — count 57 unchanged
- Step 5 · Task 5: test-wip-bundle jq-collapse (C6) — count 42 unchanged
- Step 5 · Task 6: test-glossary jq-collapse (C7) — count 58 unchanged
- Step 6 · Task 1: author ADR-0017 — stay on homegrown harness, no Bats
- Step 7 · Task 1: test-glossary.sh hermetic — §3 fixture + §13 CI guard
- Step 7 · Task 2: test-setup.sh §15 guard-skip LDS fidelity in CI
- Forge-surface step-01: ADR-0018 — the forge observation surface
- Forge-surface step-02: forge transport seam (gh/glab wrapper)
- Forge-surface step-03: status --probe-forge + forge-unreachable (Lane A)
- Forge-surface step-04: forge observe verb + ship stand-down (Lane B)
- Refine forge transition ownership
- Harden tracker sync transport

## [0.0.14] - 2026-06-28

### Documentation

- Docs(adr-0016): note step-02 direct-splice refinement + skeleton-test retirement

### Internal

- Chore: add AGENTS.md + CLAUDE.md agent onboarding stubs

### Other

- Step 1 · Task 1: author ADR-0016 closeout-write contract
- Step 1 · Task 2: scaffold ship verb skeleton + stubs + harness + tests
- Closeout-write-completion: neutralize writer-coupled skeleton-test cases (shared pre-step for step-02/03 lanes)
- Step 2: implement roadmap shipped-marker writer + lane test
- Step 3: implement active_step clearer + lane test
- Step 4: end-to-end ship composition test
- Step 5: wire ship into the coordinator Step Boundary
- Step 6: shared archived-workplan probe helper
- Step 6: doctor closeout drift check + test
- Step 6: status half-done-closeout hint + test
- Fix closeout next ranking
- Step 1: roadmap parser emits .deferred[] + test
- Step 2: surface deferred as a not-actionable bucket in next/status
- Step 3: prompt lane-opportunity assessment in roadmap authoring
- Step 4: next foreshadows imminent lane parallelism from the prereq
- Step 5: document ledger completion contract + fold it into Step Boundary
- Step 6: doctor --probe-solo flags stale ledger entries for shipped steps
- Fix PR review issues for latent state surfacing

## [0.0.13] - 2026-06-26

### Fixed

- Fix(release): stage the setup-agents manifest in the release commit

### Internal

- Chore: Remove .wip itself from the repo.

## [0.0.11] - 2026-06-25

### Added

- Feat(next): surface "author the roadmap" for a fresh brief
- Feat(orchestration): operator-engagement guard so a human-held agent is never dismissed

### Changed

- Refactor(setup): generate setup-agents commands from the plugin (ADR-0015)

### Documentation

- Docs(orchestration): make the timer-body hold-check authoring requirement explicit

### Fixed

- Fix(orchestration): make engagement guard implementable

## [0.0.10] - 2026-06-23

### Added

- Feat(orchestration): register orchestration-backends initiative
- Feat(orchestration): Task-tool backend + active-backend indirection seam
- Feat(status): opt-in Solo liveness probe + Task-backend fallback offer

### Documentation

- Docs(decisions): ADR-0013/0014 + specs for Task backend & Solo liveness

### Fixed

- Fix(scaffold): escape sed replacement-specials in wip_scaffold_render
- Fix(intake): persist the shaped body on intake apply --kind brief
- Fix(orchestration): harden backend fallback paths

## [0.0.9] - 2026-06-22

### Documentation

- Docs(roadmap): backlog hygiene — no Round 6, demand-driven

## [0.0.8] - 2026-06-20

### Documentation

- Docs(roadmap): open Round 5 — orchestration ergonomics & scaffold fixes
- Docs(roadmap): mark step-20 shipped (orchestration idle-routing guard)
- Docs(roadmap): mark step-21 shipped (LDS scaffold layer-6 naming)
- Docs(roadmap): mark step-22 shipped; close Round 5

### Other

- Step 20 · Task 1: bake the liveness-and-report gate into the Roles docs
- Step 21 · Task 1: rename LDS layer-6 scaffold dir features/ → behaviors/
- Step 22 · Task 1: add resolver fallback ladder to solo.md
- Step 22 · Task 2: document fallback_tool + abstract ladder in tier-policy.md
- Step 22 · Task 3: add features.solo.agent_tier_policy.fallback_tool (Claude) to .wip.yaml
- Step 22 · Task 4: add --agent <name|id> session-pin override to orchestrate + start
- Step 22 · Task 5: detect echoes agent_tier_policy (force_tier + fallback_tool)

## [0.0.7] - 2026-06-19

### Documentation

- Docs(roadmap): open Round 4 (extract polish) and prioritize backlog
- Docs(roadmap): mark step-16 shipped; file lds-scaffold-layer-6 backlog item
- Docs(roadmap): mark step-17 shipped; file idle-routing backlog item
- Docs(roadmap): mark step-18 shipped (extract --verify-hashes)
- Docs(roadmap): mark step-19 shipped; close Round 4

### Other

- Step 16 · Task 1: add LDS glossary partial + inclusion tests
- Step 17 · Task 1: add pure LDS §7 extraction-report renderers
- Step 17 · Task 1: write LDS §7 extraction report to disk
- Step 17 · Task 1: document extraction report in CLI spec
- Step 17 · Task 1: test extraction-report YAML↔ledger reconciliation
- Step 18 · Task 1: pure hash helpers (sha256, source_body, verify)
- Step 18 · Task 1: thread content_hash_check through report renderers
- Step 18 · Task 1: wire --verify-hashes flag + pre-write gate
- Step 18 · Task 1: document extract --verify-hashes in CLI spec
- Step 18 · Task 1: test extract --verify-hashes (match/gate/dry-run)
- Step 19 · Chunk 1: flip extract transform row + document Transform mode (v1)
- Step 19 · Chunk 2: add pure wip_extract_heading_adjust engine helper
- Step 19 · Chunk 3: render + classify + wire transform/heading_adjust
- Step 19 · Chunk 4: transform/heading_adjust tests + step-18 fixture migration

## [0.0.6] - 2026-06-16

### Added

- Feat(orchestrate): add deterministic 'orchestrate prep' plumbing verb
- Feat(orchestrate): add /wip:orchestrate plugin command

### Documentation

- Docs(orchestrate): ADR-0012 + spec — orchestrate entrypoint is a plugin command, not a CLI verb
- Docs(roadmap): amend wip-start-command entry — go is role-aware, orchestrate shipped

### Fixed

- Fix(start): make /wip:start go hand-off role-aware, not silent-solo

## [0.0.5] - 2026-06-16

### Other

- Build: add install.sh, uninstall.sh, and Makefile install targets

## [0.0.4] - 2026-06-16

### Internal

- Roadmap: fix greedy **…** match swallowing step titles (+ ship marker)

### Other

- Hooks: drop the per-commit full-test-suite gate (keep lint + hygiene)
- Start: spec for /wip:start + workplan init --activate (plan)
- Hooks: sync hygiene template after dropping the per-commit test hook
- Start: workplan init --activate + /wip:start command
- Start: mark wip-start-command shipped 2026-06-16

## [0.0.3] - 2026-06-16

### Added

- Bundle: ADR-0011 + spec for the multi-file bundle assembler (plan)
- Bundle: wip bundle assembler verb + /wip:bundle command + shared prompt
- Bundle: accept ADR-0011, mark bundle-assembler shipped 2026-06-16

### Other

- Plugin: resolve bundled wip-plumbing via CLAUDE_PLUGIN_ROOT

## [0.0.2] - 2026-06-16

### Added

- Bundle: sixth intake kind — explode lead + children into lanes (ADR-0009 + ADR-0010)
- Bundle: address PR review — slug propagation, isil hint, target-round mismatch

### Other

- Plugin: move commands/ + agents/ to the plugin root so they register

## [0.0.1] - 2026-06-16

### Added

- Step-05: wip-plumbing CLI contract spec
- Step-06: wip-plumbing detect + doctor
- Step-06.5: ADR-0008 + registry spec + workplan, roadmap slot
- Step-06.5: implement global project registry + --project
- Step-07: init + intake validate v0
- Step-07.5: intake pipeline — classify / per-kind validate / apply
- Step-08: status + next — the headline Round 2 value
- Step-08.5: roadmap amend + workplan init (intake pipeline complete)
- Step-09: repo baseline (flake / direnv / pre-commit)
- Step-10: wip porcelain — provider wiring over wip-plumbing
- Step-10.5: wip intake porcelain — LLM shaper + router on plumbing seam
- Step-11: /wip:* plugin — shared shaper prompts via wip-plumbing template
- Step-12: roles set — backend-agnostic Roles + Solo binding seam
- Step-13: wip glossary assembler — layered partials → .wip/GLOSSARY.md
- Step-14: wip setup family — five install-time scaffold verbs
- Step-15: graduate / extract — the LDS seam (Round 3 closes)
- Lanes: first-class parallel lanes in roadmaps (ADR-0010)
- Lanes: address review (append-lane ordering, dup guard, parse shape)

### Changed

- Followup: setup-lds-verb — the sixth `setup` verb

### Internal

- Roadmap: mark step-06.5 shipped 2026-06-13
- Roadmap: small hygiene pass

### Other

- Initial wip distillation: canonical glossary, manifest, and findings
- Shape the repo: glossary layering, LDS decisions, skeleton, roadmap
- Decouple orchestration (Roles) from the Solo backend
- Intake as pipeline: ADR-0009, intake-kinds spec, roadmap reshape
