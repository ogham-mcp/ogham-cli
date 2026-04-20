package native

import (
	"net/url"
	"strings"
)

// redactURL strips the password segment from a URL (typically a Postgres
// connection string) so the result is safe to log or include in an error
// message. String-level surgery avoids url.UserPassword percent-encoding
// the redacted stars back into literal bytes.
//
// Uses to date:
//   - health.go: DATABASE_URL echoed in `ogham health --json` output
//   - maintenance.go: DATABASE_URL echoed in masked-config dump
//
// Anticipated: v0.5 embedders (Voyage / OpenAI / Mistral) whose error
// paths may include the request URL with an API key in a query parameter
// should call this before formatting into error strings.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	pw, hasPass := u.User.Password()
	if !hasPass || pw == "" {
		return raw
	}
	// Replace the first occurrence of :pw@ with :***@; this survives
	// arbitrary password characters without re-encoding them.
	return strings.Replace(raw, ":"+pw+"@", ":***@", 1)
}
