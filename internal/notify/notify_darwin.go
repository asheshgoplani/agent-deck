//go:build darwin

package notify

import (
	"os/exec"
	"strings"
)

// desktopNotify uses AppleScript (osascript) — the same mechanism agent-deck
// already uses in internal/terminal/launcher_darwin.go — to raise a
// Notification Center banner. Started non-blocking; errors are ignored.
func desktopNotify(title, body string) {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	script := "display notification \"" + esc(body) + "\" with title \"" + esc(title) + "\""
	_ = exec.Command("osascript", "-e", script).Start()
}
