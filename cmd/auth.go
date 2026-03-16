package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/spf13/cobra"
)

var apiKeyFlag string

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the Ogham gateway",
	RunE: func(cmd *cobra.Command, args []string) error {
		key := apiKeyFlag

		if key == "" {
			fmt.Fprint(os.Stderr, "Enter your API key: ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			key = strings.TrimSpace(line)
		}

		if key == "" {
			return fmt.Errorf("API key is required")
		}

		ua := fmt.Sprintf("ogham-cli/%s", Version)
		client := gateway.New(config.DefaultGatewayURL, key, ua)
		_, err := client.Health()
		if err != nil {
			return fmt.Errorf("API key verification failed: %w", err)
		}

		cfg := &config.Config{APIKey: key, GatewayURL: config.DefaultGatewayURL}
		path := config.DefaultPath()
		if err := config.Save(path, cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		fmt.Fprintln(os.Stderr, "✓ API key saved to", path)
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := config.DefaultPath()
		cfg, err := config.Load(path)
		if err != nil || cfg.APIKey == "" {
			fmt.Fprintln(os.Stderr, "✗ Not logged in")
			fmt.Fprintln(os.Stderr, "  Run: ogham auth login --api-key YOUR_KEY")
			return nil
		}

		ua := fmt.Sprintf("ogham-cli/%s", Version)
		client := gateway.New(cfg.GatewayURL, cfg.APIKey, ua)
		_, err = client.Health()

		if err != nil {
			fmt.Fprintln(os.Stderr, "✗ Logged in but gateway unreachable")
		} else {
			fmt.Fprintln(os.Stderr, "✓ Logged in")
		}
		fmt.Fprintf(os.Stderr, "  Gateway: %s\n", cfg.GatewayURL)
		fmt.Fprintf(os.Stderr, "  Config:  %s\n", path)
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := config.DefaultPath()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove config: %w", err)
		}
		fmt.Fprintln(os.Stderr, "✓ Credentials removed")
		return nil
	},
}

var authTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Print current API key (for piping to scripts)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(config.DefaultPath())
		if err != nil || cfg.APIKey == "" {
			return fmt.Errorf("not logged in. Run: ogham auth login")
		}
		fmt.Print(cfg.APIKey)
		return nil
	},
}

func init() {
	authLoginCmd.Flags().StringVar(&apiKeyFlag, "api-key", "", "API key (or enter interactively)")
	authCmd.AddCommand(authLoginCmd, authStatusCmd, authLogoutCmd, authTokenCmd)
	rootCmd.AddCommand(authCmd)
}
