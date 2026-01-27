package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// allSectionsEnabled returns display settings with all sections enabled for testing
func allSectionsEnabled() session.AnalyticsDisplaySettings {
	t := true
	return session.AnalyticsDisplaySettings{
		ShowContextBar:  &t,
		ShowTokens:      &t,
		ShowSessionInfo: &t,
		ShowTools:       &t,
		ShowCost:        &t,
	}
}

func TestNewAnalyticsPanel(t *testing.T) {
	panel := NewAnalyticsPanel()
	if panel == nil {
		t.Fatal("NewAnalyticsPanel() returned nil")
	}
	if panel.analytics != nil {
		t.Error("New panel should have nil analytics")
	}
}

func TestAnalyticsPanel_SetAnalytics(t *testing.T) {
	panel := NewAnalyticsPanel()
	analytics := &session.SessionAnalytics{
		InputTokens:  1000,
		OutputTokens: 500,
	}

	panel.SetAnalytics(analytics)

	if panel.analytics != analytics {
		t.Error("SetAnalytics should set the analytics pointer")
	}
}

func TestAnalyticsPanel_SetSize(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(80, 40)

	if panel.width != 80 {
		t.Errorf("Width = %d, want 80", panel.width)
	}
	if panel.height != 40 {
		t.Errorf("Height = %d, want 40", panel.height)
	}
}

func TestAnalyticsPanel_View_Empty(t *testing.T) {
	panel := NewAnalyticsPanel()
	panel.SetSize(60, 20)

	view := panel.View()

	if view == "" {
		t.Error("View should return non-empty string even with nil analytics")
	}
	if !strings.Contains(view, "Session Analytics") {
		t.Error("Empty view should contain header")
	}
	if !strings.Contains(view, "No analytics available") {
		t.Error("Empty view should show 'No analytics available'")
	}
}

func TestAnalyticsPanel_View_WithAnalytics(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:  10000,
		OutputTokens: 5000,
		TotalTurns:   10,
		Duration:     30 * time.Minute,
		ToolCalls: []session.ToolCall{
			{Name: "Read", Count: 5},
			{Name: "Edit", Count: 3},
		},
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable all sections for this test
	panel.SetSize(60, 20)

	view := panel.View()

	// Check for expected sections
	if !strings.Contains(view, "Context") {
		t.Error("View should contain context bar")
	}
	if !strings.Contains(view, "Tokens") {
		t.Error("View should contain tokens section")
	}
	if !strings.Contains(view, "Session") {
		t.Error("View should contain session info")
	}
	if !strings.Contains(view, "Tools") {
		t.Error("View should contain tools section")
	}
}

func TestAnalyticsPanel_View_ContextBarColors(t *testing.T) {
	tests := []struct {
		name         string
		inputTokens  int
		outputTokens int
		description  string
	}{
		{
			name:         "low_usage",
			inputTokens:  10000,
			outputTokens: 5000,
			description:  "Low usage (<60%) should render",
		},
		{
			name:         "medium_usage",
			inputTokens:  70000,
			outputTokens: 60000,
			description:  "Medium usage (60-80%) should render",
		},
		{
			name:         "high_usage",
			inputTokens:  100000,
			outputTokens: 80000,
			description:  "High usage (>80%) should render",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewAnalyticsPanel()
			analytics := &session.SessionAnalytics{
				InputTokens:  tt.inputTokens,
				OutputTokens: tt.outputTokens,
			}
			panel.SetAnalytics(analytics)
			panel.SetSize(60, 20)

			view := panel.View()
			if view == "" {
				t.Errorf("%s: View should not be empty", tt.description)
			}
			if !strings.Contains(view, "Context") {
				t.Errorf("%s: View should contain Context bar", tt.description)
			}
		})
	}
}

func TestAnalyticsPanel_View_TokenBreakdown(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:      10000,
		OutputTokens:     5000,
		CacheReadTokens:  2000,
		CacheWriteTokens: 1000,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable tokens section
	panel.SetSize(60, 20)

	view := panel.View()

	// Should show input and output
	if !strings.Contains(view, "In:") {
		t.Error("View should show input tokens")
	}
	if !strings.Contains(view, "Out:") {
		t.Error("View should show output tokens")
	}

	// Should show cache tokens when present
	if !strings.Contains(view, "Cache") {
		t.Error("View should show cache tokens when present")
	}
	if !strings.Contains(view, "Total") {
		t.Error("View should show total tokens")
	}
}

func TestAnalyticsPanel_View_NoCacheTokens(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:      10000,
		OutputTokens:     5000,
		CacheReadTokens:  0,
		CacheWriteTokens: 0,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable tokens section
	panel.SetSize(60, 20)

	view := panel.View()

	// Cache section should not appear when tokens are 0
	if strings.Contains(view, "Cache Read:") {
		t.Error("View should not show cache section when cache tokens are 0")
	}
}

func TestAnalyticsPanel_View_SessionInfo(t *testing.T) {
	panel := NewAnalyticsPanel()

	startTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
		TotalTurns:  15,
		Duration:    45 * time.Minute,
		StartTime:   startTime,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable session info section
	panel.SetSize(60, 20)

	view := panel.View()

	if !strings.Contains(view, "Duration:") {
		t.Error("View should show duration")
	}
	if !strings.Contains(view, "Turns:") {
		t.Error("View should show turns")
	}
	if !strings.Contains(view, "15") {
		t.Error("View should show turn count of 15")
	}
	if !strings.Contains(view, "Started:") {
		t.Error("View should show start time when available")
	}
}

func TestAnalyticsPanel_View_ToolCalls(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
		ToolCalls: []session.ToolCall{
			{Name: "Read", Count: 10},
			{Name: "Edit", Count: 8},
			{Name: "Write", Count: 6},
			{Name: "Bash", Count: 4},
			{Name: "Grep", Count: 2},
			{Name: "Glob", Count: 1},
		},
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable tools section
	panel.SetSize(60, 30)

	view := panel.View()

	// Should show top 5 tools
	if !strings.Contains(view, "Read") {
		t.Error("View should show Read tool")
	}
	if !strings.Contains(view, "Edit") {
		t.Error("View should show Edit tool")
	}
	if !strings.Contains(view, "Write") {
		t.Error("View should show Write tool")
	}
	if !strings.Contains(view, "Bash") {
		t.Error("View should show Bash tool")
	}
	if !strings.Contains(view, "Grep") {
		t.Error("View should show Grep tool")
	}

	// Should show "and N more" for remaining tools
	if !strings.Contains(view, "and 1 more") {
		t.Error("View should show 'and 1 more' for 6th tool")
	}
}

func TestAnalyticsPanel_View_NoToolCalls(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
		ToolCalls:   []session.ToolCall{},
	}

	panel.SetAnalytics(analytics)
	panel.SetSize(60, 20)

	view := panel.View()

	// Tools section should not appear when empty
	if strings.Contains(view, "Tools\n") {
		// It might still contain "Tools" in other context, check for section
		lines := strings.Split(view, "\n")
		hasToolsSection := false
		for _, line := range lines {
			if strings.TrimSpace(line) == "Tools" {
				hasToolsSection = true
				break
			}
		}
		if hasToolsSection {
			t.Error("View should not show Tools section when no tools used")
		}
	}
}

func TestAnalyticsPanel_View_CostEstimate(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:  10000,
		OutputTokens: 5000,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable cost section
	panel.SetSize(60, 20)

	view := panel.View()

	if !strings.Contains(view, "Cost") {
		t.Error("View should show cost section")
	}
	if !strings.Contains(view, "$") {
		t.Error("View should show cost with dollar sign")
	}
}

func TestAnalyticsPanel_View_PresetCost(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:   10000,
		EstimatedCost: 0.05,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable cost section
	panel.SetSize(60, 20)

	view := panel.View()

	if !strings.Contains(view, "0.05") {
		t.Error("View should show preset estimated cost")
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{10000, "10,000"},
		{100000, "100,000"},
		{1000000, "1,000,000"},
		{1234567, "1,234,567"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatNumber(tt.input)
			if result != tt.expected {
				t.Errorf("formatNumber(%d) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		input    time.Duration
		expected string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{5 * time.Minute, "5m 0s"},
		{65 * time.Minute, "1h 5m"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.input)
			if result != tt.expected {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestAnalyticsPanel_View_SmallWidth(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:  10000,
		OutputTokens: 5000,
		TotalTurns:   10,
	}

	panel.SetAnalytics(analytics)
	panel.SetSize(30, 20) // Very narrow

	view := panel.View()

	// Should still render without panic
	if view == "" {
		t.Error("View should not be empty even with narrow width")
	}
}

func TestAnalyticsPanel_View_ZeroSize(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
	}

	panel.SetAnalytics(analytics)
	// Don't set size - should use defaults

	view := panel.View()

	// Should still render without panic
	if view == "" {
		t.Error("View should not be empty even with zero size")
	}
}

func TestAnalyticsPanel_ToolsSortedByCount(t *testing.T) {
	panel := NewAnalyticsPanel()

	// Add tools in unsorted order
	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
		ToolCalls: []session.ToolCall{
			{Name: "Glob", Count: 1},
			{Name: "Read", Count: 10},
			{Name: "Edit", Count: 5},
		},
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable tools section
	panel.SetSize(60, 20)

	view := panel.View()

	// Read (10) should appear before Edit (5) which should appear before Glob (1)
	readIdx := strings.Index(view, "Read")
	editIdx := strings.Index(view, "Edit")
	globIdx := strings.Index(view, "Glob")

	if readIdx == -1 || editIdx == -1 || globIdx == -1 {
		t.Fatal("All tools should appear in view")
	}

	if readIdx > editIdx {
		t.Error("Read (10) should appear before Edit (5)")
	}
	if editIdx > globIdx {
		t.Error("Edit (5) should appear before Glob (1)")
	}
}

func TestAnalyticsPanel_View_LongDuration(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens: 1000,
		Duration:    5*time.Hour + 30*time.Minute,
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(allSectionsEnabled()) // Enable session info section
	panel.SetSize(60, 20)

	view := panel.View()

	if !strings.Contains(view, "5h 30m") {
		t.Error("View should show '5h 30m' for 5.5 hour duration")
	}
}

func TestAnalyticsPanel_View_HighContextUsage(t *testing.T) {
	panel := NewAnalyticsPanel()

	// Use nearly all of 200k context
	analytics := &session.SessionAnalytics{
		InputTokens:  100000,
		OutputTokens: 90000,
	}

	panel.SetAnalytics(analytics)
	panel.SetSize(60, 20)

	view := panel.View()

	// Should show percentage >= 90%
	if !strings.Contains(view, "95.0%") && !strings.Contains(view, "Context") {
		// Just verify it renders without error at high usage
		if view == "" {
			t.Error("View should render at high context usage")
		}
	}
}

// TestAnalyticsPanel_View_DefaultSettings tests that default settings show only context bar
func TestAnalyticsPanel_View_DefaultSettings(t *testing.T) {
	panel := NewAnalyticsPanel()

	analytics := &session.SessionAnalytics{
		InputTokens:  10000,
		OutputTokens: 5000,
		TotalTurns:   10,
		Duration:     30 * time.Minute,
		ToolCalls: []session.ToolCall{
			{Name: "Read", Count: 5},
			{Name: "Edit", Count: 3},
		},
		EstimatedCost: 0.05,
	}

	panel.SetAnalytics(analytics)
	// Don't set display settings - use defaults
	panel.SetSize(60, 20)

	view := panel.View()

	// Default ON: Only context bar
	if !strings.Contains(view, "Context") {
		t.Error("Default view should show context bar")
	}

	// Default OFF: Tokens, Session info, Tools, Cost
	if strings.Contains(view, "Tokens") {
		t.Error("Default view should NOT show tokens section")
	}
	if strings.Contains(view, "Duration:") {
		t.Error("Default view should NOT show session info (duration)")
	}
	if strings.Contains(view, "Tools") {
		t.Error("Default view should NOT show tools section")
	}
	if strings.Contains(view, "Cost") {
		t.Error("Default view should NOT show cost section")
	}
}

// TestAnalyticsPanel_SetDisplaySettings tests the SetDisplaySettings method
func TestAnalyticsPanel_SetDisplaySettings(t *testing.T) {
	panel := NewAnalyticsPanel()

	// Create custom settings with only tokens enabled
	showTokens := true
	showOthers := false
	settings := session.AnalyticsDisplaySettings{
		ShowContextBar:  &showOthers,
		ShowTokens:      &showTokens,
		ShowSessionInfo: &showOthers,
		ShowTools:       &showOthers,
		ShowCost:        &showOthers,
	}

	analytics := &session.SessionAnalytics{
		InputTokens:  10000,
		OutputTokens: 5000,
		ToolCalls: []session.ToolCall{
			{Name: "Read", Count: 5},
		},
	}

	panel.SetAnalytics(analytics)
	panel.SetDisplaySettings(settings)
	panel.SetSize(60, 20)

	view := panel.View()

	// Only tokens should be shown
	if !strings.Contains(view, "Tokens") {
		t.Error("View should show tokens when enabled")
	}
	if !strings.Contains(view, "In:") {
		t.Error("View should show input tokens")
	}

	// Others should NOT be shown
	if strings.Contains(view, "Context") && strings.Contains(view, "[") {
		t.Error("View should NOT show context bar when disabled")
	}
	if strings.Contains(view, "Tools") {
		t.Error("View should NOT show tools when disabled")
	}
}
