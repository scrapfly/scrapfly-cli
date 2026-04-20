# Scrapfly CLI — release/dev Makefile.
# Target names mirror sdk/python/Makefile and sdk/rust/Makefile for parity.
#
# Releases are goreleaser-driven (.goreleaser.yaml and
# .goreleaser-linux-windows.yaml). `make release` tags + pushes; the
# `release` GitHub Actions workflow publishes binaries.

VERSION ?=
NEXT_VERSION ?=

BIN := scrapfly
PKG := ./cmd/scrapfly

# Path to the monorepo go-scrapfly SDK, used by `make dev-local` to
# build a CLI binary that consumes the working-tree SDK instead of
# the pinned published release. Override if the CLI lives outside
# the monorepo.
SDK_LOCAL ?= $(abspath $(CURDIR)/../../sdk/go)

.PHONY: init install dev dev-local bump generate-docs release fmt lint test vet

init:
	go version >/dev/null
	@echo "Go toolchain ok. Run 'make install' to fetch dependencies."

install:
	go mod download

dev:
	mkdir -p dist
	go build -trimpath -o dist/$(BIN) $(PKG)

# dev-local builds dist/$(BIN) against the monorepo working-tree
# go-scrapfly SDK, then restores go.mod to its release state so
# the repo stays clean for downstream `make release` / CI runs.
# Pattern mirrors apps/scrapfly/mcp-cloud/.air.toml's build cmd.
dev-local:
	@mkdir -p dist
	@echo "[dev-local] replacing go-scrapfly -> $(SDK_LOCAL)"
	go mod edit -replace=github.com/scrapfly/go-scrapfly=$(SDK_LOCAL)
	go mod tidy
	$(MAKE) _dev-local-build; status=$$?; \
	echo "[dev-local] dropping replace directive"; \
	go mod edit -dropreplace=github.com/scrapfly/go-scrapfly; \
	go mod tidy; \
	exit $$status

_dev-local-build:
	go build -trimpath -o dist/$(BIN) $(PKG)
	@echo "[dev-local] built dist/$(BIN)"

bump:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make bump VERSION=x.y.z"; exit 2; fi
	@# Match both top-level `var version = "..."` and indented `version = "..."`
	@# inside a `var ( ... )` block. The previous `^var version` anchor silently
	@# stopped matching after the var block was introduced in 583b4cf.
	sed -i -E 's/^([[:space:]]*(var )?version[[:space:]]*=[[:space:]]*")[^"]*(")/\1$(VERSION)\3/' cmd/scrapfly/root.go
	@# Fail loudly if the edit didn't actually change the file — catches future
	@# refactors of root.go that break the pattern again.
	git diff --quiet cmd/scrapfly/root.go && { echo "bump: sed did not update version; check root.go layout"; exit 1; } || true
	git add cmd/scrapfly/root.go
	git commit -m "bump version to $(VERSION)"
	git push

generate-docs:
	@mkdir -p docs/reference
	@# go doc doesn't accept `./...`; iterate through `go list` instead.
	@: > docs/reference/go-reference.txt
	@for pkg in $$(go list ./...); do \
		echo "=== $$pkg ===" >> docs/reference/go-reference.txt; \
		go doc -all "$$pkg" >> docs/reference/go-reference.txt 2>/dev/null || true; \
		echo >> docs/reference/go-reference.txt; \
	done

release:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=x.y.z [NEXT_VERSION=x.y.(z+1)]"; exit 2; fi
	git branch | grep \* | cut -d ' ' -f2 | grep main || exit 1
	git pull origin main
	$(MAKE) test
	$(MAKE) generate-docs
	-git add docs
	-git commit -m "Update API reference for version $(VERSION)"
	-git push origin main
	git tag -a v$(VERSION) -m "Version $(VERSION)"
	@# Push ONLY the new tag, not `--tags`. The previous form pushed every stale
	@# local tag (e.g. dangling v0.3.1) and triggered old, broken workflows; it
	@# also failed non-FF on the moving `latest` tag, aborting the bump step.
	git push origin v$(VERSION)
	@if [ -n "$(NEXT_VERSION)" ]; then $(MAKE) bump VERSION=$(NEXT_VERSION); fi

fmt:
	gofmt -w .

lint: vet

vet:
	go vet ./...

test:
	@# Run tests against the local go-scrapfly SDK (same pattern as dev-local
	# so unit tests can reference the new Classify/ScrapeBatchWithOptions
	# surface without waiting for an SDK release). go.mod is restored after.
	@echo "[test] replacing go-scrapfly -> $(SDK_LOCAL)"
	go mod edit -replace=github.com/scrapfly/go-scrapfly=$(SDK_LOCAL)
	go mod tidy
	go test ./...; status=$$?; \
	echo "[test] dropping replace directive"; \
	go mod edit -dropreplace=github.com/scrapfly/go-scrapfly; \
	go mod tidy; \
	exit $$status
