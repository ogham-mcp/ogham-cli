package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/ogham-mcp/ogham-cli/internal/sidecar"
)

// connectSidecar wires up the Python MCP sidecar with env vars that mirror
// Python's own config loading: project ./.env, ~/.ogham/config.env, plus
// the TOML-derived SidecarEnv on top. Later entries win. This means a user
// whose Python sidecar works today works with the Go CLI too, and any
// TOML fields (once populated) override pre-existing .env values.
func connectSidecar(ctx context.Context) (*sidecar.Client, error) {
	return connectSidecarWithProfile(ctx, "")
}

// connectSidecarWithProfile is like connectSidecar but appends an
// OGHAM_PROFILE override so the spawned sidecar resolves operations
// against the supplied profile instead of the user's active one.
// Empty profile = no override = behaves like connectSidecar.
//
// Python's get_active_profile() checks OGHAM_PROFILE first
// (see tools/memory.py:get_active_profile), so an env append is enough
// to scope a single sidecar session without the user touching their
// sentinel file.
func connectSidecarWithProfile(ctx context.Context, profile string) (*sidecar.Client, error) {
	cfg, err := native.Load(native.DefaultPath())
	if err != nil {
		return nil, err
	}

	env := native.LoadEnvFiles()
	env = append(env, cfg.SidecarEnv()...)
	if profile != "" {
		env = append(env, "OGHAM_PROFILE="+profile)
	}

	client := sidecar.New(sidecar.Options{
		Impl: &mcp.Implementation{Name: "ogham-cli", Version: Version},
		Env:  env,
	})
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("sidecar connect failed: %w\nCheck OGHAM_SIDECAR_CMD or confirm `uv run ogham serve` works in your shell. "+
			"If Python ogham needs SUPABASE_URL etc., those must be in ~/.ogham/config.env or ./.env in your cwd.", err)
	}
	return client, nil
}

// buildImportToolArgs constructs the MCP tool args for import_memories_tool.
//
// The Python tool signature is import_memories_tool(data: str, dedup_threshold: float),
// where data is a JSON string -- not a parsed object. Sending a map[string]any here
// fails FastMCP's Pydantic validation before the function body runs.
//
// Legacy-shape tolerance: if jsonBytes is the MCP envelope written by a pre-fix
// `ogham export -o file.json` (top-level keys: status, profile, format, data), we
// unwrap and forward the inner data string so old backups still import cleanly.
// Files written by export_memories directly (top-level "memories" key) pass through.
//
// Pure function; no I/O. Tested in isolation by helpers_test.go.
func buildImportToolArgs(jsonBytes []byte, dedup float64) (map[string]any, error) {
	var probe map[string]any
	if err := json.Unmarshal(jsonBytes, &probe); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	payload := string(jsonBytes)
	if status, ok := probe["status"].(string); ok && status == "exported" {
		if inner, ok := probe["data"].(string); ok {
			payload = inner
		}
	}
	return map[string]any{
		"data":            payload,
		"dedup_threshold": dedup,
	}, nil
}

// unwrapExportPayload pulls the inner export string out of the MCP envelope
// returned by export_profile. The Python tool returns
// {"status":"exported","profile":"...","format":"...","data":"<string>"} where
// data is the actual export -- a JSON string for format=json or a markdown
// document for format=markdown. We unwrap unconditionally so the file on disk
// is round-trippable through `ogham import`.
//
// Pure function; no I/O.
func unwrapExportPayload(payload any) (string, error) {
	m, ok := payload.(map[string]any)
	if !ok {
		return "", fmt.Errorf("export_profile returned unexpected shape %T (want map[string]any)", payload)
	}
	data, ok := m["data"].(string)
	if !ok {
		return "", fmt.Errorf("export_profile envelope missing string data field; got keys %v", mapKeys(m))
	}
	return data, nil
}

// mapKeys returns the keys of m sorted, for stable error messages.
func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Don't bother sorting -- error messages are diagnostic, not asserted on.
	return out
}

// splitCSV parses a comma-separated flag value. Whitespace trimmed, empties dropped.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// emitJSON writes v to stdout as indented JSON. Pure JSON, no log lines --
// CLAUDE.md Bash integration relies on stdout being parseable.
func emitJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Fprintln(os.Stdout, string(out))
	return err
}

// toolResultJSON unwraps an MCP CallToolResult into a plain value suitable
// for emitJSON or for pulling structured records out of. The Python MCP
// server puts its tool output in either StructuredContent (preferred, for
// JSON-shaped returns) or the first TextContent as a JSON string (legacy).
func toolResultJSON(r *mcp.CallToolResult) (any, error) {
	if r == nil {
		return nil, errors.New("nil tool result")
	}
	if r.IsError {
		msg := ""
		if len(r.Content) > 0 {
			if tc, ok := r.Content[0].(*mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		if msg == "" {
			msg = "tool reported error"
		}
		return nil, errors.New(msg)
	}
	if r.StructuredContent != nil {
		return r.StructuredContent, nil
	}
	if len(r.Content) > 0 {
		if tc, ok := r.Content[0].(*mcp.TextContent); ok {
			var v any
			if err := json.Unmarshal([]byte(tc.Text), &v); err == nil {
				return v, nil
			}
			// Fall back to raw text -- better to return something than nothing.
			return tc.Text, nil
		}
	}
	return nil, nil
}

// extractMemories pulls a slice of memory records out of whatever shape the
// tool returned. Tools may return []any directly, or wrap the list under
// "results"/"memories"/"items" depending on origin.
func extractMemories(result any) []map[string]any {
	switch r := result.(type) {
	case []any:
		return coerceToMaps(r)
	case map[string]any:
		// "result" (singular) is what the Python MCP tool wrapper emits for
		// most list-shaped returns -- list_recent, hybrid_search, explore_*.
		for _, key := range []string{"result", "results", "memories", "items"} {
			if v, ok := r[key]; ok {
				if arr, ok := v.([]any); ok {
					return coerceToMaps(arr)
				}
			}
		}
	}
	return nil
}

func coerceToMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// truncate shortens s to at most n runes with an ellipsis.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// printMemoryMap renders a single memory record (as returned from a tool
// call) in human-readable form.
func printMemoryMap(idx int, m map[string]any) {
	content, _ := m["content"].(string)
	id, _ := m["id"].(string)
	content = truncate(strings.ReplaceAll(content, "\n", " "), 120)

	score := ""
	if s, ok := m["score"].(float64); ok {
		score = fmt.Sprintf(" [%.3f]", s)
	}
	if id != "" {
		fmt.Printf("%d.%s %s\n   id=%s\n", idx, score, content, id)
	} else {
		fmt.Printf("%d.%s %s\n", idx, score, content)
	}
}

// printNativeMemory renders a native.Memory. Same shape as printMemoryMap
// but typed to avoid reflection on our own data.
func printNativeMemory(idx int, m native.Memory) {
	content := truncate(strings.ReplaceAll(m.Content, "\n", " "), 120)
	fmt.Printf("%d. %s\n   id=%s  created=%s\n", idx, content, m.ID, m.CreatedAt.Format("2006-01-02 15:04"))
}

// printNativeSearchResult renders a hybrid-search hit with its score.
// Matches sidecar-mode formatting so users flipping between --native and
// default see the same shape.
func printNativeSearchResult(idx int, r native.SearchResult) {
	content := truncate(strings.ReplaceAll(r.Content, "\n", " "), 120)
	fmt.Printf("%d. [%.3f] %s\n   id=%s  created=%s\n",
		idx, r.Relevance, content, r.ID, r.CreatedAt.Format("2006-01-02 15:04"))
}

// notImplemented is the response for --native on tools that haven't been
// absorbed yet. Prints guidance to stderr, exits with an error so scripts
// notice.
func notImplemented(tool string) error {
	return fmt.Errorf("native %s is not implemented yet -- use the default (sidecar) path or track progress in docs/plans/2026-04-16-go-cli-enterprise.md", tool)
}

// noteSidecarFallback prints a one-line info to stderr announcing that a
// command is running through the Python sidecar because its native path
// is not implemented yet. Suppressed when the user explicitly passed
// --sidecar / --legacy / --python (they already know) or --quiet. Stderr
// so it never pollutes stdout JSON that scripts / LLMs are parsing.
//
// Coverage audit (2026-04-20, task #138d):
//
//   - Called at: cmd/store.go, cmd/export.go, cmd/import.go -- the three
//     commands that always fall through to the sidecar today.
//   - list / search / health: only hit the sidecar when the user opted in
//     with --sidecar; this helper already no-ops in that case, so calling
//     it would be a noop. Left as-is.
//   - audit / cleanup / decay / delete / profile / stats / config show:
//     native-only, no sidecar path to announce.
//   - dashboard: prints its own "[ogham dashboard] launching: ..." banner
//     with the full argv, which is strictly more informative than the
//     generic fallback notice. Left as-is.
//   - serve: runs our own MCP server; not a fallback.
func noteSidecarFallback(tool string) {
	writeSidecarFallbackNotice(os.Stderr, tool)
}

// writeSidecarFallbackNotice is the testable core of noteSidecarFallback.
// Factored out so helpers_test.go can exercise the --sidecar / --quiet
// suppression without reassigning os.Stderr.
func writeSidecarFallbackNotice(w io.Writer, tool string) {
	if useSidecar() || useQuiet() {
		return
	}
	fmt.Fprintf(w,
		"[ogham] %q has no native Go path yet; routing through the Python sidecar. Pass --sidecar (or --quiet) to suppress this notice.\n",
		tool)
}
