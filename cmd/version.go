package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Version is set via -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Version=..."
var Version = "dev"

// Commit is set via -ldflags "-X github.com/ogham-mcp/ogham-cli/cmd.Commit=..."
// Defaults to "unknown" so local `go build` without ldflags still produces
// a runnable binary. Not required for correctness.
var Commit = "unknown"

// BuildDate is set via -ldflags to the UTC build timestamp. Same
// unknown-default pattern as Commit.
var BuildDate = "unknown"

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the binary's version + build info",
	Long: `Prints the CLI version, commit, build date, Go version, and platform.
Useful for bug reports -- include this output when you file an issue.

JSON is the default format (per the rc4 UX flip). Pass --text for a
human-readable one-liner.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		info := versionInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: BuildDate,
			Go:        runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		}
		if useText() {
			fmt.Printf("ogham-cli/%s  commit=%s  built=%s  %s  %s/%s\n",
				info.Version, info.Commit, info.BuildDate, info.Go, info.OS, info.Arch)
			return nil
		}
		return emitJSON(info)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
