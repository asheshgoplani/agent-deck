package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetClaudeConfigDir_Default(t *testing.T) {
	// Unset env var to test default/config behavior
	os.Unsetenv("CLAUDE_CONFIG_DIR")

	dir := GetClaudeConfigDir()
	home, _ := os.UserHomeDir()
	defaultPath := filepath.Join(home, ".claude")

	// If user config exists with claude.config_dir, that takes precedence
	// Otherwise, default to ~/.claude
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && userConfig.Claude.ConfigDir != "" {
		// Config exists, just verify we get a valid path
		if dir == "" {
			t.Error("GetClaudeConfigDir() returned empty string")
		}
	} else {
		// No config, should return default
		if dir != defaultPath {
			t.Errorf("GetClaudeConfigDir() = %s, want %s", dir, defaultPath)
		}
	}
}

func TestGetClaudeConfigDir_EnvOverride(t *testing.T) {
	os.Setenv("CLAUDE_CONFIG_DIR", "/custom/path")
	defer os.Unsetenv("CLAUDE_CONFIG_DIR")

	dir := GetClaudeConfigDir()
	if dir != "/custom/path" {
		t.Errorf("GetClaudeConfigDir() = %s, want /custom/path", dir)
	}
}

func TestGetClaudeSessionID_NotFound(t *testing.T) {
	id, err := GetClaudeSessionID("/nonexistent/path")
	if err == nil {
		t.Error("Expected error for nonexistent path")
	}
	if id != "" {
		t.Errorf("Expected empty ID, got %s", id)
	}
}

func TestGetMCPInfo_Empty(t *testing.T) {
	// Use isolated config dir to avoid picking up real global MCPs
	oldConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", "/nonexistent/config/dir")
	defer func() {
		if oldConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", oldConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	// Test with non-existent path - should return empty MCPInfo, no panic
	info := GetMCPInfo("/nonexistent/path/that/does/not/exist")

	if info == nil {
		t.Fatal("GetMCPInfo returned nil for non-existent path")
	}
	if info.HasAny() {
		t.Error("Expected empty MCPInfo for non-existent path")
	}
	if info.Total() != 0 {
		t.Errorf("Expected Total()=0, got %d", info.Total())
	}
}

func TestMCPInfo_HasAny(t *testing.T) {
	tests := []struct {
		name     string
		info     MCPInfo
		expected bool
	}{
		{"empty", MCPInfo{}, false},
		{"global only", MCPInfo{Global: []string{"server1"}}, true},
		{"project only", MCPInfo{Project: []string{"server1"}}, true},
		{"local only", MCPInfo{LocalMCPs: []LocalMCP{{Name: "server1", SourcePath: "/test"}}}, true},
		{"all", MCPInfo{Global: []string{"a"}, Project: []string{"b"}, LocalMCPs: []LocalMCP{{Name: "c", SourcePath: "/test"}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.HasAny(); got != tt.expected {
				t.Errorf("HasAny() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMCPInfo_Total(t *testing.T) {
	info := MCPInfo{
		Global:  []string{"a", "b"},
		Project: []string{"c"},
		LocalMCPs: []LocalMCP{
			{Name: "d", SourcePath: "/test"},
			{Name: "e", SourcePath: "/test"},
			{Name: "f", SourcePath: "/test"},
		},
	}
	if got := info.Total(); got != 6 {
		t.Errorf("Total() = %d, want 6", got)
	}
}

func TestGetLocalMCPState_NoMcpJson(t *testing.T) {
	tmpDir := t.TempDir()

	servers, err := GetLocalMCPState(tmpDir)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if len(servers) != 0 {
		t.Errorf("Expected empty list, got %d servers", len(servers))
	}
}

func TestGetLocalMCPState_DefaultMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json with two MCPs
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	servers, err := GetLocalMCPState(tmpDir)
	if err != nil {
		t.Fatalf("GetLocalMCPState failed: %v", err)
	}

	if len(servers) != 2 {
		t.Fatalf("Expected 2 servers, got %d", len(servers))
	}

	// Default mode: all enabled
	for _, s := range servers {
		if !s.Enabled {
			t.Errorf("Expected %s to be enabled in default mode", s.Name)
		}
		if s.Source != "local" {
			t.Errorf("Expected source 'local', got %s", s.Source)
		}
	}
}

func TestGetLocalMCPState_WhitelistMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}, "mcp-c": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create settings with whitelist (only mcp-a enabled)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"enabledMcpjsonServers": ["mcp-a"]}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	servers, err := GetLocalMCPState(tmpDir)
	if err != nil {
		t.Fatalf("GetLocalMCPState failed: %v", err)
	}

	// Check enabled states
	enabledCount := 0
	for _, s := range servers {
		if s.Name == "mcp-a" {
			if !s.Enabled {
				t.Error("mcp-a should be enabled (in whitelist)")
			}
			enabledCount++
		} else {
			if s.Enabled {
				t.Errorf("%s should be disabled (not in whitelist)", s.Name)
			}
		}
	}
	if enabledCount != 1 {
		t.Errorf("Expected 1 enabled MCP, found %d", enabledCount)
	}
}

func TestGetLocalMCPState_BlacklistMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}, "mcp-c": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create settings with blacklist (only mcp-b disabled)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"disabledMcpjsonServers": ["mcp-b"]}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	servers, err := GetLocalMCPState(tmpDir)
	if err != nil {
		t.Fatalf("GetLocalMCPState failed: %v", err)
	}

	// Check enabled states
	disabledCount := 0
	for _, s := range servers {
		if s.Name == "mcp-b" {
			if s.Enabled {
				t.Error("mcp-b should be disabled (in blacklist)")
			}
			disabledCount++
		} else {
			if !s.Enabled {
				t.Errorf("%s should be enabled (not in blacklist)", s.Name)
			}
		}
	}
	if disabledCount != 1 {
		t.Errorf("Expected 1 disabled MCP, found %d", disabledCount)
	}
}

func TestToggleLocalMCP_DefaultToBlacklist(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Toggle mcp-a off (should create blacklist)
	if err := ToggleLocalMCP(tmpDir, "mcp-a"); err != nil {
		t.Fatalf("ToggleLocalMCP failed: %v", err)
	}

	// Verify settings file was created with blacklist
	data, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("Failed to read settings: %v", err)
	}

	if string(data) == "" {
		t.Fatal("Settings file is empty")
	}

	// Check state
	servers, _ := GetLocalMCPState(tmpDir)
	for _, s := range servers {
		if s.Name == "mcp-a" {
			if s.Enabled {
				t.Error("mcp-a should be disabled after toggle")
			}
		} else if s.Name == "mcp-b" {
			if !s.Enabled {
				t.Error("mcp-b should still be enabled")
			}
		}
	}
}

func TestToggleLocalMCP_WhitelistMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create whitelist with mcp-a enabled
	if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"enabledMcpjsonServers": ["mcp-a"]}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Toggle mcp-b on (add to whitelist)
	if err := ToggleLocalMCP(tmpDir, "mcp-b"); err != nil {
		t.Fatalf("ToggleLocalMCP failed: %v", err)
	}

	// Check both are now enabled
	servers, _ := GetLocalMCPState(tmpDir)
	for _, s := range servers {
		if !s.Enabled {
			t.Errorf("%s should be enabled after toggle", s.Name)
		}
	}

	// Toggle mcp-a off (remove from whitelist)
	if err := ToggleLocalMCP(tmpDir, "mcp-a"); err != nil {
		t.Fatalf("ToggleLocalMCP failed: %v", err)
	}

	// Check mcp-a is now disabled
	servers, _ = GetLocalMCPState(tmpDir)
	for _, s := range servers {
		if s.Name == "mcp-a" && s.Enabled {
			t.Error("mcp-a should be disabled after toggle")
		}
		if s.Name == "mcp-b" && !s.Enabled {
			t.Error("mcp-b should still be enabled")
		}
	}
}

func TestToggleLocalMCP_BlacklistMode(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .mcp.json
	mcpJSON := `{"mcpServers": {"mcp-a": {}, "mcp-b": {}}}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), []byte(mcpJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Create blacklist with mcp-a disabled
	if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"disabledMcpjsonServers": ["mcp-a"]}`
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(settingsJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Toggle mcp-a on (remove from blacklist)
	if err := ToggleLocalMCP(tmpDir, "mcp-a"); err != nil {
		t.Fatalf("ToggleLocalMCP failed: %v", err)
	}

	// Check both are now enabled
	servers, _ := GetLocalMCPState(tmpDir)
	for _, s := range servers {
		if !s.Enabled {
			t.Errorf("%s should be enabled", s.Name)
		}
	}

	// Toggle mcp-b off (add to blacklist)
	if err := ToggleLocalMCP(tmpDir, "mcp-b"); err != nil {
		t.Fatalf("ToggleLocalMCP failed: %v", err)
	}

	// Check mcp-b is now disabled
	servers, _ = GetLocalMCPState(tmpDir)
	for _, s := range servers {
		if s.Name == "mcp-a" && !s.Enabled {
			t.Error("mcp-a should still be enabled")
		}
		if s.Name == "mcp-b" && s.Enabled {
			t.Error("mcp-b should be disabled after toggle")
		}
	}
}

func TestGetMCPMode(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		expected MCPMode
	}{
		{"empty", `{}`, MCPModeDefault},
		{"whitelist", `{"enabledMcpjsonServers": ["a"]}`, MCPModeWhitelist},
		{"blacklist", `{"disabledMcpjsonServers": ["a"]}`, MCPModeBlacklist},
		{"whitelist priority", `{"enabledMcpjsonServers": ["a"], "disabledMcpjsonServers": ["b"]}`, MCPModeWhitelist},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmpDir, ".claude", "settings.local.json"), []byte(tt.settings), 0644); err != nil {
				t.Fatal(err)
			}

			mode := GetMCPMode(tmpDir)
			if mode != tt.expected {
				t.Errorf("GetMCPMode() = %v, want %v", mode, tt.expected)
			}
		})
	}
}

func TestClearMCPCache_ClearsParentDirectories(t *testing.T) {
	// Setup: Create nested directory structure
	baseDir := t.TempDir()
	childDir := filepath.Join(baseDir, "child", "grandchild")
	if err := os.MkdirAll(childDir, 0755); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	// Populate cache for parent directory by calling GetMCPInfo
	// (This will cache the parent path)
	_ = GetMCPInfo(baseDir)

	// Now clear cache for child directory
	ClearMCPCache(childDir)

	// The parent cache should also be cleared
	// We verify by checking internal cache state
	mcpInfoCacheMu.RLock()
	_, parentCached := mcpInfoCache[baseDir]
	mcpInfoCacheMu.RUnlock()

	if parentCached {
		t.Error("parent directory cache should be cleared but wasn't")
	}
}

func TestFindActiveSessionIDExcluding(t *testing.T) {
	configDir := t.TempDir()
	projectPath := "/Users/test/my-project"
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	sessionA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	sessionB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	if err := os.WriteFile(filepath.Join(projectDir, sessionA+".jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, sessionB+".jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("no exclude returns most recent", func(t *testing.T) {
		got := findActiveSessionIDExcluding(configDir, projectPath, nil)
		if got != sessionB {
			t.Errorf("got %q, want %q (most recent)", got, sessionB)
		}
	})

	t.Run("excluding most recent returns older", func(t *testing.T) {
		exclude := map[string]bool{sessionB: true}
		got := findActiveSessionIDExcluding(configDir, projectPath, exclude)
		if got != sessionA {
			t.Errorf("got %q, want %q (after excluding %s)", got, sessionA, sessionB)
		}
	})

	t.Run("excluding all returns empty", func(t *testing.T) {
		exclude := map[string]bool{sessionA: true, sessionB: true}
		got := findActiveSessionIDExcluding(configDir, projectPath, exclude)
		if got != "" {
			t.Errorf("got %q, want empty (all excluded)", got)
		}
	})

	t.Run("skips agent files", func(t *testing.T) {
		agentFile := filepath.Join(projectDir, "agent-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.jsonl")
		if err := os.WriteFile(agentFile, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		exclude := map[string]bool{sessionB: true}
		got := findActiveSessionIDExcluding(configDir, projectPath, exclude)
		if got != sessionA {
			t.Errorf("got %q, want %q (agent file should be skipped)", got, sessionA)
		}
	})

	t.Run("nonexistent project returns empty", func(t *testing.T) {
		got := findActiveSessionIDExcluding(configDir, "/no/such/project", nil)
		if got != "" {
			t.Errorf("got %q, want empty for nonexistent project", got)
		}
	})
}

func TestFindActiveSessionIDExcluding_StaleFile(t *testing.T) {
	configDir := t.TempDir()
	projectPath := "/Users/test/stale-project"
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	sessionID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	filePath := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(filePath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filePath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	got := findActiveSessionIDExcluding(configDir, projectPath, nil)
	if got != "" {
		t.Errorf("got %q, want empty (file is stale >5min)", got)
	}
}

func TestConvertToClaudeDirName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path",
			input:    "/Users/test/project",
			expected: "-Users-test-project",
		},
		{
			name:     "path with space",
			input:    "/Users/test/My Project",
			expected: "-Users-test-My-Project",
		},
		{
			name:     "path with exclamation mark",
			input:    "/Users/test/!Contributions",
			expected: "-Users-test--Contributions",
		},
		{
			name:     "path with multiple special chars",
			input:    "/Users/test/Code cloud/!Dir",
			expected: "-Users-test-Code-cloud--Dir",
		},
		{
			name:     "real world example",
			input:    "/Users/master/Dropbox/LLM x AWST/project",
			expected: "-Users-master-Dropbox-LLM-x-AWST-project",
		},
		{
			name:     "path with dots",
			input:    "/Users/test/.hidden/file.txt",
			expected: "-Users-test--hidden-file-txt",
		},
		{
			name:     "path with parentheses",
			input:    "/Users/test/(backup)/data",
			expected: "-Users-test--backup--data",
		},
		{
			name:     "preserves existing hyphens",
			input:    "/Users/test/my-project-name",
			expected: "-Users-test-my-project-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertToClaudeDirName(tt.input)
			if got != tt.expected {
				t.Errorf("ConvertToClaudeDirName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
