package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Emit host-specific plugin manifests (OpenClaw, Agent Zero, ...)",
	Long: `Each subcommand prints a manifest (JSON / config stanza) that registers
the Ogham binary as a memory/knowledge plugin inside a host harness.

Design intent (see docs/plans/2026-04-16-go-cli-enterprise.md v0.2 scope):
the Go binary is the same everywhere; harnesses differ only in how they
discover and invoke it. Emitting manifests keeps host-specific knowledge
out of the install process -- you pipe or paste the output into the
host's config, no curl-bash installer required.`,
}

var pluginOpenClawCmd = &cobra.Command{
	Use:   "openclaw",
	Short: "Emit OpenClaw native plugin manifest",
	Long: `Prints an OpenClaw plugin config stanza. Copy the output into your
OpenClaw config file or pipe it to ` + "`openclaw plugins install -`" + ` (stdin).

Ogham registers with kind "knowledge" rather than the dedicated "memory"
slot because Neotoma currently fills that slot for structured
deterministic state. Ogham provides semantic memory + knowledge graph
on top -- the two are complementary and can coexist in the same stack.

If Neotoma is not installed, change "knowledge" to "memory" manually.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		oghamPath, _ := exec.LookPath("ogham")
		if oghamPath == "" {
			// Fall back to argv[0] so a non-PATH binary still emits a
			// runnable manifest.
			oghamPath, _ = os.Executable()
		}

		manifest := map[string]any{
			"name":        "ogham",
			"kind":        "knowledge",
			"version":     Version,
			"description": "Ogham MCP -- semantic memory and knowledge graph for AI agents",
			"command":     oghamPath,
			"args":        []string{"serve"},
			"capabilities": []string{
				"semantic_search",
				"hybrid_search",
				"knowledge_graph",
				"entity_extraction",
				"temporal_reasoning",
				"contradiction_detection",
			},
			"env_passthrough": []string{
				"DATABASE_URL", "SUPABASE_URL", "SUPABASE_KEY",
				"EMBEDDING_PROVIDER", "EMBEDDING_DIM",
				"GEMINI_API_KEY", "OPENAI_API_KEY", "VOYAGE_API_KEY", "MISTRAL_API_KEY",
				"OGHAM_PROFILE", "OGHAM_SIDECAR_EXTRAS", "OGHAM_SIDECAR_CMD",
			},
		}
		return emitJSON(manifest)
	},
}

var pluginAgentZeroCmd = &cobra.Command{
	Use:   "agent-zero",
	Short: "Emit Agent Zero memory backend config",
	Long: `Prints an Agent Zero memory backend configuration. Copy into the
relevant section of your Agent Zero instance config (typically the
memory_backend field).

Unlike the previous MCP-based Agent Zero integration, this configuration
targets the Go binary directly -- no Python wrapper script, no uv
environment setup, no --directory path. The binary is expected to be on
PATH or referenced absolutely.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		oghamPath, _ := exec.LookPath("ogham")
		if oghamPath == "" {
			oghamPath, _ = os.Executable()
		}

		manifest := map[string]any{
			"type":        "mcp",
			"name":        "ogham",
			"version":     Version,
			"description": "Ogham MCP -- persistent semantic memory",
			"transport":   "stdio",
			"command":     oghamPath,
			"args":        []string{"serve"},
			"tools": []string{
				"hybrid_search", "store_memory", "list_recent",
				"find_related", "explore_knowledge", "get_stats",
				"current_profile", "switch_profile",
			},
			"auto_register_in_prompt": true,
		}
		return emitJSON(manifest)
	},
}

// pluginHelp gives a better error than cobra's default when a user runs
// `ogham plugin` with no subcommand -- we want to list supported hosts.
func init() {
	pluginCmd.Run = func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(os.Stderr, "usage: ogham plugin <host>")
		fmt.Fprintln(os.Stderr, "supported hosts:")
		fmt.Fprintln(os.Stderr, "  openclaw     OpenClaw native plugin manifest")
		fmt.Fprintln(os.Stderr, "  agent-zero   Agent Zero memory backend config")
		os.Exit(2)
	}
	pluginCmd.AddCommand(pluginOpenClawCmd)
	pluginCmd.AddCommand(pluginAgentZeroCmd)
	rootCmd.AddCommand(pluginCmd)
}

// Ensure encoding/json is part of the build -- emitJSON already uses it,
// but keeping the import marker here protects against unused-import churn
// if the file ever gets trimmed.
var _ = json.Marshal
