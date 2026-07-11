SHELL := bash

SRC := bin/wip \
       bin/wip-plumbing \
       install.sh \
       uninstall.sh \
       $(wildcard lib/wip/*.bash) \
       $(wildcard lib/wip/wip-plumbing-subcommands/*.bash) \
       $(wildcard lib/wip/wip-subcommands/*.bash) \
       $(wildcard lib/wip/tracker-backends/*.bash)
TESTS := $(wildcard test/test-*.sh)

.PHONY: fmt lint test check deps-check hooks glossary active agents-commands install install-local uninstall uninstall-local

fmt:
	shfmt -w -i 2 -ci $(SRC) test/*.sh test/run

lint:
	shfmt -d -i 2 -ci $(SRC) test/*.sh test/run
	shellcheck -x $(SRC) test/*.sh test/run

test:
	@bash test/run

check: lint test

deps-check:
	@for d in bash jq yq git curl; do \
	  command -v $$d >/dev/null || { echo "missing dependency: $$d" >&2; exit 1; }; \
	done
	@echo "deps ok: bash jq yq git curl"

hooks:
	pre-commit install

install:
	./install.sh

install-local:
	./install.sh ~/.local

uninstall:
	./uninstall.sh

uninstall-local:
	./uninstall.sh ~/.local

glossary:
	bin/wip-plumbing glossary assemble > .wip/GLOSSARY.md

# Regenerate roles/backends/active.md from the configured backend (the
# indirection seam — ADR-0013). active.md is committed but generated.
active:
	bin/wip-plumbing orchestrate backend "$$(yq -r '.features.orchestration.backend // "solo"' .wip.yaml)"

# Regenerate templates/setup/agents/commands/*.md from the canonical plugin
# commands/*.md (ADR-0015). Committed but generated; `--check` gates drift.
agents-commands:
	contrib/sync-agents-commands
