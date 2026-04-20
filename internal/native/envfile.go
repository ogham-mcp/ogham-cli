package native

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvFiles returns env-var assignments (KEY=VALUE) drawn from the same
// files Python ogham reads, in the same precedence order. Higher-priority
// entries appear later in the slice so callers that do
//
//	cmd.Env = append(os.Environ(), LoadEnvFiles()...)
//
// end up with Python-equivalent precedence (later wins under Go's exec).
//
// Order, lowest to highest:
//  1. ~/.ogham/config.env (global fallback)
//  2. ./.env (project-local, overrides global)
func LoadEnvFiles() []string {
	var out []string

	home, _ := os.UserHomeDir()
	if home != "" {
		out = append(out, parseEnvFile(filepath.Join(home, ".ogham", "config.env"))...)
	}
	cwd, _ := os.Getwd()
	if cwd != "" {
		out = append(out, parseEnvFile(filepath.Join(cwd, ".env"))...)
	}
	return out
}

// parseEnvFile parses a POSIX-ish dotenv file. Returns KEY=VALUE strings;
// caller is responsible for ordering/precedence. Silently skips missing files
// since the files are optional and the callers tolerate emptiness.
//
// Supported syntax:
//   - blank lines and lines starting with '#' are skipped
//   - optional leading `export ` is stripped
//   - values may be wrapped in single or double quotes; quotes are removed
//   - no escape-sequence or variable interpolation (Python's env loading
//     doesn't interpolate either; keep parity, keep it simple)
func parseEnvFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)
		if key == "" {
			continue
		}
		out = append(out, key+"="+val)
	}
	return out
}

func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
