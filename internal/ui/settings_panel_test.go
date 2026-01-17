package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestSettingsPanel_InitialState(t *testing.T) {
	panel := NewSettingsPanel()
	if panel.IsVisible() {
		t.Error("SettingsPanel should not be visible initially")
	}
	if panel.cursor != 0 {
		t.Errorf("Initial cursor should be 0, got %d", panel.cursor)
	}
}

func TestSettingsPanel_Hide(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()
	panel.Hide()
	if panel.IsVisible() {
		t.Error("SettingsPanel should not be visible after Hide()")
	}
}

func TestSettingsPanel_LoadConfig(t *testing.T) {
	panel := NewSettingsPanel()
	config := &session.UserConfig{
		DefaultTool: "claude",
		Theme:       "light",
		GlobalSearch: session.GlobalSearchSettings{
			Tier: "instant",
		},
	}
	panel.LoadConfig(config)

	// selectedTool: 0=claude
	if panel.selectedTool != 0 {
		t.Errorf("Expected selectedTool 0 (claude), got %d", panel.selectedTool)
	}
	// selectedTheme: 1=light
	if panel.selectedTheme != 1 {
		t.Errorf("Expected selectedTheme 1 (light), got %d", panel.selectedTheme)
	}
	// searchTier: 1=instant
	if panel.searchTier != 1 {
		t.Errorf("Expected searchTier 1 (instant), got %d", panel.searchTier)
	}
}

func TestSettingsPanel_LoadConfig_DefaultTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		expected int
	}{
		{"claude", "claude", 0},
		{"gemini", "gemini", 1},
		{"opencode", "opencode", 2},
		{"codex", "codex", 3},
		{"empty", "", 4}, // None
		{"unknown", "custom", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			config := &session.UserConfig{DefaultTool: tt.tool}
			panel.LoadConfig(config)
			if panel.selectedTool != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, panel.selectedTool)
			}
		})
	}
}

func TestSettingsPanel_LoadConfig_SearchTier(t *testing.T) {
	tests := []struct {
		name     string
		tier     string
		expected int
	}{
		{"auto", "auto", 0},
		{"instant", "instant", 1},
		{"balanced", "balanced", 2},
		{"empty", "", 0},
		{"unknown", "fast", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			config := &session.UserConfig{
				GlobalSearch: session.GlobalSearchSettings{Tier: tt.tier},
			}
			panel.LoadConfig(config)
			if panel.searchTier != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, panel.searchTier)
			}
		})
	}
}

func TestSettingsPanel_GetConfig(t *testing.T) {
	panel := NewSettingsPanel()
	panel.selectedTool = 1 // gemini
	panel.selectedTheme = 1 // light
	panel.searchTier = 2 // balanced
	panel.globalSearchEnabled = true
	panel.recentDays = 30
	panel.logMaxSizeMB = 256
	panel.logMaxLines = 10

	config := panel.GetConfig()

	if config.DefaultTool != "gemini" {
		t.Errorf("Expected DefaultTool 'gemini', got %q", config.DefaultTool)
	}
	if config.Theme != "light" {
		t.Errorf("Expected Theme 'light', got %q", config.Theme)
	}
	if config.GlobalSearch.Tier != "balanced" {
		t.Errorf("Expected Tier 'balanced', got %q", config.GlobalSearch.Tier)
	}
	if !config.GlobalSearch.Enabled {
		t.Error("Expected Enabled to be true")
	}
	if config.GlobalSearch.RecentDays != 30 {
		t.Errorf("Expected RecentDays 30, got %d", config.GlobalSearch.RecentDays)
	}
}

func TestSettingsPanel_GetConfig_ToolMapping(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "claude"},
		{1, "gemini"},
		{2, "opencode"},
		{3, "codex"},
		{4, ""},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			panel := NewSettingsPanel()
			panel.selectedTool = tt.input
			config := panel.GetConfig()
			if config.DefaultTool != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, config.DefaultTool)
			}
		})
	}
}

func TestSettingsPanel_GetConfig_TierMapping(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "auto"},
		{1, "instant"},
		{2, "balanced"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			panel := NewSettingsPanel()
			panel.searchTier = tt.input
			config := panel.GetConfig()
			if config.GlobalSearch.Tier != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, config.GlobalSearch.Tier)
			}
		})
	}
}

func TestSettingsPanel_SetSize(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(100, 60)
	if panel.width != 100 || panel.height != 60 {
		t.Errorf("Expected size 100x60, got %dx%d", panel.width, panel.height)
	}
}

func TestSettingsPanel_Update_Navigation(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Initial cursor is 0
	// Move down
	_, _, _ = panel.Update(testKeyMsg("down"))
	if panel.cursor != 1 {
		t.Errorf("Cursor should be 1 after down key, got %d", panel.cursor)
	}

	// Move up
	_, _, _ = panel.Update(testKeyMsg("up"))
	if panel.cursor != 0 {
		t.Errorf("Cursor should be 0 after up key, got %d", panel.cursor)
	}

	// Move j (down)
	_, _, _ = panel.Update(testKeyMsg("j"))
	if panel.cursor != 1 {
		t.Errorf("Cursor should be 1 after j key, got %d", panel.cursor)
	}

	// Move k (up)
	_, _, _ = panel.Update(testKeyMsg("k"))
	if panel.cursor != 0 {
		t.Errorf("Cursor should be 0 after k key, got %d", panel.cursor)
	}
}

func TestSettingsPanel_Update_ToggleCheckbox(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Move to "Enable Global Search" (index for SettingGlobalSearchEnabled is 9)
	panel.cursor = 9
	initial := panel.globalSearchEnabled

	// Press space to toggle
	_, _, shouldSave := panel.Update(testKeyMsg(" "))
	if panel.globalSearchEnabled == initial {
		t.Error("globalSearchEnabled should have toggled")
	}
	if !shouldSave {
		t.Error("shouldSave should be true after toggle")
	}

	// Re-test toggle back with Space
	_, _, _ = panel.Update(testKeyMsg(" "))
	if panel.globalSearchEnabled != initial {
		t.Error("globalSearchEnabled should have toggled back")
	}
}

func TestSettingsPanel_Update_RadioSelection(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Move to Theme selection (cursor 0)
	panel.cursor = 0
	panel.selectedTheme = 0 // dark

	// Press right to cycle
	_, _, shouldSave := panel.Update(testKeyMsg("right"))
	if panel.selectedTheme != 1 {
		t.Errorf("selectedTheme should be 1 (light) after right key, got %d", panel.selectedTheme)
	}
	if !shouldSave {
		t.Error("shouldSave should be true after change")
	}

	// Press left to cycle back
	_, _, _ = panel.Update(testKeyMsg("left"))
	if panel.selectedTheme != 0 {
		t.Errorf("selectedTheme should be 0 (dark) after left key, got %d", panel.selectedTheme)
	}
}

func TestSettingsPanel_Update_NumberAdjustment(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Move to Recent Days (index for SettingRecentDays is 11)
	panel.cursor = 11
	panel.recentDays = 90
	initial := panel.recentDays

	// Press right to increase (increments by 10)
	_, _, shouldSave := panel.Update(testKeyMsg("right"))
	if panel.recentDays != initial+10 {
		t.Errorf("Expected %d, got %d", initial+10, panel.recentDays)
	}
	if !shouldSave {
		t.Error("shouldSave should be true after change")
	}

	// Press left to decrease
	_, _, _ = panel.Update(testKeyMsg("left"))
	if panel.recentDays != initial {
		t.Errorf("Expected %d, got %d", initial, panel.recentDays)
	}
}

func TestSettingsPanel_Update_Escape(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	_, _, _ = panel.Update(testKeyMsg("esc"))
	if panel.IsVisible() {
		t.Error("Panel should be hidden after esc")
	}
}

func TestSettingsPanel_Update_SKey(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// S key (uppercase) should hide panel
	_, _, _ = panel.Update(testKeyMsg("S"))
	if panel.IsVisible() {
		t.Error("Panel should be hidden after S key")
	}
}

func TestSettingsPanel_NeedsRestart(t *testing.T) {
	panel := NewSettingsPanel()
	
	if panel.NeedsRestart() {
		t.Error("Should not need restart initially")
	}
	
	// Changing searchTier should trigger needsRestart
	panel.cursor = 10 // SettingSearchTier
	panel.searchTier = 0
	_ = panel.adjustValue(1)
	
	if !panel.NeedsRestart() {
		t.Error("Should need restart after tier change")
	}
}

func TestSettingsPanel_View_NotVisible(t *testing.T) {
	panel := NewSettingsPanel()
	if panel.View() != "" {
		t.Error("View should be empty when not visible")
	}
}

func TestSettingsPanel_View_Visible(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(80, 40)
	panel.Show()

	view := panel.View()
	if view == "" {
		t.Error("View should not be empty when visible")
	}
	// View uses lipgloss styles which might wrap the text or add colors
	// Strip ANSI might be needed or just check for substrings
	cleanView := tmux.StripANSI(view)
	if !strings.Contains(strings.ToUpper(cleanView), "SETTINGS") {
		t.Errorf("View should contain 'SETTINGS' title, got: %q", cleanView)
	}
}

func TestSettingsPanel_View_HighlightsCursor(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(80, 40)
	panel.Show()

	// Initial cursor is 0
	if panel.cursor != 0 {
		t.Errorf("Initial cursor should be 0, got %d", panel.cursor)
	}

	// Capture view with cursor at 0
	view0 := panel.View()
	if view0 == "" {
		t.Error("View should not be empty")
	}

	// Move cursor
	_, _, _ = panel.Update(testKeyMsg("down"))
	if panel.cursor != 1 {
		t.Errorf("Cursor should be 1 after down key, got %d", panel.cursor)
	}

	// Verify view still works
	view1 := panel.View()
	if view1 == "" {
		t.Error("View should not be empty after cursor move")
	}
}

func TestSettingsPanel_LoadConfig_Theme(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"dark", "dark", 0},
		{"light", "light", 1},
		{"empty_defaults_to_dark", "", 0},
		{"invalid_defaults_to_dark", "blue", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			config := &session.UserConfig{Theme: tt.input}
			panel.LoadConfig(config)
			if panel.selectedTheme != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, panel.selectedTheme)
			}
		})
	}
}

func TestSettingsPanel_GetConfig_Theme(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected string
	}{
		{"dark", 0, "dark"},
		{"light", 1, "light"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			panel.selectedTheme = tt.input
			config := panel.GetConfig()
			if config.Theme != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, config.Theme)
			}
		})
	}
}

func TestSettingsPanelPreviewSettings(t *testing.T) {
	panel := NewSettingsPanel()

	// Initial values (default to true)
	if !panel.showOutput {
		t.Error("showOutput should be true initially")
	}
	if !panel.showAnalytics {
		t.Error("showAnalytics should be true initially")
	}
}

func TestSettingsPanel_PreviewSettings_Toggle(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Indices for Preview settings
	outputIdx := 12
	analyticsIdx := 13

	// Toggle Show Output
	panel.cursor = outputIdx
	panel.showOutput = true
	_, _, _ = panel.Update(testKeyMsg(" "))
	if panel.showOutput {
		t.Error("showOutput should be false after toggle")
	}

	// Toggle Show Analytics
	panel.cursor = analyticsIdx
	panel.showAnalytics = true
	_, _, _ = panel.Update(testKeyMsg(" "))
	if panel.showAnalytics {
		t.Error("showAnalytics should be false after toggle")
	}
}

func TestSettingsPanel_PreviewSettings_LoadConfig(t *testing.T) {
	panel := NewSettingsPanel()

	// Test loading with explicit values
	config := &session.UserConfig{
		Preview: session.PreviewSettings{
			ShowOutput:    boolPtr(true),
			ShowAnalytics: boolPtr(false),
		},
	}
	panel.LoadConfig(config)

	if !panel.showOutput {
		t.Error("showOutput should be true after loading config")
	}
	if panel.showAnalytics {
		t.Error("showAnalytics should be false after loading config")
	}

	// Test loading with nil ShowAnalytics (should default to true)
	config2 := &session.UserConfig{
		Preview: session.PreviewSettings{
			ShowOutput:    boolPtr(false),
			ShowAnalytics: nil,
		},
	}
	panel.LoadConfig(config2)

	if panel.showOutput {
		t.Error("showOutput should be false after loading config2")
	}
	if !panel.showAnalytics {
		t.Error("showAnalytics should default to true when nil")
	}
}

func TestSettingsPanel_PreviewSettings_GetConfig(t *testing.T) {
	panel := NewSettingsPanel()
	panel.showOutput = true
	panel.showAnalytics = false

	config := panel.GetConfig()

	if config.Preview.ShowOutput == nil || !*config.Preview.ShowOutput {
		t.Error("Preview.ShowOutput should be true")
	}
	if config.Preview.ShowAnalytics == nil {
		t.Error("Preview.ShowAnalytics should not be nil")
	} else if *config.Preview.ShowAnalytics {
		t.Error("Preview.ShowAnalytics should be false")
	}
}

func TestSettingsPanel_PreviewSettings_ViewContains(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(80, 50)
	panel.Show()

	view := panel.View()
	cleanView := tmux.StripANSI(view)

	expectedElements := []string{
		"PREVIEW",
		"Show Output",
		"Show Analytics",
	}

	for _, elem := range expectedElements {
		if !containsString(cleanView, elem) {
			t.Errorf("View() should contain %q", elem)
		}
	}
}

func boolPtr(b bool) *bool {
	return &b
}