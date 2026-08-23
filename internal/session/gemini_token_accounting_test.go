package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGeminiContextWindowForModel_ResolvesPerModel replaces the hardcoded 1M
// window that internal/ui/analytics_panel.go used for every Gemini session.
func TestGeminiContextWindowForModel_ResolvesPerModel(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"gemini-1.5-pro", 2000000},
		{"gemini-1.5-pro-latest", 2000000},
		{"gemini-1.5-flash", 1000000},
		{"gemini-2.0-flash", 1000000},
		{"gemini-2.5-pro", 1000000},
		{"gemini-2.5-flash-lite", 1000000},
		{"gemini-3.1-pro-preview", 1000000},
		{"totally-unknown", geminiDefaultContextWindow},
		{"", geminiDefaultContextWindow},
	}
	for _, tc := range cases {
		if got := GeminiContextWindowForModel(tc.model); got != tc.want {
			t.Errorf("GeminiContextWindowForModel(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

// TestGeminiContextWindowForModel_MostSpecificPrefixWins guards the table order:
// a "gemini-1.5" entry added later must not shadow "gemini-1.5-pro".
func TestGeminiContextWindowForModel_MostSpecificPrefixWins(t *testing.T) {
	if got := GeminiContextWindowForModel("gemini-1.5-pro-002"); got != 2000000 {
		t.Errorf("gemini-1.5-pro-002 window = %d, want 2000000", got)
	}
}

func TestGeminiSessionAnalytics_ContextPercent(t *testing.T) {
	// 500k of a 2M window = 25%, not the 50% a hardcoded 1M window would report.
	a := &GeminiSessionAnalytics{Model: "gemini-1.5-pro", CurrentContextTokens: 500000}
	if got := a.ContextPercent(0); got < 24.99 || got > 25.01 {
		t.Errorf("ContextPercent(0) = %f, want 25", got)
	}
	// Explicit limit overrides the model table.
	if got := a.ContextPercent(1000000); got < 49.99 || got > 50.01 {
		t.Errorf("ContextPercent(1000000) = %f, want 50", got)
	}
	// No divide-by-zero on a nonsensical limit.
	if got := (&GeminiSessionAnalytics{}).ContextPercent(-1); got != 0 {
		t.Errorf("ContextPercent(-1) = %f, want 0", got)
	}
}

// TestGeminiSessionAnalytics_TotalTokensPrefersReported: Gemini states its own
// per-message total; that measured number wins over any sum computed here.
func TestGeminiSessionAnalytics_TotalTokensPrefersReported(t *testing.T) {
	a := &GeminiSessionAnalytics{
		InputTokens:         7560,
		OutputTokens:        26,
		CachedTokens:        2914,
		ThoughtsTokens:      492,
		ToolTokens:          0,
		ReportedTotalTokens: 8078,
	}
	if got := a.TotalTokens(); got != 8078 {
		t.Errorf("TotalTokens() = %d, want 8078 (harness-reported total)", got)
	}
}

// TestGeminiSessionAnalytics_TotalTokensFallsBackToParts covers session files
// written before Gemini recorded a "total" field.
func TestGeminiSessionAnalytics_TotalTokensFallsBackToParts(t *testing.T) {
	a := &GeminiSessionAnalytics{
		InputTokens:    7560,
		OutputTokens:   26,
		CachedTokens:   2914, // subset of input; must NOT be added
		ThoughtsTokens: 492,
		ToolTokens:     10,
	}
	if want := 7560 + 26 + 492 + 10; a.TotalTokens() != want {
		t.Errorf("TotalTokens() = %d, want %d (cached must not be double-counted)", a.TotalTokens(), want)
	}
}

// writeGeminiSession writes a session file where UpdateGeminiAnalyticsFromDisk
// will look for it, and returns the project path.
func writeGeminiSession(t *testing.T, sessionID, body string) string {
	t.Helper()
	geminiConfigDirOverride = t.TempDir()
	t.Cleanup(func() { geminiConfigDirOverride = "" })

	projectPath := t.TempDir()
	dir := GetGeminiSessionsDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	file := filepath.Join(dir, "session-2026-07-26T10-00-"+sessionID[:8]+".json")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	return projectPath
}

// TestUpdateGeminiAnalyticsFromDisk_KeepsAllTokenCounters is the regression:
// cached/thoughts/tool/total were parsed away and thrown out.
func TestUpdateGeminiAnalyticsFromDisk_KeepsAllTokenCounters(t *testing.T) {
	const sessionID = "df8dac27-0000-0000-0000-000000000000"
	body := `{
	  "sessionId": "` + sessionID + `",
	  "startTime": "2026-07-26T10:00:00.000Z",
	  "lastUpdated": "2026-07-26T10:30:00.000Z",
	  "messages": [
	    {"type":"user","content":"hi"},
	    {"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":1000,"output":10,"cached":400,"thoughts":100,"tool":5,"total":1115}},
	    {"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":7560,"output":26,"cached":2914,"thoughts":492,"tool":0,"total":8078}}
	  ]
	}`
	projectPath := writeGeminiSession(t, sessionID, body)

	var a GeminiSessionAnalytics
	if err := UpdateGeminiAnalyticsFromDisk(projectPath, sessionID, &a); err != nil {
		t.Fatalf("UpdateGeminiAnalyticsFromDisk: %v", err)
	}

	checks := []struct {
		name string
		got  int
		want int
	}{
		{"InputTokens", a.InputTokens, 1000 + 7560},
		{"OutputTokens", a.OutputTokens, 10 + 26},
		{"CachedTokens", a.CachedTokens, 400 + 2914},
		{"ThoughtsTokens", a.ThoughtsTokens, 100 + 492},
		{"ToolTokens", a.ToolTokens, 5 + 0},
		{"ReportedTotalTokens", a.ReportedTotalTokens, 1115 + 8078},
		{"CurrentContextTokens", a.CurrentContextTokens, 7560},
		{"CurrentContextCachedTokens", a.CurrentContextCachedTokens, 2914},
		{"TotalTurns", a.TotalTurns, 2},
		{"TotalTokens()", a.TotalTokens(), 1115 + 8078},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if a.Model != "gemini-2.5-pro" {
		t.Errorf("Model = %q, want gemini-2.5-pro", a.Model)
	}
}

// TestUpdateGeminiAnalyticsFromDisk_MixedFormatsFallbackPerMessage covers a
// session spanning the Gemini CLI format change that introduced tokens.total.
// An older message must not disappear merely because a newer message reports
// its own total.
func TestUpdateGeminiAnalyticsFromDisk_MixedFormatsFallbackPerMessage(t *testing.T) {
	const sessionID = "cdef0123-0000-0000-0000-000000000000"
	body := `{
	  "sessionId": "` + sessionID + `",
	  "startTime": "2026-07-26T10:00:00.000Z",
	  "lastUpdated": "2026-07-26T10:30:00.000Z",
	  "messages": [
	    {"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":100,"output":10,"cached":40,"thoughts":5,"tool":1}},
	    {"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":200,"output":20,"cached":80,"thoughts":7,"tool":3,"total":230}}
	  ]
	}`
	projectPath := writeGeminiSession(t, sessionID, body)

	var a GeminiSessionAnalytics
	if err := UpdateGeminiAnalyticsFromDisk(projectPath, sessionID, &a); err != nil {
		t.Fatalf("UpdateGeminiAnalyticsFromDisk: %v", err)
	}

	const want = (100 + 10 + 5 + 1) + 230
	if got := a.TotalTokens(); got != want {
		t.Fatalf("TotalTokens() = %d, want %d (per-message fallback)", got, want)
	}
	if a.ReportedTotalTokens != want {
		t.Fatalf("ReportedTotalTokens = %d, want %d (complete normalized total)", a.ReportedTotalTokens, want)
	}
}

// TestUpdateGeminiAnalyticsFromDisk_ResetsAllCountersOnReparse: the function
// mutates a reused struct, so every new counter must be zeroed first or a
// re-parse would double-count.
func TestUpdateGeminiAnalyticsFromDisk_ResetsAllCountersOnReparse(t *testing.T) {
	const sessionID = "aabbccdd-0000-0000-0000-000000000000"
	body := `{
	  "sessionId": "` + sessionID + `",
	  "startTime": "2026-07-26T10:00:00.000Z",
	  "lastUpdated": "2026-07-26T10:30:00.000Z",
	  "messages": [
	    {"type":"gemini","model":"gemini-2.5-pro","tokens":{"input":100,"output":10,"cached":40,"thoughts":5,"tool":1,"total":116}}
	  ]
	}`
	projectPath := writeGeminiSession(t, sessionID, body)

	a := GeminiSessionAnalytics{
		InputTokens:                999,
		CachedTokens:               999,
		ThoughtsTokens:             999,
		ToolTokens:                 999,
		ReportedTotalTokens:        999,
		CurrentContextCachedTokens: 999,
	}
	if err := UpdateGeminiAnalyticsFromDisk(projectPath, sessionID, &a); err != nil {
		t.Fatalf("UpdateGeminiAnalyticsFromDisk: %v", err)
	}

	if a.CachedTokens != 40 || a.ThoughtsTokens != 5 || a.ToolTokens != 1 ||
		a.ReportedTotalTokens != 116 || a.CurrentContextCachedTokens != 40 {
		t.Errorf("stale counters survived re-parse: %+v", a)
	}
}
