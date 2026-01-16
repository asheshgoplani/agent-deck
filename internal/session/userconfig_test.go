package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestUserConfig_ClaudeConfigDir(t *testing.T) {
	// Create temp config file
	tmpDir := t.TempDir()
	configContent := `
[claude]
config_dir = "~/.claude-work"

[tools.test]
command = "test"
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test parsing
	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.Claude.ConfigDir != "~/.claude-work" {
		t.Errorf("Claude.ConfigDir = %s, want ~/.claude-work", config.Claude.ConfigDir)
	}
}

func TestUserConfig_ClaudeConfigDirEmpty(t *testing.T) {
	// Test with no Claude section
	tmpDir := t.TempDir()
	configContent := `
[tools.test]
command = "test"
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.Claude.ConfigDir != "" {
		t.Errorf("Claude.ConfigDir = %s, want empty string", config.Claude.ConfigDir)
	}
}

func TestGlobalSearchConfig(t *testing.T) {
	// Create temp config with global search settings
	tmpDir := t.TempDir()
	configContent := `
[global_search]
enabled = true
tier = "auto"
memory_limit_mb = 150
recent_days = 60
index_rate_limit = 30
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test parsing
	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if !config.GlobalSearch.Enabled {
		t.Error("Expected GlobalSearch.Enabled to be true")
	}
	if config.GlobalSearch.Tier != "auto" {
		t.Errorf("Expected tier 'auto', got %q", config.GlobalSearch.Tier)
	}
	if config.GlobalSearch.MemoryLimitMB != 150 {
		t.Errorf("Expected MemoryLimitMB 150, got %d", config.GlobalSearch.MemoryLimitMB)
	}
	if config.GlobalSearch.RecentDays != 60 {
		t.Errorf("Expected RecentDays 60, got %d", config.GlobalSearch.RecentDays)
	}
	if config.GlobalSearch.IndexRateLimit != 30 {
		t.Errorf("Expected IndexRateLimit 30, got %d", config.GlobalSearch.IndexRateLimit)
	}
}

func TestGlobalSearchConfigDefaults(t *testing.T) {
	// Config without global_search section should parse with zero values
	// (defaults are applied by LoadUserConfig, not parsing)
	tmpDir := t.TempDir()
	configContent := `default_tool = "claude"`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	// When parsing directly without LoadUserConfig, values should be zero
	if config.GlobalSearch.Enabled {
		t.Error("GlobalSearch.Enabled should be false when not specified (zero value)")
	}
	if config.GlobalSearch.MemoryLimitMB != 0 {
		t.Errorf("Expected default MemoryLimitMB 0 (zero value), got %d", config.GlobalSearch.MemoryLimitMB)
	}
}

func TestGlobalSearchConfigDisabled(t *testing.T) {
	// Test explicitly disabling global search
	tmpDir := t.TempDir()
	configContent := `
[global_search]
enabled = false
tier = "disabled"
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.GlobalSearch.Enabled {
		t.Error("Expected GlobalSearch.Enabled to be false")
	}
	if config.GlobalSearch.Tier != "disabled" {
		t.Errorf("Expected tier 'disabled', got %q", config.GlobalSearch.Tier)
	}
}

func TestSaveUserConfig(t *testing.T) {
	// Setup: use temp directory
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Clear cache
	ClearUserConfigCache()

	// Create agent-deck directory
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	// Create config to save
	config := &UserConfig{
		DefaultTool: "claude",
		Claude: ClaudeSettings{
			DangerousMode: true,
			ConfigDir:     "~/.claude-work",
		},
		Logs: LogSettings{
			MaxSizeMB:     20,
			MaxLines:      5000,
			RemoveOrphans: true,
		},
	}

	// Save it
	err := SaveUserConfig(config)
	if err != nil {
		t.Fatalf("SaveUserConfig failed: %v", err)
	}

	// Clear cache and reload
	ClearUserConfigCache()
	loaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig failed: %v", err)
	}

	// Verify values
	if loaded.DefaultTool != "claude" {
		t.Errorf("DefaultTool: got %q, want %q", loaded.DefaultTool, "claude")
	}
	if !loaded.Claude.DangerousMode {
		t.Error("DangerousMode should be true")
	}
	if loaded.Claude.ConfigDir != "~/.claude-work" {
		t.Errorf("ConfigDir: got %q, want %q", loaded.Claude.ConfigDir, "~/.claude-work")
	}
	if loaded.Logs.MaxSizeMB != 20 {
		t.Errorf("MaxSizeMB: got %d, want %d", loaded.Logs.MaxSizeMB, 20)
	}
}

func TestGetTheme_Default(t *testing.T) {
	// Setup: use temp directory with no config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	theme := GetTheme()
	if theme != "dark" {
		t.Errorf("GetTheme: got %q, want %q", theme, "dark")
	}
}

func TestGetTheme_Light(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with light theme
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)
	config := &UserConfig{Theme: "light"}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	theme := GetTheme()
	if theme != "light" {
		t.Errorf("GetTheme: got %q, want %q", theme, "light")
	}
}

func TestWorktreeConfig(t *testing.T) {
	// Create temp config with worktree settings
	tmpDir := t.TempDir()
	configContent := `
[worktree]
default_location = "subdirectory"
auto_cleanup = false
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Test parsing
	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.Worktree.DefaultLocation != "subdirectory" {
		t.Errorf("Expected DefaultLocation 'subdirectory', got %q", config.Worktree.DefaultLocation)
	}
	if config.Worktree.AutoCleanup {
		t.Error("Expected AutoCleanup to be false")
	}
}

func TestWorktreeConfigDefaults(t *testing.T) {
	// Config without worktree section should parse with zero values
	// (defaults are applied by GetWorktreeSettings, not parsing)
	tmpDir := t.TempDir()
	configContent := `default_tool = "claude"`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	// When parsing directly without GetWorktreeSettings, values should be zero
	if config.Worktree.DefaultLocation != "" {
		t.Errorf("Expected empty DefaultLocation (zero value), got %q", config.Worktree.DefaultLocation)
	}
	if config.Worktree.AutoCleanup {
		t.Error("AutoCleanup should be false when not specified (zero value)")
	}
}

func TestGetWorktreeSettings(t *testing.T) {
	// Setup: use temp directory with no config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// With no config, should return defaults
	settings := GetWorktreeSettings()
	if settings.DefaultLocation != "sibling" {
		t.Errorf("GetWorktreeSettings DefaultLocation: got %q, want %q", settings.DefaultLocation, "sibling")
	}
	if !settings.AutoCleanup {
		t.Error("GetWorktreeSettings AutoCleanup: should default to true")
	}
}

func TestGetWorktreeSettings_FromConfig(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with custom worktree settings
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)
	config := &UserConfig{
		Worktree: WorktreeSettings{
			DefaultLocation: "subdirectory",
			AutoCleanup:     false,
		},
	}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	settings := GetWorktreeSettings()
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("GetWorktreeSettings DefaultLocation: got %q, want %q", settings.DefaultLocation, "subdirectory")
	}
	if settings.AutoCleanup {
		t.Error("GetWorktreeSettings AutoCleanup: should be false from config")
	}
}

// ============================================================================
// Preview Settings Tests
// ============================================================================

func TestPreviewSettings(t *testing.T) {
	// Create temp config
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	// Write config with preview settings
	content := `
[preview]
show_output = true
show_analytics = false
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.Preview.ShowOutput == nil || !*config.Preview.ShowOutput {
		t.Error("Expected Preview.ShowOutput to be true")
	}
	if config.Preview.ShowAnalytics == nil {
		t.Error("Expected Preview.ShowAnalytics to be set")
	} else if *config.Preview.ShowAnalytics {
		t.Error("Expected Preview.ShowAnalytics to be false")
	}
}

func TestPreviewSettingsDefaults(t *testing.T) {
	cfg := &UserConfig{}

	// Default: output ON, analytics ON (both default to true when not set)
	if !cfg.GetShowOutput() {
		t.Error("GetShowOutput should default to true")
	}
	if !cfg.GetShowAnalytics() {
		t.Error("GetShowAnalytics should default to true")
	}
}

func TestPreviewSettingsExplicitTrue(t *testing.T) {
	// Test when analytics is explicitly set to true
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	content := `
[preview]
show_output = false
show_analytics = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.GetShowOutput() {
		t.Error("GetShowOutput should be false")
	}
	if !config.GetShowAnalytics() {
		t.Error("GetShowAnalytics should be true when explicitly set")
	}
}

func TestPreviewSettingsNotSet(t *testing.T) {
	// Test when preview section exists but analytics is not set
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	content := `
[preview]
show_output = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if !config.GetShowOutput() {
		t.Error("GetShowOutput should be true")
	}
	// When not set, ShowAnalytics should default to true
	if !config.GetShowAnalytics() {
		t.Error("GetShowAnalytics should default to true when not set")
	}
}

func TestGetPreviewSettings(t *testing.T) {
	// Setup: use temp directory with no config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// With no config, should return defaults (both true)
	settings := GetPreviewSettings()
	if !settings.GetShowOutput() {
		t.Error("GetPreviewSettings ShowOutput: should default to true")
	}
	if !settings.GetShowAnalytics() {
		t.Error("GetPreviewSettings ShowAnalytics: should default to true")
	}
}

func TestGetPreviewSettings_FromConfig(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with custom preview settings
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	// Write config directly to test explicit false
	configPath := filepath.Join(agentDeckDir, "config.toml")
	content := `
[preview]
show_output = true
show_analytics = false
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetPreviewSettings()
	if !settings.GetShowOutput() {
		t.Error("GetPreviewSettings ShowOutput: should be true from config")
	}
	if settings.GetShowAnalytics() {
		t.Error("GetPreviewSettings ShowAnalytics: should be false from config")
	}
}
