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
	cmd := exec.Command("tmux", "display-message", "-t", ht.tmuxSession, "-p",
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
func (ht *HistoryTracker) FetchHistoryGap(currentHistorySize int) (string, error) {
	if currentHistorySize <= ht.lastHistoryIndex {
		return "", nil // No new history
	}

	// Calculate the gap: lines from lastHistoryIndex to current history top
	// tmux capture-pane uses negative offsets: -S -100 means "start 100 lines back"
	//
	// Example: lastHistoryIndex=50, currentHistorySize=100, viewportRows=24
	// Gap = lines 50-75 (lines 76-99 are still visible in viewport)
	// startOffset = -(100 - 50) = -50 (50 lines back from current)
	// endOffset = -24 (stop at viewport top)

	gapSize := currentHistorySize - ht.lastHistoryIndex
	if gapSize <= 0 {
		return "", nil
	}

	// Fetch only the gap lines (not including current viewport)
	// -S = start offset (negative = lines above current view)
	// -E = end offset (negative = lines above current view)
	startOffset := -gapSize
	endOffset := -1 // -1 = one line above current viewport

	// If the gap is within current viewport, no history fetch needed
	if -startOffset <= ht.viewportRows {
		// All "new" lines are still in viewport, viewport diff will handle them
		return "", nil
	}

	// Adjust end offset to stop at viewport boundary
	endOffset = -(ht.viewportRows + 1)
	if startOffset >= endOffset {
		return "", nil // Gap is entirely within viewport
	}

	cmd := exec.Command("tmux", "capture-pane", "-t", ht.tmuxSession,
		"-p", "-e", // -p = stdout, -e = preserve escape sequences (colors)
		"-S", fmt.Sprintf("%d", startOffset),
		"-E", fmt.Sprintf("%d", endOffset))
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Update our index to mark these lines as read
	ht.lastHistoryIndex = currentHistorySize - ht.viewportRows
	if ht.lastHistoryIndex < 0 {
		ht.lastHistoryIndex = 0
	}

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
			// New line beyond previous viewport - append naturally
			if i > 0 {
				output.WriteString("\r\n")
			}
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

// RemoteHistoryTracker is a version of HistoryTracker for remote SSH sessions.
// It uses SSH bridge to run tmux commands instead of local exec.
type RemoteHistoryTracker struct {
	*HistoryTracker
	hostID    string
	sshBridge *SSHBridge
	tmuxPath  string
}

// NewRemoteHistoryTracker creates a tracker for a remote tmux session.
func NewRemoteHistoryTracker(hostID, session string, rows int, sshBridge *SSHBridge) *HistoryTracker {
	// For now, we use the base HistoryTracker but return it wrapped.
	// The remote polling methods use sshBridge directly.
	return &HistoryTracker{
		tmuxSession:       session,
		lastHistoryIndex:  0,
		lastViewportLines: []string{},
		viewportRows:      rows,
		inAltScreen:       false,
	}
}
