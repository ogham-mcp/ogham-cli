package outbox

import (
	"context"
	"testing"
)

// BenchmarkWrite measures the hook-hot-path cost: marshal + write +
// fsync + rename. Council perf-seat target: p95 <= 40 ms so the full
// hook event (filter + outbox.Write) stays under the 100 ms budget.
func BenchmarkWrite(b *testing.B) {
	o, err := New(b.TempDir())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	rec := &Record{
		Content: "Bash: git commit -m 'fix typo'",
		Profile: "work",
		Source:  "hook:post-tool",
		Tags:    []string{"type:action", "tool:Bash"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := o.Write(rec); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// BenchmarkDrain measures cost of consuming N=100 queued records.
// Realistic SessionStart-time cost when a fleet of hooks queued up
// during prior session.
func BenchmarkDrain(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		o, err := New(b.TempDir())
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		for j := 0; j < 100; j++ {
			if err := o.Write(&Record{Content: "rec", Profile: "work", Source: "h"}); err != nil {
				b.Fatalf("Write: %v", err)
			}
		}
		b.StartTimer()
		if _, err := o.Drain(context.Background(), func(_ context.Context, _ *Record) error {
			return nil
		}); err != nil {
			b.Fatalf("Drain: %v", err)
		}
	}
}
