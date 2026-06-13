SHELL := bash

SRC := bin/wip-plumbing \
       $(wildcard lib/wip/*.bash) \
       $(wildcard lib/wip/wip-plumbing-subcommands/*.bash)
TESTS := $(wildcard test/test-*.sh)

.PHONY: fmt lint test check deps-check

fmt:
	shfmt -w -i 2 -ci $(SRC) test/*.sh

lint:
	shfmt -d -i 2 -ci $(SRC) test/*.sh
	shellcheck -x $(SRC) test/*.sh

test:
	@for t in $(TESTS); do echo "== $$t =="; bash "$$t" || exit 1; done

check: lint test

deps-check:
	@for d in bash jq yq git; do \
	  command -v $$d >/dev/null || { echo "missing dependency: $$d" >&2; exit 1; }; \
	done
	@echo "deps ok: bash jq yq git"
