SHELL := bash

SRC := bin/wip \
       bin/wip-plumbing \
       install.sh \
       uninstall.sh \
       $(wildcard lib/wip/*.bash) \
       $(wildcard lib/wip/wip-plumbing-subcommands/*.bash) \
       $(wildcard lib/wip/wip-subcommands/*.bash)
TESTS := $(wildcard test/test-*.sh)

.PHONY: fmt lint test check deps-check hooks glossary install install-local uninstall uninstall-local

fmt:
	shfmt -w -i 2 -ci $(SRC) test/*.sh

lint:
	shfmt -d -i 2 -ci $(SRC) test/*.sh
	shellcheck -x $(SRC) test/*.sh

test:
	@for t in $(TESTS); do echo "== $$t =="; bash "$$t" || exit 1; done

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
