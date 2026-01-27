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
			wantBusy: true, // Restored: esc to interrupt is fallback for older Claude Code
		},
		{
			name:     "idle - just prompt",
			content:  `❯`,
			wantBusy: false,
		},
	}

	sess := &Session{DisplayName: "test"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
	sess := NewSession("box-drawing-test", "/tmp")
	sess.Command = "claude"

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
			got := sess.hasBusyIndicator(tt.content)
			if got != tt.wantBusy {
				t.Errorf("hasBusyIndicator() = %v, want %v\nContent:\n%s", got, tt.wantBusy, tt.content)
			}
		})
	}
}
