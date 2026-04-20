package extraction

import (
	"testing"
	"time"
)

// BenchmarkEntities runs the extractor on a ~500-char representative
// mixed-category input. Entities() fires on every `ogham store` call
// in native mode, so a regression here would be visible in end-to-end
// store latency. Track this number across v0.5 releases; a doubling
// from the Day 1 baseline is the signal to optimise the regex pass.
//
// Run locally:
//
//	go test -bench=BenchmarkEntities -benchmem ./internal/native/extraction/
func BenchmarkEntities(b *testing.B) {
	content := representativeContent()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Entities(content)
	}
}

// BenchmarkEntities_Empty is the baseline for input with zero matches.
// Exercises the early-return and regex-pass overhead without allocation
// pressure from the result set.
func BenchmarkEntities_Empty(b *testing.B) {
	content := "this is plain prose with no patterns in it at all here."

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Entities(content)
	}
}

// representativeContent is a ~500-char chunk mixing every category and
// typical real-world prose. Avoid changing this once numbers are logged
// against it -- cross-commit comparability matters more than surface
// realism.
func representativeContent() string {
	return "Kevin Burns pushed the PaymentGateway changes to ./cmd/root.go " +
		"after the RuntimeException surfaced in the CacheLayer. Iain McLeod " +
		"reviewed ./internal/native/supabase.go and flagged a missing error " +
		"branch in the WebhookRouter. Andres Garcia opened a ticket against " +
		"./pkg/queue/worker.go citing a ConnectionError during the deployment " +
		"window. The MetricsPublisher now emits structured events to the " +
		"QueueWorker and the SchedulerCore picks them up every 30 seconds."
}

// BenchmarkDates runs the date extractor on a representative mixed
// input containing ISO, natural, and relative forms. Dates() fires on
// every native `ogham store` so this is a hot path.
func BenchmarkDates(b *testing.B) {
	ref := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	content := "On 2026-04-20 and April 3rd, 2026 we shipped. Last week " +
		"was the rehearsal on 2025/12/01. In 2 weeks we ship again, " +
		"three days after the 15 March 2026 checkpoint. Context: " +
		"dates from 2025-11-03 through 2026-04-20."

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DatesAt(content, ref)
	}
}

// BenchmarkImportance runs the scoring function on typical content
// containing all signal classes. Called once per native `ogham store`.
// Signal-word matching uses lowercased substring checks so the hot
// path is dominated by the strings.ToLower copy.
func BenchmarkImportance(b *testing.B) {
	content := representativeContent() +
		" we decided to refactor the architecture after a failed " +
		"RuntimeException; see ./pkg/root.go and `ogham serve`."
	tags := []string{"type:decision", "project:ogham", "v0.5"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Importance(content, tags)
	}
}
