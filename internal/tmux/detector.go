package tmux

import (
	"strings"
)

// SessionState represents the detected state of a session
type SessionState string

const (
	StateIdle    SessionState = "idle"    // No activity, waiting for user
	StateBusy    SessionState = "busy"    // Actively working (output changing)
	StateWaiting SessionState = "waiting" // Showing a prompt, needs input
)

// =============================================================================
// Prompt Detector - Detects tool-specific prompts (from Claude Squad source)
// =============================================================================

// PromptDetector checks for tool-specific prompts in terminal content
// Based on Claude Squad's exact implementation:
// https://github.com/smtg-ai/claude-squad/blob/main/session/tmux/tmux.go
type PromptDetector struct {
	tool string
}

// NewPromptDetector creates a detector for the specified tool
func NewPromptDetector(tool string) *PromptDetector {
	return &PromptDetector{tool: strings.ToLower(tool)}
}

// HasPrompt checks if the terminal content contains a prompt waiting for input
// These patterns are derived from Claude Squad + additional research for edge cases
func (d *PromptDetector) HasPrompt(content string) bool {
	switch d.tool {
	case "claude":
		return d.hasClaudePrompt(content)

	case "opencode":
		// OpenCode TUI - the UI is always visible (input box, mode tabs, logo),
		// so we MUST check busy indicators first. If opencode is actively working,
		// return false to let the busy detector handle status correctly.
		//
		// Busy indicators (from opencode source: internal/tui/components/chat/list.go):
		//   - Help bar shows "esc" when busy (to cancel), vs "enter" when idle (to send)
		//   - Pulse spinner: █ ▓ ▒ ░ (spinner.Pulse, 125ms cycle)
		//   - Task strings: "Thinking...", "Generating...", "Building tool call...",
		//     "Waiting for tool response...", "Loading..."
		if d.hasOpencodeBusyIndicator(content) {
			return false
		}
		// Idle: check for opencode-specific prompt patterns
		// "press enter to send" only appears when idle (help bar text)
		// "Ask anything" is the input placeholder
		return strings.Contains(content, "press enter to send") ||
			strings.Contains(content, "Ask anything") ||
			strings.Contains(content, "open code") ||
			d.hasLineEndingWith(content, ">")

	case "gemini":
		return d.hasGeminiPrompt(content)

	case "codex":
		// Codex/OpenAI CLI patterns.
		// Busy indicators take priority over prompt markers.
		lower := strings.ToLower(content)
		if strings.Contains(lower, "esc to interrupt") ||
			strings.Contains(lower, "ctrl+c to interrupt") {
			return false
		}
		return strings.Contains(content, "codex>") ||
			strings.Contains(content, "Continue?") ||
			d.hasLineEndingWith(content, ">")

	default:
		// Generic shell - check for common prompts
		return d.hasShellPrompt(content)
	}
}

// hasClaudePrompt detects if Claude Code is waiting for input
// Handles BOTH normal mode AND --dangerously-skip-permissions mode
//
// Claude Code UI States (from research):
// - BUSY: Shows "ctrl+c to interrupt" (2024+) or "esc to interrupt" (older) with spinner
// - WAITING (normal mode): Shows permission dialogs with Yes/No options
// - WAITING (--dangerously-skip-permissions): Shows just ">" prompt
// - THINKING: Extended reasoning mode with "think"/"think harder" keywords
// - AUTO-ACCEPT: Toggled via Shift+Tab, auto-applies edits
//
// References:
// - Claude Squad: github.com/smtg-ai/claude-squad
// - CCManager state detection
// - cli-spinners: github.com/sindresorhus/cli-spinners (dots spinner)
func (d *PromptDetector) hasClaudePrompt(content string) bool {
	// Get last 15 lines for analysis (increased from 10 for better context)
	lines := strings.Split(content, "\n")
	var lastLines []string
	for i := len(lines) - 1; i >= 0 && len(lastLines) < 15; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastLines = append([]string{lines[i]}, lastLines...)
		}
	}
	recentContent := strings.Join(lastLines, "\n")
	recentLower := strings.ToLower(recentContent)

	// ═══════════════════════════════════════════════════════════════════════
	// BUSY indicators (if these are present, Claude is NOT waiting)
	// Priority: Check busy state FIRST - if busy, definitely not waiting
	// ═══════════════════════════════════════════════════════════════════════
	busyIndicators := []string{
		"ctrl+c to interrupt", // PRIMARY - current Claude Code (2024+)
		"esc to interrupt",    // FALLBACK - older versions
	}
	for _, indicator := range busyIndicators {
		if strings.Contains(recentLower, indicator) {
			return false // Claude is BUSY, not waiting
		}
	}

	// Check for spinner characters in last 3 lines (indicates active processing)
	// Includes braille spinner chars (cli-spinners "dots") AND asterisk spinners (Claude 2.1.25+)
	spinnerChars := []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
		"✳", "✽", "✶", "✢", // Claude 2.1.25+ asterisk spinner chars
	}
	// Check last 10 lines (spinner can be further up due to tip lines, borders, status bar)
	last10Lines := lastLines
	if len(last10Lines) > 10 {
		last10Lines = last10Lines[len(last10Lines)-10:]
	}
	for _, line := range last10Lines {
		// Skip lines starting with box-drawing characters (UI borders)
		trimmedLine := strings.TrimSpace(line)
		if len(trimmedLine) > 0 {
			r := []rune(trimmedLine)[0]
			if r == '│' || r == '├' || r == '└' || r == '─' || r == '┌' || r == '┐' || r == '┘' || r == '┤' || r == '┬' || r == '┴' || r == '┼' || r == '╭' || r == '╰' || r == '╮' || r == '╯' {
				continue
			}
		}
		for _, spinner := range spinnerChars {
			if strings.Contains(line, spinner) {
				// Spinner present in recent output = actively working
				return false
			}
		}
	}

	// Check for timing indicators that show Claude is processing
	// Claude 2.1.25+ uses whimsical words (90+ words like "Hullaballooing", "Clauding", etc.)
	// with unicode ellipsis: "✢ Hullaballooing… (53s · ↓ 749 tokens)"
	// Check for the universal pattern: unicode ellipsis + "tokens" in recent content
	if strings.Contains(recentLower, "…") && strings.Contains(recentLower, "tokens") {
		return false // Actively processing (any whimsical word with timing info)
	}
	// Legacy patterns (pre-2.1.25)
	if strings.Contains(recentLower, "thinking") && strings.Contains(recentLower, "tokens") {
		return false // Actively thinking
	}
	if strings.Contains(recentLower, "connecting") && strings.Contains(recentLower, "tokens") {
		return false // Connecting state
	}

	// ═══════════════════════════════════════════════════════════════════════
	// WAITING indicators - Permission prompts (normal mode)
	// ═══════════════════════════════════════════════════════════════════════
	permissionPrompts := []string{
		// From Claude Squad (most reliable indicator)
		"No, and tell Claude what to do differently",
		// Permission dialog options
		"Yes, allow once",
		"Yes, allow always",
		"Allow once",
		"Allow always",
		// Box-drawing permission dialogs
		"│ Do you want",
		"│ Would you like",
		"│ Allow",
		// Selection indicators
		"❯ Yes",
		"❯ No",
		"❯ Allow",
		// Trust prompt on startup
		"Do you trust the files in this folder?",
		// MCP permission prompts
		"Allow this MCP server",
		// Tool permission prompts
		"Run this command?",
		"Execute this?",
		"Action Required",
		"Waiting for user confirmation",
		"Allow execution of",
		// AskUserQuestion / interactive question UI
		// Claude Code renders selection options with these indicators
		"Use arrow keys to navigate",
		"Press Enter to select",
	}
	for _, prompt := range permissionPrompts {
		if strings.Contains(content, prompt) {
			return true
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// WAITING indicators - Input prompt (--dangerously-skip-permissions mode)
	// In this mode, Claude just shows ">" when waiting for next input
	// This is the PRIMARY detection method for skip-permissions mode
	// ═══════════════════════════════════════════════════════════════════════

	// Check if last non-empty line is the input prompt
	if len(lastLines) > 0 {
		lastLine := strings.TrimSpace(lastLines[len(lastLines)-1])

		// Strip ANSI codes from last line for accurate matching
		cleanLastLine := StripANSI(lastLine)
		cleanLastLine = strings.TrimSpace(cleanLastLine)

		// Claude Code shows just ">" or "❯" when waiting for input
		// Note: Claude Code uses "❯" (Unicode U+276F), not ASCII ">"
		// This is the standard prompt in --dangerously-skip-permissions mode
		if cleanLastLine == ">" || cleanLastLine == "❯" {
			return true
		}

		// Also check for "> " or "❯ " (with trailing space/cursor position)
		if cleanLastLine == "> " || cleanLastLine == "❯ " {
			return true
		}

		// Check for prompt with partial user input (user started typing)
		// Pattern: "> some text" or "❯ some text" where user is typing
		if (strings.HasPrefix(cleanLastLine, "> ") || strings.HasPrefix(cleanLastLine, "❯ ")) && !strings.Contains(cleanLastLine, "esc") {
			// Make sure it's not a quote or output line
			// Real prompts are short (user input in progress)
			if len(cleanLastLine) < 100 {
				return true
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// WAITING indicators - Prompt in recent lines (not just last line)
	// Claude Code's UI has status bar AFTER the prompt, so check last 5 lines
	// ═══════════════════════════════════════════════════════════════════════
	checkLines := lastLines
	if len(checkLines) > 5 {
		checkLines = checkLines[len(checkLines)-5:]
	}
	for _, line := range checkLines {
		cleanLine := strings.TrimSpace(StripANSI(line))
		// Normalize non-breaking spaces (U+00A0) to regular spaces
		// Claude Code uses NBSP after the prompt character
		cleanLine = strings.ReplaceAll(cleanLine, "\u00A0", " ")
		// Check for standalone prompt character (user hasn't typed yet)
		if cleanLine == ">" || cleanLine == "❯" || cleanLine == "> " || cleanLine == "❯ " {
			return true
		}
		// Check for prompt with suggestion (Claude shows "❯ Try..." when waiting)
		// This is Claude's suggestion feature - still means waiting for input
		if strings.HasPrefix(cleanLine, "❯ Try ") || strings.HasPrefix(cleanLine, "> Try ") {
			return true
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// WAITING indicators - Completion/question prompts
	// ═══════════════════════════════════════════════════════════════════════
	questionPrompts := []string{
		"Continue?",
		"Proceed?",
		"(Y/n)",
		"(y/N)",
		"[Y/n]",
		"[y/N]",
		"(yes/no)",
		"[yes/no]",
		// Plan mode prompts
		"Approve this plan?",
		"Execute plan?",
	}
	for _, prompt := range questionPrompts {
		if strings.Contains(recentContent, prompt) {
			return true
		}
	}

	// ═══════════════════════════════════════════════════════════════════════
	// WAITING indicators - Task completion signals
	// When Claude finishes a task, it shows summary and waits for next input
	// ═══════════════════════════════════════════════════════════════════════
	completionIndicators := []string{
		"Task completed",
		"Done!",
		"Finished",
		"What would you like",
		"What else",
		"Anything else",
		"Let me know if",
	}
	// Only check completion indicators if we also have the ">" prompt nearby
	hasCompletionIndicator := false
	for _, indicator := range completionIndicators {
		if strings.Contains(recentLower, strings.ToLower(indicator)) {
			hasCompletionIndicator = true
			break
		}
	}
	if hasCompletionIndicator {
		// Check if there's a ">" or "❯" in the last few lines
		completionCheckLines := lastLines
		if len(completionCheckLines) > 3 {
			completionCheckLines = completionCheckLines[len(completionCheckLines)-3:]
		}
		for _, line := range completionCheckLines {
			cleanLine := strings.TrimSpace(StripANSI(line))
			if cleanLine == ">" || cleanLine == "> " || cleanLine == "❯" || cleanLine == "❯ " {
				return true
			}
		}
	}

	return false
}

// hasGeminiPrompt detects if Gemini CLI is waiting for input.
// Checks last 10 non-blank lines for known Gemini prompt patterns.
func (d *PromptDetector) hasGeminiPrompt(content string) bool {
	lines := strings.Split(content, "\n")
	var lastLines []string
	for i := len(lines) - 1; i >= 0 && len(lastLines) < 10; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastLines = append([]string{line}, lastLines...)
		}
	}

	for _, line := range lastLines {
		// Direct prompt patterns
		if line == "gemini>" || strings.Contains(line, "gemini>") {
			return true
		}
		if strings.Contains(line, "Yes, allow once") {
			return true
		}
		if strings.Contains(line, "Type your message") {
			return true
		}
		// Generic trailing ">" prompt (common Gemini waiting state)
		if strings.HasSuffix(line, ">") {
			return true
		}
	}

	return false
}

// hasLineEndingWith checks if any recent line ends with the given suffix
func (d *PromptDetector) hasLineEndingWith(content string, suffix string) bool {
	lines := strings.Split(content, "\n")
	// Check last 5 lines
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}
	for i := len(lines) - 1; i >= start; i-- {
		line := strings.TrimSpace(lines[i])
		if line == suffix || strings.HasSuffix(line+" ", suffix+" ") {
			return true
		}
	}
	return false
}

// hasShellPrompt checks for generic shell prompts
func (d *PromptDetector) hasShellPrompt(content string) bool {
	// Check last few lines for shell prompt patterns
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return false
	}

	// Get last non-empty line
	var lastLine string
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			lastLine = trimmed
			break
		}
	}

	// Common shell prompt endings
	shellPrompts := []string{"$ ", "# ", "% ", "❯ ", "➜ ", "> "}
	for _, prompt := range shellPrompts {
		if strings.HasSuffix(lastLine+" ", prompt) {
			return true
		}
	}

	// Yes/No confirmation prompts anywhere in recent output
	confirmPatterns := []string{
		"(Y/n)", "[Y/n]", "(y/N)", "[y/N]",
		"(yes/no)", "[yes/no]",
		"Continue?", "Proceed?",
	}
	// Check last 5 lines for confirmation prompts
	checkLines := lines
	if len(checkLines) > 5 {
		checkLines = checkLines[len(checkLines)-5:]
	}
	recentContent := strings.Join(checkLines, "\n")
	for _, pattern := range confirmPatterns {
		if strings.Contains(recentContent, pattern) {
			return true
		}
	}

	return false
}

// hasOpencodeBusyIndicator checks if opencode's TUI shows signs of active processing.
// OpenCode uses a Bubble Tea TUI with these busy signals:
//   - Help bar: "esc" to cancel (only during processing)
//   - Pulse spinner: █ ▓ ▒ ░ (cycles at 125ms)
//   - Task text: "Thinking...", "Generating...", etc.
func (d *PromptDetector) hasOpencodeBusyIndicator(content string) bool {
	// "esc interrupt" or "esc to exit" in help bar = processing
	if strings.Contains(content, "esc interrupt") || strings.Contains(content, "esc to exit") {
		return true
	}
	// Pulse spinner characters (spinner.Pulse from Bubble Tea)
	// These only appear on the spinner line when opencode is actively working
	pulseChars := []string{"█", "▓", "▒", "░"}
	for _, ch := range pulseChars {
		if strings.Contains(content, ch) {
			return true
		}
	}
	// Task text patterns shown next to the spinner
	busyStrings := []string{
		"Thinking...",
		"Generating...",
		"Building tool call...",
		"Waiting for tool response...",
	}
	for _, s := range busyStrings {
		if strings.Contains(content, s) {
			return true
		}
	}
	return false
}

// =============================================================================
// ANSI Stripping Utility
// =============================================================================

// StripANSI removes ANSI escape codes from content using O(n) single-pass algorithm.
// This is important because terminal output contains color codes.
//
// PERFORMANCE: Uses strings.Builder with pre-allocation for O(n) time complexity.
// Previous implementation used string concatenation in loops which was O(n²)
// and caused 2-11 second UI freezes on large terminal output (Issue #39).
//
// NOTE: We intentionally avoid regex here because complex ANSI regex patterns
// can cause catastrophic backtracking on malformed escape sequences.
func StripANSI(content string) string {
	// Fast path: if no escape chars, return as-is
	// Note: Using IndexByte instead of ContainsAny to avoid UTF-8 validation issues
	// \x1b is ESC, \x9B is CSI (C1 control character)
	if strings.IndexByte(content, '\x1b') < 0 && strings.IndexByte(content, '\x9B') < 0 {
		return content
	}

	var b strings.Builder
	b.Grow(len(content)) // Pre-allocate to avoid reallocations

	i := 0
	for i < len(content) {
		// Check for ESC character
		if content[i] == '\x1b' {
			// CSI sequence: ESC [ ... letter
			if i+1 < len(content) && content[i+1] == '[' {
				j := i + 2
				// Skip until we find the terminating letter
				for j < len(content) {
					c := content[j]
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						j++
						break
					}
					j++
				}
				i = j
				continue
			}
			// OSC sequence: ESC ] ... BEL
			if i+1 < len(content) && content[i+1] == ']' {
				// Find BEL terminator
				bellPos := strings.Index(content[i:], "\x07")
				if bellPos != -1 {
					i += bellPos + 1
					continue
				}
				// No BEL found - find ST (ESC \) as alternative terminator
				stPos := strings.Index(content[i:], "\x1b\\")
				if stPos != -1 {
					i += stPos + 2
					continue
				}
			}
			// Other escape sequence: ESC followed by single char
			if i+1 < len(content) {
				i += 2
				continue
			}
		}
		// Check for CSI without ESC (8-bit CSI: 0x9B)
		if content[i] == '\x9B' {
			j := i + 1
			for j < len(content) {
				c := content[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		// Regular character - copy to output
		b.WriteByte(content[i])
		i++
	}

	return b.String()
}
