package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/ogham-mcp/ogham-cli/internal/native"
	"github.com/spf13/cobra"
)

var (
	auditProfile   string
	auditOperation string
	auditLimit     int
	auditJSONDeprecated bool
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show the audit trail for a memory profile",
	Long: `Reads the audit_log table in reverse-chronological order, optionally
filtered by operation (store / search / delete / update / …).

The audit log is populated by the same hooks and tool calls that
mutate the memories table, so this is the source of truth for "what
happened when" across Python MCP and Go CLI activity.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		ctx, cancelT := context.WithTimeout(ctx, 30*time.Second)
		defer cancelT()

		cfg, err := native.Load(native.DefaultPath())
		if err != nil {
			return err
		}
		profile := auditProfile
		if profile == "" {
			profile = cfg.Profile
		}

		events, err := native.Audit(ctx, cfg, profile, auditOperation, auditLimit)
		if err != nil {
			return err
		}
		if !useText() {
			return emitJSON(events)
		}
		if len(events) == 0 {
			fmt.Printf("no audit events in profile %q\n", profile)
			return nil
		}
		for _, e := range events {
			memStr := ""
			if e.MemoryID != nil {
				memStr = "  id=" + truncateID(*e.MemoryID)
			}
			fmt.Printf("%s  %-10s  %s%s\n",
				e.EventTime.Format("2006-01-02 15:04:05"),
				e.Operation,
				e.Profile,
				memStr)
		}
		return nil
	},
}

func init() {
	auditCmd.Flags().StringVar(&auditProfile, "profile", "", "Profile to query (defaults to active)")
	auditCmd.Flags().StringVar(&auditOperation, "operation", "", "Filter by operation (store/search/delete/update)")
	auditCmd.Flags().IntVar(&auditLimit, "limit", 20, "Max events to display")
	auditCmd.Flags().BoolVar(&auditJSONDeprecated, "json", false, "")
	_ = auditCmd.Flags().MarkHidden("json")
	rootCmd.AddCommand(auditCmd)
}
