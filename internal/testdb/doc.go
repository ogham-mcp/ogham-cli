// Package testdb provides a shared test-only pgvector container fixture.
//
// All implementation lives in files guarded by the `pgcontainer` build tag;
// this stub exists so `go vet ./...` finds at least one buildable file when
// the tag is not set (pre-commit hook compatibility).
package testdb
