SHELL := /bin/bash
GO ?= go
BIN := bin/bnb
WEB_DIST := server/internal/web/dist

## The version's patch number is the repository's commit count, which a compiled
## binary can't ask git for — scripts/version.mjs works it out and the linker
## stamps it in. Empty (no node, no git, a shallow clone) leaves the Go package's
## default of 0, which reads as "unstamped build" rather than as a release.
VERSION_PKG := github.com/chinmay28/bulls-and-bears/server/internal/version
PATCH := $(shell node scripts/version.mjs --patch 2>/dev/null)
LDFLAGS := -s -w $(if $(PATCH),-X $(VERSION_PKG).Patch=$(PATCH))

.PHONY: build server web icons test test-web test-icongen test-installer test-research vet lint run clean version bump-version golden parity

## build: PWA into the embed directory, then the single binary
build: web server

server: | $(WEB_DIST)/index.html
	cd server && $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BIN) ./cmd/bnb

## version: print the version this tree would build as
version:
	@node scripts/version.mjs

## bump-version: move the version line to this month (UTC) — the first commit
## on a branch does this, so a release says when its work began
bump-version:
	@node scripts/version.mjs --bump

## The Go binary embeds $(WEB_DIST), so it needs to exist even when only the
## server is being built. This placeholder is replaced by the real PWA.
$(WEB_DIST)/index.html:
	@mkdir -p $(WEB_DIST)
	@printf '<!doctype html>\n<meta charset="utf-8">\n<title>Bulls and Bears</title>\n<p>Web UI not built. Run <code>make build</code>.</p>\n' > $@

## web: build the PWA straight into the server's embed directory
web:
	cd apps/web && npm ci && npm run build

## icons: cut the PNG app icons from art/bulls-and-bears-logo.png (run after the logo changes)
icons:
	cd tools/icongen && $(GO) run . -in ../../art/bulls-and-bears-logo.png -out ../../apps/web/public

test:
	cd server && $(GO) test -race ./...

## test-icongen: the icon generator's own tests (it is a separate Go module)
test-icongen:
	cd tools/icongen && $(GO) test ./...

## test-web: the PWA's unit tests (needs node; installs apps/web's dependencies)
test-web:
	cd apps/web && npm ci && npm test

## test-installer: install, upgrade, rollback and uninstall in a sandbox (needs root)
test-installer:
	./scripts/test-quickstart.sh

## test-research: the Python side (needs uv)
test-research:
	cd research && uv run pytest

vet:
	cd server && $(GO) vet ./...

lint:
	cd server && golangci-lint run ./...

## run: build and start the server with verbose logging, data in ./data
run: server
	./$(BIN) -v

clean:
	rm -rf bin
