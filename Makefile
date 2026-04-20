# Ogham CLI development tasks

.PHONY: build test lint check clean cross-compile snapshot tag release-check

VERSION ?= dev

# Build binary
build:
	go build -ldflags "-s -w -X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o ogham .

# Run all tests
test:
	go test ./... -v

# Lint (gosec + errcheck + govet + staticcheck via golangci-lint)
lint:
	golangci-lint run ./...

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

# Pre-tag safety gate. Fails fast if the working tree is dirty or
# the target version isn't set.
release-check:
	@if [ -z "$(VERSION)" ] || [ "$(VERSION)" = "dev" ]; then \
		echo "ERROR: pass VERSION=vX.Y.Z"; exit 1; fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "ERROR: working tree is dirty"; git status --short; exit 1; fi
	@echo "OK: tree clean, version set to $(VERSION)"

# Tag + push. Triggers the GitHub Actions release workflow, which
# runs GoReleaser and publishes archives + checksums to the Releases
# page. Homebrew tap is deferred -- see .goreleaser.yml brews stanza.
#
# Usage:  make tag VERSION=v0.4.0
tag: release-check
	git tag -a $(VERSION) -m "$(VERSION)"
	git push origin $(VERSION)
	@echo "Tag $(VERSION) pushed. Watch: https://github.com/ogham-mcp/ogham-cli/actions"
