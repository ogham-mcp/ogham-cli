package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version, OS, and architecture",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("ogham-cli/%s (%s; %s)\n", Version, runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
