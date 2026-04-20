package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var profileJSONDeprecated bool

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage memory profiles",
	Long: `Profile operations: show the active profile, switch to a different one,
list profiles with memory counts, or read/set a profile's TTL.

The active profile is persisted in ~/.ogham/config.toml and mirrored to
~/.ogham/config.env so the Python sidecar picks it up on its next start.`,
}

var profileCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the active profile",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		name := cfg.Profile
		if name == "" {
			name = "default"
		}
		if !useText() {
			return emitJSON(map[string]string{"profile": name})
		}
		fmt.Println(name)
		return nil
	},
}

var profileSwitchCmd = &cobra.Command{
	Use:   "switch <name>",
	Short: "Switch the active profile (persisted to config)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		old := cfg.Profile
		cfg.Profile = args[0]

		tomlPath := native.DefaultPath()
		envPath := filepath.Join(filepath.Dir(tomlPath), "config.env")
		if err := native.Save(tomlPath, cfg); err != nil {
			return err
		}
		if err := native.SaveEnvFile(envPath, cfg); err != nil {
			return err
		}

		if !useText() {
			return emitJSON(map[string]string{"old": old, "new": cfg.Profile})
		}
		fmt.Printf("switched profile: %s → %s\n", displayProfile(old), cfg.Profile)
		return nil
	},
}

var profileListCmd = &cobra.Command{
	Use:   "list",
	Short: "List profiles with memory counts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profiles, err := native.ListProfiles(ctx, cfg)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(profiles)
		}
		if len(profiles) == 0 {
			fmt.Println("no profiles with memories yet")
			return nil
		}
		active := cfg.Profile
		if active == "" {
			active = "default"
		}
		for _, p := range profiles {
			marker := "  "
			if p.Profile == active {
				marker = "* "
			}
			fmt.Printf("%s%-20s %6d memories\n", marker, p.Profile, p.Count)
		}
		return nil
	},
}

var profileTTLCmd = &cobra.Command{
	Use:   "ttl <profile> [<days>]",
	Short: "Read or set a profile's TTL in days (omit days to read)",
	Long: `With one arg (profile name), prints the current TTL.
With two args, upserts the TTL for that profile. Pass 'none' or '-' as
the days value to clear the TTL (memories will then never expire
unless expires_at is set at store time).`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
		defer cancelTimeout()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profile := args[0]

		if len(args) == 1 {
			ttl, err := native.GetProfileTTL(ctx, cfg, profile)
			if err != nil {
				return err
			}
			if !useText() {
				return emitJSON(ttl)
			}
			if ttl.TTLDays == nil {
				fmt.Printf("profile %q: no TTL set (memories never expire by default)\n", profile)
				return nil
			}
			fmt.Printf("profile %q: TTL = %d days\n", profile, *ttl.TTLDays)
			return nil
		}

		ttlDays := -1 // sentinel "clear TTL"
		if args[1] != "none" && args[1] != "-" {
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 {
				return fmt.Errorf("ttl days must be a positive integer or 'none'/'-', got %q", args[1])
			}
			ttlDays = n
		}
		ttl, err := native.SetProfileTTL(ctx, cfg, profile, ttlDays)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(ttl)
		}
		if ttl.TTLDays == nil {
			fmt.Printf("profile %q: TTL cleared\n", profile)
		} else {
			fmt.Printf("profile %q: TTL set to %d days\n", profile, *ttl.TTLDays)
		}
		return nil
	},
}

func displayProfile(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func init() {
	profileCmd.PersistentFlags().BoolVar(&profileJSONDeprecated, "json", false, "")
	_ = profileCmd.PersistentFlags().MarkHidden("json")
	profileCmd.AddCommand(profileCurrentCmd)
	profileCmd.AddCommand(profileSwitchCmd)
	profileCmd.AddCommand(profileListCmd)
	profileCmd.AddCommand(profileTTLCmd)
	rootCmd.AddCommand(profileCmd)
}
