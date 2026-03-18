package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get home directory")
	}

	// Set a known env var for testing
	t.Setenv("AGENTDECK_TEST_DIR", "/tmp/testdir")
	t.Setenv("AGENTDECK_RELATIVE_TEST_DIR", "tmp/relative/testdir")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"absolute path", "/var/log/test.log", "/var/log/test.log"},
		{"relative path", ".env", ".env"},
		{"tilde prefix", "~/.secrets", filepath.Join(home, ".secrets")},
		{"just tilde", "~", home},
		{"tilde in middle", "/path/~/.env", "/path/~/.env"},
		{"$HOME expansion", "$HOME/.claude.env", filepath.Join(home, ".claude.env")},
		{"${HOME} expansion", "${HOME}/.claude.env", filepath.Join(home, ".claude.env")},
		{"custom env var", "$AGENTDECK_TEST_DIR/.env", "/tmp/testdir/.env"},
		{"env var in middle", "/opt/$AGENTDECK_RELATIVE_TEST_DIR/file", "/opt/tmp/relative/testdir/file"},
		{"tilde with env var after", "~/$AGENTDECK_TEST_DIR/.env", filepath.Join(home, "/tmp/testdir/.env")},
		{"tilde with ${VAR} after", "~/${AGENTDECK_TEST_DIR}/.env", filepath.Join(home, "/tmp/testdir/.env")},
		{"undefined env var", "$UNDEFINED_VAR/.env", "/.env"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandPath(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get home directory")
	}

	workDir := "/projects/myapp"

	tests := []struct {
		name     string
		path     string
		workDir  string
		expected string
	}{
		{"absolute path", "/etc/env", workDir, "/etc/env"},
		{"home path", "~/.secrets", workDir, filepath.Join(home, ".secrets")},
		{"relative path", ".env", workDir, "/projects/myapp/.env"},
		{"relative subdir", "config/.env", workDir, "/projects/myapp/config/.env"},
		{"$HOME env var", "$HOME/.claude.env", workDir, filepath.Join(home, ".claude.env")},
		{"${HOME} env var", "${HOME}/.secrets", workDir, filepath.Join(home, ".secrets")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolvePath(tt.path, tt.workDir)
			if result != tt.expected {
				t.Errorf("resolvePath(%q, %q) = %q, want %q", tt.path, tt.workDir, result, tt.expected)
			}
		})
	}
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"/etc/env", true},
		{"~/env", true},
		{"./env", true},
		{"../env", true},
		{"~", true},
		{"eval $(direnv hook bash)", false},
		{"source ~/.bashrc", false},
		{".env", false}, // Treated as inline command, not file path
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isFilePath(tt.input)
			if result != tt.expected {
				t.Errorf("isFilePath(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBuildSourceCmd(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		ignoreMissing bool
		wantContains  []string
	}{
		{
			name:          "ignore missing",
			path:          "/path/.env",
			ignoreMissing: true,
			wantContains:  []string{`[ -f "/path/.env" ]`, `source "/path/.env"`},
		},
		{
			name:          "strict mode",
			path:          "/path/.env",
			ignoreMissing: false,
			wantContains:  []string{`source "/path/.env"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSourceCmd(tt.path, tt.ignoreMissing)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("buildSourceCmd(%q, %v) = %q, want to contain %q", tt.path, tt.ignoreMissing, result, want)
				}
			}
		})
	}
}

func TestGetToolInlineEnv(t *testing.T) {
	// Save and restore the original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name     string
		tool     string
		env      map[string]string
		expected string
	}{
		{
			name:     "nil tool def returns empty",
			tool:     "nonexistent",
			env:      nil,
			expected: "",
		},
		{
			name:     "empty env map returns empty",
			tool:     "testtool",
			env:      map[string]string{},
			expected: "",
		},
		{
			name:     "single var",
			tool:     "testtool",
			env:      map[string]string{"API_KEY": "secret123"},
			expected: "export API_KEY='secret123'",
		},
		{
			name: "multiple vars sorted alphabetically",
			tool: "testtool",
			env: map[string]string{
				"ZEBRA":  "last",
				"ALPHA":  "first",
				"MIDDLE": "mid",
			},
			expected: "export ALPHA='first' && export MIDDLE='mid' && export ZEBRA='last'",
		},
		{
			name:     "value with single quotes escaped",
			tool:     "testtool",
			env:      map[string]string{"MSG": "it's a test"},
			expected: "export MSG='it'\\''s a test'",
		},
		{
			name:     "value with dollar sign not expanded",
			tool:     "testtool",
			env:      map[string]string{"VAR": "$HOME/path"},
			expected: "export VAR='$HOME/path'",
		},
		{
			name:     "value with backticks not expanded",
			tool:     "testtool",
			env:      map[string]string{"CMD": "`whoami`"},
			expected: "export CMD='`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up config cache with test tool
			userConfigCacheMu.Lock()
			if tt.env != nil || tt.tool == "testtool" {
				userConfigCache = &UserConfig{
					Tools: map[string]ToolDef{
						"testtool": {Env: tt.env},
					},
					MCPs: make(map[string]MCPDef),
				}
			} else {
				userConfigCache = &UserConfig{
					Tools: make(map[string]ToolDef),
					MCPs:  make(map[string]MCPDef),
				}
			}
			userConfigCacheMu.Unlock()

			inst := &Instance{Tool: tt.tool}
			result := inst.getToolInlineEnv()
			if result != tt.expected {
				t.Errorf("getToolInlineEnv() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestThemeEnvExport(t *testing.T) {
	// Save and restore original config cache + COLORFGBG env var
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name         string
		theme        string
		envCOLORFGBG string // parent terminal value (empty = unset)
		wantContains string
	}{
		{
			name:         "dark theme without parent env",
			theme:        "dark",
			envCOLORFGBG: "",
			wantContains: "export COLORFGBG='15;0'",
		},
		{
			name:         "light theme without parent env",
			theme:        "light",
			envCOLORFGBG: "",
			wantContains: "export COLORFGBG='0;15'",
		},
		{
			name:         "dark theme with matching parent env",
			theme:        "dark",
			envCOLORFGBG: "7;0",
			wantContains: "export COLORFGBG='7;0'", // propagate parent's exact value
		},
		{
			name:         "light theme with matching parent env",
			theme:        "light",
			envCOLORFGBG: "0;15",
			wantContains: "export COLORFGBG='0;15'", // propagate parent's exact value
		},
		{
			name:         "dark theme with mismatched parent env (parent says light)",
			theme:        "dark",
			envCOLORFGBG: "0;15",
			wantContains: "export COLORFGBG='15;0'", // override with dark value
		},
		{
			name:         "light theme with mismatched parent env (parent says dark)",
			theme:        "light",
			envCOLORFGBG: "15;0",
			wantContains: "export COLORFGBG='0;15'", // override with light value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up theme config
			userConfigCacheMu.Lock()
			userConfigCache = &UserConfig{
				Theme: tt.theme,
				MCPs:  make(map[string]MCPDef),
			}
			userConfigCacheMu.Unlock()

			// Set or unset COLORFGBG
			if tt.envCOLORFGBG != "" {
				t.Setenv("COLORFGBG", tt.envCOLORFGBG)
			} else {
				t.Setenv("COLORFGBG", "")
				os.Unsetenv("COLORFGBG")
			}

			result := themeEnvExport()
			if result != tt.wantContains {
				t.Errorf("themeEnvExport() = %q, want %q", result, tt.wantContains)
			}
		})
	}
}

func TestThemeColorFGBG(t *testing.T) {
	// Save and restore original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name     string
		theme    string
		envVal   string
		expected string
	}{
		{"dark theme", "dark", "", "15;0"},
		{"light theme", "light", "", "0;15"},
		{"dark with matching parent", "dark", "7;0", "7;0"},
		{"light with matching parent", "light", "0;8", "0;8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userConfigCacheMu.Lock()
			userConfigCache = &UserConfig{
				Theme: tt.theme,
				MCPs:  make(map[string]MCPDef),
			}
			userConfigCacheMu.Unlock()

			if tt.envVal != "" {
				t.Setenv("COLORFGBG", tt.envVal)
			} else {
				t.Setenv("COLORFGBG", "")
				os.Unsetenv("COLORFGBG")
			}

			result := ThemeColorFGBG()
			if result != tt.expected {
				t.Errorf("ThemeColorFGBG() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBuildEnvSourceCommand_IncludesTheme(t *testing.T) {
	// Save and restore original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	// Ensure no parent COLORFGBG
	t.Setenv("COLORFGBG", "")
	os.Unsetenv("COLORFGBG")

	// Set up light theme config
	userConfigCacheMu.Lock()
	userConfigCache = &UserConfig{
		Theme: "light",
		MCPs:  make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()

	inst := &Instance{Tool: "codex", ProjectPath: "/tmp"}
	result := inst.buildEnvSourceCommand()

	if !strings.Contains(result, "COLORFGBG") {
		t.Errorf("buildEnvSourceCommand() = %q, should contain COLORFGBG", result)
	}
	if !strings.Contains(result, "0;15") {
		t.Errorf("buildEnvSourceCommand() = %q, should contain light theme COLORFGBG value '0;15'", result)
	}
}

func TestShellSettings_GetIgnoreMissingEnvFiles(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name     string
		settings ShellSettings
		expected bool
	}{
		{"nil pointer defaults to true", ShellSettings{}, true},
		{"explicit true", ShellSettings{IgnoreMissingEnvFiles: &trueBool}, true},
		{"explicit false", ShellSettings{IgnoreMissingEnvFiles: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.settings.GetIgnoreMissingEnvFiles()
			if result != tt.expected {
				t.Errorf("GetIgnoreMissingEnvFiles() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetConductorEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "env-order-test"
	env := map[string]string{
		"MY_API_KEY": "conductor-value",
		"DEBUG":      "true",
	}
	if err := SetupConductor(name, "default", true, true, "", "", "", env, ""); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	inst := &Instance{
		Title:       "conductor-" + name,
		Tool:        "claude",
		ProjectPath: tmpHome,
	}

	result := inst.getConductorEnv(true)
	if result == "" {
		t.Fatal("getConductorEnv returned empty string for conductor with env vars")
	}
	if !strings.Contains(result, "export DEBUG='true'") {
		t.Errorf("expected DEBUG export, got: %s", result)
	}
	if !strings.Contains(result, "export MY_API_KEY='conductor-value'") {
		t.Errorf("expected MY_API_KEY export, got: %s", result)
	}

	// Verify ordering: DEBUG before MY_API_KEY (sorted)
	debugIdx := strings.Index(result, "DEBUG")
	apiIdx := strings.Index(result, "MY_API_KEY")
	if debugIdx > apiIdx {
		t.Error("env vars should be sorted alphabetically")
	}
}

func TestGetConductorEnv_NonConductorSession(t *testing.T) {
	inst := &Instance{
		Title: "main",
		Tool:  "claude",
	}
	if result := inst.getConductorEnv(true); result != "" {
		t.Errorf("non-conductor session should return empty, got: %s", result)
	}
}

func TestIsValidEnvKey(t *testing.T) {
	valid := []string{"HOME", "MY_VAR", "_private", "A", "API_KEY_123"}
	for _, k := range valid {
		if !isValidEnvKey(k) {
			t.Errorf("expected %q to be valid", k)
		}
	}
	invalid := []string{"", "123BAD", "HAS SPACE", "key=val", "semi;colon", "a'b"}
	for _, k := range invalid {
		if isValidEnvKey(k) {
			t.Errorf("expected %q to be invalid", k)
		}
	}
}
