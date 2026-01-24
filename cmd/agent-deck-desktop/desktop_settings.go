package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DesktopConfig represents the [desktop] section of config.toml
type DesktopConfig struct {
	Theme    string         `toml:"theme"`    // "dark", "light", or "auto"
	Terminal TerminalConfig `toml:"terminal"` // Terminal behavior settings
}

// TerminalConfig represents terminal input behavior settings
type TerminalConfig struct {
	// SoftNewline controls how to insert a newline without executing
	// Options: "shift_enter" (default), "alt_enter", "both", "disabled"
	SoftNewline string `toml:"soft_newline"`
}

// DesktopSettingsManager manages desktop-specific settings in config.toml
type DesktopSettingsManager struct {
	configPath string
}

// NewDesktopSettingsManager creates a new desktop settings manager
func NewDesktopSettingsManager() *DesktopSettingsManager {
	home, _ := os.UserHomeDir()
	return &DesktopSettingsManager{
		configPath: filepath.Join(home, ".agent-deck", "config.toml"),
	}
}

// fullConfig represents the entire config.toml structure we care about
type fullConfig struct {
	Desktop DesktopConfig `toml:"desktop"`
	// Other sections are preserved as raw TOML
}

// loadDesktopSettings loads the desktop section from config.toml
func (dsm *DesktopSettingsManager) loadDesktopSettings() (*DesktopConfig, error) {
	defaults := &DesktopConfig{
		Theme: "dark",
		Terminal: TerminalConfig{
			SoftNewline: "both", // Both Shift+Enter and Alt+Enter by default
		},
	}

	data, err := os.ReadFile(dsm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, nil
		}
		return nil, err
	}

	var config fullConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return defaults, nil // Return defaults on parse error
	}

	// Apply defaults for empty values
	if config.Desktop.Theme == "" {
		config.Desktop.Theme = "dark"
	}

	// Validate theme value
	switch config.Desktop.Theme {
	case "dark", "light", "auto":
		// Valid
	default:
		config.Desktop.Theme = "dark"
	}

	// Validate and apply defaults for terminal settings
	if config.Desktop.Terminal.SoftNewline == "" {
		config.Desktop.Terminal.SoftNewline = "both"
	}
	switch config.Desktop.Terminal.SoftNewline {
	case "shift_enter", "alt_enter", "both", "disabled":
		// Valid
	default:
		config.Desktop.Terminal.SoftNewline = "both"
	}

	return &config.Desktop, nil
}

// saveDesktopSettings saves the desktop config, preserving other sections
func (dsm *DesktopSettingsManager) saveDesktopSettings(desktop *DesktopConfig) error {
	// Read existing config to preserve other sections
	existingData, _ := os.ReadFile(dsm.configPath)

	// Parse existing config into a map to preserve unknown sections
	var existingConfig map[string]interface{}
	if len(existingData) > 0 {
		if err := toml.Unmarshal(existingData, &existingConfig); err != nil {
			existingConfig = make(map[string]interface{})
		}
	} else {
		existingConfig = make(map[string]interface{})
	}

	// Update the desktop section
	existingConfig["desktop"] = map[string]interface{}{
		"theme": desktop.Theme,
		"terminal": map[string]interface{}{
			"soft_newline": desktop.Terminal.SoftNewline,
		},
	}

	// Ensure directory exists
	dir := filepath.Dir(dsm.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// Encode to TOML
	var buf bytes.Buffer

	// Check if file existed and had content
	if len(existingData) == 0 {
		buf.WriteString("# Agent Deck Configuration\n\n")
	}

	if err := toml.NewEncoder(&buf).Encode(existingConfig); err != nil {
		return err
	}

	return os.WriteFile(dsm.configPath, buf.Bytes(), 0600)
}

// GetTheme returns the current desktop theme preference
func (dsm *DesktopSettingsManager) GetTheme() (string, error) {
	config, err := dsm.loadDesktopSettings()
	if err != nil {
		return "dark", err
	}
	return config.Theme, nil
}

// SetTheme sets the desktop theme preference
func (dsm *DesktopSettingsManager) SetTheme(theme string) error {
	// Validate theme
	theme = strings.ToLower(strings.TrimSpace(theme))
	switch theme {
	case "dark", "light", "auto":
		// Valid
	default:
		theme = "dark"
	}

	config, err := dsm.loadDesktopSettings()
	if err != nil {
		config = &DesktopConfig{}
	}

	config.Theme = theme
	return dsm.saveDesktopSettings(config)
}

// GetSoftNewline returns the soft newline key preference
// Returns: "shift_enter", "alt_enter", "both", or "disabled"
func (dsm *DesktopSettingsManager) GetSoftNewline() (string, error) {
	config, err := dsm.loadDesktopSettings()
	if err != nil {
		return "both", err
	}
	return config.Terminal.SoftNewline, nil
}

// SetSoftNewline sets the soft newline key preference
func (dsm *DesktopSettingsManager) SetSoftNewline(mode string) error {
	// Validate mode
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "shift_enter", "alt_enter", "both", "disabled":
		// Valid
	default:
		mode = "both"
	}

	config, err := dsm.loadDesktopSettings()
	if err != nil {
		config = &DesktopConfig{
			Theme: "dark",
			Terminal: TerminalConfig{
				SoftNewline: "both",
			},
		}
	}

	config.Terminal.SoftNewline = mode
	return dsm.saveDesktopSettings(config)
}

// GetTerminalConfig returns the full terminal configuration
func (dsm *DesktopSettingsManager) GetTerminalConfig() (*TerminalConfig, error) {
	config, err := dsm.loadDesktopSettings()
	if err != nil {
		return &TerminalConfig{SoftNewline: "both"}, err
	}
	return &config.Terminal, nil
}
