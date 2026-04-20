package sidecar

import (
	"context"
	"testing"
)

// testCtx returns a context bound to t's lifetime. Avoids importing t.Context
// (new in Go 1.24+) and keeps us compatible with the module's Go version.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}
