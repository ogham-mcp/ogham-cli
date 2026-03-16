package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/spf13/cobra"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Check gateway connectivity",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(config.DefaultPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.APIKey == "" {
			return fmt.Errorf("no API key configured. Run: ogham auth login --api-key YOUR_KEY")
		}

		ua := fmt.Sprintf("ogham-cli/%s", Version)
		client := gateway.New(cfg.GatewayURL, cfg.APIKey, ua)
		result, err := client.Health()
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ Gateway unreachable: %v\n", err)
			os.Exit(1)
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(healthCmd)
}
