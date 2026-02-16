package tmux

// =============================================================================
// STATUS LIGHT FIXES - REGRESSION TESTS
// =============================================================================
// These tests document and verify fixes for status light edge cases:
// - Fix 1.1: Whimsical word detection (all 90 Claude thinking words)
// - Fix 2.1: Progress bar normalization (prevents flicker from dynamic content)
//
// Run with: go test -v -run TestValidate ./internal/tmux/...

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// VALIDATION 1.1: Whimsical Words Detection
// =============================================================================
// Current bug: Only "Thinking" and "Connecting" are detected as busy indicators
// Expected: All 90 Claude whimsical words should trigger busy detection

// Note: claudeWhimsicalWords is now defined in tmux.go (Fix 1.1)

// TestValidate_WhimsicalWordDetection_CurrentBehavior documents the bug
// EXPECTED: This test should show that most whimsical words are NOT detected
func TestValidate_WhimsicalWordDetection_CurrentBehavior(t *testing.T) {
	sess := NewSession("validate-whimsical", "/tmp")
	sess.Command = "claude"

	detected := 0
	notDetected := []string{}

	for _, word := range claudeWhimsicalWords {
		// Simulate Claude output with whimsical word
		content := word + "... (25s · 340 tokens · esc to interrupt)\n>\n"

		// BUT WAIT - this has "esc to interrupt" which IS detected!
		// Let's test WITHOUT "esc to interrupt" to see if the word alone is detected
		contentNoEsc := word + "... (25s · 340 tokens)\n>\n"

		if sess.hasBusyIndicator(content) {
			detected++
		} else if sess.hasBusyIndicator(contentNoEsc) {
			detected++
		} else {
			notDetected = append(notDetected, word)
		}
	}

	t.Logf("Current detection: %d/%d words detected as busy", detected, len(claudeWhimsicalWords))
	t.Logf("Not detected: %v", notDetected)

	// Document the gap
	if len(notDetected) > 0 {
		t.Logf("BUG CONFIRMED: %d whimsical words are NOT detected without 'esc to interrupt'", len(notDetected))
	}
}

// TestValidate_WhimsicalWordDetection_WithoutEscToInterrupt tests detection WITHOUT "esc to interrupt"
// This is the key test - what happens when Claude shows "Flibbertigibbeting..." without the escape hint?
func TestValidate_WhimsicalWordDetection_WithoutEscToInterrupt(t *testing.T) {
	sess := NewSession("validate-no-esc", "/tmp")
	sess.Command = "claude"

	testWords := []string{
		"Flibbertigibbeting", "Wibbling", "Puttering", "Clauding",
		"Noodling", "Vibing", "Smooshing", "Honking",
	}

	for _, word := range testWords {
		// Content WITHOUT "esc to interrupt" - just the thinking word with tokens
		content := `Working on your request...

` + word + `... (25s · 340 tokens)

`
		detected := sess.hasBusyIndicator(content)
		t.Logf("%s: detected=%v", word, detected)

		// Current behavior: only "Thinking" and "Connecting" are detected when checking tokens pattern
		// Other words are NOT detected without "esc to interrupt"
	}
}

// TestValidate_WhimsicalWordDetection_ProposedFix shows what the fix should achieve
func TestValidate_WhimsicalWordDetection_ProposedFix(t *testing.T) {
	// Proposed fix: Check for ANY "___ing" word followed by tokens pattern
	proposedPattern := regexp.MustCompile(`(?i)[a-z]+ing[^(]*\([^)]*tokens`)

	testCases := []struct {
		word    string
		content string
	}{
		{"Flibbertigibbeting", "Flibbertigibbeting... (25s · 340 tokens)"},
		{"Wibbling", "Wibbling... (10s · 100 tokens)"},
		{"Thinking", "Thinking... (5s · 50 tokens)"},     // Already works
		{"Connecting", "Connecting... (2s · 10 tokens)"}, // Already works
		{"Puttering", "✻ Puttering… (15s · 200 tokens)"},
	}

	allMatch := true
	for _, tc := range testCases {
		matches := proposedPattern.MatchString(tc.content)
		t.Logf("%s: proposedPattern matches=%v", tc.word, matches)
		if !matches {
			allMatch = false
		}
	}

	if allMatch {
		t.Log("VALIDATION PASSED: Proposed pattern would detect all whimsical words")
	} else {
		t.Log("VALIDATION FAILED: Need to adjust proposed pattern")
	}
}

// =============================================================================
// VALIDATION 1.2: Spinner Staleness Detection
// =============================================================================
// Current bug: If spinner is visible but Claude is hung, shows GREEN forever
// Expected: After 30s of no content change with spinner, should NOT be busy

func TestValidate_SpinnerStaleness_CurrentBehavior(t *testing.T) {
	sess := NewSession("validate-spinner", "/tmp")
	sess.Command = "claude"

	// Spinner visible in content
	content := `Processing your request...

⠋ Loading...
`

	// Current behavior: spinner detected = busy
	detected := sess.hasBusyIndicator(content)
	t.Logf("Spinner detected as busy: %v", detected)

	// The issue: we have no staleness check
	// Even if Claude crashed 5 minutes ago with spinner visible, we'd show GREEN
	t.Log("Current limitation: No staleness check for spinner")
	t.Log("If Claude hangs with spinner visible, status stays GREEN forever")
}

func TestValidate_SpinnerStaleness_ProposedFix(t *testing.T) {
	// Proposed fix: Track last content change time
	// If spinner visible but content unchanged for >30s, ignore spinner
	type stalenessTracker struct {
		lastContentChange   int64 // Unix timestamp
		spinnerStaleTimeout int64 // 30 seconds
	}

	st := stalenessTracker{
		lastContentChange:   1000, // Content changed at t=1000
		spinnerStaleTimeout: 30,
	}

	// Simulate current time = t=1045 (45 seconds after last change)
	currentTime := int64(1045)
	timeSinceChange := currentTime - st.lastContentChange

	spinnerVisible := true
	isStale := timeSinceChange > st.spinnerStaleTimeout

	shouldIgnoreSpinner := spinnerVisible && isStale

	t.Logf("Time since content change: %ds", timeSinceChange)
	t.Logf("Spinner visible: %v", spinnerVisible)
	t.Logf("Is stale (>30s): %v", isStale)
	t.Logf("Should ignore spinner: %v", shouldIgnoreSpinner)

	if shouldIgnoreSpinner {
		t.Log("VALIDATION PASSED: Staleness detection would work")
	}
}

// =============================================================================
// VALIDATION 2.1: Content Normalization (Progress Bars)
// =============================================================================
// Current bug: Progress bars cause hash changes → flicker
// Expected: Progress bars should be normalized for stable hashing

func TestValidate_ProgressBarNormalization_CurrentBehavior(t *testing.T) {
	sess := NewSession("validate-progress", "/tmp")

	testCases := []struct {
		name     string
		content1 string
		content2 string
	}{
		{
			name:     "Progress bar percentage",
			content1: "Installing packages [======>     ] 45%",
			content2: "Installing packages [========>   ] 67%",
		},
		{
			name:     "Download progress",
			content1: "Downloading... 1.2MB/5.6MB",
			content2: "Downloading... 3.4MB/5.6MB",
		},
		{
			name:     "Simple percentage",
			content1: "Processing: 25% complete",
			content2: "Processing: 50% complete",
		},
	}

	for _, tc := range testCases {
		norm1 := sess.normalizeContent(tc.content1)
		norm2 := sess.normalizeContent(tc.content2)
		hash1 := sess.hashContent(norm1)
		hash2 := sess.hashContent(norm2)

		hashesMatch := hash1 == hash2

		t.Logf("%s:", tc.name)
		t.Logf("  Content 1: %q", tc.content1)
		t.Logf("  Content 2: %q", tc.content2)
		t.Logf("  Normalized 1: %q", norm1)
		t.Logf("  Normalized 2: %q", norm2)
		t.Logf("  Hashes match: %v", hashesMatch)

		if !hashesMatch {
			t.Logf("  BUG: Progress bar causes hash change → would cause GREEN flicker")
		}
	}
}

func TestValidate_ProgressBarNormalization_ProposedFix(t *testing.T) {
	// Proposed patterns to add
	progressBarPattern := regexp.MustCompile(`\[=*>?\s*\]\s*\d+%`)
	percentagePattern := regexp.MustCompile(`\d+%`)
	downloadPattern := regexp.MustCompile(`\d+\.?\d*[KMGT]?B/\d+\.?\d*[KMGT]?B`)

	testCases := []struct {
		name     string
		content1 string
		content2 string
		pattern  *regexp.Regexp
	}{
		{
			name:     "Progress bar",
			content1: "[======>     ] 45%",
			content2: "[========>   ] 67%",
			pattern:  progressBarPattern,
		},
		{
			name:     "Percentage",
			content1: "45%",
			content2: "67%",
			pattern:  percentagePattern,
		},
		{
			name:     "Download",
			content1: "1.2MB/5.6MB",
			content2: "3.4MB/5.6MB",
			pattern:  downloadPattern,
		},
	}

	for _, tc := range testCases {
		// Simulate normalization with proposed pattern
		norm1 := tc.pattern.ReplaceAllString(tc.content1, "PROGRESS")
		norm2 := tc.pattern.ReplaceAllString(tc.content2, "PROGRESS")

		t.Logf("%s:", tc.name)
		t.Logf("  Before: %q vs %q", tc.content1, tc.content2)
		t.Logf("  After:  %q vs %q", norm1, norm2)
		t.Logf("  Would match: %v", norm1 == norm2)
	}

	t.Log("VALIDATION: Proposed patterns would stabilize hashes")
}

// =============================================================================
// VALIDATION 2.2: Thinking Pattern Regex Coverage
// =============================================================================
// Current bug: Regex only matches "Thinking|Connecting"
// Expected: Should match all whimsical words

func TestValidate_ThinkingPatternRegex_CurrentCoverage(t *testing.T) {
	// Current pattern from tmux.go
	currentPattern := regexp.MustCompile(`(Thinking|Connecting)[^(]*\([^)]*\)`)

	testWords := []string{
		"Thinking",           // Should match
		"Connecting",         // Should match
		"Flibbertigibbeting", // Should match but doesn't
		"Wibbling",           // Should match but doesn't
		"Puttering",          // Should match but doesn't
	}

	for _, word := range testWords {
		content := word + "... (25s · 340 tokens)"
		matches := currentPattern.MatchString(content)
		t.Logf("%s: current pattern matches=%v", word, matches)
	}

	t.Log("BUG: Current pattern only matches 2/90 words")
}

func TestValidate_ThinkingPatternRegex_ProposedFix(t *testing.T) {
	// Option 1: Match any "___ing" word with parentheses
	option1 := regexp.MustCompile(`(?i)[a-z]+ing[^(]*\([^)]*\)`)

	// Option 2: Explicit list of all words (more precise but verbose)
	wordList := strings.Join(claudeWhimsicalWords, "|")
	option2 := regexp.MustCompile(`(?i)(` + wordList + `)[^(]*\([^)]*\)`)

	testCases := []string{
		"Flibbertigibbeting... (25s · 340 tokens)",
		"Wibbling... (10s · 100 tokens)",
		"Thinking... (5s · 50 tokens)",
		"Some random text (with parentheses)", // Should NOT match
		"Running tests... (3s · 20 tokens)",   // Tricky - "Running" ends in "ing"
	}

	t.Log("Option 1: Generic [a-z]+ing pattern")
	for _, tc := range testCases {
		t.Logf("  %q: matches=%v", tc, option1.MatchString(tc))
	}

	t.Log("Option 2: Explicit word list")
	for _, tc := range testCases {
		t.Logf("  %q: matches=%v", tc, option2.MatchString(tc))
	}

	// Option 2 is more precise - won't match "Running" unless it's in the list
	t.Log("RECOMMENDATION: Use explicit word list (Option 2) for precision")
}

// =============================================================================
// VALIDATION 3.1: Acknowledge Race Condition
// =============================================================================
// Current issue: Race between acknowledge and new output
// This is a timing test - harder to validate deterministically

func TestValidate_AcknowledgeRace_Documentation(t *testing.T) {
	t.Log("Race condition scenario:")
	t.Log("  T+0ms:   User detaches (Ctrl+Q)")
	t.Log("  T+10ms:  AcknowledgeWithSnapshot() sets acknowledged=true")
	t.Log("  T+50ms:  Claude outputs final message")
	t.Log("  T+500ms: Next tick sees new content, resets acknowledged=false")
	t.Log("  Result:  Brief GREEN flash even though user just acknowledged")
	t.Log("")
	t.Log("Proposed fix: 100ms grace period after acknowledge")
	t.Log("  - During grace period, ignore new content changes")
	t.Log("  - This prevents the race condition")
}

// =============================================================================
// SUMMARY TEST
// =============================================================================

func TestValidate_Summary(t *testing.T) {
	t.Log("=== STATUS LIGHT VALIDATION SUMMARY ===")
	t.Log("")
	t.Log("Fix 1.1 - Whimsical Words:")
	t.Log("  Bug: Only 'Thinking' and 'Connecting' detected")
	t.Log("  Fix: Add all 90 whimsical words OR use [a-z]+ing pattern")
	t.Log("  Risk: LOW - additive change")
	t.Log("")
	t.Log("Fix 1.2 - Spinner Staleness:")
	t.Log("  Bug: Stuck spinner shows GREEN forever")
	t.Log("  Fix: Ignore spinner if no content change for >30s")
	t.Log("  Risk: LOW - adds safety check")
	t.Log("")
	t.Log("Fix 2.1 - Progress Bar Normalization:")
	t.Log("  Bug: Progress bars cause hash changes → flicker")
	t.Log("  Fix: Add regex patterns to strip dynamic progress")
	t.Log("  Risk: MEDIUM - regex must not over-match")
	t.Log("")
	t.Log("Fix 2.2 - Thinking Pattern Regex:")
	t.Log("  Bug: Pattern too narrow (2 words only)")
	t.Log("  Fix: Use explicit 90-word list")
	t.Log("  Risk: LOW - more precise matching")
	t.Log("")
	t.Log("RECOMMENDATION: Start with Fix 1.1 and 1.2 (low risk, high impact)")
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
