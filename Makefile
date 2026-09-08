# Scrapfly CLI — release/dev Makefile.
# Target names mirror the other official Scrapfly SDK repos for parity.
#
# Releases are goreleaser-driven (.goreleaser.yaml and
# .goreleaser-linux-windows.yaml). `make release` tags + pushes; the
# `release` GitHub Actions workflow publishes binaries.

VERSION ?=
NEXT_VERSION ?=

BIN := scrapfly
PKG := ./cmd/scrapfly

# Path to a local go-scrapfly SDK checkout, used by `make dev-local` to
# build a CLI binary that consumes that working tree instead of the pinned
# published release. Defaults to a sibling checkout; override for any other
# layout, e.g. `make dev-local SDK_LOCAL=/path/to/go-scrapfly`.
SDK_LOCAL ?= $(abspath $(CURDIR)/../go-scrapfly)

.PHONY: init install dev dev-local bump generate-docs check-no-replace release fmt lint test vet

init:
	go version >/dev/null
	@echo "Go toolchain ok. Run 'make install' to fetch dependencies."

install:
	go mod download

dev:
	mkdir -p dist
	go build -trimpath -o dist/$(BIN) $(PKG)

# dev-local builds dist/$(BIN) against the local working-tree
# go-scrapfly SDK, then restores go.mod to its release state so
# the repo stays clean for downstream `make release` / CI runs.
dev-local:
	@mkdir -p dist
	@# Refuse before editing go.mod: a missing checkout fails `go mod tidy`
	@# mid-recipe, which skips the restore below and leaves go.mod dirty with a
	@# replace directive that check-no-replace then rejects at release time.
	@[ -f "$(SDK_LOCAL)/go.mod" ] || { echo "dev-local: no go.mod under $(SDK_LOCAL); pass SDK_LOCAL=/path/to/go-scrapfly"; exit 2; }
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
	@# The npm wrapper carries its own version. release.yml pins it to the tag
	@# before publishing, so the tarball is always right, but install.js falls
	@# back to it (`v$${pkg.version}`) when the wrapper runs from a checkout, so
	@# a stale value there downloads the wrong release assets. It sat at 0.2.0
	@# for eleven releases because only root.go was bumped here.
	sed -i -E 's/^([[:space:]]*"version"[[:space:]]*:[[:space:]]*")[^"]*(")/\1$(VERSION)\2/' packages/npm/package.json
	@# Assert the result rather than that the file changed: a bump re-run for a
	@# version a file already carries is not an error, a pattern that stopped
	@# matching after a refactor is.
	@grep -qE '^[[:space:]]*(var )?version[[:space:]]*=[[:space:]]*"$(VERSION)"' cmd/scrapfly/root.go || { echo "bump: version not set in cmd/scrapfly/root.go; check its layout"; exit 1; }
	@grep -qE '^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"$(VERSION)"' packages/npm/package.json || { echo "bump: version not set in packages/npm/package.json; check its layout"; exit 1; }
	git add cmd/scrapfly/root.go packages/npm/package.json
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

# `go install <pkg>@version` refuses any module whose go.mod carries a replace
# directive, and a relative path resolves only on the machine that set it.
# Tagging with one live turns the documented from-source install into
# a hard failure, so block the tag rather than discover it after publishing.
check-no-replace:
	@grep -qE '^[[:space:]]*replace[[:space:]]' go.mod && { \
		echo "release: go.mod carries a replace directive; tag the dependency and drop it before releasing"; \
		grep -nE '^[[:space:]]*replace[[:space:]]' go.mod; \
		exit 1; \
	} || true

release: check-no-replace
	@if [ -z "$(VERSION)" ]; then echo "Usage: make release VERSION=x.y.z [NEXT_VERSION=x.y.(z+1)]"; exit 2; fi
	@# Branch guard via rev-parse: the old pipe through grep for the
	@# current-branch marker errors under ugrep (empty subexpression).
	@[ "$$(git rev-parse --abbrev-ref HEAD)" = main ] || exit 1
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

# Tests run against the go-scrapfly release pinned in go.mod, like CI and like
# every consumer. The local-SDK round trip this target used to perform died
# with 9caa6a6 (the required surface is published): it needed a sibling
# checkout nobody has, and a failure anywhere in it skipped the restore lines
# and left go.mod carrying a replace directive, which then tripped
# check-no-replace on the next release. Use dev-local for local-SDK work.
test:
	go test ./...
