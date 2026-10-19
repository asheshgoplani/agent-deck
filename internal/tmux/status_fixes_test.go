package tmux

// =============================================================================
// STATUS LIGHT FIXES - REGRESSION TESTS
// =============================================================================
// These tests pin fixes for status light edge cases:
// - a spinner on screen is a busy indicator
// - Fix 2.1: Progress bar normalization (prevents flicker from dynamic content)
//
// They replace log-only "validation" tests written while the fixes were being
// designed; those printed what they saw and could not fail. The whimsical
// word ones are gone: busy detection no longer depends on a word list (the
// spinner + ellipsis pattern and the interrupt hint are word-independent).

import (
	"strings"
	"testing"
	"time"
)

// A braille spinner on screen means the agent is working.
func TestSpinnerIsBusyIndicator(t *testing.T) {
	sess := NewSession("validate-spinner", "/tmp")
	sess.Command = "claude"
	content := "Processing your request...\n\n⠋ Loading...\n"
	if !sess.hasBusyIndicator(content) {
		t.Fatalf("spinner screen not detected as busy: %q", content)
	}
}

// Fix 2.1: progress output that changes every tick normalizes to the same
// content, so the hash stays stable and the status light does not flicker.
func TestNormalizeContent_ProgressIndicatorsKeepHashStable(t *testing.T) {
	sess := NewSession("validate-progress", "/tmp")
	for _, tc := range []struct{ name, before, after string }{
		{"progress bar percentage", "Installing packages [======>     ] 45%", "Installing packages [========>   ] 67%"},
		{"download progress", "Downloading... 1.2MB/5.6MB", "Downloading... 3.4MB/5.6MB"},
		{"simple percentage", "Processing: 25% complete", "Processing: 50% complete"},
	} {
		n1, n2 := sess.normalizeContent(tc.before), sess.normalizeContent(tc.after)
		if sess.hashContent(n1) != sess.hashContent(n2) {
			t.Errorf("%s: normalized content differs, the hash would change every tick:\n  %q -> %q\n  %q -> %q",
				tc.name, tc.before, n1, tc.after, n2)
		}
	}
	// Real changes still change the hash.
	if a, b := sess.normalizeContent("Installing packages 45%"), sess.normalizeContent("Build failed 45%"); a == b {
		t.Errorf("different text normalized to the same content: %q", a)
	}
}

// =============================================================================
// VALIDATION 4.0: Claude Code Busy Pattern Detection (ctrl+c to interrupt)
// =============================================================================
// Current bug: Code checks for "esc to interrupt" but Claude Code shows "ctrl+c to interrupt"
// Expected: "ctrl+c to interrupt" should trigger busy detection
// This causes false negatives - Claude shows as idle when it's actually working

// TestClaudeCodeBusyPatterns tests the simplified busy indicator detection
func TestClaudeCodeBusyPatterns(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantBusy bool
	}{
		{
			name: "running - ctrl+c to interrupt visible",
			content: `Some previous output
✳ Enchanting… (ctrl+c to interrupt · 3m 17s · ↓ 3.1k tokens)
──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantBusy: true,
		},
		{
			name: "running - ctrl+c with thinking and todos",
			content: `Some output
✢ Channelling… (ctrl+c to interrupt · ctrl+t to hide todos · 2m 54s · ↓ 2.5k tokens · thinking)
❯`,
			wantBusy: true,
		},
		{
			name: "running - spinner character visible",
			content: `Working on something
⠙ Processing request...
❯`,
			wantBusy: true,
		},
		{
			name: "finished - Brewed message, no ctrl+c",
			content: `Some insight here

✻ Brewed for 3m 36s

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantBusy: false,
		},
		{
			name: "finished - Done message, no ctrl+c",
			content: `Output here
✻ Conjured for 1m 22s
❯`,
			wantBusy: false,
		},
		{
			name: "idle - tokens in skill loading output, no ctrl+c",
			content: `     └ using-superpowers: 47 tokens
     └ brainstorming: 56 tokens
     └ feature-dev:feature-dev: 25 tokens

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantBusy: false,
		},
		{
			name: "busy - esc to interrupt fallback for older Claude Code",
			content: `Some text mentioning esc to interrupt from docs
❯`,
			wantBusy: false, // Spinner-only: string patterns no longer used
		},
		{
			name:     "idle - just prompt",
			content:  `❯`,
			wantBusy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh session per test to avoid spinner grace period carryover
			sess := &Session{DisplayName: "test-" + tt.name}
			got := sess.hasBusyIndicator(tt.content)
			if got != tt.wantBusy {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.wantBusy, tt.content)
			}
		})
	}
}

// =============================================================================
// VALIDATION 4.1: "esc to interrupt" Only Matched in Status Bar Lines
// =============================================================================
// Bug: conductor output containing "esc to interrupt" in prose (e.g.
// "I is processing (esc to interrupt = running)") caused the session to be
// detected as busy/running even when Claude was waiting at the ❯ prompt.
// Fix: interrupt patterns are now only checked against the last 3 lines of
// the pane (the status bar area), not the full 25-line window.

func TestEscToInterruptOnlyMatchedInStatusBar(t *testing.T) {
	// separatorLine is the horizontal rule Claude Code draws above/below the prompt.
	const separatorLine = "────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────"

	tests := []struct {
		name     string
		content  string
		wantBusy bool
	}{
		{
			// Real busy: "esc to interrupt" appears in the last status-bar line,
			// together with the bypass-permissions affordance.
			name: "busy - esc to interrupt in status bar with bypass permissions",
			content: `Some previous output

` + separatorLine + `
❯
` + separatorLine + `
  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt                9% until auto-compact`,
			wantBusy: true,
		},
		{
			// Real busy: bare "esc to interrupt" on its own as last status-bar line
			// (older Claude Code versions).
			name: "busy - esc to interrupt alone in status bar",
			content: `Working on your request...
` + separatorLine + `
❯
` + separatorLine + `
esc to interrupt`,
			wantBusy: true,
		},
		{
			// Real busy: accept-edits variant.
			name: "busy - esc to interrupt in accept edits status bar",
			content: `Editing file...

` + separatorLine + `
❯
` + separatorLine + `
  ⏵⏵ accept edits on (shift+tab to cycle) · esc to interrupt                      9% until auto-compact`,
			wantBusy: true,
		},
		{
			// False positive regression: conductor prose output containing
			// "esc to interrupt" with parentheses is far above the status bar.
			// Before the fix this triggered busy detection.
			name: "not busy - conductor prose mentioning esc to interrupt with parens",
			content: `● I is processing (esc to interrupt = running). Verified.

  ⚡ 2

  · A    review loop BLA-840 (@assessor)
  · B    running     BLA-838 billing tests
  NEED: D stuck 4+ heartbeats.

✻ Brewed for 2m 12s · 2 shells still running

` + separatorLine + `
❯
` + separatorLine + `
  ⏵⏵ bypass permissions on · 2 shells · ↓ to manage`,
			wantBusy: false,
		},
		{
			// Another prose variant: model output quoting the phrase.
			name: "not busy - model output quoting esc to interrupt in body",
			content: `To interrupt a running task, press (esc to interrupt) in the terminal.

✻ Conjured for 1m 22s

` + separatorLine + `
❯
` + separatorLine,
			wantBusy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// detectedTool must be set so Claude string patterns ("esc to interrupt") are loaded.
			sess := &Session{DisplayName: "test-" + tt.name, detectedTool: "claude"}
			got := sess.hasBusyIndicator(tt.content)
			if got != tt.wantBusy {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.wantBusy, tt.content)
			}
		})
	}
}

// =============================================================================
// VALIDATION 5.0: thinkingPattern Requires Spinner Prefix
// =============================================================================
// Fix: thinkingPattern now requires a braille spinner character prefix
// to avoid matching normal English words like "processing" or "computing"

func TestThinkingPatternRequiresSpinner(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "spinner prefix matches",
			content: "⠋ Thinking... (25s · 340 tokens)",
			want:    true,
		},
		{
			name:    "different spinner matches",
			content: "⠸ Clauding... (10s · 100 tokens)",
			want:    true,
		},
		{
			name:    "spinner with extra space",
			content: "⠹  Computing... (5s · 50 tokens)",
			want:    true,
		},
		{
			name:    "no spinner prefix - should NOT match",
			content: "Processing... (25s · 340 tokens)",
			want:    false,
		},
		{
			name:    "bare word in normal text - should NOT match",
			content: "We are computing the result (total: 42)",
			want:    false,
		},
		{
			name:    "whimsical word without spinner - should NOT match",
			content: "Flibbertigibbeting... (25s · 340 tokens)",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := thinkingPattern.MatchString(tt.content)
			if got != tt.want {
				t.Errorf("thinkingPattern.MatchString(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

// =============================================================================
// VALIDATION 5.1: Spinner Check Skips Box-Drawing Lines
// =============================================================================
// Fix: Lines starting with box-drawing characters (│├└ etc.) are skipped
// in the spinner char check to prevent false GREEN from UI borders

func TestSpinnerCheckSkipsBoxDrawingLines(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantBusy bool
	}{
		{
			name: "spinner on normal line",
			content: `Some output
⠋ Processing request...`,
			wantBusy: true,
		},
		{
			name: "spinner-like char in box-drawing line",
			content: `│ Some box content ⠋
├ More content
└ End`,
			wantBusy: false, // Box-drawing lines should be skipped
		},
		{
			name: "box-drawing only with no real spinner",
			content: `╭─────────────────────────────╮
│ ⠋ This is a box border      │
╰─────────────────────────────╯`,
			wantBusy: false,
		},
		{
			name: "real spinner after box-drawing lines",
			content: `│ Some box content
⠙ Loading modules`,
			wantBusy: true, // The real spinner is on a non-box line
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh session per test to avoid spinner grace period carryover
			sess := NewSession("box-drawing-"+tt.name, "/tmp")
			sess.Command = "claude"
			got := sess.hasBusyIndicator(tt.content)
			if got != tt.wantBusy {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.wantBusy, tt.content)
			}
		})
	}
}

// =============================================================================
// FIX: pi "── ⠹ Working ──" banner detected as busy
// =============================================================================
// pi renders its activity indicator as a horizontal-rule banner with a braille
// spinner leading the label ("── ⠹ Working ──────"), pinned to the top of its
// UI region. The spinner check used to (a) skip any line starting with a
// box-drawing character and (b) only inspect the last 10 lines, so a genuinely
// working pi session was classified not-busy → "waiting". Reported live
// 2026-09-06 on headless soc-dev-server pi sessions (daemon probe logged
// busy_no_spinner while the pane showed the Working banner).
func TestPiWorkingBannerDetectedAsBusy(t *testing.T) {
	banner := "── ⠹ Working ──────────────────────────────────────────────"
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "pi banner near bottom of short pane",
			content: "some prose\n" + banner + "\n/var/www/servermanager (saurabh)\n↑1k ↓2k R3M 12.0%/1.0M (auto)",
			want:    true,
		},
		{
			name:    "pi banner at top of tall pane (output below)",
			content: banner + "\n" + strings.Repeat("output line\n", 30) + "footer row",
			want:    true,
		},
		{
			name:    "pi idle prompt has no banner",
			content: "awaiting your input\n\n/var/www/servermanager (saurabh)\n↑1k ↓2k R3M 12.0%/1.0M (auto)   (deepseek) deepseek-v4-flash • high",
			want:    false,
		},
		{
			name:    "vertical box with decorative spinner still skipped",
			content: "│ ⠹ decorative content │",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh session per test to avoid spinner grace period carryover.
			sess := &Session{DisplayName: "pi-" + tt.name, detectedTool: "pi"}
			if got := sess.hasBusyIndicator(tt.content); got != tt.want {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.want, tt.content)
			}
		})
	}
}

// =============================================================================
// VALIDATION 6.0: Claude Code 2.1.25+ Active Spinner Detection
// =============================================================================
// Claude Code 2.1.25 removed "ctrl+c to interrupt" from the status line.
// Active state: spinner symbol + word + unicode ellipsis (…)
// Done state: "✻ Worked for 54s" (no ellipsis)

func TestClaudeCode2125_ActiveDetection(t *testing.T) {
	// sess created per-test below to avoid grace period carryover
	_ = "claude" // tool hint

	tests := []struct {
		name     string
		content  string
		wantBusy bool
	}{
		// Active states (should be GREEN)
		{
			name:     "active - ✳ spinner with ellipsis",
			content:  "✳ Gusting… (35s · ↑ 673 tokens)",
			wantBusy: true,
		},
		{
			name:     "active - ✽ spinner with ellipsis and thinking",
			content:  "✽ Metamorphosing… (33s · ↑ 144 tokens · thinking)",
			wantBusy: true,
		},
		{
			name:     "active - · spinner with ellipsis",
			content:  "· Sublimating… (39s · ↓ 1.8k tokens)",
			wantBusy: true, // BusyRegexp catches · at line start with ellipsis as active work
		},
		{
			name:     "active - ✶ spinner with ellipsis",
			content:  "✶ Billowing… (41s · ↓ 720 tokens)",
			wantBusy: true,
		},
		{
			name:     "active - ✻ spinner with ellipsis",
			content:  "✻ Gusting… (43s · ↓ 914 tokens)",
			wantBusy: true, // BusyRegexp catches ✻ with ellipsis as active work
		},
		{
			name:     "active - ✢ spinner with ellipsis",
			content:  "✢ Channelling… (ctrl+c to interrupt · ctrl+t to hide todos · 2m 54s · ↓ 2.5k tokens · thinking)",
			wantBusy: true,
		},
		{
			name: "active - with surrounding content",
			content: `Some previous output here

✳ Cooking… (12s · ↑ 200 tokens)
──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantBusy: true,
		},
		{
			name:     "active - unknown future word with ellipsis",
			content:  "✳ Discombobulating… (5s · ↑ 50 tokens)",
			wantBusy: true,
		},
		// Multi-word task names (from TodoWrite tasks)
		{
			name:     "active - multi-word task with ✶",
			content:  "✶ Fixing Scanner Buffer Overflow… (1m 16s · ↓ 938 tokens)",
			wantBusy: true,
		},
		{
			name:     "active - multi-word task with ✻",
			content:  "✻ Adding mcp-proxy subcommand… (2m 23s · ↓ 2.7k tokens)",
			wantBusy: true, // BusyRegexp catches ✻ with ellipsis as active work
		},
		{
			name:     "active - multi-word task with ·",
			content:  "· Installing package dependencies… (45s · ↑ 312 tokens)",
			wantBusy: true, // BusyRegexp catches · at line start with ellipsis as active work
		},
		{
			name: "active - multi-word with surrounding content",
			content: `Some previous output

✻ Adding mcp-proxy subcommand… (2m 23s · ↓ 2.7k tokens)
  ✓ Fix scanner buffer overflow in socket_proxy.go
  ■ Add mcp-proxy reconnecting subcommand
  □ Build, test, and verify all changes

[Opus 4.5] Context: 54%
▶▶ bypass permissions on (shift+Tab to cycle) · 3 files +25 -3`,
			wantBusy: true, // BusyRegexp catches ✻ with ellipsis as active work
		},
		// Done states (should NOT be GREEN)
		{
			name:     "done - Worked for N seconds",
			content:  "✻ Worked for 54s",
			wantBusy: false,
		},
		{
			name:     "done - Churned for N seconds",
			content:  "✻ Churned for 47s",
			wantBusy: false,
		},
		{
			name: "done - Sautéed with prompt",
			content: `✻ Sautéed for 32s

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantBusy: false,
		},
		// Backward compat: old-style ctrl+c still works
		{
			name:     "backward compat - ctrl+c to interrupt",
			content:  "⠙ Thinking... (25s · 340 tokens · ctrl+c to interrupt)",
			wantBusy: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fresh session per test to avoid spinner grace period carryover
			sess := NewSession("claude-2125-"+tt.name, "/tmp")
			sess.Command = "claude"
			got := sess.hasBusyIndicator(tt.content)
			if got != tt.wantBusy {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.wantBusy, tt.content)
			}
		})
	}
}

func TestClaudeCode2125_NormalizeContent(t *testing.T) {
	sess := NewSession("claude-2125-normalize", "/tmp")

	// Different spinner states of the same content should normalize to the same hash
	contents := []string{
		"· Sublimating… (39s · ↓ 1.8k tokens)",
		"✳ Sublimating… (39s · ↓ 1.8k tokens)",
		"✽ Sublimating… (39s · ↓ 1.8k tokens)",
		"✶ Sublimating… (39s · ↓ 1.8k tokens)",
		"✻ Sublimating… (39s · ↓ 1.8k tokens)",
	}

	hashes := make([]string, len(contents))
	for i, c := range contents {
		hashes[i] = sess.hashContent(sess.normalizeContent(c))
	}

	// All should produce the same hash (spinner chars stripped, dynamic status normalized)
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("Hash mismatch: content[0] hash=%s, content[%d] hash=%s\n  content[0]: %q\n  content[%d]: %q",
				hashes[0], i, hashes[i], contents[0], i, contents[i])
		}
	}
}

func TestClaudeCode2125_SpinnerActiveRegex(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"✳ gusting…", true},
		{"· sublimating…", true},
		{"✻ cooking…", true},
		{"✢ channelling…", true},
		{"✶ Fixing Scanner Buffer Overflow…", true},  // multi-word task name
		{"✻ Adding mcp-proxy subcommand…", true},     // multi-word with excluded spinner
		{"· Installing package dependencies…", true}, // multi-word with · at line start
		{"✳ Running tests and building…", true},      // multi-word
		{"✻ worked for 54s", false},                  // done state, no ellipsis
		{"✻ churned for 47s", false},                 // done state, no ellipsis
		{"some random text…", false},                 // no spinner symbol
		{"✻ ", false},                                // no content after spinner
	}

	for _, tt := range tests {
		got := claudeSpinnerActivePattern.MatchString(tt.input)
		if got != tt.want {
			t.Errorf("claudeSpinnerActivePattern.MatchString(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// =============================================================================
// VALIDATION 7.0: Opencode Status Detection (#129)
// =============================================================================
// Bug: Opencode TUI elements (┃, Build, Plan) are always visible, so HasPrompt
// returned true even when opencode was busy processing. This caused GetStatus()
// to take the prompt path (which respects acknowledged flag) and lock into idle.
//
// Fix: Comprehensive busy guard using opencode's actual UI signals:
//   - "esc interrupt" / "esc to exit" in help bar (cancel action)
//   - Pulse spinner chars: █ ▓ ▒ ░ (Bubble Tea spinner.Pulse)
//   - Task strings: "Thinking...", "Generating...", etc.
// Idle detection now uses specific patterns ("press enter to send", "Ask anything")
// instead of overly broad matches like "Build" or "Plan".

func TestOpencodeBusyGuard(t *testing.T) {
	detector := NewPromptDetector("opencode")

	tests := []struct {
		name       string
		content    string
		wantPrompt bool
	}{
		// === BUSY states: HasPrompt must return false ===
		{
			name: "busy - esc interrupt with TUI elements",
			content: `┃ Ask anything
Build  Plan
Some output here...
press esc to exit cancel
┃`,
			wantPrompt: false,
		},
		{
			name: "busy - esc interrupt keyword",
			content: `opencode v1.2.3
┃ Processing your request...
esc interrupt
┃`,
			wantPrompt: false,
		},
		{
			name: "busy - pulse spinner █ (full block)",
			content: `┃
█ Thinking...
┃`,
			wantPrompt: false,
		},
		{
			name: "busy - pulse spinner ▓ (dark shade)",
			content: `┃
▓ Thinking...
┃`,
			wantPrompt: false,
		},
		{
			name: "busy - pulse spinner ▒ (medium shade)",
			content: `Some output
▒ Generating...`,
			wantPrompt: false,
		},
		{
			name:       "busy - pulse spinner ░ (light shade)",
			content:    `░ Waiting for tool response...`,
			wantPrompt: false,
		},
		{
			name: "busy - Thinking task text without spinner visible",
			content: `Some output
Thinking...
press enter to send`,
			wantPrompt: false,
		},
		{
			name:       "busy - Generating task text",
			content:    `Generating...`,
			wantPrompt: false,
		},
		{
			name:       "busy - Building tool call text",
			content:    `Building tool call...`,
			wantPrompt: false,
		},
		{
			name:       "busy - Waiting for tool response text",
			content:    `Waiting for tool response...`,
			wantPrompt: false,
		},
		{
			name: "busy - realistic opencode busy TUI",
			content: `┃ Some previous conversation                                    ┃
┃                                                                ┃
█ Thinking...
─────────────────────────────────────────────────
  Build   Plan
press esc to exit cancel                     ctrl+? help
┃                                                                ┃`,
			wantPrompt: false,
		},
		// === IDLE states: HasPrompt must return true ===
		{
			name: "idle - press enter to send (help bar)",
			content: `┃ Ask anything
Build  Plan
press enter to send the message`,
			wantPrompt: true,
		},
		{
			name:       "idle - Ask anything placeholder",
			content:    `Ask anything`,
			wantPrompt: true,
		},
		{
			name: "idle - open code logo",
			content: `open code
┃ Ask anything`,
			wantPrompt: true,
		},
		{
			name:       "idle - line ending with >",
			content:    `some prompt >`,
			wantPrompt: true,
		},
		{
			name: "idle - realistic opencode idle TUI",
			content: `┃ Here is the result of your request.                           ┃
┃                                                                ┃
─────────────────────────────────────────────────
  Build   Plan
┃ Ask anything                                                   ┃
press enter to send the message, write \ and enter to add a new line`,
			wantPrompt: true,
		},
		// === Edge cases ===
		{
			name:       "idle - opencode> prompt (line ending with >)",
			content:    `opencode>`,
			wantPrompt: true, // Matches hasLineEndingWith(">")
		},
		{
			name:       "idle - empty content",
			content:    ``,
			wantPrompt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.HasPrompt(tt.content)
			if got != tt.wantPrompt {
				t.Errorf("HasPrompt() = %v, want %v\nContent:\n%s", got, tt.wantPrompt, tt.content)
			}
		})
	}
}

func TestClaudeCode2125_DynamicStatusPattern(t *testing.T) {
	// Verify the updated dynamicStatusPattern matches new token format with arrows
	tests := []struct {
		input string
		want  bool
	}{
		{"(45s · 1234 tokens · ctrl+c to interrupt)", true}, // old format
		{"(35s · ↑ 673 tokens)", true},                      // new format with up arrow
		{"(39s · ↓ 1.8k tokens)", true},                     // new format with down arrow
		{"(33s · ↑ 144 tokens · thinking)", true},           // new with thinking
		{"(41s · ↓ 720 tokens)", true},                      // simple new format
		{"(some text)", false},                              // not a status
	}

	for _, tt := range tests {
		got := dynamicStatusPattern.MatchString(tt.input)
		if got != tt.want {
			t.Errorf("dynamicStatusPattern.MatchString(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// =============================================================================
// VALIDATION 8.0: OpenCode Question Tool Detection (#255)
// =============================================================================
// Bug: OpenCode's question tool (selection UI showing "enter submit" / "esc dismiss"
// in the help bar) is not detected as a waiting state. Sessions appear stuck in
// "running" (green) instead of "waiting" (orange).
//
// Fix: Add "enter submit" and "esc dismiss" to PromptPatterns and HasPrompt checks.
// Refined hasOpencodeBusyIndicator to avoid false positives when pulse chars appear
// in static UI elements alongside prompt-indicating strings.

func TestDefaultRawPatterns_OpenCodeQuestionTool(t *testing.T) {
	raw := DefaultRawPatterns("opencode")
	if raw == nil {
		t.Fatal("DefaultRawPatterns(opencode) returned nil")
	}

	promptPatterns := raw.PromptPatterns
	found := func(needle string) bool {
		for _, p := range promptPatterns {
			if p == needle {
				return true
			}
		}
		return false
	}

	if !found("enter submit") {
		t.Errorf("PromptPatterns missing %q (question tool prompt pattern)", "enter submit")
	}
	if !found("esc dismiss") {
		t.Errorf("PromptPatterns missing %q (question tool dismiss pattern)", "esc dismiss")
	}
}

// TestOpencodeBusyGuard_QuestionTool adds VALIDATION 8.0 test cases for question tool
// detection and false-positive prevention to the existing opencode busy guard coverage.
func TestOpencodeBusyGuard_QuestionTool(t *testing.T) {
	detector := NewPromptDetector("opencode")

	tests := []struct {
		name       string
		content    string
		wantPrompt bool
	}{
		// === IDLE states: question tool help bar (HasPrompt must return true) ===
		{
			name:       "idle - question tool help bar (enter submit + esc dismiss)",
			content:    "Select an option:\n  Option A\n  Option B\n↑↓ select     enter submit     esc dismiss",
			wantPrompt: true,
		},
		{
			name:       "idle - permission approval dialog",
			content:    "Allow tool execution?\n  Yes\n  No\n↑↓ select     enter submit     esc dismiss",
			wantPrompt: true,
		},
		{
			name:       "idle - question tool with TUI chrome",
			content:    "┃ What would you like to do?                                     ┃\n┃   Create new file                                              ┃\n┃   Edit existing file                                           ┃\n─────────────────────────────────────────────────\n↑↓ select     enter submit     esc dismiss",
			wantPrompt: true,
		},
		// === False-positive prevention: pulse char in static UI + prompt indicator ===
		{
			name:       "idle - pulse char in static UI but question tool active",
			content:    "░ Progress: 100%\n↑↓ select     enter submit     esc dismiss",
			wantPrompt: true,
		},
		// === BUSY states must still win when authoritative indicators present ===
		{
			name:       "busy - esc interrupt overrides enter submit",
			content:    "esc interrupt     enter submit     esc dismiss",
			wantPrompt: false,
		},
		{
			name:       "busy - Waiting for tool response with pulse char still busy",
			content:    "░ Waiting for tool response...\nSome output here",
			wantPrompt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.HasPrompt(tt.content)
			if got != tt.wantPrompt {
				t.Errorf("HasPrompt() = %v, want %v\nContent:\n%s", got, tt.wantPrompt, tt.content)
			}
		})
	}
}

// =============================================================================
// BENCHMARKS: Status Detection Performance Baselines
// =============================================================================
// Run with: go test -bench=. -benchmem ./internal/tmux/...

// realisticClaudeContent simulates a typical Claude Code terminal pane capture
// with mixed output, spinner status, and prompt elements.
var realisticClaudeContent = `Some previous output from Claude Code
Here is the implementation of the requested feature:

` + "```" + `go
func main() {
    fmt.Println("Hello, world!")
}
` + "```" + `

I've made the following changes:
1. Added the main function
2. Imported fmt package

✳ Cooking… (12s · ↑ 200 tokens)
──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`

var realisticClaudeDoneContent = `Here is the implementation of the requested feature:

` + "```" + `go
func main() {
    fmt.Println("Hello, world!")
}
` + "```" + `

I've made the following changes:
1. Added the main function
2. Imported fmt package

✻ Cooked for 32s

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`

func BenchmarkNormalizeContent(b *testing.B) {
	sess := NewSession("bench-normalize", "/tmp")
	sess.Command = "claude"

	b.ResetTimer()
	for b.Loop() {
		_ = sess.normalizeContent(realisticClaudeContent)
	}
}

func BenchmarkHasBusyIndicator(b *testing.B) {
	sess := NewSession("bench-busy", "/tmp")
	sess.Command = "claude"

	b.Run("active_spinner", func(b *testing.B) {
		for b.Loop() {
			_ = sess.hasBusyIndicator(realisticClaudeContent)
		}
	})

	b.Run("done_no_spinner", func(b *testing.B) {
		for b.Loop() {
			_ = sess.hasBusyIndicator(realisticClaudeDoneContent)
		}
	})
}

func BenchmarkHasPromptIndicator(b *testing.B) {
	sess := NewSession("bench-prompt", "/tmp")
	sess.Command = "claude"

	b.Run("with_prompt", func(b *testing.B) {
		for b.Loop() {
			_ = sess.hasPromptIndicator(realisticClaudeDoneContent)
		}
	})

	b.Run("no_prompt_active", func(b *testing.B) {
		for b.Loop() {
			_ = sess.hasPromptIndicator(realisticClaudeContent)
		}
	})
}

// =============================================================================
// VALIDATION 9.0: Spinner Activity Tracking
// =============================================================================
// Tests for findSpinnerInContent() and SpinnerActivityTracker (defined in tmux.go).
// Core idea: active spinner char PRESENCE (from curated set excluding ✻ and ·)
// means Claude is working. Grace period covers brief gaps between tool calls.

func TestFindSpinnerInContent(t *testing.T) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "✳", "✽", "✶", "✢"}

	tests := []struct {
		name      string
		content   string
		wantChar  string
		wantFound bool
	}{
		{
			name: "modern Claude active with asterisk spinner",
			content: `Some output from Claude...

✳ Gusting… (35s · ↑ 673 tokens)
──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantChar:  "✳",
			wantFound: true,
		},
		{
			name: "different asterisk spinner",
			content: `Some output from Claude...

✽ Metamorphosing… (33s · ↑ 144 tokens)
──────────────────────────────────────────────────────────────
❯`,
			wantChar:  "✽",
			wantFound: true,
		},
		{
			name: "braille spinner",
			content: `Working on something
⠙ Processing request...
❯`,
			wantChar:  "⠙",
			wantFound: true,
		},
		{
			name: "done state - ✻ is NOT in spinner list (excluded intentionally)",
			content: `✻ Churned for 4m 47s

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantChar:  "",
			wantFound: false,
		},
		{
			name: "no spinner - just prompt",
			content: `Some output here.

──────────────────────────────────────────────────────────────
❯
──────────────────────────────────────────────────────────────`,
			wantChar:  "",
			wantFound: false,
		},
		{
			name: "spinner in box-drawing line should be skipped",
			content: `│ ✳ some content in a box
──────────────────────────────────────────────────────────────
❯`,
			wantChar:  "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			char, line, found := findSpinnerInContent(tt.content, spinners)
			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}
			if char != tt.wantChar {
				t.Errorf("char = %q, want %q", char, tt.wantChar)
			}
			if found {
				t.Logf("Found spinner %q in line: %q", char, line)
			}
		})
	}
}

func TestSpinnerActivityTracker_MarkBusy(t *testing.T) {
	sat := NewSpinnerActivityTracker()

	// Initially not in grace period
	if sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=false before any MarkBusy")
	}

	// After marking busy, should be in grace period
	sat.MarkBusy()
	if !sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=true immediately after MarkBusy")
	}
}

func TestSpinnerActivityTracker_GracePeriodExpiry(t *testing.T) {
	sat := NewSpinnerActivityTracker()

	// Mark busy, then simulate grace period expiry
	sat.MarkBusy()
	if !sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=true immediately after MarkBusy")
	}

	// Simulate time passing beyond grace period
	sat.lastBusyTime = time.Now().Add(-7 * time.Second)
	if sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=false after grace period expired")
	}
}

func TestSpinnerActivityTracker_GracePeriod_BetweenToolCalls(t *testing.T) {
	sat := NewSpinnerActivityTracker()

	// Phase 1: Spinner is visible (Claude working on tool 1)
	sat.MarkBusy()
	sat.MarkBusy()
	sat.MarkBusy()

	// Phase 2: Spinner briefly disappears (between tool calls)
	// Should still be in grace period
	if !sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=true during tool transition")
	}

	// Phase 3: Spinner comes back (Claude working on tool 2)
	sat.MarkBusy()
	if !sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=true when spinner returns")
	}
}

func TestSpinnerActivityTracker_NoGraceWithoutActivity(t *testing.T) {
	sat := NewSpinnerActivityTracker()

	// Never marked busy
	if sat.InGracePeriod() {
		t.Error("Expected InGracePeriod=false when never marked busy")
	}
}

// TestSpinnerActivity_EndToEnd_WithHasBusyIndicator tests the full detection flow
// using hasBusyIndicator, which integrates findSpinnerInContent + SpinnerActivityTracker.
func TestSpinnerActivity_EndToEnd_WithHasBusyIndicator(t *testing.T) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "✳", "✽", "✶", "✢"}

	type event struct {
		content  string
		wantBusy bool
		desc     string
	}

	events := []event{
		// Phase 1: Claude starts working (spinner chars present)
		{"✳ Gusting… (1s · ↑ 50 tokens)\n❯", true, "start working"},
		{"✽ Gusting… (3s · ↑ 100 tokens)\n❯", true, "spinner cycling"},
		{"✶ Gusting… (5s · ↑ 150 tokens)\n❯", true, "still working"},
		{"✢ Gusting… (7s · ↑ 200 tokens)\n❯", true, "still working"},

		// Phase 2: Same spinner char (doesn't matter, presence is enough)
		{"✢ Gusting… (9s · ↑ 250 tokens)\n❯", true, "same char, still busy"},
		{"✢ Gusting… (11s · ↑ 300 tokens)\n❯", true, "same char, still busy"},

		// Phase 3: Done state (✻ not in active set)
		// Note: grace period is still active from the recent MarkBusy calls
	}

	// Use a single session to test state persistence across polls
	sess := NewSession("e2e-test", "/tmp")
	sess.Command = "claude"

	for i, ev := range events {
		got := sess.hasBusyIndicator(ev.content)
		status := "OK"
		if got != ev.wantBusy {
			status = "FAIL"
		}

		char, _, _ := findSpinnerInContent(ev.content, spinners)
		t.Logf("[%s] Poll %2d %-25s char=%-3q busy=%-5v", status, i+1, ev.desc, char, got)

		if got != ev.wantBusy {
			t.Errorf("Poll %d (%s): hasBusyIndicator=%v, want %v", i+1, ev.desc, got, ev.wantBusy)
		}
	}

	// Phase 4: Done state after grace period expires
	sess.mu.Lock()
	sess.ensureStateTrackerLocked()
	sess.stateTracker.spinnerTracker.lastBusyTime = time.Now().Add(-10 * time.Second)
	sess.mu.Unlock()

	doneContent := "✻ Worked for 54s\n\n❯"
	got := sess.hasBusyIndicator(doneContent)
	t.Logf("[%s] Poll done  %-25s busy=%-5v", map[bool]string{true: "FAIL", false: "OK"}[got], "done (grace expired)", got)
	if got {
		t.Errorf("Done state: hasBusyIndicator=%v, want false", got)
	}
}

// TestBusyPatternRegex_CatchesMidDotAndAsterisk validates that the BusyRegexps
// catch · and ✻ spinner chars (which are excluded from findSpinnerInContent's
// active set) by matching spinner+text+ellipsis patterns.
func TestBusyPatternRegex_CatchesMidDotAndAsterisk(t *testing.T) {
	raw := DefaultRawPatterns("claude")
	if raw == nil {
		t.Fatal("no default patterns for claude")
	}
	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("CompilePatterns: %v", err)
	}

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name: "middot active at line start (not in findSpinnerInContent set, caught by BusyRegexp)",
			content: `some output
· Kneading… (9m 22s · ↓ 14.1k tokens · thought for 36s)
──────────────────
❯
──────────────────
  [Opus 4.6] Context: 42%
  ⏵⏵ bypass permissions on`,
			want: true,
		},
		{
			name: "asterisk ✻ active (not in findSpinnerInContent set)",
			content: `some output
✻ Undulating… (thought for 2s)
──────────────────
❯
──────────────────
  [Opus 4.6] Context: 24%
  ⏵⏵ bypass permissions on`,
			want: true,
		},
		{
			name: "done state with ✻ (no ellipsis, should NOT match)",
			content: `some output
✻ Worked for 54s
──────────────────
❯
──────────────────
  [Opus 4.6] Context: 30%`,
			want: false,
		},
		{
			name: "done state with · (no ellipsis, should NOT match)",
			content: `some output
· Baked for 12s
──────────────────
❯
──────────────────
  [Opus 4.6] Context: 30%`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use a fresh Session per test to avoid grace period carryover
			s := &Session{
				Name:             "test-pattern",
				DisplayName:      "test-pattern",
				resolvedPatterns: resolved,
			}
			got := s.hasBusyIndicator(tt.content)
			if got != tt.want {
				t.Errorf("hasBusyIndicator=%v, want %v", got, tt.want)
			}
		})
	}
}
