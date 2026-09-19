package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

var accountingANSIStripRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSIForAccounting drops SGR sequences so assertions can match the plain
// text of a rendered panel.
func stripANSIForAccounting(s string) string {
	return accountingANSIStripRe.ReplaceAllString(s, "")
}

// TestAnalyticsPanel_ParseGapWarningShown: when transcript lines were skipped,
// the panel must say the totals may be stale rather than presenting them as
// complete. This is the visible half of the ParseGaps fix.
func TestAnalyticsPanel_ParseGapWarningShown(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetAnalytics(&session.SessionAnalytics{
		Model:                "claude-opus-4-7",
		CurrentContextTokens: 125213,
		ParseGaps:            3,
		ParseGapSample: []session.ParseGap{
			{Line: 12, Reason: session.ParseGapMalformed, Bytes: 40},
		},
	})

	out := stripANSIForAccounting(panel.View())
	if !strings.Contains(out, "3 transcript lines unparsed") {
		t.Errorf("view missing parse-gap warning:\n%s", out)
	}
	if !strings.Contains(out, "totals may be stale") {
		t.Errorf("view missing staleness caveat:\n%s", out)
	}
}

// TestAnalyticsPanel_ParseGapWarningSingular keeps the message grammatical for
// the common one-bad-line case.
func TestAnalyticsPanel_ParseGapWarningSingular(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetAnalytics(&session.SessionAnalytics{Model: "claude-opus-4-7", ParseGaps: 1})

	out := stripANSIForAccounting(panel.View())
	if !strings.Contains(out, "1 transcript line unparsed") {
		t.Errorf("view missing singular parse-gap warning:\n%s", out)
	}
}

// TestAnalyticsPanel_NoParseGapWarningOnCleanTranscript is the negative control:
// a false staleness warning would erode trust in every other number.
func TestAnalyticsPanel_NoParseGapWarningOnCleanTranscript(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetAnalytics(&session.SessionAnalytics{Model: "claude-opus-4-7", CurrentContextTokens: 1000})

	out := stripANSIForAccounting(panel.View())
	if strings.Contains(out, "unparsed") || strings.Contains(out, "may be stale") {
		t.Errorf("clean transcript must not warn:\n%s", out)
	}
}

// TestAnalyticsPanel_GeminiContextBarUsesModelWindow: the bar used to divide by
// a hardcoded 1,000,000 for every model. gemini-1.5-pro has a 2M window, so the
// same token count must read half as full.
func TestAnalyticsPanel_GeminiContextBarUsesModelWindow(t *testing.T) {
	makeBar := func(model string) string {
		panel := NewAnalyticsPanel()
		panel.SetSize(80, 24)
		panel.SetDisplaySettings(allSectionsEnabled())
		panel.SetGeminiAnalytics(&session.GeminiSessionAnalytics{
			Model:                model,
			CurrentContextTokens: 500000,
		})
		return stripANSIForAccounting(panel.renderGeminiContextBar())
	}

	if got := makeBar("gemini-1.5-pro"); !strings.Contains(got, "25.0%") {
		t.Errorf("gemini-1.5-pro (2M window) bar = %q, want 25.0%%", got)
	}
	if got := makeBar("gemini-2.5-pro"); !strings.Contains(got, "50.0%") {
		t.Errorf("gemini-2.5-pro (1M window) bar = %q, want 50.0%%", got)
	}
}

// TestAnalyticsPanel_GeminiTokensShowCachedThoughtsTool proves the counters the
// parser stopped dropping actually reach the screen.
func TestAnalyticsPanel_GeminiTokensShowCachedThoughtsTool(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetGeminiAnalytics(&session.GeminiSessionAnalytics{
		Model:               "gemini-2.5-pro",
		InputTokens:         7560,
		OutputTokens:        26,
		CachedTokens:        2914,
		ThoughtsTokens:      492,
		ToolTokens:          31,
		ReportedTotalTokens: 8109,
	})

	out := stripANSIForAccounting(panel.renderGeminiTokens())
	for _, want := range []string{"Cached:", "2,914", "Thinking:", "492", "Tool:", "31", "8,109"} {
		if !strings.Contains(out, want) {
			t.Errorf("gemini token view missing %q:\n%s", want, out)
		}
	}
}

// TestAnalyticsPanel_GeminiTokensHideEmptyCounters keeps the panel quiet when
// the harness reported nothing for those counters.
func TestAnalyticsPanel_GeminiTokensHideEmptyCounters(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 24)
	panel.SetDisplaySettings(allSectionsEnabled())
	panel.SetGeminiAnalytics(&session.GeminiSessionAnalytics{
		Model:        "gemini-2.5-pro",
		InputTokens:  100,
		OutputTokens: 20,
	})

	out := stripANSIForAccounting(panel.renderGeminiTokens())
	if strings.Contains(out, "Cached:") || strings.Contains(out, "Thinking:") || strings.Contains(out, "Tool:") {
		t.Errorf("zero counters must not render:\n%s", out)
	}
}
