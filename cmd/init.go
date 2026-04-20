package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	initNoRegister bool
	initYes        bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Interactive wizard: configure backend, embedder, profile; write config files; register MCP clients",
	Long: `Multi-step TUI wizard (powered by charmbracelet/huh) that collects:

  1. Embedding provider and model
  2. Provider API key (masked input; skipped if already in env or config)
  3. Database backend (Supabase or Postgres)
  4. Backend credentials (URL / connection string, key)
  5. Default memory profile

Then writes ~/.ogham/config.toml (Go canonical) and ~/.ogham/config.env
(Python-readable mirror), runs a pre-flight health check, and offers to
register the binary with any detected MCP clients.

If the user already has config, each step pre-fills with the current
value and can be left unchanged. Run with --yes to accept all current
values without prompting (useful for re-running after editing .env).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}

		if !initYes {
			if err := runInitWizard(cfg); err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					fmt.Fprintln(os.Stderr, "aborted")
					return nil
				}
				return err
			}
		}

		// Normalize defaults the wizard leaves blank.
		if cfg.Embedding.Dimension == 0 {
			cfg.Embedding.Dimension = 512
		}
		if cfg.Profile == "" {
			cfg.Profile = "default"
		}

		tomlPath := native.DefaultPath()
		envPath := filepath.Join(filepath.Dir(tomlPath), "config.env")

		if err := native.Save(tomlPath, cfg); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ wrote %s\n", tomlPath)

		if err := native.SaveEnvFile(envPath, cfg); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "✓ wrote %s\n", envPath)

		fmt.Fprintln(os.Stderr, "\nRunning pre-flight health check...")
		report, err := native.HealthCheck(ctx, cfg, native.HealthOptions{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  health check error: %v\n", err)
		} else {
			for _, c := range report.Checks {
				mark := "✓"
				if !c.OK {
					mark = "✗"
				}
				if c.OK {
					fmt.Fprintf(os.Stderr, "  %s %s\n", mark, c.Name)
				} else {
					fmt.Fprintf(os.Stderr, "  %s %s -- %s\n", mark, c.Name, c.Error)
				}
			}
			if !report.OK {
				fmt.Fprintln(os.Stderr, "\n⚠ One or more checks failed. Edit ~/.ogham/config.env or re-run `ogham init`.")
			}
		}

		if !initNoRegister {
			registerMCPClients()
		}

		fmt.Fprintln(os.Stderr, "\n✓ init complete")
		return nil
	},
}

// runInitWizard drives the multi-step huh form. Each field pre-fills
// from cfg and writes back on submission.
func runInitWizard(cfg *native.Config) error {
	providers := []huh.Option[string]{
		huh.NewOption("Gemini (Google)", "gemini"),
		huh.NewOption("OpenAI", "openai"),
		huh.NewOption("Voyage AI", "voyage"),
		huh.NewOption("Mistral", "mistral"),
		huh.NewOption("Ollama (local, free)", "ollama"),
	}
	backends := []huh.Option[string]{
		huh.NewOption("Supabase (PostgREST)", "supabase"),
		huh.NewOption("Postgres direct (Neon, self-hosted)", "postgres"),
	}
	if cfg.Database.Backend == "" {
		// Infer initial backend selection from whatever credentials exist.
		if cfg.Database.SupabaseURL != "" {
			cfg.Database.Backend = "supabase"
		} else if cfg.Database.URL != "" {
			cfg.Database.Backend = "postgres"
		} else {
			cfg.Database.Backend = "supabase"
		}
	}
	if cfg.Embedding.Provider == "" {
		cfg.Embedding.Provider = "gemini"
	}
	if cfg.Profile == "" {
		cfg.Profile = "work"
	}

	form := huh.NewForm(
		// Step 1: provider + API key
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Embedding provider").
				Description("Which provider generates your memory embeddings?").
				Options(providers...).
				Value(&cfg.Embedding.Provider),
			huh.NewInput().
				Title("API key").
				Description("Leave blank to keep the current value (stored in config).").
				EchoMode(huh.EchoModePassword).
				Value(&cfg.Embedding.APIKey),
			huh.NewInput().
				Title("Model (optional)").
				Description("Leave blank for the provider default (e.g. gemini-embedding-2-preview).").
				Value(&cfg.Embedding.Model),
		),

		// Step 2: backend selection
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Database backend").
				Options(backends...).
				Value(&cfg.Database.Backend),
		),

		// Step 3a: Supabase creds (only if backend == supabase)
		huh.NewGroup(
			huh.NewInput().
				Title("Supabase project URL").
				Description("e.g. https://xxxx.supabase.co").
				Value(&cfg.Database.SupabaseURL).
				Validate(validateSupabaseURL),
			huh.NewInput().
				Title("Supabase secret key").
				Description("sb_secret_... (prefers the new secret key over anon or service_role).").
				EchoMode(huh.EchoModePassword).
				Value(&cfg.Database.SupabaseKey),
		).WithHideFunc(func() bool { return cfg.Database.Backend != "supabase" }),

		// Step 3b: Postgres conn string (only if backend == postgres)
		huh.NewGroup(
			huh.NewInput().
				Title("Postgres connection string").
				Description("postgres://user:password@host:5432/dbname").
				Value(&cfg.Database.URL).
				Validate(validatePostgresURL),
		).WithHideFunc(func() bool { return cfg.Database.Backend != "postgres" }),

		// Step 4: default profile
		huh.NewGroup(
			huh.NewInput().
				Title("Default memory profile").
				Description("Which profile should CLI commands use when --profile is not passed?").
				Value(&cfg.Profile),
		),
	).WithShowHelp(true).WithShowErrors(true)

	return form.Run()
}

func validateSupabaseURL(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return errors.New("must start with https:// or http://")
	}
	return nil
}

func validatePostgresURL(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("required")
	}
	if !strings.HasPrefix(s, "postgres://") && !strings.HasPrefix(s, "postgresql://") {
		return errors.New("must start with postgres:// or postgresql://")
	}
	return nil
}

// registerMCPClients preserves the old init behaviour: best-effort
// registration with Claude Code if its CLI is on PATH, plus printed
// snippets for the other clients.
func registerMCPClients() {
	if _, err := exec.LookPath("claude"); err == nil {
		fmt.Fprintln(os.Stderr, "\nClaude Code detected. Registering Ogham MCP server...")
		register := exec.Command("claude", "mcp", "add", "ogham", "--", "ogham", "serve")
		register.Stderr = os.Stderr
		if err := register.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ Auto-registration failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Manual: claude mcp add ogham -- ogham serve")
		} else {
			fmt.Fprintln(os.Stderr, "✓ Registered with Claude Code")
		}
	}

	fmt.Fprintln(os.Stderr, "\nFor other MCP clients, add one of these snippets:")
	fmt.Fprintln(os.Stderr, `  Cursor / VS Code / Windsurf / Kiro (mcp.json):
    {"mcpServers": {"ogham": {"command": "ogham", "args": ["serve"]}}}`)
	fmt.Fprintln(os.Stderr, `  Codex:
    codex mcp add ogham -- ogham serve`)
	fmt.Fprintln(os.Stderr, `  Enterprise / locked-down Claude Code -- use Bash:
    add 'ogham search/store/list' commands to your CLAUDE.md (see README).`)
}

func init() {
	initCmd.Flags().BoolVar(&initNoRegister, "no-register", false, "Skip MCP client registration at the end")
	initCmd.Flags().BoolVar(&initYes, "yes", false, "Accept current config values without prompting")
	initCmd.Flags().StringVar(&apiKeyFlag, "api-key", "", "(legacy) gateway API key -- prefer the wizard")
	rootCmd.AddCommand(initCmd)
}
