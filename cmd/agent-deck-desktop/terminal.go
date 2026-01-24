package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Log file for debugging - can be read by the developer
var debugLogFile *os.File

// Pre-compiled regex patterns for sanitizing terminal output.
// These are compiled once at package init time for performance.
var (
	// Cursor positioning patterns
	cursorHomeRe    = regexp.MustCompile(`\x1b\[H`)
	cursorPosRe     = regexp.MustCompile(`\x1b\[\d+;\d+H`)
	cursorPosAltRe  = regexp.MustCompile(`\x1b\[\d+;\d+f`)
	cursorMoveRe    = regexp.MustCompile(`\x1b\[\d*[ABCD]`) // Up/down/forward/back
	cursorLineColRe = regexp.MustCompile(`\x1b\[\d*[EFG]`)  // Next/prev line, column absolute

	// Cursor visibility and style patterns
	cursorVisRe   = regexp.MustCompile(`\x1b\[\?25[hl]`) // Show/hide cursor
	cursorStyleRe = regexp.MustCompile(`\x1b\[\d* ?q`)   // Cursor style (block, underline, bar)

	// Screen clearing patterns
	clearScreenRe    = regexp.MustCompile(`\x1b\[2J`)   // Clear screen
	clearToEndRe     = regexp.MustCompile(`\x1b\[J`)    // Clear to end of screen
	clearScreenVarRe = regexp.MustCompile(`\x1b\[\d*J`) // Clear screen variants
	clearLineEndRe   = regexp.MustCompile(`\x1b\[K`)    // Clear to end of line
	clearLineVarRe   = regexp.MustCompile(`\x1b\[\d*K`) // Clear line variants

	// Alternate screen buffer patterns
	altScreenXtermRe = regexp.MustCompile(`\x1b\[\?1049[hl]`) // xterm alt screen
	altScreenDECRe   = regexp.MustCompile(`\x1b\[\?47[hl]`)   // DEC alt screen

	// Cursor save/restore patterns
	saveCursorANSIRe    = regexp.MustCompile(`\x1b\[s`) // Save cursor (ANSI)
	restoreCursorANSIRe = regexp.MustCompile(`\x1b\[u`) // Restore cursor (ANSI)
	saveCursorDECRe     = regexp.MustCompile(`\x1b7`)   // Save cursor (DEC)
	restoreCursorDECRe  = regexp.MustCompile(`\x1b8`)   // Restore cursor (DEC)

	// Scroll region pattern
	scrollRegionRe = regexp.MustCompile(`\x1b\[\d*;\d*r`) // Set scroll region

	// Seam-specific patterns (used during initial PTY attachment)
	seamCursorPosRe       = regexp.MustCompile(`\x1b\[\d*;\d*[Hf]`) // Cursor positioning (H or f)
	seamCursorPosSimpleRe = regexp.MustCompile(`\x1b\[\d+[Hf]`)     // Simple cursor positioning
)

func init() {
	// Create log file in temp directory
	logPath := filepath.Join(os.TempDir(), "agent-deck-desktop-debug.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err == nil {
		debugLogFile = f
		fmt.Fprintf(debugLogFile, "=== Agent Deck Desktop Debug Log ===\n")
		fmt.Fprintf(debugLogFile, "Started: %s\n", time.Now().Format(time.RFC3339))
		fmt.Fprintf(debugLogFile, "Log file: %s\n\n", logPath)
		debugLogFile.Sync()
	}
}

// debugLog writes to file AND sends to frontend console
func (t *Terminal) debugLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("15:04:05.000")
	logLine := fmt.Sprintf("[%s] %s\n", timestamp, msg)

	// Write to file
	if debugLogFile != nil {
		debugLogFile.WriteString(logLine)
		debugLogFile.Sync()
	}

	// Also send to frontend
	if t.ctx != nil {
		runtime.EventsEmit(t.ctx, "terminal:debug", TerminalEvent{SessionID: t.sessionID, Data: msg})
	}
}

// LogDiagnostic writes a diagnostic message from the frontend to the debug log file.
// This is a package-level function that writes directly to the log file.
func LogDiagnostic(message string) {
	timestamp := time.Now().Format("15:04:05.000")
	logLine := fmt.Sprintf("[%s] [FRONTEND-DIAG] %s\n", timestamp, message)

	if debugLogFile != nil {
		debugLogFile.WriteString(logLine)
		debugLogFile.Sync()
	}
}

// stripTTSMarkers removes «tts» and «/tts» markers from output.
func stripTTSMarkers(s string) string {
	s = strings.ReplaceAll(s, "«tts»", "")
	s = strings.ReplaceAll(s, "«/tts»", "")
	return s
}

// TerminalEvent is the payload structure for terminal events.
// All terminal events include sessionId for multi-pane support.
type TerminalEvent struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// Terminal manages the PTY and communication with the frontend.
type Terminal struct {
	ctx       context.Context
	sessionID string // Identifies which session this terminal serves (for multi-pane support)
	pty       *PTY
	mu        sync.Mutex
	closed    bool

	// Current tmux session (for hybrid mode)
	tmuxSession string

	// Polling mode - for proper scrollback accumulation
	tmuxPolling    bool
	tmuxStopChan   chan struct{}
	tmuxLastState  string
	historyTracker *HistoryTracker

	// Remote session support (SSH)
	remoteHostID string     // Empty for local, hostID for remote sessions
	sshBridge    *SSHBridge // Reference to SSH bridge for remote commands
}

// NewTerminal creates a new Terminal instance for the given session ID.
func NewTerminal(sessionID string) *Terminal {
	return &Terminal{
		sessionID: sessionID,
	}
}

// SetContext sets the Wails runtime context.
func (t *Terminal) SetContext(ctx context.Context) {
	t.ctx = ctx
}

// Start spawns the shell and begins reading from the PTY.
// Accepts initial terminal dimensions to set PTY size before shell starts.
func (t *Terminal) Start(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pty != nil {
		return nil // Already started
	}

	p, err := SpawnPTY("")
	if err != nil {
		return err
	}

	// Set initial size immediately before shell draws prompt
	if cols > 0 && rows > 0 {
		p.Resize(uint16(cols), uint16(rows))
	}

	t.pty = p
	t.closed = false

	// Start reading from PTY and emitting to frontend
	go t.readLoop()

	return nil
}

// StartTmuxSession connects to a tmux session using polling mode:
// 1. Fetch and emit sanitized history via terminal:history event
// 2. Attach PTY for user input only
// 3. Use polling for display updates (ensures scrollback accumulates properly)
//
// This approach solves the scrollback problem where TUI applications like Claude Code
// use escape sequences that prevent xterm.js from building up scrollback buffer.
func (t *Terminal) StartTmuxSession(tmuxSession string, cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pty != nil {
		return nil // Already started
	}

	t.debugLog("[POLLING] Starting polling session for tmux=%s cols=%d rows=%d", tmuxSession, cols, rows)

	// 1. Resize tmux window to match terminal dimensions
	if cols > 0 && rows > 0 {
		resizeCmd := exec.Command("tmux", "resize-window", "-t", tmuxSession, "-x", itoa(cols), "-y", itoa(rows))
		if err := resizeCmd.Run(); err != nil {
			t.debugLog("[POLLING] resize-window error: %v", err)
		} else {
			t.debugLog("[POLLING] Resized tmux window to %dx%d", cols, rows)
		}
		// Wait for tmux to reflow content after resize
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Fetch full history (including scrollback)
	// -S - = start from beginning of scrollback
	// -E - = end at current cursor position
	// -p = print to stdout
	// -e = include escape sequences (colors)
	historyCmd := exec.Command("tmux", "capture-pane", "-t", tmuxSession, "-p", "-e", "-S", "-", "-E", "-")
	historyOutput, err := historyCmd.Output()
	if err != nil {
		t.debugLog("[POLLING] capture-pane error: %v", err)
		// Continue anyway - history is optional
	}

	// 3. Sanitize and emit history via separate event
	if len(historyOutput) > 0 {
		history := sanitizeHistoryForXterm(string(historyOutput))
		history = normalizeCRLF(history)
		t.debugLog("[POLLING] Emitting %d bytes of sanitized history", len(history))
		if t.ctx != nil {
			runtime.EventsEmit(t.ctx, "terminal:history", TerminalEvent{SessionID: t.sessionID, Data: history})
		}
	}

	// 4. Short delay for frontend to process history
	time.Sleep(50 * time.Millisecond)

	// 5. Attach PTY to tmux (for user input only - output is handled by polling)
	t.debugLog("[POLLING] Attaching PTY to tmux session for input")
	pty, err := SpawnPTYWithCommand("tmux", "attach-session", "-t", tmuxSession)
	if err != nil {
		return fmt.Errorf("failed to attach to tmux: %w", err)
	}

	if cols > 0 && rows > 0 {
		pty.Resize(uint16(cols), uint16(rows))
	}

	t.pty = pty
	t.tmuxSession = tmuxSession
	t.closed = false

	// 6. Start PTY read loop (we'll discard output, using polling for display instead)
	// This keeps the PTY connection alive and allows tmux to detect our terminal size
	go t.readLoopDiscard()

	// 7. Start polling mode for display updates
	t.startTmuxPolling(tmuxSession, rows)

	t.debugLog("[POLLING] Session started successfully with polling mode")
	return nil
}

// SetSSHBridge sets the SSH bridge reference for remote session support.
func (t *Terminal) SetSSHBridge(bridge *SSHBridge) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sshBridge = bridge
}

// StartRemoteTmuxSession connects to a tmux session on a remote host via SSH.
// Uses SSH polling for display updates (read-only for now in Stage 3).
//
// Parameters:
//   - hostID: SSH host identifier from config.toml [ssh_hosts.X]
//   - tmuxSession: tmux session name on the remote host
//   - cols, rows: initial terminal dimensions
func (t *Terminal) StartRemoteTmuxSession(hostID, tmuxSession string, cols, rows int) error {
	// Quick lock to check if already started and get sshBridge reference
	t.mu.Lock()
	if t.pty != nil {
		t.mu.Unlock()
		return nil // Already started
	}
	if t.sshBridge == nil {
		t.mu.Unlock()
		return fmt.Errorf("SSH bridge not initialized")
	}
	sshBridge := t.sshBridge
	ctx := t.ctx
	sessionID := t.sessionID
	t.mu.Unlock()

	t.debugLog("[REMOTE] Starting remote session hostID=%s tmux=%s cols=%d rows=%d",
		hostID, tmuxSession, cols, rows)

	// Get tmux path for this host
	tmuxPath := sshBridge.GetTmuxPath(hostID)

	// 1. Resize tmux window on remote host (outside lock - network call)
	if cols > 0 && rows > 0 {
		resizeCmd := fmt.Sprintf("%s resize-window -t %q -x %d -y %d",
			tmuxPath, tmuxSession, cols, rows)
		if _, err := sshBridge.RunCommand(hostID, resizeCmd); err != nil {
			t.debugLog("[REMOTE] resize-window error: %v", err)
		} else {
			t.debugLog("[REMOTE] Resized tmux window to %dx%d", cols, rows)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 2. Fetch full history from remote (outside lock - network call)
	historyCmd := fmt.Sprintf("%s capture-pane -t %q -p -e -S - -E -", tmuxPath, tmuxSession)
	historyOutput, err := sshBridge.RunCommand(hostID, historyCmd)
	if err != nil {
		t.debugLog("[REMOTE] capture-pane error: %v", err)
		// Continue anyway - history is optional
	}

	// 3. Sanitize and emit history
	if len(historyOutput) > 0 {
		history := sanitizeHistoryForXterm(historyOutput)
		history = normalizeCRLF(history)
		t.debugLog("[REMOTE] Emitting %d bytes of sanitized history", len(history))
		if ctx != nil {
			runtime.EventsEmit(ctx, "terminal:history", TerminalEvent{SessionID: sessionID, Data: history})
		}
	}

	// 4. Short delay for frontend to process history
	time.Sleep(50 * time.Millisecond)

	// 5. Attach SSH PTY for interactive input (outside lock - network call)
	t.debugLog("[REMOTE] Attaching SSH PTY for interactive session")
	newPty, err := SpawnSSHPTY(hostID, tmuxSession, sshBridge)
	if err != nil {
		t.debugLog("[REMOTE] SSH PTY attach failed: %v", err)
		// Fall back to read-only mode - continue without PTY
		newPty = nil
	} else {
		// Set PTY size
		if cols > 0 && rows > 0 {
			newPty.Resize(uint16(cols), uint16(rows))
		}
		t.debugLog("[REMOTE] SSH PTY attached successfully")
	}

	// 6. Re-lock to commit results
	t.mu.Lock()
	// Check again that nothing changed while we were unlocked
	if t.pty != nil {
		t.mu.Unlock()
		// Another goroutine started a session - close our PTY if we made one
		if newPty != nil {
			newPty.Close()
		}
		return nil
	}

	t.pty = newPty
	t.remoteHostID = hostID
	t.tmuxSession = tmuxSession
	t.closed = false
	t.mu.Unlock()

	// Start PTY read loop after unlocking (if we have a PTY)
	if newPty != nil {
		go t.readLoopDiscard()
	}

	// 7. Start remote polling mode for display updates (100ms for SSH latency)
	t.startRemoteTmuxPolling(hostID, tmuxSession, rows)

	if newPty != nil {
		t.debugLog("[REMOTE] Session started successfully (interactive mode)")
	} else {
		t.debugLog("[REMOTE] Session started successfully (read-only polling mode)")
	}
	return nil
}

// startRemoteTmuxPolling begins polling remote tmux for display updates.
func (t *Terminal) startRemoteTmuxPolling(hostID, tmuxSession string, rows int) {
	t.tmuxPolling = true
	t.tmuxStopChan = make(chan struct{})
	t.tmuxLastState = ""
	t.historyTracker = NewRemoteHistoryTracker(hostID, tmuxSession, rows, t.sshBridge)

	go t.pollRemoteTmuxLoop(hostID, tmuxSession)
}

// pollRemoteTmuxLoop continuously polls remote tmux for display updates.
func (t *Terminal) pollRemoteTmuxLoop(hostID, tmuxSession string) {
	ticker := time.NewTicker(100 * time.Millisecond) // Slightly slower for SSH latency
	defer ticker.Stop()

	for {
		select {
		case <-t.tmuxStopChan:
			return
		case <-ticker.C:
			t.pollRemoteTmuxOnce(hostID, tmuxSession)
		}
	}
}

// pollRemoteTmuxOnce captures current remote tmux pane and emits any changes.
func (t *Terminal) pollRemoteTmuxOnce(hostID, tmuxSession string) {
	t.mu.Lock()
	polling := t.tmuxPolling
	sshBridge := t.sshBridge
	tracker := t.historyTracker
	t.mu.Unlock()

	if !polling || sshBridge == nil || tracker == nil {
		return
	}

	tmuxPath := sshBridge.GetTmuxPath(hostID)

	// Capture current viewport from remote
	captureCmd := fmt.Sprintf("%s capture-pane -t %q -p -e", tmuxPath, tmuxSession)
	output, err := sshBridge.RunCommand(hostID, captureCmd)
	if err != nil {
		t.debugLog("[REMOTE-POLL] capture-pane error: %v", err)
		// Session might have ended
		if t.ctx != nil {
			runtime.EventsEmit(t.ctx, "terminal:exit", TerminalEvent{SessionID: t.sessionID, Data: "remote tmux session ended"})
		}
		t.stopTmuxPolling()
		return
	}

	currentState := output

	// Only process if viewport changed
	t.mu.Lock()
	lastState := t.tmuxLastState
	t.mu.Unlock()

	if currentState != lastState {
		t.mu.Lock()
		t.tmuxLastState = currentState
		t.mu.Unlock()

		if t.ctx != nil {
			content := stripTTSMarkers(currentState)

			// Diff viewport and emit minimal updates
			updateSequence := tracker.DiffViewport(content)

			if len(updateSequence) > 0 {
				lines := strings.Count(content, "\n")
				t.debugLog("[REMOTE-POLL] Viewport diff: %d bytes update, %d content lines",
					len(updateSequence), lines)
				runtime.EventsEmit(t.ctx, "terminal:data", TerminalEvent{SessionID: t.sessionID, Data: updateSequence})
			}
		}
	}
}

// readLoopDiscard reads from PTY but discards output.
// This is used in polling mode where display is handled by polling tmux directly.
// The PTY is kept open for user input and to maintain the tmux connection.
func (t *Terminal) readLoopDiscard() {
	buf := make([]byte, 32*1024)

	for {
		t.mu.Lock()
		p := t.pty
		closed := t.closed
		t.mu.Unlock()

		if p == nil || closed {
			return
		}

		n, err := p.Read(buf)
		if err != nil {
			if t.ctx != nil && !t.closed {
				runtime.EventsEmit(t.ctx, "terminal:exit", TerminalEvent{SessionID: t.sessionID, Data: err.Error()})
			}
			// Stop polling when PTY exits
			t.stopTmuxPolling()
			return
		}

		// In polling mode, we discard PTY output.
		// Display updates come from polling tmux directly.
		// This prevents TUI escape sequences from corrupting xterm scrollback.
		_ = n
	}
}

// startTmuxPolling begins polling tmux for display updates.
func (t *Terminal) startTmuxPolling(tmuxSession string, rows int) {
	t.tmuxPolling = true
	t.tmuxStopChan = make(chan struct{})
	t.tmuxLastState = ""
	t.historyTracker = NewHistoryTracker(tmuxSession, rows)

	go t.pollTmuxLoop()
}

// stopTmuxPolling stops the polling goroutine.
func (t *Terminal) stopTmuxPolling() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tmuxPolling && t.tmuxStopChan != nil {
		close(t.tmuxStopChan)
		t.tmuxPolling = false
		t.tmuxSession = ""
		t.tmuxLastState = ""
		t.historyTracker = nil
		t.remoteHostID = "" // Clear remote context
	}
}

// pollTmuxLoop continuously polls tmux for display updates.
func (t *Terminal) pollTmuxLoop() {
	ticker := time.NewTicker(80 * time.Millisecond) // ~12.5 fps
	defer ticker.Stop()

	for {
		select {
		case <-t.tmuxStopChan:
			return
		case <-ticker.C:
			t.pollTmuxOnce()
		}
	}
}

// pollTmuxOnce captures current tmux pane and emits any changes.
// Uses two-phase approach:
// 1. Fetch any history gap (lines that scrolled off between polls)
// 2. Diff viewport for in-place updates (spinners, progress bars)
func (t *Terminal) pollTmuxOnce() {
	t.mu.Lock()
	session := t.tmuxSession
	polling := t.tmuxPolling
	tracker := t.historyTracker
	t.mu.Unlock()

	if !polling || session == "" || tracker == nil {
		return
	}

	// Step 1: Get tmux info - history size and alt-screen status
	historySize, inAltScreen, err := tracker.GetTmuxInfo()
	if err != nil {
		t.debugLog("[POLL] GetTmuxInfo error: %v", err)
		// Session might have ended
		if t.ctx != nil {
			runtime.EventsEmit(t.ctx, "terminal:exit", TerminalEvent{SessionID: t.sessionID, Data: "tmux session ended"})
		}
		t.stopTmuxPolling()
		return
	}

	// Step 2: Track alt-screen state changes (vim, less, htop)
	if inAltScreen != tracker.inAltScreen {
		t.debugLog("[POLL] Alt-screen changed: %v -> %v", tracker.inAltScreen, inAltScreen)
		tracker.SetAltScreen(inAltScreen)

		// Notify frontend of alt-screen state change for mouse handling
		if t.ctx != nil {
			runtime.EventsEmit(t.ctx, "terminal:altscreen", map[string]interface{}{
				"sessionId":   t.sessionID,
				"inAltScreen": inAltScreen,
			})
		}
	}

	// Step 3: If NOT in alt-screen, fetch any history gap
	// (Don't append TUI frames to scrollback when in vim/less)
	if !inAltScreen && historySize > 0 {
		gap, err := tracker.FetchHistoryGap(historySize)
		if err != nil {
			t.debugLog("[POLL] FetchHistoryGap error: %v", err)
		} else if len(gap) > 0 {
			// Append gap lines to xterm (blind, no diff - these are history)
			t.debugLog("[POLL] Appending %d bytes of history gap (historySize=%d)", len(gap), historySize)
			if t.ctx != nil {
				runtime.EventsEmit(t.ctx, "terminal:data", TerminalEvent{SessionID: t.sessionID, Data: gap})
			}
		}
	}

	// Step 4: Capture current viewport
	cmd := exec.Command("tmux", "capture-pane", "-t", session, "-p", "-e")
	output, err := cmd.Output()
	if err != nil {
		t.debugLog("[POLL] capture-pane error: %v", err)
		return
	}

	currentState := string(output)

	// Step 5: Only process if viewport changed
	t.mu.Lock()
	lastState := t.tmuxLastState
	t.mu.Unlock()

	if currentState != lastState {
		t.mu.Lock()
		t.tmuxLastState = currentState
		t.mu.Unlock()

		if t.ctx != nil {
			content := stripTTSMarkers(currentState)

			// Step 6: Diff viewport and emit minimal updates
			updateSequence := tracker.DiffViewport(content)

			if len(updateSequence) > 0 {
				lines := strings.Count(content, "\n")
				t.debugLog("[POLL] Viewport diff: %d bytes update, %d content lines, historySize=%d, altScreen=%v",
					len(updateSequence), lines, historySize, inAltScreen)
				runtime.EventsEmit(t.ctx, "terminal:data", TerminalEvent{SessionID: t.sessionID, Data: updateSequence})
			}
		}
	}
}

// sanitizeHistoryForXterm removes escape sequences that would interfere
// with scrollback accumulation while preserving colors.
func sanitizeHistoryForXterm(content string) string {
	// Remove cursor positioning (conflicts with scrollback)
	content = cursorHomeRe.ReplaceAllString(content, "")    // Cursor home
	content = cursorPosRe.ReplaceAllString(content, "")     // Cursor position
	content = cursorPosAltRe.ReplaceAllString(content, "")  // Cursor position (alternate)
	content = cursorMoveRe.ReplaceAllString(content, "")    // Cursor movement (up/down/forward/back)
	content = cursorLineColRe.ReplaceAllString(content, "") // Cursor next/prev line, column absolute

	// Remove cursor visibility and style (can cause rendering glitches on scroll)
	content = cursorVisRe.ReplaceAllString(content, "")   // Show/hide cursor
	content = cursorStyleRe.ReplaceAllString(content, "") // Cursor style (block, underline, bar)

	// Remove screen clearing
	content = clearScreenRe.ReplaceAllString(content, "")    // Clear screen
	content = clearToEndRe.ReplaceAllString(content, "")     // Clear to end of screen
	content = clearScreenVarRe.ReplaceAllString(content, "") // Clear screen variants
	content = clearLineEndRe.ReplaceAllString(content, "")   // Clear to end of line
	content = clearLineVarRe.ReplaceAllString(content, "")   // Clear line variants
	content = strings.ReplaceAll(content, "\x1bc", "")       // Full reset

	// Remove alternate screen buffer switches
	content = altScreenXtermRe.ReplaceAllString(content, "") // xterm alt screen
	content = altScreenDECRe.ReplaceAllString(content, "")   // DEC alt screen

	// Remove cursor save/restore
	content = saveCursorANSIRe.ReplaceAllString(content, "")    // Save cursor (ANSI)
	content = restoreCursorANSIRe.ReplaceAllString(content, "") // Restore cursor (ANSI)
	content = saveCursorDECRe.ReplaceAllString(content, "")     // Save cursor (DEC)
	content = restoreCursorDECRe.ReplaceAllString(content, "")  // Restore cursor (DEC)

	// Remove scroll region setting (can interfere with xterm buffer)
	content = scrollRegionRe.ReplaceAllString(content, "") // Set scroll region

	// KEEP: SGR color codes (\x1b[...m) - users want colored history

	return content
}

// normalizeCRLF converts line endings to CRLF for proper xterm rendering.
func normalizeCRLF(content string) string {
	// First normalize to LF only
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	// Then convert to CRLF
	content = strings.ReplaceAll(content, "\n", "\r\n")
	return content
}

// readLoopWithSeamFilter reads from PTY and emits to frontend,
// filtering destructive sequences from the initial output to prevent
// the "seam" glitch where tmux might clear the pre-loaded history.
func (t *Terminal) readLoopWithSeamFilter() {
	buf := make([]byte, 32*1024)
	initialBytesProcessed := 0
	const seamFilterLimit = 4096 // Filter first 4KB of output

	for {
		t.mu.Lock()
		p := t.pty
		closed := t.closed
		t.mu.Unlock()

		if p == nil || closed {
			return
		}

		n, err := p.Read(buf)
		if err != nil {
			if t.ctx != nil && !t.closed {
				runtime.EventsEmit(t.ctx, "terminal:exit", TerminalEvent{SessionID: t.sessionID, Data: err.Error()})
			}
			return
		}

		if n > 0 && t.ctx != nil {
			output := string(buf[:n])

			// Filter destructive sequences from initial PTY output
			if initialBytesProcessed < seamFilterLimit {
				output = stripSeamSequences(output)
				initialBytesProcessed += n
				t.debugLog("[SEAM] Filtered %d bytes (total: %d)", n, initialBytesProcessed)
			}

			output = stripTTSMarkers(output)
			if len(output) > 0 {
				runtime.EventsEmit(t.ctx, "terminal:data", TerminalEvent{SessionID: t.sessionID, Data: output})
			}
		}
	}
}

// stripSeamSequences removes sequences that could destroy pre-loaded scrollback
// during the initial PTY attachment.
func stripSeamSequences(s string) string {
	// Full terminal reset - always strip (destructive to scrollback)
	s = strings.ReplaceAll(s, "\x1bc", "")

	// Alternate screen switches (should be disabled by tmux config, but safety)
	s = strings.ReplaceAll(s, "\x1b[?1049h", "")
	s = strings.ReplaceAll(s, "\x1b[?1049l", "")
	s = strings.ReplaceAll(s, "\x1b[?47h", "")
	s = strings.ReplaceAll(s, "\x1b[?47l", "")

	// Clear screen - remove to prevent overwriting scrollback
	s = strings.ReplaceAll(s, "\x1b[2J", "")

	// Cursor home - remove during seam to prevent cursor jumping to top
	s = strings.ReplaceAll(s, "\x1b[H", "")

	// Remove any cursor positioning sequences (ESC [ row ; col H or f)
	// These would cause content to overwrite wrong positions
	s = seamCursorPosRe.ReplaceAllString(s, "")
	s = seamCursorPosSimpleRe.ReplaceAllString(s, "")

	return s
}

// AttachTmux attaches to an existing tmux session (direct mode, no history preload).
// Prefer StartTmuxSession for the hybrid approach with scrollback support.
func (t *Terminal) AttachTmux(tmuxSession string, cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pty != nil {
		return nil // Already started
	}

	p, err := SpawnPTYWithCommand("tmux", "attach-session", "-t", tmuxSession)
	if err != nil {
		return err
	}

	// Set initial size
	if cols > 0 && rows > 0 {
		p.Resize(uint16(cols), uint16(rows))
	}

	t.pty = p
	t.tmuxSession = tmuxSession
	t.closed = false

	// Start reading from PTY and emitting to frontend
	go t.readLoop()

	return nil
}

// Write sends data to the PTY.
func (t *Terminal) Write(data string) error {
	t.mu.Lock()
	p := t.pty
	t.mu.Unlock()

	if p == nil {
		return nil
	}

	_, err := p.Write([]byte(data))
	return err
}

// Resize changes the PTY dimensions and resizes tmux window if in hybrid mode.
func (t *Terminal) Resize(cols, rows int) error {
	t.mu.Lock()
	p := t.pty
	session := t.tmuxSession
	tracker := t.historyTracker
	remoteHost := t.remoteHostID
	sshBridge := t.sshBridge
	t.mu.Unlock()

	// Resize the PTY if attached
	if p != nil {
		if err := p.Resize(uint16(cols), uint16(rows)); err != nil {
			t.debugLog("[RESIZE] PTY resize error: %v", err)
		}
	}

	// Also resize tmux window if we're attached to a session
	if session != "" {
		if remoteHost != "" && sshBridge != nil {
			// Remote session - use SSH to resize
			tmuxPath := sshBridge.GetTmuxPath(remoteHost)
			resizeCmd := fmt.Sprintf("%s resize-window -t %q -x %d -y %d",
				tmuxPath, session, cols, rows)
			if _, err := sshBridge.RunCommand(remoteHost, resizeCmd); err != nil {
				t.debugLog("[RESIZE] Remote tmux resize-window error: %v", err)
			}
		} else {
			// Local session - use local tmux
			cmd := exec.Command("tmux", "resize-window", "-t", session, "-x", itoa(cols), "-y", itoa(rows))
			if err := cmd.Run(); err != nil {
				t.debugLog("[RESIZE] tmux resize-window error: %v", err)
			}
		}

		// Reset history tracker on resize - content will reflow at new width
		if tracker != nil {
			tracker.Reset()
			tracker.SetViewportRows(rows)
			t.debugLog("[RESIZE] Reset history tracker for new dimensions %dx%d", cols, rows)
		}
	}

	return nil
}

// GetScrollback fetches fresh scrollback content from tmux.
// Called by frontend after resize to bypass xterm.js reflow issues.
// Returns sanitized content ready for xterm.write().
func (t *Terminal) GetScrollback() (string, error) {
	t.mu.Lock()
	session := t.tmuxSession
	t.mu.Unlock()

	if session == "" {
		t.debugLog("[SCROLLBACK] No tmux session attached")
		return "", nil
	}

	// Wait briefly for tmux to finish reflowing after resize
	time.Sleep(30 * time.Millisecond)

	// Capture current pane content (scrollback + visible)
	cmd := exec.Command("tmux", "capture-pane", "-t", session, "-p", "-e", "-S", "-", "-E", "-")
	output, err := cmd.Output()
	if err != nil {
		t.debugLog("[SCROLLBACK] capture-pane error: %v", err)
		return "", err
	}

	rawContent := string(output)
	t.debugLog("[SCROLLBACK] Raw tmux output: %d bytes", len(rawContent))

	// Check for box-drawing characters in raw output
	boxDrawingCount := 0
	questionMarkCount := 0
	for _, r := range rawContent {
		if r == '─' || r == '│' || r == '┌' || r == '┐' || r == '└' || r == '┘' || r == '├' || r == '┤' || r == '┬' || r == '┴' || r == '┼' {
			boxDrawingCount++
		}
		if r == '?' {
			questionMarkCount++
		}
	}
	t.debugLog("[SCROLLBACK] Box-drawing chars: %d, Question marks: %d", boxDrawingCount, questionMarkCount)

	// Log a sample of lines containing box-drawing chars
	lines := strings.Split(rawContent, "\n")
	for i, line := range lines {
		if strings.ContainsAny(line, "─│┌┐└┘├┤┬┴┼") {
			// Log first 100 chars of this line
			sample := line
			if len(sample) > 100 {
				sample = sample[:100] + "..."
			}
			t.debugLog("[SCROLLBACK] Line %d with box chars: %q", i, sample)
			break // Just log first one
		}
	}

	// Sanitize for xterm
	content := sanitizeHistoryForXterm(rawContent)
	content = normalizeCRLF(content)

	t.debugLog("[SCROLLBACK] After sanitize: %d bytes", len(content))
	return content, nil
}

// Close terminates the PTY and stops polling.
func (t *Terminal) Close() error {
	// Stop polling first (uses its own lock)
	t.stopTmuxPolling()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.closed = true
	t.tmuxSession = ""

	if t.pty != nil {
		err := t.pty.Close()
		t.pty = nil
		return err
	}
	return nil
}

// readLoop continuously reads from PTY and emits data to frontend.
func (t *Terminal) readLoop() {
	buf := make([]byte, 32*1024)

	for {
		t.mu.Lock()
		p := t.pty
		closed := t.closed
		t.mu.Unlock()

		if p == nil || closed {
			return
		}

		n, err := p.Read(buf)
		if err != nil {
			if t.ctx != nil && !t.closed {
				runtime.EventsEmit(t.ctx, "terminal:exit", TerminalEvent{SessionID: t.sessionID, Data: err.Error()})
			}
			return
		}

		if n > 0 && t.ctx != nil {
			output := stripTTSMarkers(string(buf[:n]))
			if len(output) > 0 {
				runtime.EventsEmit(t.ctx, "terminal:data", TerminalEvent{SessionID: t.sessionID, Data: output})
			}
		}
	}
}

// itoa converts int to string (simple helper to avoid strconv import).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
