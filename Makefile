# Ogham CLI development tasks

.PHONY: build test lint check clean cross-compile

VERSION ?= dev

# Build binary
build:
	go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o ogham .

# Run all tests
test:
	go test ./... -v

# Lint
lint:
	go vet ./...
	@gofmt -l . | grep . && echo "FAIL: files need gofmt" && exit 1 || true

# Pre-commit checks
check: lint test

# Clean build artifacts
clean:
	rm -f ogham

# Cross-compile for all platforms
cross-compile:
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-darwin-amd64 .
	GOOS=linux GOARCH=amd64 go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-linux-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=$(VERSION)" -o dist/ogham-windows-amd64.exe .
