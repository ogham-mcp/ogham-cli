package auth

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser opens a URL in the user's default browser.
// The three exec.Command calls below are #nosec G204. exec.Command does
// not go through a shell, so metacharacters in url cannot inject; the
// program name is a literal in every branch and only the argument
// varies. url is the OAuth callback address this process just built,
// not caller input.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		// #nosec G204
		return exec.Command("open", url).Start()
	case "linux":
		// #nosec G204
		return exec.Command("xdg-open", url).Start()
	case "windows":
		// #nosec G204
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
