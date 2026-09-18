package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
)

// Issue #2026: the context window is inferred from the model id, and the id
// does not carry it. These tests pin the three safe behaviours shipped ahead
// of the redesign: a reading over 100% is flagged rather than shown, the
// inferred window is marked as such, and observed usage disproves a wrong
// inference.

// TestContextUsage_OverLimitIsFlaggedNotClamped (a): 494,561 tokens against a
// 200k window used to read 247% and the bar clamped it to a full-looking 100%.
// It must now be an over-limit reading with no percentage.
func TestContextUsage_OverLimitIsFlaggedNotClamped(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	a := &SessionAnalytics{
		Model:                "claude-opus-4-20250514", // 200k row
		CurrentContextTokens: 494561,
		PeakContextTokens:    494561,
	}
	u := a.ContextUsage()
	if u.Known {
		t.Fatalf("usage over the inferred window must not be Known: %+v", u)
	}
	if !u.OverLimit {
		t.Fatalf("usage over the inferred window must be OverLimit: %+v", u)
	}
	if u.Percent != 0 {
		t.Errorf("Percent = %v, want 0 for an over-limit reading", u.Percent)
	}
	if u.Window.Tokens != 200000 {
		t.Errorf("the disproved figure must be kept for the surface to name, got %+v", u.Window)
	}
	if w := a.ContextWindow(); w.Known() || !strings.Contains(w.Detail, "200") {
		t.Errorf("a window proven too small must be reported unknown with the figure named, got %+v", w)
	}
}

// TestContextUsage_PeakDisprovesWindowAfterCompaction (a): the proof survives
// compaction. Once the transcript has held more than the inferred window, a
// later, smaller turn does not make the inference trustworthy again.
func TestContextUsage_PeakDisprovesWindowAfterCompaction(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	a := &SessionAnalytics{
		Model:                "claude-opus-4-20250514",
		CurrentContextTokens: 40000,
		PeakContextTokens:    494561,
	}
	u := a.ContextUsage()
	if u.Known || !u.OverLimit {
		t.Fatalf("peak above the window must keep the reading over-limit: %+v", u)
	}
	if u.Used != 494561 {
		t.Errorf("Used = %d, want the peak that proved the window wrong", u.Used)
	}
}

// TestContextUsage_InferredWindowIsMarked (c): a prefix-table window still
// yields a percentage, but it says it is inferred.
func TestContextUsage_InferredWindowIsMarked(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	a := &SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 500000, PeakContextTokens: 500000}
	u := a.ContextUsage()
	if !u.Known {
		t.Fatalf("a reading inside an inferred window is still a reading: %+v", u)
	}
	if !u.Inferred {
		t.Errorf("window from the model-id table must be marked Inferred: %+v", u.Window)
	}
	if u.Percent < 49.99 || u.Percent > 50.01 {
		t.Errorf("Percent = %v, want 50", u.Percent)
	}
	if u.Window.Source != ctxinspect.WindowModelDefault {
		t.Errorf("Source = %v, want model-default", u.Window.Source)
	}
}

// TestContextUsage_ConfiguredWindowIsNotInferred (c): an operator-supplied
// window beats the table and is not marked inferred.
func TestContextUsage_ConfiguredWindowIsNotInferred(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "400000")
	a := &SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 100000, PeakContextTokens: 100000}
	u := a.ContextUsage()
	if !u.Known || u.Inferred {
		t.Fatalf("env-configured window must be Known and not Inferred: %+v", u)
	}
	if u.Window.Source != ctxinspect.WindowEnvOverride {
		t.Errorf("Source = %v, want env", u.Window.Source)
	}
	if u.Percent < 24.99 || u.Percent > 25.01 {
		t.Errorf("Percent = %v, want 25", u.Percent)
	}
}

// TestContextUsage_UnknownModelIsUnknownNotDefault (c): an id the table has
// never seen no longer gets a silent 200k denominator.
func TestContextUsage_UnknownModelIsUnknownNotDefault(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	for _, model := range []string{"", "totally-unknown-model"} {
		a := &SessionAnalytics{Model: model, CurrentContextTokens: 20000, PeakContextTokens: 20000}
		u := a.ContextUsage()
		if u.Known || u.OverLimit || u.Inferred {
			t.Errorf("model %q: want unknown reading, got %+v", model, u)
		}
		if u.Percent != 0 {
			t.Errorf("model %q: Percent = %v, want 0", model, u.Percent)
		}
	}
}

// TestContextUsage_Registry keeps the ids the deck sees on real sessions
// resolving through the single window registry.
func TestContextUsage_Registry(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	cases := []struct {
		model string
		want  int
	}{
		{"claude-fable-5", 1_000_000},
		{"claude-opus-5", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-opus-4-8-20260801", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-sonnet-4-6", 1_000_000},
		{"claude-opus-4-20250514", 200_000},
		{"claude-haiku-4-5", 200_000},
		{"claude-3-5-sonnet", 200_000},
		{"MiniMax-M3", 1_000_000},
		{"MiniMax-M2.7", 204_800},
		{"MiniMax-M2.5-highspeed", 204_000},
	}
	for _, tc := range cases {
		w := (&SessionAnalytics{Model: tc.model}).ContextWindow()
		if w.Tokens != tc.want || !w.Inferred() {
			t.Errorf("%q: window = %+v, want %d inferred", tc.model, w, tc.want)
		}
	}
}

// TestParseSessionJSONL_TracksPeakContext: the peak is the transcript's own
// lower bound on the window, so it must survive a later smaller turn.
func TestParseSessionJSONL_TracksPeakContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"assistant","timestamp":"2026-09-18T10:00:00Z","message":{"model":"claude-opus-5","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":300000,"output_tokens":5}}}`,
		`{"type":"assistant","timestamp":"2026-09-18T10:01:00Z","message":{"model":"claude-opus-5","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":40000,"output_tokens":5}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := ParseSessionJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.CurrentContextTokens != 40020 {
		t.Errorf("CurrentContextTokens = %d, want 40020", a.CurrentContextTokens)
	}
	if a.PeakContextTokens != 300010 {
		t.Errorf("PeakContextTokens = %d, want 300010", a.PeakContextTokens)
	}
}

// TestGeminiContextUsage_OverLimitIsFlagged (a) for the Gemini bar, which had
// the same clamp.
func TestGeminiContextUsage_OverLimitIsFlagged(t *testing.T) {
	a := &GeminiSessionAnalytics{Model: "gemini-2.5-pro", CurrentContextTokens: 1_500_000}
	u := a.ContextUsage()
	if u.Known || !u.OverLimit {
		t.Fatalf("1.5M against a 1M window must be over-limit: %+v", u)
	}
	inside := &GeminiSessionAnalytics{Model: "gemini-2.5-pro", CurrentContextTokens: 250_000}
	if u := inside.ContextUsage(); !u.Known || !u.Inferred || u.Percent < 24.99 || u.Percent > 25.01 {
		t.Errorf("250k of 1M: want 25%% inferred, got %+v", u)
	}
}
