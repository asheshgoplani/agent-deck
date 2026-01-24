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
	Theme string `toml:"theme"` // "dark", "light", or "auto"
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
