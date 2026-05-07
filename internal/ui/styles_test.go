package ui

import (
	"testing"
)

func TestColorsDefined(t *testing.T) {
	colors := []string{
		string(ColorBg),
		string(ColorSurface),
		string(ColorBorder),
		string(ColorText),
		string(ColorAccent),
	}
	for _, c := range colors {
		if c == "" {
			t.Error("Color should not be empty")
		}
	}
}

func TestStatusIndicator(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"running", "●"},
		{"waiting", "○"},
		{"idle", "◌"},
		{"error", "✕"},
		{"unknown", "◌"},
	}
	for _, tt := range tests {
		result := StatusIndicator(tt.status)
		if result == "" {
			t.Errorf("StatusIndicator(%s) returned empty", tt.status)
		}
	}
}

func TestToolIcon(t *testing.T) {
	tests := []struct {
		tool     string
		expected string
	}{
		{"claude", IconClaude},
		{"gemini", IconGemini},
		{"opencode", IconOpenCode},
		{"codex", IconCodex},
		{"cursor", "📝"},
		{"pi", IconPi},
		{"shell", IconShell},
		{"unknown", IconShell},
	}
	for _, tt := range tests {
		result := ToolIcon(tt.tool)
		if result != tt.expected {
			t.Errorf("ToolIcon(%s) = %s, want %s", tt.tool, result, tt.expected)
		}
	}
}

func TestMenuKey(t *testing.T) {
	result := MenuKey("q", "Quit")
	if result == "" {
		t.Error("MenuKey should not return empty string")
	}
}

func TestInitTheme_Dark(t *testing.T) {
	InitTheme("dark")
	if GetCurrentTheme() != ThemeDark {
		t.Errorf("Expected ThemeDark, got %v", GetCurrentTheme())
	}
	if ColorBg != darkColors.Bg {
		t.Errorf("ColorBg should be dark theme color")
	}
}

func TestInitTheme_Light(t *testing.T) {
	InitTheme("light")
	if GetCurrentTheme() != ThemeLight {
		t.Errorf("Expected ThemeLight, got %v", GetCurrentTheme())
	}
	if ColorBg != lightColors.Bg {
		t.Errorf("ColorBg should be light theme color")
	}
	// Reset to dark for other tests
	InitTheme("dark")
}

func TestInitTheme_InvalidFallsToDark(t *testing.T) {
	InitTheme("invalid")
	if GetCurrentTheme() != ThemeDark {
		t.Errorf("Invalid theme should fall back to dark")
	}
}

func TestInitTheme_StylesReinitialized(t *testing.T) {
	// Initialize with light theme
	InitTheme("light")
	// Check that a style uses light theme colors
	// BaseStyle should have ColorText foreground, which for light theme is #343b58
	if ColorText != lightColors.Text {
		t.Errorf("ColorText should be light theme value after InitTheme(light)")
	}

	// Switch to dark theme
	InitTheme("dark")
	// Check that colors switched
	if ColorText != darkColors.Text {
		t.Errorf("ColorText should be dark theme value after InitTheme(dark)")
	}
}

func TestToolStyleCache_ReinitializedOnThemeChange(t *testing.T) {
	// Initialize with dark theme
	InitTheme("dark")
	darkOrangeColor := ColorOrange

	// The tool style cache should use dark theme orange
	claudeStyle := GetToolStyle("claude")
	if claudeStyle.GetForeground() != darkOrangeColor {
		t.Errorf("Claude style should use dark theme orange color")
	}

	// Switch to light theme
	InitTheme("light")
	lightOrangeColor := ColorOrange

	// The tool style cache should now use light theme orange
	claudeStyle = GetToolStyle("claude")
	if claudeStyle.GetForeground() != lightOrangeColor {
		t.Errorf("Claude style should use light theme orange color after theme change")
	}

	// Reset to dark for other tests
	InitTheme("dark")
}
