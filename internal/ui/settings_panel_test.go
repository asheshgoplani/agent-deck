package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSettingsPanel_InitialState(t *testing.T) {
	panel := NewSettingsPanel()

	if panel.IsVisible() {
		t.Error("Panel should not be visible initially")
	}

	panel.Show()
	if !panel.IsVisible() {
		t.Error("Panel should be visible after Show()")
	}
}

func TestSettingsPanel_Hide(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	if !panel.IsVisible() {
		t.Error("Panel should be visible after Show()")
	}

	panel.Hide()

	if panel.IsVisible() {
		t.Error("Panel should not be visible after Hide()")
	}
}

func TestSettingsPanel_LoadConfig(t *testing.T) {
	panel := NewSettingsPanel()

	// Load a config with specific values
	config := &session.UserConfig{
		DefaultTool: "gemini",
		Claude: session.ClaudeSettings{
			DangerousMode: true,
			ConfigDir:     "~/.claude-work",
		},
		Updates: session.UpdateSettings{
			CheckEnabled: false,
			AutoUpdate:   true,
		},
		Logs: session.LogSettings{
			MaxSizeMB:     20,
			MaxLines:      5000,
			RemoveOrphans: false,
		},
		GlobalSearch: session.GlobalSearchSettings{
			Enabled:    true,
			Tier:       "instant",
			RecentDays: 60,
		},
	}
	panel.LoadConfig(config)

	// Check tool selection (gemini should be index 1)
	if panel.selectedTool != 1 {
		t.Errorf("selectedTool: got %d, want 1 (gemini)", panel.selectedTool)
	}
	if !panel.dangerousMode {
		t.Error("dangerousMode should be true")
	}
	if panel.claudeConfigDir != "~/.claude-work" {
		t.Errorf("claudeConfigDir: got %q, want %q", panel.claudeConfigDir, "~/.claude-work")
	}
	if panel.checkForUpdates {
		t.Error("checkForUpdates should be false")
	}
	if !panel.autoUpdate {
		t.Error("autoUpdate should be true")
	}
	if panel.logMaxSizeMB != 20 {
		t.Errorf("logMaxSizeMB: got %d, want 20", panel.logMaxSizeMB)
	}
	if panel.logMaxLines != 5000 {
		t.Errorf("logMaxLines: got %d, want 5000", panel.logMaxLines)
	}
	if panel.removeOrphans {
		t.Error("removeOrphans should be false")
	}
	if !panel.globalSearchEnabled {
		t.Error("globalSearchEnabled should be true")
	}
	// "instant" should be index 1
	if panel.searchTier != 1 {
		t.Errorf("searchTier: got %d, want 1 (instant)", panel.searchTier)
	}
	if panel.recentDays != 60 {
		t.Errorf("recentDays: got %d, want 60", panel.recentDays)
	}
}

func TestSettingsPanel_LoadConfig_DefaultTool(t *testing.T) {
	panel := NewSettingsPanel()

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
		{"unknown", "unknown-tool", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &session.UserConfig{
				DefaultTool: tt.tool,
			}
			panel.LoadConfig(config)
			if panel.selectedTool != tt.expected {
				t.Errorf("LoadConfig(%q): selectedTool = %d, want %d",
					tt.tool, panel.selectedTool, tt.expected)
			}
		})
	}
}

func TestSettingsPanel_LoadConfig_SearchTier(t *testing.T) {
	panel := NewSettingsPanel()

	tests := []struct {
		name     string
		tier     string
		expected int
	}{
		{"auto", "auto", 0},
		{"instant", "instant", 1},
		{"balanced", "balanced", 2},
		{"empty", "", 0},
		{"unknown", "unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &session.UserConfig{
				GlobalSearch: session.GlobalSearchSettings{
					Tier: tt.tier,
				},
			}
			panel.LoadConfig(config)
			if panel.searchTier != tt.expected {
				t.Errorf("LoadConfig tier %q: searchTier = %d, want %d",
					tt.tier, panel.searchTier, tt.expected)
			}
		})
	}
}

func TestSettingsPanel_GetConfig(t *testing.T) {
	panel := NewSettingsPanel()
	panel.selectedTool = 2 // opencode
	panel.dangerousMode = true
	panel.claudeConfigDir = "~/.claude-custom"
	panel.checkForUpdates = false
	panel.autoUpdate = true
	panel.logMaxSizeMB = 15
	panel.logMaxLines = 8000
	panel.removeOrphans = false
	panel.globalSearchEnabled = true
	panel.searchTier = 2 // balanced
	panel.recentDays = 45

	config := panel.GetConfig()

	if config.DefaultTool != "opencode" {
		t.Errorf("DefaultTool: got %q, want %q", config.DefaultTool, "opencode")
	}
	if !config.Claude.DangerousMode {
		t.Error("DangerousMode should be true")
	}
	if config.Claude.ConfigDir != "~/.claude-custom" {
		t.Errorf("ConfigDir: got %q, want %q", config.Claude.ConfigDir, "~/.claude-custom")
	}
	if config.Updates.CheckEnabled {
		t.Error("CheckEnabled should be false")
	}
	if !config.Updates.AutoUpdate {
		t.Error("AutoUpdate should be true")
	}
	if config.Logs.MaxSizeMB != 15 {
		t.Errorf("MaxSizeMB: got %d, want 15", config.Logs.MaxSizeMB)
	}
	if config.Logs.MaxLines != 8000 {
		t.Errorf("MaxLines: got %d, want 8000", config.Logs.MaxLines)
	}
	if config.Logs.RemoveOrphans {
		t.Error("RemoveOrphans should be false")
	}
	if !config.GlobalSearch.Enabled {
		t.Error("GlobalSearch.Enabled should be true")
	}
	if config.GlobalSearch.Tier != "balanced" {
		t.Errorf("Tier: got %q, want %q", config.GlobalSearch.Tier, "balanced")
	}
	if config.GlobalSearch.RecentDays != 45 {
		t.Errorf("RecentDays: got %d, want 45", config.GlobalSearch.RecentDays)
	}
}

func TestSettingsPanel_GetConfig_ToolMapping(t *testing.T) {
	panel := NewSettingsPanel()

	tests := []struct {
		name     string
		index    int
		expected string
	}{
		{"claude", 0, "claude"},
		{"gemini", 1, "gemini"},
		{"opencode", 2, "opencode"},
		{"codex", 3, "codex"},
		{"none", 4, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel.selectedTool = tt.index
			config := panel.GetConfig()
			if config.DefaultTool != tt.expected {
				t.Errorf("GetConfig for tool index %d: DefaultTool = %q, want %q",
					tt.index, config.DefaultTool, tt.expected)
			}
		})
	}
}

func TestSettingsPanel_GetConfig_TierMapping(t *testing.T) {
	panel := NewSettingsPanel()

	tests := []struct {
		name     string
		index    int
		expected string
	}{
		{"auto", 0, "auto"},
		{"instant", 1, "instant"},
		{"balanced", 2, "balanced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel.searchTier = tt.index
			config := panel.GetConfig()
			if config.GlobalSearch.Tier != tt.expected {
				t.Errorf("GetConfig for tier index %d: Tier = %q, want %q",
					tt.index, config.GlobalSearch.Tier, tt.expected)
			}
		})
	}
}

func TestSettingsPanel_SetSize(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(120, 60)

	if panel.width != 120 {
		t.Errorf("Width = %d, want 120", panel.width)
	}
	if panel.height != 60 {
		t.Errorf("Height = %d, want 60", panel.height)
	}
}

func TestSettingsPanel_Update_Navigation(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Initial cursor should be at 0
	if panel.cursor != 0 {
		t.Errorf("Initial cursor = %d, want 0", panel.cursor)
	}

	// Move down
	panel.Update(tea.KeyMsg{Type: tea.KeyDown})
	if panel.cursor != 1 {
		t.Errorf("After down: cursor = %d, want 1", panel.cursor)
	}

	// Move down with 'j'
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if panel.cursor != 2 {
		t.Errorf("After j: cursor = %d, want 2", panel.cursor)
	}

	// Move up
	panel.Update(tea.KeyMsg{Type: tea.KeyUp})
	if panel.cursor != 1 {
		t.Errorf("After up: cursor = %d, want 1", panel.cursor)
	}

	// Move up with 'k'
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if panel.cursor != 0 {
		t.Errorf("After k: cursor = %d, want 0", panel.cursor)
	}

	// Should not go below 0
	panel.Update(tea.KeyMsg{Type: tea.KeyUp})
	if panel.cursor != 0 {
		t.Errorf("After up at 0: cursor = %d, want 0", panel.cursor)
	}
}

func TestSettingsPanel_Update_ToggleCheckbox(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Navigate to dangerous_mode (index 2, after Theme and DefaultTool)
	panel.cursor = int(SettingDangerousMode)
	initialValue := panel.dangerousMode

	// Toggle with space
	_, _, changed := panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if panel.dangerousMode == initialValue {
		t.Error("dangerousMode should have toggled")
	}
	if !changed {
		t.Error("Update should return changed=true when value changes")
	}

	// Toggle back
	panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if panel.dangerousMode != initialValue {
		t.Error("dangerousMode should have toggled back")
	}
}

func TestSettingsPanel_Update_RadioSelection(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Navigate to DEFAULT TOOL (index 1, after Theme)
	panel.cursor = int(SettingDefaultTool)
	panel.selectedTool = 0 // claude

	// Move right
	panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	if panel.selectedTool != 1 {
		t.Errorf("After right: selectedTool = %d, want 1", panel.selectedTool)
	}

	// Move right with 'l'
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if panel.selectedTool != 2 {
		t.Errorf("After l: selectedTool = %d, want 2", panel.selectedTool)
	}

	// Move left
	panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if panel.selectedTool != 1 {
		t.Errorf("After left: selectedTool = %d, want 1", panel.selectedTool)
	}

	// Move left with 'h'
	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	if panel.selectedTool != 0 {
		t.Errorf("After h: selectedTool = %d, want 0", panel.selectedTool)
	}

	// Should not go below 0
	panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if panel.selectedTool != 0 {
		t.Errorf("After left at 0: selectedTool = %d, want 0", panel.selectedTool)
	}
}

func TestSettingsPanel_Update_NumberAdjustment(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Navigate to logMaxSizeMB (index 6, after Theme)
	panel.cursor = int(SettingLogMaxSize)
	panel.logMaxSizeMB = 10

	// Increase with right
	panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	if panel.logMaxSizeMB != 11 {
		t.Errorf("After right: logMaxSizeMB = %d, want 11", panel.logMaxSizeMB)
	}

	// Decrease with left
	panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if panel.logMaxSizeMB != 10 {
		t.Errorf("After left: logMaxSizeMB = %d, want 10", panel.logMaxSizeMB)
	}

	// Should not go below 1
	panel.logMaxSizeMB = 1
	panel.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if panel.logMaxSizeMB != 1 {
		t.Errorf("After left at 1: logMaxSizeMB = %d, want 1", panel.logMaxSizeMB)
	}
}

func TestSettingsPanel_Update_Escape(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	if !panel.IsVisible() {
		t.Error("Panel should be visible after Show()")
	}

	panel.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if panel.IsVisible() {
		t.Error("Panel should be hidden after Escape")
	}
}

func TestSettingsPanel_Update_SKey(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	if !panel.IsVisible() {
		t.Error("Panel should be visible after Show()")
	}

	panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})

	if panel.IsVisible() {
		t.Error("Panel should be hidden after pressing S")
	}
}

func TestSettingsPanel_NeedsRestart(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Initially should not need restart
	if panel.NeedsRestart() {
		t.Error("Should not need restart initially")
	}

	// Navigate to global search settings and change
	panel.cursor = int(SettingGlobalSearchEnabled)
	panel.Update(tea.KeyMsg{Type: tea.KeySpace})

	if !panel.NeedsRestart() {
		t.Error("Should need restart after changing global search setting")
	}
}

func TestSettingsPanel_View_NotVisible(t *testing.T) {
	panel := NewSettingsPanel()

	view := panel.View()
	if view != "" {
		t.Errorf("View() should return empty string when not visible, got %q", view)
	}
}

func TestSettingsPanel_View_Visible(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(80, 40)
	panel.Show()

	view := panel.View()
	if view == "" {
		t.Error("View() should return non-empty string when visible")
	}

	// Check that it contains expected elements
	expectedElements := []string{
		"Settings",
		"THEME",
		"Dark",
		"Light",
		"DEFAULT TOOL",
		"Claude",
		"Gemini",
		"CLAUDE",
		"Dangerous mode",
		"UPDATES",
		"LOGS",
		"GLOBAL SEARCH",
	}

	for _, elem := range expectedElements {
		if !containsString(view, elem) {
			t.Errorf("View() should contain %q", elem)
		}
	}
}

func TestSettingsPanel_View_HighlightsCursor(t *testing.T) {
	panel := NewSettingsPanel()
	panel.SetSize(80, 50) // Increased height for new settings
	panel.Show()

	// Just verify no crash with various cursor positions
	for i := 0; i < settingsCount; i++ {
		panel.cursor = i
		view := panel.View()
		if view == "" {
			t.Errorf("View() should return non-empty for cursor position %d", i)
		}
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSettingsPanel_ThemeToggle(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Default should be dark (0)
	if panel.selectedTheme != 0 {
		t.Errorf("Default theme should be 0 (dark), got %d", panel.selectedTheme)
	}

	// Navigate right to select light
	panel.cursor = int(SettingTheme)
	panel, _, shouldSave := panel.Update(tea.KeyMsg{Type: tea.KeyRight})

	if panel.selectedTheme != 1 {
		t.Errorf("Theme should be 1 (light) after right, got %d", panel.selectedTheme)
	}
	if !shouldSave {
		t.Error("Should trigger save on theme change")
	}
	if !panel.needsRestart {
		t.Error("Theme change should require restart")
	}
}

func TestSettingsPanel_LoadConfig_Theme(t *testing.T) {
	tests := []struct {
		name     string
		theme    string
		expected int
	}{
		{"dark", "dark", 0},
		{"light", "light", 1},
		{"empty defaults to dark", "", 0},
		{"invalid defaults to dark", "invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			config := &session.UserConfig{Theme: tt.theme}
			panel.LoadConfig(config)

			if panel.selectedTheme != tt.expected {
				t.Errorf("selectedTheme: got %d, want %d", panel.selectedTheme, tt.expected)
			}
		})
	}
}

func TestSettingsPanel_GetConfig_Theme(t *testing.T) {
	tests := []struct {
		name          string
		selectedTheme int
		expected      string
	}{
		{"dark", 0, "dark"},
		{"light", 1, "light"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewSettingsPanel()
			panel.selectedTheme = tt.selectedTheme
			config := panel.GetConfig()

			if config.Theme != tt.expected {
				t.Errorf("Theme: got %q, want %q", config.Theme, tt.expected)
			}
		})
	}
}

func TestSettingsPanelPreviewSettings(t *testing.T) {
	sp := NewSettingsPanel()

	// Find Show Output setting
	foundOutput := false
	foundAnalytics := false
	for i := 0; i < settingsCount; i++ {
		sp.cursor = i
		setting := SettingType(i)
		switch setting {
		case SettingShowOutput:
			foundOutput = true
		case SettingShowAnalytics:
			foundAnalytics = true
		}
	}

	if !foundOutput {
		t.Error("Show Output setting should exist (SettingShowOutput constant)")
	}
	if !foundAnalytics {
		t.Error("Show Analytics setting should exist (SettingShowAnalytics constant)")
	}
}

func TestSettingsPanel_PreviewSettings_Toggle(t *testing.T) {
	panel := NewSettingsPanel()
	panel.Show()

	// Test Show Output toggle
	panel.cursor = int(SettingShowOutput)
	initialOutput := panel.showOutput

	_, _, changed := panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !changed {
		t.Error("Show Output toggle should report changed=true")
	}
	if panel.showOutput == initialOutput {
		t.Error("Show Output should have toggled")
	}

	// Test Show Analytics toggle
	panel.cursor = int(SettingShowAnalytics)
	initialAnalytics := panel.showAnalytics

	_, _, changed = panel.Update(tea.KeyMsg{Type: tea.KeySpace})
	if !changed {
		t.Error("Show Analytics toggle should report changed=true")
	}
	if panel.showAnalytics == initialAnalytics {
		t.Error("Show Analytics should have toggled")
	}
}

func TestSettingsPanel_PreviewSettings_LoadConfig(t *testing.T) {
	panel := NewSettingsPanel()

	// Test loading with explicit values
	showOutputTrue := true
	showAnalyticsFalse := false
	config := &session.UserConfig{
		Preview: session.PreviewSettings{
			ShowOutput:    &showOutputTrue,
			ShowAnalytics: &showAnalyticsFalse,
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
	showOutputFalse := false
	config2 := &session.UserConfig{
		Preview: session.PreviewSettings{
			ShowOutput:    &showOutputFalse,
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

	expectedElements := []string{
		"PREVIEW",
		"Show Output",
		"Show Analytics",
	}

	for _, elem := range expectedElements {
		if !containsString(view, elem) {
			t.Errorf("View() should contain %q", elem)
		}
	}
}
