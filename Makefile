# Ogham CLI development tasks

.PHONY: build test test-race lint check clean cross-compile snapshot tag release-check cover cover-html bench pict-regen live

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
COVER_STRICT_PKGS := ./internal/native/extraction/... ./internal/native/cache/...
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

# Live embedder smoke tests. Build-tagged (`live`) so they run only
# when explicitly opted into. Each test self-skips when its
# preconditions aren't met (API key missing, local server down).
# CI never runs these -- they require credentials / a running Ollama.
#
# Overrides:
#   OLLAMA_URL                host for the Ollama probe + live call
#   OGHAM_LIVE_OLLAMA_MODEL   e.g. embeddinggemma, nomic-embed-text
#   OGHAM_LIVE_OLLAMA_DIM     e.g. 512 (MRL truncation) or 768 (native)
#   OPENAI_API_KEY            enables the OpenAI live test
#   OGHAM_LIVE_OPENAI_MODEL   e.g. text-embedding-3-small
#   OGHAM_LIVE_OPENAI_DIM     e.g. 512
#   OPENAI_BASE_URL           Azure OpenAI / LocalAI override
live:
	go test -tags live -v -run Live ./internal/native/

# Run the pgcontainer-tagged tests -- real pgvector via testcontainers-go.
# Requires a Docker daemon; this target auto-detects OrbStack on macOS.
# Tests that would otherwise need a live Supabase/Neon DB run here
# against a disposable pgvector:pg17 container with the schema bootstrapped
# from internal/native/testdata/schema_postgres.sql.
pgcontainer:
	@DOCKER_HOST=$$(test -S ~/.orbstack/run/docker.sock && echo unix://$$HOME/.orbstack/run/docker.sock || echo unix:///var/run/docker.sock) \
	go test -tags pgcontainer -v -run TestPG_ ./internal/native/

# Coverage number including the pgcontainer path. Slow (~30s boot) but
# this is the number that counts against the 90% locked gate for
# internal/native/.
coverage-full:
	@DOCKER_HOST=$$(test -S ~/.orbstack/run/docker.sock && echo unix://$$HOME/.orbstack/run/docker.sock || echo unix:///var/run/docker.sock) \
	go test -tags pgcontainer -cover ./internal/native/

# Refresh the vendored schema from the R&D repo. Run when the canonical
# schema_postgres.sql in openbrain-sharedmemory has changed; the pg
# tests key off the copy in testdata/.
sync-schema:
	@test -f ../openbrain-sharedmemory/sql/schema_postgres.sql \
	  || { echo "openbrain-sharedmemory/sql/schema_postgres.sql not found"; exit 1; }
	cp ../openbrain-sharedmemory/sql/schema_postgres.sql \
	   internal/native/testdata/schema_postgres.sql
	@echo "schema refreshed; rerun 'make pgcontainer' to verify tests still pass"

# Regenerate PICT matrices from .pict source files. CI runs this and
# fails if the committed .tsv drifts from a fresh generation.
pict-regen:
	@which pict > /dev/null || { \
	    echo "pict not installed -- brew install pict"; exit 1; }
	# Loop over every .pict model under internal/native/**/testdata,
	# regenerate its .tsv, canonical-sort the body (keep header) so
	# the committed matrix is version- and platform-independent.
	# /r:1 pins the PRNG seed across PICT builds.
	@for pf in $$(find internal/native -path '*/testdata/*.pict' -type f); do \
	    tmp=$$(mktemp); \
	    pict "$$pf" /r:1 > "$$tmp"; \
	    { head -n 1 "$$tmp"; tail -n +2 "$$tmp" | LC_ALL=C sort; } > "$$pf.tsv"; \
	    rm -f "$$tmp"; \
	    echo "regenerated $$(basename $$pf).tsv (canonical-sorted)"; \
	done

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
