package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- fakeSidecar implements sidecarCaller + sidecarLister ---------------

type fakeSidecar struct {
	// tools is the manifest returned by ListTools.
	tools []*mcp.Tool

	// callResponses maps tool name -> canned response. Missing entries
	// produce a nil result + nil error.
	callResponses map[string]*mcp.CallToolResult

	// callErr is the error every CallTool returns, when set. Takes
	// precedence over callResponses.
	callErr error

	// calls records the order of tool invocations for assertions.
	calls []sidecarCall
}

type sidecarCall struct {
	name string
	args map[string]any
}

func (f *fakeSidecar) CallTool(_ context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, sidecarCall{name: name, args: args})
	if f.callErr != nil {
		return nil, f.callErr
	}
	if r, ok := f.callResponses[name]; ok {
		return r, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "default fake response for " + name}},
	}, nil
}

func (f *fakeSidecar) ListTools(_ context.Context) ([]*mcp.Tool, error) {
	return f.tools, nil
}

// --- BuildSidecarProxyHandler tests -------------------------------------

func TestProxyHandler_ForwardsArgsAndResult(t *testing.T) {
	wantResp := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "python returned this"}},
	}
	fake := &fakeSidecar{callResponses: map[string]*mcp.CallToolResult{
		"delete_memory": wantResp,
	}}

	h := BuildSidecarProxyHandler("delete_memory", fake)

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Arguments: json.RawMessage(`{"id":"abc-123","confirm":true}`),
	}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("empty result: %+v", res)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "python returned this" {
		t.Errorf("wrong content: %+v", res.Content)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("want 1 sidecar call, got %d", len(fake.calls))
	}
	got := fake.calls[0]
	if got.name != "delete_memory" {
		t.Errorf("name = %q", got.name)
	}
	if got.args["id"] != "abc-123" || got.args["confirm"] != true {
		t.Errorf("args = %+v", got.args)
	}
}

func TestProxyHandler_EmptyArgsOK(t *testing.T) {
	fake := &fakeSidecar{}
	h := BuildSidecarProxyHandler("get_stats", fake)

	// MCP allows a call with no arguments. Our handler must not crash
	// on an empty json.RawMessage.
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if len(fake.calls) != 1 {
		t.Fatalf("want 1 sidecar call, got %d", len(fake.calls))
	}
	if got := fake.calls[0].args; len(got) != 0 {
		t.Errorf("empty args expected, got %+v", got)
	}
}

func TestProxyHandler_MalformedArgsReturnsToolError(t *testing.T) {
	fake := &fakeSidecar{}
	h := BuildSidecarProxyHandler("delete_memory", fake)

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Arguments: json.RawMessage(`not json at all`),
	}}
	res, err := h(context.Background(), req)
	// Handler returns nil error (transport fine) + IsError result (tool
	// said no). The sidecar must NOT have been called -- we rejected
	// locally before the round trip.
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true, got %+v", res)
	}
	if len(fake.calls) != 0 {
		t.Errorf("sidecar should not have been called; got %d", len(fake.calls))
	}
}

func TestProxyHandler_SidecarErrorSurfacesAsToolError(t *testing.T) {
	fake := &fakeSidecar{callErr: errors.New("subprocess dead")}
	h := BuildSidecarProxyHandler("compress_old_memories", fake)

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Arguments: json.RawMessage(`{"max_age_days":30}`),
	}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true for dead sidecar")
	}
	text, _ := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(text.Text, "subprocess dead") {
		t.Errorf("error should surface the underlying cause: %q", text.Text)
	}
}

func TestProxyHandler_ForwardsPythonIsErrorVerbatim(t *testing.T) {
	// Python surfaces its own tool-level error via IsError=true + content.
	// We must forward it verbatim so the client sees Python's message,
	// not our Go wrapper.
	pythonErr := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "profile 'nope' does not exist"}},
	}
	fake := &fakeSidecar{callResponses: map[string]*mcp.CallToolResult{
		"switch_profile": pythonErr,
	}}
	h := BuildSidecarProxyHandler("switch_profile", fake)

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Arguments: json.RawMessage(`{"profile":"nope"}`),
	}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("transport err: %v", err)
	}
	if !res.IsError {
		t.Errorf("want IsError=true forwarded verbatim")
	}
	text, _ := res.Content[0].(*mcp.TextContent)
	if text.Text != "profile 'nope' does not exist" {
		t.Errorf("python error not preserved: %q", text.Text)
	}
}

// --- RegisterProxiedTools tests -----------------------------------------

func TestRegisterProxiedTools_SkipsNativeCollisions(t *testing.T) {
	// Sidecar advertises 4 tools -- two collide with native (store_memory,
	// hybrid_search) and two are Python-only.
	// AddTool requires a non-nil InputSchema, so we supply a minimal
	// permissive one for each fake. Mirrors what Python's ListTools returns.
	objSchema := json.RawMessage(`{"type":"object"}`)
	fake := &fakeSidecar{tools: []*mcp.Tool{
		{Name: "store_memory", Description: "(python)", InputSchema: objSchema},
		{Name: "hybrid_search", Description: "(python)", InputSchema: objSchema},
		{Name: "delete_memory", Description: "delete one memory", InputSchema: objSchema},
		{Name: "compress_old_memories", Description: "llm compression", InputSchema: objSchema},
	}}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	native := map[string]struct{}{
		"store_memory":  {},
		"hybrid_search": {},
	}

	proxied, err := RegisterProxiedTools(context.Background(), server, fake, fake, native)
	if err != nil {
		t.Fatalf("RegisterProxiedTools: %v", err)
	}
	if len(proxied) != 2 {
		t.Fatalf("want 2 proxied, got %d: %v", len(proxied), proxied)
	}
	want := map[string]bool{"delete_memory": true, "compress_old_memories": true}
	for _, name := range proxied {
		if !want[name] {
			t.Errorf("unexpected proxy name %q", name)
		}
	}
}

func TestRegisterProxiedTools_EmptyManifestOK(t *testing.T) {
	fake := &fakeSidecar{tools: nil}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)

	proxied, err := RegisterProxiedTools(context.Background(), server, fake, fake, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(proxied) != 0 {
		t.Errorf("want 0 proxied for empty manifest, got %d", len(proxied))
	}
}

// errorLister implements sidecarLister but returns an error from ListTools.
type errorLister struct{ err error }

func (e *errorLister) ListTools(_ context.Context) ([]*mcp.Tool, error) {
	return nil, e.err
}

func TestRegisterProxiedTools_ListErrorBubblesUp(t *testing.T) {
	lister := &errorLister{err: errors.New("manifest fetch failed")}
	fake := &fakeSidecar{}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)

	_, err := RegisterProxiedTools(context.Background(), server, lister, fake, nil)
	if err == nil {
		t.Fatal("expected error from ListTools")
	}
	if !strings.Contains(err.Error(), "manifest fetch failed") {
		t.Errorf("error should wrap the underlying cause: %v", err)
	}
}
