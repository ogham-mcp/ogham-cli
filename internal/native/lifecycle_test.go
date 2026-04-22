//go:build pgcontainer

package native

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPG_PipelineCounts(t *testing.T) {
	ctx := context.Background()
	cfg := testCfg(t, "lifecycle-counts-test")
	resetMemories(t, cfg)

	// Seed 4 memories. The init_memory_lifecycle trigger auto-creates
	// the matching memory_lifecycle rows at stage='fresh'. We then
	// UPDATE memory_lifecycle to place some in stable/editing.
	profile := "lifecycle-counts-test"
	ids := []string{
		insertMemory(t, cfg, profile, "seed-a", nil),
		insertMemory(t, cfg, profile, "seed-b", nil),
		insertMemory(t, cfg, profile, "seed-c", nil),
		insertMemory(t, cfg, profile, "seed-d", nil),
	}

	// Force b, c to stable; d to editing; a stays fresh.
	updateLifecycleStage(t, ctx, cfg, ids[1], "stable")
	updateLifecycleStage(t, ctx, cfg, ids[2], "stable")
	updateLifecycleStage(t, ctx, cfg, ids[3], "editing")

	counts, err := PipelineCounts(ctx, cfg, profile)
	if err != nil {
		t.Fatalf("PipelineCounts: %v", err)
	}
	if counts["fresh"] != 1 || counts["stable"] != 2 || counts["editing"] != 1 {
		t.Errorf("unexpected counts: %+v", counts)
	}
}

func TestPG_PipelineCounts_MissingTableFallsBackToAllFresh(t *testing.T) {
	ctx := context.Background()
	cfg := testCfg(t, "lifecycle-counts-premigration-test")
	resetMemories(t, cfg)

	profile := "lifecycle-counts-premigration-test"

	// Drop memory_lifecycle + its triggers to simulate a pre-026 DB.
	// Seed memories AFTER the drop so the trigger isn't firing against
	// a missing table.
	simulatePreMigrationState(t, ctx, cfg)
	t.Cleanup(func() { restoreMigrationState(t, ctx, cfg) })

	_ = insertMemory(t, cfg, profile, "pre-a", nil)
	_ = insertMemory(t, cfg, profile, "pre-b", nil)

	counts, err := PipelineCounts(ctx, cfg, profile)
	if err != nil {
		t.Fatalf("PipelineCounts against pre-migration DB: %v", err)
	}
	if counts["fresh"] != 2 || counts["stable"] != 0 || counts["editing"] != 0 {
		t.Errorf("expected all-fresh fallback, got: %+v", counts)
	}
}

// --- helpers ------------------------------------------------------------

// updateLifecycleStage is a direct UPDATE on memory_lifecycle. Bypasses
// the Python advance-stage machinery (which we don't have here); the
// Phase 6 Go code is read-only so this is the only way to put the DB
// into a non-fresh state for the test.
func updateLifecycleStage(t *testing.T, ctx context.Context, cfg *Config, id, stage string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("stage update connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx,
		`UPDATE memory_lifecycle SET stage = $1, stage_entered_at = now(), updated_at = now() WHERE memory_id = $2`,
		stage, id,
	); err != nil {
		t.Fatalf("stage update exec: %v", err)
	}
}

// simulatePreMigrationState drops the triggers + memory_lifecycle table
// so PipelineCounts must take the fallback path. Restored via
// restoreMigrationState in t.Cleanup so other tests in this binary see
// the normal schema again.
func simulatePreMigrationState(t *testing.T, ctx context.Context, cfg *Config) {
	t.Helper()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("pre-migration connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Drop triggers first so the subsequent memories INSERT in the
	// fallback test doesn't try to populate a dropped table.
	stmts := []string{
		`DROP TRIGGER IF EXISTS memories_init_lifecycle ON memories`,
		`DROP TRIGGER IF EXISTS memories_sync_lifecycle_profile ON memories`,
		`DROP FUNCTION IF EXISTS init_memory_lifecycle()`,
		`DROP FUNCTION IF EXISTS sync_memory_lifecycle_profile()`,
		`DROP TABLE IF EXISTS memory_lifecycle CASCADE`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("pre-migration exec %q: %v", s, err)
		}
	}
}

// restoreMigrationState re-applies the memory_lifecycle schema so the
// shared testcontainer is reusable by subsequent tests in the same
// binary run. Mirrors the trailing block of testdata/schema_postgres.sql.
func restoreMigrationState(t *testing.T, ctx context.Context, cfg *Config) {
	t.Helper()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("restore connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	ddl := `
create table if not exists memory_lifecycle (
    memory_id uuid primary key references memories(id) on delete cascade,
    profile text not null,
    stage text not null default 'fresh',
    stage_entered_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint memory_lifecycle_stage_valid check (stage in ('fresh', 'stable', 'editing'))
);

create index if not exists memory_lifecycle_transitioning_idx
    on memory_lifecycle (profile, stage_entered_at)
    where stage in ('fresh', 'editing');

create index if not exists memory_lifecycle_profile_stage_idx
    on memory_lifecycle (profile, stage);

create or replace function init_memory_lifecycle() returns trigger as $func$
begin
    insert into memory_lifecycle (memory_id, profile, stage, stage_entered_at, updated_at)
    values (new.id, new.profile, 'fresh', new.created_at, new.created_at)
    on conflict (memory_id) do nothing;
    return new;
end;
$func$ language plpgsql;

drop trigger if exists memories_init_lifecycle on memories;
create trigger memories_init_lifecycle
    after insert on memories
    for each row
    execute function init_memory_lifecycle();

create or replace function sync_memory_lifecycle_profile() returns trigger as $func$
begin
    if new.profile is distinct from old.profile then
        update memory_lifecycle
           set profile = new.profile, updated_at = now()
         where memory_id = new.id;
    end if;
    return new;
end;
$func$ language plpgsql;

drop trigger if exists memories_sync_lifecycle_profile on memories;
create trigger memories_sync_lifecycle_profile
    after update of profile on memories
    for each row
    execute function sync_memory_lifecycle_profile();

-- Backfill lifecycle rows for the memories that were inserted under
-- the pre-migration drop, so those rows are visible to subsequent
-- tests that assume the trigger populated them.
insert into memory_lifecycle (memory_id, profile, stage, stage_entered_at, updated_at)
select id, profile, 'fresh', created_at, created_at
  from memories
on conflict (memory_id) do nothing;
`
	if _, err := conn.Exec(ctx, ddl); err != nil {
		t.Fatalf("restore DDL: %v", err)
	}
}
