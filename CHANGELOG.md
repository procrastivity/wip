# Changelog

All notable changes to this project are documented here.

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
