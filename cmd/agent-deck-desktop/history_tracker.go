package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// HistoryTracker maintains scrollback state to prevent data loss during tmux polling.
// It solves the "gap problem" where lines scroll off the viewport between poll ticks
// and would otherwise be lost forever.
//
// The key insight is that when streaming applications like Claude Code output content,
// they use TUI escape sequences (cursor positioning, in-place updates) that prevent
// xterm.js from building up a proper scrollback buffer. By polling tmux instead,
// we get the rendered content and can track history properly.
type HistoryTracker struct {
	tmuxSession       string
	lastHistoryIndex  int      // Last line index read from tmux history
	lastViewportLines []string // Last viewport state for diffing
	viewportRows      int
	inAltScreen       bool // Pause history tracking when in vim/less/etc
}

// NewHistoryTracker creates a tracker for the given tmux session.
func NewHistoryTracker(session string, rows int) *HistoryTracker {
	return &HistoryTracker{
		tmuxSession:       session,
		lastHistoryIndex:  0,
		lastViewportLines: []string{},
		viewportRows:      rows,
		inAltScreen:       false,
	}
}

// GetTmuxInfo gets current history size and alt-screen status from tmux.
// Returns: historySize (lines scrolled off top), inAltScreen (vim/less/htop active), error
func (ht *HistoryTracker) GetTmuxInfo() (historySize int, inAltScreen bool, err error) {
	// Query tmux for history_size and alternate_on
	// history_size = number of lines in scrollback (above visible pane)
	// alternate_on = 1 if app is using alternate screen buffer (vim, less, htop)
	cmd := exec.Command(tmuxBinaryPath, "display-message", "-t", ht.tmuxSession, "-p",
		"#{history_size},#{alternate_on}")
	out, err := cmd.Output()
	if err != nil {
		return 0, false, err
	}

	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) >= 2 {
		historySize, _ = strconv.Atoi(parts[0])
		inAltScreen = parts[1] == "1"
	}
	return historySize, inAltScreen, nil
}

// FetchHistoryGap retrieves lines from tmux history that we haven't seen yet.
// This ensures no lines are lost when output scrolls faster than our poll rate.
// Returns CRLF-terminated content ready for xterm.js, or empty string if no gap.
//
// Important: tmux's history_size counts lines that have scrolled OFF the visible
// viewport into the scrollback buffer. These lines need to be captured and sent
// to xterm.js for proper scrollback accumulation, since DiffViewport uses cursor
// positioning which doesn't add to xterm's scrollback buffer.
func (ht *HistoryTracker) FetchHistoryGap(currentHistorySize int) (string, error) {
	if currentHistorySize <= ht.lastHistoryIndex {
		return "", nil // No new history
	}

	// Calculate the gap: new lines that scrolled into tmux history since last fetch
	// tmux capture-pane uses negative offsets: -S -100 means "start 100 lines back"
	//
	// Example: lastHistoryIndex=50, currentHistorySize=60
	// Gap = 10 new lines that scrolled off viewport into history
	// We need to fetch lines -60 through -(lastHistoryIndex+1) = -51

	gapSize := currentHistorySize - ht.lastHistoryIndex
	if gapSize <= 0 {
		return "", nil
	}

	// Fetch gap lines from tmux history
	// -S = start offset (furthest back in history)
	// -E = end offset (closest to viewport)
	// Negative offsets: -1 is the line just above viewport, -2 is two lines up, etc.
	startOffset := -currentHistorySize            // Start at oldest unfetched line
	endOffset := -(ht.lastHistoryIndex + 1)       // End just after last fetched line

	// Clamp to valid range (don't go beyond what's in history)
	if ht.lastHistoryIndex == 0 {
		// First fetch - get all history lines above viewport
		endOffset = -1
	}

	// Sanity check: start must be before (more negative than) end
	if startOffset >= endOffset {
		return "", nil
	}

	cmd := exec.Command(tmuxBinaryPath, "capture-pane", "-t", ht.tmuxSession,
		"-p", "-e", // -p = stdout, -e = preserve escape sequences (colors)
		"-S", fmt.Sprintf("%d", startOffset),
		"-E", fmt.Sprintf("%d", endOffset))
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Update our index to mark these lines as read
	ht.lastHistoryIndex = currentHistorySize

	// Convert LF to CRLF for xterm.js
	content := string(out)
	if len(content) == 0 {
		return "", nil
	}

	// Normalize line endings
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\n", "\r\n")

	// Ensure it ends with CRLF for proper line separation
	if !strings.HasSuffix(content, "\r\n") {
		content += "\r\n"
	}

	return content, nil
}

// DiffViewport compares current viewport against last state and returns
// minimal ANSI escape sequence to update xterm.js in-place.
// This handles spinners, progress bars, and other in-place updates efficiently.
func (ht *HistoryTracker) DiffViewport(currentContent string) string {
	// Split into lines, trimming trailing empty lines (tmux pads to pane height)
	currentContent = strings.TrimRight(currentContent, "\n")
	newLines := strings.Split(currentContent, "\n")

	// If no previous state, this is first capture - do full write
	if len(ht.lastViewportLines) == 0 {
		ht.lastViewportLines = newLines
		return ht.buildFullViewportOutput(newLines)
	}

	// Calculate how many lines changed
	changedCount := 0
	for i := 0; i < len(newLines) || i < len(ht.lastViewportLines); i++ {
		var newLine, oldLine string
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if i < len(ht.lastViewportLines) {
			oldLine = ht.lastViewportLines[i]
		}
		if newLine != oldLine {
			changedCount++
		}
	}

	// Calculate diff percentage
	maxLines := len(newLines)
	if len(ht.lastViewportLines) > maxLines {
		maxLines = len(ht.lastViewportLines)
	}
	diffPercent := 0
	if maxLines > 0 {
		diffPercent = (changedCount * 100) / maxLines
	}

	// Circuit breaker: if >80% changed or major line count mismatch, do hard resync
	lineMismatch := false
	if len(ht.lastViewportLines) > 0 {
		lineDiff := len(newLines) - len(ht.lastViewportLines)
		if lineDiff < 0 {
			lineDiff = -lineDiff
		}
		lineMismatch = lineDiff > (len(ht.lastViewportLines) / 2)
	}

	if diffPercent > 80 || lineMismatch {
		ht.lastViewportLines = newLines
		return ht.buildFullViewportOutput(newLines)
	}

	// Smart diff: only update changed lines
	var output strings.Builder

	// Always start with cursor home to establish known state
	// This prevents cursor position bugs when history gap was emitted before us
	output.WriteString("\x1b[H")

	for i, newLine := range newLines {
		if i < len(ht.lastViewportLines) {
			if newLine == ht.lastViewportLines[i] {
				continue // Unchanged line, skip
			}
			// Changed line - update in place using cursor positioning
			// \x1b[<row>;1H = move cursor to row (1-indexed), column 1
			output.WriteString(fmt.Sprintf("\x1b[%d;1H", i+1))
			output.WriteString(newLine)
			output.WriteString("\x1b[K") // Clear to end of line
		} else {
			// New line beyond previous viewport - position cursor and write
			// (Can't use \r\n because cursor may be at an arbitrary row from
			// previous positioned writes of changed lines)
			output.WriteString(fmt.Sprintf("\x1b[%d;1H", i+1))
			output.WriteString(newLine)
			output.WriteString("\x1b[K") // Clear to end of line
		}
	}

	// Clear extra lines if content shrunk
	if len(newLines) < len(ht.lastViewportLines) {
		// Move to first line to clear, then erase to end of screen
		output.WriteString(fmt.Sprintf("\x1b[%d;1H\x1b[J", len(newLines)+1))
	}

	// Hide cursor (TUI apps draw their own visual cursor)
	output.WriteString("\x1b[?25l")

	ht.lastViewportLines = newLines
	return output.String()
}

// buildFullViewportOutput creates a complete viewport update (used for hard resync).
func (ht *HistoryTracker) buildFullViewportOutput(lines []string) string {
	var output strings.Builder

	// Move cursor to home position
	output.WriteString("\x1b[H")

	for i, line := range lines {
		output.WriteString(line)
		output.WriteString("\x1b[K") // Clear to end of line
		if i < len(lines)-1 {
			output.WriteString("\r\n")
		}
	}

	// Clear any remaining lines below
	output.WriteString("\r\n\x1b[J")

	// Hide cursor
	output.WriteString("\x1b[?25l")

	return output.String()
}

// Reset clears state (call on resize, session change, or alt-screen exit).
func (ht *HistoryTracker) Reset() {
	ht.lastHistoryIndex = 0
	ht.lastViewportLines = []string{}
}

// SetViewportRows updates the expected viewport height (call on resize).
func (ht *HistoryTracker) SetViewportRows(rows int) {
	ht.viewportRows = rows
}

// SetAltScreen updates alt-screen state and resets viewport tracking if exiting.
func (ht *HistoryTracker) SetAltScreen(inAltScreen bool) {
	if ht.inAltScreen && !inAltScreen {
		// Exiting alt-screen - screen content is completely different now
		ht.lastViewportLines = []string{}
	}
	ht.inAltScreen = inAltScreen
}

// NewRemoteHistoryTracker creates a HistoryTracker for a remote tmux session.
// The tracker itself is the same as local - remote command execution is handled
// by the caller via SSHBridge. The hostID and sshBridge params are accepted for
// API compatibility but the tracker doesn't use them directly.
func NewRemoteHistoryTracker(hostID, session string, rows int, sshBridge *SSHBridge) *HistoryTracker {
	// Remote polling uses sshBridge directly in terminal.go's pollRemoteTmuxOnce.
	// The tracker just tracks viewport state for diffing, same as local.
	_ = hostID    // Reserved for future use
	_ = sshBridge // Reserved for future use
	return &HistoryTracker{
		tmuxSession:       session,
		lastHistoryIndex:  0,
		lastViewportLines: []string{},
		viewportRows:      rows,
		inAltScreen:       false,
	}
}
