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

func TestUserConfig_ProfileClaudeConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[claude]
config_dir = "~/.claude-global"

[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"
`
	configPath := filepath.Join(tmpDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if got := config.GetProfileClaudeConfigDir("work"); got == "" {
		t.Fatal("GetProfileClaudeConfigDir(work) returned empty string")
	}

	if got, want := config.Profiles["work"].Claude.ConfigDir, "~/.claude-work"; got != want {
		t.Errorf("Profiles[work].Claude.ConfigDir = %q, want %q", got, want)
	}
	if got, want := config.Profiles["personal"].Claude.ConfigDir, "~/.claude-personal"; got != want {
		t.Errorf("Profiles[personal].Claude.ConfigDir = %q, want %q", got, want)
	}
	if got, want := config.Claude.ConfigDir, "~/.claude-global"; got != want {
		t.Errorf("Claude.ConfigDir = %q, want %q", got, want)
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

func TestIsClaudeCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "plain claude", command: "claude", want: true},
		{name: "absolute path", command: "/opt/homebrew/bin/claude", want: true},
		{name: "with args", command: "claude --continue", want: true},
		{name: "env prefix", command: "ANTHROPIC_BASE_URL=https://example.com claude --continue", want: true},
		{name: "quoted token", command: "'claude' --continue", want: true},
		{name: "env only", command: "ANTHROPIC_BASE_URL=https://example.com", want: false},
		{name: "different tool", command: "codex --model gpt-5", want: false},
		{name: "empty", command: "   ", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isClaudeCommand(tc.command)
			if got != tc.want {
				t.Fatalf("isClaudeCommand(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestIsClaudeCompatible_CustomToolCommands(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tmpDir, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", agentDeckDir, err)
	}

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			"claude_path": {
				Command: "/opt/homebrew/bin/claude --resume",
			},
			"claude_env": {
				Command: "ANTHROPIC_BASE_URL=https://example.com claude --continue",
			},
			"other": {
				Command: "codex --model gpt-5",
			},
		},
	}

	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	if !IsClaudeCompatible("claude") {
		t.Fatal("built-in claude should be Claude-compatible")
	}
	if !IsClaudeCompatible("claude_path") {
		t.Fatal("custom tool with Claude path should be Claude-compatible")
	}
	if !IsClaudeCompatible("claude_env") {
		t.Fatal("custom tool with env-prefixed Claude command should be Claude-compatible")
	}
	if IsClaudeCompatible("other") {
		t.Fatal("non-Claude custom tool should not be Claude-compatible")
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
	dangerousModeBool := true
	config := &UserConfig{
		DefaultTool: "claude",
		Claude: ClaudeSettings{
			DangerousMode: &dangerousModeBool,
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
	if !loaded.Claude.GetDangerousMode() {
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

func TestResolveTheme_COLORFGBGOverridesOS(t *testing.T) {
	// Setup: explicit "system" theme so ResolveTheme falls through to
	// auto-detection where COLORFGBG should be checked.
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)
	config := &UserConfig{Theme: "system"}
	_ = SaveUserConfig(config)

	tests := []struct {
		name      string
		colorfgbg string
		want      string
	}{
		{"dark terminal (bg=0)", "15;0", "dark"},
		{"dark terminal (bg=1)", "15;1", "dark"},
		{"light terminal (bg=15)", "0;15", "light"},
		{"light terminal (bg=8)", "0;8", "light"},
		{"three-part dark", "12;7;0", "dark"},
		{"three-part light", "12;7;15", "light"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COLORFGBG", tt.colorfgbg)
			ClearUserConfigCache()

			got := ResolveTheme()
			if got != tt.want {
				t.Errorf("ResolveTheme() with COLORFGBG=%q: got %q, want %q", tt.colorfgbg, got, tt.want)
			}
		})
	}
}

func TestWorktreeConfig(t *testing.T) {
	// Create temp config with worktree settings
	tmpDir := t.TempDir()
	configContent := `
[worktree]
default_location = "subdirectory"
default_enabled = true
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
	if !config.Worktree.DefaultEnabled {
		t.Error("Expected DefaultEnabled to be true")
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

	settings := GetWorktreeSettings()
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("GetWorktreeSettings DefaultLocation: got %q, want %q", settings.DefaultLocation, "subdirectory")
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
			DefaultEnabled:  true,
			AutoCleanup:     false,
		},
	}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	settings := GetWorktreeSettings()
	if settings.DefaultLocation != "subdirectory" {
		t.Errorf("GetWorktreeSettings DefaultLocation: got %q, want %q", settings.DefaultLocation, "subdirectory")
	}
	if !settings.DefaultEnabled {
		t.Error("GetWorktreeSettings DefaultEnabled: should be true from config")
	}
	if settings.AutoCleanup {
		t.Error("GetWorktreeSettings AutoCleanup: should be false from config")
	}
}

func TestWorktreeSettings_Prefix_Default(t *testing.T) {
	settings := WorktreeSettings{}
	if got := settings.Prefix(); got != "feature/" {
		t.Errorf("Prefix() with nil BranchPrefix: got %q, want %q", got, "feature/")
	}
}

func TestWorktreeSettings_Prefix_Custom(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	settings := WorktreeSettings{BranchPrefix: strPtr("dev/")}
	if got := settings.Prefix(); got != "dev/" {
		t.Errorf("Prefix() with custom BranchPrefix: got %q, want %q", got, "dev/")
	}
}

func TestWorktreeSettings_Prefix_Empty(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	settings := WorktreeSettings{BranchPrefix: strPtr("")}
	if got := settings.Prefix(); got != "" {
		t.Errorf("Prefix() with empty BranchPrefix: got %q, want %q", got, "")
	}
}

func TestGetWorktreeSettings_BranchPrefix(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with custom branch_prefix
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)
	strPtr := func(s string) *string { return &s }
	config := &UserConfig{
		Worktree: WorktreeSettings{
			BranchPrefix: strPtr("custom/"),
		},
	}
	_ = SaveUserConfig(config)
	ClearUserConfigCache()

	settings := GetWorktreeSettings()
	if got := settings.Prefix(); got != "custom/" {
		t.Errorf("GetWorktreeSettings Prefix(): got %q, want %q", got, "custom/")
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

	// Default: output ON, analytics OFF, notes OFF
	if !cfg.GetShowOutput() {
		t.Error("GetShowOutput should default to true")
	}
	if cfg.GetShowAnalytics() {
		t.Error("GetShowAnalytics should default to false")
	}
	if cfg.GetShowNotes() {
		t.Error("GetShowNotes should default to false")
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
	// When not set, ShowAnalytics should default to false
	if config.GetShowAnalytics() {
		t.Error("GetShowAnalytics should default to false when not set")
	}
	if config.GetShowNotes() {
		t.Error("GetShowNotes should default to false when not set")
	}
}

func TestPreviewSettingsShowNotesExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	content := `
[preview]
show_notes = false
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if config.GetShowNotes() {
		t.Error("GetShowNotes should be false when explicitly disabled")
	}
}

func TestPreviewSettingsShowNotesExplicitTrue(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	content := `
[preview]
show_notes = true
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	_, err := toml.DecodeFile(configPath, &config)
	if err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if !config.GetShowNotes() {
		t.Error("GetShowNotes should be true when explicitly enabled")
	}
}

func TestGetPreviewSettings(t *testing.T) {
	// Setup: use temp directory with no config
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// With no config, should return defaults (output true, analytics false)
	settings := GetPreviewSettings()
	if !settings.GetShowOutput() {
		t.Error("GetPreviewSettings ShowOutput: should default to true")
	}
	if settings.GetShowAnalytics() {
		t.Error("GetPreviewSettings ShowAnalytics: should default to false")
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

func TestPreviewSettingsNotesOutputSplitDefaultsAndClamp(t *testing.T) {
	settings := PreviewSettings{}
	if got := settings.GetNotesOutputSplit(); got != 0.33 {
		t.Fatalf("GetNotesOutputSplit default = %v, want 0.33", got)
	}

	settings.NotesOutputSplit = 0.05
	if got := settings.GetNotesOutputSplit(); got != 0.1 {
		t.Fatalf("GetNotesOutputSplit low clamp = %v, want 0.1", got)
	}

	settings.NotesOutputSplit = 0.95
	if got := settings.GetNotesOutputSplit(); got != 0.9 {
		t.Fatalf("GetNotesOutputSplit high clamp = %v, want 0.9", got)
	}

	settings.NotesOutputSplit = 0.4
	if got := settings.GetNotesOutputSplit(); got != 0.4 {
		t.Fatalf("GetNotesOutputSplit configured = %v, want 0.4", got)
	}
}

func TestInstanceSettingsFollowCwdOnAttach(t *testing.T) {
	settings := InstanceSettings{}
	if settings.GetFollowCwdOnAttach() {
		t.Fatal("GetFollowCwdOnAttach should default to false")
	}

	enabled := true
	settings.FollowCwdOnAttach = &enabled
	if !settings.GetFollowCwdOnAttach() {
		t.Fatal("GetFollowCwdOnAttach should return explicit true")
	}
}

func TestUserConfigParseFollowCwdOnAttach(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	content := `
[instances]
follow_cwd_on_attach = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	var config UserConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	if !config.Instances.GetFollowCwdOnAttach() {
		t.Fatal("instances.follow_cwd_on_attach should parse as true")
	}
}

// ============================================================================
// Notifications Settings Tests
// ============================================================================

func TestNotificationsConfig_Defaults(t *testing.T) {
	// Test that default values are applied when section not present
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// With no config file, GetNotificationsSettings should return defaults
	settings := GetNotificationsSettings()
	if !settings.Enabled {
		t.Error("notifications should be enabled by default")
	}
	if settings.MaxShown != 6 {
		t.Errorf("max_shown should default to 6, got %d", settings.MaxShown)
	}
}

func TestNotificationsConfig_FromTOML(t *testing.T) {
	// Test parsing explicit TOML config
	tmpDir := t.TempDir()
	configContent := `
[notifications]
enabled = true
max_shown = 4
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

	if !config.Notifications.Enabled {
		t.Error("Expected Notifications.Enabled to be true")
	}
	if config.Notifications.MaxShown != 4 {
		t.Errorf("Expected MaxShown 4, got %d", config.Notifications.MaxShown)
	}
}

func TestGetNotificationsSettings(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	// Create config with custom notification settings
	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	content := `
[notifications]
enabled = true
max_shown = 8
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetNotificationsSettings()
	if !settings.Enabled {
		t.Error("GetNotificationsSettings Enabled: should be true from config")
	}
	if settings.MaxShown != 8 {
		t.Errorf("GetNotificationsSettings MaxShown: got %d, want 8", settings.MaxShown)
	}
}

func TestClaudeSettings_AllowDangerousMode_TOML(t *testing.T) {
	tmpDir := t.TempDir()
	configContent := `
[claude]
dangerous_mode = false
allow_dangerous_mode = true
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

	if config.Claude.GetDangerousMode() {
		t.Error("Expected dangerous_mode false")
	}
	if !config.Claude.AllowDangerousMode {
		t.Error("Expected allow_dangerous_mode true")
	}
}

func TestClaudeSettings_AllowDangerousMode_Default(t *testing.T) {
	var config UserConfig
	if config.Claude.AllowDangerousMode {
		t.Error("allow_dangerous_mode should default to false")
	}
}

func TestGetNotificationsSettings_PartialConfig(t *testing.T) {
	// Test that missing fields get defaults
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	// Config with only enabled set, max_shown should get default
	configPath := filepath.Join(agentDeckDir, "config.toml")
	content := `
[notifications]
enabled = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetNotificationsSettings()
	if !settings.Enabled {
		t.Error("GetNotificationsSettings Enabled: should be true")
	}
	if settings.MaxShown != 6 {
		t.Errorf("GetNotificationsSettings MaxShown: should default to 6, got %d", settings.MaxShown)
	}
}

func TestGetNotificationsSettings_ShowAll(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	// Test with show_all = true
	configPath := filepath.Join(agentDeckDir, "config.toml")
	content := `
[notifications]
enabled = true
max_shown = 6
show_all = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetNotificationsSettings()
	if !settings.ShowAll {
		t.Error("GetNotificationsSettings ShowAll: should be true from config")
	}

	// Test with show_all = false
	content = `
[notifications]
enabled = true
show_all = false
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings = GetNotificationsSettings()
	if settings.ShowAll {
		t.Error("GetNotificationsSettings ShowAll: should be false from config")
	}

	// Test default (show_all not specified)
	content = `
[notifications]
enabled = true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings = GetNotificationsSettings()
	if settings.ShowAll {
		t.Error("GetNotificationsSettings ShowAll: should default to false (backward compatible)")
	}
}

func TestGetTmuxSettings_InjectStatusLine_Default(t *testing.T) {
	// Default (no config) should return true
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	// Empty config file
	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if !settings.GetInjectStatusLine() {
		t.Error("GetInjectStatusLine should default to true when not set")
	}
}

func TestGetTmuxSettings_InjectStatusLine_False(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
inject_status_line = false
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if settings.GetInjectStatusLine() {
		t.Error("GetInjectStatusLine should be false when set to false")
	}
}

func TestGetTmuxSettings_InjectStatusLine_True(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
inject_status_line = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if !settings.GetInjectStatusLine() {
		t.Error("GetInjectStatusLine should be true when set to true")
	}
}

func TestGetTmuxSettings_LaunchInUserScope_Default(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if settings.GetLaunchInUserScope() {
		t.Error("GetLaunchInUserScope should default to false when not set")
	}
}

func TestGetTmuxSettings_LaunchInUserScope_True(t *testing.T) {
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)
	ClearUserConfigCache()

	agentDeckDir := filepath.Join(tempDir, ".agent-deck")
	_ = os.MkdirAll(agentDeckDir, 0700)

	configPath := filepath.Join(agentDeckDir, "config.toml")
	configContent := `
[tmux]
launch_in_user_scope = true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	ClearUserConfigCache()

	settings := GetTmuxSettings()
	if !settings.GetLaunchInUserScope() {
		t.Error("GetLaunchInUserScope should be true when set to true")
	}
}
