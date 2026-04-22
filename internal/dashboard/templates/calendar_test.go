package templates

import (
	"testing"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
)

// TestBuildCalendar_GridShape asserts the 53-column by 7-row grid is
// always emitted regardless of input density.
func TestBuildCalendar_GridShape(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) // Wednesday
	cd := BuildCalendar(nil, now)

	if len(cd.WeekColumns) != 53 {
		t.Fatalf("want 53 columns, got %d", len(cd.WeekColumns))
	}
	for i, c := range cd.WeekColumns {
		if len(c.Cells) != 7 {
			t.Errorf("col %d: want 7 cells, got %d", i, len(c.Cells))
		}
	}
}

// TestBuildCalendar_CountsOverlay checks that a count for a specific
// day ends up in the right cell of the grid.
func TestBuildCalendar_CountsOverlay(t *testing.T) {
	now := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	yesterday := now.Add(-24 * time.Hour).Truncate(24 * time.Hour)

	cd := BuildCalendar([]native.DayCount{
		{Day: yesterday, Count: 7},
	}, now)

	if cd.Total != 7 {
		t.Errorf("Total: got %d want 7", cd.Total)
	}
	if cd.MaxCount != 7 {
		t.Errorf("MaxCount: got %d want 7", cd.MaxCount)
	}
	// Find yesterday's cell in the grid and assert its count.
	var found bool
	for _, col := range cd.WeekColumns {
		for _, cell := range col.Cells {
			if !cell.Day.IsZero() && cell.Day.Equal(yesterday) {
				found = true
				if cell.Count != 7 {
					t.Errorf("yesterday cell count: got %d want 7", cell.Count)
				}
			}
		}
	}
	if !found {
		t.Errorf("yesterday cell missing from grid")
	}
}

// TestCalendarCellClass covers the intensity bucket boundaries.
func TestCalendarCellClass(t *testing.T) {
	cases := map[int64]string{
		0:   "bg-muted",
		1:   "bg-primary/30",
		3:   "bg-primary/30",
		4:   "bg-primary/60",
		10:  "bg-primary/60",
		11:  "bg-primary/80",
		20:  "bg-primary/80",
		21:  "bg-primary",
		999: "bg-primary",
	}
	for in, want := range cases {
		got := calendarCellClass(in)
		if got != want {
			t.Errorf("count=%d: got %q want %q", in, got, want)
		}
	}
}
