# Ogham CLI development tasks

.PHONY: build test test-race lint check clean cross-compile snapshot tag release-check cover cover-html bench pict-regen

VERSION ?= dev

# Build binary
build:
	go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o ogham .

# Run all tests
test:
	go test ./... -v

# Run all tests with the race detector enabled. Slower (~2x) but catches
# data races that only surface under concurrent load.
test-race:
	go test -race ./...

# Lint (gosec + errcheck + govet + staticcheck via golangci-lint)
lint:
	golangci-lint run ./...

# Coverage measurement + 90% threshold gate, currently scoped to the
# packages that have completed the PICT-backed test sweep. As each new
# native package lands its coverage retrofit, add it to COVER_STRICT_PKGS.
# Broader internal/native/ coverage debt is tracked separately -- see
# docs/tracking04plus.md under "Testing standards".
NATIVE_COVER_MIN := 90.0
COVER_STRICT_PKGS := ./internal/native/extraction/...
cover:
	@go test -race -coverprofile=cover.out $(COVER_STRICT_PKGS) > /dev/null
	@total=$$(go tool cover -func=cover.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	echo "coverage: strict-pkgs=$${total}%"; \
	awk -v p="$$total" -v m="$(NATIVE_COVER_MIN)" \
	    'BEGIN { exit (p+0 < m+0) }' || \
	    { echo "FAIL: strict-pkgs coverage $${total}% below threshold $(NATIVE_COVER_MIN)%"; exit 1; }

# HTML report for local exploration. Writes cover.html in the repo root.
cover-html: cover
	go tool cover -html=cover.out -o cover.html
	@echo "open cover.html"

# Run benchmarks for the hot paths. -benchmem reports allocs/op so we
# can see when a change trades cpu for heap pressure.
bench:
	go test -bench=. -benchmem -run=^$$ ./internal/native/...

# Regenerate PICT matrices from .pict source files. CI runs this and
# fails if the committed .tsv drifts from a fresh generation.
pict-regen:
	@which pict > /dev/null || { \
	    echo "pict not installed -- brew install pict"; exit 1; }
	# PICT versions (homebrew vs built-from-source) emit rows in
	# different orders even with /r:1. Sort the body (keep header) so
	# the committed matrix is canonical regardless of PICT version and
	# platform.
	@tmp=$$(mktemp); \
	pict internal/native/extraction/testdata/entities.pict /r:1 > "$$tmp"; \
	{ head -n 1 "$$tmp"; tail -n +2 "$$tmp" | LC_ALL=C sort; } \
	    > internal/native/extraction/testdata/entities.pict.tsv; \
	rm -f "$$tmp"
	@echo "regenerated entities.pict.tsv (canonical-sorted)"

# Pre-commit checks
check: lint test

# Clean build artifacts
clean:
	rm -f ogham
	rm -rf dist/

# Cross-compile for all platforms
cross-compile:
	GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-darwin-amd64 .
	GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-linux-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-windows-amd64.exe .

# Dry-run the GoReleaser pipeline locally. Produces dist/ with all
# archives + checksums without publishing. Use before tagging to
# confirm the config is healthy.
snapshot:
	GORELEASER_TAP_TOKEN=dummy goreleaser release --snapshot --clean

# Pre-tag safety gate. Fails fast if tracked files have uncommitted
# changes or the target version isn't set. Untracked files (local
# notes, tracking docs) are tolerated.
release-check:
	@if [ -z "$(VERSION)" ] || [ "$(VERSION)" = "dev" ]; then \
		echo "ERROR: pass VERSION=vX.Y.Z"; exit 1; fi
	@if ! git diff-index --quiet HEAD --; then \
		echo "ERROR: tracked files have uncommitted changes"; \
		git diff-index --name-status HEAD --; exit 1; fi
	@echo "OK: tracked tree clean, version set to $(VERSION)"

# Tag + push. Triggers the GitHub Actions release workflow, which
# runs GoReleaser and publishes archives + checksums to the Releases
# page. Homebrew tap is deferred -- see .goreleaser.yml brews stanza.
#
# Usage:  make tag VERSION=v0.4.0
tag: release-check
	git tag -a $(VERSION) -m "$(VERSION)"
	git push origin $(VERSION)
	@echo "Tag $(VERSION) pushed. Watch: https://github.com/ogham-mcp/ogham-cli/actions"
