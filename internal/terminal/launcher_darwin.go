//go:build darwin

package terminal

import (
	"fmt"
	"os/exec"
	"strings"
)

// OpenSessionInNewWindow opens a new iTerm2 tab or window (per
// req.OpenAs) and types the attach command into its session. On macOS
// this requires that iTerm2 is installed; if osascript reports it cannot
// find the application we surface that error to the caller so the TUI
// can fall back gracefully.
//
// The command is built via BuildAttachCommand so it stays in lockstep
// with the cross-platform tests.
func OpenSessionInNewWindow(req AttachRequest) error {
	cmd := BuildAttachCommand(req)
	if cmd == "" {
		return fmt.Errorf("terminal: empty attach command (missing session name or remote host)")
	}
	script := buildITerm2AppleScript(cmd, req.OpenAs)
	return exec.Command("osascript", "-e", script).Run()
}

// OpenSessionInSplitPane opens a new iTerm2 vertical split pane next to the
// current session and runs the attach command inside it. Issue #1470.
func OpenSessionInSplitPane(req AttachRequest) error {
	cmd := BuildAttachCommand(req)
	if cmd == "" {
		return fmt.Errorf("terminal: empty attach command (missing session name or remote host)")
	}
	script := buildITerm2SplitPaneAppleScript(cmd)
	return exec.Command("osascript", "-e", script).Run()
}

// buildITerm2SplitPaneAppleScript returns the AppleScript that splits the
// current iTerm2 pane vertically and runs attachCmd in the new session.
// Kept pure and unexported-but-tested so the script can be exercised without
// invoking osascript.
func buildITerm2SplitPaneAppleScript(attachCmd string) string {
	escaped := strings.ReplaceAll(attachCmd, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`tell application "iTerm2"
	tell current window
		tell current session
			set newSession to (split vertically with default profile)
			tell newSession
				write text "%s"
			end tell
		end tell
	end tell
end tell`, escaped)
}

// buildITerm2AppleScript returns the AppleScript that spawns a new
// iTerm2 tab or window (per openAs) with the user's default profile and
// runs attachCmd inside it.
//
// openAs == "window" reproduces the pre-#1100 behavior (new iTerm2
// window). Any other value, including "" and "tab", produces a new tab
// inside the current window — matching iTerm's natural UX.
//
// We keep this pure and exported-to-tests so the script can be exercised
// without invoking osascript.
func buildITerm2AppleScript(attachCmd string, openAs string) string {
	// AppleScript string literals are double-quoted; escape inner quotes
	// and backslashes so a name (or ssh user@host) containing them
	// cannot break out.
	escaped := strings.ReplaceAll(attachCmd, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	if strings.EqualFold(strings.TrimSpace(openAs), "window") {
		return fmt.Sprintf(`tell application "iTerm2"
	activate
	set newWindow to (create window with default profile)
	tell current session of newWindow
		write text "%s"
	end tell
end tell`, escaped)
	}

	// Default ("tab" / "" / unknown): open a tab in the frontmost
	// window, falling back to creating a window if no iTerm window
	// exists yet. `create tab with default profile` returns a tab
	// whose current session is where we write the attach command.
	return fmt.Sprintf(`tell application "iTerm2"
	activate
	if (count of windows) = 0 then
		set newWindow to (create window with default profile)
		tell current session of newWindow
			write text "%s"
		end tell
	else
		tell current window
			set newTab to (create tab with default profile)
			tell current session of newTab
				write text "%s"
			end tell
		end tell
	end if
end tell`, escaped, escaped)
}
