//go:build live

// Live integration smoke for the sidecar-proxy path. Opted into via
// `go test -tags live -run LiveProxy ./internal/mcp/` -- off by default
// so CI + normal dev flows stay hermetic.
//
// Preconditions:
//   - `uv` on PATH (or OGHAM_SIDECAR_CMD set to an alternative launcher)
//   - `ogham-mcp` resolvable (or a wheel cache that uv can reach)
//   - ~/.ogham/config.toml pointed at a real DB OR EMBEDDING_PROVIDER=ollama
//     with a local Ollama running (the Python sidecar needs *some* config
//     to come up cleanly, else initialize() errors before ListTools).
//
// What it covers that unit tests can't:
//   - Connect() + ListTools() round-trip against the real Python MCP
//     impl -- validates Name/Description/InputSchema marshal cleanly.
//   - Confirms at least one Python-only tool the proxy would forward
//     (delete_memory) is in the manifest. If that tool ever disappears
//     this test catches it loudly.
//   - Close() exits cleanly without hanging the supervisor goroutine
//     (the `go test` process would hang otherwise).

package mcpserver

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/sidecar"
)

func TestLiveProxy_ListToolsIncludesPythonOnly(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not on PATH; skipping live proxy test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := sidecar.New(sidecar.Options{})
	if err := client.Connect(ctx); err != nil {
		t.Skipf("sidecar connect failed (python env not ready?): %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("sidecar returned empty tool manifest")
	}

	// Canary: these are Python-only right now. If they get absorbed
	// natively later the test expectation moves with them, but until
	// then they must appear in the sidecar's manifest.
	wantPythonOnly := []string{"delete_memory", "update_memory", "compress_old_memories"}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, w := range wantPythonOnly {
		if !names[w] {
			t.Errorf("expected Python-only tool %q in manifest, got %d tools (missing)", w, len(tools))
		}
	}
	t.Logf("sidecar exposed %d tools: %v", len(tools), keys(names))
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
