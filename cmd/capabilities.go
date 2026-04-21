package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// `ogham capabilities` prints the native-vs-sidecar matrix: which MCP
// tools resolve in the Go binary, which still require the Python
// sidecar, and which search augmentations are active on each path.
//
// This exists because "what does --sidecar actually unlock?" is a
// question users ask once a day. Instead of pointing them at
// internal/mcp/native_handlers.go or at the R&D tracker, they can run
// one command.
//
// The hard-coded matrix lives here (not derived from RegisterNativeTools
// at import-time) for two reasons:
//
//  1. Humans need the grouping + commentary, which the tool manifest
//     doesn't carry. "suggest_connections: postgres backend only; supabase
//     via sidecar" is a caveat that only makes sense in a curated list.
//  2. It documents the intent: when we port re_embed_all to native, the
//     edit lives in one place (this file) and the docs stay in sync with
//     the code.
//
// If the matrix drifts from the actual native registration (new native
// tool lands in RegisterNativeTools but isn't in StoreSide / SearchSide /
// ProfileConfig), TestCapabilitiesMatrix_NativeNamesMatchRegistration
// in capabilities_test.go catches it.

// capabilityEntry is one row in the matrix. Mode is "native" or
// "sidecar"; Note is optional commentary (e.g. "postgres backend only").
type capabilityEntry struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
	Note string `json:"note,omitempty"`
}

// capabilitiesMatrix is the authoritative snapshot of what lives where.
// Edit this when a new native tool lands in RegisterNativeTools or when
// a sidecar-only augmentation gets absorbed.
//
// Native-MCP blocks (StoreSide, SearchSide, ProfileConfig) name tools
// that appear in RegisterNativeTools and therefore run in-process on
// `ogham serve`. CLISidecarRouted names CLI subcommands (`ogham export`,
// `ogham import`, `ogham audit`, etc.) that ultimately call through the
// sidecar for the underlying MCP tool -- they're listed separately so
// the matrix stays truthful about which tool names resolve natively.
// SidecarOnlyTools names MCP tools that have no native path at all.
// Augmentations are retrieval-quality knobs that only exist in the
// Python pipeline.
type capabilitiesMatrix struct {
	StoreSide        []capabilityEntry `json:"store_side"`
	SearchSide       []capabilityEntry `json:"search_side"`
	ProfileConfig    []capabilityEntry `json:"profile_config"`
	CLISidecarRouted []capabilityEntry `json:"cli_sidecar_routed"`
	SidecarOnlyTools []capabilityEntry `json:"sidecar_only_tools"`
	Augmentations    []capabilityEntry `json:"search_augmentations"`
}

// Keep entries here in the same order as internal/mcp/native_handlers.go
// RegisterNativeTools, grouped by concern.
var defaultCapabilitiesMatrix = capabilitiesMatrix{
	StoreSide: []capabilityEntry{
		{Name: "store_memory", Mode: "native", Note: "fast path via internal/native/store.go"},
		{Name: "store_decision", Mode: "native"},
		{Name: "store_fact", Mode: "native"},
		{Name: "store_event", Mode: "native"},
		{Name: "store_preference", Mode: "native"},
		{Name: "update_memory", Mode: "native"},
		{Name: "delete_memory", Mode: "native"},
		{Name: "reinforce_memory", Mode: "native"},
		{Name: "contradict_memory", Mode: "native"},
	},
	SearchSide: []capabilityEntry{
		{Name: "hybrid_search", Mode: "native", Note: "CCF hybrid (vector+tsvector); no intent gating"},
		{Name: "list_recent", Mode: "native"},
		{Name: "find_related", Mode: "native"},
		{Name: "explore_knowledge", Mode: "native"},
		{Name: "suggest_connections", Mode: "native", Note: "postgres backend only; supabase via sidecar"},
	},
	ProfileConfig: []capabilityEntry{
		{Name: "switch_profile", Mode: "native"},
		{Name: "current_profile", Mode: "native"},
		{Name: "list_profiles", Mode: "native"},
		{Name: "set_profile_ttl", Mode: "native"},
		{Name: "get_config", Mode: "native"},
		{Name: "get_stats", Mode: "native"},
		{Name: "get_cache_stats", Mode: "native"},
		{Name: "health_check", Mode: "native"},
		{Name: "cleanup_expired", Mode: "native"},
		{Name: "link_unlinked", Mode: "native"},
	},
	// CLISidecarRouted: CLI subcommands exist natively and are the user-
	// facing entry points, but the underlying MCP tool still runs in the
	// Python sidecar. Called out separately so we don't claim native
	// implementation for tools that haven't been ported yet.
	CLISidecarRouted: []capabilityEntry{
		{Name: "export_profile", Mode: "sidecar", Note: "CLI: `ogham export` routes through the sidecar"},
		{Name: "import_memories_tool", Mode: "sidecar", Note: "CLI: `ogham import` routes through the sidecar"},
		{Name: "show_audit_log", Mode: "sidecar", Note: "CLI: `ogham audit` reads audit RPC natively; MCP tool is sidecar-only"},
		{Name: "show_decay_chart", Mode: "sidecar", Note: "CLI: `ogham decay` runs decay RPC natively; MCP tool is sidecar-only"},
		{Name: "show_profile_health", Mode: "sidecar", Note: "CLI: `ogham health` runs probes natively; MCP tool is sidecar-only"},
	},
	SidecarOnlyTools: []capabilityEntry{
		{Name: "compress_old_memories", Mode: "sidecar", Note: "blocked on Go chat-LLM client"},
		{Name: "re_embed_all", Mode: "sidecar", Note: "unblocked but not yet ported (~0.5 day)"},
		{Name: "Prefab dashboard tools", Mode: "sidecar", Note: "stays Python; not planned for Go port"},
	},
	Augmentations: []capabilityEntry{
		{Name: "intent detection", Mode: "sidecar", Note: "multi-hop / ordering / summary / temporal gating"},
		{Name: "strided retrieval", Mode: "sidecar", Note: "timeline-diversified candidates for summary queries"},
		{Name: "query reformulation", Mode: "sidecar", Note: "per-category gated"},
		{Name: "MMR re-ranking", Mode: "sidecar", Note: "diversity enforcement"},
		{Name: "spreading activation", Mode: "sidecar", Note: "graph-augmented retrieval via entities table"},
	},
}

// nativeMCPToolNames flattens the native-side tool names into a set
// for the drift test that compares against RegisterNativeTools.
func (m capabilitiesMatrix) nativeMCPToolNames() map[string]struct{} {
	out := map[string]struct{}{}
	for _, group := range [][]capabilityEntry{m.StoreSide, m.SearchSide, m.ProfileConfig} {
		for _, e := range group {
			if e.Mode != "native" {
				continue
			}
			out[e.Name] = struct{}{}
		}
	}
	return out
}

var capabilitiesJSON bool

var capabilitiesCmd = &cobra.Command{
	Use:   "capabilities",
	Short: "Show which MCP tools are native Go vs require the Python sidecar",
	Long: `Print the authoritative matrix of which MCP tools are implemented
natively in the Go binary versus which still require the Python sidecar
(--sidecar), plus which search augmentations are only available when
routing through the sidecar.

Text mode (default) is grouped for human reading: store-side tools,
search-side tools, profile + config, CLI subcommands that still route
through the sidecar, sidecar-only tools, and sidecar-gated search
augmentations. --json emits a structured payload suitable for scripts
and dashboards.

The Python MCP is the retrieval-quality brain (strided retrieval,
multi-hop intent patterns, MMR re-ranking, spreading activation,
query reformulation). The Go CLI is the enterprise-friendly access
door -- one static binary, zero runtime deps. Use --sidecar when you
need the full retrieval pipeline; the native path is faster but
applies a subset of the retrieval machinery.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode := "native"
		if useSidecar() {
			mode = "sidecar"
		}
		if capabilitiesJSON {
			return emitCapabilitiesJSON(os.Stdout, defaultCapabilitiesMatrix, Version, mode)
		}
		return emitCapabilitiesText(os.Stdout, defaultCapabilitiesMatrix, Version, mode)
	},
}

// capabilitiesJSONPayload is the --json output shape. Explicit struct
// (not just marshaling the matrix) so we can expose the binary's
// current version + active mode alongside the matrix, which scripts
// consuming the output often want.
type capabilitiesJSONPayload struct {
	Version                string             `json:"version"`
	Mode                   string             `json:"mode"`
	NativeTools            []string           `json:"native_tools"`
	SidecarTools           []string           `json:"sidecar_tools"`
	AugmentationsInSidecar []string           `json:"augmentations_in_sidecar"`
	Matrix                 capabilitiesMatrix `json:"matrix"`
}

func emitCapabilitiesJSON(w io.Writer, m capabilitiesMatrix, version, mode string) error {
	payload := capabilitiesJSONPayload{
		Version:                version,
		Mode:                   mode,
		NativeTools:            sortedNativeNames(m),
		SidecarTools:           sortedSidecarNames(m),
		AugmentationsInSidecar: entryNames(m.Augmentations),
		Matrix:                 m,
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal capabilities json: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(out)); err != nil {
		return err
	}
	return nil
}

// sortedNativeNames flattens every native MCP tool across the three
// native-mode groups into a sorted, deduplicated slice. Sorted so the
// --json output is byte-stable across runs -- scripts can diff it.
func sortedNativeNames(m capabilitiesMatrix) []string {
	seen := map[string]struct{}{}
	for _, group := range [][]capabilityEntry{m.StoreSide, m.SearchSide, m.ProfileConfig} {
		for _, e := range group {
			if e.Mode == "native" {
				seen[e.Name] = struct{}{}
			}
		}
	}
	return sortedKeys(seen)
}

// sortedSidecarNames flattens the CLI-sidecar-routed and sidecar-only
// groups into a sorted, deduplicated slice.
func sortedSidecarNames(m capabilitiesMatrix) []string {
	seen := map[string]struct{}{}
	for _, group := range [][]capabilityEntry{m.CLISidecarRouted, m.SidecarOnlyTools} {
		for _, e := range group {
			seen[e.Name] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// entryNames returns each entry's Name in matrix order (sidecar-only
// entries that are semantic labels like "Prefab dashboard tools" keep
// their case + spacing).
func entryNames(entries []capabilityEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func emitCapabilitiesText(w io.Writer, m capabilitiesMatrix, version, mode string) error {
	if _, err := fmt.Fprintf(w, "ogham capabilities (%s, %s)\n\n", version, mode); err != nil {
		return err
	}

	sections := []struct {
		header  string
		entries []capabilityEntry
	}{
		{"Store-side tools (native Go):", m.StoreSide},
		{"Search-side tools (native Go):", m.SearchSide},
		{"Profile + config (native Go):", m.ProfileConfig},
		{"CLI subcommands that still route through the sidecar:", m.CLISidecarRouted},
		{"Python sidecar required (use --sidecar):", m.SidecarOnlyTools},
		{"Search augmentations (Python-only today; use --sidecar for these):", m.Augmentations},
	}
	for _, s := range sections {
		fmt.Fprintln(w, s.header)
		writeGroup(w, s.entries)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Positioning:")
	fmt.Fprintln(w, "  Python MCP = brain (full retrieval quality). Go CLI = enterprise door (fast, static binary).")
	fmt.Fprintln(w, "  Use --sidecar when you need the full retrieval pipeline.")
	return nil
}

// writeGroup pretty-prints one block of entries. Native rows use an
// "ok" bullet, sidecar rows use "--" -- stays ASCII-clean in grep /
// jq pipelines and CI logs.
func writeGroup(w io.Writer, entries []capabilityEntry) {
	// Column widths chosen to fit the longest current name (~25 chars)
	// + the mode tag without wrapping in a typical 80-column terminal.
	const nameWidth = 26
	for _, e := range entries {
		bullet := sidecarBullet
		if e.Mode == "native" {
			bullet = nativeBullet
		}
		if e.Note != "" {
			// Normalise long notes so they don't push the right column
			// past the terminal. No hard wrap; just trim whitespace
			// coming from the struct literal so grep lines stay tidy.
			note := strings.TrimSpace(e.Note)
			fmt.Fprintf(w, "  %s %-*s  %-8s  %s\n",
				bullet, nameWidth, e.Name, e.Mode, note)
		} else {
			fmt.Fprintf(w, "  %s %-*s  %s\n",
				bullet, nameWidth, e.Name, e.Mode)
		}
	}
}

// Native tools render with "ok"; sidecar rows use "--". Unicode stays
// out of the way on ASCII terminals (and grep/jq stay happy) while the
// two markers still align in a monospace grid.
const (
	nativeBullet  = "ok"
	sidecarBullet = "--"
)

func init() {
	capabilitiesCmd.Flags().BoolVar(&capabilitiesJSON, "json", false,
		"Emit JSON (authoritative payload for scripts / dashboards) instead of the text matrix")
	rootCmd.AddCommand(capabilitiesCmd)
}
