package cmd

import (
	"log/slog"
	"os"
)

// Verbosity counter wired to `-v`. Each occurrence raises the log level
// one step (default -> info -> debug). --quiet lowers to error.
//
// Handler is a single shared slog.TextHandler writing to stderr, so
// every package that calls slog.Info / slog.Warn / slog.Debug lands
// in the same stream. stdout is reserved for command output (JSON or
// `--text`) so `jq` pipelines and `--text` formatting stay unaffected.
var verboseCount int

// logLevel resolves the verbose counter + --quiet into a slog.Level.
// --quiet wins over -v (explicit silence beats explicit verbosity).
func logLevel() slog.Level {
	if useQuiet() {
		return slog.LevelError
	}
	switch {
	case verboseCount >= 2:
		return slog.LevelDebug
	case verboseCount == 1:
		return slog.LevelInfo
	default:
		return slog.LevelWarn
	}
}

// initLogging installs a shared slog handler before any subcommand
// runs. Called from rootCmd.PersistentPreRunE.
func initLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel(),
	})))
}
