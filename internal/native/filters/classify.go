package filters

import "strings"

// Verdict describes why a PostToolUse event should be captured or
// skipped.
type Verdict int

const (
	// SkipOghamLoop matches tool names that look like Ogham's own
	// MCP tools (mcp__ogham__*, store_memory, hybrid_search). Capturing
	// these would create infinite hook → store → hook loops.
	SkipOghamLoop Verdict = iota
	// SkipAlwaysSkip matches tools listed in hooks_config.yaml's
	// always_skip_tools (reconnaissance commands -- Read, LS, Glob,
	// Grep, etc.).
	SkipAlwaysSkip
	// SkipNotConfigured covers tools that aren't in any capture
	// list. Default behaviour for unknown tools is "drop".
	SkipNotConfigured
	// CaptureRoutine matches tools in routine_tools -- always
	// captured (Bash, Edit, Write, etc.).
	CaptureRoutine
	// CaptureResponseGated matches tools in response_gated_tools --
	// captured only when the response/input yields a useful memory
	// (gating happens downstream of Classify).
	CaptureResponseGated
)

// String returns a label suitable for log output.
func (v Verdict) String() string {
	switch v {
	case SkipOghamLoop:
		return "skip:ogham_loop"
	case SkipAlwaysSkip:
		return "skip:always_skip"
	case SkipNotConfigured:
		return "skip:not_configured"
	case CaptureRoutine:
		return "capture:routine"
	case CaptureResponseGated:
		return "capture:response_gated"
	default:
		return "unknown"
	}
}

// ShouldCapture is true for verdicts that proceed to memory
// extraction.
func (v Verdict) ShouldCapture() bool {
	return v == CaptureRoutine || v == CaptureResponseGated
}

// skipPrefixes are the tool-name prefixes that indicate Ogham's own
// machinery. Mirrors _SKIP_PREFIXES in src/ogham/hooks.py.
var skipPrefixes = []string{
	"mcp__ogham__",
	"ogham_",
	"store_memory",
	"hybrid_search",
}

// Classify decides whether a tool name should be captured by
// PostToolUse, dropped silently, or skipped because it would loop.
// Pure function: no side effects, no state.
func Classify(toolName string) Verdict {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(toolName, p) {
			return SkipOghamLoop
		}
	}
	if config.alwaysSkip[toolName] {
		return SkipAlwaysSkip
	}
	if config.routine[toolName] {
		return CaptureRoutine
	}
	if config.responseGated[toolName] {
		return CaptureResponseGated
	}
	return SkipNotConfigured
}
