package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/auth"
	"github.com/ogham-mcp/ogham-cli/internal/config"
	"github.com/ogham-mcp/ogham-cli/internal/gateway"
	"github.com/spf13/cobra"
)

var apiKeyFlag string

const dashboardURL = "https://cloud.ogham-mcp.dev"

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the Ogham gateway",
	Long: `Sign in via browser (default) or provide an API key directly.

Browser login opens cloud.ogham-mcp.dev where you sign in with
Apple, Google, GitHub, or email. An API key is generated and
sent back to the CLI automatically.

Manual login: ogham auth login --api-key YOUR_KEY`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Manual mode: --api-key flag or interactive prompt
		if apiKeyFlag != "" {
			return loginWithKey(apiKeyFlag)
		}

		// Try browser login first
		fmt.Fprintln(os.Stderr, "Opening browser to sign in...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		port, resultCh, err := auth.StartCallbackServer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ Could not start callback server: %v\n", err)
			return fallbackToManual()
		}

		callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)
		loginURL := fmt.Sprintf("%s/auth/cli?callback=%s", dashboardURL, callbackURL)

		if err := auth.OpenBrowser(loginURL); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ Could not open browser.\n")
			fmt.Fprintf(os.Stderr, "  Visit this URL manually:\n")
			fmt.Fprintf(os.Stderr, "  %s\n\n", loginURL)
		}

		fmt.Fprintln(os.Stderr, "Waiting for authentication... (press Ctrl+C to cancel)")

		select {
		case result := <-resultCh:
			if result.Error != nil {
				return fmt.Errorf("authentication failed: %w", result.Error)
			}
			return loginWithKey(result.APIKey)
		case <-ctx.Done():
			return fmt.Errorf("authentication timed out (5 minutes)")
		}
	},
}

func loginWithKey(key string) error {
	// Verify the key works
	ua := fmt.Sprintf("ogham-cli/%s", Version)
	client := gateway.New(config.DefaultGatewayURL, key, ua)
	_, err := client.Health()
	if err != nil {
		return fmt.Errorf("API key verification failed: %w", err)
	}

	// Save
	cfg := &config.Config{APIKey: key, GatewayURL: config.DefaultGatewayURL}
	path := config.DefaultPath()
	if err := config.Save(path, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintln(os.Stderr, "✓ API key saved to", path)
	return nil
}

func fallbackToManual() error {
	fmt.Fprint(os.Stderr, "Enter your API key manually: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		return fmt.Errorf("API key is required")
	}
	return loginWithKey(key)
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := config.DefaultPath()
		cfg, err := config.Load(path)
		if err != nil || cfg.APIKey == "" {
			fmt.Fprintln(os.Stderr, "✗ Not logged in")
			fmt.Fprintln(os.Stderr, "  Run: ogham auth login")
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
	authLoginCmd.Flags().StringVar(&apiKeyFlag, "api-key", "", "API key (skip browser login)")
	authCmd.AddCommand(authLoginCmd, authStatusCmd, authLogoutCmd, authTokenCmd)
	rootCmd.AddCommand(authCmd)
}
