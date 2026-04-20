package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	deleteProfile string
	deleteYes     bool
	deleteJSONDeprecated bool
)

var deleteCmd = &cobra.Command{
	Use:   "delete <memory-id>",
	Short: "Delete a memory by ID from the active profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := strings.TrimSpace(args[0])

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 30*time.Second)
		defer cancelT()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profile := deleteProfile
		if profile == "" {
			profile = cfg.Profile
		}

		if !deleteYes {
			fmt.Printf("Delete memory %s from profile %q? [y/N] ", truncateID(id), profile)
			var answer string
			_, _ = fmt.Scanln(&answer)
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("aborted")
				return nil
			}
		}

		res, err := native.Delete(ctx, cfg, id, profile)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(res)
		}
		fmt.Printf("deleted %s from %s\n", res.ID, res.Profile)
		return nil
	},
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:8] + "…"
	}
	return id
}

func init() {
	deleteCmd.Flags().StringVar(&deleteProfile, "profile", "", "Profile the memory belongs to (defaults to active)")
	deleteCmd.Flags().BoolVarP(&deleteYes, "yes", "y", false, "Skip confirmation prompt")
	deleteCmd.Flags().BoolVar(&deleteJSONDeprecated, "json", false, "")
	_ = deleteCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(deleteCmd)
}
