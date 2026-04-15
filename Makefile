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

.PHONY: init install dev bump generate-docs release fmt lint test vet

init:
	go version >/dev/null
	@echo "Go toolchain ok. Run 'make install' to fetch dependencies."

install:
	go mod download

dev:
	mkdir -p dist
	go build -trimpath -o dist/$(BIN) $(PKG)

bump:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make bump VERSION=x.y.z"; exit 2; fi
	sed -i "s/^var version = \".*\"/var version = \"$(VERSION)\"/" cmd/scrapfly/root.go
	git add cmd/scrapfly/root.go
	git commit -m "bump version to $(VERSION)"
	git push

generate-docs:
	@mkdir -p docs/reference
	go doc -all ./... > docs/reference/go-reference.txt

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
	git push --tags
	@if [ -n "$(NEXT_VERSION)" ]; then $(MAKE) bump VERSION=$(NEXT_VERSION); fi

fmt:
	gofmt -w .

lint: vet

vet:
	go vet ./...

test:
	go test ./...
