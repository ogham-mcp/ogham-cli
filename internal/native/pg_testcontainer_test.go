//go:build pgcontainer

// Real-pgvector coverage tests. Opted into via `go test -tags pgcontainer`
// -- off by default so the hermetic `go test ./...` stays fast and doesn't
// require Docker on every contributor's machine.
//
// These tests spin up a throwaway pgvector container, bootstrap the shared
// schema, and exercise the pgx paths that sit at 0% under the default
// coverage run. Meant to close task #141 to the locked 90% gate for
// internal/native/ (excluding extraction, which is already at 95%+).
//
// CI integration: GitHub Actions has built-in Docker support on
// ubuntu-latest, so adding `-tags pgcontainer` to the coverage job is
// ~30s overhead for the container pull + schema load.
//
// Run:
//   go test -tags pgcontainer -v ./internal/native/
//   go test -tags pgcontainer -coverprofile=cov.out ./internal/native/
//
// Re-running the same binary re-uses the container if present (caches
// the bootstrap), so iterating on test logic is fast after the first run.

package native

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

// pgContainer is the shared fixture. Tests should call testDSN(t) rather
// than constructing their own -- that ensures bootstrap runs exactly
// once per test binary and the container dies cleanly when the binary
// exits.
var (
	pgOnce    sync.Once
	pgDSN     string
	pgErr     error
	pgCleanup func()
)

// testDSN returns the postgres DSN of the shared pgvector container,
// lazily launching it on first call. The schema has already been
// loaded when this returns.
//
// Tests call sql.Exec(ctx, "TRUNCATE memories, memory_relationships, ...")
// in their own setup if they want a clean slate -- the container is
// shared across all tests in this binary to keep wall-clock reasonable.
func testDSN(t *testing.T) string {
	t.Helper()
	pgOnce.Do(pgBootstrap)
	if pgErr != nil {
		t.Fatalf("postgres testcontainer failed to boot: %v", pgErr)
	}
	// Register a one-time cleanup at binary exit via TestMain if the
	// parent chose not to. Tests don't need to wire anything here.
	return pgDSN
}

// pgBootstrap launches the pgvector container, loads schema_postgres.sql,
// and writes the DSN into pgDSN for testDSN() to return. Runs under
// sync.Once so concurrent tests don't race.
func pgBootstrap() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schemaPath := findSchema()
	if schemaPath == "" {
		pgErr = fmt.Errorf("could not locate sql/schema_postgres.sql from cwd")
		return
	}

	container, err := tcpostgres.Run(ctx,
		"pgvector/pgvector:pg17",
		tcpostgres.WithDatabase("ogham_test"),
		tcpostgres.WithUsername("ogham"),
		tcpostgres.WithPassword("ogham"),
		// Schema loads at container startup via the standard
		// docker-entrypoint-initdb.d mechanism. Fast + reliable.
		tcpostgres.WithInitScripts(schemaPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(45*time.Second),
		),
	)
	if err != nil {
		pgErr = fmt.Errorf("launch pgvector: %w", err)
		return
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		pgErr = fmt.Errorf("connection string: %w", err)
		return
	}
	pgDSN = dsn
	pgCleanup = func() {
		// 10s drain is plenty for a local Docker container.
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = container.Terminate(shutdown)
	}
}

// findSchema returns the path to the vendored postgres schema that
// ships inside the package's testdata/. Keeping the schema inside the
// module keeps tests self-contained (no sibling-checkout assumptions)
// at the cost of a periodic `make sync-schema` to refresh from the
// R&D repo when the canonical schema changes.
func findSchema() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(wd, "testdata", "schema_postgres.sql")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// testCfg returns a Config wired at the shared testcontainer, with the
// caller's profile pre-set. Each test gets a fresh Config struct so
// Profile changes don't leak between tests.
func testCfg(t *testing.T, profile string) *Config {
	t.Helper()
	dsn := testDSN(t)
	cfg := &Config{
		Profile: profile,
	}
	cfg.Database.Backend = "postgres"
	cfg.Database.URL = dsn
	return cfg
}

// resetMemories truncates the tables the pgx tests touch so each test
// starts from a known-empty state. Safe because the container is
// dedicated to this test binary.
func resetMemories(t *testing.T, cfg *Config) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("reset connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx,
		"TRUNCATE memories, memory_relationships, audit_log, profile_settings CASCADE")
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// insertMemory puts one row directly into the memories table, bypassing
// the Store orchestrator. Tests that want to probe a DELETE/UPDATE/LIST
// flow can seed fixtures without triggering extraction + embedding.
func insertMemory(t *testing.T, cfg *Config, profile, content string, tags []string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("seed connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// 512-dim zero vector is fine for rows where the tests don't care
	// about vector-space semantics (delete, list, cleanup, audit).
	zeroVec := make([]byte, 0, 512*4)
	zeroVec = append(zeroVec, '[')
	for i := 0; i < 512; i++ {
		if i > 0 {
			zeroVec = append(zeroVec, ',')
		}
		zeroVec = append(zeroVec, '0')
	}
	zeroVec = append(zeroVec, ']')

	var id string
	err = conn.QueryRow(ctx, `
INSERT INTO memories (content, embedding, profile, tags, importance, surprise)
VALUES ($1, $2::vector, $3, $4, 0.5, 0.5)
RETURNING id::text`,
		content, string(zeroVec), profile, tags).Scan(&id)
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	return id
}
