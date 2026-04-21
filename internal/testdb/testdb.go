//go:build pgcontainer

// Package testdb is the shared test-only pgvector fixture. Exists so
// internal/native and internal/mcp can both spin up a real postgres
// without duplicating the 80 LOC of container boot + schema load.
//
// Only compiled under -tags pgcontainer so the default hermetic
// `go test ./...` stays Docker-free. Not a production path -- don't
// import this from non-test code.
package testdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	bootOnce sync.Once
	bootDSN  string
	bootErr  error
)

// DSN returns the connection string for the shared pgvector container,
// lazily starting it on first call. Schema is already bootstrapped.
func DSN(t *testing.T) string {
	t.Helper()
	bootOnce.Do(boot)
	if bootErr != nil {
		t.Fatalf("testdb: container boot: %v", bootErr)
	}
	return bootDSN
}

func boot() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schemaPath := findSchema()
	if schemaPath == "" {
		bootErr = fmt.Errorf("could not locate schema_postgres.sql (searched for internal/native/extraction/testdata + internal/native/testdata)")
		return
	}

	container, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:pg17",
		tcpostgres.WithDatabase("ogham_test"),
		tcpostgres.WithUsername("ogham"),
		tcpostgres.WithPassword("ogham"),
		tcpostgres.WithInitScripts(schemaPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(45*time.Second),
		),
	)
	if err != nil {
		bootErr = fmt.Errorf("launch pgvector: %w", err)
		return
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		bootErr = fmt.Errorf("connection string: %w", err)
		return
	}
	bootDSN = dsn
}

// findSchema walks up from the test cwd looking for the vendored
// schema at internal/native/testdata/schema_postgres.sql. Callers from
// either internal/native or internal/mcp both cover the same file via
// this lookup.
func findSchema() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for dir := wd; dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "internal", "native", "testdata", "schema_postgres.sql")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// Same check but from within internal/native/ the walk bottoms
		// out one directory up; handle the alternate root.
		candidate2 := filepath.Join(dir, "testdata", "schema_postgres.sql")
		if _, err := os.Stat(candidate2); err == nil {
			return candidate2
		}
	}
	return ""
}

// Reset truncates every table the shared tests touch so each test can
// start from a known-empty state. Safe because the container is
// dedicated to this test binary.
func Reset(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("testdb reset connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx,
		"TRUNCATE memories, memory_relationships, audit_log, profile_settings CASCADE")
	if err != nil {
		t.Fatalf("testdb reset truncate: %v", err)
	}
}

// InsertMemory seeds a single row directly. Bypasses the Store
// orchestrator -- tests use this to set up fixtures for delete/list/
// search flows without needing an embedder stub.
func InsertMemory(t *testing.T, dsn, profile, content string, tags []string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("testdb seed connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// 512-dim zero vector. Fine for rows the test doesn't care about
	// vector semantics for (delete, list, audit). Search tests that
	// need a real-looking vector should seed via the Store orchestrator
	// with an httptest Ollama stub instead.
	zeroVec := "["
	for i := 0; i < 512; i++ {
		if i > 0 {
			zeroVec += ","
		}
		zeroVec += "0"
	}
	zeroVec += "]"

	var id string
	err = conn.QueryRow(ctx, `
INSERT INTO memories (content, embedding, profile, tags, importance, surprise)
VALUES ($1, $2::vector, $3, $4, 0.5, 0.5)
RETURNING id::text`,
		content, zeroVec, profile, tags).Scan(&id)
	if err != nil {
		t.Fatalf("testdb seed insert: %v", err)
	}
	return id
}
