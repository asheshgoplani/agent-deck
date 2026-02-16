package tmux

import (
	"strings"
	"testing"
)

func TestDefaultRawPatterns_Claude(t *testing.T) {
	raw := DefaultRawPatterns("claude")
	if raw == nil {
		t.Fatal("expected non-nil for claude")
	}

	// Should contain the primary busy indicator
	found := false
	for _, p := range raw.BusyPatterns {
		if p == "ctrl+c to interrupt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("claude defaults missing 'ctrl+c to interrupt'")
	}

	// Should have spinner chars
	if len(raw.SpinnerChars) == 0 {
		t.Error("claude defaults missing spinner chars")
	}

	// Should have whimsical words
	if len(raw.WhimsicalWords) < 80 {
		t.Errorf("expected 80+ whimsical words, got %d", len(raw.WhimsicalWords))
	}

	// Should have the regex pattern for 2.1.25+
	hasRegex := false
	for _, p := range raw.BusyPatterns {
		if len(p) > 3 && p[:3] == "re:" {
			hasRegex = true
			break
		}
	}
	if !hasRegex {
		t.Error("claude defaults missing regex busy pattern")
	}
}

func TestDefaultRawPatterns_Gemini(t *testing.T) {
	raw := DefaultRawPatterns("gemini")
	if raw == nil {
		t.Fatal("expected non-nil for gemini")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("gemini should have busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("gemini should have prompt patterns")
	}
}

func TestDefaultRawPatterns_Codex(t *testing.T) {
	raw := DefaultRawPatterns("codex")
	if raw == nil {
		t.Fatal("expected non-nil for codex")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("codex should have busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("codex should have prompt patterns")
	}
}

func TestDefaultRawPatterns_Unknown(t *testing.T) {
	raw := DefaultRawPatterns("unknowntool")
	if raw != nil {
		t.Error("expected nil for unknown tool")
	}
}

func TestDefaultRawPatterns_CaseInsensitive(t *testing.T) {
	raw := DefaultRawPatterns("Claude")
	if raw == nil {
		t.Fatal("expected non-nil for Claude (uppercase)")
	}
}

func TestCompilePatterns_PlainStrings(t *testing.T) {
	raw := &RawPatterns{
		BusyPatterns:   []string{"busy1", "busy2"},
		PromptPatterns: []string{"prompt1"},
	}

	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resolved.BusyStrings) != 2 {
		t.Errorf("expected 2 busy strings, got %d", len(resolved.BusyStrings))
	}
	if len(resolved.BusyRegexps) != 0 {
		t.Errorf("expected 0 busy regexps, got %d", len(resolved.BusyRegexps))
	}
	if len(resolved.PromptStrings) != 1 {
		t.Errorf("expected 1 prompt string, got %d", len(resolved.PromptStrings))
	}
}

func TestCompilePatterns_RegexPrefix(t *testing.T) {
	raw := &RawPatterns{
		BusyPatterns: []string{"plain", `re:\d+\s+tokens`},
	}

	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resolved.BusyStrings) != 1 || resolved.BusyStrings[0] != "plain" {
		t.Error("plain string not preserved")
	}
	if len(resolved.BusyRegexps) != 1 {
		t.Error("regex not compiled")
	}

	// Verify the regex actually works
	if !resolved.BusyRegexps[0].MatchString("123 tokens") {
		t.Error("compiled regex should match '123 tokens'")
	}
}

func TestCompilePatterns_InvalidRegex(t *testing.T) {
	raw := &RawPatterns{
		BusyPatterns: []string{"good", "re:[invalid("},
	}

	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid regex should be skipped, not crash
	if len(resolved.BusyStrings) != 1 {
		t.Errorf("expected 1 valid string, got %d", len(resolved.BusyStrings))
	}
	if len(resolved.BusyRegexps) != 0 {
		t.Errorf("expected 0 regexps (invalid skipped), got %d", len(resolved.BusyRegexps))
	}
}

func TestCompilePatterns_Nil(t *testing.T) {
	_, err := CompilePatterns(nil)
	if err == nil {
		t.Error("expected error for nil input")
	}
}

func TestCompilePatterns_WithWhimsicalWords(t *testing.T) {
	raw := DefaultRawPatterns("claude")
	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have combo patterns built
	if resolved.ThinkingPattern == nil {
		t.Error("ThinkingPattern should be compiled")
	}
	if resolved.ThinkingPatternEllipsis == nil {
		t.Error("ThinkingPatternEllipsis should be compiled")
	}
	if resolved.SpinnerActivePattern == nil {
		t.Error("SpinnerActivePattern should be compiled")
	}

	// ThinkingPatternEllipsis should match active Claude output
	if !resolved.ThinkingPatternEllipsis.MatchString("✳ Gusting… (35s · ↑ 673 tokens)") {
		t.Error("ThinkingPatternEllipsis should match active Claude status")
	}

	// SpinnerActivePattern should match active status with ellipsis
	if !resolved.SpinnerActivePattern.MatchString("✳ Gusting…") {
		t.Error("SpinnerActivePattern should match '✳ Gusting…'")
	}
}

func TestMergeRawPatterns_ExtendMode(t *testing.T) {
	defaults := &RawPatterns{
		BusyPatterns:   []string{"default1"},
		PromptPatterns: []string{"prompt1"},
		SpinnerChars:   []string{"⠋"},
	}
	extras := &RawPatterns{
		BusyPatterns:   []string{"extra1"},
		PromptPatterns: []string{"prompt2"},
		SpinnerChars:   []string{"@"},
	}

	result := MergeRawPatterns(defaults, nil, extras)

	if len(result.BusyPatterns) != 2 {
		t.Errorf("expected 2 busy patterns, got %d", len(result.BusyPatterns))
	}
	if result.BusyPatterns[0] != "default1" || result.BusyPatterns[1] != "extra1" {
		t.Errorf("unexpected busy patterns: %v", result.BusyPatterns)
	}
	if len(result.PromptPatterns) != 2 {
		t.Errorf("expected 2 prompt patterns, got %d", len(result.PromptPatterns))
	}
	if len(result.SpinnerChars) != 2 {
		t.Errorf("expected 2 spinner chars, got %d", len(result.SpinnerChars))
	}
}

func TestMergeRawPatterns_ReplaceMode(t *testing.T) {
	defaults := &RawPatterns{
		BusyPatterns: []string{"default1", "default2"},
	}
	overrides := &RawPatterns{
		BusyPatterns: []string{"override1"}, // non-nil: replaces
	}

	result := MergeRawPatterns(defaults, overrides, nil)

	if len(result.BusyPatterns) != 1 || result.BusyPatterns[0] != "override1" {
		t.Errorf("expected override to replace defaults, got %v", result.BusyPatterns)
	}
}

func TestMergeRawPatterns_ReplaceWithEmpty(t *testing.T) {
	defaults := &RawPatterns{
		BusyPatterns: []string{"default1"},
	}
	overrides := &RawPatterns{
		BusyPatterns: []string{}, // explicitly empty: replaces with nothing
	}

	result := MergeRawPatterns(defaults, overrides, nil)

	if len(result.BusyPatterns) != 0 {
		t.Errorf("expected empty replacement, got %v", result.BusyPatterns)
	}
}

func TestMergeRawPatterns_NilDefaults(t *testing.T) {
	overrides := &RawPatterns{
		BusyPatterns:   []string{"custom1"},
		PromptPatterns: []string{"prompt1"},
	}

	result := MergeRawPatterns(nil, overrides, nil)

	if len(result.BusyPatterns) != 1 || result.BusyPatterns[0] != "custom1" {
		t.Errorf("expected custom patterns, got %v", result.BusyPatterns)
	}
}

func TestMergeRawPatterns_AllNil(t *testing.T) {
	result := MergeRawPatterns(nil, nil, nil)
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if len(result.BusyPatterns) != 0 {
		t.Error("expected empty patterns")
	}
}

func TestMergeRawPatterns_DoesNotMutateInputs(t *testing.T) {
	defaults := &RawPatterns{
		BusyPatterns: []string{"d1"},
	}
	extras := &RawPatterns{
		BusyPatterns: []string{"e1"},
	}

	_ = MergeRawPatterns(defaults, nil, extras)

	// Original slices should be unchanged
	if len(defaults.BusyPatterns) != 1 {
		t.Error("defaults mutated")
	}
	if len(extras.BusyPatterns) != 1 {
		t.Error("extras mutated")
	}
}

// TestClaudeBusyRegex_WelcomeBannerFalsePositive is a regression test for the bug
// where the Claude BusyRegexp `[✳✽✶✻✢·]\s*.+…` false-positived on the welcome
// banner line "Opus 4.6 is here · $50 free extra usage · Try fast mode or use i…".
// The `·` (middle dot) matched the char class and `…` (ellipsis from terminal
// truncation) matched the end, causing hasBusyIndicatorResolved to return true
// for an idle session. This made `session send` timeout after 80s because
// waitForAgentReady never saw "waiting" status.
func TestClaudeBusyRegex_WelcomeBannerFalsePositive(t *testing.T) {
	raw := DefaultRawPatterns("claude")
	if raw == nil {
		t.Fatal("expected non-nil patterns for claude")
	}
	resolved, err := CompilePatterns(raw)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}

	// This is the Claude Code v2.1.42 welcome banner line that gets truncated
	// to 80 columns in a tmux pane. The `·` is a separator and `…` is truncation.
	bannerLines := []string{
		"Opus 4.6 is here · $50 free extra usage · Try fast mode or use i…",
		"Opus 4.6 · Claude Max",
		"~/.agent-deck/conductor/sre",
		`❯ Try "create a util logging.py that..."`,
		"⏵⏵ bypass permissions on (shift+tab to cycle)",
	}
	bannerContent := ""
	for _, line := range bannerLines {
		bannerContent += line + "\n"
	}

	// No BusyRegexp should match the welcome banner
	for _, re := range resolved.BusyRegexps {
		if re.MatchString(bannerContent) {
			t.Errorf("BusyRegexp %q should NOT match welcome banner, but it does", re.String())
		}
	}

	// No BusyString should match the welcome banner
	for _, s := range resolved.BusyStrings {
		if strings.Contains(bannerContent, s) {
			t.Errorf("BusyString %q should NOT match welcome banner", s)
		}
	}

	// Sanity: real busy lines SHOULD still match
	busyLines := []string{
		"✳ Clauding… (10s · ↓ 200 tokens)",
		"✽ Brewing… (5s · ↑ 100 tokens)",
		"✶ Thinking… (2s)",
		"✢ Computing… (30s · ↓ 500 tokens)",
	}
	for _, line := range busyLines {
		matched := false
		for _, re := range resolved.BusyRegexps {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("BusyRegexp should match real busy line %q, but none matched", line)
		}
	}
}

func TestSpinnerRuneSet(t *testing.T) {
	runes := SpinnerRuneSet()
	if len(runes) == 0 {
		t.Fatal("expected spinner runes")
	}

	// Should include braille characters
	hasBraille := false
	for _, r := range runes {
		if r == '⠋' {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		t.Error("missing braille spinner chars")
	}

	// Should include normalization chars (· and ✻)
	hasDot := false
	hasDone := false
	for _, r := range runes {
		if r == '·' {
			hasDot = true
		}
		if r == '✻' {
			hasDone = true
		}
	}
	if !hasDot {
		t.Error("missing · from normalization set")
	}
	if !hasDone {
		t.Error("missing ✻ from normalization set")
	}
}
