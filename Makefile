SHELL := bash

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: fmt lint test check hooks build cross-compile

fmt:
	gofumpt -w .

lint:
	golangci-lint run

test:
	go test ./...

check: lint test

hooks:
	pre-commit install

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
