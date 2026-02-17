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
