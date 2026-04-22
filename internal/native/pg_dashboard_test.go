//go:build pgcontainer

// Postgres-backed tests for the dashboard-v02 helpers: ListByDateRange
// pagination (via ListOptions.Before + OnDate), StoreCountsByDay grid
// data, and AuditEntries cursor-pagination.
//
// Keeping these in their own file so the dashboard-feature delta shows
// up as a single isolated file in review -- the pg_coverage_test.go
// companion covers v0.x functions and is intentionally not touched.

package native

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- ListOptions.Before pagination --------------------------------------

func TestPG_List_BeforeCursor(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Seed three rows with deterministic created_at stamps so the
	// Before cursor behaviour is inspectable without relying on
	// clock jitter between inserts.
	base := time.Now().UTC().Add(-time.Hour)
	for i, content := range []string{"first", "second", "third"} {
		id := insertMemory(t, cfg, "work", content, nil)
		ts := base.Add(time.Duration(i) * time.Minute)
		if _, err := conn.Exec(ctx,
			`UPDATE memories SET created_at = $1 WHERE id = $2`, ts, id); err != nil {
			t.Fatalf("stamp %q: %v", content, err)
		}
	}

	// No cursor -- should return all three, newest first.
	got, err := List(context.Background(), cfg, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	if got[0].Content != "third" {
		t.Errorf("newest first: got %q", got[0].Content)
	}

	// Cursor at the "second" row's created_at -- should return only
	// "first" (strictly older).
	cutoff := got[1].CreatedAt
	page, err := List(context.Background(), cfg, ListOptions{Limit: 10, Before: cutoff})
	if err != nil {
		t.Fatalf("List (Before): %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("Before cursor: want 1 row, got %d", len(page))
	}
	if page[0].Content != "first" {
		t.Errorf("Before cursor returned wrong row: %q", page[0].Content)
	}
}

// --- ListOptions.OnDate filter -----------------------------------------

func TestPG_List_OnDateFilter(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Two memories on day A, one on day B.
	dayA := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	dayB := dayA.Add(24 * time.Hour)

	for i, content := range []string{"a-one", "a-two"} {
		id := insertMemory(t, cfg, "work", content, nil)
		ts := dayA.Add(time.Duration(i) * time.Minute)
		if _, err := conn.Exec(ctx,
			`UPDATE memories SET created_at = $1 WHERE id = $2`, ts, id); err != nil {
			t.Fatalf("stamp: %v", err)
		}
	}
	idB := insertMemory(t, cfg, "work", "b-one", nil)
	if _, err := conn.Exec(ctx,
		`UPDATE memories SET created_at = $1 WHERE id = $2`, dayB, idB); err != nil {
		t.Fatalf("stamp B: %v", err)
	}

	got, err := List(context.Background(), cfg, ListOptions{Limit: 10, OnDate: dayA})
	if err != nil {
		t.Fatalf("List (OnDate): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("OnDate dayA: want 2 rows, got %d", len(got))
	}
	for _, m := range got {
		if m.Content == "b-one" {
			t.Errorf("OnDate dayA leaked row from day B: %+v", m)
		}
	}
}

// --- StoreCountsByDay ---------------------------------------------------

func TestPG_StoreCountsByDay(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Three rows within the trailing 365 days, spread over 2 days.
	now := time.Now().UTC()
	for _, ts := range []time.Time{
		now.Add(-48 * time.Hour),
		now.Add(-47 * time.Hour),
		now.Add(-24 * time.Hour),
	} {
		id := insertMemory(t, cfg, "work", "dc-seed", nil)
		if _, err := conn.Exec(ctx,
			`UPDATE memories SET created_at = $1 WHERE id = $2`, ts, id); err != nil {
			t.Fatalf("stamp: %v", err)
		}
	}
	// One row outside the window -- must not show up in a 7-day query.
	idOld := insertMemory(t, cfg, "work", "too-old", nil)
	if _, err := conn.Exec(ctx,
		`UPDATE memories SET created_at = $1 WHERE id = $2`,
		now.Add(-400*24*time.Hour), idOld); err != nil {
		t.Fatalf("stamp old: %v", err)
	}

	got, err := StoreCountsByDay(context.Background(), cfg, 7)
	if err != nil {
		t.Fatalf("StoreCountsByDay: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 distinct days, got %d (%+v)", len(got), got)
	}
	// Aggregate counts across the two days should be 3.
	var total int64
	for _, d := range got {
		total += d.Count
		if d.Day.Hour() != 0 || d.Day.Minute() != 0 {
			t.Errorf("Day not truncated to midnight: %s", d.Day)
		}
	}
	if total != 3 {
		t.Errorf("total count: want 3 got %d", total)
	}
}

// --- AuditEntries pagination + filter ----------------------------------

func TestPG_AuditEntries_PaginatesAndFilters(t *testing.T) {
	cfg := testCfg(t, "work")
	resetMemories(t, cfg)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// Seed 3 events across two operations. Stamps are explicit so
	// Before-cursor pagination is deterministic.
	base := time.Now().UTC().Add(-time.Hour)
	for i, ev := range []struct {
		op string
		t  time.Time
	}{
		{"store", base},
		{"delete", base.Add(1 * time.Minute)},
		{"store", base.Add(2 * time.Minute)},
	} {
		_ = i
		if _, err := conn.Exec(ctx,
			`INSERT INTO audit_log (profile, operation, resource_id, metadata, event_time)
			 VALUES ('work', $1, gen_random_uuid(), '{"seq":1}'::jsonb, $2)`,
			ev.op, ev.t); err != nil {
			t.Fatalf("seed audit %q: %v", ev.op, err)
		}
	}
	// Cross-profile noise that must not leak.
	if _, err := conn.Exec(ctx,
		`INSERT INTO audit_log (profile, operation, resource_id, metadata)
		 VALUES ('personal', 'store', gen_random_uuid(), '{}'::jsonb)`); err != nil {
		t.Fatalf("seed cross-profile: %v", err)
	}

	// No filter -- 3 work-profile rows, newest first.
	got, err := AuditEntries(context.Background(), cfg, "work", "", time.Time{}, 50)
	if err != nil {
		t.Fatalf("AuditEntries: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("no-filter: want 3 got %d (%+v)", len(got), got)
	}
	if got[0].Operation != "store" {
		t.Errorf("newest-first: got %q want 'store'", got[0].Operation)
	}

	// Operation filter -- 'delete' only.
	del, err := AuditEntries(context.Background(), cfg, "work", "delete", time.Time{}, 50)
	if err != nil {
		t.Fatalf("AuditEntries delete: %v", err)
	}
	if len(del) != 1 || del[0].Operation != "delete" {
		t.Errorf("delete filter: got %+v", del)
	}

	// Before cursor -- strictly older than the most recent event.
	cutoff := got[0].EventTime
	page, err := AuditEntries(context.Background(), cfg, "work", "", cutoff, 50)
	if err != nil {
		t.Fatalf("AuditEntries (Before): %v", err)
	}
	if len(page) != 2 {
		t.Errorf("Before cursor: want 2 older rows got %d", len(page))
	}
}
