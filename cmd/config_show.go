package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var configJSONDeprecated bool

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect the current runtime configuration",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the resolved config (API keys masked)",
	Long: `Dumps the current native.Config as seen after dotenv merge and env-var
overrides. Secrets are shown as "<first4>…<last4>" so you can verify
which value is active without exposing it. Useful for "why is my
subcommand reading the wrong profile?" debugging.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		masked := native.Mask(cfg)
		tomlPath := native.DefaultPath()
		envPath := filepath.Join(filepath.Dir(tomlPath), "config.env")
		masked.Paths = map[string]string{
			"config_toml": tomlPath,
			"config_env":  envPath,
		}

		if !useText() {
			return emitJSON(masked)
		}
		fmt.Printf("profile:          %s\n", masked.Profile)
		fmt.Printf("config.toml:      %s\n", masked.Paths["config_toml"])
		fmt.Printf("config.env:       %s\n", masked.Paths["config_env"])
		fmt.Println()
		fmt.Printf("[database]\n")
		if masked.Database.Backend != "" {
			fmt.Printf("  backend:        %s\n", masked.Database.Backend)
		}
		if masked.Database.URL != "" {
			fmt.Printf("  url:            %s\n", masked.Database.URL)
		}
		if masked.Database.SupabaseURL != "" {
			fmt.Printf("  supabase_url:   %s\n", masked.Database.SupabaseURL)
		}
		if masked.Database.SupabaseKey != "" {
			fmt.Printf("  supabase_key:   %s\n", masked.Database.SupabaseKey)
		}
		fmt.Println()
		fmt.Printf("[embedding]\n")
		if masked.Embedding.Provider != "" {
			fmt.Printf("  provider:       %s\n", masked.Embedding.Provider)
		}
		if masked.Embedding.Model != "" {
			fmt.Printf("  model:          %s\n", masked.Embedding.Model)
		}
		fmt.Printf("  dimension:      %d\n", masked.Embedding.Dimension)
		if masked.Embedding.APIKey != "" {
			fmt.Printf("  api_key:        %s\n", masked.Embedding.APIKey)
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configShowCmd)
	configShowCmd.Flags().BoolVar(&configJSONDeprecated, "json", false, "")
	_ = configShowCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(configCmd)
}
