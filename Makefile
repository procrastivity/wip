SHELL := bash

# --match keeps the stamp inside the release tag namespace, the same one
# cliff.toml selects with tag_pattern. Without it `git describe` also picks
# up milestone tags like `phase-1`, and a build stamps `phase-1-10-g171ee47`
# — a version string that matches no release.
VERSION := $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Every shell script the repo carries. wip's pre-commit hooks check no
# shell files at all, so this list is the only shell gate — `make lint`,
# and so CI's lint job, checks it in full.
ORB_SHELLCHECK_FILES := .agents/setup .agents/resume contrib/orb-smoke/entrypoint contrib/orb-smoke/run contrib/orb-smoke/test-policy contrib/online-authority-proof/run contrib/online-authority-proof/runtime-test contrib/online-authority-proof/test-policy
SHELLCHECK_FILES := .envrc contrib/check-commit-msg contrib/check-gofumpt contrib/release scripts/install.sh scripts/prepare-step10-verifier-inputs.sh $(ORB_SHELLCHECK_FILES)

.PHONY: fmt lint test check prepare-step10-verifier-inputs step10-verifier-inputs-local orb-lint orb-policy-test orb-smoke authority-lab-policy-test authority-lab-config authority-lab-runtime-test hooks build cross-compile changelog release-notes

fmt:
	gofumpt -w .

lint:
	shellcheck $(SHELLCHECK_FILES)
	golangci-lint run

test:
	go test ./...

# The process acceptance consumes only a fresh, hash-checked directory. The
# callers acquire the exact external commits first; this target never fetches
# or consults credentials.
prepare-step10-verifier-inputs:
	@test -n "$(MODEL_SOURCE_DIR)" || { echo "error: MODEL_SOURCE_DIR is required" >&2; exit 1; }
	@test -n "$(WORKPLAN_SOURCE_DIR)" || { echo "error: WORKPLAN_SOURCE_DIR is required" >&2; exit 1; }
	@test -n "$(STEP10_INPUT_DIR)" || { echo "error: STEP10_INPUT_DIR is required" >&2; exit 1; }
	@scripts/prepare-step10-verifier-inputs.sh "$(MODEL_SOURCE_DIR)" "$(WORKPLAN_SOURCE_DIR)" "$(STEP10_INPUT_DIR)"

# Local twin of CI's Step 10 input acquisition. It clones the two pinned
# commits out of a local wip-reboot checkout (no network, no credentials)
# and prepares them with the same validating script CI uses. The pins are
# read from that script, so this target does not repeat them.
WIP_REBOOT_DIR ?= ../wip-reboot
STEP10_LOCAL_DIR := $(CURDIR)/.step10-local

step10-verifier-inputs-local:
	@git -C "$(WIP_REBOOT_DIR)" rev-parse --git-dir >/dev/null 2>&1 || { echo "error: WIP_REBOOT_DIR=$(WIP_REBOOT_DIR) is not a wip-reboot checkout" >&2; exit 1; }
	@rm -rf "$(STEP10_LOCAL_DIR)"
	@mkdir -p "$(STEP10_LOCAL_DIR)"
	@for source in model workplan; do \
		commit=$$(sed -n "s/^readonly $${source}_commit='\([0-9a-f]*\)'$$/\1/p" scripts/prepare-step10-verifier-inputs.sh); \
		git clone -q --no-checkout "$(WIP_REBOOT_DIR)" "$(STEP10_LOCAL_DIR)/$$source" && \
		git -C "$(STEP10_LOCAL_DIR)/$$source" checkout -q --detach "$$commit" || \
		{ echo "error: $(WIP_REBOOT_DIR) lacks pinned $$source commit $$commit; fetch it, then retry" >&2; exit 1; }; \
	done
	@scripts/prepare-step10-verifier-inputs.sh "$(STEP10_LOCAL_DIR)/model" "$(STEP10_LOCAL_DIR)/workplan" "$(STEP10_LOCAL_DIR)/inputs"
	@echo
	@echo "Run the full suite with:"
	@echo "  GOFLAGS='-timeout=45m -count=1' WIPD_STEP10_VERIFIER_INPUTS=$(STEP10_LOCAL_DIR)/inputs make check"

# `test` includes the Step 10 process acceptance, which needs pinned inputs
# (step10-verifier-inputs-local), GOFLAGS='-timeout=45m -count=1', and a
# host that permits the bubblewrap sandbox. It fails at once and names the
# missing one; docs/step10-verifier-test-inputs.md has the details.
check: lint test

orb-lint:
	shellcheck $(ORB_SHELLCHECK_FILES)

orb-policy-test: orb-lint
	./contrib/orb-smoke/test-policy

orb-smoke: orb-policy-test
	./contrib/orb-smoke/run

authority-lab-policy-test: orb-lint
	./contrib/online-authority-proof/test-policy

authority-lab-config: authority-lab-policy-test
	./contrib/online-authority-proof/run config

authority-lab-runtime-test: authority-lab-policy-test
	./contrib/online-authority-proof/runtime-test

# Both stages explicitly: .pre-commit-config.yaml declares a commit-msg
# hook, and a bare `pre-commit install` wires only the pre-commit stage,
# leaving the conventional-commit check configured but never firing.
hooks:
	pre-commit install --hook-type pre-commit --hook-type commit-msg

# CGO_ENABLED=0 everywhere in this file (and in CI, and in the Nix package) is
# load-bearing, not a default we happened to keep: it's what makes the
# single-static-binary/cross-compile promise (packaging commitment 1) hold
# regardless of later dependency choices. In particular it constrains
# store-fork/schema's eventual SQLite driver to a pure-Go implementation
# (e.g. modernc.org/sqlite), never mattn/go-sqlite3.
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/wip ./cmd/wip

cross-compile:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/wip-darwin-arm64 ./cmd/wip
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/wip-linux-amd64  ./cmd/wip

# CHANGELOG.md is generated per-release, never committed (step-01 finding
# (d)): the tag is the only human action, and git-cliff derives everything
# else from the conventional-commit history. `changelog` writes the full
# history for attachment as a release asset; `release-notes` writes just the
# slice belonging to TAG, header-stripped, for the GitHub release body.
changelog:
	mkdir -p dist
	git-cliff --output dist/CHANGELOG.md

# TAG may name a tag that already exists (CI, where the push of the tag is
# what started us) or one about to be cut (a local dry run before tagging).
# Those need different git-cliff selections, so probe for the ref first.
release-notes:
	@test -n "$(TAG)" || { echo "error: TAG is required, e.g. make release-notes TAG=v0.1.0" >&2; exit 1; }
	@mkdir -p dist
	@if git rev-parse -q --verify "refs/tags/$(TAG)" >/dev/null; then \
		git-cliff --current --strip header --output dist/RELEASE_NOTES.md; \
	else \
		git-cliff --unreleased --tag "$(TAG)" --strip header --output dist/RELEASE_NOTES.md; \
	fi
