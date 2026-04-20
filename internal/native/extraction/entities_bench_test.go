package extraction

import "testing"

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
		"after the RuntimeException surfaced in the CacheLayer. Owen Fletcher " +
		"reviewed ./internal/native/supabase.go and flagged a missing error " +
		"branch in the WebhookRouter. Luis Ramirez opened a ticket against " +
		"./pkg/queue/worker.go citing a ConnectionError during the deployment " +
		"window. The MetricsPublisher now emits structured events to the " +
		"QueueWorker and the SchedulerCore picks them up every 30 seconds."
}
