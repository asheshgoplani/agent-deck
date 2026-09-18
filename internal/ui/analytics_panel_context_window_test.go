package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/ctxtext"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2026: the context bar must never present a computed impossibility as
// a full bar, and must say when its denominator is a guess.

func claudeBar(t *testing.T, a *session.SessionAnalytics) string {
	t.Helper()
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetAnalytics(a)
	return stripANSIForAccounting(panel.renderContextBar())
}

// TestAnalyticsPanel_ContextBarInferredIsMarked (c): a table window renders
// its percentage with the inferred marker, not as an established figure.
func TestAnalyticsPanel_ContextBarInferredIsMarked(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	got := claudeBar(t, &session.SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 500000, PeakContextTokens: 500000})
	if !strings.Contains(got, "≈50.0%") || !strings.Contains(got, "inferred") {
		t.Errorf("bar = %q, want ≈50.0%% marked inferred", got)
	}
}

// TestAnalyticsPanel_ContextBarConfiguredIsPlain (c): an established window
// carries no hedge.
func TestAnalyticsPanel_ContextBarConfiguredIsPlain(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "400000")
	got := claudeBar(t, &session.SessionAnalytics{Model: "claude-opus-5", CurrentContextTokens: 100000, PeakContextTokens: 100000})
	if !strings.Contains(got, " 25.0%") || strings.Contains(got, "≈") || strings.Contains(got, "inferred") {
		t.Errorf("bar = %q, want plain 25.0%%", got)
	}
}

// TestAnalyticsPanel_ContextBarOverLimitIsNotFull (a): 247%% of a 200k window
// used to draw as a full red bar reading 100.0%%. It must now say the window
// is wrong and draw no percentage.
func TestAnalyticsPanel_ContextBarOverLimitIsNotFull(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	got := claudeBar(t, &session.SessionAnalytics{Model: "claude-opus-4-20250514", CurrentContextTokens: 494561, PeakContextTokens: 494561})
	if strings.Contains(got, "100.0%") || strings.Contains(got, "%") {
		t.Errorf("bar = %q, must not show a percentage for an over-limit reading", got)
	}
	if !strings.Contains(got, "over") || !strings.Contains(got, "200") {
		t.Errorf("bar = %q, want the disproved 200k window named as exceeded", got)
	}
	if !strings.Contains(got, "494.6k") {
		t.Errorf("bar = %q, want the observed usage shown", got)
	}
}

// TestAnalyticsPanel_ContextBarUnknownWindowHasRemedy: an unrecognised model
// renders an indeterminate bar with the way to fix it, not 200k's percentage.
func TestAnalyticsPanel_ContextBarUnknownWindowHasRemedy(t *testing.T) {
	t.Setenv(ctxtext.WindowEnvVar, "")
	got := claudeBar(t, &session.SessionAnalytics{Model: "never-seen-model", CurrentContextTokens: 20000, PeakContextTokens: 20000})
	if strings.Contains(got, "%") {
		t.Errorf("bar = %q, must not show a percentage for an unknown window", got)
	}
	if !strings.Contains(got, "unknown") || !strings.Contains(got, ctxtext.WindowEnvVar) {
		t.Errorf("bar = %q, want unknown with the remedy", got)
	}
}

// TestAnalyticsPanel_GeminiContextBarOverLimit applies (a) to the Gemini bar.
func TestAnalyticsPanel_GeminiContextBarOverLimit(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetGeminiAnalytics(&session.GeminiSessionAnalytics{Model: "gemini-2.5-pro", CurrentContextTokens: 1_500_000})
	got := stripANSIForAccounting(panel.renderGeminiContextBar())
	if strings.Contains(got, "%") || !strings.Contains(got, "over") {
		t.Errorf("gemini bar = %q, want over-limit with no percentage", got)
	}
}
