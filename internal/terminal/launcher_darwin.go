//go:build darwin

package terminal

import (
	"fmt"
	"os/exec"
	"strings"
)

// OpenSessionInNewWindow opens a new iTerm2 window and types the tmux attach
// command into its first session. On macOS this requires that iTerm2 is
// installed; if osascript reports it cannot find the application we surface
// that error to the caller so the TUI can fall back gracefully.
//
// The command is built via BuildAttachCommand so it stays in lockstep with
// the cross-platform tests.
func OpenSessionInNewWindow(req AttachRequest) error {
	cmd := BuildAttachCommand(req)
	if cmd == "" {
		return fmt.Errorf("terminal: empty tmux session name")
	}
	script := buildITerm2AppleScript(cmd)
	return exec.Command("osascript", "-e", script).Run()
}

// buildITerm2AppleScript returns the AppleScript that spawns a new iTerm2
// window with the user's default profile and runs attachCmd inside it.
//
// We keep this pure and exported-to-tests so the script can be exercised
// without invoking osascript.
func buildITerm2AppleScript(attachCmd string) string {
	// AppleScript string literals are double-quoted; escape inner quotes and
	// backslashes so a tmux session name containing them cannot break out.
	escaped := strings.ReplaceAll(attachCmd, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`tell application "iTerm2"
	activate
	set newWindow to (create window with default profile)
	tell current session of newWindow
		write text "%s"
	end tell
end tell`, escaped)
}
