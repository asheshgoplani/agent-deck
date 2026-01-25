package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopSettingsGetThemeDefault(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Default theme should be "dark"
	theme, err := dsm.GetTheme()
	if err != nil {
		t.Fatalf("GetTheme failed: %v", err)
	}
	if theme != "dark" {
		t.Errorf("Expected default theme 'dark', got '%s'", theme)
	}
}

func TestDesktopSettingsSetTheme(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Set theme to light
	err := dsm.SetTheme("light")
	if err != nil {
		t.Fatalf("SetTheme('light') failed: %v", err)
	}

	// Verify it was saved
	theme, err := dsm.GetTheme()
	if err != nil {
		t.Fatalf("GetTheme failed: %v", err)
	}
	if theme != "light" {
		t.Errorf("Expected theme 'light', got '%s'", theme)
	}

	// Set theme to dark
	err = dsm.SetTheme("dark")
	if err != nil {
		t.Fatalf("SetTheme('dark') failed: %v", err)
	}

	theme, _ = dsm.GetTheme()
	if theme != "dark" {
		t.Errorf("Expected theme 'dark', got '%s'", theme)
	}

	// Set theme to auto
	err = dsm.SetTheme("auto")
	if err != nil {
		t.Fatalf("SetTheme('auto') failed: %v", err)
	}

	theme, _ = dsm.GetTheme()
	if theme != "auto" {
		t.Errorf("Expected theme 'auto', got '%s'", theme)
	}
}

func TestDesktopSettingsThemeToggle(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Simulate toggle behavior: dark -> light -> dark
	theme, _ := dsm.GetTheme()
	if theme != "dark" {
		t.Fatalf("Expected initial theme 'dark', got '%s'", theme)
	}

	// Toggle to light
	newTheme := "light"
	if theme == "light" {
		newTheme = "dark"
	}
	err := dsm.SetTheme(newTheme)
	if err != nil {
		t.Fatalf("SetTheme toggle failed: %v", err)
	}

	theme, _ = dsm.GetTheme()
	if theme != "light" {
		t.Errorf("Expected theme 'light' after toggle, got '%s'", theme)
	}

	// Toggle back to dark
	newTheme = "dark"
	if theme == "dark" {
		newTheme = "light"
	}
	err = dsm.SetTheme(newTheme)
	if err != nil {
		t.Fatalf("SetTheme toggle failed: %v", err)
	}

	theme, _ = dsm.GetTheme()
	if theme != "dark" {
		t.Errorf("Expected theme 'dark' after toggle, got '%s'", theme)
	}
}

func TestDesktopSettingsInvalidTheme(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Invalid theme should default to "dark"
	err := dsm.SetTheme("invalid-theme")
	if err != nil {
		t.Fatalf("SetTheme('invalid') failed: %v", err)
	}

	theme, _ := dsm.GetTheme()
	if theme != "dark" {
		t.Errorf("Expected invalid theme to default to 'dark', got '%s'", theme)
	}
}

func TestDesktopSettingsThemePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Create first manager, set theme
	dsm1 := &DesktopSettingsManager{configPath: configPath}
	err := dsm1.SetTheme("light")
	if err != nil {
		t.Fatalf("SetTheme failed: %v", err)
	}

	// Create new manager (simulating app restart), verify theme persisted
	dsm2 := &DesktopSettingsManager{configPath: configPath}
	theme, err := dsm2.GetTheme()
	if err != nil {
		t.Fatalf("GetTheme failed: %v", err)
	}
	if theme != "light" {
		t.Errorf("Expected persisted theme 'light', got '%s'", theme)
	}
}

func TestDesktopSettingsPreservesOtherSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Write initial config with other settings
	initialConfig := `
[project_discovery]
scan_paths = ["~/projects"]

[desktop]
theme = "dark"

[desktop.terminal]
soft_newline = "shift_enter"
font_size = 16
scroll_speed = 120
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0600)
	if err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Change theme
	err = dsm.SetTheme("light")
	if err != nil {
		t.Fatalf("SetTheme failed: %v", err)
	}

	// Read back config and verify other settings preserved
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "project_discovery") {
		t.Error("project_discovery section was lost")
	}
	if !strings.Contains(content, "scan_paths") {
		t.Error("scan_paths setting was lost")
	}
}

func TestDesktopSettingsThemeCaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	dsm := &DesktopSettingsManager{configPath: configPath}

	// Test case insensitivity
	testCases := []struct {
		input    string
		expected string
	}{
		{"DARK", "dark"},
		{"Light", "light"},
		{"AUTO", "auto"},
		{"  dark  ", "dark"},
		{"LIGHT", "light"},
	}

	for _, tc := range testCases {
		err := dsm.SetTheme(tc.input)
		if err != nil {
			t.Fatalf("SetTheme('%s') failed: %v", tc.input, err)
		}

		theme, _ := dsm.GetTheme()
		if theme != tc.expected {
			t.Errorf("SetTheme('%s'): expected '%s', got '%s'", tc.input, tc.expected, theme)
		}
	}
}
